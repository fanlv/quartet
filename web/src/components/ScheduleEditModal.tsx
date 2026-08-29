import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { ScheduleInfo, type AgentInfo } from '../types';
import { GraphListWorkflowsResponse, GraphWorkflowSummary, GraphWorkflowWarning } from '../types/graph';
import { CronInput } from './CronInput';
import { workspaceColor, DEFAULT_WORKSPACE_ID } from '../utils/workspace';
import './ScheduleEditModal.css';

interface ScheduleEditModalProps {
  schedule?: ScheduleInfo | null;
  // Optional: workspace the task belongs to. Empty = task runs in the default
  // workspace (ws-1) at trigger time (方向二-d).
  workspaceId?: string;
  // All workspaces, used to resolve a bound schedule's workspaceId into a
  // human-readable name in the edit view.
  workspaces?: Array<{ id: string; title: string; workdir?: string }>;
  agents: AgentInfo[];
  defaultAgentIndex: number;
  onSelectJob?: (jobId: string, workspaceId?: string) => void;
  onSave: () => void;
  onClose: () => void;
}

async function readResponseError(res: Response): Promise<string> {
  const body = await res.text().catch(() => '');
  const trimmed = body.trim();
  if (!trimmed) return `HTTP ${res.status}`;
  try {
    const data = JSON.parse(trimmed);
    if (Array.isArray(data?.errors) && data.errors.length > 0) {
      return data.errors.map((err: unknown) => {
        if (typeof err === 'string') return err;
        if (err && typeof err === 'object') {
          const record = err as Record<string, unknown>;
          return record.message || record.error || JSON.stringify(record);
        }
        return String(err);
      }).join('\n');
    }
    return data?.msg || data?.error || data?.message || trimmed;
  } catch {
    return trimmed;
  }
}

async function fetchGraphWorkflows(): Promise<GraphListWorkflowsResponse> {
  const res = await fetch('/api/v1/graph/workflow/list');
  if (!res.ok) throw new Error(await readResponseError(res));
  const data = await res.json() as GraphListWorkflowsResponse;
  return { workflows: data.workflows || [], warnings: data.warnings || [] };
}

export function ScheduleEditModal({ schedule, workspaceId, workspaces, agents: _agents, defaultAgentIndex: _defaultAgentIndex, onSelectJob, onSave, onClose }: ScheduleEditModalProps) {
  const { t } = useTranslation();
  const isEdit = !!schedule;
  const overlayRef = useRef<HTMLDivElement>(null);

  const [name, setName] = useState(schedule?.name || '');
  const [cronExpr, setCronExpr] = useState(schedule?.cronExpr || '0 9 * * *');
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true);
  const [graphWorkflowId, setGraphWorkflowId] = useState(schedule?.graphWorkflowId || '');
  const [maxConcurrent, setMaxConcurrent] = useState(schedule?.maxConcurrent || 1);
  const [timeoutMins, setTimeoutMins] = useState(schedule?.timeout || 0);
  const [scheduleWorkspaceId, setScheduleWorkspaceId] = useState(schedule?.workspaceId || '');
  // Create-flow only: let the user opt in to binding to the current
  // workspace. Defaults to bind OFF — new tasks are unbound and run under the
  // default workspace (ws-1) at trigger time. Toggle on to send the current
  // workspaceId and pin the task to this workspace.
  const [bindToCurrentWs, setBindToCurrentWs] = useState(false);

  const [graphWorkflows, setGraphWorkflows] = useState<GraphWorkflowSummary[]>([]);
  const [graphWorkflowWarnings, setGraphWorkflowWarnings] = useState<GraphWorkflowWarning[]>([]);
  const [graphWorkflowsLoading, setGraphWorkflowsLoading] = useState(true);
  const [graphWorkflowsError, setGraphWorkflowsError] = useState('');
  const [saving, setSaving] = useState(false);
  const [triggering, setTriggering] = useState(false);
  const [error, setError] = useState('');
  const [viewportHeight, setViewportHeight] = useState(0);
  const [viewportOffsetTop, setViewportOffsetTop] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setGraphWorkflowsLoading(true);
    fetchGraphWorkflows()
      .then(data => {
        if (!cancelled) {
          setGraphWorkflows(data.workflows || []);
          setGraphWorkflowWarnings(data.warnings || []);
          setGraphWorkflowsError('');
        }
      })
      .catch(err => {
        if (!cancelled) {
          setGraphWorkflows([]);
          setGraphWorkflowWarnings([]);
          setGraphWorkflowsError(err instanceof Error ? err.message : String(err));
        }
      })
      .finally(() => {
        if (!cancelled) setGraphWorkflowsLoading(false);
      });

    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;

    const updateViewport = () => {
      setViewportHeight(Math.round(vv.height));
      setViewportOffsetTop(Math.max(0, Math.round(vv.offsetTop)));

      const active = document.activeElement as HTMLElement | null;
      if (active && overlayRef.current?.contains(active)) {
        requestAnimationFrame(() => {
          active.scrollIntoView({ block: 'center', behavior: 'smooth' });
        });
      }
    };

    updateViewport();
    vv.addEventListener('resize', updateViewport);
    vv.addEventListener('scroll', updateViewport);
    return () => {
      vv.removeEventListener('resize', updateViewport);
      vv.removeEventListener('scroll', updateViewport);
    };
  }, []);

  const overlayStyle = useMemo(() => {
    if (!viewportHeight) return undefined;
    return {
      top: `${viewportOffsetTop}px`,
      height: `${viewportHeight}px`,
      bottom: 'auto',
    };
  }, [viewportHeight, viewportOffsetTop]);

  const modalStyle = useMemo(() => {
    if (!viewportHeight) return undefined;
    return {
      maxHeight: `${Math.max(viewportHeight - 32, 320)}px`,
    };
  }, [viewportHeight]);

  const handleGraphWorkflowSelect = useCallback((wid: string) => {
    setGraphWorkflowId(wid);
    const wf = graphWorkflows.find(w => w.id === wid);
    if (wf) {
      setName(prev => prev || wf.name);
    }
  }, [graphWorkflows]);

  const selectedGraphWorkflow = useMemo(
    () => graphWorkflows.find(wf => wf.id === graphWorkflowId) || null,
    [graphWorkflowId, graphWorkflows]
  );
  const workflowWorkspaceLabel = useCallback((wf: GraphWorkflowSummary): string => {
    const wfWorkspaceId = wf.workspaceId || '';
    if (!wfWorkspaceId) return t('schedule.workflowWorkspaceUnbound');
    const match = workspaces?.find(w => w.id === wfWorkspaceId);
    return match?.title || wfWorkspaceId;
  }, [t, workspaces]);
  const sortedGraphWorkflows = useMemo(
    () => [...graphWorkflows].sort((a, b) => (a.name || '').localeCompare(b.name || '', undefined, { numeric: true, sensitivity: 'base' })),
    [graphWorkflows],
  );

  const selectedGraphWorkflowMissing = !!graphWorkflowId &&
    !graphWorkflowsLoading &&
    !selectedGraphWorkflow;

  // Edit-view only: resolve the schedule's bound workspaceId into a readable
  // name (+ workdir). An empty workspaceId means the task is unbound and runs
  // in the default workspace at trigger time.
  const boundWorkspace = useMemo(() => {
    if (!isEdit) return null;
    const wsId = scheduleWorkspaceId;
    if (!wsId) return { name: t('schedule.boundWorkspaceDefault'), workdir: '' };
    const match = workspaces?.find(w => w.id === wsId);
    return {
      name: match?.title || wsId,
      workdir: match?.workdir || (wsId === schedule?.workspaceId ? schedule?.workdir : '') || '',
    };
  }, [isEdit, scheduleWorkspaceId, schedule?.workspaceId, schedule?.workdir, workspaces, t]);

  const handleSave = async () => {
    if (!name.trim()) {
      setError(t('schedule.taskNameRequired'));
      return;
    }
    if (!graphWorkflowId) {
      setError(t('schedule.selectGraphWorkflowRequired'));
      return;
    }
    if (selectedGraphWorkflowMissing) {
      setError(t('schedule.graphWorkflowDeleted'));
      return;
    }
    setSaving(true);
    setError('');

    try {
      const body: Record<string, unknown> = {
        name,
        cronExpr,
        enabled,
        maxConcurrent,
        timeout: timeoutMins,
        graphWorkflowId,
      };
      if (isEdit && schedule) {
        body.workspaceId = scheduleWorkspaceId;
        if (scheduleWorkspaceId !== (schedule.workspaceId || '')) {
          body.workdir = '';
        }
        const res = await fetch(`/api/v1/schedule/${schedule.id}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!res.ok) {
          throw new Error(await readResponseError(res));
        }
      } else {
        // Only send workspaceId when the user opted to bind to the current
        // workspace. Leaving it empty tells the backend to fall back to the
        // default workspace when the cron trigger fires.
        if (workspaceId && bindToCurrentWs) body.workspaceId = workspaceId;
        const res = await fetch('/api/v1/schedule/create', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!res.ok) {
          throw new Error(await readResponseError(res));
        }
      }
      onSave();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Save failed');
    } finally {
      setSaving(false);
    }
  };

  const openLastRunJob = () => {
    if (!schedule?.lastRunJobID) return;
    onClose();
    onSelectJob?.(schedule.lastRunJobID, schedule.workspaceId || undefined);
  };

  const handleTriggerNow = async () => {
    if (!schedule || triggering) return;
    setTriggering(true);
    setError('');
    try {
      const res = await fetch(`/api/v1/schedule/${schedule.id}/run`, { method: 'POST' });
      if (res.ok) {
        onSave();
      } else {
        setError(await readResponseError(res));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setTriggering(false);
    }
  };

  return (
    <div className="schedule-modal-overlay" onClick={onClose} ref={overlayRef} style={overlayStyle}>
      <div className="schedule-modal" onClick={e => e.stopPropagation()} style={modalStyle}>
        <div className="schedule-modal-header">
          <h2>{isEdit ? t('schedule.editTitle') : t('schedule.createTitle')}</h2>
          <button className="schedule-modal-close" onClick={onClose}>×</button>
        </div>

        <div className="schedule-modal-body">
          <div className="schedule-field schedule-field-full">
            <label>{t('schedule.selectGraphWorkflow')}</label>
            {graphWorkflowsLoading ? (
              <div className="schedule-no-templates">{t('schedule.loadingGraphWorkflows')}</div>
            ) : graphWorkflowsError ? (
              <div className="schedule-load-error">{t('schedule.graphWorkflowLoadFailed')}: {graphWorkflowsError}</div>
            ) : graphWorkflows.length === 0 ? (
              <div className="schedule-no-templates">{t('schedule.noGraphWorkflows')}</div>
            ) : (
              <select
                value={graphWorkflowId}
                onChange={e => handleGraphWorkflowSelect(e.target.value)}
                className="schedule-select"
              >
                <option value="">{t('schedule.selectGraphWorkflowPlaceholder')}</option>
                {selectedGraphWorkflowMissing && (
                  <option value={graphWorkflowId}>{t('schedule.missingGraphWorkflowOption', { id: graphWorkflowId })}</option>
                )}
                {sortedGraphWorkflows.map(wf => (
                  <option key={wf.id} value={wf.id}>
                    {t('schedule.graphWorkflowOption', { name: wf.name, workspace: workflowWorkspaceLabel(wf) })}
                  </option>
                ))}
              </select>
            )}
            {selectedGraphWorkflow && (
              <div className="schedule-config-preview">
                <div>
                  {t('schedule.graphWorkflowPreview', {
                    nodes: selectedGraphWorkflow.nodeCount || 0,
                    edges: selectedGraphWorkflow.edgeCount || 0,
                  })}
                </div>
                <div className="schedule-config-preview-workspace">
                  {t('schedule.graphWorkflowWorkspace', { workspace: workflowWorkspaceLabel(selectedGraphWorkflow) })}
                </div>
              </div>
            )}
            {selectedGraphWorkflowMissing && (
              <div className="schedule-load-error">{t('schedule.graphWorkflowDeleted')}</div>
            )}
            {graphWorkflowWarnings.length > 0 && (
              <div className="schedule-workflow-warnings" data-testid="schedule-graph-workflow-warnings">
                <div className="schedule-workflow-warnings-title">{t('schedule.graphWorkflowWarnings')}</div>
                {graphWorkflowWarnings.map((warning) => (
                  <div key={`${warning.file}:${warning.error}`} className="schedule-workflow-warning-item">
                    <strong>{warning.file}</strong>: {warning.error}
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Name */}
          <div className="schedule-field">
            <label>{t('schedule.taskName')}</label>
            <input
              type="text"
              value={name}
              onChange={e => setName(e.target.value)}
              placeholder={t('schedule.taskNamePlaceholder')}
            />
          </div>

          {/* Workspace binding (create only). Lets the user decouple the
              schedule from the current workspace — unbound tasks run in the
              default workspace (ws-1) at trigger time. */}
          {!isEdit && (
            <div className="schedule-field schedule-field-row">
              <label>{t('schedule.workspaceBinding')}</label>
              <label className="schedule-ws-bind">
                <input
                  type="checkbox"
                  checked={bindToCurrentWs}
                  disabled={!workspaceId}
                  onChange={e => setBindToCurrentWs(e.target.checked)}
                />
                <span>
                  {workspaceId
                    ? (bindToCurrentWs
                        ? t('schedule.bindCurrent')
                        : t('schedule.unboundHint'))
                    : t('schedule.unboundHint')}
                </span>
              </label>
            </div>
          )}

          {/* Bound workspace (edit only) — shows which workspace this schedule
              is pinned to, or the default workspace when unbound. */}
          {isEdit && boundWorkspace && (
            <div className="schedule-field">
              <label>{t('schedule.boundWorkspace')}</label>
              <select
                className="schedule-select"
                value={scheduleWorkspaceId}
                onChange={e => setScheduleWorkspaceId(e.target.value)}
              >
                <option value="">{t('schedule.boundWorkspaceDefault')}</option>
                {scheduleWorkspaceId && !workspaces?.some(w => w.id === scheduleWorkspaceId) && (
                  <option value={scheduleWorkspaceId}>{t('schedule.missingWorkspaceOption', { id: scheduleWorkspaceId })}</option>
                )}
                {(workspaces || []).map(ws => (
                  <option key={ws.id} value={ws.id}>
                    {ws.title || ws.id}
                  </option>
                ))}
              </select>
              <div className="schedule-ws-bound">
                <span
                  className="schedule-ws-bound-dot"
                  style={{ backgroundColor: workspaceColor(scheduleWorkspaceId || DEFAULT_WORKSPACE_ID) }}
                />
                <span className="schedule-ws-bound-name">{boundWorkspace.name}</span>
                {boundWorkspace.workdir && (
                  <span className="schedule-ws-bound-workdir">{boundWorkspace.workdir}</span>
                )}
              </div>
            </div>
          )}

          {/* Run history for existing schedule. Paired next to the bound
              workspace so the two edit-only blocks share one grid row instead
              of each taking a full-width row. */}
          {isEdit && schedule && (
            <div className="schedule-field">
              <label>{t('schedule.runInfo')}</label>
              <div className="schedule-run-info">
                <div>{t('schedule.runCount', { count: schedule.runCount })}</div>
                {schedule.lastRunAt && (
                  <div>{t('schedule.lastRun')} {new Date(schedule.lastRunAt).toLocaleString()}</div>
                )}
                {schedule.nextRunAt && schedule.enabled && (
                  <div>{t('schedule.nextRun')} {new Date(schedule.nextRunAt).toLocaleString()}</div>
                )}
                {schedule.lastStatus && (
                  <div>{t('schedule.lastStatus')} {schedule.lastStatus}</div>
                )}
                {schedule.lastStatus === 'failed' && schedule.lastTriggerError && (
                  <div className="schedule-run-error">
                    <span>{t('schedule.lastTriggerError')}</span>
                    <pre>{schedule.lastTriggerError}</pre>
                  </div>
                )}
                {schedule.lastStatus === 'failed' && !schedule.lastTriggerError && schedule.lastRunJobID && (
                  <button className="schedule-run-link" type="button" onClick={openLastRunJob}>
                    {t('schedule.openLastRunJob')}
                  </button>
                )}
              </div>
            </div>
          )}

          {/* Enable toggle */}
          <div className="schedule-field schedule-field-row">
            <label>{t('schedule.enable')}</label>
            <button
              className={`schedule-toggle ${enabled ? 'on' : 'off'}`}
              onClick={() => setEnabled(!enabled)}
              type="button"
            >
              <span className="schedule-toggle-thumb" />
            </button>
          </div>

          {/* Cron */}
          <div className="schedule-field schedule-field-full">
            <label>{t('schedule.frequency')}</label>
            <CronInput value={cronExpr} onChange={setCronExpr} />
          </div>

          {/* Advanced */}
          <details className="schedule-advanced">
            <summary>{t('schedule.advanced')}</summary>
            <div className="schedule-advanced-fields">
              <div className="schedule-field">
                <label>{t('schedule.maxConcurrent')}</label>
                <input
                  type="number"
                  min={1}
                  max={5}
                  value={maxConcurrent}
                  onChange={e => setMaxConcurrent(parseInt(e.target.value) || 1)}
                />
                <span className="schedule-field-hint">{t('schedule.maxConcurrentHint')}</span>
              </div>
              <div className="schedule-field">
                <label>{t('schedule.timeout')}</label>
                <input
                  type="number"
                  min={0}
                  value={timeoutMins}
                  onChange={e => setTimeoutMins(parseInt(e.target.value) || 0)}
                />
                <span className="schedule-field-hint">{t('schedule.timeoutHint')}</span>
              </div>
            </div>
          </details>

          {error && <div className="schedule-error schedule-field-full">{error}</div>}
        </div>

        <div className="schedule-modal-footer">
          {isEdit && (
            <button className="schedule-btn schedule-btn-trigger" onClick={handleTriggerNow} disabled={triggering || saving} type="button">
              {triggering ? t('schedule.triggering') : t('schedule.triggerNow')}
            </button>
          )}
          <div className="schedule-modal-footer-right">
            <button className="schedule-btn schedule-btn-cancel" onClick={onClose} type="button">
              {t('common.cancel')}
            </button>
            <button
              className="schedule-btn schedule-btn-save"
              onClick={handleSave}
              disabled={saving || !!graphWorkflowsError || graphWorkflowsLoading || graphWorkflows.length === 0}
              type="button"
            >
              {saving ? t('common.saving') : t('common.save')}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
