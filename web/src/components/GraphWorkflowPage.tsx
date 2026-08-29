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
  type IsValidConnection,
  type NodeChange,
  type Viewport,
} from '@xyflow/react';
import type {
  GraphConfig,
  GraphEdgeState,
  GraphEvent,
  GraphHookResult,
  GraphHookResultsResponse,
  GraphInstanceState,
  GraphListWorkflowsResponse,
  GraphNode,
  GraphNodeConfig,
  GraphNodeType,
  GraphProgress,
  GraphRun,
  GraphRunConfig,
  GraphRunStatus,
  GraphRunStatusResponse,
  GraphValidationError,
  GraphValidationResponse,
  GraphWorkflow,
  GraphWorkflowResponse,
  GraphWorkflowSummary,
  GraphWorkflowType,
  GraphWorkflowWarning,
  AgentInfo,
  WorkspaceInfo,
} from '../types';
import { GraphSSEClient } from '../utils/graph-sse-client';
import { useIsMobile } from '../hooks/useIsMobile';
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
import { createEditorNode, filterNodeRemoveChanges, isNodeDeletable, markEditableNodes } from './graph/editorModel';
import { GraphCanvas, type GraphCanvasFocus } from './graph/GraphCanvas';
import { GraphInspector, BUILTIN_VARS } from './graph/GraphInspector';
import { GraphRunInspector } from './graph/GraphRunInspector';
import { registerWorkspaceColors, workspaceColor } from '../utils/workspace';
import { useAuthPrincipal } from '../auth';
import { HomeNavigation } from './HomeNavigation';
import { fetchAvailableAgentList } from '../api/agents';
import './GraphWorkflowPage.css';

type WorkspaceItem = WorkspaceInfo;

interface GraphWorkflowPageProps {
  workspaceId?: string;
  workspaceTitle?: string;
  workspaceWorkdir?: string;
  onClose: () => void;
  onDirtyChange?: (dirty: boolean) => void;
  navigationRefreshKey?: number;
  onOpenSettings?: () => void;
  onOpenStats?: () => void;
  onOpenGraph?: () => void;
  // Called after a run is started so the app can jump into the Chat page for the
  // bound Graph Job, mirroring the normal Graph run launch flow.
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
  canvas?: GraphConfig['canvas'];
}

// One undo/redo history entry: the full editable canvas state (structural
// nodes/edges + global meta) at a point in time. nodes/edges/meta are always
// replaced wholesale on edit (never mutated in place), so holding their
// references here is a safe immutable snapshot. The canvas viewport inside meta
// is intentionally NOT restored on undo (see applySnapshot) — pan/zoom is pure
// view state and undoing an edit should not yank the camera around.
interface CanvasSnapshot {
  nodes: QuartetFlowNode[];
  edges: QuartetFlowEdge[];
  meta: ConfigMeta;
}

// Cap the undo depth so a long editing session cannot grow history without
// bound. Snapshots mostly share references with live state, so this is cheap.
const HISTORY_LIMIT = 100;

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
  runConfig: { concurrencyLimit: 8 },
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

// Pin the always-present built-in variables (Code/Doc) into a variable map
// without dropping or reordering existing entries. Built-ins go first so they
// head the table; a built-in already present keeps its value. Returns a new map
// (never mutates the input).
function withBuiltinVars(variables: Record<string, string> | undefined): Record<string, string> {
  const src = variables || {};
  const next: Record<string, string> = {};
  for (const name of BUILTIN_VARS) next[name] = src[name] ?? '';
  for (const [k, v] of Object.entries(src)) next[k] = v;
  return next;
}

function metaFromConfig(config: GraphConfig): ConfigMeta {
  return {
    variables: withBuiltinVars(config.variables),
    disabledVars: [...(config.disabledVars || [])],
    runConfig: { ...(config.runConfig || {}) },
    workspaceId: config.workspaceId,
    workdir: config.workdir,
    canvas: config.canvas,
  };
}

function canonicalWorkflowConfig(workflow: GraphWorkflow): GraphConfig {
  return {
    ...workflow.config,
    // Legacy workflow records may only carry workspaceId at the record level.
    workspaceId: workflow.config.workspaceId || workflow.workspaceId,
  };
}

function configFingerprint(config: GraphConfig): string {
  const { canvas: _canvas, ...configWithoutCanvas } = config;
  return stableStringify({ ...configWithoutCanvas, variables: withBuiltinVars(configWithoutCanvas.variables) });
}

function graphNodeLabel(node: GraphNode | undefined, fallback: string): string {
  return node?.title || node?.id || fallback;
}

function seedCounterFromIds(ids: string[]): number {
  let max = 0;
  for (const id of ids) {
    const match = /-(\d+)$/.exec(id);
    if (!match) continue;
    const n = Number(match[1]);
    if (Number.isInteger(n) && n > max) max = n;
  }
  return max;
}

// Diagonal nudge applied to pasted/duplicated root nodes so the copy is offset
// from the original instead of landing exactly on top of it.
const PASTE_OFFSET = 36;

// Absolute (flow-space) top-left of a node, walking up the parent chain since
// React Flow stores child positions relative to their parent.
function absoluteOf(node: QuartetFlowNode, byId: Map<string, QuartetFlowNode>): { x: number; y: number } {
  let x = node.position.x;
  let y = node.position.y;
  let pid = node.parentId;
  const seen = new Set<string>();
  while (pid && !seen.has(pid)) {
    seen.add(pid);
    const parent = byId.get(pid);
    if (!parent) break;
    x += parent.position.x;
    y += parent.position.y;
    pid = parent.parentId;
  }
  return { x, y };
}

// Build a self-contained selection snapshot from a set of directly-selected node
// ids, for copy / duplicate. The result is independent of the live canvas so it
// can be pasted later (possibly multiple times):
//   - the closure pulls in every descendant of a selected node, so selecting a
//     loop container copies its entry/exit markers and whole body with it;
//   - a top-level start/end is the workflow's single entry/exit and is never
//     copied; a loop marker (parented start/end) only travels with its loop, so
//     an orphan marker selected without its container is dropped;
//   - any kept node whose parent is NOT in the selection becomes a "root": its
//     position is flattened to absolute flow-space and its parentId/nesting is
//     cleared, so it pastes onto the open canvas;
//   - only edges whose both endpoints are kept are included (edges crossing the
//     selection boundary are dropped — the other end would not exist).
function collectSelection(
  allNodes: QuartetFlowNode[],
  allEdges: QuartetFlowEdge[],
  rootSelected: Set<string>,
): { nodes: QuartetFlowNode[]; edges: QuartetFlowEdge[] } {
  const byId = new Map(allNodes.map((n) => [n.id, n]));
  const childrenOf = new Map<string, string[]>();
  for (const n of allNodes) {
    if (!n.parentId) continue;
    const list = childrenOf.get(n.parentId);
    if (list) list.push(n.id);
    else childrenOf.set(n.parentId, [n.id]);
  }

  // Expand the selection to every descendant.
  const ids = new Set<string>();
  const stack = [...rootSelected];
  while (stack.length) {
    const id = stack.pop() as string;
    if (ids.has(id) || !byId.has(id)) continue;
    ids.add(id);
    for (const child of childrenOf.get(id) || []) stack.push(child);
  }

  const kept = allNodes.filter((n) => {
    if (!ids.has(n.id)) return false;
    const isControl = n.data.kind === 'start' || n.data.kind === 'end';
    if (isControl && !n.parentId) return false; // workflow entry/exit singleton
    if (isControl && n.parentId && !ids.has(n.parentId)) return false; // orphan loop marker
    return true;
  });
  const keptIds = new Set(kept.map((n) => n.id));

  const nodes = kept.map((n) => {
    const graphNode: GraphNode = { ...n.data.graphNode };
    const clone: QuartetFlowNode = { ...n, selected: false, data: { ...n.data, graphNode } };
    delete (clone as { measured?: unknown }).measured;
    delete (clone as { dragging?: unknown }).dragging;
    // A node whose parent is outside the selection is promoted to a root: pin it
    // at its current absolute position and drop the now-dangling parent link.
    if (n.parentId && !keptIds.has(n.parentId)) {
      const abs = absoluteOf(n, byId);
      clone.position = { x: abs.x, y: abs.y };
      delete clone.parentId;
      Object.assign(clone, clearNestConstraint);
      delete graphNode.parentId;
      graphNode.layout = { ...(graphNode.layout || {}), x: Math.round(abs.x), y: Math.round(abs.y) };
    }
    return clone;
  });

  const edges = allEdges
    .filter((e) => keptIds.has(e.source) && keptIds.has(e.target))
    .map((e) => ({ ...e, data: e.data ? { ...e.data } : e.data }));

  return { nodes, edges };
}

// Clone a selection snapshot with fresh, collision-free ids, ready to insert.
// `mintId` mints a unique id for a given prefix (checking + reserving against
// `taken`). Root nodes (no in-selection parent) get the paste offset; children
// keep their parent-relative positions so each subtree moves as a unit.
function cloneSelection(
  selection: { nodes: QuartetFlowNode[]; edges: QuartetFlowEdge[] },
  mintId: (prefix: string, taken: Set<string>) => string,
  takenNodeIds: Set<string>,
  takenEdgeIds: Set<string>,
  offset: number,
): { nodes: QuartetFlowNode[]; edges: QuartetFlowEdge[]; rootIds: string[] } {
  const srcIds = new Set(selection.nodes.map((n) => n.id));
  const idMap = new Map<string, string>();
  for (const n of selection.nodes) idMap.set(n.id, mintId(n.data.kind, takenNodeIds));

  const rootIds: string[] = [];
  const nodes = selection.nodes.map((n) => {
    const newId = idMap.get(n.id) as string;
    const newParentId = n.parentId && srcIds.has(n.parentId) ? idMap.get(n.parentId) : undefined;
    const position = newParentId
      ? { x: n.position.x, y: n.position.y }
      : { x: n.position.x + offset, y: n.position.y + offset };
    if (!newParentId) rootIds.push(newId);

    const graphNode: GraphNode = {
      ...n.data.graphNode,
      id: newId,
      layout: { ...(n.data.graphNode.layout || {}), x: Math.round(position.x), y: Math.round(position.y) },
    };
    if (newParentId) graphNode.parentId = newParentId;
    else delete graphNode.parentId;

    const clone: QuartetFlowNode = { ...n, id: newId, position, selected: true, data: { ...n.data, graphNode } };
    if (newParentId) {
      clone.parentId = newParentId;
      Object.assign(clone, nestConstraint(n.data.kind));
    } else {
      delete clone.parentId;
      Object.assign(clone, clearNestConstraint);
    }
    return clone;
  });

  const edges = selection.edges.map((e) => {
    const source = idMap.get(e.source) as string;
    const target = idMap.get(e.target) as string;
    return { ...e, id: mintId(`edge-${source}-${target}`, takenEdgeIds), source, target };
  });

  return { nodes, edges, rootIds };
}

function formatDate(value: string): string {
  if (!value) return '-';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return value;
  const now = new Date();
  return new Intl.DateTimeFormat(i18n.language || undefined, {
    year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric',
    month: 'short',
    day: 'numeric',
  }).format(d);
}

function isGraphRunLive(status?: GraphRunStatus): boolean {
  return status === 'pending' || status === 'running' || status === 'stepStopping';
}

// A GraphRun accepts version edits while it is still actively scheduling or in a
// resumable static state. A naturally completed run is frozen (read-only
// replay). Mirrors GraphRunProgress and the backend editable set.
function isGraphRunEditable(status?: GraphRunStatus): boolean {
  return (
    status === 'running' ||
    status === 'stepStopping' ||
    status === 'recovering' ||
    status === 'stepStopped' ||
    status === 'stopped' ||
    status === 'failed' ||
    status === 'timedOut' ||
    status === 'awaitingInput'
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
  const body = await res.text().catch(() => '');
  const trimmed = body.trim();
  if (trimmed) {
    try {
      const data = JSON.parse(trimmed);
      if (Array.isArray(data?.errors) && data.errors.length > 0) {
        return data.errors.map(makeValidationLabel).join('\n');
      }
      return data?.msg || data?.error || data?.message || trimmed;
    } catch {
      return trimmed;
    }
  }
  return `HTTP ${res.status}`;
}

// Resolve the config a GraphRun executed against lives in graphFlowAdapter
// (runConfigSnapshot), shared with GraphRunProgress.
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
  // Normalize built-in variables so a legacy config (no Code/Doc) and the
  // editor's auto-filled state hash identically — opening such a workflow must
  // not register as an unsaved change.
  const normalized = { ...configWithoutCanvas, variables: withBuiltinVars(configWithoutCanvas.variables) };
  return stableStringify({ name: input.name, description: input.description, config: normalized });
}

function workflowToSummary(workflow: GraphWorkflow | GraphWorkflowSummary): GraphWorkflowSummary {
  if (!('config' in workflow)) return workflow;
  return {
    id: workflow.id,
    workspaceId: workflow.workspaceId || workflow.config?.workspaceId,
    name: workflow.name,
    description: workflow.description,
    type: workflow.type,
    createdAt: workflow.createdAt,
    updatedAt: workflow.updatedAt,
    nodeCount: workflow.config?.nodes?.length || 0,
    edgeCount: workflow.config?.edges?.length || 0,
  };
}

function upsertWorkflow(list: GraphWorkflowSummary[], workflow: GraphWorkflow | GraphWorkflowSummary): GraphWorkflowSummary[] {
  const summary = workflowToSummary(workflow);
  const exists = list.some((wf) => wf.id === summary.id);
  if (exists) return list.map((wf) => (wf.id === summary.id ? summary : wf));
  return [summary, ...list];
}

export function GraphWorkflowPage({
  workspaceId,
  workspaceTitle,
  workspaceWorkdir,
  onClose,
  onDirtyChange,
  navigationRefreshKey,
  onOpenSettings,
  onOpenStats,
  onOpenGraph,
  onRunStarted,
}: GraphWorkflowPageProps) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const canWriteWorkflows = principal?.permissions.includes('workflow.write') ?? false;
  const canExecuteWorkflows = principal?.permissions.includes('workflow.execute') ?? false;
  const canExecuteJobs = principal?.permissions.includes('job.execute') ?? false;
  const canReadAgents = principal?.permissions.includes('agent.read') ?? false;
  const canReadWorkspaces = principal?.permissions.includes('workspace.read') ?? false;
  const isMobile = useIsMobile();
  const [workflows, setWorkflows] = useState<GraphWorkflowSummary[]>([]);
  // Which library tab is active: 'user' = workflows authored in this UI,
  // 'agent' = workflows created/managed by a model through the CLI. The list is
  // filtered to the active tab; the UI only ever creates 'user' workflows.
  const [activeTab, setActiveTab] = useState<GraphWorkflowType>('user');
  // Warnings for workflow files the backend skipped during list (unreadable /
  // malformed JSON). Surfaced in the status area so a corrupt file does not just
  // silently vanish from the library.
  const [listWarnings, setListWarnings] = useState<GraphWorkflowWarning[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [selectedWorkflowUpdatedAt, setSelectedWorkflowUpdatedAt] = useState<string | undefined>();
  const [name, setName] = useState(() => i18n.t('graph.defaultName'));
  const [description, setDescription] = useState('');
  const [errors, setErrors] = useState<GraphValidationError[]>([]);
  const [message, setMessage] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [validating, setValidating] = useState(false);
  // Guards the Run button so a rapid double-click cannot fire two concurrent
  // /graph/run/start calls (each would create its own Graph Job).
  const [startingRun, setStartingRun] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<GraphWorkflowSummary | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [agentListError, setAgentListError] = useState('');
  const [viewMode, setViewMode] = useState<ViewMode>('canvas');
  const [jsonText, setJsonText] = useState('');

  // Canvas (editor) state — the structural source of truth.
  const initialConfig = useMemo(() => cloneDefaultConfig(t, workspaceId, workspaceWorkdir), [t, workspaceId, workspaceWorkdir]);
  const [nodes, setNodes] = useState<QuartetFlowNode[]>(() => markEditableNodes(configToFlow(initialConfig).nodes));
  const [edges, setEdges] = useState<QuartetFlowEdge[]>(() => configToFlow(initialConfig).edges);
  const [meta, setMeta] = useState<ConfigMeta>(() => metaFromConfig(initialConfig));
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);
  // Mobile starts with the bottom drawer collapsed so the whole canvas stays
  // visible on load; selecting a node auto-opens it (see the effect below).
  const [inspectorDrawerOpen, setInspectorDrawerOpen] = useState(() => !isMobile);
  // Mobile-only: the workflow library is an off-canvas left drawer (it would
  // otherwise collapse to a sliver in the stacked layout). Desktop ignores this.
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [focus, setFocus] = useState<GraphCanvasFocus>({ token: 0 });
  const [loadedConfig, setLoadedConfig] = useState<GraphConfig>(initialConfig);
  // Last persisted/default document snapshot. `loadedConfig` is the structural
  // base for the current canvas and therefore also changes after applying a JSON
  // draft; reset must instead restore this stable saved snapshot.
  const [savedConfig, setSavedConfig] = useState<GraphConfig>(initialConfig);
  const [viewportResetKey, setViewportResetKey] = useState(0);
  const [savedFingerprint, setSavedFingerprint] = useState(() =>
    fingerprint({ name: i18n.t('graph.defaultName'), description: '', config: initialConfig }),
  );
  const nodeCounterRef = useRef(0);
  // In-memory copy buffer for canvas copy/paste (same canvas, this tab only).
  // Holds a self-contained selection snapshot; paste clones it with fresh ids,
  // so the same buffer can be pasted repeatedly.
  const clipboardRef = useRef<{ nodes: QuartetFlowNode[]; edges: QuartetFlowEdge[] } | null>(null);
  // Live mirrors of nodes/edges so the copy/paste/duplicate callbacks (and the
  // canvas keydown handler that calls them) can read current canvas state
  // without being re-created on every node/edge change. Synced after commit;
  // only ever read in later user-event handlers, so never stale at read time.
  const nodesRef = useRef<QuartetFlowNode[]>([]);
  const edgesRef = useRef<QuartetFlowEdge[]>([]);
  // Live mirror of meta for the undo/redo snapshotter (commitHistory reads the
  // current state synchronously from refs, off the React render cycle).
  const metaRef = useRef<ConfigMeta>(metaFromConfig(initialConfig));

  // ---- Undo / redo history (canvas editing only) ----
  // past/future are stacks of CanvasSnapshot. commitHistory pushes the *current*
  // state onto `past` right before an edit mutates it, so undo restores the
  // pre-edit state. A new edit clears the redo (`future`) stack. coalesceKeyRef
  // collapses a rapid burst of same-target edits (e.g. typing in a text field)
  // into a single undo step: while the key is unchanged, repeated commits are
  // skipped. The `[undo,redo]` flags let the keydown handler reflect emptiness
  // without re-subscribing on every snapshot push.
  const historyPastRef = useRef<CanvasSnapshot[]>([]);
  const historyFutureRef = useRef<CanvasSnapshot[]>([]);
  const coalesceKeyRef = useRef<string | null>(null);
  // Drag/resize are continuous: React Flow streams many position/dimension
  // changes per gesture. These flags let onNodesChange checkpoint history ONCE
  // at the start of a gesture (the false→true edge) instead of on every frame.
  const draggingRef = useRef(false);
  const resizingRef = useRef(false);
  const [canUndo, setCanUndo] = useState(false);
  const [canRedo, setCanRedo] = useState(false);

  // Workspace selector: the full list (for the dropdown) plus open/close state.
  // The selected workspace lives in `meta.workspaceId` (persisted into config),
  // so there is no separate selection state to keep in sync.
  const [allWorkspaces, setAllWorkspaces] = useState<WorkspaceItem[]>([]);
  const [wsDropdownOpen, setWsDropdownOpen] = useState(false);
  const [workspaceListError, setWorkspaceListError] = useState('');
  const wsDropdownRef = useRef<HTMLDivElement>(null);

  // Secondary editor actions (validate/reset/delete/save-as) live in an
  // overflow menu on narrow screens; on desktop the wrapper is display:contents
  // and the menu trigger stays hidden, so the layout is unchanged.
  const [actionsMenuOpen, setActionsMenuOpen] = useState(false);
  const actionsMenuRef = useRef<HTMLDivElement>(null);

  // Run view / replay state.
  const [selectedRun, setSelectedRun] = useState<GraphRun | null>(null);
  const [viewingRun, setViewingRun] = useState(false);
  const [runProgress, setRunProgress] = useState<GraphProgress | undefined>();
  const [runInstances, setRunInstances] = useState<GraphInstanceState[]>([]);
  // Per-node hook (§ 节点 Hook) execution results for the selected run, keyed by
  // nodeId, shown in the read-only run-view node-detail panel. Fetched from the
  // /hooks endpoint rather than accumulated from SSE: the canvas SSE never opens
  // for a completed run, and an End hook finishes AFTER the run goes terminal (so
  // the SSE that would carry it is already torn down). See refreshHookResults.
  const [hookResults, setHookResults] = useState<Record<string, GraphHookResult>>({});
  // Edge run states for the selected run, so replay can show active/pruned/done
  // branch edges (GraphCanvas consumes edgeStatusById, same as GraphRunProgress).
  const [runEdges, setRunEdges] = useState<GraphEdgeState[]>([]);
  const [runMessage, setRunMessage] = useState('');
  // editingRun overlays an editable canvas on top of the run-view: the run's
  // current effective version snapshot is loaded into the editor and saved back
  // as a new GraphRun version via the bound Job. Only enterable while the
  // selected run is editable (in-flight or resumable, never naturally completed).
  const [editingRun, setEditingRun] = useState(false);
  const graphEventClientRef = useRef<GraphSSEClient | null>(null);
  const graphViewerIDRef = useRef('');
  if (!graphViewerIDRef.current) {
    graphViewerIDRef.current = typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `viewer-${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
  }
  const graphViewerURL = useCallback((jobID: string): string => {
    const params = new URLSearchParams({
      viewerId: graphViewerIDRef.current,
      visible: typeof document === 'undefined' || document.visibilityState === 'visible' ? '1' : '0',
    });
    return `/api/v1/job/${encodeURIComponent(jobID)}/graph-run/events?${params.toString()}`;
  }, []);
  const reportGraphViewerVisibility = useCallback((jobID: string, visible: boolean) => {
    void fetch(`/api/v1/job/${encodeURIComponent(jobID)}/viewer-state`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ viewerId: graphViewerIDRef.current, visible }),
      keepalive: true,
    }).catch((err) => console.debug('[GraphWorkflowPage] viewer-state report failed:', err));
  }, []);
  // One-shot consumption of the ?graphEditJob=<id> deep-link (from Chat page's
  // GraphRun "Edit"): open that job's run directly in run-version edit mode.
  const editRunIntentRef = useRef<string | null>(
    typeof window !== 'undefined' ? new URLSearchParams(window.location.search).get('graphEditJob') : null,
  );
  const workflowLoadSeqRef = useRef(0);
  const runLoadSeqRef = useRef(0);
  const runStatusRefreshSeqRef = useRef(0);
  const validationSeqRef = useRef(0);
  const saveSeqRef = useRef(0);
  const runVersionSaveSeqRef = useRef(0);

  const selectedWorkflow = useMemo(() => workflows.find((wf) => wf.id === selectedId) ?? null, [workflows, selectedId]);
  const selectedWorkflowRef = useRef<GraphWorkflowSummary | null>(null);
  const workspaceNameById = useMemo(() => {
    const names = new Map<string, string>();
    allWorkspaces.forEach((ws) => names.set(ws.id, ws.title || ws.id));
    return names;
  }, [allWorkspaces]);
  const workflowWorkspaceLabel = useCallback(
    (wf: GraphWorkflowSummary): string => {
      const wfWorkspaceId = wf.workspaceId || '';
      if (!wfWorkspaceId) return t('graph.sidebar.workspaceUnbound');
      return workspaceNameById.get(wfWorkspaceId) || wfWorkspaceId;
    },
    [t, workspaceNameById],
  );
  const sortedWorkflows = useMemo(
    () =>
      [...workflows]
        // Anything that is not explicitly 'agent' is shown in the user tab, so a
        // missing or unexpected type still surfaces somewhere instead of
        // vanishing from both tabs.
        .filter((wf) => (wf.type === 'agent' ? 'agent' : 'user') === activeTab)
        .sort((a, b) => (a.name || '').localeCompare(b.name || '', undefined, { numeric: true, sensitivity: 'base' })),
    [workflows, activeTab],
  );
  useEffect(() => {
    selectedWorkflowRef.current = selectedWorkflow;
  }, [selectedWorkflow]);
  const selectedGraphNode: GraphNode | null = useMemo(() => {
    if (!selectedNodeId) return null;
    return nodes.find((n) => n.id === selectedNodeId)?.data.graphNode ?? null;
  }, [nodes, selectedNodeId]);
  // Mobile tap-to-add target: when the selected node is a loop (or sits inside
  // one), palette clicks add the new node into that loop — touch has no HTML5
  // drag & drop, so this is the only way to populate a loop on a phone.
  const addIntoLoopId = useMemo(() => {
    if (!isMobile || !selectedNodeId) return null;
    const selected = nodes.find((n) => n.id === selectedNodeId);
    if (!selected) return null;
    if (selected.data.kind === 'loop') return selected.id;
    if (selected.parentId) {
      const parent = nodes.find((n) => n.id === selected.parentId);
      if (parent?.data.kind === 'loop') return parent.id;
    }
    return null;
  }, [isMobile, nodes, selectedNodeId]);
  const canDeleteSelectedNode = useCallback(
    (node: GraphNode): boolean => {
      const flowNode = nodes.find((n) => n.id === node.id);
      return !!flowNode && isNodeDeletable(flowNode, nodes);
    },
    [nodes],
  );
  const clearValidationState = useCallback(() => {
    setErrors([]);
  }, []);

  useEffect(() => {
    if (isMobile && selectedNodeId && viewMode === 'canvas' && (!viewingRun || editingRun)) {
      setInspectorDrawerOpen(true);
    }
  }, [editingRun, isMobile, selectedNodeId, viewingRun, viewMode]);

  // Keep the live node/edge mirrors in sync for the copy/paste/duplicate
  // handlers (which read them lazily from user-event callbacks).
  useEffect(() => {
    nodesRef.current = nodes;
  }, [nodes]);
  useEffect(() => {
    edgesRef.current = edges;
  }, [edges]);
  useEffect(() => {
    metaRef.current = meta;
  }, [meta]);

  // ---- Config <-> canvas plumbing ----
  // Wipe the undo/redo stacks. Called whenever a brand-new config is loaded into
  // the canvas (new/open workflow, reset, JSON apply, enter/cancel run edit,
  // save) — the prior edit history belongs to the old document and must not
  // bleed across loads.
  const clearHistory = useCallback(() => {
    historyPastRef.current = [];
    historyFutureRef.current = [];
    coalesceKeyRef.current = null;
    setCanUndo(false);
    setCanRedo(false);
  }, []);

  const loadConfigIntoCanvas = useCallback(
    (config: GraphConfig) => {
      const flow = configToFlow(config);
      nodeCounterRef.current = Math.max(
        nodeCounterRef.current,
        seedCounterFromIds([...flow.nodes.map((n) => n.id), ...flow.edges.map((e) => e.id)]),
      );
      setNodes(markEditableNodes(flow.nodes));
      setEdges(flow.edges);
      setMeta(metaFromConfig(config));
      setLoadedConfig(config);
      setSelectedNodeId(null);
      setInspectorDrawerOpen(true);
      setViewportResetKey((key) => key + 1);
      clearHistory();
    },
    [clearHistory],
  );

  // Push the CURRENT canvas state onto the undo stack, just before an edit
  // mutates it. Read from refs so callers can fire this synchronously at the top
  // of an event handler (before their own setNodes/setEdges). A new edit always
  // discards the redo stack. When `coalesceKey` is given and matches the key of
  // the previous commit, the commit is skipped so a burst of same-target edits
  // (e.g. dragging a slider, typing a title) collapses into one undo step.
  const commitHistory = useCallback((coalesceKey?: string) => {
    if (coalesceKey && coalesceKey === coalesceKeyRef.current) return;
    coalesceKeyRef.current = coalesceKey ?? null;
    const snapshot: CanvasSnapshot = {
      nodes: nodesRef.current,
      edges: edgesRef.current,
      meta: metaRef.current,
    };
    const past = historyPastRef.current;
    // Reference dedup: a single keypress (e.g. Delete on a selected node) can
    // fire onNodesChange AND onEdgesChange in one tick — two commits before the
    // refs re-sync, both seeing the same pre-edit state. Skip the duplicate so
    // the pair collapses into one undo step. Equal refs also mean "nothing
    // changed", so skipping is always safe.
    const top = past[past.length - 1];
    if (top && top.nodes === snapshot.nodes && top.edges === snapshot.edges && top.meta === snapshot.meta) {
      historyFutureRef.current = [];
      setCanRedo(false);
      return;
    }
    past.push(snapshot);
    if (past.length > HISTORY_LIMIT) past.shift();
    historyFutureRef.current = [];
    setCanUndo(true);
    setCanRedo(false);
  }, []);

  // Restore a snapshot into the live canvas. The viewport (meta.canvas) is pure
  // view state, so the LIVE camera is kept — undoing an edit should not yank the
  // user's pan/zoom back to where it was when the edit happened. Refs are
  // updated synchronously so a follow-up commit in the same tick is consistent.
  const applySnapshot = useCallback((snap: CanvasSnapshot) => {
    const meta: ConfigMeta = { ...snap.meta, canvas: metaRef.current.canvas };
    const nodes = markEditableNodes(snap.nodes);
    nodesRef.current = nodes;
    edgesRef.current = snap.edges;
    metaRef.current = meta;
    setNodes(nodes);
    setEdges(snap.edges);
    setMeta(meta);
    clearValidationState();
  }, [clearValidationState]);

  const undo = useCallback(() => {
    const snap = historyPastRef.current.pop();
    if (!snap) return;
    historyFutureRef.current.push({
      nodes: nodesRef.current,
      edges: edgesRef.current,
      meta: metaRef.current,
    });
    coalesceKeyRef.current = null;
    applySnapshot(snap);
    setCanUndo(historyPastRef.current.length > 0);
    setCanRedo(true);
  }, [applySnapshot]);

  const redo = useCallback(() => {
    const snap = historyFutureRef.current.pop();
    if (!snap) return;
    historyPastRef.current.push({
      nodes: nodesRef.current,
      edges: edgesRef.current,
      meta: metaRef.current,
    });
    coalesceKeyRef.current = null;
    applySnapshot(snap);
    setCanRedo(historyFutureRef.current.length > 0);
    setCanUndo(true);
  }, [applySnapshot]);

  const buildConfig = useCallback((): GraphConfig => {
    const base: GraphConfig = { ...loadedConfig, ...meta };
    return flowToConfig(nodes, edges, base);
  }, [edges, loadedConfig, meta, nodes]);
  const buildConfigRef = useRef(buildConfig);
  useEffect(() => {
    buildConfigRef.current = buildConfig;
  }, [buildConfig]);

  const currentFingerprint = useMemo(
    () => fingerprint({ name, description, config: buildConfig() }),
    [buildConfig, description, name],
  );
  const dirty = (!viewingRun || editingRun) && currentFingerprint !== savedFingerprint;
  const runVersionDirty = editingRun && currentFingerprint !== savedFingerprint;
  const hasJsonDraft = viewMode === 'json' && jsonText !== JSON.stringify(buildConfig(), null, 2);
  const discardGuardDirty = dirty || hasJsonDraft;
  const editingLocked = saving || startingRun || validating;

  // Live mirror of `dirty` so the stable discard-guard callback (wired into
  // Back / new / select-other / cancel-edit) can read the latest value without
  // taking `dirty` as a dependency and re-creating every handler on each edit.
  const dirtyRef = useRef(false);
  useEffect(() => {
    dirtyRef.current = discardGuardDirty;
    onDirtyChange?.(discardGuardDirty);
  }, [discardGuardDirty, onDirtyChange]);

  useEffect(() => {
    return () => onDirtyChange?.(false);
  }, [onDirtyChange]);

  useEffect(() => {
    if (!discardGuardDirty) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, [discardGuardDirty]);

  // Confirm before throwing away unsaved canvas edits. Returns true when it is
  // safe to proceed (not dirty, or the user accepted the prompt). A single
  // guard for every "leave the current document" path: Back, new workflow,
  // selecting another workflow, and cancelling a run-version edit.
  const guardDiscard = useCallback((): boolean => {
    if (!dirtyRef.current) return true;
    return window.confirm(i18n.t('graph.messages.discardUnsavedConfirm'));
  }, []);

  // The inspector reads global meta (variables/disabledVars/runConfig) AND the
  // live node/edge structure — the latter drives condition variable suggestions
  // (upstream outputs, loop iteration vars). Build from the current canvas so a
  // node moved into/out of a loop body updates its available variables.
  const inspectorConfig = useMemo<GraphConfig>(
    () => flowToConfig(nodes, edges, { nodes: [], edges: [], ...meta }),
    [nodes, edges, meta],
  );

  // ---- Data loading ----
  // Workflows are global — the library lists every workflow regardless of the
  // active workspace. A workflow's workspaceId is just a display hint and the
  // default workspace pre-selected when launching a run; any workflow can run
  // in any workspace via the canvas workspace selector.
  const loadWorkflows = useCallback(async (options?: { preserveMessage?: boolean; keepSelected?: boolean }) => {
    setLoading(true);
    if (!options?.preserveMessage) setMessage('');
    try {
      const res = await fetch('/api/v1/graph/workflow/list');
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphListWorkflowsResponse;
      const nextWorkflows = data.workflows || [];
      const pinned = options?.keepSelected ? selectedWorkflowRef.current : null;
      setWorkflows(pinned ? upsertWorkflow(nextWorkflows, pinned) : nextWorkflows);
      setListWarnings(data.warnings || []);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadWorkflows();
  }, [loadWorkflows]);

  useEffect(() => {
    // Load agents for the inspector's Agent/model selectors.
    if (canReadAgents) void (async () => {
      try {
        const data = await fetchAvailableAgentList();
        setAgents(data.agents);
        setAgentListError('');
      } catch (err) {
        setAgentListError(err instanceof Error ? err.message : String(err));
      }
    })();
    // Load the workspace list for the workspace selector (mirrors ChatPage).
    if (canReadWorkspaces) void (async () => {
      try {
        const res = await fetch('/api/v1/workspace/list');
        if (!res.ok) throw new Error(await readError(res));
        const data = await res.json().catch(() => null);
        const list = (data?.workspaces || []) as WorkspaceItem[];
        registerWorkspaceColors(list);
        setAllWorkspaces(list);
        setWorkspaceListError('');
      } catch (err) {
        setWorkspaceListError(err instanceof Error ? err.message : String(err));
      }
    })();
  }, [canReadAgents, canReadWorkspaces, t]);

  // Close the workspace dropdown on outside click (mirrors the former editor).
  useEffect(() => {
    if (!wsDropdownOpen) return;
    const handler = (e: MouseEvent) => {
      if (wsDropdownRef.current && !wsDropdownRef.current.contains(e.target as Node)) {
        setWsDropdownOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [wsDropdownOpen]);

  // Same outside-click close for the mobile actions overflow menu.
  useEffect(() => {
    if (!actionsMenuOpen) return;
    const handler = (e: MouseEvent) => {
      if (actionsMenuRef.current && !actionsMenuRef.current.contains(e.target as Node)) {
        setActionsMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [actionsMenuOpen]);

  useEffect(
    () => () => {
      graphEventClientRef.current?.disconnect();
      graphEventClientRef.current = null;
    },
    [],
  );

  // ---- Run status / SSE ----
  const refreshJobRunStatus = useCallback(async (
    jobId: string,
    guardSeq?: number,
    errorContext?: string,
  ): Promise<GraphRunStatusResponse | null> => {
    const refreshSeq = ++runStatusRefreshSeqRef.current;
    try {
      const res = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`);
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphRunStatusResponse;
      if (
        refreshSeq !== runStatusRefreshSeqRef.current ||
        (guardSeq !== undefined && guardSeq !== runLoadSeqRef.current)
      ) return null;
      if (data.run) setSelectedRun(data.run);
      setRunProgress(data.progress || data.run?.progress);
      const instances = data.instances || (data.progress?.instances ? Object.values(data.progress.instances) : []);
      setRunInstances(instances);
      setRunEdges(data.edges || []);
      setRunMessage('');
      return data;
    } catch (err) {
      if (
        refreshSeq !== runStatusRefreshSeqRef.current ||
        (guardSeq !== undefined && guardSeq !== runLoadSeqRef.current)
      ) return null;
      const detail = err instanceof Error ? err.message : String(err);
      setRunMessage(errorContext ? `${errorContext}; Snapshot reload failed: ${detail}` : detail);
      return null;
    }
  }, []);

  // refreshHookResults fetches the run's per-node hook results and indexes them
  // by nodeId. Guarded by runLoadSeqRef so a stale fetch from a previous run
  // selection can't overwrite the current run's results.
  const refreshHookResults = useCallback(async (jobId: string, guardSeq?: number): Promise<void> => {
    try {
      const res = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run/hooks`);
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphHookResultsResponse;
      if (guardSeq !== undefined && guardSeq !== runLoadSeqRef.current) return;
      const byNode: Record<string, GraphHookResult> = {};
      for (const r of data.results || []) byNode[r.nodeId] = r;
      setHookResults(byNode);
    } catch {
      // Hook results are auxiliary; a fetch failure must not disrupt run viewing.
    }
  }, []);

  const selectRun = useCallback(
    async (run: GraphRun) => {
      const seq = ++runLoadSeqRef.current;
      graphEventClientRef.current?.disconnect();
      graphEventClientRef.current = null;
      setSelectedRun(run);
      setViewingRun(true);
      setEditingRun(false);
      setSelectedNodeId(null);
      setRunProgress(run.progress);
      setRunInstances([]);
      setRunEdges([]);
      setHookResults({});
      setViewMode('canvas');
      setJsonText('');
      await refreshJobRunStatus(run.jobId, seq);
    },
    [refreshJobRunStatus],
  );

  const exitRunView = useCallback(() => {
    runLoadSeqRef.current += 1;
    runStatusRefreshSeqRef.current += 1;
    graphEventClientRef.current?.disconnect();
    graphEventClientRef.current = null;
    setViewingRun(false);
    setEditingRun(false);
    setSelectedRun(null);
    setHookResults({});
    // Run-scoped errors (SSE / snapshot reload / not-editable notices) belong to
    // the run strip; without clearing, they would leak into the editor status
    // area once viewingRun flips false.
    setRunMessage('');
  }, []);

  useEffect(() => {
    graphEventClientRef.current?.disconnect();
    graphEventClientRef.current = null;
    if (!selectedRun?.id || !isGraphRunLive(selectedRun.status)) return;

    const runSeq = runLoadSeqRef.current;
    let lastInstanceRefreshAt = 0;
    const throttledRefresh = () => {
      const now = Date.now();
      if (now - lastInstanceRefreshAt < 400) return;
      lastInstanceRefreshAt = now;
      void refreshJobRunStatus(selectedRun.jobId, runSeq);
    };
    const client = new GraphSSEClient({
      url: () => graphViewerURL(selectedRun.jobId),
      onReconcile: async (_reason, resumeError) => {
        reportGraphViewerVisibility(
          selectedRun.jobId,
          typeof document === 'undefined' || document.visibilityState === 'visible',
        );
        await refreshJobRunStatus(
          selectedRun.jobId,
          runSeq,
          resumeError ? `Resume point expired: ${resumeError}` : undefined,
        );
      },
      onError: (err) => setRunMessage(err.message),
      onEvent: (event) => {
        const graphEvent = event as unknown as GraphEvent;
        // Events never carry a progress snapshot: progress + node status come
        // exclusively from a (throttled) re-fetch of the run snapshot, so a
        // replayed historical event can never rewind the live progress. The
        // event only decides *when* to re-fetch. progressUpdated/error refetch
        // immediately for terminal transitions and edge state; instance/edge/
        // loop lifecycle events refetch throttled.
        if (graphEvent.type === 'progressUpdated' || graphEvent.type === 'error') {
          if (graphEvent.type === 'error' && graphEvent.message) {
            setRunMessage(graphEvent.error?.message || graphEvent.message);
          }
          void refreshJobRunStatus(selectedRun.jobId, runSeq);
          return;
        }
        if (
          graphEvent.type === 'instanceStarted' || graphEvent.type === 'instanceCompleted' ||
          graphEvent.type === 'instanceFailed' || graphEvent.type === 'instanceSkipped' ||
          graphEvent.type === 'edgeResolved' || graphEvent.type === 'loopIteration'
        ) {
          throttledRefresh();
        }
        // A live Prompt hook fires while the run is still running and the SSE
        // is connected — refetch hook results so the panel updates promptly.
        // (The End hook is handled by the terminal-transition effect below,
        // because it can land after this stream is torn down.)
        if (graphEvent.type === 'hookCompleted' || graphEvent.type === 'hookFailed') {
          void refreshHookResults(selectedRun.jobId, runSeq);
        }
      },
    });
    graphEventClientRef.current = client;
    client.connect();

    return () => {
      client.disconnect();
      if (graphEventClientRef.current === client) graphEventClientRef.current = null;
    };
  }, [graphViewerURL, refreshJobRunStatus, refreshHookResults, reportGraphViewerVisibility, selectedRun?.jobId, selectedRun?.id, selectedRun?.status]);

  useEffect(() => {
    if (!selectedRun?.jobId || !isGraphRunLive(selectedRun.status)) return;
    const jobID = selectedRun.jobId;
    const report = () => reportGraphViewerVisibility(jobID, document.visibilityState === 'visible');
    const onHide = () => reportGraphViewerVisibility(jobID, false);
    document.addEventListener('visibilitychange', report);
    window.addEventListener('pagehide', onHide);
    return () => {
      document.removeEventListener('visibilitychange', report);
      window.removeEventListener('pagehide', onHide);
    };
  }, [reportGraphViewerVisibility, selectedRun?.jobId, selectedRun?.status]);

  // Hook-result fetch (separate from the SSE refetch loop). Runs on run select
  // and on every status change. When the run is terminal, the End hook may still
  // be executing in a detached backend goroutine that finishes AFTER the run was
  // marked completed — and the canvas SSE has by then disconnected — so a single
  // fetch can race ahead of the End hook's persisted event. A short backoff burst
  // (1s/2s/4s) closes that window without indefinite polling. While the run is
  // live, the SSE's hookCompleted/hookFailed branch keeps results fresh.
  useEffect(() => {
    if (!selectedRun?.id || !selectedRun.jobId) return;
    const jobId = selectedRun.jobId;
    const runSeq = runLoadSeqRef.current;
    void refreshHookResults(jobId, runSeq);
    if (isGraphRunLive(selectedRun.status)) return;
    const timers = [1000, 2000, 4000].map((delay) =>
      setTimeout(() => void refreshHookResults(jobId, runSeq), delay),
    );
    return () => timers.forEach(clearTimeout);
  }, [refreshHookResults, selectedRun?.jobId, selectedRun?.id, selectedRun?.status]);

  // ---- Workflow CRUD ----
  const selectWorkflow = useCallback(
    async (workflow: GraphWorkflowSummary) => {
      const seq = ++workflowLoadSeqRef.current;
      setMessage('');
      setErrors([]);
      exitRunView();
      try {
        const res = await fetch(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`);
        if (!res.ok) throw new Error(await readError(res));
        const data = (await res.json()) as GraphWorkflowResponse;
        if (!data.workflow) throw new Error(t('graph.messages.workflowEmpty'));
        if (seq !== workflowLoadSeqRef.current) return;
        selectedWorkflowRef.current = workflowToSummary(data.workflow);
        setSelectedId(data.workflow.id);
        setSelectedWorkflowUpdatedAt(data.workflow.updatedAt);
        setName(data.workflow.name);
        setDescription(data.workflow.description || '');
        const loaded = canonicalWorkflowConfig(data.workflow);
        loadConfigIntoCanvas(loaded);
        // The JSON draft belongs to the document it was seeded from: drop it
        // when loading another workflow, or "Apply to canvas" would push the
        // previous workflow's JSON into this one's canvas.
        setViewMode('canvas');
        setJsonText('');
        setSavedConfig(loaded);
        setSavedFingerprint(fingerprint({
          name: data.workflow.name,
          description: data.workflow.description || '',
          config: loaded,
        }));
        setLibraryOpen(false);
      } catch (err) {
        if (seq !== workflowLoadSeqRef.current) return;
        setMessage(err instanceof Error ? err.message : String(err));
      }
    },
    [exitRunView, loadConfigIntoCanvas, t],
  );

  const startNew = useCallback(() => {
    workflowLoadSeqRef.current += 1;
    const config = cloneDefaultConfig(t, workspaceId, workspaceWorkdir);
    exitRunView();
    // The UI only ever creates 'user'-library workflows, so switch to that tab
    // — otherwise a new workflow saved while the 'agent' tab is active would be
    // filtered out of the visible list.
    setActiveTab('user');
    setSelectedId(null);
    setSelectedWorkflowUpdatedAt(undefined);
    setName(t('graph.defaultName'));
    setDescription('');
    setErrors([]);
    setMessage('');
    setLibraryOpen(false);
    loadConfigIntoCanvas(config);
    setViewMode('canvas');
    setJsonText('');
    setSavedConfig(config);
    setSavedFingerprint(fingerprint({ name: t('graph.defaultName'), description: '', config }));
  }, [exitRunView, loadConfigIntoCanvas, t, workspaceId, workspaceWorkdir]);

  const resetChanges = useCallback(() => {
    if (selectedWorkflowRef.current && selectedWorkflowRef.current.id === selectedId) {
      const saved = selectedWorkflowRef.current;
      setName(saved.name);
      setDescription(saved.description || '');
      setSelectedWorkflowUpdatedAt(saved.updatedAt);
      const config = savedConfig;
      loadConfigIntoCanvas(config);
      setViewMode('canvas');
      setJsonText('');
      setSavedFingerprint(fingerprint({
        name: saved.name,
        description: saved.description || '',
        config,
      }));
      setMessage(t('graph.messages.resetToSaved'));
      return;
    }
    startNew();
    setMessage(t('graph.messages.resetToDefault'));
  }, [loadConfigIntoCanvas, savedConfig, selectedId, startNew, t]);

  const validateConfigValue = useCallback(async (config: GraphConfig): Promise<boolean> => {
    const seq = validationSeqRef.current + 1;
    validationSeqRef.current = seq;
    const requestFingerprint = configFingerprint(config);
    setValidating(true);
    setMessage('');
    try {
      const res = await fetch('/api/v1/graph/workflow/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ config }),
      });
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as GraphValidationResponse;
      if (seq !== validationSeqRef.current || configFingerprint(buildConfigRef.current()) !== requestFingerprint) return data.valid;
      setErrors(data.errors || []);
      setMessage(data.valid ? t('graph.messages.validationPassed') : t('graph.messages.validationErrors', { count: data.errors?.length || 0 }));
      return data.valid;
    } catch (err) {
      if (seq !== validationSeqRef.current || configFingerprint(buildConfigRef.current()) !== requestFingerprint) return false;
      setMessage(err instanceof Error ? err.message : String(err));
      return false;
    } finally {
      if (seq === validationSeqRef.current) setValidating(false);
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
      const seq = saveSeqRef.current + 1;
      saveSeqRef.current = seq;
      const requestFingerprint = fingerprint({ name: trimmedName, description, config });
      setSaving(true);
      setMessage('');
      setErrors([]);
      try {
        const url = mode === 'create' ? '/api/v1/graph/workflow' : `/api/v1/graph/workflow/${encodeURIComponent(selectedId || '')}`;
        const method = mode === 'create' ? 'POST' : 'PUT';
        const body =
          mode === 'create'
            ? { name: trimmedName, description, workspaceId: config.workspaceId || workspaceId, config }
            : { name: trimmedName, description, workspaceId: config.workspaceId || workspaceId, config, updatedAt: selectedWorkflowUpdatedAt };
        const res = await fetch(url, {
          method,
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!res.ok) {
          const bodyText = await res.text().catch(() => '');
          let data: GraphWorkflowResponse | null = null;
          if (bodyText) {
            try {
              data = JSON.parse(bodyText) as GraphWorkflowResponse;
            } catch {
              data = null;
            }
          }
          if (Array.isArray(data?.errors)) {
            if (seq !== saveSeqRef.current || fingerprint({ name: trimmedName, description, config: buildConfigRef.current() }) !== requestFingerprint) return;
            setErrors(data.errors);
            setMessage(t('graph.messages.validationErrors', { count: data.errors.length }));
            return;
          }
          if (res.status === 409) {
            throw new Error((data as { msg?: string; error?: string } | null)?.msg || t('graph.messages.workflowConflict'));
          }
          throw new Error(
            (data as { msg?: string; error?: string; message?: string } | null)?.msg ||
            (data as { error?: string } | null)?.error ||
            (data as { message?: string } | null)?.message ||
            bodyText.trim() ||
            `HTTP ${res.status}`,
          );
        }
        const data = (await res.json()) as GraphWorkflowResponse;
        if (!data.workflow) throw new Error(t('graph.messages.workflowEmpty'));
        if (seq !== saveSeqRef.current) return;
        selectedWorkflowRef.current = workflowToSummary(data.workflow);
        setWorkflows((prev) => upsertWorkflow(prev, data.workflow!));
        setSelectedId(data.workflow.id);
        setSelectedWorkflowUpdatedAt(data.workflow.updatedAt);
        setSavedFingerprint(fingerprint({
          name: data.workflow.name,
          description: data.workflow.description || '',
          config: data.workflow.config,
        }));
        setSavedConfig(data.workflow.config);
        const currentAfterSave = fingerprint({ name: trimmedName, description, config: buildConfigRef.current() });
        if (currentAfterSave === requestFingerprint) {
          setName(data.workflow.name);
          setDescription(data.workflow.description || '');
          loadConfigIntoCanvas(data.workflow.config);
        }
        await loadWorkflows({ preserveMessage: true, keepSelected: true });
        setMessage(mode === 'create' ? t('graph.messages.workflowCreated') : t('graph.messages.workflowSaved'));
      } catch (err) {
        if (seq !== saveSeqRef.current || fingerprint({ name: trimmedName, description, config: buildConfigRef.current() }) !== requestFingerprint) return;
        setMessage(err instanceof Error ? err.message : String(err));
      } finally {
        if (seq === saveSeqRef.current) setSaving(false);
      }
    },
    [buildConfig, description, loadConfigIntoCanvas, loadWorkflows, name, selectedId, selectedWorkflowUpdatedAt, t, workspaceId],
  );

  const confirmDelete = useCallback(async () => {
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    setMessage('');
    try {
      const expectedUpdatedAt = deleteTarget.id === selectedId ? selectedWorkflowUpdatedAt : deleteTarget.updatedAt;
      const res = await fetch(`/api/v1/graph/workflow/${encodeURIComponent(deleteTarget.id)}`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ updatedAt: expectedUpdatedAt }),
      });
      if (!res.ok) throw new Error(await readError(res));
      if (selectedId === deleteTarget.id) startNew();
      setDeleteTarget(null);
      await loadWorkflows({ preserveMessage: true });
      setMessage(t('graph.messages.workflowDeleted'));
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setDeleting(false);
    }
  }, [deleteTarget, deleting, loadWorkflows, selectedId, selectedWorkflowUpdatedAt, startNew, t]);

  // ---- Run ----
  const startRun = useCallback(async () => {
    // Guard against a rapid double-click: validate() + the start request are
    // async, and without this the button (only disabled while `saving`) stays
    // live throughout, so each extra click fires another /graph/run/start —
    // each creating its own Graph Job. Bail if a start is already in flight.
    if (startingRun) return;
    setStartingRun(true);
    try {
      const config = await validate();
      if (!config) {
        setMessage(t('graph.messages.fixValidationFirst'));
        return;
      }
      if (dirty && selectedId && !window.confirm(t('graph.messages.runUnsavedSnapshotConfirm'))) {
        return;
      }
      setMessage('');
      const res = await fetch('/api/v1/graph/run/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          workflowId: selectedId || undefined,
          workflowUpdatedAt: selectedId ? selectedWorkflowUpdatedAt : undefined,
          workspaceId: config.workspaceId || workspaceId,
          workdir: config.workdir || workspaceWorkdir,
          config,
        }),
      });
      if (!res.ok) throw new Error(await readError(res));
      const data = (await res.json()) as { run?: GraphRun };
      if (configFingerprint(buildConfigRef.current()) !== configFingerprint(config)) return;
      // A run binds a Graph-type Job; jump into the Chat page for it like the
      // normal Graph run flow does, instead of showing the run inline on the canvas.
      if (data.run?.jobId) {
        onRunStarted(data.run.jobId);
        return;
      }
      setMessage(t('graph.messages.runStartedNoJob'));
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setStartingRun(false);
    }
  }, [dirty, onRunStarted, selectedId, selectedWorkflowUpdatedAt, startingRun, t, validate, workspaceId, workspaceWorkdir]);

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
    setViewMode('canvas');
    setJsonText('');
    if (selectedRun) loadConfigIntoCanvas(runConfigSnapshot(selectedRun));
  }, [loadConfigIntoCanvas, selectedRun]);

  const saveRunVersion = useCallback(async () => {
    if (!selectedRun) return;
    const config = buildConfig();
    const nextFingerprint = fingerprint({ name, description, config });
    if (nextFingerprint === savedFingerprint) {
      setMessage(t('graph.messages.noRunVersionChanges'));
      return;
    }
    const seq = runVersionSaveSeqRef.current + 1;
    runVersionSaveSeqRef.current = seq;
    const requestFingerprint = configFingerprint(config);
    setSaving(true);
    setMessage('');
    setErrors([]);
    try {
      const res = await fetch(`/api/v1/job/${encodeURIComponent(selectedRun.jobId)}/graph-run/version`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ config }),
      });
      if (!res.ok) {
        const bodyText = await res.text().catch(() => '');
        let data: GraphWorkflowResponse | null = null;
        if (bodyText) {
          try {
            data = JSON.parse(bodyText) as GraphWorkflowResponse;
          } catch {
            data = null;
          }
        }
        if (Array.isArray(data?.errors) && data.errors.length > 0) {
          if (seq !== runVersionSaveSeqRef.current || configFingerprint(buildConfigRef.current()) !== requestFingerprint) return;
          setErrors(data.errors);
          setMessage(t('graph.messages.validationErrors', { count: data.errors.length }));
          return;
        }
        const detail =
          (data as { msg?: string; error?: string } | null)?.msg ||
          (data as { error?: string } | null)?.error ||
          bodyText.trim() ||
          `HTTP ${res.status}`;
        // 409 = run moved to a non-editable state between entering and saving.
        setMessage(res.status === 409 ? t('graph.messages.runNotEditableDetail', { detail }) : detail);
        return;
      }
      const data = (await res.json()) as { run?: GraphRun };
      if (seq !== runVersionSaveSeqRef.current || configFingerprint(buildConfigRef.current()) !== requestFingerprint) return;
      if (data.run) {
        setSelectedRun(data.run);
        setSavedFingerprint(fingerprint({ name, description, config }));
      }
      setEditingRun(false);
      setMessage(t('graph.messages.runVersionSaved'));
      await refreshJobRunStatus(selectedRun.jobId);
    } catch (err) {
      if (seq !== runVersionSaveSeqRef.current || configFingerprint(buildConfigRef.current()) !== requestFingerprint) return;
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      if (seq === runVersionSaveSeqRef.current) setSaving(false);
    }
  }, [buildConfig, description, name, refreshJobRunStatus, savedFingerprint, selectedRun, t]);

  // Open a run directly in run-version edit mode (used by the ?graphEditJob
  // deep-link). Seeds the canvas from the run's current effective version.
  const openRunInEdit = useCallback(
    async (run: GraphRun) => {
      await selectRun(run);
      const config = runConfigSnapshot(run);
      if (!isGraphRunEditable(run.status)) {
        loadConfigIntoCanvas(config);
        setRunMessage(t('graph.messages.runNotEditable'));
        return;
      }
      loadConfigIntoCanvas(config);
      setErrors([]);
      setEditingRun(true);
      setSavedFingerprint(fingerprint({ name, description, config }));
      setMessage(t('graph.messages.editingRunHint'));
    },
    [description, loadConfigIntoCanvas, name, selectRun, t],
  );

  // Consume the ?graphEditJob deep-link once: fetch that run directly and open
  // it in edit mode, then strip the param so reloads don't re-trigger.
  useEffect(() => {
    const intent = editRunIntentRef.current;
    if (!intent) return;
    editRunIntentRef.current = null;
    const url = new URL(window.location.href);
    url.searchParams.delete('graphEditJob');
    window.history.replaceState({}, '', url.toString());
    void (async () => {
      try {
        const resp = await refreshJobRunStatus(intent);
        if (resp?.run) {
          await openRunInEdit(resp.run);
        } else {
          setRunMessage(t('graph.messages.runNotFound', { id: intent }));
        }
      } catch (err) {
        setRunMessage(err instanceof Error ? err.message : String(err));
      }
    })();
  }, [openRunInEdit, refreshJobRunStatus, t]);

  // ---- Canvas editing handlers ----
  // Checkpoint history for the undoable changes React Flow streams here. Drag
  // and resize are continuous (many changes per gesture) so we snapshot only on
  // the gesture's leading edge (dragging/resizing flips false→true). Removals
  // are discrete and always snapshot. Pure selection/measurement changes are not
  // undoable and never snapshot.
  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      let removed = false;
      let dragStart = false;
      let resizeStart = false;
      const removedIds = new Set<string>();
      for (const ch of changes) {
        if (ch.type === 'remove') {
          removed = true;
          removedIds.add(ch.id);
        } else if (ch.type === 'position') {
          if (ch.dragging && !draggingRef.current) dragStart = true;
          draggingRef.current = !!ch.dragging;
        } else if (ch.type === 'dimensions' && 'resizing' in ch) {
          if (ch.resizing && !resizingRef.current) resizeStart = true;
          resizingRef.current = !!ch.resizing;
        }
      }
      if (removed || dragStart || resizeStart) commitHistory();
      if (removed || dragStart || resizeStart) clearValidationState();
      // React Flow's delete cascade skips nodes flagged `deletable: false` (the
      // loop entry/exit markers), so deleting a loop container would orphan them
      // on the canvas. Expand any removal to the full subtree and synthesize the
      // missing remove changes so the markers and nested children go too.
      let effectiveChanges = changes;
      if (removedIds.size > 0) {
        const filtered = filterNodeRemoveChanges(changes, nodesRef.current);
        const doomed = filtered.removedIds;
        effectiveChanges = filtered.changes;
        if (doomed.size > 0) {
          // Drop edges touching any removed node — React Flow only cascades
          // edges for the nodes it actually deletes, which excludes the markers.
          setEdges((prev) => prev.filter((e) => !doomed.has(e.source) && !doomed.has(e.target)));
          setSelectedNodeId((cur) => (cur && doomed.has(cur) ? null : cur));
        }
      }
      setNodes((prev) => markEditableNodes(repinLoopPorts(applyNodeChanges(effectiveChanges, prev) as QuartetFlowNode[])));
    },
    [clearValidationState, commitHistory],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      // A node removal cascades into edge removals in the same tick; the ref
      // dedup in commitHistory collapses the pair into one undo step.
      if (changes.some((ch) => ch.type === 'remove')) {
        commitHistory();
        clearValidationState();
      }
      setEdges((prev) => applyEdgeChanges(changes, prev) as QuartetFlowEdge[]);
    },
    [clearValidationState, commitHistory],
  );

  // Mint a `${prefix}-${n}` id that collides with neither existing canvas ids
  // nor ids already minted in the same batch. The counter is seeded when a
  // workflow/run config is loaded, and collision checks cover older configs.
  const mintId = useCallback((prefix: string, taken: Set<string>): string => {
    let id = '';
    do {
      nodeCounterRef.current += 1;
      id = `${prefix}-${nodeCounterRef.current}`;
    } while (taken.has(id));
    taken.add(id);
    return id;
  }, []);

  const getConnectionError = useCallback((connection: Connection): string | null => {
    const sourceId = connection.source;
    const targetId = connection.target;
    if (!sourceId || !targetId) return t('graph.canvas.connectionInvalidEndpoint');
    if (sourceId === targetId) return t('graph.canvas.connectionSelf');
    const source = nodes.find((n) => n.id === sourceId)?.data.graphNode;
    const target = nodes.find((n) => n.id === targetId)?.data.graphNode;
    if (!source || !target) return t('graph.canvas.connectionMissingNode');
    if (source.type === 'end') return t('graph.canvas.connectionEndOut', { node: graphNodeLabel(source, sourceId) });
    if (target.type === 'start') return t('graph.canvas.connectionStartIn', { node: graphNodeLabel(target, targetId) });
    if ((source.parentId || '') !== (target.parentId || '')) return t('graph.canvas.connectionCrossScope');
    const branchPort = connection.sourceHandle === 'yes' || connection.sourceHandle === 'no' ? connection.sourceHandle : undefined;
    if (source.type === 'ifElse') {
      if (!branchPort) return t('graph.canvas.connectionIfElsePort');
      if (edges.some((edge) => edge.source === sourceId && edge.sourceHandle === branchPort)) {
        return t('graph.canvas.connectionIfElseDuplicate', { port: branchPort.toUpperCase() });
      }
    } else if (branchPort) {
      return t('graph.canvas.connectionBranchPort');
    }
    return null;
  }, [edges, nodes, t]);

  const isValidConnection = useCallback<IsValidConnection<QuartetFlowEdge>>(
    (connection) => !getConnectionError({
      source: connection.source,
      target: connection.target,
      sourceHandle: connection.sourceHandle ?? null,
      targetHandle: connection.targetHandle ?? null,
    }),
    [getConnectionError],
  );

  const onConnect = useCallback(
    (connection: Connection) => {
      const reason = getConnectionError(connection);
      if (reason) {
        setMessage(reason);
        return;
      }
      commitHistory();
      clearValidationState();
      setEdges((prev) => {
        const port = connection.sourceHandle === 'yes' || connection.sourceHandle === 'no' ? connection.sourceHandle : undefined;
        const edgeId = mintId(`edge-${connection.source}-${connection.target}`, new Set(prev.map((e) => e.id)));
        const edge: QuartetFlowEdge = {
          id: edgeId,
          source: connection.source!,
          target: connection.target!,
          sourceHandle: connection.sourceHandle ?? undefined,
          data: { port },
          markerEnd: { type: MarkerType.ArrowClosed, color: '#526158' },
          ...(port
            ? { label: port === 'yes' ? 'YES' : 'NO', labelStyle: { fill: port === 'yes' ? '#2ea043' : '#f85149', fontWeight: 700 } }
            : {}),
        };
        return addEdge(edge, prev) as QuartetFlowEdge[];
      });
    },
    [clearValidationState, commitHistory, getConnectionError, mintId],
  );

  const onAddNode = useCallback((type: GraphNodeType, position: { x: number; y: number }, parentId?: string | null) => {
    commitHistory();
    clearValidationState();
    const takenIds = new Set(nodesRef.current.map((n) => n.id));
    const created = createEditorNode(type, position, parentId, mintId, takenIds);
    setNodes((prev) => markEditableNodes(orderNodesByHierarchy([...prev, ...created])));
    setSelectedNodeId(created[0]?.id ?? null);
  }, [clearValidationState, commitHistory, mintId]);

  // Reassign a node's loop membership after a drag. React Flow positions child
  // nodes relative to their parent, so when parentId changes we convert the
  // dragged node's position between absolute and parent-relative coordinates,
  // keeping it visually put. Parents must precede children in the array.
  const onReparent = useCallback((nodeId: string, newParentId: string | null) => {
    // The drag that triggered this reparent already pushed a snapshot on its
    // leading edge (onNodesChange), so undo rewinds both the move and the
    // membership change together — don't snapshot again here.
    clearValidationState();
    setNodes((prev) => {
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
          const parentAbs = absPos(prev.find((p) => p.id === newParentId)!);
          pos = { x: abs.x - parentAbs.x, y: abs.y - parentAbs.y };
        }
        const graphNode: GraphNode = {
          ...n.data.graphNode,
          parentId: newParentId ?? undefined,
          layout: { ...(n.data.graphNode.layout || {}), x: Math.round(pos.x), y: Math.round(pos.y) },
        };
        const updated: QuartetFlowNode = {
          ...n,
          position: pos,
          parentId: newParentId ?? undefined,
          // Reset both nesting constraints, then re-apply the right one for the
          // new parent: a nested loop expands its parent, everything else is
          // pinned inside it. Leaving a loop (newParentId=null) clears both.
          ...clearNestConstraint,
          ...(newParentId ? nestConstraint(n.data.kind) : {}),
          data: { ...n.data, graphNode },
        };
        return updated;
      });
      return markEditableNodes(orderNodesByHierarchy(next));
    });
  }, [clearValidationState]);

  const patchGraphNode = useCallback((id: string, mutate: (gn: GraphNode) => GraphNode) => {
    setNodes((prev) =>
      markEditableNodes(prev.map((n) => (n.id === id ? { ...n, data: { ...n.data, graphNode: mutate(n.data.graphNode) } } : n))),
    );
  }, []);

  const onUpdateNode = useCallback(
    (id: string, patch: Partial<GraphNode>) => {
      // Coalesce by node + patched fields so typing into one field (e.g. title)
      // is a single undo step, while editing a different field starts a new one.
      commitHistory(`node:${id}:${Object.keys(patch).sort().join(',')}`);
      clearValidationState();
      patchGraphNode(id, (gn) => ({ ...gn, ...patch }));
    },
    [clearValidationState, commitHistory, patchGraphNode],
  );
  const onUpdateNodeConfig = useCallback(
    (id: string, patch: Partial<GraphNodeConfig>) => {
      commitHistory(`cfg:${id}:${Object.keys(patch).sort().join(',')}`);
      clearValidationState();
      patchGraphNode(id, (gn) => ({ ...gn, config: { ...gn.config, ...patch } }));
    },
    [clearValidationState, commitHistory, patchGraphNode],
  );
  const onDeleteNode = useCallback(
    (id: string) => {
      commitHistory();
      clearValidationState();
      const doomed = filterNodeRemoveChanges([{ type: 'remove', id }], nodesRef.current).removedIds;
      setNodes((prev) => markEditableNodes(prev.filter((n) => !doomed.has(n.id))));
      setEdges((prev) => prev.filter((e) => !doomed.has(e.source) && !doomed.has(e.target)));
      setSelectedNodeId((cur) => (cur && doomed.has(cur) ? null : cur));
    },
    [clearValidationState, commitHistory],
  );

  // Clone a selection snapshot into the live canvas with fresh ids and a paste
  // offset, then select the pasted roots. Shared by paste (from clipboardRef)
  // and duplicate (from an ad-hoc one/many-node selection). Reads current canvas
  // state via refs so a single setNodes/setEdges pair is enough.
  const insertClonedSelection = useCallback(
    (selection: { nodes: QuartetFlowNode[]; edges: QuartetFlowEdge[] }, offset: number) => {
      if (selection.nodes.length === 0) return;
      commitHistory();
      clearValidationState();
      const takenNodeIds = new Set(nodesRef.current.map((n) => n.id));
      const takenEdgeIds = new Set(edgesRef.current.map((e) => e.id));
      const cloned = cloneSelection(selection, mintId, takenNodeIds, takenEdgeIds, offset);
      // Clear any prior selection so only the freshly pasted nodes stay active.
      setNodes((prev) => {
        const deselected = prev.map((n) => (n.selected ? { ...n, selected: false } : n));
        return markEditableNodes(repinLoopPorts(orderNodesByHierarchy([...deselected, ...cloned.nodes])));
      });
      setEdges((prev) => [...prev, ...cloned.edges]);
      const firstRoot = cloned.rootIds[0] ?? null;
      if (firstRoot) setSelectedNodeId(firstRoot);
    },
    [clearValidationState, commitHistory, mintId],
  );

  // Resolve the directly-selected node ids: every canvas-multi-selected node
  // plus the inspector's single selection (they can diverge on mobile).
  const currentSelectionIds = useCallback((): Set<string> => {
    const ids = new Set<string>();
    for (const n of nodesRef.current) if (n.selected) ids.add(n.id);
    if (selectedNodeId) ids.add(selectedNodeId);
    return ids;
  }, [selectedNodeId]);

  // Copy the current selection (inspector node + any canvas multi-selection)
  // into the in-memory buffer.
  const onCopy = useCallback(() => {
    const rootSelected = currentSelectionIds();
    if (rootSelected.size === 0) return;
    const selection = collectSelection(nodesRef.current, edgesRef.current, rootSelected);
    if (selection.nodes.length === 0) return;
    clipboardRef.current = selection;
    setMessage(t('graph.messages.nodesCopied', { count: selection.nodes.length }));
  }, [currentSelectionIds, t]);

  const onPaste = useCallback(() => {
    if (clipboardRef.current) insertClonedSelection(clipboardRef.current, PASTE_OFFSET);
  }, [insertClonedSelection]);

  // Duplicate a specific node (and its subtree) in one step — used by the
  // inspector button and Cmd/Ctrl+D — without disturbing the copy buffer.
  const onDuplicateNode = useCallback(
    (id: string) => {
      const selection = collectSelection(nodesRef.current, edgesRef.current, new Set([id]));
      insertClonedSelection(selection, PASTE_OFFSET);
    },
    [insertClonedSelection],
  );

  // Duplicate the active canvas/inspector selection (Cmd/Ctrl+D on the canvas).
  const onDuplicate = useCallback(() => {
    const rootSelected = currentSelectionIds();
    if (rootSelected.size === 0) return;
    const selection = collectSelection(nodesRef.current, edgesRef.current, rootSelected);
    insertClonedSelection(selection, PASTE_OFFSET);
  }, [currentSelectionIds, insertClonedSelection]);


  const onUpdateVariables = useCallback(
    (variables: Record<string, string>, disabledVars: string[]) => {
      // Coalesce by the variable-table shape: editing a value keeps the same key
      // set so typing collapses, while add/remove/toggle changes the key set and
      // starts a fresh undo step.
      commitHistory(`vars:${Object.keys(variables).sort().join(',')}|${[...disabledVars].sort().join(',')}`);
      clearValidationState();
      setMeta((prev) => ({ ...prev, variables, disabledVars }));
    },
    [clearValidationState, commitHistory],
  );
  const onUpdateRunConfig = useCallback(
    (patch: Partial<GraphRunConfig>) => {
      commitHistory(`runcfg:${Object.keys(patch).sort().join(',')}`);
      clearValidationState();
      setMeta((prev) => ({ ...prev, runConfig: { ...prev.runConfig, ...patch } }));
    },
    [clearValidationState, commitHistory],
  );
  // Viewport (pan/zoom) is pure view state — never an undoable edit.
  const onViewportChange = useCallback((viewport: Viewport) => {
    setMeta((prev) => ({ ...prev, canvas: { viewport } }));
  }, []);

  // ---- Validation error -> canvas highlight + focus ----
  const errorNodeIds = useMemo(() => new Set(errors.map((e) => e.nodeId).filter(Boolean) as string[]), [errors]);
  const errorEdgeIds = useMemo(() => new Set(errors.map((e) => e.edgeId).filter(Boolean) as string[]), [errors]);
  const focusError = useCallback((err: GraphValidationError) => {
    if (err.nodeId) {
      setSelectedNodeId(err.nodeId);
      setInspectorDrawerOpen(true);
      setFocus({ nodeId: err.nodeId, token: Date.now() });
      return;
    }
    if (err.edgeId) {
      const edge = edgesRef.current.find((e) => e.id === err.edgeId);
      const targetNodeId = edge?.target || edge?.source;
      if (targetNodeId) {
        setSelectedNodeId(targetNodeId);
        setInspectorDrawerOpen(true);
        setFocus({ edgeId: err.edgeId, token: Date.now() });
      }
      return;
    }
    if (err.configKey || err.variable) {
      setSelectedNodeId(null);
      setInspectorDrawerOpen(true);
    }
  }, []);

  // ---- Run replay flow ----
  // In pure replay (viewingRun && !editingRun) the canvas is driven directly by
  // the run snapshot. Once editing, the editable nodes/edges state (seeded by
  // enterRunEdit) is the source of truth so user changes are captured.
  //
  // The replay topology is immutable for a given (run, version); a live run's
  // status reconcile replaces `selectedRun` with a fresh object every ~400ms, so
  // keying on the whole object would rebuild all nodes/edges twice a second on a
  // large graph. Key on id + version so the flow is built once per version.
  const replayFlow = useMemo(() => {
    if (!viewingRun || editingRun || !selectedRun) return null;
    return configToFlow(runConfigSnapshot(selectedRun));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editingRun, viewingRun, selectedRun?.id, selectedRun?.currentVersion]);
  const replayRunStatus = useMemo(() => runStatusByNode(runInstances), [runInstances]);
  // Edge active/pruned/done overlay for replay, mirroring GraphRunProgress's
  // mini-canvas. Only meaningful while viewing a run (runEdges is reset on exit).
  const replayEdgeStatus = useMemo(() => edgeStatusByEdge(runEdges), [runEdges]);
  // Pure replay restores the run's own saved viewport so it matches what ran.
  // Entering run-version edit opens the inspector, which narrows the canvas, so
  // the run's saved viewport (captured on a wider canvas) would push most nodes
  // off-screen — fit the graph instead. The normal workflow editor keeps its own
  // saved viewport.
  const canvasInitialViewport = editingRun
    ? undefined
    : viewingRun && selectedRun
      ? runConfigSnapshot(selectedRun).canvas?.viewport
      : meta.canvas?.viewport;
  // Switching between replay (read-only), run-version edit and the workflow
  // editor reuses the same mounted ReactFlow instance. React Flow caches node
  // measurements per instance, and a read-only → editable swap can leave them
  // stale so edges have no endpoints to draw (canvas looks empty until a node is
  // dragged). The same stale endpoint cache can happen after save: the editor is
  // temporarily locked read-only, then the just-saved config with the same node
  // IDs is loaded back into the same React Flow instance. Remount on mode changes,
  // config reloads and save lock/unlock transitions so every saved canvas gets a
  // fresh node measurement pass and its edges render immediately.
  const canvasMode = viewingRun ? (editingRun ? 'run-edit' : 'run-view') : 'editor';
  const canvasKey = `${canvasMode}-${viewportResetKey}-${saving ? 'saving' : 'ready'}`;

  // Nodes whose execution config is frozen during run-time editing: those with a
  // succeeded / skipped / running instance (mirrors backend validateVersionEdit).
  // The inspector disables their config fields; the backend still enforces.
  // Exception: a node inside a loop container re-runs each round against the
  // latest version, so its config stays editable mid-run (the loop container node
  // itself stays frozen). Mirrors nodeInsideLoop in services/graph/version.go.
  const frozenRunNodeIds = useMemo(() => {
    if (!editingRun) return undefined;
    const byId = new Map(nodes.map((n) => [n.id, n]));
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
    for (const inst of runInstances) {
      if (inst.status === 'succeeded' || inst.status === 'skipped' || inst.status === 'running') {
        if (insideLoop(inst.nodeId)) continue;
        frozen.add(inst.nodeId);
      }
    }
    return frozen;
  }, [editingRun, runInstances, nodes]);

  const canEditSelectedRun = canExecuteJobs && !!selectedRun && isGraphRunEditable(selectedRun.status);

  // ---- JSON advanced view ----
  // The JSON pane holds a DRAFT (jsonText). It is only the live config when the
  // user clicks "Apply to canvas" (applyJson) — the canvas/meta state remains the
  // source of truth for Validate/Save/Run, which is why those actions are
  // disabled while in JSON view. Switching INTO json seeds the draft from the
  // current canvas; switching back to canvas WITHOUT applying would silently drop
  // edits, so guard it: if the draft diverges from the canvas, confirm first.
  const switchView = useCallback(
    (mode: ViewMode) => {
      if (mode === 'json') {
        setJsonText(JSON.stringify(buildConfig(), null, 2));
        setViewMode('json');
        return;
      }
      // mode === 'canvas': warn if there is an unapplied JSON edit.
      const canvasJson = JSON.stringify(buildConfig(), null, 2);
      if (jsonText && jsonText !== canvasJson && !window.confirm(i18n.t('graph.messages.discardJsonDraftConfirm'))) {
        return;
      }
      setViewMode('canvas');
    },
    [buildConfig, jsonText],
  );
  const applyJson = useCallback(() => {
    try {
      const parsed = JSON.parse(jsonText) as GraphConfig;
      if (!parsed || !Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges)) {
        throw new Error(t('graph.messages.configNeedsNodesEdges'));
      }
      loadConfigIntoCanvas(parsed);
      setErrors([]);
      void validateConfigValue(parsed);
      setMessage(t('graph.messages.jsonApplied'));
      setViewMode('canvas');
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
  }, [jsonText, loadConfigIntoCanvas, t, validateConfigValue]);

  const selectWorkspace = useCallback(
    (ws: WorkspaceItem) => {
      commitHistory('workspace');
      clearValidationState();
      setMeta((prev) => ({ ...prev, workspaceId: ws.id, workdir: ws.workdir }));
      setWsDropdownOpen(false);
    },
    [clearValidationState, commitHistory],
  );

  // In replay the canvas shows the run's frozen topology (replayFlow); otherwise
  // it shows the live editable nodes. markEditableNodes mints new node objects,
  // so memoize it on replayFlow (rebuilt only per run version) to keep node
  // identity stable across the frequent live-run status reconciles.
  const replayCanvasNodes = useMemo(
    () => (replayFlow ? markEditableNodes(replayFlow.nodes) : null),
    [replayFlow],
  );
  const canvasNodes = viewingRun && replayCanvasNodes ? replayCanvasNodes : nodes;
  const canvasEdges = viewingRun && replayFlow ? replayFlow.edges : edges;

  return (
    <div className="graph-page">
      <HomeNavigation
        className="graph-page-header"
        workspaceTitle={workspaceTitle}
        workdir={workspaceWorkdir}
        refreshKey={navigationRefreshKey}
        activeView="graph"
        pageTitle={t('graph.header.title')}
        pageMark={<span className="graph-header-mark">◇</span>}
        onBack={() => { if (guardDiscard()) onClose(); }}
        backLabel={t('graph.header.back')}
        pageActions={isMobile ? (
            <button
              className="header-settings-btn"
              onClick={() => setLibraryOpen(true)}
              title={t('graph.header.openLibrary')}
              aria-label={t('graph.header.openLibrary')}
              data-testid="graph-library-toggle"
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="3" y1="6" x2="21" y2="6" />
                <line x1="3" y1="12" x2="21" y2="12" />
                <line x1="3" y1="18" x2="21" y2="18" />
              </svg>
            </button>
        ) : undefined}
        onOpenSettings={onOpenSettings}
        onOpenStats={onOpenStats}
        onOpenGraph={onOpenGraph}
      />

      <main className="graph-page-main">
        <aside className={`graph-sidebar${isMobile && libraryOpen ? ' graph-sidebar-open' : ''}`}>
          <div className="graph-sidebar-top">
            <div>
              <div className="graph-kicker">{workspaceTitle || workspaceId || t('graph.sidebar.workspace')}</div>
              <h2>{t('graph.sidebar.library')}</h2>
            </div>
            <div className="graph-sidebar-top-actions">
              <button
                className="graph-sidebar-icon-btn"
                onClick={() => {
                  void loadWorkflows();
                }}
                title={t('graph.sidebar.refresh')}
                aria-label={t('graph.sidebar.refresh')}
              >
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M21 12a9 9 0 1 1-2.64-6.36" />
                  <polyline points="21 3 21 9 15 9" />
                </svg>
              </button>
              {canWriteWorkflows && <button className="graph-primary-icon-btn" onClick={() => { if (guardDiscard()) startNew(); }} title={t('graph.sidebar.newWorkflow')} aria-label={t('graph.sidebar.newWorkflow')}>
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M12 5v14" />
                  <path d="M5 12h14" />
                </svg>
              </button>}
              {isMobile && (
                <button
                  className="graph-sidebar-close"
                  onClick={() => setLibraryOpen(false)}
                  title={t('graph.sidebar.closeLibrary')}
                  aria-label={t('graph.sidebar.closeLibrary')}
                  data-testid="graph-library-close"
                >
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <path d="M18 6 6 18" />
                    <path d="m6 6 12 12" />
                  </svg>
                </button>
              )}
            </div>
          </div>

          <div className="graph-library-tabs" role="tablist" aria-label={t('graph.sidebar.library')}>
            <button
              type="button"
              role="tab"
              aria-selected={activeTab === 'user'}
              className={`graph-library-tab${activeTab === 'user' ? ' active' : ''}`}
              data-testid="graph-library-tab-user"
              onClick={() => setActiveTab('user')}
            >
              {t('graph.sidebar.tabUser')}
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={activeTab === 'agent'}
              className={`graph-library-tab${activeTab === 'agent' ? ' active' : ''}`}
              data-testid="graph-library-tab-agent"
              onClick={() => setActiveTab('agent')}
            >
              {t('graph.sidebar.tabAgent')}
            </button>
          </div>

          <div className="graph-workflow-list">
            {loading ? (
              <div className="graph-empty">{t('graph.sidebar.loadingWorkflows')}</div>
            ) : sortedWorkflows.length === 0 ? (
              <div className="graph-empty">{t('graph.sidebar.noWorkflows')}</div>
            ) : (
              sortedWorkflows.map((wf) => (
                <button
                  key={wf.id}
                  type="button"
                  className={`graph-workflow-row ${wf.id === selectedId ? 'active' : ''}`}
                  data-testid={`graph-workflow-row-${wf.id}`}
                  onClick={() => { if (guardDiscard()) void selectWorkflow(wf); }}
                >
                  <span className="graph-workflow-row-title">{wf.name}</span>
                  <span className="graph-workflow-row-date">{formatDate(wf.updatedAt)}</span>
                  <span className="graph-workflow-row-workspace">
                    <span
                      className="graph-workflow-row-dot"
                      style={{ background: workspaceColor(wf.workspaceId || '') }}
                    />
                    {workflowWorkspaceLabel(wf)}
                  </span>
                </button>
              ))
            )}
          </div>

        </aside>

        {isMobile && libraryOpen && (
          <div className="graph-sidebar-overlay" onClick={() => setLibraryOpen(false)} aria-hidden="true" />
        )}

        <section className="graph-editor">
          <div className="graph-editor-head">
            <div className="graph-editor-title">
              {!viewingRun && allWorkspaces.length > 0 && (
                <div className="graph-ws-selector" ref={wsDropdownRef}>
                  <button
                    className="graph-ws-trigger"
                    type="button"
                    data-testid="graph-workspace-trigger"
                    title={t('graph.workspace.label')}
                    onClick={() => setWsDropdownOpen((v) => !v)}
                  >
                    <span
                      className="graph-ws-dot"
                      style={{ background: workspaceColor(allWorkspaces.find((w) => w.id === meta.workspaceId) ?? meta.workspaceId) }}
                    />
                    <span className="graph-ws-label">
                      {allWorkspaces.find((w) => w.id === meta.workspaceId)?.title || t('graph.workspace.label')}
                    </span>
                    <svg className="graph-ws-caret" width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
                      <path d="M2 3.5l3 3 3-3" />
                    </svg>
                  </button>
                  {wsDropdownOpen && (
                    <div className="graph-ws-dropdown">
                      {allWorkspaces.map((ws) => (
                        <button
                          key={ws.id}
                          type="button"
                          className={`graph-ws-item${meta.workspaceId === ws.id ? ' active' : ''}`}
                          data-testid="graph-workspace-item"
                          data-workspace-id={ws.id}
                          onClick={() => selectWorkspace(ws)}
                        >
                          <span className="graph-ws-item-dot" style={{ background: workspaceColor(ws) }} />
                          <span className="graph-ws-item-title">{ws.title || ws.id}</span>
                          <span className="graph-ws-item-path">{ws.workdir}</span>
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              )}
              {hasJsonDraft ? (
                <span className="graph-dirty-badge" data-testid="graph-dirty-badge">{t('graph.editor.jsonDraft')}</span>
              ) : dirty ? (
                <span className="graph-dirty-badge" data-testid="graph-dirty-badge">{t('graph.editor.unsaved')}</span>
              ) : !selectedId && !viewingRun ? (
                <span className="graph-draft-badge" data-testid="graph-draft-badge">{t('graph.editor.draft')}</span>
              ) : (
                <span className="graph-clean-badge" data-testid="graph-clean-badge">{t('graph.editor.saved')}</span>
              )}
              <input
                className="graph-name-input"
                data-testid="graph-name-input"
                value={name}
                disabled={!canWriteWorkflows || (viewingRun && !editingRun) || editingLocked}
                onChange={(e) => {
                  clearValidationState();
                  setName(e.target.value);
                }}
                placeholder={t('graph.editor.namePlaceholder')}
                aria-label={t('graph.editor.namePlaceholder')}
              />
              <input
                className="graph-desc-input"
                data-testid="graph-description-input"
                value={description}
                disabled={!canWriteWorkflows || (viewingRun && !editingRun) || editingLocked}
                onChange={(e) => {
                  clearValidationState();
                  setDescription(e.target.value);
                }}
                placeholder={t('graph.editor.descPlaceholder')}
                aria-label={t('graph.editor.descPlaceholder')}
              />
            </div>
            <div className="graph-editor-actions">
              {/* Read-only run replay locks the view toggle: the JSON view would
                  seed from (and Apply into) the hidden editor document, not the
                  run snapshot on screen. Run-version EDIT stays allowed — there
                  the editor state IS the run's working copy. */}
              <div className="graph-view-toggle">
                <button data-testid="graph-view-canvas" className={viewMode === 'canvas' ? 'active' : ''} onClick={() => switchView('canvas')} disabled={editingLocked || (viewingRun && !editingRun)}>
                  <GraphButtonIcon name="canvas" />
                  {t('graph.editor.canvas')}
                </button>
                <button data-testid="graph-view-json" className={viewMode === 'json' ? 'active' : ''} onClick={() => switchView('json')} disabled={!canWriteWorkflows || editingLocked || (viewingRun && !editingRun)}>
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
                      disabled={!canExecuteJobs || editingLocked || viewMode === 'json' || !runVersionDirty}
                      title={viewMode === 'json' ? t('graph.editor.applyJsonFirst') : undefined}
                    >
                      <GraphButtonIcon name="save" />
                      {saving ? t('graph.editor.saving') : t('graph.editor.saveRunVersion')}
                    </button>
                    <button className="graph-secondary-btn" data-testid="graph-cancel-run-edit" onClick={() => { if (guardDiscard()) cancelRunEdit(); }} disabled={editingLocked}>
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
                  <div className="graph-actions-overflow" ref={actionsMenuRef}>
                    <button
                      type="button"
                      className="graph-secondary-btn graph-actions-more"
                      data-testid="graph-actions-more"
                      aria-label={t('graph.editor.moreActions')}
                      aria-expanded={actionsMenuOpen}
                      onClick={() => setActionsMenuOpen((v) => !v)}
                    >
                      ⋯
                    </button>
                    <div className={`graph-actions-menu${actionsMenuOpen ? ' open' : ''}`}>
                      <button className="graph-secondary-btn" data-testid="graph-validate" onClick={() => { setActionsMenuOpen(false); void validate(); }} disabled={editingLocked || viewMode === 'json'} title={viewMode === 'json' ? t('graph.editor.applyJsonFirst') : undefined}>
                        <GraphButtonIcon name="check" />
                        {t('graph.editor.validate')}
                      </button>
                      <button className="graph-secondary-btn" data-testid="graph-reset" onClick={() => { setActionsMenuOpen(false); resetChanges(); }} disabled={editingLocked || viewMode === 'json' || !dirty}>
                        <GraphButtonIcon name="reset" />
                        {t('graph.editor.reset')}
                      </button>
                      {canWriteWorkflows && selectedWorkflow && (
                        <button className="graph-danger-btn" data-testid="graph-delete" onClick={() => { setActionsMenuOpen(false); if (guardDiscard()) setDeleteTarget(selectedWorkflow); }} disabled={editingLocked}>
                          <GraphButtonIcon name="trash" />
                          {t('graph.editor.delete')}
                        </button>
                      )}
                      {canWriteWorkflows && <button className="graph-secondary-btn" data-testid="graph-save-as-new" onClick={() => { setActionsMenuOpen(false); void save('create'); }} disabled={editingLocked || viewMode === 'json'} title={viewMode === 'json' ? t('graph.editor.applyJsonFirst') : undefined}>
                        <GraphButtonIcon name="copy" />
                        {t('graph.editor.saveAsNew')}
                      </button>}
                    </div>
                  </div>
                  {canWriteWorkflows && <button className="graph-primary-btn" data-testid="graph-save" onClick={() => void save(selectedWorkflow ? 'update' : 'create')} disabled={editingLocked || viewMode === 'json'} title={viewMode === 'json' ? t('graph.editor.applyJsonFirst') : undefined}>
                    <GraphButtonIcon name="save" />
                    {selectedWorkflow ? t('graph.editor.save') : t('graph.editor.create')}
                  </button>}
                  {canExecuteWorkflows && <button className="graph-run-btn" data-testid="graph-run" onClick={() => void startRun()} disabled={editingLocked || viewMode === 'json'} title={viewMode === 'json' ? t('graph.editor.applyJsonFirst') : undefined}>
                    <GraphButtonIcon name="play" />
                    {startingRun ? t('graph.editor.starting') : t('graph.editor.run')}
                  </button>}
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
                <textarea spellCheck={false} readOnly={!canWriteWorkflows} data-testid="graph-json-textarea" aria-label={t('graph.editor.jsonConfigAria')} value={jsonText} onChange={(e) => setJsonText(e.target.value)} />
                <div className="graph-json-actions">
                  {canWriteWorkflows && <button
                    className="graph-primary-btn"
                    data-testid="graph-json-apply"
                    onClick={applyJson}
                    disabled={viewingRun && !editingRun}
                  >
                    <GraphButtonIcon name="apply" />
                    {t('graph.editor.applyToCanvas')}
                  </button>}
                </div>
              </div>
            ) : (
              <>
                <GraphCanvas
                  key={canvasKey}
                  nodes={canvasNodes}
                  edges={canvasEdges}
                  readOnly={!canWriteWorkflows || (viewingRun && !editingRun) || editingLocked}
                  allowNodeDrag={editingRun && !editingLocked}
                  showMiniMap={!isMobile}
                  isMobile={isMobile}
                  addIntoLoopId={addIntoLoopId}
                  runStatusByNodeId={viewingRun ? replayRunStatus : undefined}
                  edgeStatusById={viewingRun && !editingRun ? replayEdgeStatus : undefined}
                  errorNodeIds={viewingRun && !editingRun ? undefined : errorNodeIds}
                  errorEdgeIds={viewingRun && !editingRun ? undefined : errorEdgeIds}
                  focus={focus}
                  initialViewport={canvasInitialViewport}
                  viewportResetKey={viewportResetKey}
                  onNodesChange={onNodesChange}
                  onEdgesChange={onEdgesChange}
                  onConnect={onConnect}
                  isValidConnection={isValidConnection}
                  getConnectionError={getConnectionError}
                  onNodeClick={(id) => {
                    setSelectedNodeId(id);
                    if (isMobile) setInspectorDrawerOpen(true);
                  }}
                  onPaneClick={() => {
                    setSelectedNodeId(null);
                  }}
                  onAddNode={canWriteWorkflows ? onAddNode : () => undefined}
                  onReparent={onReparent}
                  onViewportChange={onViewportChange}
                  onCopy={onCopy}
                  onPaste={onPaste}
                  onDuplicate={onDuplicate}
                  onUndo={undo}
                  onRedo={redo}
                  onHistoryCommit={commitHistory}
                  canUndo={canUndo}
                  canRedo={canRedo}
                />
                {(!viewingRun || editingRun) && (
                  <GraphInspector
                    node={selectedGraphNode}
                    config={inspectorConfig}
                    agents={agents}
                    readOnly={!canWriteWorkflows || editingLocked}
                    drawerOpen={!isMobile || inspectorDrawerOpen}
                    frozenNodeIds={frozenRunNodeIds}
                    lockRunConfig={editingRun}
                    onUpdateNode={onUpdateNode}
                    onUpdateNodeConfig={onUpdateNodeConfig}
                    onDeleteNode={onDeleteNode}
                    canDeleteNode={canDeleteSelectedNode}
                    onDuplicateNode={editingRun ? undefined : onDuplicateNode}
                    onUpdateVariables={onUpdateVariables}
                    onUpdateRunConfig={onUpdateRunConfig}
                    onDrawerToggle={() => setInspectorDrawerOpen((open) => !open)}
                  />
                )}
                {viewingRun && !editingRun && selectedNodeId && (
                  <GraphRunInspector
                    node={selectedGraphNode}
                    instance={runInstances.find((i) => i.nodeId === selectedNodeId)}
                    hookResult={hookResults[selectedNodeId]}
                    drawerOpen={!isMobile || inspectorDrawerOpen}
                    onDrawerToggle={() => setInspectorDrawerOpen((open) => !open)}
                  />
                )}
              </>
            )}
          </div>

          {(message || runMessage || agentListError || workspaceListError || listWarnings.length > 0 || errors.length > 0) && (
            <div className={`graph-status ${errors.length > 0 ? 'error' : ''}`} data-testid="graph-status" role="status" aria-live="polite">
              {message && <div data-testid="graph-message">{message}</div>}
              {!viewingRun && runMessage && <div data-testid="graph-run-message">{runMessage}</div>}
              {agentListError && <div data-testid="graph-agent-list-error">{t('graph.messages.agentListFailed', { detail: agentListError })}</div>}
              {workspaceListError && <div data-testid="graph-workspace-list-error">{t('graph.messages.workspaceListFailed', { detail: workspaceListError })}</div>}
              {listWarnings.length > 0 && (
                <div data-testid="graph-workflow-warnings">
                  <div>{t('graph.messages.workflowFilesSkipped', { count: listWarnings.length })}</div>
                  <ul>
                    {listWarnings.map((w) => (
                      <li key={w.file}>{w.file}: {w.error}</li>
                    ))}
                  </ul>
                </div>
              )}
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
        <div className="delete-confirm-overlay" onClick={() => { if (!deleting) setDeleteTarget(null); }}>
          <div className="delete-confirm-dialog" onClick={(e) => e.stopPropagation()}>
            <h3>{t('graph.dialogs.deleteWorkflowTitle')}</h3>
            <p>{t('graph.dialogs.deleteWorkflowBody', { name: deleteTarget.name })}</p>
            <div className="delete-confirm-actions">
              <button className="delete-confirm-cancel" onClick={() => setDeleteTarget(null)} disabled={deleting}>
                <GraphButtonIcon name="cancel" />
                {t('graph.dialogs.cancel')}
              </button>
              <button className="delete-confirm-ok" onClick={() => void confirmDelete()} disabled={deleting}>
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
