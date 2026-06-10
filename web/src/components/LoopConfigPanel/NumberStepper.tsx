import { useState } from 'react';

export function NumberStepper({ value, onChange, min = 1, max = 1000, disabled = false }: {
  value: number;
  onChange: (n: number) => void;
  min?: number;
  max?: number;
  disabled?: boolean;
}) {
  const [text, setText] = useState(String(value));
  const [focused, setFocused] = useState(false);

  const commit = (raw: string) => {
    const n = Math.min(max, Math.max(min, parseInt(raw) || min));
    onChange(n);
    setText(String(n));
  };

  return (
    <div className="loop-number-stepper">
      <button
        className="loop-number-stepper-btn"
        onClick={() => { const n = Math.max(min, value - 1); onChange(n); setText(String(n)); }}
        disabled={disabled || value <= min}
        type="button"
      >−</button>
      <input
        className="loop-number-stepper-input"
        type="text"
        inputMode="numeric"
        value={focused ? text : String(value)}
        disabled={disabled}
        onChange={(e) => {
          const v = e.target.value;
          if (v === '' || /^\d+$/.test(v)) setText(v);
        }}
        onFocus={() => { setText(String(value)); setFocused(true); }}
        onBlur={() => { setFocused(false); commit(text); }}
        onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); commit(text); (e.target as HTMLInputElement).blur(); } }}
      />
      <button
        className="loop-number-stepper-btn"
        onClick={() => { const n = Math.min(max, value + 1); onChange(n); setText(String(n)); }}
        disabled={disabled || value >= max}
        type="button"
      >+</button>
    </div>
  );
}
