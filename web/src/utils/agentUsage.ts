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
  primary_window?: UsageWindow; // 5-hour
  secondary_window?: UsageWindow; // 7-day
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

export type AgentUsageProvider = 'codex' | 'claude';

// agentUsageProvider maps a selected agent to a usage provider, or null when
// the agent has no quota view (eino, Gemini, etc.). ACP agent `type` is the
// full serve command (e.g. "codex-acp"), so match on
// the command and display name together.
export function agentUsageProvider(
  agentType?: string,
  displayName?: string,
): AgentUsageProvider | null {
  const s = `${agentType || ''} ${displayName || ''}`.toLowerCase();
  if (s.includes('codex')) return 'codex';
  if (s.includes('claude')) return 'claude';
  return null;
}

export async function fetchAgentUsage(
  provider: AgentUsageProvider,
): Promise<{ codex?: CodexUsage; claude?: ClaudeUsage }> {
  // `cache: 'no-store'` is required: this quota reading changes continuously
  // (the Codex 5h window especially), so a browser/intermediary HTTP-cache hit
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
  return { codex: data.codex, claude: data.claude };
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

export function getCachedUsage(provider: AgentUsageProvider): CodexUsage | ClaudeUsage | null {
  try {
    const raw = localStorage.getItem(cacheKey(provider));
    if (!raw) return null;
    const obj = JSON.parse(raw);
    return obj && typeof obj === 'object' ? obj : null;
  } catch {
    return null;
  }
}

export function setCachedUsage(
  provider: AgentUsageProvider,
  data: { codex?: CodexUsage; claude?: ClaudeUsage },
): void {
  const value = provider === 'codex' ? data.codex : data.claude;
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
