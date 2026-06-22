import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { applyNodeChanges, type NodeChange } from '@xyflow/react';
import type { TFunction } from 'i18next';
import type {
  GraphConfig,
  GraphEdgeState,
  GraphEvent,
  GraphInstanceState,
  GraphNode,
  GraphNodeConfig,
  GraphProgress,
  GraphRun,
  GraphRunStatus,
  GraphRunStatusResponse,
} from '../types';
import type { AgentInfo } from './ChatPage';
import { SSEClient } from '../utils/sse-client';
import { GraphCanvas } from './graph/GraphCanvas';
import { GraphInspector } from './graph/GraphInspector';
import {
  configToFlow,
  edgeStatusByEdge,
  flowToConfig,
  runConfigSnapshot,
  runStatusByNode,
  type QuartetFlowEdge,
  type QuartetFlowNode,
} from './graph/graphFlowAdapter';
import './GraphLoopProgress.css';

// The embedded mini canvas only visualizes run state; structural edits (add /
// connect / delete) are inert. Node dragging is allowed for layout convenience,
// so onNodesChange is wired to local state while the rest stay no-ops.
const NOOP = () => {};

interface GraphLoopProgressProps {
  runId: string | null;
  readOnly?: boolean;
  // Agent list for the inline inspector's Agent/model selectors.
  agents?: AgentInfo[];
  // When true the "Edit" button enters in-place run-version editing on this same
  // canvas (config-only; no navigation to the full Graph Workflows page).
  canEdit?: boolean;
}

// Statuses in which a run accepts a mid-run version edit (in-flight or
// resumable, never pending/completed). Mirrors GraphWorkflowPage's
// isGraphRunEditable and the backend's editable set.
const EDITABLE_STATUSES = new Set<GraphRunStatus>([
  'running',
  'pausing',
  'stepStopping',
  'recovering',
  'paused',
  'stepStopped',
  'stopped',
  'failed',
  'timedOut',
]);

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

export function GraphLoopProgress({ runId, readOnly, agents = [], canEdit }: GraphLoopProgressProps) {
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
  // ---- In-place run-version editing (config-only) ----
  // When editing, the canvas is seeded from the run's effective version snapshot
  // and becomes structurally read-only (no add / connect / delete); only node
  // config + the inspector are editable. Saved back via PUT /run/:id/version.
  const [editing, setEditing] = useState(false);
  const [editNodes, setEditNodes] = useState<QuartetFlowNode[]>([]);
  const [editEdges, setEditEdges] = useState<QuartetFlowEdge[]>([]);
  const [editSnapshot, setEditSnapshot] = useState<GraphConfig | null>(null);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const sseRef = useRef<SSEClient | null>(null);
  const eventLineRef = useRef(0);

  const isLive = !!run?.status && LIVE_STATUSES.has(run.status);
  const canPause = !readOnly && run?.status === 'running';
  const canStepStop = !readOnly && run?.status === 'running';
  // A pending pause / step-stop (not yet settled) can be cancelled, releasing
  // the held dispatch frontier back to running — mirrors Loop's "keep running".
  const canCancelStop = !readOnly && (run?.status === 'pausing' || run?.status === 'stepStopping');
  const canStop = !readOnly && !!run?.status && LIVE_STATUSES.has(run.status);
  const canResume = !readOnly && !!run?.status && RESUMABLE_STATUSES.has(run.status);
  const canEditRun = !!canEdit && !readOnly && !!run?.status && EDITABLE_STATUSES.has(run.status);

  const toggleExpanded = useCallback(() => {
    setExpanded((v) => !v);
  }, []);

  const handleProgressClick = useCallback((event: MouseEvent<HTMLDivElement>) => {
    const target = event.target;
    if (target instanceof Element && target.closest('button, a, input, textarea, select, [role="button"], .graph-loop-canvas, .graph-loop-editor, .graph-loop-edit-hint, .graph-loop-stats, .graph-loop-error')) {
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

  const doAction = useCallback(async (action: 'pause' | 'step-stop' | 'cancel-stop' | 'stop' | 'resume') => {
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
  // The canvas is read-only for structural edits but nodes can be dragged for
  // layout. Keep a local node copy so drag positions persist; reseed it whenever
  // the underlying run config changes (new run / new version snapshot).
  const [canvasNodes, setCanvasNodes] = useState<QuartetFlowNode[]>(miniFlow.nodes);
  useEffect(() => {
    setCanvasNodes(miniFlow.nodes);
  }, [miniFlow.nodes]);
  const onCanvasNodesChange = useCallback((changes: NodeChange[]) => {
    setCanvasNodes((prev) => applyNodeChanges(changes, prev) as QuartetFlowNode[]);
  }, []);
  const miniRunStatus = useMemo(() => runStatusByNode(instances), [instances]);
  const miniEdgeStatus = useMemo(() => edgeStatusByEdge(edges), [edges]);
  const miniErrorNodeIds = useMemo(
    () => new Set(instances.filter((inst) => inst.status === 'failed').map((inst) => inst.nodeId)),
    [instances],
  );

  // Nodes whose execution config is frozen for a version edit: those that
  // already have a succeeded / skipped / running instance (mirrors backend
  // validateVersionEdit). The inspector disables their config fields.
  const frozenNodeIds = useMemo(() => {
    const frozen = new Set<string>();
    for (const inst of instances) {
      if (inst.status === 'succeeded' || inst.status === 'skipped' || inst.status === 'running') {
        frozen.add(inst.nodeId);
      }
    }
    return frozen;
  }, [instances]);

  const selectedGraphNode: GraphNode | null = useMemo(() => {
    if (!selectedNodeId) return null;
    return editNodes.find((n) => n.id === selectedNodeId)?.data.graphNode ?? null;
  }, [editNodes, selectedNodeId]);

  // Enter in-place editing: seed editable nodes/edges from the run's effective
  // version snapshot. The canvas stays structurally read-only; only node config
  // changes are accepted (add/connect/delete are disabled, not just hidden).
  const enterEdit = useCallback(() => {
    if (!run || !canEditRun) return;
    const snapshot = runConfigSnapshot(run);
    const flow = configToFlow(snapshot);
    setEditSnapshot(snapshot);
    setEditNodes(flow.nodes);
    setEditEdges(flow.edges);
    setSelectedNodeId(null);
    setEditing(true);
    setExpanded(true);
    setError('');
  }, [canEditRun, run]);

  const cancelEdit = useCallback(() => {
    setEditing(false);
    setEditNodes([]);
    setEditEdges([]);
    setEditSnapshot(null);
    setSelectedNodeId(null);
    setError('');
  }, []);

  const onEditNodesChange = useCallback((changes: NodeChange[]) => {
    setEditNodes((prev) => applyNodeChanges(changes, prev) as QuartetFlowNode[]);
  }, []);

  // Patch the GraphNode carried by an editable canvas node (config-only edits).
  const patchGraphNode = useCallback((id: string, mutate: (gn: GraphNode) => GraphNode) => {
    setEditNodes((prev) =>
      prev.map((n) => (n.id === id ? { ...n, data: { ...n.data, graphNode: mutate(n.data.graphNode) } } : n)),
    );
  }, []);
  const onUpdateNode = useCallback(
    (id: string, patch: Partial<GraphNode>) => patchGraphNode(id, (gn) => ({ ...gn, ...patch })),
    [patchGraphNode],
  );
  const onUpdateNodeConfig = useCallback(
    (id: string, patch: Partial<GraphNodeConfig>) => patchGraphNode(id, (gn) => ({ ...gn, config: { ...gn.config, ...patch } })),
    [patchGraphNode],
  );

  const saveRunVersion = useCallback(async () => {
    if (!runId || !editSnapshot) return;
    const config = flowToConfig(editNodes, editEdges, editSnapshot);
    setSaving(true);
    setError('');
    try {
      const res = await fetch(`/api/v1/graph/run/${encodeURIComponent(runId)}/version`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ config }),
      });
      if (!res.ok) throw new Error(await readGraphError(res, `PUT /graph/run/${runId}/version`));
      setEditing(false);
      setSelectedNodeId(null);
      setEditSnapshot(null);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }, [editEdges, editNodes, editSnapshot, refresh, runId]);

  // The inspector's global panel reads variables/runConfig from `config`; build
  // it from the edit snapshot so it shows (read-only) run-level values.
  const inspectorConfig = useMemo<GraphConfig>(
    () => ({ nodes: [], edges: [], ...(editSnapshot || {}) }),
    [editSnapshot],
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
          {editing ? (
            <>
              <button
                type="button"
                className="graph-loop-action primary"
                onClick={() => void saveRunVersion()}
                disabled={saving}
                title={t('graph.editor.saveRunVersion')}
              >
                <GraphActionIcon type="save" />
                <span>{saving ? t('graph.editor.saving') : t('graph.editor.saveRunVersion')}</span>
              </button>
              <button
                type="button"
                className="graph-loop-action"
                onClick={cancelEdit}
                disabled={saving}
                title={t('graph.editor.cancelEdit')}
              >
                <GraphActionIcon type="cancel" />
                <span>{t('graph.editor.cancelEdit')}</span>
              </button>
            </>
          ) : (
            <>
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
              {canEditRun && (
                <button type="button" className="graph-loop-action" onClick={enterEdit} disabled={!!actionPending} title={t('graph.loop.editTitle')}>
                  <GraphActionIcon type="edit" />
                  <span>{t('graph.loop.edit')}</span>
                </button>
              )}
              {run?.status === 'pausing' ? (
                <button type="button" className="graph-loop-action primary" onClick={() => void doAction('cancel-stop')} disabled={!canCancelStop || !!actionPending} title={t('graph.loop.cancelStopTitle')}>
                  <GraphActionIcon type="keepRunning" />
                  <span>{t('graph.loop.cancelStop')}</span>
                </button>
              ) : (
                <button type="button" className="graph-loop-action warn" onClick={() => void doAction('pause')} disabled={!canPause || !!actionPending} title={t('graph.loop.pauseTitle')}>
                  <GraphActionIcon type="pause" />
                  <span>{t('graph.loop.pause')}</span>
                </button>
              )}
              {run?.status === 'stepStopping' ? (
                <button type="button" className="graph-loop-action primary" onClick={() => void doAction('cancel-stop')} disabled={!canCancelStop || !!actionPending} title={t('graph.loop.cancelStopTitle')}>
                  <GraphActionIcon type="keepRunning" />
                  <span>{t('graph.loop.cancelStop')}</span>
                </button>
              ) : (
                <button type="button" className="graph-loop-action warn" onClick={() => void doAction('step-stop')} disabled={!canStepStop || !!actionPending} title={t('graph.loop.stepStopTitle')}>
                  <GraphActionIcon type="step" />
                  <span>{t('graph.loop.stepStop')}</span>
                </button>
              )}
              <button type="button" className="graph-loop-action danger" onClick={() => void doAction('stop')} disabled={!canStop || !!actionPending} title={t('graph.loop.stopTitle')}>
                <GraphActionIcon type="stop" />
                <span>{t('graph.loop.stop')}</span>
              </button>
              <button type="button" className="graph-loop-action primary" onClick={() => void doAction('resume')} disabled={!canResume || !!actionPending} title={t('graph.loop.resumeTitle')}>
                <GraphActionIcon type="resume" />
                <span>{t('graph.loop.resume')}</span>
              </button>
            </>
          )}
        </div>
      </div>

      <div className="graph-loop-bar-wrapper">
        <div className={`graph-loop-bar status-${run?.status || 'loading'}`} style={{ width: `${percent}%` }} />
      </div>

      {editing ? (
        <>
          <div className="graph-loop-edit-hint" data-testid="graph-loop-edit-hint">
            {t('graph.loop.editConfigHint')}
          </div>
          <div className="graph-loop-editor" data-testid="graph-loop-editor">
            <div className="graph-loop-edit-canvas">
              <GraphCanvas
                key="graph-loop-edit"
                nodes={editNodes}
                edges={editEdges}
                readOnly
                showMiniMap={false}
                runStatusByNodeId={miniRunStatus}
                errorNodeIds={miniErrorNodeIds}
                onNodesChange={onEditNodesChange}
                onEdgesChange={NOOP}
                onConnect={NOOP}
                onNodeClick={(id) => setSelectedNodeId(id)}
                onPaneClick={() => setSelectedNodeId(null)}
                onAddNode={NOOP}
              />
            </div>
            <div className="graph-loop-inspector graph-dark">
              <GraphInspector
                node={selectedGraphNode}
                config={inspectorConfig}
                agents={agents}
                lockStructure
                frozenNodeIds={frozenNodeIds}
                onUpdateNode={onUpdateNode}
                onUpdateNodeConfig={onUpdateNodeConfig}
                onDeleteNode={NOOP}
                onUpdateVariables={NOOP}
                onUpdateRunConfig={NOOP}
              />
            </div>
          </div>
          {lastError && (
            <pre className="graph-loop-error" data-testid="graph-loop-error">{lastError}</pre>
          )}
        </>
      ) : expanded ? (
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
              nodes={canvasNodes}
              edges={miniFlow.edges}
              readOnly
              allowNodeDrag
              showMiniMap={false}
              runStatusByNodeId={miniRunStatus}
              edgeStatusById={miniEdgeStatus}
              errorNodeIds={miniErrorNodeIds}
              onNodesChange={onCanvasNodesChange}
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
      ) : null}
    </div>
  );
}

function GraphActionIcon({ type }: { type: 'edit' | 'pause' | 'step' | 'stop' | 'resume' | 'keepRunning' | 'expand' | 'collapse' | 'save' | 'cancel' }) {
  if (type === 'expand') {
    return <svg viewBox="0 0 24 24"><path d="m6 9 6 6 6-6" /></svg>;
  }
  if (type === 'collapse') {
    return <svg viewBox="0 0 24 24"><path d="m6 15 6-6 6 6" /></svg>;
  }
  if (type === 'keepRunning') {
    return <svg viewBox="0 0 24 24"><path d="M8 5v14l11-7-11-7Z" /><path d="M4 6v12" /></svg>;
  }
  if (type === 'save') {
    return <svg viewBox="0 0 24 24"><path d="M5 4h12l2 2v14H5Z" /><path d="M8 4v6h8V4" /><path d="M8 20v-6h8v6" /></svg>;
  }
  if (type === 'cancel') {
    return <svg viewBox="0 0 24 24"><path d="M18 6 6 18" /><path d="m6 6 12 12" /></svg>;
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
