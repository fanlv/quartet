import type { QuartetFlowEdge, QuartetFlowNode } from './graphFlowAdapter';
import { loopNodeSize } from './graphFlowAdapter';

// Layout geometry. The canvas flows left -> right (start on the left, end on the
// right), so nodes are placed into columns ("layers") and stacked vertically
// inside each column.
const NODE_W = 200;
const NODE_H = 92;
const H_GAP = 90; // horizontal gap between columns
const V_GAP = 40; // vertical gap between nodes in the same column
// Padding inside a loop container so arranged children clear its header / border.
const LOOP_PAD_X = 28;
const LOOP_PAD_TOP = 48;

interface Box {
  w: number;
  h: number;
}

// Visual footprint of a node, used to space columns/rows. Loop containers use
// their current size so siblings don't overlap the box.
function boxOf(node: QuartetFlowNode): Box {
  if (node.data?.kind === 'loop') {
    // Same precedence as the canvas/persist paths: a NodeResizer resize lives
    // on node.width/height (then measured), not on style.
    const { width, height } = loopNodeSize(node);
    return { w: width, h: height };
  }
  return { w: NODE_W, h: NODE_H };
}

/**
 * Assign each node a layer (column index) via longest-path layering over the
 * subgraph. Roots (no incoming edge within the group) start at layer 0; every
 * other node sits one column to the right of its deepest predecessor. Cycles are
 * broken defensively so a back-edge never causes infinite recursion.
 */
function assignLayers(ids: string[], edges: Array<{ source: string; target: string }>): Map<string, number> {
  const idSet = new Set(ids);
  const incoming = new Map<string, string[]>();
  const outgoing = new Map<string, string[]>();
  for (const id of ids) {
    incoming.set(id, []);
    outgoing.set(id, []);
  }
  for (const e of edges) {
    if (!idSet.has(e.source) || !idSet.has(e.target) || e.source === e.target) continue;
    outgoing.get(e.source)!.push(e.target);
    incoming.get(e.target)!.push(e.source);
  }

  const layer = new Map<string, number>();
  const visiting = new Set<string>();
  const depth = (id: string): number => {
    const cached = layer.get(id);
    if (cached !== undefined) return cached;
    if (visiting.has(id)) return 0; // cycle guard
    visiting.add(id);
    let best = 0;
    for (const pred of incoming.get(id)!) {
      best = Math.max(best, depth(pred) + 1);
    }
    visiting.delete(id);
    layer.set(id, best);
    return best;
  };
  for (const id of ids) depth(id);
  return layer;
}

/**
 * Lay out one group of sibling nodes (all sharing the same parent, or all
 * top-level) into columns. Returns the chosen top-left position per node in the
 * same coordinate space the input positions use (absolute for top-level,
 * parent-relative for loop children). `originX`/`originY` shift the whole block.
 */
function layoutGroup(
  nodes: QuartetFlowNode[],
  edges: Array<{ source: string; target: string }>,
  originX: number,
  originY: number,
): Map<string, { x: number; y: number }> {
  const out = new Map<string, { x: number; y: number }>();
  if (nodes.length === 0) return out;

  const ids = nodes.map((n) => n.id);
  const layers = assignLayers(ids, edges);
  const boxes = new Map(nodes.map((n) => [n.id, boxOf(n)]));

  // Bucket nodes by column, preserving their current array order for stable rows.
  const columns = new Map<number, QuartetFlowNode[]>();
  let maxLayer = 0;
  for (const n of nodes) {
    const l = layers.get(n.id) ?? 0;
    maxLayer = Math.max(maxLayer, l);
    (columns.get(l) ?? columns.set(l, []).get(l)!).push(n);
  }

  // Width of each column = widest node in it; x is the running sum + gaps.
  const colWidth = new Map<number, number>();
  const colHeight = new Map<number, number>();
  for (const [l, ns] of columns) {
    let w = 0;
    let h = 0;
    for (const n of ns) {
      const b = boxes.get(n.id)!;
      w = Math.max(w, b.w);
      h += b.h;
    }
    h += V_GAP * Math.max(0, ns.length - 1);
    colWidth.set(l, w);
    colHeight.set(l, h);
  }

  // Tallest column drives vertical centering so the block looks balanced.
  const blockHeight = Math.max(0, ...Array.from(colHeight.values()));

  let x = originX;
  for (let l = 0; l <= maxLayer; l += 1) {
    const ns = columns.get(l);
    const w = colWidth.get(l) ?? NODE_W;
    if (ns && ns.length) {
      let y = originY + (blockHeight - (colHeight.get(l) ?? 0)) / 2;
      for (const n of ns) {
        const b = boxes.get(n.id)!;
        // Center each node within its column width.
        out.set(n.id, { x: x + (w - b.w) / 2, y });
        y += b.h + V_GAP;
      }
    }
    x += w + H_GAP;
  }
  return out;
}

/**
 * Compute auto-arranged positions for every node on the canvas. Top-level nodes
 * are laid out in absolute coordinates; each loop container's children are laid
 * out independently in coordinates relative to that loop (the React Flow child
 * coordinate space). Returns a map of nodeId -> new position; nodes absent from
 * the map keep their current position.
 */
export function computeAutoLayout(
  nodes: QuartetFlowNode[],
  edges: QuartetFlowEdge[],
): Map<string, { x: number; y: number }> {
  const plainEdges = edges.map((e) => ({ source: e.source, target: e.target }));

  // Partition nodes by their parent (undefined = top-level).
  const groups = new Map<string | undefined, QuartetFlowNode[]>();
  for (const n of nodes) {
    const key = n.parentId ?? undefined;
    (groups.get(key) ?? groups.set(key, []).get(key)!).push(n);
  }

  const result = new Map<string, { x: number; y: number }>();
  for (const [parentId, groupNodes] of groups) {
    if (parentId === undefined) {
      // Top-level: lay out from the canvas origin with a small margin.
      for (const [id, pos] of layoutGroup(groupNodes, plainEdges, 40, 40)) {
        result.set(id, { x: Math.round(pos.x), y: Math.round(pos.y) });
      }
    } else {
      // Loop children: arrange relative to the parent, inset below its header.
      for (const [id, pos] of layoutGroup(groupNodes, plainEdges, LOOP_PAD_X, LOOP_PAD_TOP)) {
        result.set(id, { x: Math.round(pos.x), y: Math.round(pos.y) });
      }
    }
  }
  return result;
}
