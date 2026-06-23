import { useState, useCallback, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';

interface CronInputProps {
  value: string;
  onChange: (value: string) => void;
}

const PRESET_KEYS = [
  { labelKey: 'cron.presets.everyHour', descKey: 'cron.presets.everyHourDesc', value: '0 * * * *' },
  { labelKey: 'cron.presets.everyDay9', descKey: 'cron.presets.everyDay9Desc', value: '0 9 * * *' },
  { labelKey: 'cron.presets.everyDay18', descKey: 'cron.presets.everyDay18Desc', value: '0 18 * * *' },
  { labelKey: 'cron.presets.weekday9', descKey: 'cron.presets.weekday9Desc', value: '0 9 * * 1-5' },
  { labelKey: 'cron.presets.everyMonday9', descKey: 'cron.presets.everyMonday9Desc', value: '0 9 * * 1' },
  { labelKey: 'cron.presets.every30min', descKey: 'cron.presets.every30minDesc', value: '*/30 * * * *' },
  { labelKey: 'cron.presets.every5min', descKey: 'cron.presets.every5minDesc', value: '*/5 * * * *' },
  { labelKey: 'cron.presets.every1min', descKey: 'cron.presets.every1minDesc', value: '*/1 * * * *' },
];

function cronToHuman(expr: string, t: TFunction): string {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return expr;
  const [min, hour, dom, mon, dow] = parts;

  const isNum = (s: string) => /^\d+$/.test(s);

  // Every N minutes
  if (hour === '*' && dom === '*' && mon === '*' && dow === '*') {
    if (min === '*') return t('cron.human.everyMinute');
    if (min.startsWith('*/')) return t('cron.human.everyNMinutes', { n: min.slice(2) });
    if (isNum(min)) return t('cron.human.everyHourAtMinute', { min });
    return expr;
  }

  // Every N hours at fixed minute
  if (hour.startsWith('*/') && dom === '*' && mon === '*' && dow === '*' && isNum(min)) {
    return t('cron.human.everyNHours', { n: hour.slice(2) });
  }

  // Fixed time (both hour and min must be plain numbers)
  if (isNum(hour) && isNum(min) && dom === '*' && mon === '*') {
    const timeStr = `${hour.padStart(2, '0')}:${min.padStart(2, '0')}`;
    if (dow === '*') return t('cron.human.everyDayAt', { time: timeStr });
    const dowLabel = t(`cron.dow.${dow}`, { defaultValue: dow });
    return t('cron.human.dowAt', { dow: dowLabel, time: timeStr });
  }

  return expr;
}

// react-refresh only accepts component exports alongside its target, but this
// helper is too small to live in its own file. Disable the hot-reload warning
// for the one line below rather than splitting the module.
// eslint-disable-next-line react-refresh/only-export-components
export { cronToHuman };

export function CronInput({ value, onChange }: CronInputProps) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<'preset' | 'custom'>(() => {
    return PRESET_KEYS.some(p => p.value === value) ? 'preset' : 'custom';
  });
  const [customValue, setCustomValue] = useState(value);
  const [error, setError] = useState('');

  const prevValueRef = useRef(value);
  useEffect(() => {
    if (prevValueRef.current === value) {
      return;
    }
    prevValueRef.current = value;

    // Snap back to the preset tab only when `value` changes externally.
    // If the user is currently typing in custom mode, do not auto-switch
    // even if the expression happens to equal a preset (to avoid unmounting
    // the input and stealing focus).
    if (PRESET_KEYS.some(p => p.value === value)) {
      if (!(mode === 'custom' && value === customValue)) {
        setMode('preset');
      }
    }
  }, [value, mode, customValue]);

  const validateCron = useCallback((expr: string): string | null => {
    const parts = expr.trim().split(/\s+/);
    if (parts.length !== 5) return t('cron.validation.fiveFields');
    return null;
  }, [t]);

  const handleCustomChange = (v: string) => {
    setCustomValue(v);
    const err = validateCron(v);
    setError(err || '');
    if (!err) {
      onChange(v);
    }
  };

  return (
    <div className="cron-input">
      <div className="cron-input-tabs">
        <button
          className={`cron-tab ${mode === 'preset' ? 'active' : ''}`}
          onClick={() => setMode('preset')}
          type="button"
        >
          {t('cron.tabs.preset')}
        </button>
        <button
          className={`cron-tab ${mode === 'custom' ? 'active' : ''}`}
          onClick={() => { setMode('custom'); setCustomValue(value); }}
          type="button"
        >
          {t('cron.tabs.custom')}
        </button>
      </div>

      {mode === 'preset' ? (
        <div className="cron-presets">
          {PRESET_KEYS.map(p => (
            <button
              key={p.value}
              className={`cron-preset-btn ${value === p.value ? 'active' : ''}`}
              onClick={() => onChange(p.value)}
              type="button"
            >
              <span className="cron-preset-label">{t(p.labelKey)}</span>
              <span className="cron-preset-desc">{t(p.descKey)}</span>
            </button>
          ))}
        </div>
      ) : (
        <div className="cron-custom">
          <input
            className={`cron-custom-input ${error ? 'error' : ''}`}
            value={customValue}
            onChange={e => handleCustomChange(e.target.value)}
            placeholder={t('cron.placeholder')}
            spellCheck={false}
          />
          {error && <div className="cron-error">{error}</div>}
          <div className="cron-help">
            {t('cron.help')}
          </div>
        </div>
      )}

      <div className="cron-human-label">
        {cronToHuman(value, t)}
      </div>
    </div>
  );
}
