import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import i18n from '../i18n';
import {
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  MarkerType,
  type Connection,
  type EdgeChange,
  type NodeChange,
  type Viewport,
} from '@xyflow/react';
import type {
  GraphConfig,
  GraphEvent,
  GraphInstanceState,
  GraphListWorkflowsResponse,
  GraphNode,
  GraphNodeConfig,
  GraphNodeType,
  GraphProgress,
  GraphRun,
  GraphRunConfig,
  GraphRunHistoryResponse,
  GraphRunStatus,
  GraphRunStatusResponse,
  GraphValidationError,
  GraphValidationResponse,
  GraphWorkflow,
  GraphWorkflowResponse,
} from '../types';
import type { AgentInfo } from './ChatPage';
import { SSEClient } from '../utils/sse-client';
import { useIsMobile } from '../hooks/useIsMobile';
import {
  configToFlow,
  flowToConfig,
  runConfigSnapshot,
  runStatusByNode,
  type QuartetFlowEdge,
  type QuartetFlowNode,
} from './graph/graphFlowAdapter';
import { GraphCanvas, type GraphCanvasFocus } from './graph/GraphCanvas';
import { GraphInspector } from './graph/GraphInspector';
import { LOOP_DEFAULT_HEIGHT, LOOP_DEFAULT_WIDTH } from './graph/graphFlowAdapter';
import './GraphWorkflowPage.css';

interface GraphWorkflowPageProps {
  workspaceId?: string;
  workspaceTitle?: string;
  workspaceWorkdir?: string;
  onClose: () => void;
  // Called after a run is started so the app can jump into the Chat page for the
  // bound Graph Job, mirroring the startloop flow.
  onRunStarted: (jobId: string) => void;
}

type SaveMode = 'create' | 'update';
type ViewMode = 'canvas' | 'json';

// Top-level config fields the canvas does not own; kept in page state and
// re-attached when building a full GraphConfig for save/validate/run.
interface ConfigMeta {
  variables: Record<string, string>;
  disabledVars: string[];
  runConfig: GraphRunConfig;
  workspaceId?: string;
  workdir?: string;
  sandboxId?: string;
  canvas?: GraphConfig['canvas'];
}

const EMPTY_CONFIG: GraphConfig = {
  nodes: [
    { id: 'start', type: 'start', title: '', layout: { x: 80, y: 160 } },
    { id: 'shell', type: 'shell', title: 'Shell', config: { script: 'echo hello' }, layout: { x: 320, y: 160 } },
    { id: 'end', type: 'end', title: '', layout: { x: 560, y: 160 } },
  ],
  edges: [
    { id: 'edge-start-shell', sourceNodeId: 'start', targetNodeId: 'shell' },
    { id: 'edge-shell-end', sourceNodeId: 'shell', targetNodeId: 'end' },
  ],
  variables: {},
  disabledVars: [],
  runConfig: { concurrencyLimit: 4 },
};

function cloneDefaultConfig(t: TFunction, workspaceId?: string, workdir?: string): GraphConfig {
  return {
    ...EMPTY_CONFIG,
    workspaceId,
    workdir,
    nodes: EMPTY_CONFIG.nodes.map((node) => ({
      ...node,
      title: node.type === 'start' ? t('graph.nodeTitle.start') : node.type === 'end' ? t('graph.nodeTitle.end') : node.title,
      config: node.config ? { ...node.config } : undefined,
      layout: node.layout ? { ...node.layout } : undefined,
    })),
    edges: EMPTY_CONFIG.edges.map((edge) => ({ ...edge })),
    variables: {},
    disabledVars: [],
    runConfig: { ...EMPTY_CONFIG.runConfig },
  };
}

function metaFromConfig(config: GraphConfig): ConfigMeta {
  return {
    variables: { ...(config.variables || {}) },
    disabledVars: [...(config.disabledVars || [])],
    runConfig: { ...(config.runConfig || {}) },
    workspaceId: config.workspaceId,
    workdir: config.workdir,
    sandboxId: config.sandboxId,
    canvas: config.canvas,
  };
}

// start/end nodes cannot be deleted from the canvas.
function markEditable(nodes: QuartetFlowNode[]): QuartetFlowNode[] {
  return nodes.map((n) => {
    const protectedNode = n.data.kind === 'start' || n.data.kind === 'end';
    return { ...n, deletable: !protectedNode };
  });
}

function formatDate(value: string): string {
  if (!value) return '-';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  return d.toLocaleString();
}

function summarizeWorkflow(wf: GraphWorkflow, t: TFunction): string {
  const nodeCount = wf.config?.nodes?.length ?? 0;
  const edgeCount = wf.config?.edges?.length ?? 0;
  return t('graph.summary', { nodes: nodeCount, edges: edgeCount });
}

function isGraphRunLive(status?: GraphRunStatus): boolean {
  return status === 'pending' || status === 'running' || status === 'pausing' || status === 'stepStopping';
}

// A GraphRun accepts mid-run version edits while it is still in-flight or in a
// resumable terminal state — the backend appends a new version that later
// not-yet-started instances pick up. A naturally completed run is frozen
// (read-only replay). Mirrors the union of GraphLoop's live + resumable sets.
function isGraphRunEditable(status?: GraphRunStatus): boolean {
  return (
    status === 'running' ||
    status === 'pausing' ||
    status === 'stepStopping' ||
    status === 'recovering' ||
    status === 'paused' ||
    status === 'stepStopped' ||
    status === 'stopped' ||
    status === 'failed' ||
    status === 'timedOut'
  );
}

type GraphButtonIconName =
  | 'canvas'
  | 'json'
  | 'check'
  | 'play'
  | 'reset'
  | 'trash'
  | 'copy'
  | 'save'
  | 'edit'
  | 'back'
  | 'cancel'
  | 'apply';

function GraphButtonIcon({ name }: { name: GraphButtonIconName }) {
  if (name === 'canvas') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <rect x="4" y="5" width="16" height="14" rx="2" />
        <path d="M8 9h8" />
        <path d="M8 13h5" />
      </svg>
    );
  }
  if (name === 'json') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="m8 8-4 4 4 4" />
        <path d="m16 8 4 4-4 4" />
        <path d="m14 5-4 14" />
      </svg>
    );
  }
  if (name === 'check') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="m5 12 4 4L19 6" />
      </svg>
    );
  }
  if (name === 'play') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M8 5v14l11-7Z" />
      </svg>
    );
  }
  if (name === 'reset') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M4 7v5h5" />
        <path d="M20 17a8 8 0 0 1-13.7-5.6L4 12" />
      </svg>
    );
  }
  if (name === 'trash') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M4 7h16" />
        <path d="M10 11v6" />
        <path d="M14 11v6" />
        <path d="M6 7l1 14h10l1-14" />
        <path d="M9 7V4h6v3" />
      </svg>
    );
  }
  if (name === 'copy') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <rect x="8" y="8" width="11" height="11" rx="2" />
        <path d="M5 15H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v1" />
      </svg>
    );
  }
  if (name === 'save') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M5 4h12l2 2v14H5Z" />
        <path d="M8 4v6h8V4" />
        <path d="M8 20v-6h8v6" />
      </svg>
    );
  }
  if (name === 'edit') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M4 20h4.5L19 9.5a2.1 2.1 0 0 0 0-3L17.5 5a2.1 2.1 0 0 0-3 0L4 15.5V20Z" />
        <path d="M13.5 6 18 10.5" />
      </svg>
    );
  }
  if (name === 'back') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="m11 17-5-5 5-5" />
        <path d="M18 18v-2a4 4 0 0 0-4-4H6" />
      </svg>
    );
  }
  if (name === 'cancel') {
    return (
      <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M18 6 6 18" />
        <path d="m6 6 12 12" />
      </svg>
    );
  }
  return (
    <svg className="graph-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M5 12h14" />
      <path d="m13 6 6 6-6 6" />
    </svg>
  );
}

function graphProgressLabel(t: TFunction, progress?: GraphProgress): string {
  if (!progress) return t('graph.progress.none');
  return t('graph.progress.label', {
    completed: progress.completedCount,
    total: progress.totalCount,
    running: progress.runningCount,
    failed: progress.failedCount,
    skipped: progress.skippedCount,
  });
}

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

async function readError(res: Response): Promise<string> {
  const data = await res.json().catch(() => null);
  if (Array.isArray(data?.errors) && data.errors.length > 0) {
    return data.errors.map(makeValidationLabel).join('\n');
  }
  return data?.msg || data?.error || `HTTP ${res.status}`;
}

// Resolve the config a GraphRun executed against lives in graphFlowAdapter
// (runConfigSnapshot), shared with GraphLoopProgress.
function stableStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableStringify).join(',')}]`;
  }
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>;
    return `{${Object.keys(record)
      .sort()
      // Mirror Go's `omitempty`: the server drops undefined values and empty
      // maps/slices from the persisted config, so the live config (which keeps
      // `variables:{}` / `disabledVars:[]` / `canvas:undefined`) must normalize
      // the same way — otherwise `dirty` would stay true forever after a save.
      .filter((key) => !isEmptyForFingerprint(record[key]))
      .map((key) => `${JSON.stringify(key)}:${stableStringify(record[key])}`)
      .join(',')}}`;
  }
  return JSON.stringify(value) ?? 'undefined';
}

// isEmptyForFingerprint reports values the server omits via JSON omitempty:
// undefined, empty arrays and empty plain objects.
function isEmptyForFingerprint(value: unknown): boolean {
  if (value === undefined) return true;
  if (Array.isArray(value)) return value.length === 0;
  if (value && typeof value === 'object') return Object.keys(value as Record<string, unknown>).length === 0;
  return false;
}

// fingerprint builds the dirty-tracking key for a workflow. The canvas viewport
// (config.canvas) is pure view state: React Flow's fitView fires onMoveEnd after
// every load, so including it would mark a freshly-opened workflow as dirty
// forever. Pan/zoom is persisted on save but does not count as an edit.
function fingerprint(input: { name: string; description: string; config: GraphConfig }): string {
  const { canvas: _canvas, ...configWithoutCanvas } = input.config;
  return stableStringify({ name: input.name, description: input.description, config: configWithoutCanvas });
}

export function GraphWorkflowPage({ workspaceId, workspaceTitle, workspaceWorkdir, onClose, onRunStarted }: GraphWorkflowPageProps) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [workflows, setWorkflows] = useState<GraphWorkflow[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [name, setName] = useState(() => i18n.t('graph.defaultName'));
  const [description, setDescription] = useState('');
  const [errors, setErrors] = useState<GraphValidationError[]>([]);
  const [message, setMessage] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<GraphWorkflow | null>(null);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [viewMode, setViewMode] = useState<ViewMode>('canvas');
  const [jsonText, setJsonText] = useState('');

  // Canvas (editor) state — the structural source of truth.
  const initialConfig = useMemo(() => cloneDefaultConfig(t, workspaceId, workspaceWorkdir), [t, workspaceId, workspaceWorkdir]);
  const [nodes, setNodes] = useState<QuartetFlowNode[]>(() => markEditable(configToFlow(initialConfig).nodes));
  const [edges, setEdges] = useState<QuartetFlowEdge[]>(() => configToFlow(initialConfig).edges);
  const [meta, setMeta] = useState<ConfigMeta>(() => metaFromConfig(initialConfig));
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  const [inspectorDrawerOpen, setInspectorDrawerOpen] = useState(true);
  const [focus, setFocus] = useState<GraphCanvasFocus>({ token: 0 });
  const [loadedConfig, setLoadedConfig] = useState<GraphConfig>(initialConfig);
  const [viewportResetKey, setViewportResetKey] = useState(0);
  const [savedFingerprint, setSavedFingerprint] = useState(() =>
    fingerprint({ name: i18n.t('graph.defaultName'), description: '', config: initialConfig }),
  );
  const nodeCounterRef = useRef(0);

  // Run history / replay state.
  const [runs, setRuns] = useState<GraphRun[]>([]);
  const [selectedRun, setSelectedRun] = useState<GraphRun | null>(null);
  const [viewingRun, setViewingRun] = useState(false);
  const [runProgress, setRunProgress] = useState<GraphProgress | undefined>();
  const [runInstances, setRunInstances] = useState<GraphInstanceState[]>([]);
  const [runEvents, setRunEvents] = useState<GraphEvent[]>([]);
  const [runMessage, setRunMessage] = useState('');
  // editingRun overlays an editable canvas on top of the run-view: the run's
  // current effective version snapshot is loaded into the editor and saved back
  // as a new GraphRun version (PUT /run/:id/version). Only enterable while the
  // selected run is editable (in-flight or resumable, never naturally completed).
  const [editingRun, setEditingRun] = useState(false);
  const graphEventClientRef = useRef<SSEClient | null>(null);
  const runEventsCountRef = useRef(0);
  // One-shot consumption of the ?graphEditRun=<id> deep-link (from Chat page's
  // GraphLoop "Edit"): open that run directly in run-version edit mode.
  const editRunIntentRef = useRef<string | null>(
    typeof window !== 'undefined' ? new URLSearchParams(window.location.search).get('graphEditRun') : null,
  );

  const selectedWorkflow = useMemo(() => workflows.find((wf) => wf.id === selectedId) ?? null, [workflows, selectedId]);
  const selectedGraphNode: GraphNode | null = useMemo(() => {
    if (!selectedNodeId) return null;
    return nodes.find((n) => n.id === selectedNodeId)?.data.graphNode ?? null;
  }, [nodes, selectedNodeId]);

  useEffect(() => {
    if (isMobile && selectedNodeId && viewMode === 'canvas' && (!viewingRun || editingRun)) {
      setInspectorDrawerOpen(true);
    }
  }, [editingRun, isMobile, selectedNodeId, viewingRun, viewMode]);

  // ---- Config <-> canvas plumbing ----
  const loadConfigIntoCanvas = useCallback((config: GraphConfig) => {
    const flow = configToFlow(config);
    setNodes(markEditable(flow.nodes));
    setEdges(flow.edges);
    setMeta(metaFromConfig(config));
    setLoadedConfig(config);
    setSelectedNodeId(null);
    setInspectorDrawerOpen(true);
    setViewportResetKey((key) => key + 1);
  }, []);

  const buildConfig = useCallback((): GraphConfig => {
    const base: GraphConfig = { ...loadedConfig, ...meta };
    return flowToConfig(nodes, edges, base);
  }, [edges, loadedConfig, meta, nodes]);

  const currentFingerprint = useMemo(
    () => fingerprint({ name, description, config: buildConfig() }),
    [buildConfig, description, name],
  );
  const dirty = (!viewingRun || editingRun) && currentFingerprint !== savedFingerprint;

  // The inspector only reads variables/disabledVars/runConfig (all in `meta`),
  // so build its config from state rather than the ref-backed buildConfig().
  const inspectorConfig = useMemo<GraphConfig>(() => ({ nodes: [], edges: [], ...meta }), [meta]);

  // ---- Data loading ----
  const loadWorkflows = useCallback(async () => {
    setLoading(true);
    setMessage('');
    try {
      const res = await fetch('/api/v1/graph/workflow/list');
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphListWorkflowsResponse;
      setWorkflows(data.workflows || []);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadRuns = useCallback(async () => {
    try {
      const res = await fetch('/api/v1/graph/run/list');
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphRunHistoryResponse;
      setRuns(data.runs || []);
    } catch (err) {
      setRunMessage(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void loadWorkflows();
    void loadRuns();
    // Load agents for the inspector's Agent/model selectors.
    void (async () => {
      try {
        const res = await fetch('/api/v1/agent/list');
        const data = await res.json().catch(() => null);
        if (data?.code === 0 && Array.isArray(data.agent_list)) {
          setAgents(data.agent_list as AgentInfo[]);
        }
      } catch {
        /* agent list is optional for editing */
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(
    () => () => {
      graphEventClientRef.current?.disconnect();
      graphEventClientRef.current = null;
    },
    [],
  );

  useEffect(() => {
    runEventsCountRef.current = runEvents.length;
  }, [runEvents.length]);

  // ---- Run status / SSE ----
  const refreshRunStatus = useCallback(async (runId: string): Promise<GraphRunStatusResponse | null> => {
    try {
      const res = await fetch(`/api/v1/graph/run/${encodeURIComponent(runId)}`);
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphRunStatusResponse;
      if (data.run) setSelectedRun(data.run);
      setRunProgress(data.progress || data.run?.progress);
      const instances = data.instances || (data.progress?.instances ? Object.values(data.progress.instances) : []);
      setRunInstances(instances);
      setRunEvents(data.events || []);
      setRunMessage('');
      return data;
    } catch (err) {
      setRunMessage(err instanceof Error ? err.message : String(err));
      return null;
    }
  }, []);

  const selectRun = useCallback(
    async (run: GraphRun) => {
      graphEventClientRef.current?.disconnect();
      graphEventClientRef.current = null;
      setSelectedRun(run);
      setViewingRun(true);
      setEditingRun(false);
      setSelectedNodeId(null);
      setRunProgress(run.progress);
      setRunEvents([]);
      setRunInstances([]);
      await refreshRunStatus(run.id);
    },
    [refreshRunStatus],
  );

  const exitRunView = useCallback(() => {
    graphEventClientRef.current?.disconnect();
    graphEventClientRef.current = null;
    setViewingRun(false);
    setEditingRun(false);
    setSelectedRun(null);
  }, []);

  useEffect(() => {
    graphEventClientRef.current?.disconnect();
    graphEventClientRef.current = null;
    if (!selectedRun?.id || !isGraphRunLive(selectedRun.status)) return;

    const client = new SSEClient();
    graphEventClientRef.current = client;
    void client
      .connectUntilReady({
        url: `/api/v1/graph/run/${encodeURIComponent(selectedRun.id)}/events`,
        initialLastEventId: String(runEventsCountRef.current),
        onEvent: (event) => {
          const graphEvent = event as unknown as GraphEvent;
          setRunEvents((prev) => {
            if (prev.some((item) => item.id === graphEvent.id)) return prev;
            return [...prev, graphEvent];
          });
          if (graphEvent.progress) setRunProgress(graphEvent.progress);
          if (graphEvent.type === 'progressUpdated' || graphEvent.type === 'error' || graphEvent.type === 'instanceCompleted') {
            void loadRuns();
            void refreshRunStatus(selectedRun.id);
          }
        },
        onError: (err) => setRunMessage(err.message),
        onResumePointGone: () => {
          void refreshRunStatus(selectedRun.id);
        },
      })
      .catch((err) => setRunMessage(err instanceof Error ? err.message : String(err)));

    return () => {
      client.disconnect();
      if (graphEventClientRef.current === client) graphEventClientRef.current = null;
    };
  }, [loadRuns, refreshRunStatus, selectedRun?.id, selectedRun?.status]);

  // ---- Workflow CRUD ----
  const selectWorkflow = useCallback(
    async (workflow: GraphWorkflow) => {
      setMessage('');
      setErrors([]);
      exitRunView();
      try {
        const res = await fetch(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`);
        if (!res.ok) throw new Error(await readError(res));
        const data = (await res.json()) as GraphWorkflowResponse;
        if (!data.workflow) throw new Error(t('graph.messages.workflowEmpty'));
        setSelectedId(data.workflow.id);
        setName(data.workflow.name);
        setDescription(data.workflow.description || '');
        loadConfigIntoCanvas(data.workflow.config);
        setSavedFingerprint(fingerprint({
          name: data.workflow.name,
          description: data.workflow.description || '',
          config: data.workflow.config,
        }));
      } catch (err) {
        setMessage(err instanceof Error ? err.message : String(err));
      }
    },
    [exitRunView, loadConfigIntoCanvas],
  );

  const startNew = useCallback(() => {
    const config = cloneDefaultConfig(t, workspaceId, workspaceWorkdir);
    exitRunView();
    setSelectedId(null);
    setName(t('graph.defaultName'));
    setDescription('');
    setErrors([]);
    setMessage('');
    loadConfigIntoCanvas(config);
    setSavedFingerprint(fingerprint({ name: t('graph.defaultName'), description: '', config }));
  }, [exitRunView, loadConfigIntoCanvas, t, workspaceId, workspaceWorkdir]);

  const resetChanges = useCallback(() => {
    if (selectedWorkflow) {
      setName(selectedWorkflow.name);
      setDescription(selectedWorkflow.description || '');
      loadConfigIntoCanvas(selectedWorkflow.config);
      setSavedFingerprint(fingerprint({
        name: selectedWorkflow.name,
        description: selectedWorkflow.description || '',
        config: selectedWorkflow.config,
      }));
      setMessage(t('graph.messages.resetToSaved'));
      return;
    }
    startNew();
    setMessage(t('graph.messages.resetToDefault'));
  }, [loadConfigIntoCanvas, selectedWorkflow, startNew, t]);

  const validateConfigValue = useCallback(async (config: GraphConfig): Promise<boolean> => {
    setMessage('');
    try {
      const res = await fetch('/api/v1/graph/workflow/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ config }),
      });
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphValidationResponse;
      setErrors(data.errors || []);
      setMessage(data.valid ? t('graph.messages.validationPassed') : t('graph.messages.validationErrors', { count: data.errors?.length || 0 }));
      return data.valid;
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
      return false;
    }
  }, [t]);

  const validate = useCallback(async (): Promise<GraphConfig | null> => {
    const config = buildConfig();
    return (await validateConfigValue(config)) ? config : null;
  }, [buildConfig, validateConfigValue]);

  const save = useCallback(
    async (mode: SaveMode) => {
      const trimmedName = name.trim();
      if (!trimmedName) {
        setMessage(t('graph.messages.nameRequired'));
        return;
      }
      const config = buildConfig();
      setSaving(true);
      setMessage('');
      setErrors([]);
      try {
        const url = mode === 'create' ? '/api/v1/graph/workflow' : `/api/v1/graph/workflow/${encodeURIComponent(selectedId || '')}`;
        const method = mode === 'create' ? 'POST' : 'PUT';
        const body =
          mode === 'create' ? { name: trimmedName, description, workspaceId, config } : { name: trimmedName, description, config };
        const res = await fetch(url, {
          method,
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!res.ok) {
          const data = (await res.json().catch(() => null)) as GraphWorkflowResponse | null;
          if (Array.isArray(data?.errors)) {
            setErrors(data.errors);
            setMessage(t('graph.messages.validationErrors', { count: data.errors.length }));
            return;
          }
          throw new Error((data as { msg?: string; error?: string } | null)?.msg || (data as { error?: string } | null)?.error || `HTTP ${res.status}`);
        }
        const data = (await res.json()) as GraphWorkflowResponse;
        if (!data.workflow) throw new Error(t('graph.messages.workflowEmpty'));
        setSelectedId(data.workflow.id);
        setName(data.workflow.name);
        setDescription(data.workflow.description || '');
        loadConfigIntoCanvas(data.workflow.config);
        setSavedFingerprint(fingerprint({
          name: data.workflow.name,
          description: data.workflow.description || '',
          config: data.workflow.config,
        }));
        setMessage(mode === 'create' ? t('graph.messages.workflowCreated') : t('graph.messages.workflowSaved'));
        await loadWorkflows();
        await loadRuns();
      } catch (err) {
        setMessage(err instanceof Error ? err.message : String(err));
      } finally {
        setSaving(false);
      }
    },
    [buildConfig, description, loadConfigIntoCanvas, loadRuns, loadWorkflows, name, selectedId, t, workspaceId],
  );

  const confirmDelete = useCallback(async () => {
    if (!deleteTarget) return;
    setMessage('');
    try {
      const res = await fetch(`/api/v1/graph/workflow/${encodeURIComponent(deleteTarget.id)}`, { method: 'DELETE' });
      if (!res.ok) throw new Error(await readError(res));
      if (selectedId === deleteTarget.id) startNew();
      setDeleteTarget(null);
      setMessage(t('graph.messages.workflowDeleted'));
      await loadWorkflows();
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  }, [deleteTarget, loadWorkflows, selectedId, startNew, t]);

  // ---- Run ----
  const startRun = useCallback(async () => {
    const config = await validate();
    if (!config) {
      setMessage(t('graph.messages.fixValidationFirst'));
      return;
    }
    setMessage('');
    try {
      const res = await fetch('/api/v1/graph/run/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workflowId: selectedId || undefined, workspaceId, workdir: workspaceWorkdir, config }),
      });
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as { run?: GraphRun };
      // A run binds a Graph-type Job; jump into the Chat page for it like the
      // startloop flow does, instead of showing the run inline on the canvas.
      if (data.run?.jobId) {
        onRunStarted(data.run.jobId);
        return;
      }
      setMessage(t('graph.messages.runStartedNoJob'));
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  }, [onRunStarted, selectedId, t, validate, workspaceId, workspaceWorkdir]);

  // ---- Run-time version editing (§4 运行配置与版本化编辑) ----
  // Enter an editable canvas seeded from the selected run's current effective
  // version. Only allowed while the run is editable (in-flight / resumable).
  const enterRunEdit = useCallback(() => {
    if (!selectedRun || !isGraphRunEditable(selectedRun.status)) {
      setRunMessage(t('graph.messages.runNotEditable'));
      return;
    }
    const config = runConfigSnapshot(selectedRun);
    loadConfigIntoCanvas(config);
    setErrors([]);
    setEditingRun(true);
    // Seed the saved fingerprint to the loaded snapshot so the dirty badge only
    // lights up once the user actually changes something.
    setSavedFingerprint(fingerprint({ name, description, config }));
    setMessage(t('graph.messages.editingRunHint'));
  }, [description, loadConfigIntoCanvas, name, selectedRun, t]);

  const cancelRunEdit = useCallback(() => {
    setEditingRun(false);
    setErrors([]);
    setMessage('');
    if (selectedRun) loadConfigIntoCanvas(runConfigSnapshot(selectedRun));
  }, [loadConfigIntoCanvas, selectedRun]);

  const saveRunVersion = useCallback(async () => {
    if (!selectedRun) return;
    const config = buildConfig();
    setSaving(true);
    setMessage('');
    setErrors([]);
    try {
      const res = await fetch(`/api/v1/graph/run/${encodeURIComponent(selectedRun.id)}/version`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ config }),
      });
      if (!res.ok) {
        const data = (await res.json().catch(() => null)) as GraphWorkflowResponse | null;
        if (Array.isArray(data?.errors) && data.errors.length > 0) {
          setErrors(data.errors);
          setMessage(t('graph.messages.validationErrors', { count: data.errors.length }));
          return;
        }
        const detail =
          (data as { msg?: string; error?: string } | null)?.msg ||
          (data as { error?: string } | null)?.error ||
          `HTTP ${res.status}`;
        // 409 = run moved to a non-editable state between entering and saving.
        setMessage(res.status === 409 ? t('graph.messages.runNotEditableDetail', { detail }) : detail);
        return;
      }
      const data = (await res.json()) as { run?: GraphRun };
      if (data.run) {
        setSelectedRun(data.run);
        setSavedFingerprint(fingerprint({ name, description, config }));
      }
      setEditingRun(false);
      setMessage(t('graph.messages.runVersionSaved'));
      await loadRuns();
      await refreshRunStatus(selectedRun.id);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  }, [buildConfig, description, loadRuns, name, refreshRunStatus, selectedRun, t]);

  // Open a run directly in run-version edit mode (used by the ?graphEditRun
  // deep-link). Seeds the canvas from the run's current effective version.
  const openRunInEdit = useCallback(
    async (run: GraphRun) => {
      await selectRun(run);
      if (!isGraphRunEditable(run.status)) {
        setRunMessage(t('graph.messages.runNotEditable'));
        return;
      }
      const config = runConfigSnapshot(run);
      loadConfigIntoCanvas(config);
      setErrors([]);
      setEditingRun(true);
      setSavedFingerprint(fingerprint({ name, description, config }));
      setMessage(t('graph.messages.editingRunHint'));
    },
    [description, loadConfigIntoCanvas, name, selectRun, t],
  );

  // Consume the ?graphEditRun deep-link once runs are loaded: find the run and
  // open it in edit mode, then strip the param so reloads don't re-trigger.
  useEffect(() => {
    const intent = editRunIntentRef.current;
    if (!intent || runs.length === 0) return;
    editRunIntentRef.current = null;
    const run = runs.find((r) => r.id === intent);
    const url = new URL(window.location.href);
    url.searchParams.delete('graphEditRun');
    window.history.replaceState({}, '', url.toString());
    if (run) void openRunInEdit(run);
    else setRunMessage(t('graph.messages.runNotFound', { id: intent }));
  }, [openRunInEdit, runs, t]);

  // ---- Canvas editing handlers ----
  const onNodesChange = useCallback((changes: NodeChange[]) => {
    setNodes((prev) => applyNodeChanges(changes, prev) as QuartetFlowNode[]);
  }, []);
  const onEdgesChange = useCallback((changes: EdgeChange[]) => {
    setEdges((prev) => applyEdgeChanges(changes, prev) as QuartetFlowEdge[]);
  }, []);
  const onConnect = useCallback((connection: Connection) => {
    setEdges((prev) => {
      const port = connection.sourceHandle === 'yes' || connection.sourceHandle === 'no' ? connection.sourceHandle : undefined;
      nodeCounterRef.current += 1;
      const edge: QuartetFlowEdge = {
        id: `edge-${connection.source}-${connection.target}-${nodeCounterRef.current}`,
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

  const onAddNode = useCallback((type: GraphNodeType, position: { x: number; y: number }) => {
    nodeCounterRef.current += 1;
    const id = `${type}-${nodeCounterRef.current}`;
    const graphNode: GraphNode = {
      id,
      type,
      title: '',
      config: type === 'loop' ? { loopMode: 'fixed', fixedCount: 1, maxIterations: 100 } : type === 'evaluator' ? { sessionStrategy: 'new' } : type === 'prompt' ? { sessionStrategy: 'new' } : {},
      layout: { x: Math.round(position.x), y: Math.round(position.y), ...(type === 'loop' ? { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT } : {}) },
    };
    const node: QuartetFlowNode = {
      id,
      type: type === 'loop' ? 'loopGroup' : 'quartet',
      position,
      data: { kind: type, graphNode },
      deletable: type !== 'start' && type !== 'end',
      ...(type === 'loop' ? { style: { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT } } : {}),
    };
    setNodes((prev) => [...prev, node]);
    setSelectedNodeId(id);
  }, []);

  const patchGraphNode = useCallback((id: string, mutate: (gn: GraphNode) => GraphNode) => {
    setNodes((prev) =>
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
  const onDeleteNode = useCallback((id: string) => {
    setNodes((prev) => prev.filter((n) => n.id !== id && n.parentId !== id));
    setEdges((prev) => prev.filter((e) => e.source !== id && e.target !== id));
    setSelectedNodeId((cur) => (cur === id ? null : cur));
  }, []);

  const onUpdateVariables = useCallback((variables: Record<string, string>, disabledVars: string[]) => {
    setMeta((prev) => ({ ...prev, variables, disabledVars }));
  }, []);
  const onUpdateRunConfig = useCallback((patch: Partial<GraphRunConfig>) => {
    setMeta((prev) => ({ ...prev, runConfig: { ...prev.runConfig, ...patch } }));
  }, []);
  const onViewportChange = useCallback((viewport: Viewport) => {
    setMeta((prev) => ({ ...prev, canvas: { viewport } }));
  }, []);

  // ---- Validation error -> canvas highlight + focus ----
  const errorNodeIds = useMemo(() => new Set(errors.map((e) => e.nodeId).filter(Boolean) as string[]), [errors]);
  const errorEdgeIds = useMemo(() => new Set(errors.map((e) => e.edgeId).filter(Boolean) as string[]), [errors]);
  const focusError = useCallback((err: GraphValidationError) => {
    if (err.nodeId) setFocus({ nodeId: err.nodeId, token: Date.now() });
  }, []);

  // ---- Run replay flow ----
  // In pure replay (viewingRun && !editingRun) the canvas is driven directly by
  // the run snapshot. Once editing, the editable nodes/edges state (seeded by
  // enterRunEdit) is the source of truth so user changes are captured.
  const replayFlow = useMemo(() => {
    if (!viewingRun || editingRun || !selectedRun) return null;
    return configToFlow(runConfigSnapshot(selectedRun));
  }, [editingRun, selectedRun, viewingRun]);
  const replayRunStatus = useMemo(() => runStatusByNode(runInstances), [runInstances]);
  const canvasInitialViewport = viewingRun && !editingRun && selectedRun ? runConfigSnapshot(selectedRun).canvas?.viewport : meta.canvas?.viewport;

  // Nodes whose execution config is frozen during run-time editing: those with a
  // succeeded / skipped / running instance (mirrors backend validateVersionEdit).
  // The inspector disables their config fields; the backend still enforces.
  const frozenRunNodeIds = useMemo(() => {
    if (!editingRun) return undefined;
    const frozen = new Set<string>();
    for (const inst of runInstances) {
      if (inst.status === 'succeeded' || inst.status === 'skipped' || inst.status === 'running') {
        frozen.add(inst.nodeId);
      }
    }
    return frozen;
  }, [editingRun, runInstances]);

  const canEditSelectedRun = !!selectedRun && isGraphRunEditable(selectedRun.status);

  // ---- JSON advanced view ----
  const switchView = useCallback(
    (mode: ViewMode) => {
      if (mode === 'json') setJsonText(JSON.stringify(buildConfig(), null, 2));
      setViewMode(mode);
    },
    [buildConfig],
  );
  const applyJson = useCallback(() => {
    try {
      const parsed = JSON.parse(jsonText) as GraphConfig;
      if (!parsed || !Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges)) {
        throw new Error(t('graph.messages.configNeedsNodesEdges'));
      }
      loadConfigIntoCanvas(parsed);
      setMessage(t('graph.messages.jsonApplied'));
      setViewMode('canvas');
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  }, [jsonText, loadConfigIntoCanvas, t]);

  const canvasNodes = viewingRun && replayFlow ? markEditable(replayFlow.nodes) : nodes;
  const canvasEdges = viewingRun && replayFlow ? replayFlow.edges : edges;

  return (
    <div className="graph-page">
      <header className="chatbot-header graph-page-header">
        <div className="header-left">
          <button className="back-button" onClick={onClose} aria-label={t('graph.header.back')}>
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M15 18l-6-6 6-6" />
            </svg>
          </button>
          <span className="header-logo">
            <span className="graph-header-mark">◇</span>
            <span className="header-logo-text">{t('graph.header.title')}</span>
          </span>
        </div>
        <nav className="header-nav">
          <button
            className="header-settings-btn"
            onClick={() => {
              void loadWorkflows();
              void loadRuns();
            }}
            title={t('graph.header.refresh')}
            aria-label={t('graph.header.refresh')}
          >
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M21 12a9 9 0 1 1-2.64-6.36" />
              <polyline points="21 3 21 9 15 9" />
            </svg>
          </button>
        </nav>
      </header>

      <main className="graph-page-main">
        <aside className="graph-sidebar">
          <div className="graph-sidebar-top">
            <div>
              <div className="graph-kicker">{workspaceTitle || workspaceId || t('graph.sidebar.workspace')}</div>
              <h2>{t('graph.sidebar.library')}</h2>
            </div>
            <button className="graph-primary-icon-btn" onClick={startNew} title={t('graph.sidebar.newWorkflow')} aria-label={t('graph.sidebar.newWorkflow')}>
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M12 5v14" />
                <path d="M5 12h14" />
              </svg>
            </button>
          </div>

          <div className="graph-workflow-list">
            {loading ? (
              <div className="graph-empty">{t('graph.sidebar.loadingWorkflows')}</div>
            ) : workflows.length === 0 ? (
              <div className="graph-empty">{t('graph.sidebar.noWorkflows')}</div>
            ) : (
              workflows.map((wf) => (
                <button
                  key={wf.id}
                  type="button"
                  className={`graph-workflow-row ${wf.id === selectedId ? 'active' : ''}`}
                  data-testid={`graph-workflow-row-${wf.id}`}
                  onClick={() => void selectWorkflow(wf)}
                >
                  <span className="graph-workflow-row-title">{wf.name}</span>
                  <span className="graph-workflow-row-meta">{summarizeWorkflow(wf, t)}</span>
                  <span className="graph-workflow-row-date">{formatDate(wf.updatedAt)}</span>
                </button>
              ))
            )}
          </div>

        </aside>

        <section className="graph-editor">
          <div className="graph-editor-head">
            <div className="graph-editor-title">
              {dirty ? <span className="graph-dirty-badge" data-testid="graph-dirty-badge">{t('graph.editor.unsaved')}</span> : <span className="graph-clean-badge" data-testid="graph-clean-badge">{t('graph.editor.saved')}</span>}
              <input className="graph-name-input" data-testid="graph-name-input" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('graph.editor.namePlaceholder')} />
            </div>
            <div className="graph-editor-actions">
              <div className="graph-view-toggle">
                <button data-testid="graph-view-canvas" className={viewMode === 'canvas' ? 'active' : ''} onClick={() => switchView('canvas')}>
                  <GraphButtonIcon name="canvas" />
                  {t('graph.editor.canvas')}
                </button>
                <button data-testid="graph-view-json" className={viewMode === 'json' ? 'active' : ''} onClick={() => switchView('json')}>
                  <GraphButtonIcon name="json" />
                  {t('graph.editor.json')}
                </button>
              </div>
              {viewingRun ? (
                editingRun ? (
                  <>
                    <button
                      className="graph-primary-btn"
                      data-testid="graph-save-run-version"
                      onClick={() => void saveRunVersion()}
                      disabled={saving}
                    >
                      <GraphButtonIcon name="save" />
                      {saving ? t('graph.editor.saving') : t('graph.editor.saveRunVersion')}
                    </button>
                    <button className="graph-secondary-btn" data-testid="graph-cancel-run-edit" onClick={cancelRunEdit} disabled={saving}>
                      <GraphButtonIcon name="cancel" />
                      {t('graph.editor.cancelEdit')}
                    </button>
                  </>
                ) : (
                  <>
                    {canEditSelectedRun && (
                      <button className="graph-secondary-btn" data-testid="graph-edit-run" onClick={enterRunEdit}>
                        <GraphButtonIcon name="edit" />
                        {t('graph.editor.editRunVersion')}
                      </button>
                    )}
                    <button className="graph-secondary-btn" data-testid="graph-exit-run" onClick={exitRunView}>
                      <GraphButtonIcon name="back" />
                      {t('graph.editor.backToEdit')}
                    </button>
                  </>
                )
              ) : (
                <>
                  <button className="graph-secondary-btn" data-testid="graph-validate" onClick={() => void validate()} disabled={saving}>
                    <GraphButtonIcon name="check" />
                    {t('graph.editor.validate')}
                  </button>
                  <button className="graph-secondary-btn" data-testid="graph-reset" onClick={resetChanges} disabled={saving || !dirty}>
                    <GraphButtonIcon name="reset" />
                    {t('graph.editor.reset')}
                  </button>
                  {selectedWorkflow && (
                    <button className="graph-danger-btn" onClick={() => setDeleteTarget(selectedWorkflow)} disabled={saving}>
                      <GraphButtonIcon name="trash" />
                      {t('graph.editor.delete')}
                    </button>
                  )}
                  <button className="graph-secondary-btn" data-testid="graph-save-as-new" onClick={() => void save('create')} disabled={saving}>
                    <GraphButtonIcon name="copy" />
                    {t('graph.editor.saveAsNew')}
                  </button>
                  <button className="graph-primary-btn" data-testid="graph-save" onClick={() => void save(selectedWorkflow ? 'update' : 'create')} disabled={saving}>
                    <GraphButtonIcon name="save" />
                    {selectedWorkflow ? t('graph.editor.save') : t('graph.editor.create')}
                  </button>
                  <button className="graph-run-btn" data-testid="graph-run" onClick={() => void startRun()} disabled={saving}>
                    <GraphButtonIcon name="play" />
                    {t('graph.editor.run')}
                  </button>
                </>
              )}
            </div>
          </div>

          {viewingRun && (
            <div className="graph-run-strip">
              <span className={`graph-run-status status-${selectedRun?.status}`} data-testid="graph-run-strip-status">{selectedRun?.status ? t(`graph.status.${selectedRun.status}`) : ''}</span>
              <span className="graph-run-progress" data-testid="graph-run-strip-progress">{graphProgressLabel(t, runProgress)}</span>
              {editingRun && <span className="graph-run-editing-badge" data-testid="graph-run-editing-badge">{t('graph.editor.editingRunVersion')}</span>}
              {selectedRun?.lastError?.message && <span className="graph-run-strip-error">{selectedRun.lastError.message}</span>}
              {runMessage && <span className="graph-run-strip-error" data-testid="graph-run-strip-message">{runMessage}</span>}
            </div>
          )}

          <div className="graph-editor-body">
            {viewMode === 'json' ? (
              <div className="graph-json-pane">
                <textarea spellCheck={false} data-testid="graph-json-textarea" value={jsonText} onChange={(e) => setJsonText(e.target.value)} />
                <div className="graph-json-actions">
                  <button className="graph-primary-btn" data-testid="graph-json-apply" onClick={applyJson}>
                    <GraphButtonIcon name="apply" />
                    {t('graph.editor.applyToCanvas')}
                  </button>
                </div>
              </div>
            ) : (
              <>
                <GraphCanvas
                  nodes={canvasNodes}
                  edges={canvasEdges}
                  readOnly={viewingRun && !editingRun}
                  showMiniMap={!isMobile}
                  runStatusByNodeId={viewingRun ? replayRunStatus : undefined}
                  errorNodeIds={viewingRun && !editingRun ? undefined : errorNodeIds}
                  errorEdgeIds={viewingRun && !editingRun ? undefined : errorEdgeIds}
                  focus={focus}
                  initialViewport={canvasInitialViewport}
                  viewportResetKey={viewportResetKey}
                  onNodesChange={onNodesChange}
                  onEdgesChange={onEdgesChange}
                  onConnect={onConnect}
                  onNodeClick={(id) => {
                    setSelectedNodeId(id);
                    if (isMobile) setInspectorDrawerOpen(true);
                  }}
                  onPaneClick={() => {
                    setSelectedNodeId(null);
                  }}
                  onAddNode={onAddNode}
                  onViewportChange={onViewportChange}
                />
                {(!viewingRun || editingRun) && (
                  <GraphInspector
                    node={selectedGraphNode}
                    config={inspectorConfig}
                    agents={agents}
                    drawerOpen={!isMobile || inspectorDrawerOpen}
                    frozenNodeIds={frozenRunNodeIds}
                    onUpdateNode={onUpdateNode}
                    onUpdateNodeConfig={onUpdateNodeConfig}
                    onDeleteNode={onDeleteNode}
                    onUpdateVariables={onUpdateVariables}
                    onUpdateRunConfig={onUpdateRunConfig}
                    onDrawerToggle={() => setInspectorDrawerOpen((open) => !open)}
                  />
                )}
              </>
            )}
          </div>

          {(message || errors.length > 0) && (
            <div className={`graph-status ${errors.length > 0 ? 'error' : ''}`} data-testid="graph-status">
              {message && <div data-testid="graph-message">{message}</div>}
              {errors.length > 0 && (
                <ul data-testid="graph-error-list">
                  {errors.map((err, index) => (
                    <li key={`${err.type}-${err.nodeId || err.edgeId || err.variable || err.configKey || index}`}>
                      <button type="button" className="graph-error-link" data-testid="graph-error-link" onClick={() => focusError(err)}>
                        {makeValidationLabel(err)}
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </section>
      </main>

      {deleteTarget && (
        <div className="delete-confirm-overlay" onClick={() => setDeleteTarget(null)}>
          <div className="delete-confirm-dialog" onClick={(e) => e.stopPropagation()}>
            <h3>{t('graph.dialogs.deleteWorkflowTitle')}</h3>
            <p>{t('graph.dialogs.deleteWorkflowBody', { name: deleteTarget.name })}</p>
            <div className="delete-confirm-actions">
              <button className="delete-confirm-cancel" onClick={() => setDeleteTarget(null)}>
                <GraphButtonIcon name="cancel" />
                {t('graph.dialogs.cancel')}
              </button>
              <button className="delete-confirm-ok" onClick={() => void confirmDelete()}>
                <GraphButtonIcon name="trash" />
                {t('graph.dialogs.delete')}
              </button>
            </div>
          </div>
        </div>
      )}

    </div>
  );
}
