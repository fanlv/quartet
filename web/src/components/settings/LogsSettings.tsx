import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import './LogsSettings.css';

type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';
type LevelFilter = '' | 'debug' | 'info' | 'warn' | 'error';

interface LogEntry {
  id: number;
  timestamp: string;
  level: LogLevel;
  source: string;
  message: string;
}

interface ListResponse {
  code: number;
  level: LevelFilter;
  entries: LogEntry[];
}

const POLL_INTERVAL_MS = 2000;
const MAX_RENDER = 800;

const LEVEL_ORDER: Record<LogLevel, number> = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

export function LogsSettings() {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [paused, setPaused] = useState(false);
  const [levelFilter, setLevelFilter] = useState<LevelFilter>('');
  const [sourceFilter, setSourceFilter] = useState<'all' | 'backend' | 'frontend'>('all');
  const [keyword, setKeyword] = useState('');
  const [serverLevel, setServerLevel] = useState<LevelFilter>('info');
  const [error, setError] = useState<string | null>(null);
  const sinceRef = useRef<number>(0);
  const inFlightRef = useRef<boolean>(false);
  const listRef = useRef<HTMLDivElement>(null);

  const fetchOnce = async (reset = false) => {
    if (!reset && inFlightRef.current) return;
    inFlightRef.current = true;
    try {
      const params = new URLSearchParams();
      params.set('limit', '500');
      if (levelFilter) params.set('level', levelFilter);
      // The server partitions entries by `kind` BEFORE the 500-row cap, so
      // backend / frontend views actually return up to 500 of that kind.
      // Filtering the response client-side instead would silently truncate
      // whichever bucket happened to be in the minority of recent activity.
      if (sourceFilter !== 'all') params.set('kind', sourceFilter);
      if (keyword) params.set('keyword', keyword);
      if (!reset && sinceRef.current > 0) params.set('since', String(sinceRef.current));
      const res = await fetch(`/api/v1/logs/list?${params.toString()}`);
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }
      const data: ListResponse = await res.json();
      if (data.code !== 0) {
        throw new Error('logs api returned error');
      }
      setServerLevel(data.level);
      setError(null);
      const incoming = data.entries || [];
      if (reset) {
        sinceRef.current = incoming.length > 0 ? incoming[0].id : 0;
        setEntries(mergeById([], incoming, MAX_RENDER));
      } else if (incoming.length > 0) {
        sinceRef.current = incoming[0].id;
        setEntries(prev => mergeById(prev, incoming, MAX_RENDER));
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      inFlightRef.current = false;
    }
  };

  // Initial fetch + on filter change
  useEffect(() => {
    sinceRef.current = 0;
    fetchOnce(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [levelFilter, sourceFilter, keyword]);

  // Polling
  useEffect(() => {
    if (!autoRefresh || paused) return;
    const id = setInterval(() => fetchOnce(false), POLL_INTERVAL_MS);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, paused, levelFilter, sourceFilter, keyword]);

  const onClear = async () => {
    if (!window.confirm(t('settings.logs.confirmClear'))) return;
    await fetch('/api/v1/logs/clear', { method: 'POST' });
    sinceRef.current = 0;
    setEntries([]);
    fetchOnce(true);
  };

  const onSetLevel = async (lvl: LevelFilter) => {
    if (!lvl) return;
    const res = await fetch('/api/v1/logs/level', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ level: lvl }),
    });
    if (res.ok) {
      const data = await res.json();
      setServerLevel(data.level);
    }
  };

  const filteredEntries = useMemo(() => {
    let result = entries;
    if (levelFilter) {
      const filterOrder = LEVEL_ORDER[levelFilter.toUpperCase() as LogLevel];
      result = result.filter(e => LEVEL_ORDER[e.level] >= filterOrder);
    }
    // Source partitioning is done server-side via the `kind` query param so
    // the 500-row cap applies to the chosen bucket. No need to filter again
    // client-side — doing so would only mask stale entries left over from a
    // prior fetch under a different kind, which mergeById already replaces
    // when fetchOnce(reset=true) fires on the sourceFilter change.
    if (keyword) {
      const lowerKeyword = keyword.toLowerCase();
      result = result.filter(e =>
        e.message.toLowerCase().includes(lowerKeyword) ||
        e.source.toLowerCase().includes(lowerKeyword)
      );
    }
    return result;
  }, [entries, levelFilter, keyword]);

  return (
    <div className="logs-settings">
      <div className="logs-toolbar">
        <div className="logs-toolbar-row logs-toolbar-filters">
          <label className="logs-toolbar-field">
            <span className="logs-toolbar-label">{t('settings.logs.level')}</span>
            <select
              className="logs-select"
              value={serverLevel}
              onChange={e => onSetLevel(e.target.value as LevelFilter)}
            >
              <option value="debug">{t('settings.logs.levelDebug')}</option>
              <option value="info">{t('settings.logs.levelInfo')}</option>
              <option value="warn">{t('settings.logs.levelWarn')}</option>
              <option value="error">{t('settings.logs.levelError')}</option>
            </select>
          </label>
          <label className="logs-toolbar-field">
            <span className="logs-toolbar-label">{t('settings.logs.filter')}</span>
            <select
              className="logs-select"
              value={levelFilter}
              onChange={e => setLevelFilter(e.target.value as LevelFilter)}
            >
              <option value="">{t('settings.logs.filterAll')}</option>
              <option value="debug">{t('settings.logs.filterDebugPlus')}</option>
              <option value="info">{t('settings.logs.filterInfoPlus')}</option>
              <option value="warn">{t('settings.logs.filterWarnPlus')}</option>
              <option value="error">{t('settings.logs.filterErrorOnly')}</option>
            </select>
          </label>
          <label className="logs-toolbar-field">
            <span className="logs-toolbar-label">{t('settings.logs.source')}</span>
            <select
              className="logs-select"
              value={sourceFilter}
              onChange={e => setSourceFilter(e.target.value as 'all' | 'backend' | 'frontend')}
            >
              <option value="all">{t('settings.logs.sourceAll')}</option>
              <option value="backend">{t('settings.logs.sourceBackend')}</option>
              <option value="frontend">{t('settings.logs.sourceFrontend')}</option>
            </select>
          </label>
          <label className="logs-toolbar-field logs-toolbar-field-grow">
            <span className="logs-toolbar-label">{t('settings.logs.keyword')}</span>
            <input
              className="logs-input"
              type="text"
              value={keyword}
              onChange={e => setKeyword(e.target.value)}
              placeholder={t('settings.logs.keywordPlaceholder')}
            />
          </label>
        </div>
        <div className="logs-toolbar-row logs-toolbar-actions">
          <label className="logs-checkbox">
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={e => setAutoRefresh(e.target.checked)}
            />
            {t('settings.logs.autoRefresh')}
          </label>
          <button
            type="button"
            className="logs-btn"
            onClick={() => setPaused(p => !p)}
          >
            {paused ? t('settings.logs.resume') : t('settings.logs.pauseScroll')}
          </button>
          <button type="button" className="logs-btn" onClick={() => fetchOnce(true)}>
            {t('settings.logs.refresh')}
          </button>
          <button type="button" className="logs-btn logs-btn-danger" onClick={onClear}>
            {t('settings.logs.clearBuffer')}
          </button>
          <span className="logs-status">
            {t('settings.logs.totalEntries', { count: filteredEntries.length })}
            {error ? <span className="logs-error">{'　·　'}{error}</span> : null}
          </span>
        </div>
      </div>

      <div className="logs-list" ref={listRef}>
        {filteredEntries.length === 0 ? (
          <div className="logs-empty">{t('settings.logs.noLogs')}</div>
        ) : (
          filteredEntries.map(e => (
            <div key={e.id} className={`logs-row logs-row-${e.level.toLowerCase()}`}>
              <span className="logs-ts">{formatTs(e.timestamp)}</span>
              <span className={`logs-level logs-level-${e.level.toLowerCase()}`}>
                {e.level}
              </span>
              <pre className="logs-msg">
                {e.source ? <span className="logs-msg-source">{e.source}</span> : null}
                {e.source ? ' ' : ''}
                {e.message}
              </pre>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function formatTs(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export { LEVEL_ORDER };

function mergeById(prev: LogEntry[], incoming: LogEntry[], cap: number): LogEntry[] {
  const seen = new Set<number>();
  const out: LogEntry[] = [];
  for (const e of incoming) {
    if (seen.has(e.id)) continue;
    seen.add(e.id);
    out.push(e);
    if (out.length >= cap) return out;
  }
  for (const e of prev) {
    if (seen.has(e.id)) continue;
    seen.add(e.id);
    out.push(e);
    if (out.length >= cap) return out;
  }
  return out;
}
