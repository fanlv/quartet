import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { ScheduleInfo } from '../types';
import { timerFetch } from '../utils/timers';
import { ScheduleEditModal, type TimerOptions } from './ScheduleEditModal';
import { TimerDialog } from './TimerDialog';
import { TimerConfiguration, TimerIcon } from './TimerPresentation';
import {
  TimerExecutionDetail, TimerExecutionRow, TimerPlanRow, TimerRecords, TimerSessionRow,
  type TimerExecution, type TimerSession,
} from './TimerRecords';
import './TimerManagement.css';

type View = 'plans' | 'executions' | 'sessions';
const views = ['plans', 'executions', 'sessions'] as const;
const viewIcons = { plans: 'clock', executions: 'history', sessions: 'sessions' } as const;
const statusOptions = ['accepted', 'dispatching', 'pending', 'running', 'awaitingInput', 'recovering', 'completed', 'failed', 'stopped', 'timedOut', 'interrupted', 'skipped', 'missed'];
const initialOptions: TimerOptions = { workspaces: [], workflows: [], agents: [] };
const base = '/api/v1/schedule';

function locationFilters(canReadJobs: boolean) {
  const query = new URLSearchParams(window.location.search);
  const tab = query.get('timerTab');
  const view: View = tab === 'executions' || (tab === 'sessions' && canReadJobs) ? tab : 'plans';
  return { view, scheduleId: query.get('timerScheduleId') ?? '', executionId: query.get('timerExecutionId') ?? '' };
}

function jobLink(jobId: string, scheduleId: string, executionId: string, workspaceId?: string, sessionId?: string) {
  const query = new URLSearchParams({ jobId, timerScheduleId: scheduleId, timerExecutionId: executionId, timerTab: 'executions' });
  if (workspaceId) query.set('workspaceId', workspaceId);
  if (sessionId) query.set('sessionId', sessionId);
  return `/?${query}`;
}

function TimerFilter({ label, allLabel, value, options, onChange }: {
  label: string;
  allLabel: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  const selected = options.find(option => option.value === value);
  const displayValue = value ? selected?.label || value : allLabel;

  return (
    <label className="timer-filter" data-active={value ? true : undefined} title={value ? displayValue : undefined}>
      {value && <span className="timer-filter-label" aria-hidden="true">{label}</span>}
      <span className="timer-filter-value" aria-hidden="true">{displayValue}</span>
      <span className="dropdown-chevron" aria-hidden="true" />
      <select aria-label={label} value={value} onChange={event => onChange(event.target.value)}>
        <option value="">{allLabel}</option>
        {value && !selected && <option value={value}>{value}</option>}
        {options.map(option => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    </label>
  );
}

export default function TimerManagement({ permissions }: { permissions: string[] }) {
  const { t } = useTranslation();
  const canWrite = permissions.includes('schedule.write');
  const canReadJobs = permissions.includes('job.read');
  const canReadWorkspaces = permissions.includes('workspace.read');
  const canReadWorkflows = permissions.includes('workflow.read');
  const canReadAgents = permissions.includes('agent.read');
  const [{ view, scheduleId, executionId }, setFilters] = useState(() => locationFilters(canReadJobs));
  const [workspace, setWorkspace] = useState('');
  const [status, setStatus] = useState('');
  const [timers, setTimers] = useState<ScheduleInfo[]>([]);
  const [executions, setExecutions] = useState<TimerExecution[]>([]);
  const [sessions, setSessions] = useState<TimerSession[]>([]);
  const [options, setOptions] = useState<TimerOptions>(initialOptions);
  const [editor, setEditor] = useState<ScheduleInfo | null | undefined>();
  const [selection, setSelection] = useState('');
  const [confirm, setConfirm] = useState<{ timer: ScheduleInfo; action: 'enable' | 'delete' }>();
  const [actionError, setActionError] = useState('');
  const [planError, setPlanError] = useState('');
  const [historyError, setHistoryError] = useState('');
  const [optionsError, setOptionsError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [planLoading, setPlanLoading] = useState(true);
  const [historyLoading, setHistoryLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const [cursor, setCursor] = useState('');
  const [nextCursor, setNextCursor] = useState('');
  const intents = useRef(new Map<string, string>());
  const scrollRef = useRef<HTMLDivElement>(null);
  const refresh = () => { setCursor(''); setRevision(value => value + 1); };

  const navigate = (tab: View, plan = '', execution = '') => {
    const url = new URL(window.location.href);
    url.searchParams.set('timerTab', tab);
    for (const [key, value] of [['timerScheduleId', plan], ['timerExecutionId', execution]]) {
      if (value) url.searchParams.set(key, value);
      else url.searchParams.delete(key);
    }
    window.history.replaceState({}, '', url);
    setCursor('');
    setFilters({ view: tab, scheduleId: plan, executionId: execution });
  };

  useEffect(() => {
    const scrollArea = scrollRef.current;
    scrollArea?.scrollTo({ top: 0 });
    if (view === 'executions' && executionId) scrollArea?.focus({ preventScroll: true });
  }, [view, scheduleId, executionId]);

  useEffect(() => {
    const controller = new AbortController();
    const fetchOptions = async () => {
      const results = await Promise.allSettled([
        canReadWorkspaces ? timerFetch<{ workspaces: TimerOptions['workspaces'] }>('/api/v1/workspace/list', 'GET', undefined, controller.signal) : Promise.resolve({ workspaces: [] }),
        canReadWorkflows ? timerFetch<{ workflows: TimerOptions['workflows']; warnings?: { file: string; error: string }[] }>('/api/v1/graph/workflow/list', 'GET', undefined, controller.signal) : Promise.resolve({ workflows: [], warnings: [] }),
        canReadAgents ? timerFetch<{ agent_list: TimerOptions['agents'] }>('/api/v1/agent/list?cachedOnly=true', 'GET', undefined, controller.signal) : Promise.resolve({ agent_list: [] }),
      ] as const);
      if (controller.signal.aborted) return;
      const [workspaces, workflows, agents] = results;
      setOptions({
        workspaces: workspaces.status === 'fulfilled' ? workspaces.value.workspaces ?? [] : [],
        workflows: workflows.status === 'fulfilled' ? workflows.value.workflows ?? [] : [],
        agents: agents.status === 'fulfilled' ? agents.value.agent_list ?? [] : [],
      });
      const warnings = workflows.status === 'fulfilled' ? workflows.value.warnings ?? [] : [];
      setOptionsError([
        ...results.flatMap(result => result.status === 'rejected' ? [String(result.reason)] : []),
        ...warnings.map(item => item.file + ': ' + item.error),
      ].join('\n'));
    };
    void fetchOptions();
    return () => controller.abort();
  }, [canReadAgents, canReadWorkflows, canReadWorkspaces]);

  useEffect(() => {
    if (view === 'plans' || (view === 'sessions' && !canReadJobs)) return;
    const controller = new AbortController();
    const query = new URLSearchParams();
    if (scheduleId) query.set('scheduleId', scheduleId);
    if (cursor) query.set('cursor', cursor);
    if (executionId) query.set('executionId', executionId);
    if (view === 'executions' && !executionId) {
      if (workspace) query.set('workspaceId', workspace);
      if (status) query.set('status', status);
    }
    const load = async () => {
      try {
        if (view === 'executions') {
          const data = await timerFetch<{ executions: TimerExecution[]; nextCursor: string }>(`${base}/executions?${query}`, 'GET', undefined, controller.signal);
          if (controller.signal.aborted) return;
          setExecutions(previous => cursor ? [...previous, ...data.executions.filter(item => !previous.some(row => row.id === item.id))] : data.executions);
          setNextCursor(data.nextCursor);
        } else {
          const data = await timerFetch<{ sessions: TimerSession[]; nextCursor: string }>(`${base}/sessions?${query}`, 'GET', undefined, controller.signal);
          if (controller.signal.aborted) return;
          setSessions(previous => cursor ? [...previous, ...data.sessions.filter(item => !previous.some(row => row.id === item.id))] : data.sessions);
          setNextCursor(data.nextCursor);
        }
        setHistoryError('');
      } catch (err) {
        if (!controller.signal.aborted) setHistoryError(String(err));
      } finally {
        if (!controller.signal.aborted) setHistoryLoading(false);
      }
    };
    setHistoryLoading(true);
    setHistoryError('');
    setNextCursor('');
    void load();
    const poll = !cursor ? window.setInterval(() => { if (!document.hidden) void load(); }, 4000) : undefined;
    return () => { controller.abort(); window.clearInterval(poll); };
  }, [view, workspace, status, scheduleId, executionId, cursor, revision, canReadJobs]);

  useEffect(() => {
    const controller = new AbortController();
    const load = async () => {
      try {
        const data = await timerFetch<{ schedules: ScheduleInfo[] }>(`${base}/list`, 'GET', undefined, controller.signal);
        if (controller.signal.aborted) return;
        setTimers(data.schedules);
        setPlanError('');
      } catch (err) {
        if (!controller.signal.aborted) setPlanError(String(err));
      } finally {
        if (!controller.signal.aborted) setPlanLoading(false);
      }
    };
    void load();
    const poll = window.setInterval(() => { if (!document.hidden) void load(); }, 8000);
    return () => { controller.abort(); window.clearInterval(poll); };
  }, [revision]);

  const doAction = async (timer: ScheduleInfo, action: 'enable' | 'disable' | 'delete' | 'run') => {
    if (busy) return;
    setBusy(true);
    setActionError('');
    setNotice('');
    const path = `${base}/${timer.id}`;
    try {
      if (action === 'run') {
        const clientRequestId = intents.current.get(timer.id) ?? crypto.randomUUID();
        intents.current.set(timer.id, clientRequestId);
        const run = await timerFetch<TimerExecution>(`${path}/run`, 'POST', { clientRequestId });
        intents.current.delete(timer.id);
        navigate('executions', timer.id, run.id);
      } else if (action === 'delete') {
        await timerFetch(path, 'DELETE');
        setSelection('');
      } else {
        await timerFetch(path, 'PUT', { enabled: action === 'enable', expectedApproval: action === 'enable' ? timer.approvalPreview : undefined });
      }
      setConfirm(undefined);
      setNotice(t(`timers.actionNotice.${action}`, { name: timer.name }));
      refresh();
    } catch (err) {
      setActionError(String(err));
      if (action === 'enable') setConfirm(undefined);
    } finally {
      setBusy(false);
    }
  };
  const workspaceName = (id?: string) => options.workspaces.find(item => item.id === id)?.title || id;
  const workflowName = (id?: string) => options.workflows.find(item => item.id === id)?.name || id;
  const isExecutionDetail = view === 'executions' && Boolean(executionId);
  const showHistoryList = view !== 'plans' && !isExecutionDetail;
  const record = isExecutionDetail ? executions.find(item => item.id === executionId && (!scheduleId || item.scheduleId === scheduleId)) : undefined;
  const visibleExecutions = executions.filter(item => (
    (!scheduleId || item.scheduleId === scheduleId) && (!workspace || item.workspaceId === workspace) && (!status || item.status === status)
  ));
  const visibleSessions = canReadJobs ? sessions.filter(item => (
    (!scheduleId || item.scheduleId === scheduleId) && (!executionId || item.executionId === executionId)
  )) : [];
  const hasFilters = Boolean(scheduleId || executionId || (view === 'executions' && (workspace || status)));
  const loading = view === 'plans' ? planLoading : historyLoading && !cursor && !record;
  const hasRecords = view === 'plans' ? timers.length > 0
    : isExecutionDetail ? Boolean(record)
    : view === 'executions' ? visibleExecutions.length > 0 : visibleSessions.length > 0;
  const showRows = !loading && hasRecords;
  const emptyMessageKey = isExecutionDetail ? 'timers.executionMissing'
    : showHistoryList && hasFilters ? 'timers.filters.noMatches' : `timers.emptyState.${view}`;
  const recordState = loading ? (
    <div className="timer-state" role="status">
      <span className="timer-spinner" aria-hidden="true" />
      {t('common.loading')}
    </div>
  ) : !hasRecords && !(view === 'plans' ? planError : historyError) && (
    <div className="timer-state">
      <span className="timer-state-icon"><TimerIcon name={viewIcons[view]} /></span>
      <p>{t(emptyMessageKey)}</p>
    </div>
  );
  const refreshButton = (
    <button type="button" className="timer-button timer-refresh" onClick={refresh} disabled={historyLoading} aria-label={t('timers.refresh')} title={t('timers.refresh')}>
      <TimerIcon name="refresh" /><span>{t('timers.refresh')}</span>
    </button>
  );

  return (
    <main className="timer-page">
      <div className="timer-toolbar timer-shell">
        <nav className="timer-tabs" aria-label={t('timers.title')}>
          {views.filter(tab => tab !== 'sessions' || canReadJobs).map(tab => (
            <button className="timer-tab" type="button" key={tab} aria-current={view === tab ? 'page' : undefined} onClick={() => navigate(tab)}>
              <TimerIcon name={viewIcons[tab]} />{t(`timers.${tab}`)}
            </button>
          ))}
        </nav>
        {canWrite && view === 'plans' && (
          <button type="button" className="timer-button timer-button-primary timer-add" onClick={() => setEditor(null)}>
            <TimerIcon name="plus" />{t('timers.add')}
          </button>
        )}
      </div>

      <div className="timer-scroll" ref={scrollRef} tabIndex={-1}>
        <div className="timer-content timer-shell">
          {isExecutionDetail && (
            <div className="timer-detail-nav">
              <button type="button" className="timer-button timer-button-quiet" onClick={() => navigate('executions', scheduleId)}>
                <TimerIcon name="back" />{t('timers.backToExecutions')}
              </button>
              {refreshButton}
            </div>
          )}
          {notice && <p role="status" className="timer-feedback timer-feedback-success"><TimerIcon name="check" />{notice}</p>}
          {actionError && !confirm && <pre className="timer-feedback timer-feedback-error" role="alert">{actionError}</pre>}
          {planError && <pre className="timer-feedback timer-feedback-error" role="alert">{planError}</pre>}
          {historyError && view !== 'plans' && <pre className="timer-feedback timer-feedback-error" role="alert">{historyError}</pre>}
          {optionsError && editor === undefined && <pre className="timer-feedback timer-feedback-error" role="alert" data-testid="schedule-graph-workflow-warnings">{optionsError}</pre>}

          {!showHistoryList && recordState}

          {showRows && view === 'plans' && (
            <TimerRecords view="plans">
              {timers.map(timer => {
                const kind = timer.kind || 'graph';
                const canExecute = permissions.includes(kind === 'prompt' ? 'job.execute' : 'workflow.execute');
                const canRun = canExecute && permissions.includes('schedule.execute');
                const expanded = selection === timer.id;

                return (
                  <TimerPlanRow
                    key={timer.id}
                    timer={timer}
                    workspaceName={workspaceName(timer.workspaceId)}
                    workflowName={workflowName(timer.graphWorkflowId)}
                    expanded={expanded}
                    onToggle={() => setSelection(expanded ? '' : timer.id)}
                    actions={
                      <>
                        <div className="timer-actions">
                          <button className="timer-button" type="button" onClick={() => navigate('executions', timer.id)}>{t('timers.executions')}</button>
                          {canReadJobs && (
                            <button className="timer-button" type="button" onClick={() => navigate('sessions', timer.id)}>{t('timers.sessions')}</button>
                          )}
                        </div>
                        {(canWrite || canRun) && (
                          <div className="timer-actions">
                            {canWrite && (
                              <>
                                <button className="timer-button timer-button-quiet" type="button" onClick={() => setEditor(timer)}>{t('timers.edit')}</button>
                                {timer.enabled ? (
                                  <button className="timer-button timer-button-quiet" type="button" disabled={busy} onClick={() => void doAction(timer, 'disable')}>{t('timers.disable')}</button>
                                ) : (
                                  <button className="timer-button timer-button-quiet" type="button" disabled={busy || !canExecute || !timer.approvalPreview} onClick={() => {
                                    setActionError('');
                                    setConfirm({ timer, action: 'enable' });
                                  }}>{t('timers.enable')}</button>
                                )}
                                <button className="timer-button timer-button-danger" type="button" disabled={busy} onClick={() => {
                                  setActionError('');
                                  setConfirm({ timer, action: 'delete' });
                                }}>{t('timers.delete')}</button>
                              </>
                            )}
                            {canRun && (
                              <button className="timer-button timer-button-primary" type="button" disabled={busy || timer.planState === 'ended' || timer.planState === 'pendingApproval'} onClick={() => void doAction(timer, 'run')}>
                                <TimerIcon name="play" />{t('timers.run')}
                              </button>
                            )}
                          </div>
                        )}
                      </>
                    }
                  />
                );
              })}
            </TimerRecords>
          )}

          {showHistoryList && (
            <TimerRecords
              view={view}
              showColumns={showRows}
              toolbar={
                <div className="timer-list-toolbar">
                  <div className="timer-list-filters" role="group" aria-label={t('timers.filters.label')}>
                    <TimerFilter
                      label={t('timers.plans')}
                      allLabel={t('timers.filters.allTimers')}
                      value={scheduleId}
                      options={timers.map(timer => ({ value: timer.id, label: timer.name }))}
                      onChange={value => navigate(view, value)}
                    />
                    {view === 'executions' && canReadWorkspaces && (
                      <TimerFilter
                        label={t('timers.workspace')}
                        allLabel={t('timers.filters.allWorkspaces')}
                        value={workspace}
                        options={options.workspaces.map(item => ({ value: item.id, label: item.title || item.id }))}
                        onChange={value => { setCursor(''); setWorkspace(value); }}
                      />
                    )}
                    {view === 'executions' && (
                      <TimerFilter
                        label={t('timers.state')}
                        allLabel={t('timers.filters.allStatuses')}
                        value={status}
                        options={statusOptions.map(value => ({ value, label: t(`timers.status.${value}`, { defaultValue: value }) }))}
                        onChange={value => { setCursor(''); setStatus(value); }}
                      />
                    )}
                    {hasFilters && (
                      <button type="button" className="timer-button timer-button-quiet" onClick={() => {
                        if (view === 'executions') {
                          setWorkspace('');
                          setStatus('');
                        }
                        navigate(view);
                      }}>{t('timers.resetFilters')}</button>
                    )}
                  </div>
                  <div className="timer-list-actions">
                    {view === 'sessions' && executionId && (
                      <button type="button" className="timer-button timer-button-quiet" onClick={() => navigate('executions', scheduleId, executionId)}>
                        <TimerIcon name="back" />{t('timers.backToExecution')}
                      </button>
                    )}
                    {refreshButton}
                  </div>
                </div>
              }
            >
              {!showRows && recordState}
              {showRows && view === 'executions' && visibleExecutions.map(item => (
                <TimerExecutionRow key={item.id} record={item} workspaceName={workspaceName(item.workspaceId)} onOpen={() => navigate('executions', scheduleId, item.id)} />
              ))}
              {showRows && view === 'sessions' && visibleSessions.map(session => (
                <TimerSessionRow
                  key={session.id}
                  session={session}
                  href={jobLink(session.jobId, session.scheduleId, session.executionId, session.workspaceId, session.id)}
                  onOpenExecution={() => navigate('executions', session.scheduleId, session.executionId)}
                />
              ))}
            </TimerRecords>
          )}
          {record && (
            <TimerExecutionDetail
              record={record}
              workspaceName={workspaceName(record.workspaceId || record.configuration?.workspaceId)}
              workflowName={workflowName(record.workflowId || record.configuration?.graphWorkflowId)}
              actions={
                <div className="timer-actions">
                  {scheduleId !== record.scheduleId && (
                    <button className="timer-button timer-button-quiet" type="button" onClick={() => navigate('executions', record.scheduleId)}>{t('timers.filterTimer')}</button>
                  )}
                  {canReadJobs && record.jobId && !record.detailsError && (
                    <>
                      <button className="timer-button" type="button" onClick={() => navigate('sessions', record.scheduleId, record.id)}>
                        <TimerIcon name="sessions" />{t('timers.sessions')}
                      </button>
                      <a className="timer-button timer-button-primary" href={jobLink(record.jobId, record.scheduleId, record.id, record.workspaceId)}>
                        {t('timers.openJob')}<TimerIcon name="open" />
                      </a>
                    </>
                  )}
                </div>
              }
            />
          )}

          {showHistoryList && (nextCursor || (historyLoading && cursor)) && (
            <div className="timer-pagination">
              <button type="button" className="timer-button" disabled={historyLoading} onClick={() => setCursor(nextCursor)}>
                {t(historyLoading ? 'common.loading' : 'timers.more')}
              </button>
            </div>
          )}
        </div>
      </div>

      {editor !== undefined && (
        <ScheduleEditModal schedule={editor} options={options} optionsError={optionsError} permissions={permissions}
          onClose={() => setEditor(undefined)} onSave={saved => {
            setEditor(undefined);
            setSelection(saved.id);
            setNotice(t('timers.saved', { name: saved.name, id: saved.id }));
            refresh();
          }}
        />
      )}
      {confirm && (
        <TimerDialog
          title={t(`timers.confirmAction.${confirm.action}`, { name: confirm.timer.name })}
          busy={busy}
          compact={confirm.action === 'delete'}
          onClose={() => setConfirm(undefined)}
          onSubmit={event => { event.preventDefault(); void doAction(confirm.timer, confirm.action); }}
          actions={
            <>
              <button type="button" className="timer-button" disabled={busy} onClick={() => setConfirm(undefined)}>{t('common.cancel')}</button>
              <button
                className={`timer-button ${confirm.action === 'delete' ? 'timer-button-destructive' : 'timer-button-primary'}`}
                type="submit"
                disabled={busy}
              >{t(`timers.${confirm.action}`)}</button>
            </>
          }
        >
          {actionError && <pre className="timer-feedback timer-feedback-error" role="alert">{actionError}</pre>}
          {confirm.action === 'enable' ? (
            <TimerConfiguration timer={confirm.timer} workspaceName={workspaceName(confirm.timer.workspaceId)} workflowName={workflowName(confirm.timer.graphWorkflowId)} />
          ) : <p className="timer-dialog-message">{t('timers.deleteDescription')}</p>}
        </TimerDialog>
      )}
    </main>
  );
}
