export type GraphNodeType = 'start' | 'end' | 'shell' | 'prompt' | 'clarify' | 'ifElse' | 'loop';
export type GraphEdgePort = 'default' | 'yes' | 'no';
export type GraphSessionStrategy = 'new' | 'inherit';
export type GraphLoopMode = 'fixed' | 'until';
// End node hook behavior: 'default' runs the global settings script, 'custom'
// runs the node's own hookScript, 'off' disables it. Empty/undefined = default.
export type GraphEndHookMode = 'default' | 'custom' | 'off';
// Which library a workflow belongs to: 'user' = hand-authored in the Web UI,
// 'agent' = created/managed by a model through the CLI. Legacy workflows with no
// type are treated as 'user' by the backend's read-time normalization.
export type GraphWorkflowType = 'user' | 'agent';

export interface GraphWorkflow {
  id: string;
  workspaceId?: string;
  name: string;
  description?: string;
  type?: GraphWorkflowType;
  config: GraphConfig;
  createdAt: string;
  updatedAt: string;
  deleted?: boolean;
}

export interface GraphWorkflowSummary {
  id: string;
  workspaceId?: string;
  name: string;
  description?: string;
  type?: GraphWorkflowType;
  createdAt: string;
  updatedAt: string;
  nodeCount: number;
  edgeCount: number;
}

export interface GraphConfig {
  nodes: GraphNode[];
  edges: GraphEdge[];
  variables?: Record<string, string>;
  disabledVars?: string[];
  canvas?: GraphCanvasState;
  runConfig?: GraphRunConfig;
  workspaceId?: string;
  workdir?: string;
  sandboxId?: string;
}

export interface GraphNode {
  id: string;
  type: GraphNodeType;
  title?: string;
  parentId?: string;
  config?: GraphNodeConfig;
  layout?: GraphNodeLayout;
  metadata?: Record<string, string>;
}

export interface GraphNodeConfig {
  script?: string;
  prompt?: string;
  agentType?: string;
  modelId?: string;
  acpMode?: string;
  acpThoughtLevel?: string;
  sessionStrategy?: GraphSessionStrategy;
  outputVariables?: string[];
  lastAssistantAlias?: string;
  timeoutSeconds?: number;
  condition?: string;
  loopMode?: GraphLoopMode;
  fixedCount?: number;
  untilCondition?: string;
  maxIterations?: number;
  // Post-completion side-effect shell script. Prompt nodes run it when
  // non-empty; End nodes run it when endHookMode is 'custom'.
  hookScript?: string;
  // End nodes only: which hook to run (default global script / custom / off).
  endHookMode?: GraphEndHookMode;
}

export interface GraphNodeLayout {
  x: number;
  y: number;
  width?: number;
  height?: number;
}

export interface GraphEdge {
  id: string;
  sourceNodeId: string;
  targetNodeId: string;
  sourcePort?: GraphEdgePort;
  targetPort?: string;
  metadata?: Record<string, string>;
}

export interface GraphCanvasState {
  viewport?: GraphCanvasViewport;
}

export interface GraphCanvasViewport {
  x: number;
  y: number;
  zoom: number;
}

export interface GraphRunConfig {
  concurrencyLimit?: number;
  defaultNodeTimeoutSec?: number;
  jobTimeoutSec?: number;
  defaultLoopMaxIters?: number;
  instanceLimit?: number;
  snapshotByteLimit?: number;
}

export interface GraphValidationError {
  type: 'structure' | 'node' | 'edge' | 'variable' | 'config' | 'session';
  message: string;
  nodeId?: string;
  edgeId?: string;
  variable?: string;
  configKey?: string;
}

export interface GraphWorkflowWarning {
  file: string;
  error: string;
}

export interface GraphListWorkflowsResponse {
  workflows: GraphWorkflowSummary[];
  // Workflow files that were skipped during the list because they were
  // unreadable or malformed (mirrors model.GraphWorkflowWarning). Surfaced so
  // the UI can show the offending file + raw error instead of the workflow
  // silently vanishing from the list.
  warnings?: GraphWorkflowWarning[];
}

export interface GraphWorkflowResponse {
  workflow?: GraphWorkflow;
  errors?: GraphValidationError[];
}

export interface GraphValidationResponse {
  valid: boolean;
  errors?: GraphValidationError[];
}

export type GraphRunStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'stepStopping'
  | 'stepStopped'
  | 'stopped'
  | 'timedOut'
  | 'recovering'
  | 'awaitingInput';

export type GraphInstanceStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'interrupted' | 'awaitingInput';
export type GraphEventType =
  | 'instanceStarted'
  | 'instanceCompleted'
  | 'instanceFailed'
  | 'instanceSkipped'
  | 'edgeResolved'
  | 'variableWritten'
  | 'loopIteration'
  | 'progressUpdated'
  | 'agentMessageStart'
  | 'agentMessageDelta'
  | 'agentMessageEnd'
  | 'agentThoughtStart'
  | 'agentThoughtDelta'
  | 'agentThoughtEnd'
  | 'agentToolStart'
  | 'agentToolArgs'
  | 'agentToolResult'
  | 'agentToolEnd'
  | 'agentTokenUsage'
  | 'log'
  | 'error'
  | 'hookCompleted'
  | 'hookFailed';

export interface GraphRun {
  id: string;
  workflowId?: string;
  jobId: string;
  workspaceId?: string;
  status: GraphRunStatus;
  baseSnapshot: GraphRunSnapshot;
  versions?: GraphRunVersion[];
  currentVersion: number;
  progress?: GraphProgress;
  resume?: GraphResumeState;
  startedAt?: number;
  finishedAt?: number;
  createdAt: string;
  updatedAt: string;
  lastError?: GraphRuntimeError;
  // Instances a resume reset removed from the live set but that carried a
  // session; preserved so the Chat sidebar keeps listing prior-attempt
  // conversations. Keyed by instance-key string. Live instances win on overlap.
  archivedInstances?: Record<string, GraphInstanceState>;
}

// Per-node model snapshot captured at run start / version edit (mirrors
// model.GraphRunSnapshot.ModelSnapshots, map[string]string): keyed by the
// node's ModelID, the value is the model's display form — the ACP model
// identifier itself, already display-ready and snapshotted as-is.

// Per-node agent snapshot captured at run start / version edit (mirrors
// model.GraphAgentSnapshot).
export interface GraphAgentSnapshot {
  agentType: string;
  modelId?: string;
  acpMode?: string;
  acpThoughtLevel?: string;
  systemPrompt?: string;
}

export interface GraphRunSnapshot {
  workflowId?: string;
  workflowName?: string;
  config: GraphConfig;
  modelSnapshots?: Record<string, string>;
  agentSnapshots?: Record<string, GraphAgentSnapshot>;
  capturedAt: number;
}

export interface GraphRunVersion {
  version: number;
  config: GraphConfig;
  modelSnapshots?: Record<string, string>;
  agentSnapshots?: Record<string, GraphAgentSnapshot>;
  reason?: string;
  createdAt: number;
  createdBy?: string;
}

export interface GraphInstanceKey {
  nodeId: string;
  iterations?: Array<{ loopNodeId: string; index: number }>;
}

export interface GraphRuntimeError {
  runId?: string;
  instanceKey?: GraphInstanceKey;
  nodeId?: string;
  nodeTitle?: string;
  nodeType?: GraphNodeType;
  message: string;
  retryCount?: number;
  canResume: boolean;
  stdout?: string;
  stderr?: string;
  exitCode?: number;
  modelOutput?: string;
  details?: Record<string, string>;
}

export interface GraphInstanceState {
  key: GraphInstanceKey;
  nodeId: string;
  nodeTitle?: string;
  nodeType: GraphNodeType;
  status: GraphInstanceStatus;
  version: number;
  batchId?: string;
  // sessionId is the instance's §3 lineage (outflow) session. displaySessionId
  // is the session the Chat sidebar lists/opens for this instance: Agent nodes
  // reuse sessionId, Shell nodes carry their own recording session here.
  sessionId?: string;
  displaySessionId?: string;
  // Note: the engine also records per-instance variable snapshots
  // (visibleVariables / outputVariables) on disk for audit, but the run-status
  // API strips them — they are never sent to the client, so no field here.
  startedAt?: number;
  finishedAt?: number;
  durationMs?: number;
  error?: GraphRuntimeError;
  blockedReason?: string;
}

export interface GraphEdgeState {
  edgeId: string;
  sourceInstanceKey: GraphInstanceKey;
  targetInstanceKey: GraphInstanceKey;
  status: 'pending' | 'active' | 'pruned';
  resolvedAt?: number;
  reason?: string;
}

export interface GraphProgress {
  totalCount: number;
  completedCount: number;
  failedCount: number;
  skippedCount: number;
  interruptedCount: number;
  runningCount: number;
  currentKeys?: GraphInstanceKey[];
  instances?: Record<string, GraphInstanceState>;
  lastError?: string;
}

export interface GraphLoopState {
  loopNodeId: string;
  instanceKey: GraphInstanceKey;
  currentIteration: number;
  completed: boolean;
  variables?: Record<string, string>;
  // Session flowing into the current round (the loop scope's roundEntrySession),
  // persisted so resume can rebuild the in-flight round's inflow session.
  entrySession?: string;
}

// §3 会话血缘: how an instance's session was derived (mirrors
// model.GraphSessionLineage).
export interface GraphSessionLineage {
  strategy: GraphSessionStrategy;
  sessionId?: string;
  parentSessionId?: string;
  parentInstanceKey?: GraphInstanceKey;
}

export interface GraphReadyBatchMember {
  key: GraphInstanceKey;
  status: GraphInstanceStatus;
}

export interface GraphReadyBatch {
  id: string;
  version: number;
  members: Record<string, GraphReadyBatchMember>;
  createdAt: number;
}

export interface GraphResumeState {
  readyKeys?: GraphInstanceKey[];
  edgeStates?: Record<string, GraphEdgeState>;
  loopState?: Record<string, GraphLoopState>;
  variablesByKey?: Record<string, Record<string, string>>;
  sessionLineageByKey?: Record<string, GraphSessionLineage>;
  frozenBatch?: GraphReadyBatch;
}

export interface GraphEvent {
  id: string;
  runId: string;
  type: GraphEventType;
  instanceKey?: GraphInstanceKey;
  nodeId?: string;
  edgeId?: string;
  message?: string;
  payload?: Record<string, string>;
  error?: GraphRuntimeError;
  createdAt: number;
}

export interface GraphRunResponse {
  run?: GraphRun;
  errors?: GraphValidationError[];
}

export interface GraphRunStatusResponse {
  run?: GraphRun;
  progress?: GraphProgress;
  instances?: GraphInstanceState[];
  edges?: GraphEdgeState[];
  eventCount?: number;
}

// GraphHookResult is one node hook's execution result (§ 节点 Hook), surfaced in
// the run-view node-detail panel. Source is the hook origin ("prompt" | "end");
// status is "completed" | "failed". nodeTitle/nodeType are carried from the
// hook event so the panel needs no editor node list.
export interface GraphHookResult {
  nodeId: string;
  nodeTitle?: string;
  nodeType?: GraphNodeType;
  source?: string;
  status: 'completed' | 'failed';
  exitCode?: number;
  stdout?: string;
  stderr?: string;
  message?: string;
  finishedAt: number;
}

export interface GraphHookResultsResponse {
  results?: GraphHookResult[];
}
