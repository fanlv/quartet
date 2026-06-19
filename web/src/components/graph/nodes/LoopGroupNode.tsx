import { Handle, Position, type NodeProps } from '@xyflow/react';
import { useTranslation } from 'react-i18next';
import type { QuartetFlowNode } from '../graphFlowAdapter';

export function LoopGroupNode({ data, selected }: NodeProps<QuartetFlowNode>) {
  const { t } = useTranslation();
  const { graphNode, runStatus, hasError } = data;
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
      <Handle type="target" position={Position.Left} />
      <div className="qg-loop-head">
        <span className="qg-loop-badge">🔁 {graphNode.title || t('graph.node.loopDefaultTitle')}</span>
        <span className="qg-loop-meta">{meta}</span>
        {cfg?.maxIterations ? <span className="qg-loop-cap">{t('graph.node.loopFallback', { count: cfg.maxIterations })}</span> : null}
      </div>
      <Handle type="source" position={Position.Right} />
    </div>
  );
}
