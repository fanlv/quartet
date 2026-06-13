import { MessageRoleEnum, MessageStatusEnum, ToolCallStatusEnum } from './protocol';

export interface BaseMessage {
  id: string;
  role: MessageRoleEnum;
  createdAt: number;
  status: MessageStatusEnum;
  content: string;
  sessionId?: string;
  clientMessageId?: string;
  pending?: boolean;
  failed?: boolean;
}

export interface UserMessage extends BaseMessage {
  role: MessageRoleEnum.USER;
  imageUrls?: string[];
}

export interface AssistantMessage extends BaseMessage {
  role: MessageRoleEnum.ASSISTANT;
  name?: string;
  thinkingContent?: string;
  isThinking?: boolean;
  isShellOutput?: boolean;
  // isSummary marks the first assistant entry on history reload when
  // the server has a summary for this session — the bubble renders
  // the compressed summary text rather than an original assistant
  // turn, signalling to the user that the pre-summary history is no
  // longer in the LLM context.
  isSummary?: boolean;
  /** Timestamp when the assistant bubble finished (TEXT_MESSAGE_END). */
  finishedAt?: number;
  /** Timestamp when deep thinking ended (first non-thinking delta or isThinking flip). */
  thinkingFinishedAt?: number;
}

export interface ToolMessage extends BaseMessage {
  role: MessageRoleEnum.TOOL;
  toolCallId: string;
  toolCallName: string;
  toolCallArgs: string;
  toolCallStatus: ToolCallStatusEnum;
  parentMessageId?: string;
  finishedAt?: number;
  // placeholderReason carries the reason string (canceled /
  // interrupted / superseded) the server included when the backend
  // synthesised a placeholder tool result. Only populated when
  // toolCallStatus === Placeholder.
  placeholderReason?: string;
}

/**
 * System messages are transient UI feedback — e.g. slash-command results
 * pushed as a COMMAND_SYSTEM_MESSAGE SSE event. They are not saved to the
 * Job's message history and disappear on page refresh.
 */
export interface SystemMessage extends BaseMessage {
  role: MessageRoleEnum.SYSTEM;
  // Canonical command that produced this message (e.g. "/workspace", "/job").
  // The renderer uses this to turn list items in /ws list / /job list output
  // into clickable "run /ws N" / "run /job N" links. Empty for messages that
  // shouldn't be parsed.
  commandSource?: string;
}

export type Message = UserMessage | AssistantMessage | ToolMessage | SystemMessage;

export interface RunAgentInput {
  sessionId: string;
  messages: Array<{
    id: string;
    type: string;
    content: string;
    timestamp: number;
    role: string;
  }>;
  context?: {
    timestamp: number;
    timezone: string;
    userId?: string;
  };
  state?: Record<string, unknown>;
}

// Job-related types
export type RoundMode = 'beforeRound' | 'eachRepeat' | 'none';
export type RoundType = 'prompt' | 'shell' | 'evaluator';

// FlowNode recursive tree types
export type FlowNodeType = 'step' | 'group';

export interface FlowNode {
  id: string;
  type: FlowNodeType;
  label?: string;
  // Step fields
  message?: string;
  repeatCount?: number;
  roundMode?: RoundMode;
  roundType?: RoundType;
  // Per-step agent/model overrides (only meaningful when roundMode != 'none')
  agentType?: string;
  modelId?: string;
  acpMode?: string;
  // Group fields
  iterationCount?: number;
  children?: FlowNode[];
}

// Legacy round definition (kept for backward compatibility)
export interface LoopRound {
  message: string;
  repeatCount: number;
  roundMode: RoundMode;
  roundType?: RoundType;
}

export interface LoopConfig {
  flow?: FlowNode[];
  variables?: Record<string, string>;
  // disabledVars lists user-variable keys toggled off. A disabled variable
  // keeps its value in `variables` but is substituted as an empty string.
  disabledVars?: string[];
  // Deprecated legacy fields
  iterationCount?: number;
  rounds?: LoopRound[];
}

export interface LoopTemplate {
  id: string;
  name: string;
  config: LoopConfig;
  createdAt: string;
  updatedAt?: string;
  scheduleCount?: number;
}

export interface JobInfo {
  id: string;
  title: string;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'stopped';
  mode?: 'interactive' | 'loop';
  createdAt: number;
  updatedAt: number;
  loopConfig?: LoopConfig;
  progress?: {
    totalSteps: number;
    currentPath?: number[];
    completedCount: number;
    failedCount: number;
  };
  sessionCount: number;
  scheduleId?: string;
}

export interface ScheduleInfo {
  id: string;
  name: string;
  enabled: boolean;
  cronExpr: string;
  templateId?: string;
  loopConfig?: LoopConfig;
  workspaceId: string;
  workdir?: string;
  maxConcurrent?: number;
  timeout?: number;
  lastRunAt?: number;
  lastRunJobID?: string;
  lastStatus?: string;
  nextRunAt?: number;
  runCount: number;
  createdAt: number;
  updatedAt: number;
}
