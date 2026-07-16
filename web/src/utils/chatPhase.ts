// Chat loading phase — a fine-grained status for the "AI 正在思考..."
// loading indicator. Two sources feed it:
//
//   - Backend "agent_phase" custom events cover the silent preparation
//     window before any content streams (subprocess launch / reconnect /
//     history replay / waiting for the first token). See
//     types/model/event.go AgentPhase*.
//   - The frontend derives the streaming phases (reasoning / replying /
//     tool) directly from the message/tool events it already receives, so
//     no extra backend event is needed for those.
//
// Both converge into a single ChatPhase that MessageList renders as the
// loading label.

export type ChatPhaseKind =
  // Backend preparation phases (from agent_phase custom events)
  | 'starting'
  | 'reconnecting'
  | 'loading_history'
  | 'thinking'
  // Frontend-derived streaming phases
  | 'reasoning'
  | 'replying'
  | 'tool';

export interface ChatPhase {
  kind: ChatPhaseKind;
  /** Extra context, e.g. the tool name for the 'tool' phase. */
  detail?: string;
}

// Backend agent_phase strings map 1:1 onto the matching kinds. Unknown
// strings return null so a future backend phase never renders as a raw
// token.
const BACKEND_PHASES: Record<string, ChatPhaseKind> = {
  starting: 'starting',
  reconnecting: 'reconnecting',
  loading_history: 'loading_history',
  thinking: 'thinking',
};

export function backendPhaseKind(phase: string | undefined): ChatPhaseKind | null {
  if (!phase) return null;
  return BACKEND_PHASES[phase] ?? null;
}

const LABELS: Record<ChatPhaseKind, string> = {
  starting: '正在启动 Agent…',
  reconnecting: 'Agent 重连中…',
  loading_history: '载入会话历史…',
  thinking: 'AI 正在思考…',
  reasoning: '深度思考中…',
  replying: '正在回复…',
  tool: '正在调用工具…',
};

export function phaseLabel(phase: ChatPhase | null | undefined): string | undefined {
  if (!phase) return undefined;
  return LABELS[phase.kind];
}
