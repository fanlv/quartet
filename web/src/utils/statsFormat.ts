// Format helpers shared by the Job List day header, the Usage Stats page and
// the chat composer's context badge. Kept close to the i18n / display layer so
// the same conventions govern every place where stats appear.

/**
 * Format a duration (ms) for the usage-stats display.
 *
 * Distinct from DurationBadge's `formatDuration` which is geared toward
 * per-step "exact" timings (showing seconds even at the hour scale).
 * Stats are roll-up totals where second-level precision is noise:
 *   - < 1s   → "0s"
 *   - < 60s  → "30s"
 *   - < 60m  → "18m"
 *   - else   → "1h 5m"
 */
export function formatStatsDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0s';
  if (ms < 1000) return '0s';
  if (ms < 60_000) {
    return `${Math.floor(ms / 1000)}s`;
  }
  if (ms < 3_600_000) {
    return `${Math.floor(ms / 60_000)}m`;
  }
  const h = Math.floor(ms / 3_600_000);
  const m = Math.floor((ms % 3_600_000) / 60_000);
  if (m === 0) return `${h}h`;
  return `${h}h ${m}m`;
}

/**
 * Format a count (turn / token / call) using K / M abbreviations once it
 * gets large enough that the raw digits become hard to scan. Below 1k we
 * keep the exact integer.
 */
export function formatStatsCount(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0';
  if (n < 1000) return String(Math.floor(n));
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}K`;
  return `${(n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0)}M`;
}

/**
 * Format a context size (tokens) for the chat composer badge.
 *
 * Keeps two decimals so the number visibly moves round to round — a context
 * badge that only shows "128K" looks frozen while the conversation grows —
 * and switches to M past a million rather than printing "1234.57K", which
 * modern long-context models reach.
 */
export function formatTokenCount(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0';
  if (n < 1000) return String(Math.floor(n));
  if (n < 1_000_000) return `${(n / 1000).toFixed(2)}K`;
  return `${(n / 1_000_000).toFixed(2)}M`;
}
