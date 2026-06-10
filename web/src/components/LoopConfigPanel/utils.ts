import { FlowNode, LoopConfig, RoundMode, Script } from '../../types';

let idCounter = 0;

export function generateId(): string {
  return `fn-${Date.now()}-${++idCounter}-${Math.random().toString(36).slice(2, 6)}`;
}

export function calcTotalSteps(nodes: FlowNode[]): number {
  let total = 0;
  for (const n of nodes) {
    if (n.type === 'step') {
      total += Math.max(1, n.repeatCount || 1);
    } else if (n.type === 'group') {
      const ic = Math.max(1, n.iterationCount || 1);
      total += ic * calcTotalSteps(n.children || []);
    }
  }
  return total;
}

export function countNodes(nodes: FlowNode[]): number {
  let count = 0;
  for (const n of nodes) {
    count++;
    if (n.type === 'group' && n.children) {
      count += countNodes(n.children);
    }
  }
  return count;
}

// ---------------------------------------------------------------------------
// Loop session plan
//
// A Loop run executes leaf steps in a deterministic order (groups expand by
// iterationCount, steps by repeatCount) and the backend opens a new ACP
// session at certain boundaries. We mirror that here so the progress UI can
// show "which session / step within the session" without round-tripping to
// the server.
//
// Session boundary rules (must stay in sync with the backend —
// services/job/executor_step.go + executor_loop.go, and
// docs/feature-2026-05-08-loop-eachrepeat-session-reuse-fix.md):
//   - first executed step           -> always a new session
//   - roundMode 'beforeRound'        -> new session each time the step NODE is
//                                       entered; its repeatCount repeats reuse it
//   - roundMode 'eachRepeat'         -> new session for EVERY repeat
//   - roundMode 'none' (or empty)    -> reuse the previous session (the very
//                                       first executed step still gets one)
//
// Execution order mirrors the backend enumStepPaths walk in
// types/model/flow_node.go.
// ---------------------------------------------------------------------------

export interface LoopSessionLeaf {
  path: number[];
  sessionIndex: number; // 0-based session this leaf belongs to
}

export interface LoopSessionPlan {
  leaves: LoopSessionLeaf[];
  totalSessions: number;
  sessionStepCounts: number[]; // sessionStepCounts[i] = number of steps in session i
}

export interface LoopSessionLocation {
  sessionNumber: number; // 1-based; 0 when not located yet
  totalSessions: number;
  stepInSession: number; // 1-based within the current session; 0 when not located
  stepsInCurrentSession: number;
}

/**
 * Walk the flow in execution order and assign each leaf step to a session.
 * Pure function — no side effects, safe to call on every render.
 *
 * `actualIterations` optionally overrides a group's iterationCount with the
 * number of rounds it actually ran (keyed by the group's dot-joined node path,
 * e.g. "0.0"). Used for conditional groups that stopped early so the session /
 * step denominator reflects the real run instead of the static cap.
 */
export function computeLoopSessionPlan(
  flow: FlowNode[],
  actualIterations?: Record<string, number>
): LoopSessionPlan {
  const leaves: LoopSessionLeaf[] = [];
  // -1 means "no session opened yet". Bumped to a fresh index whenever a
  // boundary rule fires.
  let currentSession = -1;

  const walk = (nodes: FlowNode[], basePath: number[]) => {
    nodes.forEach((node, i) => {
      if (node.type === 'step') {
        const rc = Math.max(1, node.repeatCount || 1);
        const mode: RoundMode = node.roundMode || 'none';
        for (let r = 0; r < rc; r++) {
          const isFirstEver = currentSession < 0;
          const isNodeEntry = r === 0;
          const openNew =
            isFirstEver ||
            mode === 'eachRepeat' ||
            (mode === 'beforeRound' && isNodeEntry);
          if (openNew) currentSession += 1;
          leaves.push({ path: [...basePath, i, r], sessionIndex: currentSession });
        }
      } else if (node.type === 'group') {
        const nodePath = [...basePath, i].join('.');
        const override = actualIterations?.[nodePath];
        const ic =
          override != null && override > 0
            ? override
            : Math.max(1, node.iterationCount || 1);
        for (let iter = 0; iter < ic; iter++) {
          walk(node.children || [], [...basePath, i, iter]);
        }
      }
    });
  };

  walk(flow, []);

  const totalSessions = currentSession < 0 ? 0 : currentSession + 1;
  const sessionStepCounts = new Array(totalSessions).fill(0);
  for (const leaf of leaves) {
    sessionStepCounts[leaf.sessionIndex] += 1;
  }

  return { leaves, totalSessions, sessionStepCounts };
}

function pathsEqual(a: number[], b: number[]): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

/**
 * Locate currentPath within a session plan and derive the 1-based
 * "Session X / Y" and "Step M / N" display values. Returns zeroed positions
 * when the path is not found (e.g. before the loop has started).
 */
export function locateInSessionPlan(
  plan: LoopSessionPlan,
  currentPath?: number[]
): LoopSessionLocation {
  const base: LoopSessionLocation = {
    sessionNumber: 0,
    totalSessions: plan.totalSessions,
    stepInSession: 0,
    stepsInCurrentSession: 0,
  };
  if (!currentPath || currentPath.length === 0) return base;

  const idx = plan.leaves.findIndex((leaf) => pathsEqual(leaf.path, currentPath));
  if (idx < 0) return base;

  const sessionIndex = plan.leaves[idx].sessionIndex;
  // Step position within the session = how many earlier leaves share this
  // session index, plus this one.
  let stepInSession = 0;
  for (let i = 0; i <= idx; i++) {
    if (plan.leaves[i].sessionIndex === sessionIndex) stepInSession += 1;
  }

  return {
    sessionNumber: sessionIndex + 1,
    totalSessions: plan.totalSessions,
    stepInSession,
    stepsInCurrentSession: plan.sessionStepCounts[sessionIndex] || 0,
  };
}

export function isStepValid(node: FlowNode): boolean {
  if (node.type !== 'step') return false;
  const rt = node.roundType || 'prompt';
  if (rt === 'shell') {
    return Boolean(node.scriptId);
  }
  // prompt and evaluator both require a non-empty message.
  return Boolean(node.message?.trim());
}

export function isFlowValid(nodes: FlowNode[]): boolean {
  if (nodes.length === 0) return false;
  for (const n of nodes) {
    if (n.type === 'step') {
      if (!isStepValid(n)) return false;
    } else if (n.type === 'group') {
      if (!n.children || n.children.length === 0 || !isFlowValid(n.children)) return false;
    }
  }
  return true;
}

export function migrateOldConfig(config: LoopConfig): LoopConfig {
  if (config.flow && config.flow.length > 0) return config;
  if (!config.rounds || config.rounds.length === 0) return config;

  const children: FlowNode[] = config.rounds.map((r, i) => ({
    id: generateId(),
    type: 'step' as const,
    label: `Round ${i + 1}`,
    message: r.message,
    repeatCount: r.repeatCount,
    roundMode: r.roundMode,
    roundType: r.roundType,
    scriptId: r.scriptId,
    scriptName: r.scriptName,
  }));

  return {
    flow: [{
      id: generateId(),
      type: 'group',
      label: '',
      iterationCount: Math.max(1, config.iterationCount || 1),
      children,
    }],
    variables: config.variables,
  };
}

export function makeDefaultFlow(): FlowNode[] {
  return [{
    id: generateId(),
    type: 'group',
    label: '',
    iterationCount: 1,
    children: [{
      id: generateId(),
      type: 'step',
      message: '',
      repeatCount: 1,
      roundMode: 'beforeRound' as RoundMode,
      roundType: 'prompt',
    }],
  }];
}

/** Walk all step nodes in the flow tree and collect shell variable names. */
export function collectShellVars(nodes: FlowNode[], scripts?: Script[]): { varName: string; nodeId: string }[] {
  const all: { varName: string; nodeId: string }[] = [];
  const seen = new Set<string>();
  const scriptMap = new Map<string, string>();
  if (scripts) {
    for (const s of scripts) {
      scriptMap.set(s.id, s.content);
    }
  }

  function walk(ns: FlowNode[]) {
    for (const n of ns) {
      if (n.type === 'step' && n.roundType === 'shell') {
        // Resolve content: prefer scripts lookup by scriptId, fall back to message
        const content = (n.scriptId && scriptMap.get(n.scriptId)) || n.message || '';
        if (!content) {
          if (n.children) walk(n.children);
          continue;
        }
        const vars: string[] = [];
        const helperRegex = /quartet_set\s+["'](\w+)["']/g;
        let match;
        while ((match = helperRegex.exec(content)) !== null) {
          if (!vars.includes(match[1])) vars.push(match[1]);
        }
        const ctrlRegex = /echo\s+["'](\w+)=.*["']\s*>>\s*["']?\$QUARTET_CONTROL["']?/g;
        while ((match = ctrlRegex.exec(content)) !== null) {
          if (!vars.includes(match[1])) vars.push(match[1]);
        }
        const legacyRegex = /<<SET_VAR:(\w+)=/g;
        while ((match = legacyRegex.exec(content)) !== null) {
          if (!vars.includes(match[1])) vars.push(match[1]);
        }
        for (const v of vars) {
          if (!seen.has(v)) {
            seen.add(v);
            all.push({ varName: v, nodeId: n.id });
          }
        }
      } else if (n.type === 'group' && n.children) {
        walk(n.children);
      }
    }
  }

  walk(nodes);
  return all;
}

/** Detect shell vars for a specific step node. */
export function detectShellVarsForStep(message: string): string[] {
  const vars: string[] = [];
  let match;
  const helperRegex = /quartet_set\s+["'](\w+)["']/g;
  while ((match = helperRegex.exec(message)) !== null) {
    if (!vars.includes(match[1])) vars.push(match[1]);
  }
  const ctrlRegex = /echo\s+["'](\w+)=.*["']\s*>>\s*["']?\$QUARTET_CONTROL["']?/g;
  while ((match = ctrlRegex.exec(message)) !== null) {
    if (!vars.includes(match[1])) vars.push(match[1]);
  }
  const legacyRegex = /<<SET_VAR:(\w+)=/g;
  while ((match = legacyRegex.exec(message)) !== null) {
    if (!vars.includes(match[1])) vars.push(match[1]);
  }
  return vars;
}

/** Immutable update of a node deep in the tree by id. */
export function updateNodeInFlow(nodes: FlowNode[], nodeId: string, updater: (n: FlowNode) => FlowNode): FlowNode[] {
  return nodes.map((n) => {
    if (n.id === nodeId) return updater(n);
    if (n.type === 'group' && n.children) {
      return { ...n, children: updateNodeInFlow(n.children, nodeId, updater) };
    }
    return n;
  });
}

/** Remove a node from the tree by id. */
export function removeNodeFromFlow(nodes: FlowNode[], nodeId: string): FlowNode[] {
  return nodes
    .filter((n) => n.id !== nodeId)
    .map((n) => {
      if (n.type === 'group' && n.children) {
        return { ...n, children: removeNodeFromFlow(n.children, nodeId) };
      }
      return n;
    })
    .filter((n) => !(n.type === 'group' && (!n.children || n.children.length === 0)));
}

/** Return the id of the first step node encountered in a depth-first walk, or null if there are no steps. */
export function findFirstStepId(nodes: FlowNode[]): string | null {
  for (const n of nodes) {
    if (n.type === 'step') return n.id;
    if (n.type === 'group' && n.children) {
      const id = findFirstStepId(n.children);
      if (id) return id;
    }
  }
  return null;
}

/** Check if a step node id exists anywhere in the flow tree. */
export function hasStepId(nodes: FlowNode[], id: string): boolean {
  for (const n of nodes) {
    if (n.type === 'step' && n.id === id) return true;
    if (n.type === 'group' && n.children && hasStepId(n.children, id)) return true;
  }
  return false;
}

/** Build a short preview string for a collapsed step row. */
export function getStepPreview(node: FlowNode, maxChars = 56): string {
  if (node.type !== 'step') return '';
  const isShell = (node.roundType || 'prompt') === 'shell';
  if (isShell) {
    return node.scriptName || '';
  }
  const msg = (node.message || '').trim();
  if (!msg) return '';
  if (msg.length <= maxChars) return msg;
  return msg.slice(0, maxChars) + '...';
}

/** Add a node as a child of a group, or at root level. */
export function addNodeToGroup(nodes: FlowNode[], parentId: string | null, newNode: FlowNode): FlowNode[] {
  if (parentId === null) {
    return [...nodes, newNode];
  }
  return nodes.map((n) => {
    if (n.id === parentId && n.type === 'group') {
      return { ...n, children: [...(n.children || []), newNode] };
    }
    if (n.type === 'group' && n.children) {
      return { ...n, children: addNodeToGroup(n.children, parentId, newNode) };
    }
    return n;
  });
}

export const MAX_DEPTH = 5;

export const ROUND_MODE_OPTIONS: { value: RoundMode; label: string; desc: string }[] = [
  { value: 'none', label: '不开新会话', desc: '复用已有会话' },
  { value: 'beforeRound', label: 'Round 循环前新建', desc: '本轮开始前创建新会话' },
  { value: 'eachRepeat', label: 'Round 每次循环重复新建', desc: '每次重复都创建新会话' },
];

export const DEPTH_COLORS = ['#555', '#2563eb', '#9333ea', '#dc2626', '#d97706'];
