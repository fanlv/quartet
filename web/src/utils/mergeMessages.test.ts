import { describe, expect, it } from 'vitest';
import { mergeLatestHistoryPage, mergeMessages } from './mergeMessages';
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

describe('mergeLatestHistoryPage', () => {
  it('keeps messages the newest page does not cover in front of it', () => {
    // The user replied and the turn produced enough records to push both the
    // previous answer and the user message out of the newest page. A reload
    // that only kept the page would erase them from the conversation.
    const olderAnswer = baseMessage('assistant-old', MessageRoleEnum.ASSISTANT, 'previous answer');
    const userMessage = baseMessage('client-1', MessageRoleEnum.USER, 'new question');
    const pageHead = baseMessage('tool-run-1', MessageRoleEnum.ASSISTANT, 'working on it');
    const pageTail = baseMessage('tool-run-2', MessageRoleEnum.ASSISTANT, 'still working');

    const merged = mergeLatestHistoryPage(
      [olderAnswer, userMessage, pageHead],
      [pageHead, pageTail],
    );

    expect(merged).toEqual([olderAnswer, userMessage, pageHead, pageTail]);
  });

  it('keeps settled messages in front when the page overlaps nothing in memory', () => {
    const olderAnswer = baseMessage('assistant-old', MessageRoleEnum.ASSISTANT, 'previous answer');
    const userMessage = baseMessage('client-1', MessageRoleEnum.USER, 'new question');
    const streaming = baseMessage('assistant-live', MessageRoleEnum.ASSISTANT, 'partial', {
      status: MessageStatusEnum.Started,
    });
    const pageOnly = baseMessage('assistant-page', MessageRoleEnum.ASSISTANT, 'persisted answer');

    const merged = mergeLatestHistoryPage([olderAnswer, userMessage, streaming], [pageOnly]);

    // Settled history stays in front of the page; the still-streaming bubble
    // is a live artefact and belongs after it.
    expect(merged).toEqual([olderAnswer, userMessage, pageOnly, streaming]);
  });

  it('drops settled live bubbles inside the region the page covers', () => {
    // One persisted assistant row can collapse several streamed bubbles, so
    // the pre-collapse bubble must not survive next to the persisted row —
    // its text would render twice.
    const userMessage = baseMessage('client-1', MessageRoleEnum.USER, 'question');
    const firstChunk = baseMessage('stream-1', MessageRoleEnum.ASSISTANT, 'part one ');
    const collapsed = baseMessage('stream-2', MessageRoleEnum.ASSISTANT, 'part one part two');

    const merged = mergeLatestHistoryPage(
      [userMessage, firstChunk, baseMessage('stream-2', MessageRoleEnum.ASSISTANT, 'part two')],
      [userMessage, collapsed],
    );

    expect(merged).toEqual([userMessage, collapsed]);
  });

  it('keeps an unconfirmed optimistic user message the page has not persisted yet', () => {
    const answer = baseMessage('assistant-1', MessageRoleEnum.ASSISTANT, 'answer');
    const optimistic = baseMessage('client-2', MessageRoleEnum.USER, 'just sent', {
      clientMessageId: 'client-2',
      pending: true,
    });

    expect(mergeLatestHistoryPage([answer, optimistic], [answer])).toEqual([answer, optimistic]);
  });

  it('keeps a transient bubble anchored between its neighbours instead of sweeping it to the end', () => {
    // A slash-command bubble is never persisted, so the newest page cannot
    // carry it. Appending it after the page put an older bubble below the
    // newest user message — the duplicate-looking bubble users reported.
    const firstQuestion = baseMessage('user-1', MessageRoleEnum.USER, 'first question');
    const firstAnswer = baseMessage('assistant-1', MessageRoleEnum.ASSISTANT, 'first answer');
    const commandBubble = baseMessage('cmd-1', MessageRoleEnum.SYSTEM, '/help output');
    const secondQuestion = baseMessage('user-2', MessageRoleEnum.USER, 'second question');
    const secondAnswer = baseMessage('assistant-2', MessageRoleEnum.ASSISTANT, 'second answer');

    const merged = mergeLatestHistoryPage(
      [firstQuestion, firstAnswer, commandBubble, secondQuestion, secondAnswer],
      [firstQuestion, firstAnswer, secondQuestion, secondAnswer],
    );

    expect(merged).toEqual([firstQuestion, firstAnswer, commandBubble, secondQuestion, secondAnswer]);
  });

  it('leaves the list untouched when the newest page is empty', () => {
    const existing = [
      baseMessage('assistant-1', MessageRoleEnum.ASSISTANT, 'answer'),
      baseMessage('client-1', MessageRoleEnum.USER, 'question'),
    ];

    expect(mergeLatestHistoryPage(existing, [])).toBe(existing);
  });
});

describe('pinned round heads', () => {
  // The message queue reports the message the backend is running as `active`.
  // On a turn long enough to push it out of the newest page, the chat pins a
  // stand-in copy to the front of the loaded window. No merge may reorder that
  // copy into page order: it stands for a record that lives ABOVE the page.
  const pinnedHead = baseMessage('initial-1', MessageRoleEnum.USER, 'opening question', {
    clientMessageId: 'initial-1', pending: true, roundHeadPinned: true,
  });

  it('keeps the pinned head in front when the newest page does not reach it', () => {
    const pageTool = toolMessage('call-9', 'call-9', 'late tool output');
    const pageAnswer = baseMessage('assistant-9', MessageRoleEnum.ASSISTANT, 'late answer');

    const merged = mergeLatestHistoryPage([pinnedHead, pageTool], [pageTool, pageAnswer]);

    expect(merged).toEqual([pinnedHead, pageTool, pageAnswer]);
  });

  it('keeps the pinned head in front even when the page shares no id with the list', () => {
    const pageAnswer = baseMessage('assistant-9', MessageRoleEnum.ASSISTANT, 'late answer');

    expect(mergeLatestHistoryPage([pinnedHead], [pageAnswer])).toEqual([pinnedHead, pageAnswer]);
  });

  it('drops the pinned head once a page carries the real record', () => {
    const realHead = baseMessage('initial-1', MessageRoleEnum.USER, 'opening question', {
      clientMessageId: 'initial-1',
    });
    const firstAnswer = baseMessage('assistant-1', MessageRoleEnum.ASSISTANT, 'first answer');

    // Scrolling up prepends the earlier page that finally contains record 0.
    const merged = mergeMessages([pinnedHead], [realHead, firstAnswer]);

    expect(merged).toEqual([realHead, firstAnswer]);
  });

  it('floats the pinned head above an earlier page that still does not reach it', () => {
    const middleAnswer = baseMessage('assistant-5', MessageRoleEnum.ASSISTANT, 'middle answer');
    const windowTool = toolMessage('call-9', 'call-9', 'late tool output');

    const merged = mergeMessages([pinnedHead, windowTool], [middleAnswer]);

    expect(merged).toEqual([pinnedHead, middleAnswer, windowTool]);
  });
});
