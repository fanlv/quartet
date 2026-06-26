import { Handle, NodeResizer, Position, type NodeProps } from '@xyflow/react';
import { useTranslation } from 'react-i18next';
import type { QuartetFlowNode } from '../graphFlowAdapter';

// Floor for the loop container so it always fits its head plus at least one
// child row; keeps the resize handle from collapsing the box to nothing.
const LOOP_MIN_WIDTH = 280;
const LOOP_MIN_HEIGHT = 180;

export function LoopGroupNode({ data, selected }: NodeProps<QuartetFlowNode>) {
  const { t } = useTranslation();
  const { graphNode, runStatus, hasError, editable } = data;
  const cfg = graphNode.config;
  const meta =
    cfg?.loopMode === 'until' ? (
      <>
        {t('graph.node.loopUntil')}<span className="qg-mono">{cfg.untilCondition || t('graph.node.loopCondition')}</span>
      </>
    ) : (
      <>
        {t('graph.node.loopFixedPrefix')}<b>{cfg?.fixedCount ?? 0}</b>{t('graph.node.loopFixedSuffix')}
      </>
    );

  const className = [
    'qg-loop',
    selected ? 'selected' : '',
    runStatus ? `run-${runStatus}` : '',
    hasError ? 'has-error' : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={className} data-testid={`graph-loop-${graphNode.id}`}>
      {editable && (
        <NodeResizer
          minWidth={LOOP_MIN_WIDTH}
          minHeight={LOOP_MIN_HEIGHT}
          isVisible={selected}
          lineClassName="qg-loop-resize-line"
          handleClassName="qg-loop-resize-handle"
        />
      )}
      {/* Pin the container's outward connect points to the head row. The loop's
          entry/exit markers sit vertically-centred on the same left/right
          borders, and React Flow always paints a child node (z=1) above its
          parent container (z=0) — so a handle left at the vertical centre is
          completely covered by the marker and cannot be grabbed to start (or
          land) a connection. Offsetting them up into the head row clears the
          markers so the loop is connectable again. */}
      <Handle type="target" position={Position.Left} style={{ top: 17 }} />
      <div className="qg-loop-head">
        <span className="qg-loop-badge">🔁 {graphNode.title || t('graph.node.loopDefaultTitle')}</span>
        <span className="qg-loop-meta">{meta}</span>
        {cfg?.loopMode === 'until' && cfg?.maxIterations ? <span className="qg-loop-cap">{t('graph.node.loopFallback', { count: cfg.maxIterations })}</span> : null}
      </div>
      <Handle type="source" position={Position.Right} style={{ top: 17 }} />
    </div>
  );
}
