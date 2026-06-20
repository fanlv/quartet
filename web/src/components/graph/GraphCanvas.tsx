import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Connection,
  type EdgeChange,
  type NodeChange,
  type Viewport,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { GraphInstanceStatus, GraphNodeType } from '../../types/graph';
import type { QuartetFlowEdge, QuartetFlowNode } from './graphFlowAdapter';
import { edgeRunDisplay } from './graphFlowAdapter';
import { QuartetNode } from './nodes/QuartetNode';
import { LoopGroupNode } from './nodes/LoopGroupNode';
import { DeletableEdge } from './edges/DeletableEdge';
import { KINDS, PALETTE_ORDER, labelOf, subOf } from './nodes/kinds';
import './nodes/nodes.css';
import './GraphCanvas.css';

const nodeTypes = { quartet: QuartetNode, loopGroup: LoopGroupNode };
const edgeTypes = { deletable: DeletableEdge };

// Edge styling per run-display state. Colors are tuned for the dark canvas:
//   done    -> solid blue, edge already traversed
//   flowing -> solid green + animated dashes, the live running frontier
//   pruned  -> dim grey dashed, branch not taken
//   pending -> muted grey, not run yet
// Outside a run the edges keep their default look (no run status map present).
const DONE_EDGE_STYLE = { stroke: '#4493f8', strokeWidth: 2.5, opacity: 1 };
const FLOWING_EDGE_STYLE = { stroke: '#2ea043', strokeWidth: 3, opacity: 1 };
const PRUNED_EDGE_STYLE = { stroke: '#4c5663', strokeDasharray: '4 4', strokeWidth: 1.5, opacity: 0.45 };
// Not run yet: muted neutral so it clearly recedes behind done/flowing edges.
const PENDING_EDGE_STYLE = { stroke: '#8b949e', strokeWidth: 1.5, opacity: 0.8 };
const DEFAULT_FIT_VIEW_OPTIONS = { padding: 0.38, maxZoom: 0.85 };
type ConnectPort = 'default' | 'yes' | 'no';

// Keep the arrowhead color in sync with the edge color so a "done"/"flowing"
// edge does not end in the neutral default arrow.
function withMarkerColor(marker: QuartetFlowEdge['markerEnd'], color: string): QuartetFlowEdge['markerEnd'] {
  if (marker && typeof marker === 'object') return { ...marker, color };
  return marker;
}

export interface GraphCanvasFocus {
  nodeId?: string;
  token: number;
}

interface GraphCanvasProps {
  nodes: QuartetFlowNode[];
  edges: QuartetFlowEdge[];
  readOnly?: boolean;
  // When read-only, still allow nodes to be dragged around (view-only reposition,
  // no structural edits). onNodesChange receives the position changes so the
  // caller can keep its node state in sync.
  allowNodeDrag?: boolean;
  showMiniMap?: boolean;
  runStatusByNodeId?: Record<string, GraphInstanceStatus>;
  edgeStatusById?: Record<string, 'pending' | 'active' | 'pruned'>;
  errorNodeIds?: Set<string>;
  errorEdgeIds?: Set<string>;
  focus?: GraphCanvasFocus;
  initialViewport?: Viewport;
  viewportResetKey?: number;
  onNodesChange: (changes: NodeChange[]) => void;
  onEdgesChange: (changes: EdgeChange[]) => void;
  onConnect: (connection: Connection) => void;
  onNodeClick: (id: string) => void;
  onPaneClick: () => void;
  // position is in flow coordinates. parentId is the loop container the node was
  // dropped into (so it becomes a loop child), or null when dropped on open canvas.
  onAddNode: (type: GraphNodeType, position: { x: number; y: number }, parentId?: string | null) => void;
  // Called when a node is dragged into / out of a loop container. newParentId is
  // the loop container's id, or null when the node was dropped outside any loop.
  onReparent?: (nodeId: string, newParentId: string | null) => void;
  onViewportChange?: (viewport: Viewport) => void;
}

function CanvasInner({
  nodes,
  edges,
  readOnly,
  allowNodeDrag,
  showMiniMap = true,
  runStatusByNodeId,
  edgeStatusById,
  errorNodeIds,
  errorEdgeIds,
  focus,
  initialViewport,
  viewportResetKey = 0,
  onNodesChange,
  onEdgesChange,
  onConnect,
  onNodeClick,
  onPaneClick,
  onAddNode,
  onReparent,
  onViewportChange,
}: GraphCanvasProps) {
  const { t } = useTranslation();
  const rf = useReactFlow();
  const wrapRef = useRef<HTMLDivElement>(null);
  const lastFocusToken = useRef<number>(-1);
  const initialViewportRef = useRef(initialViewport);
  const [tapConnect, setTapConnect] = useState(false);
  const [connectSource, setConnectSource] = useState<{ id: string; kind: GraphNodeType } | null>(null);
  const [connectPort, setConnectPort] = useState<ConnectPort>('default');

  useEffect(() => {
    initialViewportRef.current = initialViewport;
  }, [initialViewport]);

  useEffect(() => {
    const viewport = initialViewportRef.current;
    if (viewport) {
      void rf.setViewport(viewport, { duration: 0 });
      return;
    }
    if (nodes.length > 0) {
      void rf.fitView({ ...DEFAULT_FIT_VIEW_OPTIONS, duration: 0 });
    }
  }, [nodes.length, rf, viewportResetKey]);

  // Overlay run status + validation error flags onto node/edge visuals without
  // mutating the structural state owned by the page.
  const displayNodes = useMemo(
    () =>
      nodes.map((n) => ({
        ...n,
        className: [
          n.className,
          tapConnect ? 'qg-connect-candidate' : '',
          connectSource?.id === n.id ? 'qg-connect-source' : '',
        ].filter(Boolean).join(' '),
        data: {
          ...n.data,
          runStatus: runStatusByNodeId?.[n.id],
          hasError: errorNodeIds?.has(n.id) ?? false,
        },
      })),
    [connectSource, nodes, runStatusByNodeId, errorNodeIds, tapConnect],
  );

  const displayEdges = useMemo(
    () =>
      edges.map((e) => {
        const hasError = errorEdgeIds?.has(e.id) ?? false;
        // Run styling only applies when we are visualizing a run (edge status map
        // present); in the plain editor edges keep their default look.
        const inRun = !!edgeStatusById;
        const display = inRun
          ? edgeRunDisplay(edgeStatusById?.[e.id], runStatusByNodeId?.[e.target])
          : undefined;
        let style = e.style;
        let markerEnd = e.markerEnd;
        if (display === 'pruned') {
          style = { ...style, ...PRUNED_EDGE_STYLE };
          markerEnd = withMarkerColor(markerEnd, '#4c5663');
        } else if (display === 'flowing') {
          style = { ...style, ...FLOWING_EDGE_STYLE };
          markerEnd = withMarkerColor(markerEnd, '#2ea043');
        } else if (display === 'done') {
          style = { ...style, ...DONE_EDGE_STYLE };
          markerEnd = withMarkerColor(markerEnd, '#4493f8');
        } else if (display === 'pending') {
          style = { ...style, ...PENDING_EDGE_STYLE };
          markerEnd = withMarkerColor(markerEnd, '#8b949e');
        }
        if (hasError) {
          style = { ...style, stroke: '#f85149', strokeWidth: 2, opacity: 1 };
          markerEnd = withMarkerColor(markerEnd, '#f85149');
        }
        // Route through the custom edge so the midpoint × delete button (and the
        // YES/NO pill) render as tap targets. The native `label` is dropped to
        // avoid a duplicate branch label. canDelete is off in read-only replay.
        const { label: _label, labelStyle: _labelStyle, ...rest } = e;
        return {
          ...rest,
          type: 'deletable',
          style,
          markerEnd,
          // Only the live running frontier gets the marching-ants animation so
          // "running" reads differently from an already-completed edge.
          animated: display === 'flowing',
          data: { ...e.data, canDelete: !readOnly, runDisplay: display },
        };
      }),
    [edges, edgeStatusById, errorEdgeIds, readOnly, runStatusByNodeId],
  );

  // Center the canvas on an element when the user clicks a validation error.
  useEffect(() => {
    if (!focus || focus.token === lastFocusToken.current) return;
    lastFocusToken.current = focus.token;
    if (focus.nodeId) {
      const node = nodes.find((n) => n.id === focus.nodeId);
      if (node) {
        void rf.fitView({ nodes: [{ id: node.id }], duration: 300, maxZoom: 1.2, padding: 0.4 });
      }
    }
  }, [focus, nodes, rf]);

  // Absolute (flow-space) top-left of a node, walking up the parent chain since
  // React Flow stores child positions relative to their parent.
  const absoluteOrigin = useCallback(
    (node: QuartetFlowNode): { x: number; y: number } => {
      let x = node.position.x;
      let y = node.position.y;
      let pid = node.parentId;
      const seen = new Set<string>();
      while (pid && !seen.has(pid)) {
        seen.add(pid);
        const parent = nodes.find((n) => n.id === pid);
        if (!parent) break;
        x += parent.position.x;
        y += parent.position.y;
        pid = parent.parentId;
      }
      return { x, y };
    },
    [nodes],
  );

  // Find the loop container whose box contains a flow-space point. When loops
  // overlap (nested), the smallest (most deeply nested) one wins so dropping into
  // a nested loop works. Returns null when the point is on the open canvas.
  const loopContainerAt = useCallback(
    (point: { x: number; y: number }): QuartetFlowNode | null => {
      let target: QuartetFlowNode | null = null;
      let targetArea = Infinity;
      for (const n of nodes) {
        if (n.data?.kind !== 'loop') continue;
        const origin = absoluteOrigin(n);
        const w = typeof n.width === 'number' ? n.width : Number(n.style?.width) || 0;
        const h = typeof n.height === 'number' ? n.height : Number(n.style?.height) || 0;
        if (!w || !h) continue;
        if (point.x < origin.x || point.x > origin.x + w || point.y < origin.y || point.y > origin.y + h) {
          continue;
        }
        const area = w * h;
        if (area < targetArea) {
          target = n;
          targetArea = area;
        }
      }
      return target;
    },
    [absoluteOrigin, nodes],
  );

  const handleDrop = useCallback(
    (event: React.DragEvent) => {
      event.preventDefault();
      if (readOnly) return;
      const type = event.dataTransfer.getData('application/quartet-node') as GraphNodeType;
      if (!type) return;
      const position = rf.screenToFlowPosition({ x: event.clientX, y: event.clientY });
      // Dropping a non-loop node inside a loop container makes it a child of that
      // loop. Loops are never nested by drop here (kept top-level); they can be
      // reparented later by dragging if needed.
      const container = type === 'loop' ? null : loopContainerAt(position);
      if (container) {
        const origin = absoluteOrigin(container);
        onAddNode(type, { x: position.x - origin.x, y: position.y - origin.y }, container.id);
        return;
      }
      onAddNode(type, position, null);
    },
    [absoluteOrigin, loopContainerAt, onAddNode, rf, readOnly],
  );

  const handleDragOver = useCallback((event: React.DragEvent) => {
    event.preventDefault();
    event.dataTransfer.dropEffect = 'move';
  }, []);

  // After a node is dragged, decide whether it landed inside a loop container
  // and notify the page so it can (re)assign parentId. start/end/loop nodes are
  // never reparented by dragging — only business nodes flow into loops. A loop
  // cannot become its own ancestor, so candidate containers exclude the dragged
  // node itself and any node already inside it.
  const handleNodeDragStop = useCallback(
    (_event: unknown, node: QuartetFlowNode) => {
      if (readOnly || !onReparent) return;
      const dragged = nodes.find((n) => n.id === node.id);
      if (!dragged || dragged.data.kind === 'start' || dragged.data.kind === 'end') return;

      // Collect the dragged node's descendant ids so a loop can't be dropped
      // into its own subtree (would create a parent cycle).
      const descendants = new Set<string>([node.id]);
      let grew = true;
      while (grew) {
        grew = false;
        for (const n of nodes) {
          if (n.parentId && descendants.has(n.parentId) && !descendants.has(n.id)) {
            descendants.add(n.id);
            grew = true;
          }
        }
      }

      // Find loop containers overlapping the dragged node; pick the smallest
      // (most deeply nested) valid one so dropping into a nested loop works.
      const overlaps = rf.getIntersectingNodes(node).filter(
        (n) => (n as QuartetFlowNode).data?.kind === 'loop' && !descendants.has(n.id),
      ) as QuartetFlowNode[];
      let target: QuartetFlowNode | null = null;
      let targetArea = Infinity;
      for (const cand of overlaps) {
        const w = typeof cand.width === 'number' ? cand.width : Number(cand.style?.width) || 0;
        const h = typeof cand.height === 'number' ? cand.height : Number(cand.style?.height) || 0;
        const area = w * h || Infinity;
        if (area < targetArea) {
          target = cand;
          targetArea = area;
        }
      }

      const newParentId = target ? target.id : null;
      const currentParentId = dragged.parentId ?? null;
      if (newParentId !== currentParentId) {
        onReparent(node.id, newParentId);
      }
    },
    [nodes, onReparent, readOnly, rf],
  );

  // Click-to-add (mobile-friendly): drop the node at the current viewport center.
  // If a loop container covers that center, the node is added inside it.
  const handlePaletteClick = useCallback(
    (type: GraphNodeType) => {
      if (readOnly) return;
      const rect = wrapRef.current?.getBoundingClientRect();
      const center = rect
        ? rf.screenToFlowPosition({ x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 })
        : { x: 200, y: 160 };
      const container = type === 'loop' ? null : loopContainerAt(center);
      if (container) {
        const origin = absoluteOrigin(container);
        onAddNode(type, { x: center.x - origin.x, y: center.y - origin.y }, container.id);
        return;
      }
      onAddNode(type, center, null);
    },
    [absoluteOrigin, loopContainerAt, onAddNode, rf, readOnly],
  );

  const toggleTapConnect = useCallback(() => {
    if (readOnly) return;
    setTapConnect((active) => {
      if (active) {
        setConnectSource(null);
      }
      return !active;
    });
  }, [readOnly]);

  const finishTapConnect = useCallback(
    (targetId: string) => {
      if (!connectSource) {
        const sourceNode = nodes.find((n) => n.id === targetId);
        if (!sourceNode) return;
        setConnectSource({ id: targetId, kind: sourceNode.data.kind });
        setConnectPort(sourceNode.data.kind === 'ifElse' ? 'yes' : 'default');
        onNodeClick(targetId);
        return;
      }
      if (connectSource.id === targetId) {
        setConnectSource(null);
        onNodeClick(targetId);
        return;
      }
      const sourceHandle = connectSource.kind === 'ifElse' && connectPort !== 'default' ? connectPort : null;
      onConnect({
        source: connectSource.id,
        target: targetId,
        sourceHandle,
        targetHandle: null,
      });
      setConnectSource(null);
      onNodeClick(targetId);
    },
    [connectPort, connectSource, nodes, onConnect, onNodeClick],
  );

  const handleNodeClick = useCallback(
    (id: string) => {
      if (tapConnect && !readOnly) {
        finishTapConnect(id);
        return;
      }
      onNodeClick(id);
    },
    [finishTapConnect, onNodeClick, readOnly, tapConnect],
  );

  const connectTargets = useMemo(
    () => (connectSource ? nodes.filter((n) => n.id !== connectSource.id) : []),
    [connectSource, nodes],
  );

  const handlePaneClick = useCallback(() => {
    if (tapConnect && connectSource) {
      setConnectSource(null);
      return;
    }
    onPaneClick();
  }, [connectSource, onPaneClick, tapConnect]);

  return (
    <div className="graph-canvas">
      {!readOnly && (
        <div className="graph-palette">
          <h3>{t('graph.canvas.nodes')}</h3>
          {PALETTE_ORDER.map((type) => {
            const k = KINDS[type];
            return (
              <button
                key={type}
                type="button"
                className="graph-node-chip"
                draggable
                onDragStart={(e) => {
                  e.dataTransfer.setData('application/quartet-node', type);
                  e.dataTransfer.effectAllowed = 'move';
                }}
                onClick={() => handlePaletteClick(type)}
              >
                <span className="graph-chip-ico" style={{ background: k.color }}>
                  {k.icon}
                </span>
                <span className="graph-chip-text">
                  <span className="graph-chip-label">{labelOf(t, type)}</span>
                  <span className="graph-chip-sub">{subOf(t, type)}</span>
                </span>
              </button>
            );
          })}
          <div className="graph-palette-hint">{t('graph.canvas.paletteHint')}</div>
        </div>
      )}

      <div className="graph-canvas-wrap" ref={wrapRef} onDrop={handleDrop} onDragOver={handleDragOver}>
        {!readOnly && (
          <div className={`graph-connect-bar ${tapConnect ? 'active' : ''}`}>
            <button type="button" className="graph-connect-toggle" onClick={toggleTapConnect}>
              {tapConnect ? t('graph.canvas.exitConnect') : t('graph.canvas.startConnect')}
            </button>
            {tapConnect && (
              <>
                <span className="graph-connect-state">
                  {connectSource ? t('graph.canvas.sourcePrefix', { id: connectSource.id }) : t('graph.canvas.connectHint')}
                </span>
                {connectSource?.kind === 'ifElse' && (
                  <span className="graph-connect-ports" role="group" aria-label={t('graph.canvas.ifElsePorts')}>
                    <button
                      type="button"
                      className={connectPort === 'yes' ? 'active' : ''}
                      onClick={() => setConnectPort('yes')}
                    >
                      YES
                    </button>
                    <button
                      type="button"
                      className={connectPort === 'no' ? 'active' : ''}
                      onClick={() => setConnectPort('no')}
                    >
                      NO
                    </button>
                  </span>
                )}
                {connectTargets.length > 0 && (
                  <span className="graph-connect-targets" role="group" aria-label={t('graph.canvas.connectTargets')}>
                    {connectTargets.map((node) => (
                      <button key={node.id} type="button" onClick={() => finishTapConnect(node.id)}>
                        {node.data.graphNode.title || node.id}
                      </button>
                    ))}
                  </span>
                )}
              </>
            )}
          </div>
        )}
        <ReactFlow
          nodes={displayNodes}
          edges={displayEdges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={readOnly && !allowNodeDrag ? undefined : onNodesChange}
          onEdgesChange={readOnly ? undefined : onEdgesChange}
          onConnect={readOnly ? undefined : onConnect}
          onNodeClick={(_, node) => handleNodeClick(node.id)}
          onNodeDragStop={readOnly ? undefined : handleNodeDragStop}
          onPaneClick={handlePaneClick}
          onMoveEnd={(_, viewport) => onViewportChange?.(viewport)}
          nodesDraggable={!readOnly || !!allowNodeDrag}
          nodesConnectable={!readOnly}
          connectionRadius={36}
          panOnScroll
          zoomOnPinch
          elementsSelectable
          minZoom={0.2}
          maxZoom={2}
          proOptions={{ hideAttribution: true }}
        >
          <Background gap={18} color="#2d3643" />
          <Controls showInteractive={false} />
          {showMiniMap && <MiniMap pannable zoomable className="graph-minimap" />}
        </ReactFlow>
      </div>
    </div>
  );
}

export function GraphCanvas(props: GraphCanvasProps) {
  return (
    <ReactFlowProvider>
      <CanvasInner {...props} />
    </ReactFlowProvider>
  );
}
