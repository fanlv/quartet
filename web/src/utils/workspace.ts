// Workspace-level helpers shared across components. Kept deliberately small.
//
// - Color strip: stored on the backend as a random hex (#rrggbb) assigned at
//   creation time (and backfilled for legacy records on first load). The
//   frontend reads it from the workspace record; only when a caller has nothing
//   but an id do we fall back to a stable hash-derived hue so the UI still
//   renders something reasonable.
// - Default workspace detection: fixed ID "ws-1", no IsDefault flag in the data
//   model.
// - Per-workspace preferences (default agent / model): stored on the backend.
//   A localStorage fallback is kept only long enough to migrate older Web data.

export const DEFAULT_WORKSPACE_ID = 'ws-1';

export function isDefaultWorkspace(id: string | undefined | null): boolean {
  return id === DEFAULT_WORKSPACE_ID;
}

// djb2 hash — stable across sessions and browsers. Good enough for picking a
// hue in HSL space when no server-assigned color is available.
function djb2(str: string): number {
  let hash = 5381;
  for (let i = 0; i < str.length; i++) {
    hash = ((hash << 5) + hash + str.charCodeAt(i)) | 0;
  }
  return hash >>> 0;
}

// Prefer the server-assigned color; fall back to a stable hash-derived HSL for
// callers that only have an id. Accepts either a workspace-like object or a
// bare id string so the one helper covers every caller.
export function workspaceColor(
  input: { id?: string | null; color?: string | null } | string | null | undefined,
): string {
  if (!input) return '#cbd5e1';
  if (typeof input === 'string') {
    const cached = colorRegistry.get(input);
    if (cached) return cached;
    return hashColor(input);
  }
  if (input.color) return input.color;
  if (input.id) {
    const cached = colorRegistry.get(input.id);
    if (cached) return cached;
  }
  return hashColor(input.id);
}

// Module-level id → color cache populated whenever the caller fetches a
// workspace list. Lets places that only have a workspaceId (job rows, footer
// tag) render the same server-assigned color as the settings page without
// having to thread the full record through every component.
const colorRegistry = new Map<string, string>();

export function registerWorkspaceColors(
  items: Array<{
    id?: string | null;
    color?: string | null;
    defaultAgent?: string | null;
    defaultModel?: string | null;
  }> | undefined | null,
) {
  if (!items) return;
  for (const w of items) {
    if (!w?.id) continue;
    if (w.color) colorRegistry.set(w.id, w.color);
    const supportsSharedPrefs = Object.prototype.hasOwnProperty.call(w, 'defaultAgent')
      || Object.prototype.hasOwnProperty.call(w, 'defaultModel');
    if (supportsSharedPrefs) {
      serverPrefsRegistry.set(w.id, {
        defaultAgent: w.defaultAgent || undefined,
        defaultModel: w.defaultModel || undefined,
      });
    }
  }
}

function hashColor(id: string | undefined | null): string {
  if (!id) return '#cbd5e1';
  const hue = djb2(id) % 360;
  return hslToHex(hue, 70, 55);
}

function hslToHex(h: number, s: number, l: number): string {
  s /= 100;
  l /= 100;
  const k = (n: number) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n: number) => {
    const v = l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
    return Math.round(255 * v)
      .toString(16)
      .padStart(2, '0');
  };
  return `#${f(0)}${f(8)}${f(4)}`;
}

// Per-workspace preferences shared by Web and native clients.
export interface WorkspacePrefs {
  defaultAgent?: string;
  defaultModel?: string;
}

export function validWorkspaceDefaultModel(
  modelId: string | undefined,
  availableModels: Array<{ modelId: string }> | undefined,
): string | undefined {
  if (!modelId) return undefined;
  return availableModels?.some((model) => model.modelId === modelId) ? modelId : undefined;
}

const serverPrefsRegistry = new Map<string, WorkspacePrefs>();

function prefsKey(wsId: string): string {
  return `workspacePrefs_${wsId}`;
}

export function loadWorkspacePrefs(wsId: string | undefined | null): WorkspacePrefs {
  if (!wsId) return {};
  const serverPrefs = serverPrefsRegistry.get(wsId);
  if (serverPrefsRegistry.has(wsId)) return serverPrefs || {};
  try {
    const raw = localStorage.getItem(prefsKey(wsId));
    if (!raw) return {};
    const obj = JSON.parse(raw);
    return {
      defaultAgent: typeof obj.defaultAgent === 'string' ? obj.defaultAgent : undefined,
      defaultModel: typeof obj.defaultModel === 'string' ? obj.defaultModel : undefined,
    };
  } catch {
    return {};
  }
}

export function saveWorkspacePrefs(wsId: string, prefs: WorkspacePrefs) {
  if (!wsId) return;
  const clean: WorkspacePrefs = {};
  if (prefs.defaultAgent) clean.defaultAgent = prefs.defaultAgent;
  if (prefs.defaultModel) clean.defaultModel = prefs.defaultModel;
  if (Object.keys(clean).length === 0) {
    localStorage.removeItem(prefsKey(wsId));
  } else {
    localStorage.setItem(prefsKey(wsId), JSON.stringify(clean));
  }
}

// Snapshot matching keys before deleting them. Removing entries while walking
// localStorage by index shifts later entries and can otherwise skip adjacent
// workspace preference records.
export function clearDeletedAgentLocalPreferences(agentId: string) {
  if (localStorage.getItem('last_agent_type') === agentId) {
    localStorage.removeItem('last_agent_type');
  }
  const keys = Array.from({ length: localStorage.length }, (_, index) => localStorage.key(index))
    .filter((key): key is string => !!key?.startsWith('workspacePrefs_'));
  for (const key of keys) {
    try {
      const value = JSON.parse(localStorage.getItem(key) || '{}') as { defaultAgent?: string; defaultModel?: string };
      if (value.defaultAgent !== agentId) continue;
      delete value.defaultAgent;
      delete value.defaultModel;
      if (Object.keys(value).length === 0) localStorage.removeItem(key);
      else localStorage.setItem(key, JSON.stringify(value));
    } catch {
      localStorage.removeItem(key);
    }
  }
}

export function registerWorkspacePrefs(wsId: string, prefs: WorkspacePrefs) {
  if (!wsId) return;
  serverPrefsRegistry.set(wsId, {
    defaultAgent: prefs.defaultAgent || undefined,
    defaultModel: prefs.defaultModel || undefined,
  });
}

// Move a legacy browser-local preference into the versioned workspace patch
// endpoint. The server wins when it already has a shared value. Failed writes
// keep localStorage intact so a later page load can retry without losing data.
export async function migrateWorkspacePrefsToServer(
  wsId: string,
  resolveAgent?: (value: string) => string | undefined,
  modelBelongsToAgent?: (agentId: string, modelId: string) => boolean,
): Promise<WorkspacePrefs> {
  if (!wsId) return {};
  let local: WorkspacePrefs = {};
  try {
    const raw = localStorage.getItem(prefsKey(wsId));
    if (raw) {
      const value = JSON.parse(raw);
      const storedAgent = typeof value.defaultAgent === 'string' ? value.defaultAgent : undefined;
      const resolvedAgent = storedAgent && resolveAgent ? resolveAgent(storedAgent) : storedAgent;
      const storedModel = typeof value.defaultModel === 'string' ? value.defaultModel : undefined;
      local = resolvedAgent ? {
        defaultAgent: resolvedAgent,
        defaultModel: storedModel && (!modelBelongsToAgent || modelBelongsToAgent(resolvedAgent, storedModel))
          ? storedModel
          : undefined,
      } : {};
    }
  } catch { /* leave local empty */ }

  const res = await fetch(`/api/v1/workspace/${encodeURIComponent(wsId)}`);
  if (!res.ok) return local;
  const workspace = await res.json();
  const supportsSharedPrefs = Object.prototype.hasOwnProperty.call(workspace, 'defaultAgent')
    || Object.prototype.hasOwnProperty.call(workspace, 'defaultModel');
  if (!supportsSharedPrefs) return local;
  const sharedRaw: WorkspacePrefs = {
    defaultAgent: typeof workspace?.defaultAgent === 'string' && workspace.defaultAgent ? workspace.defaultAgent : undefined,
    defaultModel: typeof workspace?.defaultModel === 'string' && workspace.defaultModel ? workspace.defaultModel : undefined,
  };
  const resolvedSharedAgent = sharedRaw.defaultAgent && resolveAgent
    ? resolveAgent(sharedRaw.defaultAgent)
    : sharedRaw.defaultAgent;
  const shared: WorkspacePrefs = resolvedSharedAgent ? {
    defaultAgent: resolvedSharedAgent,
    defaultModel: sharedRaw.defaultModel && (!modelBelongsToAgent || modelBelongsToAgent(resolvedSharedAgent, sharedRaw.defaultModel))
      ? sharedRaw.defaultModel
      : undefined,
  } : {};
  const rawHasShared = !!(sharedRaw.defaultAgent || sharedRaw.defaultModel);
  const hasShared = !!(shared.defaultAgent || shared.defaultModel);
  const source = hasShared ? shared : local;
  const sharedAlreadyCanonical = shared.defaultAgent === sharedRaw.defaultAgent
    && shared.defaultModel === sharedRaw.defaultModel;
  if ((hasShared && sharedAlreadyCanonical) || (!rawHasShared && !local.defaultAgent && !local.defaultModel)) {
    registerWorkspacePrefs(wsId, source);
    try { localStorage.removeItem(prefsKey(wsId)); } catch { /* ignore */ }
    return source;
  }

  const update = await fetch(`/api/v1/workspace/${encodeURIComponent(wsId)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      expectedVersion: workspace.version,
      defaultAgent: source.defaultAgent || '',
      defaultModel: source.defaultModel || '',
    }),
  });
  if (!update.ok) return local;
  const saved = await update.json();
  if (!Object.prototype.hasOwnProperty.call(saved, 'defaultAgent')
    && !Object.prototype.hasOwnProperty.call(saved, 'defaultModel')) {
    return local;
  }
  const migrated = {
    defaultAgent: saved?.defaultAgent || source.defaultAgent,
    defaultModel: saved?.defaultModel || source.defaultModel,
  };
  registerWorkspacePrefs(wsId, migrated);
  try { localStorage.removeItem(prefsKey(wsId)); } catch { /* ignore */ }
  return migrated;
}

// Session key: track the most recently used workspace so first-open can
// restore it. Keyed per workspace so multi-workspace switching in one session
// keeps all entries.
export const LAST_WORKSPACE_STORAGE_KEY = 'last_used_workspace_id';

export function getLastUsedWorkspaceId(): string | undefined {
  try {
    const v = localStorage.getItem(LAST_WORKSPACE_STORAGE_KEY);
    return v || undefined;
  } catch {
    return undefined;
  }
}

export function setLastUsedWorkspaceId(wsId: string) {
  try {
    localStorage.setItem(LAST_WORKSPACE_STORAGE_KEY, wsId);
  } catch {
    /* ignore */
  }
}
