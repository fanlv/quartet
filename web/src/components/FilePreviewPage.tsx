import { Children, isValidElement, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import type { Components } from 'react-markdown';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize';
import remarkGfm from 'remark-gfm';
import { copyToClipboard } from '../utils/clipboard';
import { useAuthPrincipal } from '../auth';
import './FilePreviewPage.css';

interface FilePreviewData {
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
}

type MermaidAPI = typeof import('mermaid')['default'];

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
let mermaidPromise: Promise<MermaidAPI> | null = null;
const mermaidMinZoom = 0.25;
const mermaidMaxZoom = 2;
const mermaidZoomStep = 0.25;

function loadMermaid(): Promise<MermaidAPI> {
  if (!mermaidPromise) {
    mermaidPromise = import('mermaid').then(({ default: mermaid }) => {
      mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'strict',
        suppressErrorRendering: true,
        theme: 'base',
        look: 'classic',
        fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans SC', sans-serif",
        themeVariables: {
          background: '#ffffff',
          primaryColor: '#edf4ff',
          primaryTextColor: '#172033',
          primaryBorderColor: '#7ca5e8',
          secondaryColor: '#f3f6fa',
          secondaryTextColor: '#263247',
          secondaryBorderColor: '#aebaca',
          tertiaryColor: '#f8fafc',
          tertiaryTextColor: '#263247',
          tertiaryBorderColor: '#cbd5e1',
          lineColor: '#65758b',
          edgeLabelBackground: '#ffffff',
          clusterBkg: '#f8fafc',
          clusterBorder: '#cbd5e1',
          noteBkgColor: '#fff7e7',
          noteBorderColor: '#e5bd73',
          noteTextColor: '#4a3a1f',
        },
        flowchart: {
          curve: 'basis',
          htmlLabels: false,
          useMaxWidth: false,
        },
      });
      return mermaid;
    }).catch((error) => {
      mermaidPromise = null;
      throw error;
    });
  }
  return mermaidPromise;
}

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

function normalizeLocalPath(baseFilePath: string, target: string): string {
  const targetWithoutFragment = target.split('#', 1)[0].split('?', 1)[0];
  if (targetWithoutFragment.startsWith('/')) return targetWithoutFragment;

  const baseParts = baseFilePath.split('/').slice(0, -1);
  for (const part of targetWithoutFragment.split('/')) {
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
  return url.toString();
}

function buildReturnUrl(): string {
  const url = new URL(window.location.href);
  url.searchParams.delete('view');
  url.searchParams.delete('path');
  return url.toString();
}

// Public share links read through a token-scoped endpoint; everything else
// goes through the shared authenticated reader.
async function readPreviewFile(path: string, jobId: string, signal: AbortSignal): Promise<FilePreviewData> {
  const params = new URLSearchParams(window.location.search);
  const fileShareToken = params.get('fileShareToken');

  if (fileShareToken) {
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

function fullErrorDetail(error: unknown): string {
  if (error instanceof Error) return error.stack || `${error.name}: ${error.message}`;
  return String(error);
}

function MermaidDiagram({ source }: { source: string }) {
  const reactId = useId();
  const renderId = useMemo(() => `file-preview-mermaid-${reactId.replace(/[^a-zA-Z0-9_-]/g, '')}`, [reactId]);
  const containerRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [error, setError] = useState('');
  const [naturalWidth, setNaturalWidth] = useState(0);
  const [zoom, setZoom] = useState(1);

  useEffect(() => {
    let cancelled = false;
    const container = containerRef.current;
    setStatus('loading');
    setError('');
    setNaturalWidth(0);
    setZoom(1);
    if (container) container.replaceChildren();

    void loadMermaid()
      .then((mermaid) => mermaid.render(renderId, source))
      .then(({ svg, bindFunctions }) => {
        if (cancelled || !container) return;
        container.innerHTML = svg;
        const svgElement = container.querySelector('svg');
        const naturalWidth = svgElement?.viewBox.baseVal.width;
        if (svgElement && naturalWidth && Number.isFinite(naturalWidth)) {
          svgElement.style.width = `${Math.ceil(naturalWidth)}px`;
          svgElement.style.maxWidth = 'none';
          svgElement.style.height = 'auto';
          setNaturalWidth(Math.ceil(naturalWidth));
        }
        bindFunctions?.(container);
        setStatus('ready');
      })
      .catch((reason: unknown) => {
        if (cancelled) return;
        const detail = fullErrorDetail(reason);
        console.error('[FilePreview] Mermaid render failed', reason);
        setError(detail);
        setStatus('error');
      });

    return () => {
      cancelled = true;
      container?.replaceChildren();
    };
  }, [renderId, source]);

  useEffect(() => {
    const svgElement = containerRef.current?.querySelector('svg');
    if (!svgElement || !naturalWidth) return;
    svgElement.style.width = `${Math.round(naturalWidth * zoom)}px`;
  }, [naturalWidth, zoom]);

  const updateZoom = useCallback((nextZoom: number) => {
    setZoom(Math.min(mermaidMaxZoom, Math.max(mermaidMinZoom, nextZoom)));
  }, []);

  const fitToWidth = useCallback(() => {
    const container = containerRef.current;
    if (!container || !naturalWidth) return;
    const style = window.getComputedStyle(container);
    const horizontalPadding = Number.parseFloat(style.paddingLeft) + Number.parseFloat(style.paddingRight);
    const availableWidth = Math.max(1, container.clientWidth - horizontalPadding);
    updateZoom(Math.min(1, availableWidth / naturalWidth));
    container.scrollLeft = 0;
  }, [naturalWidth, updateZoom]);

  if (status === 'error') {
    return (
      <div className="file-preview-mermaid-error" role="alert">
        <strong>Mermaid 图表渲染失败</strong>
        <pre>{error}</pre>
        <details>
          <summary>查看 Mermaid 源文</summary>
          <pre>{source}</pre>
        </details>
      </div>
    );
  }

  return (
    <figure className={`file-preview-mermaid ${status === 'loading' ? 'is-loading' : ''}`}>
      {status === 'loading' && (
        <div className="file-preview-mermaid-loading" role="status">
          <span className="file-preview-spinner" />
          <span>正在渲染图表…</span>
        </div>
      )}
      {status === 'ready' && (
        <figcaption className="file-preview-mermaid-toolbar">
          <span>Mermaid</span>
          <div className="file-preview-mermaid-zoom" role="group" aria-label="图表缩放">
            <button
              type="button"
              title="缩小"
              aria-label="缩小图表"
              disabled={zoom <= mermaidMinZoom}
              onClick={() => updateZoom(zoom - mermaidZoomStep)}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14" /></svg>
            </button>
            <button
              type="button"
              className="file-preview-mermaid-percent"
              title="重置为 100%"
              aria-label={`当前缩放 ${Math.round(zoom * 100)}%，点击重置为 100%`}
              onClick={() => updateZoom(1)}
            >
              {Math.round(zoom * 100)}%
            </button>
            <button
              type="button"
              title="放大"
              aria-label="放大图表"
              disabled={zoom >= mermaidMaxZoom}
              onClick={() => updateZoom(zoom + mermaidZoomStep)}
            >
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
            </button>
            <button type="button" title="适应宽度" aria-label="图表适应宽度" onClick={fitToWidth}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m8 3-5 5 5 5M3 8h7M16 3l5 5-5 5M21 8h-7M8 21l-5-5 5-5M3 16h7M16 21l5-5-5-5M21 16h-7" /></svg>
            </button>
          </div>
        </figcaption>
      )}
      <div
        ref={containerRef}
        className="file-preview-mermaid-canvas"
        role="img"
        aria-label="Mermaid 图表"
      />
    </figure>
  );
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

function MarkdownPreviewImage({ basePath, src, alt }: { basePath: string; src: string; alt: string }) {
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
    const params = new URLSearchParams(window.location.search);
    const fileShareToken = params.get('fileShareToken');
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
  }, [basePath, external, src]);

  if (!src) return null;
  if (failed) return <span className="file-preview-image-error">图片加载失败：{alt || src}</span>;
  if (!external && !blobUrl) return <span className="file-preview-image-loading">正在加载图片…</span>;

  return (
    <img
      src={external ? src : blobUrl}
      alt={alt}
      loading="lazy"
      referrerPolicy="no-referrer"
      onError={() => setFailed(true)}
    />
  );
}

export function FilePreviewPage() {
  const principal = useAuthPrincipal();
  const params = useMemo(() => new URLSearchParams(window.location.search), []);
  const path = params.get('path')?.trim() || '';
  const jobId = params.get('jobId')?.trim() || '';
  const fileShareToken = params.get('fileShareToken') || '';
  const isPublic = !!fileShareToken;
  const canShareFiles = !isPublic && (principal?.permissions.includes('file.share') ?? false);
  const markdown = isMarkdownPath(path);
  const html = isHtmlPath(path);
  const renderedDocument = markdown || html;
  const [data, setData] = useState<FilePreviewData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(!!path);
  const [showSource, setShowSource] = useState(!renderedDocument);
  const [wrapText, setWrapText] = useState(true);
  const [copied, setCopied] = useState(false);
  const [shareToken, setShareToken] = useState('');
  const [shareLoading, setShareLoading] = useState(false);
  const [shareCopied, setShareCopied] = useState(false);

  useEffect(() => {
    if (!canShareFiles || !path) return;
    void fetch(`/api/v1/file-share/get?path=${encodeURIComponent(path)}`)
      .then((res) => res.json())
      .then((data) => { if (data.shared) setShareToken(data.token); })
      .catch(() => {});
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
    void readPreviewFile(path, jobId, controller.signal)
      .then((result) => setData(result))
      .catch((reason: unknown) => {
        if (reason instanceof DOMException && reason.name === 'AbortError') return;
        setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [jobId, path]);

  const lineCount = data?.content ? data.content.split('\n').length : 0;
  const typeLabel = markdown ? 'Markdown' : html ? 'HTML' : (extensionFromPath(path).slice(1).toUpperCase() || 'Text');

  const handleCopy = useCallback(() => {
    if (!data) return;
    void copyToClipboard(data.content).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    }).catch((reason: unknown) => {
      setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    });
  }, [data]);

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
      return <MarkdownPreviewImage basePath={path} src={target} alt={alt || ''} />;
    },
    table: ({ children }) => <div className="file-preview-table-wrap"><table>{children}</table></div>,
    code: ({ className, children }) => <code className={className}>{children}</code>,
    pre: ({ children }) => <MarkdownPre>{children}</MarkdownPre>,
  }), [path]);

  return (
    <div className="file-preview-page">
      <header className="file-preview-toolbar">
        <div className="file-preview-identity">
          <span className="file-preview-icon"><PreviewIcon /></span>
          <div className="file-preview-title-group">
            <strong title={path}>{fileNameFromPath(path)}</strong>
            <span title={path}>{path || '未指定文件'}</span>
          </div>
          <span className="file-preview-type">{typeLabel}</span>
          {data && <span className="file-preview-meta">{formatSize(data.size)} · {lineCount} 行</span>}
        </div>

        <div className="file-preview-actions">
          {renderedDocument && data && !data.binary && (
            <div className="file-preview-segmented" role="group" aria-label="预览模式">
              <button type="button" className={!showSource ? 'active' : ''} onClick={() => setShowSource(false)}>{html ? '预览' : '阅读'}</button>
              <button type="button" className={showSource ? 'active' : ''} onClick={() => setShowSource(true)}>源文</button>
            </div>
          )}
          {data && showSource && !data.binary && (
            <button type="button" className={`file-preview-button ${wrapText ? 'active' : ''}`} onClick={() => setWrapText((value) => !value)}>
              自动换行
            </button>
          )}
          {data && !data.binary && (
            <button type="button" className="file-preview-button" onClick={handleCopy}>
              {copied ? '已复制' : '复制内容'}
            </button>
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

      {data?.truncated && (
        <div className="file-preview-notice" role="status">
          文件超过 1 MB，接口未返回完整内容。当前页面显示的是服务端返回的提示信息。
        </div>
      )}

      <main className={`file-preview-stage ${showSource ? 'source-mode' : html ? 'html-mode' : 'reading-mode'}`}>
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

        {!loading && data?.binary && (
          <div className="file-preview-state" role="status">
            <strong>这是二进制文件</strong>
            <span>独立预览页目前支持 Markdown 和 UTF-8 文本文件。</span>
          </div>
        )}

        {!loading && data && !data.binary && !showSource && markdown && (
          <article className="file-preview-document">
            <ReactMarkdown
              remarkPlugins={[remarkGfm]}
              rehypePlugins={[rehypeRaw, [rehypeSanitize, markdownSanitizeSchema]]}
              components={markdownComponents}
            >
              {data.content}
            </ReactMarkdown>
          </article>
        )}

        {!loading && data && !data.binary && !showSource && html && (
          <HtmlPreviewDocument content={data.content} title={fileNameFromPath(path)} />
        )}

        {!loading && data && !data.binary && showSource && (
          <section className="file-preview-source" aria-label="文件源文">
            <pre className={wrapText ? 'is-wrapped' : ''}>{data.content || ' '}</pre>
          </section>
        )}
      </main>
    </div>
  );
}
