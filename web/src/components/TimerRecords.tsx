import { useId, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { ScheduleInfo } from '../types';
import { TimerConfiguration, TimerField, TimerIcon, TimerSchedule, TimerStatus, TimerTime } from './TimerPresentation';

export interface TimerExecution {
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

const columns = {
  plans: ['name', 'plan', 'nextRun', 'state', 'lastStatus'],
  executions: ['name', 'job', 'triggered', 'trigger', 'state'],
};

export function TimerRecords({ view, toolbar, showColumns = true, showJobColumn = false, children }: {
  view: keyof typeof columns;
  toolbar?: ReactNode;
  showColumns?: boolean;
  showJobColumn?: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <div className="timer-records" data-view={view} data-job-column={showJobColumn || undefined}>
      {toolbar}
      {showColumns && (
        <div className="timer-columns timer-row-grid" aria-hidden="true">
          {columns[view].filter(column => column !== 'job' || showJobColumn).map(column => <span key={column}>{t(`timers.${column}`)}</span>)}
        </div>
      )}
      {children}
    </div>
  );
}

function RecordTitle({ title, meta, icon, labelId }: { title: string; meta?: string; icon: 'graph' | 'prompt'; labelId?: string }) {
  return (
    <span className="timer-record-title" id={labelId} aria-hidden={labelId ? true : undefined}>
      <span className="timer-record-icon"><TimerIcon name={icon} /></span>
      <span className="timer-record-text">
        <strong>{title}</strong>
        {meta && <span className="timer-meta">{meta}</span>}
      </span>
    </span>
  );
}

function RecordCell({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="timer-cell">
      <span className="timer-cell-label">{label}</span>
      {children}
    </span>
  );
}

export function TimerPlanRow({ timer, workspaceName, workflowName, expanded, onToggle, actions }: {
  timer: ScheduleInfo;
  workspaceName?: string;
  workflowName?: string;
  expanded: boolean;
  onToggle: () => void;
  actions: ReactNode;
}) {
  const { t } = useTranslation();
  const kind = timer.kind || 'graph';

  return (
    <article className="timer-record">
      <button type="button" className="timer-entry timer-row-grid" aria-expanded={expanded} onClick={onToggle}>
        <RecordTitle title={timer.name} meta={[t(`timers.${kind}`), workspaceName].filter(Boolean).join(' · ')} icon={kind} />
        <RecordCell label={t('timers.plan')}>
          <TimerSchedule timer={timer} />
          <span className="timer-meta">{[t(timer.runAt ? 'timers.single' : 'timers.repeat'), timer.timeZone].filter(Boolean).join(' · ')}</span>
        </RecordCell>
        <RecordCell label={t('timers.nextRun')}><TimerTime value={timer.nextRunAt} zone={timer.timeZone} /></RecordCell>
        <RecordCell label={t('timers.state')}><TimerStatus value={timer.planState || (timer.enabled ? 'enabled' : 'disabled')} /></RecordCell>
        <RecordCell label={t('timers.lastStatus')}><TimerStatus value={timer.lastStatus} /></RecordCell>
        <TimerIcon name="chevron" className="timer-row-chevron" />
      </button>
      {expanded && (
        <div className="timer-record-panel">
          {timer.stateReason && <p className="timer-feedback">{timer.stateReason}</p>}
          {timer.lastTriggerError && <pre className="timer-feedback timer-feedback-error">{timer.lastTriggerError}</pre>}
          <TimerConfiguration timer={timer} workspaceName={workspaceName} workflowName={workflowName}>
            <TimerField label={t('timers.runCount')}>{timer.runCount}</TimerField>
            <TimerField label={t('timers.created')}><TimerTime value={timer.createdAt} zone={timer.timeZone} /></TimerField>
            <TimerField label={t('timers.updated')}><TimerTime value={timer.updatedAt} zone={timer.timeZone} /></TimerField>
            <TimerField label={t('timers.identifier')} wide><code>{timer.id}</code></TimerField>
          </TimerConfiguration>
          <div className="timer-actions timer-actions-split">{actions}</div>
        </div>
      )}
    </article>
  );
}

export function TimerJobAction({ record, primary = false }: { record: TimerExecution; primary?: boolean }) {
  const { t } = useTranslation();
  const className = `timer-button timer-job-action${primary ? ' timer-button-primary' : ''}`;
  const label = <>{t('timers.openJob')}<TimerIcon name="open" /></>;

  if (!record.jobId || record.detailsError) {
    return <button type="button" className={className} disabled title={record.detailsError || t('timers.jobNotCreated')}>{label}</button>;
  }

  const query = new URLSearchParams({
    jobId: record.jobId,
    timerScheduleId: record.scheduleId,
    timerExecutionId: record.id,
    timerTab: 'executions',
  });
  if (record.workspaceId) query.set('workspaceId', record.workspaceId);

  return <a className={className} href={`/?${query}`} target="_blank" rel="noopener noreferrer">{label}</a>;
}

export function TimerExecutionRow({ record, workspaceName, onOpen, canReadJobs }: {
  record: TimerExecution;
  workspaceName?: string;
  onOpen: () => void;
  canReadJobs: boolean;
}) {
  const { t } = useTranslation();
  const titleId = useId();

  return (
    <article className="timer-record timer-entry timer-execution-row timer-row-grid">
      <button type="button" className="timer-execution-open" aria-labelledby={titleId} onClick={onOpen} />
      <RecordTitle title={record.name} meta={[t(`timers.${record.kind}`, { defaultValue: record.kind }), workspaceName].filter(Boolean).join(' · ')} icon={record.kind === 'prompt' ? 'prompt' : 'graph'} labelId={titleId} />
      {canReadJobs && <TimerJobAction record={record} />}
      <RecordCell label={t('timers.triggered')}><TimerTime value={record.triggeredAt || record.scheduledAt} zone={record.configuration?.timeZone} /></RecordCell>
      <RecordCell label={t('timers.trigger')}>{t(`timers.${record.trigger}`, { defaultValue: record.trigger })}</RecordCell>
      <RecordCell label={t('timers.state')}><TimerStatus value={record.status} /></RecordCell>
      <TimerIcon name="chevron" className="timer-row-chevron" />
    </article>
  );
}

export function TimerExecutionDetail({ record, workspaceName, workflowName, actions }: {
  record: TimerExecution;
  workspaceName?: string;
  workflowName?: string;
  actions: ReactNode;
}) {
  const { t } = useTranslation();
  const zone = record.configuration?.timeZone;
  const meta = [t(`timers.${record.kind}`, { defaultValue: record.kind }), t(`timers.${record.trigger}`, { defaultValue: record.trigger }), workspaceName].filter(Boolean).join(' · ');

  return (
    <article className="timer-execution-detail">
      <header className="timer-detail-header">
        <div className="timer-detail-title">
          <span className="timer-record-icon"><TimerIcon name={record.kind === 'prompt' ? 'prompt' : 'graph'} /></span>
          <div>
            <div className="timer-detail-heading">
              <h2>{record.name}</h2>
              <TimerStatus value={record.status} />
            </div>
            {meta && <p className="timer-meta">{meta}</p>}
          </div>
        </div>
        {actions}
      </header>
      <div className="timer-detail-body">
        {record.error && <pre className="timer-feedback timer-feedback-error">{record.error}</pre>}
        {record.detailsError && <pre className="timer-feedback timer-feedback-error">{record.detailsError}</pre>}
        <dl className="timer-details timer-execution-times">
          {record.scheduledAt && (
            <TimerField label={t('timers.runAt')}>
              <TimerTime value={record.scheduledAt} zone={zone} />
              {record.missedThrough && <> – <TimerTime value={record.missedThrough} zone={zone} /></>}
            </TimerField>
          )}
          {record.triggeredAt && <TimerField label={t('timers.triggered')}><TimerTime value={record.triggeredAt} zone={zone} seconds /></TimerField>}
          <TimerField label={t('timers.started')}><TimerTime value={record.startedAt} zone={zone} seconds /></TimerField>
          <TimerField label={t('timers.finished')}><TimerTime value={record.finishedAt} zone={zone} seconds /></TimerField>
        </dl>
        {(record.configuration || workflowName) && (
          <section className="timer-detail-section">
            <h3>{t('timers.configuration')}</h3>
            {record.configuration ? (
              <TimerConfiguration timer={record.configuration} workspaceName={workspaceName} workflowName={workflowName} />
            ) : (
              <dl className="timer-details"><TimerField label={t('timers.graph')}>{workflowName}</TimerField></dl>
            )}
          </section>
        )}
      </div>
    </article>
  );
}
