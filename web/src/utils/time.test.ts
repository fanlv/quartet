import { afterEach, describe, expect, it, vi } from 'vitest';
import { formatMessageTime } from './time';

describe('formatMessageTime', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('returns empty string for empty or invalid timestamps', () => {
    expect(formatMessageTime(undefined)).toBe('');
    expect(formatMessageTime(null)).toBe('');
    expect(formatMessageTime(0)).toBe('');
    expect(formatMessageTime(Number.NaN)).toBe('');
  });

  it('formats same-day timestamps as HH:mm', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 5, 3, 18, 30));

    expect(formatMessageTime(new Date(2026, 5, 3, 9, 5).getTime())).toBe('09:05');
  });

  it('formats same-year timestamps with month and day', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 5, 3, 18, 30));

    expect(formatMessageTime(new Date(2026, 0, 2, 7, 8).getTime())).toBe('01-02 07:08');
  });

  it('formats different-year timestamps with year, month and day', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 5, 3, 18, 30));

    expect(formatMessageTime(new Date(2025, 11, 31, 23, 59).getTime())).toBe('2025-12-31 23:59');
  });
});
