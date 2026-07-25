// Agent subscription / quota info shown on the Home page for the Codex and
// Claude ACP agents. Fetched fresh on every agent-type switch.

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
  name?: string;
  key_suffix?: string;
  version?: string; // e.g. "v2.1.202"
  today_cost: number;
  total_cost: number;
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

export type AgentUsageProvider = 'codex' | 'claude' | 'antigravity' | 'kimi' | 'qoder';

// agentUsageProvider maps a selected agent to a usage provider, or null when
// the agent has no quota view. ACP agent `type` is the full serve
// command (e.g. "codex-acp", "antigravity-acp"), so match on the command and
// display name together.
export function agentUsageProvider(
  agentType?: string,
  displayName?: string,
): AgentUsageProvider | null {
  const s = `${agentType || ''} ${displayName || ''}`.toLowerCase();
  if (s.includes('antigravity')) return 'antigravity';
  if (s.includes('codex')) return 'codex';
  if (s.includes('claude')) return 'claude';
  if (s.includes('qoder') || s.includes('qcode')) return 'qoder';
  if (s.includes('kimi')) return 'kimi';
  return null;
}

export interface AgentUsagePayload {
  codex?: CodexUsage;
  claude?: ClaudeUsage;
  antigravity?: AntigravityUsage;
  kimi?: KimiUsage;
  qoder?: QoderUsage;
}

export async function fetchAgentUsage(provider: AgentUsageProvider): Promise<AgentUsagePayload> {
  // `cache: 'no-store'` is required: this quota reading changes continuously
  // (Codex windows especially), so a browser/intermediary HTTP-cache hit
  // would serve an old snapshot and — since the result is re-written to the
  // localStorage cache — make the stale value stick across refreshes.
  const res = await fetch(`/api/v1/agent/usage?type=${provider}`, { cache: 'no-store' });
  const data = await res.json().catch(() => null);
  if (!res.ok || !data || data.code !== 0) {
    const msg =
      (data && (data.msg || data.message || data.error)) ||
      `get agent usage failed (status ${res.status})`;
    throw new Error(msg);
  }
  return { codex: data.codex, claude: data.claude, antigravity: data.antigravity, kimi: data.kimi, qoder: data.qoder };
}

// fetchAgentVersion returns the installed CLI version of a known ACP agent
// (e.g. "v1.17.18"), keyed by its serve command (the agent's `type`). Used for
// every known agent that has no quota view of its own — the backend resolves
// the command to a binary and runs `<bin> --version`. Returns "" when the agent
// advertises no parseable version; throws on request / unknown-command errors.
export async function fetchAgentVersion(command: string): Promise<string> {
  const res = await fetch(`/api/v1/agent/version?command=${encodeURIComponent(command)}`, {
    cache: 'no-store',
  });
  const data = await res.json().catch(() => null);
  if (!res.ok || !data || data.code !== 0) {
    const msg =
      (data && (data.msg || data.message || data.error)) ||
      `get agent version failed (status ${res.status})`;
    throw new Error(msg);
  }
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
): CodexUsage | ClaudeUsage | AntigravityUsage | KimiUsage | QoderUsage | null {
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
