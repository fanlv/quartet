import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { MouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import {
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  MarkerType,
  type Connection,
  type EdgeChange,
  type NodeChange,
} from '@xyflow/react';
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
  GraphValidationError,
} from '../types';
import type { AgentInfo } from './ChatPage';
import { SSEClient } from '../utils/sse-client';
import { GraphCanvas, type GraphCanvasFocus } from './graph/GraphCanvas';
import { GraphInspector } from './graph/GraphInspector';
import {
  clearNestConstraint,
  configToFlow,
  edgeStatusByEdge,
  flowToConfig,
  nestConstraint,
  orderNodesByHierarchy,
  repinLoopPorts,
  runConfigSnapshot,
  runStatusByNode,
  type QuartetFlowEdge,
  type QuartetFlowNode,
} from './graph/graphFlowAdapter';
import { createEditorNode, filterNodeRemoveChanges, markEditableNodes } from './graph/editorModel';
import './GraphLoopProgress.css';

// The embedded mini canvas visualizes run state. Outside edit mode structural
// edits are inert; in run-version edit mode, unfrozen structure can be repaired
// and the backend re-validates the exact frozen rules.
const NOOP = () => {};

function isEmptyForFingerprint(value: unknown): boolean {
  if (value === undefined) return true;
  if (Array.isArray(value)) return value.length === 0;
  if (value && typeof value === 'object') return Object.keys(value as Record<string, unknown>).length === 0;
  return false;
}

function stableStringify(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`;
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record)
      .sort()
      .filter((key) => !isEmptyForFingerprint(record[key]))
      .map((key) => `${JSON.stringify(key)}:${stableStringify(record[key])}`)
      .join(',')}}`;
  }
  return JSON.stringify(value) ?? 'undefined';
}

function mintId(prefix: string, taken: Set<string>): string {
  let id = '';
  do {
    id = `${prefix}-${Date.now()}-${Math.floor(Math.random() * 10000)}`;
  } while (taken.has(id));
  taken.add(id);
  return id;
}

interface GraphLoopProgressProps {
  jobId: string | null;
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

// LIVE = run is actively scheduling and still producing events → keep an SSE
// tail open. RESUMABLE mirrors the backend's isResumableStatus (run_control.go),
// which INCLUDES 'recovering': a crash-recovered run is a static, resumable
// terminal — it does not auto-continue and emits no new events, so it is NOT
// live (no SSE tail, no replay) and instead shows a Resume action. Keeping these
// two in sync with the backend is load-bearing: if 'recovering' were in neither
// set the run would show neither Stop nor Resume and get stuck.
const LIVE_STATUSES = new Set<GraphRunStatus>(['pending', 'running', 'pausing', 'stepStopping']);
const RESUMABLE_STATUSES = new Set<GraphRunStatus>(['failed', 'paused', 'stepStopped', 'stopped', 'timedOut', 'recovering']);

function makeValidationLabel(err: GraphValidationError): string {
  const loc = [
    err.nodeId ? `node=${err.nodeId}` : '',
    err.edgeId ? `edge=${err.edgeId}` : '',
    err.variable ? `var=${err.variable}` : '',
    err.configKey ? `config=${err.configKey}` : '',
  ]
    .filter(Boolean)
    .join(', ');
  return loc ? `[${err.type}] ${err.message} (${loc})` : `[${err.type}] ${err.message}`;
}

async function readGraphErrorDetail(response: Response, prefix?: string): Promise<{ message: string; errors: GraphValidationError[] }> {
  const body = await response.text().catch(() => '');
  const trimmed = body.trim();
  let detail = trimmed;
  let errors: GraphValidationError[] = [];
  if (trimmed) {
    try {
      const parsed = JSON.parse(trimmed);
      if (Array.isArray(parsed?.errors) && parsed.errors.length > 0) {
        errors = parsed.errors as GraphValidationError[];
        detail = errors.map(makeValidationLabel).join('\n');
      } else if (typeof parsed?.msg === 'string') detail = parsed.msg;
      else if (typeof parsed?.error === 'string') detail = parsed.error;
      else if (typeof parsed?.message === 'string') detail = parsed.message;
    } catch {
      // Keep raw non-JSON bodies.
    }
  }
  const message = detail ? `HTTP ${response.status}: ${detail}` : `HTTP ${response.status}`;
  return { message: prefix ? `${prefix}: ${message}` : message, errors };
}

async function readGraphError(response: Response, prefix?: string): Promise<string> {
  return (await readGraphErrorDetail(response, prefix)).message;
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

export function GraphLoopProgress({ jobId, runId, readOnly, agents = [], canEdit }: GraphLoopProgressProps) {
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
  // ---- In-place run-version editing ----
  // When editing, the canvas is seeded from the run's effective version snapshot
  // and unfrozen nodes/edges can be repaired. Frozen node config is disabled by
  // the inspector and the backend remains authoritative. Saved back through the
  // bound Job's graph-run version endpoint.
  const [editing, setEditing] = useState(false);
  const [editNodes, setEditNodes] = useState<QuartetFlowNode[]>([]);
  const [editEdges, setEditEdges] = useState<QuartetFlowEdge[]>([]);
  const [editSnapshot, setEditSnapshot] = useState<GraphConfig | null>(null);
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [validationErrors, setValidationErrors] = useState<GraphValidationError[]>([]);
  const [focus, setFocus] = useState<GraphCanvasFocus>({ token: 0 });
  const [savedEditFingerprint, setSavedEditFingerprint] = useState('');
  const [saving, setSaving] = useState(false);
  const sseRef = useRef<SSEClient | null>(null);
  const bindingRef = useRef(`${jobId || ''}:${runId || ''}`);

  const isLive = !!run?.status && LIVE_STATUSES.has(run.status);
  const canPause = !readOnly && run?.status === 'running';
  const canStepStop = !readOnly && run?.status === 'running';
  // A pending pause / legacy step-stop (not yet settled) can be cancelled, releasing
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
    if (!jobId) {
      setRun(null);
      setProgress(null);
      setInstances([]);
      setEdges([]);
      return;
    }
    setLoading(true);
    try {
      const res = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`);
      if (!res.ok) throw new Error(await readGraphError(res, `GET /job/${jobId}/graph-run`));
      const data = await res.json() as GraphRunStatusResponse;
      setRun(data.run || null);
      setProgress(data.progress || data.run?.progress || null);
      setInstances(data.instances || []);
      setEdges(data.edges || []);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [jobId]);

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
    if (!jobId || !isLive) return;

    const client = new SSEClient();
    sseRef.current = client;
    let closed = false;
    let lastInstanceRefreshAt = 0;
    const throttledRefresh = () => {
      const now = Date.now();
      if (now - lastInstanceRefreshAt < 400) return;
      lastInstanceRefreshAt = now;
      void refresh();
    };
    void client.connectUntilReady({
      url: `/api/v1/job/${encodeURIComponent(jobId)}/graph-run/events`,
      // Resume from the buffer tail. The server's Last-Event-ID is now an
      // in-memory buffer seq (not a file line count), so seeding it from a
      // client-side counter is meaningless; the SSE client overwrites this with
      // the server-sent `id:` (seq) for any subsequent reconnect.
      initialLastEventId: '0',
      onEvent: (raw) => {
        const event = raw as unknown as GraphEvent;
        // Events never carry a progress snapshot: progress + node status come
        // exclusively from a (throttled) re-fetch of the run snapshot, so a
        // replayed historical event can never rewind the live progress. The
        // event's job here is only to decide *when* to re-fetch.
        // progressUpdated/error re-fetch immediately (terminal transition +
        // edges); instance/edge/loop lifecycle events re-fetch throttled.
        if (event.type === 'progressUpdated' || event.type === 'error') {
          if (event.type === 'error' && event.message) {
            setError(event.error?.message || event.message);
          }
          void refresh();
          return;
        }
        if (
          event.type === 'instanceStarted' || event.type === 'instanceCompleted' ||
          event.type === 'instanceFailed' || event.type === 'instanceSkipped' ||
          event.type === 'edgeResolved' || event.type === 'loopIteration'
        ) {
          throttledRefresh();
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
  }, [isLive, refresh, jobId]);

  const doAction = useCallback(async (action: 'pause' | 'step-stop' | 'cancel-stop' | 'stop' | 'resume') => {
    if (!jobId) return;
    setActionPending(action);
    setError('');
    try {
      const res = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run/${action}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
      });
      if (!res.ok) throw new Error(await readGraphError(res, `POST /job/${jobId}/graph-run/${action}`));
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
  }, [progress, refresh, jobId]);

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
  const validationErrorNodeIds = useMemo(() => new Set(validationErrors.map((err) => err.nodeId).filter(Boolean) as string[]), [validationErrors]);
  const validationErrorEdgeIds = useMemo(() => new Set(validationErrors.map((err) => err.edgeId).filter(Boolean) as string[]), [validationErrors]);
  const currentEditFingerprint = useMemo(
    () => (editSnapshot ? stableStringify(flowToConfig(editNodes, editEdges, editSnapshot)) : ''),
    [editEdges, editNodes, editSnapshot],
  );
  const editDirty = editing && currentEditFingerprint !== savedEditFingerprint;

  useEffect(() => {
    const binding = `${jobId || ''}:${runId || ''}`;
    if (bindingRef.current === binding) return;
    if (editing && editDirty && !window.confirm(t('graph.messages.discardUnsavedConfirm'))) {
      return;
    }
    bindingRef.current = binding;
    setEditing(false);
    setEditNodes([]);
    setEditEdges([]);
    setEditSnapshot(null);
    setSelectedNodeId(null);
    setValidationErrors([]);
    setSavedEditFingerprint('');
    setError('');
  }, [editDirty, editing, jobId, runId, t]);

  // Nodes whose execution config is frozen for a version edit. A node inside a
  // loop body re-runs each round, so it stays editable and the next round uses
  // the new config. The loop container itself remains frozen once started.
  const frozenNodeIds = useMemo(() => {
    const byId = new Map(editNodes.map((n) => [n.id, n]));
    const insideLoop = (nodeId: string): boolean => {
      let pid = byId.get(nodeId)?.parentId;
      while (pid) {
        const parent = byId.get(pid);
        if (!parent) return false;
        if (parent.data?.kind === 'loop') return true;
        pid = parent.parentId;
      }
      return false;
    };
    const frozen = new Set<string>();
    for (const inst of instances) {
      if (inst.status === 'succeeded' || inst.status === 'skipped' || inst.status === 'running') {
        if (insideLoop(inst.nodeId)) continue;
        frozen.add(inst.nodeId);
      }
    }
    return frozen;
  }, [editNodes, instances]);

  const selectedGraphNode: GraphNode | null = useMemo(() => {
    if (!selectedNodeId) return null;
    return editNodes.find((n) => n.id === selectedNodeId)?.data.graphNode ?? null;
  }, [editNodes, selectedNodeId]);

  // Enter in-place editing: seed editable nodes/edges from the run's effective
  // version snapshot. Unfrozen structure can be repaired; the backend still
  // enforces the exact run-version edit rules when saving.
  const enterEdit = useCallback(() => {
    if (!run || !canEditRun) return;
    const snapshot = runConfigSnapshot(run);
    const flow = configToFlow(snapshot);
    const initialConfig = flowToConfig(flow.nodes, flow.edges, snapshot);
    setEditSnapshot(snapshot);
    setEditNodes(markEditableNodes(flow.nodes));
    setEditEdges(flow.edges);
    setSelectedNodeId(null);
    setValidationErrors([]);
    setSavedEditFingerprint(stableStringify(initialConfig));
    setEditing(true);
    setExpanded(true);
    setError('');
  }, [canEditRun, run]);

  const discardEdit = useCallback(() => {
    setEditing(false);
    setEditNodes([]);
    setEditEdges([]);
    setEditSnapshot(null);
    setSelectedNodeId(null);
    setValidationErrors([]);
    setSavedEditFingerprint('');
    setError('');
  }, []);

  const cancelEdit = useCallback(() => {
    if (editDirty && !window.confirm(t('graph.messages.discardUnsavedConfirm'))) return;
    discardEdit();
  }, [discardEdit, editDirty, t]);

  const focusValidationError = useCallback((err: GraphValidationError) => {
    if (err.nodeId) {
      setSelectedNodeId(err.nodeId);
      setFocus({ nodeId: err.nodeId, token: Date.now() });
      return;
    }
    if (err.edgeId) {
      const edge = editEdges.find((e) => e.id === err.edgeId);
      const targetNodeId = edge?.target || edge?.source;
      if (targetNodeId) setSelectedNodeId(targetNodeId);
      setFocus({ edgeId: err.edgeId, token: Date.now() });
    }
  }, [editEdges]);

  const onEditNodesChange = useCallback((changes: NodeChange[]) => {
    setValidationErrors([]);
    const removedIds = new Set(changes.filter((ch) => ch.type === 'remove').map((ch) => ch.id));
    setEditNodes((prev) => {
      let effectiveChanges = changes;
      let doomed = new Set<string>();
      if (removedIds.size > 0) {
        const filtered = filterNodeRemoveChanges(changes, prev);
        effectiveChanges = filtered.changes;
        doomed = filtered.removedIds;
        if (doomed.size > 0) {
          setEditEdges((edges) => edges.filter((e) => !doomed.has(e.source) && !doomed.has(e.target)));
          setSelectedNodeId((cur) => (cur && doomed.has(cur) ? null : cur));
        }
      }
      return markEditableNodes(repinLoopPorts(applyNodeChanges(effectiveChanges, prev) as QuartetFlowNode[]));
    });
  }, []);
  const onEditEdgesChange = useCallback((changes: EdgeChange[]) => {
    setValidationErrors([]);
    setEditEdges((prev) => applyEdgeChanges(changes, prev) as QuartetFlowEdge[]);
  }, []);
  const onEditConnect = useCallback((connection: Connection) => {
    setValidationErrors([]);
    setEditEdges((prev) => {
      const port = connection.sourceHandle === 'yes' || connection.sourceHandle === 'no' ? connection.sourceHandle : undefined;
      const id = mintId(`edge-${connection.source}-${connection.target}`, new Set(prev.map((e) => e.id)));
      const edge: QuartetFlowEdge = {
        id,
        source: connection.source!,
        target: connection.target!,
        sourceHandle: connection.sourceHandle ?? undefined,
        data: { port },
        markerEnd: { type: MarkerType.ArrowClosed, color: '#4c5663' },
        ...(port
          ? { label: port === 'yes' ? 'YES' : 'NO', labelStyle: { fill: port === 'yes' ? '#2ea043' : '#f85149', fontWeight: 700 } }
          : {}),
      };
      return addEdge(edge, prev) as QuartetFlowEdge[];
    });
  }, []);
  const onAddEditNode = useCallback((type: GraphNode['type'], position: { x: number; y: number }, parentId?: string | null) => {
    setValidationErrors([]);
    const takenIds = new Set(editNodes.map((n) => n.id));
    const created = createEditorNode(type, position, parentId, mintId, takenIds);
    setEditNodes((prev) => markEditableNodes(orderNodesByHierarchy([...prev, ...created])));
    setSelectedNodeId(created[0]?.id ?? null);
  }, [editNodes]);
  const onEditReparent = useCallback((nodeId: string, newParentId: string | null) => {
    setValidationErrors([]);
    setEditNodes((prev) => {
      const absPos = (n: QuartetFlowNode): { x: number; y: number } => {
        let x = n.position.x;
        let y = n.position.y;
        let pid = n.parentId;
        const seen = new Set<string>();
        while (pid && !seen.has(pid)) {
          seen.add(pid);
          const parent = prev.find((p) => p.id === pid);
          if (!parent) break;
          x += parent.position.x;
          y += parent.position.y;
          pid = parent.parentId;
        }
        return { x, y };
      };
      const next = prev.map((n) => {
        if (n.id !== nodeId) return n;
        const abs = absPos(n);
        let pos = abs;
        if (newParentId) {
          const parent = prev.find((p) => p.id === newParentId);
          if (!parent) return n;
          const parentAbs = absPos(parent);
          pos = { x: abs.x - parentAbs.x, y: abs.y - parentAbs.y };
        }
        const graphNode: GraphNode = {
          ...n.data.graphNode,
          parentId: newParentId ?? undefined,
          layout: { ...(n.data.graphNode.layout || {}), x: Math.round(pos.x), y: Math.round(pos.y) },
        };
        return {
          ...n,
          position: pos,
          parentId: newParentId ?? undefined,
          ...clearNestConstraint,
          ...(newParentId ? nestConstraint(n.data.kind) : {}),
          data: { ...n.data, graphNode },
        };
      });
      return markEditableNodes(orderNodesByHierarchy(next));
    });
  }, []);
  const onDeleteEditNode = useCallback((id: string) => {
    setValidationErrors([]);
    setEditNodes((prev) => {
      const doomed = filterNodeRemoveChanges([{ type: 'remove', id }], prev).removedIds;
      setEditEdges((edges) => edges.filter((e) => !doomed.has(e.source) && !doomed.has(e.target)));
      setSelectedNodeId((cur) => (cur && doomed.has(cur) ? null : cur));
      return markEditableNodes(prev.filter((n) => !doomed.has(n.id)));
    });
  }, []);

  // Patch the GraphNode carried by an editable canvas node (config-only edits).
  const patchGraphNode = useCallback((id: string, mutate: (gn: GraphNode) => GraphNode) => {
    setValidationErrors([]);
    setEditNodes((prev) =>
      markEditableNodes(prev.map((n) => (n.id === id ? { ...n, data: { ...n.data, graphNode: mutate(n.data.graphNode) } } : n))),
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
    if (!jobId || !editSnapshot) return;
    const config = flowToConfig(editNodes, editEdges, editSnapshot);
    setSaving(true);
    setError('');
    setValidationErrors([]);
    try {
      const res = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run/version`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ config }),
      });
      if (!res.ok) {
        const detail = await readGraphErrorDetail(res, `PUT /job/${jobId}/graph-run/version`);
        if (detail.errors.length > 0) {
          setValidationErrors(detail.errors);
          setError(t('graph.messages.validationErrors', { count: detail.errors.length }));
          return;
        }
        throw new Error(detail.message);
      }
      discardEdit();
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }, [discardEdit, editEdges, editNodes, editSnapshot, refresh, jobId, t]);

  // The inspector needs both run-level values and the live edited topology for
  // condition variable suggestions (upstream outputs, loop body outputs, loop
  // iteration vars).
  const inspectorConfig = useMemo<GraphConfig>(
    () => (editSnapshot ? flowToConfig(editNodes, editEdges, editSnapshot) : { nodes: [], edges: [] }),
    [editEdges, editNodes, editSnapshot],
  );

  const lastError = run?.lastError?.message || progress?.lastError || error;
  const doneText = progress
    ? t('graph.loop.done', { count: progress.completedCount, total: progress.totalCount })
    : t('graph.progress.none');

  if (!jobId || !runId) {
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
              {run?.status === 'pausing' || run?.status === 'stepStopping' ? (
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
              <button type="button" className="graph-loop-action warn" onClick={() => void doAction('step-stop')} disabled={!canStepStop || !!actionPending} title={t('graph.loop.stepStopTitle')}>
                <GraphActionIcon type="pause" />
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
                readOnly={false}
                showMiniMap={false}
                runStatusByNodeId={miniRunStatus}
                errorNodeIds={validationErrorNodeIds}
                errorEdgeIds={validationErrorEdgeIds}
                focus={focus}
                onNodesChange={onEditNodesChange}
                onEdgesChange={onEditEdgesChange}
                onConnect={onEditConnect}
                onNodeClick={(id) => setSelectedNodeId(id)}
                onPaneClick={() => setSelectedNodeId(null)}
                onAddNode={onAddEditNode}
                onReparent={onEditReparent}
              />
            </div>
            <div className="graph-loop-inspector graph-dark">
              <GraphInspector
                node={selectedGraphNode}
                config={inspectorConfig}
                agents={agents}
                frozenNodeIds={frozenNodeIds}
                lockGlobals
                onUpdateNode={onUpdateNode}
                onUpdateNodeConfig={onUpdateNodeConfig}
                onDeleteNode={onDeleteEditNode}
                onUpdateVariables={NOOP}
                onUpdateRunConfig={NOOP}
              />
            </div>
          </div>
          {validationErrors.length > 0 && (
            <ul className="graph-loop-validation-list" data-testid="graph-loop-error-list">
              {validationErrors.map((err, index) => (
                <li key={`${err.type}-${err.nodeId || err.edgeId || err.variable || err.configKey || index}`}>
                  <button type="button" className="graph-loop-error-link" data-testid="graph-loop-error-link" onClick={() => focusValidationError(err)}>
                    {makeValidationLabel(err)}
                  </button>
                </li>
              ))}
            </ul>
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

        </>
      ) : null}

      {lastError && (
        <pre className="graph-loop-error" data-testid="graph-loop-error">{lastError}</pre>
      )}
    </div>
  );
}

function GraphActionIcon({ type }: { type: 'edit' | 'pause' | 'stop' | 'resume' | 'keepRunning' | 'expand' | 'collapse' | 'save' | 'cancel' }) {
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
  if (type === 'stop') {
    return <svg viewBox="0 0 24 24"><rect x="7" y="7" width="10" height="10" rx="2" /></svg>;
  }
  return <svg viewBox="0 0 24 24"><path d="M5 12a7 7 0 0 1 12-5l2 2" /><path d="M19 5v4h-4" /><path d="M19 12a7 7 0 0 1-12 5l-2-2" /><path d="M5 19v-4h4" /></svg>;
}
