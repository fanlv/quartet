// isImageUrl returns true for strings that look like an http(s) URL. Callers
// use it to decide whether to render an <img> vs. treat the value as an
// emoji / text icon. The check is deliberately permissive: the backend is
// trusted to return only real image URLs, so we only need to distinguish
// URL-shaped input from an emoji or short string.
export function isImageUrl(s: string): boolean {
  return s.startsWith('http://') || s.startsWith('https://');
}
