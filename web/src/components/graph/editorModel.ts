import type { NodeChange } from '@xyflow/react';
import type { GraphNode, GraphNodeType } from '../../types/graph';
import {
  LOOP_DEFAULT_HEIGHT,
  LOOP_DEFAULT_WIDTH,
  QG_LOOP_PORT_H,
  QG_LOOP_PORT_W,
  loopPortPosition,
  nestConstraint,
  orderNodesByHierarchy,
  type QuartetFlowNode,
} from './graphFlowAdapter';

export function withDescendants(ids: Set<string>, nodes: QuartetFlowNode[]): Set<string> {
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

function mainControlCounts(nodes: QuartetFlowNode[]): Record<'start' | 'end', number> {
  return nodes.reduce(
    (counts, n) => {
      if (!n.parentId && (n.data.kind === 'start' || n.data.kind === 'end')) counts[n.data.kind] += 1;
      return counts;
    },
    { start: 0, end: 0 },
  );
}

export function isNodeDeletable(node: QuartetFlowNode, nodes: QuartetFlowNode[]): boolean {
  if (node.parentId && (node.data.kind === 'start' || node.data.kind === 'end')) return false;
  if (!node.parentId && (node.data.kind === 'start' || node.data.kind === 'end')) {
    return mainControlCounts(nodes)[node.data.kind] > 1;
  }
  return true;
}

export function markEditableNodes(nodes: QuartetFlowNode[]): QuartetFlowNode[] {
  return nodes.map((n) => ({ ...n, deletable: isNodeDeletable(n, nodes) }));
}

export function deletableIdsForBatch(ids: Set<string>, nodes: QuartetFlowNode[]): Set<string> {
  const expanded = withDescendants(ids, nodes);
  const counts = mainControlCounts(nodes);
  const controlsToDelete = { start: 0, end: 0 };
  for (const n of nodes) {
    if (expanded.has(n.id) && !n.parentId && (n.data.kind === 'start' || n.data.kind === 'end')) {
      controlsToDelete[n.data.kind] += 1;
    }
  }
  const out = new Set<string>();
  for (const n of nodes) {
    if (!expanded.has(n.id)) continue;
    if (!n.parentId && (n.data.kind === 'start' || n.data.kind === 'end') && counts[n.data.kind] - controlsToDelete[n.data.kind] < 1) {
      continue;
    }
    out.add(n.id);
  }
  return out;
}

export function filterNodeRemoveChanges(changes: NodeChange[], nodes: QuartetFlowNode[]): { changes: NodeChange[]; removedIds: Set<string> } {
  const requested = new Set(changes.filter((ch) => ch.type === 'remove').map((ch) => ch.id));
  if (requested.size === 0) return { changes, removedIds: requested };

  const allowed = deletableIdsForBatch(requested, nodes);
  const next: NodeChange[] = changes.filter((ch) => ch.type !== 'remove' || allowed.has(ch.id));
  for (const id of allowed) {
    if (!requested.has(id)) next.push({ type: 'remove', id });
  }
  return { changes: next, removedIds: allowed };
}

export function createEditorNode(
  type: GraphNodeType,
  position: { x: number; y: number },
  parentId: string | null | undefined,
  mintId: (prefix: string, taken: Set<string>) => string,
  takenIds: Set<string>,
): QuartetFlowNode[] {
  const id = mintId(type, takenIds);
  const intoParent = parentId && type !== 'loop' ? parentId : undefined;
  const graphNode: GraphNode = {
    id,
    type,
    title: '',
    ...(intoParent ? { parentId: intoParent } : {}),
    config: type === 'loop' ? { loopMode: 'fixed', fixedCount: 1 } : type === 'prompt' || type === 'clarify' ? { sessionStrategy: 'new' } : {},
    layout: {
      x: Math.round(position.x),
      y: Math.round(position.y),
      ...(type === 'loop' ? { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT } : {}),
    },
  };
  const node: QuartetFlowNode = {
    id,
    type: type === 'loop' ? 'loopGroup' : 'quartet',
    position,
    ...(intoParent ? { parentId: intoParent, ...nestConstraint(type) } : {}),
    data: { kind: type, graphNode },
    ...(type === 'loop' ? { style: { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT } } : {}),
  };
  const created: QuartetFlowNode[] = [node];
  if (type === 'loop') {
    const entryPos = loopPortPosition(LOOP_DEFAULT_WIDTH, LOOP_DEFAULT_HEIGHT, true);
    const exitPos = loopPortPosition(LOOP_DEFAULT_WIDTH, LOOP_DEFAULT_HEIGHT, false);
    const startId = mintId('start', takenIds);
    const endId = mintId('end', takenIds);
    created.push({
      id: startId,
      type: 'quartet',
      position: entryPos,
      parentId: id,
      extent: 'parent',
      style: { width: QG_LOOP_PORT_W, height: QG_LOOP_PORT_H },
      draggable: false,
      data: { kind: 'start', graphNode: { id: startId, type: 'start', title: '', parentId: id, layout: { x: entryPos.x, y: entryPos.y } } },
      deletable: false,
    } as QuartetFlowNode);
    created.push({
      id: endId,
      type: 'quartet',
      position: exitPos,
      parentId: id,
      extent: 'parent',
      style: { width: QG_LOOP_PORT_W, height: QG_LOOP_PORT_H },
      draggable: false,
      data: { kind: 'end', graphNode: { id: endId, type: 'end', title: '', parentId: id, layout: { x: exitPos.x, y: exitPos.y } } },
      deletable: false,
    } as QuartetFlowNode);
  }
  return orderNodesByHierarchy(created);
}
