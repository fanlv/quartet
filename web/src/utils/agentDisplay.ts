import { useEffect, useSyncExternalStore } from 'react';

// Minimal display projection of an Agent record, mirrored from the backend
// model.AgentDisplayInfo. Resolved on demand for historical references (old
// session serve commands, graph node snapshot agent types) so history keeps
// rendering after an Agent is renamed or deleted.
export interface AgentDisplayInfo {
  agentId: string;
  displayName: string;
  iconUrl: string;
  deleted: boolean;
}

// null means "resolved, but no catalog record matched" — the reference is an
// unknown Agent. undefined (absent from the map) means "not resolved yet".
type CacheValue = AgentDisplayInfo | null;

const cache = new Map<string, CacheValue>();
const queued = new Set<string>();
const inFlight = new Set<string>();
const listeners = new Set<() => void>();
let flushTimer: ReturnType<typeof setTimeout> | null = null;
let version = 0;

function notify() {
  version += 1;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function getVersion(): number {
  return version;
}

async function flushQueue(): Promise<void> {
  const ids = [...queued].filter((id) => !inFlight.has(id));
  if (ids.length === 0) return;
  for (const id of ids) {
    queued.delete(id);
    inFlight.add(id);
  }
  try {
    const res = await fetch('/api/v1/agent/display-info/resolve', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids }),
    });
    const data = res.ok ? await res.json().catch(() => null) : null;
    const agents = (data?.agents ?? {}) as Record<string, AgentDisplayInfo>;
    for (const id of ids) {
      cache.set(id, agents[id] ?? null);
    }
  } catch (err) {
    // Leave the ids unresolved so the next ensure retries; the UI keeps its
    // previous fallback instead of sticking on a false "unknown Agent".
    console.warn('[agentDisplay] resolve failed:', err);
  } finally {
    for (const id of ids) inFlight.delete(id);
    notify();
  }
}

// ensureAgentDisplays schedules private-mode resolution for references that
// are neither cached nor in flight. Calls are cheap: batching collapses a
// render pass into one POST.
export function ensureAgentDisplays(refs: Array<string | null | undefined>, force = false): void {
  let added = false;
  for (const raw of refs) {
    const ref = raw?.trim();
    if (!ref || (!force && cache.has(ref)) || inFlight.has(ref) || queued.has(ref)) continue;
    if (force) cache.delete(ref);
    queued.add(ref);
    added = true;
  }
  if (!added || flushTimer) return;
  flushTimer = setTimeout(() => {
    flushTimer = null;
    void flushQueue();
  }, 50);
}

// peekAgentDisplay returns the cached resolution for a reference:
// undefined = not resolved yet, null = resolved to no record (unknown Agent).
export function peekAgentDisplay(ref: string | null | undefined): CacheValue | undefined {
  if (!ref) return undefined;
  return cache.get(ref);
}

// primeAgentDisplays fills the cache from a public share response's `agents`
// map. Share pages never call the private resolve endpoint; whatever the
// public payload omits is treated as an unknown Agent.
export function primeAgentDisplays(agents: Record<string, AgentDisplayInfo> | null | undefined): void {
  if (!agents) return;
  let changed = false;
  for (const [ref, info] of Object.entries(agents)) {
    if (!info) continue;
    const prev = cache.get(ref);
    if (prev && prev.agentId === info.agentId && prev.displayName === info.displayName && prev.iconUrl === info.iconUrl && prev.deleted === info.deleted) continue;
    cache.set(ref, info);
    changed = true;
  }
  if (changed) notify();
}

// markAgentDisplayUnknown records that a public share response carried no
// record for this reference. Only meaningful in share mode, where absence
// from the payload is authoritative (the share client has no other source).
export function markAgentDisplayUnknown(ref: string | null | undefined, force = false): void {
  if (!ref || (!force && cache.has(ref))) return;
  cache.set(ref, null);
  notify();
}

export function invalidateAgentDisplays(refs?: Array<string | null | undefined>): void {
  if (!refs) {
    cache.clear();
    notify();
    return;
  }
  let changed = false;
  for (const ref of refs) {
    if (ref && cache.delete(ref)) changed = true;
  }
  if (changed) notify();
}

// useAgentDisplay resolves one historical reference. In share mode
// (resolve=false) it only reads the primed cache; privately it schedules a
// batched resolve on miss.
export function useAgentDisplay(ref: string | null | undefined, resolve: boolean): CacheValue | undefined {
  useSyncExternalStore(subscribe, getVersion);
  useEffect(() => {
    if (resolve && ref && !cache.has(ref)) ensureAgentDisplays([ref]);
  }, [ref, resolve]);
  return peekAgentDisplay(ref);
}

// useAgentDisplayVersion re-renders the caller whenever any resolution lands.
// Used by components (JobChat) that resolve references for many sessions
// through plain callbacks instead of one hook per reference.
export function useAgentDisplayVersion(): number {
  return useSyncExternalStore(subscribe, getVersion);
}
