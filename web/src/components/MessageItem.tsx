import React, { useState, useEffect, useRef, createContext, useContext, useCallback, useMemo, memo } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import type { Components } from 'react-markdown';
import { Message, UserMessage, AssistantMessage, ToolMessage, SystemMessage, MessageRoleEnum, MessageStatusEnum, ToolCallStatusEnum, type CommandSystemMessageEvent } from '../types';
import { copyToClipboard } from '../utils/clipboard';
import { detectLanguage, getLanguageLabel, tokenizeLine } from '../utils/syntaxHighlight';
import { formatMessageTime } from '../utils/time';
import { isImageUrl } from '../utils/url';
import { DurationBadge } from './DurationBadge';
import './MessageItem.css';

type OpenFileViewerFn = (filePath: string, line?: number, endLine?: number) => void;
const FileViewerContext = createContext<OpenFileViewerFn | null>(null);
const WorkdirContext = createContext<string>('');

// Module-level cache of /api/v1/file-exists results. A single message can
// reference the same file multiple times (e.g. a code review listing 15
// hits in the same header file) and before this cache each FileChip mount
// fired its own HTTP request. The cache dedupes concurrent lookups via a
// Promise and memoises results across the page.
//
// Bounded to FILE_EXISTS_CACHE_MAX entries with LRU eviction so a
// long-lived SPA session browsing many distinct paths can't grow the map
// without bound. A Map preserves insertion order, so re-inserting a key
// after `delete` moves it to the most-recently-used end; eviction drops
// from the oldest end via the first iterator entry.
//
// Negative results expire after FILE_EXISTS_NEG_TTL_MS so an agent that
// creates a file *after* the chip first rendered (common with write-tool
// responses) eventually becomes clickable without a full page reload.
// Positive results are not expired — a file that exists is safe to keep
// clickable indefinitely; if it's later deleted the click-through will
// just surface the error in the viewer.
const FILE_EXISTS_CACHE_MAX = 500;
const FILE_EXISTS_NEG_TTL_MS = 30_000;
type FileExistsEntry = { promise: Promise<boolean>; exists?: boolean; stampMs: number };
const fileExistsCache = new Map<string, FileExistsEntry>();
function touchFileExistsCache(key: string, value: FileExistsEntry) {
  fileExistsCache.delete(key);
  fileExistsCache.set(key, value);
  while (fileExistsCache.size > FILE_EXISTS_CACHE_MAX) {
    const oldest = fileExistsCache.keys().next().value;
    if (oldest === undefined) break;
    fileExistsCache.delete(oldest);
  }
}
function checkFileExists(resolvedPath: string): Promise<boolean> {
  const cached = fileExistsCache.get(resolvedPath);
  if (cached) {
    // In-flight: always reuse to dedupe concurrent fetches.
    // Positive: cache indefinitely.
    // Negative: reuse while fresh, re-query once the TTL is up so a
    // file that appears mid-session eventually becomes clickable.
    if (
      cached.exists === undefined ||
      cached.exists === true ||
      Date.now() - cached.stampMs < FILE_EXISTS_NEG_TTL_MS
    ) {
      touchFileExistsCache(resolvedPath, cached);
      return cached.promise;
    }
    fileExistsCache.delete(resolvedPath);
  }
  const entry: FileExistsEntry = { promise: Promise.resolve(false), stampMs: Date.now() };
  entry.promise = fetch(`/api/v1/file-exists?path=${encodeURIComponent(resolvedPath)}`)
    .then(res => res.json())
    .then(data => {
      const exists = Boolean(data.exists);
      entry.exists = exists;
      entry.stampMs = Date.now();
      return exists;
    })
    .catch(() => {
      // Drop the failed entry so a later retry (e.g. network recovered)
      // can re-query instead of sticking to the error forever. Identity
      // check: only delete when the cache still holds the same entry
      // that failed — an LRU eviction + re-insert sequence where a fresh
      // request replaced our entry would otherwise get clobbered when
      // the original request finally rejects.
      if (fileExistsCache.get(resolvedPath) === entry) {
        fileExistsCache.delete(resolvedPath);
      }
      return false;
    });
  touchFileExistsCache(resolvedPath, entry);
  return entry.promise;
}
function showToast(message: string) {
  const existing = document.querySelector('.copy-toast');
  if (existing) {
    existing.remove();
  }

  const toast = document.createElement('div');
  toast.className = 'copy-toast';
  toast.textContent = message;
  document.body.appendChild(toast);

  setTimeout(() => {
    toast.classList.add('show');
  }, 10);

  setTimeout(() => {
    toast.classList.remove('show');
    setTimeout(() => toast.remove(), 300);
  }, 2000);
}

interface MessageItemProps {
  message: Message;
  agentIconUrl?: string;
  agentDisplayName?: string;
  jobId?: string;
  workdir?: string;
  shareToken?: string;
}

/**
 * Check whether a string looks like a local file path that should be rendered as a FileChip.
 *
 * Basename (last path segment) must match ONE of:
 *  A) "name.ext" — name is non-empty, ext starts with a letter
 *     e.g. cli.js, README.md, .eslintrc.json, .quartet-ctrl-123.txt
 *  B) dotfile — starts with "." followed by a letter, then word chars / dashes
 *     e.g. .gitignore, .env, .quartet-ctrl-123
 *
 * Extension filter (rule A only) rejects version numbers (v2.1.96) and sizes (1.4K).
 * All candidates are further verified via the file-exists API at render time.
 *
 * Examples that MATCH:    cli.js, .gitignore, .env, .eslintrc.json, /path/.quartet-ctrl-123
 * Examples that DON'T:    v2.1.96, 1.4K, 18:32:46, Makefile (no ext, no dot prefix)
 */
function isLocalFileTarget(target: string): boolean {
  // strip line/column/anchor suffixes before checking
  const clean = target.replace(/[#:].*$/, '');
  const basename = clean.split('/').pop() || '';
  // Rule A: name.ext with letter-starting extension
  // Rule B: dotfile like .gitignore, .env
  const isDotfile = /^\.[a-zA-Z][\w.-]*$/.test(basename);
  const hasFileExt = /^.+\.[a-zA-Z]\w*$/.test(basename);
  if (!hasFileExt && !isDotfile) return false;
  if (target.startsWith('/') && !target.startsWith('//')) return true;
  if (target.startsWith('./') || target.startsWith('../')) return true;
  // bare dotfile (e.g. .gitignore, .env)
  if (isDotfile) return true;
  // bare relative with file extension (e.g. src/foo.go, foo.ts:10)
  if (/^[\w][\w./_-]*\.[a-zA-Z]+/.test(clean)) return true;
  return false;
}

function parseLocalFileTarget(target: string): { filePath: string; line?: number; endLine?: number; column?: number } {
  const hashMatch = target.match(/^(.*?)#L(\d+)(?:-L?(\d+))?(?:C(\d+))?$/);
  if (hashMatch) {
    return {
      filePath: hashMatch[1],
      line: Number(hashMatch[2]),
      endLine: hashMatch[3] ? Number(hashMatch[3]) : undefined,
      column: hashMatch[4] ? Number(hashMatch[4]) : undefined,
    };
  }
  const colonMatch = target.match(/^(.*?):(\d+)(?:-(\d+))?(?::(\d+))?$/);
  if (colonMatch) {
    return {
      filePath: colonMatch[1],
      line: Number(colonMatch[2]),
      endLine: colonMatch[3] ? Number(colonMatch[3]) : undefined,
      column: colonMatch[4] ? Number(colonMatch[4]) : undefined,
    };
  }
  return { filePath: target };
}

function normalizeLocalFileTarget(target: string): string {
  return target.replace(/#L\d+(-L?\d+)?(C\d+)?$/, '').replace(/:\d+(-\d+)?(:\d+)?$/, '');
}

function resolveFilePath(filePath: string, workdir: string): string {
  if (filePath.startsWith('/')) return filePath;
  if (!workdir) return filePath;
  const base = workdir.endsWith('/') ? workdir : workdir + '/';
  return base + filePath;
}

function extractFileName(target: string): string {
  const normalized = normalizeLocalFileTarget(target);
  const segments = normalized.split('/').filter(Boolean);
  return segments[segments.length - 1] || normalized;
}

function UserMessageContent({ message }: { message: UserMessage }) {
  const { t } = useTranslation();
  const userMsg = message as UserMessage;
  const imageUrls = userMsg.imageUrls;
  const timeStr = formatMessageTime(message.createdAt);
  // `pending` tracks optimistic-message reconciliation and can outlive the
  // HTTP request. deliveryStatus is the user-facing transport acknowledgement.
  // Fall back to the legacy flags for messages created by an older UI bundle.
  const isSending = userMsg.deliveryStatus === 'sending'
    || (!userMsg.deliveryStatus && !!userMsg.pending && !userMsg.failed);
  const isSendFailed = userMsg.deliveryStatus === 'failed' || !!userMsg.failed;
  const sendStatusText = isSending
    ? t('chat.messageSending')
    : userMsg.sendError
      ? t('chat.messageSendFailedDetail', { error: userMsg.sendError })
      : t('chat.messageSendFailed');
  return (
    <div className="message-item user-message" data-testid="message-item" data-message-id={message.id} data-message-role="user" data-session-id={message.sessionId || ''}>
      <div className="message-content">
        <div className="user-bubble-row">
          <div className="user-meta-col">
            <CopyMessageButton content={message.content} />
            {timeStr && <div className="message-timestamp user-timestamp">{timeStr}</div>}
          </div>
          <div className="message-bubble user-bubble">
            {imageUrls && imageUrls.length > 0 && (
              <div className="user-message-images">
                {imageUrls.map((url, i) => (
                  <MessageImage key={i} path={url} />
                ))}
              </div>
            )}
            <div className="markdown-content">
              {userMsg.isShellOutput
                ? <pre className="shell-content">{message.content}</pre>
                : renderMarkdown(message.content)}
            </div>
          </div>
        </div>
        {(isSending || isSendFailed) && (
          <div
            className={`user-send-status${isSendFailed ? ' failed' : ''}`}
            role={isSendFailed ? 'alert' : 'status'}
            aria-live={isSendFailed ? 'assertive' : 'polite'}
          >
            {isSending
              ? <span className="user-send-spinner" aria-hidden="true" />
              : <span className="user-send-failed-icon" aria-hidden="true">!</span>}
            <span>{sendStatusText}</span>
          </div>
        )}
      </div>
    </div>
  );
}

const MSG_AUTH_TOKEN_KEY = 'quartet.x_auth_token';

// Share-mode info (shareToken + jobId) is provided by MessageItem and consumed
// by MessageImage via context. Using context instead of a module-level variable
// keeps each MessageItem subtree isolated — critical when share-mode and
// normal-mode messages coexist on the same page or render in quick succession.
type ShareInfo = { shareToken: string; jobId: string };
const ShareInfoContext = createContext<ShareInfo | null>(null);

function buildMessageImageUrl(path: string, shareInfo: ShareInfo | null): string {
  if (path.startsWith('http://') || path.startsWith('https://') || path.startsWith('data:')) return path;
  if (shareInfo) {
    return `/api/v1/public/serve-file?path=${encodeURIComponent(path)}&shareToken=${encodeURIComponent(shareInfo.shareToken)}&jobId=${encodeURIComponent(shareInfo.jobId)}`;
  }
  const token = (localStorage.getItem(MSG_AUTH_TOKEN_KEY) ?? '').trim();
  let url = `/api/v1/serve-file?path=${encodeURIComponent(path)}`;
  if (token) url += `&token=${encodeURIComponent(token)}`;
  return url;
}

function readPixelCustomProperty(style: CSSStyleDeclaration, name: string, fallback: number): number {
  const value = Number.parseFloat(style.getPropertyValue(name));
  return Number.isFinite(value) && value > 0 ? value : fallback;
}

function getMessageImageLayoutWidth(img: HTMLImageElement): number | null {
  const { naturalWidth, naturalHeight } = img;
  if (!naturalWidth || !naturalHeight) return null;

  const style = window.getComputedStyle(img);
  const maxWidth = readPixelCustomProperty(style, '--message-image-max-width', 480);
  const cssMaxHeight = readPixelCustomProperty(style, '--message-image-max-height', 320);
  const maxHeight = Math.min(cssMaxHeight, window.innerHeight * 0.6);
  const scale = Math.min(1, maxWidth / naturalWidth, maxHeight / naturalHeight);

  return Math.max(1, Math.round(naturalWidth * scale));
}

function MessageImage({ path, alt }: { path: string; alt?: string }) {
  const shareInfo = useContext(ShareInfoContext);
  const [failed, setFailed] = useState(false);
  const [layoutWidth, setLayoutWidth] = useState<number | null>(null);
  const url = buildMessageImageUrl(path, shareInfo);

  useEffect(() => {
    setLayoutWidth(null);
  }, [url]);

  if (failed) {
    // Show inline placeholder with file name when image cannot be loaded
    // (e.g. 403 in share mode for workdir paths).
    const fileName = path.split('/').pop() || path;
    return (
      <span className="message-image-placeholder" title={path}>
        🖼 {fileName}
      </span>
    );
  }

  return (
    <img
      src={url}
      alt={alt || ''}
      className="user-message-image"
      style={layoutWidth == null ? undefined : { width: `${layoutWidth}px` }}
      // The serve-file URL carries the auth token as a query string. Without
      // an explicit no-referrer policy the browser sends the full URL (token
      // and all) to any third-party domain the image links to, e.g. if the
      // user clicks through into another origin. The flag also prevents the
      // token leaking into access logs of cross-origin redirects.
      referrerPolicy="no-referrer"
      onLoad={(event) => {
        setLayoutWidth(getMessageImageLayoutWidth(event.currentTarget));
      }}
      onError={() => setFailed(true)}
      onClick={() => window.open(url, '_blank', 'noopener,noreferrer')}
    />
  );
}

function CopyMessageButton({ content }: { content: string }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback((e: React.MouseEvent) => {
    e.stopPropagation();
    copyToClipboard(content).then(() => {
      setCopied(true);
      showToast('复制成功');
      setTimeout(() => setCopied(false), 2000);
    }).catch(() => {
      showToast('复制失败');
    });
  }, [content]);

  return (
    <button className="assistant-copy-btn" onClick={handleCopy} title="复制">
      {copied ? (
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
          <polyline points="20 6 9 17 4 12" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
          <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
        </svg>
      )}
    </button>
  );
}

function CopyMessageFooterButton({ content }: { content: string }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback((e: React.MouseEvent) => {
    e.stopPropagation();
    copyToClipboard(content).then(() => {
      setCopied(true);
      showToast('复制成功');
      setTimeout(() => setCopied(false), 2000);
    }).catch(() => {
      showToast('复制失败');
    });
  }, [content]);

  return (
    <button
      className="message-copy-footer-btn"
      onClick={handleCopy}
      title="复制消息内容"
      aria-label={copied ? '已复制' : '复制消息内容'}
    >
      {copied ? (
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
          <polyline points="20 6 9 17 4 12" />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
          <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
        </svg>
      )}
    </button>
  );
}

function AssistantMessageContent({ message, agentIconUrl, agentDisplayName }: { message: AssistantMessage; agentIconUrl?: string; agentDisplayName?: string }) {
  const { t } = useTranslation();
  const messageRef = useRef<HTMLDivElement | null>(null);
  const [showFooterCopyButton, setShowFooterCopyButton] = useState(false);
  const rawContent = useMemo(() => {
    const parts: string[] = [];
    if (message.thinkingContent) parts.push(message.thinkingContent);
    if (message.content?.trim()) parts.push(message.content);
    return parts.join('\n\n');
  }, [message.thinkingContent, message.content]);

  const isStreaming = message.status === MessageStatusEnum.Started;
  const shouldMeasureOverflow = !isStreaming && !!rawContent;
  const [prevShouldMeasureOverflow, setPrevShouldMeasureOverflow] = useState(shouldMeasureOverflow);
  // Reset the "tall message" indicator the moment we stop measuring (e.g.
  // new streaming round, content cleared). Doing this during render keeps
  // the reset out of useEffect and avoids a cascading render warning.
  if (prevShouldMeasureOverflow !== shouldMeasureOverflow) {
    setPrevShouldMeasureOverflow(shouldMeasureOverflow);
    if (!shouldMeasureOverflow) setShowFooterCopyButton(false);
  }

  useEffect(() => {
    if (!shouldMeasureOverflow) return;

    const measure = () => {
      const height = messageRef.current?.getBoundingClientRect().height ?? 0;
      setShowFooterCopyButton(height > window.innerHeight);
    };

    const rafId = window.requestAnimationFrame(measure);
    window.addEventListener('resize', measure);

    let observer: ResizeObserver | null = null;
    if (typeof ResizeObserver !== 'undefined' && messageRef.current) {
      observer = new ResizeObserver(() => measure());
      observer.observe(messageRef.current);
    }

    return () => {
      window.cancelAnimationFrame(rafId);
      window.removeEventListener('resize', measure);
      observer?.disconnect();
    };
  }, [shouldMeasureOverflow]);

  const timeStr = formatMessageTime(message.createdAt);

  // An assistant message with neither thinking content nor body content has
  // nothing to show. Rendering it anyway produces an empty .message-item that
  // is invisible (height 0) yet still contributes its 16px bottom margin. Many
  // such empty rows accumulate between thinking blocks and inflate the spacing
  // between them, so render nothing in that case.
  if (!message.thinkingContent && !message.content?.trim()) {
    return null;
  }

  return (
    <div
      className="message-item assistant-message"
      ref={messageRef}
      data-testid="message-item"
      data-message-id={message.id}
      data-message-role="assistant"
      data-message-status={message.status || ''}
      data-session-id={message.sessionId || ''}
    >
      <div className="assistant-content-wrapper">
        {message.thinkingContent && (
          <div className="thinking-block" data-testid="message-thinking-block">
            <div className="thinking-header">
              <span className="thinking-icon">💭</span>
              <span>{t('chat.deepThinking')}</span>
              {message.isThinking && <span className="thinking-indicator" />}
              <DurationBadge
                // Backward-compat: old history messages may miss `thinkingFinishedAt`.
                // Only show a running badge while the message is actively thinking.
                startedAt={message.isThinking || message.thinkingFinishedAt != null ? message.createdAt : undefined}
                endedAt={message.thinkingFinishedAt}
                variant="thinking"
              />
            </div>
            <div className="thinking-content markdown-content">
              {renderMarkdown(message.thinkingContent)}
            </div>
          </div>
        )}
        {message.content?.trim() && (
          <div className="assistant-bubble" data-testid="assistant-message-bubble">
            <div className="assistant-bubble-header">
              {message.isShellOutput ? (
                <span className="assistant-bubble-icon">💻</span>
              ) : agentIconUrl ? (
                isImageUrl(agentIconUrl)
                  ? <img src={agentIconUrl} alt="" className="assistant-bubble-icon-img" referrerPolicy="no-referrer" />
                  : <span className="assistant-bubble-icon">{agentIconUrl}</span>
              ) : (
                <span className="assistant-bubble-icon">✨</span>
              )}
              <span className="assistant-bubble-name">{message.isShellOutput ? 'Shell' : (agentDisplayName || 'ASSISTANT')}</span>
              {isStreaming && (
                <span className="thinking-indicator" />
              )}
              <DurationBadge
                // Start = thinkingFinishedAt when available so the body
                // span excludes the thought segment; falls back to
                // createdAt for messages that had no thinking phase.
                //
                // Legacy history rows may carry thinking content without
                // a thinkingFinishedAt field (pre-feature data). In that
                // case the fallback would silently fold the thought time
                // into the body, overstating the assistant bubble. Treat
                // "field missing = do not render" so history replay does
                // not show a misleading duration rather than show one
                // that double-counts the thought phase.
                startedAt={(() => {
                  if (isStreaming) return message.thinkingFinishedAt ?? message.createdAt;
                  if (message.finishedAt == null) return undefined;
                  const hasThinking = !!message.thinkingContent;
                  if (hasThinking && message.thinkingFinishedAt == null) return undefined;
                  return message.thinkingFinishedAt ?? message.createdAt;
                })()}
                endedAt={message.finishedAt}
                variant="assistant"
              />
              {timeStr && !isStreaming && <span className="message-timestamp assistant-timestamp">{timeStr}</span>}
              {!isStreaming && rawContent && (
                <CopyMessageButton content={rawContent} />
              )}
            </div>
            <div className="markdown-content">
              {message.isShellOutput
                ? <pre className="shell-content">{message.content}</pre>
                : renderMarkdown(message.content)}
              {isStreaming && (
                <span className="typing-indicator" />
              )}
            </div>
            {!isStreaming && rawContent && showFooterCopyButton && (
              <div className="assistant-bubble-actions">
                <CopyMessageFooterButton content={rawContent} />
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function sanitizeToolName(name: string | undefined | null): string {
  if (!name || name === 'undefined' || name === 'null') return '';
  return name;
}

function getToolIcon(toolName: string): string {
  const iconMap: Record<string, string> = {
    Agent: '🤖',
    Read: '📖',
    Edit: '✏️',
    Write: '📝',
    Glob: '🗂️',
    Grep: '🔍',
    WebSearch: '🌐',
    WebFetch: '⬇️',
    Bash: '💻',
    Terminal: '💻',
    Task: '📝',
    TaskOutput: '📤',
    TaskStop: '🛑',
    TaskCreate: '📝',
    TaskGet: '🔎',
    TaskUpdate: '🛠️',
    TaskList: '📋',
    EnterPlanMode: '🗺️',
    ExitPlanMode: '🚪',
    NotebookEdit: '📓',
    AskUserQuestion: '❓',
    Skill: '🧠',
    LSP: '🧩',
    EnterWorktree: '🌿',
    ExitWorktree: '🍂',
    TeamCreate: '👥➕',
    TeamDelete: '👥❌',
    SendMessage: '✉️',
    CronCreate: '⏰',
    CronDelete: '⏰',
    CronList: '⏰',
    browser_click: '🖱️',
    browser_evaluate: '⚙️',
    browser_get_html: '📄',
    browser_get_page_info: 'ℹ️',
    browser_get_title: '📑',
    browser_get_url: '🔗',
    browser_navigate: '🧭',
    browser_pdf: '📋',
    browser_screenshot: '📸',
    browser_scroll: '📜',
    browser_type: '⌨️',
    browser_wait_visible: '👁️',
  };
  if (iconMap[toolName]) return iconMap[toolName];
  const matchedKey = Object.keys(iconMap).find((key) => toolName.startsWith(key));
  return matchedKey ? iconMap[matchedKey] : '💻';
}

function looksLikeMarkdown(text: string): boolean {
  if (!text || text.length < 3) return false;
  const mdPatterns = [
    /^#{1,6}\s+/m,           // headings
    /\*\*.+?\*\*/,           // bold
    /^[-*+]\s+/m,            // unordered list
    /^\d+\.\s+/m,            // ordered list
    /^```/m,                 // code fence
    /\[.+?\]\(.+?\)/,        // links
  ];
  let matchCount = 0;
  for (const p of mdPatterns) {
    if (p.test(text)) matchCount++;
  }
  return matchCount >= 1;
}

function renderJsonHighlighted(obj: object): React.ReactNode[] {
  const json = JSON.stringify(obj, null, 2);
  const nodes: React.ReactNode[] = [];
  // Tokenize JSON string for highlighting
  const tokenRegex = /("(?:\\.|[^"\\])*"\s*:\s*)|("(?:\\.|[^"\\])*")|(\b(?:true|false|null)\b)|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|([{}[\],])|(\s+)/g;
  let match: RegExpExecArray | null;
  let i = 0;

  while ((match = tokenRegex.exec(json)) !== null) {
    const [fullMatch] = match;
    if (match[1]) {
      // key: value pair - split key from colon
      const colonIdx = fullMatch.lastIndexOf(':');
      const key = fullMatch.slice(0, colonIdx);
      const colon = fullMatch.slice(colonIdx);
      nodes.push(<span key={i++} className="json-key">{key}</span>);
      nodes.push(<span key={i++} className="json-punctuation">{colon}</span>);
    } else if (match[2]) {
      nodes.push(<span key={i++} className="json-string">{fullMatch}</span>);
    } else if (match[3]) {
      nodes.push(<span key={i++} className="json-boolean">{fullMatch}</span>);
    } else if (match[4]) {
      nodes.push(<span key={i++} className="json-number">{fullMatch}</span>);
    } else if (match[5]) {
      nodes.push(<span key={i++} className="json-punctuation">{fullMatch}</span>);
    } else {
      nodes.push(fullMatch);
    }
  }

  return nodes;
}

// While a tool call is still streaming, `toolCallArgs` is an unterminated JSON
// fragment (an open string, a half-written object). `JSON.parse` throws on it
// and the UI used to fall back to dumping the raw escaped string — the giant
// one-line blob with literal `\n`. Best-effort close the open string and any
// unbalanced brackets so we can still parse it into a structured object mid
// stream. Rare tails we can't repair (a bare key with no colon yet) just fail
// to parse and fall back to the raw string, same as before.
function completePartialJson(raw: string): string {
  const closers: string[] = [];
  let inString = false;
  let escaped = false;
  for (let i = 0; i < raw.length; i++) {
    const ch = raw[i];
    if (inString) {
      if (escaped) escaped = false;
      else if (ch === '\\') escaped = true;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === '"') inString = true;
    else if (ch === '{') closers.push('}');
    else if (ch === '[') closers.push(']');
    else if (ch === '}' || ch === ']') closers.pop();
  }
  let s = raw;
  if (escaped) s = s.slice(0, -1); // dangling backslash would escape our closing quote
  if (inString) s += '"';
  s = s.replace(/\s+$/, '');
  if (s.endsWith(',')) s = s.slice(0, -1); // JSON forbids trailing commas
  if (s.endsWith(':')) s += 'null'; // key with no value yet
  for (let i = closers.length - 1; i >= 0; i--) s += closers[i];
  return s;
}

// Parse tool-call args tolerantly. Returns the parsed value plus whether it
// came from a partial (still-streaming) fragment, so callers can decide how
// much to trust it. Non-object results (a bare string being streamed) stay as
// the raw string — the field view only kicks in for objects.
function parseToolArgs(raw: string | undefined | null): { value: string | object | null; partial: boolean } {
  if (!raw) return { value: null, partial: false };
  try {
    return { value: JSON.parse(raw), partial: false };
  } catch {
    // fall through to partial repair
  }
  try {
    const val = JSON.parse(completePartialJson(raw));
    if (val && typeof val === 'object') return { value: val, partial: true };
  } catch {
    // give up, show the raw fragment
  }
  return { value: raw, partial: true };
}

// A string value renders as a multi-line block (rather than inline) when it
// carries real structure worth its own row: embedded newlines, or just long
// enough that inlining would wrap awkwardly.
const PARAM_BLOCK_MIN_LEN = 80;
function isBlockString(val: string): boolean {
  return val.includes('\n') || val.length > PARAM_BLOCK_MIN_LEN;
}

function renderScalarValue(val: unknown): React.ReactNode {
  if (val === null) return <span className="tool-param-null">null</span>;
  if (typeof val === 'boolean') return <span className="json-boolean">{String(val)}</span>;
  if (typeof val === 'number') return <span className="json-number">{String(val)}</span>;
  return <span className="tool-param-str">{String(val)}</span>;
}

function ToolParamField({ name, value }: { name: string; value: unknown }) {
  // Multi-line / long string → labelled block with real newlines (the actual
  // string value, not JSON-escaped), so Write.content, Edit diffs and multi
  // line shell commands read like the text they are.
  if (typeof value === 'string' && isBlockString(value)) {
    return (
      <div className="tool-param-field block">
        <div className="tool-param-key">{name}</div>
        <pre className="tool-param-block-value">{value}</pre>
      </div>
    );
  }
  // Nested object / array → compact JSON with the existing highlighter.
  if (value !== null && typeof value === 'object') {
    return (
      <div className="tool-param-field block">
        <div className="tool-param-key">{name}</div>
        <pre className="tool-param-block-value tool-json-code">{renderJsonHighlighted(value as object)}</pre>
      </div>
    );
  }
  // Short scalar → single inline row.
  return (
    <div className="tool-param-field inline">
      <span className="tool-param-key">{name}</span>
      <span className="tool-param-value">{renderScalarValue(value)}</span>
    </div>
  );
}

function ToolParamsView({ args }: { args: object }) {
  const entries = Object.entries(args);
  if (entries.length === 0) {
    return <pre className="tool-param-block-value">{'{}'}</pre>;
  }
  return (
    <div className="tool-params">
      {entries.map(([key, val]) => (
        <ToolParamField key={key} name={key} value={val} />
      ))}
    </div>
  );
}

const LARGE_TOOL_PAYLOAD_THRESHOLD = 100_000;
const TOOL_PAYLOAD_PREVIEW_CHARS = 32_000;

function LargeToolPayload({
  content,
  label,
}: {
  content: string;
  label: '参数' | '结果';
}) {
  const [showFull, setShowFull] = useState(false);
  const visibleContent = showFull ? content : content.slice(0, TOOL_PAYLOAD_PREVIEW_CHARS);
  const handleCopy = (e: React.MouseEvent) => {
    e.stopPropagation();
    copyToClipboard(content).then(() => {
      showToast('复制成功');
    }).catch(() => {
      showToast('复制失败');
    });
  };

  return (
    <>
      <div className="tool-large-payload-notice">
        <span>
          {showFull
            ? `正在显示完整${label}（${content.length.toLocaleString()} 字符）`
            : `${label}较大，预览前 ${visibleContent.length.toLocaleString()} / ${content.length.toLocaleString()} 字符`}
        </span>
        <button
          type="button"
          className="tool-large-payload-toggle"
          onClick={() => setShowFull((value) => !value)}
        >
          {showFull ? '收起完整内容' : '显示完整内容'}
        </button>
      </div>
      <div className="tool-code-wrapper result-wrapper">
        <pre className="tool-code result-code">{visibleContent}</pre>
        <button className="copy-btn" onClick={handleCopy} title={`复制完整${label}`}>
          <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
            <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
          </svg>
        </button>
      </div>
    </>
  );
}

function ToolMessageDetails({ message }: { message: ToolMessage }) {
  const toolCallArgs = message.toolCallArgs || '';
  const resultContent = message.content || '';
  const argsAreLarge = toolCallArgs.length > LARGE_TOOL_PAYLOAD_THRESHOLD;
  // Repository convention requires full error details. Error cards still
  // start collapsed, but once opened they render the complete result.
  const resultIsLarge = message.toolCallStatus !== ToolCallStatusEnum.Error
    && resultContent.length > LARGE_TOOL_PAYLOAD_THRESHOLD;
  const parsedArgs = argsAreLarge ? null : parseToolArgs(toolCallArgs).value;

  let parsedResult: string | object | null = null;
  if (!resultIsLarge) {
    try {
      parsedResult = resultContent ? JSON.parse(resultContent) : null;
    } catch {
      parsedResult = resultContent;
    }
  }

  const handleCopy = (text: string, e: React.MouseEvent) => {
    e.stopPropagation();
    copyToClipboard(text).then(() => {
      showToast('复制成功');
    }).catch(() => {
      showToast('复制失败');
    });
  };

  const argsStr = parsedArgs
    ? typeof parsedArgs === 'string'
      ? parsedArgs
      : JSON.stringify(parsedArgs, null, 2)
    : '';
  const resultStr = parsedResult
    ? typeof parsedResult === 'string'
      ? parsedResult
      : JSON.stringify(parsedResult, null, 2)
    : '';
  const argsIsJson = parsedArgs !== null && typeof parsedArgs === 'object';
  const resultIsJson = parsedResult !== null && typeof parsedResult === 'object';
  const resultIsMarkdown = !resultIsJson
    && typeof parsedResult === 'string'
    && message.toolCallStatus !== ToolCallStatusEnum.Error
    && looksLikeMarkdown(parsedResult);

  return (
    <>
      {toolCallArgs && (
        <div className="tool-section" data-testid="tool-call-parameters">
          <div className="tool-section-title">
            PARAMETERS
            {argsIsJson && <span className="tool-content-type-badge json-badge">JSON</span>}
          </div>
          {argsAreLarge ? (
            <LargeToolPayload content={toolCallArgs} label="参数" />
          ) : (
            <div className="tool-code-wrapper">
              {argsIsJson ? (
                <ToolParamsView args={parsedArgs as object} />
              ) : (
                <pre className="tool-code">{argsStr}</pre>
              )}
              <button className="copy-btn" onClick={(e) => handleCopy(argsStr, e)} title="复制">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
              </button>
            </div>
          )}
        </div>
      )}

      {resultContent && (
        <div className="tool-section" data-testid="tool-call-result">
          <div className="tool-section-title">
            RESULT
            {resultIsJson && <span className="tool-content-type-badge json-badge">JSON</span>}
            {resultIsMarkdown && <span className="tool-content-type-badge md-badge">MD</span>}
          </div>
          {resultIsLarge ? (
            <LargeToolPayload content={resultContent} label="结果" />
          ) : resultIsMarkdown ? (
            <div className="tool-markdown-result">
              <div className="markdown-content">{renderMarkdown(resultStr)}</div>
              <button className="copy-btn" onClick={(e) => handleCopy(resultStr, e)} title="复制">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
                </svg>
              </button>
            </div>
          ) : (
            <div className="tool-code-wrapper result-wrapper">
              {resultIsJson ? (
                <pre className="tool-code result-code tool-json-code">{renderJsonHighlighted(parsedResult as object)}</pre>
              ) : (
                <pre className="tool-code result-code">{resultStr}</pre>
              )}
              <button className="copy-btn" onClick={(e) => handleCopy(resultStr, e)} title="复制">
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
                  <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2 2v1"/>
                </svg>
              </button>
            </div>
          )}
        </div>
      )}
    </>
  );
}

function ToolMessageContent({ message }: { message: ToolMessage }) {
  const isCompleted = message.toolCallStatus !== ToolCallStatusEnum.Processing;
  const payloadIsLarge = (message.toolCallArgs?.length ?? 0) > LARGE_TOOL_PAYLOAD_THRESHOLD
    || (message.content?.length ?? 0) > LARGE_TOOL_PAYLOAD_THRESHOLD;
  const [isExpanded, setIsExpanded] = useState(!isCompleted && !payloadIsLarge);
  // Auto-collapse on Processing → terminal transition via "derived state on
  // prop change" (see https://react.dev/reference/react/useState — updating
  // state during render). This replaces the old key-based remount trick
  // that co-located "default state per status" with component identity;
  // the remount defeated DurationBadge's monotonic-clamp (maxShown) and
  // caused the visible "3m → 300ms" jump on completion. Using a render-time
  // check instead of useEffect avoids an extra commit/re-render cycle.
  const [prevStatus, setPrevStatus] = useState(message.toolCallStatus);
  const [previouslyLarge, setPreviouslyLarge] = useState(payloadIsLarge);
  const becameTerminal = prevStatus === ToolCallStatusEnum.Processing
    && message.toolCallStatus !== ToolCallStatusEnum.Processing;
  const becameLarge = !previouslyLarge && payloadIsLarge;
  if (prevStatus !== message.toolCallStatus) {
    setPrevStatus(message.toolCallStatus);
    if (becameTerminal) {
      setIsExpanded(false);
    }
  }
  if (previouslyLarge !== payloadIsLarge) {
    setPreviouslyLarge(payloadIsLarge);
    if (becameLarge) {
      setIsExpanded(false);
    }
  }
  // A terminal result can arrive as a single megabyte-sized event. The state
  // updates above schedule the collapse, while this derived value prevents the
  // current render from parsing and mounting that payload before React retries.
  const detailsExpanded = isExpanded && !becameTerminal && !becameLarge;
  const toolName = sanitizeToolName(message.toolCallName);
  const shouldShowDuration = message.toolCallStatus === ToolCallStatusEnum.Processing || message.finishedAt != null;

  const statusLabel: Record<ToolCallStatusEnum, string> = {
    [ToolCallStatusEnum.Processing]: 'Running',
    [ToolCallStatusEnum.Success]: 'Completed',
    [ToolCallStatusEnum.Error]: 'Error',
    // Placeholder: the run ended before the tool produced a real result,
    // so the result on disk is a synthetic marker — surface the reason
    // (canceled / interrupted / superseded) in the tooltip so users know
    // why this bubble is neither green nor red.
    [ToolCallStatusEnum.Placeholder]: message.placeholderReason
      ? `Incomplete (${message.placeholderReason})`
      : 'Incomplete',
  };

  const statusClass: Record<ToolCallStatusEnum, string> = {
    [ToolCallStatusEnum.Processing]: 'processing',
    [ToolCallStatusEnum.Success]: 'success',
    [ToolCallStatusEnum.Error]: 'error',
    [ToolCallStatusEnum.Placeholder]: 'placeholder',
  };

  return (
    <div
      className="message-item tool-message"
      data-testid="message-item"
      data-message-id={message.id}
      data-message-role="tool"
      data-tool-name={toolName}
      data-tool-status={statusClass[message.toolCallStatus]}
      data-session-id={message.sessionId || ''}
    >
      <div className="message-content">
        <div className={`tool-call-card ${detailsExpanded ? 'expanded' : 'collapsed'}`} data-testid="tool-call-block" data-expanded={detailsExpanded ? 'true' : 'false'}>
          <div className="tool-call-header" onClick={() => setIsExpanded((value) => !value)} data-testid="tool-call-header">
            <span className="tool-icon">{getToolIcon(toolName)}</span>
            <span className="tool-name" data-testid="tool-call-name">{toolName || 'Tool'}</span>
            <DurationBadge
              // Backward-compat: old history tool messages may miss `finishedAt`.
              // Only show a running badge while the tool is actively processing.
              startedAt={shouldShowDuration ? message.createdAt : undefined}
              endedAt={message.finishedAt}
              variant="tool"
            />
            <span
              className={`tool-status-badge ${statusClass[message.toolCallStatus]}`}
              title={statusLabel[message.toolCallStatus]}
              aria-label={statusLabel[message.toolCallStatus]}
              data-testid="tool-call-status"
            >
              {message.toolCallStatus === ToolCallStatusEnum.Processing && (
                <svg className="status-spinner" viewBox="0 0 16 16" fill="none">
                  <circle cx="8" cy="8" r="5.5" stroke="currentColor" strokeWidth="2" opacity="0.25" />
                  <path d="M8 2.5A5.5 5.5 0 0 1 13.5 8" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
                </svg>
              )}
              {message.toolCallStatus === ToolCallStatusEnum.Success && (
                <svg className="status-check" viewBox="0 0 12 12" fill="none">
                  <path d="M10 3L4.5 8.5L2 6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/>
                </svg>
              )}
              {message.toolCallStatus === ToolCallStatusEnum.Error && (
                <svg className="status-error" viewBox="0 0 12 12" fill="none">
                  <path d="M3 3L9 9M9 3L3 9" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
                </svg>
              )}
              {message.toolCallStatus === ToolCallStatusEnum.Placeholder && (
                <svg className="status-placeholder" viewBox="0 0 12 12" fill="none">
                  <path d="M3 6h6" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
                </svg>
              )}
            </span>
            <span className="expand-icon">
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2">
                <polyline points={detailsExpanded ? "18 15 12 9 6 15" : "6 9 12 15 18 9"} />
              </svg>
            </span>
          </div>

          {detailsExpanded && <ToolMessageDetails message={message} />}

          {message.toolCallStatus === ToolCallStatusEnum.Processing && (
            <div className="tool-loading">
              <div className="tool-loading-bar" />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// Schemes we are willing to expose via a clickable href. Anything else —
// javascript:, data:, vbscript:, file:, blob:, etc. — is rendered as
// plain text so a model-generated message can't smuggle a script trigger
// into the UI via <a href="javascript:...">.
const SAFE_LINK_SCHEMES = new Set(['http', 'https', 'mailto']);

function isSafeLinkUrl(url: string): boolean {
  // Relative URLs (no scheme) are fine — they resolve against the current
  // document origin. Only gate absolute URLs.
  const match = url.match(/^([a-zA-Z][a-zA-Z0-9+.-]*):/);
  if (!match) return true;
  return SAFE_LINK_SCHEMES.has(match[1].toLowerCase());
}

// Bare file-path regex (same shape as the previous custom parser).
// Walks a plain-text node and emits <FileChip> for literal paths like
// `src/foo.go:10-20` or `/abs/path#L5`.
const LOCAL_PATH_RE = /((?:\.{1,2}\/)?(?:\/)?(?:[\w./_-]+\.[a-zA-Z][\w]*|[\w./_-]*\.[a-zA-Z][\w.-]*)(?::\d+(?:-\d+)?(?::\d+)?)?(?:#L\d+(?:-L?\d+)?(?:C\d+)?)?)/g;

function splitTextForFilePaths(text: string, keyPrefix: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let lastIndex = 0;
  let chipIdx = 0;
  LOCAL_PATH_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = LOCAL_PATH_RE.exec(text)) !== null) {
    const raw = m[1];
    if (!isLocalFileTarget(raw)) continue;
    if (m.index > lastIndex) out.push(text.slice(lastIndex, m.index));
    const filePath = normalizeLocalFileTarget(raw);
    const fileName = extractFileName(raw);
    const { line, endLine } = parseLocalFileTarget(raw);
    out.push(
      <FileChip
        key={`${keyPrefix}-fpath-${chipIdx++}`}
        filePath={filePath}
        fileName={fileName}
        rawText={raw}
        line={line}
        endLine={endLine}
      />,
    );
    lastIndex = m.index + raw.length;
  }
  if (lastIndex < text.length) out.push(text.slice(lastIndex));
  return out.length > 0 ? out : [text];
}

function processTextChildren(children: React.ReactNode, keyPrefix: string): React.ReactNode {
  return React.Children.map(children, (child, idx): React.ReactNode => {
    if (typeof child === 'string') {
      const nodes = splitTextForFilePaths(child, `${keyPrefix}-${idx}`);
      return nodes.length === 1 ? nodes[0] : <>{nodes}</>;
    }
    return child;
  });
}

function extractLinkText(node: React.ReactNode): string {
  if (node == null || typeof node === 'boolean') return '';
  if (typeof node === 'string') return node;
  if (typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(extractLinkText).join('');
  if (React.isValidElement(node)) {
    const el = node as React.ReactElement<{ children?: React.ReactNode }>;
    return extractLinkText(el.props.children);
  }
  return '';
}

const MD_COMPONENTS: Components = {
  img: ({ src, alt }) => (
    <MessageImage path={typeof src === 'string' ? src : ''} alt={alt || ''} />
  ),
  a: ({ href, children }) => {
    const url = String(href || '').replace(/\\\//g, '/');
    if (isLocalFileTarget(url)) {
      const filePath = normalizeLocalFileTarget(url);
      const { line, endLine } = parseLocalFileTarget(url);
      const fileName = extractLinkText(children) || extractFileName(url);
      return <FileChip filePath={filePath} fileName={fileName} rawText={url} line={line} endLine={endLine} />;
    }
    if (!isSafeLinkUrl(url)) return <span>{children}</span>;
    return (
      <a href={url} target="_blank" rel="noopener noreferrer" className="inline-link">
        {children}
      </a>
    );
  },
  // `node.position.start.line !== node.position.end.line` distinguishes
  // block code (```-fenced, spans multiple lines) from inline code (single
  // line, never contains \n per CommonMark). Fallback to the `language-*`
  // className only covers fenced blocks with an explicit language.
  code: ({ className, children, node }) => {
    const pos = node?.position;
    const isBlock = pos
      ? pos.start.line !== pos.end.line
      : !!(className && /\blanguage-/.test(className));
    if (isBlock) return <code className={className}>{children}</code>;
    return <code className="inline-code">{children}</code>;
  },
  pre: ({ children }) => <pre className="markdown-code-block">{children}</pre>,
  h1: ({ children }) => <h1 className="markdown-heading markdown-h1">{processTextChildren(children, 'h1')}</h1>,
  h2: ({ children }) => <h2 className="markdown-heading markdown-h2">{processTextChildren(children, 'h2')}</h2>,
  h3: ({ children }) => <h3 className="markdown-heading markdown-h3">{processTextChildren(children, 'h3')}</h3>,
  h4: ({ children }) => <h4 className="markdown-heading markdown-h4">{processTextChildren(children, 'h4')}</h4>,
  h5: ({ children }) => <h5 className="markdown-heading markdown-h5">{processTextChildren(children, 'h5')}</h5>,
  h6: ({ children }) => <h6 className="markdown-heading markdown-h6">{processTextChildren(children, 'h6')}</h6>,
  p: ({ children }) => <p>{processTextChildren(children, 'p')}</p>,
  li: ({ children }) => <li>{processTextChildren(children, 'li')}</li>,
  td: ({ children }) => <td>{processTextChildren(children, 'td')}</td>,
  th: ({ children }) => <th>{processTextChildren(children, 'th')}</th>,
  strong: ({ children }) => <strong>{processTextChildren(children, 'strong')}</strong>,
  em: ({ children }) => <em>{processTextChildren(children, 'em')}</em>,
  del: ({ children }) => <del>{processTextChildren(children, 'del')}</del>,
  table: ({ children }) => (
    <div className="markdown-table-wrapper">
      <table>{children}</table>
    </div>
  ),
};

function renderMarkdown(content: string): React.ReactElement {
  return (
    <ReactMarkdown remarkPlugins={[remarkGfm]} components={MD_COMPONENTS}>
      {content}
    </ReactMarkdown>
  );
}

function FileChip({ filePath, fileName, rawText, line, endLine }: { filePath: string; fileName: string; rawText: string; line?: number; endLine?: number }) {
  const openFileViewer = useContext(FileViewerContext);
  const workdir = useContext(WorkdirContext);
  const shareInfo = useContext(ShareInfoContext);
  const resolvedPath = resolveFilePath(filePath, workdir);
  const isAbsolute = resolvedPath.startsWith('/');
  const [exists, setExists] = useState<boolean | null>(null);

  useEffect(() => {
    // In share mode, file-exists API is not available — render as plain text.
    if (shareInfo) return;
    // A relative path means workdir was empty so resolveFilePath could not
    // produce an absolute path. The backend file APIs reject relative paths
    // (400 "path must be absolute"), so don't probe — render as plain text
    // instead of firing a request that can only fail.
    if (!isAbsolute) return;
    let cancelled = false;
    checkFileExists(resolvedPath).then(ok => {
      if (!cancelled) setExists(ok);
    });
    return () => { cancelled = true; };
  }, [resolvedPath, isAbsolute, shareInfo]);

  // While loading, render as plain text to avoid flash; if not a file, stay as
  // text. Render the full path the user typed (rawText), not just the basename,
  // so a non-existent file still shows its complete relative/absolute path.
  if (exists !== true) return <>{rawText}</>;

  return (
    <button
      type="button"
      className="file-link-chip"
      title={resolvedPath}
      onClick={() => {
        if (openFileViewer) {
          openFileViewer(resolvedPath, line, endLine);
        } else {
          copyToClipboard(resolvedPath).then(() => showToast(`已复制路径: ${fileName}`)).catch(() => showToast('复制路径失败'));
        }
      }}
    >
      <span className="file-link-icon">
        <svg viewBox="0 0 16 16" width="1em" height="1em" fill="currentColor">
          <path d="M3.5 1A1.5 1.5 0 0 0 2 2.5v11A1.5 1.5 0 0 0 3.5 15h9a1.5 1.5 0 0 0 1.5-1.5V5.414a1 1 0 0 0-.293-.707l-3.414-3.414A1 1 0 0 0 9.586 1H3.5Zm0 1h5.586L13 5.914V13.5a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5v-11a.5.5 0 0 1 .5-.5Z"/>
        </svg>
      </span>
      <span className="file-link-label">{fileName}</span>
    </button>
  );
}

interface ViewingFileState {
  path: string;
  name: string;
  content: string;
  line?: number;
  endLine?: number;
  size: number;
  truncated: boolean;
  binary: boolean;
  loading: boolean;
  error?: string;
}

function HighlightedLine({ line, lang }: { line: string; lang: string | null }) {
  const tokens = tokenizeLine(line, lang);
  if (tokens.length === 1 && tokens[0].type === null) {
    return <>{line || '\u00A0'}</>;
  }
  return (
    <>
      {tokens.map((tok, i) =>
        tok.type ? (
          <span key={i} className={`hl-${tok.type}`}>{tok.value}</span>
        ) : (
          tok.value
        )
      )}
      {line === '' && '\u00A0'}
    </>
  );
}

function FileViewerModal({ file, jobId, onClose }: { file: ViewingFileState; jobId?: string; onClose: () => void }) {
  const lineNumbers = file.content ? file.content.split('\n') : [];
  const scrolledRef = useRef(false);
  const lang = detectLanguage(file.path);
  const langLabel = getLanguageLabel(file.path);
  const [copiedPath, setCopiedPath] = useState(false);
  const [copiedContent, setCopiedContent] = useState(false);

  const handleCopyPath = () => {
    copyToClipboard(file.path).then(() => {
      setCopiedPath(true);
      showToast('已复制路径');
      setTimeout(() => setCopiedPath(false), 2000);
    }).catch(() => showToast('复制失败'));
  };

  const handleCopyContent = () => {
    copyToClipboard(file.content).then(() => {
      setCopiedContent(true);
      showToast('已复制内容');
      setTimeout(() => setCopiedContent(false), 2000);
    }).catch(() => showToast('复制失败'));
  };

  const handleOpenStandalonePreview = () => {
    const url = new URL(window.location.href);
    url.searchParams.set('view', 'file-preview');
    url.searchParams.set('path', file.path);
    if (jobId) url.searchParams.set('jobId', jobId);
    window.open(url.toString(), '_blank', 'noopener,noreferrer');
  };

  return createPortal(
    <>
      <div className="file-viewer-overlay" onClick={onClose} />
      <div className="file-viewer-modal">
        <div className="file-viewer-header">
          <div className="file-viewer-header-left">
            <div className="file-viewer-file-icon">
              <svg viewBox="0 0 16 16" width="16" height="16" fill="currentColor">
                <path d="M3.5 1A1.5 1.5 0 0 0 2 2.5v11A1.5 1.5 0 0 0 3.5 15h9a1.5 1.5 0 0 0 1.5-1.5V5.414a1 1 0 0 0-.293-.707l-3.414-3.414A1 1 0 0 0 9.586 1H3.5Zm0 1h5.586L13 5.914V13.5a.5.5 0 0 1-.5.5h-9a.5.5 0 0 1-.5-.5v-11a.5.5 0 0 1 .5-.5Z"/>
              </svg>
            </div>
            <span className="file-viewer-filename" title={file.path}>{file.name}</span>
            <span className="file-viewer-lang-badge">{langLabel}</span>
            {file.size > 0 && <span className="file-viewer-size">{formatFileSize(file.size)}</span>}
          </div>
          <div className="file-viewer-header-right">
            {!file.loading && !file.error && !file.binary && (
              <button className="file-viewer-header-btn" title="在新页面预览" aria-label="在新页面预览" onClick={handleOpenStandalonePreview}>
                <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2"><path d="M14 3h7v7"/><path d="M10 14 21 3"/><path d="M21 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5"/></svg>
              </button>
            )}
            <button className="file-viewer-header-btn" title="复制内容" onClick={handleCopyContent}>
              {copiedContent ? (
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="20 6 9 17 4 12" /></svg>
              ) : (
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>
              )}
            </button>
            <button className="file-viewer-header-btn" title="复制路径" onClick={handleCopyPath}>
              {copiedPath ? (
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><polyline points="20 6 9 17 4 12" /></svg>
              ) : (
                <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" /><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" /></svg>
              )}
            </button>
            <button className="file-viewer-header-btn file-viewer-close-btn" onClick={onClose}>
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M18 6L6 18M6 6l12 12" />
              </svg>
            </button>
          </div>
        </div>
        {file.truncated && (
          <div className="file-viewer-notice">文件超过 1MB，仅展示前 1MB 内容</div>
        )}
        {file.loading ? (
          <div className="file-viewer-loading">
            <div className="file-viewer-loading-bar" />
            <span>加载中...</span>
          </div>
        ) : file.error ? (
          <div className="file-viewer-error">{file.error}</div>
        ) : file.binary ? (
          <div className="file-viewer-binary">二进制文件，无法预览</div>
        ) : (
          <div className="file-viewer-body">
            <div className="file-viewer-code" role="table" aria-label="file content">
              {lineNumbers.map((lineContent, idx) => {
                const lineNum = idx + 1;
                const startLine = file.line;
                const endLine = file.endLine ?? file.line;
                const isHighlighted = startLine !== undefined && endLine !== undefined && lineNum >= startLine && lineNum <= endLine;
                const isScrollTarget = startLine !== undefined && lineNum === startLine;
                return (
                  <div
                    key={idx}
                    className={`file-viewer-row${isHighlighted ? ' file-viewer-line-highlight' : ''}`}
                    ref={
                      isScrollTarget
                        ? (el) => {
                            if (el && !scrolledRef.current) {
                              scrolledRef.current = true;
                              el.scrollIntoView({ block: 'center' });
                            }
                          }
                        : undefined
                    }
                    role="row"
                  >
                    <div className="file-viewer-line-number" role="cell">{lineNum}</div>
                    <div className="file-viewer-line-content" role="cell"><HighlightedLine line={lineContent} lang={lang} /></div>
                  </div>
                );
              })}
            </div>
          </div>
        )}
        <div className="file-viewer-footer">
          <span className="file-viewer-path">{file.path}</span>
          <span className="file-viewer-line-count">{lineNumbers.length} lines</span>
        </div>
      </div>
    </>,
    document.body
  );
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export const MessageItem = memo(function MessageItem({ message, agentIconUrl, agentDisplayName, jobId, workdir, shareToken }: MessageItemProps) {
  const shareInfo = useMemo<ShareInfo | null>(
    () => (shareToken && jobId ? { shareToken, jobId } : null),
    [shareToken, jobId]
  );
  const [viewingFile, setViewingFile] = useState<ViewingFileState | null>(null);

  const openFileViewer: OpenFileViewerFn = useCallback(async (filePath: string, line?: number, endLine?: number) => {
    const name = filePath.split('/').filter(Boolean).pop() || filePath;
    setViewingFile({ path: filePath, name, content: '', line, endLine, size: 0, truncated: false, binary: false, loading: true });
    try {
      const res = await fetch('/api/v1/read-file', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path: filePath, job_id: jobId || '' }),
      });
      const data = await res.json();
      if (data.code === 0) {
        setViewingFile({
          path: filePath, name, line, endLine,
          content: data.content,
          size: data.size || 0,
          truncated: !!data.truncated,
          binary: !!data.binary,
          loading: false,
        });
      } else {
        setViewingFile((prev) => prev ? { ...prev, loading: false, error: data.message || '读取文件失败' } : null);
      }
    } catch {
      setViewingFile((prev) => prev ? { ...prev, loading: false, error: '网络错误，无法读取文件' } : null);
    }
  }, [jobId]);

  const content = (
    <ShareInfoContext.Provider value={shareInfo}>
    <WorkdirContext.Provider value={workdir || ''}>
    <FileViewerContext.Provider value={openFileViewer}>
      {(() => {
        switch (message.role) {
          case MessageRoleEnum.USER:
            return <UserMessageContent message={message as UserMessage} />;
          case MessageRoleEnum.ASSISTANT:
            return <AssistantMessageContent message={message as AssistantMessage} agentIconUrl={agentIconUrl} agentDisplayName={agentDisplayName} />;
          case MessageRoleEnum.TOOL:
            return <ToolMessageContent message={message as ToolMessage} />;
          case MessageRoleEnum.SYSTEM:
            // Transient system bubble (slash-command feedback). `/ws list`
            // and `/job list` get structured rendering so each row becomes a
            // clickable link (equivalent to `/ws N` / `/job N`). Other
            // commands render as plain pre-wrapped text.
            return (
              <SystemCommandBubble message={message as SystemMessage} jobId={jobId} />
            );
          default:
            return null;
        }
      })()}
      {viewingFile && <FileViewerModal file={viewingFile} jobId={jobId} onClose={() => setViewingFile(null)} />}
    </FileViewerContext.Provider>
    </WorkdirContext.Provider>
    </ShareInfoContext.Provider>
  );

  return content;
});

// ---- System command bubble (slash-command feedback) ----
//
// Each command output is parsed into a structured layout rather than a raw
// text block: /help becomes a list of command cards, /status becomes a
// key/value grid, and /workspace + /job "list" become clickable rows that
// replay /ws N / /job N.
interface SystemCommandBubbleProps {
  message: SystemMessage;
  jobId?: string;
}

const NUMBER_ROW_RE = /^(\*?)(\d+)\.\s+(.*)$/;
const KV_ROW_RE = /^([^:：]+)[:：]\s*(.*)$/;

function SystemCommandBubble({ message, jobId }: SystemCommandBubbleProps) {
  const { t } = useTranslation();
  const source = message.commandSource || '';
  const content = message.content;

  const sendCommand = async (cmd: string) => {
    if (!jobId) return;
    try {
      const response = await fetch(`/api/v1/job/${encodeURIComponent(jobId)}/message`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ messages: [{ role: 'user', content: cmd }] }),
      });
      const rawBody = await response.text().catch(() => '');
      let body: Record<string, unknown> | null = null;
      if (rawBody) {
        try {
          body = JSON.parse(rawBody) as Record<string, unknown>;
        } catch {
          body = null;
        }
      }
      if (!response.ok) {
        const detail =
          (typeof body?.msg === 'string' && body.msg) ||
          (typeof body?.error === 'string' && body.error) ||
          (typeof body?.message === 'string' && body.message) ||
          rawBody ||
          `HTTP ${response.status}`;
        throw new Error(`HTTP ${response.status}: ${detail}`);
      }

      const event = body?.event as CommandSystemMessageEvent | undefined;
      if (event?.action?.type) {
        window.dispatchEvent(new CustomEvent('quartet:command-action', { detail: event.action }));
      }
      if (event?.text) {
        showToast(event.text);
      }
    } catch (err) {
      console.error('[system-command-bubble] send failed:', err);
      const detail = err instanceof Error ? err.message : String(err);
      showToast(t('chat.commandSendFailed', { error: detail }));
    }
  };

  let body: React.ReactNode;
  let headerLabel = '系统消息';

  if (source === '/help') {
    headerLabel = '可用命令';
    body = <HelpBody content={content} />;
  } else if (source === '/status' || source === '/info') {
    headerLabel = '聊天状态';
    body = <StatusBody content={content} />;
  } else if ((source === '/workspace' || source === '/ws' || source === '/job') && jobId) {
    headerLabel = source === '/job' ? 'Job 列表' : '工作空间列表';
    const linkCmd = source === '/job' ? '/job' : '/ws';
    body = <ListBody content={content} linkCmd={linkCmd} onSelect={sendCommand} />;
  } else {
    body = <div className="system-message-plain">{content}</div>;
  }

  return (
    <div className="system-message-bubble" data-testid="message-item" data-message-id={message.id} data-message-role="system" data-command-source={source}>
      <div className="system-message-header">
        <span className="system-message-badge">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <circle cx="12" cy="12" r="9" />
            <path d="M12 8v4" />
            <circle cx="12" cy="16" r="0.8" fill="currentColor" />
          </svg>
          {headerLabel}
        </span>
        {source && <span className="system-message-source">{source}</span>}
      </div>
      <div className="system-message-body">{body}</div>
    </div>
  );
}

interface HelpEntry {
  name: string;
  description: string;
  usage?: string;
  aliases: string[];
}

function parseHelp(content: string): HelpEntry[] {
  const lines = content.split('\n');
  const entries: HelpEntry[] = [];
  let current: HelpEntry | null = null;
  for (const raw of lines) {
    const line = raw.trimEnd();
    if (!line) continue;
    const cmdMatch = /^(\/[a-zA-Z][\w-]*)\s*-\s*(.*)$/.exec(line);
    if (cmdMatch) {
      if (current) entries.push(current);
      current = { name: cmdMatch[1], description: cmdMatch[2], aliases: [] };
      continue;
    }
    if (!current) continue;
    const usageMatch = /^\s+用法[:：]\s*(.*)$/.exec(line);
    if (usageMatch) {
      current.usage = usageMatch[1];
      continue;
    }
    const aliasMatch = /^\s+别名[:：]\s*(.*)$/.exec(line);
    if (aliasMatch) {
      current.aliases.push(aliasMatch[1]);
      continue;
    }
  }
  if (current) entries.push(current);
  return entries;
}

function HelpBody({ content }: { content: string }) {
  const entries = parseHelp(content);
  if (entries.length === 0) {
    return <div className="system-message-plain">{content}</div>;
  }
  return (
    <ul className="system-help-list">
      {entries.map((e) => (
        <li key={e.name} className="system-help-item">
          <div className="system-help-row">
            <code className="system-help-name">{e.name}</code>
            <span className="system-help-desc">{e.description}</span>
          </div>
          {e.usage && (
            <div className="system-help-meta">
              <span className="system-help-meta-label">用法</span>
              <code className="system-help-meta-value">{e.usage}</code>
            </div>
          )}
          {e.aliases.length > 0 && (
            <div className="system-help-meta">
              <span className="system-help-meta-label">别名</span>
              <span className="system-help-alias-row">
                {e.aliases.map((a) => (
                  <code key={a} className="system-help-alias">{a}</code>
                ))}
              </span>
            </div>
          )}
        </li>
      ))}
    </ul>
  );
}

function StatusBody({ content }: { content: string }) {
  const lines = content.split('\n').map((l) => l.trimEnd()).filter((l) => l !== '');
  const rows: Array<{ key: string; value: string } | { text: string }> = [];
  for (const line of lines) {
    const m = KV_ROW_RE.exec(line);
    if (m && m[2] !== '') {
      rows.push({ key: m[1].trim(), value: m[2].trim() });
    } else {
      rows.push({ text: line });
    }
  }
  const kvs = rows.filter((r): r is { key: string; value: string } => 'key' in r);
  const captions = rows.filter((r): r is { text: string } => 'text' in r);
  if (kvs.length === 0) {
    return <div className="system-message-plain">{content}</div>;
  }
  return (
    <div>
      {captions.map((c, i) => (
        <div key={`cap-${i}`} className="system-status-caption">{c.text}</div>
      ))}
      <dl className="system-status-grid">
        {kvs.map((r, i) => (
          <div key={i} className="system-status-row">
            <dt className="system-status-key">{r.key}</dt>
            <dd className="system-status-value">
              <StatusValue value={r.value} />
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function StatusValue({ value }: { value: string }) {
  const statusClass = statusColorClass(value);
  if (statusClass) {
    return (
      <span className={`system-status-pill ${statusClass}`}>
        <span className="system-status-pill-dot" />
        {value}
      </span>
    );
  }
  return <span>{value}</span>;
}

function statusColorClass(value: string): string {
  const v = value.toLowerCase();
  if (v === 'completed' || v === 'done' || v === 'succeeded') return 'is-ok';
  if (v === 'running' || v === 'active' || v === 'in_progress') return 'is-running';
  if (v === 'failed' || v === 'error') return 'is-error';
  if (v === 'idle' || v === 'pending' || v === 'waiting') return 'is-pending';
  return '';
}

function ListBody({
  content,
  linkCmd,
  onSelect,
}: {
  content: string;
  linkCmd: string;
  onSelect: (cmd: string) => void;
}) {
  const lines = content.split('\n');
  const items: React.ReactNode[] = [];
  const captions: string[] = [];
  let footer = '';
  lines.forEach((line, i) => {
    const m = NUMBER_ROW_RE.exec(line);
    if (m) {
      const isCurrent = m[1] === '*';
      const n = m[2];
      const rest = m[3];
      items.push(
        <button
          key={`row-${i}`}
          type="button"
          className={`system-list-row${isCurrent ? ' is-current' : ''}`}
          onClick={() => onSelect(`${linkCmd} ${n}`)}
          title={`运行 ${linkCmd} ${n}`}
        >
          <span className="system-list-index">{n}</span>
          <span className="system-list-text">{rest}</span>
          {isCurrent && <span className="system-list-current-tag">当前</span>}
        </button>
      );
    } else if (line.trim().startsWith('*')) {
      footer = line.trim();
    } else if (line.trim() !== '') {
      captions.push(line.trim());
    }
  });
  return (
    <div>
      {captions.map((c, i) => (
        <div key={`cap-${i}`} className="system-list-caption">{c}</div>
      ))}
      {items.length > 0 ? (
        <div className="system-list">{items}</div>
      ) : (
        <div className="system-message-plain">{content}</div>
      )}
      {footer && <div className="system-list-footer">{footer}</div>}
    </div>
  );
}
