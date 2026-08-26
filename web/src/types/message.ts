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
  deliveryStatus?: 'sending' | 'sent' | 'failed';
  sendError?: string;
}

export interface FileAttachment {
  path: string;
  name: string;
  mimeType?: string;
  size?: number;
}

export interface UserMessage extends BaseMessage {
  role: MessageRoleEnum.USER;
  imageUrls?: string[];
  fileAttachments?: FileAttachment[];
  // True for the script of a Graph Shell node's display session. Rendered as
  // preformatted text (not markdown) so newlines and shell syntax survive.
  isShellOutput?: boolean;
}

export interface AssistantMessage extends BaseMessage {
  role: MessageRoleEnum.ASSISTANT;
  name?: string;
  thinkingContent?: string;
  isThinking?: boolean;
  isShellOutput?: boolean;
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

