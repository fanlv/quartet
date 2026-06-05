import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { LoopConfig, LoopTemplate, ScheduleInfo } from '../types';
import { AgentInfo } from './ChatPage';
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
  onSave: () => void;
  onClose: () => void;
}

async function fetchTemplates(): Promise<LoopTemplate[]> {
  const res = await fetch('/api/v1/template/list');
  if (!res.ok) return [];
  const data = await res.json();
  return data.templates || [];
}

export function ScheduleEditModal({ schedule, workspaceId, workspaces, agents: _agents, defaultAgentIndex: _defaultAgentIndex, onSave, onClose }: ScheduleEditModalProps) {
  const { t } = useTranslation();
  const isEdit = !!schedule;
  const overlayRef = useRef<HTMLDivElement>(null);

  const [name, setName] = useState(schedule?.name || '');
  const [cronExpr, setCronExpr] = useState(schedule?.cronExpr || '0 9 * * *');
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true);
  const [templateId, setTemplateId] = useState(schedule?.templateId || '');
  const [loopConfig, setLoopConfig] = useState<LoopConfig | null>(schedule?.loopConfig || null);
  const [maxConcurrent, setMaxConcurrent] = useState(schedule?.maxConcurrent || 1);
  const [timeoutMins, setTimeoutMins] = useState(schedule?.timeout || 0);
  // Create-flow only: let the user opt in to binding to the current
  // workspace. Defaults to bind OFF — new tasks are unbound and run under the
  // default workspace (ws-1) at trigger time. Toggle on to send the current
  // workspaceId and pin the task to this workspace.
  const [bindToCurrentWs, setBindToCurrentWs] = useState(false);

  const [templates, setTemplates] = useState<LoopTemplate[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [viewportHeight, setViewportHeight] = useState(0);
  const [viewportOffsetTop, setViewportOffsetTop] = useState(0);

  useEffect(() => {
    fetchTemplates().then(setTemplates);
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

  const handleTemplateSelect = useCallback((tid: string) => {
    setTemplateId(tid);
    const tmpl = templates.find(t => t.id === tid);
    if (tmpl) {
      setLoopConfig(tmpl.config);
      setName(prev => prev || tmpl.name);
    }
  }, [templates]);

  // Edit-view only: resolve the schedule's bound workspaceId into a readable
  // name (+ workdir). An empty workspaceId means the task is unbound and runs
  // in the default workspace at trigger time.
  const boundWorkspace = useMemo(() => {
    if (!isEdit) return null;
    const wsId = schedule?.workspaceId;
    if (!wsId) return { name: t('schedule.boundWorkspaceDefault'), workdir: '' };
    const match = workspaces?.find(w => w.id === wsId);
    return {
      name: match?.title || wsId,
      workdir: match?.workdir || schedule?.workdir || '',
    };
  }, [isEdit, schedule?.workspaceId, schedule?.workdir, workspaces, t]);

  const handleSave = async () => {
    if (!name.trim()) {
      setError(t('schedule.taskNameRequired'));
      return;
    }
    if (!loopConfig) {
      setError(t('schedule.selectTemplateRequired'));
      return;
    }
    setSaving(true);
    setError('');

    try {
      if (isEdit && schedule) {
        const res = await fetch(`/api/v1/schedule/${schedule.id}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            name,
            cronExpr,
            enabled,
            templateId,
            loopConfig,
            maxConcurrent,
            timeout: timeoutMins,
          }),
        });
        if (!res.ok) {
          const data = await res.json().catch(() => ({}));
          throw new Error(data.msg || 'Failed to update');
        }
      } else {
        const body: Record<string, unknown> = {
          name,
          cronExpr,
          enabled,
          templateId,
          loopConfig,
          maxConcurrent,
          timeout: timeoutMins,
        };
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
          const data = await res.json().catch(() => ({}));
          throw new Error(data.msg || 'Failed to create');
        }
      }
      onSave();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Save failed');
    } finally {
      setSaving(false);
    }
  };

  const handleTriggerNow = async () => {
    if (!schedule) return;
    try {
      const res = await fetch(`/api/v1/schedule/${schedule.id}/run`, { method: 'POST' });
      if (res.ok) {
        onSave();
      } else {
        const data = await res.json().catch(() => ({}));
        setError(data.msg || 'Trigger failed');
      }
    } catch { /* ignore */ }
  };

  return (
    <div className="schedule-modal-overlay" onClick={onClose} ref={overlayRef} style={overlayStyle}>
      <div className="schedule-modal" onClick={e => e.stopPropagation()} style={modalStyle}>
        <div className="schedule-modal-header">
          <h2>{isEdit ? t('schedule.editTitle') : t('schedule.createTitle')}</h2>
          <button className="schedule-modal-close" onClick={onClose}>×</button>
        </div>

        <div className="schedule-modal-body">
          {/* Template selection */}
          <div className="schedule-field schedule-field-full">
            <label>{t('schedule.selectTemplate')}</label>
            {templates.length === 0 ? (
              <div className="schedule-no-templates">{t('schedule.noTemplates')}</div>
            ) : (
              <select
                value={templateId}
                onChange={e => handleTemplateSelect(e.target.value)}
                className="schedule-select"
              >
                <option value="">{t('schedule.selectTemplatePlaceholder')}</option>
                {(() => {
                  const scheduled = templates.filter(t2 => (t2.scheduleCount ?? 0) > 0);
                  const other = templates.filter(t2 => (t2.scheduleCount ?? 0) === 0);
                  return (
                    <>
                      {scheduled.length > 0 && (
                        <optgroup label={t('loop.template.categoryScheduled')}>
                          {scheduled.map(t2 => (
                            <option key={t2.id} value={t2.id}>
                              {t2.name}  ⏰ × {t2.scheduleCount ?? 0}
                            </option>
                          ))}
                        </optgroup>
                      )}
                      {other.length > 0 && (
                        <optgroup label={t('loop.template.categoryOther')}>
                          {other.map(t2 => (
                            <option key={t2.id} value={t2.id}>{t2.name}</option>
                          ))}
                        </optgroup>
                      )}
                    </>
                  );
                })()}
              </select>
            )}
            {loopConfig && (
              <div className="schedule-config-preview">
                {t('schedule.configPreview', { nodes: loopConfig.flow?.length || 0 })}
                {loopConfig.variables && Object.keys(loopConfig.variables).length > 0 && (
                  <span>{t('schedule.configPreviewVars', { vars: Object.keys(loopConfig.variables).length })}</span>
                )}
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
              <div className="schedule-ws-bound">
                <span
                  className="schedule-ws-bound-dot"
                  style={{ backgroundColor: workspaceColor(schedule?.workspaceId || DEFAULT_WORKSPACE_ID) }}
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
            <button className="schedule-btn schedule-btn-trigger" onClick={handleTriggerNow} type="button">
              {t('schedule.triggerNow')}
            </button>
          )}
          <div className="schedule-modal-footer-right">
            <button className="schedule-btn schedule-btn-cancel" onClick={onClose} type="button">
              {t('common.cancel')}
            </button>
            <button
              className="schedule-btn schedule-btn-save"
              onClick={handleSave}
              disabled={saving}
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
