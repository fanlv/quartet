import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuthPrincipal } from '../../auth';
import {
  dispatchMessagePresetsChanged,
  responseError,
  type MessagePreset,
  type MessagePresetLoadError,
  type MessagePresetScopeResponse,
  type OrphanMessagePreset,
} from '../../utils/messagePresets';
import './MessagePresetSettings.css';

interface WorkspaceOption { id: string; title: string; workdir: string; }
type Scope = { type: 'global' } | { type: 'workspace'; id: string } | { type: 'orphan'; id: string };
interface Props { onDirtyChange?: (dirty: boolean) => void; }

function newPreset(): MessagePreset {
  const suffix = typeof crypto !== 'undefined' && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`;
  return { id: `preset-${suffix}`, name: '', content: '' };
}
function cloneMessages(items: MessagePreset[]) { return items.map((item) => ({ ...item })); }

export function MessagePresetSettings({ onDirtyChange }: Props) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const canReadGlobal = principal?.permissions.includes('config.read') ?? false;
  const canWriteGlobal = principal?.permissions.includes('config.write') ?? false;
  const canReadWorkspace = principal?.permissions.includes('workspace.read') ?? false;
  const canWriteWorkspace = principal?.permissions.includes('workspace.write') ?? false;
  const [workspaces, setWorkspaces] = useState<WorkspaceOption[]>([]);
  const [orphans, setOrphans] = useState<OrphanMessagePreset[]>([]);
  const orphansRef = useRef<OrphanMessagePreset[]>([]);
  const scopeLoadSequence = useRef(0);
  const [orphanErrors, setOrphanErrors] = useState<MessagePresetLoadError[]>([]);
  const [scope, setScope] = useState<Scope>(() => canReadGlobal ? { type: 'global' } : { type: 'workspace', id: '' });
  const [revision, setRevision] = useState('missing');
  const [messages, setMessages] = useState<MessagePreset[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [notice, setNotice] = useState<{ kind: 'success' | 'error'; text: string } | null>(null);

  const canWriteScope = scope.type === 'global' ? canWriteGlobal : scope.type === 'workspace' ? canWriteWorkspace : false;
  const selectedOrphan = scope.type === 'orphan' ? orphans.find((item) => item.config.workspaceId === scope.id) : undefined;

  const loadScope = useCallback(async (nextScope: Scope) => {
    const request = ++scopeLoadSequence.current;
    setLoading(true);
    try {
      if (nextScope.type === 'orphan') {
        const item = orphansRef.current.find((candidate) => candidate.config.workspaceId === nextScope.id);
        if (!item) throw new Error(t('settings.messagePresets.orphanMissing'));
        setRevision(item.revision);
        setMessages(cloneMessages(item.config.messages || []));
      } else {
        const url = nextScope.type === 'global' ? '/api/v1/config/message-presets/global' : `/api/v1/config/message-presets/workspaces/${encodeURIComponent(nextScope.id)}`;
        const response = await fetch(url, { cache: 'no-store' });
        if (!response.ok) throw new Error(await responseError(response));
        const body = await response.json() as MessagePresetScopeResponse;
        if (request !== scopeLoadSequence.current) return;
        setRevision(body.revision);
        setMessages(cloneMessages(body.config.messages || []));
      }
      if (request !== scopeLoadSequence.current) return;
      setDirty(false);
    } catch (error) {
      if (request !== scopeLoadSequence.current) return;
      setMessages([]);
      setNotice({ kind: 'error', text: error instanceof Error ? error.message : String(error) });
      setDirty(false);
    } finally {
      if (request === scopeLoadSequence.current) setLoading(false);
    }
  }, [t]);

  const loadMetadata = useCallback(async () => {
    const requests: Promise<void>[] = [];
    if (canReadWorkspace) requests.push(fetch('/api/v1/workspace/list', { cache: 'no-store' }).then(async (response) => {
      if (!response.ok) throw new Error(await responseError(response));
      const body = await response.json();
      setWorkspaces(body.workspaces || []);
    }));
    if (canReadGlobal) requests.push(fetch('/api/v1/config/message-presets/orphans', { cache: 'no-store' }).then(async (response) => {
      if (!response.ok) throw new Error(await responseError(response));
      const body = await response.json() as { configs?: OrphanMessagePreset[]; errors?: MessagePresetLoadError[] };
      orphansRef.current = body.configs || [];
      setOrphans(orphansRef.current);
      setOrphanErrors(body.errors || []);
    }));
    try { await Promise.all(requests); }
    catch (error) { setNotice({ kind: 'error', text: error instanceof Error ? error.message : String(error) }); }
  }, [canReadGlobal, canReadWorkspace]);

  useEffect(() => { void loadMetadata(); }, [loadMetadata]);
  useEffect(() => {
    if (scope.type === 'global' && !canReadGlobal) {
      setScope({ type: 'workspace', id: workspaces[0]?.id || '' });
      return;
    }
    if (scope.type === 'workspace' && !scope.id && workspaces.length > 0) {
      setScope({ type: 'workspace', id: workspaces[0].id });
      return;
    }
    if (scope.type === 'workspace' && !scope.id) {
      setLoading(false);
      return;
    }
    void loadScope(scope);
  }, [canReadGlobal, loadScope, scope, workspaces]);
  useEffect(() => { onDirtyChange?.(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange]);
  useEffect(() => {
    const beforeUnload = (event: BeforeUnloadEvent) => { if (dirty) { event.preventDefault(); event.returnValue = ''; } };
    window.addEventListener('beforeunload', beforeUnload);
    return () => window.removeEventListener('beforeunload', beforeUnload);
  }, [dirty]);

  const scopeValue = scope.type === 'global' ? 'global' : `${scope.type}:${scope.id}`;
  const chooseScope = (value: string) => {
    if (dirty && !window.confirm(t('settings.messagePresets.discardConfirm'))) return;
    setNotice(null);
    if (value === 'global') setScope({ type: 'global' });
    else if (value.startsWith('workspace:')) setScope({ type: 'workspace', id: value.slice('workspace:'.length) });
    else setScope({ type: 'orphan', id: value.slice('orphan:'.length) });
  };
  const changeMessages = (next: MessagePreset[]) => { setMessages(next); setDirty(true); setNotice(null); };

  const save = async () => {
    if (!canWriteScope || scope.type === 'orphan') return;
    setSaving(true); setNotice(null);
    try {
      const url = scope.type === 'global' ? '/api/v1/config/message-presets/global' : `/api/v1/config/message-presets/workspaces/${encodeURIComponent(scope.id)}`;
      const response = await fetch(url, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ revision, messages }) });
      if (!response.ok) throw new Error(await responseError(response));
      const body = await response.json() as MessagePresetScopeResponse;
      setRevision(body.revision); setMessages(cloneMessages(body.config.messages || [])); setDirty(false);
      setNotice({ kind: 'success', text: t('settings.messagePresets.saved') });
      dispatchMessagePresetsChanged(scope.type === 'workspace' ? scope.id : undefined);
      await loadMetadata();
    } catch (error) { setNotice({ kind: 'error', text: error instanceof Error ? error.message : String(error) }); }
    finally { setSaving(false); }
  };

  const deleteOrphan = async () => {
    if (!selectedOrphan || !canWriteGlobal || !window.confirm(t('settings.messagePresets.deleteOrphanConfirm'))) return;
    setSaving(true);
    try {
      const id = selectedOrphan.config.workspaceId || '';
      const response = await fetch(`/api/v1/config/message-presets/orphans/${encodeURIComponent(id)}?revision=${encodeURIComponent(selectedOrphan.revision)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await responseError(response));
      await loadMetadata(); setScope(canReadGlobal ? { type: 'global' } : { type: 'workspace', id: workspaces[0]?.id || '' });
      setNotice({ kind: 'success', text: t('settings.messagePresets.deleted') });
    } catch (error) { setNotice({ kind: 'error', text: error instanceof Error ? error.message : String(error) }); }
    finally { setSaving(false); }
  };

  const rebindOrphan = async (targetWorkspaceId: string) => {
    if (!selectedOrphan || !targetWorkspaceId || !canWriteGlobal || !canWriteWorkspace) return;
    setSaving(true);
    try {
      const sourceID = selectedOrphan.config.workspaceId || '';
      const targetResponse = await fetch(`/api/v1/config/message-presets/workspaces/${encodeURIComponent(targetWorkspaceId)}`, { cache: 'no-store' });
      if (!targetResponse.ok) throw new Error(await responseError(targetResponse));
      const targetConfig = await targetResponse.json() as MessagePresetScopeResponse;
      if (targetConfig.revision !== 'missing') throw new Error(t('settings.messagePresets.targetHasPresets'));
      const response = await fetch(`/api/v1/config/message-presets/orphans/${encodeURIComponent(sourceID)}/rebind`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ revision: selectedOrphan.revision, targetWorkspaceId }) });
      if (!response.ok) throw new Error(await responseError(response));
      await loadMetadata(); setScope({ type: 'workspace', id: targetWorkspaceId });
      setNotice({ kind: 'success', text: t('settings.messagePresets.rebound') }); dispatchMessagePresetsChanged(targetWorkspaceId);
    } catch (error) { setNotice({ kind: 'error', text: error instanceof Error ? error.message : String(error) }); }
    finally { setSaving(false); }
  };

  const availableRebindTargets = useMemo(() => workspaces.filter((workspace) => !orphans.some((item) => item.config.workspaceId === workspace.id)), [orphans, workspaces]);

  return <div className="preset-settings" data-testid="message-preset-settings">
    <div className="preset-settings-header"><div><h3>{t('settings.messagePresets.title')}</h3><p>{t('settings.messagePresets.description')}</p></div>
      <select value={scopeValue} disabled={saving} onChange={(event) => chooseScope(event.target.value)} aria-label={t('settings.messagePresets.scope')}>
        {canReadGlobal && <option value="global">{t('settings.messagePresets.global')}</option>}
        {canReadWorkspace && workspaces.map((workspace) => <option key={workspace.id} value={`workspace:${workspace.id}`}>{workspace.title || workspace.id} — {workspace.workdir}</option>)}
        {canReadGlobal && orphans.map((item) => <option key={item.config.workspaceId} value={`orphan:${item.config.workspaceId}`}>{t('settings.messagePresets.orphanLabel', { name: item.config.workspaceTitle || item.config.workspaceId })}</option>)}
      </select>
    </div>
    {orphanErrors.map((error) => <div key={`${error.file}:${error.error}`} className="preset-notice error">{error.file}: {error.error}</div>)}
    {notice && <div className={`preset-notice ${notice.kind}`}>{notice.text}</div>}
    {scope.type === 'orphan' && selectedOrphan && <div className="preset-orphan-actions"><div><strong>{selectedOrphan.config.workspaceTitle || selectedOrphan.config.workspaceId}</strong><span>{selectedOrphan.config.workspaceWorkdir}</span></div>
      {canWriteGlobal && canWriteWorkspace && <select defaultValue="" onChange={(event) => { if (event.target.value) void rebindOrphan(event.target.value); }} disabled={saving}><option value="">{t('settings.messagePresets.rebind')}</option>{availableRebindTargets.map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.title} — {workspace.workdir}</option>)}</select>}
      {canWriteGlobal && <button className="danger" onClick={() => void deleteOrphan()} disabled={saving}>{t('common.delete')}</button>}
    </div>}
    {loading ? <div className="preset-empty">{t('common.loading')}</div> : <div className="preset-list">
      {messages.length === 0 && <div className="preset-empty">{t('settings.messagePresets.empty')}</div>}
      {messages.map((item, index) => <div className="preset-row" key={item.id}><div className="preset-order">
        <button disabled={!canWriteScope || index === 0} onClick={() => { const next = [...messages]; [next[index - 1], next[index]] = [next[index], next[index - 1]]; changeMessages(next); }}>↑</button>
        <button disabled={!canWriteScope || index === messages.length - 1} onClick={() => { const next = [...messages]; [next[index], next[index + 1]] = [next[index + 1], next[index]]; changeMessages(next); }}>↓</button>
      </div><div className="preset-fields"><input value={item.name || ''} disabled={!canWriteScope} placeholder={t('settings.messagePresets.namePlaceholder')} onChange={(event) => changeMessages(messages.map((candidate, i) => i === index ? { ...candidate, name: event.target.value } : candidate))}/>
        <textarea value={item.content} disabled={!canWriteScope} rows={4} placeholder={t('settings.messagePresets.contentPlaceholder')} onChange={(event) => changeMessages(messages.map((candidate, i) => i === index ? { ...candidate, content: event.target.value } : candidate))}/></div>
        {canWriteScope && <button className="danger" onClick={() => changeMessages(messages.filter((_, i) => i !== index))}>{t('common.delete')}</button>}</div>)}
    </div>}
    {scope.type !== 'orphan' && canWriteScope && <div className="preset-footer"><button onClick={() => changeMessages([...messages, newPreset()])}>{t('settings.messagePresets.add')}</button><button className="primary" onClick={() => void save()} disabled={saving || !dirty}>{saving ? t('common.saving') : t('common.save')}</button></div>}
  </div>;
}
