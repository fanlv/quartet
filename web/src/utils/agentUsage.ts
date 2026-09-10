// Agent subscription / quota info shown on the Home page for supported ACP
// agents. Fetched fresh on every agent-type switch.

export interface UsageWindow {
  used_percent: number;
  limit_window_seconds: number;
  reset_after_seconds: number;
  reset_at: number;
}

export interface CodexUsage {
  email?: string;
  plan_type?: string;
  version?: string; // e.g. "v1.1.0"
  // Upstream field positions; use each window's limit_window_seconds for its duration.
  primary_window?: UsageWindow;
  secondary_window?: UsageWindow;
  reset_credits: number; // count of available rate-limit reset credits
  reset_credit_expiries?: number[]; // unix seconds, one per available credit, ascending
}

export interface ClaudeUsage {
  plan_type?: string;
  rate_limit_tier?: string;
  version?: string; // e.g. "v2.1.202"
  five_hour?: UsageWindow;
  seven_day?: UsageWindow;
  seven_day_opus?: UsageWindow;
  weekly_scoped?: Array<UsageWindow & { label: string }>;
  extra_usage?: {
    is_enabled: boolean;
    monthly_limit?: number;
    used_credits?: number;
    currency?: string;
  };
}

// Antigravity (agy) plan snapshot: the agy CLI version plus the two model
// groups' quota windows (Claude/GPT and Gemini), each with a 7-day (weekly) and
// a 5-hour bucket. Each window reuses UsageWindow — used_percent is the used
// share and reset_at the bucket's reset time. A window is absent when the API
// doesn't report that bucket.
export interface AntigravityUsage {
  version?: string; // e.g. "v1.1.1"
  claude_weekly?: UsageWindow; // Claude/GPT group, 7-day
  claude_5h?: UsageWindow; // Claude/GPT group, 5-hour
  gemini_weekly?: UsageWindow; // Gemini group, 7-day
  gemini_5h?: UsageWindow; // Gemini group, 5-hour
}

// Kimi Code plan snapshot: the kimi CLI version plus the three quota windows.
// weekly / five_hour are rate-limit windows with a reset time; total is the
// cumulative quota pool with no reset. A window is absent when the API doesn't
// report a usable limit for it.
export interface KimiUsage {
  version?: string; // e.g. "v0.1.0"
  parallel_limit?: number; // max concurrent sessions
  weekly?: UsageWindow; // 7-day quota
  five_hour?: UsageWindow; // 5-hour quota
  total?: UsageWindow; // cumulative quota, no reset
}

// QoderCN credits quota snapshot: a single cumulative credits pool (no
// rate-limit window reset). The pool expires wholesale at expires_at.
export interface QoderUsage {
  version?: string; // e.g. "v1.0.48"
  plan_type?: string; // e.g. "personal_professional_trial"
  unit?: string; // always "credits"
  total: number;
  used: number;
  remaining: number;
  used_percent: number; // 0–100
  expires_at?: number; // unix seconds
  quota_exceeded: boolean;
}

// Cursor plan snapshot from cursor.com's usage-summary endpoint plus the Grok
// Bot usage RPC. The three plan windows share the monthly billing-cycle reset
// (reset_at = billingCycleEnd, limit_window_seconds = cycle length):
// primary_window is the plan total, secondary_window the Auto lane,
// tertiary_window the API lane. grok_bot_window is absent when the account has
// no Grok Bot access or the API reports no usable percent/reset for it.
export interface CursorUsage {
  version?: string; // e.g. "v2026.09.08"
  membership_type?: string; // e.g. "pro" | "team" | "enterprise"
  primary_window?: UsageWindow; // plan total usage (Auto + API lanes)
  secondary_window?: UsageWindow; // Auto lane usage
  tertiary_window?: UsageWindow; // API lane usage
  grok_bot_window?: UsageWindow; // Grok Bot usage
}

export type AgentUsageProvider = 'codex' | 'claude' | 'antigravity' | 'kimi' | 'qoder' | 'cursor';

// agentUsageProvider maps a selected built-in agent to a usage provider, or
// null when the agent has no quota view. Match only stable built-in IDs and
// declared historical commands: display names are user-controlled for custom
// agents and must never opt them into another account's quota data.
export function agentUsageProvider(
  agentType?: string,
  _displayName?: string,
): AgentUsageProvider | null {
  const command = (agentType || '').trim().replace(/\s+/g, ' ').toLowerCase();
  if (['antigravity', 'agy', 'antigravity-acp'].includes(command)) return 'antigravity';
  if (['codex', 'codex-acp', 'npx @agentclientprotocol/codex-acp', 'npx @zed-industries/codex-acp'].includes(command)) return 'codex';
  if (['claude', 'claude-agent-acp', 'npx @agentclientprotocol/claude-agent-acp'].includes(command)) return 'claude';
  if (['qoderclicn', 'qoderclicn --acp', 'qwen', 'qwen --acp'].includes(command)) return 'qoder';
  if (['kimi', 'kimi acp'].includes(command)) return 'kimi';
  if (['cursor-agent', 'cursor-agent acp'].includes(command)) return 'cursor';
  return null;
}

export interface AgentUsagePayload {
  codex?: CodexUsage;
  claude?: ClaudeUsage;
  antigravity?: AntigravityUsage;
  kimi?: KimiUsage;
  qoder?: QoderUsage;
  cursor?: CursorUsage;
}

async function readJSONResponse(response: Response, operation: string): Promise<Record<string, unknown>> {
  const body = await response.text();
  let data: Record<string, unknown> | null = null;
  if (body) {
    try {
      const parsed: unknown = JSON.parse(body);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        data = parsed as Record<string, unknown>;
      }
    } catch {
      // The raw response remains part of the error below.
    }
  }
  if (!response.ok || data?.code !== 0) {
    const detail = body || '(empty response body)';
    throw new Error(`${operation} failed (HTTP ${response.status}): ${detail}`);
  }
  if (!data) {
    throw new Error(`${operation} returned invalid JSON (HTTP ${response.status}): ${body || '(empty response body)'}`);
  }
  return data;
}

export async function fetchAgentUsage(provider: AgentUsageProvider): Promise<AgentUsagePayload> {
  // `cache: 'no-store'` is required: this quota reading changes continuously
  // (Codex windows especially), so a browser/intermediary HTTP-cache hit
  // would serve an old snapshot and — since the result is re-written to the
  // localStorage cache — make the stale value stick across refreshes.
  const url = `/api/v1/agent/usage?type=${provider}`;
  const operation = `GET ${new URL(url, window.location.href).toString()}`;
  let res: Response;
  try {
    res = await fetch(url, { cache: 'no-store' });
  } catch (error) {
    throw new Error(`${operation} failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  const data = await readJSONResponse(res, operation);
  return {
    codex: data.codex as CodexUsage | undefined,
    claude: data.claude as ClaudeUsage | undefined,
    antigravity: data.antigravity as AntigravityUsage | undefined,
    kimi: data.kimi as KimiUsage | undefined,
    qoder: data.qoder as QoderUsage | undefined,
    cursor: data.cursor as CursorUsage | undefined,
  };
}

// fetchAgentVersion returns the installed CLI version of a known ACP agent
// (e.g. "v1.17.18"), keyed by its serve command (the agent's `type`). Used for
// every known agent that has no quota view of its own — the backend resolves
// the command to a binary and runs `<bin> --version`. Returns "" when the agent
// advertises no parseable version; throws on request / unknown-command errors.
export async function fetchAgentVersion(command: string): Promise<string> {
  const url = `/api/v1/agent/version?command=${encodeURIComponent(command)}`;
  const operation = `GET ${new URL(url, window.location.href).toString()}`;
  let res: Response;
  try {
    res = await fetch(url, { cache: 'no-store' });
  } catch (error) {
    throw new Error(`${operation} failed: ${error instanceof Error ? error.message : String(error)}`);
  }
  const data = await readJSONResponse(res, operation);
  return typeof data.version === 'string' ? data.version : '';
}

// Persistent cache of the last successful usage payload per provider, stored in
// localStorage. Lets the card show the previously-fetched plan info instantly —
// on page load, when the user switches agent type, or when the composer
// re-mounts — while a fresh request loads in the background
// (stale-while-revalidate, no loading flash). A failed refresh keeps whatever
// is cached here; with no cache the card shows nothing.
function cacheKey(provider: AgentUsageProvider): string {
  return `agentUsage_${provider}`;
}

export function getCachedUsage(
  provider: AgentUsageProvider,
): CodexUsage | ClaudeUsage | AntigravityUsage | KimiUsage | QoderUsage | CursorUsage | null {
  try {
    const raw = localStorage.getItem(cacheKey(provider));
    if (!raw) return null;
    const obj = JSON.parse(raw);
    return obj && typeof obj === 'object' ? obj : null;
  } catch {
    return null;
  }
}

export function setCachedUsage(provider: AgentUsageProvider, data: AgentUsagePayload): void {
  const value = data[provider];
  try {
    if (value) localStorage.setItem(cacheKey(provider), JSON.stringify(value));
    else localStorage.removeItem(cacheKey(provider));
  } catch {
    /* ignore quota / serialization errors */
  }
}

// Persistent cache of the last successful version string per agent command,
// keyed by the serve command so switching agents shows the previously-probed
// version instantly while a fresh probe runs in the background.
function versionCacheKey(command: string): string {
  return `agentVersion_${command}`;
}

export function getCachedVersion(command: string): string {
  try {
    return localStorage.getItem(versionCacheKey(command)) || '';
  } catch {
    return '';
  }
}

export function setCachedVersion(command: string, version: string): void {
  try {
    if (version) localStorage.setItem(versionCacheKey(command), version);
    else localStorage.removeItem(versionCacheKey(command));
  } catch {
    /* ignore quota / serialization errors */
  }
}
