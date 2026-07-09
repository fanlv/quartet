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
  reset_credits: number;
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
// full serve command (e.g. "npx @agentclientprotocol/codex-acp"), so match on
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
  const res = await fetch(`/api/v1/agent/usage?type=${provider}`);
  const data = await res.json().catch(() => null);
  if (!res.ok || !data || data.code !== 0) {
    const msg =
      (data && (data.msg || data.message || data.error)) ||
      `get agent usage failed (status ${res.status})`;
    throw new Error(msg);
  }
  return { codex: data.codex, claude: data.claude };
}

// Module-level cache of the last successful usage payload per provider. Lets
// the card show the previously-fetched plan info instantly when the user
// switches agent type (or the composer re-mounts) while a fresh request loads
// in the background — stale-while-revalidate, no loading flash.
const usageCache: { codex: CodexUsage | null; claude: ClaudeUsage | null } = {
  codex: null,
  claude: null,
};

export function getCachedUsage(provider: AgentUsageProvider): CodexUsage | ClaudeUsage | null {
  return provider === 'codex' ? usageCache.codex : usageCache.claude;
}

export function setCachedUsage(
  provider: AgentUsageProvider,
  data: { codex?: CodexUsage; claude?: ClaudeUsage },
): void {
  if (provider === 'codex') usageCache.codex = data.codex ?? null;
  else usageCache.claude = data.claude ?? null;
}
