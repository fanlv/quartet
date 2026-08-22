import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { DirPicker } from '../DirPicker';
import {
  DEFAULT_WORKSPACE_ID,
  isDefaultWorkspace,
  loadWorkspacePrefs,
  registerWorkspacePrefs,
  registerWorkspaceColors,
  saveWorkspacePrefs,
  workspaceColor,
  type WorkspacePrefs,
} from '../../utils/workspace';
import './WorkspacesSettings.css';

interface WorkspaceItem {
  id: string;
  version: number;
  title: string;
  description: string;
  workdir: string;
  defaultAgent?: string;
  defaultModel?: string;
  color?: string;
  favorite: boolean;
  sortOrder: number;
}

interface AgentInfo {
  agent_id: string;
  type: string;
  display_name: string;
  available: boolean;
  models?: { availableModels: Array<{ modelId: string; name: string }>; currentModelId: string };
}

export function WorkspacesSettings() {
  const { t } = useTranslation();
  const [workspaces, setWorkspaces] = useState<WorkspaceItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [formState, setFormState] = useState<{ mode: 'closed' } | { mode: 'create' } | { mode: 'edit'; ws: WorkspaceItem }>({ mode: 'closed' });
  const [deleteConfirm, setDeleteConfirm] = useState<WorkspaceItem | null>(null);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [regenerating, setRegenerating] = useState(false);
  const [organizing, setOrganizing] = useState(false);

  const applyWorkspaceList = useCallback((list: WorkspaceItem[]) => {
    registerWorkspaceColors(list);
    setWorkspaces(list);
    for (const ws of list) {
      try { localStorage.setItem(`workspace_${ws.id}`, JSON.stringify(ws)); } catch { /* ignore */ }
    }
    window.dispatchEvent(new CustomEvent('quartet:workspace-list-updated', { detail: list }));
  }, []);

  const refresh = useCallback(async () => {
    try {
      const res = await fetch('/api/v1/workspace/list');
      if (!res.ok) return;
      const data = await res.json();
      const list: WorkspaceItem[] = data?.workspaces || [];
      registerWorkspaceColors(list);
      setWorkspaces(list);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    fetch('/api/v1/agent/list')
      .then((r) => r.json())
      .then((d) => setAgents((d?.agent_list || []).filter((agent: AgentInfo) => agent.available !== false)))
      .catch(() => setAgents([]));
  }, [refresh]);

  const handleRegenerateColors = useCallback(async () => {
    if (regenerating) return;
    setRegenerating(true);
    try {
      const res = await fetch('/api/v1/workspace/regenerate-colors', { method: 'POST' });
      if (!res.ok) {
        const err = await res.json().catch(() => null);
        alert(`${t('settings.workspace.regenerateFailed')}: ${err?.error || res.status}`);
        return;
      }
      const data = await res.json();
      const list: WorkspaceItem[] = data?.workspaces || [];
      applyWorkspaceList(list);
    } catch (err) {
      alert(`${t('settings.workspace.regenerateFailed')}: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setRegenerating(false);
    }
  }, [applyWorkspaceList, regenerating, t]);

  const readResponseError = useCallback(async (res: Response) => {
    const body = await res.text().catch((err) => String(err));
    return body || `HTTP ${res.status} ${res.statusText}`;
  }, []);

  const handleFavorite = useCallback(async (ws: WorkspaceItem) => {
    if (organizing) return;
    setOrganizing(true);
    try {
      const res = await fetch(`/api/v1/workspace/${ws.id}/favorite`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ favorite: !ws.favorite }),
      });
      if (!res.ok) {
        alert(`${t('settings.workspace.favoriteFailed')}: ${await readResponseError(res)}`);
        return;
      }
      const data = await res.json();
      applyWorkspaceList((data?.workspaces || []) as WorkspaceItem[]);
    } catch (err) {
      alert(`${t('settings.workspace.favoriteFailed')}: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setOrganizing(false);
    }
  }, [applyWorkspaceList, organizing, readResponseError, t]);

  const handleMove = useCallback(async (index: number, direction: -1 | 1) => {
    if (organizing) return;
    const targetIndex = index + direction;
    const ws = workspaces[index];
    const target = workspaces[targetIndex];
    if (!ws || !target || ws.favorite !== target.favorite) return;

    const reordered = workspaces.slice();
    [reordered[index], reordered[targetIndex]] = [reordered[targetIndex], reordered[index]];
    setOrganizing(true);
    try {
      const res = await fetch('/api/v1/workspace/order', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspaceIds: reordered.map((item) => item.id) }),
      });
      if (!res.ok) {
        alert(`${t('settings.workspace.orderFailed')}: ${await readResponseError(res)}`);
        return;
      }
      const data = await res.json();
      applyWorkspaceList((data?.workspaces || []) as WorkspaceItem[]);
    } catch (err) {
      alert(`${t('settings.workspace.orderFailed')}: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setOrganizing(false);
    }
  }, [applyWorkspaceList, organizing, readResponseError, t, workspaces]);

  const handleDelete = useCallback(async () => {
    if (!deleteConfirm) return;
    const id = deleteConfirm.id;
    setDeleteConfirm(null);
    try {
      const res = await fetch(`/api/v1/workspace/${id}`, { method: 'DELETE' });
      if (!res.ok) {
        const err = await res.json().catch(() => null);
        alert(`${t('settings.workspace.deleteFailed')}: ${err?.error || res.status}`);
        return;
      }
      try { localStorage.removeItem(`workspace_${id}`); } catch { /* ignore */ }
      window.dispatchEvent(new CustomEvent('quartet:workspace-deleted', { detail: { id } }));
      void refresh();
    } catch (err) {
      alert(`${t('settings.workspace.deleteFailed')}: ${err instanceof Error ? err.message : String(err)}`);
    }
  }, [deleteConfirm, refresh, t]);

  return (
    <div className="ws-settings">
      <div className="ws-settings-header">
        <h3>{t('settings.workspace.title')}</h3>
        <div className="ws-settings-header-actions">
          <button
            className="ws-settings-regen"
            onClick={handleRegenerateColors}
            disabled={regenerating || loading || workspaces.length === 0}
            title={t('settings.workspace.regenerateColors')}
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M21 2v6h-6" />
              <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
              <path d="M3 22v-6h6" />
              <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
            </svg>
            {regenerating ? t('settings.workspace.regenerating') : t('settings.workspace.regenerateColors')}
          </button>
          <button className="ws-settings-create" onClick={() => setFormState({ mode: 'create' })}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
              <line x1="12" y1="5" x2="12" y2="19" />
              <line x1="5" y1="12" x2="19" y2="12" />
            </svg>
            {t('settings.workspace.createWorkspace')}
          </button>
        </div>
      </div>

      {loading ? (
        <div className="ws-settings-empty">{t('common.loading')}</div>
      ) : workspaces.length === 0 ? (
        <div className="ws-settings-empty">{t('settings.workspace.noWorkspaces')}</div>
      ) : (
        <div className="ws-settings-list">
          {workspaces.map((ws, index) => {
            const isDefault = isDefaultWorkspace(ws.id);
            const prefs = loadWorkspacePrefs(ws.id);
            const canMoveUp = index > 0 && workspaces[index - 1].favorite === ws.favorite;
            const canMoveDown = index < workspaces.length - 1 && workspaces[index + 1].favorite === ws.favorite;
            return (
              <div key={ws.id} className="ws-settings-row">
                <span className="ws-settings-row-color" style={{ backgroundColor: workspaceColor(ws) }} />
                <div className="ws-settings-row-main">
                  <div className="ws-settings-row-title">
                    {ws.title || ws.id}
                    {isDefault && <span className="ws-settings-row-badge">{t('settings.workspace.defaultBadge')}</span>}
                  </div>
                  <div className="ws-settings-row-path">{ws.workdir}</div>
                  {(prefs.defaultAgent || prefs.defaultModel) && (
                    <div className="ws-settings-row-prefs">
                      {t('settings.workspace.defaultPrefs', { agent: prefs.defaultAgent || '—', model: prefs.defaultModel || '—' })}
                    </div>
                  )}
                </div>
                <div className="ws-settings-row-order">
                  <button
                    className={`ws-settings-favorite${ws.favorite ? ' active' : ''}`}
                    onClick={() => void handleFavorite(ws)}
                    disabled={organizing}
                    aria-label={ws.favorite ? t('settings.workspace.unfavorite') : t('settings.workspace.favorite')}
                    title={ws.favorite ? t('settings.workspace.unfavorite') : t('settings.workspace.favorite')}
                  >{ws.favorite ? '★' : '☆'}</button>
                  <button
                    onClick={() => void handleMove(index, -1)}
                    disabled={organizing || !canMoveUp}
                    aria-label={t('settings.workspace.moveUp')}
                    title={t('settings.workspace.moveUp')}
                  >↑</button>
                  <button
                    onClick={() => void handleMove(index, 1)}
                    disabled={organizing || !canMoveDown}
                    aria-label={t('settings.workspace.moveDown')}
                    title={t('settings.workspace.moveDown')}
                  >↓</button>
                </div>
                <div className="ws-settings-row-actions">
                  <button onClick={() => setFormState({ mode: 'edit', ws })}>{t('common.edit')}</button>
                  <button
                    className="danger"
                    onClick={() => setDeleteConfirm(ws)}
                    disabled={isDefault}
                    title={isDefault ? t('settings.workspace.cannotDeleteDefault') : t('common.delete')}
                  >{t('common.delete')}</button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {formState.mode !== 'closed' && (
        <WorkspaceFormModal
          mode={formState.mode}
          initial={formState.mode === 'edit' ? formState.ws : undefined}
          agents={agents}
          onClose={() => setFormState({ mode: 'closed' })}
          onSaved={() => { setFormState({ mode: 'closed' }); void refresh(); }}
        />
      )}

      {deleteConfirm && (
        <div className="ws-settings-modal-overlay" onClick={() => setDeleteConfirm(null)}>
          <div className="ws-settings-modal" onClick={(e) => e.stopPropagation()}>
            <h3>{t('settings.workspace.deleteWorkspace')}</h3>
            <p>{t('settings.workspace.deleteConfirm', { title: deleteConfirm.title || deleteConfirm.id })}</p>
            <div className="ws-settings-modal-actions">
              <button onClick={() => setDeleteConfirm(null)}>{t('common.cancel')}</button>
              <button className="danger" onClick={handleDelete}>{t('common.delete')}</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ---- Form modal ----

interface FormProps {
  mode: 'create' | 'edit';
  initial?: WorkspaceItem;
  agents: AgentInfo[];
  onClose: () => void;
  onSaved: () => void;
}

function WorkspaceFormModal({ mode, initial, agents, onClose, onSaved }: FormProps) {
  const { t } = useTranslation();
  const [title, setTitle] = useState(initial?.title || '');
  const [description, setDescription] = useState(initial?.description || '');
  const [workdir, setWorkdir] = useState(initial?.workdir || '');
  const [showDirPicker, setShowDirPicker] = useState(false);
  const [prefs, setPrefs] = useState<WorkspacePrefs>(() => (initial ? loadWorkspacePrefs(initial.id) : {}));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  // Prefill the workdir picker for new workspaces with the backend's
  // canonical default (sandbox UserHomeDir → $HOME → temp dir). Skipped in
  // edit mode and skipped if the user has already typed something before
  // the request resolves.
  useEffect(() => {
    if (mode !== 'create') return;
    let cancelled = false;
    fetch('/api/v1/workspace/default-workdir')
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (cancelled || !data || typeof data.workdir !== 'string') return;
        setWorkdir((current) => (current ? current : data.workdir));
      })
      .catch(() => {
        // Non-fatal: fall back to an empty input, which the user can fill manually.
      });
    return () => {
      cancelled = true;
    };
  }, [mode]);

  const isDefault = initial ? isDefaultWorkspace(initial.id) : false;

  const selectedAgentInfo = prefs.defaultAgent
    ? agents.find((a) => a.type === prefs.defaultAgent)
    : undefined;
  const availableModels = selectedAgentInfo?.models?.availableModels || [];

  const canSave = title.trim() && workdir.trim() && !saving;

  const handleSave = async () => {
    if (!canSave) return;
    setSaving(true);
    setError('');
    try {
      const body = {
        title: title.trim(),
        description: description.trim(),
        workdir: workdir.trim(),
        defaultAgent: prefs.defaultAgent || '',
        defaultModel: prefs.defaultModel || '',
      };
      const url = mode === 'edit' ? `/api/v1/workspace/${initial!.id}` : '/api/v1/workspace/create';
      const method = mode === 'edit' ? 'PUT' : 'POST';
      const res = await fetch(url, {
        method: mode === 'edit' ? 'PATCH' : method,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(mode === 'edit' ? {
          expectedVersion: initial!.version,
          title: body.title,
          description: body.description,
          workdir: body.workdir,
          defaultAgent: body.defaultAgent,
          defaultModel: body.defaultModel,
        } : body),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        throw new Error(data?.error || `HTTP ${res.status}`);
      }
      const data = await res.json();
      const savedId = (data?.id as string | undefined) ?? initial?.id;
      if (savedId) {
        const supportsSharedPrefs = Object.prototype.hasOwnProperty.call(data, 'defaultAgent')
          || Object.prototype.hasOwnProperty.call(data, 'defaultModel');
        if (supportsSharedPrefs) {
          registerWorkspacePrefs(savedId, prefs);
          try { localStorage.removeItem(`workspacePrefs_${savedId}`); } catch { /* ignore */ }
        } else {
          saveWorkspacePrefs(savedId, prefs);
        }
        try {
          const cached = data as WorkspaceItem;
          localStorage.setItem(`workspace_${savedId}`, JSON.stringify(cached));
          window.dispatchEvent(new CustomEvent('quartet:workspace-updated', { detail: cached }));
        } catch { /* ignore */ }
      }
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <div className="ws-settings-modal-overlay" onClick={onClose}>
        <div className="ws-settings-modal ws-form-modal" onClick={(e) => e.stopPropagation()}>
          <h3>{mode === 'edit' ? t('settings.workspace.formEdit') : t('settings.workspace.formCreate')}</h3>
          {isDefault && <p className="ws-form-hint">{t('settings.workspace.formDefaultHint', { id: DEFAULT_WORKSPACE_ID })}</p>}
          <div className="ws-form-field">
            <label>{t('settings.workspace.formName')}</label>
            <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={t('settings.workspace.formNamePlaceholder')} />
          </div>
          <div className="ws-form-field">
            <label>{t('settings.workspace.formDescription')}</label>
            <textarea value={description} onChange={(e) => setDescription(e.target.value)} placeholder={t('settings.workspace.formDescPlaceholder')} rows={2} />
          </div>
          <div className="ws-form-field">
            <label>{t('settings.workspace.formWorkdir')}</label>
            <div className="ws-form-workdir">
              <input value={workdir} onChange={(e) => setWorkdir(e.target.value)} placeholder={t('settings.workspace.formWorkdirPlaceholder')} />
              <button onClick={() => setShowDirPicker(true)}>{t('common.browse')}</button>
            </div>
          </div>
          <div className="ws-form-field">
            <label>{t('settings.workspace.formDefaultAgent')}</label>
            <select
              value={prefs.defaultAgent || ''}
              onChange={(e) => setPrefs({ defaultAgent: e.target.value || undefined, defaultModel: undefined })}
            >
              <option value="">{t('settings.workspace.formDefaultAgentOption')}</option>
              {agents.map((a) => (
                <option key={a.type} value={a.type}>{a.display_name || a.type}</option>
              ))}
            </select>
            <span className="ws-form-hint">{t('settings.workspace.formDefaultAgentHint')}</span>
          </div>
          {availableModels.length > 0 && (
            <div className="ws-form-field">
              <label>{t('settings.workspace.formDefaultModel')}</label>
              <select
                value={prefs.defaultModel || ''}
                onChange={(e) => setPrefs({ ...prefs, defaultModel: e.target.value || undefined })}
              >
                <option value="">{t('settings.workspace.formDefaultModelOption')}</option>
                {availableModels.map((m) => (
                  <option key={m.modelId} value={m.modelId}>{m.name}</option>
                ))}
              </select>
            </div>
          )}
          {error && <div className="ws-form-error">{error}</div>}
          <div className="ws-settings-modal-actions">
            <button onClick={onClose}>{t('common.cancel')}</button>
            <button className="primary" onClick={handleSave} disabled={!canSave}>
              {saving ? t('common.saving') : (mode === 'edit' ? t('common.save') : t('common.create'))}
            </button>
          </div>
        </div>
      </div>
      {showDirPicker && (
        <DirPicker
          initialPath={workdir || '/'}
          onConfirm={(dir) => { setWorkdir(dir); setShowDirPicker(false); }}
          onCancel={() => setShowDirPicker(false)}
        />
      )}
    </>
  );
}
