import { Children, isValidElement, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import type { Components } from 'react-markdown';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize';
import remarkGfm from 'remark-gfm';
import { useTranslation } from 'react-i18next';
import { copyToClipboard } from '../utils/clipboard';
import { isPdfFile, maxEmbeddedFileSize, writeFile } from '../utils/file';
import { detectLanguage, getLanguageLabel } from '../utils/syntaxHighlight';
import { useAuthPrincipal } from '../auth';
import { useIsMobile } from '../hooks/useIsMobile';
import { DEFAULT_WORKSPACE_ID, getLastUsedWorkspaceId } from '../utils/workspace';
import { FileBrowser } from './FileBrowser';
import { MermaidDiagram } from './MermaidDiagram';
import { SourceCode } from './FileViewer/SourceCode';
import './FilePreviewPage.css';

interface FilePreviewData {
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
}

interface MarkdownOutlineItem {
  id: string;
  label: string;
  depth: number;
}

const markdownExtensions = new Set(['.md', '.markdown', '.mdown', '.mkd', '.mkdn', '.mdx']);
const htmlExtensions = new Set(['.html', '.htm']);
const externalUrlPattern = /^(?:https?:|mailto:|tel:|data:)/i;
const externalResourceUrlPattern = /^(?:https?:|data:|blob:)/i;
const markdownSanitizeSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    p: [...(defaultSchema.attributes?.p || []), ['align', 'center', 'left', 'right']],
  },
};
function fileNameFromPath(path: string): string {
  return path.split('/').filter(Boolean).pop() || path || '未命名文件';
}

function extensionFromPath(path: string): string {
  const name = fileNameFromPath(path);
  const dot = name.lastIndexOf('.');
  return dot >= 0 ? name.slice(dot).toLowerCase() : '';
}

function isMarkdownPath(path: string): boolean {
  return markdownExtensions.has(extensionFromPath(path));
}

function isHtmlPath(path: string): boolean {
  return htmlExtensions.has(extensionFromPath(path));
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// Markdown destinations arrive percent-encoded (both the escape sequences
// written by the author, like "image%201.png", and the re-encoding applied by
// the markdown-to-HTML pipeline to non-ASCII characters). The file APIs take
// literal filesystem paths, so decode first; malformed escapes stay as-is.
function decodePathTarget(target: string): string {
  try {
    return decodeURIComponent(target);
  } catch {
    return target;
  }
}

function normalizeLocalPath(baseFilePath: string, target: string): string {
  const targetWithoutFragment = target.split('#', 1)[0].split('?', 1)[0];
  const decodedTarget = decodePathTarget(targetWithoutFragment);
  if (decodedTarget.startsWith('/')) return decodedTarget;

  const baseParts = baseFilePath.split('/').slice(0, -1);
  for (const part of decodedTarget.split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') {
      if (baseParts.length > 1) baseParts.pop();
      continue;
    }
    baseParts.push(part);
  }
  return baseParts.join('/') || '/';
}

function buildPreviewUrl(path: string): string {
  const url = new URL(window.location.href);
  url.searchParams.set('view', 'file-preview');
  url.searchParams.set('path', path);
  url.hash = '';
  return url.toString();
}

function buildReturnUrl(): string {
  const url = new URL(window.location.href);
  url.searchParams.delete('view');
  url.searchParams.delete('path');
  return url.toString();
}

// Raw-bytes URL for downloads and inline embedding (PDF). Uses the public
// share endpoint when a fileShareToken is present.
function buildServeFileUrl(path: string, fileShareToken: string): string {
  const query = new URLSearchParams({ path });
  if (fileShareToken) {
    query.set('fileShareToken', fileShareToken);
    return `/api/v1/public/file-preview/serve-file?${query.toString()}`;
  }
  return `/api/v1/serve-file?${query.toString()}`;
}

async function readPreviewContext(endpoint: string, signal: AbortSignal): Promise<{ workspaceId?: string; workdir?: string }> {
  const response = await fetch(endpoint, { signal });
  const rawBody = await response.text();
  if (!response.ok) {
    const status = `${response.status}${response.statusText ? ` ${response.statusText}` : ''}`;
    throw new Error(`GET ${endpoint} returned HTTP ${status}${rawBody ? `\n${rawBody}` : ''}`);
  }
  try {
    return JSON.parse(rawBody);
  } catch (reason) {
    throw new Error(`GET ${endpoint} returned invalid JSON\n${rawBody}`, { cause: reason });
  }
}

// A share token reads through the public endpoint only when the viewer has
// no session. A logged-in viewer uses the authenticated reader, so owner
// actions stay available on the same URL.
async function readPreviewFile(path: string, jobId: string, authenticated: boolean, signal: AbortSignal): Promise<FilePreviewData> {
  const params = new URLSearchParams(window.location.search);
  const fileShareToken = params.get('fileShareToken');

  if (!authenticated && fileShareToken) {
    const endpoint = `/api/v1/public/file-preview/read-file?fileShareToken=${encodeURIComponent(fileShareToken)}`;
    const response = await fetch(endpoint, { signal });
    const rawBody = await response.text();
    if (!response.ok) {
      const status = `${response.status}${response.statusText ? ` ${response.statusText}` : ''}`;
      throw new Error(`GET ${endpoint} returned HTTP ${status}${rawBody ? `\n${rawBody}` : ''}`);
    }
    let data: { code?: number; msg?: string; content?: string; size?: number; truncated?: boolean; binary?: boolean };
    try {
      data = JSON.parse(rawBody);
    } catch (error) {
      throw new Error(`GET ${endpoint} returned invalid JSON\n${rawBody}`, { cause: error });
    }
    if (data.code !== 0) {
      throw new Error(`GET ${endpoint} returned code ${String(data.code)}${rawBody ? `\n${rawBody}` : ''}`);
    }
    return {
      content: data.content ?? '',
      size: data.size ?? 0,
      truncated: !!data.truncated,
      binary: !!data.binary,
    };
  }

  const endpoint = '/api/v1/read-file';
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, job_id: jobId }),
    signal,
  });
  const rawBody = await response.text();
  if (!response.ok) {
    const status = `${response.status}${response.statusText ? ` ${response.statusText}` : ''}`;
    throw new Error(`POST ${endpoint} returned HTTP ${status}${rawBody ? `\n${rawBody}` : ''}`);
  }

  let data: { code?: number; msg?: string; content?: string; size?: number; truncated?: boolean; binary?: boolean };
  try {
    data = JSON.parse(rawBody);
  } catch (error) {
    throw new Error(`POST ${endpoint} returned invalid JSON\n${rawBody}`, { cause: error });
  }
  if (data.code !== 0) {
    throw new Error(`POST ${endpoint} returned code ${String(data.code)}${rawBody ? `\n${rawBody}` : ''}`);
  }
  return {
    content: data.content ?? '',
    size: data.size ?? 0,
    truncated: !!data.truncated,
    binary: !!data.binary,
  };
}

function PreviewIcon() {
  return (
    <svg viewBox="0 0 24 24" aria-hidden="true">
      <path d="M6.75 2.75h7.1L18.75 7.7v13.55H6.75z" />
      <path d="M13.75 2.75v5h5" />
      <path d="M9.5 12h6M9.5 15.5h6" />
    </svg>
  );
}

function nodeText(node: ReactNode): string {
  return Children.toArray(node).map((child) => {
    if (typeof child === 'string' || typeof child === 'number') return String(child);
    if (isValidElement<{ children?: ReactNode }>(child)) return nodeText(child.props.children);
    return '';
  }).join('');
}

function headingSlug(label: string): string {
  return label
    .normalize('NFKC')
    .trim()
    .toLocaleLowerCase()
    .replace(/[^\p{Letter}\p{Number}\p{Mark}_\s-]/gu, '')
    .replace(/\s+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^-|-$/g, '') || 'section';
}

function assignMarkdownHeadingIds(article: HTMLElement): MarkdownOutlineItem[] {
  const usedIds = new Set<string>();
  const headings = Array.from(article.querySelectorAll<HTMLHeadingElement>('h1, h2, h3, h4, h5, h6'))
    .filter((heading) => heading.textContent?.trim());
  const minimumLevel = headings.reduce((minimum, heading) => (
    Math.min(minimum, Number(heading.tagName.slice(1)))
  ), 6);

  return headings.map((heading) => {
    const label = heading.textContent?.trim() || '';
    const level = Number(heading.tagName.slice(1));
    const baseId = heading.id.trim() || headingSlug(label);
    let id = baseId;
    let duplicateIndex = 2;
    while (usedIds.has(id)) {
      id = `${baseId}-${duplicateIndex}`;
      duplicateIndex += 1;
    }
    usedIds.add(id);
    heading.id = id;

    return {
      id,
      label,
      depth: Math.min(4, Math.max(0, level - minimumLevel)),
    };
  });
}

const headingScrollKeys = new Set(['ArrowDown', 'ArrowUp', 'PageDown', 'PageUp', 'Home', 'End', ' ']);

function headingAlignmentDelta(stage: HTMLElement, heading: HTMLElement): number {
  const margin = Number.parseFloat(getComputedStyle(heading).scrollMarginTop) || 0;
  return heading.getBoundingClientRect().top - stage.getBoundingClientRect().top - margin;
}

// Images and diagrams above a heading often have no height until their bytes
// arrive. A one-shot smooth scroll records the destination before that growth
// and then stops, leaving the viewport a few sections short of the heading.
function layoutPendingAbove(article: HTMLElement, heading: HTMLElement): boolean {
  const limit = heading.getBoundingClientRect().top + 4;
  const pending = article.querySelectorAll<HTMLElement>('img, .file-preview-image-loading, .mermaid-diagram.is-loading');
  for (const node of pending) {
    if (node.getBoundingClientRect().top > limit) continue;
    if (node instanceof HTMLImageElement && node.complete) continue;
    return true;
  }
  return false;
}

function scrollStageToHeading(
  stage: HTMLElement,
  article: HTMLElement,
  headingId: string,
  behavior: ScrollBehavior,
): () => void {
  const findHeading = () => article.querySelector<HTMLElement>(`#${CSS.escape(headingId)}`);
  const initial = findHeading();
  if (!initial) return () => {};

  const deadline = performance.now() + 8000;
  const previousAnchor = stage.style.overflowAnchor;
  let stopped = false;
  let raf = 0;
  let settleTimer = 0;

  const stop = () => {
    if (stopped) return;
    stopped = true;
    if (raf) window.cancelAnimationFrame(raf);
    window.clearTimeout(settleTimer);
    observer.disconnect();
    article.removeEventListener('load', wake, true);
    article.removeEventListener('error', wake, true);
    stage.removeEventListener('wheel', onUser);
    stage.removeEventListener('touchmove', onUser);
    stage.removeEventListener('pointerdown', onUser);
    window.removeEventListener('keydown', onKey);
    stage.style.overflowAnchor = previousAnchor;
  };

  const wake = () => {
    if (stopped || raf !== 0) return;
    window.clearTimeout(settleTimer);
    settleTimer = 0;
    raf = window.requestAnimationFrame(tick);
  };

  const onUser = () => stop();
  const onKey = (event: KeyboardEvent) => {
    if (headingScrollKeys.has(event.key)) stop();
  };

  const observer = new ResizeObserver(wake);
  stage.style.overflowAnchor = 'none';
  observer.observe(article);
  article.addEventListener('load', wake, true);
  article.addEventListener('error', wake, true);
  stage.addEventListener('wheel', onUser, { passive: true });
  stage.addEventListener('touchmove', onUser, { passive: true });
  stage.addEventListener('pointerdown', onUser);
  window.addEventListener('keydown', onKey);

  const tick = () => {
    raf = 0;
    if (stopped) return;
    const heading = findHeading();
    if (!heading) {
      stop();
      return;
    }

    const delta = headingAlignmentDelta(stage, heading);
    const pending = layoutPendingAbove(article, heading);
    const timedOut = performance.now() > deadline;

    if (Math.abs(delta) > 2) {
      const distance = Math.abs(delta);
      // Keep chasing the heading's live position. A fixed ease toward the
      // distance measured at click time ends early once diagrams above it load.
      const cap = behavior === 'smooth' ? Math.max(distance * 0.2, 72) : distance;
      const before = stage.scrollTop;
      stage.scrollTop += Math.sign(delta) * Math.min(distance, cap);
      if (stage.scrollTop !== before) {
        raf = window.requestAnimationFrame(tick);
        return;
      }
      if (!pending || timedOut) {
        stop();
        return;
      }
    }

    if ((pending && !timedOut) || Math.abs(delta) > 2) {
      window.clearTimeout(settleTimer);
      settleTimer = window.setTimeout(wake, 120);
      return;
    }

    window.clearTimeout(settleTimer);
    settleTimer = window.setTimeout(() => {
      settleTimer = 0;
      if (stopped) return;
      const node = findHeading();
      if (!node) {
        stop();
        return;
      }
      const stillOff = Math.abs(headingAlignmentDelta(stage, node)) > 2;
      const stillPending = layoutPendingAbove(article, node) && performance.now() <= deadline;
      if (stillOff || stillPending) {
        wake();
        return;
      }
      stop();
    }, 200);
  };

  raf = window.requestAnimationFrame(tick);
  return stop;
}

function HtmlPreviewDocument({ content, title }: { content: string; title: string }) {
  return (
    <iframe
      className="file-preview-html-frame"
      title={`${title} HTML 预览`}
      srcDoc={content}
      sandbox="allow-scripts allow-forms allow-modals allow-popups allow-popups-to-escape-sandbox allow-downloads"
      referrerPolicy="no-referrer"
    />
  );
}

function MarkdownPre({ children }: { children?: ReactNode }) {
  const child = Children.toArray(children)[0];
  if (isValidElement<{ className?: string; children?: ReactNode }>(child)) {
    const className = child.props.className || '';
    if (/\blanguage-mermaid\b/i.test(className)) {
      return <MermaidDiagram source={nodeText(child.props.children).replace(/\n$/, '')} />;
    }
  }
  return <pre>{children}</pre>;
}

function MarkdownPreviewImage({ basePath, src, alt, authenticated }: { basePath: string; src: string; alt: string; authenticated: boolean }) {
  const external = externalResourceUrlPattern.test(src);
  const [blobUrl, setBlobUrl] = useState('');
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    setBlobUrl('');
    setFailed(false);
    if (!src || external) return;

    const controller = new AbortController();
    let objectUrl = '';
    const localPath = normalizeLocalPath(basePath, src);
    const fileShareToken = authenticated ? '' : (new URLSearchParams(window.location.search).get('fileShareToken') || '');
    const serveUrl = fileShareToken
      ? `/api/v1/public/file-preview/serve-file?fileShareToken=${encodeURIComponent(fileShareToken)}&path=${encodeURIComponent(localPath)}`
      : `/api/v1/serve-file?path=${encodeURIComponent(localPath)}`;
    void fetch(serveUrl, { signal: controller.signal })
      .then(async (response) => {
        if (!response.ok) {
          const detail = await response.text();
          throw new Error(`GET /api/v1/serve-file returned HTTP ${response.status}${response.statusText ? ` ${response.statusText}` : ''}${detail ? `\n${detail}` : ''}`);
        }
        return response.blob();
      })
      .then((blob) => {
        if (controller.signal.aborted) return;
        objectUrl = URL.createObjectURL(blob);
        setBlobUrl(objectUrl);
      })
      .catch((reason: unknown) => {
        if (reason instanceof DOMException && reason.name === 'AbortError') return;
        console.error(`[FilePreview] failed to load image ${localPath}`, reason);
        setFailed(true);
      });

    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [authenticated, basePath, external, src]);

  if (!src) return null;
  if (failed) return <span className="file-preview-image-error">图片加载失败：{alt || src}</span>;
  if (!external && !blobUrl) return <span className="file-preview-image-loading">正在加载图片…</span>;

  return (
    <img
      src={external ? src : blobUrl}
      alt={alt}
      referrerPolicy="no-referrer"
      onError={() => setFailed(true)}
    />
  );
}

export function FilePreviewPage() {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const isMobile = useIsMobile();
  const [{ initialPath, jobId, workspaceId, fileShareToken, jobShareToken }] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    return {
      initialPath: params.get('path')?.trim() || '',
      jobId: params.get('jobId')?.trim() || '',
      workspaceId: params.get('workspaceId')?.trim() || '',
      fileShareToken: params.get('fileShareToken') || '',
      jobShareToken: params.get('shareToken') || '',
    };
  });
  const [path, setPath] = useState(initialPath);
  const authenticated = !!principal;
  const isPublic = !!fileShareToken && !authenticated;
  const isSharedLink = isPublic || !!jobShareToken;
  const canBrowseFiles = !isSharedLink && (principal?.permissions.includes('file.read') ?? false)
    && (principal?.permissions.includes('workspace.read') ?? false);
  const canShareFiles = !isPublic && (principal?.permissions.includes('file.share') ?? false);
  const canWriteFiles = !isPublic && (principal?.permissions.includes('file.write') ?? false);
  const markdown = isMarkdownPath(path);
  const html = isHtmlPath(path);
  const pdf = isPdfFile(fileNameFromPath(path));
  const renderedDocument = markdown || html;
  const [data, setData] = useState<FilePreviewData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(!!path);
  const [showSource, setShowSource] = useState(!renderedDocument);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [saved, setSaved] = useState(false);
  const savingRef = useRef(false);
  const [wrapText, setWrapText] = useState(true);
  const [copied, setCopied] = useState(false);
  const [pathCopied, setPathCopied] = useState(false);
  const [shareToken, setShareToken] = useState(fileShareToken);
  const [shareLoading, setShareLoading] = useState(false);
  const [shareCopied, setShareCopied] = useState(false);
  const [fileBrowserOpen, setFileBrowserOpen] = useState(false);
  const [browserRootPath, setBrowserRootPath] = useState('');
  const [browserLoading, setBrowserLoading] = useState(false);
  const [browserError, setBrowserError] = useState('');
  const [markdownOutline, setMarkdownOutline] = useState<MarkdownOutlineItem[]>([]);
  const [activeHeadingId, setActiveHeadingId] = useState('');
  const stageRef = useRef<HTMLElement>(null);
  const markdownArticleRef = useRef<HTMLElement>(null);
  const cancelHeadingScrollRef = useRef<(() => void) | null>(null);
  const dirty = editing && draft !== data?.content;
  const previewContent = editing ? draft : data?.content ?? '';
  const canEdit = canWriteFiles && !!data && !loading && !error && !pdf && !data.binary && !data.truncated;

  useEffect(() => {
    if (!canBrowseFiles || !fileBrowserOpen || browserRootPath) return;
    const controller = new AbortController();
    setBrowserLoading(true);
    setBrowserError('');
    const loadWorkspace = async () => {
      let id = workspaceId;
      if (!id && jobId && principal?.permissions.includes('job.read')) {
        const job = await readPreviewContext(`/api/v1/job/${encodeURIComponent(jobId)}`, controller.signal);
        id = job.workspaceId || '';
      }
      id ||= getLastUsedWorkspaceId() || DEFAULT_WORKSPACE_ID;
      const endpoint = `/api/v1/workspace/${encodeURIComponent(id)}`;
      const workspace = await readPreviewContext(endpoint, controller.signal);
      if (!workspace.workdir?.trim()) throw new Error(t('filePreview.workspaceDirectoryMissing', { id }));
      if (!controller.signal.aborted) setBrowserRootPath(workspace.workdir);
    };
    void loadWorkspace().catch((reason: unknown) => {
      if (controller.signal.aborted) return;
      setBrowserError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    }).finally(() => {
      if (!controller.signal.aborted) setBrowserLoading(false);
    });
    return () => controller.abort();
  }, [canBrowseFiles, fileBrowserOpen, browserRootPath, workspaceId, jobId, principal, t]);

  const handleFileSelect = useCallback((nextPath: string, updateHistory = true): boolean => {
    if (nextPath === path) return true;
    if (savingRef.current || shareLoading || (dirty && !window.confirm(t('filePreview.discardChanges')))) return false;
    if (updateHistory) window.history.pushState(window.history.state, '', buildPreviewUrl(nextPath));
    setPath(nextPath);
    setData(null);
    setLoading(!!nextPath);
    setError('');
    setShowSource(!isMarkdownPath(nextPath) && !isHtmlPath(nextPath));
    setEditing(false);
    setDraft('');
    setSaveError('');
    setSaved(false);
    setCopied(false);
    setPathCopied(false);
    setShareToken('');
    setShareCopied(false);
    setMarkdownOutline([]);
    setActiveHeadingId('');
    cancelHeadingScrollRef.current?.();
    cancelHeadingScrollRef.current = null;
    stageRef.current?.scrollTo({ top: 0, left: 0 });
    if (isMobile) setFileBrowserOpen(false);
    return true;
  }, [path, shareLoading, dirty, isMobile, t]);

  useEffect(() => {
    if (isSharedLink) return;
    const handlePopState = () => {
      const nextPath = new URLSearchParams(window.location.search).get('path')?.trim() || '';
      if (!handleFileSelect(nextPath, false)) {
        window.history.pushState(window.history.state, '', buildPreviewUrl(path));
      }
    };
    window.addEventListener('popstate', handlePopState);
    return () => window.removeEventListener('popstate', handlePopState);
  }, [handleFileSelect, isSharedLink, path]);

  useEffect(() => {
    if (!dirty && !saving) return;
    const handleBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', handleBeforeUnload);
    return () => window.removeEventListener('beforeunload', handleBeforeUnload);
  }, [dirty, saving]);

  const handleEdit = useCallback(() => {
    if (!canEdit || !data) return;
    setDraft(data.content);
    setSaveError('');
    setSaved(false);
    setShowSource(true);
    setEditing(true);
  }, [canEdit, data]);

  const handleCancelEdit = useCallback(() => {
    if (savingRef.current || (dirty && !window.confirm(t('filePreview.discardChanges')))) return;
    setEditing(false);
    setSaveError('');
  }, [dirty, t]);

  const handleSave = useCallback(async () => {
    if (!canEdit || !editing || !dirty || savingRef.current || !data) return;
    savingRef.current = true;
    setSaving(true);
    setSaveError('');
    setSaved(false);
    try {
      await writeFile(path, draft, jobId);
      // Commit the successful write before refreshing, so a read failure cannot
      // leave the user believing their changes were not saved.
      setData({ content: draft, size: new TextEncoder().encode(draft).length, binary: false, truncated: false });
      setEditing(false);
      setSaved(true);
      try {
        const latest = await readPreviewFile(path, jobId, authenticated, new AbortController().signal);
        setData(latest);
      } catch (reason: unknown) {
        const detail = reason instanceof Error ? reason.stack || reason.message : String(reason);
        setSaveError(`${t('filePreview.refreshFailed')}\n${detail}`);
      }
    } catch (reason: unknown) {
      const detail = reason instanceof Error ? reason.stack || reason.message : String(reason);
      setSaveError(`${t('filePreview.saveFailed')}\n${detail}`);
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  }, [authenticated, canEdit, editing, dirty, data, path, draft, jobId, t]);

  useEffect(() => {
    if (!editing) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') {
        event.preventDefault();
        void handleSave();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [editing, handleSave]);

  useEffect(() => {
    if (!canShareFiles || !path) return;
    const controller = new AbortController();
    void fetch(`/api/v1/file-share/get?path=${encodeURIComponent(path)}`, { signal: controller.signal })
      .then(async (res) => {
        const data = await res.json() as { shared?: boolean; token?: string };
        if (!res.ok || controller.signal.aborted) return;
        setShareToken(data.shared ? data.token || '' : '');
      })
      .catch(() => {});
    return () => controller.abort();
  }, [canShareFiles, path]);

  useEffect(() => {
    document.title = path ? `${fileNameFromPath(path)} · 文件预览` : '文件预览';
  }, [path]);

  useEffect(() => {
    if (!path) {
      setError('缺少文件路径。URL 参数 path 是必填项。');
      setLoading(false);
      return;
    }

    const controller = new AbortController();
    setLoading(true);
    setError('');
    void readPreviewFile(path, jobId, authenticated, controller.signal)
      .then((result) => { if (!controller.signal.aborted) setData(result); })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return;
        setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [authenticated, jobId, path]);

  useEffect(() => {
    if (!markdown || showSource || !data || data.binary) {
      setMarkdownOutline([]);
      setActiveHeadingId('');
      return;
    }

    const article = markdownArticleRef.current;
    const stage = stageRef.current;
    if (!article || !stage) return;

    const outline = assignMarkdownHeadingIds(article);
    const headings = Array.from(article.querySelectorAll<HTMLHeadingElement>('h1, h2, h3, h4, h5, h6'))
      .filter((heading) => outline.some((item) => item.id === heading.id));
    setMarkdownOutline(outline);

    if (outline.length === 0) {
      setActiveHeadingId('');
      return;
    }

    let animationFrame = 0;
    const updateActiveHeading = () => {
      animationFrame = 0;
      const stageTop = stage.getBoundingClientRect().top;
      const activationLine = stageTop + 36;
      const isAtBottom = stage.scrollHeight - stage.scrollTop - stage.clientHeight < 2;
      let activeId = outline[0].id;

      if (isAtBottom) {
        activeId = outline[outline.length - 1].id;
      } else {
        for (const heading of headings) {
          if (heading.getBoundingClientRect().top > activationLine) break;
          activeId = heading.id;
        }
      }
      setActiveHeadingId((current) => current === activeId ? current : activeId);
    };
    const scheduleActiveHeadingUpdate = () => {
      if (animationFrame === 0) animationFrame = window.requestAnimationFrame(updateActiveHeading);
    };

    stage.addEventListener('scroll', scheduleActiveHeadingUpdate, { passive: true });
    window.addEventListener('resize', scheduleActiveHeadingUpdate);
    scheduleActiveHeadingUpdate();

    const rawHash = window.location.hash.slice(1);
    let hash = rawHash;
    try {
      hash = decodeURIComponent(rawHash);
    } catch {
      // Keep a malformed hash literal instead of letting it break the preview.
    }
    const hashTarget = headings.find((heading) => heading.id === hash);
    if (hashTarget) {
      cancelHeadingScrollRef.current?.();
      cancelHeadingScrollRef.current = scrollStageToHeading(stage, article, hashTarget.id, 'auto');
    }

    return () => {
      stage.removeEventListener('scroll', scheduleActiveHeadingUpdate);
      window.removeEventListener('resize', scheduleActiveHeadingUpdate);
      if (animationFrame !== 0) window.cancelAnimationFrame(animationFrame);
      cancelHeadingScrollRef.current?.();
      cancelHeadingScrollRef.current = null;
    };
  }, [data, markdown, showSource, previewContent]);

  const sourceLines = useMemo(() => data ? data.content.split('\n') : [], [data]);
  const lineCount = sourceLines.length;
  const sourceLanguage = detectLanguage(path);
  const typeLabel = sourceLanguage
    ? getLanguageLabel(path)
    : (extensionFromPath(path).slice(1).toUpperCase() || 'Text');

  const handleCopy = useCallback(() => {
    if (!data) return;
    void copyToClipboard(editing ? draft : data.content).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    }).catch((reason: unknown) => {
      setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    });
  }, [data, draft, editing]);

  const handleCopyPath = useCallback(() => {
    if (!path) return;
    void copyToClipboard(path).then(() => {
      setPathCopied(true);
      window.setTimeout(() => setPathCopied(false), 1800);
    }).catch((reason: unknown) => {
      setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    });
  }, [path]);

  const handleShare = useCallback(async () => {
    if (!path || isPublic) return;
    setShareLoading(true);
    try {
      const res = await fetch('/api/v1/file-share/create', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      const token = data.token;
      setShareToken(token);
      const url = new URL(window.location.href);
      url.searchParams.set('fileShareToken', token);
      url.searchParams.delete('jobId');
      await copyToClipboard(url.toString());
      setShareCopied(true);
      setTimeout(() => setShareCopied(false), 2000);
    } catch (err) {
      console.error('Failed to share file:', err);
    } finally {
      setShareLoading(false);
    }
  }, [path, isPublic]);

  const handleCopyShareLink = useCallback(async () => {
    if (!shareToken) return;
    const url = new URL(window.location.href);
    url.searchParams.set('fileShareToken', shareToken);
    url.searchParams.delete('jobId');
    await copyToClipboard(url.toString());
    setShareCopied(true);
    setTimeout(() => setShareCopied(false), 2000);
  }, [shareToken]);

  const handleUnshare = useCallback(async () => {
    if (!shareToken) return;
    try {
      const res = await fetch('/api/v1/file-share/delete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: shareToken }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      setShareToken('');
    } catch (err) {
      console.error('Failed to unshare file:', err);
    }
  }, [shareToken]);

  const handleOutlineSelect = useCallback((id: string) => {
    const article = markdownArticleRef.current;
    if (!article) return;
    const heading = Array.from(article.querySelectorAll<HTMLHeadingElement>('h1, h2, h3, h4, h5, h6'))
      .find((candidate) => candidate.id === id);
    if (!heading) return;

    const stage = stageRef.current;
    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (stage) {
      cancelHeadingScrollRef.current?.();
      cancelHeadingScrollRef.current = scrollStageToHeading(stage, article, id, reduceMotion ? 'auto' : 'smooth');
    }
    setActiveHeadingId(id);
    const url = new URL(window.location.href);
    url.hash = id;
    window.history.replaceState(window.history.state, '', url);
  }, []);

  const markdownComponents = useMemo<Components>(() => ({
    a: ({ href, children }) => {
      const target = href || '';
      if (!target || target.startsWith('#')) return <a href={target}>{children}</a>;
      if (externalUrlPattern.test(target)) {
        return <a href={target} target="_blank" rel="noopener noreferrer">{children}</a>;
      }
      const localPath = normalizeLocalPath(path, target);
      return <a href={buildPreviewUrl(localPath)} target="_blank" rel="noopener noreferrer">{children}</a>;
    },
    img: ({ src, alt }) => {
      const target = typeof src === 'string' ? src : '';
      return <MarkdownPreviewImage basePath={path} src={target} alt={alt || ''} authenticated={authenticated} />;
    },
    table: ({ children }) => <div className="file-preview-table-wrap"><table>{children}</table></div>,
    code: ({ className, children }) => <code className={className}>{children}</code>,
    pre: ({ children }) => <MarkdownPre>{children}</MarkdownPre>,
  }), [authenticated, path]);

  return (
    <div className="file-preview-page">
      <header className="file-preview-toolbar">
        <div className="file-preview-identity">
          <button
            type="button"
            className={`file-preview-icon${fileBrowserOpen ? ' active' : ''}`}
            title={t(isSharedLink ? 'filePreview.browseDisabledOnShare' : 'filePreview.browseWorkspace')}
            aria-label={t('filePreview.browseWorkspace')}
            aria-expanded={fileBrowserOpen}
            aria-controls="file-preview-browser"
            disabled={!canBrowseFiles}
            onClick={() => setFileBrowserOpen((open) => !open)}
          >
            <PreviewIcon />
          </button>
          <div className="file-preview-title-group">
            <strong aria-describedby="file-preview-path-tooltip">{fileNameFromPath(path)}</strong>
            <span className="file-preview-path" aria-describedby="file-preview-path-tooltip">{path || '未指定文件'}</span>
            {path && <span id="file-preview-path-tooltip" className="file-preview-path-tooltip" role="tooltip">{path}</span>}
          </div>
          <span className="file-preview-type">{typeLabel}</span>
          {data && <span className="file-preview-meta">{formatSize(data.size)}{!pdf && ` · ${lineCount} 行`}</span>}
        </div>

        <div className="file-preview-actions">
          {renderedDocument && data && !data.binary && (
            <div className="file-preview-segmented" role="group" aria-label="预览模式">
              <button type="button" className={!showSource ? 'active' : ''} disabled={saving} onClick={() => setShowSource(false)}>{html ? '预览' : '阅读'}</button>
              <button type="button" className={showSource ? 'active' : ''} disabled={saving} onClick={() => setShowSource(true)}>源文</button>
            </div>
          )}
          {canEdit && showSource && !editing && (
            <button type="button" className="file-preview-button" onClick={handleEdit} disabled={saving}>
              {t('filePreview.edit')}
            </button>
          )}
          {editing && (
            <>
              <span className="file-preview-edit-status" role="status">{t(dirty ? 'filePreview.unsaved' : 'filePreview.editing')}</span>
              <button type="button" className="file-preview-button active" onClick={() => void handleSave()} disabled={!dirty || saving}>
                {t(saving ? 'filePreview.saving' : 'filePreview.save')}
              </button>
              <button type="button" className="file-preview-button" onClick={handleCancelEdit} disabled={saving}>
                {t('filePreview.cancel')}
              </button>
            </>
          )}
          {saved && !saveError && <span className="file-preview-edit-status" role="status">{t('filePreview.saved')}</span>}
          {data && showSource && !data.binary && !pdf && (
            <button type="button" className={`file-preview-button ${wrapText ? 'active' : ''}`} onClick={() => setWrapText((value) => !value)}>
              自动换行
            </button>
          )}
          {data && !data.binary && !isSharedLink && !pdf && (
            <button type="button" className="file-preview-button" onClick={handleCopy}>
              {copied ? '已复制' : '复制内容'}
            </button>
          )}
          {data && path && !isSharedLink && (
            <button type="button" className="file-preview-button" onClick={handleCopyPath}>
              {pathCopied ? '已复制' : '复制路径'}
            </button>
          )}
          {data && path && !isSharedLink && (
            <a
              className="file-preview-button"
              href={`${buildServeFileUrl(path, isPublic ? fileShareToken : '')}${isPublic ? '' : '&download=1'}`}
              download={fileNameFromPath(path)}
            >
              {t('filePreview.download')}
            </a>
          )}
          {canShareFiles && data && !shareToken && (
            <button type="button" className="file-preview-button" onClick={handleShare} disabled={shareLoading}>
              {shareLoading ? '分享中…' : '分享'}
            </button>
          )}
          {canShareFiles && shareToken && (
            <>
              <button type="button" className="file-preview-button" onClick={handleCopyShareLink}>
                {shareCopied ? '已复制' : '复制分享链接'}
              </button>
              <button type="button" className="file-preview-button" onClick={handleUnshare}>
                取消分享
              </button>
            </>
          )}
          {!isPublic && (
            <a className="file-preview-button file-preview-return" href={buildReturnUrl()}>返回 Quartet</a>
          )}
        </div>
      </header>

      {saveError && <pre className="file-preview-save-error" role="alert">{saveError}</pre>}
      {browserLoading && fileBrowserOpen && <div className="file-preview-notice" role="status">{t('filePreview.loadingWorkspace')}</div>}
      {browserError && fileBrowserOpen && <pre className="file-preview-save-error" role="alert">{browserError}</pre>}
      {canBrowseFiles && fileBrowserOpen && browserRootPath && (
        <div id="file-preview-browser">
          <FileBrowser
            rootPath={browserRootPath}
            jobId={jobId}
            selectedPath={path}
            onFileSelect={handleFileSelect}
            onClose={() => setFileBrowserOpen(false)}
          />
        </div>
      )}

      {data?.truncated && !pdf && (
        <div className="file-preview-notice" role="status">
          文件超过 1 MB，接口未返回完整内容。当前页面显示的是服务端返回的提示信息。
        </div>
      )}

      <main ref={stageRef} className={`file-preview-stage ${pdf ? 'pdf-mode' : showSource ? 'source-mode' : html ? 'html-mode' : 'reading-mode'}`}>
        {loading && (
          <div className="file-preview-state" role="status">
            <span className="file-preview-spinner" />
            <strong>正在读取文件</strong>
            <span>{path}</span>
          </div>
        )}

        {!loading && error && (
          <div className="file-preview-error" role="alert">
            <span>文件预览失败</span>
            <h1>{fileNameFromPath(path)}</h1>
            <pre>{error}</pre>
          </div>
        )}

        {!loading && data && pdf && (
          data.size > maxEmbeddedFileSize ? (
            <div className="file-preview-state" role="status">
              <strong>{t('filePreview.pdfTooLarge')}</strong>
              <span>{t('filePreview.pdfTooLargeHint')}</span>
            </div>
          ) : (
            <iframe
              className="file-preview-pdf-frame"
              title={t('filePreview.pdfFrameTitle', { name: fileNameFromPath(path) })}
              src={buildServeFileUrl(path, isPublic ? fileShareToken : '')}
            />
          )
        )}

        {!loading && data?.binary && !pdf && (
          <div className="file-preview-state" role="status">
            <strong>这是二进制文件</strong>
            <span>独立预览页目前支持 Markdown 和 UTF-8 文本文件。</span>
          </div>
        )}

        {!loading && data && !data.binary && !showSource && markdown && (
          <div className={`file-preview-reading-layout${markdownOutline.length > 0 ? ' has-outline' : ''}`}>
            {markdownOutline.length > 0 && (
              <aside className="file-preview-outline" aria-label={t('filePreview.outline')}>
                <div className="file-preview-outline-title">
                  <svg viewBox="0 0 24 24" aria-hidden="true">
                    <path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01" />
                  </svg>
                  <span>{t('filePreview.outline')}</span>
                </div>
                <nav className="file-preview-outline-nav">
                  <ol>
                    {markdownOutline.map((item) => (
                      <li key={item.id} className={`file-preview-outline-item depth-${item.depth}`}>
                        <button
                          type="button"
                          className={activeHeadingId === item.id ? 'active' : ''}
                          aria-current={activeHeadingId === item.id ? 'location' : undefined}
                          onClick={() => handleOutlineSelect(item.id)}
                        >
                          {item.label}
                        </button>
                      </li>
                    ))}
                  </ol>
                </nav>
              </aside>
            )}
            <article ref={markdownArticleRef} className="file-preview-document">
              <ReactMarkdown
                remarkPlugins={[remarkGfm]}
                rehypePlugins={[rehypeRaw, [rehypeSanitize, markdownSanitizeSchema]]}
                components={markdownComponents}
              >
                {previewContent}
              </ReactMarkdown>
            </article>
          </div>
        )}

        {!loading && data && !data.binary && !showSource && html && (
          <HtmlPreviewDocument content={previewContent} title={fileNameFromPath(path)} />
        )}

        {!loading && data && !data.binary && !pdf && (showSource || editing) && (
          <section className="file-preview-source" aria-label="文件源文" hidden={!showSource}>
            {editing ? (
              <textarea
                className="file-preview-editor"
                aria-label={t('filePreview.editor')}
                value={draft}
                onChange={(event) => {
                  // Browsers normalize textarea newlines to LF. Preserve files
                  // that consistently use CRLF when editing their source.
                  const value = event.target.value;
                  const crlf = data.content.includes('\r\n') && !/(?<!\r)\n/.test(data.content);
                  setDraft(crlf ? value.replace(/\r?\n/g, '\r\n') : value);
                  setSaved(false);
                }}
                readOnly={saving}
                wrap={wrapText ? 'soft' : 'off'}
                spellCheck={false}
                autoCapitalize="off"
                autoCorrect="off"
                autoFocus
              />
            ) : <SourceCode
              lines={sourceLines}
              path={path}
              classPrefix="file-preview-source"
              scrollClassName="file-preview-source-scroll"
              wrapText={wrapText}
            />}
          </section>
        )}
      </main>
    </div>
  );
}
