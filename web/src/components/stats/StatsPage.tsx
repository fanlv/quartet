import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react';
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
  tokens: {
    total: number;
    assistant: number;
    thought: number;
    toolCall: number;
  };
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

interface UsageReport {
  range: {
    from: string;
    to: string;
  };
  byWorkspace: WorkspaceRow[];
  byModel: ModelRow[];
  byTool: ToolRow[];
  daily: DailyRow[];
  note: string;
  failed?: boolean;
  error?: string;
}

type RangePreset = '7d' | '30d' | '90d' | 'all' | 'custom';
type Metric = 'duration' | 'counts' | 'tokens';
type CountSub = 'turns' | 'assistant' | 'thought' | 'toolCall';
type TokenSub = 'tokenTotal' | 'assistant' | 'thought' | 'toolCall';

const UNKNOWN_MODEL_ID = '__unknown_model__';
const API_UNKNOWN_MODEL_ID = '(unknown model)';
const TOTAL_SERIES_KEY = '__total__';
const TOTAL_SERIES_COLOR = '#0f172a';
const TOP_N = 10;

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
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const dd = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${dd}`;
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

export function StatsPage({ onClose, currentWorkspaceId, onJumpToWorkspace }: StatsPageProps) {
  const { t, i18n } = useTranslation();
  const [preset, setPreset] = useState<RangePreset>('7d');
  const [customFrom, setCustomFrom] = useState<string>(shiftDate(6));
  const [customTo, setCustomTo] = useState<string>(todayDateKey());
  const [metric, setMetric] = useState<Metric>('duration');
  const [countSub, setCountSub] = useState<CountSub>('turns');
  const [tokenSub, setTokenSub] = useState<TokenSub>('tokenTotal');
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

  return (
    <main className="stats-page">
      <div className="stats-shell">
        <header className="stats-header">
          <h2 className="stats-title">{t('stats.title')}</h2>
          <button className="stats-close" onClick={onClose} aria-label={t('common.close')}>×</button>
        </header>

        <div className="stats-banner">{t(report?.note || 'stats.tokensLocalEstimateNote')}</div>

        <div className="stats-controls">
          <div className="stats-controls-group">
            <span className="stats-controls-label">{t('stats.rangeLabel')}</span>
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
                <label>
                  {t('stats.customFrom')}{' '}
                  <input
                    type="date"
                    value={customFrom}
                    max={customTo || undefined}
                    onChange={(e) => setCustomFrom(e.target.value)}
                  />
                </label>
                <label>
                  {t('stats.customTo')}{' '}
                  <input
                    type="date"
                    value={customTo}
                    min={customFrom || undefined}
                    onChange={(e) => setCustomTo(e.target.value)}
                  />
                </label>
              </div>
            )}
          </div>

          <div className="stats-controls-group">
            <div className="stats-segmented">
              {(['duration', 'counts', 'tokens'] as Metric[]).map((m) => (
                <button
                  key={m}
                  className={`stats-segmented-btn ${metric === m ? 'active' : ''}`}
                  onClick={() => setMetric(m)}
                >
                  {t(`stats.metric.${m}`)}
                </button>
              ))}
            </div>
            {metric === 'counts' && (
              <select
                className="stats-submetric-select"
                value={countSub}
                onChange={(e) => setCountSub(e.target.value as CountSub)}
              >
                {(['turns', 'assistant', 'thought', 'toolCall'] as CountSub[]).map((s) => (
                  <option key={s} value={s}>{t(`stats.subMetric.${s}`)}</option>
                ))}
              </select>
            )}
            {metric === 'tokens' && (
              <select
                className="stats-submetric-select"
                value={tokenSub}
                onChange={(e) => setTokenSub(e.target.value as TokenSub)}
              >
                {(['tokenTotal', 'assistant', 'thought', 'toolCall'] as TokenSub[]).map((s) => (
                  <option key={s} value={s}>{t(`stats.subMetric.${s}`)}</option>
                ))}
              </select>
            )}
          </div>
        </div>

        <div className="stats-body">
          {loading && !report && <StatsSkeletonGrid label={t('stats.loading')} />}
          {error && (
            <div className="stats-status stats-error">
              <div>{t('stats.fetchError', { message: error })}</div>
              <button type="button" className="stats-retry-btn" onClick={() => void fetchReport()}>
                {t('stats.retry')}
              </button>
            </div>
          )}
          {!loading && !error && isEmpty && (
            <div className="stats-status">{t('stats.noDataInRange')}</div>
          )}
          {report && !isEmpty && (
            <div className="stats-grid">
              <ByWorkspaceView
                rows={report.byWorkspace}
                metric={metric}
                countSub={countSub}
                tokenSub={tokenSub}
                currentWorkspaceId={currentWorkspaceId}
                onJumpToWorkspace={onJumpToWorkspace}
              />
              <ByModelView
                rows={report.byModel}
                metric={metric}
                countSub={countSub}
                tokenSub={tokenSub}
              />
              <ByToolView rows={report.byTool} metric={metric} />
              <TrendView range={report.range} daily={report.daily} metric={metric} countSub={countSub} tokenSub={tokenSub} />
            </div>
          )}
        </div>
      </div>
    </main>
  );
}

function StatsSkeletonGrid({ label }: { label: string }) {
  return (
    <div className="stats-grid stats-skeleton-grid" aria-busy="true" aria-label={label}>
      {[0, 1, 2].map((section) => (
        <section key={section} className="stats-section stats-skeleton-section">
          <div className="stats-skeleton-title" />
          <div className="stats-skeleton-chart short" />
        </section>
      ))}
      <section className="stats-section stats-section-trend stats-skeleton-section">
        <div className="stats-skeleton-title" />
        <div className="stats-skeleton-chart" />
      </section>
    </div>
  );
}

// Pull the metric value out of a SectionTotals row so all three tables can
// share one accessor.
function pickValue(row: SectionTotals, metric: Metric, countSub: CountSub, tokenSub: TokenSub): number {
  if (metric === 'duration') return row.totalMs;
  if (metric === 'counts') {
    switch (countSub) {
      case 'turns': return row.turnCount;
      case 'assistant': return row.assistantCount;
      case 'thought': return row.thoughtCount;
      case 'toolCall': return row.toolCallCount;
    }
  }
  // tokens
  switch (tokenSub) {
    case 'tokenTotal': return row.tokens.total;
    case 'assistant': return row.tokens.assistant;
    case 'thought': return row.tokens.thought;
    case 'toolCall': return row.tokens.toolCall;
  }
}

function formatMetricValue(value: number, metric: Metric): string {
  if (metric === 'duration') return formatStatsDuration(value);
  return formatStatsCount(value);
}

function trendAxisUnitKey(value: number, metric: Metric): string {
  if (metric === 'duration') return value >= 3_600_000 ? 'hour' : 'minute';
  if (metric === 'counts') return 'count';
  return 'tokens';
}

interface ViewBaseProps {
  metric: Metric;
  countSub: CountSub;
  tokenSub: TokenSub;
}

interface ChartTooltipRow {
  label: string;
  value: string;
  swatch?: string;
  muted?: boolean;
  emphasized?: boolean;
}

interface ChartTooltipState {
  x: number;
  y: number;
  title: string;
  subtitle?: string;
  rows: ChartTooltipRow[];
}

function ChartTooltipPanel({ state }: { state: ChartTooltipState }) {
  return (
    <div className="stats-chart-tooltip" style={{ left: state.x, top: state.y }}>
      <div className="stats-chart-tooltip-title">{state.title}</div>
      {state.subtitle && <div className="stats-chart-tooltip-subtitle">{state.subtitle}</div>}
      {state.rows.map((row, i) => (
        <div
          key={`${row.label}-${i}`}
          className={`stats-chart-tooltip-row ${row.muted ? 'muted' : ''} ${row.emphasized ? 'emphasized' : ''}`}
        >
          <span className="stats-chart-tooltip-row-label">
            {row.swatch && <span className="stats-chart-tooltip-swatch" style={{ background: row.swatch }} />}
            {row.label}
          </span>
          <strong>{row.value}</strong>
        </div>
      ))}
    </div>
  );
}

// breakdownRows builds the per-section breakdown shown inside the chart
// tooltip. The "active" metric/sub-metric is highlighted; the others are
// rendered muted so the user keeps the full breakdown for context without
// losing focus on what the chart is currently encoding.
function breakdownRows(
  t: (key: string) => string,
  totals: SectionTotals,
  metric: Metric,
  countSub: CountSub,
  tokenSub: TokenSub,
): ChartTooltipRow[] {
  if (metric === 'duration') {
    return [
      { label: t('stats.table.duration'), value: formatStatsDuration(totals.totalMs), emphasized: true },
      { label: t('stats.table.turns'), value: formatStatsCount(totals.turnCount), muted: true },
    ];
  }
  if (metric === 'counts') {
    return [
      { label: t('stats.table.turns'), value: formatStatsCount(totals.turnCount), emphasized: countSub === 'turns', muted: countSub !== 'turns' },
      { label: t('stats.table.assistant'), value: formatStatsCount(totals.assistantCount), emphasized: countSub === 'assistant', muted: countSub !== 'assistant' },
      { label: t('stats.table.thought'), value: formatStatsCount(totals.thoughtCount), emphasized: countSub === 'thought', muted: countSub !== 'thought' },
      { label: t('stats.table.toolCall'), value: formatStatsCount(totals.toolCallCount), emphasized: countSub === 'toolCall', muted: countSub !== 'toolCall' },
    ];
  }
  return [
    { label: t('stats.table.tokenTotal'), value: formatStatsCount(totals.tokens.total), emphasized: tokenSub === 'tokenTotal', muted: tokenSub !== 'tokenTotal' },
    { label: t('stats.table.tokenAssistant'), value: formatStatsCount(totals.tokens.assistant), emphasized: tokenSub === 'assistant', muted: tokenSub !== 'assistant' },
    { label: t('stats.table.tokenThought'), value: formatStatsCount(totals.tokens.thought), emphasized: tokenSub === 'thought', muted: tokenSub !== 'thought' },
    { label: t('stats.table.tokenToolCall'), value: formatStatsCount(totals.tokens.toolCall), emphasized: tokenSub === 'toolCall', muted: tokenSub !== 'toolCall' },
  ];
}

function polarToCartesian(cx: number, cy: number, r: number, angle: number): { x: number; y: number } {
  return { x: cx + r * Math.cos(angle), y: cy + r * Math.sin(angle) };
}

// donutArc builds an SVG path for an annulus slice between startAngle and
// endAngle (radians, 0 = top, increasing clockwise). Full-circle is rendered
// as two stacked half slices to avoid the zero-length-arc edge case.
function donutArc(cx: number, cy: number, r: number, innerR: number, startAngle: number, endAngle: number): string {
  if (endAngle - startAngle >= 2 * Math.PI - 1e-6) {
    const mid = startAngle + Math.PI;
    return donutArc(cx, cy, r, innerR, startAngle, mid) + ' ' + donutArc(cx, cy, r, innerR, mid, endAngle);
  }
  const a1 = startAngle - Math.PI / 2;
  const a2 = endAngle - Math.PI / 2;
  const oS = polarToCartesian(cx, cy, r, a1);
  const oE = polarToCartesian(cx, cy, r, a2);
  const iS = polarToCartesian(cx, cy, innerR, a2);
  const iE = polarToCartesian(cx, cy, innerR, a1);
  const largeArc = endAngle - startAngle > Math.PI ? 1 : 0;
  return [
    `M ${oS.x} ${oS.y}`,
    `A ${r} ${r} 0 ${largeArc} 1 ${oE.x} ${oE.y}`,
    `L ${iS.x} ${iS.y}`,
    `A ${innerR} ${innerR} 0 ${largeArc} 0 ${iE.x} ${iE.y}`,
    `Z`,
  ].join(' ');
}

function ByWorkspaceView({
  rows,
  metric,
  countSub,
  tokenSub,
  currentWorkspaceId,
  onJumpToWorkspace,
}: ViewBaseProps & {
  rows: WorkspaceRow[];
  currentWorkspaceId?: string;
  onJumpToWorkspace?: (workspaceId: string) => void;
}) {
  const { t } = useTranslation();
  const [tooltip, setTooltip] = useState<ChartTooltipState | null>(null);

  const items = useMemo(() => {
    const out = rows.map((r) => ({
      id: r.workspaceId,
      label: r.workspaceName || r.workspaceId,
      value: pickValue(r, metric, countSub, tokenSub),
      row: r,
    }));
    out.sort((a, b) => {
      if (b.value !== a.value) return b.value - a.value;
      return a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' });
    });
    return out;
  }, [rows, metric, countSub, tokenSub]);

  const total = items.reduce((sum, it) => sum + it.value, 0);
  const positiveItems = items.filter((it) => it.value > 0);
  const colorById = useMemo(() => {
    const map = new Map<string, string>();
    positiveItems.forEach((it, i) => map.set(it.id, stableColor(i)));
    return map;
  }, [positiveItems]);

  const cx = 90;
  const cy = 90;
  const outerR = 78;
  const innerR = 46;
  const arcs = useMemo(() => {
    if (total <= 0) return [] as Array<{ id: string; d: string; color: string; item: typeof items[number] }>;
    let cumulative = 0;
    return positiveItems.map((it) => {
      const startAngle = (cumulative / total) * 2 * Math.PI;
      cumulative += it.value;
      const endAngle = (cumulative / total) * 2 * Math.PI;
      return {
        id: it.id,
        d: donutArc(cx, cy, outerR, innerR, startAngle, endAngle),
        color: colorById.get(it.id) || '#cbd5e1',
        item: it,
      };
    });
  }, [positiveItems, total, colorById]);

  const showTooltip = (
    e: ReactMouseEvent<Element>,
    item: typeof items[number],
  ) => {
    const pct = total > 0 ? (item.value / total) * 100 : 0;
    setTooltip({
      x: e.clientX + 12,
      y: e.clientY + 12,
      title: item.label,
      subtitle: t('stats.chart.shareOfTotal', { pct: pct.toFixed(1) }),
      rows: breakdownRows(t, item.row, metric, countSub, tokenSub),
    });
  };

  return (
    <section className="stats-section">
      <h3 className="stats-section-title">{t('stats.view.byWorkspace')}</h3>
      {items.length === 0 || total <= 0 ? (
        <div className="stats-section-empty">{t('stats.noDataInRange')}</div>
      ) : (
        <>
          <div className="stats-donut-wrap">
            <svg
              width={180}
              height={180}
              viewBox="0 0 180 180"
              preserveAspectRatio="xMidYMid meet"
              className="stats-donut"
              aria-hidden="true"
            >
              {arcs.map((a) => {
                const isCurrent = currentWorkspaceId === a.id;
                const clickable = Boolean(onJumpToWorkspace && !a.item.row.deleted);
                return (
                  <path
                    key={a.id}
                    d={a.d}
                    fill={a.color}
                    className={`stats-donut-slice ${isCurrent ? 'current' : ''} ${clickable ? 'clickable' : ''}`}
                    onMouseEnter={(e) => showTooltip(e, a.item)}
                    onMouseMove={(e) => showTooltip(e, a.item)}
                    onMouseLeave={() => setTooltip(null)}
                    onClick={() => clickable && onJumpToWorkspace?.(a.id)}
                  />
                );
              })}
              <text x={cx} y={cy - 4} textAnchor="middle" className="stats-donut-center-value">
                {formatMetricValue(total, metric)}
              </text>
              <text x={cx} y={cy + 12} textAnchor="middle" className="stats-donut-center-label">
                {t('stats.chart.totalLabel')}
              </text>
            </svg>
          </div>
          <ul className="stats-chart-legend">
            {items.map((item) => {
              const swatch = colorById.get(item.id) || '#cbd5e1';
              const pct = total > 0 ? (item.value / total) * 100 : 0;
              const isCurrent = currentWorkspaceId === item.id;
              const clickable = Boolean(onJumpToWorkspace && item.row.workspaceId && !item.row.deleted);
              const onActivate = () => {
                if (clickable) onJumpToWorkspace?.(item.row.workspaceId);
              };
              return (
                <li
                  key={item.id}
                  className={`stats-chart-legend-item ${isCurrent ? 'current' : ''} ${clickable ? 'clickable' : ''} ${item.value === 0 ? 'zero' : ''}`}
                  onMouseEnter={(e) => showTooltip(e, item)}
                  onMouseMove={(e) => showTooltip(e, item)}
                  onMouseLeave={() => setTooltip(null)}
                  onClick={onActivate}
                  onKeyDown={(e) => {
                    if (!clickable) return;
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      onActivate();
                    }
                  }}
                  role={clickable ? 'button' : undefined}
                  tabIndex={clickable ? 0 : undefined}
                >
                  <span className="stats-chart-legend-swatch" style={{ background: swatch }} />
                  <span className="stats-chart-legend-label">
                    {item.label}
                    {item.row.deleted && (
                      <span className="stats-chart-legend-muted"> {t('stats.table.deletedSuffix')}</span>
                    )}
                  </span>
                  <span className="stats-chart-legend-value">{formatMetricValue(item.value, metric)}</span>
                  <span className="stats-chart-legend-pct">{pct.toFixed(1)}%</span>
                </li>
              );
            })}
          </ul>
        </>
      )}
      {tooltip && <ChartTooltipPanel state={tooltip} />}
    </section>
  );
}

function ByModelView({ rows, metric, countSub, tokenSub }: ViewBaseProps & { rows: ModelRow[] }) {
  const { t } = useTranslation();
  const [tooltip, setTooltip] = useState<ChartTooltipState | null>(null);
  const items = useMemo(() => {
    const out = rows.map((r) => {
      const rawLabel = r.modelName || r.modelId;
      const label = isUnknownModelLabel(rawLabel) ? t('stats.unknownModel') : (rawLabel as string);
      return {
        id: r.modelId,
        label,
        value: pickValue(r, metric, countSub, tokenSub),
        row: r,
      };
    });
    out.sort((a, b) => {
      if (b.value !== a.value) return b.value - a.value;
      return a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' });
    });
    return out;
  }, [rows, metric, countSub, tokenSub, t]);
  const positiveItems = items.filter((it) => it.value > 0);
  const visible = positiveItems.slice(0, TOP_N);
  const overflow = positiveItems.slice(TOP_N);
  const max = visible.reduce((m, it) => Math.max(m, it.value), 0);

  const showTooltip = (e: ReactMouseEvent<Element>, item: typeof items[number]) => {
    setTooltip({
      x: e.clientX + 12,
      y: e.clientY + 12,
      title: item.label,
      rows: breakdownRows(t, item.row, metric, countSub, tokenSub),
    });
  };

  return (
    <section className="stats-section">
      <h3 className="stats-section-title">{t('stats.view.byModel')}</h3>
      {visible.length === 0 ? (
        <div className="stats-section-empty">{t('stats.noDataInRange')}</div>
      ) : (
        <ul className="stats-hbar-list">
          {visible.map((it, i) => {
            const pct = max > 0 ? (it.value / max) * 100 : 0;
            return (
              <li
                key={it.id}
                className="stats-hbar-item"
                onMouseEnter={(e) => showTooltip(e, it)}
                onMouseMove={(e) => showTooltip(e, it)}
                onMouseLeave={() => setTooltip(null)}
              >
                <div className="stats-hbar-label" title={it.label}>{it.label}</div>
                <div className="stats-hbar-track">
                  <div
                    className="stats-hbar-fill"
                    style={{ width: `${Math.max(pct, 1.5)}%`, background: stableColor(i) }}
                  />
                </div>
                <div className="stats-hbar-value">{formatMetricValue(it.value, metric)}</div>
              </li>
            );
          })}
          {overflow.length > 0 && (
            <li className="stats-hbar-overflow">
              {t('stats.chart.othersHidden', { count: overflow.length })}
            </li>
          )}
        </ul>
      )}
      {tooltip && <ChartTooltipPanel state={tooltip} />}
    </section>
  );
}

function ByToolView({ rows, metric }: { rows: ToolRow[]; metric: Metric }) {
  const { t } = useTranslation();
  const [tooltip, setTooltip] = useState<ChartTooltipState | null>(null);
  const items = useMemo(() => {
    const out = rows.map((r) => ({
      id: r.toolKey,
      label: r.toolKey,
      value: metric === 'duration' ? r.totalMs : r.count,
      row: r,
    }));
    out.sort((a, b) => {
      if (b.value !== a.value) return b.value - a.value;
      return a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: 'base' });
    });
    return out;
  }, [rows, metric]);

  if (metric === 'tokens') {
    return (
      <section className="stats-section stats-section-tools">
        <h3 className="stats-section-title">{t('stats.view.byTool')}</h3>
        <div className="stats-section-empty">{t('stats.tokensNotApplicable')}</div>
      </section>
    );
  }

  const positiveItems = items.filter((it) => it.value > 0);
  const max = positiveItems.reduce((m, it) => Math.max(m, it.value), 0);

  const showTooltip = (e: ReactMouseEvent<Element>, item: typeof items[number]) => {
    setTooltip({
      x: e.clientX + 12,
      y: e.clientY + 12,
      title: item.label,
      rows: [
        { label: t('stats.table.duration'), value: formatStatsDuration(item.row.totalMs), emphasized: metric === 'duration', muted: metric !== 'duration' },
        { label: t('stats.table.calls'), value: formatStatsCount(item.row.count), emphasized: metric === 'counts', muted: metric === 'duration' },
      ],
    });
  };

  return (
    <section className="stats-section stats-section-tools">
      <h3 className="stats-section-title">{t('stats.view.byTool')}</h3>
      {positiveItems.length === 0 ? (
        <div className="stats-section-empty">{t('stats.tools.noToolsInRange')}</div>
      ) : (
        <ul className="stats-hbar-list stats-hbar-list-scrollable">
          {positiveItems.map((it, i) => {
            const pct = max > 0 ? (it.value / max) * 100 : 0;
            return (
              <li
                key={it.id}
                className="stats-hbar-item"
                onMouseEnter={(e) => showTooltip(e, it)}
                onMouseMove={(e) => showTooltip(e, it)}
                onMouseLeave={() => setTooltip(null)}
              >
                <div className="stats-hbar-label" title={it.label}>{it.label}</div>
                <div className="stats-hbar-track">
                  <div
                    className="stats-hbar-fill"
                    style={{ width: `${Math.max(pct, 1.5)}%`, background: stableColor(i) }}
                  />
                </div>
                <div className="stats-hbar-value">
                  {metric === 'duration'
                    ? formatStatsDuration(it.value)
                    : formatStatsCount(it.value)}
                </div>
              </li>
            );
          })}
        </ul>
      )}
      {tooltip && <ChartTooltipPanel state={tooltip} />}
    </section>
  );
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
      assistant: 0,
      thought: 0,
      toolCall: 0,
    },
  };
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
  x: number;
  y: number;
  day: DailyRow;
  segments: TrendSegment[];
}

function trendSegments(day: DailyRow, metric: Metric, countSub: CountSub, tokenSub: TokenSub): TrendSegment[] {
  const total = pickValue(day, metric, countSub, tokenSub);
  const segments: TrendSegment[] = [];
  let modelTotal = 0;
  if (day.models) {
    for (const [mid, st] of Object.entries(day.models)) {
      const value = pickValue(st, metric, countSub, tokenSub);
      modelTotal += value;
      if (value > 0) segments.push({ modelId: isUnknownModelLabel(mid) ? UNKNOWN_MODEL_ID : mid, value });
    }
    const residual = total - modelTotal;
    if (residual > 0) segments.push({ modelId: UNKNOWN_MODEL_ID, value: residual });
  } else if (total > 0) {
    segments.push({ modelId: UNKNOWN_MODEL_ID, value: total });
  }
  return segments;
}

function TrendView({ range, daily, metric, countSub, tokenSub }: ViewBaseProps & { range?: { from: string; to: string }; daily: DailyRow[] }) {
  const { t } = useTranslation();
  const [hiddenModels, setHiddenModels] = useState<Set<string>>(() => new Set());
  const [tooltip, setTooltip] = useState<TrendTooltipState | null>(null);
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [wrapWidth, setWrapWidth] = useState<number>(0);
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
  const dailySegments = useMemo(
    () => filledDaily.map((day) => trendSegments(day, metric, countSub, tokenSub)),
    [filledDaily, metric, countSub, tokenSub],
  );
  const totalSeries = useMemo(
    () => filledDaily.map((day) => pickValue(day, metric, countSub, tokenSub)),
    [filledDaily, metric, countSub, tokenSub],
  );
  const { models, palette } = useMemo(() => {
    const modelSet = new Set<string>();
    for (const segs of dailySegments) {
      segs.forEach((seg) => modelSet.add(seg.modelId));
    }
    const models = Array.from(modelSet).sort();
    const palette = models.map((_, i) => stableColor(i));
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
    const out = new Map<string, number[]>();
    for (const m of models) out.set(m, new Array(filledDaily.length).fill(0));
    dailySegments.forEach((segs, dayIdx) => {
      for (const seg of segs) {
        const arr = out.get(seg.modelId);
        if (arr) arr[dayIdx] = seg.value;
      }
    });
    return out;
  }, [models, dailySegments, filledDaily.length]);
  const max = useMemo(() => {
    let m = 0;
    for (const segs of dailySegments) {
      for (const seg of segs) {
        if (effectiveHiddenModels.has(seg.modelId)) continue;
        if (seg.value > m) m = seg.value;
      }
    }
    if (!effectiveHiddenModels.has(TOTAL_SERIES_KEY)) {
      for (const v of totalSeries) {
        if (v > m) m = v;
      }
    }
    return m;
  }, [dailySegments, totalSeries, effectiveHiddenModels]);

  if (filledDaily.length === 0 || max === 0) {
    return (
      <section className="stats-section stats-section-trend">
        <h3 className="stats-section-title">{t('stats.view.trend')}</h3>
        <div className="stats-section-empty">{t('stats.noDataInRange')}</div>
      </section>
    );
  }

  const height = 220;
  const padding = { top: 16, right: 24, bottom: 28, left: 40 };
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
  const gridLevels = [0.25, 0.5, 0.75];
  const visibleCount = allSeriesKeys.filter((k) => !effectiveHiddenModels.has(k)).length;
  const toggleModel = (model: string) => {
    setHiddenModels((prev) => {
      const next = new Set(prev);
      if (next.has(model)) {
        next.delete(model);
      } else if (visibleCount > 1) {
        next.add(model);
      }
      return next;
    });
  };
  const moveTooltip = (e: ReactMouseEvent<SVGRectElement>, day: DailyRow, segments: TrendSegment[]) => {
    setTooltip({ x: e.clientX + 12, y: e.clientY + 12, day, segments });
  };
  const tooltipMetricLabel = metric === 'duration'
    ? t('stats.table.duration')
    : metric === 'counts'
      ? t(`stats.subMetric.${countSub}`)
      : t(`stats.subMetric.${tokenSub}`);
  const formatModelLabel = (modelId: string) => {
    if (isUnknownModelLabel(modelId)) return t('stats.unknownModel');
    const label = modelNames.get(modelId) || modelId;
    return isUnknownModelLabel(label) ? t('stats.unknownModel') : label;
  };
  const axisUnit = t(`stats.trend.unit.${trendAxisUnitKey(max, metric)}`);

  return (
    <section className="stats-section stats-section-trend">
      <h3 className="stats-section-title">{t('stats.view.trend')}</h3>
      <div className="stats-trend-chart-wrap" ref={wrapRef}>
        <svg
          width={virtualWidth}
          height={height}
          viewBox={`0 0 ${virtualWidth} ${height}`}
          className="stats-trend-chart"
        >
          <text x={4} y={padding.top + 4} className="stats-trend-axis">
            {formatMetricValue(max, metric)}
          </text>
          <text x={virtualWidth - padding.right} y={padding.top + 4} textAnchor="end" className="stats-trend-axis stats-trend-axis-unit">
            {t('stats.trend.axisUnit', { unit: axisUnit })}
          </text>
          <text x={4} y={padding.top + innerHeight} className="stats-trend-axis">0</text>
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
          {!effectiveHiddenModels.has(TOTAL_SERIES_KEY) && totalSeries.some((v) => v > 0) && (
            <g>
              {totalSeries.length > 1 && (
                <polyline
                  points={totalSeries.map((v, i) => `${xAt(i)},${yAt(v)}`).join(' ')}
                  fill="none"
                  stroke={TOTAL_SERIES_COLOR}
                  strokeWidth={2.5}
                  strokeDasharray="6 4"
                  className="stats-trend-line stats-trend-line-total"
                />
              )}
              {totalSeries.map((v, i) => (
                <circle
                  key={i}
                  cx={xAt(i)}
                  cy={yAt(v)}
                  r={hoverIdx === i ? 4 : 3}
                  fill={TOTAL_SERIES_COLOR}
                  className="stats-trend-point stats-trend-point-total"
                />
              ))}
            </g>
          )}
          {models.map((modelId, mi) => {
            if (effectiveHiddenModels.has(modelId)) return null;
            const values = seriesByModel.get(modelId) || [];
            const points = values.map((v, i) => `${xAt(i)},${yAt(v)}`).join(' ');
            const color = palette[mi];
            return (
              <g key={modelId}>
                {values.length > 1 && (
                  <polyline
                    points={points}
                    fill="none"
                    stroke={color}
                    strokeWidth={2}
                    className="stats-trend-line"
                  />
                )}
                {values.map((v, i) => (
                  <circle
                    key={i}
                    cx={xAt(i)}
                    cy={yAt(v)}
                    r={hoverIdx === i ? 4 : 3}
                    fill={color}
                    className="stats-trend-point"
                  />
                ))}
              </g>
            );
          })}
          {filledDaily.map((day, idx) => (
            <g key={day.date}>
              <rect
                x={padding.left + idx * colWidth}
                y={padding.top}
                width={colWidth}
                height={innerHeight}
                fill="transparent"
                className="stats-trend-hitbox"
                onMouseEnter={(e) => { setHoverIdx(idx); moveTooltip(e, day, dailySegments[idx]); }}
                onMouseMove={(e) => { setHoverIdx(idx); moveTooltip(e, day, dailySegments[idx]); }}
                onMouseLeave={() => { setHoverIdx(null); setTooltip(null); }}
              />
              <text
                x={xAt(idx)}
                y={height - 8}
                textAnchor="middle"
                className="stats-trend-tick"
              >
                {day.date.slice(5)}
              </text>
            </g>
          ))}
        </svg>
      </div>
      {/* Legend */}
      <div className="stats-trend-legend">
        <button
          key={TOTAL_SERIES_KEY}
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
        <div className="stats-trend-tooltip" style={{ left: tooltip.x, top: tooltip.y }}>
          <div className="stats-trend-tooltip-title">{tooltip.day.date}</div>
          <div className="stats-trend-tooltip-row">
            <span>{tooltipMetricLabel}</span>
            <strong>{formatMetricValue(pickValue(tooltip.day, metric, countSub, tokenSub), metric)}</strong>
          </div>
          <div className="stats-trend-tooltip-row">
            <span>{t('stats.table.turns')}</span>
            <strong>{formatStatsCount(tooltip.day.turnCount)}</strong>
          </div>
          <div className="stats-trend-tooltip-divider" />
          {tooltip.segments.length === 0 ? (
            <div className="stats-trend-tooltip-muted">{t('stats.noDataInRange')}</div>
          ) : tooltip.segments.map((seg) => (
            <div key={seg.modelId} className="stats-trend-tooltip-row">
              <span>{formatModelLabel(seg.modelId)}</span>
              <strong>{formatMetricValue(seg.value, metric)}</strong>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

// stableColor maps an index to a deterministic color from a small palette.
// Using HSL so the palette grows gracefully when more models appear.
function stableColor(idx: number): string {
  const hues = [210, 145, 30, 270, 0, 50, 175, 320, 100, 250];
  const hue = hues[idx % hues.length];
  return `hsl(${hue}, 60%, 55%)`;
}
