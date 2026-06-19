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
    case 'evaluator':
      return truncate(cfg?.prompt, 80) || <i>{t('graph.node.noCriteria')}</i>;
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

export function QuartetNode({ data, selected }: NodeProps<QuartetFlowNode>) {
  const { t } = useTranslation();
  const { kind, graphNode, runStatus, hasError } = data;
  const cfg = graphNode.config;
  const k = kindOf(kind);
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
