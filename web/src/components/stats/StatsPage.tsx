import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
} from 'react';
import { useTranslation } from 'react-i18next';
import { formatStatsCount, formatStatsDuration } from '../../utils/statsFormat';
import './StatsPage.css';

// Types mirroring the /api/v1/stats/usage response. The shapes intentionally
// mirror the Go-side structs (lowerCamel) so adding/removing a field on the
// backend is a single mechanical change here.
interface SectionTotals {
  totalMs: number;
  turnCount: number;
  assistantCount: number;
  thoughtCount: number;
  toolCallCount: number;
  tokens: TokenTotals;
}

interface TokenTotals {
  // `total` is the backend-computed display total. Provider details and the
  // estimate fields below explain that value; they must never be added to it.
  total: number;
  reported?: number;
  input?: number;
  output?: number;
  cachedRead?: number;
  cachedWrite?: number;
  reasoning?: number;
  imageEstimate?: number;
  estimated?: number;
  reportedTurns?: number;
  estimatedTurns?: number;
  assistant: number;
  thought: number;
  toolCall: number;
}

interface WorkspaceRow extends SectionTotals {
  workspaceId: string;
  workspaceName?: string;
  deleted?: boolean;
}

interface ModelRow extends SectionTotals {
  modelId: string;
  modelName?: string;
}

interface ToolRow {
  toolKey: string;
  count: number;
  totalMs: number;
}

interface DailyRow extends SectionTotals {
  date: string;
  models?: Record<string, SectionTotals>;
  modelNames?: Record<string, string>;
}

// PreviousTotals carries the equal-length preceding period's headline metrics
// so the overview cards can render period-over-period deltas. Present only
// when compare=1 and the range is bounded (not "All").
interface PreviousTotals {
  totalMs: number;
  turnCount: number;
  toolCallCount: number;
  tokensTotal: number;
  cacheHitRate?: number | null;
  workspaceCount: number;
}

interface UsageReport {
  range: {
    from: string;
    to: string;
  };
  byWorkspace: WorkspaceRow[];
  byModel: ModelRow[];
  byTool: ToolRow[];
  daily: DailyRow[];
  previous?: PreviousTotals;
  note: string;
  failed?: boolean;
  error?: string;
}

type RangePreset = '7d' | '30d' | '90d' | 'all' | 'custom';
// TrendMetric is the metric the trend chart encodes. Scoped to the trend
// chart only; the KPI band and rank lists each show a fixed metric.
type TrendMetric = 'duration' | 'turns' | 'tokens' | 'cache';

const UNKNOWN_MODEL_ID = '__unknown_model__';
const API_UNKNOWN_MODEL_ID = '(unknown model)';
const TOTAL_SERIES_KEY = '__total__';
const TOP_N = 8;

// Color lock for KPI band + rank lists: the same botanical ladder used by
// QuartetTheme on iOS, plus one neutral fallback.
const TOTAL_COLOR = '#16a34a';
const SERIES_LADDER = ['#047857', '#059669', '#16a34a', '#22c55e', '#4d7c0f', '#84cc16', '#a3e635'];
const NEUTRAL_SERIES = '#738077';

function seriesColor(idx: number): string {
  if (idx < SERIES_LADDER.length) return SERIES_LADDER[idx];
  return NEUTRAL_SERIES;
}

// Total stays in the brand green; model lines use the same categorical order
// as iOS and resolve through theme-aware CSS variables.
const TREND_PALETTE = [
  'var(--chart-blue)',
  'var(--chart-orange)',
  'var(--chart-violet)',
  'var(--chart-rose)',
  'var(--chart-cyan)',
  'var(--chart-amber)',
  'var(--chart-graphite)',
];

function trendColor(idx: number): string {
  return TREND_PALETTE[idx % TREND_PALETTE.length];
}

function isUnknownModelLabel(value?: string): boolean {
  return !value || value === UNKNOWN_MODEL_ID || value === API_UNKNOWN_MODEL_ID;
}

interface StatsPageProps {
  onClose: () => void;
  currentWorkspaceId?: string;
  onJumpToWorkspace?: (workspaceId: string) => void;
}

// Shift today's date by `days` days into the past. Returned as YYYY-MM-DD in
// local time so it matches the server-side day key.
function shiftDate(days: number): string {
  const d = new Date();
  d.setDate(d.getDate() - days);
  return formatDateKey(d);
}

function todayDateKey(): string {
  return shiftDate(0);
}

function presetRange(preset: RangePreset): { from: string; to: string } {
  const to = todayDateKey();
  switch (preset) {
    case '7d':
      return { from: shiftDate(6), to };
    case '30d':
      return { from: shiftDate(29), to };
    case '90d':
      return { from: shiftDate(89), to };
    case 'all':
      return { from: '', to: '' };
    default:
      return { from: shiftDate(6), to };
  }
}

// rangeDays returns the inclusive day span of a from/to pair, or 0 when either
// bound is missing (e.g. "All").
function rangeDays(from: string, to: string): number {
  const f = parseDateKey(from);
  const t = parseDateKey(to);
  if (!f || !t) return 0;
  return Math.round((t.getTime() - f.getTime()) / 86_400_000) + 1;
}

export function StatsPage({ onClose, currentWorkspaceId, onJumpToWorkspace }: StatsPageProps) {
  const { t, i18n } = useTranslation();
  const [preset, setPreset] = useState<RangePreset>('30d');
  const [customFrom, setCustomFrom] = useState<string>(shiftDate(29));
  const [customTo, setCustomTo] = useState<string>(todayDateKey());
  const [trendMetric, setTrendMetric] = useState<TrendMetric>('tokens');
  const [report, setReport] = useState<UsageReport | null>(null);
  const [loading, setLoading] = useState<boolean>(false);
  const [error, setError] = useState<string>('');
  const requestSeqRef = useRef(0);
  const lastSuccessfulReportRef = useRef<UsageReport | null>(null);

  const { from, to } = useMemo(() => {
    if (preset === 'custom') return { from: customFrom, to: customTo };
    return presetRange(preset);
  }, [preset, customFrom, customTo]);

  const fetchReport = useCallback(async (signal?: AbortSignal) => {
    const requestSeq = requestSeqRef.current + 1;
    requestSeqRef.current = requestSeq;
    const shouldApply = () => requestSeqRef.current === requestSeq && !signal?.aborted;
    setLoading(true);
    setError('');
    setReport(null);
    try {
      const params = new URLSearchParams();
      if (preset === 'all') params.set('all', '1');
      if (from) params.set('from', from);
      if (to) params.set('to', to);
      // Period-over-period deltas only make sense for a bounded window.
      if (preset !== 'all') params.set('compare', '1');
      const url = '/api/v1/stats/usage' + (params.toString() ? `?${params.toString()}` : '');
      const res = await fetch(url, {
        signal,
        headers: { 'Accept-Language': i18n.resolvedLanguage || i18n.language },
      });
      if (!res.ok) {
        const errBody = await res.text().catch(() => '');
        throw new Error(errBody || `HTTP ${res.status}`);
      }
      const data = (await res.json()) as UsageReport;
      if (!shouldApply()) return;
      const normalized = {
        ...data,
        byWorkspace: data.byWorkspace || [],
        byModel: data.byModel || [],
        byTool: data.byTool || [],
        daily: data.daily || [],
      };
      if (data.failed || data.error) {
        // The backend may return partial aggregates together with an error
        // when only some month files failed to load. Do not render partial
        // data as the current result; only fall back to a known-complete
        // previous report if one exists.
        setReport(lastSuccessfulReportRef.current);
        setError(data.error || t('stats.unknownError'));
        return;
      }
      lastSuccessfulReportRef.current = normalized;
      setReport(normalized);
    } catch (err) {
      if (!shouldApply() || (err instanceof DOMException && err.name === 'AbortError')) return;
      const msg = err instanceof Error ? err.message : String(err);
      setReport(lastSuccessfulReportRef.current);
      setError(msg);
    } finally {
      if (shouldApply()) setLoading(false);
    }
  }, [from, i18n.language, i18n.resolvedLanguage, preset, t, to]);

  useEffect(() => {
    const controller = new AbortController();
    void fetchReport(controller.signal);
    return () => controller.abort();
  }, [fetchReport]);

  const isEmpty = !loading && !error && (!report || (
    report.byWorkspace.length === 0 &&
    report.byModel.length === 0 &&
    report.byTool.length === 0 &&
    report.daily.length === 0
  ));

  // KPI totals are summed from the workspace rows (one row per active
  // workspace), which already roll up every model + tool for that workspace.
  const kpis = useMemo(() => computeKpis(report), [report]);
  const periodDays = rangeDays(from, to);

  return (
    <main className="stats-page">
      <div className="stats-shell">
        <header className="stats-header">
          <h2 className="stats-title">{t('stats.title')}</h2>
          <div className="stats-range">
            <div className="stats-segmented">
              {(['7d', '30d', '90d', 'all', 'custom'] as RangePreset[]).map((p) => (
                <button
                  key={p}
                  className={`stats-segmented-btn ${preset === p ? 'active' : ''}`}
                  onClick={() => setPreset(p)}
                >
                  {t(`stats.range${p === '7d' ? '7d' : p === '30d' ? '30d' : p === '90d' ? '90d' : p === 'all' ? 'All' : 'Custom'}`)}
                </button>
              ))}
            </div>
            {preset === 'custom' && (
              <div className="stats-custom-range">
                <input
                  type="date"
                  aria-label={t('stats.customFrom')}
                  value={customFrom}
                  max={customTo || undefined}
                  onChange={(e) => setCustomFrom(e.target.value)}
                />
                <span className="stats-custom-sep">→</span>
                <input
                  type="date"
                  aria-label={t('stats.customTo')}
                  value={customTo}
                  min={customFrom || undefined}
                  onChange={(e) => setCustomTo(e.target.value)}
                />
              </div>
            )}
          </div>
          <button className="stats-close" onClick={onClose} aria-label={t('common.close')}>×</button>
        </header>

        <div className="stats-body">
          {loading && !report && <StatsSkeleton />}
          {error && (
            <div className="stats-status stats-error">
              <div>{t('stats.fetchError', { message: error })}</div>
              <button type="button" className="stats-retry-btn" onClick={() => void fetchReport()}>
                {t('stats.retry')}
              </button>
            </div>
          )}
          {!loading && !error && isEmpty && (
            <div className="stats-empty">
              <div className="stats-empty-title">{t('stats.noDataInRange')}</div>
              <div className="stats-empty-hint">{t('stats.emptyHint')}</div>
            </div>
          )}
          {report && !isEmpty && (
            <>
              <KpiBand kpis={kpis} previous={report.previous} periodDays={periodDays} />
              <TrendCard
                range={report.range}
                daily={report.daily}
                metric={trendMetric}
                onMetricChange={setTrendMetric}
              />
              <div className="stats-rank-grid">
                <ByWorkspaceRank
                  rows={report.byWorkspace}
                  currentWorkspaceId={currentWorkspaceId}
                  onJumpToWorkspace={onJumpToWorkspace}
                />
                <ByModelRank rows={report.byModel} />
                <ByToolRank rows={report.byTool} />
              </div>
            </>
          )}
        </div>
      </div>
    </main>
  );
}

// ---------------------------------------------------------------------------
// KPI overview band
// ---------------------------------------------------------------------------

interface Kpis {
  totalMs: number;
  turnCount: number;
  tokensTotal: number;
  cacheHitRate: number | null;
  toolCallCount: number;
  workspaceCount: number;
}

function computeKpis(report: UsageReport | null): Kpis {
  const out: Kpis = { totalMs: 0, turnCount: 0, tokensTotal: 0, cacheHitRate: null, toolCallCount: 0, workspaceCount: 0 };
  if (!report) return out;
  const cacheTokens: TokenTotals = { total: 0, assistant: 0, thought: 0, toolCall: 0 };
  out.workspaceCount = report.byWorkspace.length;
  for (const ws of report.byWorkspace) {
    out.totalMs += ws.totalMs;
    out.turnCount += ws.turnCount;
    out.tokensTotal += ws.tokens.total;
    cacheTokens.reported = tokenCount(cacheTokens, 'reported') + tokenCount(ws.tokens, 'reported');
    cacheTokens.input = tokenCount(cacheTokens, 'input') + tokenCount(ws.tokens, 'input');
    cacheTokens.output = tokenCount(cacheTokens, 'output') + tokenCount(ws.tokens, 'output');
    cacheTokens.cachedRead = tokenCount(cacheTokens, 'cachedRead') + tokenCount(ws.tokens, 'cachedRead');
    cacheTokens.cachedWrite = tokenCount(cacheTokens, 'cachedWrite') + tokenCount(ws.tokens, 'cachedWrite');
    out.toolCallCount += ws.toolCallCount;
  }
  out.cacheHitRate = tokenCacheHitRate(cacheTokens);
  return out;
}

interface KpiCardSpec {
  key: string;
  label: string;
  value: string;
  current?: number;
  previous?: number;
}

function KpiBand({ kpis, previous, periodDays }: { kpis: Kpis; previous?: PreviousTotals; periodDays: number }) {
  const { t } = useTranslation();
  const cards: KpiCardSpec[] = [
    { key: 'duration', label: t('stats.kpi.duration'), value: formatStatsDuration(kpis.totalMs), current: kpis.totalMs, previous: previous?.totalMs },
    { key: 'turns', label: t('stats.kpi.turns'), value: formatStatsCount(kpis.turnCount), current: kpis.turnCount, previous: previous?.turnCount },
    { key: 'tokens', label: t('stats.kpi.tokens'), value: formatStatsCount(kpis.tokensTotal), current: kpis.tokensTotal, previous: previous?.tokensTotal },
    { key: 'toolCalls', label: t('stats.kpi.toolCalls'), value: formatStatsCount(kpis.toolCallCount), current: kpis.toolCallCount, previous: previous?.toolCallCount },
    { key: 'cache', label: t('stats.kpi.cache'), value: formatTokenCacheHitRate(kpis.cacheHitRate), current: kpis.cacheHitRate ?? undefined, previous: previous?.cacheHitRate ?? undefined },
    { key: 'workspaces', label: t('stats.kpi.workspaces'), value: formatStatsCount(kpis.workspaceCount), current: kpis.workspaceCount, previous: previous?.workspaceCount },
  ];
  return (
    <div className="stats-kpi-band">
      {cards.map((c) => (
        <div key={c.key} className="stats-kpi-card">
          <div className="stats-kpi-label">{c.label}</div>
          <div className="stats-kpi-value">{c.value}</div>
          <KpiDelta current={c.current} previous={c.previous} periodDays={periodDays} />
        </div>
      ))}
    </div>
  );
}

// KpiDelta renders the period-over-period change. We treat an increase as the
// accent (more usage is the expected "active" direction) and a decrease as
// neutral grey, deliberately avoiding red/green so the band stays calm.
function KpiDelta({ current, previous, periodDays }: { current?: number; previous?: number; periodDays: number }) {
  const { t } = useTranslation();
  if (current === undefined || previous === undefined) {
    return <div className="stats-kpi-delta stats-kpi-delta-empty">&nbsp;</div>;
  }
  if (previous === 0) {
    const label = current > 0
      ? t('stats.kpi.vsPrevious', { days: periodDays })
      : t('stats.kpi.noPrevious');
    return (
      <div className="stats-kpi-delta stats-kpi-delta-flat" title={label}>
        {current > 0 ? '—' : t('stats.kpi.noPrevious')}
      </div>
    );
  }
  const pct = ((current - previous) / previous) * 100;
  const up = pct >= 0;
  const rounded = Math.abs(pct) >= 100 ? Math.round(Math.abs(pct)) : Math.abs(pct).toFixed(0);
  return (
    <div
      className={`stats-kpi-delta ${up ? 'up' : 'down'}`}
      title={t('stats.kpi.vsPrevious', { days: periodDays })}
    >
      <span className="stats-kpi-delta-arrow">{up ? '▲' : '▼'}</span>
      {rounded}%
    </div>
  );
}

// ---------------------------------------------------------------------------
// Shared loading / value helpers
// ---------------------------------------------------------------------------

function StatsSkeleton() {
  return (
    <div className="stats-skeleton" aria-busy="true">
      <div className="stats-kpi-band">
        {[0, 1, 2, 3, 4, 5].map((i) => (
          <div key={i} className="stats-kpi-card stats-skeleton-card">
            <div className="stats-skeleton-line short" />
            <div className="stats-skeleton-line tall" />
          </div>
        ))}
      </div>
      <div className="stats-card stats-skeleton-card">
        <div className="stats-skeleton-line short" />
        <div className="stats-skeleton-chart" />
      </div>
      <div className="stats-rank-grid">
        {[0, 1, 2].map((i) => (
          <div key={i} className="stats-card stats-skeleton-card">
            <div className="stats-skeleton-line short" />
            <div className="stats-skeleton-line" />
            <div className="stats-skeleton-line" />
            <div className="stats-skeleton-line" />
          </div>
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Horizontal-bar rank lists (workspace / model / tool share one component)
// ---------------------------------------------------------------------------

interface RankItem {
  id: string;
  label: string;
  value: string;
  raw: number;
  deleted?: boolean;
  clickable?: boolean;
  current?: boolean;
}

function RankList({
  title,
  items,
  overflowCount,
  onActivate,
  emptyText,
}: {
  title: string;
  items: RankItem[];
  overflowCount: number;
  onActivate?: (id: string) => void;
  emptyText: string;
}) {
  const { t } = useTranslation();
  const max = items.reduce((m, it) => Math.max(m, it.raw), 0);
  return (
    <section className="stats-card stats-rank">
      <h3 className="stats-card-title">{title}</h3>
      {items.length === 0 ? (
        <div className="stats-rank-empty">{emptyText}</div>
      ) : (
        <ul className="stats-rank-list">
          {items.map((it, i) => {
            const pct = max > 0 ? (it.raw / max) * 100 : 0;
            const clickable = Boolean(it.clickable && onActivate);
            return (
              <li
                key={it.id}
                className={`stats-rank-row ${it.current ? 'current' : ''} ${clickable ? 'clickable' : ''}`}
                onClick={clickable ? () => onActivate?.(it.id) : undefined}
                onKeyDown={clickable ? (e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    onActivate?.(it.id);
                  }
                } : undefined}
                role={clickable ? 'button' : undefined}
                tabIndex={clickable ? 0 : undefined}
              >
                <div className="stats-rank-head">
                  <span className="stats-rank-label" title={it.label}>
                    {it.label}
                    {it.deleted && <span className="stats-rank-muted"> {t('stats.table.deletedSuffix')}</span>}
                  </span>
                  <span className="stats-rank-value">{it.value}</span>
                </div>
                <div className="stats-rank-track">
                  <div
                    className="stats-rank-fill"
                    style={{ width: `${Math.max(pct, 2)}%`, background: seriesColor(i) }}
                  />
                </div>
              </li>
            );
          })}
          {overflowCount > 0 && (
            <li className="stats-rank-overflow">{t('stats.chart.othersHidden', { count: overflowCount })}</li>
          )}
        </ul>
      )}
    </section>
  );
}

function ByWorkspaceRank({
  rows,
  currentWorkspaceId,
  onJumpToWorkspace,
}: {
  rows: WorkspaceRow[];
  currentWorkspaceId?: string;
  onJumpToWorkspace?: (workspaceId: string) => void;
}) {
  const { t } = useTranslation();
  const { items, overflow } = useMemo(() => {
    const sorted = rows
      .map((r) => ({
        id: r.workspaceId,
        label: r.workspaceName || r.workspaceId,
        raw: r.totalMs,
        value: formatStatsDuration(r.totalMs),
        deleted: r.deleted,
        clickable: Boolean(r.workspaceId && !r.deleted),
        current: currentWorkspaceId === r.workspaceId,
      }))
      .filter((it) => it.raw > 0)
      .sort((a, b) => (b.raw !== a.raw ? b.raw - a.raw : a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' })));
    return { items: sorted.slice(0, TOP_N), overflow: Math.max(0, sorted.length - TOP_N) };
  }, [rows, currentWorkspaceId]);

  return (
    <RankList
      title={t('stats.view.byWorkspace')}
      items={items}
      overflowCount={overflow}
      onActivate={onJumpToWorkspace}
      emptyText={t('stats.noDataInRange')}
    />
  );
}

function ByModelRank({ rows }: { rows: ModelRow[] }) {
  const { t } = useTranslation();
  const { items, overflow } = useMemo(() => {
    const sorted = rows
      .map((r) => {
        const raw = r.modelName || r.modelId;
        const label = isUnknownModelLabel(raw) ? t('stats.unknownModel') : (raw as string);
        return { id: r.modelId || UNKNOWN_MODEL_ID, label, raw: r.totalMs, value: formatStatsDuration(r.totalMs) };
      })
      .filter((it) => it.raw > 0)
      .sort((a, b) => (b.raw !== a.raw ? b.raw - a.raw : a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' })));
    return { items: sorted.slice(0, TOP_N), overflow: Math.max(0, sorted.length - TOP_N) };
  }, [rows, t]);

  return (
    <RankList
      title={t('stats.view.byModel')}
      items={items}
      overflowCount={overflow}
      emptyText={t('stats.noDataInRange')}
    />
  );
}

// Tool rank is fixed to call count (the most meaningful tool dimension), so it
// never depends on the trend chart's metric and never hits a "not collected"
// half-state for tokens.
function ByToolRank({ rows }: { rows: ToolRow[] }) {
  const { t } = useTranslation();
  const { items, overflow } = useMemo(() => {
    const sorted = rows
      .map((r) => ({ id: r.toolKey, label: r.toolKey, raw: r.count, value: formatStatsCount(r.count) }))
      .filter((it) => it.raw > 0)
      .sort((a, b) => (b.raw !== a.raw ? b.raw - a.raw : a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' })));
    return { items: sorted.slice(0, TOP_N), overflow: Math.max(0, sorted.length - TOP_N) };
  }, [rows]);

  return (
    <RankList
      title={t('stats.view.byTool')}
      items={items}
      overflowCount={overflow}
      emptyText={t('stats.tools.noToolsInRange')}
    />
  );
}

// ---------------------------------------------------------------------------
// Trend chart (the only chart with an in-chart metric switch)
// ---------------------------------------------------------------------------

function pickTrendValue(row: SectionTotals, metric: TrendMetric): number | null {
  if (metric === 'duration') return row.totalMs;
  if (metric === 'turns') return row.turnCount;
  if (metric === 'tokens') return row.tokens.total;
  return tokenCacheHitRate(row.tokens);
}

function formatTrendValue(value: number | null, metric: TrendMetric): string {
  if (metric === 'cache') return formatTokenCacheHitRate(value);
  if (value === null) return '—';
  if (metric === 'duration') return formatStatsDuration(value);
  return formatStatsCount(value);
}

function tokenCount(tokens: TokenTotals | undefined, field: keyof TokenTotals): number {
  if (!tokens) return 0;
  const value = tokens[field];
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0;
}

// Providers disagree on whether `input` already includes cache reads/writes.
// `reported - output` is the common provider-input total across both shapes;
// the remaining candidates keep partial third-party reports useful and ensure
// a malformed sample can never render a hit rate above 100%.
function tokenCacheHitRate(tokens: TokenTotals | undefined): number | null {
  const reported = tokenCount(tokens, 'reported');
  const output = tokenCount(tokens, 'output');
  const input = tokenCount(tokens, 'input');
  const cachedRead = tokenCount(tokens, 'cachedRead');
  const cachedWrite = tokenCount(tokens, 'cachedWrite');
  const providerInput = Math.max(0, reported - output, input, cachedRead + cachedWrite);
  if (providerInput <= 0) return null;
  return Math.min(1, cachedRead / providerInput);
}

function formatTokenCacheHitRate(rate: number | null): string {
  return rate === null ? '—' : `${(rate * 100).toFixed(1)}%`;
}

function trendAxisUnitKey(value: number, metric: TrendMetric): string {
  if (metric === 'duration') return value >= 3_600_000 ? 'hour' : 'minute';
  if (metric === 'turns') return 'count';
  if (metric === 'cache') return 'percent';
  return 'tokens';
}

function trendTitleKey(metric: TrendMetric): string {
  if (metric === 'tokens') return 'stats.view.dailyTokens';
  if (metric === 'cache') return 'stats.view.dailyCache';
  return 'stats.view.trend';
}

function emptySectionTotals(): SectionTotals {
  return {
    totalMs: 0,
    turnCount: 0,
    assistantCount: 0,
    thoughtCount: 0,
    toolCallCount: 0,
    tokens: {
      total: 0,
      reported: 0,
      input: 0,
      output: 0,
      cachedRead: 0,
      cachedWrite: 0,
      reasoning: 0,
      imageEstimate: 0,
      estimated: 0,
      reportedTurns: 0,
      estimatedTurns: 0,
      assistant: 0,
      thought: 0,
      toolCall: 0,
    },
  };
}

function TokenDetails({ tokens, turnCount }: { tokens: TokenTotals; turnCount: number }) {
  const { t } = useTranslation();
  const cacheHitRate = tokenCacheHitRate(tokens);
  const coverage = computeTokenCoverage([{ ...emptySectionTotals(), turnCount, tokens }]);
  return (
    <>
      <div className="stats-trend-tooltip-row stats-trend-tooltip-total">
        <span>{t('stats.table.tokenTotal')}</span>
        <strong>{formatStatsCount(tokenCount(tokens, 'total'))}</strong>
      </div>
      <TokenSourceSummary coverage={coverage} compact titleKey="stats.tokens.daySourceTitle" />
      <div className="stats-trend-tooltip-section">{t('stats.tokens.detailsSection')}</div>
      {([
        ['input', 'input'],
        ['output', 'output'],
        ['cachedRead', 'cachedRead'],
        ['cachedWrite', 'cachedWrite'],
        ['reasoning', 'reasoning'],
      ] as const).map(([field, label]) => (
        <div key={field} className="stats-trend-tooltip-row stats-trend-tooltip-detail">
          <span>{t(`stats.tokens.${label}`)}</span>
          <strong>{formatStatsCount(tokenCount(tokens, field))}</strong>
        </div>
      ))}
      <div className="stats-trend-tooltip-row stats-trend-tooltip-detail">
        <span>{t('stats.tokens.cacheHitRate')}</span>
        <strong>{formatTokenCacheHitRate(cacheHitRate)}</strong>
      </div>
      <div className="stats-trend-tooltip-hint">{t('stats.tokens.cacheHitRateHint')}</div>
      <div className="stats-trend-tooltip-divider" />
      <div className="stats-trend-tooltip-row stats-trend-tooltip-detail">
        <span>{t('stats.tokens.imageEstimateShort')}</span>
        <strong>{formatStatsCount(tokenCount(tokens, 'imageEstimate'))}</strong>
      </div>
      <div className="stats-trend-tooltip-hint">{t('stats.tokens.imageEstimateHint')}</div>
    </>
  );
}

interface TokenCoverage {
  totalTurns: number;
  reportedTurns: number;
  estimatedTurns: number;
  reportedPercent: number;
  estimatedPercent: number;
  reportedTokens: number;
  estimatedTokens: number;
}

// The backend classifies every recorded turn as either provider-reported or
// locally estimated, so the two counters always add up to turnCount.
function computeTokenCoverage(rows: SectionTotals[]): TokenCoverage {
  let totalTurns = 0;
  let reportedTurns = 0;
  let estimatedTurns = 0;
  let reportedTokens = 0;
  let estimatedTokens = 0;
  for (const row of rows) {
    totalTurns += Math.max(0, row.turnCount || 0);
    reportedTurns += tokenCount(row.tokens, 'reportedTurns');
    estimatedTurns += tokenCount(row.tokens, 'estimatedTurns');
    reportedTokens += tokenCount(row.tokens, 'reported');
    estimatedTokens += tokenCount(row.tokens, 'estimated');
  }
  const reportedPercent = totalTurns > 0 ? Math.round((reportedTurns / totalTurns) * 100) : 0;
  const estimatedPercent = totalTurns > 0 ? Math.max(0, 100 - reportedPercent) : 0;
  return { totalTurns, reportedTurns, estimatedTurns, reportedPercent, estimatedPercent, reportedTokens, estimatedTokens };
}

// TokenSourceSummary makes the mutually exclusive source split explicit. Token
// amounts are primary, while execution counts explain the coverage of each
// source. This avoids presenting provider details as values to add together.
function TokenSourceSummary({
  coverage,
  compact = false,
  titleKey = 'stats.tokens.sourceTitle',
}: {
  coverage: TokenCoverage;
  compact?: boolean;
  titleKey?: string;
}) {
  const { t } = useTranslation();
  const sources = [
    {
      key: 'reported',
      label: t('stats.tokens.modelReported'),
      description: t('stats.tokens.modelReportedHint'),
      tokens: coverage.reportedTokens,
      turns: coverage.reportedTurns,
      percent: coverage.reportedPercent,
    },
    {
      key: 'estimated',
      label: t('stats.tokens.quartetEstimated'),
      description: t('stats.tokens.quartetEstimatedHint'),
      tokens: coverage.estimatedTokens,
      turns: coverage.estimatedTurns,
      percent: coverage.estimatedPercent,
    },
  ];
  return (
    <section className={`stats-token-source ${compact ? 'compact' : ''}`} aria-label={t(titleKey)}>
      <div className="stats-token-source-head">
        <span>{t(titleKey)}</span>
        <span>{coverage.totalTurns > 0
          ? t('stats.tokens.executionCount', { count: coverage.totalTurns, formattedCount: formatStatsCount(coverage.totalTurns) })
          : t('stats.tokens.coverageUnavailable')}</span>
      </div>
      {coverage.totalTurns > 0 && (
        <div
          className="stats-token-source-bar"
          role="img"
          aria-label={t('stats.tokens.sourceBarAriaLabel', { reported: coverage.reportedPercent, estimated: coverage.estimatedPercent })}
        >
          <span className="reported" style={{ width: `${coverage.reportedPercent}%` }} />
          <span className="estimated" style={{ width: `${coverage.estimatedPercent}%` }} />
        </div>
      )}
      <div className="stats-token-source-grid">
        {sources.map((source) => (
          <div key={source.key} className={`stats-token-source-card ${source.key}`}>
            <div className="stats-token-source-card-head">
              <span className="stats-token-source-name"><i aria-hidden="true" />{source.label}</span>
              <strong>{formatStatsCount(source.tokens)}</strong>
            </div>
            <div className="stats-token-source-meta">
              {t('stats.tokens.sourceCoverage', { count: source.turns, formattedCount: formatStatsCount(source.turns), percent: source.percent })}
            </div>
            {!compact && <div className="stats-token-source-description">{source.description}</div>}
          </div>
        ))}
      </div>
      <div className="stats-token-source-definition">{t('stats.tokens.turnDefinition')}</div>
    </section>
  );
}

// Fields rendered by the daily token panel, in the same order as the iOS
// client's day detail. Every field is shown even when zero so the grid keeps a
// stable shape while hovering across days.
const TOKEN_DAY_FIELDS: Array<{ field: keyof TokenTotals; labelKey: string }> = [
  { field: 'input', labelKey: 'stats.tokens.input' },
  { field: 'output', labelKey: 'stats.tokens.output' },
  { field: 'cachedRead', labelKey: 'stats.tokens.cachedRead' },
  { field: 'cachedWrite', labelKey: 'stats.tokens.cachedWrite' },
  { field: 'reasoning', labelKey: 'stats.tokens.reasoning' },
];

// TokenDayPanel is the always-visible counterpart of the hover tooltip: it
// pins one day's token composition below the chart so the numbers can be read
// without holding the pointer over a column. Shows the hovered/focused day, or
// the last day of the range when nothing is hovered.
function TokenDayPanel({ day, latest }: { day: DailyRow; latest: boolean }) {
  const { t } = useTranslation();
  const coverage = useMemo(() => computeTokenCoverage([day]), [day]);
  const total = tokenCount(day.tokens, 'total');
  const cacheHitRate = tokenCacheHitRate(day.tokens);
  return (
    <section
      className="stats-token-day"
      aria-label={t('stats.tokens.dayPanelAriaLabel', { date: day.date, value: formatStatsCount(total) })}
    >
      <div className="stats-token-day-head">
        <div className="stats-token-day-heading">
          <span className="stats-token-day-title">{t('stats.tokens.dayPanelTitle')}</span>
          <span className="stats-token-day-date">
            {day.date}
            {latest && <em className="stats-token-day-badge">{t('stats.tokens.dayPanelLatest')}</em>}
          </span>
        </div>
        <div className="stats-token-day-summary">
          <div className="stats-token-day-total">
            <span className="stats-token-day-total-label">{t('stats.table.tokenTotal')}</span>
            <strong className="stats-token-day-total-value">{formatStatsCount(total)}</strong>
            <span className="stats-token-day-turns">
              {t('stats.table.turns')} {formatStatsCount(Math.max(0, day.turnCount || 0))}
            </span>
          </div>
          <div className="stats-token-day-cache-rate">
            <span>{t('stats.tokens.cacheHitRate')}</span>
            <strong>{formatTokenCacheHitRate(cacheHitRate)}</strong>
          </div>
        </div>
      </div>
      <TokenSourceSummary coverage={coverage} titleKey="stats.tokens.daySourceTitle" />
      <div className="stats-token-day-section">{t('stats.tokens.detailsSection')}</div>
      <div className="stats-token-day-hint">{t('stats.tokens.detailsHint')}</div>
      <div className="stats-token-day-grid">
        {TOKEN_DAY_FIELDS.map(({ field, labelKey }) => (
          <div key={field} className="stats-token-day-cell">
            <span className="stats-token-day-cell-label">{t(labelKey)}</span>
            <strong className="stats-token-day-cell-value">{formatStatsCount(tokenCount(day.tokens, field))}</strong>
          </div>
        ))}
      </div>
      <div className="stats-token-day-subset">
        <span>{t('stats.tokens.imageEstimateShort')}</span>
        <strong>{formatStatsCount(tokenCount(day.tokens, 'imageEstimate'))}</strong>
        <small>{t('stats.tokens.imageEstimateHint')}</small>
      </div>
      <div className="stats-token-day-hint">{t('stats.tokens.cacheHitRateHint')}</div>
      <div className="stats-token-day-hint">{t('stats.tokens.dayPanelPickHint')}</div>
    </section>
  );
}

function parseDateKey(dateKey: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateKey);
  if (!m) return null;
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
}

function formatDateKey(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${dd}`;
}

function addDays(d: Date, days: number): Date {
  const next = new Date(d);
  next.setDate(next.getDate() + days);
  return next;
}

function fillDailyRange(daily: DailyRow[], range?: { from: string; to: string }): DailyRow[] {
  if (!range?.from || !range?.to) return daily;
  const from = parseDateKey(range.from);
  const to = parseDateKey(range.to);
  if (!from || !to || from > to) return daily;
  const byDate = new Map(daily.map((row) => [row.date, row]));
  const out: DailyRow[] = [];
  for (let cur = from; cur <= to; cur = addDays(cur, 1)) {
    const key = formatDateKey(cur);
    out.push(byDate.get(key) || { date: key, ...emptySectionTotals(), models: {}, modelNames: {} });
  }
  return out;
}

interface TrendSegment {
  modelId: string;
  value: number;
}

interface TrendTooltipState {
  anchorX: number;
  anchorY: number;
  day: DailyRow;
  segments: TrendSegment[];
}

function trendSegments(day: DailyRow, metric: TrendMetric): TrendSegment[] {
  const total = pickTrendValue(day, metric);
  const segments: TrendSegment[] = [];

  // Cache hit rates are ratios, so each model is plotted independently. They
  // must not be summed or assigned a residual as additive metrics are.
  if (metric === 'cache') {
    if (day.models) {
      for (const [mid, st] of Object.entries(day.models)) {
        const value = pickTrendValue(st, metric);
        if (value !== null) segments.push({ modelId: isUnknownModelLabel(mid) ? UNKNOWN_MODEL_ID : mid, value });
      }
    } else if (total !== null) {
      segments.push({ modelId: UNKNOWN_MODEL_ID, value: total });
    }
    return segments;
  }

  const numericTotal = total ?? 0;
  let modelTotal = 0;
  if (day.models) {
    for (const [mid, st] of Object.entries(day.models)) {
      const value = pickTrendValue(st, metric) ?? 0;
      modelTotal += value;
      if (value > 0) segments.push({ modelId: isUnknownModelLabel(mid) ? UNKNOWN_MODEL_ID : mid, value });
    }
    const residual = numericTotal - modelTotal;
    if (residual > 0) segments.push({ modelId: UNKNOWN_MODEL_ID, value: residual });
  } else if (numericTotal > 0) {
    segments.push({ modelId: UNKNOWN_MODEL_ID, value: numericTotal });
  }
  return segments;
}

function TrendCard({
  range,
  daily,
  metric,
  onMetricChange,
}: {
  range?: { from: string; to: string };
  daily: DailyRow[];
  metric: TrendMetric;
  onMetricChange: (m: TrendMetric) => void;
}) {
  const { t } = useTranslation();
  const [hiddenModels, setHiddenModels] = useState<Set<string>>(() => new Set());
  const [tooltip, setTooltip] = useState<TrendTooltipState | null>(null);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const [focusedDayIndex, setFocusedDayIndex] = useState(0);
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const dayHitboxRefs = useRef<Array<SVGRectElement | null>>([]);
  const tooltipRef = useRef<HTMLDivElement | null>(null);
  const tooltipCloseTimerRef = useRef<number | null>(null);
  const [wrapWidth, setWrapWidth] = useState<number>(0);

  const cancelTooltipClose = useCallback(() => {
    if (tooltipCloseTimerRef.current !== null) {
      window.clearTimeout(tooltipCloseTimerRef.current);
      tooltipCloseTimerRef.current = null;
    }
  }, []);

  const closeTooltipSoon = useCallback(() => {
    cancelTooltipClose();
    tooltipCloseTimerRef.current = window.setTimeout(() => {
      setTooltip(null);
      setHoverIdx(null);
      tooltipCloseTimerRef.current = null;
    }, 160);
  }, [cancelTooltipClose]);

  useEffect(() => () => cancelTooltipClose(), [cancelTooltipClose]);

  // The tooltip uses viewport coordinates. Clamp the rendered element after
  // measuring it so long token/model breakdowns remain inside the viewport.
  useLayoutEffect(() => {
    const element = tooltipRef.current;
    if (!tooltip || !element) return;
    const edge = 8;
    const gap = 12;
    const rect = element.getBoundingClientRect();
    let left = tooltip.anchorX + gap;
    let top = tooltip.anchorY + gap;
    if (left + rect.width > window.innerWidth - edge) {
      left = tooltip.anchorX - gap - rect.width;
    }
    if (top + rect.height > window.innerHeight - edge) {
      top = tooltip.anchorY - gap - rect.height;
    }
    left = Math.max(edge, Math.min(left, window.innerWidth - edge - rect.width));
    top = Math.max(edge, Math.min(top, window.innerHeight - edge - rect.height));
    element.style.left = `${Math.round(left)}px`;
    element.style.top = `${Math.round(top)}px`;
  }, [metric, t, tooltip]);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const w = entry.contentRect.width;
        if (w > 0) setWrapWidth(w);
      }
    });
    ro.observe(el);
    setWrapWidth(el.getBoundingClientRect().width);
    return () => ro.disconnect();
  }, []);

  const filledDaily = useMemo(() => fillDailyRange(daily, range), [daily, range]);
  useEffect(() => {
    dayHitboxRefs.current.length = filledDaily.length;
    setFocusedDayIndex((current) => Math.min(current, Math.max(0, filledDaily.length - 1)));
  }, [filledDaily.length]);
  const tokenCoverage = useMemo(() => computeTokenCoverage(filledDaily), [filledDaily]);
  // The token panel follows the pointer/keyboard focus and falls back to the
  // last day of the range, so it always has something to show.
  const panelIdx = hoverIdx !== null && hoverIdx < filledDaily.length ? hoverIdx : filledDaily.length - 1;
  const panelDay = filledDaily[panelIdx];
  const panelIsFallback = panelIdx !== hoverIdx;
  const dailySegments = useMemo(
    () => filledDaily.map((day) => trendSegments(day, metric)),
    [filledDaily, metric],
  );
  const totalSeries = useMemo(
    () => filledDaily.map((day) => pickTrendValue(day, metric)),
    [filledDaily, metric],
  );
  const { models, palette } = useMemo(() => {
    const modelSet = new Set<string>();
    for (const segs of dailySegments) segs.forEach((seg) => modelSet.add(seg.modelId));
    const models = Array.from(modelSet).sort();
    const palette = models.map((_, i) => trendColor(i));
    return { models, palette };
  }, [dailySegments]);
  const modelNames = useMemo(() => {
    const out = new Map<string, string>();
    for (const day of filledDaily) {
      if (!day.modelNames) continue;
      for (const [modelId, modelName] of Object.entries(day.modelNames)) {
        out.set(isUnknownModelLabel(modelId) ? UNKNOWN_MODEL_ID : modelId, modelName);
      }
    }
    return out;
  }, [filledDaily]);
  const allSeriesKeys = useMemo(() => [TOTAL_SERIES_KEY, ...models], [models]);
  const effectiveHiddenModels = useMemo(() => {
    if (allSeriesKeys.length > 0 && allSeriesKeys.every((k) => hiddenModels.has(k))) return new Set<string>();
    return hiddenModels;
  }, [allSeriesKeys, hiddenModels]);
  const seriesByModel = useMemo(() => {
    const out = new Map<string, Array<number | null>>();
    for (const m of models) out.set(m, new Array(filledDaily.length).fill(metric === 'cache' ? null : 0));
    dailySegments.forEach((segs, dayIdx) => {
      for (const seg of segs) {
        const arr = out.get(seg.modelId);
        if (arr) arr[dayIdx] = seg.value;
      }
    });
    return out;
  }, [models, dailySegments, filledDaily.length, metric]);
  const max = useMemo(() => {
    // A fixed percentage scale makes day-to-day cache rates honest and easy
    // to compare, including ranges whose highest hit rate is well below 100%.
    if (metric === 'cache') return 1;
    let m = 0;
    for (const segs of dailySegments) {
      for (const seg of segs) {
        if (effectiveHiddenModels.has(seg.modelId)) continue;
        if (seg.value > m) m = seg.value;
      }
    }
    if (!effectiveHiddenModels.has(TOTAL_SERIES_KEY)) {
      for (const v of totalSeries) if (v !== null && v > m) m = v;
    }
    return m;
  }, [dailySegments, totalSeries, effectiveHiddenModels, metric]);
  const hasTrendData = metric === 'cache'
    ? totalSeries.some((value) => value !== null) || dailySegments.some((segments) => segments.length > 0)
    : max > 0;

  const metricSwitch = (
    <div className="stats-segmented stats-segmented-sm" aria-label={t('stats.metric.selectorLabel')}>
      {(['duration', 'turns', 'tokens', 'cache'] as TrendMetric[]).map((m) => (
        <button
          key={m}
          type="button"
          className={`stats-segmented-btn ${metric === m ? 'active' : ''}`}
          onClick={() => onMetricChange(m)}
          aria-pressed={metric === m}
        >
          {t(`stats.metric.${m}`)}
        </button>
      ))}
    </div>
  );

  if (filledDaily.length === 0 || !hasTrendData) {
    return (
      <section className="stats-card stats-trend">
        <div className="stats-card-head">
          <h3 className="stats-card-title">{t(trendTitleKey(metric))}</h3>
          {metricSwitch}
        </div>
        {metric === 'tokens' && <TokenSourceSummary coverage={tokenCoverage} />}
        {metric === 'cache' && <div className="stats-cache-note" role="note">{t('stats.tokens.cacheHitRateHint')}</div>}
        <div className="stats-rank-empty stats-trend-empty">
          {t(metric === 'cache' ? 'stats.tokens.cacheUnavailable' : 'stats.noDataInRange')}
        </div>
        {metric === 'tokens' && panelDay && <TokenDayPanel day={panelDay} latest={panelIsFallback} />}
      </section>
    );
  }

  const height = 240;
  const padding = { top: 18, right: 24, bottom: 30, left: 44 };
  const innerHeight = height - padding.top - padding.bottom;
  const minColWidth = 24;
  const naturalWidth = Math.max(720, filledDaily.length * 96);
  const cramWidth = padding.left + padding.right + filledDaily.length * minColWidth;
  const fitWidth = wrapWidth > 0 ? Math.max(wrapWidth, cramWidth) : naturalWidth;
  const virtualWidth = wrapWidth > 0 && wrapWidth < naturalWidth ? fitWidth : naturalWidth;
  const innerWidth = virtualWidth - padding.left - padding.right;
  const colWidth = innerWidth / filledDaily.length;
  const xAt = (idx: number) => padding.left + idx * colWidth + colWidth / 2;
  const yAt = (value: number) => padding.top + innerHeight - (value / max) * innerHeight;
  const linePath = (values: Array<number | null>): string => {
    let continuing = false;
    return values.map((value, idx) => {
      if (value === null) {
        continuing = false;
        return '';
      }
      const command = continuing ? 'L' : 'M';
      continuing = true;
      return `${command}${xAt(idx)},${yAt(value)}`;
    }).filter(Boolean).join(' ');
  };
  const gridLevels = [0.25, 0.5, 0.75];
  // Render per-point value labels on the Total line whenever columns aren't
  // razor-thin. Labels always sit flat above each point.
  const showPointLabels = colWidth >= 22;
  const visibleCount = allSeriesKeys.filter((k) => !effectiveHiddenModels.has(k)).length;
  const toggleModel = (model: string) => {
    setHiddenModels((prev) => {
      const next = new Set(prev);
      if (next.has(model)) next.delete(model);
      else if (visibleCount > 1) next.add(model);
      return next;
    });
  };
  const showTooltip = (anchorX: number, anchorY: number, day: DailyRow, segments: TrendSegment[]) => {
    cancelTooltipClose();
    setTooltip({ anchorX, anchorY, day, segments });
  };
  const moveTooltip = (e: ReactMouseEvent<SVGRectElement>, day: DailyRow, segments: TrendSegment[]) => {
    showTooltip(e.clientX, e.clientY, day, segments);
  };
  const focusDay = (index: number) => {
    const nextIndex = Math.max(0, Math.min(index, filledDaily.length - 1));
    setFocusedDayIndex(nextIndex);
    dayHitboxRefs.current[nextIndex]?.focus();
  };
  const handleDayKeyDown = (event: ReactKeyboardEvent<SVGRectElement>, index: number) => {
    let nextIndex: number | null = null;
    if (event.key === 'ArrowLeft') nextIndex = index - 1;
    if (event.key === 'ArrowRight') nextIndex = index + 1;
    if (event.key === 'Home') nextIndex = 0;
    if (event.key === 'End') nextIndex = filledDaily.length - 1;
    if (nextIndex === null) return;
    event.preventDefault();
    focusDay(nextIndex);
  };
  const formatModelLabel = (modelId: string) => {
    if (isUnknownModelLabel(modelId)) return t('stats.unknownModel');
    const label = modelNames.get(modelId) || modelId;
    return isUnknownModelLabel(label) ? t('stats.unknownModel') : label;
  };
  const axisUnit = t(`stats.trend.unit.${trendAxisUnitKey(max, metric)}`);

  return (
    <section className="stats-card stats-trend">
      <div className="stats-card-head">
        <h3 className="stats-card-title">{t(trendTitleKey(metric))}</h3>
        {metricSwitch}
      </div>
      {metric === 'tokens' && <TokenSourceSummary coverage={tokenCoverage} />}
      {metric === 'cache' && <div className="stats-cache-note" role="note">{t('stats.tokens.cacheHitRateHint')}</div>}
      <div className="stats-trend-chart-wrap" ref={wrapRef}>
        <svg
          width={virtualWidth}
          height={height}
          viewBox={`0 0 ${virtualWidth} ${height}`}
          className="stats-trend-chart"
          role="group"
          aria-label={t('stats.trend.chartAriaLabel')}
        >
          <text x={4} y={padding.top + 4} className="stats-trend-axis">{formatTrendValue(max, metric)}</text>
          <text x={virtualWidth - padding.right} y={padding.top + 4} textAnchor="end" className="stats-trend-axis stats-trend-axis-unit">
            {t('stats.trend.axisUnit', { unit: axisUnit })}
          </text>
          <text x={4} y={padding.top + innerHeight} className="stats-trend-axis">{metric === 'cache' ? formatTrendValue(0, metric) : '0'}</text>
          {gridLevels.map((lvl) => (
            <line
              key={lvl}
              x1={padding.left}
              y1={padding.top + innerHeight * lvl}
              x2={padding.left + innerWidth}
              y2={padding.top + innerHeight * lvl}
              className="stats-trend-grid"
            />
          ))}
          <line
            x1={padding.left}
            y1={padding.top + innerHeight}
            x2={padding.left + innerWidth}
            y2={padding.top + innerHeight}
            className="stats-trend-baseline"
          />
          {hoverIdx !== null && (
            <line
              x1={xAt(hoverIdx)}
              y1={padding.top}
              x2={xAt(hoverIdx)}
              y2={padding.top + innerHeight}
              className="stats-trend-guide"
            />
          )}
          {!effectiveHiddenModels.has(TOTAL_SERIES_KEY) && totalSeries.some((v) => v !== null) && (
            <g>
              {totalSeries.filter((value) => value !== null).length > 1 && (
                <path
                  d={linePath(totalSeries)}
                  fill="none"
                  stroke={TOTAL_COLOR}
                  strokeWidth={2}
                  className="stats-trend-line stats-trend-line-total"
                />
              )}
              {totalSeries.map((v, i) => v === null ? null : (
                <circle key={i} cx={xAt(i)} cy={yAt(v)} r={hoverIdx === i ? 4 : 3} fill={TOTAL_COLOR} className="stats-trend-point stats-trend-point-total" />
              ))}
              {/* Value labels on the Total line. Tilt to -45° on narrow columns
                  so adjacent labels don't collide; flat when columns are wide.
                  The hover tooltip still carries the full breakdown. */}
              {showPointLabels && totalSeries.map((v, i) => {
                if (v === null || (metric !== 'cache' && v <= 0)) return null;
                const px = xAt(i);
                const py = yAt(v) - (hoverIdx === i ? 11 : 9);
                return (
                  <text
                    key={i}
                    x={px}
                    y={py}
                    textAnchor="middle"
                    className="stats-trend-point-label"
                  >
                    {formatTrendValue(v, metric)}
                  </text>
                );
              })}
            </g>
          )}
          {models.map((modelId, mi) => {
            if (effectiveHiddenModels.has(modelId)) return null;
            const values = seriesByModel.get(modelId) || [];
            const color = palette[mi];
            return (
              <g key={modelId}>
                {values.filter((value) => value !== null).length > 1 && (
                  <path d={linePath(values)} fill="none" stroke={color} strokeWidth={2} className="stats-trend-line" />
                )}
                {values.map((v, i) => v === null ? null : (
                  <circle key={i} cx={xAt(i)} cy={yAt(v)} r={hoverIdx === i ? 4 : 3} fill={color} className="stats-trend-point" />
                ))}
              </g>
            );
          })}
          {filledDaily.map((day, idx) => (
            <g key={day.date}>
              <rect
                ref={(node) => { dayHitboxRefs.current[idx] = node; }}
                x={padding.left + idx * colWidth}
                y={padding.top}
                width={colWidth}
                height={innerHeight}
                fill="transparent"
                className="stats-trend-hitbox"
                tabIndex={idx === focusedDayIndex ? 0 : -1}
                role="img"
                aria-label={t('stats.trend.dayAriaLabel', {
                  date: day.date,
                  metric: t(`stats.metric.${metric}`),
                  value: formatTrendValue(pickTrendValue(day, metric), metric),
                })}
                onMouseEnter={(e) => { setHoverIdx(idx); moveTooltip(e, day, dailySegments[idx]); }}
                onMouseMove={(e) => { setHoverIdx(idx); moveTooltip(e, day, dailySegments[idx]); }}
                onMouseDown={(event) => {
                  event.preventDefault();
                  event.currentTarget.blur();
                }}
                onMouseLeave={closeTooltipSoon}
                onFocus={(e) => {
                  cancelTooltipClose();
                  setFocusedDayIndex(idx);
                  setHoverIdx(idx);
                  const rect = e.currentTarget.getBoundingClientRect();
                  showTooltip(rect.left + rect.width / 2, rect.top + rect.height / 2, day, dailySegments[idx]);
                }}
                onBlur={closeTooltipSoon}
                onKeyDown={(event) => handleDayKeyDown(event, idx)}
              />
              <text x={xAt(idx)} y={height - 8} textAnchor="middle" className="stats-trend-tick">{day.date.slice(5)}</text>
            </g>
          ))}
        </svg>
      </div>
      {metric === 'tokens' && panelDay && <TokenDayPanel day={panelDay} latest={panelIsFallback} />}
      <div className="stats-trend-legend">
        <button
          type="button"
          className={`stats-trend-legend-item stats-trend-legend-total ${effectiveHiddenModels.has(TOTAL_SERIES_KEY) ? 'muted' : ''}`}
          onClick={() => toggleModel(TOTAL_SERIES_KEY)}
          title={t('stats.chart.totalLabel')}
        >
          <span className="stats-trend-swatch stats-trend-swatch-total" />
          {t('stats.chart.totalLabel')}
        </button>
        {models.map((m, i) => (
          <button
            key={m}
            type="button"
            className={`stats-trend-legend-item ${effectiveHiddenModels.has(m) ? 'muted' : ''}`}
            onClick={() => toggleModel(m)}
            title={formatModelLabel(m)}
          >
            <span className="stats-trend-swatch" style={{ background: palette[i] }} />
            {formatModelLabel(m)}
          </button>
        ))}
      </div>
      {tooltip && (
        <div
          ref={tooltipRef}
          className="stats-trend-tooltip"
          role="status"
          style={{ left: tooltip.anchorX + 12, top: tooltip.anchorY + 12 }}
          tabIndex={-1}
          onMouseEnter={cancelTooltipClose}
          onMouseLeave={closeTooltipSoon}
          onFocus={cancelTooltipClose}
          onBlur={closeTooltipSoon}
        >
          <div className="stats-trend-tooltip-title">{tooltip.day.date}</div>
          {metric === 'tokens' ? (
            <TokenDetails tokens={tooltip.day.tokens} turnCount={tooltip.day.turnCount} />
          ) : (
            <div className="stats-trend-tooltip-row">
              <span>{t(metric === 'cache' ? 'stats.tokens.cacheHitRate' : `stats.metric.${metric}`)}</span>
              <strong>{formatTrendValue(pickTrendValue(tooltip.day, metric), metric)}</strong>
            </div>
          )}
          {metric === 'cache' && <div className="stats-trend-tooltip-hint">{t('stats.tokens.cacheHitRateHint')}</div>}
          {metric !== 'tokens' && <div className="stats-trend-tooltip-row">
            <span>{t('stats.table.turns')}</span>
            <strong>{formatStatsCount(tooltip.day.turnCount)}</strong>
          </div>}
          <div className="stats-trend-tooltip-divider" />
          {(metric === 'tokens' || metric === 'cache') && <div className="stats-trend-tooltip-section">{t('stats.tokens.modelBreakdown')}</div>}
          {tooltip.segments.length === 0 ? (
            <div className="stats-trend-tooltip-muted">{t('stats.noDataInRange')}</div>
          ) : tooltip.segments.map((seg) => (
            <div key={seg.modelId} className="stats-trend-tooltip-row">
              <span>{formatModelLabel(seg.modelId)}</span>
              <strong>{formatTrendValue(seg.value, metric)}</strong>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}
