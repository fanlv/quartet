import { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation, Trans } from 'react-i18next';
import { LoopConfig, LoopTemplate, FlowNode, Script } from '../../types';
import { AgentInfo } from '../ChatPage';
import { FlowStepEditor } from './FlowStepEditor';
import { FlowOutline, FlowIssue } from './FlowOutline';
import {
  generateId, calcTotalSteps, countNodes, isFlowValid, migrateOldConfig,
  makeDefaultFlow, collectShellVars, findFirstStepId,
  updateNodeInFlow, removeNodeFromFlow, MAX_DEPTH,
} from './utils';
import { workspaceColor } from '../../utils/workspace';
import '../LoopConfigPanel.css';

type WorkspaceItem = { id: string; title: string; description: string; workdir: string; color?: string };

interface LoopConfigPanelProps {
  onConfirm: (config: LoopConfig, workdir?: string, workspaceId?: string) => void;
  onCancel: () => void;
  agents: AgentInfo[];
  workspaces?: WorkspaceItem[];
  currentWorkspaceId?: string;
  // --- Edit mode (editing an existing job's LoopConfig) ---
  // When provided, the panel seeds its flow/variables from this config instead
  // of a blank default, and switches the footer action from "Start" to "Save".
  initialConfig?: LoopConfig;
  // runningLock locks structure editing: while a loop job is running, only the
  // per-step fields (prompt / agent / model / mode) may change — adding,
  // removing, reordering nodes and changing repeat / iteration / round settings
  // is disabled (the running flow snapshot must not be restructured). Ignored
  // unless initialConfig is provided.
  runningLock?: boolean;
  // onSave is invoked (instead of onConfirm) when the panel is in edit mode.
  // Returning a rejected promise keeps the panel open and surfaces saveError.
  onSave?: (config: LoopConfig) => Promise<void>;
  // saveError is rendered in the footer when a save attempt fails (e.g. the
  // backend rejects a structure change on a running job).
  saveError?: string;
}

async function fetchTemplates(): Promise<LoopTemplate[]> {
  const res = await fetch('/api/v1/template/list');
  if (!res.ok) return [];
  const data = await res.json();
  return data.templates || [];
}

async function saveTemplateApi(name: string, config: LoopConfig): Promise<LoopTemplate | null> {
  const res = await fetch('/api/v1/template/save', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, config }),
  });
  if (!res.ok) return null;
  const data = await res.json();
  return data.template || null;
}

async function updateTemplateApi(id: string, name: string, config: LoopConfig): Promise<LoopTemplate | null> {
  const res = await fetch(`/api/v1/template/${id}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, config }),
  });
  if (!res.ok) return null;
  const data = await res.json();
  return data.template || null;
}

async function deleteTemplateApi(id: string): Promise<boolean> {
  const res = await fetch(`/api/v1/template/${id}`, { method: 'DELETE' });
  return res.ok;
}

async function fetchScriptsApi(): Promise<Script[]> {
  const res = await fetch('/api/v1/script/list');
  if (!res.ok) return [];
  const data = await res.json();
  return data.scripts || [];
}

interface NodeLocation {
  node: FlowNode;
  parentId: string | null;
  index: number;
  siblingCount: number;
  depth: number;
}

function createStep(): FlowNode {
  return {
    id: generateId(),
    type: 'step',
    message: '',
    repeatCount: 1,
    roundMode: 'none',
    roundType: 'prompt',
  };
}

function createGroup(): FlowNode {
  const firstStep = createStep();
  return {
    id: generateId(),
    type: 'group',
    label: '',
    iterationCount: 1,
    children: [firstStep],
  };
}

// createEvaluator builds an evaluator step: a prompt-style node whose output is
// parsed as a loop stop signal. roundMode defaults to 'none' so it reuses the
// round's current session and can see the business steps' history (§5.3).
function createEvaluator(): FlowNode {
  return {
    id: generateId(),
    type: 'step',
    message: '',
    repeatCount: 1,
    roundMode: 'none',
    roundType: 'evaluator',
  };
}

// groupHasEvaluator reports whether a group directly contains an evaluator step.
function groupHasEvaluator(node: FlowNode): boolean {
  return (node.children || []).some((c) => c.type === 'step' && c.roundType === 'evaluator');
}

// computeEvaluatorWarnings returns non-blocking config guidance (§5.3 / §2.4)
// for an evaluator node, based on its preceding sibling business steps inside
// the same group. Returns [] for non-evaluator nodes or when no warning applies.
function computeEvaluatorWarnings(
  flow: FlowNode[],
  nodeId: string,
  t: (key: string, opts?: Record<string, unknown>) => string
): string[] {
  // Find the parent group's children and the node's index within them.
  let siblings: FlowNode[] | null = null;
  let index = -1;
  const walk = (items: FlowNode[]) => {
    for (let i = 0; i < items.length; i++) {
      const n = items[i];
      if (n.id === nodeId) {
        siblings = items;
        index = i;
        return;
      }
      if (n.type === 'group' && n.children) walk(n.children);
      if (siblings) return;
    }
  };
  walk(flow);
  if (!siblings || index < 0) return [];
  const node = (siblings as FlowNode[])[index];
  if (node.type !== 'step' || node.roundType !== 'evaluator') return [];

  const warnings: string[] = [];
  const priorBusiness = (siblings as FlowNode[])
    .slice(0, index)
    .filter((n) => n.type === 'step' && n.roundType !== 'evaluator');

  // §5.3: a none-evaluator after an eachRepeat/beforeRound business step lands
  // in a fresh empty session and can't see this round's business output.
  if ((node.roundMode || 'none') === 'none') {
    const lastBusiness = priorBusiness[priorBusiness.length - 1];
    const freshMode = lastBusiness && (lastBusiness.roundMode === 'eachRepeat' || lastBusiness.roundMode === 'beforeRound');
    if (freshMode) {
      warnings.push(t('loop.step.evaluator.warnEmptySession'));
    }
  }

  // §2.4: a failing business step (no ContinueOnError) fails the job before the
  // evaluator runs; "fix until tests pass" loops need ContinueOnError.
  const hasUnguardedBusiness = priorBusiness.some((n) => !n.continueOnError);
  if (hasUnguardedBusiness) {
    warnings.push(t('loop.step.evaluator.warnContinueOnError'));
  }

  return warnings;
}

function deepCloneFlowNode(node: FlowNode): FlowNode {
  return {
    ...node,
    id: generateId(),
    children: node.children?.map(deepCloneFlowNode),
  };
}

function getDefaultCollapsedGroupIds(nodes: FlowNode[]): Set<string> {
  const ids = new Set<string>();

  const walk = (items: FlowNode[], depth: number) => {
    const groupsAtLevel = items.filter((node) => node.type === 'group');
    if (depth === 1 && groupsAtLevel.length >= 2) {
      groupsAtLevel.forEach((node) => ids.add(node.id));
    }

    items.forEach((node) => {
      if (node.type === 'group' && node.children?.length) {
        walk(node.children, depth + 1);
      }
    });
  };

  walk(nodes, 0);
  return ids;
}

function mergeDefaultCollapsedGroupIds(prev: Set<string>, nodes: FlowNode[]): Set<string> {
  const defaults = getDefaultCollapsedGroupIds(nodes);
  if (defaults.size === 0) return prev;

  const next = new Set(prev);
  defaults.forEach((id) => next.add(id));
  return next;
}

function findNodeLocation(nodes: FlowNode[], nodeId: string | null, parentId: string | null = null, depth = 0): NodeLocation | null {
  if (!nodeId) return null;
  for (let i = 0; i < nodes.length; i++) {
    const node = nodes[i];
    if (node.id === nodeId) {
      return { node, parentId, index: i, siblingCount: nodes.length, depth };
    }
    if (node.type === 'group' && node.children) {
      const found = findNodeLocation(node.children, nodeId, node.id, depth + 1);
      if (found) return found;
    }
  }
  return null;
}

function findNodeById(nodes: FlowNode[], nodeId: string | null): FlowNode | null {
  return findNodeLocation(nodes, nodeId)?.node || null;
}

function insertNodeAfter(nodes: FlowNode[], targetId: string, newNode: FlowNode): FlowNode[] {
  let inserted = false;
  const walk = (items: FlowNode[]): FlowNode[] => {
    const next: FlowNode[] = [];
    for (const item of items) {
      if (item.type === 'group' && item.children) {
        next.push({ ...item, children: walk(item.children) });
      } else {
        next.push(item);
      }
      if (item.id === targetId) {
        next.push(newNode);
        inserted = true;
      }
    }
    return next;
  };
  const updated = walk(nodes);
  return inserted ? updated : [...nodes, newNode];
}

function moveNodeInFlow(nodes: FlowNode[], nodeId: string, direction: -1 | 1): FlowNode[] {
  const index = nodes.findIndex((node) => node.id === nodeId);
  if (index >= 0) {
    const target = index + direction;
    if (target < 0 || target >= nodes.length) return nodes;
    const next = [...nodes];
    [next[index], next[target]] = [next[target], next[index]];
    return next;
  }
  return nodes.map((node) => {
    if (node.type === 'group' && node.children) {
      return { ...node, children: moveNodeInFlow(node.children, nodeId, direction) };
    }
    return node;
  });
}

function collectFlowIssues(nodes: FlowNode[], t: (key: string, opts?: Record<string, unknown>) => string): FlowIssue[] {
  const issues: FlowIssue[] = [];
  const walk = (items: FlowNode[], depth: number) => {
    for (const node of items) {
      if (node.type === 'step') {
        const roundType = node.roundType || 'prompt';
        if (roundType === 'shell' && !node.scriptId) {
          issues.push({ nodeId: node.id, message: t('loop.validation.scriptRequired') });
        } else if (roundType === 'evaluator') {
          if (depth === 0) {
            issues.push({ nodeId: node.id, message: t('loop.validation.evaluatorInGroup') });
          } else if (!node.message?.trim()) {
            issues.push({ nodeId: node.id, message: t('loop.validation.evaluatorPromptRequired') });
          }
        } else if (roundType !== 'shell' && !node.message?.trim()) {
          issues.push({ nodeId: node.id, message: t('loop.validation.promptRequired') });
        }
      } else if (!node.children || node.children.length === 0) {
        issues.push({ nodeId: node.id, message: t('loop.validation.groupEmpty') });
      } else {
        walk(node.children, depth + 1);
      }
    }
  };
  walk(nodes, 0);
  return issues;
}

function getTemplateStats(tmpl: LoopTemplate) {
  const migrated = migrateOldConfig(tmpl.config);
  const templateFlow = migrated.flow || [];
  const variables = migrated.variables ? Object.keys(migrated.variables).length : 0;
  return {
    nodes: countNodes(templateFlow),
    steps: calcTotalSteps(templateFlow),
    variables,
  };
}

export function LoopConfigPanel({ onConfirm, onCancel, agents, workspaces, currentWorkspaceId, initialConfig, runningLock, onSave, saveError }: LoopConfigPanelProps) {
  const { t, i18n } = useTranslation();
  const isZh = (i18n.resolvedLanguage || i18n.language || 'en').startsWith('zh');
  const varTagExample = isZh ? '{{变量名}}' : '{{variableName}}';
  // Edit mode: seed flow/variables from the existing job config. structureLocked
  // (running job) disables every structure-mutating control; field edits stay live.
  const isEditMode = !!initialConfig || !!onSave;
  const structureLocked = !!runningLock;
  const [initialFlow] = useState<FlowNode[]>(() => {
    if (initialConfig) {
      const migrated = migrateOldConfig(initialConfig);
      if (migrated.flow && migrated.flow.length > 0) return migrated.flow;
    }
    return makeDefaultFlow();
  });
  const [flow, setFlow] = useState<FlowNode[]>(initialFlow);
  const [variables, setVariables] = useState<{ key: string; value: string }[]>(() => {
    const migrated = initialConfig ? migrateOldConfig(initialConfig) : null;
    return migrated?.variables
      ? Object.entries(migrated.variables).map(([key, value]) => ({ key, value }))
      : [];
  });

  const [templates, setTemplates] = useState<LoopTemplate[]>([]);
  const [selectedTemplateId, setSelectedTemplateId] = useState('');
  const [templateDropdownOpen, setTemplateDropdownOpen] = useState(false);
  const [showTemplateLibrary, setShowTemplateLibrary] = useState(false);
  const [templateSearch, setTemplateSearch] = useState('');
  const [showSaveDialog, setShowSaveDialog] = useState(false);
  const [templateName, setTemplateName] = useState('');
  const [saving, setSaving] = useState(false);
  const [showUpdateDialog, setShowUpdateDialog] = useState(false);
  const [updateName, setUpdateName] = useState('');
  const [updating, setUpdating] = useState(false);
  const [updateError, setUpdateError] = useState('');
  const [confirmDeleteId, setConfirmDeleteId] = useState('');
  const [showLeaveConfirm, setShowLeaveConfirm] = useState(false);
  const [scripts, setScripts] = useState<Script[]>([]);
  const [copyToast, setCopyToast] = useState('');
  const [showImportDialog, setShowImportDialog] = useState(false);
  const [importText, setImportText] = useState('');
  const [importError, setImportError] = useState('');

  const [dirty, setDirty] = useState(false);
  const markDirty = useCallback(() => { if (!dirty) setDirty(true); }, [dirty]);
  // Edit-mode save in flight (distinct from the template `saving` flag).
  const [savingConfig, setSavingConfig] = useState(false);

  // Workspace selector
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState<string | undefined>(currentWorkspaceId);
  const [wsDropdownOpen, setWsDropdownOpen] = useState(false);
  const wsDropdownRef = useRef<HTMLDivElement>(null);

  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(() => findFirstStepId(initialFlow) || initialFlow[0]?.id || null);
  const [collapsedGroupIds, setCollapsedGroupIds] = useState<Set<string>>(() => getDefaultCollapsedGroupIds(initialFlow));
  const [moreMenuOpen, setMoreMenuOpen] = useState(false);
  const [variablesPanelOpen, setVariablesPanelOpen] = useState(false);
  const [mobileTab, setMobileTab] = useState<'flow' | 'config' | 'variables'>('flow');
  const [confirmDeleteNodeId, setConfirmDeleteNodeId] = useState('');
  const moreMenuRef = useRef<HTMLDivElement>(null);
  const templateDropdownRef = useRef<HTMLDivElement>(null);

  const loadTemplates = useCallback(async () => {
    const list = await fetchTemplates();
    const sorted = [...list].sort((a, b) => {
      const aSched = (a.scheduleCount ?? 0) > 0 ? 1 : 0;
      const bSched = (b.scheduleCount ?? 0) > 0 ? 1 : 0;
      if (aSched !== bSched) return aSched - bSched;
      return a.name.localeCompare(b.name, undefined, { numeric: true });
    });
    setTemplates(sorted);
  }, []);

  const loadScripts = useCallback(async () => {
    const list = await fetchScriptsApi();
    setScripts(list);
  }, []);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void loadTemplates();
      void loadScripts();
    }, 0);
    return () => window.clearTimeout(timer);
  }, [loadTemplates, loadScripts]);

  // Close More menu on outside click
  useEffect(() => {
    if (!moreMenuOpen) return;
    const handler = (e: MouseEvent) => {
      if (moreMenuRef.current && !moreMenuRef.current.contains(e.target as Node)) {
        setMoreMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [moreMenuOpen]);

  // Close quick template dropdown on outside click
  useEffect(() => {
    if (!templateDropdownOpen) return;
    const handler = (e: MouseEvent) => {
      if (templateDropdownRef.current && !templateDropdownRef.current.contains(e.target as Node)) {
        setTemplateDropdownOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [templateDropdownOpen]);

  // Close workspace dropdown on outside click
  useEffect(() => {
    if (!wsDropdownOpen) return;
    const handler = (e: MouseEvent) => {
      if (wsDropdownRef.current && !wsDropdownRef.current.contains(e.target as Node)) {
        setWsDropdownOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [wsDropdownOpen]);

  // Esc key handling
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      e.preventDefault();
      if (showLeaveConfirm) { setShowLeaveConfirm(false); return; }
      if (showImportDialog) { setShowImportDialog(false); return; }
      if (confirmDeleteNodeId) { setConfirmDeleteNodeId(''); return; }
      if (confirmDeleteId) { setConfirmDeleteId(''); return; }
      if (showUpdateDialog) { setShowUpdateDialog(false); return; }
      if (showSaveDialog) { setShowSaveDialog(false); return; }
      if (showTemplateLibrary) { setShowTemplateLibrary(false); return; }
      if (templateDropdownOpen) { setTemplateDropdownOpen(false); return; }
      if (variablesPanelOpen) { setVariablesPanelOpen(false); return; }
      if (moreMenuOpen) { setMoreMenuOpen(false); return; }
      handleTryCancel();
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showLeaveConfirm, showImportDialog, confirmDeleteNodeId, confirmDeleteId, showUpdateDialog, showSaveDialog, showTemplateLibrary, templateDropdownOpen, variablesPanelOpen, moreMenuOpen, dirty]);

  // Viewport sync for iPad/mobile
  const bodyRef = useRef<HTMLDivElement>(null);
  const overlayRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;
    const sync = () => {
      const overlay = overlayRef.current;
      if (!overlay) return;
      overlay.style.height = vv.height + 'px';
      overlay.style.top = vv.offsetTop + 'px';
      overlay.style.bottom = 'auto';
    };
    const scrollFocusedIntoView = () => {
      const el = document.activeElement;
      if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
        setTimeout(() => {
          el.scrollIntoView({ block: 'center', behavior: 'smooth' });
        }, 150);
      }
    };
    const onResize = () => { sync(); scrollFocusedIntoView(); };
    const onFocusIn = (e: FocusEvent) => {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
        setTimeout(() => {
          (e.target as HTMLElement).scrollIntoView({ block: 'center', behavior: 'smooth' });
        }, 300);
      }
    };
    const overlay = overlayRef.current;
    vv.addEventListener('resize', onResize);
    vv.addEventListener('scroll', sync);
    overlay?.addEventListener('focusin', onFocusIn);
    return () => {
      vv.removeEventListener('resize', onResize);
      vv.removeEventListener('scroll', sync);
      overlay?.removeEventListener('focusin', onFocusIn);
      if (overlay) { overlay.style.height = ''; overlay.style.top = ''; overlay.style.bottom = ''; }
    };
  }, []);

  const buildLoopConfigJson = useCallback(() => {
    const vars: Record<string, string> = {};
    variables.forEach((v) => {
      if (v.key.trim()) vars[v.key.trim()] = v.value;
    });
    const config: LoopConfig = {
      flow,
      ...(Object.keys(vars).length > 0 ? { variables: vars } : {}),
    };
    return config;
  }, [flow, variables]);

  const copyToClipboard = useCallback(async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      document.body.appendChild(textarea);
      textarea.select();
      document.execCommand('copy');
      document.body.removeChild(textarea);
    }
  }, []);

  const handleCopyConfig = useCallback(async () => {
    const config = buildLoopConfigJson();
    const json = JSON.stringify(config, null, 2);
    await copyToClipboard(json);
    setCopyToast(t('loop.toast.copiedConfig'));
    setTimeout(() => setCopyToast(''), 2000);
  }, [buildLoopConfigJson, copyToClipboard, t]);

  const handleImportConfig = useCallback(() => {
    setImportError('');
    try {
      const parsed = JSON.parse(importText.trim());
      // Support both { flow, variables } and { loopConfig: { flow, variables } }
      const config: LoopConfig = parsed.loopConfig || parsed;
      const migrated = migrateOldConfig(config);
      if (!migrated.flow || !Array.isArray(migrated.flow) || migrated.flow.length === 0) {
        setImportError(t('loop.import.errors.missingFlow'));
        return;
      }
      setFlow(migrated.flow);
      setCollapsedGroupIds(getDefaultCollapsedGroupIds(migrated.flow));
      setSelectedNodeId(migrated.flow[0]?.id || findFirstStepId(migrated.flow) || null);
      const vars = migrated.variables
        ? Object.entries(migrated.variables).map(([key, value]) => ({ key, value }))
        : [];
      setVariables(vars);
      setShowImportDialog(false);
      setImportText('');
      setImportError('');
      setSelectedTemplateId('');
      markDirty();
      setCopyToast(t('loop.toast.importedConfig'));
      setTimeout(() => setCopyToast(''), 2000);
    } catch {
      setImportError(t('loop.import.errors.jsonParseFailed'));
    }
  }, [importText, markDirty, t]);

  const handleConfirm = () => {
    if (!isFlowValid(flow)) return;
    const vars: Record<string, string> = {};
    variables.forEach((v) => {
      if (v.key.trim()) vars[v.key.trim()] = v.value;
    });
    const config: LoopConfig = { flow, ...(Object.keys(vars).length > 0 ? { variables: vars } : {}) };
    if (isEditMode && onSave) {
      setSavingConfig(true);
      onSave(config)
        .then(() => { setDirty(false); })
        .finally(() => setSavingConfig(false));
      return;
    }
    const selectedWs = workspaces?.find((w) => w.id === selectedWorkspaceId);
    onConfirm(
      config,
      selectedWs?.workdir,
      selectedWs?.id,
    );
  };

  const handleSelectTemplate = (tmpl: LoopTemplate) => {
    const migrated = migrateOldConfig(tmpl.config);
    const nextFlow = migrated.flow || makeDefaultFlow();
    setFlow(nextFlow);
    setCollapsedGroupIds(getDefaultCollapsedGroupIds(nextFlow));
    setSelectedNodeId(nextFlow[0]?.id || null);
    const vars = migrated.variables
      ? Object.entries(migrated.variables).map(([key, value]) => ({ key, value }))
      : [];
    setVariables(vars);
    setSelectedTemplateId(tmpl.id);
    setShowTemplateLibrary(false);
    setTemplateDropdownOpen(false);
    setTemplateSearch('');
    setDirty(false);
  };

  const handleConfirmDelete = async () => {
    if (!confirmDeleteId) return;
    if (await deleteTemplateApi(confirmDeleteId)) {
      setTemplates((prev) => prev.filter((t) => t.id !== confirmDeleteId));
      if (selectedTemplateId === confirmDeleteId) setSelectedTemplateId('');
    }
    setConfirmDeleteId('');
  };

  const handleReset = () => {
    const nextFlow = makeDefaultFlow();
    setSelectedTemplateId('');
    setFlow(nextFlow);
    setCollapsedGroupIds(getDefaultCollapsedGroupIds(nextFlow));
    setSelectedNodeId(findFirstStepId(nextFlow) || nextFlow[0]?.id || null);
    setVariables([]);
    setTemplateDropdownOpen(false);
    setDirty(false);
  };

  const handleSaveTemplate = async () => {
    if (!templateName.trim()) return;
    setSaving(true);
    const vars: Record<string, string> = {};
    variables.forEach((v) => {
      if (v.key.trim()) vars[v.key.trim()] = v.value;
    });
    const config: LoopConfig = {
      flow,
      ...(Object.keys(vars).length > 0 ? { variables: vars } : {}),
    };
    const tmpl = await saveTemplateApi(templateName.trim(), config);
    if (tmpl) {
      setTemplates((prev) => [tmpl, ...prev]);
      setSelectedTemplateId(tmpl.id);
    }
    setSaving(false);
    setShowSaveDialog(false);
    setTemplateName('');
    setDirty(false);
  };

  const handleOpenUpdateDialog = () => {
    if (!selectedTemplateId) return;
    const current = templates.find((t) => t.id === selectedTemplateId);
    setUpdateName(current ? current.name : '');
    setUpdateError('');
    setShowUpdateDialog(true);
  };

  const handleUpdateTemplate = async () => {
    if (!selectedTemplateId || !updateName.trim()) return;
    setUpdating(true);
    setUpdateError('');
    const vars: Record<string, string> = {};
    variables.forEach((v) => {
      if (v.key.trim()) vars[v.key.trim()] = v.value;
    });
    const config: LoopConfig = {
      flow,
      ...(Object.keys(vars).length > 0 ? { variables: vars } : {}),
    };
    const tmpl = await updateTemplateApi(selectedTemplateId, updateName.trim(), config);
    setUpdating(false);
    if (!tmpl) {
      setUpdateError(t('loop.template.errors.updateFailed'));
      return;
    }
    setTemplates((prev) => prev.map((t) => (t.id === tmpl.id ? tmpl : t)));
    setShowUpdateDialog(false);
    setUpdateName('');
    setDirty(false);
  };

  function handleTryCancel() {
    if (dirty) {
      setShowLeaveConfirm(true);
    } else {
      onCancel();
    }
  }

  const handleAddVariable = () => {
    setVariables([...variables, { key: '', value: '' }]);
    markDirty();
  };
  const handleRemoveVariable = (index: number) => {
    setVariables(variables.filter((_, i) => i !== index));
    markDirty();
  };
  const handleVariableChange = (index: number, field: 'key' | 'value', val: string) => {
    setVariables(variables.map((v, i) => (i === index ? { ...v, [field]: val } : v)));
    markDirty();
  };

  const handleAddStep = (targetGroupId: string | null = null) => {
    const newStep = createStep();
    setFlow((prev) => {
      if (targetGroupId) {
        return updateNodeInFlow(prev, targetGroupId, (node) => ({
          ...node,
          children: [...(node.children || []), newStep],
        }));
      }
      if (selectedNodeId) return insertNodeAfter(prev, selectedNodeId, newStep);
      return [...prev, newStep];
    });
    setSelectedNodeId(newStep.id);
    setMobileTab('config');
    markDirty();
  };

  const handleAddEvaluator = (targetGroupId: string | null = null) => {
    const newEval = createEvaluator();
    setFlow((prev) => {
      if (targetGroupId) {
        return updateNodeInFlow(prev, targetGroupId, (node) => ({
          ...node,
          children: [...(node.children || []), newEval],
        }));
      }
      if (selectedNodeId) return insertNodeAfter(prev, selectedNodeId, newEval);
      return [...prev, newEval];
    });
    setSelectedNodeId(newEval.id);
    setMobileTab('config');
    markDirty();
  };

  const handleAddGroup = (targetGroupId: string | null = null) => {
    const newGroup = createGroup();
    const firstStepId = findFirstStepId([newGroup]);
    setFlow((prev) => {
      let next: FlowNode[];
      if (targetGroupId) {
        next = updateNodeInFlow(prev, targetGroupId, (node) => ({
          ...node,
          children: [...(node.children || []), newGroup],
        }));
      } else if (selectedNodeId) {
        next = insertNodeAfter(prev, selectedNodeId, newGroup);
      } else {
        next = [...prev, newGroup];
      }
      setCollapsedGroupIds((collapsed) => mergeDefaultCollapsedGroupIds(collapsed, next));
      return next;
    });
    setSelectedNodeId(firstStepId || newGroup.id);
    setMobileTab('config');
    markDirty();
  };

  const handleDuplicateNode = (nodeId: string) => {
    const node = findNodeById(flow, nodeId);
    if (!node) return;
    const clone = deepCloneFlowNode(node);
    setFlow((prev) => {
      const next = insertNodeAfter(prev, nodeId, clone);
      setCollapsedGroupIds((collapsed) => mergeDefaultCollapsedGroupIds(collapsed, next));
      return next;
    });
    setSelectedNodeId(clone.type === 'group' ? (findFirstStepId([clone]) || clone.id) : clone.id);
    setMobileTab('config');
    markDirty();
  };

  const handleMoveNode = (nodeId: string, direction: -1 | 1) => {
    setFlow((prev) => moveNodeInFlow(prev, nodeId, direction));
    setSelectedNodeId(nodeId);
    markDirty();
  };

  const handleRequestDeleteNode = (nodeId: string) => {
    setConfirmDeleteNodeId(nodeId);
  };

  const handleConfirmDeleteNode = () => {
    if (!confirmDeleteNodeId) return;
    const updated = removeNodeFromFlow(flow, confirmDeleteNodeId);
    setFlow(updated);
    setSelectedNodeId(findFirstStepId(updated) || updated[0]?.id || null);
    setConfirmDeleteNodeId('');
    markDirty();
  };

  const valid = isFlowValid(flow);
  const selectedTemplate = templates.find((t) => t.id === selectedTemplateId);
  const normalizedTemplateSearch = templateSearch.trim().toLowerCase();
  const filteredTemplates = useMemo(() => {
    if (!normalizedTemplateSearch) return templates;
    return templates.filter((tmpl) => tmpl.name.toLowerCase().includes(normalizedTemplateSearch));
  }, [normalizedTemplateSearch, templates]);
  const scheduledTemplates = useMemo(
    () => filteredTemplates.filter((tmpl) => (tmpl.scheduleCount ?? 0) > 0),
    [filteredTemplates]
  );
  const otherTemplates = useMemo(
    () => filteredTemplates.filter((tmpl) => (tmpl.scheduleCount ?? 0) === 0),
    [filteredTemplates]
  );
  const definedVars = variables.filter((v) => v.key.trim());
  const totalSteps = useMemo(() => calcTotalSteps(flow), [flow]);
  const nodeCount = useMemo(() => countNodes(flow), [flow]);
  const allShellVars = useMemo(() => collectShellVars(flow, scripts), [flow, scripts]);
  const issues = useMemo(() => collectFlowIssues(flow, t), [flow, t]);
  const issuesByNode = useMemo(() => new Map(issues.map((issue) => [issue.nodeId, issue])), [issues]);
  const selectedNode = useMemo(() => findNodeById(flow, selectedNodeId), [flow, selectedNodeId]);
  const selectedLocation = useMemo(() => findNodeLocation(flow, selectedNodeId), [flow, selectedNodeId]);
  // An evaluator must live inside a group. Resolve the target group from the
  // selection: the group itself when a group is selected, or the parent group
  // when a step inside a group is selected. undefined => no valid target, so the
  // "add evaluator" entry is hidden (a top-level break is meaningless).
  const evaluatorTargetGroupId = useMemo<string | null | undefined>(() => {
    if (!selectedNode || !selectedLocation) return undefined;
    if (selectedNode.type === 'group') return selectedNode.id;
    return selectedLocation.parentId ?? undefined;
  }, [selectedNode, selectedLocation]);
  const firstIssue = issues[0];
  const selectedIssue = selectedNodeId ? issuesByNode.get(selectedNodeId) : undefined;
  const formatTemplateDate = useCallback((tmpl: LoopTemplate) => {
    const source = tmpl.updatedAt || tmpl.createdAt;
    if (!source) return '';
    const date = new Date(source);
    if (Number.isNaN(date.getTime())) return '';
    return new Intl.DateTimeFormat(i18n.language || undefined, {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    }).format(date);
  }, [i18n.language]);

  const handleJumpToFirstIssue = () => {
    if (!firstIssue) return;
    setSelectedNodeId(firstIssue.nodeId);
    setMobileTab('config');
  };

  // Save dialog viewport sync
  const saveOverlayRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!showSaveDialog) return;
    const vv = window.visualViewport;
    if (!vv) return;
    const adjust = () => {
      const el = saveOverlayRef.current;
      if (!el) return;
      el.style.top = vv.offsetTop + 'px';
      el.style.height = vv.height + 'px';
      el.style.bottom = 'auto';
    };
    adjust();
    vv.addEventListener('resize', adjust);
    vv.addEventListener('scroll', adjust);
    return () => { vv.removeEventListener('resize', adjust); vv.removeEventListener('scroll', adjust); };
  }, [showSaveDialog]);

  // Update dialog viewport sync
  const updateOverlayRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!showUpdateDialog) return;
    const vv = window.visualViewport;
    if (!vv) return;
    const adjust = () => {
      const el = updateOverlayRef.current;
      if (!el) return;
      el.style.top = vv.offsetTop + 'px';
      el.style.height = vv.height + 'px';
      el.style.bottom = 'auto';
    };
    adjust();
    vv.addEventListener('resize', adjust);
    vv.addEventListener('scroll', adjust);
    return () => { vv.removeEventListener('resize', adjust); vv.removeEventListener('scroll', adjust); };
  }, [showUpdateDialog]);

  const onUpdateTree = useCallback((updater: (nodes: FlowNode[]) => FlowNode[]) => {
    setFlow((prev) => updater(prev));
  }, []);

  const renderTemplateCard = (tmpl: LoopTemplate) => {
    const stats = getTemplateStats(tmpl);
    const isSelected = tmpl.id === selectedTemplateId;
    const isScheduled = (tmpl.scheduleCount ?? 0) > 0;
    const updatedAt = formatTemplateDate(tmpl);
    return (
      <div key={tmpl.id} className={`loop-template-card${isSelected ? ' selected' : ''}`} onClick={() => handleSelectTemplate(tmpl)} style={{ cursor: 'pointer' }}>
        <div className="loop-template-card-main">
          <div className="loop-template-card-title-row">
            <h5>{tmpl.name}</h5>
            {isSelected && <span className="loop-template-selected-badge">{t('loop.template.selectedTag')}</span>}
            {isScheduled && (
              <span
                className="loop-template-badge"
                title={t('loop.template.scheduleBadgeTitle', { count: tmpl.scheduleCount ?? 0 })}
              >
                {t('loop.template.scheduleBadge', { count: tmpl.scheduleCount ?? 0 })}
              </span>
            )}
          </div>
          <div className="loop-template-card-stats">
            <span>{t('loop.footer.nodes', { count: stats.nodes })}</span>
            <span>{t('loop.flow.steps', { count: stats.steps })}</span>
            <span>{t('loop.footer.variables', { count: stats.variables })}</span>
          </div>
          {updatedAt && (
            <div className="loop-template-card-time">
              {t('loop.template.updatedAt', { time: updatedAt })}
            </div>
          )}
        </div>
        <button
          className="loop-template-card-delete"
          onClick={(e) => {
            e.stopPropagation();
            setConfirmDeleteId(tmpl.id);
          }}
          type="button"
          title={t('common.delete')}
        >
          ×
        </button>
      </div>
    );
  };

  return (
    <div className="loop-config-overlay" ref={overlayRef} onClick={handleTryCancel} onTouchMove={(e) => e.preventDefault()} data-testid="loop-config-overlay">
      <div className="loop-config-panel" onClick={(e) => e.stopPropagation()} onTouchMove={(e) => e.stopPropagation()} data-testid="loop-config-panel">
        <div className="loop-config-header" data-testid="loop-config-header">
          <div className="loop-config-header-copy">
            <h3>{isEditMode ? t('loop.panel.editTitle') : t('loop.panel.title')}</h3>
            <span className="loop-config-header-summary">
              {structureLocked ? (
                <span className="loop-config-running-lock" data-testid="loop-config-running-lock">{t('loop.edit.runningLockHint')}</span>
              ) : (
                <>
                  {dirty ? t('loop.state.unsaved') : t('loop.state.clean')}
                  {' · '}
                </>
              )}
              {t('loop.footer.nodes', { count: nodeCount })}
              {' · '}
              {t('loop.flow.steps', { count: totalSteps })}
              {' · '}
              {t('loop.footer.variables', { count: definedVars.length })}
            </span>
          </div>
          <div className="loop-config-header-actions">
            {!isEditMode && workspaces && workspaces.length > 0 && (
              <div className="loop-ws-selector" ref={wsDropdownRef}>
                <button
                  className="loop-ws-trigger"
                  type="button"
                  data-testid="loop-config-workspace-trigger"
                  onClick={() => setWsDropdownOpen((v) => !v)}
                >
                  <span
                    className="loop-ws-dot"
                    style={{ background: workspaceColor(workspaces.find((w) => w.id === selectedWorkspaceId) ?? selectedWorkspaceId) }}
                  />
                  <span className="loop-ws-label">
                    {workspaces.find((w) => w.id === selectedWorkspaceId)?.title || t('loop.workspace.defaultLabel')}
                  </span>
                  <svg className="loop-ws-caret" width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
                    <path d="M2 3.5l3 3 3-3" />
                  </svg>
                </button>
                {wsDropdownOpen && (
                  <div className="loop-ws-dropdown">
                    <div
                      className={`loop-ws-item${!selectedWorkspaceId ? ' active' : ''}`}
                      data-testid="loop-config-workspace-item"
                      data-workspace-id=""
                      onClick={() => { setSelectedWorkspaceId(undefined); setWsDropdownOpen(false); }}
                    >
                      <span className="loop-ws-item-dot" style={{ background: '#cbd5e1' }} />
                      <span className="loop-ws-item-title">{t('loop.workspace.defaultLabel')}</span>
                    </div>
                    {workspaces.map((ws) => (
                      <div
                        key={ws.id}
                        className={`loop-ws-item${selectedWorkspaceId === ws.id ? ' active' : ''}`}
                        data-testid="loop-config-workspace-item"
                        data-workspace-id={ws.id}
                        onClick={() => { setSelectedWorkspaceId(ws.id); setWsDropdownOpen(false); }}
                      >
                        <span className="loop-ws-item-dot" style={{ background: workspaceColor(ws) }} />
                        <span className="loop-ws-item-title">{ws.title || ws.id}</span>
                        <span className="loop-ws-item-path">{ws.workdir}</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
            {!isEditMode && (
              <div className="loop-template-quick-select" ref={templateDropdownRef}>
                <button
                  className={`loop-template-current${templateDropdownOpen ? ' open' : ''}`}
                  type="button"
                  onClick={() => setTemplateDropdownOpen((v) => !v)}
                  aria-expanded={templateDropdownOpen}
                >
                  <span className="loop-template-current-copy">
                    <span className="loop-template-current-label">{t('loop.template.currentLabel')}</span>
                    <span className={selectedTemplate ? 'loop-template-current-name' : 'loop-template-current-placeholder'}>
                      {selectedTemplate ? selectedTemplate.name : t('loop.footer.blank')}
                    </span>
                  </span>
                  <svg className="loop-template-current-caret" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
                    <path d="M3 4.5L6 7.5L9 4.5" />
                  </svg>
                </button>
                {templateDropdownOpen && (
                  <div className="loop-template-quick-dropdown">
                    <button
                      type="button"
                      className={`loop-template-quick-item${!selectedTemplateId ? ' active' : ''}`}
                      onClick={handleReset}
                    >
                      <span className="loop-template-quick-name">{t('loop.footer.blank')}</span>
                      <span className="loop-template-quick-meta">{t('loop.actions.reset')}</span>
                    </button>
                    {templates.length === 0 ? (
                      <div className="loop-template-quick-empty">
                        {t('loop.template.emptyLibraryTitle')}
                      </div>
                    ) : (
                      templates.map((tmpl) => {
                        const stats = getTemplateStats(tmpl);
                        const isSelected = tmpl.id === selectedTemplateId;
                        const isScheduled = (tmpl.scheduleCount ?? 0) > 0;
                        return (
                          <button
                            key={tmpl.id}
                            type="button"
                            className={`loop-template-quick-item${isSelected ? ' active' : ''}`}
                            onClick={() => handleSelectTemplate(tmpl)}
                          >
                            <span className="loop-template-quick-name">{tmpl.name}</span>
                            <span className="loop-template-quick-meta">
                              {t('loop.flow.steps', { count: stats.steps })}
                              {' · '}
                              {t('loop.footer.variables', { count: stats.variables })}
                              {isScheduled ? ` · ${t('loop.template.scheduleBadge', { count: tmpl.scheduleCount ?? 0 })}` : ''}
                            </span>
                          </button>
                        );
                      })
                    )}
                  </div>
                )}
              </div>
            )}
            {!isEditMode && (
              <button
                className="loop-template-library-btn"
                data-testid="loop-config-template-library-button"
                onClick={() => {
                  setShowTemplateLibrary(true);
                  setTemplateSearch('');
                }}
                type="button"
              >
                {t('loop.template.libraryButton')}
              </button>
            )}
            <button className="loop-config-close" onClick={handleTryCancel} type="button" data-testid="loop-config-close-button">
              <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
                <path d="M4 4l8 8M12 4l-8 8" />
              </svg>
            </button>
          </div>
        </div>

        <div className="loop-config-body" ref={bodyRef} data-testid="loop-config-body">
          <div className="loop-mobile-tabs">
            <button className={mobileTab === 'flow' ? 'active' : ''} onClick={() => setMobileTab('flow')} type="button">{t('loop.tabs.flow')}</button>
            <button className={mobileTab === 'config' ? 'active' : ''} onClick={() => setMobileTab('config')} type="button">{t('loop.tabs.config')}</button>
            <button className={mobileTab === 'variables' ? 'active' : ''} onClick={() => setMobileTab('variables')} type="button">{t('loop.tabs.variables')}</button>
          </div>

          <div className={`loop-workbench mobile-${mobileTab}`} data-testid="loop-config-workbench" data-mobile-tab={mobileTab}>
            <section className="loop-flow-pane" data-testid="loop-config-flow-pane">
              <div className="loop-pane-header">
                <div>
                  <h4>{t('loop.flow.title')}</h4>
                  <span>{t('loop.flow.meta', { nodes: nodeCount, steps: totalSteps })}</span>
                </div>
                <div className="loop-pane-actions">
                  {!structureLocked && (
                    <>
                      <button onClick={() => handleAddStep(selectedNode?.type === 'group' ? selectedNode.id : null)} type="button" data-testid="loop-config-add-step-button">{t('loop.actions.addMenuStep')}</button>
                      {evaluatorTargetGroupId !== undefined && (
                        <button onClick={() => handleAddEvaluator(evaluatorTargetGroupId)} type="button" data-testid="loop-config-add-evaluator-button">{t('loop.actions.addMenuEvaluator')}</button>
                      )}
                      {(!selectedNode || selectedNode.type !== 'group' || (selectedLocation?.depth ?? 0) + 1 < MAX_DEPTH) && (
                        <button onClick={() => handleAddGroup(selectedNode?.type === 'group' ? selectedNode.id : null)} type="button" data-testid="loop-config-add-group-button">{t('loop.actions.addMenuGroup')}</button>
                      )}
                    </>
                  )}
                </div>
              </div>
              <FlowOutline
                nodes={flow}
                selectedNodeId={selectedNodeId}
                collapsedGroupIds={collapsedGroupIds}
                issuesByNode={issuesByNode}
                onSelect={(nodeId) => {
                  setSelectedNodeId(nodeId);
                  setMobileTab('config');
                }}
                onToggleGroup={(nodeId) => {
                  setCollapsedGroupIds((prev) => {
                    const next = new Set(prev);
                    if (next.has(nodeId)) next.delete(nodeId);
                    else next.add(nodeId);
                    return next;
                  });
                }}
                onAddStep={handleAddStep}
                onAddGroup={handleAddGroup}
                onAddEvaluator={handleAddEvaluator}
                onDuplicate={handleDuplicateNode}
                onMove={handleMoveNode}
                onDelete={handleRequestDeleteNode}
                onUpdateIterationCount={(nodeId, count) => {
                  onUpdateTree((nodes) => updateNodeInFlow(nodes, nodeId, (node) => ({ ...node, iterationCount: count })));
                  markDirty();
                }}
                onUpdateRepeatCount={(nodeId, count) => {
                  onUpdateTree((nodes) => updateNodeInFlow(nodes, nodeId, (node) => ({ ...node, repeatCount: count })));
                  markDirty();
                }}
                structureLocked={structureLocked}
              />
              {firstIssue && (
                <div className="loop-outline-issue">
                  <span>{firstIssue.message}</span>
                  <button onClick={handleJumpToFirstIssue} type="button">{t('loop.actions.jumpToIssue')}</button>
                </div>
              )}
            </section>

            <section className="loop-inspector-pane" data-testid="loop-config-inspector-pane">
              <div className="loop-pane-header">
                <div>
                  <h4>{selectedNode?.type === 'group' ? t('loop.inspector.groupTitle') : t('loop.inspector.stepTitle')}</h4>
                  <span>
                    {selectedIssue
                      ? selectedIssue.message
                      : t('loop.inspector.ready')}
                  </span>
                </div>
              </div>
              {!selectedNode && (
                <div className="loop-inspector-empty">{t('loop.inspector.empty')}</div>
              )}
              {selectedNode?.type === 'group' && (
                <div className="loop-group-inspector">
                  <label className="loop-inspector-label">{t('loop.inspector.groupName')}</label>
                  <input
                    className="loop-inspector-input"
                    value={selectedNode.label || ''}
                    onChange={(e) => {
                      onUpdateTree((nodes) => updateNodeInFlow(nodes, selectedNode.id, (node) => ({ ...node, label: e.target.value })));
                      markDirty();
                    }}
                    placeholder={selectedLocation?.depth === 0 ? t('loop.group.mainLabel') : t('loop.group.defaultLabel', { index: (selectedLocation?.index ?? 0) + 1 })}
                  />
                  <div className="loop-inspector-stats">
                    <span>{t('loop.footer.nodes', { count: countNodes(selectedNode.children || []) })}</span>
                    <span>{t('loop.flow.steps', { count: calcTotalSteps(selectedNode.children || []) })}</span>
                  </div>
                  {groupHasEvaluator(selectedNode) && (
                    <span className="loop-group-completion-hint">
                      {t('loop.group.evaluatorHint', { max: selectedNode.iterationCount || 1 })}
                    </span>
                  )}
                  {selectedLocation?.depth === 0 && (
                    <div className="loop-group-variables-section">
                      <label className="loop-inspector-label">{t('loop.variables.label')}</label>
                      <span className="loop-group-variables-hint">
                        {variables.length === 0 ? (
                          <Trans
                            i18nKey="loop.variables.emptyHint"
                            values={{ varTag: varTagExample }}
                            components={[<code />]}
                          />
                        ) : (
                          <Trans
                            i18nKey="loop.variables.countHint"
                            count={variables.length}
                            values={{ count: variables.length, varTag: varTagExample }}
                            components={[<code />]}
                          />
                        )}
                      </span>
                      <div className="loop-variable-list">
                        {variables.map((v, idx) => (
                          <div key={idx} className="loop-variable-row">
                            <input className="loop-variable-key" type="text" value={v.key} onChange={(e) => handleVariableChange(idx, 'key', e.target.value)} placeholder={t('loop.variables.keyPlaceholder')} />
                            <input className="loop-variable-value" type="text" value={v.value} onChange={(e) => handleVariableChange(idx, 'value', e.target.value)} placeholder={t('loop.variables.valuePlaceholder')} />
                            <button className="loop-variable-remove" onClick={() => handleRemoveVariable(idx)} type="button">×</button>
                          </div>
                        ))}
                      </div>
                      <button className="loop-variable-add-wide" onClick={handleAddVariable} type="button">{t('loop.actions.addVariable')}</button>
                    </div>
                  )}
                </div>
              )}
              {selectedNode?.type === 'step' && selectedLocation && (
                <div className="loop-step-inspector">
                  <FlowStepEditor
                    node={selectedNode}
                    stepIndex={selectedLocation.index}
                    isFirstStep={findFirstStepId(flow) === selectedNode.id}
                    canRemove={false}
                    depth={selectedLocation.depth}
                    scripts={scripts}
                    definedVars={definedVars}
                    allShellVars={allShellVars}
                    agents={agents}
                    warnings={selectedNode.roundType === 'evaluator' ? computeEvaluatorWarnings(flow, selectedNode.id, t) : undefined}
                    isExpanded
                    onExpandedChange={() => undefined}
                    onUpdate={(updated) => {
                      onUpdateTree((nodes) => updateNodeInFlow(nodes, selectedNode.id, () => updated));
                      markDirty();
                    }}
                    onRemove={() => handleRequestDeleteNode(selectedNode.id)}
                    structureLocked={structureLocked}
                  />
                </div>
              )}
            </section>

            <section className={`loop-variables-pane${variablesPanelOpen ? ' open' : ''}`} data-testid="loop-config-variables-pane" data-open={variablesPanelOpen ? 'true' : 'false'}>
              <div className="loop-pane-header">
                <div>
                  <h4>{t('loop.variables.label')}</h4>
                  <span>
                    {variables.length === 0 ? (
                      <Trans
                        i18nKey="loop.variables.emptyHint"
                        values={{ varTag: varTagExample }}
                        components={[<code />]}
                      />
                    ) : (
                      <Trans
                        i18nKey="loop.variables.countHint"
                        count={variables.length}
                        values={{ count: variables.length, varTag: varTagExample }}
                        components={[<code />]}
                      />
                    )}
                  </span>
                </div>
                <button onClick={() => { setVariablesPanelOpen(false); setMobileTab('config'); }} type="button">{t('common.close')}</button>
              </div>
              <div className="loop-variable-list">
                {variables.map((v, idx) => (
                  <div key={idx} className="loop-variable-row">
                    <input className="loop-variable-key" type="text" value={v.key} onChange={(e) => handleVariableChange(idx, 'key', e.target.value)} placeholder={t('loop.variables.keyPlaceholder')} />
                    <input className="loop-variable-value" type="text" value={v.value} onChange={(e) => handleVariableChange(idx, 'value', e.target.value)} placeholder={t('loop.variables.valuePlaceholder')} />
                    <button className="loop-variable-remove" onClick={() => handleRemoveVariable(idx)} type="button">×</button>
                  </div>
                ))}
              </div>
              <button className="loop-variable-add-wide" onClick={handleAddVariable} type="button">{t('loop.actions.addVariable')}</button>
            </section>
          </div>
        </div>

        <div className="loop-config-footer" data-testid="loop-config-footer">
          <div className="loop-config-footer-copy">
            <span className="loop-config-footer-meta">
              {selectedTemplate
                ? t('loop.footer.template', { name: selectedTemplate.name })
                : t('loop.footer.blank')}
              {' · '}
              {t('loop.footer.nodes', { count: nodeCount })}
              {' · '}
              {t('loop.footer.variables', { count: definedVars.length })}
              {firstIssue && (
                <>
                  {' · '}
                  <button className="loop-footer-issue-btn" onClick={handleJumpToFirstIssue} type="button">
                    {t('loop.footer.firstIssue', { message: firstIssue.message })}
                  </button>
                </>
              )}
            </span>
            {saveError && (
              <span className="loop-config-footer-error" data-testid="loop-config-save-error">{saveError}</span>
            )}
          </div>
          <div className="loop-config-footer-actions">
            <button className="loop-secondary-btn" onClick={handleCopyConfig} disabled={!valid} type="button" title={t('loop.actions.copyConfig')}>
              <svg className="loop-btn-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
              </svg>
              <span className="loop-btn-label">{t('loop.actions.copyConfig')}</span>
            </button>
            {!isEditMode && (
              <button className="loop-secondary-btn" onClick={() => { setShowImportDialog(true); setImportText(''); setImportError(''); }} type="button" title={t('loop.actions.importConfig')}>
                <svg className="loop-btn-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
                  <polyline points="7 10 12 15 17 10"/>
                  <line x1="12" y1="15" x2="12" y2="3"/>
                </svg>
                <span className="loop-btn-label">{t('loop.actions.importConfig')}</span>
              </button>
            )}
            <button
              type="button"
              className="loop-secondary-btn"
              onClick={() => setShowSaveDialog(true)}
              disabled={!valid}
              title={t('loop.actions.saveAsTemplate')}
            >
              <svg className="loop-btn-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/>
                <polyline points="17 21 17 13 7 13 7 21"/>
                <polyline points="7 3 7 8 15 8"/>
              </svg>
              <span className="loop-btn-label">{t('loop.actions.saveAsTemplate')}</span>
            </button>
            {selectedTemplateId && (
              <button
                type="button"
                className="loop-secondary-btn"
                onClick={handleOpenUpdateDialog}
                disabled={!valid}
                title={t('loop.actions.updateTemplateTitle')}
              >
                <svg className="loop-btn-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <polyline points="23 4 23 10 17 10"/>
                  <path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>
                </svg>
                <span className="loop-btn-label">{t('loop.actions.updateTemplate')}</span>
              </button>
            )}
            <button className="loop-secondary-btn" onClick={handleReset} type="button" title={t('loop.actions.reset')}>
              <svg className="loop-btn-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="M3 6h18"/>
                <path d="M8 6V4h8v2"/>
                <path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/>
                <line x1="10" y1="11" x2="10" y2="17"/>
                <line x1="14" y1="11" x2="14" y2="17"/>
              </svg>
              <span className="loop-btn-label">{t('loop.actions.reset')}</span>
            </button>
            <button
              type="button"
              className="loop-start-btn"
              onClick={handleConfirm}
              disabled={!valid || savingConfig}
              title={isEditMode ? t('loop.actions.save') : t('loop.actions.start')}
              aria-label={isEditMode ? t('loop.actions.save') : t('loop.actions.start')}
              data-testid={isEditMode ? 'loop-config-save-button' : 'loop-config-start-button'}
            >
              {isEditMode ? (
                <svg className="loop-btn-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/>
                  <polyline points="17 21 17 13 7 13 7 21"/>
                  <polyline points="7 3 7 8 15 8"/>
                </svg>
              ) : (
                <svg className="loop-btn-icon" viewBox="0 0 24 24" fill="currentColor">
                  <path d="M8 5v14l11-7z"/>
                </svg>
              )}
              <span className="loop-btn-label">{isEditMode ? (savingConfig ? t('loop.actions.saving') : t('loop.actions.save')) : t('loop.actions.start')}</span>
            </button>
          </div>
        </div>
      </div>

      {/* Copy toast */}
      {copyToast && <div className="loop-copy-toast">{copyToast}</div>}

      {/* Template library */}
      {showTemplateLibrary && createPortal(
        <div className="loop-template-library-overlay" onClick={() => setShowTemplateLibrary(false)}>
          <div className="loop-template-library" onClick={(e) => e.stopPropagation()}>
            <div className="loop-template-library-header">
              <div>
                <h4>{t('loop.template.libraryTitle')}</h4>
                <p>{t('loop.template.librarySubtitle')}</p>
              </div>
              <button className="loop-template-library-close" onClick={() => setShowTemplateLibrary(false)} type="button">
                {t('common.close')}
              </button>
            </div>

            <div className="loop-template-library-toolbar">
              <input
                className="loop-template-search"
                value={templateSearch}
                onChange={(e) => setTemplateSearch(e.target.value)}
                placeholder={t('loop.template.searchPlaceholder')}
              />
              <button
                onClick={() => {
                  setShowTemplateLibrary(false);
                  setShowImportDialog(true);
                  setImportText('');
                  setImportError('');
                }}
                type="button"
              >
                {t('loop.actions.importConfig')}
              </button>
              <button
                onClick={() => {
                  setShowTemplateLibrary(false);
                  setShowSaveDialog(true);
                }}
                disabled={!valid}
                type="button"
              >
                {t('loop.actions.saveAsTemplate')}
              </button>
            </div>

            {dirty && (
              <div className="loop-template-library-notice">
                {t('loop.template.unsavedNotice')}
              </div>
            )}

            <div className="loop-template-library-content">
              {templates.length === 0 ? (
                <div className="loop-template-library-empty">
                  <h5>{t('loop.template.emptyLibraryTitle')}</h5>
                  <p>{t('loop.template.emptyLibraryDesc')}</p>
                  <button
                    onClick={() => {
                      setShowTemplateLibrary(false);
                      setShowSaveDialog(true);
                    }}
                    disabled={!valid}
                    type="button"
                  >
                    {t('loop.actions.saveAsTemplate')}
                  </button>
                </div>
              ) : filteredTemplates.length === 0 ? (
                <div className="loop-template-library-empty">
                  <h5>{t('loop.template.emptySearchTitle')}</h5>
                  <p>{t('loop.template.emptySearchDesc')}</p>
                </div>
              ) : (
                <>
                  {otherTemplates.length > 0 && (
                    <section className="loop-template-library-section">
                      <div className="loop-template-library-section-title">{t('loop.template.categoryOther')}</div>
                      <div className="loop-template-card-list">{otherTemplates.map(renderTemplateCard)}</div>
                    </section>
                  )}
                  {scheduledTemplates.length > 0 && (
                    <section className="loop-template-library-section">
                      <div className="loop-template-library-section-title">{t('loop.template.categoryScheduled')}</div>
                      <div className="loop-template-card-list">{scheduledTemplates.map(renderTemplateCard)}</div>
                    </section>
                  )}
                </>
              )}
            </div>

            <div className="loop-template-library-footer">
              <button
                onClick={() => {
                  handleReset();
                  setShowTemplateLibrary(false);
                }}
                type="button"
              >
                {t('loop.actions.reset')}
              </button>
              <button className="loop-save-dialog-confirm" onClick={() => setShowTemplateLibrary(false)} type="button">
                {t('common.close')}
              </button>
            </div>
          </div>
        </div>,
        document.body
      )}

      {/* Save dialog */}
      {showSaveDialog && createPortal(
        <div className="loop-save-dialog-overlay" ref={saveOverlayRef} onClick={() => setShowSaveDialog(false)}>
          <div className="loop-save-dialog" onClick={(e) => e.stopPropagation()}>
            <h4>{t('loop.dialog.save.title')}</h4>
            <input type="text" className="loop-save-dialog-input" placeholder={t('loop.dialog.save.placeholder')} value={templateName} onChange={(e) => setTemplateName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && handleSaveTemplate()} autoFocus />
            <div className="loop-save-dialog-actions">
              <button onClick={() => setShowSaveDialog(false)} type="button">{t('common.cancel')}</button>
              <button className="loop-save-dialog-confirm" onClick={handleSaveTemplate} disabled={!templateName.trim() || saving} type="button">{saving ? t('common.saving') : t('common.save')}</button>
            </div>
          </div>
        </div>,
        document.body
      )}

      {/* Update dialog */}
      {showUpdateDialog && createPortal(
        <div className="loop-save-dialog-overlay" ref={updateOverlayRef} onClick={() => setShowUpdateDialog(false)}>
          <div className="loop-save-dialog" onClick={(e) => e.stopPropagation()}>
            <h4>{t('loop.dialog.update.title')}</h4>
            <p className="loop-confirm-delete-text">{t('loop.dialog.update.desc', { name: templates.find((t) => t.id === selectedTemplateId)?.name ?? '' })}</p>
            <input type="text" className="loop-save-dialog-input" placeholder={t('loop.dialog.update.placeholder')} value={updateName} onChange={(e) => { setUpdateName(e.target.value); setUpdateError(''); }} onKeyDown={(e) => e.key === 'Enter' && handleUpdateTemplate()} autoFocus />
            {updateError && <p className="loop-import-error">{updateError}</p>}
            <div className="loop-save-dialog-actions">
              <button onClick={() => setShowUpdateDialog(false)} type="button">{t('common.cancel')}</button>
              <button className="loop-save-dialog-confirm" onClick={handleUpdateTemplate} disabled={!updateName.trim() || updating} type="button">{updating ? t('loop.dialog.update.updating') : t('loop.dialog.update.confirm')}</button>
            </div>
          </div>
        </div>,
        document.body
      )}

      {/* Delete confirm */}
      {confirmDeleteId && (
        <div className="loop-save-dialog-overlay" onClick={() => setConfirmDeleteId('')}>
          <div className="loop-save-dialog" onClick={(e) => e.stopPropagation()}>
            <h4>{t('loop.dialog.delete.title')}</h4>
            <p className="loop-confirm-delete-text">{t('loop.dialog.delete.desc', { name: templates.find((t) => t.id === confirmDeleteId)?.name ?? '' })}</p>
            <div className="loop-save-dialog-actions">
              <button onClick={() => setConfirmDeleteId('')} type="button">{t('common.cancel')}</button>
              <button className="loop-confirm-delete-btn" onClick={handleConfirmDelete} type="button">{t('common.delete')}</button>
            </div>
          </div>
        </div>
      )}

      {/* Node delete confirm */}
      {confirmDeleteNodeId && (
        <div className="loop-save-dialog-overlay" onClick={() => setConfirmDeleteNodeId('')}>
          <div className="loop-save-dialog" onClick={(e) => e.stopPropagation()}>
            <h4>{t('loop.dialog.deleteNode.title')}</h4>
            <p className="loop-confirm-delete-text">{t('loop.dialog.deleteNode.desc')}</p>
            <div className="loop-save-dialog-actions">
              <button onClick={() => setConfirmDeleteNodeId('')} type="button">{t('common.cancel')}</button>
              <button className="loop-confirm-delete-btn" onClick={handleConfirmDeleteNode} type="button">{t('common.delete')}</button>
            </div>
          </div>
        </div>
      )}

      {/* Leave confirm */}
      {showLeaveConfirm && (
        <div className="loop-save-dialog-overlay" onClick={() => setShowLeaveConfirm(false)}>
          <div className="loop-save-dialog" onClick={(e) => e.stopPropagation()}>
            <h4>{t('loop.dialog.leave.title')}</h4>
            <p className="loop-confirm-delete-text">{t('loop.dialog.leave.desc')}</p>
            <div className="loop-save-dialog-actions">
              <button onClick={() => setShowLeaveConfirm(false)} type="button">{t('loop.dialog.leave.stay')}</button>
              <button className="loop-confirm-delete-btn" onClick={onCancel} type="button">{t('loop.dialog.leave.confirm')}</button>
            </div>
          </div>
        </div>
      )}

      {/* Import config dialog */}
      {showImportDialog && createPortal(
        <div className="loop-save-dialog-overlay" onClick={() => setShowImportDialog(false)}>
          <div className="loop-import-dialog" onClick={(e) => e.stopPropagation()}>
            <h4>{t('loop.dialog.import.title')}</h4>
            <p className="loop-import-hint">{t('loop.dialog.import.hint')}</p>
            <textarea
              className="loop-import-textarea"
              placeholder='{"flow": [...], "variables": {...}}'
              value={importText}
              onChange={(e) => { setImportText(e.target.value); setImportError(''); }}
              autoFocus
              spellCheck={false}
            />
            {importError && <p className="loop-import-error">{importError}</p>}
            <div className="loop-save-dialog-actions">
              <button onClick={() => setShowImportDialog(false)} type="button">{t('common.cancel')}</button>
              <button className="loop-save-dialog-confirm" onClick={handleImportConfig} disabled={!importText.trim()} type="button">{t('loop.dialog.import.confirm')}</button>
            </div>
          </div>
        </div>,
        document.body
      )}
    </div>
  );
}
