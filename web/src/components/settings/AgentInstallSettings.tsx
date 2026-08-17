import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { isImageUrl } from '../../utils/url';
import './AgentInstallSettings.css';

interface InstallStepResult {
  display: string;
  stdout: string;
  stderr: string;
  exit_code: number;
  timed_out: boolean;
  error?: string;
  duration_ms: number;
}

interface InstallResult {
  agent_id: string;
  steps: InstallStepResult[];
  installed: boolean;
  install_error?: string;
  validation?: { ok: boolean; error?: string };
}

interface RuntimeDefinition {
  bin: string;
  acp_program: string;
  acp_args: string[];
}

interface CatalogAgent {
  agent_id: string;
  source: 'builtin' | 'custom';
  display_name: string;
  icon_url: string;
  definition: RuntimeDefinition;
  supports_headless_print: boolean;
  deprecated: boolean;
  lifecycle: string;
  current_revision?: string;
  install_method?: string;
  install_commands?: string[];
  install_instructions?: string;
  auto_installable?: boolean;
  auto_uninstallable?: boolean;
  installed: boolean;
  availability: string;
  availability_error?: string;
  last_validation_status?: string;
  last_validation_error?: string;
  last_validation_at?: number;
  delete_error?: string;
  refreshing?: boolean;
}

interface CustomFormState {
  agentId?: string;
  restore?: boolean;
  displayName: string;
  iconUrl: string;
  bin: string;
  acpProgram: string;
  argsText: string;
  supportsHeadlessPrint: boolean;
  environmentText: string;
}

interface DeleteImpact {
  agent_id: string;
  cleared_settings: string[];
  retained_workflows: string[];
  retained_schedules: string[];
  retained_jobs: string[];
  retained_sessions: string[];
  blocking_job_ids: string[];
}

interface DeleteStopResult {
  job_id: string;
  graph_run_id?: string;
  stopped: boolean;
  error?: string;
}

interface DeleteResult {
  status: string;
  stop_results?: DeleteStopResult[];
  impact: DeleteImpact;
}

interface ValidationFeedback {
  status: 'checking' | 'success' | 'warning' | 'error';
  message: string;
}

const emptyCustomForm: CustomFormState = {
  displayName: '',
  iconUrl: '',
  bin: '',
  acpProgram: '',
  argsText: '',
  supportsHeadlessPrint: false,
  environmentText: '',
};

async function readResponse(res: Response): Promise<Record<string, unknown>> {
  const raw = await res.text().catch((err) => {
    throw new Error(`HTTP ${res.status}: failed to read response: ${String(err)}`);
  });
  let data: Record<string, unknown> | null = null;
  if (raw) {
    try {
      data = JSON.parse(raw) as Record<string, unknown>;
    } catch (err) {
      throw new Error(`HTTP ${res.status}: invalid JSON response: ${String(err)}\n${raw}`);
    }
  }
  if (!res.ok || data?.code !== 0) {
    const detail = [data?.msg, data?.message, data?.error]
      .find((value): value is string => typeof value === 'string' && value !== '');
    throw new Error(detail || `HTTP ${res.status}${raw ? `\n${raw}` : ''}`);
  }
  return data;
}

// AgentInstallSettings lists built-in agents that are not installed yet and
// runs their preset install flow. The backend only accepts an agent_id and
// executes catalog-declared commands; the full step output, recheck and
// validation result are shown verbatim (collapsed by default).
export function AgentInstallSettings() {
  const { t } = useTranslation();
  const [catalog, setCatalog] = useState<CatalogAgent[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  // Which agent is currently running an install or uninstall flow. The backend
  // serializes both under one lock, so all install/uninstall buttons disable
  // while any one runs.
  const [installBusy, setInstallBusy] = useState<{ id: string; action: 'install' | 'uninstall' } | null>(null);
  // Results survive the catalog refresh so the user can still inspect the full
  // step output after the card's button state flips.
  const [results, setResults] = useState<Record<string, { action: 'install' | 'uninstall'; result: InstallResult }>>({});
  const [requestErrors, setRequestErrors] = useState<Record<string, string>>({});
  const [form, setForm] = useState<CustomFormState | null>(null);
  const [managementPending, setManagementPending] = useState('');
  const [managementMessage, setManagementMessage] = useState('');
  const [deleteResult, setDeleteResult] = useState<DeleteResult | null>(null);
  const [revisionMap, setRevisionMap] = useState<Record<string, Array<{ revision: string; definition: RuntimeDefinition }>>>({});
  const [validationFeedback, setValidationFeedback] = useState<Record<string, ValidationFeedback>>({});

  const loadData = useCallback(async (showLoading = true) => {
    if (showLoading) setLoading(true);
    setLoadError('');
    try {
      const responses = await Promise.all([
        fetch('/api/v1/agent/catalog'),
        fetch('/api/v1/agent/catalog/deleted'),
      ]);
      const [catalogData, deletedData] = await Promise.all([
        readResponse(responses[0]),
        readResponse(responses[1]),
      ]);
      const catalogItems = Array.isArray(catalogData.agents)
        ? catalogData.agents as CatalogAgent[]
        : [];
      const deletedItems = Array.isArray(deletedData.agents)
        ? deletedData.agents as CatalogAgent[]
        : [];
      setCatalog([...catalogItems, ...deletedItems]);
    } catch (err) {
      setLoadError(String(err));
    } finally {
      if (showLoading) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadData();
  }, [loadData]);

  const openCustomForm = (agent?: CatalogAgent, restore = false) => {
    setManagementMessage('');
    setForm(agent ? {
      agentId: agent.agent_id,
      restore,
      displayName: agent.display_name,
      iconUrl: agent.icon_url,
      bin: agent.definition.bin,
      acpProgram: agent.definition.acp_program,
      argsText: agent.definition.acp_args.join('\n'),
      supportsHeadlessPrint: agent.supports_headless_print,
      environmentText: '',
    } : { ...emptyCustomForm });
  };

  const submitCustom = async () => {
    if (!form) return;
    setManagementPending(form.agentId || 'create');
    setManagementMessage('');
    try {
      const canEditEnvironment = !form.agentId || form.restore;
      const environment = canEditEnvironment
        ? form.environmentText.split('\n').map((line) => line.trim()).filter(Boolean).map((line) => {
            const separator = line.indexOf('=');
            if (separator < 1) throw new Error(`Invalid environment entry: ${line}`);
            return { key: line.slice(0, separator).trim(), value: line.slice(separator + 1), enabled: true };
          })
        : undefined;
      const body = {
        display_name: form.displayName,
        icon_url: form.iconUrl,
        supports_headless_print: form.supportsHeadlessPrint,
        definition: {
          bin: form.bin,
          acp_program: form.acpProgram,
          acp_args: form.argsText
            .split('\n')
            .map((value) => value.endsWith('\r') ? value.slice(0, -1) : value)
            .filter((value) => value !== ''),
        },
        ...(environment ? { environment } : {}),
      };
      const url = !form.agentId
        ? '/api/v1/agent/custom'
        : form.restore
          ? `/api/v1/agent/custom/${encodeURIComponent(form.agentId)}/restore`
          : `/api/v1/agent/custom/${encodeURIComponent(form.agentId)}`;
      const data = await readResponse(await fetch(url, {
        method: !form.agentId || form.restore ? 'POST' : 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      }));
      setForm(null);
      setManagementMessage(
        typeof data.warning === 'string' && data.warning
          ? `${t('common.saveSuccess')}\n${data.warning}`
          : t('common.saveSuccess'),
      );
      window.dispatchEvent(new CustomEvent('quartet:agent-catalog-changed', {
        detail: { agentId: form.agentId },
      }));
      await loadData();
    } catch (err) {
      setManagementMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setManagementPending('');
    }
  };

  const revalidate = async (agent: CatalogAgent) => {
    setManagementPending(agent.agent_id);
    setManagementMessage('');
    setValidationFeedback((current) => ({
      ...current,
      [agent.agent_id]: {
        status: 'checking',
        message: t('settings.agents.checkInProgress'),
      },
    }));
    try {
      const data = await readResponse(await fetch(`/api/v1/agent/${encodeURIComponent(agent.agent_id)}/revalidate`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      }));
      await loadData(false);
      const warning = typeof data.warning === 'string' ? data.warning : '';
      setValidationFeedback((current) => ({
        ...current,
        [agent.agent_id]: {
          status: warning ? 'warning' : 'success',
          message: warning || t('settings.agents.checkSucceeded'),
        },
      }));
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      await loadData(false);
      setValidationFeedback((current) => ({
        ...current,
        [agent.agent_id]: { status: 'error', message },
      }));
    } finally {
      setManagementPending('');
    }
  };

  const loadRevisions = async (agentId: string) => {
    if (revisionMap[agentId]) return;
    try {
      const data = await readResponse(await fetch(`/api/v1/agent/catalog/${encodeURIComponent(agentId)}`));
      setRevisionMap((current) => ({
        ...current,
        [agentId]: Array.isArray(data.revisions) ? data.revisions : [],
      }));
    } catch (err) {
      setManagementMessage(err instanceof Error ? err.message : String(err));
    }
  };

  const deleteCustom = async (agent: CatalogAgent, force = false) => {
    setManagementPending(agent.agent_id);
    setManagementMessage('');
    setDeleteResult(null);
    try {
      const impactData = await readResponse(await fetch(
        `/api/v1/agent/custom/${encodeURIComponent(agent.agent_id)}/delete-impact`,
      ));
      const impact = impactData.impact as DeleteImpact;
      const lines = [
        force
          ? t('settings.agents.forceDeleteConfirm', { name: agent.display_name })
          : t('settings.agents.deleteConfirm', { name: agent.display_name }),
        '',
        t('settings.agents.deleteImpact.cleared', {
          value: impact.cleared_settings.length > 0 ? impact.cleared_settings.join(', ') : t('common.none'),
        }),
        t('settings.agents.deleteImpact.workflows', { value: impact.retained_workflows.join(', ') || t('common.none') }),
        t('settings.agents.deleteImpact.schedules', { value: impact.retained_schedules.join(', ') || t('common.none') }),
        t('settings.agents.deleteImpact.jobs', { value: impact.retained_jobs.join(', ') || t('common.none') }),
        t('settings.agents.deleteImpact.sessions', { value: impact.retained_sessions.join(', ') || t('common.none') }),
        t('settings.agents.deleteImpact.blocking', { value: impact.blocking_job_ids.join(', ') || t('common.none') }),
      ];
      if (!window.confirm(lines.join('\n'))) return;
      setCatalog((current) => current.map((item) => (
        item.agent_id === agent.agent_id
          ? { ...item, lifecycle: 'deleting', availability: 'deleting' }
          : item
      )));
      setManagementMessage(t('settings.agents.deleteProgress'));
      const resultData = await readResponse(await fetch(`/api/v1/agent/custom/${encodeURIComponent(agent.agent_id)}/delete`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ force }),
      }));
      setDeleteResult(resultData.result as DeleteResult);
      setManagementMessage(t('settings.agents.deleteSuccess'));
      window.dispatchEvent(new CustomEvent('quartet:agent-catalog-changed', {
        detail: { agentId: agent.agent_id },
      }));
      if (localStorage.getItem('last_agent_type') === agent.agent_id) {
        localStorage.removeItem('last_agent_type');
      }
      for (let index = 0; index < localStorage.length; index += 1) {
        const key = localStorage.key(index);
        if (!key?.startsWith('workspacePrefs_')) continue;
        try {
          const value = JSON.parse(localStorage.getItem(key) || '{}') as { defaultAgent?: string; defaultModel?: string };
          if (value.defaultAgent !== agent.agent_id) continue;
          delete value.defaultAgent;
          delete value.defaultModel;
          if (Object.keys(value).length === 0) localStorage.removeItem(key);
          else localStorage.setItem(key, JSON.stringify(value));
        } catch {
          localStorage.removeItem(key);
        }
      }
      await loadData();
    } catch (err) {
      setManagementMessage(err instanceof Error ? err.message : String(err));
      await loadData();
    } finally {
      setManagementPending('');
    }
  };

  const install = async (agentId: string) => {
    setInstallBusy({ id: agentId, action: 'install' });
    setRequestErrors((prev) => ({ ...prev, [agentId]: '' }));
    try {
      const data = await readResponse(await fetch('/api/v1/agent/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ agent_id: agentId }),
      }));
      const result = data.result as InstallResult;
      setResults((prev) => ({ ...prev, [agentId]: { action: 'install', result } }));
      await loadData();
    } catch (err) {
      setRequestErrors((prev) => ({ ...prev, [agentId]: String(err) }));
    } finally {
      setInstallBusy(null);
    }
  };

  const uninstall = async (agentId: string) => {
    setInstallBusy({ id: agentId, action: 'uninstall' });
    setRequestErrors((prev) => ({ ...prev, [agentId]: '' }));
    try {
      const data = await readResponse(await fetch(`/api/v1/agent/${encodeURIComponent(agentId)}/uninstall`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      }));
      const result = data.result as InstallResult;
      setResults((prev) => ({ ...prev, [agentId]: { action: 'uninstall', result } }));
      await loadData();
    } catch (err) {
      setRequestErrors((prev) => ({ ...prev, [agentId]: String(err) }));
    } finally {
      setInstallBusy(null);
    }
  };

  const dismissResult = (agentId: string) => {
    setResults((prev) => {
      const next = { ...prev };
      delete next[agentId];
      return next;
    });
  };

  const renderIcon = (icon: string) =>
    isImageUrl(icon) ? (
      <img src={icon} alt="" className="agent-install-icon" referrerPolicy="no-referrer" />
    ) : (
      <span className="agent-install-icon agent-install-icon-emoji">{icon}</span>
    );

  const renderResult = (agentId: string, action: 'install' | 'uninstall', result: InstallResult) => {
    const ok = action === 'uninstall'
      ? !result.installed && result.steps.every((step) => step.exit_code === 0 && !step.error && !step.timed_out)
      : result.installed && result.validation?.ok;
    const summaryKey = action === 'uninstall'
      ? (ok ? 'settings.agents.result.uninstallSuccess' : 'settings.agents.result.uninstallFailed')
      : ok
        ? 'settings.agents.result.success'
        : result.installed
          ? 'settings.agents.result.validationFailed'
          : 'settings.agents.result.failed';
    return (
      <div key={agentId} className={`agent-install-result ${ok ? 'success' : 'failure'}`}>
        <div className="agent-install-result-header">
          <span>{t(summaryKey)}</span>
          <button className="agent-install-result-dismiss" onClick={() => dismissResult(agentId)}>×</button>
        </div>
        <details className="agent-install-result-details">
          <summary>{t('settings.agents.result.details')}</summary>
          {result.steps.map((step, i) => (
            <div key={i} className="agent-install-step">
              <div className="agent-install-step-title">
                <code>{step.display}</code>
                <span className="agent-install-step-meta">
                  {t('settings.agents.result.exitCode')}: {step.exit_code}
                  {step.timed_out ? ` · ${t('settings.agents.result.timedOut')}` : ''}
                  {` · ${(step.duration_ms / 1000).toFixed(1)}s`}
                </span>
              </div>
              {step.error && <pre className="agent-install-output error">{step.error}</pre>}
              {step.stdout && <pre className="agent-install-output">{step.stdout}</pre>}
              {step.stderr && <pre className="agent-install-output error">{step.stderr}</pre>}
            </div>
          ))}
          {result.install_error && (
            <div className="agent-install-step">
              <div className="agent-install-step-title">{t('settings.agents.result.recheck')}</div>
              <pre className="agent-install-output error">{result.install_error}</pre>
            </div>
          )}
          {result.validation && (
            <div className="agent-install-step">
              <div className="agent-install-step-title">
                {t('settings.agents.result.validation')}: {result.validation.ok ? 'OK' : t('settings.agents.result.failed')}
              </div>
              {result.validation.error && <pre className="agent-install-output error">{result.validation.error}</pre>}
            </div>
          )}
        </details>
      </div>
    );
  };

  if (loading) {
    return <div className="agent-install-loading">{t('common.loading')}</div>;
  }
  if (loadError) {
    return (
      <div className="agent-install-load-error">
        <span>{t('common.loadFailed')}: {loadError}</span>
        <button className="agent-install-retry" onClick={() => void loadData()}>{t('common.retry')}</button>
      </div>
    );
  }

  return (
    <div className="agent-install">
      <div className="agent-install-desc">{t('settings.agents.description')}</div>

      {Object.entries(results).map(([agentId, entry]) => renderResult(agentId, entry.action, entry.result))}

      <div className="agent-directory-toolbar">
        <strong>{t('settings.tabs.agents')}</strong>
        <button className="settings-btn settings-btn-primary" onClick={() => openCustomForm()}>
          {t('common.add')}
        </button>
      </div>

      {managementMessage && (
        <pre className="agent-install-request-error">{managementMessage}</pre>
      )}
      {deleteResult && (
        <details className="agent-install-result-details" open>
          <summary>{t('settings.agents.deleteResult')}</summary>
          <pre className="agent-install-output">
            {t('settings.agents.deleteImpact.cleared', {
              value: deleteResult.impact.cleared_settings.join(', ') || t('common.none'),
            })}
            {'\n'}
            {t('settings.agents.deleteImpact.workflows', {
              value: deleteResult.impact.retained_workflows.join(', ') || t('common.none'),
            })}
            {'\n'}
            {t('settings.agents.deleteImpact.schedules', {
              value: deleteResult.impact.retained_schedules.join(', ') || t('common.none'),
            })}
            {'\n'}
            {t('settings.agents.deleteImpact.jobs', {
              value: deleteResult.impact.retained_jobs.join(', ') || t('common.none'),
            })}
            {'\n'}
            {t('settings.agents.deleteImpact.sessions', {
              value: deleteResult.impact.retained_sessions.join(', ') || t('common.none'),
            })}
          </pre>
          {(deleteResult.stop_results || []).map((result) => (
            <pre key={result.job_id} className={`agent-install-output ${result.error ? 'error' : ''}`}>
              {result.job_id}: {result.stopped ? t('settings.agents.stopSucceeded') : result.error}
            </pre>
          ))}
        </details>
      )}

      {catalog.length === 0 && Object.keys(results).length === 0 && (
        <div className="agent-install-empty">{t('settings.agents.empty')}</div>
      )}

      <div className="agent-install-list">
        {catalog.map((agent) => {
          const command = [agent.definition.acp_program, ...(agent.definition.acp_args || [])]
            .filter(Boolean)
            .join(' ');
          const busy = installBusy?.id === agent.agent_id;
          const checkFeedback = validationFeedback[agent.agent_id];
          const checking = checkFeedback?.status === 'checking';
          const showManual = agent.source === 'builtin' && !agent.deprecated
            && !agent.installed && !agent.auto_installable && !!agent.install_instructions;
          return (
            <div key={agent.agent_id} className="agent-install-card" data-testid="agent-install-card" data-agent-id={agent.agent_id}>
              <div className="agent-install-card-head">
                {renderIcon(agent.icon_url)}
                <div className="agent-install-card-title">
                  <span className="agent-install-name">{agent.display_name}</span>
                  {agent.source === 'builtin' && agent.install_method && (
                    <span className={`agent-install-method method-${agent.install_method}`}>
                      {t(`settings.agents.method.${agent.install_method}`)}
                    </span>
                  )}
                  {agent.source === 'custom' && (
                    <span className="agent-install-method method-custom">{t('settings.agents.source.custom')}</span>
                  )}
                </div>
                <div className="agent-install-actions">
                  {agent.source === 'builtin' && !agent.deprecated && !agent.installed && agent.auto_installable && (
                    <button
                      className="agent-install-btn"
                      disabled={installBusy !== null}
                      onClick={() => void install(agent.agent_id)}
                      data-testid="agent-install-button"
                    >
                      {busy && installBusy?.action === 'install' ? t('common.installing') : t('common.install')}
                    </button>
                  )}
                  {agent.source === 'builtin' && !agent.deprecated && agent.installed && agent.auto_uninstallable && (
                    <button
                      className="settings-btn settings-btn-danger"
                      disabled={installBusy !== null}
                      onClick={() => void uninstall(agent.agent_id)}
                      data-testid="agent-uninstall-button"
                    >
                      {busy && installBusy?.action === 'uninstall' ? t('settings.agents.uninstalling') : t('settings.agents.uninstall')}
                    </button>
                  )}
                  {agent.lifecycle !== 'deleted' && !agent.deprecated && agent.installed && (
                    <button
                      className="settings-btn settings-btn-secondary agent-check-btn"
                      disabled={managementPending !== ''}
                      onClick={() => void revalidate(agent)}
                      title={t('settings.agents.checkAvailabilityHint')}
                      aria-label={t('settings.agents.checkAvailabilityFor', { name: agent.display_name })}
                      aria-describedby={checkFeedback ? `agent-check-feedback-${agent.agent_id}` : undefined}
                    >
                      {checking && <span className="agent-check-spinner" aria-hidden="true" />}
                      {t(checking ? 'settings.agents.checkingAvailability' : 'settings.agents.checkAvailability')}
                    </button>
                  )}
                  {agent.source === 'custom' && agent.lifecycle === 'active' && (
                    <>
                      <button className="settings-btn settings-btn-secondary" disabled={managementPending !== ''} onClick={() => openCustomForm(agent)}>
                        {t('common.edit')}
                      </button>
                      <button className="settings-btn settings-btn-danger" disabled={managementPending !== ''} onClick={() => void deleteCustom(agent)}>
                        {t('common.delete')}
                      </button>
                      <button className="settings-btn settings-btn-danger" disabled={managementPending !== ''} onClick={() => void deleteCustom(agent, true)}>
                        {t('settings.agents.forceDelete')}
                      </button>
                    </>
                  )}
                  {agent.source === 'custom' && agent.lifecycle === 'deleting' && (
                    <button className="settings-btn settings-btn-danger" disabled={managementPending !== ''} onClick={() => void deleteCustom(agent, true)}>
                      {t('common.retry')}
                    </button>
                  )}
                  {agent.source === 'custom' && agent.lifecycle === 'deleted' && (
                    <button className="settings-btn settings-btn-secondary" disabled={managementPending !== ''} onClick={() => openCustomForm(agent, true)}>
                      {t('common.restore')}
                    </button>
                  )}
                </div>
              </div>

              {checkFeedback && (
                <div
                  id={`agent-check-feedback-${agent.agent_id}`}
                  className={`agent-check-feedback ${checkFeedback.status}`}
                  role={checkFeedback.status === 'error' ? 'alert' : 'status'}
                  aria-live={checkFeedback.status === 'error' ? 'assertive' : 'polite'}
                >
                  <span className="agent-check-feedback-icon" aria-hidden="true">
                    {checkFeedback.status === 'checking'
                      ? <span className="agent-check-spinner" />
                      : checkFeedback.status === 'success'
                        ? '✓'
                        : checkFeedback.status === 'warning'
                          ? '!'
                          : '×'}
                  </span>
                  <div className="agent-check-feedback-content">
                    <strong>
                      {t(`settings.agents.checkStatus.${checkFeedback.status}`)}
                    </strong>
                    <pre>{checkFeedback.message}</pre>
                  </div>
                </div>
              )}

              <div className="agent-install-card-body">
                <div className="agent-install-meta">
                  <span className={`agent-status agent-status-${agent.availability}`}>
                    {t(`settings.agents.status.${agent.availability}`)}
                  </span>
                  {agent.refreshing && <span>{t('settings.agents.status.refreshing')}</span>}
                  {agent.current_revision && <code className="agent-install-rev">{agent.current_revision}</code>}
                </div>
                <div className="agent-install-row">
                  <span className="agent-install-label">Agent ID</span>
                  <code>{agent.agent_id}</code>
                </div>
                <div className="agent-install-row">
                  <span className="agent-install-label">{t('settings.agents.launchDefinition')}</span>
                  <code>{command}</code>
                </div>
                {agent.install_commands && agent.install_commands.length > 0 && (
                  <div className="agent-install-row">
                    <span className="agent-install-label">{t('settings.agents.installCommands')}</span>
                    <div className="agent-install-commands">
                      {agent.install_commands.map((cmd) => <code key={cmd}>{cmd}</code>)}
                    </div>
                  </div>
                )}
                {showManual && (
                  <div className="agent-install-instructions">{agent.install_instructions}</div>
                )}
                {(agent.availability_error || agent.delete_error) && (
                  <pre className="agent-directory-error">{agent.availability_error || agent.delete_error}</pre>
                )}
                {agent.last_validation_status && agent.last_validation_at ? (
                  <div className="agent-install-meta">
                    <span>
                      {t('settings.agents.lastValidation')}: {t(`settings.agents.status.${agent.last_validation_status}`)}
                    </span>
                    <span>{new Date(agent.last_validation_at).toLocaleString()}</span>
                  </div>
                ) : null}
                {agent.availability === 'not_installed' && agent.last_validation_error && (
                  <pre className="agent-directory-error">{agent.last_validation_error}</pre>
                )}
                <details className="agent-directory-revisions" onToggle={(event) => {
                  if (event.currentTarget.open) void loadRevisions(agent.agent_id);
                }}>
                  <summary>{t('settings.agents.revisions')}</summary>
                  {(revisionMap[agent.agent_id] || []).map((revision) => (
                    <div key={revision.revision} className="agent-directory-revision">
                      <code>{revision.revision}</code>
                      <span>{revision.definition.acp_program}</span>
                      {revision.definition.acp_args.map((arg, index) => (
                        <code key={`${revision.revision}-${index}`}>{arg}</code>
                      ))}
                    </div>
                  ))}
                </details>
              </div>

              {requestErrors[agent.agent_id] && (
                <div className="agent-install-request-error">{requestErrors[agent.agent_id]}</div>
              )}
            </div>
          );
        })}
      </div>

      {form && (
        <div className="agent-custom-form-overlay" onMouseDown={(event) => {
          if (event.target === event.currentTarget) setForm(null);
        }}>
          <div className="agent-custom-form-modal">
            <div className="agent-custom-form-modal-header">
              <strong>{form.agentId ? (form.restore ? t('common.restore') : t('common.edit')) : t('common.add')}</strong>
              <button className="agent-install-result-dismiss" onClick={() => setForm(null)}>×</button>
            </div>
            <div className="agent-custom-form">
              <div className="settings-form-group">
                <label className="settings-label">{t('settings.agents.form.displayName')}</label>
                <input className="settings-input" value={form.displayName} onChange={(event) => setForm({ ...form, displayName: event.target.value })} />
              </div>
              <div className="settings-form-group">
                <label className="settings-label">{t('settings.agents.form.iconUrl')}</label>
                <input className="settings-input" value={form.iconUrl} onChange={(event) => setForm({ ...form, iconUrl: event.target.value })} />
              </div>
              <div className="settings-form-group">
                <label className="settings-label">{t('settings.agents.form.bin')}</label>
                <input className="settings-input" value={form.bin} onChange={(event) => setForm({ ...form, bin: event.target.value })} />
              </div>
              <div className="settings-form-group">
                <label className="settings-label">{t('settings.agents.form.acpProgram')}</label>
                <input className="settings-input" value={form.acpProgram} onChange={(event) => setForm({ ...form, acpProgram: event.target.value })} />
              </div>
              <div className="settings-form-group">
                <label className="settings-label">{t('settings.agents.form.arguments')}</label>
                <textarea className="settings-input" rows={4} value={form.argsText} onChange={(event) => setForm({ ...form, argsText: event.target.value })} />
              </div>
              {(!form.agentId || form.restore) && (
                <div className="settings-form-group">
                  <label className="settings-label">{t('settings.agents.form.environment')}</label>
                  <textarea className="settings-input" rows={4} value={form.environmentText} onChange={(event) => setForm({ ...form, environmentText: event.target.value })} placeholder="KEY=value" />
                </div>
              )}
              <label className="agent-custom-checkbox">
                <input type="checkbox" checked={form.supportsHeadlessPrint} onChange={(event) => setForm({ ...form, supportsHeadlessPrint: event.target.checked })} />
                <span>bin -p prompt</span>
              </label>
              <div className="settings-btn-group">
                <button className="settings-btn settings-btn-secondary" onClick={() => setForm(null)}>{t('common.cancel')}</button>
                <button className="settings-btn settings-btn-primary" disabled={managementPending !== ''} onClick={() => void submitCustom()}>{t('common.save')}</button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
