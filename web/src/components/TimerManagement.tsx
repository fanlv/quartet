import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { ScheduleInfo } from '../types';
import { cronToHuman } from './CronInput';
import { ScheduleEditModal, type TimerOptions } from './ScheduleEditModal';
import { timerDate, timerFetch } from '../utils/timers';
import './ScheduleEditModal.css';

type View = 'plans' | 'executions' | 'sessions';
interface Execution {
  id: string;
  scheduleId: string;
  name: string;
  kind: string;
  workflowId?: string;
  workspaceId?: string;
  trigger: string;
  status: string;
  jobId?: string;
  error?: string;
  detailsError?: string;
  triggeredAt?: string;
  scheduledAt?: string;
  missedThrough?: string;
  startedAt?: string;
  finishedAt?: string;
  configuration?: ScheduleInfo;
}
interface Session {
  id: string;
  scheduleId: string;
  scheduleName: string;
  executionId: string;
  jobId: string;
  workspaceId: string;
  title: string;
  agentId?: string;
  modelId?: string;
  nodeId?: string;
  nodeName?: string;
  loop?: { loopNodeId: string; index: number }[];
  createdAt: string;
}
const statusOptions = ['accepted', 'pending', 'running', 'awaitingInput', 'recovering', 'completed', 'failed', 'stopped', 'timedOut', 'interrupted', 'skipped', 'missed'];
const initialOptions: TimerOptions = { workspaces: [], workflows: [], agents: [] };
const base = '/api/v1/schedule';

function locationFilters() {
  const query = new URLSearchParams(window.location.search);
  const tab = query.get('timerTab');
  const view: View = tab === 'sessions' || tab === 'executions' ? tab : 'plans';
  return { view, scheduleId: query.get('timerScheduleId') ?? '', executionId: query.get('timerExecutionId') ?? '' };
}

function jobLink(jobId: string, scheduleId: string, executionId: string, workspaceId?: string, sessionId?: string) {
  const query = new URLSearchParams({ jobId, timerScheduleId: scheduleId, timerExecutionId: executionId, timerTab: 'executions' });
  if (workspaceId) query.set('workspaceId', workspaceId);
  if (sessionId) query.set('sessionId', sessionId);
  return `/?${query}`;
}

export default function TimerManagement({ permissions }: { permissions: string[] }) {
  const { t } = useTranslation();
  const [{ view, scheduleId, executionId }, setFilters] = useState(locationFilters);
  const [workspace, setWorkspace] = useState('');
  const [status, setStatus] = useState('');
  const [timers, setTimers] = useState<ScheduleInfo[]>([]);
  const [executions, setExecutions] = useState<Execution[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [options, setOptions] = useState<TimerOptions>(initialOptions);
  const [editor, setEditor] = useState<ScheduleInfo | null | undefined>();
  const [selection, setSelection] = useState('');
  const [confirm, setConfirm] = useState<{ timer: ScheduleInfo; action: 'enable' | 'delete' }>();
  const [error, setError] = useState('');
  const [optionsError, setOptionsError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const [cursor, setCursor] = useState('');
  const [nextCursor, setNextCursor] = useState('');
  const intents = useRef(new Map<string, string>());
  const canWrite = permissions.includes('schedule.write');
  const canReadJobs = permissions.includes('job.read');
  const canReadWorkspaces = permissions.includes('workspace.read');
  const canReadWorkflows = permissions.includes('workflow.read');
  const canReadAgents = permissions.includes('agent.read');
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
    const controller = new AbortController();
    const load = async () => {
      const query = new URLSearchParams();
      if (scheduleId) query.set('scheduleId', scheduleId);
      if (cursor) query.set('cursor', cursor);
      if (view === 'executions' && workspace) query.set('workspaceId', workspace);
      if (view === 'executions' && status) query.set('status', status);
      if (view !== 'plans' && executionId) query.set('executionId', executionId);
      try {
        if (view === 'executions') {
          const data = await timerFetch<{ executions: Execution[]; nextCursor: string }>(`${base}/executions?${query}`, 'GET', undefined, controller.signal);
          if (controller.signal.aborted) return;
          setExecutions(previous => cursor ? [...previous, ...data.executions.filter(item => !previous.some(row => row.id === item.id))] : data.executions);
          setNextCursor(data.nextCursor);
        } else if (view === 'sessions' && canReadJobs) {
          const data = await timerFetch<{ sessions: Session[]; nextCursor: string }>(`${base}/sessions?${query}`, 'GET', undefined, controller.signal);
          if (controller.signal.aborted) return;
          setSessions(previous => cursor ? [...previous, ...data.sessions.filter(item => !previous.some(row => row.id === item.id))] : data.sessions);
          setNextCursor(data.nextCursor);
        }
      } catch (err) {
        if (!controller.signal.aborted) setError(String(err));
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    };
    setLoading(true);
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
        if (!controller.signal.aborted) setTimers(data.schedules);
      } catch (err) {
        if (!controller.signal.aborted) setError(String(err));
      }
    };
    void load();
    const poll = window.setInterval(() => { if (!document.hidden) void load(); }, 8000);
    return () => { controller.abort(); window.clearInterval(poll); };
  }, [revision]);

  const doAction = async (timer: ScheduleInfo, action: 'enable' | 'disable' | 'delete' | 'run') => {
    if (busy) return;
    setBusy(true);
    setError('');
    setNotice('');
    const path = `${base}/${timer.id}`;
    try {
      if (action === 'run') {
        const clientRequestId = intents.current.get(timer.id) ?? crypto.randomUUID();
        intents.current.set(timer.id, clientRequestId);
        const run = await timerFetch<Execution>(`${path}/run`, 'POST', { clientRequestId });
        intents.current.delete(timer.id);
        navigate('executions', timer.id, run.id);
      } else if (action === 'delete') {
        await timerFetch(path, 'DELETE');
        setSelection('');
      } else {
        await timerFetch(path, 'PUT', { enabled: action === 'enable', expectedApproval: action === 'enable' ? timer.approvalPreview : undefined });
      }
      setConfirm(undefined);
      setNotice(t('timers.saved', { name: timer.name, id: timer.id }));
      refresh();
    } catch (err) {
      setError(String(err));
      if (action === 'enable') setConfirm(undefined);
    } finally {
      setBusy(false);
    }
  };
  const showPlan = (timer: ScheduleInfo) => timer.runAt ? timerDate(timer.runAt, timer.timeZone) : cronToHuman(timer.cronExpr, t);
  const workspaceName = (id?: string) => options.workspaces.find(item => item.id === id)?.title || id;
  const workflowName = (id?: string) => options.workflows.find(item => item.id === id)?.name || id;
  const showStatus = (value?: string) => value ? t(`timers.status.${value}`, { defaultValue: value }) : '—';
  const filterTimers = timers.some(item => item.id === scheduleId) || !scheduleId
    ? timers : [...timers, { id: scheduleId, name: scheduleId }];
  const visibleExecutions = executions.filter(record => (!scheduleId || record.scheduleId === scheduleId) && (!workspace || record.workspaceId === workspace) && (!status || record.status === status));

  return (
    <main className="timer-page">
      <div className="timer-toolbar">
        <nav aria-label={t('timers.title')}>
          {(['plans', 'executions', ...(canReadJobs ? ['sessions'] : [])] as View[]).map(tab => (
            <button type="button" key={tab} aria-current={view === tab ? 'page' : undefined} onClick={() => navigate(tab)}>{t(`timers.${tab}`)}</button>
          ))}
        </nav>
        {canWrite && view === 'plans' && <button type="button" className="timer-primary" onClick={() => setEditor(null)}>{t('timers.add')}</button>}
      </div>
      {notice && <p role="status" className="timer-notice">{notice}</p>}
      {error && <pre className="timer-error" role="alert">{error}</pre>}
      {optionsError && <pre className="timer-error" role="alert" data-testid="schedule-graph-workflow-warnings">{optionsError}</pre>}
      {view !== 'plans' && <div className="timer-filters">
        <label>{t('timers.plans')}<select value={scheduleId} onChange={event => navigate(view, event.target.value)}>
          <option value="">{t('timers.all')}</option>
          {filterTimers.map(timer => <option key={timer.id} value={timer.id}>{timer.name}</option>)}
        </select></label>
        {view === 'executions' && <>
          <label>{t('timers.workspace')}<select value={workspace} onChange={event => { setCursor(''); setWorkspace(event.target.value); }}>
            <option value="">{t('timers.all')}</option>{options.workspaces.map(item => <option key={item.id} value={item.id}>{item.title || item.id}</option>)}
          </select></label>
          <label>{t('timers.state')}<select value={status} onChange={event => { setCursor(''); setStatus(event.target.value); }}>
            <option value="">{t('timers.all')}</option>{statusOptions.map(value => <option key={value} value={value}>{showStatus(value)}</option>)}
          </select></label>
        </>}
        {view === 'sessions' && executionId && <button type="button" onClick={() => navigate('executions', scheduleId, executionId)}>{t('timers.backToExecution')}</button>}
        <button type="button" onClick={refresh}>{t('timers.refresh')}</button>
      </div>}
      <div className="timer-records">
        {view === 'plans' && timers.map(timer => {
          const kind = timer.kind || 'graph';
          const canExecute = permissions.includes(kind === 'prompt' ? 'job.execute' : 'workflow.execute');
          const expanded = selection === timer.id;
          return <article key={timer.id}>
            <button type="button" className="timer-entry" aria-expanded={expanded} onClick={() => setSelection(expanded ? '' : timer.id)}>
              <strong>{timer.name}</strong>
              <span>{t(`timers.${kind}`)} · {showPlan(timer)}</span>
              <span>{showStatus(timer.planState)}{timer.nextRunAt ? ` · ${timerDate(timer.nextRunAt, timer.timeZone)}` : ''}</span>
              <span>{showStatus(timer.lastStatus)}</span>
            </button>
            {expanded && <section className="timer-entry-body">
              <dl className="timer-details">
                <dt>{t('timers.identifier')}</dt><dd>{timer.id}</dd>
                <dt>{t('timers.target')}</dt><dd>{kind === 'prompt' ? timer.prompt : `${workflowName(timer.graphWorkflowId) || ''} ${timer.workflowVersion || ''}`}</dd>
                <dt>{t('timers.workspace')}</dt><dd>{workspaceName(timer.workspaceId)} · {timer.workdir}</dd>
                {kind === 'prompt' && <><dt>{t('timers.agent')}</dt><dd>{timer.agentId} · {timer.modelId}</dd></>}
                <dt>{t('timers.zone')}</dt><dd>{timer.timeZone}</dd>
                <dt>{t('timers.maxRuns')}</dt><dd>{timer.runCount} / {timer.runAt ? 1 : timer.maxRuns || '∞'}</dd>
                <dt>{t('timers.endDate')}</dt><dd>{timer.endDate || '—'}</dd>
                <dt>{t('timers.concurrent')}</dt><dd>{timer.maxConcurrent || 1}</dd>
                {kind === 'prompt' && <><dt>{t('timers.timeout')}</dt><dd>{timer.timeout || '∞'}</dd></>}
                <dt>{t('timers.created')}</dt><dd>{timerDate(timer.createdAt, timer.timeZone)}</dd>
                <dt>{t('timers.updated')}</dt><dd>{timerDate(timer.updatedAt, timer.timeZone)}</dd>
              </dl>
              {timer.stateReason && <p>{timer.stateReason}</p>}
              {timer.lastTriggerError && <pre className="timer-error">{timer.lastTriggerError}</pre>}
              <div className="timer-actions">
                <button type="button" onClick={() => navigate('executions', timer.id)}>{t('timers.executions')}</button>
                {canReadJobs && <button type="button" onClick={() => navigate('sessions', timer.id)}>{t('timers.sessions')}</button>}
                {canWrite && <>
                  <button type="button" onClick={() => setEditor(timer)}>{t('timers.edit')}</button>
                  {timer.enabled ? <button type="button" disabled={busy} onClick={() => void doAction(timer, 'disable')}>{t('timers.disable')}</button>
                    : <button type="button" disabled={busy || !canExecute || !timer.approvalPreview} onClick={() => setConfirm({ timer, action: 'enable' })}>{t('timers.enable')}</button>}
                  <button type="button" onClick={() => setConfirm({ timer, action: 'delete' })}>{t('timers.delete')}</button>
                </>}
                {canExecute && permissions.includes('schedule.execute') && <button type="button" disabled={busy || timer.planState === 'ended' || timer.planState === 'pendingApproval'} onClick={() => void doAction(timer, 'run')}>{t('timers.run')}</button>}
              </div>
            </section>}
          </article>;
        })}
        {view === 'executions' && visibleExecutions.map(record => (
          <details key={record.id} open={record.id === executionId || undefined}>
            <summary className="timer-entry"><strong>{record.name}</strong><span>{t(`timers.${record.kind}`)} · {workspaceName(record.workspaceId)}</span>
              <span>{timerDate(record.triggeredAt || record.scheduledAt || 0, record.configuration?.timeZone)} · {t(`timers.${record.trigger}`)}</span>
              <span>{showStatus(record.status)}</span></summary>
            <section className="timer-entry-body">
              <dl className="timer-details">
                <dt>{t('timers.target')}</dt><dd>{record.configuration?.prompt || workflowName(record.workflowId) || '—'}</dd>
                {record.configuration && <>
                  <dt>{t('timers.plan')}</dt><dd>{showPlan(record.configuration)} · {record.configuration.timeZone}</dd>
                  <dt>{t('timers.workspace')}</dt><dd>{workspaceName(record.workspaceId)} · {record.configuration.workdir}</dd>
                  {record.configuration.workflowVersion && <><dt>{t('timers.version')}</dt><dd>{record.configuration.workflowVersion}</dd></>}
                  {record.configuration.agentId && <><dt>{t('timers.agent')}</dt><dd>{record.configuration.agentId} · {record.configuration.modelId}</dd></>}
                  {record.configuration.endDate && <><dt>{t('timers.endDate')}</dt><dd>{record.configuration.endDate}</dd></>}
                  <dt>{t('timers.concurrent')}</dt><dd>{record.configuration.maxConcurrent || 1}</dd>
                  {record.kind === 'prompt' && <><dt>{t('timers.timeout')}</dt><dd>{record.configuration.timeout || '∞'}</dd></>}
                </>}
                {record.scheduledAt && <><dt>{t('timers.runAt')}</dt><dd>{timerDate(record.scheduledAt, record.configuration?.timeZone)}{record.missedThrough ? ` – ${timerDate(record.missedThrough, record.configuration?.timeZone)}` : ''}</dd></>}
                {record.startedAt && <><dt>{t('timers.started')}</dt><dd>{timerDate(record.startedAt, record.configuration?.timeZone)}</dd></>}
                {record.finishedAt && <><dt>{t('timers.finished')}</dt><dd>{timerDate(record.finishedAt, record.configuration?.timeZone)}</dd></>}
              </dl>
              {record.error && <pre className="timer-error">{record.error}</pre>}
              {record.detailsError && <pre className="timer-error">{record.detailsError}</pre>}
              <div className="timer-actions">
                <button type="button" onClick={() => navigate('executions', record.scheduleId)}>{t('timers.filterTimer')}</button>
                {canReadJobs && record.jobId && !record.detailsError && <>
                  <a href={jobLink(record.jobId, record.scheduleId, record.id, record.workspaceId)}>{t('timers.openJob')}</a>
                  <button type="button" onClick={() => navigate('sessions', record.scheduleId, record.id)}>{t('timers.sessions')}</button>
                </>}
              </div>
            </section>
          </details>
        ))}
        {view === 'sessions' && canReadJobs && sessions.filter(item => (!scheduleId || item.scheduleId === scheduleId) && (!executionId || item.executionId === executionId)).map(session => (
          <article key={session.id} className="timer-entry timer-session">
            <a href={jobLink(session.jobId, session.scheduleId, session.executionId, session.workspaceId, session.id)}>{session.title || session.id}</a>
            <span>{session.scheduleName}{session.nodeName || session.nodeId ? ` · ${session.nodeName || session.nodeId}` : ''}{session.loop?.length ? ` · ${session.loop.map(item => item.index).join(' / ')}` : ''}</span>
            <span>{session.agentId} {session.modelId} · {timerDate(session.createdAt)}</span>
            <button type="button" onClick={() => navigate('executions', session.scheduleId, session.executionId)}>{t('timers.backToExecution')}</button>
          </article>
        ))}
      </div>
      {loading && view !== 'plans' && <p role="status">{t('common.loading')}</p>}
      {!loading && (view === 'plans' ? !timers.length : view === 'executions' ? !visibleExecutions.length : !sessions.length) && <p className="timer-empty">{t('timers.empty')}</p>}
      {nextCursor && <button type="button" className="timer-more" onClick={() => setCursor(nextCursor)}>{t('timers.more')}</button>}
      {editor !== undefined && <ScheduleEditModal schedule={editor} options={options} permissions={permissions}
        onClose={() => setEditor(undefined)} onSave={saved => {
          setEditor(undefined);
          setSelection(saved.id);
          setNotice(t('timers.saved', { name: saved.name, id: saved.id }));
          refresh();
        }} />}
      {confirm && <div className="timer-overlay"><section className="timer-editor" role="dialog" aria-modal="true" aria-label={t(`timers.${confirm.action}`)}>
        <header><h2>{t(`timers.${confirm.action}`)} · {confirm.timer.name}</h2>
          <button type="button" aria-label={t('common.close')} onClick={() => setConfirm(undefined)} disabled={busy}>×</button></header>
        <div className="timer-entry-body">
          {error && <pre className="timer-error" role="alert">{error}</pre>}
          {confirm.action === 'enable' && <p>{showPlan(confirm.timer)} · {confirm.timer.timeZone}<br />
            {confirm.timer.prompt || `${workflowName(confirm.timer.graphWorkflowId)} ${confirm.timer.workflowVersion ?? ''}`}<br />
            {workspaceName(confirm.timer.workspaceId)} · {confirm.timer.workdir}</p>}
          <button className="timer-primary" type="button" disabled={busy} onClick={() => void doAction(confirm.timer, confirm.action)}>{t(`timers.${confirm.action}`)}</button>
        </div>
      </section></div>}
    </main>
  );
}
