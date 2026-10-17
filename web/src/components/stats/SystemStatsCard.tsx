import { useEffect, useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { SystemStats } from '../../types/systemStats';
import './SystemStatsCard.css';

function byteParts(bytes: number, locale: string): { value: string; unit: string } {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const index = bytes > 0 ? Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1) : 0;
  return {
    value: new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(bytes / 1024 ** index),
    unit: units[index],
  };
}

function formatBytes(bytes: number, locale: string): string {
  const { value, unit } = byteParts(bytes, locale);
  return `${value} ${unit}`;
}

function compactCapacity(value: NonNullable<SystemStats['memory']>, locale: string): string {
  const used = byteParts(value.usedBytes, locale);
  const total = byteParts(value.totalBytes, locale);
  return used.unit === total.unit
    ? `${used.value} / ${total.value} ${total.unit}`
    : `${used.value} ${used.unit} / ${total.value} ${total.unit}`;
}

export function SystemStatsCard() {
  const { t, i18n } = useTranslation();
  const [data, setData] = useState<SystemStats | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [revision, setRevision] = useState(0);
  const [expanded, setExpanded] = useState(false);
  const detailsId = useId();
  const locale = i18n.resolvedLanguage || i18n.language;

  useEffect(() => {
    const controller = new AbortController();
    let pending = false;
    const refresh = async () => {
      if (document.hidden || pending || controller.signal.aborted) return;
      pending = true;
      setLoading(true);
      try {
        const response = await fetch('/api/v1/stats/system', { signal: controller.signal, cache: 'no-store' });
        const body = await response.text();
        if (!response.ok) {
          throw new Error(`HTTP ${response.status} ${response.statusText}\n\n${body}`);
        }
        let sample: SystemStats;
        try {
          sample = JSON.parse(body) as SystemStats;
        } catch (cause) {
          throw new Error(`HTTP ${response.status} ${response.statusText}\n\n${body}\n\n${String(cause)}`);
        }
        if (!controller.signal.aborted) {
          setData(sample);
          setError('');
        }
      } catch (cause) {
        if (!controller.signal.aborted) {
          const detail = cause instanceof Error ? cause.message : String(cause);
          setError(`GET ${new URL('/api/v1/stats/system', window.location.href).href}\n${detail}`);
        }
      } finally {
        pending = false;
        if (!controller.signal.aborted) setLoading(false);
      }
    };
    const onVisible = () => { if (!document.hidden) void refresh(); };
    void refresh();
    const timer = window.setInterval(() => { void refresh(); }, 5_000);
    document.addEventListener('visibilitychange', onVisible);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
    };
  }, [revision]);

  const percent = (value: number) => new Intl.NumberFormat(locale, {
    style: 'percent', minimumFractionDigits: 1, maximumFractionDigits: 1,
  }).format(value / 100);
  const capacity = (value: SystemStats['memory']) => value
    ? t('stats.system.capacity', { used: formatBytes(value.usedBytes, locale), total: formatBytes(value.totalBytes, locale) })
    : t('stats.system.unavailable');
  const metrics = [
    {
      id: 'cpu', label: t('stats.system.cpu'), usage: data?.cpu?.usagePercent,
      detail: data?.cpu ? t('stats.system.cores', { count: data.cpu.logicalCores }) : t('stats.system.unavailable'),
      compactDetail: data?.cpu ? t('stats.system.cores', { count: data.cpu.logicalCores }) : t('stats.system.unavailable'),
      hint: data?.cpu?.model,
    },
    {
      id: 'memory', label: t('stats.system.memory'), usage: data?.memory?.usagePercent,
      detail: capacity(data?.memory ?? null),
      compactDetail: data?.memory ? compactCapacity(data.memory, locale) : t('stats.system.unavailable'),
      hint: data?.memory ? t('stats.system.available', { value: formatBytes(data.memory.availableBytes, locale) }) : '',
    },
    {
      id: 'disk', label: t('stats.system.disk'), usage: data?.disk?.usagePercent,
      detail: capacity(data?.disk ?? null),
      compactDetail: data?.disk ? compactCapacity(data.disk, locale) : t('stats.system.unavailable'),
      hint: data?.disk ? t('stats.system.available', { value: formatBytes(data.disk.availableBytes, locale) }) : '',
    },
  ];
  const errors = [error, ...(data?.errors ?? [])].filter(Boolean);
  const sampledAt = data?.sampledAt ? new Date(data.sampledAt) : null;

  return (
    <section className="stats-card system-stats" aria-label={t('stats.system.title')} aria-busy={loading && !data}>
      <div className="system-stats-head">
        <div className="system-stats-heading">
          <h3 className="stats-card-title" title={t('stats.system.description')}>{t('stats.system.title')}</h3>
          {data?.hostname && <span className="system-stats-host" title={data.hostname}>{data.hostname}</span>}
        </div>
        <div className="system-stats-actions">
          <button
            type="button"
            className="system-stats-toggle"
            aria-expanded={expanded}
            aria-controls={detailsId}
            onClick={() => setExpanded((value) => !value)}
          >
            {t('stats.system.details')}
            <svg className={expanded ? 'expanded' : ''} width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
              <path d="m4 6 4 4 4-4" />
            </svg>
          </button>
          <button
            type="button"
            className="system-stats-refresh"
            aria-label={t('stats.system.refresh')}
            title={t('stats.system.refresh')}
            disabled={loading}
            onClick={() => setRevision((value) => value + 1)}
          >
            <svg className={loading ? 'system-stats-spinning' : ''} width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M20 7v5h-5M4 17v-5h5" />
              <path d="M6.1 7a7 7 0 0 1 11.5-1L20 9M4 15l2.4 3A7 7 0 0 0 17.9 17" />
            </svg>
          </button>
        </div>
      </div>
      <div className="system-stats-grid">
        {metrics.map((metric) => (
          <div className="system-stats-metric" key={metric.id}>
            <div className="system-stats-label">{metric.label}</div>
            <strong className={`system-stats-value${loading && !data ? ' system-stats-placeholder' : ''}`}>
              {metric.usage == null ? '—' : percent(metric.usage)}
            </strong>
            {metric.usage != null && (
              <div
                className={`system-stats-meter ${metric.usage >= 90 ? 'critical' : metric.usage >= 75 ? 'warning' : ''}`}
                role="meter"
                aria-label={metric.label}
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={metric.usage}
                aria-valuetext={percent(metric.usage)}
              >
                <span style={{ transform: `scaleX(${Math.min(100, Math.max(0, metric.usage)) / 100})` }} />
              </div>
            )}
            <div className="system-stats-detail" title={metric.detail}>
              <span className="system-stats-accessible">{loading && !data ? t('stats.system.loading') : metric.detail}</span>
              <span aria-hidden="true">{loading && !data ? t('stats.system.loading') : metric.compactDetail}</span>
            </div>
          </div>
        ))}
      </div>
      <div className="system-stats-details" id={detailsId} hidden={!expanded}>
        <div className="system-stats-meta">
          <span>{t('stats.system.description')}</span>
          {data?.hostname && <span>{data.hostname}</span>}
          {sampledAt && !Number.isNaN(sampledAt.getTime()) && (
            <span>{t('stats.system.updatedAt', { time: sampledAt.toLocaleTimeString(locale) })}</span>
          )}
        </div>
        <dl className="system-stats-detail-list">
          {metrics.map((metric) => (
            <div key={metric.id}>
              <dt>{metric.label}</dt>
              <dd>{metric.detail}{metric.hint && <span>{metric.hint}</span>}</dd>
            </div>
          ))}
        </dl>
        <div className="system-stats-path">
          {t('stats.system.diskScope')}{data?.disk?.path && <code>{data.disk.path}</code>}
        </div>
      </div>
      {errors.length > 0 && (
        <div className="system-stats-error" role="alert">
          <strong>{t('stats.system.fetchError')}</strong>
          <pre>{errors.join('\n\n')}</pre>
        </div>
      )}
    </section>
  );
}
