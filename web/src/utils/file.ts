// Shared file-reading helpers used by every file viewer surface: the chat
// message viewer, the workspace file browser and the standalone preview
// page. Errors carry the full server response — callers surface it verbatim
// instead of collapsing it to "load failed".

export interface FileContent {
  content: string;
  size: number;
  truncated: boolean;
  binary: boolean;
}

const imageExts = new Set(['.png', '.jpg', '.jpeg', '.gif', '.bmp', '.webp', '.svg', '.ico']);

export function isImageFile(name: string): boolean {
  const dot = name.lastIndexOf('.');
  const ext = dot >= 0 ? name.slice(dot).toLowerCase() : '';
  return imageExts.has(ext);
}

export function fileNameFromPath(path: string): string {
  return path.split('/').filter(Boolean).pop() || path || '未命名文件';
}

export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// URL of the standalone preview page for `path`. Built from the current
// location so workspace/job query params survive; `jobId` is only appended
// when given (the preview page itself already carries one).
export function buildFilePreviewUrl(path: string, jobId?: string): string {
  const url = new URL(window.location.href);
  url.searchParams.set('view', 'file-preview');
  url.searchParams.set('path', path);
  if (jobId) url.searchParams.set('jobId', jobId);
  return url.toString();
}

export async function readFile(path: string, jobId?: string, signal?: AbortSignal): Promise<FileContent> {
  const endpoint = '/api/v1/read-file';
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, job_id: jobId || '' }),
    signal,
  });
  const rawBody = await response.text();
  if (!response.ok) {
    const status = `${response.status}${response.statusText ? ` ${response.statusText}` : ''}`;
    throw new Error(`POST ${endpoint} returned HTTP ${status}${rawBody ? `\n${rawBody}` : ''}`);
  }

  let data: { code?: number; msg?: string; message?: string; content?: string; size?: number; truncated?: boolean; binary?: boolean };
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

// Images are fetched as a blob so the authenticated cookie path is identical
// to the JSON API; the caller owns revoking the returned object URL.
export async function fetchFileAsBlobUrl(path: string, signal?: AbortSignal): Promise<string> {
  const endpoint = `/api/v1/serve-file?path=${encodeURIComponent(path)}`;
  const res = await fetch(endpoint, { signal });
  if (!res.ok) {
    const body = await res.text().catch(() => '');
    const status = `${res.status}${res.statusText ? ` ${res.statusText}` : ''}`;
    throw new Error(`GET ${endpoint} returned HTTP ${status}${body ? `\n${body}` : ''}`);
  }
  return URL.createObjectURL(await res.blob());
}
