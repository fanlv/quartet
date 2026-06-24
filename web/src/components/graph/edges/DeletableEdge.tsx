import { useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  useReactFlow,
  type EdgeProps,
} from '@xyflow/react';
import type { QuartetFlowEdge } from '../graphFlowAdapter';

// Custom edge that draws an always-visible × button at its midpoint so edges can
// be deleted with a single tap — touch devices (iPad) have no Delete key and
// hitting a thin edge to select it is unreliable. The YES/NO branch label is
// rendered as an inline HTML pill next to the button. The delete affordance is
// suppressed in read-only replay (data.canDelete === false).
export function DeletableEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  style,
  data,
}: EdgeProps<QuartetFlowEdge>) {
  const { t } = useTranslation();
  const { deleteElements } = useReactFlow();
  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
  });

  const onDelete = useCallback(
    (event: React.MouseEvent) => {
      event.stopPropagation();
      // In controlled mode this dispatches a `remove` change through the page's
      // onEdgesChange, keeping the edges state authoritative.
      void deleteElements({ edges: [{ id }] });
    },
    [deleteElements, id],
  );

  const port = data?.port;
  const canDelete = data?.canDelete !== false;

  return (
    <>
      <BaseEdge id={id} path={edgePath} markerEnd={markerEnd} style={style} />
      <EdgeLabelRenderer>
        <div
          className="qg-edge-label nodrag nopan"
          style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
        >
          {port && <span className={`qg-edge-port ${port}`}>{port === 'yes' ? 'YES' : 'NO'}</span>}
          {canDelete && (
            <button
              type="button"
              className="qg-edge-delete"
              data-testid={`graph-edge-delete-${id}`}
              onClick={onDelete}
              aria-label={t('graph.canvas.deleteEdge')}
              title={t('graph.canvas.deleteEdge')}
            >
              ×
            </button>
          )}
        </div>
      </EdgeLabelRenderer>
    </>
  );
}
