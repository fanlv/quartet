import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import {
  agentUsageProvider,
  fetchAgentUsage,
  fetchAgentVersion,
  getCachedUsage,
  getCachedVersion,
  setCachedUsage,
  setCachedVersion,
  type AgentUsageProvider,
  type CodexUsage,
  type ClaudeUsage,
  type AntigravityUsage,
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
 *  `percent` and is colored by usage tier (or by an explicit `tone` class, used
 *  by the reset-credit ring whose color semantics differ); the short label (5h
 *  / 7d / a count) sits in the center so each ring stays identifiable, and the
 *  full detail lives in the tooltip. The tooltip shows on hover and can also be
 *  pinned open by clicking the ring (click again, or click outside, to hide).
 *  It is rendered in a portal with fixed positioning so no ancestor's
 *  `overflow: hidden` (the composer footer clips its content) can cut it off. */
function UsageRing({
  percent,
  label,
  title,
  tone,
}: {
  percent: number;
  label: string;
  title: ReactNode;
  tone?: string;
}) {
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
  const colorClass = tone ?? pctClass(pct);

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
      className={`usage-ring ${colorClass}${pinned ? ' pinned' : ''}`}
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

/** Small refresh button shared by the quota card and the version chip. */
function RefreshButton({ loading, onClick }: { loading: boolean; onClick: () => void }) {
  const { t } = useTranslation();
  return (
    <button
      type="button"
      className={`usage-inline-refresh ${loading ? 'spinning' : ''}`}
      onClick={onClick}
      disabled={loading}
      title={t('agentUsage.refresh')}
      aria-label={t('agentUsage.refresh')}
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
        <path d="M23 4v6h-6M1 20v-6h6" />
        <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15" />
      </svg>
    </button>
  );
}

const pad = (n: number): string => String(n).padStart(2, '0');

// Short label derived from the actual window length returned by the provider.
// Codex's primary / secondary field names do not imply a fixed duration.
function formatWindowLabel(windowSeconds: number): string {
  const day = 24 * 60 * 60;
  const hour = 60 * 60;
  const minute = 60;
  if (windowSeconds > 0 && windowSeconds % day === 0) return `${windowSeconds / day}d`;
  if (windowSeconds > 0 && windowSeconds % hour === 0) return `${windowSeconds / hour}h`;
  if (windowSeconds > 0 && windowSeconds % minute === 0) return `${windowSeconds / minute}m`;
  return `${Math.max(0, windowSeconds)}s`;
}

// Absolute reset time for a window. Codex returns reset_at (unix seconds); fall
// back to now + reset_after_seconds when the API omits it. Multi-day windows
// include the MM-dd prefix; shorter windows show only HH:mm.
function formatResetAt(
  w: UsageWindow,
  withDate = w.limit_window_seconds >= 24 * 60 * 60,
): string {
  const d = w.reset_at > 0 ? new Date(w.reset_at * 1000) : new Date(Date.now() + w.reset_after_seconds * 1000);
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return withDate
    ? `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}`
    : hm;
}

// Local "MM-dd HH:mm" for a reset-credit expiry (unix seconds).
function formatExpiry(unixSec: number): string {
  const d = new Date(unixSec * 1000);
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Compact inline usage strip shown in the composer footer (after the
 *  image-upload button). Codex / Claude get a full quota view; every other
 *  known ACP agent gets a version chip. Returns null for agents that are
 *  neither (no quota view and no parseable version, e.g. the built-in runner). */
export function AgentUsageCard({ agentType, displayName }: AgentUsageCardProps) {
  const provider = agentUsageProvider(agentType, displayName);
  if (provider) return <AgentQuotaCard provider={provider} />;
  if (agentType) return <AgentVersionChip command={agentType} />;
  return null;
}

/** Full quota view for the Codex / Claude agents (rate-limit rings + version,
 *  or today/total spend + version).
 *
 *  Switching agent type keeps the previously-fetched plan info on screen (from
 *  a localStorage cache that also survives page reloads) and refreshes it
 *  asynchronously — the new data swaps in only once its request returns, so
 *  there is no loading flash. A failed refresh is silent: the last cached data
 *  stays on screen, and if nothing was ever cached the strip shows no data (no
 *  error message). */
function AgentQuotaCard({ provider }: { provider: AgentUsageProvider }) {
  const { t } = useTranslation();

  const [loading, setLoading] = useState(false);
  const [codex, setCodex] = useState<CodexUsage | null>(
    () => (getCachedUsage('codex') as CodexUsage | null) ?? null,
  );
  const [claude, setClaude] = useState<ClaudeUsage | null>(
    () => (getCachedUsage('claude') as ClaudeUsage | null) ?? null,
  );
  const [antigravity, setAntigravity] = useState<AntigravityUsage | null>(
    () => (getCachedUsage('antigravity') as AntigravityUsage | null) ?? null,
  );

  const load = useCallback((p: AgentUsageProvider) => {
    let cancelled = false;
    setLoading(true);
    fetchAgentUsage(p)
      .then((data) => {
        if (cancelled) return;
        setCachedUsage(p, data);
        if (p === 'codex') setCodex(data.codex ?? null);
        else if (p === 'claude') setClaude(data.claude ?? null);
        else setAntigravity(data.antigravity ?? null);
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

  useEffect(() => load(provider), [provider, load]);

  // Tooltip for a usage ring, e.g. "5h 1% · 15:30 重置".
  const ringTitle = (label: string, w: UsageWindow, withDate?: boolean) =>
    `${label} ${Math.round(w.used_percent)}% · ${t('agentUsage.resetAt', {
      time: formatResetAt(w, withDate),
    })}`;

  const current = provider === 'codex' ? codex : provider === 'claude' ? claude : antigravity;

  return (
    <div className="agent-usage-inline" data-testid="agent-usage-card" data-provider={provider}>
      {loading && !current ? (
        <span className="usage-spin" aria-label={t('agentUsage.loading')} />
      ) : provider === 'codex' && codex ? (
        <>
          {codex.version && <span className="usage-inline-ver">{codex.version}</span>}
          {[codex.primary_window, codex.secondary_window].map((window, index) => {
            if (!window) return null;
            const label = formatWindowLabel(window.limit_window_seconds);
            return (
              <UsageRing
                key={index}
                percent={window.used_percent}
                label={label}
                title={ringTitle(label, window)}
              />
            );
          })}
          <UsageRing
            percent={codex.reset_credits > 0 ? 100 : 0}
            label={String(codex.reset_credits)}
            tone={codex.reset_credits > 0 ? 'credit' : 'credit-empty'}
            title={
              <>
                <span className="usage-tip-line usage-tip-head">
                  {t('agentUsage.resetCreditsLeft', { count: codex.reset_credits })}
                </span>
                {(codex.reset_credit_expiries ?? []).map((e, i) => (
                  <span key={i} className="usage-tip-line">
                    {t('agentUsage.creditExpiry', { time: formatExpiry(e) })}
                  </span>
                ))}
              </>
            }
          />
        </>
      ) : provider === 'claude' && claude ? (
        <>
          {claude.version && <span className="usage-inline-ver">{claude.version}</span>}
          <span className="usage-inline-metric" title={t('agentUsage.today')}>
            <svg
              className="usage-metric-icon today"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              <rect x="3" y="4.5" width="18" height="16.5" rx="2" />
              <path d="M16 2.5v4M8 2.5v4M3 9.5h18" />
            </svg>
            <b className="today">${money(claude.today_cost)}</b>
          </span>
          <span className="usage-inline-metric" title={t('agentUsage.total')}>
            <svg
              className="usage-metric-icon"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              <path d="M17 5H7l5 7-5 7h10" />
            </svg>
            <b>${money(claude.total_cost)}</b>
          </span>
        </>
      ) : provider === 'antigravity' && antigravity ? (
        <>
          {antigravity.version && <span className="usage-inline-ver">{antigravity.version}</span>}
          {(antigravity.claude_5h || antigravity.claude_weekly) && (
            <span className="usage-window-group" aria-label="Claude and GPT usage">
              <span className="usage-provider-mark" aria-hidden="true">C</span>
              {antigravity.claude_5h && (
                <UsageRing
                  percent={antigravity.claude_5h.used_percent}
                  label="5h"
                  title={ringTitle('Claude 5h', antigravity.claude_5h)}
                />
              )}
              {antigravity.claude_weekly && (
                <UsageRing
                  percent={antigravity.claude_weekly.used_percent}
                  label="7d"
                  title={ringTitle('Claude 7d', antigravity.claude_weekly, true)}
                />
              )}
            </span>
          )}
          {(antigravity.gemini_5h || antigravity.gemini_weekly) && (
            <span className="usage-window-group" aria-label="Gemini usage">
              <span className="usage-provider-mark" aria-hidden="true">G</span>
              {antigravity.gemini_5h && (
                <UsageRing
                  percent={antigravity.gemini_5h.used_percent}
                  label="5h"
                  title={ringTitle('Gemini 5h', antigravity.gemini_5h)}
                />
              )}
              {antigravity.gemini_weekly && (
                <UsageRing
                  percent={antigravity.gemini_weekly.used_percent}
                  label="7d"
                  title={ringTitle('Gemini 7d', antigravity.gemini_weekly, true)}
                />
              )}
            </span>
          )}
        </>
      ) : null}

      <RefreshButton loading={loading} onClick={() => load(provider)} />
    </div>
  );
}

/** Version-only chip for known ACP agents that have no quota view (traex,
 *  opencode, gemini, cursor, droid, qwen, ...). The version is fetched from the
 *  backend (`<bin> --version`) and cached per agent command so switching agents
 *  shows the last-probed version instantly while a fresh probe runs.
 *
 *  Version is supplementary, so the chip renders nothing until it actually has
 *  a version: agents without a parseable version — or non-ACP agents like the
 *  built-in runner — show no empty strip and no loading flash. */
function AgentVersionChip({ command }: { command: string }) {
  const [version, setVersion] = useState<string>(() => getCachedVersion(command));
  const [loading, setLoading] = useState(false);

  const load = useCallback((cmd: string) => {
    let cancelled = false;
    setLoading(true);
    fetchAgentVersion(cmd)
      .then((v) => {
        if (cancelled) return;
        setCachedVersion(cmd, v);
        setVersion(v);
      })
      .catch(() => {
        // Swallow: keep the last cached version (if any); never surface errors.
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    // Swap to the cached version for the newly-selected agent instantly, then
    // refresh it in the background.
    setVersion(getCachedVersion(command));
    return load(command);
  }, [command, load]);

  if (!version) return null;

  return (
    <div className="agent-usage-inline" data-testid="agent-usage-card" data-provider="version">
      <span className="usage-inline-ver">{version}</span>
      <RefreshButton loading={loading} onClick={() => load(command)} />
    </div>
  );
}
