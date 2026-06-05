// Workspace-level helpers shared across components. Kept deliberately small.
//
// - Color strip: stored on the backend as a random hex (#rrggbb) assigned at
//   creation time (and backfilled for legacy records on first load). The
//   frontend reads it from the workspace record; only when a caller has nothing
//   but an id do we fall back to a stable hash-derived hue so the UI still
//   renders something reasonable.
// - Default workspace detection: fixed ID "ws-1", no IsDefault flag in the data
//   model.
// - Per-workspace preferences (default agent / model): stored in localStorage;
//   IM side does not read these.

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
  items: Array<{ id?: string | null; color?: string | null }> | undefined | null,
) {
  if (!items) return;
  for (const w of items) {
    if (w?.id && w.color) colorRegistry.set(w.id, w.color);
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

// Per-workspace preferences (Web-only; IM side ignores these).
export interface WorkspacePrefs {
  defaultAgent?: string;
  defaultModel?: string;
}

function prefsKey(wsId: string): string {
  return `workspacePrefs_${wsId}`;
}

export function loadWorkspacePrefs(wsId: string | undefined | null): WorkspacePrefs {
  if (!wsId) return {};
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
