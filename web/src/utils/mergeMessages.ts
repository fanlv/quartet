import { MessageRoleEnum, MessageStatusEnum } from '../types/protocol';
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
 * thinkingContent covers that case.
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
 * Lookup sets describing what an incoming (history) batch already covers, so
 * a live message can be recognised as superseded even when the two sides
 * disagree on the id. Shared by every merge path so the semantic dedup rules
 * live in exactly one place.
 */
interface SupersedeIndex {
  ids: Set<string>;
  clientMessageIds: Set<string>;
  thoughtKeys: Set<string>;
  toolCallIds?: Set<string>;
}

function buildSupersedeIndex(incoming: Message[], options?: MergeOptions): SupersedeIndex {
  const index: SupersedeIndex = {
    ids: new Set(incoming.map((m) => m.id)),
    // Only drop optimistic user messages when history has the confirmed
    // version. Without this check, a freshly-sent message (not yet persisted
    // by the backend) would be dropped during a syncJobState reload race.
    clientMessageIds: new Set(),
    // Keyed by (sessionId, thinkingContent) so a live thought bubble whose id
    // no longer matches its persisted thought_msg_id is dropped in favour of
    // the history version.
    thoughtKeys: new Set(),
    toolCallIds: options?.deduplicateToolCallIds ? new Set() : undefined,
  };
  for (const hm of incoming) {
    if (hm.role === MessageRoleEnum.USER && hm.clientMessageId) {
      index.clientMessageIds.add(hm.clientMessageId);
    }
    if (hm.sessionId && isPureThoughtBubble(hm)) {
      index.thoughtKeys.add(thoughtKey(hm.sessionId, hm.thinkingContent ?? ''));
    }
    if (index.toolCallIds && hm.role === MessageRoleEnum.TOOL) {
      index.toolCallIds.add((hm as ToolMessage).toolCallId);
    }
  }
  return index;
}

function isSuperseded(message: Message, index: SupersedeIndex): boolean {
  if (index.ids.has(message.id)) return true;
  if (message.role === MessageRoleEnum.USER && message.clientMessageId
    && index.clientMessageIds.has(message.clientMessageId)) return true;
  if (message.sessionId && isPureThoughtBubble(message)
    && index.thoughtKeys.has(thoughtKey(message.sessionId, message.thinkingContent ?? ''))) return true;
  if (index.toolCallIds && message.role === MessageRoleEnum.TOOL
    && index.toolCallIds.has((message as ToolMessage).toolCallId)) return true;
  return false;
}

/** Prefers the existing copy while it is still accumulating streaming deltas. */
function preferLongerContent(historyMessage: Message, existingById: Map<string, Message>): Message {
  const em = existingById.get(historyMessage.id);
  if (em && (em.content?.length ?? 0) > (historyMessage.content?.length ?? 0)) return em;
  return historyMessage;
}

/**
 * Splits off the pinned round heads in `existing`.
 *
 * A pinned round head is a stand-in for a user message that is already on disk
 * but sits ABOVE the loaded window (see `roundHeadPinned`). No merge may
 * position it by page order — it belongs before everything the window holds —
 * so every merge path holds it aside and puts it back at the very front. Once
 * an incoming page carries the real record, the stand-in is dropped and the
 * real one takes its place in page order.
 */
function extractPinnedRoundHeads(
  existing: Message[],
  incoming: Message[],
): { pinned: Message[]; rest: Message[] } {
  if (!existing.some((message) => message.roundHeadPinned === true)) {
    return { pinned: [], rest: existing };
  }
  const incomingIds = new Set(incoming.map((message) => message.id));
  const pinned: Message[] = [];
  const rest: Message[] = [];
  for (const message of existing) {
    if (message.roundHeadPinned !== true) {
      rest.push(message);
    } else if (!incomingIds.has(message.id)) {
      pinned.push(message);
    }
  }
  return { pinned, rest };
}

/**
 * Merges incoming (history) messages with existing (SSE/live) messages.
 *
 * Core algorithm:
 * 1. For each incoming message, prefer the existing version if it has
 *    longer content (streaming deltas still accumulating).
 * 2. Existing-only messages (not in incoming set) are kept unless they
 *    are duplicates: optimistic user messages, or (optionally) tool
 *    messages with matching toolCallId.
 *
 * Returns [...merged_incoming, ...filtered_existing_only]. That ordering only
 * makes sense when `incoming` covers the front of the timeline — a complete
 * history, or an earlier page being prepended. Use mergeLatestHistoryPage when
 * `incoming` is the newest page instead.
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
  const { pinned, rest } = extractPinnedRoundHeads(existing, incoming);
  const merged = mergeUnpinned(rest, incoming, options);
  return pinned.length > 0 ? dedupeById([...pinned, ...merged], 'merge') : merged;
}

function mergeUnpinned(
  existing: Message[],
  incoming: Message[],
  options?: MergeOptions,
): Message[] {
  if (existing.length === 0) return dedupeById(incoming, 'incoming');
  if (incoming.length === 0) return dedupeById(existing, 'existing');

  const existingById = new Map(existing.map((m) => [m.id, m]));
  const merged = incoming.map((hm) => preferLongerContent(hm, existingById));

  const index = buildSupersedeIndex(incoming, options);
  const existingOnly = existing.filter((m) => !isSuperseded(m, index));

  return dedupeById([...merged, ...existingOnly], 'merge');
}

/**
 * A "transient" message is a live artefact of the current round rather than
 * settled history: a system/command bubble (never persisted), an optimistic
 * user message still waiting for its RUN_STARTED confirmation, or a bubble
 * that is still streaming.
 */
function isTransient(message: Message): boolean {
  return message.role === MessageRoleEnum.SYSTEM
    || message.pending === true
    || message.status !== MessageStatusEnum.Finished;
}

/**
 * Reconciles the newest history page into the in-memory message list.
 *
 * `latest` is only the tail page of the transcript, so it describes the end of
 * the timeline and says nothing about anything before it. Everything the page
 * does not cover lives only in `existing` and must stay IN FRONT of the page:
 * earlier pages the user scrolled in, and — on a turn long enough to push it
 * out of the newest page — the user message that started the round.
 *
 * The list is therefore spliced, not rebuilt. The page acts as the spine for
 * the region it covers: its messages are laid down in page order, and live
 * messages the page does not carry are kept anchored between the same
 * neighbours they had before, instead of being swept to the end of the list
 * (which is how an older bubble ends up rendered below the newest message).
 *
 *     [ existing before the page ] [ page, with live messages in place ]
 *
 * Inside the covered region a settled message the page does not carry is
 * dropped: one persisted assistant row can collapse several streamed bubbles
 * and only the last streaming id survives into history, so keeping the
 * pre-collapse bubbles would render their text twice. Transient messages
 * (system/command bubbles, unconfirmed optimistic user messages, still
 * streaming bubbles) are not on disk yet and are kept.
 *
 * When the page shares no id with the list at all, the settled prefix is
 * treated as older history (a long turn can push the whole list out of the
 * newest page) and only the trailing transient run is kept behind the page.
 *
 * Pinned round heads are held aside and restored at the very front, since they
 * stand in for a message the page cannot place (see extractPinnedRoundHeads).
 */
export function mergeLatestHistoryPage(existing: Message[], latest: Message[]): Message[] {
  const { pinned, rest } = extractPinnedRoundHeads(existing, latest);
  const spliced = spliceLatestHistoryPage(rest, latest);
  return pinned.length > 0 ? dedupeById([...pinned, ...spliced], 'latest-page') : spliced;
}

function spliceLatestHistoryPage(existing: Message[], latest: Message[]): Message[] {
  if (existing.length === 0) return dedupeById(latest, 'latest-page');
  if (latest.length === 0) return dedupeById(existing, 'existing');

  const pagePositionById = new Map<string, number>();
  latest.forEach((message, position) => {
    if (!pagePositionById.has(message.id)) pagePositionById.set(message.id, position);
  });

  const spineStart = existing.findIndex((message) => pagePositionById.has(message.id));
  if (spineStart < 0) {
    let tailStart = existing.length;
    while (tailStart > 0 && isTransient(existing[tailStart - 1])) tailStart--;
    return dedupeById([
      ...existing.slice(0, tailStart),
      ...mergeMessages(existing.slice(tailStart), latest, { deduplicateToolCallIds: true }),
    ], 'latest-page');
  }

  const index = buildSupersedeIndex(latest, { deduplicateToolCallIds: true });
  const existingById = new Map(existing.map((message) => [message.id, message]));
  const out = existing.slice(0, spineStart);
  let pageCursor = 0;
  const layPageThrough = (position: number) => {
    while (pageCursor <= position) {
      out.push(preferLongerContent(latest[pageCursor], existingById));
      pageCursor++;
    }
  };

  for (let i = spineStart; i < existing.length; i++) {
    const message = existing[i];
    const position = pagePositionById.get(message.id);
    if (position !== undefined) {
      layPageThrough(position);
      continue;
    }
    if (!isSuperseded(message, index) && isTransient(message)) out.push(message);
  }
  layPageThrough(latest.length - 1);

  return dedupeById(out, 'latest-page');
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
