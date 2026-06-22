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

export interface GraphListWorkflowsResponse {
  workflows: GraphWorkflow[];
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

export interface GraphRunSnapshot {
  workflowId?: string;
  workflowName?: string;
  config: GraphConfig;
  modelSnapshots?: Record<string, unknown>;
  agentSnapshots?: Record<string, unknown>;
  capturedAt: number;
}

export interface GraphRunVersion {
  version: number;
  config: GraphConfig;
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
  visibleVariables?: Record<string, string>;
  outputVariables?: Record<string, string>;
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
  loopState?: Record<string, GraphLoopState>;
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
  progress?: GraphProgress;
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
  events?: GraphEvent[];
}

export interface GraphRunHistoryResponse {
  runs: GraphRun[];
}
