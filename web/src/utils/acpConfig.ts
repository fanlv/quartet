import type {
  SessionModelState,
  SessionModeState,
  SessionThoughtLevelState,
} from '../types';

export type ACPConfigTarget = 'model' | 'mode' | 'thoughtLevel';

// ACPConfigState mirrors the backend response: each list is present only when
// the ACP switch returned a refreshed list for it. Switching model or
// thoughtLevel re-links both; switching mode returns none of them.
export interface ACPConfigState {
  models?: SessionModelState;
  modes?: SessionModeState;
  thoughtLevels?: SessionThoughtLevelState;
}

export interface SetACPConfigParams {
  target: ACPConfigTarget;
  // sessionId switches on a live session; agentType updates the Home cache.
  // Pass exactly one.
  sessionId?: string;
  agentType?: string;
  // Current selection used to address the cached model-linked lists. For the
  // session path only the target's value is applied.
  model?: string;
  mode?: string;
  thoughtLevel?: string;
}

const thoughtLevelRelinkInflight = new Map<string, Promise<SessionThoughtLevelState>>();

// setACPConfig applies an ACP live-config switch and returns the refreshed
// selector lists. Throws on a non-OK response so callers can roll back the
// optimistic UI selection.
export async function setACPConfig(params: SetACPConfigParams): Promise<ACPConfigState> {
  const res = await fetch('/api/v1/agent/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(params),
  });
  const data = await res.json().catch(() => null);
  if (!res.ok || !data || data.code !== 0) {
    const msg = (data && (data.msg || data.message || data.error)) || `set ACP ${params.target} failed (status ${res.status})`;
    throw new Error(msg);
  }
  return {
    models: data.models,
    modes: data.modes,
    thoughtLevels: data.thoughtLevels,
  };
}

// relinkACPThoughtLevels resolves the thought-level options for one concrete
// ACP agent/model pair. The agent list is backed by an asynchronous probe
// cache, so its thoughtLevels may belong to a different model by the time a UI
// picks its initial agent. Always re-select the concrete model before using the
// list. Concurrent callers share only the in-flight request; completed results
// are deliberately not cached so every fresh selector load performs a refresh.
export function relinkACPThoughtLevels(
  agentType: string,
  modelId: string,
): Promise<SessionThoughtLevelState> {
  const key = `${agentType}::${modelId}`;
  const existing = thoughtLevelRelinkInflight.get(key);
  if (existing) return existing;

  const request = setACPConfig({
    target: 'model',
    agentType,
    model: modelId,
  }).then((state) => state.thoughtLevels || {
    availableThoughtLevels: [],
    currentThoughtLevelId: '',
  }).finally(() => {
    if (thoughtLevelRelinkInflight.get(key) === request) {
      thoughtLevelRelinkInflight.delete(key);
    }
  });

  thoughtLevelRelinkInflight.set(key, request);
  return request;
}
