import { useState, useCallback, useMemo, useRef, useEffect, type SetStateAction } from 'react';
import {
  Message,
  AssistantMessage,
  ToolMessage,
  AgentEvent,
  EventTypeEnum,
  CommandSystemMessageEvent,
  MessageRoleEnum,
  MessageStatusEnum,
  ToolCallStatusEnum,
  JobProgress,
  FlowNode,
  LoopConfig,
  type GraphInstanceState,
  type GraphInstanceStatus,
  type GraphInstanceKey,
  type GraphRunStatus,
  type GraphRunStatusResponse,
} from '../types';
import { SSEClient } from '../utils/sse-client';
import { mergeMessages } from '../utils/mergeMessages';
import { useConnectionStatus } from '../contexts/ConnectionStatus';
import { isKnownCommand } from '../utils/commands';
import i18n from '../i18n';

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

// Strip runtime builtin variables from a loop variable map, leaving only the
// user-defined entries. The server injects builtins like _job_id / _current_time
// into the persisted map during execution; surfacing them as editable rows would
// be confusing and round-tripping them back risks clobbering server state.
//
// Always returns a concrete map: a null/undefined input (the backend omits an
// empty `variables` map via `omitempty`) maps to `{}` — "hydrated, no user
// variables" — NOT undefined. Callers invoke this only once the job's loopConfig
// is confirmed hydrated, so the result always carries hydrated semantics. The
// `loopVariables` state stays `undefined` only at its initial pre-hydration
// value; JobChat treats that lone `undefined` as "not hydrated yet" and only
// then falls back to initialLoopConfig variables. Returning `undefined` here
// would let an omitted (saved-empty) map resurrect stale initial variables.
const builtinLoopVars = new Set([
  '_job_id',
  '_job_title',
  '_job_workdir',
  '_workspace_id',
  '_current_time',
  '_current_path',
  '_last_assistant_msg',
]);

function userLoopVariables(vars: Record<string, string> | undefined | null): Record<string, string> {
  const out: Record<string, string> = {};
  if (vars == null) return out;
  for (const [k, v] of Object.entries(vars)) {
    if (!builtinLoopVars.has(k)) out[k] = v;
  }
  return out;
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
// remaining loop sessions are eventually in memory (smooth tab switches)
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

function getLastLoopSessionId(sessions: LoopSessionEntry[]): string | null {
  return sessions.length > 0 ? sessions[sessions.length - 1].sessionId : null;
}

// mapGraphInstanceStatus collapses a GraphInstanceState status onto the loop
// session-entry status union so graph node sessions render in the same sidebar
// as loop iterations. succeeded/skipped → completed (both terminal-OK in the
// sidebar's eyes), failed → failed, interrupted → interrupted, anything still
// in flight → running.
function mapGraphInstanceStatus(status: GraphInstanceStatus): LoopSessionEntry['status'] {
  switch (status) {
    case 'succeeded':
    case 'skipped':
      return 'completed';
    case 'failed':
      return 'failed';
    case 'interrupted':
      return 'interrupted';
    default:
      return 'running';
  }
}

// graphInstanceLabel builds a human-readable sidebar label for a node instance,
// appending the loop iteration context (loopNodeId#n) so repeated nodes inside
// a loop stay distinguishable.
function graphInstanceLabel(inst: GraphInstanceState): string {
  const base = inst.nodeTitle || inst.nodeId;
  const key: GraphInstanceKey | undefined = inst.key;
  const iters = key?.iterations;
  if (iters && iters.length > 0) {
    const suffix = iters.map((it) => `${it.loopNodeId}#${it.index + 1}`).join(' / ');
    return `${base} · ${suffix}`;
  }
  return base;
}

// graphSessionEntries derives loop-style session entries from a graph run's
// executed instances. Agent nodes (Prompt/Evaluator) expose their session via
// sessionId; Shell nodes record their own transcript session in
// displaySessionId. Nodes with neither (IfElse/start/end) have no session and
// show on the mini canvas instead.
function graphSessionEntries(instances: GraphInstanceState[]): LoopSessionEntry[] {
  const entries: LoopSessionEntry[] = [];
  // Order by execution start so the session sidebar numbers nodes in the
  // order they actually ran (e.g. an upstream Shell before its downstream
  // Prompt), not the backend's instance-map iteration order. Array.sort is
  // stable, so instances that share a startedAt — or have none yet (still
  // pending, sorted last) — keep their original relative order.
  const ordered = [...instances].sort((a, b) => {
    const sa = a.startedAt ?? Number.POSITIVE_INFINITY;
    const sb = b.startedAt ?? Number.POSITIVE_INFINITY;
    return sa - sb;
  });
  for (const inst of ordered) {
    // Prefer displaySessionId (Shell nodes record their own transcript session
    // there); fall back to the lineage sessionId for Agent nodes.
    const displaySid = inst.displaySessionId || inst.sessionId;
    if (!displaySid) continue;
    entries.push({
      sessionId: displaySid,
      path: [],
      label: graphInstanceLabel(inst),
      status: mapGraphInstanceStatus(inst.status),
      durationMs: inst.durationMs,
      startedAt: inst.startedAt || undefined,
      error: inst.error?.message,
    });
  }
  return entries;
}

// GRAPH_LIVE_STATUSES mirrors GraphLoopProgress: the run is still producing
// events while in any of these states, so the Chat page keeps an SSE
// subscription open to refresh node sessions as they complete.
const GRAPH_LIVE_STATUSES = new Set<GraphRunStatus>(['pending', 'running', 'pausing', 'stepStopping', 'recovering']);

export interface QueuedMessage {
  id: string;
  content: string;
  imageUrls?: string[];
  modelId?: string | null;
  acpMode?: string;
  acpThoughtLevel?: string;
  agentType?: string;
}

export interface LoopSessionEntry {
  sessionId: string;
  path: number[];
  label: string;
  // 'interrupted' marks iterations that were still running when the job
  // was stopped — the backend preserves Resume.NextPath so Continue can
  // re-run them, so the sidebar must NOT paint these as 'completed'.
  status: 'running' | 'completed' | 'failed' | 'interrupted';
  durationMs?: number;
  tokens?: number;
  error?: string;
  /** Timestamp when the iteration started (for real-time duration display while running). */
  startedAt?: number;
}

interface UseJobChatOptions {
  existingJobId?: string;
  initialSessionId?: string;
  shareToken?: string;
  /** Fired when the backend returns 404 for the existing Job (deleted / never
   *  existed). Lets the parent clear the stale jobId from URL + state and
   *  route back to the workspace home, instead of leaving the user stuck on
   *  an empty chat page. */
  onJobNotFound?: (jobId: string) => void;
}

export function useJobChat(options: UseJobChatOptions = {}) {
  const { existingJobId, initialSessionId, shareToken, onJobNotFound } = options;
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
  const [jobTitle, setJobTitle] = useState('');
  const [jobShareTokenState, setJobShareTokenState] = useState<string | null>(null);
  // Set when /job/:id returns 404. Gates the SSE auto-connect (no point
  // hammering /events for a job that doesn't exist) and lets JobChat surface
  // a dedicated "job not found" banner instead of an empty chat.
  const [jobNotFound, setJobNotFound] = useState(false);
  const [messages, setMessages] = useState<Message[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [isLoadingHistory, setIsLoadingHistory] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
    const sig = `${event.command} ${event.present || ''} ${event.text}`;
    const seen = appliedCommandEventsRef.current;
    // Drop entries older than the dedup window so the map can't grow unbounded.
    for (const [k, ts] of seen) {
      if (now - ts > 10_000) seen.delete(k);
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

  // Loop state
  const [isLoop, setIsLoop] = useState(false);
  const isLoopRef = useRef(false);
  const [isGraph, setIsGraph] = useState(false);
  const [graphRunId, setGraphRunId] = useState<string | null>(null);
  const [loopProgress, setLoopProgress] = useState<JobProgress | null>(null);
  const [loopStatus, setLoopStatus] = useState<'idle' | 'running' | 'completed' | 'stopped' | 'failed'>('idle');
  // True once a graceful "stop after step" has been requested but the loop has
  // not yet reached the step boundary where it stops. Drives the "keep running"
  // affordance. The button only renders while status === 'running', so a stale
  // value is naturally masked once the loop stops; it is reset on job switch and
  // on Continue (a fresh run) to be safe.
  const [stopPending, setStopPending] = useState(false);
  // Flow tree of the current loop job, used by the progress UI to derive the
  // per-session / per-step position. Hydrated when the job is fetched or when
  // a fresh loop is started from initialLoopConfig.
  const [loopFlow, setLoopFlow] = useState<FlowNode[] | null>(null);

  // User-defined loop variables of the current job, hydrated alongside loopFlow
  // so the editor can show and round-trip them. `undefined` means the job has
  // not hydrated yet; `{}` means it has no user variables and must override any
  // stale initialLoopConfig fallback.
  const [loopVariables, setLoopVariables] = useState<Record<string, string> | undefined>(undefined);
  // Disabled (toggled-off) user-variable keys, hydrated alongside loopVariables
  // so reopening the editor on a saved job shows the saved enable/disable state.
  const [loopDisabledVars, setLoopDisabledVars] = useState<string[] | undefined>(undefined);

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

  // Loop session tracking
  const [loopSessions, setLoopSessionsState] = useState<LoopSessionEntry[]>([]);
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

  // Per-session agent metadata (populated during loadHistory for loop sessions)
  const sessionMetaMapRef = useRef<Map<string, { modelId: string | null; type: string | null; acpMode: string | null; acpThoughtLevel: string | null }>>(new Map());

  const [eventsReady, setEventsReady] = useState(false);
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
  // Bumped by sendMessage / startLoop / continueLoop when SSE has been
  // disconnected (terminal event cleanup). Forces the auto-connect useEffect
  // to re-fire and establish a fresh SSE subscription for the new run.
  const [sseReconnectSeq, setSseReconnectSeq] = useState(0);

  const eventSseRef = useRef<SSEClient | null>(null);
  const historyLoadedRef = useRef(false);
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
  const loopSessionsRef = useRef<LoopSessionEntry[]>([]);
  const activeSessionIdRef = useRef<string | null>(null);
  const followLatestSessionRef = useRef(true);
  // Wall-clock timestamp of the last SSE event received (any type, including
  // keep-alive surrogates). Used by the idle-watchdog inside the SSE
  // auto-connect effect to detect the "subscriber stuck but connection still
  // alive" case — when the loop appears to be running but the stream has
  // gone silent, we resync /job/:id to recover any missed terminal status.
  const lastEventReceivedAtRef = useRef<number>(Date.now());
  // Mirror of isLoading so the watchdog interval can read it without
  // re-subscribing the SSE effect on every loading-state flip.
  const isLoadingRef = useRef(false);

  const setLoopSessions = useCallback((value: SetStateAction<LoopSessionEntry[]>) => {
    setLoopSessionsState((prev) => {
      const next = typeof value === 'function'
        ? (value as (prevState: LoopSessionEntry[]) => LoopSessionEntry[])(prev)
        : value;
      loopSessionsRef.current = next;
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
      sessionId !== null && sessionId === getLastLoopSessionId(loopSessionsRef.current)
    );
  }, [applyActiveSessionSelection]);

  // Mirror isLoading -> ref so the SSE idle-watchdog interval can read the
  // current value without re-subscribing the SSE effect on every flip.
  useEffect(() => {
    isLoadingRef.current = isLoading;
  }, [isLoading]);

  // Reset all state when existingJobId changes so a reused component
  // does not briefly show stale data from the previous job.
  useEffect(() => {
    historyLoadedRef.current = false;
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
    setMessages([]);
    setIsLoop(false);
    isLoopRef.current = false;
    setIsGraph(false);
    setGraphRunId(null);
    setLoopFlow(null);
    setLoopVariables(undefined);
    setLoopDisabledVars(undefined);
    setLoopProgress(null);
    setLoopStatus('idle');
    setStopPending(false);
    setLoopSessions([]);
    applyActiveSessionSelection(null, true);
    setEndedSessionIds(new Set());
    setEventsReady(false);
    // For an existing job the snapshot fetch is what populates
    // lastEventSeqRef.current; the SSE effect must wait for it. For the
    // new-chat flow (no existingJobId) the buffer is brand new so seq=0
    // is legal and the gate opens straight away.
    setSnapshotReady(!existingJobId);
    // [TRACE-SEQ0] gate initial state on jobId change.
    console.debug(`[JobEvents][TRACE-SEQ0] gate-init existingJobId=${existingJobId ?? '(none)'} snapshotReadyInitial=${!existingJobId} lastEventSeqRef=${JSON.stringify(lastEventSeqRef.current)}`);
    sessionMetaMapRef.current = new Map();
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
  }, [existingJobId, applyActiveSessionSelection, setLoopSessions]);

  const handleEventRef = useRef<(event: AgentEvent) => void>(() => {});
  // Ref to syncJobState so handleEvent can call it without a dependency cycle
  // (syncJobState is defined after handleEvent). Updated alongside handleEventRef.
  const syncJobStateRef = useRef<(id: string, metadataOnly?: boolean, forceSkipMessages?: boolean) => Promise<void>>(() => Promise.resolve());
  // Distinguish "start loop execution" from "interactive send in loop mode".
  // Backend currently emits JOB_* events for both flows.
  const shouldResetOnJobStartRef = useRef(false);
  // Track whether a loop is actively running (set on JOB_STARTED, cleared on JOB_COMPLETED/STOPPED/FAILED).
  const loopRunningRef = useRef(false);

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
      // A freshly opened long-running job can replay the current round's
      // ITERATION_STARTED from hours ago. Treating that old event timestamp as
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

  // Handle events from both interactive and loop modes
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

    const finalizeRunningLoopSessions = (
      prev: LoopSessionEntry[],
      nextStatus: LoopSessionEntry['status'],
      endedAt: number,
    ): LoopSessionEntry[] => {
      if (!prev.some((s) => s.status === 'running')) return prev;
      return prev.map((s) => {
        if (s.status !== 'running') return s;
        // When an iteration is interrupted (stop / failure), we may never
        // receive ITERATION_* result events that carry durationMs. Approximate
        // using startedAt and the terminal event timestamp to avoid duration
        // disappearing in the sidebar.
        const durationMs = (s.durationMs == null && s.startedAt != null)
          ? Math.max(0, endedAt - s.startedAt)
          : s.durationMs;
        return { ...s, status: nextStatus, durationMs };
      });
    };

    switch (event.type) {
      // Job-level events
      case EventTypeEnum.JOB_STARTED:
        // When replaying history events on refresh, don't reset a terminal status
        // that was already set from the job API response.
        setLoopStatus((prev) => {
          if (prev === 'completed' || prev === 'stopped' || prev === 'failed') return prev;
          return 'running';
        });
        loopRunningRef.current = true;
        setJobStartedAt((prev) => prev ?? event.timestamp);
        setJobFinishedAt(undefined);
        setLoopProgress((prev) => {
          // Don't reset progress if already loaded from job API with results.
          // Skipped-only progress (empty-prompt skips record no results) must
          // survive too: resetting to the event's static totalSteps would drop
          // skippedPaths and undo the deduction the backend already persisted.
          if (prev && ((prev.results && prev.results.length > 0) || (prev.skippedPaths && Object.keys(prev.skippedPaths).length > 0))) return prev;
          return {
            totalSteps: event.totalSteps,
            completedCount: 0,
            failedCount: 0,
          };
        });
        if (shouldResetOnJobStartRef.current) {
          setLoopSessions([]);
          setEndedSessionIds(new Set());
          applyActiveSessionSelection(null, true);
          shouldResetOnJobStartRef.current = false;
        }
        break;

      case EventTypeEnum.JOB_COMPLETED: {
        // runOutcome describes the actual outcome of the run that just
        // ended. For loop runs it equals the event type; for interactive
        // sends on an already-terminal job the event type is the restored
        // prior status, while runOutcome reflects this send's real
        // result. Fall back to the event type when the field is absent
        // (legacy backend).
        const runOutcome = event.runOutcome ?? 'completed';
        // Only drive loop-level UI state when this is a loop run. If the
        // event reflects a restored prior status (interactive send),
        // leave loopStatus alone so a previously-stopped loop still
        // reads as stopped in the sidebar.
        if (loopRunningRef.current) {
          setLoopStatus('completed');
        }
        loopRunningRef.current = false;
        setStopPending(false);
        setJobFinishedAt(event.timestamp || Date.now());
        setLoopProgress(event.progress);
        setIsLoading(false);
        // Mark all sessions as ended so input becomes editable
        setEndedSessionIds((prev) => {
          const next = new Set(prev);
          if (event.progress?.results) {
            for (const r of event.progress.results) {
              next.add(r.sessionId);
            }
          }
          return next;
        });
        // Flush any remaining "running" session entries based on this
        // run's actual outcome, not the restored job status.
        const sessionStatus: LoopSessionEntry['status'] =
          runOutcome === 'stopped' ? 'interrupted'
            : runOutcome === 'failed' ? 'failed'
              : 'completed';
        setLoopSessions((prev) => finalizeRunningLoopSessions(prev, sessionStatus, event.timestamp || Date.now()));
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
        if (event.jobId) syncJobStateRef.current(event.jobId, undefined, true).catch((err) => {
          console.warn('[useJobChat] JOB_COMPLETED syncJobState failed:', err);
        });
        // Job is terminal — disconnect SSE to stop the infinite
        // reconnect cycle (connect → idle timeout → server close → reconnect).
        console.info('[useJobChat] JOB_COMPLETED: disconnecting SSE (terminal state)');
        eventSseRef.current?.disconnect();
        break;
      }

      case EventTypeEnum.JOB_STOPPED: {
        const runOutcome = event.runOutcome ?? 'stopped';
        if (loopRunningRef.current) {
          setLoopStatus('stopped');
        }
        loopRunningRef.current = false;
        setStopPending(false);
        setJobFinishedAt(event.timestamp || Date.now());
        setLoopProgress(event.progress);
        setIsLoading(false);
        setEndedSessionIds((prev) => {
          const next = new Set(prev);
          if (event.progress?.results) {
            for (const r of event.progress.results) {
              next.add(r.sessionId);
            }
          }
          return next;
        });
        // Running iterations at the moment of stop are interrupted, not
        // completed — backend preserves Resume.NextPath so Continue can
        // re-run them. Mark them as 'interrupted' so the sidebar does
        // not falsely paint a partial iteration green.
        const sessionStatus: LoopSessionEntry['status'] =
          runOutcome === 'completed' ? 'completed'
            : runOutcome === 'failed' ? 'failed'
              : 'interrupted';
        setLoopSessions((prev) => finalizeRunningLoopSessions(prev, sessionStatus, event.timestamp || Date.now()));
        finalizeInFlightMessages(event.timestamp || Date.now(), {
          toolProcessingStatus:
            runOutcome === 'completed' ? ToolCallStatusEnum.Success
              : ToolCallStatusEnum.Placeholder,
          placeholderReason:
            runOutcome === 'failed' ? 'job_failed' : 'interrupted',
        });
        if (event.jobId) syncJobStateRef.current(event.jobId, undefined, true).catch((err) => {
          console.warn('[useJobChat] JOB_STOPPED syncJobState failed:', err);
        });
        // Job is terminal — disconnect SSE to stop the infinite reconnect cycle.
        console.info('[useJobChat] JOB_STOPPED: disconnecting SSE (terminal state)');
        eventSseRef.current?.disconnect();
        break;
      }

      case EventTypeEnum.JOB_FAILED: {
        const runOutcome = event.runOutcome ?? 'failed';
        if (loopRunningRef.current) {
          setLoopStatus('failed');
        }
        loopRunningRef.current = false;
        setStopPending(false);
        setJobFinishedAt(event.timestamp || Date.now());
        if (event.progress) setLoopProgress(event.progress);
        setError(event.message);
        setIsLoading(false);
        setEndedSessionIds((prev) => {
          const next = new Set(prev);
          if (event.progress?.results) {
            for (const r of event.progress.results) {
              next.add(r.sessionId);
            }
          }
          return next;
        });
        const sessionStatus: LoopSessionEntry['status'] =
          runOutcome === 'completed' ? 'completed'
            : runOutcome === 'stopped' ? 'interrupted'
              : 'failed';
        setLoopSessions((prev) => finalizeRunningLoopSessions(prev, sessionStatus, event.timestamp || Date.now()));
        finalizeInFlightMessages(event.timestamp || Date.now(), {
          toolProcessingStatus:
            runOutcome === 'completed' ? ToolCallStatusEnum.Success
              : ToolCallStatusEnum.Placeholder,
          placeholderReason:
            runOutcome === 'stopped' ? 'interrupted' : 'job_failed',
        });
        if (event.jobId) syncJobStateRef.current(event.jobId, undefined, true).catch((err) => {
          console.warn('[useJobChat] JOB_FAILED syncJobState failed:', err);
        });
        // Job is terminal — disconnect SSE to stop the infinite reconnect cycle.
        console.info('[useJobChat] JOB_FAILED: disconnecting SSE (terminal state)');
        eventSseRef.current?.disconnect();
        break;
      }

      case EventTypeEnum.ITERATION_STARTED: {
        const iterSessionId = event.sessionId;
        const path: number[] = event.path || [];
        const label = path.map((p: number) => p + 1).join('.');
        const clientMessageId = event.clientMessageId;
        const isInteractiveSend = !!clientMessageId;
        const shouldFollowLatestSession = !isInteractiveSend
          && (followLatestSessionRef.current || !activeSessionIdRef.current);

        // Only update loop progress for loop execution, not for interactive message sends.
        if (!isInteractiveSend) {
          setLoopProgress((prev) =>
            prev ? {
              ...prev,
              currentPath: path,
            } : prev
          );
        }

        // Only add session entry for loop execution, not for interactive message sends.
        if (!isInteractiveSend) {
          setLoopSessions((prev) => {
            const idx = prev.findIndex((s) => s.sessionId === iterSessionId && s.path.length === path.length && s.path.every((v: number, i: number) => v === path[i]));
            if (idx >= 0) {
              // Backfill startedAt on entries pre-populated from the job API
              // (page refresh path) that didn't have it. Without this, the
              // Loop Sidebar can't render the live duration badge for a
              // running session after a refresh, even though the SSE ring
              // buffer re-delivers ITERATION_STARTED on reconnect.
              if (prev[idx].startedAt == null) {
                const updated = [...prev];
                updated[idx] = { ...updated[idx], startedAt: event.timestamp };
                return updated;
              }
              return prev;
            }
            return [...prev, {
              sessionId: iterSessionId,
              path,
              label,
              status: 'running',
              startedAt: event.timestamp,
            }];
          });
        }

        if (isInteractiveSend) {
          activeSessionIdRef.current = iterSessionId;
          setActiveSessionIdState(iterSessionId);
        } else if (shouldFollowLatestSession) {
          applyActiveSessionSelection(iterSessionId, true);
        }
        setLoadedSessionIds((prev) => prev.has(iterSessionId) ? prev : new Set([...prev, iterSessionId]));

        // Populate session metadata from the SSE event so that
        // resolveAgentForSession can resolve the correct agent icon/name
        // without waiting for loadHistory to complete.
        if (!sessionMetaMapRef.current.has(iterSessionId)) {
          sessionMetaMapRef.current.set(iterSessionId, {
            modelId: event.modelId || null,
            type: event.agentType || null,
            acpMode: event.acpMode || null,
            acpThoughtLevel: event.acpThoughtLevel || null,
          });
        }

        // Confirm optimistic interactive user message by clientMessageId.
        if (isInteractiveSend) {
          setMessages((prev) =>
            prev.map((m) =>
              m.role === MessageRoleEnum.USER && m.clientMessageId === clientMessageId
                ? { ...m, sessionId: iterSessionId, pending: false, failed: false }
                : m
            )
          );
        }

        // Insert synthetic user message only for loop execution.
        if (!isInteractiveSend && isLoopRef.current && event.message) {
          // A loop session can contain multiple auto-sent user turns. Use runId
          // when available so later turns in the same session do not collide with
          // the first synthetic message.
          const userMsgId = `loop-user-${event.runId || `${iterSessionId}-${path.join('-')}-${event.timestamp}`}`;
          const userMsg: Message = {
            id: userMsgId,
            role: MessageRoleEnum.USER,
            content: event.message,
            createdAt: event.timestamp,
            status: MessageStatusEnum.Finished,
            sessionId: iterSessionId,
          };
          setMessages((prev) => {
            if (prev.some((m) => m.id === userMsgId)) return prev;
            // History IDs differ from the synthetic ID. Deduplicate by the same
            // session + content instead of session only, otherwise later auto
            // turns in the same loop session are incorrectly suppressed.
            if (prev.some((m) => m.role === MessageRoleEnum.USER && m.sessionId === iterSessionId && m.content === event.message)) return prev;
            return [...prev, userMsg];
          });
        }
        break;
      }

      case EventTypeEnum.ITERATION_COMPLETED:
        setLoopProgress((prev) => {
          if (!prev) return prev;
          if (event.result && prev.results?.some(
            (r: { path: number[] }) =>
              r.path.length === event.result.path.length && r.path.every((v: number, i: number) => v === event.result.path[i])
          )) {
            return prev;
          }
          return {
            ...prev,
            completedCount: prev.completedCount + 1,
            results: [...(prev.results || []), event.result],
          };
        });
        if (event.result) {
          const rPath = event.result.path || [];
          const rLabel = rPath.map((p: number) => p + 1).join('.');
          setLoopSessions((prev) => {
            const idx = prev.findIndex((s) =>
              s.sessionId === event.result.sessionId && s.path.length === rPath.length && s.path.every((v: number, i: number) => v === rPath[i])
            );
            if (idx >= 0) {
              const updated = [...prev];
              updated[idx] = { ...updated[idx], status: 'completed' as const, durationMs: event.result.durationMs, tokens: event.result.tokens };
              return updated;
            }
            return [...prev, {
              sessionId: event.result.sessionId,
              path: rPath,
              label: rLabel,
              status: 'completed' as const,
              durationMs: event.result.durationMs,
              tokens: event.result.tokens,
            }];
          });
          setEndedSessionIds((prev) => new Set(prev).add(event.result.sessionId));
        }
        break;

      case EventTypeEnum.ITERATION_FAILED:
        if (loopRunningRef.current) {
          setLoopProgress((prev) => {
            if (!prev) return prev;
            if (event.result && prev.results?.some(
              (r: { path: number[] }) =>
                r.path.length === event.result.path.length && r.path.every((v: number, i: number) => v === event.result.path[i])
            )) {
              return prev;
            }
            return {
              ...prev,
              failedCount: prev.failedCount + 1,
              results: [...(prev.results || []), event.result],
            };
          });
        }
        if (!loopRunningRef.current) {
          if (event.result?.error) {
            setError(event.result.error);
          }
          setIsLoading(false);
        }
        if (event.result) {
          const fPath = event.result.path || [];
          const fLabel = fPath.map((p: number) => p + 1).join('.');
          setLoopSessions((prev) => {
            const idx = prev.findIndex((s) =>
              s.sessionId === event.result.sessionId && s.path.length === fPath.length && s.path.every((v: number, i: number) => v === fPath[i])
            );
            if (idx >= 0) {
              const updated = [...prev];
              updated[idx] = { ...updated[idx], status: 'failed' as const, durationMs: event.result.durationMs, tokens: event.result.tokens, error: event.result.error };
              return updated;
            }
            return [...prev, {
              sessionId: event.result.sessionId,
              path: fPath,
              label: fLabel,
              status: 'failed' as const,
              durationMs: event.result.durationMs,
              tokens: event.result.tokens,
              error: event.result.error,
            }];
          });
          setEndedSessionIds((prev) => new Set(prev).add(event.result.sessionId));
        }
        break;

      // Agent-level events (same as useAgentChat)
      case EventTypeEnum.RUN_STARTED:
        setIsLoading(true);
        setError(null);
        // Reset timing for this new run (interactive mode resets per-round)
        if (!loopRunningRef.current) {
          setJobStartedAt(event.timestamp || Date.now());
          setJobFinishedAt(undefined);
        }
        break;

      case EventTypeEnum.RUN_FINISHED:
        if (!loopRunningRef.current) {
          setIsLoading(false);
          // In interactive mode (no loop), RUN_FINISHED marks the end of the round
          setJobFinishedAt((prev) => prev ?? (event.timestamp || Date.now()));
        }
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
        if (!loopRunningRef.current) {
          setIsLoading(false);
          // In interactive mode (no loop), RUN_ERROR also marks the end of the round.
          // Mirror RUN_FINISHED so the ChatInput badge doesn't briefly disappear
          // between RUN_ERROR and the subsequent JOB_* terminal event.
          setJobFinishedAt((prev) => prev ?? (event.timestamp || Date.now()));
        }
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
        setMessages((prev) =>
          prev.map((msg) => {
            if (msg.id === event.messageId && msg.role === MessageRoleEnum.ASSISTANT) {
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
            }
            return msg;
          })
        );
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
        setMessages((prev) =>
          prev.map((msg) => {
            if (msg.id === event.toolCallId && msg.role === MessageRoleEnum.TOOL) {
              if (msg.status === MessageStatusEnum.Finished) return msg;
              const toolMsg = msg as ToolMessage;
              return { ...toolMsg, toolCallArgs: toolMsg.toolCallArgs + event.delta, toolCallStatus: event.toolCallStatus || toolMsg.toolCallStatus };
            }
            return msg;
          })
        );
        break;

      case EventTypeEnum.TOOL_CALL_RESULT:
        setMessages((prev) =>
          prev.map((msg) => {
            if (msg.id === event.toolCallId && msg.role === MessageRoleEnum.TOOL) {
              if (msg.status === MessageStatusEnum.Finished) return msg;
              const toolMsg = msg as ToolMessage;
              return { ...toolMsg, content: toolMsg.content + event.delta, toolCallStatus: event.toolCallStatus };
            }
            return msg;
          })
        );
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
        if (event.name === 'token_usage') {
          const usage = event.value as { totalTokens?: number };
          if (usage?.totalTokens) setTotalTokens(usage.totalTokens);
        }
        if (event.name === 'job_title_updated') {
          const payload = event.value as { title?: string } | string | null;
          const nextTitle = typeof payload === 'string' ? payload : payload?.title;
          if (nextTitle) setJobTitle(nextTitle);
        }
        if (event.name === 'progress_total_updated') {
          // The backend recomputed the total-steps denominator: a group broke
          // early via stepStopLoop (evaluator STOP or Shell STOP_LOOP), or a
          // prompt step was skipped because its rendered prompt was empty.
          // Merge the per-group actual counts, the skipped-leaf set and the
          // advanced current path so the progress bar fills and the
          // session/step plan reflects the real run instead of the static cap.
          const payload = event.value as {
            totalSteps?: number;
            groupActualIterations?: Record<string, number>;
            groupActualLeafCounts?: Record<string, number>;
            skippedPaths?: Record<string, boolean>;
            currentPath?: number[] | null;
          } | null;
          if (payload) {
            setLoopProgress((prev) =>
              prev
                ? {
                    ...prev,
                    totalSteps:
                      typeof payload.totalSteps === 'number' ? payload.totalSteps : prev.totalSteps,
                    groupActualIterations:
                      payload.groupActualIterations ?? prev.groupActualIterations,
                    groupActualLeafCounts:
                      payload.groupActualLeafCounts ?? prev.groupActualLeafCounts,
                    skippedPaths: payload.skippedPaths ?? prev.skippedPaths,
                    // currentPath is authoritative when the key is present: a
                    // skip at the tail of the flow legitimately CLEARS it
                    // (null), which must not fall back to the stale path.
                    currentPath:
                      'currentPath' in payload ? payload.currentPath ?? undefined : prev.currentPath,
                  }
                : prev
            );
          }
        }
        if (event.name === 'graceful_stop_pending') {
          // Another tab requested or cancelled a "stop after step", or the loop
          // consumed the request at a step boundary. Sync the local pending
          // state so this tab's stop buttons match. Runtime-only — not persisted.
          const payload = event.value as { pending?: boolean } | null;
          setStopPending(!!payload?.pending);
        }
        break;

      case EventTypeEnum.COMMAND_SYSTEM_MESSAGE:
        applyCommandEvent(event as CommandSystemMessageEvent);
        break;

      default:
        break;
    }
  }, [applyActiveSessionSelection, applyCommandEvent, clearPendingCommandWatchdog, finalizeInFlightMessages, setLoopSessions, updateServerClock]);

  // Keep ref in sync so the SSE effect always uses the latest handler
  handleEventRef.current = handleEvent;

  // Load history for existing job
  const loadHistory = useCallback(async (sid: string, tagSessionId?: string) => {
      const response = await fetch(apiUrl(`/sessions/${sid}/messages`));
      if (!response.ok) {
        if (response.status === 404) return [];
        throw new Error(`Failed to load history for session ${sid} (HTTP ${response.status})`);
      }
      const data = await response.json();

      // Always store per-session metadata for loop sessions
      const metaKey = tagSessionId || sid;
      sessionMetaMapRef.current.set(metaKey, {
        modelId: data.modelId || null,
        type: data.type || null,
        acpMode: data.acpMode || null,
        acpThoughtLevel: data.acpThoughtLevel || null,
      });

      if (!tagSessionId) {
        setSessionModelId(data.modelId || null);
        if (data.type) setSessionType(data.type);
        if (data.acpMode) setSessionACPMode(data.acpMode);
        if (data.acpThoughtLevel) setSessionACPThoughtLevel(data.acpThoughtLevel);
        if (data.tokenUsage?.totalTokens) setTotalTokens(data.tokenUsage.totalTokens);
        if (data.workdir) setSessionWorkdir(data.workdir);
      }

      const historyMessages = data.messages || [];
      const converted: Message[] = [];
      const now = Date.now();

      for (const msg of historyMessages) {
        if (msg.role === 'user') {
          converted.push({ id: msg.id, role: MessageRoleEnum.USER, content: msg.content, createdAt: msg.startedAt || now, status: MessageStatusEnum.Finished, sessionId: tagSessionId, pending: false, failed: false, imageUrls: msg.imageUrls || undefined });
        } else if (msg.role === 'assistant') {
          if (msg.isThinking) {
            // Separate thought entry emitted by the history API when thought_msg_id is present.
            converted.push({ id: msg.id, role: MessageRoleEnum.ASSISTANT, content: '', createdAt: msg.startedAt || now, status: MessageStatusEnum.Finished, thinkingContent: msg.reasoningContent || '', isThinking: false, isShellOutput: false, sessionId: tagSessionId, finishedAt: msg.finishedAt || undefined, thinkingFinishedAt: msg.thoughtFinishedAt || undefined });
          } else {
            converted.push({ id: msg.id, role: MessageRoleEnum.ASSISTANT, content: msg.content, createdAt: msg.startedAt || now, status: MessageStatusEnum.Finished, thinkingContent: msg.reasoningContent || '', isThinking: false, isShellOutput: msg.isShellOutput || false, isSummary: msg.isSummary || false, sessionId: tagSessionId, finishedAt: msg.finishedAt || undefined, thinkingFinishedAt: msg.thoughtFinishedAt || undefined });
          }
          if (msg.toolCalls) {
            // Legacy history may not carry per-tool `startedAt`. In that case,
            // approximate the tool start with the assistant/thinking end
            // boundary instead of `Date.now()`, otherwise a historical
            // `finishedAt` combined with a fresh page-load timestamp would
            // produce a negative elapsed and hide the badge entirely.
            const toolCreatedAtFallback = msg.finishedAt || msg.thoughtFinishedAt || msg.startedAt || now;
            for (const tc of msg.toolCalls) {
              converted.push({ id: tc.id, role: MessageRoleEnum.TOOL, content: '', createdAt: toolCreatedAtFallback, status: MessageStatusEnum.Started, toolCallId: tc.id, toolCallName: (tc.name && tc.name !== 'undefined') ? tc.name : '', toolCallArgs: tc.arguments, toolCallStatus: ToolCallStatusEnum.Processing, parentMessageId: msg.id, sessionId: tagSessionId });
            }
          }
        } else if (msg.role === 'tool') {
          const idx = converted.findIndex((m) => m.role === MessageRoleEnum.TOOL && (m as ToolMessage).toolCallId === msg.toolCallId);
          if (idx >= 0) {
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
  }, [apiUrl]);

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
      if (Array.isArray(job?.loopConfig?.flow)) {
        setLoopFlow(job.loopConfig.flow);
        setLoopVariables(userLoopVariables(job.loopConfig.variables));
        setLoopDisabledVars(job.loopConfig.disabledVars || []);
      }

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
        setIsLoading(false);
        // Job is terminal — disconnect SSE to prevent infinite reconnect loops.
        // This covers Case B: client reconnects after the terminal event was
        // already sent (page refresh, mobile background recovery, network flap).
        // Case A (client online when terminal event arrives) is handled in
        // handleEvent's JOB_COMPLETED/STOPPED/FAILED branches.
        if (eventSseRef.current && !eventSseRef.current.isDisconnected()) {
          console.info(`[JobEvents] syncJobState: disconnecting SSE (job terminal, status=${status}, jobId=${id})`);
          eventSseRef.current.disconnect();
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
        // during failure, or idle-watchdog recovery). Without this, non-loop
        // interactive jobs lose the failure reason display on refresh.
        if (runOutcome === 'failed' && job.progress?.lastError) {
          setError((prev: string | null) => prev || job.progress.lastError);
        }
      }

        if (job.mode === 'loop') {
          setIsGraph(false);
          setGraphRunId(null);
          type AuthoritativeResult = {
            sessionId: string;
            path: number[];
            success: boolean;
            durationMs?: number;
            tokens?: number;
            error?: string;
          };
          const entryKey = (sessionId: string, path: number[]) =>
            `${sessionId}|${path.join('.')}`;
          const authoritativeResults = new Map<string, AuthoritativeResult>();
          if (job.progress?.results) {
            for (const r of job.progress.results as AuthoritativeResult[]) {
              authoritativeResults.set(entryKey(r.sessionId, r.path || []), r);
            }
          }
          const appendMissingRounds = (patched: LoopSessionEntry[], existingKeys: Set<string>) => {
            for (const r of authoritativeResults.values()) {
              const rPath = r.path || [];
              const key = entryKey(r.sessionId, rPath);
              if (existingKeys.has(key)) continue;
              patched.push({
                sessionId: r.sessionId,
                path: rPath,
                label: rPath.map((p) => p + 1).join('.') || '-',
                status: r.success ? 'completed' : 'failed',
                durationMs: r.durationMs,
                tokens: r.tokens,
                error: r.error,
              });
              existingKeys.add(key);
            }
          };

          if (isTerminal) {
            setLoopStatus(status === 'completed' ? 'completed' : status === 'stopped' ? 'stopped' : 'failed');
            loopRunningRef.current = false;
          // Flush any remaining "running" session entries to the matching
          // terminal status. Stopped jobs produce 'interrupted' entries
          // (backend preserves Resume.NextPath), not 'completed'.
          const terminalStatus: LoopSessionEntry['status'] =
            status === 'failed' ? 'failed'
              : status === 'stopped' ? 'interrupted'
                : 'completed';
          // Prefer authoritative results from job.progress.results over local
          // fallback: on reconnect the ring buffer may have been cleared
          // (terminal events flush it), so any ITERATION_COMPLETED missed
          // while disconnected won't be replayed — the only recovery path
          // is the job API's persisted results.
          //
          // Key by `sessionId|path` (not just sessionId): in non-eachRepeat
          // round modes a single session is reused across multiple steps,
          // so progress.results can contain multiple entries sharing the
          // same sessionId. Collapsing them by sessionId would let the
          // last round overwrite earlier ones.
            setLoopSessions((prev) => {
              const existingKeys = new Set(
                prev.map((s) => entryKey(s.sessionId, s.path))
              );
              const hasRunning = prev.some((s) => s.status === 'running');
              const needsBackfill = prev.some((s) =>
                authoritativeResults.has(entryKey(s.sessionId, s.path))
              );
              const hasMissingRounds = [...authoritativeResults.values()].some(
                (r) => !existingKeys.has(entryKey(r.sessionId, r.path || []))
              );
              if (!hasRunning && !needsBackfill && !hasMissingRounds) {
                return prev;
              }
            // Prefer the persisted terminal timestamp when the job is already
            // in a terminal state: `job.finishedAt` is the actual stop point,
            // while a reconnect-time "server now" projection would include
            // any offline gap between stop and reconnect. Fall back to the
            // projection only when the API response lacks finishedAt.
            const endedAt = job.finishedAt ?? getServerNowEstimate();
            const patched: LoopSessionEntry[] = prev.map((s) => {
              const authoritative = authoritativeResults.get(
                entryKey(s.sessionId, s.path)
              );
              if (authoritative) {
                return {
                  ...s,
                  status: authoritative.success ? 'completed' : 'failed',
                  durationMs: authoritative.durationMs ?? s.durationMs,
                  tokens: authoritative.tokens ?? s.tokens,
                  error: authoritative.error ?? s.error,
                };
              }
              if (s.status !== 'running') return s;
              const durationMs = (s.durationMs == null && s.startedAt != null)
                ? Math.max(0, endedAt - s.startedAt)
                : s.durationMs;
              return { ...s, status: terminalStatus, durationMs };
            });
            // Append rounds that exist in progress.results but were never
            // materialised locally — e.g. iteration events that both fired
            // inside the disconnect window and were flushed from the ring
            // buffer before reconnect. Without this, such rounds disappear
            // from the sidebar even though the backend has their result.
              appendMissingRounds(patched, existingKeys);
              return patched;
            });
          } else if (authoritativeResults.size > 0) {
            setLoopSessions((prev) => {
              const existingKeys = new Set(
                prev.map((s) => entryKey(s.sessionId, s.path))
              );
              const needsBackfill = prev.some((s) =>
                authoritativeResults.has(entryKey(s.sessionId, s.path))
              );
              const hasMissingRounds = [...authoritativeResults.values()].some(
                (r) => !existingKeys.has(entryKey(r.sessionId, r.path || []))
              );
              if (!needsBackfill && !hasMissingRounds) {
                return prev;
              }
            const patched: LoopSessionEntry[] = prev.map((s) => {
              const authoritative = authoritativeResults.get(entryKey(s.sessionId, s.path));
              if (!authoritative) return s;
              return {
                ...s,
                status: authoritative.success ? 'completed' as const : 'failed' as const,
                durationMs: authoritative.durationMs ?? s.durationMs,
                tokens: authoritative.tokens ?? s.tokens,
                error: authoritative.error ?? s.error,
              };
            });
              appendMissingRounds(patched, existingKeys);
              return patched;
            });
          }
          if (job.progress) {
            setLoopProgress(job.progress);
            // Restore the runtime-only graceful-stop pending state from the
            // snapshot. syncJobState is the authoritative recovery path for SSE
            // reconnect / watchdog / pre-connect snapshot, and graceful_stop_pending
            // is a transient (unbuffered) event that may not replay after a
            // reconnect — so the snapshot is the only reliable source. Without
            // this, a missed pending=true left the "Keep running" affordance
            // hidden, and a missed pending=false left a stale "stop after step".
            setStopPending(status === 'running' && !!job.progress.gracefulStopPending);
        }
      } else if (job.mode === 'graph') {
        setIsLoop(false);
        isLoopRef.current = false;
        setIsGraph(true);
        setGraphRunId(typeof job.graphRunId === 'string' && job.graphRunId ? job.graphRunId : null);
        setStopPending(false);
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
        const isLoopMode = job.mode === 'loop';
        if (isLoopMode) {
          // Loop recovery: load the active session synchronously, then warm
          // the rest at idle priority (matches initial hydration /
          // reload-from-disk). Non-loop falls through to the full parallel
          // load below — those jobs have few sessions and the token total
          // depends on all of them being loaded.
          const activeSid =
            (activeSessionIdRef.current && sessionIds.includes(activeSessionIdRef.current))
              ? activeSessionIdRef.current
              : sessionIds[sessionIds.length - 1];
          let activeMsgs: Message[] = [];
          try {
            activeMsgs = await loadHistory(activeSid, activeSid);
          } catch (err) {
            console.warn(`[JobEvents] syncJobState: failed to load active session ${activeSid}:`, err);
          }
          // Generation check: discard if a newer syncJobState started while we
          // were loading (mirrors the parallel path's post-load guard).
          if (gen !== syncGenerationRef.current) {
            console.debug(`[JobEvents] syncJobState stale after active load: gen=${gen} current=${syncGenerationRef.current}`);
            return;
          }
          if (activeMsgs.length > 0) {
            setMessages((prev) => mergeMessages(prev, activeMsgs, { deduplicateToolCallIds: true }));
          }
          setLoadedSessionIds((prev) => new Set([...prev, activeSid]));

          const remainingIds = sessionIds.filter((sid) => sid !== activeSid);
          if (remainingIds.length > 0) {
            idlePrefetchSessions(
              remainingIds,
              async (sid) => {
                if (gen !== syncGenerationRef.current || loadedSessionIdsRef.current.has(sid)) return;
                let msgs: Message[];
                try {
                  msgs = await loadHistory(sid, sid);
                } catch (err) {
                  console.warn(`[JobEvents] syncJobState: failed to prefetch session ${sid}:`, err);
                  failedSessionIdsRef.current = new Set([...failedSessionIdsRef.current, sid]);
                  return;
                }
                if (gen !== syncGenerationRef.current) return;
                if (msgs.length > 0) {
                  setMessages((prev) => mergeMessages(prev, msgs, { deduplicateToolCallIds: true }));
                }
                setLoadedSessionIds((prev) => new Set([...prev, sid]));
              },
              () => gen !== syncGenerationRef.current,
            );
          }
        } else {
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
        // Sync loadedSessionIds so UI doesn't show "Loading session messages..."
        // for sessions whose messages we just loaded.
        setLoadedSessionIds((prev) => {
          const next = new Set(prev);
          for (const sid of sessionIds) next.add(sid);
          return next;
        });
        }
      }
      } // end !skipMessages

    } catch (err) {
      console.warn('[SSE reconnect] failed to sync job state:', err);
      throw err;
    }
  }, [apiUrl, finalizeInFlightMessages, loadHistory, setLoopSessions, getServerNowEstimate, seedServerClockFromResponse]);

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

    let cancelled = false;
    const currentJobId = jobId;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    // Cancel handle for background idle-prefetch started by a disk reload /
    // sync. A reconnect may trigger another reload, so we cancel the prior
    // prefetch before starting a new one and on effect cleanup.
    let cancelIdlePrefetch: (() => void) | null = null;

    // Backoffs after attempt 0 / 1 / 2 fail. Total budget = 1 initial + 3
    // retries. After all retries are exhausted we surface the server's
    // original 410 message verbatim — no replacement, no truncation, per
    // the project rule that errors must be shown to the user.
    const RETRY_BACKOFFS_MS = [200, 1000, 3000];

    // Idle-watchdog: when the loop appears to be running (isLoading=true)
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

    const scheduleNextOrSurface = (currentAttempt: number, errMsg: string) => {
      if (cancelled) return;
      const backoff = RETRY_BACKOFFS_MS[currentAttempt];
      if (backoff === undefined) {
        // Retry budget exhausted. Surface the server's original error so
        // the user sees exactly what the server said.
        console.error(`[JobEvents] resume-point recovery exhausted after ${currentAttempt + 1} attempts: ${errMsg}`);
        setError(errMsg || 'SSE resume point gone');
        return;
      }
      console.warn(`[JobEvents] resume point gone (attempt ${currentAttempt + 1}); retrying in ${backoff}ms: ${errMsg}`);
      retryTimer = setTimeout(() => { void attemptConnect(currentAttempt + 1); }, backoff);
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

      const isLoopJob = Array.isArray(job?.loopConfig?.flow) && job.loopConfig.flow.length > 0;
      if (isLoopJob) {
        // Mirror initial hydration: load the active session synchronously so
        // it paints immediately, then warm the rest at idle priority. The
        // active session is the one the user is currently viewing (or the
        // shared initialSessionId, else the last). The load-on-switch effect
        // still covers any session the user clicks before prefetch reaches it.
        const activeSid =
          (activeSessionIdRef.current && sessionIds.includes(activeSessionIdRef.current))
            ? activeSessionIdRef.current
            : (initialSessionId && sessionIds.includes(initialSessionId))
              ? initialSessionId
              : sessionIds[sessionIds.length - 1];

        const activeMsgs = await loadHistory(activeSid, activeSid);
        if (cancelled) return;
        setMessages((prev) => mergeMessages(prev, activeMsgs, { deduplicateToolCallIds: true }));
        setLoadedSessionIds((prev) => new Set([...prev, activeSid]));

        const remainingIds = sessionIds.filter((sid) => sid !== activeSid);
        if (remainingIds.length > 0 && !cancelled) {
          if (cancelIdlePrefetch) cancelIdlePrefetch();
          cancelIdlePrefetch = idlePrefetchSessions(
            remainingIds,
            async (sid) => {
              if (cancelled || loadedSessionIdsRef.current.has(sid)) return;
              let msgs: Message[];
              try {
                msgs = await loadHistory(sid, sid);
              } catch (err) {
                console.error(`[reload-from-disk] Failed to prefetch session ${sid}:`, err);
                failedSessionIdsRef.current = new Set([...failedSessionIdsRef.current, sid]);
                return;
              }
              if (cancelled) return;
              if (msgs.length > 0) {
                setMessages((prev) => mergeMessages(prev, msgs, { deduplicateToolCallIds: true }));
              }
              setLoadedSessionIds((prev) => new Set([...prev, sid]));
            },
            () => cancelled,
          );
        }
      } else {
        // Non-loop job may have multiple sessions (e.g. agent type switch).
        // Load all sessions so no messages are lost.
        const allMsgs: Message[] = [];
        for (const sid of sessionIds) {
          if (cancelled) return;
          const msgs = await loadHistory(sid);
          allMsgs.push(...msgs);
        }
        if (cancelled) return;
        setMessages((prev) => mergeMessages(prev, allMsgs, { deduplicateToolCallIds: true }));
      }
      console.debug(`[JobEvents][TRACE-SEQ0] reload-from-disk done jobId=${currentJobId} sessions=${sessionIds.length} isLoop=${isLoopJob}`);
    };

    const attemptConnect = async (attempt: number): Promise<void> => {
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
      // Pass metadataOnly=true on attempt 0 because the hydration effect
      // already loaded messages; we only need the fresh lastEventSeq here.
      // On retries (attempt > 0), reloadMessagesFromDisk handles messages
      // separately below, so we also pass forceSkipMessages=true to prevent
      // syncJobState from doing a redundant message reload (which otherwise
      // happens for terminal jobs due to the metadataOnly && !isTerminal rule).
      try {
        await syncJobState(currentJobId, true, attempt > 0);
      } catch (err) {
        if (cancelled) return;
        if (attempt === 0) {
          // First attempt: tolerate snapshot failure and fall back to
          // empty Last-Event-ID. The server's 410 path will recover us
          // (and that path counts against the retry budget below).
          console.warn('[JobEvents] pre-connect snapshot fetch failed; will let SSE recover via 410 path:', err);
        } else {
          // Subsequent attempts MUST have a fresh snapshot — without it
          // we'd retry with the same stale seq and 410 again.
          const originalError = resumeGoneErrorRef.current || 'HTTP 410';
          const snapshotError = err instanceof Error ? err.message : String(err);
          scheduleNextOrSurface(attempt, `Resume point expired: ${originalError}; Snapshot reload failed: ${snapshotError}`);
          return;
        }
      }
      if (cancelled) return;

      // attempt > 0 means we've taken a 410 (or a snapshot failure that the
      // server-side 410 path could not recover from on its own). The buffer's
      // GC contract guarantees any GC'd event is already persisted: closed
      // rounds → messages.jsonl, B-class state → job.json. So the correct
      // recovery is to rebuild the message list from disk before reconnecting,
      // not just retry SSE with a fresher seq — the latter leaves a gap in
      // the UI for any closed-round events that were buffered when we first
      // loaded the page but have since been GC'd.
      if (attempt > 0) {
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

      const client = new SSEClient();
      eventSseRef.current = client;

      console.debug(`[JobEvents][TRACE-SEQ0] connectUntilReady jobId=${jobId} attempt=${attempt} initialLastEventId=${JSON.stringify(lastEventSeqRef.current)}`);

      client.connectUntilReady({
        url: apiUrl(`/job/${jobId}/events`),
        initialLastEventId: lastEventSeqRef.current,
        onEvent: (event) => handleEventRef.current(event),
        onError: (err) => {
          if (cancelled) return;
          setError(err.message || String(err));
        },
        onDisconnect: () => {
          reportDisconnect();
        },
        onReconnect: () => {
          reportReconnect();
          // Only sync metadata (title, status, progress, lastEventSeq).
          // SSE resumes from lastEventId so no events are lost; full
          // message reload would race with live SSE events and cause
          // visual duplication of old messages.
          console.debug(`[JobEvents] onReconnect: syncing metadata for jobId=${currentJobId}`);
          void syncJobState(currentJobId, true).catch((err) => {
            console.warn('[onReconnect] syncJobState failed:', err);
          });
        },
        onResumePointGone: (errorMessage) => {
          // The buffer GC'd past our Last-Event-ID. Schedule another
          // attempt (snapshot + fresh client) up to the retry budget;
          // after that, surface the server's original error to the user.
          if (cancelled) return;
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
        setEventsReady(true);
        // Sync metadata after SSE connects. Message reload is always skipped
        // here: on attempt 0 the hydration effect already loaded messages,
        // and on attempt > 0 reloadMessagesFromDisk handled it. Allowing
        // syncJobState to reload messages for terminal jobs would cause a
        // redundant second full reload.
        void syncJobState(currentJobId, true, true).catch((err) => {
          console.warn('[post-connect] syncJobState failed:', err);
        });
      }).catch((err) => {
        if (!cancelled) {
          console.error('[JobEvents] connect failed:', err);
        }
      });
    };

    void attemptConnect(0);

    return () => {
      cancelled = true;
      setEventsReady(false);
      window.clearInterval(watchdog);
      if (retryTimer) clearTimeout(retryTimer);
      if (cancelIdlePrefetch) cancelIdlePrefetch();
      // Close whichever client is currently in use (initial OR any one
      // installed by a 410 retry). Closure-capturing the first client
      // would leak retry-installed ones across unmount.
      eventSseRef.current?.disconnect();
      eventSseRef.current = null;
    };
  }, [jobId, jobNotFound, snapshotReady, sseReconnectSeq, apiUrl, syncJobState, reportDisconnect, reportReconnect, seedServerClockFromResponse]);

  // Graph mode: keep node sessions live. The job-events SSE above carries no
  // graph traffic (graph runs emit on their own /graph/run/:runId/events
  // stream), so subscribe separately while the run is in flight. We don't
  // re-stream agent token deltas into messages here — GraphLoopProgress already
  // animates the run, and the message view is the per-node conversation. On any
  // instance lifecycle / progress event we reconcile: rebuild the session list
  // from the run's instances and reload the messages of sessions whose node
  // just produced output.
  const graphSseRef = useRef<SSEClient | null>(null);
  useEffect(() => {
    graphSseRef.current?.disconnect();
    graphSseRef.current = null;
    if (!isGraph || !graphRunId) return;

    let cancelled = false;
    let lastInstanceRefreshAt = 0;

    const reconcile = async () => {
      if (cancelled) return;
      let instances: GraphInstanceState[] = [];
      let runStatus: GraphRunStatus | undefined;
      try {
        const res = await fetch(apiUrl(`/graph/run/${encodeURIComponent(graphRunId)}`));
        if (!res.ok) return;
        const data = (await res.json()) as GraphRunStatusResponse;
        instances = data.instances || [];
        runStatus = data.run?.status;
      } catch (err) {
        console.error(`[graph-sse] reconcile fetch failed for run ${graphRunId}:`, err);
        return;
      }
      if (cancelled) return;
      const entries = graphSessionEntries(instances);
      if (entries.length > 0) setLoopSessions(entries);
      const terminalSids = instances
        .map((i) => ({ sid: i.displaySessionId || i.sessionId, status: i.status }))
        .filter((i): i is { sid: string; status: GraphInstanceStatus } => !!i.sid && i.status !== 'running' && i.status !== 'pending')
        .map((i) => i.sid);
      if (terminalSids.length > 0) setEndedSessionIds((prev) => new Set([...prev, ...terminalSids]));

      // Reload the currently-viewed session so a node finishing while the user
      // watches it fills in its conversation. Other sessions reload lazily when
      // selected (handled by the load-on-switch effect).
      const active = activeSessionIdRef.current;
      if (active && entries.some((e) => e.sessionId === active)) {
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

      if (runStatus && !GRAPH_LIVE_STATUSES.has(runStatus)) {
        setIsLoading(false);
      }
    };

    const client = new SSEClient();
    graphSseRef.current = client;
    void client.connectUntilReady({
      url: apiUrl(`/graph/run/${encodeURIComponent(graphRunId)}/events`),
      initialLastEventId: '0',
      onEvent: (raw) => {
        const evt = raw as unknown as { type?: string };
        const t = evt.type;
        if (t === 'instanceStarted' || t === 'instanceCompleted' || t === 'instanceFailed'
          || t === 'instanceSkipped' || t === 'progressUpdated') {
          // Throttle reconciliation so a burst of events triggers at most one
          // refetch per ~400ms instead of hammering the run-status endpoint.
          const now = Date.now();
          if (now - lastInstanceRefreshAt < 400) return;
          lastInstanceRefreshAt = now;
          void reconcile();
        }
      },
      onError: () => { /* progress component surfaces graph errors */ },
      onResumePointGone: () => void reconcile(),
    }).catch(() => { /* best-effort live refresh */ });

    return () => {
      cancelled = true;
      client.disconnect();
      if (graphSseRef.current === client) graphSseRef.current = null;
    };
  }, [isGraph, graphRunId, apiUrl, loadHistory, setLoopSessions]);

  // Send interactive message.
  //
  // options.bypassCommand — skip the slash-command fast path AND tell the
  // server to skip its command-dispatch branch. Used by the home-page path
  // where the user typed `/help` as the very first message so the text becomes
  // the Job's first message, not a command.
  const sendMessage = useCallback(async (content: string, modelId?: string | null, targetSessionId?: string | null, imageUrls?: string[], acpMode?: string, agentType?: string, acpThoughtLevel?: string, options?: { bypassCommand?: boolean }) => {
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
    const hasImages = !!imageUrls && imageUrls.length > 0;
    if (!options?.bypassCommand && !hasImages && isKnownCommand(trimmed)) {
      // Clear any previous pending watchdog before dispatching a new command.
      clearPendingCommandWatchdog();
      try {
        const res = await fetch(`/api/v1/job/${jobId}/message`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ messages: [{ role: 'user', content: trimmed }] }),
        });
        if (!res.ok) {
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

    const userMessageId = crypto.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
    const clientMessageId = userMessageId;
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
      imageUrls: imageUrls && imageUrls.length > 0 ? imageUrls : undefined,
    } as Message;

    setMessages((prev) => [...prev, userMessage]);
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

    // If SSE was disconnected by a prior terminal event (JOB_COMPLETED /
    // STOPPED / FAILED), bump the reconnect seq to re-establish the SSE
    // subscription before the new run starts emitting events.
    if (!eventSseRef.current || eventSseRef.current.isDisconnected()) {
      console.info('[sendMessage] SSE disconnected, triggering reconnect for new run');
      setSseReconnectSeq((s) => s + 1);
    }

    try {
      const payload: Record<string, unknown> = {
        messages: [{
          id: userMessageId,
          type: 'text',
          content,
          timestamp: Date.now(),
          role: 'user',
          imageUrls: imageUrls && imageUrls.length > 0 ? imageUrls : undefined,
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
      // Safety net: if the frontend command list drifts from the backend's
      // (see utils/commands.ts — drift is explicitly allowed), the backend
      // may still intercept a command the client didn't fast-path. In that
      // case the response carries `command_dispatched` (plus the inline
      // `event`); clean up the optimistic user bubble and render the result.
      const body = await response.json().catch(() => null);
      if (body?.status === 'command_dispatched') {
        setMessages((prev) => prev.filter((m) => m.id !== userMessageId));
        setIsLoading(false);
        const event = body?.event as CommandSystemMessageEvent | undefined;
        if (event && event.text) applyCommandEvent(event);
      }
      // Events come through the /events SSE connection
    } catch (err) {
      console.error('[sendMessage] error:', err);
      setMessages((prev) =>
        prev.map((msg) =>
          msg.id === userMessageId
            ? { ...msg, pending: false, failed: true }
            : msg
        )
      );
      setError(err instanceof Error ? err.message : 'Failed to send message');
      setIsLoading(false);
    }
  }, [jobId, isPublic, clearPendingCommandWatchdog, applyCommandEvent]);

  // Cleanup watchdog on unmount.
  useEffect(() => {
    return () => {
      try { clearPendingCommandWatchdog(); } catch { /* ignore */ }
    };
  }, [clearPendingCommandWatchdog]);

  // Queue a message to send after the current run finishes (interactive mode only)
  const queueMessage = useCallback((msg: Omit<QueuedMessage, 'id'>) => {
    const id = crypto.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
    setQueuedMessages((prev) => [...prev, { ...msg, id }]);
  }, []);

  const cancelQueuedMessage = useCallback((id: string) => {
    setQueuedMessages((prev) => prev.filter((m) => m.id !== id));
  }, []);

  const clearQueuedMessages = useCallback(() => {
    setQueuedMessages([]);
  }, []);

  // Mutex: while true, we've dispatched a queued send and are waiting for `isLoading`
  // to flip true (job actually started). Prevents the effect from re-entering and
  // flushing the whole queue in one microtask.
  const queueDispatchingRef = useRef(false);

  // Auto-send the next queued message when the current run becomes idle (interactive mode only).
  // Sends STRICTLY one-at-a-time: waits for `isLoading` to go false (job finished) before
  // firing the next item in the queue.
  useEffect(() => {
    if (isLoop) return;
    // A run is in progress — clear the dispatch lock and wait for it to finish.
    if (isLoading) {
      queueDispatchingRef.current = false;
      return;
    }
    // Just dispatched a send; isLoading hasn't flipped true yet — don't re-enter.
    if (queueDispatchingRef.current) return;
    if (queuedMessages.length === 0) return;

    queueDispatchingRef.current = true;
    const head = queuedMessages[0];
    setQueuedMessages((prev) => prev.slice(1));
    sendMessage(head.content, head.modelId ?? null, null, head.imageUrls, head.acpMode, head.agentType, head.acpThoughtLevel).catch((err) => {
      console.error('[queuedMessage] send failed:', err);
      queueDispatchingRef.current = false;
    });
  }, [isLoading, isLoop, queuedMessages, sendMessage]);

  // Start loop execution
  const startLoop = useCallback(async () => {
    if (!jobId || isPublic) return;
    setIsLoading(true);
    setError(null);
    shouldResetOnJobStartRef.current = true;
    // Accumulate previous turn's duration before resetting.
    if (jobStartedAtRef.current != null && jobFinishedAtRef.current != null) {
      setInteractiveAccumulatedMs((prev) => prev + (jobFinishedAtRef.current! - jobStartedAtRef.current!));
    }
    // Fresh run: clear any round timestamps left over from a previous run
    // so the footer "total duration" badge anchors to the new JOB_STARTED.
    setJobStartedAt(undefined);
    setJobFinishedAt(undefined);

    // Re-establish SSE if it was torn down by a prior terminal event.
    if (!eventSseRef.current || eventSseRef.current.isDisconnected()) {
      console.info('[startLoop] SSE disconnected, triggering reconnect for new run');
      setSseReconnectSeq((s) => s + 1);
    }

    try {
      const response = await fetch(`/api/v1/job/${jobId}/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      });
      if (!response.ok) {
        throw new Error(await readHTTPError(response));
      }
    } catch (err) {
      console.error('[startLoop] error:', err);
      setError(err instanceof Error ? err.message : 'Failed to start loop');
      setIsLoading(false);
    }
  }, [jobId, isPublic]);

  const continueLoop = useCallback(async () => {
    if (!jobId || isPublic) return;
    setIsLoading(true);
    setError(null);
    // A fresh run: drop any stale graceful-stop request from the prior run so
    // the new run doesn't start showing "keep running".
    setStopPending(false);
    // Accumulate previous turn's duration before resetting.
    if (jobStartedAtRef.current != null && jobFinishedAtRef.current != null) {
      setInteractiveAccumulatedMs((prev) => prev + (jobFinishedAtRef.current! - jobStartedAtRef.current!));
    }
    // Continue starts a fresh run: clear the prior run's round timestamps
    // so the footer "total duration" badge anchors to the new JOB_STARTED
    // instead of carrying over the previous run's start time.
    setJobStartedAt(undefined);
    setJobFinishedAt(undefined);

    // Re-establish SSE if it was torn down by a prior terminal event.
    if (!eventSseRef.current || eventSseRef.current.isDisconnected()) {
      console.info('[continueLoop] SSE disconnected, triggering reconnect for new run');
      setSseReconnectSeq((s) => s + 1);
    }

    try {
      const response = await fetch(`/api/v1/job/${jobId}/continue`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      });
      if (!response.ok) {
        throw new Error(await readHTTPError(response));
      }
      const data = await response.json().catch(() => null);
      // The backend accepted the continue and a new run is launching. Flip to
      // running immediately so the stop buttons enable without waiting for the
      // SSE JOB_STARTED round-trip (which previously left the UI stuck showing
      // the prior "stopped" state with only Continue available).
      loopRunningRef.current = true;
      setLoopStatus('running');
      if (data?.progress) {
        setLoopProgress(data.progress);
      }
    } catch (err) {
      console.error('[continueLoop] error:', err);
      setError(err instanceof Error ? err.message : 'Failed to continue loop');
      setIsLoading(false);
    }
  }, [jobId, isPublic]);

  // Stop loop execution. graceful=true lets the current step finish and stops
  // at the next step boundary (resume preserved); the default hard stop cancels
  // the in-flight step immediately. A successful graceful request flips
  // stopPending so the UI can offer "keep running" until the loop actually stops.
  const stopLoop = useCallback(async (graceful = false) => {
    if (!jobId) return;
    try {
      const response = await fetch(`/api/v1/job/${jobId}/stop`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ graceful }),
      });
      if (!response.ok) {
        throw new Error(await readHTTPError(response));
      }
      if (graceful) {
        const data = await response.json().catch(() => null);
        if (data?.status === 'stopping') {
          setStopPending(true);
          showCommandToast(i18n.t('loop.stop.gracefulRequested'));
        }
      } else {
        // A hard stop never reaches a graceful step boundary, so the backend
        // won't emit graceful_stop_pending=false. Clear the local flag here so
        // an escalation from "stop after step" to "stop now" drops the
        // "keep running" affordance immediately.
        setStopPending(false);
      }
    } catch (err) {
      console.error('[stopLoop] error:', err);
      setError(err instanceof Error ? err.message : 'Failed to stop loop');
    }
  }, [jobId]);

  // Cancel a pending graceful stop so the loop keeps running. Only meaningful
  // while stopPending is true (the request has not yet been consumed at a step
  // boundary). Clears stopPending optimistically on success.
  const cancelStop = useCallback(async () => {
    if (!jobId) return;
    try {
      const response = await fetch(`/api/v1/job/${jobId}/stop`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ graceful: true, cancel: true }),
      });
      if (!response.ok) {
        throw new Error(await readHTTPError(response));
      }
      setStopPending(false);
      showCommandToast(i18n.t('loop.stop.gracefulCancelled'));
    } catch (err) {
      console.error('[cancelStop] error:', err);
      setError(err instanceof Error ? err.message : 'Failed to cancel stop');
    }
  }, [jobId]);

  // Edit a loop job's LoopConfig. The backend applies it as a full replacement
  // when the job is not running, or as a per-step field update when running
  // (rejecting structure changes with 409). Rethrows on failure so the editor
  // can keep its panel open and surface the message.
  const updateLoopConfig = useCallback(async (config: LoopConfig) => {
    if (!jobId || isPublic) return;
    const response = await fetch(`/api/v1/job/${jobId}/loop-config`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ loopConfig: config }),
    });
    if (!response.ok) {
      throw new Error(await readHTTPError(response, `PUT /job/${jobId}/loop-config`));
    }
    const data = await response.json().catch(() => null);
    // Reflect the edit locally: the flow drives both the progress session/step
    // plan and (for a stopped job) a subsequent Continue. Variables hydrate the
    // editor on its next open so the saved set is shown rather than an empty list.
    if (config.flow) {
      setLoopFlow(config.flow);
    }
    setLoopVariables(userLoopVariables(config.variables));
    setLoopDisabledVars(config.disabledVars || []);
    if (data?.progress) {
      setLoopProgress(data.progress);
    }
  }, [jobId, isPublic]);

  const stopGeneration = useCallback(async () => {
    if (isLoop) {
      await stopLoop();
    } else {
      if (jobId) {
        try {
          await fetch(`/api/v1/job/${jobId}/stop`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
          });
        } catch (err) {
          console.error('[stopGeneration] error:', err);
        }
      }
      setIsLoading(false);
    }
  }, [isLoop, stopLoop, jobId]);

  const clearMessages = useCallback(() => {
    setMessages([]);
    setError(null);
  }, []);

  // Load job details for existing job
  useEffect(() => {
    if (!existingJobId || historyLoadedRef.current) return;
    let cancelled = false;
    // Cancel handle for the background idle-prefetch of non-active loop
    // sessions; wired into this effect's cleanup so a job switch / unmount
    // drops any pending idle callback immediately.
    let cancelIdlePrefetch: (() => void) | null = null;
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
        if (job.shareToken) setJobShareTokenState(job.shareToken);
        if (Array.isArray(job?.loopConfig?.flow)) {
          setLoopFlow(job.loopConfig.flow);
          setLoopVariables(userLoopVariables(job.loopConfig.variables));
          setLoopDisabledVars(job.loopConfig.disabledVars || []);
        }
        // Hydrate base job metadata on refresh, especially for loop mode where
        // history is loaded with tagged session ids and won't set these fields.
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

        if (job.mode === 'loop') {
          setIsLoop(true);
          isLoopRef.current = true;
          setIsGraph(false);
          setGraphRunId(null);
          if (job.progress) {
            setLoopProgress(job.progress);
            // Restore the runtime-only graceful-stop pending state from the
            // snapshot so a refresh / second tab shows the "keep running"
            // affordance. Only meaningful while running; a terminal job never
            // has a pending stop.
            setStopPending(job.status === 'running' && !!job.progress.gracefulStopPending);
            const deriveExtraSessionStatus = (status: string): LoopSessionEntry['status'] => {
              if (status === 'running') return 'running';
              if (status === 'failed') return 'failed';
              // Stopped jobs preserve Resume.NextPath on the backend, so any
              // iteration missing a recorded result is a resumable half-done
              // step, not a successful completion.
              if (status === 'stopped') return 'interrupted';
              return 'completed';
            };
            const formatSessionPathLabel = (path: number[]) =>
              path.map((p: number) => p + 1).join('.') || '-';
            const sessionEntryKey = (sessionId: string, path: number[]) =>
              `${sessionId}|${path.join('.')}`;
            const appendMissingSessionEntries = (
              entries: LoopSessionEntry[],
              existingSessionIds: Set<string>
            ) => {
              const next = [...entries];
              const existingEntryKeys = new Set(
                entries.map((entry) => sessionEntryKey(entry.sessionId, entry.path))
              );
              // Sum of already-completed iteration durations. Used to anchor the
              // running entry's startedAt as `job.startedAt + completedBaseMs`,
              // so the bottom DurationBadge can compute live elapsed after a
              // page refresh — before any SSE event has had a chance to set
              // the per-session startedAt.
              const completedBaseMs = entries.reduce(
                (sum, e) => sum + (e.durationMs ?? 0),
                0
              );
              const currentStartedAt =
                typeof job.progress?.currentStartedAt === 'number' && job.progress.currentStartedAt > 0
                  ? job.progress.currentStartedAt
                  : undefined;
              const isCurrentPath = (path: number[]) => {
                const currentPath = job.progress?.currentPath ?? [];
                return path.length === currentPath.length
                  && path.every((v: number, i: number) => v === currentPath[i]);
              };
              const appendEntry = (sessionId: string, path: number[]) => {
                const key = sessionEntryKey(sessionId, path);
                if (existingEntryKeys.has(key)) return;
                const status = deriveExtraSessionStatus(job.status);
                next.push({
                  sessionId,
                  path,
                  label: formatSessionPathLabel(path),
                  status,
                  startedAt: (() => {
                    if (status !== 'running') return undefined;
                    if (currentStartedAt != null && isCurrentPath(path)) return currentStartedAt;
                    if (typeof job.startedAt === 'number' && job.startedAt > 0) {
                      return job.startedAt + completedBaseMs;
                    }
                    return undefined;
                  })(),
                });
                existingEntryKeys.add(key);
              };
              const resumePath = job.resume?.nextPath ?? [];
              const hasResumePath = resumePath.length > 0 && !!job.resume?.sessionId;
              if (hasResumePath && job.resume?.sessionId) {
                // A stopped/failed round can share the same sessionId with earlier
                // completed rounds. Initial hydration must therefore append by the
                // full sessionId+path key, not only by sessionId, otherwise the
                // interrupted round disappears after refresh.
                appendEntry(job.resume.sessionId, resumePath);
                existingSessionIds.add(job.resume.sessionId);
              }
              if (!job.sessionIds?.length) return next;
              for (const sid of job.sessionIds) {
                if (existingSessionIds.has(sid)) continue;
                const path = job.resume?.sessionId === sid && resumePath.length > 0
                  ? resumePath
                  : (job.progress?.currentPath ?? []);
                appendEntry(sid, path);
                existingSessionIds.add(sid);
              }
              return next;
            };
            // Rebuild session entries from existing results
            if (job.progress.results?.length > 0) {
              let entries: LoopSessionEntry[] = job.progress.results.map((r: { path: number[]; sessionId: string; success: boolean; durationMs: number; tokens: number; error?: string }) => ({
                sessionId: r.sessionId,
                path: r.path || [],
                label: (r.path || []).map((p: number) => p + 1).join('.'),
                status: r.success ? 'completed' as const : 'failed' as const,
                durationMs: r.durationMs,
                tokens: r.tokens,
                error: r.error,
              }));
              setLoopSessions(entries);
              // Select initialSessionId if provided and valid, otherwise last session
              if (entries.length > 0) {
                const targetSid = initialSessionId && entries.some(e => e.sessionId === initialSessionId)
                  ? initialSessionId
                  : entries[entries.length - 1].sessionId;
                applyActiveSessionSelection(targetSid, targetSid === getLastLoopSessionId(entries));
              }

              const resultSessionIds = new Set(entries.map((e: LoopSessionEntry) => e.sessionId));
              const nextEntries = appendMissingSessionEntries(entries, resultSessionIds);
              if (nextEntries.length !== entries.length) {
                entries = nextEntries;
                setLoopSessions([...entries]);
                const targetSid2 = initialSessionId && entries.some(e => e.sessionId === initialSessionId)
                  ? initialSessionId
                  : entries[entries.length - 1].sessionId;
                applyActiveSessionSelection(targetSid2, targetSid2 === getLastLoopSessionId(entries));
              }
              const entrySessionIds = new Set(entries.map((e: LoopSessionEntry) => e.sessionId));

              // Mark completed/stopped/failed sessions as ended
              if (job.status !== 'running') {
                setEndedSessionIds(entrySessionIds);
              } else {
                // Only mark sessions that have results as ended
                const endedIds = new Set<string>(
                  job.progress.results.map((r: { sessionId: string }) => r.sessionId)
                );
                setEndedSessionIds(endedIds);
              }

              // Load messages: active session first, then rest in parallel
              const uniqueSessionIds = [...entrySessionIds];
              const activeSid = (initialSessionId && uniqueSessionIds.includes(initialSessionId))
                ? initialSessionId
                : entries[entries.length - 1]?.sessionId;

              // Step 1: Load active session first to unblock UI
              if (activeSid && !cancelled) {
                const activeMessages = await loadHistory(activeSid, activeSid);
                if (!cancelled) {
                  setMessages((prev) => {
                    if (prev.length === 0) return activeMessages;
                    return mergeMessages(prev, activeMessages);
                  });
                  setLoadedSessionIds(new Set([activeSid]));
                  setIsLoadingHistory(false); // Unblock UI after active session loads
                }
              }

              // Step 2: Warm remaining sessions in the background at idle
              // priority. They load lazily on tab switch (load-on-switch
              // effect); this prefetch just gets them into memory eventually
              // for smooth switching without competing with the first paint.
              const remainingIds = uniqueSessionIds.filter(id => id !== activeSid);
              if (remainingIds.length > 0 && !cancelled) {
                cancelIdlePrefetch = idlePrefetchSessions(
                  remainingIds,
                  async (sid) => {
                    if (cancelled || loadedSessionIdsRef.current.has(sid)) return;
                    let msgs: Message[];
                    try {
                      msgs = await loadHistory(sid, sid);
                    } catch (err) {
                      console.error(`[hydration] Failed to prefetch session ${sid}:`, err);
                      // Record failure for retry-on-switch instead of marking loaded with empty content.
                      failedSessionIdsRef.current = new Set([...failedSessionIdsRef.current, sid]);
                      return;
                    }
                    if (cancelled) return;
                    if (msgs.length > 0) {
                      setMessages((prev) => mergeMessages(prev, msgs));
                    }
                    // Mark loaded even when empty so switching to this tab
                    // shows an empty chat instead of an infinite spinner.
                    setLoadedSessionIds((prev) => new Set([...prev, sid]));
                  },
                  () => cancelled,
                );
              }
            }

            // Handle case: session exists but no iteration result has been recorded yet.
            if (!cancelled && !job.progress?.results?.length && job.sessionIds?.length > 0) {
              const hydratedStatus = deriveExtraSessionStatus(job.status);
              const hydratedStartedAt =
                hydratedStatus === 'running' && typeof job.progress?.currentStartedAt === 'number' && job.progress.currentStartedAt > 0
                  ? job.progress.currentStartedAt
                  : hydratedStatus === 'running' && typeof job.startedAt === 'number' && job.startedAt > 0
                    ? job.startedAt
                    : undefined;
              const entries: LoopSessionEntry[] = job.sessionIds.map((sid: string) => ({
                sessionId: sid,
                path: job.resume?.sessionId === sid && (job.resume?.nextPath?.length ?? 0) > 0
                  ? job.resume.nextPath
                  : (job.progress?.currentPath ?? []),
                label: (job.resume?.sessionId === sid && (job.resume?.nextPath?.length ?? 0) > 0
                  ? job.resume.nextPath
                  : (job.progress?.currentPath ?? [])).map((p: number) => p + 1).join('.') || '-',
                status: hydratedStatus,
                startedAt: hydratedStartedAt,
              }));
              if (entries.length > 0) {
                setLoopSessions(entries);
                if (job.status !== 'running') {
                  setEndedSessionIds(new Set(job.sessionIds));
                }
                const targetSid3 = initialSessionId && entries.some(e => e.sessionId === initialSessionId)
                  ? initialSessionId
                  : entries[entries.length - 1].sessionId;
                applyActiveSessionSelection(targetSid3, targetSid3 === getLastLoopSessionId(entries));

                // Load messages: active session first, then rest in parallel
                const activeSid = targetSid3;

                // Step 1: Load active session first to unblock UI
                if (!cancelled) {
                  const activeMessages = await loadHistory(activeSid, activeSid);
                  if (!cancelled) {
                    setMessages((prev) => {
                      if (prev.length === 0) return activeMessages;
                      return mergeMessages(prev, activeMessages);
                    });
                    setLoadedSessionIds(new Set([activeSid]));
                    setIsLoadingHistory(false); // Unblock UI after active session loads
                  }
                }

                // Step 2: Warm remaining sessions in the background at idle
                // priority (lazy on tab switch; this just pre-warms memory).
                const remainingIds = job.sessionIds.filter((sid: string) => sid !== activeSid);
                if (remainingIds.length > 0 && !cancelled) {
                  cancelIdlePrefetch = idlePrefetchSessions(
                    remainingIds,
                    async (sid) => {
                      if (cancelled || loadedSessionIdsRef.current.has(sid)) return;
                      let msgs: Message[];
                      try {
                        msgs = await loadHistory(sid, sid);
                      } catch (err) {
                        console.error(`[hydration] Failed to prefetch session ${sid}:`, err);
                        failedSessionIdsRef.current = new Set([...failedSessionIdsRef.current, sid]);
                        return;
                      }
                      if (cancelled) return;
                      if (msgs.length > 0) {
                        setMessages((prev) => mergeMessages(prev, msgs));
                      }
                      // Mark loaded even when empty so switching to this tab
                      // shows an empty chat instead of an infinite spinner.
                      setLoadedSessionIds((prev) => new Set([...prev, sid]));
                    },
                    () => cancelled,
                  );
                }
              }
            }
          }
          if (!cancelled) {
            setLoopStatus((prev) => {
              // Don't overwrite a terminal status that SSE may have already set
              if (prev === 'completed' || prev === 'stopped' || prev === 'failed') return prev;
              return job.status === 'running' ? 'running' : job.status === 'completed' ? 'completed' : job.status === 'stopped' ? 'stopped' : job.status === 'failed' ? 'failed' : 'idle';
            });
          }
        } else if (job.mode === 'graph') {
          setIsLoop(false);
          isLoopRef.current = false;
          setIsGraph(true);
          const runId = typeof job.graphRunId === 'string' && job.graphRunId ? job.graphRunId : null;
          setGraphRunId(runId);
          setStopPending(false);
          // Drive the session-sidebar header status off the Job status, the same
          // way loop mode maps it (graph control lives in GraphLoopProgress).
          setLoopStatus(job.status === 'running' ? 'running' : job.status === 'completed' ? 'completed' : job.status === 'stopped' ? 'stopped' : job.status === 'failed' ? 'failed' : 'idle');

          // Graph node sessions are ordinary sessions. Surface them in the same
          // session sidebar + MessageList loop mode uses by deriving loop-style
          // entries from the run's executed instances, then hydrating the active
          // session's history (others prefetched at idle). The mini canvas and
          // GraphLoopProgress carry the live run visualization; here we only
          // populate the per-node conversation view.
          if (runId && !cancelled) {
            let graphInstances: GraphInstanceState[] = [];
            try {
              const runRes = await fetch(apiUrl(`/graph/run/${encodeURIComponent(runId)}`));
              if (runRes.ok) {
                const runData = (await runRes.json()) as GraphRunStatusResponse;
                graphInstances = runData.instances || [];
              }
            } catch (err) {
              console.error(`[hydration] Failed to load graph run ${runId}:`, err);
            }
            if (!cancelled) {
              const entries = graphSessionEntries(graphInstances);
              if (entries.length > 0) {
                setLoopSessions(entries);
                if (job.status !== 'running') {
                  setEndedSessionIds(new Set(entries.map((e) => e.sessionId)));
                }
                const runningInst = graphInstances.find((i) => i.status === 'running' && i.sessionId);
                const activeSid = initialSessionId && entries.some((e) => e.sessionId === initialSessionId)
                  ? initialSessionId
                  : (runningInst?.sessionId ?? entries[entries.length - 1].sessionId);
                applyActiveSessionSelection(activeSid, activeSid === getLastLoopSessionId(entries));

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
          setIsLoop(false);
          isLoopRef.current = false;
          setIsGraph(false);
          setGraphRunId(null);
          // Non-loop job: load history for all sessions (may have multiple
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
                // semantic dedup (optimistic user messages, loop-user
                // messages, and pure thought bubbles whose live id diverged
                // from the persisted thought_msg_id) as every other history
                // merge. A hand-rolled id-only filter here would miss thought
                // bubbles and reintroduce duplicate thinking bubbles.
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
  }, [existingJobId, initialSessionId, apiUrl, loadHistory, applyActiveSessionSelection, setLoopSessions, seedServerClockFromResponse]);

  // When the active session changes in loop mode, update session-level metadata
  // so ChatInput/MessageList reflect the session's agent/model.
  // Also re-run when loadedSessionIds changes: if a session was selected before
  // its history finished loading, sessionMetaMapRef had no data and the effect
  // returned early. Re-running when the session finishes loading picks up the
  // newly available metadata.
  useEffect(() => {
    if ((!isLoop && !isGraph) || !activeSessionId) return;
    const meta = sessionMetaMapRef.current.get(activeSessionId);
    if (!meta) return;
    if (meta.modelId != null) setSessionModelId(meta.modelId);
    if (meta.type != null) setSessionType(meta.type);
    setSessionACPMode(meta.acpMode);
    setSessionACPThoughtLevel(meta.acpThoughtLevel);
  }, [isLoop, isGraph, activeSessionId, loadedSessionIds]);

  // Load-on-switch: when the user selects a loop / graph session whose history
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
    if ((!isLoop && !isGraph) || !activeSessionId) return;
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
  }, [isLoop, isGraph, activeSessionId, loadedSessionIds, loadHistory]);

  // Retry loading a session that failed during background hydration when the
  // user switches to it. Without this, the session would appear as a blank
  // chat with no error indicator and no way to recover.
  useEffect(() => {
    if ((!isLoop && !isGraph) || !activeSessionId) return;
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
  }, [isLoop, isGraph, activeSessionId, loadedSessionIds]);

  // Defensive dedup at the aggregation point. The messages array is written
  // by several paths (SSE live events, initial history load, reconnect merge,
  // loop Step1+Step2 parallel session merges). Each path has its own dedup,
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
    messages: (isLoop || isGraph) ? filteredMessages : dedupedMessages,
    allMessages: dedupedMessages,
    isLoading,
    isLoadingHistory,
    error,
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
    // Loop state
    isLoop,
    isGraph,
    graphRunId,
    loopProgress,
    loopStatus,
    stopPending,
    loopFlow,
    loopVariables,
    loopDisabledVars,
    loopSessions,
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
    startLoop,
    continueLoop,
    stopLoop,
    cancelStop,
    updateLoopConfig,
    stopGeneration,
    clearMessages,
    eventsReady,
    isPublic,
  };
}
