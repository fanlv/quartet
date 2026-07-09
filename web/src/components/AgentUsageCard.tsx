import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import {
  agentUsageProvider,
  fetchAgentUsage,
  getCachedUsage,
  setCachedUsage,
  type AgentUsageProvider,
  type CodexUsage,
  type ClaudeUsage,
  type UsageWindow,
} from '../utils/agentUsage';
import './AgentUsageCard.css';

interface AgentUsageCardProps {
  agentType?: string;
  displayName?: string;
}

function money(n: number): string {
  return n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

function pctClass(pct: number): string {
  if (pct >= 80) return 'pct-hi';
  if (pct >= 50) return 'pct-mid';
  return 'pct-lo';
}

/** Tiny circular progress icon for a usage window. The arc fills to
 *  `percent` and is colored by usage tier; the short label (5h / 7d) sits in
 *  the center so each ring stays identifiable, and the full "5h 1% · 15:30
 *  重置" text lives in the tooltip. The tooltip shows on hover and can also be
 *  pinned open by clicking the ring (click again, or click outside, to hide).
 *  It is rendered in a portal with fixed positioning so no ancestor's
 *  `overflow: hidden` (the composer footer clips its content) can cut it off. */
function UsageRing({ percent, label, title }: { percent: number; label: string; title: string }) {
  const pct = Math.max(0, Math.min(100, percent));
  const [hover, setHover] = useState(false);
  const [pinned, setPinned] = useState(false);
  const [pos, setPos] = useState({ left: 0, top: 0 });
  const ref = useRef<HTMLSpanElement>(null);
  const size = 20;
  const stroke = 3;
  const r = (size - stroke) / 2;
  const c = 2 * Math.PI * r;
  const offset = c * (1 - pct / 100);
  const visible = hover || pinned;

  const updatePos = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const box = el.getBoundingClientRect();
    setPos({ left: box.left + box.width / 2, top: box.top - 6 });
  }, []);

  // Keep the portal tooltip anchored to the ring while it is visible, even as
  // the page scrolls or resizes.
  useLayoutEffect(() => {
    if (!visible) return;
    updatePos();
    const onMove = () => updatePos();
    window.addEventListener('scroll', onMove, true);
    window.addEventListener('resize', onMove);
    return () => {
      window.removeEventListener('scroll', onMove, true);
      window.removeEventListener('resize', onMove);
    };
  }, [visible, updatePos]);

  // While pinned, a click anywhere outside the ring dismisses the tooltip.
  useEffect(() => {
    if (!pinned) return;
    const onDocClick = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setPinned(false);
    };
    document.addEventListener('mousedown', onDocClick);
    return () => document.removeEventListener('mousedown', onDocClick);
  }, [pinned]);

  return (
    <span
      ref={ref}
      className={`usage-ring ${pctClass(pct)}${pinned ? ' pinned' : ''}`}
      onClick={() => setPinned((v) => !v)}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
    >
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
        <circle className="usage-ring-track" cx={size / 2} cy={size / 2} r={r} strokeWidth={stroke} fill="none" />
        <circle
          className="usage-ring-arc"
          cx={size / 2}
          cy={size / 2}
          r={r}
          strokeWidth={stroke}
          fill="none"
          strokeDasharray={c}
          strokeDashoffset={offset}
          strokeLinecap="round"
          transform={`rotate(-90 ${size / 2} ${size / 2})`}
        />
      </svg>
      <span className="usage-ring-label">{label}</span>
      {visible &&
        createPortal(
          <span className="usage-ring-tip" role="tooltip" style={{ left: pos.left, top: pos.top }}>
            {title}
          </span>,
          document.body,
        )}
    </span>
  );
}

const pad = (n: number): string => String(n).padStart(2, '0');

// Absolute reset time for a window. Codex returns reset_at (unix seconds); fall
// back to now + reset_after_seconds when the API omits it. `withDate` adds the
// MM-dd prefix for the multi-day (7d) window; the 5h window shows only HH:mm.
function formatResetAt(w: UsageWindow, withDate: boolean): string {
  const d = w.reset_at > 0 ? new Date(w.reset_at * 1000) : new Date(Date.now() + w.reset_after_seconds * 1000);
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return withDate ? `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}` : hm;
}

/** Compact inline usage strip shown in the composer footer (after the
 *  image-upload button) for the Codex / Claude agents. Returns null for any
 *  agent without a usage view.
 *
 *  Switching agent type keeps the previously-fetched plan info on screen (from
 *  a module-level cache) and refreshes it asynchronously — the new data swaps
 *  in only once its request returns, so there is no loading flash. A failed
 *  refresh is silent: the last successful data stays on screen, and if nothing
 *  ever succeeded the strip shows no data (no error message). */
export function AgentUsageCard({ agentType, displayName }: AgentUsageCardProps) {
  const { t } = useTranslation();
  const provider = agentUsageProvider(agentType, displayName);

  const [loading, setLoading] = useState(false);
  const [codex, setCodex] = useState<CodexUsage | null>(
    () => (getCachedUsage('codex') as CodexUsage | null) ?? null,
  );
  const [claude, setClaude] = useState<ClaudeUsage | null>(
    () => (getCachedUsage('claude') as ClaudeUsage | null) ?? null,
  );

  const load = useCallback((p: AgentUsageProvider) => {
    let cancelled = false;
    setLoading(true);
    fetchAgentUsage(p)
      .then((data) => {
        if (cancelled) return;
        setCachedUsage(p, data);
        if (p === 'codex') setCodex(data.codex ?? null);
        else setClaude(data.claude ?? null);
      })
      .catch(() => {
        // Swallow: keep the last successful data on screen (if any) and never
        // surface the request error to the user.
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!provider) return;
    const cancel = load(provider);
    return cancel;
  }, [provider, load]);

  if (!provider) return null;

  // Tooltip for a usage ring, e.g. "5h 1% · 15:30 重置".
  const ringTitle = (label: string, w: UsageWindow, withDate: boolean) =>
    `${label} ${Math.round(w.used_percent)}% · ${t('agentUsage.resetAt', {
      time: formatResetAt(w, withDate),
    })}`;

  const current = provider === 'codex' ? codex : claude;

  return (
    <div className="agent-usage-inline" data-testid="agent-usage-card" data-provider={provider}>
      {loading && !current ? (
        <span className="usage-spin" aria-label={t('agentUsage.loading')} />
      ) : provider === 'codex' && codex ? (
        <>
          {codex.version && <span className="usage-inline-ver">{codex.version}</span>}
          {codex.primary_window && (
            <UsageRing
              percent={codex.primary_window.used_percent}
              label="5h"
              title={ringTitle('5h', codex.primary_window, false)}
            />
          )}
          {codex.secondary_window && (
            <UsageRing
              percent={codex.secondary_window.used_percent}
              label="7d"
              title={ringTitle('7d', codex.secondary_window, true)}
            />
          )}
        </>
      ) : provider === 'claude' && claude ? (
        <>
          {claude.version && <span className="usage-inline-ver">{claude.version}</span>}
          <span className="usage-inline-metric" title={t('agentUsage.today')}>
            now <b className="today">${money(claude.today_cost)}</b>
          </span>
          <span className="usage-inline-metric" title={t('agentUsage.total')}>
            sum <b>${money(claude.total_cost)}</b>
          </span>
        </>
      ) : null}

      <button
        type="button"
        className={`usage-inline-refresh ${loading ? 'spinning' : ''}`}
        onClick={() => load(provider)}
        disabled={loading}
        title={t('agentUsage.refresh')}
        aria-label={t('agentUsage.refresh')}
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M23 4v6h-6M1 20v-6h6" />
          <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15" />
        </svg>
      </button>
    </div>
  );
}
