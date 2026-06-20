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
      node.extent = 'parent';
    }
    if (gn.type === 'loop') {
      node.style = {
        width: layout?.width ?? LOOP_DEFAULT_WIDTH,
        height: layout?.height ?? LOOP_DEFAULT_HEIGHT,
      };
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
      const style = rn.style as { width?: number | string; height?: number | string } | undefined;
      const w = typeof style?.width === 'number' ? style.width : base.layout?.width ?? LOOP_DEFAULT_WIDTH;
      const h = typeof style?.height === 'number' ? style.height : base.layout?.height ?? LOOP_DEFAULT_HEIGHT;
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

/**
 * Build a lookup of nodeId -> latest instance status, used to highlight nodes
 * during run replay. When multiple instances exist for one node (loops), the
 * most "advanced" status wins so the canvas reflects real progress.
 */
const STATUS_RANK: Record<GraphInstanceStatus, number> = {
  pending: 0,
  skipped: 1,
  interrupted: 2,
  running: 3,
  failed: 4,
  succeeded: 5,
};

export function runStatusByNode(
  instances: GraphInstanceState[] | undefined,
): Record<string, GraphInstanceStatus> {
  const out: Record<string, GraphInstanceStatus> = {};
  for (const inst of instances || []) {
    const prev = out[inst.nodeId];
    if (!prev || STATUS_RANK[inst.status] > STATUS_RANK[prev]) {
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
  for (const edge of edges || []) out[edge.edgeId] = edge.status;
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
