import { useCallback, useEffect, useMemo, useState } from 'react';
import ReactMarkdown from 'react-markdown';
import type { Components } from 'react-markdown';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize';
import remarkGfm from 'remark-gfm';
import { copyToClipboard } from '../utils/clipboard';
import './FilePreviewPage.css';

interface FilePreviewData {
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
}

const markdownExtensions = new Set(['.md', '.markdown', '.mdown', '.mkd', '.mkdn', '.mdx']);
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

async function readFile(path: string, jobId: string, signal: AbortSignal): Promise<FilePreviewData> {
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
    void fetch(`/api/v1/serve-file?path=${encodeURIComponent(localPath)}`, { signal: controller.signal })
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
  const params = useMemo(() => new URLSearchParams(window.location.search), []);
  const path = params.get('path')?.trim() || '';
  const jobId = params.get('jobId')?.trim() || '';
  const markdown = isMarkdownPath(path);
  const [data, setData] = useState<FilePreviewData | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(!!path);
  const [showSource, setShowSource] = useState(!markdown);
  const [wrapText, setWrapText] = useState(true);
  const [copied, setCopied] = useState(false);

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
    void readFile(path, jobId, controller.signal)
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
  const typeLabel = markdown ? 'Markdown' : (extensionFromPath(path).slice(1).toUpperCase() || 'Text');

  const handleCopy = useCallback(() => {
    if (!data) return;
    void copyToClipboard(data.content).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    }).catch((reason: unknown) => {
      setError(reason instanceof Error ? reason.stack || reason.message : String(reason));
    });
  }, [data]);

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
          {markdown && data && !data.binary && (
            <div className="file-preview-segmented" role="group" aria-label="预览模式">
              <button type="button" className={!showSource ? 'active' : ''} onClick={() => setShowSource(false)}>阅读</button>
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
          <a className="file-preview-button file-preview-return" href={buildReturnUrl()}>返回 Quartet</a>
        </div>
      </header>

      {data?.truncated && (
        <div className="file-preview-notice" role="status">
          文件超过 1 MB，接口未返回完整内容。当前页面显示的是服务端返回的提示信息。
        </div>
      )}

      <main className={`file-preview-stage ${showSource ? 'source-mode' : 'reading-mode'}`}>
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

        {!loading && data && !data.binary && !showSource && (
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

        {!loading && data && !data.binary && showSource && (
          <section className="file-preview-source" aria-label="文件源文">
            <pre className={wrapText ? 'is-wrapped' : ''}>{data.content || ' '}</pre>
          </section>
        )}
      </main>
    </div>
  );
}
