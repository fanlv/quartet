import { useEffect, useImperativeHandle, useLayoutEffect, useRef, useCallback, useState, type UIEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Message, MessageRoleEnum } from '../types';
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
  controlsRef?: React.RefObject<MessageListHandle | null>;
  onTimelineViewChange?: (state: TimelineViewState) => void;
}

/**
 * Timeline mode published to the parent. Mirrors the iOS split: browsing
 * (= free reading, auto-scroll off) vs following (= pinned to the latest
 * message). `hasPending` distinguishes the two floating-button labels:
 * "回到底部" when nothing moved below the fold, "有新消息" once it did.
 */
export interface TimelineViewState {
  browsing: boolean;
  hasPending: boolean;
}

/**
 * Imperative commands exposed to the parent (JobChat) so a single event
 * handler can leave browsing mode and scroll in one go — without waiting for
 * a state round-trip through props. The mode itself travels the other way,
 * through `onTimelineViewChange`.
 */
export interface MessageListHandle {
  /** Leave browsing mode; scrolls to the bottom only if already near it. */
  resumeFollowing: () => void;
  /**
   * Leave browsing mode AND scroll to the bottom unconditionally — the
   * explicit "take me to the newest content" gesture (send, floating button).
   */
  forceFollowAndScrollToBottom: () => void;
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
  controlsRef,
  onTimelineViewChange,
}: MessageListProps) {
  const { t } = useTranslation();
  const containerRef = useRef<HTMLDivElement>(null);
  const prevIsLoadingRef = useRef(isLoading);
  const prevFollowBottomRef = useRef(followBottom);
  const prevScrollContextKeyRef = useRef<string | undefined>(undefined);
  // Track whether the user has intentionally scrolled away from the bottom.
  // When true, we stop auto-scrolling so the user can read history in peace.
  const userScrolledUpRef = useRef(false);
  const browsingMessageCountRef = useRef<number | null>(null);
  const pendingPrependAnchorRef = useRef<PrependAnchor | null>(null);
  const scrollToBottomAfterWindowChangeRef = useRef(false);
  // Latest-bubble fingerprint taken the moment browsing started; any drift
  // means new content arrived while the user was reading history.
  const browsingFingerprintRef = useRef<string | null>(null);
  const hasPendingNewMessagesRef = useRef(false);
  // Re-render trigger for the floating-button state; the scroll positions
  // themselves live in refs so scrolling never re-renders the timeline.
  const [, setScrollUiVersion] = useState(0);
  const [timelineWindow, setTimelineWindow] = useState<TimelineWindowState>({
    contextKey: scrollContextKey,
    messageCount: INITIAL_MESSAGE_COUNT,
  });

  // The message list owns the vertical scrollbar while the composer is its
  // sibling. Measure the reserved gutter so both can share the same content
  // edge even when `scrollbar-width: thin` maps to different pixels by browser
  // or platform.
  useLayoutEffect(() => {
    const el = containerRef.current;
    const layout = el?.closest<HTMLElement>('.chatbot-main');
    if (!el || !layout) return;

    const syncScrollbarGutter = () => {
      const value = `${el.offsetWidth - el.clientWidth}px`;
      if (layout.style.getPropertyValue('--message-list-scrollbar-gutter') !== value) {
        layout.style.setProperty('--message-list-scrollbar-gutter', value);
      }
    };
    syncScrollbarGutter();

    const observer = typeof ResizeObserver === 'undefined'
      ? null
      : new ResizeObserver(syncScrollbarGutter);
    observer?.observe(el);
    window.addEventListener('resize', syncScrollbarGutter);

    return () => {
      observer?.disconnect();
      window.removeEventListener('resize', syncScrollbarGutter);
      layout.style.removeProperty('--message-list-scrollbar-gutter');
    };
  }, []);

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

  /**
   * Fingerprint of the latest visible round: which message is last plus how
   * far along it is. New appends change the id; streaming into the same
   * bubble changes the length. Compared against the snapshot taken when the
   * user started browsing to decide between "回到底部" and "有新消息".
   */
  const latestFingerprint = useCallback(() => {
    const last = messages[messages.length - 1];
    if (!last) return '';
    const progress = last.content.length
      + (last.role === MessageRoleEnum.TOOL ? last.toolCallArgs.length : 0)
      + (last.role === MessageRoleEnum.ASSISTANT ? (last.thinkingContent?.length ?? 0) : 0);
    return `${last.id}:${progress}`;
  }, [messages]);

  const markFollowing = useCallback(() => {
    userScrolledUpRef.current = false;
    browsingMessageCountRef.current = null;
    browsingFingerprintRef.current = null;
    hasPendingNewMessagesRef.current = false;
    setScrollUiVersion((v) => v + 1);
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

  const forceFollowAndScrollToBottom = useCallback(() => {
    resumeFollowing();
    scrollToBottom();
  }, [resumeFollowing, scrollToBottom]);

  // Synchronous command API for the send gesture and the floating button.
  useImperativeHandle(controlsRef, () => ({
    resumeFollowing,
    forceFollowAndScrollToBottom,
  }), [resumeFollowing, forceFollowAndScrollToBottom]);

  // Render-time pending-flag maintenance: while browsing, drift between the
  // latest-bubble fingerprint and the browsing snapshot means new content
  // arrived (or kept streaming) below the fold; toggle the flag and request a
  // render so the parent's floating button can swap labels. Runs only when
  // the inputs can actually change: browsing active, fingerprint taken.
  if (userScrolledUpRef.current && browsingFingerprintRef.current != null) {
    const pending = latestFingerprint() !== browsingFingerprintRef.current;
    if (pending !== hasPendingNewMessagesRef.current) {
      hasPendingNewMessagesRef.current = pending;
      setScrollUiVersion((v) => v + 1);
    }
  }

  // Publish the mode to the parent. The refs above are the source of truth
  // and every write to them is paired with a re-render, so reading them here
  // yields the value of the paint being prepared. Notifying through an effect
  // (rather than letting the parent read the refs) is what makes the floating
  // button appear on a conversation that is otherwise idle: a scroll
  // re-renders this component only, never its parent.
  const timelineBrowsing = userScrolledUpRef.current;
  const timelineHasPending = timelineBrowsing && hasPendingNewMessagesRef.current;
  useEffect(() => {
    onTimelineViewChange?.({ browsing: timelineBrowsing, hasPending: timelineHasPending });
  }, [onTimelineViewChange, timelineBrowsing, timelineHasPending]);

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

    // Latch browsing even when followBottom is false (Graph viewing a
    // pre-latest session): that timeline is frozen, but the user scrolling
    // away from its bottom still deserves the "回到底部" affordance. The
    // flag is harmless there — auto-scroll is gated on followBottom anyway —
    // and the grant-back effect below clears it when the license returns.
    if (!userScrolledUpRef.current) {
      userScrolledUpRef.current = true;
      browsingMessageCountRef.current = messages.length;
      browsingFingerprintRef.current = latestFingerprint();
      hasPendingNewMessagesRef.current = false;
      // The floating "回到底部 / 有新消息" button lives in the parent; this
      // re-render is what pushes the new mode up to it.
      setScrollUiVersion((v) => v + 1);
    }
    if ((nearTop || hasScrolledIntoTopLoadedPage(el)) && (hiddenMessageCount > 0 || hasMoreEarlier)) {
      loadEarlierMessages();
    }
  }, [hasMoreEarlier, hasScrolledIntoTopLoadedPage, hiddenMessageCount, latestFingerprint, loadEarlierMessages, markFollowing, messages.length]);

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
    browsingFingerprintRef.current = null;
    hasPendingNewMessagesRef.current = false;
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

  // `followBottom=false` (Graph viewing a pre-latest session) freezes the
  // timeline: the auto-scroll effect above skips it, and the user may have
  // scrolled around inside it. What's left here is the grant-back
  // transition: the list must return to follow so the latest session's
  // stream can take the scroll position over again.
  useEffect(() => {
    if (!scrollContextKey) return;
    if (followBottom && userScrolledUpRef.current && prevFollowBottomRef.current === false) {
      resumeFollowing();
    }
    prevFollowBottomRef.current = followBottom;
  }, [followBottom, resumeFollowing, scrollContextKey]);

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
