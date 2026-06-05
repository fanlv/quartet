import { useMemo, useEffect, useRef, useCallback } from 'react';
import { LoopSessionEntry } from '../hooks/useJobChat';
import { DurationBadge } from './DurationBadge';
import './LoopSessionSidebar.css';

interface SessionGroup {
  sessionId: string;
  entries: LoopSessionEntry[];
  // 'interrupted' = at least one entry was still running when the job
  // was stopped; Resume.NextPath is preserved backend-side so Continue
  // can re-run it. Visually distinct from 'completed'.
  status: 'running' | 'completed' | 'failed' | 'interrupted';
  totalDurationMs: number;
  totalTokens: number;
  /** startedAt list for currently running entries, used with totalDurationMs to keep the metric consistent. */
  runningStartedAts: number[];
}

interface LoopSessionSidebarProps {
  sessions: LoopSessionEntry[];
  loopStatus?: 'idle' | 'running' | 'completed' | 'stopped' | 'failed';
  activeSessionId: string | null;
  onSelectSession: (sessionId: string) => void;
}

export function LoopSessionSidebar({ sessions, loopStatus = 'idle', activeSessionId, onSelectSession }: LoopSessionSidebarProps) {
  const activeRef = useRef<HTMLDivElement | null>(null);
  const scrolledRef = useRef(false);

  // Scroll active session into view on first render / when activeSessionId changes
  useEffect(() => {
    scrolledRef.current = false;
  }, [activeSessionId]);

  const activeItemRef = useCallback((node: HTMLDivElement | null) => {
    activeRef.current = node;
    if (node && !scrolledRef.current) {
      scrolledRef.current = true;
      node.scrollIntoView({ block: 'nearest' });
    }
  }, []);

  const groups = useMemo(() => {
    const map = new Map<string, SessionGroup>();
    for (const s of sessions) {
      let group = map.get(s.sessionId);
      if (!group) {
        group = {
          sessionId: s.sessionId,
          entries: [],
          status: 'completed',
          totalDurationMs: 0,
          totalTokens: 0,
          runningStartedAts: [],
        };
        map.set(s.sessionId, group);
      }
      group.entries.push(s);
      if (s.durationMs != null) {
        group.totalDurationMs += s.durationMs;
      }
      if (s.tokens != null) group.totalTokens += s.tokens;
      // Track running entries so the live duration can be computed as
      // completed execution time + in-flight execution time.
      if (s.status === 'running' && s.startedAt != null) {
        group.runningStartedAts.push(s.startedAt);
      }
      // Status priority: running > failed > interrupted > completed.
      if (s.status === 'running') {
        group.status = 'running';
      } else if (s.status === 'failed' && group.status !== 'running') {
        group.status = 'failed';
      } else if (s.status === 'interrupted' && group.status !== 'running' && group.status !== 'failed') {
        group.status = 'interrupted';
      }
    }
    return Array.from(map.values());
  }, [sessions]);

  // Compute aggregate duration for the sidebar header
  const aggregateDuration = useMemo(() => {
    let totalMs = 0;
    const runningStartedAts: number[] = [];
    for (const g of groups) {
      totalMs += g.totalDurationMs;
      runningStartedAts.push(...g.runningStartedAts);
    }
    return { totalMs, runningStartedAts };
  }, [groups]);

  const emptyText = useMemo(() => {
    switch (loopStatus) {
      case 'running':
        return 'Waiting for sessions...';
      case 'stopped':
        return 'Paused before any session started.';
      case 'failed':
        return 'Stopped before any session started.';
      case 'completed':
        return 'No sessions were recorded.';
      default:
        return 'No sessions yet.';
    }
  }, [loopStatus]);

  return (
    <div className="loop-sidebar" data-testid="loop-session-sidebar" data-loop-status={loopStatus}>
      <div className="loop-sidebar-header">
        <span className="loop-sidebar-title">Sessions</span>
        <span className="loop-sidebar-count">{groups.length}</span>
        {(aggregateDuration.totalMs > 0 || aggregateDuration.runningStartedAts.length > 0) && (
          <DurationBadge
            startedAt={aggregateDuration.runningStartedAts}
            baseMs={aggregateDuration.totalMs}
            variant="total"
          />
        )}
      </div>
      <div className="loop-sidebar-list" data-testid="loop-session-list">
        {groups.map((g, idx) => (
          <div
            key={g.sessionId}
            ref={g.sessionId === activeSessionId ? activeItemRef : undefined}
            className={`loop-sidebar-item ${g.sessionId === activeSessionId ? 'active' : ''} ${g.status}`}
            data-testid="loop-session-item"
            data-session-id={g.sessionId}
            data-session-status={g.status}
            data-active={g.sessionId === activeSessionId ? 'true' : 'false'}
            onClick={() => onSelectSession(g.sessionId)}
          >
            <div className="loop-sidebar-item-header">
              <span className="loop-sidebar-item-icon">
                {g.status === 'running' ? '⏳'
                  : g.status === 'completed' ? '✓'
                  : g.status === 'interrupted' ? '⏸'
                  : '✗'}
              </span>
              <span className="loop-sidebar-item-label">
                Session #{idx + 1}
              </span>
              <span className="loop-sidebar-item-rounds">
                {g.entries.length} round{g.entries.length !== 1 ? 's' : ''}
              </span>
            </div>
            <div className="loop-sidebar-item-meta">
              {(g.status === 'running' ? g.runningStartedAts.length > 0 || g.totalDurationMs > 0 : g.totalDurationMs > 0) && (
                <DurationBadge
                  // Keep a single <DurationBadge> instance across the
                  // running → completed transition so its internal
                  // monotonic-clamp ref survives. Switching to a plain
                  // <span> on completion would unmount the badge and drop
                  // the ref, re-introducing the visible "jump back" at the
                  // completion instant.
                  startedAt={g.status === 'running' ? g.runningStartedAts : undefined}
                  baseMs={g.totalDurationMs}
                  variant="total"
                />
              )}
              {g.totalTokens > 0 && (
                <span className="loop-sidebar-item-tokens">
                  {g.totalTokens >= 1000 ? `${(g.totalTokens / 1000).toFixed(1)}K` : g.totalTokens} tok
                </span>
              )}
            </div>
          </div>
        ))}
        {groups.length === 0 && (
          <div className="loop-sidebar-empty" data-testid="loop-session-empty">{emptyText}</div>
        )}
      </div>
    </div>
  );
}
