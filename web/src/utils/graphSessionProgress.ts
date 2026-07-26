import type {
  GraphConfig,
  GraphInstanceState,
  GraphNode,
  GraphRunStatus,
} from '../types';

export interface GraphSessionProgress {
  sessionNumber: number;
  totalSessions: number;
  stepNumber: number;
}

function displaySessionId(instance: GraphInstanceState): string {
  return instance.displaySessionId || instance.sessionId || '';
}

function instanceKeyString(instance: GraphInstanceState): string {
  const iterations = instance.key?.iterations || [];
  const prefix = iterations.map((iteration) => `${iteration.loopNodeId}#${iteration.index}`);
  prefix.push(instance.nodeId);
  return prefix.join('/');
}

function compareByStart(a: GraphInstanceState, b: GraphInstanceState): number {
  const startedDiff = (a.startedAt ?? Number.POSITIVE_INFINITY) - (b.startedAt ?? Number.POSITIVE_INFINITY);
  if (startedDiff !== 0) return startedDiff;
  return instanceKeyString(a).localeCompare(instanceKeyString(b));
}

function isSessionStep(instance: GraphInstanceState): boolean {
  return (
    (instance.nodeType === 'prompt' || instance.nodeType === 'clarify' || instance.nodeType === 'shell') &&
    !!displaySessionId(instance)
  );
}

function opensSession(node: GraphNode): boolean {
  if (node.type === 'shell') return true;
  if (node.type !== 'prompt' && node.type !== 'clarify') return false;
  return node.config?.sessionStrategy !== 'inherit';
}

function loopMaxRounds(config: GraphConfig, loop: GraphNode): number {
  if (loop.config?.loopMode === 'fixed') {
    return Math.max(0, loop.config.fixedCount ?? 0);
  }
  const nodeMax = loop.config?.maxIterations ?? 0;
  if (nodeMax > 0) return nodeMax;
  const defaultMax = config.runConfig?.defaultLoopMaxIters ?? 0;
  if (defaultMax > 0) return defaultMax;
  return 100;
}

function maxNodeInstances(config: GraphConfig, nodesById: Map<string, GraphNode>, node: GraphNode): number {
  let total = 1;
  let parentId = node.parentId;
  while (parentId) {
    const parent = nodesById.get(parentId);
    if (!parent) break;
    if (parent.type === 'loop') total *= loopMaxRounds(config, parent);
    parentId = parent.parentId;
  }
  return total;
}

function plannedSessionCount(config: GraphConfig, instances: GraphInstanceState[]): number {
  const nodesById = new Map(config.nodes.map((node) => [node.id, node]));
  let total = 0;
  for (const node of config.nodes) {
    if (opensSession(node)) total += maxNodeInstances(config, nodesById, node);
  }

  // A pruned session-opening instance never creates a real session. Remove it
  // from the static upper bound as soon as the scheduler makes that decision.
  for (const instance of instances) {
    if (instance.status !== 'skipped') continue;
    const node = nodesById.get(instance.nodeId);
    if (node && opensSession(node)) total -= 1;
  }
  return Math.max(0, total);
}

/**
 * Derive the compact Graph progress summary from the same session-bearing
 * instances shown in the Chat sidebar.
 *
 * Session numbers follow first-start order and group inherited Agent steps by
 * their shared session id. Step is the current round within that session.
 */
export function locateGraphSessionProgress(
  config: GraphConfig,
  instances: GraphInstanceState[],
  status?: GraphRunStatus,
): GraphSessionProgress | null {
  const ordered = instances.filter(isSessionStep).sort(compareByStart);
  if (ordered.length === 0) return null;

  const sessionIds: string[] = [];
  const seenSessionIds = new Set<string>();
  for (const instance of ordered) {
    const sessionId = displaySessionId(instance);
    if (!seenSessionIds.has(sessionId)) {
      seenSessionIds.add(sessionId);
      sessionIds.push(sessionId);
    }
  }

  const active =
    ordered.filter((instance) => instance.status === 'running' || instance.status === 'awaitingInput').at(-1) ??
    ordered.at(-1);
  if (!active) return null;

  const activeSessionId = displaySessionId(active);
  const activeIndex = ordered.indexOf(active);
  let stepNumber = 0;
  for (let index = 0; index <= activeIndex; index += 1) {
    if (displaySessionId(ordered[index]) === activeSessionId) stepNumber += 1;
  }

  const actualSessions = sessionIds.length;
  const plannedSessions = plannedSessionCount(config, instances);
  // Natural completion knows the exact branch / loop outcome, so its recorded
  // sessions are the truthful denominator. In-flight and resumable runs retain
  // the remaining static plan, while never allowing the total to fall behind
  // sessions that have already started.
  const totalSessions =
    status === 'completed'
      ? actualSessions
      : Math.max(actualSessions, plannedSessions);

  return {
    sessionNumber: sessionIds.indexOf(activeSessionId) + 1,
    totalSessions,
    stepNumber,
  };
}
