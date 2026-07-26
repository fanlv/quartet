import { memo } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type { GraphNodeConfig } from '../../../types/graph';
import type { QuartetFlowNode } from '../graphFlowAdapter';
import { kindOf, labelOf } from './kinds';

function truncate(s: string | undefined, n = 70): string {
  const v = s || '';
  return v.length > n ? `${v.slice(0, n)}…` : v;
}

// Collect {{var}} references from the textual config fields so the node can
// display its input variable pills.
function extractVars(cfg: GraphNodeConfig | undefined): string[] {
  const txt = [cfg?.script, cfg?.prompt, cfg?.condition, cfg?.untilCondition].filter(Boolean).join(' ');
  const set = new Set<string>();
  const re = /\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(txt))) set.add(m[1]);
  return [...set];
}

function bodyContent(t: TFunction, kind: string, cfg: GraphNodeConfig | undefined) {
  switch (kind) {
    case 'shell':
      return cfg?.script ? <span className="qg-mono">$ {truncate(cfg.script)}</span> : <i>{t('graph.node.noCommand')}</i>;
    case 'prompt':
      return (
        <span>
          {cfg?.agentType ? (
            <>
              <span className="qg-mono">{cfg.agentType}</span> ·{' '}
            </>
          ) : null}
          {truncate(cfg?.prompt) || <i>{t('graph.node.noPrompt')}</i>}
        </span>
      );
    case 'clarify':
      return (
        <span>
          {cfg?.agentType ? (
            <>
              <span className="qg-mono">{cfg.agentType}</span> ·{' '}
            </>
          ) : null}
          {truncate(cfg?.prompt) || <i>{t('graph.node.noClarifyPrompt')}</i>}
        </span>
      );
    case 'ifElse':
      return cfg?.condition ? (
        <span>
          {t('graph.node.conditionPrefix')}<span className="qg-mono">{truncate(cfg.condition)}</span>
        </span>
      ) : (
        <i>{t('graph.node.noCondition')}</i>
      );
    case 'start':
      return t('graph.node.startBody');
    case 'end':
      return t('graph.node.endBody');
    default:
      return null;
  }
}

function QuartetNodeImpl({ data, selected }: NodeProps<QuartetFlowNode>) {
  const { t } = useTranslation();
  const { kind, graphNode, runStatus, hasError } = data;
  const cfg = graphNode.config;
  const k = kindOf(kind);

  // A loop-scoped start/end (parentId set) is the loop's entry / exit marker,
  // rendered as a port tab mounted flush ON the container border rather than a
  // full node card. The entry exposes an inward (source) handle on its right
  // edge; the exit an inward (target) handle on its left edge, so the user wires
  // entry → body → exit. Sizing / pinning to the border is owned by the adapter
  // (configToFlow), keeping the marker glued to the boundary as the loop resizes.
  if ((kind === 'start' || kind === 'end') && graphNode.parentId) {
    const isEntry = kind === 'start';
    const markerClass = [
      'qg-loop-port',
      isEntry ? 'entry' : 'exit',
      selected ? 'selected' : '',
      runStatus ? `run-${runStatus}` : '',
      hasError ? 'has-error' : '',
    ]
      .filter(Boolean)
      .join(' ');
    return (
      <div
        className={markerClass}
        data-testid={`graph-loop-port-${graphNode.id}`}
        title={isEntry ? t('graph.node.loopEntryTitle') : t('graph.node.loopExitTitle')}
      >
        <span className="qg-loop-port-dot">{isEntry ? '▶' : '■'}</span>
        <span className="qg-loop-port-label">
          {isEntry ? t('graph.node.loopEntryLabel') : t('graph.node.loopExitLabel')}
        </span>
        {isEntry ? (
          <Handle type="source" position={Position.Right} />
        ) : (
          <Handle type="target" position={Position.Left} />
        )}
      </div>
    );
  }

  const inVars = extractVars(cfg);
  const outVars = cfg?.outputVariables || [];
  const alias = cfg?.lastAssistantAlias;
  const body = bodyContent(t, kind, cfg);

  const className = [
    'qg-node',
    selected ? 'selected' : '',
    runStatus ? `run-${runStatus}` : '',
    hasError ? 'has-error' : '',
  ]
    .filter(Boolean)
    .join(' ');

  const isBranch = kind === 'ifElse';

  return (
    <div className={className} data-testid={`graph-node-${graphNode.id}`}>
      {kind !== 'start' && <Handle type="target" position={Position.Left} />}
      <div className="qg-node-head">
        <div className="qg-node-ico" style={{ background: k.color }}>
          {k.icon}
        </div>
        <div className="qg-node-title">{graphNode.title || labelOf(t, kind)}</div>
        <div className="qg-node-kind">{labelOf(t, kind)}</div>
      </div>
      {body && <div className="qg-node-body">{body}</div>}
      {(inVars.length > 0 || outVars.length > 0 || alias) && (
        <div className="qg-node-vars">
          {inVars.map((v) => (
            <span className="qg-var-pill" key={`in-${v}`}>{`{{${v}}}`}</span>
          ))}
          {outVars.map((v) => (
            <span className="qg-var-pill out" key={`out-${v}`}>{`→ ${v}`}</span>
          ))}
          {alias && <span className="qg-var-pill alias">{`→ ${alias}`}</span>}
        </div>
      )}
      {isBranch ? (
        <>
          <Handle id="yes" type="source" position={Position.Right} style={{ top: '35%' }} />
          <Handle id="no" type="source" position={Position.Right} style={{ top: '70%' }} />
          <span className="qg-port-tag yes">YES</span>
          <span className="qg-port-tag no">NO</span>
        </>
      ) : kind !== 'end' ? (
        <Handle type="source" position={Position.Right} />
      ) : null}
    </div>
  );
}

// React Flow rebuilds the nodes array (and each node's `data` object) on every
// live-run status reconcile — roughly twice a second — even though only a few
// nodes actually change. Without memoization that re-runs each node's
// translation + {{var}} extraction + JSX for the whole graph, which is what
// pegs the main thread and freezes the page on a large workflow (e.g. 72 nodes)
// on a phone. The comparator below bails out unless something this node renders
// actually changed: its selection, the GraphNode it wraps (kept referentially
// stable per version by the page — a config edit produces a new object), or the
// injected run-status / error flags. `data.editable` is intentionally ignored
// (QuartetNode does not read it; only LoopGroupNode does).
function quartetNodePropsEqual(a: NodeProps<QuartetFlowNode>, b: NodeProps<QuartetFlowNode>): boolean {
  return (
    a.selected === b.selected &&
    a.data.graphNode === b.data.graphNode &&
    a.data.kind === b.data.kind &&
    a.data.runStatus === b.data.runStatus &&
    a.data.hasError === b.data.hasError
  );
}

export const QuartetNode = memo(QuartetNodeImpl, quartetNodePropsEqual);
