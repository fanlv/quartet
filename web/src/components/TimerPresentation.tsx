import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import type { ScheduleInfo } from '../types';
import { timerDate } from '../utils/timers';
import { ClockIcon } from './ComposerIcons';
import { cronToHuman } from './CronInput';

const iconPaths = {
  history: 'M3 12a9 9 0 1 1 2.6 6.4M3 5v7h7m2-4v4l3 2',
  sessions: 'M4 5h16v11H9l-5 4V5Zm4 4h8m-8 3h5',
  graph: 'M12 2v4m0 4v4m-6 4v-3a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v3M10 6h4v4h-4zM4 18h4v4H4zm12 0h4v4h-4z',
  prompt: 'M5 6l6 6-6 6m8 0h6',
  plus: 'M12 5v14m-7-7h14',
  refresh: 'M20 7v5h-5M4 17v-5h5m-4.5 0a7.5 7.5 0 0 1 13.6-4.4L20 12M4 12l1.9 4.4a7.5 7.5 0 0 0 13.6-4.4',
  chevron: 'm9 6 6 6-6 6',
  back: 'm10 6-6 6 6 6m-6-6h16',
  play: 'm8 5 11 7-11 7V5Z',
  open: 'M13 5h6v6m0-6-8 8m7 1v5H5V6h5',
  check: 'm5 12 4 4L19 6',
  close: 'm6 6 12 12M18 6 6 18',
};

export function TimerIcon({ name, className = 'timer-icon' }: {
  name: 'clock' | keyof typeof iconPaths;
  className?: string;
}) {
  if (name === 'clock') return <ClockIcon className={className} />;
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={iconPaths[name]} />
    </svg>
  );
}

const statusTones: Record<string, string> = {
  enabled: 'success', completed: 'success',
  accepted: 'active', dispatching: 'active', pending: 'active', running: 'active', recovering: 'active',
  pendingApproval: 'warning', awaitingInput: 'warning', skipped: 'warning', missed: 'warning',
  failed: 'danger', timedOut: 'danger', interrupted: 'danger',
};

export function TimerStatus({ value }: { value?: string }) {
  const { t } = useTranslation();
  if (!value) return <span className="timer-muted">—</span>;
  return <span className="timer-status" data-tone={statusTones[value] || 'neutral'}>{t(`timers.status.${value}`, { defaultValue: value })}</span>;
}

export function TimerTime({ value, zone, seconds }: { value?: string | number; zone?: string; seconds?: boolean }) {
  const { i18n } = useTranslation();
  if (!value) return <span className="timer-muted">—</span>;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return <span>{value}</span>;
  return <time className="timer-time" dateTime={date.toISOString()}>{timerDate(value, zone, i18n.resolvedLanguage, seconds ? 'medium' : 'short')}</time>;
}

export function TimerSchedule({ timer }: { timer: ScheduleInfo }) {
  const { t } = useTranslation();
  return timer.runAt
    ? <TimerTime value={timer.runAt} zone={timer.timeZone} />
    : <span>{cronToHuman(timer.cronExpr, t) || '—'}</span>;
}

export function TimerField({ label, children, wide }: { label: string; children?: ReactNode; wide?: boolean }) {
  return (
    <div className={wide ? 'timer-field-wide' : undefined}>
      <dt>{label}</dt>
      <dd>{children ?? <span className="timer-muted">—</span>}</dd>
    </div>
  );
}

export function TimerConfiguration({ timer, workspaceName, workflowName, children }: {
  timer: ScheduleInfo;
  workspaceName?: string;
  workflowName?: string;
  children?: ReactNode;
}) {
  const { t } = useTranslation();
  const prompt = timer.kind === 'prompt';
  const cron = timer.runAt ? '' : timer.cronExpr;
  const readableCron = cron && cronToHuman(cron, t) !== cron;

  return (
    <dl className="timer-details">
      {prompt && <TimerField label={t('timers.target')} wide>{timer.prompt || undefined}</TimerField>}
      {!prompt && <TimerField label={t('timers.graph')}>{workflowName || timer.graphWorkflowId || undefined}</TimerField>}
      {(!cron || readableCron) && <TimerField label={t('timers.plan')}><TimerSchedule timer={timer} /></TimerField>}
      {cron && <TimerField label={t('timers.cron')}><code>{cron}</code></TimerField>}
      <TimerField label={t('timers.zone')}>{timer.timeZone || undefined}</TimerField>
      <TimerField label={t('timers.workspace')}>{workspaceName || timer.workspaceId || undefined}</TimerField>
      {timer.workflowVersion && <TimerField label={t('timers.version')}><code>{timer.workflowVersion}</code></TimerField>}
      {timer.agentId && <TimerField label={t('timers.agent')}>{timer.agentId}</TimerField>}
      {timer.modelId && <TimerField label={t('timers.model')}>{timer.modelId}</TimerField>}
      <TimerField label={t('timers.maxRuns')}>{timer.runAt ? 1 : timer.maxRuns || '∞'}</TimerField>
      <TimerField label={t('timers.concurrent')}>{timer.maxConcurrent || 1}</TimerField>
      {prompt && <TimerField label={t('timers.timeout')}>{timer.timeout || '∞'}</TimerField>}
      {timer.endDate && <TimerField label={t('timers.endDate')}>{timer.endDate}</TimerField>}
      {timer.workdir && <TimerField label={t('timers.workdir')} wide><code>{timer.workdir}</code></TimerField>}
      {children}
    </dl>
  );
}
