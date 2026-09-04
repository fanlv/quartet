import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { StatsPage } from './StatsPage';

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('StatsPage token trend accessibility', () => {
  it('shows the latest day by default, ignores hover, and uses arrow keys to select adjacent days', async () => {
    const totals = (total: number, imageEstimate: number) => ({
      totalMs: 1_000,
      turnCount: 1,
      assistantCount: 1,
      thoughtCount: 0,
      toolCallCount: 0,
      tokens: {
        total,
        reported: total,
        input: total,
        output: 0,
        cachedRead: 0,
        cachedWrite: 0,
        reasoning: 0,
        imageEstimate,
        estimated: 0,
        reportedTurns: 1,
        estimatedTurns: 0,
        assistant: 0,
        thought: 0,
        toolCall: 0,
      },
    });
    vi.mocked(fetch).mockResolvedValue(jsonResponse({
      range: { from: '2026-08-24', to: '2026-08-26' },
      byWorkspace: [],
      byModel: [],
      byTool: [],
      daily: [
        { date: '2026-08-24', ...totals(100, 10), models: {} },
        { date: '2026-08-25', ...totals(200, 20), models: {} },
        { date: '2026-08-26', ...totals(300, 30), models: {} },
      ],
      note: 'stats.tokensLocalEstimateNote',
    }));

    render(<StatsPage onClose={vi.fn()} />);

    const chart = await screen.findByRole('group', { name: /Usage trend by day/ });
    const days = within(chart).getAllByRole('button');
    expect(days).toHaveLength(3);
    expect(days.map((day) => day.getAttribute('tabindex'))).toEqual(['-1', '-1', '0']);
    expect(days.map((day) => day.getAttribute('aria-pressed'))).toEqual(['false', 'false', 'true']);
    expect(screen.getByLabelText(/Token breakdown for 2026-08-26/)).toBeInTheDocument();

    fireEvent.mouseEnter(days[0]);
    expect(screen.getByLabelText(/Token breakdown for 2026-08-26/)).toBeInTheDocument();

    act(() => {
      days[0].focus();
    });
    expect(await screen.findByText('Image estimate')).toBeInTheDocument();
    fireEvent.keyDown(days[0], { key: 'ArrowRight' });

    expect(document.activeElement).toBe(days[1]);
    expect(days.map((day) => day.getAttribute('tabindex'))).toEqual(['-1', '0', '-1']);
    expect(days.map((day) => day.getAttribute('aria-pressed'))).toEqual(['false', 'true', 'false']);
    expect(screen.getByLabelText(/Token breakdown for 2026-08-25/)).toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });
});
