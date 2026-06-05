import { useEffect, useRef, useCallback } from 'react';
import { Message } from '../types';
import { MessageItem } from './MessageItem';
import { WelcomeHero } from './WelcomeHero';
import './MessageList.css';

interface MessageListProps {
  messages: Message[];
  isLoading: boolean;
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

export function MessageList({
  messages,
  isLoading,
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
  const containerRef = useRef<HTMLDivElement>(null);
  const prevIsLoadingRef = useRef(isLoading);
  const prevScrollContextKeyRef = useRef<string | undefined>(undefined);
  // Track whether the user has intentionally scrolled away from the bottom.
  // When true, we stop auto-scrolling so the user can read history in peace.
  const userScrolledUpRef = useRef(false);

  const scrollToBottom = useCallback(() => {
    if (containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, []);

  // Listen to user scroll events to detect manual scroll-up.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const onScroll = () => {
      const { scrollTop, scrollHeight, clientHeight } = el;
      const nearBottom = scrollHeight - scrollTop - clientHeight < 80;
      userScrolledUpRef.current = !nearBottom;
    };
    el.addEventListener('scroll', onScroll, { passive: true });
    return () => el.removeEventListener('scroll', onScroll);
  }, []);

  // When switching jobs/sessions, always land at the bottom once so the latest
  // content in that context is visible immediately.
  useEffect(() => {
    if (prevScrollContextKeyRef.current === scrollContextKey) return;
    prevScrollContextKeyRef.current = scrollContextKey;
    userScrolledUpRef.current = false;
    scrollToBottom();
  }, [scrollContextKey, scrollToBottom]);

  // Scroll to bottom when streaming starts (isLoading becomes true),
  // so users entering a page with an active SSE stream see the latest output.
  // This also resets the scrolled-up flag because a new stream is starting.
  useEffect(() => {
    if (followBottom && !prevIsLoadingRef.current && isLoading) {
      userScrolledUpRef.current = false;
      scrollToBottom();
    }
    prevIsLoadingRef.current = isLoading;
  }, [followBottom, isLoading, scrollToBottom]);

  // Auto-scroll on message updates, but only if the user hasn't scrolled up.
  useEffect(() => {
    if (followBottom && !userScrolledUpRef.current) {
      scrollToBottom();
    }
  }, [followBottom, messages, scrollToBottom]);

  return (
    <div className="message-list" ref={containerRef} data-testid="message-list" data-loading={isLoading ? 'true' : 'false'}>
      {messages.length === 0 ? (
        <div className="empty-state" data-testid="message-list-empty">
          <WelcomeHero
            onSuggestionClick={onSendMessage}
            disabled={isLoading}
          />
        </div>
      ) : (
        <>
          {messages.map((message) => {
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
              <span>AI 正在思考...</span>
            </div>
          )}
        </>
      )}
    </div>
  );
}
