import { describe, expect, it } from 'vitest';
import { formatStatsCount, formatStatsDuration } from './statsFormat';

describe('statsFormat utils', () => {
  it('formats roll-up durations by display scale', () => {
    expect(formatStatsDuration(Number.NaN)).toBe('0s');
    expect(formatStatsDuration(-1)).toBe('0s');
    expect(formatStatsDuration(999)).toBe('0s');
    expect(formatStatsDuration(30_999)).toBe('30s');
    expect(formatStatsDuration(18 * 60_000 + 59_000)).toBe('18m');
    expect(formatStatsDuration(3_600_000)).toBe('1h');
    expect(formatStatsDuration(3_600_000 + 5 * 60_000)).toBe('1h 5m');
  });

  it('formats counts using exact, K and M forms', () => {
    expect(formatStatsCount(Number.POSITIVE_INFINITY)).toBe('0');
    expect(formatStatsCount(-1)).toBe('0');
    expect(formatStatsCount(999.9)).toBe('999');
    expect(formatStatsCount(1_234)).toBe('1.2K');
    expect(formatStatsCount(12_345)).toBe('12.3K');
    expect(formatStatsCount(1_234_567)).toBe('1.2M');
    expect(formatStatsCount(12_345_678)).toBe('12.3M');
  });
});
