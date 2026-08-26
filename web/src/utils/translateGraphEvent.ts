import {
  AgentEvent,
  EventTypeEnum,
  MessageRoleEnum,
  ToolCallStatusEnum,
  type TextMessageStartEvent,
  type TextMessageContentEvent,
  type TextMessageEndEvent,
  type ToolCallStartEvent,
  type ToolCallArgsEvent,
  type ToolCallResultEvent,
  type ToolCallEndEvent,
  type ToolCallStitchedEvent,
} from '../types';
import type { GraphEvent } from '../types/graph';

// translateGraphEvent maps a graph run's agent-level GraphEvent (the
// agentMessage* / agentThought* / agentTool* token stream the backend writes to
// the run's event JSONL — see services/graph/runtime.go graphEventHandler) into
// the generic AgentEvent shape that useJobChat's handleEvent already knows how
// to render. Feeding the translated event through handleEvent reuses the entire
// agent streaming pipeline (per-id bubble creation, thinking vs. content
// routing, tool bubble lifecycle, Finished-status guards, id dedup), so graph
// agent nodes stream token-by-token through the same chat renderer.
//
// Returns null for events that carry no chat payload (lifecycle, edge/variable,
// log/error, token usage) — those are handled by the caller's reconcile path.
//
// The message/thought/tool ids in payload are the SAME uuids the backend
// persists to the session transcript (Extra[msg_id]/[thought_msg_id]) and
// returns from the history API, so a live bubble and its later history reload
// share an id and dedup cleanly.
export function translateGraphEvent(ev: GraphEvent): AgentEvent | null {
  const p = ev.payload ?? {};
  const sessionId = p.sessionId;
  // Every chat bubble must be tagged with a session so the message-list filter
  // (filteredMessages, keyed by activeSessionId) can route it. Without one the
  // bubble would never be attributable to a session — drop it.
  if (!sessionId) return null;

  const base = {
    sessionId,
    runId: ev.runId,
    timestamp: ev.createdAt,
    jobId: p.jobId,
  };

  switch (ev.type) {
    case 'agentMessageStart':
      return {
        ...base,
        type: EventTypeEnum.TEXT_MESSAGE_START,
        messageId: p.messageId ?? '',
        role: MessageRoleEnum.ASSISTANT,
        external: { isThinking: false },
      } as TextMessageStartEvent;

    case 'agentMessageDelta':
      return {
        ...base,
        type: EventTypeEnum.TEXT_MESSAGE_CONTENT,
        messageId: p.messageId ?? '',
        role: MessageRoleEnum.ASSISTANT,
        delta: p.delta ?? ev.message ?? '',
        external: { isThinking: false },
      } as TextMessageContentEvent;

    case 'agentMessageEnd':
      return {
        ...base,
        type: EventTypeEnum.TEXT_MESSAGE_END,
        messageId: p.messageId ?? '',
        role: MessageRoleEnum.ASSISTANT,
        external: { isThinking: false },
      } as TextMessageEndEvent;

    case 'agentThoughtStart':
      return {
        ...base,
        type: EventTypeEnum.TEXT_MESSAGE_START,
        messageId: p.messageId ?? '',
        role: MessageRoleEnum.ASSISTANT,
        external: { isThinking: true },
      } as TextMessageStartEvent;

    case 'agentThoughtDelta':
      return {
        ...base,
        type: EventTypeEnum.TEXT_MESSAGE_CONTENT,
        messageId: p.messageId ?? '',
        role: MessageRoleEnum.ASSISTANT,
        delta: p.delta ?? ev.message ?? '',
        external: { isThinking: true },
      } as TextMessageContentEvent;

    case 'agentThoughtEnd':
      return {
        ...base,
        type: EventTypeEnum.TEXT_MESSAGE_END,
        messageId: p.messageId ?? '',
        role: MessageRoleEnum.ASSISTANT,
        external: { isThinking: true },
      } as TextMessageEndEvent;

    case 'agentToolStart':
      return {
        ...base,
        type: EventTypeEnum.TOOL_CALL_START,
        toolCallId: p.toolCallId ?? '',
        toolCallName: p.toolName ?? '',
        toolCallStatus: mapToolStatus(p.status),
      } as ToolCallStartEvent;

    case 'agentToolArgs':
      return {
        ...base,
        type: EventTypeEnum.TOOL_CALL_ARGS,
        toolCallId: p.toolCallId ?? '',
        delta: p.delta ?? '',
        replace: p.replace === 'true',
        toolCallStatus: mapToolStatus(p.status),
      } as ToolCallArgsEvent;

    case 'agentToolResult':
      // The backend reuses agentToolResult for both the normal terminal result
      // and the late "stitched" rewrite of a bubble that was already flushed as
      // a Placeholder. The stitched variant MUST map to TOOL_CALL_STITCHED —
      // that is the only handler that rewrites an already-Finished bubble in
      // place; routing it as TOOL_CALL_RESULT would hit the Finished guard and
      // be dropped, leaving the stale "interrupted" placeholder on screen.
      if (p.stitched === 'true') {
        return {
          ...base,
          type: EventTypeEnum.TOOL_CALL_STITCHED,
          toolCallId: p.toolCallId ?? '',
          delta: p.delta ?? '',
          toolCallStatus: mapToolStatus(p.status),
          supersededAgoMs: p.supersededAgoMs ? Number(p.supersededAgoMs) : undefined,
        } as ToolCallStitchedEvent;
      }
      return {
        ...base,
        type: EventTypeEnum.TOOL_CALL_RESULT,
        toolCallId: p.toolCallId ?? '',
        delta: p.delta ?? '',
        toolCallStatus: mapToolStatus(p.status),
      } as ToolCallResultEvent;

    case 'agentToolEnd': {
      const status = mapToolStatus(p.status);
      const evt: ToolCallEndEvent = {
        ...base,
        type: EventTypeEnum.TOOL_CALL_END,
        toolCallId: p.toolCallId ?? '',
        toolCallStatus: status,
      };
      // Carry the placeholder reason so the bubble tooltip matches what a
      // history reload would show for an interrupted/superseded tool call.
      if (status === ToolCallStatusEnum.Placeholder && p.placeholderReason) {
        evt.external = { placeholderReason: p.placeholderReason };
      }
      return evt;
    }

    // agentTokenUsage is intentionally dropped: feeding it as a token_usage
    // CUSTOM event would make the chat header's total jump around as parallel
    // graph nodes stream. Per-session token counts still arrive via the
    // reconcile/history path.
    default:
      return null;
  }
}

function mapToolStatus(status?: string): ToolCallStatusEnum {
  switch (status) {
    case 'Success':
      return ToolCallStatusEnum.Success;
    case 'Error':
      return ToolCallStatusEnum.Error;
    case 'Placeholder':
      return ToolCallStatusEnum.Placeholder;
    default:
      return ToolCallStatusEnum.Processing;
  }
}
