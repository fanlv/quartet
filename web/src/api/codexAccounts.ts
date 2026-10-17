import { getCSRFToken } from '../auth';

export interface CodexAccount {
  id: string;
  name: string;
  note: string;
  auth_type: 'chatgpt' | 'api_key';
  email?: string;
  account_id?: string;
  workspace_id?: string;
  has_config: boolean;
  model?: string;
  provider?: string;
  created_at: number;
  updated_at: number;
  last_used_at?: number;
}

export interface CodexAccountsResponse {
  code: number;
  revision: string;
  live_revision: string;
  accounts: CodexAccount[];
  current: {
    home: string;
    account?: CodexAccount;
    managed_id?: string;
    status: string;
    has_auth: boolean;
    has_config: boolean;
    config_applied: boolean;
    error?: string;
    blocked_reason?: string;
    availability: string;
    validation_error?: string;
    validated_at?: number;
  };
  has_backup: boolean;
  operations: Array<{ id: string; action: string; account_id?: string; status: string; error?: string; finished_at: number }>;
  warning?: string;
  active_login?: CodexLogin;
}

export interface CodexLogin {
  id: string;
  name: string;
  status: 'running' | 'saved' | 'error' | 'cancelled';
  output: string;
  error?: string;
  account_id?: string;
}

const base = '/api/v1/agent/codex/accounts';

// randomUUID is unavailable on plain HTTP LAN addresses. getRandomValues is
// available there too, so keep the same UUID contract for local deployments.
export function newCodexOperationID(): string {
  if (crypto.randomUUID) return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export async function codexRequest<T = Record<string, unknown>>(path = '', init?: RequestInit): Promise<T> {
  const url = `${base}${path}`;
  const method = init?.method || 'GET';
  let response: Response;
  const headers = new Headers(init?.headers);
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', getCSRFToken());
  try { response = await fetch(url, { cache: 'no-store', ...init, headers }); }
  catch (err) { throw new Error(`${method} ${url}\n\n${String(err)}`); }
  const context = `${method} ${url}\nHTTP ${response.status}`;
  const raw = await response.text().catch((err) => { throw new Error(`${context}\n\n${String(err)}`); });
  let result: { code?: number };
  try { result = JSON.parse(raw); }
  catch (err) { throw new Error(`${context}\n\n${String(err)}\n\n${raw}`); }
  if (!response.ok || result?.code !== 0) throw new Error(`${context}\n\n${raw}`);
  return result as T;
}

export function fetchCodexAccounts(signal?: AbortSignal) {
  return codexRequest<CodexAccountsResponse>('', { signal });
}

export function mutateCodexAccount(
  state: CodexAccountsResponse,
  action: string,
  id: string | undefined,
  payload: Record<string, unknown> = {},
  operationId = newCodexOperationID(),
) {
  const path = id ? `/${encodeURIComponent(id)}${action === 'update' || action === 'delete' ? '' : `/${action}`}` : `/actions/${action}`;
  return codexRequest<CodexAccountsResponse>(path, {
    method: action === 'delete' ? 'DELETE' : action === 'update' ? 'PUT' : 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ revision: state.revision, live_revision: state.live_revision, operation_id: operationId, ...payload }),
  });
}
