import type { AgentInfo, ModelInfoACP } from '../components/ChatPage';

// AgentPrefs mirrors repository.AgentPrefs (Go). Per-ACP-agent-type favorites
// + defaults, keyed by agent type in the parent map.
export interface AgentPrefs {
  favorite_model_ids?: string[];
  default_model_id?: string;
  default_mode?: string;
  default_thought_level?: string;
}

export type AgentPrefsMap = Record<string, AgentPrefs>;

// Module-level cache so the home composer, JobChat and the settings tab don't
// each re-fetch settings. Mirrors the lightweight caching style of
// utils/workspace.ts. Call invalidateAgentPrefs() after a save to force a
// reload on the next fetch.
let cache: AgentPrefsMap | null = null;
let inflight: Promise<AgentPrefsMap> | null = null;

export function invalidateAgentPrefs(): void {
  cache = null;
  inflight = null;
}

export async function fetchAgentPrefs(force = false): Promise<AgentPrefsMap> {
  if (!force && cache) return cache;
  if (!force && inflight) return inflight;
  const p = (async () => {
    try {
      const res = await fetch('/api/v1/config/settings/get');
      const data = await res.json().catch(() => null);
      const prefs: AgentPrefsMap = data?.code === 0 && data.settings?.agent_prefs ? data.settings.agent_prefs : {};
      cache = prefs;
      return prefs;
    } catch (err) {
      console.error('Failed to fetch agent prefs:', err);
      cache = {};
      return cache;
    } finally {
      inflight = null;
    }
  })();
  inflight = p;
  return p;
}

// splitFavoriteModels partitions an agent's available models into a pinned
// "favorites" group (ordered by the saved favoriteIds order, honoring pin
// order) followed by the rest. When there are no favorites the list is
// returned unchanged so callers can render a single flat group as before.
export function splitFavoriteModels(
  available: ModelInfoACP[],
  favoriteIds?: string[],
): { favorites: ModelInfoACP[]; rest: ModelInfoACP[] } {
  if (!favoriteIds || favoriteIds.length === 0) {
    return { favorites: [], rest: available };
  }
  const favSet = new Set(favoriteIds);
  const favorites = favoriteIds
    .map((id) => available.find((m) => m.modelId === id))
    .filter((m): m is ModelInfoACP => Boolean(m));
  const rest = available.filter((m) => !favSet.has(m.modelId));
  return { favorites, rest };
}

export interface ResolvedAgentDefaults {
  modelId?: string;
  modeId?: string;
  thoughtLevelId?: string;
}

// resolveAgentDefaults is the single source of truth for "which model/mode/
// thought_level should an agent start on". It validates every saved default
// against the agent's live available list and falls back when stale.
//
//   model: workspaceDefaultModel (if valid) > pref.default_model_id (if valid)
//          > availableModels[0]
//   mode / thought_level: pref default (if valid) else undefined (leave the
//          backend-seeded current value untouched — no list[0] forcing)
export function resolveAgentDefaults(
  agent: AgentInfo,
  pref?: AgentPrefs,
  opts?: { workspaceDefaultModel?: string },
): ResolvedAgentDefaults {
  const out: ResolvedAgentDefaults = {};

  const models = agent.models?.availableModels;
  if (models && models.length > 0) {
    const has = (id?: string) => !!id && models.some((m) => m.modelId === id);
    if (has(opts?.workspaceDefaultModel)) {
      out.modelId = opts!.workspaceDefaultModel;
    } else if (has(pref?.default_model_id)) {
      out.modelId = pref!.default_model_id;
    } else {
      out.modelId = models[0].modelId;
    }
  }

  const modes = agent.modes?.availableModes;
  if (modes && pref?.default_mode && modes.some((m) => m.id === pref.default_mode)) {
    out.modeId = pref.default_mode;
  }

  const levels = agent.thoughtLevels?.availableThoughtLevels;
  if (levels && pref?.default_thought_level && levels.some((m) => m.id === pref.default_thought_level)) {
    out.thoughtLevelId = pref.default_thought_level;
  }

  return out;
}

// applyDefaultsToAgent returns a new AgentInfo with the resolved defaults
// written into the current* fields. Pure (does not mutate input).
export function applyDefaultsToAgent(agent: AgentInfo, resolved: ResolvedAgentDefaults): AgentInfo {
  let next = agent;
  if (resolved.modelId && next.models && next.models.currentModelId !== resolved.modelId) {
    next = { ...next, models: { ...next.models, currentModelId: resolved.modelId } };
  }
  if (resolved.modeId && next.modes && next.modes.currentModeId !== resolved.modeId) {
    next = { ...next, modes: { ...next.modes, currentModeId: resolved.modeId } };
  }
  if (resolved.thoughtLevelId && next.thoughtLevels && next.thoughtLevels.currentThoughtLevelId !== resolved.thoughtLevelId) {
    next = { ...next, thoughtLevels: { ...next.thoughtLevels, currentThoughtLevelId: resolved.thoughtLevelId } };
  }
  return next;
}
