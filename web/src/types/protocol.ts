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
  jobId?: string;
}

export interface RunStartedEvent extends BaseEvent {
  type: EventTypeEnum.RUN_STARTED;
  clientMessageId?: string;
}

export interface TokenUsage {
  totalTokens: number;
  estimated: boolean;
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

export interface JobStartedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_STARTED;
}

// RunOutcome describes what actually happened in the run that just
// ended. Distinct from the event type
// because an interactive send on an already-terminal job restores the
// prior job status, so JOB_* may reflect prior-state; runOutcome
// always reflects this run's actual result.
export type RunOutcome = 'completed' | 'stopped' | 'failed';

export interface JobCompletedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_COMPLETED;
  runOutcome?: RunOutcome;
}

export interface JobStoppedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_STOPPED;
  runOutcome?: RunOutcome;
}

export interface JobFailedEvent extends BaseEvent {
  type: EventTypeEnum.JOB_FAILED;
  message: string;
  runOutcome?: RunOutcome;
}

export interface CommandAction {
  type?: string;
  workspaceId?: string;
  jobId?: string;
  clientMessageId?: string;
}

export interface CommandSystemMessageEvent extends BaseEvent {
  type: EventTypeEnum.COMMAND_SYSTEM_MESSAGE;
  clientMessageId?: string;
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
  | CommandSystemMessageEvent;
