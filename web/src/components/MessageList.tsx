import { useEffect, useLayoutEffect, useRef, useCallback, useState, type UIEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Message } from '../types';
import { MessageItem } from './MessageItem';
import { WelcomeHero } from './WelcomeHero';
import './MessageList.css';

interface MessageListProps {
  messages: Message[];
  isLoading: boolean;
  /** Label for the loading indicator; falls back to "AI 正在思考..." when absent. */
  loadingLabel?: string;
  onSendMessage?: (message: string, imageUrls?: string[]) => void;
  agentIconUrl?: string;
  agentDisplayName?: string;
  resolveAgentForSession?: (sessionId?: string) => { iconUrl?: string; displayName?: string };
  jobId?: string;
  workdir?: string;
  shareToken?: string;
  followBottom?: boolean;
  scrollContextKey?: string;
}

const INITIAL_MESSAGE_COUNT = 80;
const EARLIER_PAGE_SIZE = 60;
const TOP_LOAD_THRESHOLD_PX = 48;

interface TimelineWindowState {
  contextKey?: string;
  messageCount: number;
}

interface PrependAnchor {
  element: HTMLElement | null;
  top: number;
  scrollHeight: number;
  scrollTop: number;
}

export function MessageList({
  messages,
  isLoading,
  loadingLabel,
  onSendMessage,
  agentIconUrl,
  agentDisplayName,
  resolveAgentForSession,
  jobId,
  workdir,
  shareToken,
  followBottom = true,
  scrollContextKey,
}: MessageListProps) {
  const { t } = useTranslation();
  const containerRef = useRef<HTMLDivElement>(null);
  const prevIsLoadingRef = useRef(isLoading);
  const prevScrollContextKeyRef = useRef<string | undefined>(undefined);
  // Track whether the user has intentionally scrolled away from the bottom.
  // When true, we stop auto-scrolling so the user can read history in peace.
  const userScrolledUpRef = useRef(false);
  const browsingMessageCountRef = useRef<number | null>(null);
  const pendingPrependAnchorRef = useRef<PrependAnchor | null>(null);
  const scrollToBottomAfterWindowChangeRef = useRef(false);
  const [timelineWindow, setTimelineWindow] = useState<TimelineWindowState>({
    contextKey: scrollContextKey,
    messageCount: INITIAL_MESSAGE_COUNT,
  });

  // A context switch always renders the bounded initial window immediately,
  // even before the layout effect below synchronizes the state object.
  const visibleMessageCount = timelineWindow.contextKey === scrollContextKey
    ? timelineWindow.messageCount
    : INITIAL_MESSAGE_COUNT;
  // While the user reads history, live messages appended at the bottom must
  // not consume the window quota and evict the message currently at the top.
  const appendedWhileBrowsing = browsingMessageCountRef.current == null
    ? 0
    : Math.max(0, messages.length - browsingMessageCountRef.current);
  const effectiveMessageCount = visibleMessageCount + appendedWhileBrowsing;
  const firstVisibleMessageIndex = Math.max(0, messages.length - effectiveMessageCount);
  const visibleMessages = messages.slice(firstVisibleMessageIndex);
  const hiddenMessageCount = firstVisibleMessageIndex;

  const scrollToBottom = useCallback(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, []);

  const markFollowing = useCallback(() => {
    userScrolledUpRef.current = false;
    browsingMessageCountRef.current = null;
  }, []);

  const resumeFollowing = useCallback(() => {
    markFollowing();
    pendingPrependAnchorRef.current = null;

    if (visibleMessageCount === INITIAL_MESSAGE_COUNT) {
      scrollToBottom();
      return;
    }

    scrollToBottomAfterWindowChangeRef.current = true;
    setTimelineWindow({
      contextKey: scrollContextKey,
      messageCount: INITIAL_MESSAGE_COUNT,
    });
  }, [markFollowing, scrollContextKey, scrollToBottom, visibleMessageCount]);

  const loadEarlierMessages = useCallback(() => {
    const el = containerRef.current;
    if (!el || hiddenMessageCount === 0 || pendingPrependAnchorRef.current) return;

    const anchor = el.querySelector<HTMLElement>('[data-message-id]');
    pendingPrependAnchorRef.current = {
      element: anchor,
      top: anchor?.getBoundingClientRect().top ?? 0,
      scrollHeight: el.scrollHeight,
      scrollTop: el.scrollTop,
    };
    setTimelineWindow({
      contextKey: scrollContextKey,
      messageCount: Math.min(messages.length, visibleMessageCount + EARLIER_PAGE_SIZE),
    });
  }, [hiddenMessageCount, messages.length, scrollContextKey, visibleMessageCount]);

  // Restore the viewport after prepending a page. Prefer the first existing
  // message as a real DOM anchor because message heights are variable; fall
  // back to the scroll-height delta if that message did not render a node.
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const pending = pendingPrependAnchorRef.current;
    if (pending) {
      if (pending.element?.isConnected) {
        el.scrollTop += pending.element.getBoundingClientRect().top - pending.top;
      } else {
        el.scrollTop = pending.scrollTop + el.scrollHeight - pending.scrollHeight;
      }
      pendingPrependAnchorRef.current = null;
    }

    if (scrollToBottomAfterWindowChangeRef.current) {
      scrollToBottomAfterWindowChangeRef.current = false;
      scrollToBottom();
    }
  }, [timelineWindow, scrollToBottom]);

  // Listen to user scroll events to detect manual scroll-up.
  const handleScroll = useCallback((event: UIEvent<HTMLDivElement>) => {
    const el = event.currentTarget;
    const { scrollTop, scrollHeight, clientHeight } = el;
    const nearBottom = scrollHeight - scrollTop - clientHeight < 80;

    if (nearBottom) {
      if (userScrolledUpRef.current) markFollowing();
      return;
    }

    if (!userScrolledUpRef.current) {
      userScrolledUpRef.current = true;
      browsingMessageCountRef.current = messages.length;
    }
    if (scrollTop <= TOP_LOAD_THRESHOLD_PX) loadEarlierMessages();
  }, [loadEarlierMessages, markFollowing, messages.length]);

  // When switching jobs/sessions, always land at the bottom once so the latest
  // content in that context is visible immediately.
  useLayoutEffect(() => {
    if (prevScrollContextKeyRef.current === scrollContextKey) return;
    prevScrollContextKeyRef.current = scrollContextKey;
    userScrolledUpRef.current = false;
    browsingMessageCountRef.current = null;
    pendingPrependAnchorRef.current = null;
    scrollToBottomAfterWindowChangeRef.current = false;
    setTimelineWindow({
      contextKey: scrollContextKey,
      messageCount: INITIAL_MESSAGE_COUNT,
    });
    scrollToBottom();
  }, [scrollContextKey, scrollToBottom]);

  // Scroll to bottom when streaming starts (isLoading becomes true),
  // so users entering a page with an active SSE stream see the latest output.
  // This also resets the scrolled-up flag because a new stream is starting.
  useEffect(() => {
    if (followBottom && !prevIsLoadingRef.current && isLoading) {
      resumeFollowing();
    }
    prevIsLoadingRef.current = isLoading;
  }, [followBottom, isLoading, resumeFollowing]);

  // Auto-scroll on message updates, but only if the user hasn't scrolled up.
  useEffect(() => {
    if (followBottom && !userScrolledUpRef.current) {
      scrollToBottom();
    }
  }, [followBottom, messages, scrollToBottom]);

  return (
    <div
      className="message-list"
      ref={containerRef}
      data-testid="message-list"
      data-loading={isLoading ? 'true' : 'false'}
      onScroll={handleScroll}
    >
      {messages.length === 0 ? (
        <div className="empty-state" data-testid="message-list-empty">
          <WelcomeHero
            onSuggestionClick={onSendMessage}
            disabled={isLoading}
          />
        </div>
      ) : (
        <>
          {hiddenMessageCount > 0 && (
            <div
              className="message-history-loader"
              data-testid="message-history-loader"
              role="status"
              aria-label={t('chat.loadingEarlierMessages')}
            >
              <span className="message-history-loader-spinner" aria-hidden="true" />
            </div>
          )}
          {visibleMessages.map((message) => {
            // Resolve agent icon per message from session metadata; fall back to global props
            const resolved = resolveAgentForSession?.(message.sessionId);
            const msgIconUrl = resolved?.iconUrl ?? agentIconUrl;
            const msgDisplayName = resolved?.displayName ?? agentDisplayName;
            return (
              <MessageItem key={message.id} message={message} agentIconUrl={msgIconUrl} agentDisplayName={msgDisplayName} jobId={jobId} workdir={workdir} shareToken={shareToken} />
            );
          })}
          {isLoading && messages.length > 0 && (
            <div className="loading-indicator" data-testid="message-loading-indicator">
              <div className="loading-dots">
                <span />
                <span />
                <span />
              </div>
              <span>{loadingLabel ?? 'AI 正在思考...'}</span>
            </div>
          )}
        </>
      )}
    </div>
  );
}
