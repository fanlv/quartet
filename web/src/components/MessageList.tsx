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
  hasMoreEarlier?: boolean;
  onNeedEarlier?: () => Promise<number>;
}

const INITIAL_MESSAGE_COUNT = 80;
const EARLIER_PAGE_SIZE = 80;
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

/**
 * First message node that a prepended page pushes down. Pinned round heads are
 * skipped: they stand for a message above the window and stay put across a
 * prepend, so anchoring on one would measure a zero delta and let the reading
 * position jump by the height of the newly inserted page.
 */
function firstMessageElement(container: HTMLElement): HTMLElement | null {
  const candidate = container.querySelector<HTMLElement>('[data-message-id]:not([data-round-head-pinned])');
  return candidate?.dataset.messageId ? candidate : null;
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
  hasMoreEarlier = false,
  onNeedEarlier,
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
  // A pinned round head stands for a message that lives ABOVE the loaded
  // window, and pinning exists precisely so the user can still see the message
  // that started the round. It sits at the very front, which is exactly what a
  // tail-anchored window drops first, so keep it out of the quota and always
  // render it. It is not "hidden earlier history" either — the counter below
  // drives the load-earlier affordance and must ignore it.
  let pinnedHeadCount = 0;
  while (pinnedHeadCount < messages.length && messages[pinnedHeadCount].roundHeadPinned === true) pinnedHeadCount++;
  const firstVisibleMessageIndex = Math.max(pinnedHeadCount, messages.length - effectiveMessageCount);
  const visibleMessages = pinnedHeadCount > 0
    ? [...messages.slice(0, pinnedHeadCount), ...messages.slice(firstVisibleMessageIndex)]
    : messages.slice(firstVisibleMessageIndex);
  const hiddenMessageCount = firstVisibleMessageIndex - pinnedHeadCount;

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

  // Every prepend must widen the render window by what it added, otherwise the
  // new page lands in the hidden region — which sits at the TOP of the list, so
  // it silently un-renders whatever the user was reading up there.
  const growWindowBy = useCallback((loadedCount: number) => {
    if (loadedCount <= 0) return;
    setTimelineWindow((current) => ({
      contextKey: scrollContextKey,
      messageCount: (current.contextKey === scrollContextKey
        ? current.messageCount
        : INITIAL_MESSAGE_COUNT) + loadedCount,
    }));
  }, [scrollContextKey]);

  // The message one page below the top of what is loaded. Reaching it means the
  // user has scrolled into the topmost loaded page and the next one should be
  // fetched NOW — waiting until they hit the very top makes them stall there on
  // every page. Null when less than a page is loaded above the newest one:
  // there is nothing to measure against, so any upward scroll is the signal.
  const earlierBufferSentinelId = messages.length - pinnedHeadCount > EARLIER_PAGE_SIZE
    ? messages[pinnedHeadCount + EARLIER_PAGE_SIZE].id
    : null;
  const hasScrolledIntoTopLoadedPage = useCallback((el: HTMLElement) => {
    if (!earlierBufferSentinelId) return true;
    const sentinel = el.querySelector<HTMLElement>(`[data-message-id="${CSS.escape(earlierBufferSentinelId)}"]`);
    if (!sentinel) return false;
    return sentinel.getBoundingClientRect().top <= el.getBoundingClientRect().bottom;
  }, [earlierBufferSentinelId]);

  const loadEarlierMessages = useCallback(() => {
    const el = containerRef.current;
    if (!el || pendingPrependAnchorRef.current) return;
    if (hiddenMessageCount === 0) {
      if (hasMoreEarlier && onNeedEarlier) {
        const anchor = firstMessageElement(el);
        pendingPrependAnchorRef.current = {
          element: anchor,
          top: anchor?.getBoundingClientRect().top ?? 0,
          scrollHeight: el.scrollHeight,
          scrollTop: el.scrollTop,
        };
        void onNeedEarlier().then((loadedCount) => {
          if (loadedCount <= 0) {
            pendingPrependAnchorRef.current = null;
            return;
          }
          if (browsingMessageCountRef.current != null) {
            browsingMessageCountRef.current += loadedCount;
          }
          growWindowBy(loadedCount);
        }).catch(() => {
          pendingPrependAnchorRef.current = null;
        });
      }
      return;
    }

    const anchor = firstMessageElement(el);
    const willConsumeAllBufferedMessages = visibleMessageCount + EARLIER_PAGE_SIZE >= messages.length;
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
    if (willConsumeAllBufferedMessages && hasMoreEarlier && onNeedEarlier) {
      void onNeedEarlier().then((loadedCount) => {
        if (browsingMessageCountRef.current != null) {
          browsingMessageCountRef.current += loadedCount;
        }
        // Grow the window by what was prepended, exactly like the other fetch
        // path. Without this the whole new page lands in the hidden region —
        // and that region is at the TOP of the list, so content the user was
        // already looking at (the round head standing in for a message above
        // the window) silently drops out of the render.
        growWindowBy(loadedCount);
      }).catch(() => {});
    }
  }, [growWindowBy, hasMoreEarlier, hiddenMessageCount, messages.length, onNeedEarlier, scrollContextKey, visibleMessageCount]);

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
    const nearTop = scrollTop <= TOP_LOAD_THRESHOLD_PX;

    // Bottom is checked first on purpose: on a list only slightly taller than
    // the viewport both ends are "near", and treating that as browsing latches
    // userScrolledUp for good — follow-the-bottom then stays off and every new
    // message (including the one the user just sent) lands below the fold.
    if (nearBottom) {
      if (userScrolledUpRef.current) markFollowing();
      return;
    }

    if (!userScrolledUpRef.current) {
      userScrolledUpRef.current = true;
      browsingMessageCountRef.current = messages.length;
    }
    if ((nearTop || hasScrolledIntoTopLoadedPage(el)) && (hiddenMessageCount > 0 || hasMoreEarlier)) {
      loadEarlierMessages();
    }
  }, [hasMoreEarlier, hasScrolledIntoTopLoadedPage, hiddenMessageCount, loadEarlierMessages, markFollowing, messages.length]);

  // Prime the conversation to two pages. The first paint deliberately renders
  // one page only, but a single page leaves nothing above the viewport to
  // measure against, so "fetch the next page before the user reaches the top"
  // cannot work on the first scroll up. Runs once per conversation, after the
  // first paint, and consumes the page the hook already prefetched — so it adds
  // no request, only makes the buffer visible.
  const primedContextRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (primedContextRef.current === scrollContextKey || messages.length === 0) return;
    primedContextRef.current = scrollContextKey;
    if (hasMoreEarlier || hiddenMessageCount > 0) loadEarlierMessages();
  }, [hasMoreEarlier, hiddenMessageCount, loadEarlierMessages, messages.length, scrollContextKey]);

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
