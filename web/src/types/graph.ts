export type GraphNodeType = 'start' | 'end' | 'shell' | 'prompt' | 'evaluator' | 'ifElse' | 'loop';
export type GraphEdgePort = 'default' | 'yes' | 'no';
export type GraphSessionStrategy = 'new' | 'inherit';
export type GraphLoopMode = 'fixed' | 'until';

export interface GraphWorkflow {
  id: string;
  workspaceId?: string;
  name: string;
  description?: string;
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
  | 'pausing'
  | 'paused'
  | 'stepStopping'
  | 'stepStopped'
  | 'stopped'
  | 'timedOut'
  | 'recovering';

export type GraphInstanceStatus = 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'interrupted';
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
  | 'error';

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
// model.ModelInstance). The frontend does not drive model construction from
// these, so connection details are kept loosely typed; the identifying fields
// are spelled out so snapshot inspection has real shape instead of `unknown`.
export interface GraphModelSnapshot {
  id?: number;
  model_class?: string;
  display_name?: string;
  thinking_type?: string;
  enable_base64_url?: boolean;
  status?: number;
  [key: string]: unknown;
}

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
  modelSnapshots?: Record<string, GraphModelSnapshot>;
  agentSnapshots?: Record<string, GraphAgentSnapshot>;
  capturedAt: number;
}

export interface GraphRunVersion {
  version: number;
  config: GraphConfig;
  modelSnapshots?: Record<string, GraphModelSnapshot>;
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
