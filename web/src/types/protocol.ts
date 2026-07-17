export enum EventTypeEnum {
  TEXT_MESSAGE_START = 'TEXT_MESSAGE_START',
  TEXT_MESSAGE_CONTENT = 'TEXT_MESSAGE_CONTENT',
  TEXT_MESSAGE_END = 'TEXT_MESSAGE_END',
  TOOL_CALL_START = 'TOOL_CALL_START',
  TOOL_CALL_ARGS = 'TOOL_CALL_ARGS',
  TOOL_CALL_RESULT = 'TOOL_CALL_RESULT',
  TOOL_CALL_END = 'TOOL_CALL_END',
  // TOOL_CALL_STITCHED rewrites a Placeholder tool bubble in place when
  // the tool's terminal status finally arrived after the round was
  // eagerly flushed. Frontend handlers must accept this event even on
  // bubbles already in MessageStatusEnum.Finished, unlike TOOL_CALL_RESULT
  // / TOOL_CALL_END which are gated against late updates.
  TOOL_CALL_STITCHED = 'TOOL_CALL_STITCHED',
  CUSTOM = 'CUSTOM',
  RUN_STARTED = 'RUN_STARTED',
  RUN_FINISHED = 'RUN_FINISHED',
  RUN_ERROR = 'RUN_ERROR',
  // Job-level events
  JOB_STARTED = 'JOB_STARTED',
  JOB_COMPLETED = 'JOB_COMPLETED',
  JOB_STOPPED = 'JOB_STOPPED',
  JOB_FAILED = 'JOB_FAILED',
  ITERATION_STARTED = 'ITERATION_STARTED',
  ITERATION_COMPLETED = 'ITERATION_COMPLETED',
  ITERATION_FAILED = 'ITERATION_FAILED',
  // Chat-page slash-command feedback (方向一-2). Transient: not persisted.
  COMMAND_SYSTEM_MESSAGE = 'COMMAND_SYSTEM_MESSAGE',
}

export enum MessageRoleEnum {
  USER = 'user',
  ASSISTANT = 'assistant',
  SYSTEM = 'system',
  TOOL = 'tool',
  CUSTOM = 'custom',
}

export enum MessageStatusEnum {
  Loading = 'Loading',
  Started = 'Started',
  Finished = 'Finished',
  Error = 'Error',
}

export enum ToolCallStatusEnum {
  Processing = 'Processing',
  Success = 'Success',
  Error = 'Error',
  // Placeholder is set on history reload for tool results that the
  // backend synthesised because the run was cancelled / interrupted /
  // superseded before the tool produced a real result. Visually
  // distinct from Success (the run never completed) and Error (no
  // actual failure occurred); the server also provides a reason
  // string on the HistoryMessage for tooltip rendering.
  Placeholder = 'Placeholder',
}

export interface BaseEvent {
  type: EventTypeEnum;
  sessionId: string;
  runId: string;
  stepId?: string;
  timestamp: number;
  external?: Record<string, unknown>;
  // Loop context
  jobId?: string;
  path?: number[];
}

export interface RunStartedEvent extends BaseEvent {
  type: EventTypeEnum.RUN_STARTED;
}

export interface TokenUsage {
  totalTokens: number;
}

export interface RunFinishedEvent extends BaseEvent {
  type: EventTypeEnum.RUN_FINISHED;
}

export type RunErrorCode = 'INTERNAL' | 'TIMEOUT' | 'NETWORK' | 'RATE_LIMIT' | 'SHELL' | 'PANIC';

export interface RunErrorEvent extends BaseEvent {
  type: EventTypeEnum.RUN_ERROR;
  message: string;
  code?: RunErrorCode;
}

export interface TextMessageStartEvent extends BaseEvent {
  type: EventTypeEnum.TEXT_MESSAGE_START;
  messageId: string;
  role: MessageRoleEnum;
  name?: string;
  description?: string;
  external?: {
    isThinking?: boolean;
    [key: string]: unknown;
  };
}

export interface TextMessageContentEvent extends BaseEvent {
  type: EventTypeEnum.TEXT_MESSAGE_CONTENT;
  messageId: string;
  role: MessageRoleEnum;
  name?: string;
  description?: string;
  delta: string;
  external?: {
    isThinking?: boolean;
    [key: string]: unknown;
  };
}

export interface TextMessageEndEvent extends BaseEvent {
  type: EventTypeEnum.TEXT_MESSAGE_END;
  messageId: string;
  role: MessageRoleEnum;
  name?: string;
  description?: string;
  external?: {
    isThinking?: boolean;
    [key: string]: unknown;
  };
}

export interface ToolCallStartEvent extends BaseEvent {
  type: EventTypeEnum.TOOL_CALL_START;
  toolCallId: string;
  toolCallName: string;
  parentMessageId?: string;
  toolCallStatus?: ToolCallStatusEnum;
}

export interface ToolCallArgsEvent extends BaseEvent {
  type: EventTypeEnum.TOOL_CALL_ARGS;
  toolCallId: string;
  toolCallName?: string;
  parentMessageId?: string;
  delta: string;
  replace?: boolean;
  toolCallStatus?: ToolCallStatusEnum;
}

export interface ToolCallResultEvent extends BaseEvent {
  type: EventTypeEnum.TOOL_CALL_RESULT;
  toolCallId: string;
  toolCallName?: string;
  parentMessageId?: string;
  delta: string;
  toolCallStatus: ToolCallStatusEnum;
}

export interface ToolCallEndEvent extends BaseEvent {
  type: EventTypeEnum.TOOL_CALL_END;
  toolCallId: string;
  toolCallName?: string;
  parentMessageId?: string;
  toolCallStatus?: ToolCallStatusEnum;
}

export interface ToolCallStitchedEvent extends BaseEvent {
  type: EventTypeEnum.TOOL_CALL_STITCHED;
  toolCallId: string;
  toolCallName?: string;
  parentMessageId?: string;
  delta: string;
  toolCallStatus: ToolCallStatusEnum;
  // Gap (ms) between the eager flush that placeholdered this tool and
  // the late terminal that produced this stitch. Useful for tooltips
  // / observability — the value is informational only.
  supersededAgoMs?: number;
}

export interface CustomEvent extends BaseEvent {
  type: EventTypeEnum.CUSTOM;
  name: string;
  value: unknown;
}

// Job-level event interfaces
export interface IterationResult {
  path: number[];
  sessionId: string;
  success: boolean;
  durationMs: number;
  tokens: number;
  error?: string;
  content?: string;
}

export interface JobProgress {
  totalSteps: number;
  currentPath?: number[];
  /** Unix-ms timestamp for the currently running iteration, persisted by the backend. */
  currentStartedAt?: number;
  completedCount: number;
  failedCount: number;
  results?: IterationResult[];
  lastError?: string;
  persistWarnings?: string[];
  // groupActualIterations maps a group's dot-joined node path (e.g. "0.0") to
  // the number of rounds it actually ran when it broke early via stepStopLoop
  // (evaluator STOP or Shell STOP_LOOP). Used to recompute the session/step
  // plan denominator so the progress text and bar reflect the real run instead
  // of the static iteration cap.
  groupActualIterations?: Record<string, number>;
  // groupActualLeafCounts maps the same group path to the exact number of leaf
  // steps the group CONSUMED before STOP — executed plus empty-prompt-skipped.
  // Unlike iteration counts, this also trims sibling steps skipped after STOP
  // within the final iteration. The session plan keeps this slot prefix, then
  // filters skippedPaths leaves out of it.
  groupActualLeafCounts?: Record<string, number>;
  // skippedPaths records leaf slots (dot-joined full step paths, iteration and
  // repeat indices included, e.g. "0.1.2.0") whose rendered prompt was empty
  // and were therefore skipped without running — no session, no round, no chat
  // messages. Each entry already decremented totalSteps on the backend; the
  // session plan filters these leaves out so session/step numbering matches
  // the real run.
  skippedPaths?: Record<string, boolean>;
  // gracefulStopPending reports a "stop after step" was requested and not yet
  // consumed at a step boundary. Runtime-only (never persisted): the backend
  // synthesizes it onto the GET /job/:id snapshot and broadcasts changes via a
  // transient graceful_stop_pending custom event, so a refresh / second tab can
  // restore the "keep running" affordance.
  gracefulStopPending?: boolean;
}

export interface JobStartedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_STARTED;
  totalSteps: number;
}

// RunOutcome describes what actually happened in the run that just
// ended (loop run, or interactive send). Distinct from the event type
// because an interactive send on an already-terminal job restores the
// prior job status, so JOB_* may reflect prior-state; runOutcome
// always reflects this run's actual result.
export type RunOutcome = 'completed' | 'stopped' | 'failed';

export interface JobCompletedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_COMPLETED;
  progress: JobProgress;
  runOutcome?: RunOutcome;
}

export interface JobStoppedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_STOPPED;
  progress: JobProgress;
  runOutcome?: RunOutcome;
}

export interface JobFailedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_FAILED;
  message: string;
  progress?: JobProgress;
  runOutcome?: RunOutcome;
}

export interface IterationStartedEvent extends BaseEvent {
  type: EventTypeEnum.ITERATION_STARTED;
  message?: string;
  clientMessageId?: string;
  modelId?: string;
  agentType?: string;
  acpMode?: string;
  acpThoughtLevel?: string;
}

export interface IterationCompletedEvent extends BaseEvent {
  type: EventTypeEnum.ITERATION_COMPLETED;
  result: IterationResult;
}

export interface IterationFailedEvent extends BaseEvent {
  type: EventTypeEnum.ITERATION_FAILED;
  result: IterationResult;
}

export interface CommandAction {
  type?: string;
  workspaceId?: string;
  jobId?: string;
}

export interface CommandSystemMessageEvent extends BaseEvent {
  type: EventTypeEnum.COMMAND_SYSTEM_MESSAGE;
  command: string;
  text: string;
  present?: string;
  action?: CommandAction;
}

export type AgentEvent =
  | RunStartedEvent
  | RunFinishedEvent
  | RunErrorEvent
  | TextMessageStartEvent
  | TextMessageContentEvent
  | TextMessageEndEvent
  | ToolCallStartEvent
  | ToolCallArgsEvent
  | ToolCallResultEvent
  | ToolCallEndEvent
  | ToolCallStitchedEvent
  | CustomEvent
  | JobStartedEvent
  | JobCompletedEvent
  | JobStoppedEvent
  | JobFailedEvent
  | IterationStartedEvent
  | IterationCompletedEvent
  | IterationFailedEvent
  | CommandSystemMessageEvent;
