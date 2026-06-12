import { MessageRoleEnum } from '../types/protocol';
import type { AssistantMessage, Message, ToolMessage } from '../types/message';

/**
 * A "pure thought bubble" is an assistant message that carries only
 * reasoning text (thinkingContent) and no body content. The history API
 * splits an assistant turn's reasoning into its own entry (keyed by the
 * stored thought_msg_id) and the live SSE stream creates a separate bubble
 * on OnThoughtStart, so the same thought exists as two messages with
 * potentially different ids. When the live id and the persisted
 * thought_msg_id momentarily diverge (e.g. a history refetch races a still
 * in-flight round before the id is durably stored), id-based dedup misses
 * them and the thought renders twice. Semantic dedup by sessionId +
 * thinkingContent is the same fallback already used for synthetic loop-user
 * messages.
 */
function isPureThoughtBubble(m: Message): m is AssistantMessage {
  if (m.role !== MessageRoleEnum.ASSISTANT) return false;
  const am = m as AssistantMessage;
  return !!am.thinkingContent && !am.content;
}

function thoughtKey(sessionId: string, thinkingContent: string): string {
  return `${sessionId}\x00${thinkingContent}`;
}

export interface MergeOptions {
  /**
   * When true, also filters out existing tool messages whose toolCallId
   * already exists in the incoming history (used by syncJobState which
   * has confirmed history from the server).
   */
  deduplicateToolCallIds?: boolean;
}

/**
 * Merges incoming (history) messages with existing (SSE/live) messages.
 *
 * Core algorithm:
 * 1. For each incoming message, prefer the existing version if it has
 *    longer content (streaming deltas still accumulating).
 * 2. Existing-only messages (not in incoming set) are kept unless they
 *    are duplicates: optimistic user messages, synthetic loop-user messages
 *    now covered by history, or (optionally) tool messages with matching
 *    toolCallId.
 *
 * Returns [...merged_incoming, ...filtered_existing_only].
 *
 * The result is guaranteed unique-by-id (first occurrence wins). Step 3 only
 * dedups existing-only against incoming, so a duplicate id *within* incoming
 * (observed in practice for tool messages keyed by OpenAI `call_*` ids that the
 * backend replays across a reconnect) would otherwise slip into state and only
 * get masked at render time. Deduping here keeps the `messages` state itself
 * invariant-clean so downstream SSE merges reason over a unique-id list.
 */
export function mergeMessages(
  existing: Message[],
  incoming: Message[],
  options?: MergeOptions,
): Message[] {
  if (existing.length === 0) return dedupeById(incoming, 'incoming');
  if (incoming.length === 0) return dedupeById(existing, 'existing');

  const existingMap = new Map(existing.map((m) => [m.id, m]));

  // Step 1: For each incoming message, prefer existing version if it has
  // longer content (i.e. streaming is still in progress).
  const merged = incoming.map((hm) => {
    const em = existingMap.get(hm.id);
    if (em && (em.content?.length ?? 0) > (hm.content?.length ?? 0)) {
      return em;
    }
    return hm;
  });

  // Step 2: Build lookup sets for dedup.
  const incomingIds = new Set(incoming.map((m) => m.id));

  // Build a set of clientMessageIds present in incoming history so we only
  // drop optimistic user messages when a confirmed version truly exists.
  const incomingClientMessageIds = new Set<string>();
  for (const hm of incoming) {
    if (hm.role === MessageRoleEnum.USER && hm.clientMessageId) {
      incomingClientMessageIds.add(hm.clientMessageId);
    }
  }

  const historyUserKeys = new Set<string>();
  for (const hm of incoming) {
    if (hm.role === MessageRoleEnum.USER && hm.sessionId) {
      historyUserKeys.add(`${hm.sessionId}\x00${hm.content}`);
    }
  }

  // Build a set of (sessionId, thinkingContent) for pure thought bubbles
  // present in history, so a live thought bubble whose id no longer matches
  // its persisted thought_msg_id is dropped in favour of the history version.
  const historyThoughtKeys = new Set<string>();
  for (const hm of incoming) {
    if (hm.sessionId && isPureThoughtBubble(hm)) {
      historyThoughtKeys.add(thoughtKey(hm.sessionId, hm.thinkingContent ?? ''));
    }
  }

  let historyToolCallIds: Set<string> | undefined;
  if (options?.deduplicateToolCallIds) {
    historyToolCallIds = new Set<string>();
    for (const hm of incoming) {
      if (hm.role === MessageRoleEnum.TOOL) {
        historyToolCallIds.add((hm as ToolMessage).toolCallId);
      }
    }
  }

  // Step 3: Filter existing-only messages, removing duplicates.
  const existingOnly = existing.filter((m) => {
    if (incomingIds.has(m.id)) return false;
    // Drop optimistic user messages only when history has the confirmed version.
    // Without this check, a freshly-sent message (not yet persisted by the
    // backend) would be dropped during a syncJobState reload race.
    if (m.role === MessageRoleEnum.USER && m.clientMessageId && incomingClientMessageIds.has(m.clientMessageId)) return false;
    // Drop synthetic loop user messages whose confirmed version now exists in history
    if (m.role === MessageRoleEnum.USER && m.id.startsWith('loop-user-') && m.sessionId) {
      if (historyUserKeys.has(`${m.sessionId}\x00${m.content}`)) return false;
    }
    // Drop a live thought bubble whose equivalent (same sessionId +
    // thinkingContent) already exists in history under a different id.
    if (m.sessionId && isPureThoughtBubble(m)) {
      if (historyThoughtKeys.has(thoughtKey(m.sessionId, m.thinkingContent ?? ''))) return false;
    }
    // Optionally drop tool messages whose toolCallId is covered by history
    if (historyToolCallIds && m.role === MessageRoleEnum.TOOL) {
      if (historyToolCallIds.has((m as ToolMessage).toolCallId)) return false;
    }
    return true;
  });

  return dedupeById([...merged, ...existingOnly], 'merge');
}

/**
 * Returns `messages` with duplicate ids removed (first occurrence kept). When
 * there are no duplicates the original array is returned unchanged, preserving
 * reference identity for the common clean case (no needless reallocation). In
 * DEV, logs the offending id and source so a duplicate that reaches state can
 * be traced back to the path that produced it, rather than only surfacing as a
 * render-time warning in MessageList.
 */
function dedupeById(messages: Message[], source: string): Message[] {
  const seen = new Set<string>();
  let firstDuplicateIndex = -1;
  for (let i = 0; i < messages.length; i++) {
    if (seen.has(messages[i].id)) {
      firstDuplicateIndex = i;
      break;
    }
    seen.add(messages[i].id);
  }
  if (firstDuplicateIndex === -1) return messages;

  const out = messages.slice(0, firstDuplicateIndex);
  const duplicate = messages[firstDuplicateIndex].id;
  for (let i = firstDuplicateIndex; i < messages.length; i++) {
    if (seen.has(messages[i].id)) continue;
    seen.add(messages[i].id);
    out.push(messages[i]);
  }
  if (import.meta.env.DEV) {
    console.warn(`[mergeMessages] dropped duplicate message id from ${source}:`, duplicate);
  }
  return out;
}
