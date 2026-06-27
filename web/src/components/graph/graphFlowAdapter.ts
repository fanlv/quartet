import type { Edge, Node } from '@xyflow/react';
import { MarkerType } from '@xyflow/react';
import type {
  GraphConfig,
  GraphEdge,
  GraphEdgePort,
  GraphEdgeState,
  GraphInstanceState,
  GraphInstanceStatus,
  GraphNode,
  GraphNodeType,
  GraphRun,
} from '../../types/graph';

// Default geometry for loop containers when layout width/height are absent.
export const LOOP_DEFAULT_WIDTH = 560;
export const LOOP_DEFAULT_HEIGHT = 320;

// Size of a loop entry/exit marker. The marker is a port "tab" mounted flush on
// the container border (not a free-floating node), so it has a fixed box that
// the CSS fills and pins to the left (entry) / right (exit) edge.
export const QG_LOOP_PORT_W = 62;
export const QG_LOOP_PORT_H = 26;

// Position of a loop entry/exit marker in the loop's child coordinate space.
// Both are vertically centred on the border midline; the entry hugs the left
// border (x=0) and the exit hugs the right border (x=width-portWidth) so each
// tab's flat edge lands exactly on the boundary. Marker positions are fully
// derived from the container size (the markers are not user-draggable), so this
// is the single source of truth used at seed, load and resize time.
export function loopPortPosition(
  loopWidth: number,
  loopHeight: number,
  isEntry: boolean,
): { x: number; y: number } {
  return {
    x: isEntry ? 0 : Math.round(loopWidth - QG_LOOP_PORT_W),
    y: Math.round(loopHeight / 2 - QG_LOOP_PORT_H / 2),
  };
}

// A loop-scoped start/end node (parentId set) is the loop's entry / exit border
// marker, rendered as a pinned port tab rather than a normal draggable node.
export function isLoopPort(node: { type: GraphNodeType; parentId?: string }): boolean {
  return !!node.parentId && (node.type === 'start' || node.type === 'end');
}

// Live (post-resize) size of a loop container node. NodeResizer writes the new
// size to node.width/height; before any resize it lives in node.style. Mirrors
// the precedence flowToConfig uses when persisting the container size.
function loopNodeSize(node: QuartetFlowNode): { width: number; height: number } {
  const style = node.style as { width?: number | string; height?: number | string } | undefined;
  const num = (v: unknown): number | undefined => (typeof v === 'number' ? v : undefined);
  return {
    width: num(node.width) ?? num(node.measured?.width) ?? num(style?.width) ?? LOOP_DEFAULT_WIDTH,
    height: num(node.height) ?? num(node.measured?.height) ?? num(style?.height) ?? LOOP_DEFAULT_HEIGHT,
  };
}

/**
 * Re-pin every loop entry/exit marker flush to its parent container's current
 * border. The markers are not user-draggable, so their position is derived from
 * the live loop size — call this after any node change (notably a NodeResizer
 * resize) so the exit tab tracks the right edge and both tabs stay vertically
 * centred. Marker nodes whose position is already correct are returned as-is so
 * referential equality is preserved where possible.
 */
export function repinLoopPorts(nodes: QuartetFlowNode[]): QuartetFlowNode[] {
  const loopSizes = new Map<string, { width: number; height: number }>();
  for (const n of nodes) {
    if (n.data?.kind === 'loop') loopSizes.set(n.id, loopNodeSize(n));
  }
  if (loopSizes.size === 0) return nodes;
  return nodes.map((n) => {
    if (!n.parentId || (n.data?.kind !== 'start' && n.data?.kind !== 'end')) return n;
    const size = loopSizes.get(n.parentId);
    if (!size) return n;
    const pos = loopPortPosition(size.width, size.height, n.data.kind === 'start');
    if (n.position.x === pos.x && n.position.y === pos.y) return n;
    return { ...n, position: pos };
  });
}

// Auto-layout fallback when a node has no persisted position.
const AUTO_X_STEP = 220;
const AUTO_Y = 140;

const EDGE_ARROW_COLOR = '#4c5663';
const YES_COLOR = '#2ea043';
const NO_COLOR = '#f85149';

// Data payload carried by every React Flow node in the canvas. We keep the
// original GraphNode so flowToConfig can preserve unedited fields (config,
// title, metadata) without re-deriving them from the visual representation.
export interface QuartetNodeData {
  kind: GraphNodeType;
  graphNode: GraphNode;
  /** Live run status injected during run replay / live highlight. */
  runStatus?: GraphInstanceStatus;
  /** True when a validation error points at this node. */
  hasError?: boolean;
  /** True when the canvas is editable (drives the loop resize handles). */
  editable?: boolean;
  [key: string]: unknown;
}

export type QuartetFlowNode = Node<QuartetNodeData>;

export interface QuartetEdgeData {
  port?: GraphEdgePort;
  /** Edge resolution state during run replay (pending/active/pruned). */
  edgeStatus?: 'pending' | 'active' | 'pruned';
  /** Derived visual run state (pending/flowing/done/pruned). */
  runDisplay?: EdgeRunDisplay;
  /** Whether the custom edge renders its × delete affordance (off in replay). */
  canDelete?: boolean;
  [key: string]: unknown;
}

export type QuartetFlowEdge = Edge<QuartetEdgeData>;

function rfNodeType(kind: GraphNodeType): string {
  return kind === 'loop' ? 'loopGroup' : 'quartet';
}

/**
 * React Flow constraint applied to a node nested inside a loop container.
 *
 * Business/control nodes (shell/prompt/clarify/if-else) get NO constraint:
 * they are plain children that can be dragged freely, including OUT of the loop
 * box. Drag-out is then detected by the canvas drag-stop handler, which
 * reparents the node to the top level. `extent: 'parent'` was previously used
 * to keep them visually contained, but it hard-clamped them inside the parent
 * bounds — making it impossible to ever drag a node back out of a loop.
 *
 * A NESTED LOOP container uses `expandParent: true` (still no extent): a loop
 * box is large and its own subgraph grows, so it must move/resize freely and
 * grow the enclosing loop to fit rather than being trapped by it.
 *
 * (Loop entry/exit markers are pinned separately with `draggable: false`, so
 * they never need an extent here.)
 *
 * Returned as a spreadable patch so callers can also clear the opposite field
 * (e.g. on reparent) with {@link clearNestConstraint}.
 */
export function nestConstraint(kind: GraphNodeType): { expandParent: true } | Record<string, never> {
  return kind === 'loop' ? { expandParent: true } : {};
}

/** Patch that removes both nesting constraints (used when a node leaves a loop). */
export const clearNestConstraint = { extent: undefined, expandParent: undefined } as const;

/**
 * Stable-sort nodes so every parent precedes its children. React Flow requires
 * this ordering (a child node referencing a `parentId` that appears later in
 * the array is dropped at render time), and the backend walks the scope tree
 * the same way. Generic over anything carrying `id` + optional `parentId`, so
 * it works for both QuartetFlowNode and persisted GraphNode. Order among nodes
 * at the same depth is preserved, keeping diffs stable across save/reopen.
 */
export function orderNodesByHierarchy<T extends { id: string; parentId?: string }>(nodes: T[]): T[] {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const depthCache = new Map<string, number>();
  const depthOf = (n: T): number => {
    let depth = 0;
    const seen = new Set<string>();
    let cur: T | undefined = n;
    while (cur?.parentId && !seen.has(cur.id)) {
      const cached = depthCache.get(cur.id);
      if (cached !== undefined) return depth + cached;
      seen.add(cur.id);
      cur = byId.get(cur.parentId);
      depth += 1;
    }
    return depth;
  };
  return nodes
    .map((n, index) => ({ n, index, depth: depthOf(n) }))
    .sort((a, b) => a.depth - b.depth || a.index - b.index)
    .map((e) => e.n);
}

/** Convert a persisted GraphConfig into React Flow nodes + edges. */
export function configToFlow(config: GraphConfig): {
  nodes: QuartetFlowNode[];
  edges: QuartetFlowEdge[];
} {
  // Loop container sizes, so entry/exit markers can be pinned flush to their
  // parent's border regardless of any stale persisted marker position.
  const loopSize = new Map<string, { width: number; height: number }>();
  for (const gn of config.nodes || []) {
    if (gn.type === 'loop') {
      loopSize.set(gn.id, {
        width: gn.layout?.width ?? LOOP_DEFAULT_WIDTH,
        height: gn.layout?.height ?? LOOP_DEFAULT_HEIGHT,
      });
    }
  }

  const nodes: QuartetFlowNode[] = (config.nodes || []).map((gn, index) => {
    const layout = gn.layout;
    const position = {
      x: typeof layout?.x === 'number' ? layout.x : index * AUTO_X_STEP + 40,
      y: typeof layout?.y === 'number' ? layout.y : AUTO_Y,
    };
    const node: QuartetFlowNode = {
      id: gn.id,
      type: rfNodeType(gn.type),
      position,
      data: { kind: gn.type, graphNode: gn },
    };
    if (gn.parentId) {
      node.parentId = gn.parentId;
      Object.assign(node, nestConstraint(gn.type));
    }
    if (gn.type === 'loop') {
      node.style = {
        width: layout?.width ?? LOOP_DEFAULT_WIDTH,
        height: layout?.height ?? LOOP_DEFAULT_HEIGHT,
      };
    }
    // A loop-scoped start/end is the loop's entry / exit border marker: a fixed
    // port tab pinned flush on its parent's left / right border, derived from the
    // container size and not user-draggable (it always tracks the boundary).
    if (isLoopPort(gn)) {
      const isEntry = gn.type === 'start';
      const parent = loopSize.get(gn.parentId!) ?? { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT };
      node.position = loopPortPosition(parent.width, parent.height, isEntry);
      node.style = { width: QG_LOOP_PORT_W, height: QG_LOOP_PORT_H };
      node.draggable = false;
    }
    return node;
  });

  const edges: QuartetFlowEdge[] = (config.edges || []).map((ge) => {
    const port = ge.sourcePort && ge.sourcePort !== 'default' ? ge.sourcePort : undefined;
    const edge: QuartetFlowEdge = {
      id: ge.id,
      source: ge.sourceNodeId,
      target: ge.targetNodeId,
      data: { port },
      markerEnd: { type: MarkerType.ArrowClosed, color: EDGE_ARROW_COLOR },
    };
    if (port) {
      edge.sourceHandle = port;
      edge.label = port === 'yes' ? 'YES' : 'NO';
      edge.labelStyle = { fill: port === 'yes' ? YES_COLOR : NO_COLOR, fontWeight: 700 };
    }
    return edge;
  });

  return { nodes: orderNodesByHierarchy(nodes), edges };
}

function portFromHandle(handle: string | null | undefined): GraphEdgePort | undefined {
  if (handle === 'yes' || handle === 'no') return handle;
  return undefined;
}

/**
 * Rebuild a GraphConfig from the current canvas state, preserving everything
 * that the canvas does not own: per-node config/title/metadata, top-level
 * variables/disabledVars/runConfig/workspace fields, and edge metadata. Only
 * layout, parentId and edge source ports come from the visual state.
 *
 * Node and edge ordering follows prevConfig first (stable diffs across
 * save/reopen), with any brand-new elements appended in canvas order.
 */
export function flowToConfig(
  rfNodes: QuartetFlowNode[],
  rfEdges: QuartetFlowEdge[],
  prevConfig: GraphConfig,
): GraphConfig {
  const prevNodeIndex = new Map<string, number>();
  (prevConfig.nodes || []).forEach((n, i) => prevNodeIndex.set(n.id, i));
  const prevEdgeIndex = new Map<string, number>();
  (prevConfig.edges || []).forEach((e, i) => prevEdgeIndex.set(e.id, i));

  const nodes: GraphNode[] = rfNodes.map((rn) => {
    const base = rn.data.graphNode;
    const next: GraphNode = {
      ...base,
      id: rn.id,
      type: rn.data.kind,
    };
    const layout = { ...(base.layout || {}), x: rn.position.x, y: rn.position.y };
    if (rn.data.kind === 'loop') {
      // NodeResizer writes resized dimensions to node.width/height (via
      // setAttributes) and node.measured; the initial size lives in style.
      // Prefer the live measured/explicit size so a resize survives save+reload,
      // falling back to style then the persisted layout then the default.
      const style = rn.style as { width?: number | string; height?: number | string } | undefined;
      const num = (v: unknown): number | undefined => (typeof v === 'number' ? v : undefined);
      const w = num(rn.width) ?? num(rn.measured?.width) ?? num(style?.width) ?? base.layout?.width ?? LOOP_DEFAULT_WIDTH;
      const h = num(rn.height) ?? num(rn.measured?.height) ?? num(style?.height) ?? base.layout?.height ?? LOOP_DEFAULT_HEIGHT;
      layout.width = w;
      layout.height = h;
    }
    next.layout = layout;
    if (rn.parentId) {
      next.parentId = rn.parentId;
    } else {
      delete next.parentId;
    }
    return next;
  });

  const edges: GraphEdge[] = rfEdges.map((re) => {
    const prev = prevConfig.edges?.[prevEdgeIndex.get(re.id) ?? -1];
    const port = portFromHandle(re.sourceHandle) || re.data?.port;
    const next: GraphEdge = {
      ...(prev || {}),
      id: re.id,
      sourceNodeId: re.source,
      targetNodeId: re.target,
    };
    if (port) {
      next.sourcePort = port;
    } else {
      delete next.sourcePort;
    }
    return next;
  });

  const nodeSort = (a: GraphNode, b: GraphNode) =>
    (prevNodeIndex.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (prevNodeIndex.get(b.id) ?? Number.MAX_SAFE_INTEGER);
  const edgeSort = (a: GraphEdge, b: GraphEdge) =>
    (prevEdgeIndex.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (prevEdgeIndex.get(b.id) ?? Number.MAX_SAFE_INTEGER);

  return {
    ...prevConfig,
    // Stable diff order first (prevConfig order), then re-order so parents
    // precede children — required for a clean reload and matches the backend.
    nodes: orderNodesByHierarchy([...nodes].sort(nodeSort)),
    edges: [...edges].sort(edgeSort),
  };
}

const NODE_STATUS_PRIORITY: GraphInstanceStatus[] = ['failed', 'running', 'interrupted', 'pending', 'skipped', 'succeeded'];
const NODE_STATUS_RANK = new Map<GraphInstanceStatus, number>(NODE_STATUS_PRIORITY.map((status, index) => [status, index]));

export function runStatusByNode(
  instances: GraphInstanceState[] | undefined,
): Record<string, GraphInstanceStatus> {
  const out: Record<string, GraphInstanceStatus> = {};
  for (const inst of instances || []) {
    const prev = out[inst.nodeId];
    if (!prev || (NODE_STATUS_RANK.get(inst.status) ?? 999) < (NODE_STATUS_RANK.get(prev) ?? 999)) {
      out[inst.nodeId] = inst.status;
    }
  }
  return out;
}

/** Build a lookup of edgeId -> raw resolution status from edge run states. */
export function edgeStatusByEdge(
  edges: GraphEdgeState[] | undefined,
): Record<string, 'pending' | 'active' | 'pruned'> {
  const out: Record<string, 'pending' | 'active' | 'pruned'> = {};
  const sawPruned = new Set<string>();
  for (const edge of edges || []) {
    const prev = out[edge.edgeId];
    if (!prev) {
      out[edge.edgeId] = edge.status;
      if (edge.status === 'pruned') sawPruned.add(edge.edgeId);
      continue;
    }
    if (edge.status === 'active') {
      out[edge.edgeId] = 'active';
      continue;
    }
    if (edge.status === 'pruned') {
      sawPruned.add(edge.edgeId);
      if (prev === 'pending') out[edge.edgeId] = 'pruned';
      continue;
    }
    if (prev === 'pending' && !sawPruned.has(edge.edgeId)) out[edge.edgeId] = 'pending';
  }
  return out;
}

// Visual run state for an edge, used to clearly separate "already ran",
// "running" (the live frontier) and "not run yet" from a "pruned" branch.
export type EdgeRunDisplay = 'pending' | 'flowing' | 'done' | 'pruned';

const NODE_FINISHED = new Set<GraphInstanceStatus>(['succeeded', 'failed', 'skipped', 'interrupted']);

/**
 * Resolve how an edge should be drawn during a run by combining its own
 * resolution state with the run status of the node it points at:
 *   - pruned  -> branch was not taken
 *   - done    -> edge was taken and its downstream node already finished
 *   - flowing -> edge was taken and its downstream node is still running / queued
 *   - pending -> edge has not been resolved yet (not run)
 */
export function edgeRunDisplay(
  rawStatus: 'pending' | 'active' | 'pruned' | undefined,
  targetStatus: GraphInstanceStatus | undefined,
): EdgeRunDisplay {
  if (rawStatus === 'pruned') return 'pruned';
  if (rawStatus === 'active') {
    if (targetStatus && NODE_FINISHED.has(targetStatus)) return 'done';
    return 'flowing';
  }
  return 'pending';
}

// Resolve the config a GraphRun executed against, preferring its current
// version snapshot so historical runs replay against their own layout.
export function runConfigSnapshot(run: GraphRun): GraphConfig {
  const versions = run.versions || [];
  const match = versions.find((v) => v.version === run.currentVersion);
  return match?.config || run.baseSnapshot?.config || { nodes: [], edges: [] };
}
