import { MessageRoleEnum } from '../types/protocol';
import type { Message, ToolMessage } from '../types/message';

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
 */
export function mergeMessages(
  existing: Message[],
  incoming: Message[],
  options?: MergeOptions,
): Message[] {
  if (existing.length === 0) return incoming;
  if (incoming.length === 0) return existing;

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
    // Optionally drop tool messages whose toolCallId is covered by history
    if (historyToolCallIds && m.role === MessageRoleEnum.TOOL) {
      if (historyToolCallIds.has((m as ToolMessage).toolCallId)) return false;
    }
    return true;
  });

  return [...merged, ...existingOnly];
}
