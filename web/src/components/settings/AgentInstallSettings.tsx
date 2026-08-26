import { useCallback, useEffect, useRef, useState } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { isImageUrl, resolveIconSrc } from '../../utils/url';
import { clearDeletedAgentLocalPreferences } from '../../utils/workspace';
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

type InstallAction = 'install' | 'upgrade' | 'uninstall';

interface AgentVersionComponent {
  name: string;
  kind: 'npm' | 'binary';
  current_version?: string;
  latest_version?: string;
  update_available: boolean;
  error?: string;
}

interface AgentVersionInfo {
  agent_id: string;
  components: AgentVersionComponent[];
  update_available: boolean;
  upgrade_supported: boolean;
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

interface InstallRequestFailure {
  action: InstallAction;
  kind: 'busy' | 'error';
  detail: string;
}

interface BatchUpgradeProgress {
  completed: number;
  total: number;
  currentAgentId: string;
  currentAgentName: string;
}

type UpgradeRequestOutcome = 'result' | 'error' | 'conflict';

interface ParsedVersionResponse {
  agents: AgentVersionInfo[];
  checkedAt: number;
}

interface ResponseShapeIssue {
  key: string;
  values?: Record<string, unknown>;
}

class ResponseError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ResponseError';
    this.status = status;
  }
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

type JSONResponseParser<T> = (
  data: Record<string, unknown>,
  context: string,
  raw: string,
) => T;

async function readResponse<T = Record<string, unknown>>(
  t: TFunction,
  res: Response,
  method = 'GET',
  requestURL = res.url,
  parse?: JSONResponseParser<T>,
): Promise<T> {
  const context = `${method} ${requestURL}\nHTTP ${res.status}`;
  const raw = await res.text().catch((err) => {
    throw new ResponseError(res.status, `${context}\n\n${t('settings.agents.diagnostics.readResponseFailed', {
      error: String(err),
    })}`);
  });
  let data: Record<string, unknown> | null = null;
  if (raw) {
    try {
      data = JSON.parse(raw) as Record<string, unknown>;
    } catch (err) {
      throw new ResponseError(res.status, `${context}\n\n${t('settings.agents.diagnostics.invalidJSONResponse', {
        error: String(err),
      })}\n\n${raw}`);
    }
  }
  if (!res.ok || data?.code !== 0) {
    throw new ResponseError(res.status, `${context}\n\n${raw || t('settings.agents.diagnostics.emptyResponseBody')}`);
  }
  const responseData = data as Record<string, unknown>;
  return parse ? parse(responseData, context, raw) : responseData as T;
}

async function requestJSON<T = Record<string, unknown>>(
  t: TFunction,
  url: string,
  init?: RequestInit,
  parse?: JSONResponseParser<T>,
): Promise<T> {
  const method = init?.method || 'GET';
  let response: Response;
  try {
    response = await fetch(url, init);
  } catch (err) {
    throw new ResponseError(0, `${method} ${url}\n\n${String(err)}`);
  }
  return readResponse(t, response, method, url, parse);
}

function toInstallRequestFailure(err: unknown, action: InstallAction): InstallRequestFailure {
  return {
    action,
    kind: err instanceof ResponseError && err.status === 409 ? 'busy' : 'error',
    detail: err instanceof Error ? err.message : String(err),
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function invalidResponseShape(
  t: TFunction,
  context: string,
  raw: string,
  issue: ResponseShapeIssue,
): Error {
  return new Error(`${context}\n\n${t('settings.agents.diagnostics.invalidResponseShape', {
    reason: t(issue.key, issue.values),
  })}\n\n${raw || t('settings.agents.diagnostics.emptyResponseBody')}`);
}

function hasOptionalString(record: Record<string, unknown>, key: string): boolean {
  return !(key in record) || typeof record[key] === 'string';
}

function hasOptionalBoolean(record: Record<string, unknown>, key: string): boolean {
  return !(key in record) || typeof record[key] === 'boolean';
}

function isCatalogAgent(value: unknown): value is CatalogAgent {
  if (!isRecord(value) || !isRecord(value.definition)) return false;
  const definition = value.definition;
  return typeof value.agent_id === 'string'
    && value.agent_id.trim() !== ''
    && (value.source === 'builtin' || value.source === 'custom')
    && typeof value.display_name === 'string'
    && typeof value.icon_url === 'string'
    && typeof definition.bin === 'string'
    && typeof definition.acp_program === 'string'
    && Array.isArray(definition.acp_args)
    && definition.acp_args.every((arg) => typeof arg === 'string')
    && typeof value.supports_headless_print === 'boolean'
    && typeof value.deprecated === 'boolean'
    && typeof value.lifecycle === 'string'
    && typeof value.installed === 'boolean'
    && typeof value.availability === 'string'
    && hasOptionalString(value, 'current_revision')
    && hasOptionalString(value, 'install_method')
    && (!('install_commands' in value)
      || (Array.isArray(value.install_commands) && value.install_commands.every((command) => typeof command === 'string')))
    && hasOptionalString(value, 'install_instructions')
    && hasOptionalBoolean(value, 'auto_installable')
    && hasOptionalBoolean(value, 'auto_uninstallable')
    && hasOptionalString(value, 'availability_error')
    && hasOptionalString(value, 'last_validation_status')
    && hasOptionalString(value, 'last_validation_error')
    && (!('last_validation_at' in value)
      || (typeof value.last_validation_at === 'number' && Number.isFinite(value.last_validation_at)))
    && hasOptionalString(value, 'delete_error')
    && hasOptionalBoolean(value, 'refreshing');
}

function normalizeCatalogAgent(value: unknown): unknown {
  if (!isRecord(value) || !isRecord(value.definition) || value.definition.acp_args !== null) {
    return value;
  }
  return {
    ...value,
    definition: {
      ...value.definition,
      acp_args: [],
    },
  };
}

function readCatalogAgents(
  t: TFunction,
  data: Record<string, unknown>,
  context: string,
  raw: string,
  listShapeKey: string,
): CatalogAgent[] {
  if (!Array.isArray(data.agents)) {
    throw invalidResponseShape(t, context, raw, { key: listShapeKey });
  }
  const agents = data.agents.map(normalizeCatalogAgent);
  if (!agents.every(isCatalogAgent)) {
    throw invalidResponseShape(t, context, raw, {
      key: 'settings.agents.diagnostics.shape.catalogAgentFields',
    });
  }
  return agents;
}

function isVersionComponent(value: unknown): value is AgentVersionComponent {
  return isRecord(value)
    && typeof value.name === 'string'
    && (value.kind === 'npm' || value.kind === 'binary')
    && typeof value.update_available === 'boolean'
    && hasOptionalString(value, 'current_version')
    && hasOptionalString(value, 'latest_version')
    && hasOptionalString(value, 'error');
}

function isVersionInfo(value: unknown): value is AgentVersionInfo {
  return isRecord(value)
    && typeof value.agent_id === 'string'
    && value.agent_id.trim() !== ''
    && Array.isArray(value.components)
    && value.components.every(isVersionComponent)
    && typeof value.update_available === 'boolean'
    && typeof value.upgrade_supported === 'boolean';
}

function readVersionResponse(
  t: TFunction,
  data: Record<string, unknown>,
  context: string,
  raw: string,
): ParsedVersionResponse {
  if (!Array.isArray(data.agents)) {
    throw invalidResponseShape(t, context, raw, {
      key: 'settings.agents.diagnostics.shape.versionsAgentsArray',
    });
  }
  if (!data.agents.every((item) => isRecord(item))) {
    throw invalidResponseShape(t, context, raw, {
      key: 'settings.agents.diagnostics.shape.versionAgentFields',
    });
  }
  if (!data.agents.every((item) => Array.isArray(item.components) && item.components.every(isVersionComponent))) {
    throw invalidResponseShape(t, context, raw, {
      key: 'settings.agents.diagnostics.shape.versionComponentFields',
    });
  }
  const agents: AgentVersionInfo[] = [];
  for (const item of data.agents) {
    if (!isVersionInfo(item)) {
      throw invalidResponseShape(t, context, raw, {
        key: 'settings.agents.diagnostics.shape.versionAgentFields',
      });
    }
    agents.push(item);
  }
  if ('checked_at' in data && (typeof data.checked_at !== 'number' || !Number.isFinite(data.checked_at))) {
    throw invalidResponseShape(t, context, raw, {
      key: 'settings.agents.diagnostics.shape.versionsCheckedAtNumber',
    });
  }
  return {
    agents,
    checkedAt: typeof data.checked_at === 'number' ? data.checked_at : Date.now(),
  };
}

function readUpgradeResult(
  t: TFunction,
  data: Record<string, unknown>,
  expectedAgentId: string,
  context: string,
  raw: string,
): InstallResult {
  const result = data.result;
  let issue: ResponseShapeIssue | null = null;
  if (!isRecord(result)) {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeResultObject' };
  } else if (!Array.isArray(result.steps)) {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeStepsArray' };
  } else if (typeof result.installed !== 'boolean') {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeInstalledBoolean' };
  } else if (!result.steps.every((step) => isRecord(step))) {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeStepObjects' };
  } else if (typeof result.agent_id !== 'string' || result.agent_id.trim() === '') {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeAgentIdNonEmpty' };
  } else if (result.agent_id !== expectedAgentId) {
    issue = {
      key: 'settings.agents.diagnostics.shape.upgradeAgentIdMismatch',
      values: { agentId: JSON.stringify(expectedAgentId) },
    };
  } else if ('install_error' in result && typeof result.install_error !== 'string') {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeInstallErrorString' };
  } else if (!result.steps.every((step) => (
    typeof step.display === 'string'
      && typeof step.stdout === 'string'
      && typeof step.stderr === 'string'
      && Number.isInteger(step.exit_code)
      && typeof step.timed_out === 'boolean'
      && typeof step.duration_ms === 'number'
      && Number.isInteger(step.duration_ms)
      && step.duration_ms >= 0
      && (!('error' in step) || typeof step.error === 'string')
  ))) {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeStepFields' };
  } else if ('validation' in result && (
    !isRecord(result.validation)
      || typeof result.validation.ok !== 'boolean'
      || ('error' in result.validation && typeof result.validation.error !== 'string')
  )) {
    issue = { key: 'settings.agents.diagnostics.shape.upgradeValidationShape' };
  }
  if (issue) {
    throw invalidResponseShape(t, context, raw, issue);
  }
  return result as unknown as InstallResult;
}

// AgentInstallSettings manages the full Agent catalog, checks installed
// component versions and runs catalog-controlled install/upgrade flows. The
// backend only accepts an agent_id; complete step output, recheck and
// validation results remain visible in the UI.
export function AgentInstallSettings() {
  const { t } = useTranslation();
  const [catalog, setCatalog] = useState<CatalogAgent[]>([]);
  const [loading, setLoading] = useState(true);
  const [hasLoadedCatalog, setHasLoadedCatalog] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [catalogRefreshError, setCatalogRefreshError] = useState('');
  // Which agent is currently running an install or uninstall flow. The backend
  // serializes both under one lock, so all install/uninstall buttons disable
  // while any one runs.
  const [installBusy, setInstallBusy] = useState<{ id: string; action: InstallAction } | null>(null);
  // Results survive the catalog refresh so the user can still inspect the full
  // step output after the card's button state flips.
  const [results, setResults] = useState<Record<string, { action: InstallAction; result: InstallResult }>>({});
  const [requestErrors, setRequestErrors] = useState<Record<string, InstallRequestFailure | undefined>>({});
  const [form, setForm] = useState<CustomFormState | null>(null);
  const [managementPending, setManagementPending] = useState('');
  const [managementMessage, setManagementMessage] = useState('');
  const [deleteResult, setDeleteResult] = useState<DeleteResult | null>(null);
  const [revisionMap, setRevisionMap] = useState<Record<string, Array<{ revision: string; definition: RuntimeDefinition }>>>({});
  const [validationFeedback, setValidationFeedback] = useState<Record<string, ValidationFeedback>>({});
  const [versions, setVersions] = useState<Record<string, AgentVersionInfo>>({});
  const [versionChecking, setVersionChecking] = useState(false);
  const [versionError, setVersionError] = useState('');
  const [versionsCheckedAt, setVersionsCheckedAt] = useState<number | null>(null);
  const [batchUpgrade, setBatchUpgrade] = useState<BatchUpgradeProgress | null>(null);
  const batchUpgradeRunningRef = useRef(false);
  const installActionRunningRef = useRef(false);

  const loadData = useCallback(async (showLoading = true, preserveContentOnError = false) => {
    if (showLoading) setLoading(true);
    if (preserveContentOnError) {
      setCatalogRefreshError('');
    } else {
      setLoadError('');
    }
    try {
      const [catalogData, deletedData] = await Promise.all([
        requestJSON(t, '/api/v1/agent/catalog', undefined, (data, context, raw) => (
          readCatalogAgents(t, data, context, raw, 'settings.agents.diagnostics.shape.catalogAgentsArray')
        )),
        requestJSON(t, '/api/v1/agent/catalog/deleted', undefined, (data, context, raw) => (
          readCatalogAgents(t, data, context, raw, 'settings.agents.diagnostics.shape.catalogDeletedAgentsArray')
        )),
      ]);
      setCatalog([...catalogData, ...deletedData]);
      setHasLoadedCatalog(true);
      setCatalogRefreshError('');
    } catch (err) {
      if (preserveContentOnError) {
        setCatalogRefreshError(err instanceof Error ? err.message : String(err));
      } else {
        setLoadError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      if (showLoading) setLoading(false);
    }
  }, [t]);

  const loadVersions = useCallback(async (force = false) => {
    setVersionChecking(true);
    setVersionError('');
    try {
      const query = force ? '?force=1' : '';
      const url = `/api/v1/agent/versions${query}`;
      const data = await requestJSON(t, url, undefined, (response, context, raw) => (
        readVersionResponse(t, response, context, raw)
      ));
      setVersions(Object.fromEntries(data.agents.map((item) => [item.agent_id, item])));
      setVersionsCheckedAt(data.checkedAt);
    } catch (err) {
      setVersions({});
      setVersionsCheckedAt(null);
      setVersionError(err instanceof Error ? err.message : String(err));
    } finally {
      setVersionChecking(false);
    }
  }, [t]);

  useEffect(() => {
    void loadData();
    void loadVersions();
  }, [loadData, loadVersions]);

  const batchUpgradeCandidates = catalog.filter((agent) => {
    const version = versions[agent.agent_id];
    return agent.source === 'builtin'
      && !agent.deprecated
      && agent.installed
      && version?.update_available === true
      && version.upgrade_supported === true;
  });

  const openCustomForm = (agent?: CatalogAgent, restore = false) => {
    if (installActionRunningRef.current || batchUpgradeRunningRef.current || installBusy !== null || batchUpgrade !== null || managementPending !== '') return;
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
    if (!form || installActionRunningRef.current || batchUpgradeRunningRef.current || batchUpgrade !== null || installBusy !== null || managementPending !== '') return;
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
      const data = await requestJSON(t, url, {
        method: !form.agentId || form.restore ? 'POST' : 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
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
      await loadVersions(true);
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
      const url = `/api/v1/agent/${encodeURIComponent(agent.agent_id)}/revalidate`;
      const data = await requestJSON(t, url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      });
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
      const url = `/api/v1/agent/catalog/${encodeURIComponent(agentId)}`;
      const data = await requestJSON(t, url);
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
      const impactURL = `/api/v1/agent/custom/${encodeURIComponent(agent.agent_id)}/delete-impact`;
      const impactData = await requestJSON(t, impactURL);
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
      const deleteURL = `/api/v1/agent/custom/${encodeURIComponent(agent.agent_id)}/delete`;
      const resultData = await requestJSON(t, deleteURL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ force }),
      });
      setDeleteResult(resultData.result as DeleteResult);
      setManagementMessage(t('settings.agents.deleteSuccess'));
      window.dispatchEvent(new CustomEvent('quartet:agent-catalog-changed', {
        detail: { agentId: agent.agent_id },
      }));
      clearDeletedAgentLocalPreferences(agent.agent_id);
      await loadData();
      await loadVersions(true);
    } catch (err) {
      setManagementMessage(err instanceof Error ? err.message : String(err));
      await loadData();
    } finally {
      setManagementPending('');
    }
  };

  const install = async (agentId: string) => {
    if (installActionRunningRef.current || batchUpgradeRunningRef.current || managementPending !== '' || form !== null) return;
    installActionRunningRef.current = true;
    setInstallBusy({ id: agentId, action: 'install' });
    setRequestErrors((prev) => ({ ...prev, [agentId]: undefined }));
    setResults((prev) => {
      const next = { ...prev };
      delete next[agentId];
      return next;
    });
    try {
      const url = '/api/v1/agent/install';
      const data = await requestJSON(t, url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ agent_id: agentId }),
      });
      const result = data.result as InstallResult;
      setResults((prev) => ({ ...prev, [agentId]: { action: 'install', result } }));
      await loadData();
      await loadVersions(true);
    } catch (err) {
      setRequestErrors((prev) => ({ ...prev, [agentId]: toInstallRequestFailure(err, 'install') }));
    } finally {
      setInstallBusy(null);
      installActionRunningRef.current = false;
    }
  };

  const upgradeChanges = (agent: CatalogAgent) => {
    const version = versions[agent.agent_id];
    return (version?.components || [])
      .filter((component) => component.update_available)
      .map((component) => `${component.name}: ${component.current_version || t('settings.agents.version.notInstalled')} → ${component.latest_version || '?'}`)
      .join('\n');
  };

  const runUpgradeRequest = async (agentId: string): Promise<UpgradeRequestOutcome> => {
    setRequestErrors((prev) => ({ ...prev, [agentId]: undefined }));
    setResults((prev) => {
      const next = { ...prev };
      delete next[agentId];
      return next;
    });
    try {
      const url = `/api/v1/agent/${encodeURIComponent(agentId)}/upgrade`;
      const result = await requestJSON(t, url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      }, (data, context, raw) => readUpgradeResult(t, data, agentId, context, raw));
      setResults((prev) => ({ ...prev, [agentId]: { action: 'upgrade', result } }));
      return 'result';
    } catch (err) {
      setRequestErrors((prev) => ({ ...prev, [agentId]: toInstallRequestFailure(err, 'upgrade') }));
      return err instanceof ResponseError && err.status === 409 ? 'conflict' : 'error';
    }
  };

  const upgrade = async (agent: CatalogAgent, skipConfirm = false) => {
    if (installActionRunningRef.current || batchUpgradeRunningRef.current || managementPending !== '' || form !== null) return;
    const version = versions[agent.agent_id];
    if (!version?.update_available || !version.upgrade_supported) return;
    const changes = upgradeChanges(agent);
    if (!skipConfirm && !window.confirm(t('settings.agents.version.upgradeConfirm', {
      name: agent.display_name,
      changes,
    }))) return;

    installActionRunningRef.current = true;
    setInstallBusy({ id: agent.agent_id, action: 'upgrade' });
    try {
      await runUpgradeRequest(agent.agent_id);
    } finally {
      try {
        await Promise.all([
          loadData(false, true),
          loadVersions(true),
        ]);
      } finally {
        setInstallBusy(null);
        installActionRunningRef.current = false;
      }
    }
  };

  const upgradeAll = async () => {
    const candidates = [...batchUpgradeCandidates];
    if (batchUpgradeRunningRef.current || installActionRunningRef.current || candidates.length === 0 || installBusy !== null || managementPending !== '' || versionChecking || form !== null) return;
    batchUpgradeRunningRef.current = true;

    const summary = candidates.map((agent) => {
      const changes = upgradeChanges(agent)
        .split('\n')
        .filter(Boolean)
        .map((change) => `  ${change}`)
        .join('\n');
      return `${agent.display_name} (${agent.agent_id})${changes ? `\n${changes}` : ''}`;
    }).join('\n\n');
    if (!window.confirm(t('settings.agents.version.upgradeAllConfirm', {
      count: candidates.length,
      agents: summary,
    }))) {
      batchUpgradeRunningRef.current = false;
      return;
    }

    setBatchUpgrade({
      completed: 0,
      total: candidates.length,
      currentAgentId: candidates[0].agent_id,
      currentAgentName: candidates[0].display_name,
    });
    try {
      for (let index = 0; index < candidates.length; index += 1) {
        const agent = candidates[index];
        setInstallBusy({ id: agent.agent_id, action: 'upgrade' });
        setBatchUpgrade({
          completed: index,
          total: candidates.length,
          currentAgentId: agent.agent_id,
          currentAgentName: agent.display_name,
        });
        const outcome = await runUpgradeRequest(agent.agent_id);
        setBatchUpgrade({
          completed: index + 1,
          total: candidates.length,
          currentAgentId: agent.agent_id,
          currentAgentName: agent.display_name,
        });
        if (outcome === 'conflict') break;
      }
    } finally {
      try {
        await loadData(false, true);
        await loadVersions(true);
      } finally {
        setInstallBusy(null);
        setBatchUpgrade(null);
        batchUpgradeRunningRef.current = false;
      }
    }
  };

  const uninstall = async (agentId: string) => {
    if (installActionRunningRef.current || batchUpgradeRunningRef.current || managementPending !== '' || form !== null) return;
    installActionRunningRef.current = true;
    setInstallBusy({ id: agentId, action: 'uninstall' });
    setRequestErrors((prev) => ({ ...prev, [agentId]: undefined }));
    setResults((prev) => {
      const next = { ...prev };
      delete next[agentId];
      return next;
    });
    try {
      const url = `/api/v1/agent/${encodeURIComponent(agentId)}/uninstall`;
      const data = await requestJSON(t, url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      });
      const result = data.result as InstallResult;
      setResults((prev) => ({ ...prev, [agentId]: { action: 'uninstall', result } }));
      await loadData();
      await loadVersions(true);
    } catch (err) {
      setRequestErrors((prev) => ({ ...prev, [agentId]: toInstallRequestFailure(err, 'uninstall') }));
    } finally {
      setInstallBusy(null);
      installActionRunningRef.current = false;
    }
  };

  const retryInstallAction = (agent: CatalogAgent, action: InstallAction) => {
    if (action === 'upgrade') {
      void upgrade(agent, true);
      return;
    }
    if (action === 'uninstall') {
      void uninstall(agent.agent_id);
      return;
    }
    void install(agent.agent_id);
  };

  const dismissRequestError = (agentId: string) => {
    setRequestErrors((prev) => ({ ...prev, [agentId]: undefined }));
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
      <img src={resolveIconSrc(icon)} alt="" className="agent-install-icon" referrerPolicy="no-referrer" />
    ) : (
      <span className="agent-install-icon agent-install-icon-emoji">{icon}</span>
    );

  const renderResult = (agentId: string, action: InstallAction, result: InstallResult) => {
    const agentName = catalog.find((agent) => agent.agent_id === agentId)?.display_name || agentId;
    const stepsSucceeded = result.steps.every((step) => step.exit_code === 0 && !step.error && !step.timed_out);
    const ok = action === 'uninstall'
      ? !result.installed && stepsSucceeded
      : result.installed && stepsSucceeded && result.validation?.ok;
    const summaryKey = action === 'uninstall'
      ? (ok ? 'settings.agents.result.uninstallSuccess' : 'settings.agents.result.uninstallFailed')
      : action === 'upgrade'
        ? ok
          ? 'settings.agents.result.upgradeSuccess'
          : result.installed && stepsSucceeded
            ? 'settings.agents.result.upgradeValidationFailed'
            : 'settings.agents.result.upgradeFailed'
      : ok
        ? 'settings.agents.result.success'
        : result.installed
          ? 'settings.agents.result.validationFailed'
          : 'settings.agents.result.failed';
    return (
      <div
        key={agentId}
        className={`agent-install-result ${ok ? 'success' : 'failure'}`}
        role={ok ? 'status' : 'alert'}
        aria-live={ok ? 'polite' : 'assertive'}
      >
        <div className="agent-install-result-header">
          <span>{agentName} · {t(summaryKey)}</span>
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

  const renderVersionInfo = (agent: CatalogAgent, info?: AgentVersionInfo) => {
    if (!agent.installed) return null;
    if (!info) {
      return versionChecking ? (
        <div className="agent-version-panel checking">
          <span className="agent-check-spinner" aria-hidden="true" />
          <span>{t('settings.agents.version.checking')}</span>
        </div>
      ) : null;
    }
    const hasKnownLatest = info.components.some((component) => !!component.latest_version);
    const status = info.update_available ? 'update' : hasKnownLatest ? 'current' : 'local';
    return (
      <div className={`agent-version-panel ${status}`}>
        <div className="agent-version-panel-head">
          <strong>{t(`settings.agents.version.status.${status}`)}</strong>
          {versionsCheckedAt && (
            <span>{t('settings.agents.version.checkedAt', { value: new Date(versionsCheckedAt).toLocaleString() })}</span>
          )}
        </div>
        <div className="agent-version-components">
          {info.components.map((component) => (
            <div key={`${component.kind}-${component.name}`} className="agent-version-component">
              <div className="agent-version-component-main">
                <code>{component.name}</code>
                <span className="agent-version-kind">{component.kind}</span>
                <span>
                  {component.current_version || t('settings.agents.version.notInstalled')}
                  {component.update_available && component.latest_version
                    ? ` → ${component.latest_version}`
                    : ''}
                </span>
                {component.update_available && (
                  <span className="agent-version-update-badge">{t('settings.agents.version.updateAvailable')}</span>
                )}
              </div>
              {component.error && <pre className="agent-version-component-error">{component.error}</pre>}
            </div>
          ))}
        </div>
      </div>
    );
  };

  if (loading) {
    return <div className="agent-install-loading">{t('common.loading')}</div>;
  }
  if (loadError && !hasLoadedCatalog) {
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

      <div className="agent-directory-toolbar">
        <div className="agent-directory-toolbar-title">
          <strong>{t('settings.tabs.agents')}</strong>
          {batchUpgradeCandidates.length > 0 && (
            <span className="agent-version-count">
              {t('settings.agents.version.agentUpdateCount', {
                count: batchUpgradeCandidates.length,
              })}
            </span>
          )}
        </div>
        <div className="agent-directory-toolbar-actions">
          <button
            className="settings-btn agent-upgrade-all-btn"
            disabled={batchUpgradeCandidates.length === 0 || versionChecking || installBusy !== null || managementPending !== '' || form !== null}
            onClick={() => void upgradeAll()}
            data-testid="agent-upgrade-all-button"
          >
            {batchUpgrade && <span className="agent-check-spinner" aria-hidden="true" />}
            {t('settings.agents.version.upgradeAll')}
          </button>
          <button
            className="settings-btn settings-btn-secondary agent-version-check-btn"
            disabled={versionChecking || installBusy !== null || managementPending !== ''}
            onClick={() => void loadVersions(true)}
          >
            {versionChecking && <span className="agent-check-spinner" aria-hidden="true" />}
            {t(versionChecking ? 'settings.agents.version.checking' : 'settings.agents.version.check')}
          </button>
          <button
            className="settings-btn settings-btn-primary"
            disabled={batchUpgrade !== null || installBusy !== null || managementPending !== ''}
            onClick={() => openCustomForm()}
            data-testid="agent-add-button"
          >
            {t('common.add')}
          </button>
        </div>
      </div>

      {batchUpgrade && (
        <div className="agent-batch-upgrade-progress" role="status" aria-live="polite" data-testid="agent-batch-upgrade-progress">
          <span className="agent-check-spinner" aria-hidden="true" />
          <div>
            <strong>{t('settings.agents.version.batchProgress', {
              completed: batchUpgrade.completed,
              total: batchUpgrade.total,
            })}</strong>
            <span>{t('settings.agents.version.batchCurrent', { name: batchUpgrade.currentAgentName })}</span>
            <code>{batchUpgrade.currentAgentId}</code>
          </div>
        </div>
      )}

      {catalogRefreshError && (
        <pre className="agent-install-request-error" role="alert">{catalogRefreshError}</pre>
      )}
      {loadError && hasLoadedCatalog && (
        <pre className="agent-install-request-error" role="alert">{loadError}</pre>
      )}

      {Object.entries(results).map(([agentId, entry]) => renderResult(agentId, entry.action, entry.result))}

      {versionError && (
        <pre className="agent-install-request-error">{versionError}</pre>
      )}

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
          const requestError = requestErrors[agent.agent_id];
          const checkFeedback = validationFeedback[agent.agent_id];
          const checking = checkFeedback?.status === 'checking';
          const versionInfo = versions[agent.agent_id];
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
                      disabled={installBusy !== null || managementPending !== '' || form !== null}
                      onClick={() => void install(agent.agent_id)}
                      data-testid="agent-install-button"
                    >
                      {busy && installBusy?.action === 'install'
                        ? t('settings.agents.installing')
                        : t('settings.agents.install')}
                    </button>
                  )}
                  {agent.source === 'builtin' && !agent.deprecated && agent.installed
                    && versionInfo?.update_available && versionInfo.upgrade_supported && (
                    <button
                      className="settings-btn agent-upgrade-btn"
                      disabled={installBusy !== null || managementPending !== '' || form !== null}
                      onClick={() => void upgrade(agent)}
                      data-testid="agent-upgrade-button"
                    >
                      {busy && installBusy?.action === 'upgrade'
                        ? t('settings.agents.version.upgrading')
                        : t('settings.agents.version.upgrade')}
                    </button>
                  )}
                  {agent.source === 'builtin' && !agent.deprecated && agent.installed && agent.auto_uninstallable && (
                    <button
                      className="settings-btn settings-btn-danger"
                      disabled={installBusy !== null || managementPending !== '' || form !== null}
                      onClick={() => void uninstall(agent.agent_id)}
                      data-testid="agent-uninstall-button"
                    >
                      {busy && installBusy?.action === 'uninstall' ? t('settings.agents.uninstalling') : t('settings.agents.uninstall')}
                    </button>
                  )}
                  {agent.lifecycle !== 'deleted' && !agent.deprecated && agent.installed && (
                    <button
                      className="settings-btn settings-btn-secondary agent-check-btn"
                      disabled={managementPending !== '' || batchUpgrade !== null || installBusy !== null}
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
                      <button className="settings-btn settings-btn-secondary" disabled={managementPending !== '' || batchUpgrade !== null || installBusy !== null} onClick={() => openCustomForm(agent)}>
                        {t('common.edit')}
                      </button>
                      <button className="settings-btn settings-btn-danger" disabled={managementPending !== '' || batchUpgrade !== null || installBusy !== null} onClick={() => void deleteCustom(agent)}>
                        {t('common.delete')}
                      </button>
                      <button className="settings-btn settings-btn-danger" disabled={managementPending !== '' || batchUpgrade !== null || installBusy !== null} onClick={() => void deleteCustom(agent, true)}>
                        {t('settings.agents.forceDelete')}
                      </button>
                    </>
                  )}
                  {agent.source === 'custom' && agent.lifecycle === 'deleting' && (
                    <button className="settings-btn settings-btn-danger" disabled={managementPending !== '' || batchUpgrade !== null || installBusy !== null} onClick={() => void deleteCustom(agent, true)}>
                      {t('common.retry')}
                    </button>
                  )}
                  {agent.source === 'custom' && agent.lifecycle === 'deleted' && (
                    <button className="settings-btn settings-btn-secondary" disabled={managementPending !== '' || batchUpgrade !== null || installBusy !== null} onClick={() => openCustomForm(agent, true)}>
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

              {renderVersionInfo(agent, versionInfo)}

              {busy && installBusy && (
                <div className={`agent-install-progress ${installBusy.action === 'upgrade' ? 'upgrade' : ''}`} role="status" aria-live="polite">
                  <span className="agent-check-spinner" aria-hidden="true" />
                  <div>
                    <strong>{t(`settings.agents.request.progress.${installBusy.action}`)}</strong>
                    <span>{t('settings.agents.request.progressHint')}</span>
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

              {requestError && (
                <div
                  className={`agent-install-request-feedback ${requestError.kind}`}
                  role="alert"
                  data-testid="agent-install-request-feedback"
                >
                  <span className="agent-install-request-icon" aria-hidden="true">
                    {requestError.kind === 'busy' ? '…' : '×'}
                  </span>
                  <div className="agent-install-request-content">
                    <strong>
                      {t(`settings.agents.request.${requestError.kind}Title`, {
                        action: t(`settings.agents.request.action.${requestError.action}`),
                      })}
                    </strong>
                    <p>{t(`settings.agents.request.${requestError.kind}Hint`, {
                      action: t(`settings.agents.request.action.${requestError.action}`),
                    })}</p>
                    <pre>{requestError.detail}</pre>
                  </div>
                  <div className="agent-install-request-actions">
                    <button
                      type="button"
                      className="settings-btn settings-btn-secondary"
                      onClick={() => dismissRequestError(agent.agent_id)}
                    >
                      {t('common.close')}
                    </button>
                    <button
                      type="button"
                      className="settings-btn settings-btn-primary"
                      disabled={installBusy !== null
                        || managementPending !== ''
                        || batchUpgrade !== null
                        || form !== null
                        || versionChecking
                        || (requestError.action === 'upgrade'
                          && (!versionInfo?.update_available || !versionInfo.upgrade_supported))}
                      onClick={() => retryInstallAction(agent, requestError.action)}
                    >
                      {t('settings.agents.request.retry')}
                    </button>
                  </div>
                </div>
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
                <button
                  className="settings-btn settings-btn-primary"
                  disabled={managementPending !== '' || batchUpgrade !== null || installBusy !== null}
                  onClick={() => void submitCustom()}
                  data-testid="agent-custom-save-button"
                >
                  {t('common.save')}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
