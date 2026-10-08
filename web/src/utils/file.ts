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

export function isPdfFile(name: string): boolean {
  const dot = name.lastIndexOf('.');
  const ext = dot >= 0 ? name.slice(dot).toLowerCase() : '';
  return ext === '.pdf';
}

// Mirrors the backend /api/v1/serve-file size cap (10MB): larger files are
// refused there, so they cannot be embedded either.
export const maxEmbeddedFileSize = 10 * 1024 * 1024;

export function buildServeFileUrl(path: string): string {
  return `/api/v1/serve-file?path=${encodeURIComponent(path)}`;
}

// Authenticated save. download=1 streams the file as an attachment with no
// size cap. Headers are checked first so a JSON error is shown in full;
// the body of a successful response is cancelled and the browser then saves
// the file itself, instead of buffering it in the page.
export async function downloadWorkspaceFile(path: string, fileName: string): Promise<void> {
  const name = fileName || 'download';
  const params = new URLSearchParams({ path, download: '1', name });
  const endpoint = `/api/v1/serve-file?${params.toString()}`;
  const response = await fetch(endpoint);
  if (!response.ok) {
    const body = await response.text().catch(() => '');
    const status = `${response.status}${response.statusText ? ` ${response.statusText}` : ''}`;
    throw new Error(`GET ${endpoint} returned HTTP ${status}${body ? `\n${body}` : ''}`);
  }
  await response.body?.cancel().catch(() => undefined);
  const anchor = document.createElement('a');
  anchor.href = endpoint;
  anchor.download = name;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
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

export async function writeFile(path: string, content: string, jobId?: string): Promise<void> {
  const endpoint = '/api/v1/write-file';
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path, content, job_id: jobId || '' }),
  });
  const rawBody = await response.text();
  if (!response.ok) {
    const status = `${response.status}${response.statusText ? ` ${response.statusText}` : ''}`;
    throw new Error(`POST ${endpoint} returned HTTP ${status}${rawBody ? `\n${rawBody}` : ''}`);
  }
  let data: { code?: number };
  try {
    data = JSON.parse(rawBody);
  } catch (error) {
    throw new Error(`POST ${endpoint} returned invalid JSON\n${rawBody}`, { cause: error });
  }
  if (data.code !== 0) {
    throw new Error(`POST ${endpoint} returned code ${String(data.code)}${rawBody ? `\n${rawBody}` : ''}`);
  }
}

// Images are fetched as a blob so the authenticated cookie path is identical
// to the JSON API; the caller owns revoking the returned object URL.
export async function fetchFileAsBlobUrl(path: string, signal?: AbortSignal): Promise<string> {
  const endpoint = buildServeFileUrl(path);
  const res = await fetch(endpoint, { signal });
  if (!res.ok) {
    const body = await res.text().catch(() => '');
    const status = `${res.status}${res.statusText ? ` ${res.statusText}` : ''}`;
    throw new Error(`GET ${endpoint} returned HTTP ${status}${body ? `\n${body}` : ''}`);
  }
  return URL.createObjectURL(await res.blob());
}
