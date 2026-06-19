import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type {
  GraphEdgeState,
  GraphEvent,
  GraphInstanceState,
  GraphProgress,
  GraphRun,
  GraphRunStatus,
  GraphRunStatusResponse,
} from '../types';
import { SSEClient } from '../utils/sse-client';
import { GraphCanvas } from './graph/GraphCanvas';
import { configToFlow, edgeStatusByEdge, runConfigSnapshot, runStatusByNode } from './graph/graphFlowAdapter';
import './GraphLoopProgress.css';

// The embedded mini canvas only visualizes run state; every edit affordance is
// inert (readOnly already disables interaction), so all handlers are no-ops.
const NOOP = () => {};

interface GraphLoopProgressProps {
  runId: string | null;
  readOnly?: boolean;
  onEdit?: () => void;
}

const LIVE_STATUSES = new Set<GraphRunStatus>(['pending', 'running', 'pausing', 'stepStopping', 'recovering']);
const RESUMABLE_STATUSES = new Set<GraphRunStatus>(['failed', 'paused', 'stepStopped', 'stopped', 'timedOut']);

async function readGraphError(response: Response, prefix?: string): Promise<string> {
  const body = await response.text().catch(() => '');
  const trimmed = body.trim();
  let detail = trimmed;
  if (trimmed) {
    try {
      const parsed = JSON.parse(trimmed);
      if (Array.isArray(parsed?.errors) && parsed.errors.length > 0) {
        detail = parsed.errors.map((err: { message?: string; nodeId?: string; edgeId?: string; variable?: string; configKey?: string }) => {
          const loc = [err.nodeId, err.edgeId, err.variable, err.configKey].filter(Boolean).join(', ');
          return loc ? `${err.message || 'validation error'} (${loc})` : (err.message || 'validation error');
        }).join('\n');
      } else if (typeof parsed?.msg === 'string') detail = parsed.msg;
      else if (typeof parsed?.error === 'string') detail = parsed.error;
      else if (typeof parsed?.message === 'string') detail = parsed.message;
    } catch {
      // Keep raw non-JSON bodies.
    }
  }
  const message = detail ? `HTTP ${response.status}: ${detail}` : `HTTP ${response.status}`;
  return prefix ? `${prefix}: ${message}` : message;
}

function statusLabel(t: TFunction, status?: GraphRunStatus): string {
  switch (status) {
    case 'pending': return t('graph.status.pending');
    case 'running': return t('graph.status.running');
    case 'completed': return t('graph.status.completed');
    case 'failed': return t('graph.status.failed');
    case 'pausing': return t('graph.status.pausing');
    case 'paused': return t('graph.status.paused');
    case 'stepStopping': return t('graph.status.stepStopping');
    case 'stepStopped': return t('graph.status.stepStopped');
    case 'stopped': return t('graph.status.stopped');
    case 'timedOut': return t('graph.status.timedOut');
    case 'recovering': return t('graph.status.recovering');
    default: return t('graph.status.notLoaded');
  }
}

export function GraphLoopProgress({ runId, readOnly, onEdit }: GraphLoopProgressProps) {
  const { t } = useTranslation();
  const [run, setRun] = useState<GraphRun | null>(null);
  const [progress, setProgress] = useState<GraphProgress | null>(null);
  const [instances, setInstances] = useState<GraphInstanceState[]>([]);
  const [edges, setEdges] = useState<GraphEdgeState[]>([]);
  const [loading, setLoading] = useState(false);
  const [actionPending, setActionPending] = useState<string | null>(null);
  const [error, setError] = useState('');
  // Detail body (progress bar, stats, canvas) is collapsed by default;
  // only the header row stays visible until the user expands it.
  const [expanded, setExpanded] = useState(false);
  const sseRef = useRef<SSEClient | null>(null);
  const eventLineRef = useRef(0);

  const isLive = !!run?.status && LIVE_STATUSES.has(run.status);
  const canPause = !readOnly && run?.status === 'running';
  const canStepStop = !readOnly && run?.status === 'running';
  const canStop = !readOnly && !!run?.status && LIVE_STATUSES.has(run.status);
  const canResume = !readOnly && !!run?.status && RESUMABLE_STATUSES.has(run.status);

  const toggleExpanded = useCallback(() => {
    setExpanded((v) => !v);
  }, []);

  const handleProgressClick = useCallback((event: MouseEvent<HTMLDivElement>) => {
    const target = event.target;
    if (target instanceof Element && target.closest('button, a, input, textarea, select, [role="button"]')) {
      return;
    }
    toggleExpanded();
  }, [toggleExpanded]);

  const refresh = useCallback(async () => {
    if (!runId) {
      setRun(null);
      setProgress(null);
      setInstances([]);
      setEdges([]);
      return;
    }
    setLoading(true);
    try {
      const res = await fetch(`/api/v1/graph/run/${encodeURIComponent(runId)}`);
      if (!res.ok) throw new Error(await readGraphError(res, `GET /graph/run/${runId}`));
      const data = await res.json() as GraphRunStatusResponse;
      setRun(data.run || null);
      setProgress(data.progress || data.run?.progress || null);
      setInstances(data.instances || []);
      setEdges(data.edges || []);
      eventLineRef.current = data.events?.length || 0;
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [runId]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => () => {
    sseRef.current?.disconnect();
    sseRef.current = null;
  }, []);

  useEffect(() => {
    sseRef.current?.disconnect();
    sseRef.current = null;
    if (!runId || !isLive) return;

    const client = new SSEClient();
    sseRef.current = client;
    let closed = false;
    void client.connectUntilReady({
      url: `/api/v1/graph/run/${encodeURIComponent(runId)}/events`,
      initialLastEventId: String(eventLineRef.current),
      onEvent: (raw) => {
        const event = raw as unknown as GraphEvent;
        eventLineRef.current += 1;
        if (event.progress) setProgress(event.progress);
        if (event.type === 'progressUpdated' || event.type === 'error' || event.type === 'instanceCompleted' || event.type === 'instanceFailed') {
          void refresh();
        }
      },
      onError: (err) => setError(err.message),
      onResumePointGone: () => void refresh(),
    }).catch((err) => {
      if (!closed) setError(err instanceof Error ? err.message : String(err));
    });

    return () => {
      closed = true;
      client.disconnect();
      if (sseRef.current === client) sseRef.current = null;
    };
  }, [isLive, refresh, runId]);

  const doAction = useCallback(async (action: 'pause' | 'step-stop' | 'stop' | 'resume') => {
    if (!runId) return;
    setActionPending(action);
    setError('');
    try {
      const res = await fetch(`/api/v1/graph/run/${encodeURIComponent(runId)}/${action}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      });
      if (!res.ok) throw new Error(await readGraphError(res, `POST /graph/run/${runId}/${action}`));
      const data = await res.json().catch(() => null) as { run?: GraphRun } | null;
      if (data?.run) {
        setRun(data.run);
        setProgress(data.run.progress || progress);
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setActionPending(null);
    }
  }, [progress, refresh, runId]);

  const percent = useMemo(() => {
    if (!progress) return 0;
    if (progress.totalCount <= 0) return run?.status === 'completed' ? 100 : 0;
    return Math.min(100, Math.round((progress.completedCount / progress.totalCount) * 100));
  }, [progress, run?.status]);

  // Mini read-only canvas inputs derived from the run we already fetch: the
  // executed config (versioned snapshot), per-node run status, edge resolution
  // state and failed nodes for the error outline.
  const miniFlow = useMemo(() => (run ? configToFlow(runConfigSnapshot(run)) : { nodes: [], edges: [] }), [run]);
  const miniRunStatus = useMemo(() => runStatusByNode(instances), [instances]);
  const miniEdgeStatus = useMemo(() => edgeStatusByEdge(edges), [edges]);
  const miniErrorNodeIds = useMemo(
    () => new Set(instances.filter((inst) => inst.status === 'failed').map((inst) => inst.nodeId)),
    [instances],
  );

  const lastError = run?.lastError?.message || progress?.lastError || error;
  const doneText = progress
    ? t('graph.loop.done', { count: progress.completedCount, total: progress.totalCount })
    : t('graph.progress.none');

  if (!runId) {
    return (
      <div className="graph-loop-progress" data-testid="graph-loop-progress">
        <div className="graph-loop-empty">{t('graph.loop.notBound')}</div>
      </div>
    );
  }

  return (
    <div
      className="graph-loop-progress"
      data-testid="graph-loop-progress"
      data-graph-status={run?.status || 'loading'}
      onClick={handleProgressClick}
    >
      <div className="graph-loop-header">
        <div className="graph-loop-title">
          <span className={`graph-loop-status status-${run?.status || 'loading'}`}>{loading ? t('graph.status.loading') : statusLabel(t, run?.status)}</span>
          <span className="graph-loop-info">
            <span className="graph-loop-done">{doneText}</span>
            <span>{t('graph.loop.running', { count: progress?.runningCount ?? 0 })}</span>
            <span>{t('graph.loop.failed', { count: progress?.failedCount ?? 0 })}</span>
            <span>{t('graph.loop.skipped', { count: progress?.skippedCount ?? 0 })}</span>
          </span>
        </div>
        <div className="graph-loop-actions" aria-label={t('graph.loop.controls')}>
          <button
            type="button"
            className="graph-loop-action"
            onClick={toggleExpanded}
            aria-expanded={expanded}
            title={expanded ? t('graph.loop.collapse') : t('graph.loop.expand')}
          >
            <GraphActionIcon type={expanded ? 'collapse' : 'expand'} />
            <span>{expanded ? t('graph.loop.collapse') : t('graph.loop.expand')}</span>
          </button>
          {onEdit && (
            <button type="button" className="graph-loop-action" onClick={onEdit} disabled={!!actionPending} title={t('graph.loop.editTitle')}>
              <GraphActionIcon type="edit" />
              <span>{t('graph.loop.edit')}</span>
            </button>
          )}
          <button type="button" className="graph-loop-action warn" onClick={() => void doAction('pause')} disabled={!canPause || !!actionPending} title={t('graph.loop.pauseTitle')}>
            <GraphActionIcon type="pause" />
            <span>{t('graph.loop.pause')}</span>
          </button>
          <button type="button" className="graph-loop-action warn" onClick={() => void doAction('step-stop')} disabled={!canStepStop || !!actionPending} title={t('graph.loop.stepStopTitle')}>
            <GraphActionIcon type="step" />
            <span>{t('graph.loop.stepStop')}</span>
          </button>
          <button type="button" className="graph-loop-action danger" onClick={() => void doAction('stop')} disabled={!canStop || !!actionPending} title={t('graph.loop.stopTitle')}>
            <GraphActionIcon type="stop" />
            <span>{t('graph.loop.stop')}</span>
          </button>
          <button type="button" className="graph-loop-action primary" onClick={() => void doAction('resume')} disabled={!canResume || !!actionPending} title={t('graph.loop.resumeTitle')}>
            <GraphActionIcon type="resume" />
            <span>{t('graph.loop.resume')}</span>
          </button>
        </div>
      </div>

      <div className="graph-loop-bar-wrapper">
        <div className={`graph-loop-bar status-${run?.status || 'loading'}`} style={{ width: `${percent}%` }} />
      </div>

      {expanded && (
        <>
          <div className="graph-loop-stats">
            <span>{t('graph.loop.done', { count: progress?.completedCount ?? 0, total: progress?.totalCount ?? 0 })}</span>
            <span>{t('graph.loop.running', { count: progress?.runningCount ?? 0 })}</span>
            <span>{t('graph.loop.failed', { count: progress?.failedCount ?? 0 })}</span>
            <span>{t('graph.loop.skipped', { count: progress?.skippedCount ?? 0 })}</span>
            <span>{t('graph.loop.interrupted', { count: progress?.interruptedCount ?? 0 })}</span>
          </div>

          <div className="graph-loop-canvas" data-testid="graph-loop-canvas">
            <GraphCanvas
              nodes={miniFlow.nodes}
              edges={miniFlow.edges}
              readOnly
              showMiniMap={false}
              runStatusByNodeId={miniRunStatus}
              edgeStatusById={miniEdgeStatus}
              errorNodeIds={miniErrorNodeIds}
              onNodesChange={NOOP}
              onEdgesChange={NOOP}
              onConnect={NOOP}
              onNodeClick={NOOP}
              onPaneClick={NOOP}
              onAddNode={NOOP}
            />
            <div className="graph-loop-legend" aria-label={t('graph.loop.legendTitle')}>
              <span className="graph-loop-legend-title">{t('graph.loop.legendTitle')}</span>
              <span className="graph-loop-legend-item">
                <i className="graph-loop-legend-line done" />
                {t('graph.loop.legendDone')}
              </span>
              <span className="graph-loop-legend-item">
                <i className="graph-loop-legend-line flowing" />
                {t('graph.loop.legendFlowing')}
              </span>
              <span className="graph-loop-legend-item">
                <i className="graph-loop-legend-line pending" />
                {t('graph.loop.legendPending')}
              </span>
              <span className="graph-loop-legend-item">
                <i className="graph-loop-legend-line pruned" />
                {t('graph.loop.legendPruned')}
              </span>
            </div>
          </div>

          {lastError && (
            <pre className="graph-loop-error" data-testid="graph-loop-error">{lastError}</pre>
          )}
        </>
      )}
    </div>
  );
}

function GraphActionIcon({ type }: { type: 'edit' | 'pause' | 'step' | 'stop' | 'resume' | 'expand' | 'collapse' }) {
  if (type === 'expand') {
    return <svg viewBox="0 0 24 24"><path d="m6 9 6 6 6-6" /></svg>;
  }
  if (type === 'collapse') {
    return <svg viewBox="0 0 24 24"><path d="m6 15 6-6 6 6" /></svg>;
  }
  if (type === 'edit') {
    return <svg viewBox="0 0 24 24"><path d="M4 20h4.5L19 9.5a2.1 2.1 0 0 0 0-3L17.5 5a2.1 2.1 0 0 0-3 0L4 15.5V20Z" /><path d="M13.5 6 18 10.5" /></svg>;
  }
  if (type === 'pause') {
    return <svg viewBox="0 0 24 24"><path d="M8 5v14" /><path d="M16 5v14" /></svg>;
  }
  if (type === 'step') {
    return <svg viewBox="0 0 24 24"><path d="M6 5v14" /><path d="M10 7h3a4 4 0 0 1 0 8h-3" /><path d="m17 17 2 2 3-4" /></svg>;
  }
  if (type === 'stop') {
    return <svg viewBox="0 0 24 24"><rect x="7" y="7" width="10" height="10" rx="2" /></svg>;
  }
  return <svg viewBox="0 0 24 24"><path d="M5 12a7 7 0 0 1 12-5l2 2" /><path d="M19 5v4h-4" /><path d="M19 12a7 7 0 0 1-12 5l-2-2" /><path d="M5 19v-4h4" /></svg>;
}
