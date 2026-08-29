import { useState, useCallback, useMemo, useRef, useEffect, type SetStateAction } from 'react';
import {
  Message,
  UserMessage,
  AssistantMessage,
  ToolMessage,
  AgentEvent,
  EventTypeEnum,
  CommandSystemMessageEvent,
  MessageRoleEnum,
  MessageStatusEnum,
  ToolCallStatusEnum,
  type FileAttachment,
  type GraphInstanceState,
  type GraphInstanceStatus,
  type GraphInstanceKey,
  type GraphRunStatus,
  type GraphRunStatusResponse,
  type GraphEvent,
} from '../types';
import { SSEClient } from '../utils/sse-client';
import { GraphSSEClient } from '../utils/graph-sse-client';
import { mergeMessages } from '../utils/mergeMessages';
import { markAgentDisplayUnknown, primeAgentDisplays } from '../utils/agentDisplay';
import { translateGraphEvent } from '../utils/translateGraphEvent';
import { backendPhaseKind, type ChatPhase } from '../utils/chatPhase';
import { useConnectionStatus } from '../contexts/ConnectionStatus';
import { isKnownCommand } from '../utils/commands';
import { claimJobCreateIntent, clearJobCreateIntent } from '../utils/jobCreateIntent';

// deriveStreamingPhase reads the current streaming phase off the LAST
// message — O(1), so it runs inline on render with no useMemo. A tail
// message that is still streaming (not Finished) tells us what the agent
// is doing right now: a live tool call, a reasoning segment, or the reply
// body. Returns null when the tail is the user turn or an already-finished
// bubble (the caller falls back to the backend preparation phase, then to
// the default label). This is also what rebuilds the phase after a page
// refresh, since messages are restored from the snapshot + SSE resume.
function deriveStreamingPhase(messages: Message[]): ChatPhase | null {
  const last = messages[messages.length - 1];
  if (!last || last.status === MessageStatusEnum.Finished) return null;
  if (last.role === MessageRoleEnum.TOOL) {
    const name = (last as ToolMessage).toolCallName;
    return { kind: 'tool', detail: name || undefined };
  }
  if (last.role === MessageRoleEnum.ASSISTANT) {
    return { kind: (last as AssistantMessage).isThinking ? 'reasoning' : 'replying' };
  }
  return null;
}

// showCommandToast displays a transient toast for slash-command feedback.
// Kept local here so the command branch in the SSE handler has somewhere to
// call without pulling in a larger toast framework. Plain DOM so SSR / tests
// can ignore it safely.
function showCommandToast(text: string) {
  if (typeof document === 'undefined') return;
  const existing = document.querySelector('.copy-toast');
  if (existing) existing.remove();
  const toast = document.createElement('div');
  toast.className = 'copy-toast';
  toast.textContent = text;
  document.body.appendChild(toast);
  setTimeout(() => toast.classList.add('show'), 10);
  setTimeout(() => {
    toast.classList.remove('show');
    setTimeout(() => toast.remove(), 300);
  }, 2800);
}

function isIgnorableNetworkError(err: unknown): boolean {
  const message = (err instanceof Error ? err.message : String(err || '')).trim().toLowerCase();
  if (!message) return false;
  return (
    message === 'network error' ||
    message === 'failed to fetch' ||
    message.includes('networkerror when attempting to fetch resource')
  );
}

async function readHTTPError(response: Response, prefix?: string): Promise<string> {
  const body = await response.text().catch(() => '');
  const trimmed = body.trim();
  let detail = trimmed;
  if (trimmed) {
    try {
      const parsed = JSON.parse(trimmed);
      if (typeof parsed?.msg === 'string') detail = parsed.msg;
      else if (typeof parsed?.error === 'string') detail = parsed.error;
      else if (typeof parsed?.message === 'string') detail = parsed.message;
      const extras = [
        typeof parsed?.code === 'string' ? `code=${parsed.code}` : '',
        typeof parsed?.reason === 'string' ? `reason=${parsed.reason}` : '',
      ].filter(Boolean);
      if (extras.length > 0) detail = detail ? `${detail} (${extras.join(', ')})` : extras.join(', ');
    } catch {
      // Keep the raw response text for non-JSON error bodies.
    }
  }
  const message = detail ? `HTTP ${response.status}: ${detail}` : `HTTP ${response.status}`;
  return prefix ? `${prefix}: ${message}` : message;
}

// Execute async tasks with a concurrency limit
async function parallelLimit<T>(tasks: (() => Promise<T>)[], limit: number): Promise<T[]> {
  const results: T[] = new Array(tasks.length);
  let i = 0;
  async function next(): Promise<void> {
    const idx = i++;
    if (idx >= tasks.length) return;
    results[idx] = await tasks[idx]();
    await next();
  }
  await Promise.all(Array.from({ length: Math.min(limit, tasks.length) }, () => next()));
  return results;
}

// idlePrefetchSessions lazily warms a list of session histories in the
// background at low priority. Used after the active session is loaded so the
// remaining Graph sessions are eventually in memory (smooth tab switches)
// without competing with the first paint for network/CPU.
//
// Each session is loaded one at a time, scheduled via requestIdleCallback so
// it only runs when the browser is otherwise idle; environments without it
// (older Safari, jsdom in tests) fall back to a short setTimeout. Before every
// load we re-check isCancelled() so an unmounted hook / switched job stops the
// chain promptly. A manual tab switch races ahead via the per-session
// load-on-switch effect — that path marks the session loaded and loadOne here
// can no-op it (the caller's loadOne already merges idempotently).
//
// Returns a cancel() handle the caller wires into its effect cleanup so a
// pending idle callback is dropped immediately on job switch / unmount.
function idlePrefetchSessions(
  sessionIds: string[],
  loadOne: (sid: string) => Promise<void>,
  isCancelled: () => boolean,
): () => void {
  const ric: typeof requestIdleCallback | undefined =
    typeof requestIdleCallback === 'function' ? requestIdleCallback : undefined;
  const cic: typeof cancelIdleCallback | undefined =
    typeof cancelIdleCallback === 'function' ? cancelIdleCallback : undefined;

  let idleHandle: number | null = null;
  let timeoutHandle: ReturnType<typeof setTimeout> | null = null;
  let stopped = false;

  const schedule = (fn: () => void) => {
    if (ric) {
      idleHandle = ric(fn, { timeout: 2000 });
    } else {
      timeoutHandle = setTimeout(fn, 50);
    }
  };

  let index = 0;
  const step = () => {
    if (stopped || isCancelled()) return;
    if (index >= sessionIds.length) return;
    const sid = sessionIds[index++];
    void loadOne(sid)
      .catch(() => {
        // loadOne is expected to record its own failure (failedSessionIdsRef);
        // swallow here so one bad session doesn't halt the prefetch chain.
      })
      .finally(() => {
        if (stopped || isCancelled()) return;
        schedule(step);
      });
  };
  schedule(step);

  return () => {
    stopped = true;
    if (idleHandle !== null && cic) cic(idleHandle);
    if (timeoutHandle !== null) clearTimeout(timeoutHandle);
  };
}

function getLastGraphSessionId(sessions: GraphSessionEntry[]): string | null {
  return sessions.length > 0 ? sessions[sessions.length - 1].sessionId : null;
}

// mapGraphInstanceStatus collapses a GraphInstanceState status onto the Graph
// session-entry status union. succeeded/skipped → completed (both terminal-OK in the
// sidebar's eyes), failed → failed, interrupted → interrupted, anything still
// in flight → running.
function mapGraphInstanceStatus(status: GraphInstanceStatus): GraphSessionEntry['status'] {
  switch (status) {
    case 'succeeded':
    case 'skipped':
      return 'completed';
    // A clarify node parked at awaitingInput has finished its turn and is open
    // for the user to discuss — render it as completed (no spinner) rather than
    // running, so the sidebar doesn't imply the agent is still busy.
    case 'awaitingInput':
      return 'completed';
    case 'failed':
      return 'failed';
    case 'interrupted':
      return 'interrupted';
    default:
      return 'running';
  }
}

// instanceKeyString mirrors the backend (services/graph/runtime.go): a
// main-scope key is just the node id; a loop-scoped key prefixes each iteration
// as "loopNodeId#index/" ahead of the node id. Used to dedup archived instances
// (keyed by this string on the run) against the live instance set.
function instanceKeyString(key: GraphInstanceKey | undefined): string {
  if (!key) return '';
  if (!key.iterations || key.iterations.length === 0) return key.nodeId;
  const parts = key.iterations.map((it) => `${it.loopNodeId}#${it.index}`);
  parts.push(key.nodeId);
  return parts.join('/');
}

// graphSessionEntries derives session entries from a Graph run's
// executed instances. Agent nodes (Prompt/Clarify) expose their session via
// sessionId; Shell nodes record their own transcript session in
// displaySessionId. Nodes with neither (IfElse/start/end) have no session and
// show on the mini canvas instead.
//
// `archived` (run.archivedInstances) carries instances a resume reset removed
// from the live set but that still own a session — including succeeded loop
// siblings wiped by a wholesale loop reset. They are merged back so the sidebar
// keeps listing prior-attempt conversations across a resume. Live wins per
// instance key: a re-run that reproduced a key supersedes its archived attempt,
// so only keys absent from the live set are revived.
function graphSessionEntries(
  instances: GraphInstanceState[],
  archived?: Record<string, GraphInstanceState>,
): GraphSessionEntry[] {
  let merged = instances;
  if (archived) {
    const liveKeys = new Set(instances.map((i) => instanceKeyString(i.key)));
    const revived = Object.entries(archived)
      .filter(([keyStr]) => !liveKeys.has(keyStr))
      .map(([, inst]) => inst);
    if (revived.length > 0) merged = [...instances, ...revived];
  }
  const entries: GraphSessionEntry[] = [];
  // Order by execution start so the session sidebar numbers nodes in the
  // order they actually ran (e.g. an upstream Shell before its downstream
  // Prompt), not the backend's instance-map iteration order. Array.sort is
  // stable, so instances that share a startedAt — or have none yet (still
  // pending, sorted last) — keep their original relative order.
  const ordered = [...merged].sort((a, b) => {
    const sa = a.startedAt ?? Number.POSITIVE_INFINITY;
    const sb = b.startedAt ?? Number.POSITIVE_INFINITY;
    return sa - sb;
  });
  for (const inst of ordered) {
    // Only nodes that own a real conversation/transcript belong in the session
    // sidebar: Agent nodes (Prompt/Clarify) and Shell nodes. Control nodes
    // (loop/ifElse/start/end) inherit a sessionId via session lineage but never
    // represent a chat round — and a loop instance that is still iterating stays
    // `running`, which would pin its whole session group to ⏳ forever even after
    // every actual round has finished. Skip them.
    if (inst.nodeType !== 'prompt' && inst.nodeType !== 'clarify' && inst.nodeType !== 'shell') {
      continue;
    }
    // Prefer displaySessionId (Shell nodes record their own transcript session
    // there); fall back to the lineage sessionId for Agent nodes.
    const displaySid = inst.displaySessionId || inst.sessionId;
    if (!displaySid) continue;
    entries.push({
      sessionId: displaySid,
      status: mapGraphInstanceStatus(inst.status),
      durationMs: inst.durationMs,
      startedAt: inst.startedAt || undefined,
    });
  }
  return entries;
}

// GRAPH_LIVE_STATUSES mirrors GraphRunProgress's LIVE_STATUSES: the run is
// still actively scheduling and producing events in these states. 'recovering'
// is intentionally excluded — a crash-recovered run is a static, resumable
// terminal that emits no new events, so the Chat page treats it as non-live
// (stops the loading spinner) and relies on the snapshot reconcile.
const GRAPH_LIVE_STATUSES = new Set<GraphRunStatus>(['pending', 'running', 'stepStopping']);

// A stop request is tiny and the backend answers in milliseconds; the long
// timeout only bounds the pathological case — behind an HTTP/1.1 hop a
// connection pool saturated by long-lived SSE streams can queue the POST
// indefinitely, which previously left the click looking dead with no error.
const STOP_REQUEST_TIMEOUT_MS = 15_000;

function stopRequestErrorMessage(err: unknown, fallback: string): string {
  return err instanceof Error ? err.message : fallback;
}

export interface QueuedMessage {
  id: string;
  content: string;
  imageUrls?: string[];
  fileAttachments?: FileAttachment[];
  modelId?: string | null;
  acpMode?: string;
  acpThoughtLevel?: string;
  agentType?: string;
  state?: 'queued' | 'blocked' | 'processing';
  error?: string;
}

interface MessageQueueItem {
  id: string;
  messages?: Array<{ content?: string; imageUrls?: string[]; fileAttachments?: FileAttachment[] }>;
  modelId?: string;
  acpMode?: string;
  acpThoughtLevel?: string;
  agentType?: string;
  state?: 'queued' | 'blocked' | 'processing';
  error?: string;
  createdAt?: number;
}

interface MessageQueueSnapshot {
  jobId: string;
  version: number;
  paused: boolean;
  pauseReason?: string;
  willContinue: boolean;
  active?: MessageQueueItem;
  items: MessageQueueItem[];
}

export interface GraphSessionEntry {
  sessionId: string;
  // 'interrupted' marks a Graph node execution that did not finish.
  status: 'running' | 'completed' | 'failed' | 'interrupted';
  durationMs?: number;
  /** Timestamp when the node execution started. */
  startedAt?: number;
}

interface UseJobChatOptions {
  existingJobId?: string;
  initialSessionId?: string;
  shareToken?: string;
  /** Optimistic first message created by the home page. It is rendered while
   * job history, agents and the SSE connection are still initializing. */
  initialUserMessage?: UserMessage;
  /** Fired when the backend returns 404 for the existing Job (deleted / never
   *  existed). Lets the parent clear the stale jobId from URL + state and
   *  route back to the workspace home, instead of leaving the user stuck on
   *  an empty chat page. */
  onJobNotFound?: (jobId: string) => void;
}

export function useJobChat(options: UseJobChatOptions = {}) {
  const { existingJobId, initialSessionId, shareToken, initialUserMessage, onJobNotFound } = options;
  const isPublic = !!shareToken;
  // Latest onJobNotFound stored in a ref so the load effect doesn't have to
  // depend on the callback identity (parent wrappers reallocate it every
  // render, which would re-fire the load effect and re-trigger the 404).
  const onJobNotFoundRef = useRef(onJobNotFound);
  useEffect(() => {
    onJobNotFoundRef.current = onJobNotFound;
  }, [onJobNotFound]);

  // Helper to build API URLs with public prefix and shareToken when in share mode
  const apiUrl = useCallback((path: string, extraParams?: Record<string, string>) => {
    const prefix = isPublic ? '/api/v1/public' : '/api/v1';
    const url = new URL(prefix + path, window.location.origin);
    if (isPublic && shareToken) {
      url.searchParams.set('shareToken', shareToken);
      if (existingJobId) url.searchParams.set('jobId', existingJobId);
    }
    if (extraParams) {
      for (const [k, v] of Object.entries(extraParams)) url.searchParams.set(k, v);
    }
    return url.pathname + url.search;
  }, [isPublic, shareToken, existingJobId]);
  const { reportDisconnect, reportReconnect } = useConnectionStatus();
  const [jobId, setJobId] = useState<string | null>(existingJobId || null);
  const jobIdRef = useRef<string | null>(existingJobId || null);
  jobIdRef.current = jobId;

  // Viewer presence (§ 结束 Hook「无人查看才通知」): the backend treats an
  // authenticated event stream as "a human is watching this Job" and keeps the
  // task-end notification hook quiet while somebody is. A live connection alone
  // is not enough evidence — the browser keeps an SSE stream alive in a hidden
  // tab — so every stream carries a per-page viewerId plus its current
  // visibility, and we re-report on visibility changes and on reconnect (a
  // reconnect re-registers the viewer server-side with whatever its URL said).
  const viewerIdRef = useRef<string>('');
  if (!viewerIdRef.current) {
    viewerIdRef.current = typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `viewer-${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
  }
  // Query params every event stream carries so the backend can register this
  // page as a viewer. A share-mode reader is not a viewer (the notification
  // belongs to the Job's owner, not to whoever opened the link), so the params
  // are omitted there.
  const viewerParams = useCallback((): Record<string, string> | undefined => {
    if (isPublic) return undefined;
    const visible = typeof document === 'undefined' || document.visibilityState === 'visible';
    return { viewerId: viewerIdRef.current, visible: visible ? '1' : '0' };
  }, [isPublic]);
  const reportViewerVisibility = useCallback((visible: boolean) => {
    if (isPublic) return;
    const id = jobIdRef.current;
    if (!id) return;
    // Fire-and-forget: a failed report only means the backend keeps its previous
    // view of this page, which at worst sends (or withholds) one notification.
    // Never surface it to the user.
    void fetch(`/api/v1/job/${encodeURIComponent(id)}/viewer-state`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ viewerId: viewerIdRef.current, visible }),
      keepalive: true,
    }).catch((err) => console.debug('[useJobChat] viewer-state report failed:', err));
  }, [isPublic]);
  const [jobTitle, setJobTitle] = useState('');
  const [jobShareTokenState, setJobShareTokenState] = useState<string | null>(null);
  const [jobShareShowWorkspaceName, setJobShareShowWorkspaceName] = useState(false);
  const [publicWorkspaceName, setPublicWorkspaceName] = useState<string | null>(null);
  // Set when /job/:id returns 404. Gates the SSE auto-connect (no point
  // hammering /events for a job that doesn't exist) and lets JobChat surface
  // a dedicated "job not found" banner instead of an empty chat.
  const [jobNotFound, setJobNotFound] = useState(false);
  const [messages, setMessages] = useState<Message[]>(() => initialUserMessage ? [initialUserMessage] : []);
  const [isLoading, setIsLoading] = useState(false);
  const [isLoadingHistory, setIsLoadingHistory] = useState(false);
  // Backend preparation-phase hint (subprocess launch / reconnect /
  // history replay / waiting for the first token). Set ONLY from
  // agent_phase custom events and reset at each round start. The streaming
  // phases (reasoning / replying / tool) are derived from the message list
  // (see activePhase below), so the handlers stay free of per-phase
  // bookkeeping. Delivered via a transient event that does not replay on
  // refresh — fine: the derived streaming phase plus the default
  // "AI 正在思考..." fallback rebuild the visible state after a reload.
  const [backendPhase, setBackendPhase] = useState<ChatPhase | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [titleGenerationError, setTitleGenerationError] = useState<string | null>(null);
  const [totalTokens, setTotalTokens] = useState(0);
  const [sessionWorkdir, setSessionWorkdir] = useState<string | null>(null);
  const [sessionModelId, setSessionModelId] = useState<string | null>(null);
  const [sessionType, setSessionType] = useState<string | null>(null);
  const [sessionACPMode, setSessionACPMode] = useState<string | null>(null);
  const [sessionACPThoughtLevel, setSessionACPThoughtLevel] = useState<string | null>(null);

  // Slash-command fast path relies on a COMMAND_SYSTEM_MESSAGE event for
  // user-visible feedback. The backend delivers it two ways: inline in the
  // POST /message response (deterministic — survives a torn-down SSE on a
  // terminal job) AND as a transient SSE broadcast (so OTHER tabs update). A
  // tab that is still SSE-connected therefore sees both copies; this set
  // remembers recently-applied event signatures so the second copy is
  // dropped instead of rendering a duplicate bubble / re-firing the action.
  const pendingCommandTimeoutRef = useRef<number | null>(null);
  const pendingCommandRef = useRef<string>('');
  const appliedCommandEventsRef = useRef<Map<string, number>>(new Map());

  const clearPendingCommandWatchdog = useCallback(() => {
    if (pendingCommandTimeoutRef.current) {
      window.clearTimeout(pendingCommandTimeoutRef.current);
      pendingCommandTimeoutRef.current = null;
    }
    pendingCommandRef.current = '';
  }, []);

  // Render a slash-command result (inline system bubble or toast) and fire any
  // embedded action. Shared by the POST-response inline path and the SSE
  // transient path so both stay in lockstep. Returns false (and does nothing)
  // when this exact event was already applied within the dedup window — the
  // two delivery paths can both reach a tab that is SSE-connected.
  const applyCommandEvent = useCallback((event: CommandSystemMessageEvent): boolean => {
    const now = Date.now();
    const sig = event.clientMessageId
      ? `client:${event.clientMessageId}`
      : `${event.command}\u0000${event.present || ''}\u0000${event.text}`;
    const seen = appliedCommandEventsRef.current;
    // Client-keyed command entries must survive for this page lifetime: an
    // unknown-result retry may return the persisted command event long after
    // the original SSE copy. Legacy events without a client ID retain the
    // short text-signature window.
    for (const [k, ts] of seen) {
      if (!k.startsWith('client:') && now - ts > 10_000) seen.delete(k);
    }
    if (seen.has(sig)) return false;
    seen.set(sig, now);

    // Any command feedback implies the command pipeline is alive; clear the
    // watchdog so we don't show a false timeout toast.
    clearPendingCommandWatchdog();
    // Transient system bubble for slash-command results. We render a
    // lightweight inline system message but don't persist it — on refresh it
    // disappears. Toast vs inline is driven by `present`.
    if (event.present === 'toast') {
      showCommandToast(event.text);
    } else {
      setMessages((prev) => [
        ...prev,
        {
          id: `cmd-${now}-${Math.random().toString(36).slice(2, 8)}`,
          role: MessageRoleEnum.SYSTEM,
          content: event.text,
          status: MessageStatusEnum.Finished,
          sessionId: activeSessionIdRef.current || '',
          createdAt: now,
          commandSource: event.command,
        } as Message,
      ]);
    }
    // Fire a custom DOM event so the parent (App.tsx) can apply the action
    // (switch workspace / bind job / new job) via the shared public entry
    // functions.
    if (event.action?.type) {
      window.dispatchEvent(new CustomEvent('quartet:command-action', { detail: event.action }));
    }
    return true;
  }, [clearPendingCommandWatchdog]);

  // Pending message queue (interactive mode only: messages composed while a run is in progress)
  const [queuedMessages, setQueuedMessages] = useState<QueuedMessage[]>([]);
  const [messageQueuePaused, setMessageQueuePaused] = useState(false);
  const [messageQueuePauseReason, setMessageQueuePauseReason] = useState<string | null>(null);
  const messageQueueVersionRef = useRef(-1);
  const messageQueueWillContinueRef = useRef(false);
  const queuedMessagesRef = useRef<QueuedMessage[]>([]);
  const knownQueuedMessagesRef = useRef<Map<string, QueuedMessage>>(new Map());
  // A message submitted while this tab considers the Job idle is already
  // rendered as an optimistic user bubble. The durable queue necessarily
  // persists that message as `queued` before its async dispatcher can claim
  // it, but that implementation transition is not a user-visible waiting
  // state. Keep those foreground IDs out of ChatInput's queue projection
  // for this page lifetime so a slower, older queue refresh cannot re-show
  // one after RUN_STARTED. Messages explicitly submitted while a run is in
  // flight are not added here and continue to render as queue pills.
  const foregroundMessageIdsRef = useRef<Set<string>>(new Set());
  const activeClientMessageIdRef = useRef<string | null>(null);

  const applyMessageQueueSnapshot = useCallback((snapshot: MessageQueueSnapshot | null | undefined) => {
    if (!snapshot || typeof snapshot.version !== 'number' || snapshot.version < messageQueueVersionRef.current) return;
    if (snapshot.jobId && jobIdRef.current && snapshot.jobId !== jobIdRef.current) return;
    messageQueueVersionRef.current = snapshot.version;
    messageQueueWillContinueRef.current = !!snapshot.willContinue;
    const projectItem = (item: MessageQueueItem): QueuedMessage => ({
      id: item.id,
      content: item.messages?.map((message) => message.content || '').filter(Boolean).join('\n') || '',
      imageUrls: item.messages?.flatMap((message) => message.imageUrls || []),
      fileAttachments: item.messages?.flatMap((message) => message.fileAttachments || []),
      modelId: item.modelId,
      acpMode: item.acpMode,
      acpThoughtLevel: item.acpThoughtLevel,
      agentType: item.agentType,
      state: item.state,
      error: item.error,
    });
    const projectedItems = (snapshot.items || []).map(projectItem);
    for (const item of projectedItems) knownQueuedMessagesRef.current.set(item.id, item);
    // A foreground dispatch can lose its immediate slot if another sender
    // wins the race, the queue is paused, or preparation fails. Those are
    // real waiting states: move the message out of the conversation and show
    // the queue pill (including the complete blocked error).
    const surfacedForegroundIds = new Set(
      projectedItems
        .filter((item, index) => foregroundMessageIdsRef.current.has(item.id) && (
          item.state === 'blocked' || snapshot.paused || !!snapshot.active || index > 0
        ))
        .map((item) => item.id),
    );
    if (surfacedForegroundIds.size > 0) {
      for (const id of surfacedForegroundIds) foregroundMessageIdsRef.current.delete(id);
      setMessages((prev) => prev.filter((message) =>
        !message.clientMessageId || !surfacedForegroundIds.has(message.clientMessageId)
      ));
    }
    const items = projectedItems.filter((item) => !foregroundMessageIdsRef.current.has(item.id));
    if (snapshot.active) {
      const active = projectItem(snapshot.active);
      knownQueuedMessagesRef.current.set(active.id, active);
      activeClientMessageIdRef.current = active.id;
      setMessages((prev) => prev.some((message) => message.id === active.id) ? prev : [...prev, {
        id: active.id, role: MessageRoleEnum.USER, content: active.content,
        createdAt: snapshot.active?.createdAt || Date.now(), status: MessageStatusEnum.Finished,
        clientMessageId: active.id, pending: true, deliveryStatus: 'sent', imageUrls: active.imageUrls, fileAttachments: active.fileAttachments,
      } as Message]);
    } else {
      activeClientMessageIdRef.current = null;
    }
    queuedMessagesRef.current = items;
    setQueuedMessages(items);
    setMessageQueuePaused(!!snapshot.paused);
    setMessageQueuePauseReason(snapshot.pauseReason || null);
    setIsLoading(!!snapshot.willContinue);
  }, []);

  const refreshMessageQueue = useCallback(async (targetJobId?: string) => {
    const id = targetJobId || jobId;
    if (!id || isPublic) return null;
    const response = await fetch(`/api/v1/job/${encodeURIComponent(id)}/message-queue`);
    if (!response.ok) throw new Error(await readHTTPError(response));
    const body = await response.json();
    const snapshot = body?.queue as MessageQueueSnapshot | undefined;
    applyMessageQueueSnapshot(snapshot);
    return snapshot || null;
  }, [applyMessageQueueSnapshot, isPublic, jobId]);

  const [isGraph, setIsGraph] = useState(false);
  const [graphRunId, setGraphRunId] = useState<string | null>(null);
  // Page-level graph state shared by the chat/session view and
  // GraphRunProgress. useJobChat owns the single live Graph SSE subscription;
  // every structural event reconciles one authoritative run snapshot here and
  // the progress component consumes that snapshot instead of opening a second
  // long-lived connection to the same endpoint.
  const [graphRunStatusSnapshot, setGraphRunStatusSnapshot] = useState<GraphRunStatusResponse | null>(null);
  const [graphStreamError, setGraphStreamError] = useState<string | null>(null);
  const applyGraphRunStatusSnapshot = useCallback((snapshot: GraphRunStatusResponse) => {
    setGraphRunStatusSnapshot(snapshot);
    setGraphStreamError(null);
    if (snapshot.run?.status) {
      setIsLoading(GRAPH_LIVE_STATUSES.has(snapshot.run.status));
    }
  }, []);
  const graphRunStatus = graphRunStatusSnapshot?.run?.status;
  // True while the bound graph run is actively scheduling (pending/running/
  // stepStopping). Distinct from isLoading: an interactive discussion turn on
  // a non-live run also flips isLoading, but only a live run owns the graph
  // event stream. While graphRunLive, the job-events SSE is not subscribed —
  // one long-lived stream per page instead of two, which matters on HTTP/1.1
  // where each stream holds a scarce per-origin connection slot.
  const graphRunLive = !!isGraph && !!graphRunStatus && GRAPH_LIVE_STATUSES.has(graphRunStatus);
  const [graphSessionStatus, setGraphSessionStatus] = useState<'idle' | 'running' | 'completed' | 'stopped' | 'failed'>('idle');

  // Job-level timing for total duration display
  const [jobStartedAt, setJobStartedAt] = useState<number | undefined>(undefined);
  const [jobFinishedAt, setJobFinishedAt] = useState<number | undefined>(undefined);

  // Interactive mode: accumulated duration across all completed turns (ms).
  // Each time a new turn starts, the previous turn's duration is added here.
  const [interactiveAccumulatedMs, setInteractiveAccumulatedMs] = useState(0);
  // Refs to capture current timing values inside callbacks without stale closures
  const jobStartedAtRef = useRef<number | undefined>(undefined);
  const jobFinishedAtRef = useRef<number | undefined>(undefined);
  // Keep refs in sync with state
  jobStartedAtRef.current = jobStartedAt;
  jobFinishedAtRef.current = jobFinishedAt;

  // Graph node session tracking
  const [graphSessions, setGraphSessionsState] = useState<GraphSessionEntry[]>([]);
  const [activeSessionId, setActiveSessionIdState] = useState<string | null>(null);
  const [endedSessionIds, setEndedSessionIds] = useState<Set<string>>(new Set());
  const [loadedSessionIds, setLoadedSessionIds] = useState<Set<string>>(new Set());
  // Ref mirror of loadedSessionIds so the background idle-prefetch callback
  // (which runs asynchronously, well after its enclosing render) can skip
  // sessions a load-on-switch already pulled in, without re-running on every
  // change or capturing a stale closure value. Synced in an effect; the
  // prefetch tolerates a one-frame lag (it also dedups via the merge and the
  // generation / cancelled guards).
  const loadedSessionIdsRef = useRef<Set<string>>(loadedSessionIds);
  useEffect(() => {
    loadedSessionIdsRef.current = loadedSessionIds;
  }, [loadedSessionIds]);
  // Sessions whose background hydration failed; will be retried on switch.
  const failedSessionIdsRef = useRef<Set<string>>(new Set());

  // Per-session agent metadata (populated during loadHistory for Graph sessions)
  const sessionMetaMapRef = useRef<Map<string, { modelId: string | null; type: string | null; acpMode: string | null; acpThoughtLevel: string | null }>>(new Map());

  // Per-session context size in tokens, keyed by session id. The composer
  // badge must show the size of the session the user is looking at — the
  // selected session in Graph mode, the newest one otherwise — so both
  // the live usage events and the history loads land here first and only the
  // displayed session's value is promoted to `totalTokens`. Without the map a
  // background Graph node session usage event while the user reads
  // iteration N) or an out-of-order parallel history load paints a foreign
  // number over the badge.
  const sessionTokensRef = useRef<Map<string, number>>(new Map());

  const [eventsReady, setEventsReady] = useState(false);
  const eventsReadyRef = useRef(false);
  const eventSseRef = useRef<SSEClient | null>(null);
  const eventStreamReadyWaitersRef = useRef<Set<(error?: Error) => void>>(new Set());
  // Gate the SSE auto-connect effect on the existing-job hydration effect
  // having seeded lastEventSeqRef.current from the snapshot. Without this
  // gate the two effects race on first mount: the SSE effect fires with an
  // empty Last-Event-ID, the server parses it as startSeq=0, and any job
  // whose buffer has GC'd past seq 0 (the steady state for anything older
  // than a few seconds) immediately responds with 410 — visible to the user
  // as "event buffer no longer contains seq=0 ... reload snapshot".
  //
  // For jobs that have no existingJobId (the new-chat flow) the buffer is
  // brand new (headSeq=0, nextSeq=0) so Subscribe(0) is legal and the gate
  // opens immediately on mount.
  const [snapshotReady, setSnapshotReady] = useState(false);
  // Bumped by sendMessage when SSE has been
  // disconnected (terminal event cleanup). Forces the auto-connect useEffect
  // to re-fire and establish a fresh SSE subscription for the new run.
  const [sseReconnectSeq, setSseReconnectSeq] = useState(0);

  const settleEventStreamReadyWaiters = useCallback((error?: Error) => {
    const waiters = [...eventStreamReadyWaitersRef.current];
    eventStreamReadyWaitersRef.current.clear();
    for (const waiter of waiters) waiter(error);
  }, []);

  const markEventStreamReady = useCallback((ready: boolean) => {
    eventsReadyRef.current = ready;
    setEventsReady(ready);
    if (ready) settleEventStreamReadyWaiters();
  }, [settleEventStreamReadyWaiters]);

  const waitForEventStreamReady = useCallback((timeoutMs = 15_000): Promise<void> => {
    const current = eventSseRef.current;
    if (eventsReadyRef.current && current && !current.isDisconnected()) {
      return Promise.resolve();
    }

    return new Promise<void>((resolve, reject) => {
      const settle = (error?: Error) => {
        window.clearTimeout(timeoutID);
        eventStreamReadyWaitersRef.current.delete(settle);
        if (error) reject(error);
        else resolve();
      };
      eventStreamReadyWaitersRef.current.add(settle);
      const timeoutID = window.setTimeout(() => {
        settle(new Error(`event stream was not ready within ${timeoutMs}ms`));
      }, timeoutMs);

      // Close the small race between the initial readiness check and adding
      // the waiter: connectUntilReady may have resolved in that interval.
      const latest = eventSseRef.current;
      if (eventsReadyRef.current && latest && !latest.isDisconnected()) settle();
    });
  }, []);

  const ensureEventStreamReady = useCallback(async (action: string): Promise<void> => {
    const current = eventSseRef.current;
    if (eventsReadyRef.current && current && !current.isDisconnected()) return;

    // The previous terminal event intentionally closes SSE. Surface that
    // real preparation step immediately while React establishes a fresh
    // subscription, then do not launch the backend run until the server has
    // registered its reader. Otherwise unbuffered agent_phase events can be
    // emitted into the disconnect window and disappear.
    setBackendPhase({ kind: 'reconnecting' });
    if (!current || current.isDisconnected()) {
      setSseReconnectSeq((seq) => seq + 1);
    }

    try {
      await waitForEventStreamReady();
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      throw new Error(`${action}: ${detail}`);
    }
  }, [waitForEventStreamReady]);

  const historyLoadedRef = useRef(false);
  // Tracks initial history hydration independently from historyLoadedRef so a
  // failed initial load can still be retried by a later graph reconcile after
  // the attempt settles. The generation prevents an older effect cleanup from
  // clearing the flag for a newer hydration of the same job.
  const historyHydratingRef = useRef(false);
  const historyHydrationGenerationRef = useRef(0);
  // All history-loading paths (initial hydration, load-on-switch, graph
  // reconcile and reconnect recovery) share this map. A large graph session can
  // otherwise be fetched and JSON-decoded several times concurrently before
  // loadedSessionIds is committed by React. Entries live only until the request
  // settles: this deliberately deduplicates in-flight work without turning the
  // browser into a second long-lived history cache.
  const historyLoadsInFlightRef = useRef<Map<string, Promise<Message[]>>>(new Map());
  // Resume sequence handed back by the snapshot endpoint (job.lastEventSeq).
  // Updated whenever syncJobState fetches the snapshot and consumed by the
  // SSE connect path so reconnects after a 410 / page refresh resume from
  // the right point in the per-job buffer instead of always restarting at
  // the tail.
  const lastEventSeqRef = useRef<string>('');
  const resumeGoneErrorRef = useRef<string>('');
  // Generation counter for syncJobState concurrency control. Incremented on
  // every call; stale responses (where captured gen < current) are discarded.
  const syncGenerationRef = useRef<number>(0);
  // Buffer SSE events that arrive before history is loaded so they are not lost.
  const pendingEventsRef = useRef<AgentEvent[]>([]);
  const graphSessionsRef = useRef<GraphSessionEntry[]>([]);
  const activeSessionIdRef = useRef<string | null>(null);
  const followLatestSessionRef = useRef(true);
  // Wall-clock timestamp of the last SSE event received (any type, including
  // keep-alive surrogates). Used by the idle-watchdog inside the SSE
  // auto-connect effect to detect the "subscriber stuck but connection still
  // alive" case — when the job appears to be running but the stream has
  // gone silent, we resync /job/:id to recover any missed terminal status.
  const lastEventReceivedAtRef = useRef<number>(Date.now());
  // Mirror of isLoading so the watchdog interval can read it without
  // re-subscribing the SSE effect on every loading-state flip.
  const isLoadingRef = useRef(false);

  const setGraphSessions = useCallback((value: SetStateAction<GraphSessionEntry[]>) => {
    setGraphSessionsState((prev) => {
      const next = typeof value === 'function'
        ? (value as (prevState: GraphSessionEntry[]) => GraphSessionEntry[])(prev)
        : value;
      graphSessionsRef.current = next;
      return next;
    });
  }, []);

  const applyActiveSessionSelection = useCallback((sessionId: string | null, followLatest: boolean) => {
    followLatestSessionRef.current = followLatest;
    activeSessionIdRef.current = sessionId;
    setActiveSessionIdState(sessionId);
  }, []);

  const setActiveSessionId = useCallback((sessionId: string | null) => {
    applyActiveSessionSelection(
      sessionId,
      sessionId !== null && sessionId === getLastGraphSessionId(graphSessionsRef.current)
    );
  }, [applyActiveSessionSelection]);

  // Record a session's context size and promote it to the composer badge when
  // that session is the one on screen. `activeSessionIdRef` is only set in
  // Graph mode, so a null active session means single-conversation mode
  // where every usage report belongs to the session being displayed.
  //
  // 0 is a legal value (a freshly created session has an empty context), so the
  // caller-side checks are `!= null`, not truthiness — otherwise switching to a
  // brand-new session leaves the previous session's number on the badge.
  const recordSessionTokens = useCallback((sessionId: string | null | undefined, tokens: number) => {
    if (!Number.isFinite(tokens) || tokens < 0) return;
    if (!sessionId) {
      // Defensive: every backend usage event carries a session id. With
      // nothing to key on, treat it as the current conversation's count.
      setTotalTokens(tokens);
      return;
    }
    sessionTokensRef.current.set(sessionId, tokens);
    const displayedSessionId = activeSessionIdRef.current;
    if (!displayedSessionId || displayedSessionId === sessionId) setTotalTokens(tokens);
  }, []);

  // Mirror isLoading -> ref so the SSE idle-watchdog interval can read the
  // current value without re-subscribing the SSE effect on every flip.
  useEffect(() => {
    isLoadingRef.current = isLoading;
  }, [isLoading]);

  useEffect(() => {
    if (!existingJobId || isPublic || !snapshotReady || isGraph) return;
    let cancelled = false;
    const refresh = () => {
      void refreshMessageQueue(existingJobId).catch((err) => {
        if (!cancelled) {
          console.warn('[messageQueue] initial refresh failed:', err);
          setError(err instanceof Error ? err.message : String(err));
        }
      });
    };
    refresh();
    const onVisible = () => { if (document.visibilityState === 'visible') refresh(); };
    document.addEventListener('visibilitychange', onVisible);
    return () => { cancelled = true; document.removeEventListener('visibilitychange', onVisible); };
  }, [existingJobId, isGraph, isPublic, refreshMessageQueue, snapshotReady]);

  // Reset all state when existingJobId changes so a reused component
  // does not briefly show stale data from the previous job.
  useEffect(() => {
    historyLoadedRef.current = false;
    historyHydratingRef.current = false;
    ++historyHydrationGenerationRef.current;
    pendingEventsRef.current = [];
    // Invalidate any in-flight syncJobState responses from the previous job —
    // bumping the generation ensures their stale-check fails on arrival.
    ++syncGenerationRef.current;
    // Reset server-clock calibration: the estimate is derived from this job's
    // event stream. Reusing the previous job's latestServerTs can break live
    // duration ticks when switching from a running job to a historical job.
    serverClockRef.current = null;
    // Drop the previous job's resume seq — it lives in a different per-job
    // sequence space and would 410 immediately if reused on the new job.
    lastEventSeqRef.current = '';
    setJobId(existingJobId || null);
    setJobTitle('');
    setJobShareShowWorkspaceName(false);
    setPublicWorkspaceName(null);
    setMessages(initialUserMessage ? [initialUserMessage] : []);
    setQueuedMessages([]);
    queuedMessagesRef.current = [];
    knownQueuedMessagesRef.current.clear();
    foregroundMessageIdsRef.current.clear();
    messageQueueVersionRef.current = -1;
    messageQueueWillContinueRef.current = false;
    setMessageQueuePaused(false);
    setMessageQueuePauseReason(null);
    activeClientMessageIdRef.current = null;
    setIsGraph(false);
    setGraphRunId(null);
    setGraphSessionStatus('idle');
    setGraphSessions([]);
    applyActiveSessionSelection(null, true);
    setEndedSessionIds(new Set());
    settleEventStreamReadyWaiters(new Error('event stream changed before it became ready'));
    markEventStreamReady(false);
    setBackendPhase(null);
    // For an existing job the snapshot fetch is what populates
    // lastEventSeqRef.current; the SSE effect must wait for it. For the
    // new-chat flow (no existingJobId) the buffer is brand new so seq=0
    // is legal and the gate opens straight away.
    setSnapshotReady(!existingJobId);
    // [TRACE-SEQ0] gate initial state on jobId change.
    console.debug(`[JobEvents][TRACE-SEQ0] gate-init existingJobId=${existingJobId ?? '(none)'} snapshotReadyInitial=${!existingJobId} lastEventSeqRef=${JSON.stringify(lastEventSeqRef.current)}`);
    sessionMetaMapRef.current = new Map();
    sessionTokensRef.current = new Map();
    setTotalTokens(0);
    setError(null);
    setIsLoading(false);
    setIsLoadingHistory(false);
    // Clear round timestamps so the previous job's "total duration" badge
    // doesn't leak into the newly selected job. Both the hydration fetch
    // and the JOB_STARTED replay use `prev ?? ...`, which cannot overwrite
    // stale values — they must start undefined on every job switch.
    setJobStartedAt(undefined);
    setJobFinishedAt(undefined);
    setInteractiveAccumulatedMs(0);
  }, [existingJobId, initialUserMessage, applyActiveSessionSelection, markEventStreamReady, setGraphSessions, settleEventStreamReadyWaiters]);

  const handleEventRef = useRef<(event: AgentEvent) => void>(() => {});
  // Ref to syncJobState so handleEvent can call it without a dependency cycle
  // (syncJobState is defined after handleEvent). Updated alongside handleEventRef.
  const syncJobStateRef = useRef<(id: string, metadataOnly?: boolean, forceSkipMessages?: boolean) => Promise<void>>(() => Promise.resolve());
  // Server-clock estimate for DurationBadge live ticks. Without this, the
  // live tick reads `Date.now() - event.timestamp`, which mixes client
  // (browser) and server (backend) wall clocks. Skew between them, SSE
  // delivery latency, or ring-buffer replay of old events all cause the
  // live elapsed to be inflated while the finished elapsed (server-only
  // subtraction) is correct — producing a visible "3m → 300ms" jump on
  // completion. The offset lets live ticks compute elapsed in the same
  // reference frame as the finished value.
  //
  // Rule: take max(event.timestamp), not last, so burst-delivered replayed
  // events with old timestamps do not pull the estimate back into the past
  // once a newer real-time event has been seen.
  const serverClockRef = useRef<{ latestServerTs: number; clientReceivedAtMs: number } | null>(null);
  const SERVER_CLOCK_STALE_EVENT_TOLERANCE_MS = 30_000;
  const updateServerClock = useCallback((serverTs: number | undefined) => {
    if (typeof serverTs !== 'number' || !Number.isFinite(serverTs) || serverTs <= 0) return;
    const prev = serverClockRef.current;
    const clientNow = Date.now();
    if (prev != null) {
      const projectedNow = prev.latestServerTs + (clientNow - prev.clientReceivedAtMs);
      // A freshly opened long-running job can replay old events. Treating an
      // old event timestamp as
      // "server now" makes a running DurationBadge compute now≈startedAt and
      // show 0ms until the projection catches up. Once an HTTP snapshot has
      // seeded the real server wall clock, only let events move the estimate
      // forward or near-forward; never rewind it to stale replay time.
      if (serverTs < projectedNow - SERVER_CLOCK_STALE_EVENT_TOLERANCE_MS) return;
    } else if (serverTs < clientNow - SERVER_CLOCK_STALE_EVENT_TOLERANCE_MS) {
      // No snapshot seed yet: don't let an old ring-buffer event establish a
      // bad zero-duration clock. Real-time events are close to Date.now(); old
      // replay is not.
      return;
    }
    if (prev == null || serverTs > prev.latestServerTs) {
      serverClockRef.current = { latestServerTs: serverTs, clientReceivedAtMs: clientNow };
    }
  }, []);
  const seedServerClockFromResponse = useCallback((res: Response) => {
    const dateHeader = res.headers.get('date');
    if (!dateHeader) return;
    const serverNow = new Date(dateHeader).getTime();
    if (!Number.isFinite(serverNow) || serverNow <= 0) return;
    const clientNow = Date.now();
    const prev = serverClockRef.current;
    if (prev == null || serverNow > prev.latestServerTs) {
      serverClockRef.current = { latestServerTs: serverNow, clientReceivedAtMs: clientNow };
    }
  }, []);
  const getServerNowEstimate = useCallback(() => {
    const entry = serverClockRef.current;
    if (entry == null) return Date.now();
    return entry.latestServerTs + (Date.now() - entry.clientReceivedAtMs);
  }, []);

  // Finalize local in-flight assistant/tool messages when the job reaches a
  // terminal state. Extracted so both the real-time terminal event handlers
  // and the snapshot-based recovery path (syncJobState) can share the logic.
  const finalizeInFlightMessages = useCallback((ts: number, opts: {
    toolProcessingStatus: ToolCallStatusEnum;
    placeholderReason?: string;
  }) => {
    setMessages((prev) => prev.map((msg) => {
      if (msg.status === MessageStatusEnum.Finished) return msg;
      if (msg.status === MessageStatusEnum.Error) return msg;

      if (msg.role === MessageRoleEnum.TOOL) {
        const toolMsg = msg as ToolMessage;
        const isProcessing = toolMsg.toolCallStatus === ToolCallStatusEnum.Processing;
        const nextStatus = isProcessing ? opts.toolProcessingStatus : toolMsg.toolCallStatus;
        return {
          ...toolMsg,
          status: MessageStatusEnum.Finished,
          toolCallStatus: nextStatus,
          placeholderReason: (nextStatus === ToolCallStatusEnum.Placeholder)
            ? (toolMsg.placeholderReason ?? opts.placeholderReason)
            : toolMsg.placeholderReason,
          finishedAt: toolMsg.finishedAt ?? ts,
        };
      }

      if (msg.role === MessageRoleEnum.ASSISTANT) {
        const assistantMsg = msg as AssistantMessage;
        return {
          ...assistantMsg,
          status: MessageStatusEnum.Finished,
          isThinking: false,
          finishedAt: assistantMsg.finishedAt ?? ts,
          thinkingFinishedAt: assistantMsg.thinkingFinishedAt
            ?? (assistantMsg.isThinking ? ts : undefined),
        };
      }

      return { ...msg, status: MessageStatusEnum.Finished };
    }));
  }, [setMessages]);

  // Handle interactive job events and translated Graph Agent events.
  const handleEvent = useCallback((event: AgentEvent) => {
    // Track the server clock from every event as close to arrival time as
    // possible, so DurationBadge live ticks can project server-now without
    // mixing reference frames. Done before the pre-history buffer check so
    // a running job's SSE traffic seeds the offset even before history is
    // ready. See serverClockRef declaration for the reason-frame rationale.
    updateServerClock(event.timestamp);

    // Idle-watchdog liveness ping: any received event refreshes the silence
    // timer so resyncing only fires when the stream is genuinely quiet.
    lastEventReceivedAtRef.current = Date.now();

    // Buffer SSE events that arrive before history is loaded from the API.
    // Previously these were dropped, causing messages to go missing on page
    // refresh for running jobs. They will be replayed once history loads.
    if (!historyLoadedRef.current) {
      pendingEventsRef.current.push(event);
      return;
    }

    switch (event.type) {
      // Job-level events
      case EventTypeEnum.JOB_STARTED:
        setBackendPhase(null);
        setJobStartedAt((prev) => prev ?? event.timestamp);
        setJobFinishedAt(undefined);
        break;

      case EventTypeEnum.JOB_COMPLETED: {
        const runOutcome = event.runOutcome ?? 'completed';
        setJobFinishedAt(event.timestamp || Date.now());
        setIsLoading(messageQueueWillContinueRef.current);
        finalizeInFlightMessages(event.timestamp || Date.now(), {
          toolProcessingStatus:
            runOutcome === 'completed' ? ToolCallStatusEnum.Success
              : ToolCallStatusEnum.Placeholder,
          placeholderReason:
            runOutcome === 'stopped' ? 'interrupted'
              : runOutcome === 'failed' ? 'job_failed'
                : undefined,
        });
        // Sync persisted state (title, progress, lastEventSeq) now that
        // the run is done. Previously done by the [DONE] → re-subscribe
        // path; with persistent SSE connections this is the only trigger.
        // Pass forceSkipMessages=true: messages were already delivered by
        // the live SSE stream; reloading from disk would race with the
        // in-memory state and produce duplicates (history IDs can differ
        // from streaming IDs for thinking/tool messages).
        if (event.jobId) void refreshMessageQueue(event.jobId).then((queue) => {
          if (queue?.willContinue) { setIsLoading(true); return; }
        }).catch((err) => console.warn('[useJobChat] JOB_COMPLETED queue sync failed:', err));
        if (event.jobId) syncJobStateRef.current(event.jobId, undefined, true).catch((err) => console.warn('[useJobChat] JOB_COMPLETED syncJobState failed:', err));
        break;
      }

      case EventTypeEnum.JOB_STOPPED: {
        const runOutcome = event.runOutcome ?? 'stopped';
        setJobFinishedAt(event.timestamp || Date.now());
        setIsLoading(messageQueueWillContinueRef.current);
        finalizeInFlightMessages(event.timestamp || Date.now(), {
          toolProcessingStatus:
            runOutcome === 'completed' ? ToolCallStatusEnum.Success
              : ToolCallStatusEnum.Placeholder,
          placeholderReason:
            runOutcome === 'failed' ? 'job_failed' : 'interrupted',
        });
        if (event.jobId) void refreshMessageQueue(event.jobId).then((queue) => {
          if (queue?.willContinue) { setIsLoading(true); return; }
        }).catch((err) => console.warn('[useJobChat] JOB_STOPPED queue sync failed:', err));
        if (event.jobId) syncJobStateRef.current(event.jobId, undefined, true).catch((err) => console.warn('[useJobChat] JOB_STOPPED syncJobState failed:', err));
        break;
      }

      case EventTypeEnum.JOB_FAILED: {
        const runOutcome = event.runOutcome ?? 'failed';
        setJobFinishedAt(event.timestamp || Date.now());
        setError(event.message);
        setIsLoading(messageQueueWillContinueRef.current);
        finalizeInFlightMessages(event.timestamp || Date.now(), {
          toolProcessingStatus:
            runOutcome === 'completed' ? ToolCallStatusEnum.Success
              : ToolCallStatusEnum.Placeholder,
          placeholderReason:
            runOutcome === 'stopped' ? 'interrupted' : 'job_failed',
        });
        if (event.jobId) void refreshMessageQueue(event.jobId).then((queue) => {
          if (queue?.willContinue) { setIsLoading(true); return; }
        }).catch((err) => console.warn('[useJobChat] JOB_FAILED queue sync failed:', err));
        if (event.jobId) syncJobStateRef.current(event.jobId, undefined, true).catch((err) => console.warn('[useJobChat] JOB_FAILED syncJobState failed:', err));
        break;
      }

      // Agent-level events (same as useAgentChat)
      case EventTypeEnum.RUN_STARTED:
        setIsLoading(true);
        setError(null);
        if (event.clientMessageId) {
          activeClientMessageIdRef.current = event.clientMessageId;
          const queued = knownQueuedMessagesRef.current.get(event.clientMessageId);
          setMessages((prev) =>
            (queued && !prev.some((message) => message.id === queued.id)
              ? [...prev, {
                  id: queued.id,
                  role: MessageRoleEnum.USER,
                  content: queued.content,
                  createdAt: event.timestamp || Date.now(),
                  status: MessageStatusEnum.Finished,
                  sessionId: event.sessionId || undefined,
                  clientMessageId: queued.id,
                  pending: false,
                  deliveryStatus: 'sent',
                  imageUrls: queued.imageUrls,
                  fileAttachments: queued.fileAttachments,
                } as Message]
              : prev
            ).map((message) =>
              message.role === MessageRoleEnum.USER && message.clientMessageId === event.clientMessageId
                ? {
                    ...message,
                    sessionId: event.sessionId || message.sessionId,
                    pending: false,
                    failed: false,
                    deliveryStatus: 'sent',
                    sendError: undefined,
                  }
                : message
            )
          );
        }
        setJobStartedAt(event.timestamp || Date.now());
        setJobFinishedAt(undefined);
        break;

      case EventTypeEnum.RUN_FINISHED:
        activeClientMessageIdRef.current = null;
        setIsLoading(messageQueueWillContinueRef.current);
        setJobFinishedAt((prev) => prev ?? (event.timestamp || Date.now()));
        setMessages((prev) => {
          const ts = event.timestamp || Date.now();
          return prev.map((msg) => {
            if (msg.status === MessageStatusEnum.Finished) return msg;
            if (msg.role === MessageRoleEnum.TOOL) {
              const toolMsg = msg as ToolMessage;
              return { ...toolMsg, status: MessageStatusEnum.Finished, toolCallStatus: toolMsg.toolCallStatus === ToolCallStatusEnum.Processing ? ToolCallStatusEnum.Success : toolMsg.toolCallStatus, finishedAt: toolMsg.finishedAt ?? ts };
            }
            if (msg.role === MessageRoleEnum.ASSISTANT) {
              const assistantMsg = msg as AssistantMessage;
              return { ...assistantMsg, status: MessageStatusEnum.Finished, isThinking: false, finishedAt: assistantMsg.finishedAt ?? ts, thinkingFinishedAt: assistantMsg.thinkingFinishedAt ?? (assistantMsg.isThinking ? ts : undefined) };
            }
            return { ...msg, status: MessageStatusEnum.Finished };
          });
        });
        break;

      case EventTypeEnum.RUN_ERROR:
        activeClientMessageIdRef.current = null;
        setIsLoading(messageQueueWillContinueRef.current);
        // Mirror RUN_FINISHED so the ChatInput badge doesn't briefly disappear
        // between RUN_ERROR and the subsequent JOB_* terminal event.
        setJobFinishedAt((prev) => prev ?? (event.timestamp || Date.now()));
        setError(event.message);
        break;

      case EventTypeEnum.TEXT_MESSAGE_START: {
        const isThinking = event.external?.isThinking ?? false;
        const isShellOutput = event.external?.isShellOutput === true;
        const newMessage: AssistantMessage = {
          id: event.messageId,
          role: MessageRoleEnum.ASSISTANT,
          content: '',
          createdAt: event.timestamp,
          status: MessageStatusEnum.Started,
          name: event.name,
          thinkingContent: '',
          isThinking,
          isShellOutput,
          sessionId: event.sessionId,
        };
        setMessages((prev) => {
          const existingIndex = prev.findIndex((m) => m.id === event.messageId);
          if (existingIndex >= 0) {
            // Don't regress a Finished message (e.g. from history) with a
            // replayed START event that carries empty content.
            if (prev[existingIndex].status === MessageStatusEnum.Finished) {
              return prev;
            }
            const updated = [...prev];
            updated[existingIndex] = { ...updated[existingIndex], ...newMessage };
            return updated;
          }
          return [...prev, newMessage];
        });
        break;
      }

      case EventTypeEnum.TEXT_MESSAGE_CONTENT: {
        const isThinking = event.external?.isThinking ?? false;
        setMessages((prev) => {
          const idx = prev.findIndex((m) => m.id === event.messageId && m.role === MessageRoleEnum.ASSISTANT);
          if (idx < 0) {
            // No bubble for this messageId — the TEXT_MESSAGE_START was
            // missed (e.g. graph SSE reconnect from the buffer tail after
            // a page refresh). Auto-create the bubble so streaming
            // resumes visibly instead of silently dropping every delta.
            const newMsg: AssistantMessage = {
              id: event.messageId,
              role: MessageRoleEnum.ASSISTANT,
              content: isThinking ? '' : (event.delta || ''),
              createdAt: event.timestamp,
              status: MessageStatusEnum.Started,
              thinkingContent: isThinking ? (event.delta || '') : '',
              isThinking,
              isShellOutput: false,
              sessionId: event.sessionId,
            };
            return [...prev, newMsg];
          }
          return prev.map((msg, i) => {
            if (i !== idx) return msg;
            // Don't append delta to an already-Finished message from history.
            if (msg.status === MessageStatusEnum.Finished) return msg;
            const assistantMsg = msg as AssistantMessage;
            if (isThinking) {
              return {
                ...assistantMsg,
                thinkingContent: (assistantMsg.thinkingContent || '') + event.delta,
                isThinking: true,
              };
            }
            // Transition from thinking to non-thinking: record thinkingFinishedAt
            const thinkingFinishedAt = assistantMsg.isThinking && !assistantMsg.thinkingFinishedAt
              ? (event.timestamp || Date.now())
              : assistantMsg.thinkingFinishedAt;
            return {
              ...assistantMsg,
              content: assistantMsg.content + event.delta,
              isThinking: false,
              thinkingFinishedAt,
            };
          });
        });
        break;
      }

      case EventTypeEnum.TEXT_MESSAGE_END:
        setMessages((prev) =>
          prev.map((msg) => {
            if (msg.id === event.messageId && msg.role === MessageRoleEnum.ASSISTANT) {
              const assistantMsg = msg as AssistantMessage;
              // Already has finishedAt (e.g. set by RUN_FINISHED arriving first) — skip
              if (assistantMsg.finishedAt) return msg;
              const finishedAt = event.timestamp || Date.now();
              // If thinking never transitioned, set thinkingFinishedAt = finishedAt
              const thinkingFinishedAt = assistantMsg.isThinking && !assistantMsg.thinkingFinishedAt
                ? finishedAt
                : assistantMsg.thinkingFinishedAt;
              return { ...assistantMsg, status: MessageStatusEnum.Finished, isThinking: false, finishedAt, thinkingFinishedAt };
            }
            return msg;
          })
        );
        break;

      case EventTypeEnum.TOOL_CALL_START: {
        const toolMessage: ToolMessage = {
          id: event.toolCallId,
          role: MessageRoleEnum.TOOL,
          content: '',
          createdAt: event.timestamp,
          status: MessageStatusEnum.Started,
          toolCallId: event.toolCallId,
          toolCallName: (event.toolCallName && event.toolCallName !== 'undefined') ? event.toolCallName : '',
          toolCallArgs: '',
          toolCallStatus: event.toolCallStatus || ToolCallStatusEnum.Processing,
          parentMessageId: event.parentMessageId,
          sessionId: event.sessionId,
        };
        setMessages((prev) => {
          // Deduplicate: if a tool message with this ID already exists (e.g. from
          // history load), skip if already Finished, otherwise update.
          const existingIndex = prev.findIndex((m) => m.id === event.toolCallId);
          if (existingIndex >= 0) {
            if (prev[existingIndex].status === MessageStatusEnum.Finished) {
              return prev;
            }
            const updated = [...prev];
            updated[existingIndex] = { ...updated[existingIndex], ...toolMessage };
            return updated;
          }
          return [...prev, toolMessage];
        });
        break;
      }

      case EventTypeEnum.TOOL_CALL_ARGS:
        setMessages((prev) => {
          const idx = prev.findIndex((m) => m.id === event.toolCallId && m.role === MessageRoleEnum.TOOL);
          if (idx < 0) {
            // TOOL_CALL_START missed (graph SSE reconnect). Create the bubble.
            const newTool: ToolMessage = {
              id: event.toolCallId,
              role: MessageRoleEnum.TOOL,
              content: '',
              createdAt: event.timestamp,
              status: MessageStatusEnum.Started,
              toolCallId: event.toolCallId,
              toolCallName: '',
              toolCallArgs: event.delta || '',
              toolCallStatus: event.toolCallStatus || ToolCallStatusEnum.Processing,
              sessionId: event.sessionId,
            };
            return [...prev, newTool];
          }
          return prev.map((msg, i) => {
            if (i !== idx) return msg;
            if (msg.status === MessageStatusEnum.Finished) return msg;
            const toolMsg = msg as ToolMessage;
            return {
              ...toolMsg,
              toolCallArgs: event.replace ? event.delta : toolMsg.toolCallArgs + event.delta,
              toolCallStatus: event.toolCallStatus || toolMsg.toolCallStatus,
            };
          });
        });
        break;

      case EventTypeEnum.TOOL_CALL_RESULT:
        setMessages((prev) => {
          const idx = prev.findIndex((m) => m.id === event.toolCallId && m.role === MessageRoleEnum.TOOL);
          if (idx < 0) {
            // TOOL_CALL_START missed (graph SSE reconnect). Create the bubble.
            const newTool: ToolMessage = {
              id: event.toolCallId,
              role: MessageRoleEnum.TOOL,
              content: event.delta || '',
              createdAt: event.timestamp,
              status: MessageStatusEnum.Started,
              toolCallId: event.toolCallId,
              toolCallName: '',
              toolCallArgs: '',
              toolCallStatus: event.toolCallStatus || ToolCallStatusEnum.Processing,
              sessionId: event.sessionId,
            };
            return [...prev, newTool];
          }
          return prev.map((msg, i) => {
            if (i !== idx) return msg;
            if (msg.status === MessageStatusEnum.Finished) return msg;
            const toolMsg = msg as ToolMessage;
            return { ...toolMsg, content: toolMsg.content + event.delta, toolCallStatus: event.toolCallStatus };
          });
        });
        break;

      case EventTypeEnum.TOOL_CALL_END:
        setMessages((prev) =>
          prev.map((msg) => {
            if (msg.id === event.toolCallId && msg.role === MessageRoleEnum.TOOL) {
              const toolMsg = msg as ToolMessage;
              const nextStatus = event.toolCallStatus || ToolCallStatusEnum.Success;
              // For Placeholder (run interrupted/superseded), carry the
              // reason string the backend passed via event.external so the
              // bubble tooltip matches what history reload will show.
              let placeholderReason = toolMsg.placeholderReason;
              if (nextStatus === ToolCallStatusEnum.Placeholder) {
                const reason = event.external?.placeholderReason;
                if (typeof reason === 'string' && reason) {
                  placeholderReason = reason;
                } else if (!placeholderReason) {
                  placeholderReason = 'interrupted';
                }
              }
              return {
                ...toolMsg,
                status: MessageStatusEnum.Finished,
                toolCallStatus: nextStatus,
                placeholderReason,
                finishedAt: event.timestamp || Date.now(),
              };
            }
            return msg;
          })
        );
        break;

      case EventTypeEnum.TOOL_CALL_STITCHED:
        // Late-arriving terminal for a tool call that was already closed
        // as Placeholder by an eager-flush supersede. Unlike TOOL_CALL_RESULT
        // / TOOL_CALL_END this MUST update messages already in the
        // Finished state so the open page rewrites the placeholder bubble
        // in place — without it, the live UI shows "interrupted" until a
        // refresh even though disk + memory now hold the real result.
        setMessages((prev) =>
          prev.map((msg) => {
            if (msg.id === event.toolCallId && msg.role === MessageRoleEnum.TOOL) {
              const toolMsg = msg as ToolMessage;
              const nextStatus = event.toolCallStatus || ToolCallStatusEnum.Success;
              return {
                ...toolMsg,
                content: event.delta,
                status: MessageStatusEnum.Finished,
                toolCallStatus: nextStatus,
                placeholderReason: undefined,
                finishedAt: event.timestamp || toolMsg.finishedAt || Date.now(),
              };
            }
            return msg;
          })
        );
        break;

      case EventTypeEnum.CUSTOM:
        if (event.name === 'agent_phase') {
          // Preparation-window hint from the ACP agent (transient). Streaming
          // phases are derived from the message list, so only the backend
          // preparation phase is stored here.
          const v = event.value as { phase?: string; detail?: string } | null;
          const kind = backendPhaseKind(v?.phase);
          setBackendPhase(kind ? { kind, detail: v?.detail } : null);
          break;
        }
        if (event.name === 'token_usage') {
          const usage = event.value as { totalTokens?: number };
          if (usage?.totalTokens != null) recordSessionTokens(event.sessionId, usage.totalTokens);
        }
        if (event.name === 'job_title_updated') {
          const payload = event.value as { title?: string } | string | null;
          const nextTitle = typeof payload === 'string' ? payload : payload?.title;
          if (nextTitle) setJobTitle(nextTitle);
        }
        if (event.name === 'job_title_generation_failed') {
          const payload = event.value as { error?: string } | string | null;
          const nextError = typeof payload === 'string' ? payload : payload?.error;
          if (nextError) setTitleGenerationError(nextError);
        }
        if (event.name === 'job_title_generation_error_cleared') {
          setTitleGenerationError(null);
        }
        if (event.name === 'message_queue_changed') {
          void refreshMessageQueue(event.jobId).catch((err) => {
            console.warn('[messageQueue] refresh after event failed:', err);
          });
        }
        break;

      case EventTypeEnum.COMMAND_SYSTEM_MESSAGE:
        applyCommandEvent(event as CommandSystemMessageEvent);
        break;

      default:
        break;
    }
  }, [applyActiveSessionSelection, applyCommandEvent, finalizeInFlightMessages, markEventStreamReady, recordSessionTokens, refreshMessageQueue, setGraphSessions, updateServerClock]);

  // Keep ref in sync so the SSE effect always uses the latest handler
  handleEventRef.current = handleEvent;

  // Load history for existing job
  const loadHistory = useCallback((sid: string, tagSessionId?: string): Promise<Message[]> => {
    const inFlight = historyLoadsInFlightRef.current.get(sid);
    if (inFlight) return inFlight;

    // Read synchronously so a response landing after the user switched jobs is
    // recognisable as stale — see the token bookkeeping below. Call-site
    // generation / cancelled guards cover the message merge, but this runs
    // inside the request.
    const requestJobId = jobIdRef.current;

    const request = (async () => {
      const response = await fetch(apiUrl(`/sessions/${sid}/messages`));
      if (!response.ok) {
        if (response.status === 404) return [];
        throw new Error(`Failed to load history for session ${sid} (HTTP ${response.status})`);
      }
      const data = await response.json();

      // Public share responses carry the minimal display info of the Agent
      // this session references. Prime the shared display cache with it (the
      // share page never calls the private resolve endpoint); a referenced
      // Agent missing from the map is unresolvable — rendered as unknown.
      if (isPublic) {
        primeAgentDisplays(data.agents);
        if (data.type && !data.agents?.[data.type]) markAgentDisplayUnknown(data.type, true);
      }

      // Always store per-session metadata for Graph sessions
      const metaKey = tagSessionId || sid;
      sessionMetaMapRef.current.set(metaKey, {
        modelId: data.modelId || null,
        type: data.type || null,
        acpMode: data.acpMode || null,
        acpThoughtLevel: data.acpThoughtLevel || null,
      });
      // Keyed by the real session id (not metaKey) and recorded for EVERY
      // session, tagged or not: Graph pages only ever load tagged
      // sessions, so gating this on `!tagSessionId` like the metadata below
      // left their badge stuck at 0 until a live usage event arrived — i.e.
      // permanently for a finished run. Skipped when the job changed under us
      // so a late response can't paint the previous job's number.
      if (jobIdRef.current === requestJobId) {
        recordSessionTokens(sid, data.tokenUsage?.totalTokens ?? 0);
      }

      if (!tagSessionId) {
        setSessionModelId(data.modelId || null);
        if (data.type) setSessionType(data.type);
        if (data.acpMode) setSessionACPMode(data.acpMode);
        if (data.acpThoughtLevel) setSessionACPThoughtLevel(data.acpThoughtLevel);
        if (data.workdir) setSessionWorkdir(data.workdir);
      }

      const historyMessages = data.messages || [];
      const converted: Message[] = [];
      // Tool results reference an earlier assistant tool call. Keep the first
      // converted index for each call ID so matching thousands of persisted
      // results stays O(n), instead of scanning the growing array for every
      // result (O(n²) on long sessions).
      const toolMessageIndexByCallId = new Map<string, number>();
      const now = Date.now();

      for (const msg of historyMessages) {
        if (msg.role === 'user') {
          converted.push({ id: msg.id, role: MessageRoleEnum.USER, content: msg.content, createdAt: msg.startedAt || now, status: MessageStatusEnum.Finished, sessionId: tagSessionId, pending: false, failed: false, imageUrls: msg.imageUrls || undefined, fileAttachments: msg.fileAttachments || undefined, isShellOutput: msg.isShellOutput || false });
        } else if (msg.role === 'assistant') {
          if (msg.isThinking) {
            // Separate thought entry emitted by the history API when thought_msg_id is present.
            converted.push({ id: msg.id, role: MessageRoleEnum.ASSISTANT, content: '', createdAt: msg.startedAt || now, status: MessageStatusEnum.Finished, thinkingContent: msg.reasoningContent || '', isThinking: false, isShellOutput: false, sessionId: tagSessionId, finishedAt: msg.finishedAt || undefined, thinkingFinishedAt: msg.thoughtFinishedAt || undefined });
          } else {
            converted.push({ id: msg.id, role: MessageRoleEnum.ASSISTANT, content: msg.content, createdAt: msg.startedAt || now, status: MessageStatusEnum.Finished, thinkingContent: msg.reasoningContent || '', isThinking: false, isShellOutput: msg.isShellOutput || false, sessionId: tagSessionId, finishedAt: msg.finishedAt || undefined, thinkingFinishedAt: msg.thoughtFinishedAt || undefined });
          }
          if (msg.toolCalls) {
            // Legacy history may not carry per-tool `startedAt`. In that case,
            // approximate the tool start with the assistant/thinking end
            // boundary instead of `Date.now()`, otherwise a historical
            // `finishedAt` combined with a fresh page-load timestamp would
            // produce a negative elapsed and hide the badge entirely.
            const toolCreatedAtFallback = msg.finishedAt || msg.thoughtFinishedAt || msg.startedAt || now;
            for (const tc of msg.toolCalls) {
              const toolIndex = converted.length;
              converted.push({ id: tc.id, role: MessageRoleEnum.TOOL, content: '', createdAt: toolCreatedAtFallback, status: MessageStatusEnum.Started, toolCallId: tc.id, toolCallName: (tc.name && tc.name !== 'undefined') ? tc.name : '', toolCallArgs: tc.arguments, toolCallStatus: ToolCallStatusEnum.Processing, parentMessageId: msg.id, sessionId: tagSessionId });
              // Preserve the old findIndex semantics if malformed history
              // contains the same tool-call ID more than once: the first call
              // remains the result target.
              if (!toolMessageIndexByCallId.has(tc.id)) {
                toolMessageIndexByCallId.set(tc.id, toolIndex);
              }
            }
          }
        } else if (msg.role === 'tool') {
          const idx = toolMessageIndexByCallId.get(msg.toolCallId);
          if (idx !== undefined) {
            // Priority: placeholder > failed > success. Placeholder
            // indicates a synthesised result (run cancelled /
            // interrupted / superseded) and must not be painted as
            // green "Completed"; it carries a reason string for the
            // UI. Failed ("[failed]" content prefix) stays Error.
            let status: ToolCallStatusEnum;
            if (msg.placeholder) {
              status = ToolCallStatusEnum.Placeholder;
            } else if (msg.failed) {
              status = ToolCallStatusEnum.Error;
            } else {
              status = ToolCallStatusEnum.Success;
            }
            converted[idx] = { ...converted[idx], content: msg.content, status: MessageStatusEnum.Finished, toolCallStatus: status, placeholderReason: msg.placeholderReason || undefined, createdAt: msg.startedAt || converted[idx].createdAt, finishedAt: msg.finishedAt || undefined } as ToolMessage;
          }
        }
      }

      for (let j = 0; j < converted.length; j++) {
        const m = converted[j];
        if (m.role === MessageRoleEnum.TOOL && (m as ToolMessage).toolCallStatus === ToolCallStatusEnum.Processing) {
          // A tool bubble still stuck in Processing at this point means
          // the history contained an assistant ToolCall but no tool
          // result row at all — e.g. the run was killed before any
          // flush could synthesise a placeholder. Forcing Success here
          // would again mis-represent the run; mark it Placeholder
          // with an "unknown" reason instead so the UI is honest that
          // the tool never completed.
          converted[j] = { ...m, status: MessageStatusEnum.Finished, toolCallStatus: ToolCallStatusEnum.Placeholder, placeholderReason: 'unknown' } as ToolMessage;
        }
      }

      return converted;
    })();
    historyLoadsInFlightRef.current.set(sid, request);
    const clearInFlight = () => {
      // Identity check prevents an older completion from deleting a newer
      // request installed for the same session after this one settled.
      if (historyLoadsInFlightRef.current.get(sid) === request) {
        historyLoadsInFlightRef.current.delete(sid);
      }
    };
    void request.then(clearInFlight, clearInFlight);
    return request;
  }, [apiUrl, isPublic, recordSessionTokens]);

  // Re-sync job state after SSE reconnect to recover from missed events.
  // When called during the initial connection (historyLoadedRef is still false),
  // skip the expensive message reload since the initial-load effect + buffered
  // SSE events already cover the full message history.  Only sync job metadata
  // (title, status, progress) on the first connect.
  //
  // metadataOnly=true skips the message reload for non-terminal jobs. Used by
  // onReconnect and post-connect paths where SSE resumes from lastEventId
  // without event loss — full message reload would race with live SSE events.
  // However, when the job is terminal, messages are always reloaded even with
  // metadataOnly=true, because SSE is done and the idle-watchdog recovery
  // path needs to pick up any messages missed during the silent period.
  const syncJobState = useCallback(async (id: string, metadataOnly?: boolean, forceSkipMessages?: boolean) => {
    const isInitialSync = !historyLoadedRef.current;
    const gen = ++syncGenerationRef.current;
    try {
      const res = await fetch(apiUrl(`/job/${id}`));
      if (!res.ok) {
        throw new Error(await readHTTPError(res, `GET /job/${id}`));
      }
      seedServerClockFromResponse(res);
      const job = await res.json();
      // Discard stale responses — a newer syncJobState call was initiated
      // while this request was in flight.
      if (gen !== syncGenerationRef.current) {
        console.debug(`[JobEvents] syncJobState stale response discarded: gen=${gen} current=${syncGenerationRef.current}`);
        return;
      }
      // Capture the snapshot's resume seq as the new Last-Event-ID. The
      // SSE connect / reconnect / 410-fallback paths all read this ref.
      if (typeof job.lastEventSeq === 'number') {
        lastEventSeqRef.current = String(job.lastEventSeq);
        console.debug(`[JobEvents][TRACE-SEQ0] syncJobState seed jobId=${id} lastEventSeq=${job.lastEventSeq} isInitialSync=${isInitialSync}`);
      } else {
        console.debug(`[JobEvents][TRACE-SEQ0] syncJobState NO lastEventSeq jobId=${id} typeof=${typeof job.lastEventSeq} value=${JSON.stringify(job.lastEventSeq)}`);
      }
      if (job.title) {
        setJobTitle(job.title);
      }
      if (isPublic) {
        primeAgentDisplays(job.agents);
        const sharedWorkspaceName = typeof job.shareContext?.workspaceName === 'string'
          ? job.shareContext.workspaceName.trim()
          : '';
        setPublicWorkspaceName(sharedWorkspaceName || null);
      }
      setTitleGenerationError(
        typeof job.titleGenerationError === 'string' && job.titleGenerationError
          ? job.titleGenerationError
          : null,
      );
      // Hydrate round timing so the badge persists across reconnects.
      if (job.startedAt) {
        setJobStartedAt((prev) => prev ?? job.startedAt);
      }
      if (job.finishedAt) {
        setJobFinishedAt((prev) => prev ?? job.finishedAt);
      }

      const status = job.status as string;
      const isTerminal = status === 'completed' || status === 'stopped' || status === 'failed';

      if (isTerminal) {
        let queueWillContinue = false;
        if (job.mode !== 'graph') {
          try {
            queueWillContinue = !!(await refreshMessageQueue(id))?.willContinue;
          } catch (queueError) {
            console.warn('[useJobChat] terminal queue sync failed:', queueError);
          }
        }
        const hasPendingRunStart = eventStreamReadyWaitersRef.current.size > 0;
        // For graph jobs "running" is owned by the graph run status snapshot
        // (applyGraphRunStatusSnapshot / GraphRunProgress), NOT job.status — the
        // two can differ (a live/orphaned graph run whose bound job.status lags
        // or was written by an interactive discussion turn). Letting a terminal
        // job.status force the spinner off here is exactly what desynced the
        // composer from a "运行中" GraphRunProgress, so leave isLoading to the
        // graph snapshot for graph jobs. A send/start/continue waiting for SSE
        // readiness is also already loading; this snapshot describes the prior
        // terminal run and must not flash the composer back to idle.
        if (job.mode !== 'graph' && !hasPendingRunStart && !queueWillContinue) {
          setIsLoading(false);
        } else if (queueWillContinue) {
          setIsLoading(true);
        }
        // Finalize any in-flight assistant/tool messages that were left in
        // Started/Processing state because the real-time terminal SSE event
        // was missed (e.g. idle-watchdog recovery path).
        //
        // Use lastRunOutcome (persisted by backend) instead of job.status to
        // determine how to finalize. For interactive sends on an already-terminal
        // job, job.status is the restored prior status while lastRunOutcome
        // reflects the actual outcome of the most recent send.
        const runOutcome = (job.lastRunOutcome as string) || status;
        console.debug(`[JobEvents] syncJobState: job terminal (status=${status}, lastRunOutcome=${runOutcome}), finalizing in-flight messages jobId=${id} metadataOnly=${metadataOnly}`);
        const ts = job.finishedAt || Date.now();
        finalizeInFlightMessages(ts, {
          toolProcessingStatus:
            runOutcome === 'completed' ? ToolCallStatusEnum.Success
              : ToolCallStatusEnum.Placeholder,
          placeholderReason:
            runOutcome === 'stopped' ? 'interrupted'
              : runOutcome === 'failed' ? 'job_failed'
                : undefined,
        });
        // Restore error banner from persisted lastError when the real-time
        // JOB_FAILED / RUN_ERROR event was missed (e.g. page was closed
        // during failure, or idle-watchdog recovery). Without this,
        // interactive jobs lose the failure reason display on refresh.
        if (runOutcome === 'failed' && job.progress?.lastError) {
          setError((prev: string | null) => prev || job.progress.lastError);
        }
      }

      if (job.mode === 'graph') {
        setIsGraph(true);
        setGraphRunId(typeof job.graphRunId === 'string' && job.graphRunId ? job.graphRunId : null);
      } else {
        setIsGraph(false);
        setGraphRunId(null);
      }

      // Reload messages for all sessions to recover any missed during disconnect.
      // Skip on initial sync — the initial-load effect + buffered SSE replay
      // already handles message hydration and running this concurrently causes
      // duplicate messages when IDs between SSE and history don't match.
      // Also skip when metadataOnly is true AND the job is still running —
      // the caller (onReconnect / post-connect) knows SSE resumes from
      // lastEventId without event loss, so a full message reload would only
      // race with live SSE events and cause visual duplication.
      // However, when metadataOnly is true but the job is terminal, we DO
      // reload messages — this is the idle-watchdog recovery path where
      // terminal events were missed and SSE is no longer delivering anything.
      const skipMessages = forceSkipMessages || isInitialSync || (metadataOnly && !isTerminal);
      console.debug(`[JobEvents] syncJobState: jobId=${id} status=${status} isInitialSync=${isInitialSync} metadataOnly=${metadataOnly} isTerminal=${isTerminal} skipMessages=${skipMessages}`);
      if (!skipMessages && job.mode !== 'graph') {
        const sessionIds: string[] = job.sessionIds || [];
        if (sessionIds.length > 0) {
        const results = await parallelLimit(
          sessionIds.map((sid) => async () => {
            try {
              return await loadHistory(sid, undefined);
            } catch (err) {
              console.warn(`[JobEvents] syncJobState: failed to load session ${sid}:`, err);
              return [];
            }
          }),
          5
        );
        const allMessages = results.flat();
        // Second generation check: discard if a newer syncJobState was
        // initiated while we were loading history (the first check only
        // guards the snapshot fetch, not the subsequent parallel loads).
        if (gen !== syncGenerationRef.current) {
          console.debug(`[JobEvents] syncJobState stale after history load: gen=${gen} current=${syncGenerationRef.current}`);
          return;
        }
        if (allMessages.length > 0) {
          setMessages((prev) => mergeMessages(prev, allMessages, { deduplicateToolCallIds: true }));
        }
        // These loads run concurrently, so whichever response landed last had
        // set the badge — a coin flip between sessions. The composer always
        // sends into the newest session, so pin the badge to that one.
        const newestSid = sessionIds[sessionIds.length - 1];
        const newestTokens = sessionTokensRef.current.get(newestSid);
        if (newestTokens != null) setTotalTokens(newestTokens);
        // Sync loadedSessionIds so UI doesn't show "Loading session messages..."
        // for sessions whose messages we just loaded.
        setLoadedSessionIds((prev) => {
          const next = new Set(prev);
          for (const sid of sessionIds) next.add(sid);
          return next;
        });
      }
      } // end !skipMessages

    } catch (err) {
      console.warn('[SSE reconnect] failed to sync job state:', err);
      throw err;
    }
  }, [apiUrl, finalizeInFlightMessages, isPublic, loadHistory, refreshMessageQueue, seedServerClockFromResponse]);

  // Keep syncJobStateRef in sync so handleEvent can call it.
  syncJobStateRef.current = syncJobState;

  // Auto-connect SSE when jobId is available.
  // Uses handleEventRef so the effect only depends on jobId (not handleEvent).
  useEffect(() => {
    if (!jobId) return;
    // Skip the SSE subscription when the job has already been confirmed
    // missing — otherwise we'd hit /events 404 on top of the /job/:id 404
    // for every navigation to a stale jobId, plus log noise.
    if (jobNotFound) return;
    // Wait for the existing-job hydration effect to seed lastEventSeqRef
    // from the snapshot (or to give up and flip the gate from .finally).
    // Without this gate the two effects race on first mount, the SSE
    // request goes out with an empty Last-Event-ID, the server parses
    // it as startSeq=0, and any GC'd buffer responds 410 immediately.
    if (!snapshotReady) return;
    // While a graph run is live, this page subscribes only the graph-run
    // event stream (see the graph SSE effect below) — one long-lived stream
    // per page instead of two. On HTTP/1.1 each SSE stream holds one of the
    // browser's ~6 per-origin connections, and enough open pages starve
    // ordinary POSTs (like stop) in the socket pool. The job stream carries
    // nothing a live run needs: interactive discussion events only matter
    // once the run leaves a live state, which flips graphRunLive off and
    // re-subscribes here.
    if (isGraph && graphRunLive) return;
    markEventStreamReady(false);

    let cancelled = false;
    const currentJobId = jobId;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    // Backoffs after attempt 0 / 1 / 2 fail. Total budget = 1 initial + 3
    // retries. After all retries are exhausted we surface the original
    // connection or 410 error verbatim, per the project rule that errors
    // must be shown to the user.
    const RETRY_BACKOFFS_MS = [200, 1000, 3000];

    // Idle-watchdog: when the job appears to be running (isLoading=true)
    // but no SSE event has arrived for a while, the subscriber may have
    // been silently evicted upstream or be wedged behind a slow writer
    // — a known failure shape for terminal events (JOB_COMPLETED /
    // STOPPED / FAILED) on backlogged channels. Resync /job/:id so the
    // UI recovers terminal status without waiting for the user to refresh.
    // Threshold is generous so steady streaming and idle keep-alives don't
    // trigger spurious fetches.
    //
    // Logging: a silent SSE stream is EXPECTED during long thinking / long
    // tool execution (the backend ACP transport can block for minutes), so the
    // routine resync logs at debug. Only escalate to warn when the resync
    // actually recovered a missed terminal event — i.e. the job was loading
    // before the sync and is no longer loading after — which is the real
    // failure this watchdog exists to catch.
    const IDLE_WATCHDOG_INTERVAL_MS = 15_000;
    const IDLE_WATCHDOG_THRESHOLD_MS = 45_000;
    const watchdog = window.setInterval(() => {
      if (!isLoadingRef.current) return;
      const idleMs = Date.now() - lastEventReceivedAtRef.current;
      if (idleMs < IDLE_WATCHDOG_THRESHOLD_MS) return;
      lastEventReceivedAtRef.current = Date.now();
      console.debug(`[JobEvents] idle-watchdog: SSE silent for ${idleMs}ms while loading, resyncing /job/${currentJobId} (expected during long thinking/tool-exec)`);
      void syncJobState(currentJobId, true)
        .then(() => {
          if (!isLoadingRef.current) {
            console.warn(`[JobEvents] idle-watchdog: recovered missed terminal state for /job/${currentJobId} after ${idleMs}ms SSE silence`);
          }
        })
        .catch((err) => {
          console.warn('[idle-watchdog] syncJobState failed:', err);
        });
    }, IDLE_WATCHDOG_INTERVAL_MS);

    const scheduleNextOrSurface = (currentAttempt: number, errMsg: string, reloadFromDisk = true) => {
      if (cancelled) return;
      const backoff = RETRY_BACKOFFS_MS[currentAttempt];
      if (backoff === undefined) {
        // Retry budget exhausted. Surface the original error so the user sees
        // exactly what failed, whether it was a stale resume point or the
        // initial transport connection.
        const message = errMsg || 'SSE connection failed';
        const error = new Error(message);
        console.error(`[JobEvents] connection recovery exhausted after ${currentAttempt + 1} attempts: ${message}`);
        setError(message);
        settleEventStreamReadyWaiters(error);
        return;
      }
      console.warn(`[JobEvents] connection failed (attempt ${currentAttempt + 1}); retrying in ${backoff}ms: ${errMsg}`);
      retryTimer = setTimeout(() => { void attemptConnect(currentAttempt + 1, reloadFromDisk); }, backoff);
    };

    // Pull the authoritative session list off /job/:id, then re-fetch
    // /sessions/:sid/messages for each. The disk-side message store is the
    // source of truth for closed rounds and B-class state — what's missing
    // from the buffer must be there.
    //
    // Merge (not replace) the disk history into the current list. A bare
    // replace drops a just-sent optimistic user message: when the user sends
    // into a long-idle / post-restart job, the SSE resume point is already
    // gone, so the send triggers this 410-recovery path before the message
    // is confirmed (by RUN_STARTED via clientMessageId) or even persisted to
    // disk. Replacing with disk history — which doesn't yet contain that
    // message — makes it vanish until a manual refresh. mergeMessages keeps
    // the unconfirmed optimistic message (its clientMessageId is absent from
    // history) while still reconciling streaming-state messages by id /
    // toolCallId, which is all the old replace was buying us.
    const reloadMessagesFromDisk = async (currentJobId: string): Promise<void> => {
      const res = await fetch(apiUrl(`/job/${currentJobId}`));
      if (!res.ok) {
        throw new Error(await readHTTPError(res, `reload from disk: GET /job/${currentJobId}`));
      }
      seedServerClockFromResponse(res);
      const job = await res.json();
      if (cancelled) return;

      const sessionIds: string[] = Array.isArray(job?.sessionIds) ? job.sessionIds : [];
      // Drop any SSE events buffered before the reload — they pre-date the
      // disk-side message list we're about to install.
      pendingEventsRef.current = [];

      if (sessionIds.length === 0) {
        // No sessions on disk yet. Don't blank the list outright — a just-sent
        // optimistic user message (still pending RUN_STARTED confirmation, not
        // yet persisted) must survive, otherwise it vanishes until refresh.
        // Keep only unconfirmed optimistic user messages; everything else this
        // job had came from disk/SSE and is superseded by the empty history.
        setMessages((prev) =>
          prev.filter((m) => m.role === MessageRoleEnum.USER && m.pending && m.clientMessageId)
        );
        console.debug(`[JobEvents][TRACE-SEQ0] reload-from-disk done jobId=${currentJobId} sessions=0`);
        return;
      }

      // Interactive jobs may have multiple sessions after an Agent switch.
      // Load all of them so no conversation history is lost.
      const allMsgs: Message[] = [];
      for (const sid of sessionIds) {
        if (cancelled) return;
        const msgs = await loadHistory(sid);
        allMsgs.push(...msgs);
      }
      if (cancelled) return;
      setMessages((prev) => mergeMessages(prev, allMsgs, { deduplicateToolCallIds: true }));
      console.debug(`[JobEvents][TRACE-SEQ0] reload-from-disk done jobId=${currentJobId} sessions=${sessionIds.length}`);
    };

    const attemptConnect = async (attempt: number, reloadFromDisk = false): Promise<void> => {
      if (cancelled) return;

      // Always re-fetch the snapshot before opening / re-opening the SSE
      // stream so lastEventSeqRef matches what the server can still serve:
      //
      //   - On attempt 0 it populates lastEventSeqRef from a fresh snapshot
      //     so /events doesn't get startSeq=0. The server treats startSeq=0
      //     as valid only on a fresh buffer (headSeq=0); once any GC has
      //     run (the steady state for any job alive more than a few
      //     seconds) it returns 410, forcing a wasted recovery round-trip
      //     and a noisy `subscribe gone` log on every page open.
      //   - On retries it rotates the resume point past whatever the
      //     buffer GC'd while we were disconnected. Without this we'd
      //     reconnect with the same stale seq and hit 410 again.
      //
      // This reconnect-time syncJobState only needs to refresh lastEventSeq
      // before (re)opening the stream — it must NEVER reload messages:
      //   - attempt 0: the hydration effect already loaded messages.
      //   - attempt > 0: reloadMessagesFromDisk (below) handles messages.
      // Always pass forceSkipMessages=true. metadataOnly alone is NOT enough:
      // the `metadataOnly && !isTerminal` rule still reloads for a *terminal*
      // job, so a reply to a completed job (SSE was torn down on JOB_COMPLETED,
      // then reconnected here) would merge disk history into the still in-memory
      // live stream and duplicate any message whose streaming ID differs from
      // the persisted (round-collapsed) ID — a plain assistant bubble has no
      // semantic dedup, only id-based. This is the exact race JOB_COMPLETED's
      // own syncJobState call already guards against with forceSkipMessages=true.
      try {
        await syncJobState(currentJobId, true, true);
      } catch (err) {
        if (cancelled) return;
        if (attempt === 0) {
          // First attempt: tolerate snapshot failure and fall back to
          // empty Last-Event-ID. The server's 410 path will recover us
          // (and that path counts against the retry budget below).
          console.warn('[JobEvents] pre-connect snapshot fetch failed; will let SSE recover via 410 path:', err);
        } else if (reloadFromDisk) {
          // Subsequent attempts MUST have a fresh snapshot — without it
          // we'd retry with the same stale seq and 410 again.
          const originalError = resumeGoneErrorRef.current || 'HTTP 410';
          const snapshotError = err instanceof Error ? err.message : String(err);
          scheduleNextOrSurface(attempt, `Resume point expired: ${originalError}; Snapshot reload failed: ${snapshotError}`);
          return;
        } else {
          const snapshotError = err instanceof Error ? err.message : String(err);
          scheduleNextOrSurface(attempt, snapshotError, false);
          return;
        }
      }
      if (cancelled) return;

      // A retry after 410 rebuilds from disk because buffered events may have
      // been GC'd. A plain initial transport failure has no such event gap and
      // only needs a fresh SSE connection.
      if (reloadFromDisk) {
        try {
          await reloadMessagesFromDisk(currentJobId);
          resumeGoneErrorRef.current = '';
        } catch (err) {
          if (cancelled) return;
          const originalError = resumeGoneErrorRef.current || 'HTTP 410';
          const snapshotError = err instanceof Error ? err.message : String(err);
          scheduleNextOrSurface(attempt, `Resume point expired: ${originalError}; Snapshot reload failed: ${snapshotError}`);
          return;
        }
        if (cancelled) return;
      }

      // Disconnect any previously-active client BEFORE swapping in the
      // new one. Without this the old client's reader stays attached to
      // the per-job buffer and keeps pinning minCursor — a slow leak that
      // accumulates one phantom reader per 410 fallback.
      eventSseRef.current?.disconnect();
      markEventStreamReady(false);

      const client = new SSEClient();
      eventSseRef.current = client;
      let connectionRejected = false;

      console.debug(`[JobEvents][TRACE-SEQ0] connectUntilReady jobId=${jobId} attempt=${attempt} initialLastEventId=${JSON.stringify(lastEventSeqRef.current)}`);

      client.connectUntilReady({
        url: () => apiUrl(`/job/${jobId}/events`, viewerParams()),
        initialLastEventId: lastEventSeqRef.current,
        onEvent: (event) => handleEventRef.current(event),
        onError: (err) => {
          if (cancelled) return;
          if (isIgnorableNetworkError(err)) return;
          connectionRejected = true;
          const error = err instanceof Error ? err : new Error(String(err));
          setError(error.message);
          settleEventStreamReadyWaiters(error);
        },
        onDisconnect: () => {
          connectionRejected = true;
          markEventStreamReady(false);
          reportDisconnect();
        },
        onReconnect: () => {
          const hasPendingRunStart = eventStreamReadyWaitersRef.current.size > 0;
          markEventStreamReady(true);
          reportReconnect();
          setError(null);
          // The URL factory already supplied a fresh value for this connection;
          // restate it after registration to close a visibility-change race
          // during the HTTP handshake.
          reportViewerVisibility(
            typeof document === 'undefined' || document.visibilityState === 'visible',
          );
          // Only sync metadata (title, status, progress, lastEventSeq).
          // SSE resumes from lastEventId so no events are lost; full
          // message reload would race with live SSE events and cause
          // visual duplication of old messages.
          if (!hasPendingRunStart) {
            console.debug(`[JobEvents] onReconnect: syncing metadata for jobId=${currentJobId}`);
            void syncJobState(currentJobId, true).catch((err) => {
              console.warn('[onReconnect] syncJobState failed:', err);
            });
          }
        },
        onResumePointGone: (errorMessage) => {
          // The buffer GC'd past our Last-Event-ID. Schedule another
          // attempt (snapshot + fresh client) up to the retry budget;
          // after that, surface the server's original error to the user.
          if (cancelled) return;
          connectionRejected = true;
          resumeGoneErrorRef.current = errorMessage || 'HTTP 410';
          console.debug(`[JobEvents][TRACE-SEQ0] resume-gone (410) jobId=${currentJobId} attempt=${attempt} lastEventSeqRef=${JSON.stringify(lastEventSeqRef.current)} serverMsg=${JSON.stringify(errorMessage)}`);
          scheduleNextOrSurface(attempt, errorMessage);
        },
      }).then(() => {
        if (cancelled) return;
        // Only the most recently created client should advance UI state;
        // a stale `then` from a client that was already replaced by a
        // retry must not flip the flags back.
        if (eventSseRef.current !== client) return;
        // connectUntilReady also resolves after its 410/auth handlers return.
        // Those paths did not register a live reader and must not release a
        // pending send before the scheduled recovery attempt succeeds.
        if (connectionRejected) return;
        const hasPendingRunStart = eventStreamReadyWaitersRef.current.size > 0;
        markEventStreamReady(true);
        setError(null);
        reportViewerVisibility(
          typeof document === 'undefined' || document.visibilityState === 'visible',
        );
        // Sync metadata after SSE connects. Message reload is always skipped
        // here: on the initial/plain transport path hydration already loaded
        // messages, and 410 recovery reloads them before reconnecting. Allowing
        // syncJobState to reload messages for terminal jobs would cause a
        // redundant second full reload.
        // When a send/start/continue call is waiting for this connection, the
        // pre-connect snapshot still describes the previous terminal run.
        // A second sync here could observe that stale terminal state and close
        // the reader again in the tiny gap before the POST flips the job to
        // running, recreating the exact phase-event loss this handshake fixes.
        if (!hasPendingRunStart) {
          void syncJobState(currentJobId, true, true).catch((err) => {
            console.warn('[post-connect] syncJobState failed:', err);
          });
        }
      }).catch((err) => {
        if (!cancelled) {
          // A newer reconnect/effect may already have replaced this client.
          // Its stale rejection must not disconnect or schedule work for the
          // active subscription.
          if (eventSseRef.current !== client) return;
          const error = err instanceof Error ? err : new Error(String(err));
          console.error('[JobEvents] connect failed:', error);
          // connectUntilReady leaves its options/controller installed when the
          // initial fetch rejects. Explicitly disconnect so sendMessage can
          // recognize the dead client and force a new subscription.
          client.disconnect();
          markEventStreamReady(false);
          setError(error.message);
          settleEventStreamReadyWaiters(error);
          scheduleNextOrSurface(attempt, error.message, false);
        }
      });
    };

    void attemptConnect(0);

    return () => {
      cancelled = true;
      markEventStreamReady(false);
      window.clearInterval(watchdog);
      if (retryTimer) clearTimeout(retryTimer);
      // Close whichever client is currently in use (initial OR any one
      // installed by a 410 retry). Closure-capturing the first client
      // would leak retry-installed ones across unmount.
      eventSseRef.current?.disconnect();
      eventSseRef.current = null;
    };
  }, [jobId, jobNotFound, snapshotReady, isGraph, graphRunLive, sseReconnectSeq, apiUrl, syncJobState, reportDisconnect, reportReconnect, seedServerClockFromResponse, initialSessionId, loadHistory, markEventStreamReady, settleEventStreamReadyWaiters, viewerParams, reportViewerVisibility]);

  // Report this page's visibility so the backend can tell "watching the stream"
  // from "stream left open in a hidden tab". pagehide covers the cases where a
  // tab is frozen or navigated away without unmounting the component (mobile
  // Safari's page cache), where the connection can outlive the user's attention.
  useEffect(() => {
    if (!jobId || isPublic) return;
    const report = () => reportViewerVisibility(document.visibilityState === 'visible');
    const onHide = () => reportViewerVisibility(false);
    document.addEventListener('visibilitychange', report);
    window.addEventListener('pagehide', onHide);
    return () => {
      document.removeEventListener('visibilitychange', report);
      window.removeEventListener('pagehide', onHide);
    };
  }, [jobId, isPublic, reportViewerVisibility]);

  // Graph mode has one page-level stream. It feeds agent deltas into the chat,
  // reconciles node sessions, and publishes the same authoritative run snapshot
  // to GraphRunProgress. Keeping all three consumers behind this one owner
  // avoids opening a second /graph-run/events connection from the progress
  // component (which exhausted HTTP/1.1 connection slots across two tabs).
  const graphSseRef = useRef<GraphSSEClient | null>(null);
  useEffect(() => {
    graphSseRef.current?.disconnect();
    graphSseRef.current = null;
    // Only a live run produces graph events; interactive discussion turns on
    // a non-live run (awaitingInput/terminal) flow over the job-events stream
    // instead, which the effect above re-subscribes as soon as graphRunLive
    // flips off. Using graphRunLive (not isLoading) as the gate also keeps the
    // subscription stable across boolean-equal status changes.
    if (!isGraph || !graphRunId || !jobId || !graphRunLive) return;

    let cancelled = false;
    let lastInstanceRefreshAt = 0;
    let trailingRefreshTimer: ReturnType<typeof setTimeout> | null = null;
    let reconcileSeq = 0;

    const reconcile = async () => {
      if (cancelled) return;
      const seq = ++reconcileSeq;
      let data: GraphRunStatusResponse;
      try {
        const res = await fetch(apiUrl(`/job/${encodeURIComponent(jobId)}/graph-run`));
        if (!res.ok) {
          throw new Error(await readHTTPError(res, `GET /job/${jobId}/graph-run`));
        }
        data = (await res.json()) as GraphRunStatusResponse;
        // Share pages resolve historical agent display exclusively from the
        // public payloads' agents maps (see loadHistory).
        if (isPublic) primeAgentDisplays(data.agents);
      } catch (err) {
        const error = err instanceof Error ? err : new Error(String(err));
        console.error(`[graph-sse] reconcile fetch failed for job ${jobId} run ${graphRunId}:`, error);
        throw error;
      }
      if (cancelled || seq !== reconcileSeq) return;
      applyGraphRunStatusSnapshot(data);
      const instances = data.instances || [];
      const archivedInstances = data.run?.archivedInstances;
      const entries = graphSessionEntries(instances, archivedInstances);
      if (entries.length > 0) setGraphSessions(entries);
      // Follow the latest-started node's session while the user hasn't pinned an
      // earlier one, so a freshly-started node's session — and the user message
      // it just sent — becomes the visible conversation the instant the node
      // starts, instead of only when the run ends. Mirrors the Graph hydration
      // follow-latest and the hydration path's latest-entry default.
      // entries are sorted by startedAt, so the last is the most recently
      // started node that has a session. Backend now sets displaySessionId at
      // dispatch (eager session visibility), so this fires mid-run, not just at
      // completion.
      const latestSid = getLastGraphSessionId(entries);
      if (
        latestSid &&
        latestSid !== activeSessionIdRef.current &&
        (followLatestSessionRef.current || !activeSessionIdRef.current)
      ) {
        applyActiveSessionSelection(latestSid, true);
      }
      const terminalSids = instances
        .map((i) => ({ sid: i.displaySessionId || i.sessionId, status: i.status }))
        .filter((i): i is { sid: string; status: GraphInstanceStatus } => !!i.sid && i.status !== 'running' && i.status !== 'pending')
        .map((i) => i.sid);
      if (terminalSids.length > 0) setEndedSessionIds((prev) => new Set([...prev, ...terminalSids]));

      // Reload the currently-viewed session so a node finishing while the user
      // watches it fills in its conversation. Other sessions reload lazily when
      // selected (handled by the load-on-switch effect).
      const active = activeSessionIdRef.current;
      if (!historyHydratingRef.current && active && entries.some((e) => e.sessionId === active)) {
        try {
          const msgs = await loadHistory(active, active);
          if (!cancelled && msgs.length > 0) {
            setMessages((prev) => mergeMessages(prev, msgs, { deduplicateToolCallIds: true }));
            setLoadedSessionIds((prev) => new Set([...prev, active]));
          }
        } catch (err) {
          console.error(`[graph-sse] reload active session ${active} failed:`, err);
        }
      }

      // applyGraphRunStatusSnapshot above is the single source of truth for
      // the page's live/terminal state, including Resume actions initiated by
      // GraphRunProgress and terminal transitions observed on this stream.
    };

    const runReconcile = () => {
      void reconcile().catch((err) => {
        setGraphStreamError(err instanceof Error ? err.message : String(err));
      });
    };

    // Throttle reconciliation so bursts of graph events don't hammer the
    // run-status endpoint. Keep a trailing refresh: an Agent node emits
    // `instanceStarted` before its session is opened, then another
    // `instanceStarted` with message="session opened" milliseconds later.
    // Dropping that trailing refresh leaves the new session out of the chat
    // sidebar until the node completes or the user refreshes.
    const scheduleReconcile = (force = false) => {
      const now = Date.now();
      const waitMs = Math.max(0, 400 - (now - lastInstanceRefreshAt));
      if (force || waitMs === 0) {
        if (trailingRefreshTimer) {
          clearTimeout(trailingRefreshTimer);
          trailingRefreshTimer = null;
        }
        lastInstanceRefreshAt = now;
        runReconcile();
        return;
      }
      if (trailingRefreshTimer) return;
      trailingRefreshTimer = setTimeout(() => {
        trailingRefreshTimer = null;
        lastInstanceRefreshAt = Date.now();
        runReconcile();
      }, waitMs);
    };
    const throttledReconcile = () => {
      scheduleReconcile(false);
    };
    const immediateReconcile = () => {
      scheduleReconcile(true);
    };

    const client = new GraphSSEClient({
      url: () => apiUrl(`/job/${encodeURIComponent(jobId)}/graph-run/events`, viewerParams()),
      onReconcile: () => {
        reportViewerVisibility(
          typeof document === 'undefined' || document.visibilityState === 'visible',
        );
        return reconcile();
      },
      onError: (err) => setGraphStreamError(err.message || String(err)),
      onEvent: (raw) => {
        const evt = raw as unknown as GraphEvent;
        const t = evt.type;
        if (t === 'progressUpdated' || t === 'error') {
          if (t === 'error') {
            setGraphStreamError(evt.error?.message || evt.message || 'Graph run stream reported an error');
          }
          immediateReconcile();
          return;
        }
        if (t === 'instanceStarted' || t === 'instanceCompleted' || t === 'instanceFailed'
          || t === 'instanceSkipped' || t === 'edgeResolved' || t === 'loopIteration') {
          if (t === 'instanceStarted' && evt.message === 'session opened') {
            immediateReconcile();
          } else {
            throttledReconcile();
          }
          return;
        }
        // Agent token stream: translate the graph event into the shared
        // AgentEvent shape and feed it through handleEvent so graph nodes stream
        // token-by-token (thought / content / tool calls), instead of only
        // surfacing at node completion.
        const translated = translateGraphEvent(evt);
        if (!translated) return;
        // A node's first agent delta can arrive before reconcile has added its
        // session to the list / made it the active (followed) conversation. The
        // bubble is created tagged with its sessionId so nothing is lost, but it
        // stays filtered out of view until the session is active — nudge a
        // reconcile so the new session surfaces and follow-latest selects it.
        const sid = evt.payload?.sessionId;
        if (sid && !graphSessionsRef.current.some((s) => s.sessionId === sid)) {
          throttledReconcile();
        }
        handleEventRef.current(translated);
      },
    });
    graphSseRef.current = client;
    client.connect();

    return () => {
      cancelled = true;
      if (trailingRefreshTimer) {
        clearTimeout(trailingRefreshTimer);
        trailingRefreshTimer = null;
      }
      client.disconnect();
      if (graphSseRef.current === client) graphSseRef.current = null;
    };
  }, [isGraph, graphRunId, jobId, graphRunLive, apiUrl, isPublic, loadHistory, setGraphSessions, applyActiveSessionSelection, applyGraphRunStatusSnapshot, viewerParams, reportViewerVisibility]);

  // While the job-events SSE is gated off (live graph run), the job title —
  // generated a few seconds after the run starts — has no event channel. Poll
  // it once on bind and once shortly after; the terminal syncJobState pass
  // (via the re-subscribed job stream) covers everything later. Deliberately
  // does NOT reuse syncJobState here: for a live run job.status can be a
  // stale-terminal leftover from a previous discussion turn, and
  // syncJobState's terminal path would finalize the streaming bubbles as
  // interrupted.
  const graphMetaSyncedRef = useRef<string | null>(null);
  useEffect(() => {
    if (!isGraph || !jobId || !graphRunLive || !graphRunId) return;
    if (graphMetaSyncedRef.current === graphRunId) return;
    graphMetaSyncedRef.current = graphRunId;
    const syncMeta = () =>
      fetch(apiUrl(`/job/${encodeURIComponent(jobId)}`))
        .then(async (res) => {
          if (!res.ok) throw new Error(await readHTTPError(res, `GET /job/${jobId}`));
          const job = await res.json();
          if (job.title) setJobTitle(job.title);
        })
        .catch((err) => console.warn('[useJobChat] graph metadata sync failed:', err));
    void syncMeta();
    const timer = setTimeout(() => { void syncMeta(); }, 10_000);
    return () => clearTimeout(timer);
  }, [isGraph, jobId, graphRunId, graphRunLive, apiUrl]);

  // Send interactive message.
  //
  // options.bypassCommand — skip the slash-command fast path AND tell the
  // server to skip its command-dispatch branch. Used by the home-page path
  // where the user typed `/help` as the very first message so the text becomes
  // the Job's first message, not a command.
  const sendMessage = useCallback(async (content: string, modelId?: string | null, targetSessionId?: string | null, imageUrls?: string[], fileAttachments?: FileAttachment[], acpMode?: string, agentType?: string, acpThoughtLevel?: string, options?: { bypassCommand?: boolean; optimisticMessageId?: string; presentAsQueued?: boolean; onAccepted?: (clientMessageId: string) => void }) => {
    if (!jobId || isPublic) return;
    // We're about to flip the buffering flag so handleEvent will route
    // incoming SSE events straight to state. Any events that were buffered
    // BEFORE this point must be replayed first — otherwise they are lost
    // (the history-load effect would normally replay them, but the user has
    // sent a message before history finished loading).
    if (!historyLoadedRef.current) {
      const pending = pendingEventsRef.current;
      pendingEventsRef.current = [];
      historyLoadedRef.current = true;
      for (const evt of pending) {
        handleEventRef.current(evt);
      }
    }

    // Slash-command fast path: known commands are intercepted by the backend
    // and reply via COMMAND_SYSTEM_MESSAGE SSE events. Skip the optimistic
    // user-bubble insertion and the loading state — otherwise the bubble
    // would flash for one render and isLoading would flicker on and off.
    //
    // Skip the fast path when the user has attached images: commands don't
    // consume imageUrls, so sending "/help" with an image would orphan the
    // upload. Fall through to the normal message flow; the backend also
    // declines its command branch when ImageUrls is non-empty (see
    // job_message.go), so the message + images stay bundled end-to-end.
    const trimmed = content.trim();
    const hasAttachments = (!!imageUrls && imageUrls.length > 0) || (!!fileAttachments && fileAttachments.length > 0);
    if (!options?.bypassCommand && !hasAttachments && isKnownCommand(trimmed)) {
      const commandIntentScope = `command:${jobId}`;
      const commandClientMessageId = options?.optimisticMessageId
        ?? claimJobCreateIntent(commandIntentScope, { content: trimmed });
      // Clear any previous pending watchdog before dispatching a new command.
      clearPendingCommandWatchdog();
      try {
        const res = await fetch(`/api/v1/job/${jobId}/message`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            clientMessageId: commandClientMessageId,
            messages: [{ role: 'user', content: trimmed }],
          }),
        });
        if (!res.ok) {
          clearJobCreateIntent(commandIntentScope, commandClientMessageId);
          const errData = await res.json().catch(() => null);
          const msg = errData?.error || `HTTP ${res.status}`;
          console.error('[sendMessage] command dispatch failed:', msg);
          showCommandToast(`命令发送失败：${msg}`);
          return;
        }

        // The backend returns the rendered command result inline (in addition
        // to the transient SSE broadcast). Render it directly so delivery does
        // not depend on this tab holding a live SSE connection — interactive
        // jobs tear the SSE down after each finished round, so the broadcast
        // alone would land on no reader and the page would show nothing.
        // applyCommandEvent dedups against the SSE copy when both arrive.
        const body = await res.json().catch(() => null);
        clearJobCreateIntent(commandIntentScope, commandClientMessageId);
        const event = body?.event as CommandSystemMessageEvent | undefined;
        if (event && event.text) {
          applyCommandEvent(event);
        } else {
          // No inline event (older backend / unexpected shape): fall back to
          // the SSE-only behaviour with a short watchdog so the user isn't
          // left in limbo if the broadcast never arrives.
          pendingCommandRef.current = trimmed;
          pendingCommandTimeoutRef.current = window.setTimeout(() => {
            if (!pendingCommandRef.current) return;
            showCommandToast('命令已发送，但暂未收到响应（可能连接断开或服务异常）。请稍后重试。');
            clearPendingCommandWatchdog();
          }, 6_000);
        }
      } catch (err) {
        console.error('[sendMessage] command dispatch error:', err);
        showCommandToast('命令发送失败：网络错误。请检查网络连接后重试。');
      }
      return;
    }
    const effectiveBypassCommand = options?.bypassCommand ?? false;

    const userMessageId = options?.optimisticMessageId
      ?? crypto.randomUUID?.()
      ?? `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
    const clientMessageId = userMessageId;
    if (!options?.presentAsQueued) {
      foregroundMessageIdsRef.current.add(clientMessageId);
    }
    const userMessage: Message = {
      id: userMessageId,
      role: MessageRoleEnum.USER,
      content,
      createdAt: Date.now(),
      status: MessageStatusEnum.Finished,
      // Use targetSessionId if provided; otherwise fall back to the current
      // activeSessionId so the optimistic message is visible through the
      // session filter immediately (before RUN_STARTED confirms it).
      sessionId: targetSessionId || activeSessionIdRef.current || undefined,
      clientMessageId,
      pending: true,
      deliveryStatus: 'sending',
      imageUrls: imageUrls && imageUrls.length > 0 ? imageUrls : undefined,
      fileAttachments: fileAttachments && fileAttachments.length > 0 ? fileAttachments : undefined,
    } as Message;

    setMessages((prev) => {
      const optimisticIndex = prev.findIndex((message) => message.id === userMessageId);
      if (optimisticIndex < 0) return [...prev, userMessage];
      const next = [...prev];
      next[optimisticIndex] = userMessage;
      return next;
    });
    setBackendPhase(null);
    setIsLoading(true);
    setError(null);
    // Accumulate previous turn's duration before resetting (interactive mode).
    if (jobStartedAtRef.current != null && jobFinishedAtRef.current != null) {
      setInteractiveAccumulatedMs((prev) => prev + (jobFinishedAtRef.current! - jobStartedAtRef.current!));
    }
    // Clear the previous round's timestamps so the footer "total duration"
    // badge doesn't briefly flash the prior round's value before RUN_STARTED
    // arrives. RUN_STARTED still sets the authoritative start; this only
    // closes the gap between setIsLoading(true) and the first SSE event.
    setJobStartedAt(undefined);
    setJobFinishedAt(undefined);

    try {
      await ensureEventStreamReady('send message');

      const payload: Record<string, unknown> = {
        messages: [{
          id: userMessageId,
          type: 'text',
          content,
          timestamp: Date.now(),
          role: 'user',
          imageUrls: imageUrls && imageUrls.length > 0 ? imageUrls : undefined,
          fileAttachments: fileAttachments && fileAttachments.length > 0 ? fileAttachments : undefined,
        }],
      };
      if (modelId) payload.modelId = modelId;
      if (targetSessionId) payload.sessionId = targetSessionId;
      payload.clientMessageId = clientMessageId;
      if (acpMode) payload.acpMode = acpMode;
      if (acpThoughtLevel) payload.acpThoughtLevel = acpThoughtLevel;
      if (agentType) payload.agentType = agentType;
      if (effectiveBypassCommand) payload.bypassCommand = true;

      const response = await fetch(`/api/v1/job/${jobId}/message`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      if (!response.ok) {
        throw new Error(await readHTTPError(response));
      }
      options?.onAccepted?.(clientMessageId);
      // Safety net: if the frontend command list drifts from the backend's
      // (see utils/commands.ts — drift is explicitly allowed), the backend
      // may still intercept a command the client didn't fast-path. In that
      // case the response carries `command_dispatched` (plus the inline
      // `event`); clean up the optimistic user bubble and render the result.
      const body = await response.json().catch(() => null);
      const applyQueuedSubmission = (snapshot: MessageQueueSnapshot | undefined, forceQueued: boolean) => {
        const queuedIndex = snapshot?.items?.findIndex((item) => item.id === userMessageId) ?? -1;
        const queuedItem = queuedIndex >= 0 ? snapshot?.items?.[queuedIndex] : undefined;
        const waitingBehindAnother = !!snapshot?.active && snapshot.active.id !== userMessageId;
        const presentAsQueued = forceQueued
          || !!snapshot?.paused
          || queuedItem?.state === 'blocked'
          || waitingBehindAnother
          || queuedIndex > 0;
        if (presentAsQueued) {
          foregroundMessageIdsRef.current.delete(userMessageId);
          setMessages((prev) => prev.filter((message) => message.id !== userMessageId));
        } else {
          setMessages((prev) => prev.map((message) =>
            message.id === userMessageId ? { ...message, deliveryStatus: 'sent', sendError: undefined } : message
          ));
        }
        applyMessageQueueSnapshot(snapshot);
        setIsLoading(!!snapshot?.willContinue);
      };
      if (body?.status === 'command_dispatched' || body?.status === 'command_duplicate') {
        foregroundMessageIdsRef.current.delete(userMessageId);
        setMessages((prev) => prev.filter((m) => m.id !== userMessageId));
        setIsLoading(messageQueueWillContinueRef.current);
        const event = body?.event as CommandSystemMessageEvent | undefined;
        if (event && event.text) applyCommandEvent(event);
      } else if (body?.status === 'duplicate') {
        const messageState = typeof body?.messageState === 'string' ? body.messageState : '';
        if (messageState === 'queued' || messageState === 'blocked' || messageState === 'deleted') {
          if (activeClientMessageIdRef.current === userMessageId) return;
          const snapshot = body?.queue as MessageQueueSnapshot | undefined;
          applyQueuedSubmission(snapshot, !!options?.presentAsQueued || messageState !== 'queued');
          return;
        }
        const stillProcessing = messageState === 'processing';
        if (!stillProcessing) foregroundMessageIdsRef.current.delete(userMessageId);
        // This 200 acknowledges the original delivery; it did not start a new
        // run. A processing receipt can keep following that original run's SSE,
        // while a terminal/interrupted receipt must reconcile from disk because
        // no fresh RUN_STARTED or terminal event will be emitted for the retry.
        setMessages((prev) =>
          prev.map((msg) =>
            msg.id === userMessageId
              ? {
                  ...msg,
                  pending: stillProcessing,
                  failed: messageState === 'interrupted',
                  deliveryStatus: messageState === 'interrupted' ? 'failed' : 'sent',
                  sendError: messageState === 'interrupted'
                    ? '消息已被服务端接收，但处理因服务重启而中断；如需重新执行，请作为新消息发送。'
                    : undefined,
                }
              : msg
          )
        );
        if (!stillProcessing) {
          setIsLoading(false);
          await syncJobState(jobId);
        }
      } else if (body?.status === 'queued') {
        if (activeClientMessageIdRef.current === userMessageId) return;
        const snapshot = body?.queue as MessageQueueSnapshot | undefined;
        applyQueuedSubmission(snapshot, !!options?.presentAsQueued);
      } else if (body?.status === 'deleted') {
        foregroundMessageIdsRef.current.delete(userMessageId);
        setMessages((prev) => prev.filter((message) => message.id !== userMessageId));
        applyMessageQueueSnapshot(body?.queue as MessageQueueSnapshot);
        setIsLoading(!!body?.queue?.willContinue);
      } else {
        // The HTTP response is the delivery acknowledgement: the backend has
        // accepted the message and started the run. Keep `pending` untouched
        // until RUN_STARTED reconciles the optimistic bubble with its session,
        // but stop the delivery spinner immediately.
        setMessages((prev) =>
          prev.map((msg) =>
            msg.id === userMessageId
              ? { ...msg, deliveryStatus: 'sent', sendError: undefined }
              : msg
          )
        );
      }
      // Events come through the /events SSE connection
    } catch (err) {
      console.error('[sendMessage] error:', err);
      const errorMessage = err instanceof Error ? err.message : String(err || 'Failed to send message');
      const ignoreNetworkError = isIgnorableNetworkError(err);
      if (!ignoreNetworkError) foregroundMessageIdsRef.current.delete(userMessageId);
      if (ignoreNetworkError) {
        reportDisconnect();
      }
      setMessages((prev) =>
        prev.map((msg) =>
          msg.id === userMessageId
            ? { ...msg, pending: false, failed: true, deliveryStatus: 'failed', sendError: errorMessage }
            : msg
        )
      );
      setIsLoading(messageQueueWillContinueRef.current);
      if (ignoreNetworkError) {
        return;
      }
      setError(errorMessage);
    }
  }, [jobId, isPublic, clearPendingCommandWatchdog, applyCommandEvent, applyMessageQueueSnapshot, ensureEventStreamReady, reportDisconnect, syncJobState]);

  // Cleanup watchdog on unmount.
  useEffect(() => {
    return () => {
      try { clearPendingCommandWatchdog(); } catch { /* ignore */ }
    };
  }, [clearPendingCommandWatchdog]);

  const queueMessage = useCallback((msg: Omit<QueuedMessage, 'id'>) => {
    if (isGraph) return;
    void sendMessage(msg.content, msg.modelId ?? null, null, msg.imageUrls, msg.fileAttachments, msg.acpMode, msg.agentType, msg.acpThoughtLevel, { presentAsQueued: true });
  }, [isGraph, sendMessage]);

  const cancelQueuedMessage = useCallback(async (id: string) => {
    if (!jobId || isPublic) return;
    const response = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/message-queue/${encodeURIComponent(id)}`, { method: 'DELETE' });
    if (!response.ok) {
      const requestError = new Error(await readHTTPError(response));
      setError(requestError.message);
      throw requestError;
    }
    const body = await response.json();
    applyMessageQueueSnapshot(body?.queue as MessageQueueSnapshot);
  }, [applyMessageQueueSnapshot, isPublic, jobId]);

  const continueMessageQueue = useCallback(async () => {
    if (!jobId || isPublic) return;
    const response = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/message-queue/continue`, { method: 'POST' });
    if (!response.ok) {
      const requestError = new Error(await readHTTPError(response));
      setError(requestError.message);
      throw requestError;
    }
    const body = await response.json();
    applyMessageQueueSnapshot(body?.queue as MessageQueueSnapshot);
    setIsLoading(!!body?.queue?.willContinue);
  }, [applyMessageQueueSnapshot, isPublic, jobId]);

  const clearQueuedMessages = useCallback(() => {
    void Promise.all(queuedMessagesRef.current.map((message) => cancelQueuedMessage(message.id))).catch((err) => {
      setError(err instanceof Error ? err.message : String(err));
    });
  }, [cancelQueuedMessage]);

  const stopGeneration = useCallback(async () => {
    if (!jobId) {
      // No job on the backend yet — the in-flight state is purely local.
      setIsLoading(false);
      return;
    }
    try {
      const response = await fetch(`/api/v1/job/${jobId}/stop`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        signal: AbortSignal.timeout(STOP_REQUEST_TIMEOUT_MS),
      });
      if (!response.ok) {
        throw new Error(await readHTTPError(response));
      }
      // Only drop the loading state once the backend has actually accepted the
      // stop — clearing it after a swallowed failure used to leave a "stopped"
      // UI over a still-running job.
      setIsLoading(false);
    } catch (err) {
      console.error('[stopGeneration] error:', err);
      setError(stopRequestErrorMessage(err, 'Failed to stop'));
    }
  }, [jobId]);

  const clearMessages = useCallback(() => {
    setMessages([]);
    setError(null);
  }, []);

  // Load job details for existing job
  useEffect(() => {
    if (!existingJobId || historyLoadedRef.current) return;
    let cancelled = false;
    // Cancel handle for the background idle-prefetch of non-active Graph
    // sessions; wired into this effect's cleanup so a job switch / unmount
    // drops any pending idle callback immediately.
    let cancelIdlePrefetch: (() => void) | null = null;
    const hydrationGeneration = ++historyHydrationGenerationRef.current;
    historyHydratingRef.current = true;
    setJobId(existingJobId);
    setIsLoadingHistory(true);
    setJobNotFound(false);
    fetch(apiUrl(`/job/${existingJobId}`))
      .then(async (res) => {
        // Critical: the backend returns 404 with a JSON error envelope
        // ({"code":-1,"msg":"job not found"}). Without this guard, the body
        // parses cleanly into the success path, every hydration step short-
        // circuits because the fields are undefined, historyLoadedRef gets
        // pinned to true, and the user is left on an empty chat with no
        // error indicator. So gate on res.ok before treating the body as a
        // Job payload.
        if (!res.ok) {
          const msg = await readHTTPError(res, `GET /job/${existingJobId}`);
          const err = new Error(msg) as Error & { status?: number };
          err.status = res.status;
          throw err;
        }
        seedServerClockFromResponse(res);
        return res.json();
      })
      .then(async (job) => {
        if (cancelled) return;
        // Seed the SSE resume seq from the snapshot BEFORE any history /
        // metadata work so the SSE auto-connect effect — which is gated on
        // snapshotReady flipping in .finally — has the right Last-Event-ID
        // ready the moment its gate opens. Without this seed the SSE
        // request goes out with an empty Last-Event-ID, the server parses
        // it as startSeq=0, and any job whose buffer has GC'd past 0
        // immediately responds with 410.
        if (typeof job.lastEventSeq === 'number') {
          lastEventSeqRef.current = String(job.lastEventSeq);
          console.debug(`[JobEvents][TRACE-SEQ0] hydration seed jobId=${existingJobId} lastEventSeq=${job.lastEventSeq}`);
        } else {
          console.debug(`[JobEvents][TRACE-SEQ0] hydration NO lastEventSeq jobId=${existingJobId} typeof=${typeof job.lastEventSeq} value=${JSON.stringify(job.lastEventSeq)}`);
        }
        setJobTitle(job.title || '');
        if (isPublic) {
          primeAgentDisplays(job.agents);
          const sharedWorkspaceName = typeof job.shareContext?.workspaceName === 'string'
            ? job.shareContext.workspaceName.trim()
            : '';
          setPublicWorkspaceName(sharedWorkspaceName || null);
        } else {
          if (job.shareToken) setJobShareTokenState(job.shareToken);
          setJobShareShowWorkspaceName(job.shareShowWorkspaceName === true);
        }
        // Hydrate base job metadata on refresh.
        if (job.workdir) setSessionWorkdir(job.workdir);
        // modelId, agentType, acpMode are now per-session; they will
        // be hydrated from GetMessagesResponse when session history is loaded below.

        // Hydrate round timing from persisted job timestamps so the "total
        // duration" badge survives page refresh (ring buffer is flushed after
        // terminal events, SSE replay cannot restore these).
        if (job.startedAt) {
          setJobStartedAt((prev) => prev ?? job.startedAt);
        }
        if (job.finishedAt) {
          setJobFinishedAt((prev) => prev ?? job.finishedAt);
        }

        if (job.mode === 'graph') {
          setIsGraph(true);
          const runId = typeof job.graphRunId === 'string' && job.graphRunId ? job.graphRunId : null;
          setGraphRunId(runId);
          // Drive the session-sidebar header status off the Job status.
          setGraphSessionStatus(job.status === 'running' ? 'running' : job.status === 'completed' ? 'completed' : job.status === 'stopped' ? 'stopped' : job.status === 'failed' ? 'failed' : 'idle');

          // Graph node sessions are ordinary sessions. Surface them in the
          // session sidebar by deriving entries from executed instances, then hydrating the active
          // session's history (others prefetched at idle). The mini canvas and
          // GraphRunProgress carry the live run visualization; here we only
          // populate the per-node conversation view.
          if (runId && !cancelled) {
            let graphInstances: GraphInstanceState[] = [];
            let graphArchived: Record<string, GraphInstanceState> | undefined;
            try {
              const runRes = await fetch(apiUrl(`/job/${encodeURIComponent(job.id)}/graph-run`));
              if (runRes.ok) {
                const runData = (await runRes.json()) as GraphRunStatusResponse;
                applyGraphRunStatusSnapshot(runData);
                graphInstances = runData.instances || [];
                graphArchived = runData.run?.archivedInstances;
              }
            } catch (err) {
              console.error(`[hydration] Failed to load graph run ${runId} for job ${job.id}:`, err);
            }
            if (!cancelled) {
              const entries = graphSessionEntries(graphInstances, graphArchived);
              if (entries.length > 0) {
                setGraphSessions(entries);
                if (job.status !== 'running') {
                  setEndedSessionIds(new Set(entries.map((e) => e.sessionId)));
                }
                const activeSid = initialSessionId && entries.some((e) => e.sessionId === initialSessionId)
                  ? initialSessionId
                  : entries[entries.length - 1].sessionId;
                applyActiveSessionSelection(activeSid, activeSid === getLastGraphSessionId(entries));

                const activeMessages = await loadHistory(activeSid, activeSid);
                if (!cancelled) {
                  setMessages((prev) => (prev.length === 0 ? activeMessages : mergeMessages(prev, activeMessages)));
                  setLoadedSessionIds(new Set([activeSid]));
                  setIsLoadingHistory(false);
                }

                const remainingIds = entries.map((e) => e.sessionId).filter((sid) => sid !== activeSid);
                if (remainingIds.length > 0 && !cancelled) {
                  cancelIdlePrefetch = idlePrefetchSessions(
                    remainingIds,
                    async (sid) => {
                      if (cancelled || loadedSessionIdsRef.current.has(sid)) return;
                      let msgs: Message[];
                      try {
                        msgs = await loadHistory(sid, sid);
                      } catch (err) {
                        console.error(`[hydration] Failed to prefetch graph session ${sid}:`, err);
                        failedSessionIdsRef.current = new Set([...failedSessionIdsRef.current, sid]);
                        return;
                      }
                      if (cancelled) return;
                      if (msgs.length > 0) setMessages((prev) => mergeMessages(prev, msgs));
                      setLoadedSessionIds((prev) => new Set([...prev, sid]));
                    },
                    () => cancelled,
                  );
                }
              }
            }
          }
        } else {
          setIsGraph(false);
          setGraphRunId(null);
          // Interactive job: load history for all sessions (may have multiple
          // sessions when agent type was switched mid-conversation).
          if (!cancelled && job.sessionIds?.length > 0) {
            const allMsgs: Message[] = [];
            for (const sid of job.sessionIds) {
              if (cancelled) return;
              const sessionMsgs = await loadHistory(sid);
              allMsgs.push(...sessionMsgs);
            }
            if (!cancelled && allMsgs.length > 0) {
              setMessages((prev) => {
                if (prev.length === 0) return allMsgs;
                // Reuse mergeMessages so this path gets the same id-based and
                // semantic dedup (optimistic user messages and pure thought
                // bubbles whose live id diverged from the persisted
                // thought_msg_id) as every other history merge. A hand-rolled
                // id-only filter here would miss thought bubbles and
                // reintroduce duplicate thinking bubbles.
                return mergeMessages(prev, allMsgs, { deduplicateToolCallIds: true });
              });
            }
          }
        }

        // If the job is still running, reflect that in isLoading so the UI
        // shows the stop button instead of the send button after page refresh.
        if (!cancelled && job.status === 'running') {
          setIsLoading(true);
        }

        if (!cancelled) {
          historyLoadedRef.current = true;
          // Replay buffered SSE events. With unified message IDs (the backend
          // now stores the SSE message ID in Extra["msg_id"] and uses it in the
          // history API), events for already-loaded messages will be deduplicated
          // by the TEXT_MESSAGE_START and TOOL_CALL_START handlers (findIndex by
          // ID), while events for unflushed messages will create new entries.
          const pending = pendingEventsRef.current;
          pendingEventsRef.current = [];
          for (const evt of pending) {
            handleEventRef.current(evt);
          }
        }
      })
      .catch((err: Error & { status?: number }) => {
        if (cancelled) return;
        const status = err?.status;
        if (!status && isIgnorableNetworkError(err)) {
          console.error(`[JobChat] ignored transient network error while loading job: jobId=${existingJobId} err=${err?.message || String(err)}`);
          reportDisconnect();
          return;
        }
        if (status === 404) {
          // Stale jobId from URL / localStorage — the Job has been deleted
          // (or never existed). Surface a dedicated state so the parent can
          // clear the URL and route back to the workspace home, instead of
          // letting the user sit on an empty chat with no feedback.
          console.warn(`[JobChat] job not found: jobId=${existingJobId}`);
          setJobNotFound(true);
          setError('Job not found');
          try {
            onJobNotFoundRef.current?.(existingJobId);
          } catch (cbErr) {
            console.error('[JobChat] onJobNotFound callback failed:', cbErr);
          }
          return;
        }
        // Surface the real error message + status so the user (and logs)
        // know whether this is a network blip vs. server error vs. parse
        // failure. The previous "Failed to load job" wallpaper hid the
        // actual cause.
        const msg = err?.message ? err.message : String(err);
        const prefix = status ? `HTTP ${status}` : 'load failed';
        console.error(`[JobChat] failed to load job: jobId=${existingJobId} ${prefix} err=${msg}`);
        setError(`Failed to load job: ${msg}`);
      })
      .finally(() => {
        if (historyHydrationGenerationRef.current === hydrationGeneration) {
          historyHydratingRef.current = false;
        }
        if (!cancelled) {
          setIsLoadingHistory(false);
          // Open the SSE auto-connect gate. By this point lastEventSeqRef
          // is either seeded from a successful snapshot (the common path)
          // or still empty because the snapshot fetch failed; in the
          // failure case the SSE handler's 410 fallback (with its own
          // retry budget) is the recovery path, not this effect.
          console.debug(`[JobEvents][TRACE-SEQ0] gate-open jobId=${existingJobId} lastEventSeqRef=${JSON.stringify(lastEventSeqRef.current)}`);
          setSnapshotReady(true);
        }
      });
    return () => {
      cancelled = true;
      if (cancelIdlePrefetch) cancelIdlePrefetch();
    };
  }, [existingJobId, initialSessionId, apiUrl, isPublic, loadHistory, applyActiveSessionSelection, setGraphSessions, seedServerClockFromResponse, reportDisconnect, applyGraphRunStatusSnapshot]);

  // When the active Graph session changes, update session-level metadata
  // so ChatInput/MessageList reflect the session's agent/model.
  // Also re-run when loadedSessionIds changes: if a session was selected before
  // its history finished loading, sessionMetaMapRef had no data and the effect
  // returned early. Re-running when the session finishes loading picks up the
  // newly available metadata.
  useEffect(() => {
    if (!isGraph || !activeSessionId) return;
    // The token badge follows the selected session too. Kept outside the
    // `meta` guard below: a session can have a recorded context size before
    // (or without) metadata, and leaving the previous session's number on
    // screen misreports the context the next message would be sent into.
    const tokens = sessionTokensRef.current.get(activeSessionId);
    if (tokens != null) setTotalTokens(tokens);
    const meta = sessionMetaMapRef.current.get(activeSessionId);
    if (!meta) return;
    if (meta.modelId != null) setSessionModelId(meta.modelId);
    if (meta.type != null) setSessionType(meta.type);
    setSessionACPMode(meta.acpMode);
    setSessionACPThoughtLevel(meta.acpThoughtLevel);
  }, [isGraph, activeSessionId, loadedSessionIds, loadHistory]);

  // Load-on-switch: when the user selects a Graph session whose history
  // has not been loaded yet, fetch it on demand. Background idle-prefetch
  // (initial hydration) eventually warms every session, but a live graph run
  // only prefetches the active node session — sibling node sessions (e.g. a
  // downstream Prompt while the user is watching the Shell) are never warmed,
  // so without this they sit on "Loading session messages..." forever until a
  // manual refresh re-runs hydration. A short in-flight guard prevents the
  // effect (which also re-fires on every loadedSessionIds change) from issuing
  // duplicate fetches for the same session. Failed loads are handed to the
  // retry-on-switch effect below via failedSessionIdsRef.
  const switchLoadingSessionsRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    if (!isGraph || !activeSessionId) return;
    if (loadedSessionIds.has(activeSessionId)) return;
    // The retry effect owns sessions that already failed a load.
    if (failedSessionIdsRef.current.has(activeSessionId)) return;
    if (switchLoadingSessionsRef.current.has(activeSessionId)) return;
    const sid = activeSessionId;
    switchLoadingSessionsRef.current.add(sid);
    let cancelled = false;
    (async () => {
      try {
        const msgs = await loadHistory(sid, sid);
        if (cancelled) return;
        if (msgs.length > 0) {
          setMessages((prev) => mergeMessages(prev, msgs, { deduplicateToolCallIds: true }));
        }
        setLoadedSessionIds((prev) => new Set([...prev, sid]));
      } catch (err) {
        console.error(`[load-on-switch] Failed to load session ${sid}:`, err);
        failedSessionIdsRef.current = new Set([...failedSessionIdsRef.current, sid]);
      } finally {
        switchLoadingSessionsRef.current.delete(sid);
      }
    })();
    return () => { cancelled = true; };
  }, [isGraph, activeSessionId, loadedSessionIds, loadHistory]);

  // Retry loading a session that failed during background hydration when the
  // user switches to it. Without this, the session would appear as a blank
  // chat with no error indicator and no way to recover.
  useEffect(() => {
    if (!isGraph || !activeSessionId) return;
    if (!failedSessionIdsRef.current.has(activeSessionId)) return;
    // Already loaded (e.g. by another path) — clean up stale failure record.
    if (loadedSessionIds.has(activeSessionId)) {
      failedSessionIdsRef.current = new Set(
        [...failedSessionIdsRef.current].filter(id => id !== activeSessionId)
      );
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        const msgs = await loadHistory(activeSessionId, activeSessionId);
        if (cancelled) return;
        failedSessionIdsRef.current = new Set(
          [...failedSessionIdsRef.current].filter(id => id !== activeSessionId)
        );
        if (msgs.length > 0) {
          setMessages((prev) => mergeMessages(prev, msgs));
        }
        setLoadedSessionIds((prev) => new Set([...prev, activeSessionId]));
      } catch (err) {
        console.error(`[retry] Failed to reload session ${activeSessionId}:`, err);
        // Leave in failedSessionIdsRef so next switch can retry again.
      }
    })();
    return () => { cancelled = true; };
  }, [isGraph, activeSessionId, loadedSessionIds, loadHistory]);

  // Defensive dedup at the aggregation point. The messages array is written
  // by several paths (SSE live events, initial history load, reconnect merge,
  // Graph session hydration). Each path has its own dedup,
  // but if any of them slips a duplicate id through (observed in practice for
  // tool messages keyed by OpenAI's `call_*` ids), React emits a duplicate-key
  // warning in MessageList. Keep the FIRST occurrence so the ordering seen
  // upstream (history before SSE appends, active-session Step1 before Step2
  // background loads) is preserved.
  const dedupedMessages = useMemo(() => {
    const seen = new Set<string>();
    const out: Message[] = [];
    let duplicate: string | null = null;
    for (const m of messages) {
      if (seen.has(m.id)) {
        if (!duplicate) duplicate = m.id;
        continue;
      }
      seen.add(m.id);
      out.push(m);
    }
    if (duplicate && import.meta.env.DEV) {
      console.warn('[useJobChat] dropped duplicate message id:', duplicate);
    }
    return out;
  }, [messages]);

  // Loading-indicator phase. The streaming phase (derived from the tail
  // message) takes precedence over the backend preparation hint; when
  // neither applies, MessageList falls back to the default "AI 正在思考..."
  // label. O(1) render-time compute — no useMemo needed.
  const activePhase = deriveStreamingPhase(dedupedMessages) ?? backendPhase;

  // Compute filtered messages for the active session. Memoised so SSE
  // deltas (which land as new `messages` arrays every few hundred ms)
  // don't force downstream consumers to re-filter the full history on
  // every render — messages lists can reach thousands of entries during
  // long interactive sessions.
  const filteredMessages = useMemo(
    () => (activeSessionId ? dedupedMessages.filter((m) => m.sessionId === activeSessionId) : dedupedMessages),
    [dedupedMessages, activeSessionId],
  );

  return {
    jobId,
    jobTitle,
    setJobTitle,
    jobShareToken: jobShareTokenState,
    jobShareShowWorkspaceName,
    publicWorkspaceName,
    messages: isGraph ? filteredMessages : dedupedMessages,
    allMessages: dedupedMessages,
    isLoading,
    isLoadingHistory,
    activePhase,
    error,
    titleGenerationError,
    clearTitleGenerationError: () => setTitleGenerationError(null),
    jobNotFound,
    totalTokens,
    roundStartedAt: jobStartedAt,
    roundFinishedAt: jobFinishedAt,
    interactiveAccumulatedMs,
    // Returns the best estimate of current server wall-clock time (in ms).
    // Consumers should wrap their duration-rendering subtree in
    // ServerClockProvider with this function so DurationBadge live ticks
    // project elapsed in the same reference frame as the backend `timestamp`
    // on *_END events. Falls back to Date.now() before any event is seen.
    getServerNow: getServerNowEstimate,
    sessionWorkdir,
    sessionModelId,
    sessionType,
    sessionACPMode,
    sessionACPThoughtLevel,
    isGraph,
    graphRunId,
    graphRunStatusSnapshot,
    graphStreamError,
    applyGraphRunStatusSnapshot,
    graphSessionStatus,
    graphSessions,
    activeSessionId,
    setActiveSessionId,
    endedSessionIds,
    loadedSessionIds,
    // Session metadata resolver (maps sessionId -> { modelId, type, acpMode })
    getSessionMeta: (sessionId: string) => sessionMetaMapRef.current.get(sessionId) ?? null,
    // Actions
    sendMessage,
    queueMessage,
    cancelQueuedMessage,
    clearQueuedMessages,
    queuedMessages,
    messageQueuePaused,
    messageQueuePauseReason,
    continueMessageQueue,
    stopGeneration,
    clearMessages,
    eventsReady,
    isPublic,
  };
}
