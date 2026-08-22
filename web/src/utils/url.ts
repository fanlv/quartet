// Path the backend's IconCacheURL emits for icons it proxies and caches.
const ICON_PROXY_PATH = '/api/v1/icon';
const PUBLIC_ICON_PROXY_PATH = '/api/v1/public/icon';

// Mirrors the localStorage key owned by main.tsx / AuthGate. Duplicated rather
// than imported to keep this util free of app-shell imports, matching how the
// other <img>-src builders in this codebase read the token.
const AUTH_TOKEN_STORAGE_KEY = 'quartet.x_auth_token';

// isImageUrl returns true for strings that look like an http(s) URL or a
// data:image/ URI. Callers use it to decide whether to render an <img> vs. treat the value as an
// emoji / text icon. The check is deliberately permissive: the backend is
// trusted to return only real image URLs, so we only need to distinguish
// URL-shaped input from an emoji or short string.
export function isImageUrl(s: string): boolean {
  return s.startsWith('http://') || s.startsWith('https://') || s.startsWith('data:image/') || s.startsWith(ICON_PROXY_PATH);
}

export type IconShareInfo = { shareToken: string; jobId: string };

// resolveIconSrc turns a backend-issued icon value into something an <img> can
// actually load.
//
// /api/v1/icon sits behind the API auth middleware (it fetches arbitrary
// caller-supplied URLs server-side, so leaving it open would expose an SSRF
// probe). <img> cannot carry an X-AGENT-AUTH header, so the token rides the
// ?token= query fallback the auth middleware accepts — the same approach
// buildMessageImageUrl uses for /api/v1/serve-file.
//
// In a public share context there is no agent token, so the request is
// rewritten onto /api/v1/public/icon and the job's shareToken authorizes it.
//
// Anything that is not a proxied icon (external http(s) URL, data: URI, emoji)
// passes through untouched.
export function resolveIconSrc(iconUrl: string | undefined, shareInfo?: IconShareInfo | null): string {
  if (!iconUrl || !iconUrl.startsWith(ICON_PROXY_PATH)) return iconUrl ?? '';

  if (shareInfo) {
    const rewritten = PUBLIC_ICON_PROXY_PATH + iconUrl.slice(ICON_PROXY_PATH.length);
    return appendQuery(
      rewritten,
      `shareToken=${encodeURIComponent(shareInfo.shareToken)}&jobId=${encodeURIComponent(shareInfo.jobId)}`,
    );
  }

  const token = (localStorage.getItem(AUTH_TOKEN_STORAGE_KEY) ?? '').trim();
  if (!token) return iconUrl;
  return appendQuery(iconUrl, `token=${encodeURIComponent(token)}`);
}

function appendQuery(url: string, params: string): string {
  return `${url}${url.includes('?') ? '&' : '?'}${params}`;
}
