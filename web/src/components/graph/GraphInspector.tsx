import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { AgentInfo } from '../ChatPage';
import { useACPThoughtLevels } from '../../hooks/useACPThoughtLevels';
import type {
  GraphConfig,
  GraphEndHookMode,
  GraphLoopMode,
  GraphNode,
  GraphNodeConfig,
  GraphRunConfig,
  GraphSessionStrategy,
} from '../../types/graph';
import { kindOf, labelOf } from './nodes/kinds';
import { useAgentDisplay } from '../../utils/agentDisplay';
import { ConditionBuilder } from './ConditionBuilder';
import { DirPicker } from '../DirPicker';
import './GraphInspector.css';

// Always-present global variables. They are ordinary string variables at
// runtime (env injection + {{token}} substitution); "built-in" only means the
// editor pins them (fixed names, cannot be renamed or deleted) and offers a
// path picker so a value can be a directory or file. Conventionally Code holds
// the code location and Doc the docs location.
export const BUILTIN_VARS = ['Code', 'Doc'] as const;
const BUILTIN_VAR_SET = new Set<string>(BUILTIN_VARS);

// Reserved variable always available to a condition: the upstream node's raw
// final assistant message. Mirrors services/graph reservedLastAssistant.
const RESERVED_LAST_ASSISTANT = '_last_assistant_msg';

// Engine-injected runtime variable always available to any node / condition:
// the current timestamp (RFC3339), stamped at dispatch / eval time. Mirrors
// services/graph consts.VarCurrentTime (loopvars.go runtimeVars).
const RESERVED_CURRENT_TIME = '_current_time';

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
  out.add(RESERVED_CURRENT_TIME);

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
  // Makes only the global variables panel read-only while keeping node,
  // structure, and run-config edits available.
  lockGlobals?: boolean;
  // Makes only the run-config panel read-only.
  lockRunConfig?: boolean;
  onUpdateNode: (id: string, patch: Partial<GraphNode>) => void;
  onUpdateNodeConfig: (id: string, patch: Partial<GraphNodeConfig>) => void;
  onDeleteNode: (id: string) => void;
  canDeleteNode?: (node: GraphNode) => boolean;
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

function integerOrUndefined(value: string): number | undefined {
  if (value.trim() === '') return undefined;
  const n = Number(value);
  return Number.isInteger(n) ? n : undefined;
}
function updateInteger(value: string, update: (value: number | undefined) => void) {
  if (value.trim() !== '' && !Number.isInteger(Number(value))) return;
  update(integerOrUndefined(value));
}

export function GraphInspector({
  node,
  config,
  agents,
  readOnly,
  drawerOpen = true,
  frozenNodeIds,
  lockStructure,
  lockGlobals,
  lockRunConfig,
  onUpdateNode,
  onUpdateNodeConfig,
  onDeleteNode,
  canDeleteNode,
  onDuplicateNode,
  onUpdateVariables,
  onUpdateRunConfig,
  onDrawerToggle,
}: GraphInspectorProps) {
  const { t } = useTranslation();
  // Custom (user-defined) variable rows exclude the built-ins, which render in
  // their own pinned section above the table.
  const variableRows = useMemo(
    () => Object.entries(config.variables || {}).filter(([k]) => !BUILTIN_VAR_SET.has(k)),
    [config.variables],
  );
  const disabled = new Set(config.disabledVars || []);
  // Run-version editing may lock structure, variables, and run config
  // independently because variables can now hot-apply to future instances while
  // run config still has runtime-specific behavior.
  const globalsReadOnly = readOnly || lockStructure || lockGlobals;
  const runConfigReadOnly = readOnly || lockStructure || lockGlobals || lockRunConfig;

  // Which built-in variable's path picker is open (null = closed). The picker
  // can select either a directory or a file and writes the absolute path back
  // into that variable's value.
  const [pickerVar, setPickerVar] = useState<string | null>(null);

  // Mobile bottom drawer: half-height by default, expandable to near-full
  // viewport so long prompts stay editable. Desktop hides the expand button
  // and ignores this state.
  const [drawerFull, setDrawerFull] = useState(false);

  const selectedAgent = node?.config?.agentType
    ? agents.find((a) => a.type === node.config?.agentType)
    : undefined;
  // A node referencing an Agent that left the current list (deleted custom
  // Agent, renamed/legacy identifier) resolves its display info from the
  // retained catalog records so the selector still shows a meaningful name
  // instead of a blank value. GraphInspector only renders in private pages,
  // so the resolve endpoint is always reachable here.
  const unresolvedAgentType = node?.config?.agentType && !selectedAgent ? node.config.agentType : null;
  const unresolvedAgentDisplay = useAgentDisplay(unresolvedAgentType, true);
  const unresolvedAgentLabel = unresolvedAgentDisplay
    ? unresolvedAgentDisplay.deleted
      ? t('chat.agentDeletedName', { name: unresolvedAgentDisplay.displayName })
      : unresolvedAgentDisplay.displayName
    : unresolvedAgentDisplay === null
      ? t('chat.unknownAgent')
      : unresolvedAgentType || '';
  const availableModels = selectedAgent?.models?.availableModels || [];
  const availableModes = selectedAgent?.modes?.availableModes || [];

  const isAgentNodeType = node?.type === 'prompt' || node?.type === 'clarify';
  const linkAgentType = node?.config?.agentType || '';
  const staticModels = selectedAgent?.models;
  // Effective model of the node: explicit override, else the agent's default.
  const linkModelId = node?.config?.modelId || staticModels?.currentModelId || '';
  const inheritsUpstreamSession = node?.config?.sessionStrategy === 'inherit';
  const {
    state: linkedLevels,
    loading: thoughtLevelLinking,
    error: thoughtLevelLinkError,
  } = useACPThoughtLevels(
    linkAgentType,
    linkModelId,
    isAgentNodeType && !inheritsUpstreamSession && Boolean(staticModels),
    linkModelId === staticModels?.currentModelId ? selectedAgent?.thoughtLevels || null : null,
  );
  const availableThoughtLevels = linkedLevels?.availableThoughtLevels || [];

  // Variable names selectable in the If-Else / loop-until condition builder.
  // Computed for any selected node (empty when nothing is selected); kept above
  // the early return so hook order stays stable.
  const conditionVars = useMemo(
    () => (node ? collectAvailableVars(config, node.id) : []),
    [config, node],
  );

  // ---- Mobile keyboard handling ----
  // On narrow screens the inspector is a `position: fixed; bottom: 0` drawer
  // anchored to the LAYOUT viewport. On iOS the virtual keyboard does not shrink
  // the layout viewport — it only shrinks the VISUAL viewport — so `bottom: 0`
  // ends up behind the keyboard and hides the fields being edited. We track
  // window.visualViewport and expose the keyboard height (--gi-kb-inset) and
  // visible height (--gi-vv-height) as CSS custom properties the stylesheet uses
  // to lift the drawer above the keyboard and clamp its height, and we scroll
  // the focused field into view. Mirrors the pattern in ScheduleEditModal /
  // LoopConfigPanel / AgentsLocalEditor. No-op on desktop (inset stays 0).
  const asideRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;
    const el = asideRef.current;
    if (!el) return;

    // Keyboard height = how much the on-screen keyboard occludes the bottom of
    // the LAYOUT viewport (which `position: fixed; bottom` anchors to): the gap
    // between the layout-viewport bottom and the visual-viewport bottom. Small
    // values come from browser chrome (e.g. a bottom URL bar), not a keyboard,
    // so we ignore anything under the same 100px threshold main.tsx uses to
    // detect the keyboard. 0 on desktop, where there is no virtual keyboard.
    const keyboardInset = () => {
      const raw = window.innerHeight - vv.height - vv.offsetTop;
      return raw > 100 ? Math.round(raw) : 0;
    };

    const sync = () => {
      el.style.setProperty('--gi-kb-inset', `${keyboardInset()}px`);
      el.style.setProperty('--gi-vv-height', `${Math.round(vv.height)}px`);
    };

    // Center the focused field in the drawer's scroll area, but only while the
    // keyboard is open — so this never causes scroll jumps on desktop (where the
    // drawer is a static side panel and the inset is always 0).
    const scrollActiveIntoView = () => {
      if (keyboardInset() <= 0) return;
      const active = document.activeElement;
      if (
        (active instanceof HTMLInputElement ||
          active instanceof HTMLTextAreaElement ||
          active instanceof HTMLSelectElement) &&
        el.contains(active)
      ) {
        active.scrollIntoView({ block: 'center', behavior: 'smooth' });
      }
    };

    // Keyboard open/resize: reposition the drawer, then re-center the field.
    const onResize = () => {
      sync();
      setTimeout(scrollActiveIntoView, 150);
    };
    // Focusing another field while the keyboard is already up fires no resize,
    // so re-center here too (delayed to let any keyboard height change settle).
    const onFocusIn = () => {
      setTimeout(scrollActiveIntoView, 300);
    };

    vv.addEventListener('resize', onResize);
    vv.addEventListener('scroll', sync);
    el.addEventListener('focusin', onFocusIn);
    sync();

    return () => {
      vv.removeEventListener('resize', onResize);
      vv.removeEventListener('scroll', sync);
      el.removeEventListener('focusin', onFocusIn);
      el.style.removeProperty('--gi-kb-inset');
      el.style.removeProperty('--gi-vv-height');
    };
  }, []);

  // ---- Global variable table (shown when nothing is selected) ----
  // Rebuild the full variable map from the edited custom rows, always pinning
  // the built-ins (Code/Doc) back in with their current values so editing a
  // custom row never drops them. Built-in names are also rejected here as a
  // safety net against custom rows colliding with them.
  const commitCustomVars = (customEntries: [string, string][]) => {
    const vars = config.variables || {};
    const next: Record<string, string> = {};
    for (const name of BUILTIN_VARS) next[name] = vars[name] ?? '';
    for (const [k, v] of customEntries) {
      if (k && !BUILTIN_VAR_SET.has(k)) next[k] = v;
    }
    onUpdateVariables(next, [...disabled].filter((d) => next[d] !== undefined));
  };
  const setVar = (index: number, key: string, value: string) => {
    // Block renaming a custom row into a reserved built-in name (snap back to
    // the prior key) so Code/Doc stay exclusively owned by the built-in section.
    const prevKey = variableRows[index]?.[0];
    const safeKey = BUILTIN_VAR_SET.has(key) ? (prevKey ?? key) : key;
    const entries = [...variableRows];
    entries[index] = [safeKey, value];
    commitCustomVars(entries);
  };
  const removeVar = (index: number) => {
    commitCustomVars(variableRows.filter((_, i) => i !== index));
  };
  const addVar = () => {
    const existing = config.variables || {};
    let i = 1;
    let key = 'var';
    while (existing[key] !== undefined || BUILTIN_VAR_SET.has(key)) {
      key = `var${i++}`;
    }
    commitCustomVars([...variableRows, [key, '']]);
  };
  const toggleDisabled = (key: string) => {
    const nextDisabled = new Set(disabled);
    if (nextDisabled.has(key)) nextDisabled.delete(key);
    else nextDisabled.add(key);
    onUpdateVariables({ ...(config.variables || {}) }, [...nextDisabled]);
  };
  // Built-in variable value edit (preserves map order — built-ins keep their
  // slot, only the value changes).
  const setBuiltinVar = (name: string, value: string) => {
    const next = { ...(config.variables || {}) };
    next[name] = value;
    onUpdateVariables(next, [...disabled].filter((d) => next[d] !== undefined));
  };

  const runConfig = config.runConfig || {};
  const fieldAria = (label: string) => label.replace(/\s*\([^)]*\)\s*/g, ' ').replace(/\s+/g, ' ').trim();
  const inspectorTitle = node ? t('graph.inspector.nodeConfig') : t('graph.inspector.globalConfig');
  const inspectorSubtitle = node ? (node.title || node.id) : t('graph.inspector.varsAndRunConfig');
  const asideClassName = `graph-inspector ${drawerOpen ? 'drawer-open' : 'drawer-collapsed'}${drawerFull ? ' drawer-full' : ''}`;
  const DrawerHeader = (
    <div className="gi-drawer-bar">
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
      <button
        type="button"
        className="gi-drawer-expand"
        data-testid="graph-inspector-drawer-expand"
        aria-label={drawerFull ? t('graph.inspector.collapse') : t('graph.inspector.expand')}
        aria-pressed={drawerFull}
        title={drawerFull ? t('graph.inspector.collapse') : t('graph.inspector.expand')}
        onClick={() => {
          if (!drawerOpen) {
            setDrawerFull(true);
            onDrawerToggle?.();
          } else {
            setDrawerFull((v) => !v);
          }
        }}
      >
        {drawerFull ? (
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M8 3v3a2 2 0 0 1-2 2H3m18 0h-3a2 2 0 0 1-2-2V3m0 18v-3a2 2 0 0 1 2-2h3M3 16h3a2 2 0 0 1 2 2v3" />
          </svg>
        ) : (
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M8 3H5a2 2 0 0 0-2 2v3m18 0V5a2 2 0 0 0-2-2h-3m0 18h3a2 2 0 0 0 2-2v-3M3 16v3a2 2 0 0 0 2 2h3" />
          </svg>
        )}
      </button>
    </div>
  );

  const GlobalPanel = (
    <div className="gi-section">
      <div className="gi-workdir-note" data-testid="gi-workdir-note">
        {t('graph.inspector.workdirNote')}
      </div>

      <h3>{t('graph.inspector.builtinVariables')}</h3>
      <div className="gi-desc">{t('graph.inspector.builtinVariablesDesc')}</div>
      {BUILTIN_VARS.map((name) => {
        const value = config.variables?.[name] ?? '';
        return (
          <div className="gi-var-row gi-builtin-row" key={name} data-testid={`gi-builtin-${name}`}>
            <span className="gi-var-key gi-builtin-name" title={name}>{name}</span>
            <input
              className="gi-var-val"
              aria-label={t('graph.inspector.builtinValueAria', { name })}
              value={value}
              placeholder={t('graph.inspector.builtinValuePlaceholder')}
              disabled={globalsReadOnly}
              onChange={(e) => setBuiltinVar(name, e.target.value)}
            />
            <button
              type="button"
              className="gi-var-browse"
              disabled={globalsReadOnly}
              title={t('graph.inspector.browsePath')}
              aria-label={t('graph.inspector.browsePath')}
              onClick={() => setPickerVar(name)}
            >
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
              </svg>
            </button>
            <label className="gi-var-disabled" title={t('graph.inspector.disableVar')}>
              <input
                type="checkbox"
                checked={disabled.has(name)}
                disabled={globalsReadOnly}
                onChange={() => toggleDisabled(name)}
              />
            </label>
          </div>
        );
      })}

      <h3 style={{ marginTop: 18 }}>{t('graph.inspector.globalVariables')}</h3>
      <div className="gi-desc">{t('graph.inspector.globalVariablesDesc')}</div>
      {variableRows.length === 0 && <div className="gi-empty">{t('graph.inspector.noVariables')}</div>}
      {variableRows.map(([k, v], i) => (
        <div className="gi-var-row" key={i}>
          <input
            className="gi-var-key"
            aria-label={t('graph.inspector.variableNameAria', { index: i + 1 })}
            value={k}
            placeholder={t('graph.inspector.varNamePlaceholder')}
            disabled={globalsReadOnly}
            onChange={(e) => setVar(i, e.target.value, v)}
          />
          <input
            className="gi-var-val"
            aria-label={t('graph.inspector.variableValueAria', { name: k || String(i + 1) })}
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
          aria-label={fieldAria(t('graph.inspector.concurrency'))}
          min={1}
          max={16}
          step={1}
          value={runConfig.concurrencyLimit ?? ''}
          placeholder="8"
          disabled={runConfigReadOnly}
          onChange={(e) => updateInteger(e.target.value, (value) => onUpdateRunConfig({ concurrencyLimit: value }))}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.defaultNodeTimeout')}</label>
        <input
          type="number"
          aria-label={fieldAria(t('graph.inspector.defaultNodeTimeout'))}
          min={0}
          step={1}
          value={runConfig.defaultNodeTimeoutSec ?? ''}
          placeholder="0"
          disabled={runConfigReadOnly}
          onChange={(e) => updateInteger(e.target.value, (value) => onUpdateRunConfig({ defaultNodeTimeoutSec: value }))}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.jobTimeout')}</label>
        <input
          type="number"
          aria-label={fieldAria(t('graph.inspector.jobTimeout'))}
          min={0}
          step={1}
          value={runConfig.jobTimeoutSec ?? ''}
          placeholder="0"
          disabled={runConfigReadOnly}
          onChange={(e) => updateInteger(e.target.value, (value) => onUpdateRunConfig({ jobTimeoutSec: value }))}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.loopMaxItersFallback')}</label>
        <input
          type="number"
          aria-label={fieldAria(t('graph.inspector.loopMaxItersFallback'))}
          min={1}
          max={1000}
          step={1}
          value={runConfig.defaultLoopMaxIters ?? ''}
          placeholder="100"
          disabled={runConfigReadOnly}
          onChange={(e) => updateInteger(e.target.value, (value) => onUpdateRunConfig({ defaultLoopMaxIters: value }))}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.instanceLimit')}</label>
        <input
          type="number"
          aria-label={fieldAria(t('graph.inspector.instanceLimit'))}
          min={1}
          step={1}
          value={runConfig.instanceLimit ?? ''}
          placeholder="100000"
          disabled={runConfigReadOnly}
          onChange={(e) => updateInteger(e.target.value, (value) => onUpdateRunConfig({ instanceLimit: value }))}
        />
      </div>
      <div className="gi-field">
        <label>{t('graph.inspector.snapshotByteLimit')}</label>
        <input
          type="number"
          aria-label={fieldAria(t('graph.inspector.snapshotByteLimit'))}
          min={1}
          step={1}
          value={runConfig.snapshotByteLimit ?? ''}
          placeholder="1073741824"
          disabled={runConfigReadOnly}
          onChange={(e) => updateInteger(e.target.value, (value) => onUpdateRunConfig({ snapshotByteLimit: value }))}
        />
      </div>

      {pickerVar && (
        <DirPicker
          selectFile
          title={t('graph.inspector.pickPathTitle', { name: pickerVar })}
          initialPath={config.variables?.[pickerVar] || config.workdir || ''}
          onConfirm={(path) => {
            setBuiltinVar(pickerVar, path);
            setPickerVar(null);
          }}
          onCancel={() => setPickerVar(null)}
        />
      )}
    </div>
  );

  if (!node) {
    return (
      <aside ref={asideRef} className={asideClassName} data-testid="graph-inspector">
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

  const isAgentNode = node.type === 'prompt' || node.type === 'clarify';
  const canHaveOutput = node.type === 'shell' || isAgentNode;

  const TitleField = (
    <div className="gi-field">
      <label>{t('graph.inspector.nodeName')}</label>
      <input aria-label={t('graph.inspector.nodeName')} value={node.title || ''} disabled={readOnly} onChange={(e) => onUpdateNode(node.id, { title: e.target.value })} />
    </div>
  );

  const OutputFields = canHaveOutput && (
    <>
      <div className="gi-field">
        <label>
          {t('graph.inspector.outputVarsOptional')}
        </label>
        <input
          aria-label={t('graph.inspector.outputVarsOptional')}
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
          aria-label={t('graph.inspector.lastAssistantAlias')}
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
        aria-label={fieldAria(t('graph.inspector.nodeTimeout'))}
        min={0}
        step={1}
        value={cfg.timeoutSeconds ?? ''}
        disabled={readOnly}
        onChange={(e) => updateInteger(e.target.value, (value) => setCfg({ timeoutSeconds: value }))}
      />
    </div>
  );

  // HookField is the post-completion side-effect script editor. Reused by Prompt
  // nodes (always shown) and by End nodes in 'custom' mode. The script runs after
  // the node completes; its output is ignored and failures only log.
  const HookField = (
    <div className="gi-field">
      <label>{t('graph.inspector.hookScript')}</label>
      <textarea
        aria-label={t('graph.inspector.hookScript')}
        value={cfg.hookScript || ''}
        placeholder={t('graph.inspector.hookScriptPlaceholder')}
        disabled={readOnly}
        onChange={(e) => setCfg({ hookScript: e.target.value })}
      />
      <div className="gi-desc">{t('graph.inspector.hookScriptDesc')}</div>
    </div>
  );

  const endHookMode: GraphEndHookMode = cfg.endHookMode || 'default';
  const EndHookFields = node.type === 'end' && (
    <>
      <div className="gi-field">
        <label>{t('graph.inspector.endHookMode')}</label>
        <select
          aria-label={t('graph.inspector.endHookMode')}
          value={endHookMode}
          disabled={readOnly}
          onChange={(e) => setCfg({ endHookMode: e.target.value as GraphEndHookMode })}
        >
          <option value="default">{t('graph.inspector.endHookModeDefault')}</option>
          <option value="custom">{t('graph.inspector.endHookModeCustom')}</option>
          <option value="off">{t('graph.inspector.endHookModeOff')}</option>
        </select>
        <div className="gi-desc">{t('graph.inspector.endHookModeDesc')}</div>
      </div>
      {endHookMode === 'custom' && HookField}
    </>
  );

  // An `inherit` node forks the upstream Agent's session and reuses its
  // Agent/model/thought_level, so those fields are irrelevant (and the backend
  // validator exempts them). Hide Agent type / model / thought_level then,
  // leaving only the session-strategy selector so the user can switch back.
  const inheritsSession = cfg.sessionStrategy === 'inherit';
  const AgentFields = isAgentNode && (
    <>
      {!inheritsSession && (
        <>
          <div className="gi-field">
            <label>{t('graph.inspector.agent')}</label>
            <select
              aria-label={t('graph.inspector.agent')}
              value={cfg.agentType || ''}
              disabled={readOnly}
              onChange={(e) => {
                const agent = agents.find((a) => a.type === e.target.value);
                setCfg({
                  agentType: e.target.value,
                  modelId: agent?.models?.currentModelId || agent?.model_id || '',
                  // 切换 Agent type 后清空 acpMode / thought_level：不同 Agent 的 mode /
                  // thought_level 取值集合互不相同，残留旧值（如从 trae 的 "default"
                  // 切到只认 read-only/agent/agent-full-access 的 codex）会在运行时触发
                  // "set mode failed: Invalid params" 或 "agent does not advertise a
                  // thought_level config option" 报错。清空后由后端 applyPersistedConfig
                  // 跳过设置、直接走该 Agent 的默认 mode / thought_level。
                  acpMode: undefined,
                  acpThoughtLevel: undefined,
                });
              }}
            >
              <option value="">{t('graph.inspector.selectAgent')}</option>
              {agents.map((a) => (
                <option key={`${a.type}-${a.model_id}`} value={a.type}>
                  {a.display_name}
                </option>
              ))}
              {unresolvedAgentType && (
                <option value={unresolvedAgentType}>{unresolvedAgentLabel}</option>
              )}
            </select>
          </div>
          {availableModels.length > 0 && (
            <div className="gi-field">
              <label>{t('graph.inspector.model')}</label>
              <select
                aria-label={t('graph.inspector.model')}
                value={cfg.modelId || ''}
                disabled={readOnly}
                onChange={(e) => setCfg({ modelId: e.target.value, acpThoughtLevel: undefined })}
              >
                {availableModels.map((m) => (
                  <option key={m.modelId} value={m.modelId}>
                    {m.name || m.modelId}
                  </option>
                ))}
              </select>
            </div>
          )}
          {availableModes.length > 1 && (
            <div className="gi-field">
              <label>{t('graph.inspector.mode')}</label>
              <select
                aria-label={t('graph.inspector.mode')}
                value={cfg.acpMode || ''}
                disabled={readOnly}
                onChange={(e) => setCfg({ acpMode: e.target.value || undefined })}
              >
                <option value="">{t('graph.inspector.modeDefault')}</option>
                {availableModes.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name || m.id}
                  </option>
                ))}
              </select>
              <div className="gi-desc">{t('graph.inspector.modeDesc')}</div>
            </div>
          )}
          {(availableThoughtLevels.length > 1 || thoughtLevelLinking || thoughtLevelLinkError) && (
            <div className="gi-field">
              <label>{t('graph.inspector.thoughtLevel')}</label>
              {availableThoughtLevels.length > 1 && (
                <select
                  aria-label={t('graph.inspector.thoughtLevel')}
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
              )}
              {thoughtLevelLinking ? (
                <div className="gi-desc">{t('graph.inspector.thoughtLevelLinking')}</div>
              ) : thoughtLevelLinkError ? (
                <div className="gi-error">{t('graph.inspector.thoughtLevelLinkFailed', { detail: thoughtLevelLinkError })}</div>
              ) : (
                <div className="gi-desc">{t('graph.inspector.thoughtLevelDesc')}</div>
              )}
            </div>
          )}
        </>
      )}
      <div className="gi-field">
        <label>{t('graph.inspector.sessionStrategy')}</label>
        <select
          aria-label={t('graph.inspector.sessionStrategy')}
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
    <aside ref={asideRef} className={asideClassName} data-testid="graph-inspector">
      {DrawerHeader}
      <div className="gi-scroll">
        <h3>{t('graph.inspector.nodeConfig')}</h3>
        <div className="gi-kind-badge" style={{ background: `${k.color}22`, color: k.color, borderColor: `${k.color}55` }}>
          {k.icon} {labelOf(t, node.type)}
        </div>

        {frozen && (
          <div className="gi-frozen-banner" data-testid="gi-frozen-banner">
            {t(node.type === 'loop' ? 'graph.inspector.frozenBannerLoop' : 'graph.inspector.frozenBanner')}
          </div>
        )}

        {TitleField}

        {/* A frozen loop container keeps most fields locked, but its FixedCount is
            evaluated at each round boundary so it stays editable mid-run (backend
            allows the FixedCount-only edit). Disabling the whole fieldset would
            disable that input too (a disabled <fieldset> disables all descendants),
            so loop nodes opt out of the blanket freeze and re-apply it per field. */}
        <fieldset className="gi-fieldset" disabled={frozen && node.type !== 'loop'}>

      {node.type === 'shell' && (
        <>
          <div className="gi-field">
            <label>{t('graph.inspector.shellScript')}</label>
            <textarea
              aria-label={t('graph.inspector.shellScript')}
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
              aria-label={t('graph.inspector.prompt')}
              value={cfg.prompt || ''}
              placeholder={t('graph.inspector.promptPlaceholder')}
              disabled={readOnly}
              onChange={(e) => setCfg({ prompt: e.target.value })}
            />
            <div className="gi-desc">{t('graph.inspector.promptDesc', { token: `{{${t('graph.inspector.varName')}}}` })}</div>
          </div>
          {OutputFields}
          {TimeoutField}
          {HookField}
        </>
      )}

      {node.type === 'clarify' && (
        <>
          {AgentFields}
          <div className="gi-field">
            <label>{t('graph.inspector.clarifyPrompt')}</label>
            <textarea
              aria-label={t('graph.inspector.clarifyPrompt')}
              value={cfg.prompt || ''}
              placeholder={t('graph.inspector.clarifyPromptPlaceholder')}
              disabled={readOnly}
              onChange={(e) => setCfg({ prompt: e.target.value })}
            />
            <div className="gi-desc">{t('graph.inspector.clarifyPromptDesc')}</div>
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
                disabled={readOnly || frozen}
                onClick={() => setCfg({ loopMode: 'fixed' as GraphLoopMode, maxIterations: undefined })}
              >
                {t('graph.inspector.loopFixed')}
              </button>
              <button
                type="button"
                className={cfg.loopMode === 'until' ? 'active' : ''}
                disabled={readOnly || frozen}
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
                  readOnly={readOnly || frozen}
                  onChange={(next) => setCfg({ untilCondition: next })}
                  t={t}
                />
              </div>
              <div className="gi-field">
                <label>{t('graph.inspector.maxIterations')}</label>
                <input
                  type="number"
                  aria-label={fieldAria(t('graph.inspector.maxIterations'))}
                  min={1}
                  max={1000}
                  step={1}
                  value={cfg.maxIterations ?? ''}
                  placeholder="100"
                  disabled={readOnly || frozen}
                  onChange={(e) => updateInteger(e.target.value, (value) => setCfg({ maxIterations: value }))}
                />
              </div>
            </>
          ) : (
            <div className="gi-field">
              <label>{t('graph.inspector.fixedCount')}</label>
              <input
                type="number"
                aria-label={fieldAria(t('graph.inspector.fixedCount'))}
                min={0}
                step={1}
                value={cfg.fixedCount ?? ''}
                disabled={readOnly}
                onChange={(e) => updateInteger(e.target.value, (value) => setCfg({ fixedCount: value }))}
              />
            </div>
          )}
          <div className="gi-desc">{t('graph.inspector.loopBodyHint')}</div>
        </>
      )}

      {node.type === 'start' && (
        <div className="gi-desc">{t('graph.inspector.controlNodeDesc')}</div>
      )}

      {node.type === 'end' && (
        <>
          <div className="gi-desc">{t('graph.inspector.controlNodeDesc')}</div>
          {EndHookFields}
        </>
      )}

      {/* Loop containers leave the freeze fieldset (so FixedCount stays editable),
          so re-apply the freeze to their delete/duplicate actions here. Other
          frozen node types are still covered by the disabled fieldset above. */}
      {!readOnly && !lockStructure && !(frozen && node.type === 'loop') && (canDeleteNode ? canDeleteNode(node) : node.type !== 'start' && node.type !== 'end') && (
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
