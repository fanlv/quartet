import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { AgentInfo } from '../ChatPage';
import type {
  GraphConfig,
  GraphLoopMode,
  GraphNode,
  GraphNodeConfig,
  GraphRunConfig,
  GraphSessionStrategy,
} from '../../types/graph';
import { kindOf, labelOf } from './nodes/kinds';
import { ConditionBuilder } from './ConditionBuilder';
import './GraphInspector.css';

// Reserved variable always available to a condition: the upstream node's raw
// final assistant message. Mirrors services/graph reservedLastAssistant.
const RESERVED_LAST_ASSISTANT = '_last_assistant_msg';

// Engine-injected loop iteration variables, available to any node inside a loop
// body (and to the loop's own "until" condition). Mirrors services/graph
// loopvars.go QUARTET_LOOP_* names.
const LOOP_ITERATION_VARS = ['QUARTET_LOOP_INDEX', 'QUARTET_LOOP_FIXED_COUNT', 'QUARTET_LOOP_MAX_ITERS'];

// Variable names a condition at `nodeId` may reference: global variable keys,
// the reserved _last_assistant_msg, and every upstream node's declared outputs
// + last-assistant alias (found by walking edges backwards). For a loop node
// the body's own nodes (parentId === nodeId) are included too, since the
// "until" condition is evaluated after each round with body outputs visible.
// Collection is best-effort — the builder's inputs are comboboxes, so a name
// that isn't listed can still be typed by hand.
function collectAvailableVars(config: GraphConfig, nodeId: string): string[] {
  const nodes = config.nodes || [];
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const incoming = new Map<string, string[]>();
  for (const e of config.edges || []) {
    const list = incoming.get(e.targetNodeId);
    if (list) list.push(e.sourceNodeId);
    else incoming.set(e.targetNodeId, [e.sourceNodeId]);
  }

  const out = new Set<string>();
  for (const k of Object.keys(config.variables || {})) out.add(k);
  out.add(RESERVED_LAST_ASSISTANT);

  const addNodeOutputs = (n?: GraphNode) => {
    if (!n) return;
    for (const v of n.config?.outputVariables || []) if (v) out.add(v);
    if (n.config?.lastAssistantAlias) out.add(n.config.lastAssistantAlias);
  };

  const seen = new Set<string>();
  const queue = [...(incoming.get(nodeId) || [])];
  while (queue.length) {
    const cur = queue.shift() as string;
    if (seen.has(cur)) continue;
    seen.add(cur);
    addNodeOutputs(byId.get(cur));
    for (const src of incoming.get(cur) || []) queue.push(src);
  }

  const self = byId.get(nodeId);
  if (self?.type === 'loop') {
    for (const n of nodes) if (n.parentId === nodeId) addNodeOutputs(n);
  }

  // Loop iteration vars are visible to any node inside a loop body, and to the
  // loop container's own "until" condition (evaluated after each round).
  if (self?.parentId || self?.type === 'loop') {
    for (const v of LOOP_ITERATION_VARS) out.add(v);
  }

  return [...out].sort((a, b) => a.localeCompare(b));
}

interface GraphInspectorProps {
  node: GraphNode | null;
  config: GraphConfig;
  agents: AgentInfo[];
  readOnly?: boolean;
  drawerOpen?: boolean;
  // Node IDs whose execution config is frozen for run-time version editing
  // (they already have a succeeded/skipped/running instance). When the selected
  // node is frozen, its config fields are disabled and a banner is shown. The
  // backend still enforces this — the UI block is a convenience.
  frozenNodeIds?: Set<string>;
  // When true the graph structure is locked: only per-node config can change.
  // The delete-node button is hidden and the global variable / run-config panel
  // becomes read-only. Used by run-version editing (in-place and full-page),
  // where adding/removing nodes & edges is disallowed.
  lockStructure?: boolean;
  onUpdateNode: (id: string, patch: Partial<GraphNode>) => void;
  onUpdateNodeConfig: (id: string, patch: Partial<GraphNodeConfig>) => void;
  onDeleteNode: (id: string) => void;
  // Duplicate this node (and, for a loop, its whole body) onto the canvas.
  // Omitted while the graph structure is locked (run-version editing).
  onDuplicateNode?: (id: string) => void;
  onUpdateVariables: (variables: Record<string, string>, disabledVars: string[]) => void;
  onUpdateRunConfig: (patch: Partial<GraphRunConfig>) => void;
  onDrawerToggle?: () => void;
}

// CSV <-> string[] helpers for the output-variable list (single-line scalars).
function listToText(list: string[] | undefined): string {
  return (list || []).join(', ');
}
function textToList(text: string): string[] {
  return text
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
}

function numberOrUndefined(value: string): number | undefined {
  if (value.trim() === '') return undefined;
  const n = Number(value);
  return Number.isFinite(n) ? n : undefined;
}

export function GraphInspector({
  node,
  config,
  agents,
  readOnly,
  drawerOpen = true,
  frozenNodeIds,
  lockStructure,
  onUpdateNode,
  onUpdateNodeConfig,
  onDeleteNode,
  onDuplicateNode,
  onUpdateVariables,
  onUpdateRunConfig,
  onDrawerToggle,
}: GraphInspectorProps) {
  const { t } = useTranslation();
  const variableRows = useMemo(
    () => Object.entries(config.variables || {}),
    [config.variables],
  );
  const disabled = new Set(config.disabledVars || []);
  // Structure-lock (run-version editing) makes the global variable table and
  // run-config read-only — only per-node config may change there.
  const globalsReadOnly = readOnly || lockStructure;

  const selectedAgent = node?.config?.agentType
    ? agents.find((a) => a.type === node.config?.agentType)
    : undefined;
  const availableModels = selectedAgent?.models?.availableModels || [];
  const availableThoughtLevels = selectedAgent?.thoughtLevels?.availableThoughtLevels || [];

  // Variable names selectable in the If-Else / loop-until condition builder.
  // Computed for any selected node (empty when nothing is selected); kept above
  // the early return so hook order stays stable.
  const conditionVars = useMemo(
    () => (node ? collectAvailableVars(config, node.id) : []),
    [config, node],
  );

  // ---- Global variable table (shown when nothing is selected) ----
  const setVar = (index: number, key: string, value: string) => {
    const entries = [...variableRows];
    entries[index] = [key, value];
    const next: Record<string, string> = {};
    for (const [k, v] of entries) {
      if (k) next[k] = v;
    }
    onUpdateVariables(next, [...disabled].filter((d) => next[d] !== undefined));
  };
  const removeVar = (index: number) => {
    const entries = variableRows.filter((_, i) => i !== index);
    const next: Record<string, string> = {};
    for (const [k, v] of entries) {
      if (k) next[k] = v;
    }
    onUpdateVariables(next, [...disabled].filter((d) => next[d] !== undefined));
  };
  const addVar = () => {
    const next = { ...(config.variables || {}) };
    let i = 1;
    let key = 'var';
    while (next[key] !== undefined) {
      key = `var${i++}`;
    }
    next[key] = '';
    onUpdateVariables(next, [...disabled]);
  };
  const toggleDisabled = (key: string) => {
    const nextDisabled = new Set(disabled);
    if (nextDisabled.has(key)) nextDisabled.delete(key);
    else nextDisabled.add(key);
    onUpdateVariables({ ...(config.variables || {}) }, [...nextDisabled]);
  };

  const runConfig = config.runConfig || {};
  const inspectorTitle = node ? t('graph.inspector.nodeConfig') : t('graph.inspector.globalConfig');
  const inspectorSubtitle = node ? (node.title || node.id) : t('graph.inspector.varsAndRunConfig');
  const asideClassName = `graph-inspector ${drawerOpen ? 'drawer-open' : 'drawer-collapsed'}`;
  const DrawerHeader = (
    <button
      type="button"
      className="gi-drawer-handle"
      data-testid="graph-inspector-drawer-toggle"
      aria-expanded={drawerOpen}
      onClick={onDrawerToggle}
      disabled={!onDrawerToggle}
    >
      <span className="gi-drawer-grip" aria-hidden="true" />
      <span className="gi-drawer-title">
        <span>{inspectorTitle}</span>
        <small>{inspectorSubtitle}</small>
      </span>
      <span className="gi-drawer-chevron" aria-hidden="true">
        {drawerOpen ? '⌄' : '⌃'}
      </span>
    </button>
  );

  const GlobalPanel = (
    <div className="gi-section">
      <div className="gi-workdir-note" data-testid="gi-workdir-note">
        {t('graph.inspector.workdirNote')}
      </div>
      <h3>{t('graph.inspector.globalVariables')}</h3>
      <div className="gi-desc">{t('graph.inspector.globalVariablesDesc')}</div>
      {variableRows.length === 0 && <div className="gi-empty">{t('graph.inspector.noVariables')}</div>}
      {variableRows.map(([k, v], i) => (
        <div className="gi-var-row" key={i}>
          <input
            className="gi-var-key"
            value={k}
            placeholder={t('graph.inspector.varNamePlaceholder')}
            disabled={globalsReadOnly}
            onChange={(e) => setVar(i, e.target.value, v)}
          />
          <input
            className="gi-var-val"
            value={v}
            placeholder={t('graph.inspector.varValuePlaceholder')}
            disabled={globalsReadOnly}
            onChange={(e) => setVar(i, k, e.target.value)}
          />
          <label className="gi-var-disabled" title={t('graph.inspector.disableVar')}>
            <input
              type="checkbox"
              checked={disabled.has(k)}
              disabled={globalsReadOnly}
              onChange={() => toggleDisabled(k)}
            />
          </label>
          <button className="gi-var-del" disabled={globalsReadOnly} onClick={() => removeVar(i)} aria-label={t('graph.inspector.deleteVar')}>
            ×
          </button>
        </div>
      ))}
      {!globalsReadOnly && (
        <button className="gi-add-btn" onClick={addVar}>
          {t('graph.inspector.addVar')}
        </button>
      )}

      <h3 style={{ marginTop: 18 }}>{t('graph.inspector.runConfig')}</h3>
      <div className="gi-field">
        <label>{t('graph.inspector.concurrency')}</label>
        <input
          type="number"
          min={1}
          max={16}
          value={runConfig.concurrencyLimit ?? ''}
          placeholder="8"
          disabled={globalsReadOnly}
          onChange={(e) => onUpdateRunConfig({ concurrencyLimit: numberOrUndefined(e.target.value) })}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.defaultNodeTimeout')}</label>
        <input
          type="number"
          min={0}
          value={runConfig.defaultNodeTimeoutSec ?? ''}
          placeholder="0"
          disabled={globalsReadOnly}
          onChange={(e) => onUpdateRunConfig({ defaultNodeTimeoutSec: numberOrUndefined(e.target.value) })}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.jobTimeout')}</label>
        <input
          type="number"
          min={0}
          value={runConfig.jobTimeoutSec ?? ''}
          placeholder="0"
          disabled={globalsReadOnly}
          onChange={(e) => onUpdateRunConfig({ jobTimeoutSec: numberOrUndefined(e.target.value) })}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.loopMaxItersFallback')}</label>
        <input
          type="number"
          min={1}
          max={1000}
          value={runConfig.defaultLoopMaxIters ?? ''}
          placeholder="100"
          disabled={globalsReadOnly}
          onChange={(e) => onUpdateRunConfig({ defaultLoopMaxIters: numberOrUndefined(e.target.value) })}
        />
      </div>
    </div>
  );

  if (!node) {
    return (
      <aside className={asideClassName} data-testid="graph-inspector">
        {DrawerHeader}
        <div className="gi-scroll">
          <div className="gi-empty-hint">{t('graph.inspector.selectNodeHint')}</div>
          {GlobalPanel}
        </div>
      </aside>
    );
  }

  const k = kindOf(node.type);
  const cfg = node.config || {};
  const setCfg = (patch: Partial<GraphNodeConfig>) => onUpdateNodeConfig(node.id, patch);
  // During run-time version editing, a node that already has a reliable /
  // in-flight instance cannot change its execution config (backend enforces).
  const frozen = !!frozenNodeIds?.has(node.id);

  const isAgentNode = node.type === 'prompt' || node.type === 'evaluator';
  const canHaveOutput = node.type === 'shell' || isAgentNode;

  const TitleField = (
    <div className="gi-field">
      <label>{t('graph.inspector.nodeName')}</label>
      <input value={node.title || ''} disabled={readOnly} onChange={(e) => onUpdateNode(node.id, { title: e.target.value })} />
    </div>
  );

  const OutputFields = canHaveOutput && (
    <>
      <div className="gi-field">
        <label>
          {node.type === 'evaluator' ? t('graph.inspector.outputVarsEvaluator') : t('graph.inspector.outputVarsOptional')}
        </label>
        <input
          value={listToText(cfg.outputVariables)}
          placeholder={t('graph.inspector.outputVarsPlaceholder')}
          disabled={readOnly}
          onChange={(e) => setCfg({ outputVariables: textToList(e.target.value) })}
        />
        <div className="gi-desc">
          {node.type === 'shell'
            ? t('graph.inspector.outputVarsShellDesc')
            : t('graph.inspector.outputVarsAgentDesc')}
        </div>
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.lastAssistantAlias')}</label>
        <input
          value={cfg.lastAssistantAlias || ''}
          placeholder={t('graph.inspector.lastAssistantAliasPlaceholder')}
          disabled={readOnly}
          onChange={(e) => setCfg({ lastAssistantAlias: e.target.value })}
        />
        <div className="gi-desc">{t('graph.inspector.lastAssistantAliasDesc')}</div>
      </div>
    </>
  );

  const TimeoutField = (node.type === 'shell' || isAgentNode) && (
    <div className="gi-field">
      <label>{t('graph.inspector.nodeTimeout')}</label>
      <input
        type="number"
        min={0}
        value={cfg.timeoutSeconds ?? ''}
        disabled={readOnly}
        onChange={(e) => setCfg({ timeoutSeconds: numberOrUndefined(e.target.value) })}
      />
    </div>
  );

  const AgentFields = isAgentNode && (
    <>
      <div className="gi-field">
        <label>{t('graph.inspector.agent')}</label>
        <select
          value={cfg.agentType || ''}
          disabled={readOnly}
          onChange={(e) => {
            const agent = agents.find((a) => a.type === e.target.value);
            setCfg({
              agentType: e.target.value,
              modelId: agent?.models?.currentModelId || agent?.model_id || '',
            });
          }}
        >
          <option value="">{t('graph.inspector.selectAgent')}</option>
          {agents.map((a) => (
            <option key={`${a.type}-${a.model_id}`} value={a.type}>
              {a.display_name}
            </option>
          ))}
        </select>
      </div>
      {availableModels.length > 0 && (
        <div className="gi-field">
          <label>{t('graph.inspector.model')}</label>
          <select value={cfg.modelId || ''} disabled={readOnly} onChange={(e) => setCfg({ modelId: e.target.value })}>
            {availableModels.map((m) => (
              <option key={m.modelId} value={m.modelId}>
                {m.name || m.modelId}
              </option>
            ))}
          </select>
        </div>
      )}
      {availableThoughtLevels.length > 0 && (
        <div className="gi-field">
          <label>{t('graph.inspector.thoughtLevel')}</label>
          <select
            value={cfg.acpThoughtLevel || ''}
            disabled={readOnly}
            onChange={(e) => setCfg({ acpThoughtLevel: e.target.value || undefined })}
          >
            <option value="">{t('graph.inspector.thoughtLevelDefault')}</option>
            {availableThoughtLevels.map((tl) => (
              <option key={tl.id} value={tl.id}>
                {tl.name}
              </option>
            ))}
          </select>
          <div className="gi-desc">{t('graph.inspector.thoughtLevelDesc')}</div>
        </div>
      )}
      <div className="gi-field">
        <label>{t('graph.inspector.sessionStrategy')}</label>
        <select
          value={cfg.sessionStrategy || 'new'}
          disabled={readOnly}
          onChange={(e) => setCfg({ sessionStrategy: e.target.value as GraphSessionStrategy })}
        >
          <option value="new">{t('graph.inspector.sessionNew')}</option>
          <option value="inherit">{t('graph.inspector.sessionInherit')}</option>
        </select>
        <div className="gi-desc">{t('graph.inspector.sessionStrategyDesc')}</div>
      </div>
    </>
  );

  return (
    <aside className={asideClassName} data-testid="graph-inspector">
      {DrawerHeader}
      <div className="gi-scroll">
        <h3>{t('graph.inspector.nodeConfig')}</h3>
        <div className="gi-kind-badge" style={{ background: `${k.color}22`, color: k.color, borderColor: `${k.color}55` }}>
          {k.icon} {labelOf(t, node.type)}
        </div>

        {frozen && (
          <div className="gi-frozen-banner" data-testid="gi-frozen-banner">
            {t('graph.inspector.frozenBanner')}
          </div>
        )}

        <fieldset className="gi-fieldset" disabled={frozen}>
          {TitleField}

      {node.type === 'shell' && (
        <>
          <div className="gi-field">
            <label>{t('graph.inspector.shellScript')}</label>
            <textarea
              value={cfg.script || ''}
              placeholder={t('graph.inspector.shellScriptPlaceholder')}
              disabled={readOnly}
              onChange={(e) => setCfg({ script: e.target.value })}
            />
            <div className="gi-desc">{t('graph.inspector.shellScriptDesc', { token: `{{${t('graph.inspector.varName')}}}` })}</div>
          </div>
          {OutputFields}
          {TimeoutField}
        </>
      )}

      {node.type === 'prompt' && (
        <>
          {AgentFields}
          <div className="gi-field">
            <label>{t('graph.inspector.prompt')}</label>
            <textarea
              value={cfg.prompt || ''}
              placeholder={t('graph.inspector.promptPlaceholder')}
              disabled={readOnly}
              onChange={(e) => setCfg({ prompt: e.target.value })}
            />
            <div className="gi-desc">{t('graph.inspector.promptDesc', { token: `{{${t('graph.inspector.varName')}}}` })}</div>
          </div>
          {OutputFields}
          {TimeoutField}
        </>
      )}

      {node.type === 'evaluator' && (
        <>
          {AgentFields}
          <div className="gi-field">
            <label>{t('graph.inspector.evaluatorPrompt')}</label>
            <textarea
              value={cfg.prompt || ''}
              placeholder={t('graph.inspector.evaluatorPromptPlaceholder')}
              disabled={readOnly}
              onChange={(e) => setCfg({ prompt: e.target.value })}
            />
            <div className="gi-desc">{t('graph.inspector.evaluatorPromptDesc')}</div>
          </div>
          {OutputFields}
          {TimeoutField}
        </>
      )}

      {node.type === 'ifElse' && (
        <div className="gi-field">
          <label>{t('graph.inspector.condition')}</label>
          <ConditionBuilder
            key={`cond-${node.id}`}
            fieldId={node.id}
            value={cfg.condition || ''}
            availableVars={conditionVars}
            readOnly={readOnly}
            onChange={(next) => setCfg({ condition: next })}
            t={t}
          />
          <div className="gi-desc">
            {t('graph.inspector.conditionDescPrefix')}<b style={{ color: '#2ea043' }}>{t('graph.inspector.conditionDescYes')}</b>{t('graph.inspector.conditionDescMid')}<b style={{ color: '#f85149' }}>{t('graph.inspector.conditionDescNo')}</b>{t('graph.inspector.conditionDescSuffix')}
          </div>
        </div>
      )}

      {node.type === 'loop' && (
        <>
          <div className="gi-field">
            <label>{t('graph.inspector.loopMode')}</label>
            <div className="gi-seg">
              <button
                type="button"
                className={cfg.loopMode !== 'until' ? 'active' : ''}
                disabled={readOnly}
                onClick={() => setCfg({ loopMode: 'fixed' as GraphLoopMode, maxIterations: undefined })}
              >
                {t('graph.inspector.loopFixed')}
              </button>
              <button
                type="button"
                className={cfg.loopMode === 'until' ? 'active' : ''}
                disabled={readOnly}
                onClick={() => setCfg({ loopMode: 'until' as GraphLoopMode })}
              >
                {t('graph.inspector.loopUntil')}
              </button>
            </div>
          </div>
          {cfg.loopMode === 'until' ? (
            <>
              <div className="gi-field">
                <label>{t('graph.inspector.untilCondition')}</label>
                <ConditionBuilder
                  key={`until-${node.id}`}
                  fieldId={`until-${node.id}`}
                  value={cfg.untilCondition || ''}
                  availableVars={conditionVars}
                  readOnly={readOnly}
                  onChange={(next) => setCfg({ untilCondition: next })}
                  t={t}
                />
              </div>
              <div className="gi-field">
                <label>{t('graph.inspector.maxIterations')}</label>
                <input
                  type="number"
                  min={1}
                  max={1000}
                  value={cfg.maxIterations ?? ''}
                  placeholder="100"
                  disabled={readOnly}
                  onChange={(e) => setCfg({ maxIterations: numberOrUndefined(e.target.value) })}
                />
              </div>
            </>
          ) : (
            <div className="gi-field">
              <label>{t('graph.inspector.fixedCount')}</label>
              <input
                type="number"
                min={0}
                value={cfg.fixedCount ?? ''}
                disabled={readOnly}
                onChange={(e) => setCfg({ fixedCount: numberOrUndefined(e.target.value) })}
              />
            </div>
          )}
          <div className="gi-desc">{t('graph.inspector.loopBodyHint')}</div>
        </>
      )}

      {(node.type === 'start' || node.type === 'end') && (
        <div className="gi-desc">{t('graph.inspector.controlNodeDesc')}</div>
      )}

      {!readOnly && !lockStructure && !frozen && node.type !== 'start' && node.type !== 'end' && (
        <div className="gi-node-actions">
          {onDuplicateNode && (
            <button className="gi-dup-btn" onClick={() => onDuplicateNode(node.id)}>
              {t('graph.inspector.duplicateNode')}
            </button>
          )}
          <button className="gi-delete-btn" onClick={() => onDeleteNode(node.id)}>
            {t('graph.inspector.deleteNode')}
          </button>
        </div>
      )}
        </fieldset>

        {GlobalPanel}
      </div>
    </aside>
  );
}
