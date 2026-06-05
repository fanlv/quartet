import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import './SkillSettings.css';

interface SkillInfo {
  name: string;
  path: string;
  scope: string;
  agents: string[];
}

interface SkillFindResult {
  name: string;
  installs: string;
  url: string;
}

type ScopeTab = 'project' | 'global';

const API_BASE = '/api/v1/skills';

const DEFAULT_AGENTS = ['codex', 'claude-code', 'trae-cn','opencode'];

export function SkillSettings() {
  const { t } = useTranslation();
  const [scope, setScope] = useState<ScopeTab>('global');
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filterText, setFilterText] = useState('');

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

  // Check/Update state
  const [checkOutput, setCheckOutput] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [updating, setUpdating] = useState(false);

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

  const filteredSkills = useMemo(() => {
    if (!filterText.trim()) return skills;
    const keyword = filterText.trim().toLowerCase();
    return skills.filter(
      (s) =>
        s.name.toLowerCase().includes(keyword) ||
        s.path.toLowerCase().includes(keyword) ||
        s.agents?.some((a) => a.toLowerCase().includes(keyword))
    );
  }, [skills, filterText]);

  const loadSkills = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const resp = await fetch(`${API_BASE}/list?global=${scope === 'global'}`);
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('common.loadFailed'));
      }
      setSkills(data.skills || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [scope, t]);

  useEffect(() => {
    loadSkills();
  }, [loadSkills]);

  useEffect(() => {
    if (message) {
      const timer = setTimeout(() => setMessage(null), 4000);
      return () => clearTimeout(timer);
    }
  }, [message]);

  const handleRemove = async (name: string) => {
    if (!confirm(t('settings.skill.confirmUninstall', { name }))) return;
    try {
      const resp = await fetch(`${API_BASE}/remove`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, global: scope === 'global' }),
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('settings.skill.uninstallFailed'));
      }
      setMessage({ type: 'success', text: t('settings.skill.uninstalled', { name }) });
      loadSkills();
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

  const handleAdd = async () => {
    if (!addPackage.trim()) return;
    try {
      setAdding(true);
      const resp = await fetch(`${API_BASE}/add`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ package: addPackage.trim(), global: addGlobal, agents: addAgents }),
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || data.output || t('settings.skill.installFailed'));
      }
      setMessage({ type: 'success', text: t('settings.skill.installed', { name: addPackage.trim() }) });
      setShowAddModal(false);
      setAddPackage('');
      setSearchQuery('');
      setSearchResults([]);
      loadSkills();
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : t('settings.skill.installFailed') });
    } finally {
      setAdding(false);
    }
  };

  const handleInstallFromSearch = async (pkgName: string) => {
    setAddPackage(pkgName);
    setAdding(true);
    try {
      const resp = await fetch(`${API_BASE}/add`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ package: pkgName, global: addGlobal, agents: addAgents }),
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || data.output || t('settings.skill.installFailed'));
      }
      setMessage({ type: 'success', text: t('settings.skill.installed', { name: pkgName }) });
      loadSkills();
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : t('settings.skill.installFailed') });
    } finally {
      setAdding(false);
    }
  };

  const handleCheck = async () => {
    try {
      setChecking(true);
      setCheckOutput(null);
      const resp = await fetch(`${API_BASE}/check`);
      const data = await resp.json();
      setCheckOutput(data.output || t('settings.skill.checkComplete'));
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : t('settings.skill.checkFailed') });
    } finally {
      setChecking(false);
    }
  };

  const handleUpdate = async () => {
    try {
      setUpdating(true);
      const resp = await fetch(`${API_BASE}/update`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('settings.skill.updateFailed'));
      }
      setMessage({ type: 'success', text: t('settings.skill.allUpdated') });
      setCheckOutput(null);
      loadSkills();
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : t('settings.skill.updateFailed') });
    } finally {
      setUpdating(false);
    }
  };

  if (loading) {
    return (
      <div className="skill-settings">
        <div className="skill-loading">{t('common.loading')}</div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="skill-settings">
        <div className="skill-error">
          <p>{error}</p>
          <button className="settings-btn settings-btn-primary" onClick={loadSkills}>
            {t('common.retry')}
          </button>
        </div>
      </div>
    );
  }

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
              className="settings-btn settings-btn-secondary"
              onClick={handleCheck}
              disabled={checking}
            >
              {checking ? t('settings.skill.checking') : t('settings.skill.checkUpdate')}
            </button>
            <button
              className="settings-btn settings-btn-primary"
              onClick={() => {
                setShowAddModal(true);
                setAddGlobal(scope === 'global');
                setAddAgents([...DEFAULT_AGENTS]);
              }}
            >
              {t('settings.skill.installSkill')}
            </button>
          </div>
        </div>

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

        {checkOutput && (
          <div className="skill-check-output">
            <div className="skill-check-header">
              <span>{t('settings.skill.updateCheckResult')}</span>
              <div className="skill-check-actions">
                <button
                  className="settings-btn settings-btn-primary"
                  onClick={handleUpdate}
                  disabled={updating}
                  style={{ padding: '4px 12px', fontSize: '12px' }}
                >
                  {updating ? t('settings.skill.updating') : t('settings.skill.updateAll')}
                </button>
                <button
                  className="skill-check-close"
                  onClick={() => setCheckOutput(null)}
                >
                  x
                </button>
              </div>
            </div>
            <pre className="skill-check-content">{checkOutput}</pre>
          </div>
        )}

        <div className="skill-list">
          {filteredSkills.length === 0 ? (
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
              <div key={skill.name} className="skill-card">
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
                      onClick={() => handleRemove(skill.name)}
                    >
                      {t('common.uninstall')}
                    </button>
                  </div>
                </div>
                {skill.agents && skill.agents.length > 0 && (
                  <div className="skill-card-agents">
                    <span className="skill-agents-label">Agent:</span>
                    {skill.agents.map((agent) => (
                      <span key={agent} className="skill-agent-tag">{agent}</span>
                    ))}
                  </div>
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

              <div className="form-group">
                <label className="form-label">{t('settings.skill.installModal.installToAgents')}</label>
                <div className="skill-agents-select">
                  {DEFAULT_AGENTS.map((agent) => (
                    <label key={agent} className="skill-agent-checkbox">
                      <input
                        type="checkbox"
                        checked={addAgents.includes(agent)}
                        onChange={(e) => {
                          setAddAgents((prev) =>
                            e.target.checked
                              ? [...prev, agent]
                              : prev.filter((a) => a !== agent)
                          );
                        }}
                      />
                      <span>{agent}</span>
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
                  {searchResults.map((result, idx) => (
                    <div key={idx} className="skill-search-item">
                      <div className="skill-search-item-info">
                        <span className="skill-search-item-name">{result.name}</span>
                        <span className="skill-search-item-installs">{result.installs} {t('settings.skill.installModal.installs')}</span>
                      </div>
                      <button
                        className="settings-btn settings-btn-primary"
                        style={{ padding: '4px 12px', fontSize: '12px' }}
                        onClick={() => handleInstallFromSearch(result.name)}
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
