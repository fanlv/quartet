import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { copyToClipboard } from '../utils/clipboard';
import { showToast } from '../utils/toast';
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
  type KimiUsage,
  type QoderUsage,
  type CursorUsage,
  type UsageWindow,
} from '../utils/agentUsage';
import './AgentUsageCard.css';

interface AgentUsageCardProps {
  agentType?: string;
  displayName?: string;
}

function pctClass(pct: number): string {
  if (pct >= 80) return 'pct-hi';
  if (pct >= 50) return 'pct-mid';
  return 'pct-lo';
}

// Credit counts come back as floats (300.0); show whole numbers cleanly and keep
// at most one decimal for fractional pools.
function formatCredits(n: number): string {
  return Number.isInteger(n) ? String(n) : n.toLocaleString(undefined, { maximumFractionDigits: 1 });
}

// Turn a snake_case API plan id (e.g. "personal_professional_trial") into a
// human-friendly Title Case label for the tooltip.
function prettifyPlan(plan?: string): string {
  if (!plan) return '';
  return plan
    .split('_')
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ');
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

/** Wraps arbitrary content with the same hover/click-to-pin portaled tooltip
 *  the usage rings use, so non-ring content (e.g. the QoderCN credits meter)
 *  can carry a rich multi-line tooltip without duplicating the positioning
 *  logic. The tooltip reuses the `.usage-ring-tip` style. */
function HoverTip({
  tip,
  children,
  ariaLabel,
  onActivate,
}: {
  tip: ReactNode;
  children: ReactNode;
  ariaLabel?: string;
  onActivate?: () => void;
}) {
  const [hover, setHover] = useState(false);
  const [pinned, setPinned] = useState(false);
  const [pos, setPos] = useState({ left: 0, top: 0 });
  const ref = useRef<HTMLSpanElement>(null);
  const visible = hover || pinned;

  const updatePos = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const box = el.getBoundingClientRect();
    setPos({ left: box.left + box.width / 2, top: box.top - 6 });
  }, []);

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
      className="usage-tip-anchor"
      role="button"
      tabIndex={0}
      aria-label={ariaLabel}
      onClick={() => {
        setPinned((v) => !v);
        onActivate?.();
      }}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          setPinned((value) => !value);
          onActivate?.();
        }
      }}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
    >
      {children}
      {visible &&
        createPortal(
          <span className="usage-ring-tip" role="tooltip" style={{ left: pos.left, top: pos.top }}>
            {tip}
          </span>,
          document.body,
        )}
    </span>
  );
}

function UsageErrorIndicator({ message }: { message: string }) {
  const { t } = useTranslation();
  return (
    <HoverTip
      tip={<span className="usage-error-detail">{message}</span>}
      ariaLabel={t('agentUsage.error')}
      onActivate={() => {
        void copyToClipboard(message)
          .then(() => showToast(t('common.copySuccess')))
          .catch(() => showToast(t('common.copyFailed')));
      }}
    >
      <span className="usage-inline-error" title={message}>!</span>
    </HoverTip>
  );
}

interface AntigravityWindow {
  label: string;
  value: UsageWindow;
  withDate?: boolean;
}

/** Antigravity exposes two independent quota pools. A named, two-row meter is
 *  easier to scan than four adjacent rings: the model, window, percentage and
 *  pressure are all visible without opening the tooltip. */
function AntigravityQuotaGroup({
  name,
  windows,
  ringTitle,
}: {
  name: string;
  windows: AntigravityWindow[];
  ringTitle: (label: string, value: UsageWindow, withDate?: boolean) => ReactNode;
}) {
  const { t } = useTranslation();

  return (
    <HoverTip
      tip={
        <>
          <span className="usage-tip-line usage-tip-head">
            {name} · {t('agentUsage.used')}
          </span>
          {windows.map((window) => (
            <span key={window.label} className="usage-tip-line">
              {ringTitle(`${name} ${window.label}`, window.value, window.withDate)}
            </span>
          ))}
        </>
      }
    >
      <span
        className="usage-provider-quota"
        aria-label={`${name} ${t('agentUsage.used')}, ${windows
          .map((window) => `${window.label} ${Math.round(window.value.used_percent)}%`)
          .join(', ')}`}
      >
        <span className="usage-provider-quota-head">
          <strong>{name}</strong>
          <span>{t('agentUsage.used')}</span>
        </span>
        {windows.map((window) => {
          const percent = Math.max(0, Math.min(100, window.value.used_percent));
          const tier = pctClass(percent);
          return (
            <span key={window.label} className="usage-window-row">
              <span className="usage-window-period">{window.label}</span>
              <strong className={tier}>{Math.round(percent)}%</strong>
              <span className="usage-window-bar" aria-hidden="true">
                <span className={`usage-window-fill ${tier}`} style={{ width: `${percent}%` }} />
              </span>
            </span>
          );
        })}
      </span>
    </HoverTip>
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
 *  image-upload button). Supported providers get their quota view; every other
 *  known ACP agent gets a version chip. Returns null for agents that are
 *  neither (no quota view and no parseable version, e.g. the built-in runner). */
export function AgentUsageCard({ agentType, displayName }: AgentUsageCardProps) {
  const provider = agentUsageProvider(agentType, displayName);
  if (provider && agentType) return <AgentQuotaCard provider={provider} command={agentType} />;
  if (agentType) return <AgentVersionChip command={agentType} />;
  return null;
}

/** Full quota view for supported agents (rate-limit rings + version and any
 *  provider-specific plan details).
 *
 *  Switching agent type keeps the previously-fetched plan info on screen (from
 *  a localStorage cache that also survives page reloads) and refreshes it
 *  asynchronously — the new data swaps in only once its request returns, so
 *  there is no loading flash. A failed refresh keeps the last cached data and
 *  exposes the full error through a compact warning control. */
function AgentQuotaCard({ provider, command }: { provider: AgentUsageProvider; command: string }) {
  const { t } = useTranslation();

  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fallbackVersion, setFallbackVersion] = useState(() => getCachedVersion(command));
  const requestSequence = useRef(0);
  const [codex, setCodex] = useState<CodexUsage | null>(
    () => (getCachedUsage('codex') as CodexUsage | null) ?? null,
  );
  const [claude, setClaude] = useState<ClaudeUsage | null>(
    () => (getCachedUsage('claude') as ClaudeUsage | null) ?? null,
  );
  const [antigravity, setAntigravity] = useState<AntigravityUsage | null>(
    () => (getCachedUsage('antigravity') as AntigravityUsage | null) ?? null,
  );
  const [kimi, setKimi] = useState<KimiUsage | null>(
    () => (getCachedUsage('kimi') as KimiUsage | null) ?? null,
  );
  const [qoder, setQoder] = useState<QoderUsage | null>(
    () => (getCachedUsage('qoder') as QoderUsage | null) ?? null,
  );
  const [cursor, setCursor] = useState<CursorUsage | null>(
    () => (getCachedUsage('cursor') as CursorUsage | null) ?? null,
  );

  const load = useCallback((p: AgentUsageProvider) => {
    const sequence = ++requestSequence.current;
    setLoading(true);
    setError(null);
    fetchAgentUsage(p)
      .then((data) => {
        if (sequence !== requestSequence.current) return;
        setCachedUsage(p, data);
        if (p === 'codex') setCodex(data.codex ?? null);
        else if (p === 'claude') setClaude(data.claude ?? null);
        else if (p === 'antigravity') setAntigravity(data.antigravity ?? null);
        else if (p === 'kimi') setKimi(data.kimi ?? null);
        else if (p === 'qoder') setQoder(data.qoder ?? null);
        else if (p === 'cursor') setCursor(data.cursor ?? null);
      })
      .catch((reason: unknown) => {
        if (sequence !== requestSequence.current) return;
        // Preserve the last successful snapshot, but do not hide the live
        // refresh failure. The warning exposes the complete server message.
        setError(reason instanceof Error ? reason.message : String(reason));
        void fetchAgentVersion(command).then((version) => {
          if (sequence !== requestSequence.current) return;
          setCachedVersion(command, version);
          setFallbackVersion(version);
        }).catch(() => {
          // The complete quota error is already visible. Version fallback is
          // supplementary and must not replace that primary failure.
        });
      })
      .finally(() => {
        if (sequence === requestSequence.current) setLoading(false);
      });
    return () => {
      if (sequence === requestSequence.current) requestSequence.current += 1;
    };
  }, [command]);

  useEffect(() => () => { requestSequence.current += 1; }, []);

  useEffect(() => {
    setFallbackVersion(getCachedVersion(command));
    return load(provider);
  }, [command, provider, load]);

  // Tooltip for a usage ring, e.g. "5h 1% · 15:30 重置".
  const ringTitle = (label: string, w: UsageWindow, withDate?: boolean) => {
    const usage = `${label} ${Math.round(w.used_percent)}%`;
    if (w.reset_at <= 0 && w.reset_after_seconds <= 0) return usage;
    return `${usage} · ${t('agentUsage.resetAt', { time: formatResetAt(w, withDate) })}`;
  };

  const current =
    provider === 'codex'
      ? codex
      : provider === 'claude'
        ? claude
        : provider === 'antigravity'
          ? antigravity
          : provider === 'qoder'
            ? qoder
            : provider === 'kimi'
              ? kimi
              : cursor;
  const currentVersion = current?.version;

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
          {claude.plan_type && <span className="usage-inline-ver">{prettifyPlan(claude.plan_type)}</span>}
          {[
            { label: '5h', window: claude.five_hour },
            { label: '7d', window: claude.seven_day },
            { label: 'Opus', window: claude.seven_day_opus },
            ...(claude.weekly_scoped ?? []).map((window) => ({ label: window.label, window })),
          ].map(({ label, window }, index) => {
            if (!window) return null;
            return (
              <UsageRing
                key={`${label}-${index}`}
                percent={window.used_percent}
                label={label}
                title={ringTitle(label, window)}
              />
            );
          })}
          {claude.extra_usage?.is_enabled && (
            <HoverTip
              tip={t('agentUsage.claudeExtraUsage', {
                used: formatCredits(claude.extra_usage.used_credits ?? 0),
                limit: claude.extra_usage.monthly_limit == null
                  ? '—'
                  : formatCredits(claude.extra_usage.monthly_limit),
                currency: claude.extra_usage.currency || 'USD',
              })}
            >
              <span className="usage-inline-metric">
                <b>{formatCredits(claude.extra_usage.used_credits ?? 0)}</b>
                <span>{claude.extra_usage.currency || 'USD'}</span>
              </span>
            </HoverTip>
          )}
        </>
      ) : provider === 'antigravity' && antigravity ? (
        <span className="usage-antigravity">
          {antigravity.version && <span className="usage-inline-ver">{antigravity.version}</span>}
          <span className="usage-antigravity-groups">
            {(antigravity.claude_5h || antigravity.claude_weekly) && (
              <AntigravityQuotaGroup
                name="Claude"
                windows={[
                  ...(antigravity.claude_5h
                    ? [{ label: '5h', value: antigravity.claude_5h }]
                    : []),
                  ...(antigravity.claude_weekly
                    ? [{ label: '7d', value: antigravity.claude_weekly, withDate: true }]
                    : []),
                ]}
                ringTitle={ringTitle}
              />
            )}
            {(antigravity.gemini_5h || antigravity.gemini_weekly) && (
              <AntigravityQuotaGroup
                name="Gemini"
                windows={[
                  ...(antigravity.gemini_5h
                    ? [{ label: '5h', value: antigravity.gemini_5h }]
                    : []),
                  ...(antigravity.gemini_weekly
                    ? [{ label: '7d', value: antigravity.gemini_weekly, withDate: true }]
                    : []),
                ]}
                ringTitle={ringTitle}
              />
            )}
          </span>
        </span>
      ) : provider === 'qoder' && qoder ? (
        <>
          {qoder.version && <span className="usage-inline-ver">{qoder.version}</span>}
          <HoverTip
            tip={
              <>
                <span className="usage-tip-line usage-tip-head">
                  {t('agentUsage.qoderUsed', { used: formatCredits(qoder.used), total: formatCredits(qoder.total) })}
                </span>
                {prettifyPlan(qoder.plan_type) && (
                  <span className="usage-tip-line">{prettifyPlan(qoder.plan_type)}</span>
                )}
                {qoder.expires_at ? (
                  <span className="usage-tip-line">
                    {t('agentUsage.qoderExpires', { time: formatExpiry(qoder.expires_at) })}
                  </span>
                ) : null}
                {qoder.quota_exceeded && (
                  <span className="usage-tip-line">{t('agentUsage.qoderExceeded')}</span>
                )}
              </>
            }
          >
            <span className="usage-inline-metric usage-qoder">
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
                <circle cx="12" cy="12" r="9" />
                <path d="M12 7v10M15 9.3c0-1.3-1.3-2-3-2s-3 .7-3 2c0 2.6 6 1.6 6 4.2 0 1.3-1.3 2-3 2s-3-.7-3-2" />
              </svg>
              <b className={qoder.quota_exceeded ? 'pct-hi' : pctClass(qoder.used_percent)}>
                {formatCredits(qoder.used)}
              </b>
              <span className="usage-qoder-total">/ {formatCredits(qoder.total)}</span>
              <span className="usage-qoder-bar" aria-hidden="true">
                <span
                  className={`usage-qoder-fill ${qoder.quota_exceeded ? 'pct-hi' : pctClass(qoder.used_percent)}`}
                  style={{ width: `${Math.max(0, Math.min(100, qoder.used_percent))}%` }}
                />
              </span>
            </span>
          </HoverTip>
        </>
      ) : provider === 'kimi' && kimi ? (
        <>
          {kimi.version && <span className="usage-inline-ver">{kimi.version}</span>}
          {[kimi.weekly, kimi.five_hour].map((window, index) => {
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
          {kimi.total && (
            <UsageRing
              percent={kimi.total.used_percent}
              label="Σ"
              title={
                <>
                  <span className="usage-tip-line usage-tip-head">
                    {t('agentUsage.kimiTotal')} {Math.round(kimi.total.used_percent)}%
                  </span>
                  {kimi.parallel_limit ? (
                    <span className="usage-tip-line">
                      {t('agentUsage.kimiParallel', { count: kimi.parallel_limit })}
                    </span>
                  ) : null}
                </>
              }
            />
          )}
        </>
      ) : provider === 'cursor' && cursor ? (
        <>
          {cursor.version && <span className="usage-inline-ver">{cursor.version}</span>}
          {prettifyPlan(cursor.membership_type) && (
            <span className="usage-inline-ver">{prettifyPlan(cursor.membership_type)}</span>
          )}
          {[
            { label: t('agentUsage.cursorTotal'), window: cursor.primary_window, withDate: true },
            { label: 'Auto', window: cursor.secondary_window, withDate: true },
            { label: 'API', window: cursor.tertiary_window, withDate: true },
            // Grok Bot periods are not monthly; let the date prefix follow the
            // window length like the other providers.
            { label: 'Grok', window: cursor.grok_bot_window },
          ].map(({ label, window, withDate }, index) => {
            if (!window) return null;
            return (
              <UsageRing
                key={`${label}-${index}`}
                percent={window.used_percent}
                label={label}
                title={ringTitle(label, window, withDate)}
              />
            );
          })}
        </>
      ) : null}
      {!currentVersion && fallbackVersion && <span className="usage-inline-ver">{fallbackVersion}</span>}

      <RefreshButton loading={loading} onClick={() => load(provider)} />
      {error && <UsageErrorIndicator message={error} />}
    </div>
  );
}

/** Version-only chip for known ACP agents that have no quota view (traex,
 *  opencode, cursor, droid, qwen, ...). The version is fetched from the
 *  backend (`<bin> --version`) and cached per agent command so switching agents
 *  shows the last-probed version instantly while a fresh probe runs.
 *
 *  Version is supplementary, so the chip renders nothing until it actually has
 *  a version: agents without a parseable version — or non-ACP agents like the
 *  built-in runner — show no empty strip and no loading flash. */
function AgentVersionChip({ command }: { command: string }) {
  const [version, setVersion] = useState<string>(() => getCachedVersion(command));
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestSequence = useRef(0);

  const load = useCallback((cmd: string) => {
    const sequence = ++requestSequence.current;
    setLoading(true);
    setError(null);
    fetchAgentVersion(cmd)
      .then((v) => {
        if (sequence !== requestSequence.current) return;
        setCachedVersion(cmd, v);
        setVersion(v);
      })
      .catch((reason: unknown) => {
        if (sequence !== requestSequence.current) return;
        setError(reason instanceof Error ? reason.message : String(reason));
      })
      .finally(() => {
        if (sequence === requestSequence.current) setLoading(false);
      });
    return () => {
      if (sequence === requestSequence.current) requestSequence.current += 1;
    };
  }, []);

  useEffect(() => () => { requestSequence.current += 1; }, []);

  useEffect(() => {
    // Swap to the cached version for the newly-selected agent instantly, then
    // refresh it in the background.
    setVersion(getCachedVersion(command));
    return load(command);
  }, [command, load]);

  if (!version && !error) return null;

  return (
    <div className="agent-usage-inline" data-testid="agent-usage-card" data-provider="version">
      <span className="usage-inline-ver">{version}</span>
      <RefreshButton loading={loading} onClick={() => load(command)} />
      {error && <UsageErrorIndicator message={error} />}
    </div>
  );
}
