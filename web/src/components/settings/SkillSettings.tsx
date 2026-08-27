import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { invalidateSkillsCache, type SkillInfo } from '../../utils/skills';
import { useAuthPrincipal } from '../../auth';
import './SkillSettings.css';

interface SkillFindResult {
  name: string;
  installs: string;
  url: string;
}

interface ProjectToolsInstallResult {
  output: string;
}

interface WorkspaceOption {
  id: string;
  title?: string;
  workdir?: string;
}

type ScopeTab = 'project' | 'global';

const API_BASE = '/api/v1/skills';

// Agent targets offered by the install modal. `slug` is what `skills add -a`
// accepts; `label` mirrors the display name the CLI reports back in the skill
// list. `preselected` entries are the ones this project installs to by default.
const AGENT_TARGETS: { slug: string; label: string; preselected?: boolean }[] = [
  { slug: 'codex', label: 'Codex', preselected: true },
  { slug: 'claude-code', label: 'Claude Code', preselected: true },
  { slug: 'trae-cn', label: 'Trae CN', preselected: true },
  { slug: 'opencode', label: 'OpenCode', preselected: true },
  { slug: 'cursor', label: 'Cursor' },
  { slug: 'gemini-cli', label: 'Gemini CLI' },
  { slug: 'kimi-code-cli', label: 'Kimi Code CLI' },
  { slug: 'antigravity-cli', label: 'Antigravity CLI' },
  { slug: 'trae', label: 'Trae' },
];

const DEFAULT_AGENTS = AGENT_TARGETS.filter((a) => a.preselected).map((a) => a.slug);

const READY_POLL_DELAY_MS = 500;
// ~90s of polling: comfortably longer than the backend's own CLI timeout.
const READY_POLL_ATTEMPTS = 180;

// `skills add --all` installs to every agent the CLI knows about (50+), so the
// full tag list would dwarf the rest of the card. Show a readable slice and let
// the user expand on demand.
const AGENT_TAG_PREVIEW = 6;

/** Agent identifiers appear as slugs on install ("claude-code") and as display
 *  names in the listing ("Claude Code"); fold both to one comparable form so
 *  the filter box matches either spelling. */
function normalizeAgent(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]/g, '');
}

function workspaceLabel(ws: WorkspaceOption): string {
  const title = ws.title?.trim();
  if (title && ws.workdir) return `${title} — ${ws.workdir}`;
  return title || ws.workdir || ws.id;
}

/** Agent tag row for one skill, collapsed to a preview slice by default. */
function SkillAgentTags({ agents }: { agents: string[] }) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const hidden = agents.length - AGENT_TAG_PREVIEW;
  const shown = expanded ? agents : agents.slice(0, AGENT_TAG_PREVIEW);
  return (
    <div className="skill-card-agents">
      <span className="skill-agents-label">Agent:</span>
      {shown.map((agent) => (
        <span key={agent} className="skill-agent-tag">{agent}</span>
      ))}
      {hidden > 0 && (
        <button
          type="button"
          className="skill-agent-tag skill-agent-tag-toggle"
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? t('settings.skill.agentsCollapse') : t('settings.skill.agentsMore', { count: hidden })}
        </button>
      )}
    </div>
  );
}

export function SkillSettings({ workspaceId }: { workspaceId?: string }) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const canManage = !!principal?.permissions.includes('skills.manage');

  const [scope, setScope] = useState<ScopeTab>('global');
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filterText, setFilterText] = useState('');

  // Project scope belongs to one workspace directory: ACP agents run with the
  // workspace workdir as their cwd, so those are the only project skills they
  // can load. The backend refuses project-scope calls without a workspace.
  const [workspaces, setWorkspaces] = useState<WorkspaceOption[]>([]);
  const [projectWorkspaceId, setProjectWorkspaceId] = useState(workspaceId || '');

  // Add modal state
  const [showAddModal, setShowAddModal] = useState(false);
  const [addPackage, setAddPackage] = useState('');
  const [addGlobal, setAddGlobal] = useState(false);
  const [adding, setAdding] = useState(false);
  const [addAgents, setAddAgents] = useState<string[]>([...DEFAULT_AGENTS]);

  // Search state
  const [searchQuery, setSearchQuery] = useState('');
  const [searchResults, setSearchResults] = useState<SkillFindResult[]>([]);
  const [searching, setSearching] = useState(false);

  // Update state
  const [updateOutput, setUpdateOutput] = useState<string | null>(null);
  const [updating, setUpdating] = useState(false);

  // Current project quartet-cli + bundled skills installation state
  const [projectInstalling, setProjectInstalling] = useState(false);
  const [projectInstallOutput, setProjectInstallOutput] = useState<{
    type: 'success' | 'error';
    text: string;
  } | null>(null);

  // Operation result message
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);
  const modalBodyRef = useRef<HTMLDivElement>(null);
  const modalContentRef = useRef<HTMLDivElement>(null);

  // iPad/手机端：使用 visualViewport API 检测键盘弹出，动态调整弹窗高度
  useEffect(() => {
    if (!showAddModal) return;
    const vv = window.visualViewport;
    if (!vv) return;

    const handleResize = () => {
      const modalContent = modalContentRef.current;
      const overlay = modalContent?.parentElement;
      if (!modalContent || !overlay) return;
      const keyboardHeight = window.innerHeight - vv.height;
      if (keyboardHeight > 100) {
        const h = vv.height;
        modalContent.style.height = `${h - 20}px`;
        modalContent.style.maxHeight = `${h - 20}px`;
        overlay.style.alignItems = 'flex-start';
        overlay.style.paddingTop = `${vv.offsetTop + 10}px`;
      } else {
        modalContent.style.height = '';
        modalContent.style.maxHeight = '';
        overlay.style.alignItems = '';
        overlay.style.paddingTop = '';
      }
    };

    vv.addEventListener('resize', handleResize);
    vv.addEventListener('scroll', handleResize);
    return () => {
      vv.removeEventListener('resize', handleResize);
      vv.removeEventListener('scroll', handleResize);
    };
  }, [showAddModal]);

  // 输入框获得焦点时滚动到可视区域
  useEffect(() => {
    const container = modalBodyRef.current;
    if (!container) return;

    const handleFocus = (e: Event) => {
      const target = e.target as HTMLElement;
      if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA') {
        setTimeout(() => {
          target.scrollIntoView({ behavior: 'smooth', block: 'center' });
        }, 400);
      }
    };

    container.addEventListener('focusin', handleFocus);
    return () => container.removeEventListener('focusin', handleFocus);
  }, [showAddModal]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await fetch('/api/v1/workspace/list');
        const data = await resp.json();
        if (cancelled) return;
        const list: WorkspaceOption[] = Array.isArray(data?.workspaces) ? data.workspaces : [];
        setWorkspaces(list);
        setProjectWorkspaceId((prev) => {
          if (prev && list.some((ws) => ws.id === prev)) return prev;
          if (workspaceId && list.some((ws) => ws.id === workspaceId)) return workspaceId;
          return list[0]?.id ?? '';
        });
      } catch {
        // The global scope stays fully usable without the workspace list; the
        // project tab reports the missing selection on its own.
      }
    })();
    return () => { cancelled = true; };
  }, [workspaceId]);

  const filteredSkills = useMemo(() => {
    if (!filterText.trim()) return skills;
    const keyword = filterText.trim().toLowerCase();
    const agentKeyword = normalizeAgent(keyword);
    return skills.filter(
      (s) =>
        s.name.toLowerCase().includes(keyword) ||
        s.path.toLowerCase().includes(keyword) ||
        s.source?.toLowerCase().includes(keyword) ||
        (!!agentKeyword && s.agents?.some((a) => normalizeAgent(a).includes(agentKeyword)))
    );
  }, [skills, filterText]);

  // Sequence guard: fast scope/workspace switching would otherwise let a slow
  // earlier response overwrite the list belonging to the current selection.
  const loadSeqRef = useRef(0);

  const loadSkills = useCallback(async () => {
    const seq = ++loadSeqRef.current;
    const global = scope === 'global';
    if (!global && !projectWorkspaceId) {
      setSkills([]);
      setLoading(false);
      setError(workspaces.length === 0 ? t('settings.skill.noWorkspace') : t('settings.skill.selectWorkspace'));
      return;
    }

    setLoading(true);
    setError(null);
    const params = new URLSearchParams({ global: String(global) });
    if (!global) params.set('workspaceId', projectWorkspaceId);

    // The backend builds its listing by shelling out to the skills CLI. Until
    // that first attempt finishes it answers ready=false, which must not be
    // rendered as "nothing installed". The backend times its own CLI call out
    // and then reports the failure as ready, so the cap here is only a
    // belt-and-braces bound on the polling.
    for (let attempt = 0; attempt < READY_POLL_ATTEMPTS; attempt++) {
      try {
        const resp = await fetch(`${API_BASE}/list?${params.toString()}`, { cache: 'no-store' });
        const data = await resp.json();
        if (seq !== loadSeqRef.current) return;
        if (data.code !== 0) throw new Error(data.msg || t('common.loadFailed'));
        if (data.ready === false) {
          await new Promise((resolve) => window.setTimeout(resolve, READY_POLL_DELAY_MS));
          if (seq !== loadSeqRef.current) return;
          continue;
        }
        setSkills(Array.isArray(data.skills) ? data.skills : []);
        // A listing can succeed partially: stale cached entries plus the full
        // text of the failed refresh. Show both rather than hiding the error.
        setError(data.error || null);
      } catch (err) {
        if (seq !== loadSeqRef.current) return;
        setSkills([]);
        setError(err instanceof Error ? err.message : t('common.loadFailed'));
      }
      setLoading(false);
      return;
    }
    setLoading(false);
    setError(t('settings.skill.listNotReady'));
  }, [scope, projectWorkspaceId, workspaces.length, t]);

  useEffect(() => {
    loadSkills();
  }, [loadSkills]);

  useEffect(() => {
    if (message) {
      const timer = setTimeout(() => setMessage(null), 4000);
      return () => clearTimeout(timer);
    }
  }, [message]);

  /** Scope payload for the mutation endpoints, or null when project scope has
   *  no workspace selected. */
  const scopePayload = useCallback((global: boolean) => {
    if (global) return { global: true };
    if (!projectWorkspaceId) return null;
    return { global: false, workspaceId: projectWorkspaceId };
  }, [projectWorkspaceId]);

  /** Read a mutation reply, raising the backend's full error text on failure. */
  const readCommandResult = async (resp: Response, fallbackKey: string) => {
    const raw = await resp.text();
    let data: { code?: number; msg?: string; output?: string };
    try {
      data = JSON.parse(raw);
    } catch {
      throw new Error(raw || t(fallbackKey));
    }
    if (!resp.ok || data.code !== 0) {
      throw new Error([data.msg, data.output].filter(Boolean).join('\n') || raw || t(fallbackKey));
    }
    return data;
  };

  /** After any install / uninstall / update the shared list feeding the chat
   *  input's "/" completion is stale too. */
  const afterMutation = useCallback(() => {
    invalidateSkillsCache();
    loadSkills();
  }, [loadSkills]);

  const handleRemove = async (skill: SkillInfo) => {
    if (!confirm(t('settings.skill.confirmUninstall', { name: skill.name }))) return;
    // Uninstall in the scope the skill itself reports, not the visible tab.
    const payload = scopePayload(skill.scope === 'global');
    if (!payload) {
      setMessage({ type: 'error', text: t('settings.skill.selectWorkspace') });
      return;
    }
    try {
      const resp = await fetch(`${API_BASE}/remove`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: skill.name, ...payload }),
      });
      await readCommandResult(resp, 'settings.skill.uninstallFailed');
      setMessage({ type: 'success', text: t('settings.skill.uninstalled', { name: skill.name }) });
      afterMutation();
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : t('settings.skill.uninstallFailed') });
    }
  };

  const handleSearch = async () => {
    if (!searchQuery.trim()) return;
    try {
      setSearching(true);
      setSearchResults([]);
      const resp = await fetch(`${API_BASE}/find?query=${encodeURIComponent(searchQuery.trim())}`);
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('settings.skill.searchFailed'));
      }
      setSearchResults(data.results || []);
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : t('settings.skill.searchFailed') });
    } finally {
      setSearching(false);
    }
  };

  const installPackage = async (pkgName: string) => {
    const payload = scopePayload(addGlobal);
    if (!payload) {
      setMessage({ type: 'error', text: t('settings.skill.selectWorkspace') });
      return false;
    }
    try {
      setAdding(true);
      const resp = await fetch(`${API_BASE}/add`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ package: pkgName, agents: addAgents, ...payload }),
      });
      await readCommandResult(resp, 'settings.skill.installFailed');
      setMessage({ type: 'success', text: t('settings.skill.installed', { name: pkgName }) });
      // Reveal the scope the skill actually landed in; staying on the other tab
      // would look like the install did nothing.
      setScope(addGlobal ? 'global' : 'project');
      afterMutation();
      return true;
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : t('settings.skill.installFailed') });
      return false;
    } finally {
      setAdding(false);
    }
  };

  const handleAdd = async () => {
    const pkg = addPackage.trim();
    if (!pkg) return;
    if (await installPackage(pkg)) {
      setShowAddModal(false);
      setAddPackage('');
      setSearchQuery('');
      setSearchResults([]);
    }
  };

  // The skills CLI has no read-only update check — its `check` verb is an alias
  // of `update` — so this button is labelled and confirmed as what it is.
  const handleUpdateAll = async () => {
    const payload = scopePayload(scope === 'global');
    if (!payload) {
      setMessage({ type: 'error', text: t('settings.skill.selectWorkspace') });
      return;
    }
    const scopeName = scope === 'global' ? t('settings.skill.scopeGlobal') : t('settings.skill.scopeProject');
    if (!confirm(t('settings.skill.confirmUpdateAll', { scope: scopeName }))) return;
    try {
      setUpdating(true);
      setUpdateOutput(null);
      const resp = await fetch(`${API_BASE}/update`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      const data = await readCommandResult(resp, 'settings.skill.updateFailed');
      setUpdateOutput(data.output || t('settings.skill.allUpdated'));
      setMessage({ type: 'success', text: t('settings.skill.allUpdated') });
      afterMutation();
    } catch (err) {
      const text = err instanceof Error ? err.message : t('settings.skill.updateFailed');
      setUpdateOutput(text);
      setMessage({ type: 'error', text: t('settings.skill.updateFailed') });
    } finally {
      setUpdating(false);
    }
  };

  const handleInstallProjectTools = async () => {
    try {
      setProjectInstalling(true);
      setProjectInstallOutput(null);
      const resp = await fetch(`${API_BASE}/install-project-tools`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
      });
      const raw = await resp.text();
      let data: { code?: number; msg?: string; result?: ProjectToolsInstallResult };
      try {
        data = JSON.parse(raw);
      } catch {
        throw new Error(raw || t('settings.skill.projectInstallFailed'));
      }
      if (!resp.ok || data.code !== 0) {
        throw new Error(data.msg || raw || t('settings.skill.projectInstallFailed'));
      }

      const output = data.result?.output || t('settings.skill.projectInstallSuccess');
      setProjectInstallOutput({ type: 'success', text: output });
      setMessage({ type: 'success', text: t('settings.skill.projectInstallSuccess') });
      afterMutation();
    } catch (err) {
      const fullError = err instanceof Error ? err.message : t('settings.skill.projectInstallFailed');
      setProjectInstallOutput({ type: 'error', text: fullError });
      setMessage({ type: 'error', text: t('settings.skill.projectInstallFailed') });
    } finally {
      setProjectInstalling(false);
    }
  };

  const busy = projectInstalling || updating || adding;

  return (
    <div className="skill-settings">
      {message && !showAddModal && (
        <div className={`skill-message skill-message-${message.type}`}>
          {message.text}
        </div>
      )}

      <section className="settings-section">
        <div className="skill-header">
          <div className="skill-header-left">
            <h3 className="settings-section-title">{t('settings.skill.sectionTitle')}</h3>
            <div className="skill-scope-tabs">
              <button
                className={`skill-scope-tab ${scope === 'project' ? 'active' : ''}`}
                onClick={() => setScope('project')}
              >
                {t('settings.skill.scopeProject')}
              </button>
              <button
                className={`skill-scope-tab ${scope === 'global' ? 'active' : ''}`}
                onClick={() => setScope('global')}
              >
                {t('settings.skill.scopeGlobal')}
              </button>
            </div>
          </div>
          <div className="skill-header-actions">
            <button
              className="settings-btn skill-project-install-btn"
              onClick={handleInstallProjectTools}
              disabled={!canManage || busy}
              title={canManage ? t('settings.skill.projectInstallHint') : t('settings.skill.manageDenied')}
            >
              {projectInstalling
                ? t('settings.skill.projectInstalling')
                : t('settings.skill.installProjectTools')}
            </button>
            <button
              className="settings-btn settings-btn-secondary"
              onClick={handleUpdateAll}
              disabled={!canManage || busy}
              title={canManage ? t('settings.skill.updateAllHint') : t('settings.skill.manageDenied')}
            >
              {updating ? t('settings.skill.updating') : t('settings.skill.updateAll')}
            </button>
            <button
              className="settings-btn settings-btn-primary"
              onClick={() => {
                setShowAddModal(true);
                setAddGlobal(scope === 'global');
                setAddAgents([...DEFAULT_AGENTS]);
              }}
              disabled={!canManage || busy}
              title={canManage ? undefined : t('settings.skill.manageDenied')}
            >
              {t('settings.skill.installSkill')}
            </button>
          </div>
        </div>

        {scope === 'project' && (
          <div className="skill-workspace-picker">
            <label className="skill-workspace-label" htmlFor="skillProjectWorkspace">
              {t('settings.skill.workspaceLabel')}
            </label>
            <select
              id="skillProjectWorkspace"
              className="form-input skill-workspace-select"
              value={projectWorkspaceId}
              onChange={(e) => setProjectWorkspaceId(e.target.value)}
              disabled={workspaces.length === 0}
            >
              {workspaces.length === 0 && <option value="">{t('settings.skill.noWorkspace')}</option>}
              {workspaces.map((ws) => (
                <option key={ws.id} value={ws.id}>{workspaceLabel(ws)}</option>
              ))}
            </select>
            <span className="skill-workspace-hint">{t('settings.skill.workspaceHint')}</span>
          </div>
        )}

        {projectInstallOutput && (
          <div
            className={`skill-project-output skill-project-output-${projectInstallOutput.type}`}
            role={projectInstallOutput.type === 'error' ? 'alert' : 'status'}
          >
            <div className="skill-check-header">
              <span>{t('settings.skill.projectInstallResult')}</span>
              <button
                className="skill-check-close"
                onClick={() => setProjectInstallOutput(null)}
                aria-label={t('common.close')}
              >
                x
              </button>
            </div>
            <pre className="skill-check-content">{projectInstallOutput.text}</pre>
          </div>
        )}

        <div className="skill-filter">
          <input
            type="text"
            className="form-input skill-filter-input"
            value={filterText}
            onChange={(e) => setFilterText(e.target.value)}
            placeholder={t('settings.skill.searchPlaceholder')}
          />
          {filterText && (
            <button
              className="skill-filter-clear"
              onClick={() => setFilterText('')}
            >
              &times;
            </button>
          )}
        </div>

        {updateOutput && (
          <div className="skill-check-output">
            <div className="skill-check-header">
              <span>{t('settings.skill.updateResult')}</span>
              <div className="skill-check-actions">
                <button
                  className="skill-check-close"
                  onClick={() => setUpdateOutput(null)}
                  aria-label={t('common.close')}
                >
                  x
                </button>
              </div>
            </div>
            <pre className="skill-check-content">{updateOutput}</pre>
          </div>
        )}

        {error && (
          <div className="skill-message skill-message-error" role="alert">
            <pre className="skill-error-detail">{error}</pre>
            <button className="settings-btn settings-btn-secondary" onClick={loadSkills}>
              {t('common.retry')}
            </button>
          </div>
        )}

        <div className="skill-list">
          {loading ? (
            <div className="skill-loading">{t('common.loading')}</div>
          ) : filteredSkills.length === 0 ? (
            <div className="skill-empty">
              {filterText.trim() ? (
                <p>{t('settings.skill.noMatchingSkills')}</p>
              ) : (
                <>
                  <p>{t('settings.skill.noSkillsInstalled')}</p>
                  <p className="skill-empty-hint">
                    {t('settings.skill.installHint')}
                  </p>
                </>
              )}
            </div>
          ) : (
            filteredSkills.map((skill) => (
              <div key={`${skill.scope}:${skill.path || skill.name}`} className="skill-card">
                <div className="skill-card-header">
                  <div className="skill-card-info">
                    <span className="skill-card-icon">&#x1F4E6;</span>
                    <div className="skill-card-meta">
                      <span className="skill-card-name">{skill.name}</span>
                      <span className="skill-card-path" title={skill.path}>{skill.path}</span>
                    </div>
                  </div>
                  <div className="skill-card-actions">
                    <span className={`skill-scope-badge skill-scope-${skill.scope}`}>
                      {skill.scope === 'global' ? t('settings.skill.scopeGlobal') : t('settings.skill.scopeProject')}
                    </span>
                    <button
                      className="model-action-btn model-action-btn-danger"
                      onClick={() => handleRemove(skill)}
                      disabled={!canManage || busy}
                      title={canManage ? undefined : t('settings.skill.manageDenied')}
                    >
                      {t('common.uninstall')}
                    </button>
                  </div>
                </div>
                {skill.source && (
                  <div className="skill-card-source">
                    <span className="skill-agents-label">{t('settings.skill.source')}:</span>
                    {skill.sourceUrl ? (
                      <a href={skill.sourceUrl} target="_blank" rel="noreferrer noopener">{skill.source}</a>
                    ) : (
                      <span>{skill.source}</span>
                    )}
                  </div>
                )}
                {skill.agents && skill.agents.length > 0 && (
                  <SkillAgentTags agents={skill.agents} />
                )}
              </div>
            ))
          )}
        </div>
      </section>

      {/* Add Skill Modal */}
      {showAddModal && (
        <div className="modal-overlay" onClick={() => setShowAddModal(false)}>
          <div className="modal-content" ref={modalContentRef} onClick={(e) => e.stopPropagation()} style={{ width: '560px' }}>
            <div className="modal-header">
              <span className="modal-title">{t('settings.skill.installModal.title')}</span>
              <button className="modal-close" onClick={() => setShowAddModal(false)}>
                x
              </button>
            </div>
            <div className="modal-body" ref={modalBodyRef}>
              {message && (
                <div className={`skill-message skill-message-${message.type}`}>
                  {message.text}
                </div>
              )}
              {/* Direct install */}
              <div className="form-group">
                <label className="form-label">
                  {t('settings.skill.installModal.packageLabel')} <span className="required">*</span>
                </label>
                <div className="skill-add-input-row">
                  <input
                    type="text"
                    className="form-input"
                    value={addPackage}
                    onChange={(e) => setAddPackage(e.target.value)}
                    placeholder={t('settings.skill.installModal.packagePlaceholder')}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') handleAdd();
                    }}
                  />
                  <button
                    className="settings-btn settings-btn-primary"
                    onClick={handleAdd}
                    disabled={adding || !addPackage.trim()}
                  >
                    {adding ? t('common.installing') : t('common.install')}
                  </button>
                </div>
              </div>

              <div className="form-group form-group-checkbox">
                <input
                  type="checkbox"
                  id="addGlobal"
                  checked={addGlobal}
                  onChange={(e) => setAddGlobal(e.target.checked)}
                />
                <label htmlFor="addGlobal">{t('settings.skill.installModal.installGlobal')}</label>
              </div>

              {!addGlobal && (
                <div className="form-group">
                  <label className="form-label" htmlFor="addWorkspace">
                    {t('settings.skill.workspaceLabel')} <span className="required">*</span>
                  </label>
                  <select
                    id="addWorkspace"
                    className="form-input"
                    value={projectWorkspaceId}
                    onChange={(e) => setProjectWorkspaceId(e.target.value)}
                    disabled={workspaces.length === 0}
                  >
                    {workspaces.length === 0 && <option value="">{t('settings.skill.noWorkspace')}</option>}
                    {workspaces.map((ws) => (
                      <option key={ws.id} value={ws.id}>{workspaceLabel(ws)}</option>
                    ))}
                  </select>
                  <p className="skill-workspace-hint">{t('settings.skill.workspaceHint')}</p>
                </div>
              )}

              <div className="form-group">
                <label className="form-label">{t('settings.skill.installModal.installToAgents')}</label>
                <div className="skill-agents-select">
                  {AGENT_TARGETS.map((agent) => (
                    <label key={agent.slug} className="skill-agent-checkbox">
                      <input
                        type="checkbox"
                        checked={addAgents.includes(agent.slug)}
                        onChange={(e) => {
                          setAddAgents((prev) =>
                            e.target.checked
                              ? [...prev, agent.slug]
                              : prev.filter((a) => a !== agent.slug)
                          );
                        }}
                      />
                      <span>{agent.label}</span>
                    </label>
                  ))}
                </div>
              </div>

              {/* Search */}
              <div className="skill-search-divider">
                <span>{t('settings.skill.installModal.orSearch')}</span>
              </div>

              <div className="form-group">
                <div className="skill-add-input-row">
                  <input
                    type="text"
                    className="form-input"
                    value={searchQuery}
                    onChange={(e) => setSearchQuery(e.target.value)}
                    placeholder={t('settings.skill.installModal.searchPlaceholder')}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') handleSearch();
                    }}
                  />
                  <button
                    className="settings-btn settings-btn-secondary"
                    onClick={handleSearch}
                    disabled={searching || !searchQuery.trim()}
                  >
                    {searching ? t('common.searching') : t('common.search')}
                  </button>
                </div>
              </div>

              {/* Search results */}
              {searchResults.length > 0 && (
                <div className="skill-search-results">
                  {searchResults.map((result) => (
                    <div key={result.name} className="skill-search-item">
                      <div className="skill-search-item-info">
                        {result.url ? (
                          <a
                            className="skill-search-item-name"
                            href={result.url}
                            target="_blank"
                            rel="noreferrer noopener"
                          >
                            {result.name}
                          </a>
                        ) : (
                          <span className="skill-search-item-name">{result.name}</span>
                        )}
                        <span className="skill-search-item-installs">{result.installs} {t('settings.skill.installModal.installs')}</span>
                      </div>
                      <button
                        className="settings-btn settings-btn-primary"
                        style={{ padding: '4px 12px', fontSize: '12px' }}
                        onClick={() => installPackage(result.name)}
                        disabled={adding}
                      >
                        {t('common.install')}
                      </button>
                    </div>
                  ))}
                </div>
              )}
              {searching && (
                <div className="skill-search-loading">{t('common.searching')}</div>
              )}
            </div>
            <div className="modal-footer">
              <button
                className="settings-btn settings-btn-secondary"
                onClick={() => {
                  setShowAddModal(false);
                  setSearchQuery('');
                  setSearchResults([]);
                  setAddPackage('');
                }}
              >
                {t('common.close')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
