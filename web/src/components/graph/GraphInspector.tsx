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
import './GraphInspector.css';

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
  onUpdateNode: (id: string, patch: Partial<GraphNode>) => void;
  onUpdateNodeConfig: (id: string, patch: Partial<GraphNodeConfig>) => void;
  onDeleteNode: (id: string) => void;
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
  onUpdateNode,
  onUpdateNodeConfig,
  onDeleteNode,
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

  const selectedAgent = node?.config?.agentType
    ? agents.find((a) => a.type === node.config?.agentType)
    : undefined;
  const availableModels = selectedAgent?.models?.availableModels || [];
  const availableThoughtLevels = selectedAgent?.thoughtLevels?.availableThoughtLevels || [];

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
        <div className="gi-var-row" key={`${i}-${k}`}>
          <input
            className="gi-var-key"
            value={k}
            placeholder={t('graph.inspector.varNamePlaceholder')}
            disabled={readOnly}
            onChange={(e) => setVar(i, e.target.value, v)}
          />
          <input
            className="gi-var-val"
            value={v}
            placeholder={t('graph.inspector.varValuePlaceholder')}
            disabled={readOnly}
            onChange={(e) => setVar(i, k, e.target.value)}
          />
          <label className="gi-var-disabled" title={t('graph.inspector.disableVar')}>
            <input
              type="checkbox"
              checked={disabled.has(k)}
              disabled={readOnly}
              onChange={() => toggleDisabled(k)}
            />
          </label>
          <button className="gi-var-del" disabled={readOnly} onClick={() => removeVar(i)} aria-label={t('graph.inspector.deleteVar')}>
            ×
          </button>
        </div>
      ))}
      {!readOnly && (
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
          placeholder="4"
          disabled={readOnly}
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
          disabled={readOnly}
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
          disabled={readOnly}
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
          disabled={readOnly}
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
          <input
            value={cfg.condition || ''}
            placeholder='{{verdict}} == "PASS"'
            disabled={readOnly}
            onChange={(e) => setCfg({ condition: e.target.value })}
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
                onClick={() => setCfg({ loopMode: 'fixed' as GraphLoopMode })}
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
            <div className="gi-field">
              <label>{t('graph.inspector.untilCondition')}</label>
              <input
                value={cfg.untilCondition || ''}
                placeholder='{{verdict}} == "PASS"'
                disabled={readOnly}
                onChange={(e) => setCfg({ untilCondition: e.target.value })}
              />
            </div>
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
            <div className="gi-desc">{t('graph.inspector.maxIterationsDesc')}</div>
          </div>
        </>
      )}

      {(node.type === 'start' || node.type === 'end') && (
        <div className="gi-desc">{t('graph.inspector.controlNodeDesc')}</div>
      )}

      {!readOnly && !frozen && node.type !== 'start' && node.type !== 'end' && (
        <button className="gi-delete-btn" onClick={() => onDeleteNode(node.id)}>
          {t('graph.inspector.deleteNode')}
        </button>
      )}
        </fieldset>

        {GlobalPanel}
      </div>
    </aside>
  );
}
