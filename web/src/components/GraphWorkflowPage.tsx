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
  clearNestConstraint,
  configToFlow,
  flowToConfig,
  nestConstraint,
  orderNodesByHierarchy,
  repinLoopPorts,
  runConfigSnapshot,
  runStatusByNode,
  type QuartetFlowEdge,
  type QuartetFlowNode,
} from './graph/graphFlowAdapter';
import { GraphCanvas, type GraphCanvasFocus } from './graph/GraphCanvas';
import { GraphInspector, BUILTIN_VARS } from './graph/GraphInspector';
import {
  LOOP_DEFAULT_HEIGHT,
  LOOP_DEFAULT_WIDTH,
  QG_LOOP_PORT_H,
  QG_LOOP_PORT_W,
  loopPortPosition,
} from './graph/graphFlowAdapter';
import { registerWorkspaceColors, workspaceColor } from '../utils/workspace';
import './GraphWorkflowPage.css';

// Workspace list item shape, mirrored from ChatPage's /workspace/list usage.
type WorkspaceItem = { id: string; title: string; description: string; workdir: string; color?: string };

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

// Expand a set of to-be-deleted node ids to include every descendant, so
// deleting a loop container also removes its body AND its entry/exit markers.
// We can't lean on React Flow's native cascade: its getElementsToRemove skips
// any node with `deletable: false` (which the loop markers carry to block solo
// deletion), leaving them orphaned on the canvas when their loop is removed.
function withDescendants(ids: Set<string>, nodes: QuartetFlowNode[]): Set<string> {
  const childrenOf = new Map<string, string[]>();
  for (const n of nodes) {
    if (!n.parentId) continue;
    const list = childrenOf.get(n.parentId);
    if (list) list.push(n.id);
    else childrenOf.set(n.parentId, [n.id]);
  }
  const out = new Set<string>();
  const stack = [...ids];
  while (stack.length) {
    const id = stack.pop() as string;
    if (out.has(id)) continue;
    out.add(id);
    for (const child of childrenOf.get(id) || []) stack.push(child);
  }
  return out;
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
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  return `${mm}-${dd}`;
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
  // Normalize built-in variables so a legacy config (no Code/Doc) and the
  // editor's auto-filled state hash identically — opening such a workflow must
  // not register as an unsaved change.
  const normalized = { ...configWithoutCanvas, variables: withBuiltinVars(configWithoutCanvas.variables) };
  return stableStringify({ name: input.name, description: input.description, config: normalized });
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
  // Mobile-only: the workflow library is an off-canvas left drawer (it would
  // otherwise collapse to a sliver in the stacked layout). Desktop ignores this.
  const [libraryOpen, setLibraryOpen] = useState(false);
  const [focus, setFocus] = useState<GraphCanvasFocus>({ token: 0 });
  const [loadedConfig, setLoadedConfig] = useState<GraphConfig>(initialConfig);
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
  const wsDropdownRef = useRef<HTMLDivElement>(null);

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
  const sortedWorkflows = useMemo(
    () => [...workflows].sort((a, b) => (a.name || '').localeCompare(b.name || '', undefined, { numeric: true, sensitivity: 'base' })),
    [workflows],
  );
  const selectedGraphNode: GraphNode | null = useMemo(() => {
    if (!selectedNodeId) return null;
    return nodes.find((n) => n.id === selectedNodeId)?.data.graphNode ?? null;
  }, [nodes, selectedNodeId]);

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
      setNodes(markEditable(flow.nodes));
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
    nodesRef.current = snap.nodes;
    edgesRef.current = snap.edges;
    metaRef.current = meta;
    setNodes(snap.nodes);
    setEdges(snap.edges);
    setMeta(meta);
  }, []);

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

  const currentFingerprint = useMemo(
    () => fingerprint({ name, description, config: buildConfig() }),
    [buildConfig, description, name],
  );
  const dirty = (!viewingRun || editingRun) && currentFingerprint !== savedFingerprint;

  // The inspector reads global meta (variables/disabledVars/runConfig) AND the
  // live node/edge structure — the latter drives condition variable suggestions
  // (upstream outputs, loop iteration vars). Build from the current canvas so a
  // node moved into/out of a loop body updates its available variables.
  const inspectorConfig = useMemo<GraphConfig>(
    () => flowToConfig(nodes, edges, { nodes: [], edges: [], ...meta }),
    [nodes, edges, meta],
  );

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
    // Load the workspace list for the workspace selector (mirrors ChatPage).
    void (async () => {
      try {
        const res = await fetch('/api/v1/workspace/list');
        if (!res.ok) return;
        const data = await res.json().catch(() => null);
        const list = (data?.workspaces || []) as WorkspaceItem[];
        registerWorkspaceColors(list);
        setAllWorkspaces(list);
      } catch {
        /* workspace list is optional for editing */
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Close the workspace dropdown on outside click (mirrors LoopConfigPanel).
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
        // Legacy configs may not carry workspaceId inside config — fall back to
        // the workflow record's top-level workspaceId so the selector shows it.
        const loaded: GraphConfig = {
          ...data.workflow.config,
          workspaceId: data.workflow.config.workspaceId || data.workflow.workspaceId,
        };
        loadConfigIntoCanvas(loaded);
        setSavedFingerprint(fingerprint({
          name: data.workflow.name,
          description: data.workflow.description || '',
          config: loaded,
        }));
        setLibraryOpen(false);
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
    setLibraryOpen(false);
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
          mode === 'create'
            ? { name: trimmedName, description, workspaceId: config.workspaceId || workspaceId, config }
            : { name: trimmedName, description, config };
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
        body: JSON.stringify({
          workflowId: selectedId || undefined,
          workspaceId: config.workspaceId || workspaceId,
          workdir: config.workdir || workspaceWorkdir,
          config,
        }),
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
      // React Flow's delete cascade skips nodes flagged `deletable: false` (the
      // loop entry/exit markers), so deleting a loop container would orphan them
      // on the canvas. Expand any removal to the full subtree and synthesize the
      // missing remove changes so the markers and nested children go too.
      let effectiveChanges = changes;
      if (removedIds.size > 0) {
        const doomed = withDescendants(removedIds, nodesRef.current);
        if (doomed.size > removedIds.size) {
          const extra: NodeChange[] = [];
          for (const id of doomed) {
            if (!removedIds.has(id)) extra.push({ type: 'remove', id });
          }
          effectiveChanges = [...changes, ...extra];
          // Drop edges touching any removed node — React Flow only cascades
          // edges for the nodes it actually deletes, which excludes the markers.
          setEdges((prev) => prev.filter((e) => !doomed.has(e.source) && !doomed.has(e.target)));
          setSelectedNodeId((cur) => (cur && doomed.has(cur) ? null : cur));
        }
      }
      setNodes((prev) => repinLoopPorts(applyNodeChanges(effectiveChanges, prev) as QuartetFlowNode[]));
    },
    [commitHistory],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      // A node removal cascades into edge removals in the same tick; the ref
      // dedup in commitHistory collapses the pair into one undo step.
      if (changes.some((ch) => ch.type === 'remove')) commitHistory();
      setEdges((prev) => applyEdgeChanges(changes, prev) as QuartetFlowEdge[]);
    },
    [commitHistory],
  );
  const onConnect = useCallback(
    (connection: Connection) => {
      commitHistory();
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
    },
    [commitHistory],
  );

  const onAddNode = useCallback((type: GraphNodeType, position: { x: number; y: number }, parentId?: string | null) => {
    commitHistory();
    nodeCounterRef.current += 1;
    const id = `${type}-${nodeCounterRef.current}`;
    // A loop container can hold business nodes, ifElse, nested loops and the loop
    // entry/exit markers — but a fresh start/end is never dropped from the
    // palette. Dropping onto a loop is ignored for loop (loops stay top-level).
    const intoParent = parentId && type !== 'loop' ? parentId : undefined;
    const graphNode: GraphNode = {
      id,
      type,
      title: '',
      ...(intoParent ? { parentId: intoParent } : {}),
      config: type === 'loop' ? { loopMode: 'fixed', fixedCount: 1 } : type === 'evaluator' ? { sessionStrategy: 'new' } : type === 'prompt' ? { sessionStrategy: 'new' } : {},
      layout: { x: Math.round(position.x), y: Math.round(position.y), ...(type === 'loop' ? { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT } : {}) },
    };
    const node: QuartetFlowNode = {
      id,
      type: type === 'loop' ? 'loopGroup' : 'quartet',
      position,
      ...(intoParent ? { parentId: intoParent, ...nestConstraint(type) } : {}),
      data: { kind: type, graphNode },
      deletable: type !== 'start' && type !== 'end',
      ...(type === 'loop' ? { style: { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT } } : {}),
    };
    // A loop container is invalid without exactly one entry marker (a loop-scoped
    // start, on the left border) and at least one exit marker (a loop-scoped end,
    // on the right border). Since start/end are not in the palette, seed both
    // automatically so the loop is usable and passes backend validation out of
    // the box. The user wires entry → body → exit. The markers are port tabs
    // pinned flush on each border (position from loopPortPosition, not draggable),
    // so they read as connection points on the container edge.
    const extra: QuartetFlowNode[] = [];
    if (type === 'loop') {
      const entryPos = loopPortPosition(LOOP_DEFAULT_WIDTH, LOOP_DEFAULT_HEIGHT, true);
      const exitPos = loopPortPosition(LOOP_DEFAULT_WIDTH, LOOP_DEFAULT_HEIGHT, false);
      nodeCounterRef.current += 1;
      const startId = `start-${nodeCounterRef.current}`;
      const startGraphNode: GraphNode = {
        id: startId,
        type: 'start',
        title: '',
        parentId: id,
        layout: { x: entryPos.x, y: entryPos.y },
      };
      extra.push({
        id: startId,
        type: 'quartet',
        position: entryPos,
        parentId: id,
        extent: 'parent',
        style: { width: QG_LOOP_PORT_W, height: QG_LOOP_PORT_H },
        draggable: false,
        data: { kind: 'start', graphNode: startGraphNode },
        deletable: false,
      });
      nodeCounterRef.current += 1;
      const endId = `end-${nodeCounterRef.current}`;
      const endGraphNode: GraphNode = {
        id: endId,
        type: 'end',
        title: '',
        parentId: id,
        layout: { x: exitPos.x, y: exitPos.y },
      };
      extra.push({
        id: endId,
        type: 'quartet',
        position: exitPos,
        parentId: id,
        extent: 'parent',
        style: { width: QG_LOOP_PORT_W, height: QG_LOOP_PORT_H },
        draggable: false,
        data: { kind: 'end', graphNode: endGraphNode },
        deletable: false,
      });
    }
    setNodes((prev) => orderNodesByHierarchy([...prev, node, ...extra]));
    setSelectedNodeId(id);
  }, [commitHistory]);

  // Reassign a node's loop membership after a drag. React Flow positions child
  // nodes relative to their parent, so when parentId changes we convert the
  // dragged node's position between absolute and parent-relative coordinates,
  // keeping it visually put. Parents must precede children in the array.
  const onReparent = useCallback((nodeId: string, newParentId: string | null) => {
    // The drag that triggered this reparent already pushed a snapshot on its
    // leading edge (onNodesChange), so undo rewinds both the move and the
    // membership change together — don't snapshot again here.
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
      return orderNodesByHierarchy(next);
    });
  }, []);

  const patchGraphNode = useCallback((id: string, mutate: (gn: GraphNode) => GraphNode) => {
    setNodes((prev) =>
      prev.map((n) => (n.id === id ? { ...n, data: { ...n.data, graphNode: mutate(n.data.graphNode) } } : n)),
    );
  }, []);

  const onUpdateNode = useCallback(
    (id: string, patch: Partial<GraphNode>) => {
      // Coalesce by node + patched fields so typing into one field (e.g. title)
      // is a single undo step, while editing a different field starts a new one.
      commitHistory(`node:${id}:${Object.keys(patch).sort().join(',')}`);
      patchGraphNode(id, (gn) => ({ ...gn, ...patch }));
    },
    [commitHistory, patchGraphNode],
  );
  const onUpdateNodeConfig = useCallback(
    (id: string, patch: Partial<GraphNodeConfig>) => {
      commitHistory(`cfg:${id}:${Object.keys(patch).sort().join(',')}`);
      patchGraphNode(id, (gn) => ({ ...gn, config: { ...gn.config, ...patch } }));
    },
    [commitHistory, patchGraphNode],
  );
  const onDeleteNode = useCallback(
    (id: string) => {
      commitHistory();
      // Remove the node and its entire subtree (a loop's body + entry/exit
      // markers, including nested loops) — not just its direct children.
      const doomed = withDescendants(new Set([id]), nodesRef.current);
      setNodes((prev) => prev.filter((n) => !doomed.has(n.id)));
      setEdges((prev) => prev.filter((e) => !doomed.has(e.source) && !doomed.has(e.target)));
      setSelectedNodeId((cur) => (cur && doomed.has(cur) ? null : cur));
    },
    [commitHistory],
  );

  // Mint a `${prefix}-${n}` id that collides with neither the live canvas nor
  // ids already minted in the same paste batch. The shared nodeCounterRef keeps
  // numbers climbing across adds/edges; `taken` guards against the counter ever
  // overlapping an id loaded from a saved workflow (which the counter is never
  // seeded from).
  const mintId = useCallback((prefix: string, taken: Set<string>): string => {
    let id = '';
    do {
      nodeCounterRef.current += 1;
      id = `${prefix}-${nodeCounterRef.current}`;
    } while (taken.has(id));
    taken.add(id);
    return id;
  }, []);

  // Clone a selection snapshot into the live canvas with fresh ids and a paste
  // offset, then select the pasted roots. Shared by paste (from clipboardRef)
  // and duplicate (from an ad-hoc one/many-node selection). Reads current canvas
  // state via refs so a single setNodes/setEdges pair is enough.
  const insertClonedSelection = useCallback(
    (selection: { nodes: QuartetFlowNode[]; edges: QuartetFlowEdge[] }, offset: number) => {
      if (selection.nodes.length === 0) return;
      commitHistory();
      const takenNodeIds = new Set(nodesRef.current.map((n) => n.id));
      const takenEdgeIds = new Set(edgesRef.current.map((e) => e.id));
      const cloned = cloneSelection(selection, mintId, takenNodeIds, takenEdgeIds, offset);
      // Clear any prior selection so only the freshly pasted nodes stay active.
      setNodes((prev) => {
        const deselected = prev.map((n) => (n.selected ? { ...n, selected: false } : n));
        return repinLoopPorts(orderNodesByHierarchy([...deselected, ...cloned.nodes]));
      });
      setEdges((prev) => [...prev, ...cloned.edges]);
      const firstRoot = cloned.rootIds[0] ?? null;
      if (firstRoot) setSelectedNodeId(firstRoot);
    },
    [commitHistory, mintId],
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
      setMeta((prev) => ({ ...prev, variables, disabledVars }));
    },
    [commitHistory],
  );
  const onUpdateRunConfig = useCallback(
    (patch: Partial<GraphRunConfig>) => {
      commitHistory(`runcfg:${Object.keys(patch).sort().join(',')}`);
      setMeta((prev) => ({ ...prev, runConfig: { ...prev.runConfig, ...patch } }));
    },
    [commitHistory],
  );
  // Viewport (pan/zoom) is pure view state — never an undoable edit.
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
  // dragged). Remounting on mode change forces a fresh measure so edges render.
  const canvasMode = viewingRun ? (editingRun ? 'run-edit' : 'run-view') : 'editor';

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
          {isMobile && (
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
          )}
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
        <aside className={`graph-sidebar${isMobile && libraryOpen ? ' graph-sidebar-open' : ''}`}>
          <div className="graph-sidebar-top">
            <div>
              <div className="graph-kicker">{workspaceTitle || workspaceId || t('graph.sidebar.workspace')}</div>
              <h2>{t('graph.sidebar.library')}</h2>
            </div>
            <div className="graph-sidebar-top-actions">
              <button className="graph-primary-icon-btn" onClick={startNew} title={t('graph.sidebar.newWorkflow')} aria-label={t('graph.sidebar.newWorkflow')}>
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M12 5v14" />
                  <path d="M5 12h14" />
                </svg>
              </button>
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

          <div className="graph-workflow-list">
            {loading ? (
              <div className="graph-empty">{t('graph.sidebar.loadingWorkflows')}</div>
            ) : workflows.length === 0 ? (
              <div className="graph-empty">{t('graph.sidebar.noWorkflows')}</div>
            ) : (
              sortedWorkflows.map((wf) => (
                <button
                  key={wf.id}
                  type="button"
                  className={`graph-workflow-row ${wf.id === selectedId ? 'active' : ''}`}
                  data-testid={`graph-workflow-row-${wf.id}`}
                  onClick={() => void selectWorkflow(wf)}
                >
                  <span className="graph-workflow-row-title">{wf.name}</span>
                  <span className="graph-workflow-row-date">{formatDate(wf.updatedAt)}</span>
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
                        <div
                          key={ws.id}
                          className={`graph-ws-item${meta.workspaceId === ws.id ? ' active' : ''}`}
                          data-testid="graph-workspace-item"
                          data-workspace-id={ws.id}
                          onClick={() => {
                            setMeta((prev) => ({ ...prev, workspaceId: ws.id, workdir: ws.workdir }));
                            setWsDropdownOpen(false);
                          }}
                        >
                          <span className="graph-ws-item-dot" style={{ background: workspaceColor(ws) }} />
                          <span className="graph-ws-item-title">{ws.title || ws.id}</span>
                          <span className="graph-ws-item-path">{ws.workdir}</span>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}
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
                  key={canvasMode}
                  nodes={canvasNodes}
                  edges={canvasEdges}
                  readOnly={viewingRun}
                  allowNodeDrag={editingRun}
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
                  onReparent={editingRun ? undefined : onReparent}
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
                    lockStructure={editingRun}
                    drawerOpen={!isMobile || inspectorDrawerOpen}
                    frozenNodeIds={frozenRunNodeIds}
                    onUpdateNode={onUpdateNode}
                    onUpdateNodeConfig={onUpdateNodeConfig}
                    onDeleteNode={onDeleteNode}
                    onDuplicateNode={editingRun ? undefined : onDuplicateNode}
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
