import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { createPortal } from 'react-dom';
import { useAuthPrincipal } from '../auth';
import type { ScheduleInfo, WorkspaceInfo } from '../types';
import { ScheduleEditModal } from './ScheduleEditModal';

async function readScheduleResponse<T>(response: Response): Promise<T> {
  const raw = await response.text();
  let value: T & { code?: number };
  try { value = JSON.parse(raw) as typeof value; }
  catch { throw new Error(`${response.url}\nHTTP ${response.status}\n${raw}`); }
  if (!response.ok || (value.code !== undefined && value.code !== 0)) {
    throw new Error(`${response.url}\nHTTP ${response.status}\n${raw}`);
  }
  return value;
}

// Keep Graph scheduling on its existing API and editor. The group-chat home
// exposes the same capability from its navigation instead of the old job list.
export function PersonalSchedules({ onSelectJob }: { onSelectJob?: (jobId: string, workspaceId?: string) => void }) {
  const { t, i18n } = useTranslation();
  const principal = useAuthPrincipal();
  const writable = !!principal?.permissions.includes('schedule.write');
  const canReadWorkspace = !!principal?.permissions.includes('workspace.read');
  const [rows, setRows] = useState<ScheduleInfo[]>([]);
  const [workspaces, setWorkspaces] = useState<WorkspaceInfo[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<ScheduleInfo | null | undefined>();
  const [deleting, setDeleting] = useState<ScheduleInfo>();
  const [busy, setBusy] = useState('');
  const generation = useRef(0);
  const invalidate = useCallback(() => { generation.current++; }, []);
  const refresh = useCallback(async () => {
    const current = ++generation.current;
    try {
      const [value, workspaceList] = await Promise.all([
        fetch('/api/v1/schedule/list').then(readScheduleResponse<{ schedules: ScheduleInfo[] }>),
        canReadWorkspace ? fetch('/api/v1/workspace/list').then(readScheduleResponse<{ workspaces: WorkspaceInfo[] }>) : Promise.resolve({ workspaces: [] }),
      ]);
      if (generation.current !== current) return;
      setRows(value.schedules || []);
      setWorkspaces(workspaceList.workspaces || []);
      setError('');
    } catch (e) { if (generation.current === current) setError(String(e)); }
    finally { if (generation.current === current) setLoaded(true); }
  }, [canReadWorkspace]);
  useEffect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), 60_000);
    const focus = () => void refresh();
    window.addEventListener('focus', focus);
    return () => { invalidate(); clearInterval(timer); window.removeEventListener('focus', focus); };
  }, [refresh, invalidate]);
  const control = async (row: ScheduleInfo, action: 'toggle' | 'delete') => {
    if (busy) return;
    setBusy(row.id);
    try {
      await readScheduleResponse(await fetch(`/api/v1/schedule/${encodeURIComponent(row.id)}${action === 'toggle' ? '/toggle' : ''}`, { method: action === 'toggle' ? 'POST' : 'DELETE' }));
      setDeleting(undefined);
      await refresh();
    } catch (e) { setError(String(e)); }
    finally { setBusy(''); }
  };
  return <div className="personal-schedules">
    {writable && <button className="personal-primary" onClick={() => setEditing(null)}>{t('home.newTask')}</button>}
    {error && <pre className="personal-error" role="alert">{error}</pre>}
    {!loaded && <p role="status">{t('personal.loading')}</p>}
    {loaded && rows.length === 0 && <p className="personal-search-summary">{t('personal.noSchedules')}</p>}
    {rows.map(row => <div className="personal-schedule" key={row.id}>
      <button className="personal-schedule-name" onClick={() => setEditing(row)} disabled={!writable}>
        <strong>{row.name}</strong><span>{row.cronExpr}</span>
        <span>{row.enabled ? (row.nextRunAt ? new Date(row.nextRunAt).toLocaleString(i18n.language) : t('personal.scheduleEnabled')) : t('home.disabled')}</span>
      </button>
      {row.lastTriggerError && <pre className="personal-error">{row.lastTriggerError}</pre>}
      <div className="personal-schedule-actions">
        {row.lastRunJobID && onSelectJob && <button onClick={() => onSelectJob(row.lastRunJobID!, row.workspaceId)}>{t('schedule.openLastRunJob')}</button>}
        {writable && <>
          <button disabled={!!busy} onClick={() => void control(row, 'toggle')}>{t(row.enabled ? 'personal.disableSchedule' : 'personal.enableSchedule')}</button>
          <button disabled={!!busy} onClick={() => setDeleting(row)}>{t('common.delete')}</button>
        </>}
      </div>
    </div>)}
    {editing !== undefined && writable && createPortal(<ScheduleEditModal schedule={editing} workspaces={workspaces} agents={[]} defaultAgentIndex={0} onSelectJob={onSelectJob} onClose={() => setEditing(undefined)} onSave={() => { setEditing(undefined); void refresh(); }} />, document.body)}
    {deleting && createPortal(<div className="delete-confirm-overlay" onClick={() => !busy && setDeleting(undefined)}><div className="delete-confirm-dialog" role="dialog" aria-modal="true" aria-label={t('common.delete')} onClick={e => e.stopPropagation()}>
      <p>{t('personal.deleteScheduleConfirm', { name: deleting.name })}</p>
      {error && <pre className="personal-error" role="alert">{error}</pre>}
      <div className="delete-confirm-actions"><button disabled={!!busy} onClick={() => setDeleting(undefined)}>{t('common.close')}</button><button className="delete-confirm-ok" disabled={!!busy} onClick={() => void control(deleting, 'delete')}>{t('common.delete')}</button></div>
    </div></div>, document.body)}
  </div>;
}
