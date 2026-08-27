// Path the backend's IconCacheURL emits for icons it proxies and caches.
const ICON_PROXY_PATH = '/api/v1/icon';

// isImageUrl returns true for strings that look like an http(s) URL or a
// data:image/ URI. Callers use it to decide whether to render an <img> vs. treat the value as an
// emoji / text icon. The check is deliberately permissive: the backend is
// trusted to return only real image URLs, so we only need to distinguish
// URL-shaped input from an emoji or short string.
export function isImageUrl(s: string): boolean {
  return s.startsWith('http://') || s.startsWith('https://') || s.startsWith('data:image/') || s.startsWith(ICON_PROXY_PATH) || s.startsWith('/api/v1/public/job/');
}

export type IconShareInfo = { shareToken: string };

// resolveIconSrc turns a backend-issued icon value into something an <img> can
// actually load.
//
// /api/v1/icon sits behind the API auth middleware (it fetches arbitrary
// caller-supplied URLs server-side, so leaving it open would expose an SSRF
// probe). Browser-native same-origin requests carry the session cookie.
//
// Public share responses now return a Job-scoped opaque icon URL. The URL
// contains no upstream target and already carries the Agent identity in its
// path; this helper only attaches the share credential.
//
// Anything that is not a proxied icon (external http(s) URL, data: URI, emoji)
// passes through untouched.
export function resolveIconSrc(iconUrl: string | undefined, shareInfo?: IconShareInfo | null): string {
  if (!iconUrl) return '';
  if (shareInfo && iconUrl.startsWith('/api/v1/public/job/')) {
    return appendQuery(
      iconUrl,
      `shareToken=${encodeURIComponent(shareInfo.shareToken)}`,
    );
  }
  return iconUrl;
}

function appendQuery(url: string, params: string): string {
  return `${url}${url.includes('?') ? '&' : '?'}${params}`;
}
