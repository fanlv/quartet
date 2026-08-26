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
  type IsValidConnection,
  type NodeChange,
  type Viewport,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { GraphInstanceStatus, GraphNodeType } from '../../types/graph';
import type { QuartetFlowEdge, QuartetFlowNode } from './graphFlowAdapter';
import { edgeRunDisplay } from './graphFlowAdapter';
import { computeAutoLayout } from './autoLayout';
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
const DONE_EDGE_STYLE = { stroke: '#16a34a', strokeWidth: 2.5, opacity: 1 };
const FLOWING_EDGE_STYLE = { stroke: '#42e978', strokeWidth: 3, opacity: 1 };
const PRUNED_EDGE_STYLE = { stroke: '#4c5663', strokeDasharray: '4 4', strokeWidth: 1.5, opacity: 0.45 };
// Not run yet: muted neutral so it clearly recedes behind done/flowing edges.
const PENDING_EDGE_STYLE = { stroke: '#a3b2a7', strokeWidth: 1.5, opacity: 0.8 };
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
  edgeId?: string;
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
  isMobile?: boolean;
  // Mobile tap-to-add target: when set (a loop node is selected, or the selected
  // node sits inside a loop), palette clicks add business nodes into this loop
  // container. Touch has no HTML5 drag & drop, so without this there is no way
  // to add a node into a loop on a phone. Desktop ignores it.
  addIntoLoopId?: string | null;
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
  // Copy/paste/duplicate the current selection. Wired only in the editable
  // workflow editor (omitted during run replay). Cmd/Ctrl+C / V / D on the
  // canvas call these; copy/duplicate also reachable from the inspector.
  onCopy?: () => void;
  onPaste?: () => void;
  onDuplicate?: () => void;
  // Undo / redo the last canvas edit (Cmd/Ctrl+Z, Cmd/Ctrl+Shift+Z or Ctrl+Y).
  // Wired only in the editable editor. onHistoryCommit is called right before a
  // canvas-internal mutation (e.g. auto-layout) so it lands as one undo step.
  // canUndo/canRedo drive the toolbar buttons' enabled state.
  onUndo?: () => void;
  onRedo?: () => void;
  onHistoryCommit?: () => void;
  isValidConnection?: IsValidConnection<QuartetFlowEdge>;
  getConnectionError?: (connection: Connection) => string | null;
  canUndo?: boolean;
  canRedo?: boolean;
}

function CanvasInner({
  nodes,
  edges,
  readOnly,
  allowNodeDrag,
  showMiniMap = true,
  isMobile = false,
  addIntoLoopId = null,
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
  onCopy,
  onPaste,
  onDuplicate,
  onUndo,
  onRedo,
  onHistoryCommit,
  isValidConnection,
  getConnectionError,
  canUndo,
  canRedo,
}: GraphCanvasProps) {
  const { t } = useTranslation();
  const rf = useReactFlow();
  const wrapRef = useRef<HTMLDivElement>(null);
  const lastFocusToken = useRef<number>(-1);
  const initialViewportRef = useRef(initialViewport);
  const [tapConnect, setTapConnect] = useState(false);
  const [connectSource, setConnectSource] = useState<{ id: string; kind: GraphNodeType } | null>(null);
  const [connectPort, setConnectPort] = useState<ConnectPort>('default');
  const [connectError, setConnectError] = useState('');

  useEffect(() => {
    initialViewportRef.current = initialViewport;
  }, [initialViewport]);

  // The initial camera (saved viewport, or a full-graph fit) is applied exactly
  // ONCE per mounted canvas, on the first run where it can be computed. This
  // effect also re-fires whenever nodes.length changes — re-fitting then would
  // reset the user's pan/zoom on every node add/delete (notably in run-version
  // editing, where initialViewport is intentionally undefined). Explicit
  // re-fits are unaffected: callers bump viewportResetKey, which is part of the
  // canvas key upstream and remounts this component with a fresh ref.
  const viewportInitializedRef = useRef(false);

  useEffect(() => {
    if (viewportInitializedRef.current) return;
    const viewport = initialViewportRef.current;
    // A zoom of 0 is not a real saved viewport — it is the Go zero value that
    // the backend serializes for workflows created without a canvas (e.g. via
    // the CLI). Restoring it would scale every node to nothing and blank the
    // canvas, so treat it like "no viewport" and fall back to fitView.
    // Mobile also skips the saved viewport: it was usually panned/zoomed on a
    // wide desktop screen and frames the wrong area on a phone — fit the whole
    // graph instead so the canvas never opens on empty space.
    if (viewport && viewport.zoom > 0 && !isMobile) {
      viewportInitializedRef.current = true;
      void rf.setViewport(viewport, { duration: 0 });
      return;
    }
    if (nodes.length > 0) {
      viewportInitializedRef.current = true;
      void rf.fitView({ ...DEFAULT_FIT_VIEW_OPTIONS, duration: 0 });
    }
  }, [isMobile, nodes.length, rf, viewportResetKey]);

  // Overlay run status + validation error flags onto node/edge visuals without
  // mutating the structural state owned by the page. The per-node object is
  // rebuilt on every render, but each custom node component is memoized (see
  // QuartetNode / LoopGroupNode) and bails out when its own visible inputs are
  // unchanged, so a live-run status reconcile only re-renders the nodes that
  // actually changed rather than all of them.
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
          editable: !readOnly,
        },
      })),
    [connectSource, nodes, runStatusByNodeId, errorNodeIds, tapConnect, readOnly],
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
          markerEnd = withMarkerColor(markerEnd, '#42e978');
        } else if (display === 'done') {
          style = { ...style, ...DONE_EDGE_STYLE };
          markerEnd = withMarkerColor(markerEnd, '#16a34a');
        } else if (display === 'pending') {
          style = { ...style, ...PENDING_EDGE_STYLE };
          markerEnd = withMarkerColor(markerEnd, '#a3b2a7');
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
    } else if (focus.edgeId) {
      const edge = edges.find((e) => e.id === focus.edgeId);
      const fitNodes = [edge?.source, edge?.target].filter(Boolean).map((id) => ({ id: id as string }));
      if (fitNodes.length > 0) {
        void rf.fitView({ nodes: fitNodes, duration: 300, maxZoom: 1.2, padding: 0.4 });
      }
    }
  }, [edges, focus, nodes, rf]);

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
      // Dropping a business node inside a loop container makes it a child of
      // that loop. Main-graph start/end controls stay top-level; loop-scoped
      // entry/exit markers are generated with the loop container.
      const container = type === 'loop' || type === 'start' || type === 'end' ? null : loopContainerAt(position);
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

  // Click-to-add: on desktop it always drops the node at the top level near the
  // viewport center. It deliberately does NOT auto-nest into a loop container
  // that happens to overlap the center — that made click-add land inside a loop
  // unexpectedly, and (with the old extent clamp) the node could not be dragged
  // back out. On mobile (no HTML5 drag & drop) the selected loop — via
  // addIntoLoopId — is the explicit tap-to-add target instead, so a loop can
  // still be populated from the palette on a phone.
  const handlePaletteClick = useCallback(
    (type: GraphNodeType) => {
      if (readOnly) return;
      if (isMobile && addIntoLoopId && type !== 'loop' && type !== 'start' && type !== 'end') {
        const container = nodes.find((n) => n.id === addIntoLoopId);
        if (container) {
          // Child positions are relative to the container; stack the new node
          // below the last child (or just under the container header when empty).
          const children = nodes.filter((n) => n.parentId === container.id);
          const bottom = children.reduce((max, n) => Math.max(max, n.position.y), 0);
          onAddNode(type, { x: 24, y: children.length > 0 ? bottom + 96 : 56 }, container.id);
          return;
        }
      }
      const rect = wrapRef.current?.getBoundingClientRect();
      const center = rect
        ? rf.screenToFlowPosition({ x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 })
        : { x: 200, y: 160 };
      onAddNode(type, center, null);
    },
    [addIntoLoopId, isMobile, nodes, onAddNode, rf, readOnly],
  );

  const toggleTapConnect = useCallback(() => {
    if (readOnly) return;
    setTapConnect((active) => {
      if (active) {
        setConnectSource(null);
        setConnectError('');
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
        setConnectError('');
        onNodeClick(targetId);
        return;
      }
      if (connectSource.id === targetId) {
        setConnectSource(null);
        setConnectError('');
        onNodeClick(targetId);
        return;
      }
      const sourceHandle = connectSource.kind === 'ifElse' && connectPort !== 'default' ? connectPort : null;
      const connection: Connection = {
        source: connectSource.id,
        target: targetId,
        sourceHandle,
        targetHandle: null,
      };
      const reason = getConnectionError?.(connection);
      if (reason) {
        setConnectError(reason);
        return;
      }
      onConnect(connection);
      setConnectSource(null);
      setConnectError('');
      onNodeClick(targetId);
    },
    [connectPort, connectSource, getConnectionError, nodes, onConnect, onNodeClick],
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
      setConnectError('');
      return;
    }
    onPaneClick();
  }, [connectSource, onPaneClick, tapConnect]);

  // Auto-arrange: recompute every node position into a tidy left-to-right layered
  // layout and push the moves through the normal position-change flow so the page
  // state (and persisted layout) stays in sync. Then fit the view to the result.
  const handleAutoLayout = useCallback(() => {
    if (readOnly) return;
    const positions = computeAutoLayout(nodes, edges);
    const changes: NodeChange[] = [];
    for (const n of nodes) {
      const pos = positions.get(n.id);
      if (!pos) continue;
      if (pos.x === n.position.x && pos.y === n.position.y) continue;
      changes.push({ id: n.id, type: 'position', position: pos, dragging: false });
    }
    if (changes.length === 0) return;
    // Auto-layout sends a batch of dragging:false moves, which onNodesChange
    // does not snapshot (no drag gesture). Commit explicitly so the whole
    // re-layout is a single undo step.
    onHistoryCommit?.();
    onNodesChange(changes);
    window.requestAnimationFrame(() => {
      void rf.fitView({ ...DEFAULT_FIT_VIEW_OPTIONS, duration: 300 });
    });
  }, [edges, nodes, onHistoryCommit, onNodesChange, readOnly, rf]);

  // Keyboard shortcuts for copy / paste / duplicate of the selection. Attached
  // at document level so the canvas need not hold DOM focus, but ignored while a
  // text field is focused (so Cmd/Ctrl+C/V/D there keep their normal meaning)
  // and in read-only replay. preventDefault stops the browser default (notably
  // Cmd/Ctrl+D = add bookmark).
  useEffect(() => {
    if (readOnly) return;
    const isEditableTarget = (el: EventTarget | null): boolean => {
      if (!(el instanceof HTMLElement)) return false;
      const tag = el.tagName;
      return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || el.isContentEditable;
    };
    // A non-collapsed text selection means the user is copying visible text
    // (e.g. a node label) — let the browser's native copy win in that case.
    const hasTextSelection = (): boolean => {
      const sel = window.getSelection();
      return !!sel && !sel.isCollapsed && sel.toString().trim().length > 0;
    };
    const handler = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
      if (isEditableTarget(e.target) || isEditableTarget(document.activeElement)) return;
      const key = e.key.toLowerCase();
      if (key === 'c' && onCopy) {
        if (hasTextSelection()) return;
        e.preventDefault();
        onCopy();
      } else if (key === 'v' && onPaste) {
        e.preventDefault();
        onPaste();
      } else if (key === 'd' && onDuplicate) {
        e.preventDefault();
        onDuplicate();
      } else if (key === 'z') {
        // Cmd/Ctrl+Z = undo, Cmd/Ctrl+Shift+Z = redo (mac convention).
        if (e.shiftKey) {
          if (!onRedo) return;
          e.preventDefault();
          onRedo();
        } else {
          if (!onUndo) return;
          e.preventDefault();
          onUndo();
        }
      } else if (key === 'y' && onRedo) {
        // Ctrl+Y = redo (Windows convention).
        e.preventDefault();
        onRedo();
      }
    };
    document.addEventListener('keydown', handler);
    return () => document.removeEventListener('keydown', handler);
  }, [onCopy, onDuplicate, onPaste, onRedo, onUndo, readOnly]);

  return (
    <div className="graph-canvas">
      {!readOnly && (
        <div className="graph-palette">
          <h3>{t('graph.canvas.nodes')}</h3>
          {PALETTE_ORDER.map((type) => {
            const k = KINDS[type];
            return (
              // A plain <div role="button"> rather than a <button>: native HTML5
              // drag does not reliably initiate from a <button> in Chromium (the
              // button swallows the mousedown so dragstart never fires), which
              // left only click-to-add working. A draggable div drags correctly
              // and still adds on click via onClick / keyboard.
              <div
                key={type}
                role="button"
                tabIndex={0}
                className="graph-node-chip"
                draggable
                onDragStart={(e) => {
                  e.dataTransfer.setData('application/quartet-node', type);
                  e.dataTransfer.effectAllowed = 'move';
                }}
                onClick={() => handlePaletteClick(type)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    handlePaletteClick(type);
                  }
                }}
              >
                <span className="graph-chip-ico" style={{ background: k.color }}>
                  {k.icon}
                </span>
                <span className="graph-chip-text">
                  <span className="graph-chip-label">{labelOf(t, type)}</span>
                  <span className="graph-chip-sub">{subOf(t, type)}</span>
                </span>
              </div>
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
            <button
              type="button"
              className="graph-connect-toggle"
              onClick={handleAutoLayout}
              title={t('graph.canvas.autoLayout')}
            >
              {t('graph.canvas.autoLayout')}
            </button>
            {onUndo && (
              <button
                type="button"
                className="graph-connect-icon-btn"
                onClick={onUndo}
                disabled={!canUndo}
                title={t('graph.canvas.undo')}
                aria-label={t('graph.canvas.undo')}
                data-testid="graph-undo"
              >
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M9 14 4 9l5-5" />
                  <path d="M4 9h11a5 5 0 0 1 0 10h-1" />
                </svg>
              </button>
            )}
            {onRedo && (
              <button
                type="button"
                className="graph-connect-icon-btn"
                onClick={onRedo}
                disabled={!canRedo}
                title={t('graph.canvas.redo')}
                aria-label={t('graph.canvas.redo')}
                data-testid="graph-redo"
              >
                <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="m15 14 5-5-5-5" />
                  <path d="M20 9H9a5 5 0 0 0 0 10h1" />
                </svg>
              </button>
            )}
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
                {connectSource && connectTargets.length > 0 && (
                  <span className="graph-connect-targets" role="group" aria-label={t('graph.canvas.connectTargets')}>
                    {connectTargets.map((node) => {
                      const sourceHandle = connectSource.kind === 'ifElse' && connectPort !== 'default' ? connectPort : null;
                      const reason = getConnectionError?.({
                        source: connectSource.id,
                        target: node.id,
                        sourceHandle,
                        targetHandle: null,
                      });
                      return (
                        <button
                          key={node.id}
                          type="button"
                          disabled={!!reason}
                          title={reason || undefined}
                          onClick={() => finishTapConnect(node.id)}
                        >
                          {node.data.graphNode.title || node.id}
                        </button>
                      );
                    })}
                  </span>
                )}
                {connectError && <span className="graph-connect-error">{connectError}</span>}
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
          isValidConnection={isValidConnection}
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
          <Background gap={18} color="#285f39" />
          <Controls showInteractive={false} />
          {showMiniMap && <MiniMap className="graph-minimap" />}
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
