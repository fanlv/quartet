import { FlowNode, RoundMode } from '../types';

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
//   - roundMode 'eachRepeat'         -> new session for EVERY repeat AND the
//                                       session is dropped after the step runs,
//                                       so a following 'none' step lands in a
//                                       fresh session (backend resetSessionAfterStep)
//   - roundMode 'none' (or empty)    -> reuse the live session if one is still
//                                       open; the first executed step (and a
//                                       step right after an eachRepeat) gets a
//                                       new one because none is open
//
// The backend tracks a single currentSessionID across steps. eachRepeat clears
// it after the step runs (resetSessionAfterStep); the next step then sees an
// empty session and spawns a fresh one. We mirror that with `sessionLive`
// below so the UI session/step grouping matches the real run. (The other
// resetSessionAfterStep trigger, "next step starts a fresh session", has no
// net effect here: that next step would open its own session anyway.)
//
// Execution order mirrors the backend enumStepPaths walk in
// types/model/flow_node.go.
//
// Loop mode is retired: this module now serves only the read-only archive view
// (LoopProgress) of historical loop jobs.
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

interface LoopLeafPath {
  path: number[];
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
 * e.g. "0.0"). `actualLeafCounts` additionally trims siblings skipped after
 * STOP within the final actual iteration; its value is the group's CONSUMED
 * slot prefix (executed + empty-prompt-skipped). `skippedPaths` then filters
 * out the leaves whose rendered prompt was empty and never ran (keyed by the
 * dot-joined FULL leaf path, iteration/repeat indices included). Order matters
 * and mirrors the backend (§2.4): static expansion → group prefix trim →
 * skipped-leaf filter → session assignment. Together they make the session /
 * step denominator reflect the backend's real executed plan instead of the
 * static cap.
 */
export function computeLoopSessionPlan(
  flow: FlowNode[],
  actualIterations?: Record<string, number>,
  actualLeafCounts?: Record<string, number>,
  skippedPaths?: Record<string, boolean>
): LoopSessionPlan {
  const paths: LoopLeafPath[] = [];

  const walk = (nodes: FlowNode[], basePath: number[]) => {
    nodes.forEach((node, i) => {
      if (node.type === 'step') {
        const rc = Math.max(1, node.repeatCount || 1);
        for (let r = 0; r < rc; r++) {
          paths.push({ path: [...basePath, i, r] });
        }
      } else if (node.type === 'group') {
        const nodePath = [...basePath, i].join('.');
        const override = actualIterations?.[nodePath];
        const ic =
          override != null && override > 0
            ? override
            : Math.max(1, node.iterationCount || 1);
        const start = paths.length;
        for (let iter = 0; iter < ic; iter++) {
          walk(node.children || [], [...basePath, i, iter]);
        }
        // Actual iteration counts trim future iterations. Leaf counts additionally
        // trim sibling steps skipped after STOP within the final actual iteration,
        // matching the backend progress denominator backfill. The kept prefix is
        // the group's consumed slots — executed AND empty-prompt-skipped — so a
        // skipped leaf inside it survives the trim and is removed by the
        // skipped-path filter below instead.
        const actualLeaves = actualLeafCounts?.[nodePath];
        if (actualLeaves != null && actualLeaves >= 0) {
          paths.splice(start + actualLeaves);
        }
      }
    });
  };

  walk(flow, []);

  // Filter empty-prompt-skipped leaves AFTER group trimming and BEFORE session
  // assignment: a skipped leaf never ran, so it must not occupy a session slot
  // nor open a session — a session containing only skipped leaves disappears
  // entirely, matching the backend (which neither consumes nor resets the
  // session pointer on skip).
  const kept = skippedPaths
    ? paths.filter((leaf) => !skippedPaths[leaf.path.join('.')])
    : paths;

  // Assign sessions after any STOP-based trimming. This avoids skipped steps
  // affecting the reusable-session state of later siblings.
  const leaves: LoopSessionLeaf[] = [];
  let currentSession = -1;
  let sessionLive = false;
  const entrySeen = new Set<string>();
  for (const leaf of kept) {
    const node = findStepNode(flow, leaf.path);
    const mode: RoundMode = node?.roundMode || 'none';
    const stepEntryKey = leaf.path.slice(0, -1).join('.');
    const isNodeEntry = !entrySeen.has(stepEntryKey);
    entrySeen.add(stepEntryKey);
    const openNew =
      !sessionLive ||
      mode === 'eachRepeat' ||
      (mode === 'beforeRound' && isNodeEntry);
    if (openNew) currentSession += 1;
    leaves.push({ path: leaf.path, sessionIndex: currentSession });
    sessionLive = mode !== 'eachRepeat';
  }

  const totalSessions = currentSession < 0 ? 0 : currentSession + 1;
  const sessionStepCounts = new Array(totalSessions).fill(0);
  for (const leaf of leaves) {
    sessionStepCounts[leaf.sessionIndex] += 1;
  }

  return { leaves, totalSessions, sessionStepCounts };
}

function findStepNode(flow: FlowNode[], path: number[]): FlowNode | undefined {
  let nodes = flow;
  let node: FlowNode | undefined;
  for (let idx = 0; idx < path.length;) {
    node = nodes[path[idx]];
    if (!node) return undefined;
    if (node.type === 'step') return node;
    // Group paths are encoded as [groupIndex, iteration, ...children]. The
    // iteration component is not needed for locating the static child node.
    nodes = node.children || [];
    idx += 2;
  }
  return node?.type === 'step' ? node : undefined;
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
