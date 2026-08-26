import { useMemo, useEffect, useRef, useCallback } from 'react';
import { GraphSessionEntry } from '../hooks/useJobChat';
import { DurationBadge } from './DurationBadge';
import './GraphSessionSidebar.css';

interface SessionGroup {
  sessionId: string;
  entries: GraphSessionEntry[];
  // 'interrupted' = at least one Graph node execution did not finish.
  status: 'running' | 'completed' | 'failed' | 'interrupted';
  totalDurationMs: number;
  /** startedAt list for currently running entries, used with totalDurationMs to keep the metric consistent. */
  runningStartedAts: number[];
}

interface GraphSessionSidebarProps {
  sessions: GraphSessionEntry[];
  status?: 'idle' | 'running' | 'completed' | 'stopped' | 'failed';
  activeSessionId: string | null;
  onSelectSession: (sessionId: string) => void;
}

export function GraphSessionSidebar({ sessions, status = 'idle', activeSessionId, onSelectSession }: GraphSessionSidebarProps) {
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
          runningStartedAts: [],
        };
        map.set(s.sessionId, group);
      }
      group.entries.push(s);
      if (s.durationMs != null) {
        group.totalDurationMs += s.durationMs;
      }
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
    switch (status) {
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
  }, [status]);

  return (
    <div className="graph-session-sidebar" data-testid="graph-session-sidebar" data-graph-status={status}>
      <div className="graph-session-sidebar-header">
        <span className="graph-session-sidebar-title">Sessions</span>
        <span className="graph-session-sidebar-count">{groups.length}</span>
        {(aggregateDuration.totalMs > 0 || aggregateDuration.runningStartedAts.length > 0) && (
          <DurationBadge
            startedAt={aggregateDuration.runningStartedAts}
            baseMs={aggregateDuration.totalMs}
            variant="total"
          />
        )}
      </div>
      <div className="graph-session-sidebar-list" data-testid="graph-session-list">
        {groups.map((g, idx) => (
          <div
            key={g.sessionId}
            ref={g.sessionId === activeSessionId ? activeItemRef : undefined}
            className={`graph-session-sidebar-item ${g.sessionId === activeSessionId ? 'active' : ''} ${g.status}`}
            data-testid="graph-session-item"
            data-session-id={g.sessionId}
            data-session-status={g.status}
            data-active={g.sessionId === activeSessionId ? 'true' : 'false'}
            onClick={() => onSelectSession(g.sessionId)}
          >
            <div className="graph-session-sidebar-item-header">
              <span className="graph-session-sidebar-item-icon">
                {g.status === 'running' ? '⏳'
                  : g.status === 'completed' ? '✓'
                  : g.status === 'interrupted' ? '⏸'
                  : '✗'}
              </span>
              <span className="graph-session-sidebar-item-label">
                Session #{idx + 1}
              </span>
              <span className="graph-session-sidebar-item-rounds">
                {g.entries.length} round{g.entries.length !== 1 ? 's' : ''}
              </span>
            </div>
            <div className="graph-session-sidebar-item-meta">
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
            </div>
          </div>
        ))}
        {groups.length === 0 && (
          <div className="graph-session-sidebar-empty" data-testid="graph-session-empty">{emptyText}</div>
        )}
      </div>
    </div>
  );
}
