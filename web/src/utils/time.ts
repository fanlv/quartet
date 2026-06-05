/**
 * Format a unix-millisecond timestamp for display on message bubbles.
 * - Same day: "HH:mm"
 * - Same year: "MM-DD HH:mm"
 * - Different year: "YYYY-MM-DD HH:mm"
 * - Returns empty string if timestamp is 0 or falsy.
 */
export function formatMessageTime(ts: number | undefined | null): string {
  if (!ts) return '';
  const date = new Date(ts);
  if (isNaN(date.getTime())) return '';

  const now = new Date();
  const pad = (n: number) => String(n).padStart(2, '0');
  const hhmm = `${pad(date.getHours())}:${pad(date.getMinutes())}`;

  if (
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate()
  ) {
    return hhmm;
  }

  const mmdd = `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
  if (date.getFullYear() === now.getFullYear()) {
    return `${mmdd} ${hhmm}`;
  }

  return `${date.getFullYear()}-${mmdd} ${hhmm}`;
}
