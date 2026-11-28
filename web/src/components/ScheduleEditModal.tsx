import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { AgentInfo, ScheduleInfo, WorkspaceInfo } from '../types';
import { timerDate, timerFetch } from '../utils/timers';

export interface TimerOptions {
  workspaces: WorkspaceInfo[];
  workflows: { id: string; name: string }[];
  agents: AgentInfo[];
}

function localInputDate(value?: string, zone?: string) {
  if (!value) return '';
  const formatter = new Intl.DateTimeFormat('en-CA', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23',
    timeZone: zone && zone !== 'Local' ? zone : undefined,
  });
  const parts = Object.fromEntries(formatter.formatToParts(new Date(value)).map(({ type, value }) => [type, value]));
  return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
}

const repeatPresets = [
  ['0 * * * *', 'cron.presets.everyHour'],
  ['0 9 * * *', 'cron.presets.everyDay9'],
  ['0 9 * * 1', 'cron.presets.everyMonday9'],
] as const;

interface Props {
  schedule: ScheduleInfo | null;
  options: TimerOptions;
  permissions: string[];
  onSave: (schedule: ScheduleInfo) => void;
  onClose: () => void;
}

export function ScheduleEditModal({ schedule, options, permissions, onSave, onClose }: Props) {
  const { t } = useTranslation();
  const [kind, setKind] = useState(schedule?.kind ?? 'graph');
  const [name, setName] = useState(schedule?.name ?? '');
  const [workflow, setWorkflow] = useState(schedule?.graphWorkflowId ?? '');
  const [prompt, setPrompt] = useState(schedule?.prompt ?? '');
  const [workspace, setWorkspace] = useState(schedule?.workspaceId ?? '');
  const [workdir, setWorkdir] = useState(schedule?.workdir ?? '');
  const [agent, setAgent] = useState(schedule?.agentId ?? '');
  const [model, setModel] = useState(schedule?.modelId ?? '');
  const [single, setSingle] = useState(Boolean(schedule?.runAt));
  const [cron, setCron] = useState(schedule?.cronExpr || '0 9 * * *');
  const [customCron, setCustomCron] = useState(Boolean(schedule?.cronExpr && !repeatPresets.some(([rule]) => rule === schedule.cronExpr)));
  const [runAt, setRunAt] = useState(() => localInputDate(schedule?.runAt, schedule?.timeZone));
  const [zone, setZone] = useState(schedule?.timeZone ?? '');
  const [endDate, setEndDate] = useState(schedule?.endDate ?? '');
  const [maxRuns, setMaxRuns] = useState(schedule?.maxRuns ?? 0);
  const [maxConcurrent, setMaxConcurrent] = useState(schedule?.maxConcurrent ?? 1);
  const [timeout, setTimeout] = useState(schedule?.timeout ?? 0);
  const [enabled, setEnabled] = useState(schedule?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [confirmation, setConfirmation] = useState<{ preview: ScheduleInfo; body: object; path: string } | null>(null);
  const canEnable = permissions.includes(kind === 'prompt' ? 'job.execute' : 'workflow.execute');
  const models = options.agents.find(item => item.agent_id === agent)?.models?.availableModels ?? [];
  const availableWorkflow = options.workflows.some(item => item.id === workflow);

  useEffect(() => {
    const close = (event: KeyboardEvent) => { if (event.key === 'Escape' && !busy) onClose(); };
    window.addEventListener('keydown', close);
    return () => window.removeEventListener('keydown', close);
  }, [busy, onClose]);

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError('');
    const base = '/api/v1/schedule';
    const path = schedule ? `${base}/${schedule.id}` : `${base}/create`;
    const starts = enabled && (canEnable || Boolean(schedule?.enabled));
    const body = {
      name, kind, graphWorkflowId: workflow, prompt, agentId: agent, modelId: model, workspaceId: workspace, workdir,
      cronExpr: single ? '' : cron,
      runAt: single ? (schedule?.runAt && runAt === localInputDate(schedule.runAt, schedule.timeZone) && zone === schedule.timeZone ? schedule.runAt : runAt) : '',
      timeZone: zone, endDate: single ? '' : endDate, maxRuns: single ? 0 : maxRuns,
      maxConcurrent, timeout, enabled: schedule?.enabled && !canEnable ? undefined : starts,
    };
    try {
      if (confirmation) {
        const request = { ...confirmation.body, expectedApproval: confirmation.preview.approvalPreview };
        const saved = schedule
          ? await timerFetch<ScheduleInfo>(confirmation.path, 'PUT', request)
          : (await timerFetch<{ schedule: ScheduleInfo }>(confirmation.path, 'POST', request)).schedule;
        onSave(saved);
      } else if (starts && canEnable) {
        const preview = await timerFetch<ScheduleInfo>(schedule ? `${path}/preview` : `${base}/preview`, 'POST', body);
        setConfirmation({ preview, body, path });
      } else {
        const saved = schedule
          ? await timerFetch<ScheduleInfo>(path, 'PUT', body)
          : (await timerFetch<{ schedule: ScheduleInfo }>(path, 'POST', body)).schedule;
        onSave(saved);
      }
    } catch (err) {
      setError(String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="timer-overlay" onClick={event => { if (!busy && event.target === event.currentTarget) onClose(); }}>
      <section className="timer-editor" role="dialog" aria-modal="true" aria-labelledby="timer-editor-title">
        <header><h2 id="timer-editor-title">{t(confirmation ? 'timers.confirm' : schedule ? 'timers.edit' : 'timers.add')}</h2>
          <button type="button" aria-label={t('common.close')} disabled={busy} onClick={onClose}>×</button></header>
        <form onSubmit={save}>
          {error && <pre role="alert" className="timer-error">{error}</pre>}
          {confirmation ? (
            <dl className="timer-details">
              <dt>{t('timers.name')}</dt><dd>{confirmation.preview.name}</dd>
              <dt>{t('timers.target')}</dt><dd>{confirmation.preview.prompt || `${workflow} @ ${confirmation.preview.workflowVersion}`}</dd>
              <dt>{t('timers.plan')}</dt><dd>{confirmation.preview.runAt ? timerDate(confirmation.preview.runAt, confirmation.preview.timeZone) : confirmation.preview.cronExpr} · {confirmation.preview.timeZone}</dd>
              <dt>{t('timers.workspace')}</dt><dd>{confirmation.preview.workspaceId} · {confirmation.preview.workdir}</dd>
              {confirmation.preview.agentId && <><dt>{t('timers.agent')}</dt><dd>{confirmation.preview.agentId} · {confirmation.preview.modelId}</dd></>}
              {confirmation.preview.endDate && <><dt>{t('timers.endDate')}</dt><dd>{confirmation.preview.endDate}</dd></>}
              <dt>{t('timers.concurrent')}</dt><dd>{maxConcurrent}</dd>
              <dt>{t('timers.maxRuns')}</dt><dd>{maxRuns || '∞'}</dd>
              {kind === 'prompt' && <><dt>{t('timers.timeout')}</dt><dd>{timeout || '∞'}</dd></>}
            </dl>
          ) : (
            <div className="timer-fields">
              <label>{t('timers.name')}<input required value={name} onChange={event => setName(event.target.value)} /></label>
              <label>{t('timers.kind')}<select value={kind} onChange={event => setKind(event.target.value as 'graph' | 'prompt')}>
                <option value="graph">{t('timers.graph')}</option><option value="prompt">{t('timers.prompt')}</option>
              </select></label>
              {kind === 'graph' ? <label className="timer-field-wide">{t('timers.graph')}<select required value={workflow} onChange={event => setWorkflow(event.target.value)}>
                <option value="">{t('timers.selectWorkflow')}</option>
                {workflow && !availableWorkflow && <option value={workflow}>{workflow}</option>}
                {options.workflows.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
              </select></label> : <>
                <label className="timer-field-wide">{t('timers.prompt')}<textarea required rows={4} value={prompt} onChange={event => setPrompt(event.target.value)} /></label>
                <label>{t('timers.agent')}<input list="timer-agent-values" value={agent} onChange={event => { setAgent(event.target.value); setModel(''); }} placeholder={t('timers.workspaceDefaults')} /></label>
                <datalist id="timer-agent-values">{options.agents.map(item => <option key={item.agent_id} value={item.agent_id}>{item.display_name}</option>)}</datalist>
                <label>{t('timers.model')}<input list="timer-model-values" value={model} onChange={event => setModel(event.target.value)} placeholder={t('timers.workspaceDefaults')} /></label>
                <datalist id="timer-model-values">{models.map(item => <option key={item.modelId} value={item.modelId}>{item.name}</option>)}</datalist>
              </>}
              <label>{t('timers.workspace')}<select value={workspace} onChange={event => { setWorkspace(event.target.value); setWorkdir(''); }}>
                <option value="">{t('timers.backendDefault')}</option>
                {options.workspaces.map(item => <option key={item.id} value={item.id}>{item.title || item.id}</option>)}
              </select></label>
              <label>{t('timers.workdir')}<input value={workdir} onChange={event => setWorkdir(event.target.value)} placeholder={t('timers.workspaceDefaults')} /></label>
              <label>{t('timers.plan')}<select value={single ? 'single' : 'repeat'} onChange={event => setSingle(event.target.value === 'single')}>
                <option value="repeat">{t('timers.repeat')}</option><option value="single">{t('timers.single')}</option>
              </select></label>
              <label>{t('timers.zone')}<input value={zone} onChange={event => setZone(event.target.value)} placeholder={t('timers.backendDefault')} /></label>
              {single ? <label>{t('timers.runAt')}<input type="datetime-local" required value={runAt} onChange={event => setRunAt(event.target.value)} /></label> : <>
                <label>{t('timers.frequency')}<select value={customCron ? '' : cron} onChange={event => {
                  setCustomCron(!event.target.value);
                  if (event.target.value) setCron(event.target.value);
                }}>
                  {repeatPresets.map(([rule, label]) => <option key={rule} value={rule}>{t(label)}</option>)}
                  <option value="">{t('cron.tabs.custom')}</option>
                </select></label>
                {customCron && <label>{t('timers.cron')}<input required value={cron} onChange={event => setCron(event.target.value)} /></label>}
                <label>{t('timers.endDate')}<input type="date" value={endDate} onChange={event => setEndDate(event.target.value)} /></label>
                <label>{t('timers.maxRuns')}<input type="number" min="0" value={maxRuns} onChange={event => setMaxRuns(event.target.valueAsNumber || 0)} /></label>
              </>}
              <label>{t('timers.concurrent')}<input type="number" min="1" value={maxConcurrent} onChange={event => setMaxConcurrent(event.target.valueAsNumber || 1)} /></label>
              {kind === 'prompt' && <label>{t('timers.timeout')}<input type="number" min="0" value={timeout} onChange={event => setTimeout(event.target.valueAsNumber || 0)} /></label>}
              <label className="timer-checkbox"><input type="checkbox" checked={canEnable ? enabled : Boolean(schedule?.enabled)} disabled={!canEnable} onChange={event => setEnabled(event.target.checked)} />{t('timers.enabled')}</label>
            </div>
          )}
          <footer>
            {confirmation && <button type="button" onClick={() => setConfirmation(null)} disabled={busy}>{t('timers.edit')}</button>}
            <button className="timer-primary" type="submit" disabled={busy}>{t(confirmation ? 'timers.confirmEnable' : 'timers.save')}</button>
          </footer>
        </form>
      </section>
    </div>
  );
}
