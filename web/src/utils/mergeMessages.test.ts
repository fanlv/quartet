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

  it('drops synthetic loop user messages once matching session/content history exists', () => {
    const synthetic = baseMessage('loop-user-1', MessageRoleEnum.USER, 'loop prompt', { sessionId: 'session-1' });
    const confirmed = baseMessage('history-1', MessageRoleEnum.USER, 'loop prompt', { sessionId: 'session-1' });

    expect(mergeMessages([synthetic], [confirmed])).toEqual([confirmed]);
  });

  it('optionally deduplicates existing tool messages by tool call id', () => {
    const existingTool = toolMessage('tool-live', 'tool-call-1', 'live result');
    const incomingTool = toolMessage('tool-history', 'tool-call-1', 'history result');

    expect(mergeMessages([existingTool], [incomingTool])).toEqual([incomingTool, existingTool]);
    expect(mergeMessages([existingTool], [incomingTool], { deduplicateToolCallIds: true })).toEqual([incomingTool]);
  });
});
