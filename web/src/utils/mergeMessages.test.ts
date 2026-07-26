import { describe, expect, it } from 'vitest';
import { mergeMessages } from './mergeMessages';
import type { Message, ToolMessage } from '../types/message';
import { MessageRoleEnum, MessageStatusEnum, ToolCallStatusEnum } from '../types/protocol';

function baseMessage(id: string, role: MessageRoleEnum, content: string, extra: Partial<Message> = {}): Message {
  return {
    id,
    role,
    content,
    createdAt: 1,
    status: MessageStatusEnum.Finished,
    ...extra,
  } as Message;
}

function toolMessage(id: string, toolCallId: string, content = ''): ToolMessage {
  return {
    id,
    role: MessageRoleEnum.TOOL,
    content,
    createdAt: 1,
    status: MessageStatusEnum.Finished,
    toolCallId,
    toolCallName: 'test_tool',
    toolCallArgs: '{}',
    toolCallStatus: ToolCallStatusEnum.Success,
  };
}

describe('mergeMessages', () => {
  it('returns the other side directly when one side is empty', () => {
    const existing = [baseMessage('m1', MessageRoleEnum.USER, 'hello')];
    const incoming = [baseMessage('m2', MessageRoleEnum.ASSISTANT, 'world')];

    expect(mergeMessages(existing, [])).toBe(existing);
    expect(mergeMessages([], incoming)).toBe(incoming);
  });

  it('prefers existing streaming content when it is longer than confirmed history', () => {
    const existingLonger = baseMessage('assistant-1', MessageRoleEnum.ASSISTANT, 'hello streaming world');
    const incomingShorter = baseMessage('assistant-1', MessageRoleEnum.ASSISTANT, 'hello');

    expect(mergeMessages([existingLonger], [incomingShorter])).toEqual([existingLonger]);
  });

  it('drops optimistic user messages after confirmed history with the same client message id arrives', () => {
    const optimistic = baseMessage('local-1', MessageRoleEnum.USER, 'question', { clientMessageId: 'client-1' });
    const confirmed = baseMessage('history-1', MessageRoleEnum.USER, 'question', { clientMessageId: 'client-1' });

    expect(mergeMessages([optimistic], [confirmed])).toEqual([confirmed]);
  });

  it('keeps fresh optimistic user messages when history has not confirmed them yet', () => {
    const optimistic = baseMessage('local-1', MessageRoleEnum.USER, 'new question', { clientMessageId: 'client-new' });
    const olderHistory = baseMessage('history-1', MessageRoleEnum.ASSISTANT, 'older answer');

    expect(mergeMessages([optimistic], [olderHistory])).toEqual([olderHistory, optimistic]);
  });

  it('optionally deduplicates existing tool messages by tool call id', () => {
    const existingTool = toolMessage('tool-live', 'tool-call-1', 'live result');
    const incomingTool = toolMessage('tool-history', 'tool-call-1', 'history result');

    expect(mergeMessages([existingTool], [incomingTool])).toEqual([incomingTool, existingTool]);
    expect(mergeMessages([existingTool], [incomingTool], { deduplicateToolCallIds: true })).toEqual([incomingTool]);
  });

  it('drops a live thought bubble when history has the same session/thinkingContent under a different id', () => {
    // Live SSE thought bubble (id from OnThoughtStart) and history thought
    // bubble (id from persisted thought_msg_id) momentarily diverge; without
    // semantic dedup both render and the thought shows twice.
    const liveThought = baseMessage('live-uuid', MessageRoleEnum.ASSISTANT, '', {
      sessionId: 'session-1',
      thinkingContent: 'reasoning about the layout',
    });
    const historyThought = baseMessage('thought-msg-id', MessageRoleEnum.ASSISTANT, '', {
      sessionId: 'session-1',
      thinkingContent: 'reasoning about the layout',
    });

    expect(mergeMessages([liveThought], [historyThought])).toEqual([historyThought]);
  });

  it('keeps thought bubbles with different thinkingContent', () => {
    const thoughtA = baseMessage('uuid-a', MessageRoleEnum.ASSISTANT, '', {
      sessionId: 'session-1',
      thinkingContent: 'first reasoning',
    });
    const thoughtB = baseMessage('uuid-b', MessageRoleEnum.ASSISTANT, '', {
      sessionId: 'session-1',
      thinkingContent: 'second reasoning',
    });

    expect(mergeMessages([thoughtA], [thoughtB])).toEqual([thoughtB, thoughtA]);
  });

  it('does not treat an assistant message with body content as a thought bubble', () => {
    // Same sessionId + thinkingContent, but the existing message also has
    // body content, so it is a real assistant turn, not a pure thought
    // bubble — it must be preserved.
    const liveAnswer = baseMessage('live-uuid', MessageRoleEnum.ASSISTANT, 'the answer', {
      sessionId: 'session-1',
      thinkingContent: 'shared reasoning',
    });
    const historyThought = baseMessage('thought-msg-id', MessageRoleEnum.ASSISTANT, '', {
      sessionId: 'session-1',
      thinkingContent: 'shared reasoning',
    });

    expect(mergeMessages([liveAnswer], [historyThought])).toEqual([historyThought, liveAnswer]);
  });
});
