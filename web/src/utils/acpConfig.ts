import type {
  SessionModelState,
  SessionModeState,
  SessionThoughtLevelState,
} from '../components/ChatPage';

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
  // sessionId switches on a live session; agentType runs a Home preview. Pass
  // exactly one.
  sessionId?: string;
  agentType?: string;
  // Full current selection so a Home preview can replay it before reading the
  // linked lists back. For the session path only the target's value is used.
  model?: string;
  mode?: string;
  thoughtLevel?: string;
}

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
    const msg = (data && (data.message || data.error)) || `set ACP ${params.target} failed (status ${res.status})`;
    throw new Error(msg);
  }
  return {
    models: data.models,
    modes: data.modes,
    thoughtLevels: data.thoughtLevels,
  };
}
