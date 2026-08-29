import { useState, useEffect, useRef } from 'react';
import { useServerNow } from '../contexts/ServerClock';
import { ClockIcon } from './ComposerIcons';
import './DurationBadge.css';

/**
 * Format a duration in milliseconds to a human-readable string.
 * - < 1s  → "234ms"
 * - < 60s → "1.2s"
 * - < 60m → "1m 23s"
 * - else  → "1h 2m"
 */
// eslint-disable-next-line react-refresh/only-export-components
export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  if (ms < 3_600_000) {
    const m = Math.floor(ms / 60_000);
    const s = Math.floor((ms % 60_000) / 1000);
    return `${m}m ${s}s`;
  }
  const h = Math.floor(ms / 3_600_000);
  const m = Math.floor((ms % 3_600_000) / 60_000);
  return `${h}h ${m}m`;
}

export type DurationVariant = 'thinking' | 'assistant' | 'tool' | 'total';

interface DurationBadgeProps {
  startedAt?: number | number[];
  endedAt?: number;
  baseMs?: number;
  variant?: DurationVariant;
  showIcon?: boolean;
}

export function DurationBadge({ startedAt, endedAt, baseMs = 0, variant = 'assistant', showIcon = false }: DurationBadgeProps) {
  // `now` here must be the server-wall-clock estimate, not the raw browser
  // clock. If we mixed frames (live uses client Date.now, finished uses
  // server timestamps), clock skew / SSE delivery latency / ring-buffer
  // replay all manifest as a visible jump when the badge switches from
  // running to finished. See ServerClockProvider for how the estimate is
  // maintained.
  const getServerNow = useServerNow();
  const startedAtList = Array.isArray(startedAt)
    ? startedAt.filter((value): value is number => value != null)
    : startedAt != null
      ? [startedAt]
      : [];
  // Start from the current server-clock estimate so a running badge does
  // not flash `0ms` on its first paint before the first timer tick lands.
  const [now, setNow] = useState<number>(() => getServerNow());
  // Keep the displayed duration monotonic across running → finished
  // transitions. Without this, a live estimate can be a few hundred ms
  // ahead of the server-reported terminal duration and visibly jump
  // backwards on completion.
  const computeElapsed = (endTime: number) => (
    startedAtList.length === 0
      ? baseMs
      : Math.max(0, baseMs + startedAtList.reduce((sum, start) => sum + (endTime - start), 0))
  );
  // Seed from current computed value so we don't regress if the badge becomes
  // finished before the first timer tick.
  const [maxShown, setMaxShown] = useState<number>(() => computeElapsed(endedAt ?? getServerNow()));
  const intervalRef = useRef<number | null>(null);
  const isRunning = startedAtList.length > 0 && endedAt == null;

  const inputsRef = useRef<{ startedAtList: number[]; endedAt?: number; baseMs: number }>({
    startedAtList,
    endedAt,
    baseMs,
  });
  // Keep the latest getServerNow in a ref so the running-tick effect does
  // not list it in its dependency array — doing so would reset the 250ms
  // interval on every parent render if the context ever returned a fresh
  // function, even though the underlying offset is stable.
  const getServerNowRef = useRef(getServerNow);
  getServerNowRef.current = getServerNow;

  useEffect(() => {
    inputsRef.current = { startedAtList, endedAt, baseMs };
    // Sync maxShown to not be below the latest computed value when props change.
    // Defer the state write to avoid `setState-in-effect` lint.
    const rafId = window.requestAnimationFrame(() => {
      const endTime = endedAt ?? getServerNowRef.current();
      const elapsed = computeElapsed(endTime);
      setMaxShown((prev) => (elapsed > prev ? elapsed : prev));
    });
    return () => window.cancelAnimationFrame(rafId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [startedAtList, endedAt, baseMs]);

  useEffect(() => {
    if (isRunning) {
      // Avoid a noticeable "0ms" flash: schedule an immediate tick after mount.
      // (eslint forbids calling setState synchronously in effects.)
      const tick = () => {
        const t = getServerNowRef.current();
        setNow(t);
        const { startedAtList: startList, endedAt: endAt, baseMs: base } = inputsRef.current;
        const endTime = endAt ?? t;
        const elapsed = startList.length === 0
          ? base
          : Math.max(0, base + startList.reduce((sum, start) => sum + (endTime - start), 0));
        setMaxShown((prev) => (elapsed > prev ? elapsed : prev));
      };

      const rafId = window.requestAnimationFrame(tick);
      intervalRef.current = window.setInterval(tick, 250);
      return () => {
        window.cancelAnimationFrame(rafId);
        if (intervalRef.current != null) window.clearInterval(intervalRef.current);
        intervalRef.current = null;
      };
    } else {
      if (intervalRef.current != null) {
        window.clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    }
  }, [isRunning]);

  if (startedAtList.length === 0) {
    if (baseMs > 0) {
      const displayed = Math.max(baseMs, maxShown);
      return (
        <span className={`duration-badge duration-badge--${variant} duration-badge--finished ${showIcon ? 'duration-badge--with-icon' : ''}`}>
          {showIcon && <ClockIcon />}
          {formatDuration(displayed)}
        </span>
      );
    }
    return null;
  }

  const endTime = endedAt ?? now;
  // `now` is the server-wall-clock estimate (see useServerNow). Keeping the
  // non-negative clamp as a belt-and-suspenders: the estimate is still
  // subject to brief transients right after mount (e.g. before any event
  // has been observed) during which `Date.now()` may sit slightly ahead
  // or behind the latest known server time. Clamping avoids a hidden
  // badge in that window.
  const elapsed = Math.max(0, baseMs + startedAtList.reduce((sum, start) => sum + (endTime - start), 0));
  const displayed = Math.max(elapsed, maxShown);

  return (
    <span className={`duration-badge duration-badge--${variant} ${isRunning ? 'duration-badge--running' : 'duration-badge--finished'} ${showIcon ? 'duration-badge--with-icon' : ''}`}>
      {showIcon && <ClockIcon />}
      {formatDuration(displayed)}
    </span>
  );
}
