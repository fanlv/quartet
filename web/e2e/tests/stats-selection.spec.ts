import { expect, test } from '../fixtures/test'

type Totals = {
  totalMs: number
  turnCount: number
  assistantCount: number
  thoughtCount: number
  toolCallCount: number
  tokens: {
    total: number
    reported: number
    input: number
    output: number
    cachedRead: number
    cachedWrite: number
    reasoning: number
    imageEstimate: number
    estimated: number
    reportedTurns: number
    estimatedTurns: number
    assistant: number
    thought: number
    toolCall: number
  }
}

function dateKey(date: Date) {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function shiftedDateKey(days: number) {
  const date = new Date()
  date.setDate(date.getDate() + days)
  return dateKey(date)
}

function totals(totalMs: number, tokens: number): Totals {
  return {
    totalMs,
    turnCount: 2,
    assistantCount: 2,
    thoughtCount: 0,
    toolCallCount: 1,
    tokens: {
      total: tokens,
      reported: tokens,
      input: Math.round(tokens * 0.8),
      output: Math.round(tokens * 0.2),
      cachedRead: 0,
      cachedWrite: 0,
      reasoning: 0,
      imageEstimate: 0,
      estimated: 0,
      reportedTurns: 2,
      estimatedTurns: 0,
      assistant: 0,
      thought: 0,
      toolCall: 0,
    },
  }
}

test('selecting a usage-statistics date does not draw a focus frame', async ({ page }, testInfo) => {
  const dates = [shiftedDateKey(-2), shiftedDateKey(-1), shiftedDateKey(0)]
  const daily = [totals(3_600_000, 1_000), totals(7_200_000, 2_000), totals(10_800_000, 3_000)]

  await page.route('**/api/v1/stats/usage**', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({
      range: { from: dates[0], to: dates[2] },
      byWorkspace: [{ workspaceId: 'ws-1', workspaceName: 'E2E Workspace', ...totals(21_600_000, 6_000) }],
      byModel: [],
      byTool: [],
      daily: dates.map((date, index) => ({ date, ...daily[index], models: {}, modelNames: {} })),
      note: '',
    }),
  }))
  await page.addInitScript(() => localStorage.setItem('quartet-language', 'en'))
  await page.goto('/?workspaceId=ws-1&view=stats')

  await expect(page.getByRole('heading', { name: 'Usage Statistics' })).toBeVisible()
  const days = page.locator('.stats-trend-hitbox')
  await expect(days).toHaveCount(3)

  const selectedDay = days.first()
  await selectedDay.click()
  await expect(selectedDay).toHaveAttribute('aria-pressed', 'true')

  const focusStyle = await selectedDay.evaluate((element) => {
    const style = getComputedStyle(element)
    return {
      outlineStyle: style.outlineStyle,
      stroke: style.stroke,
      filter: style.filter,
    }
  })
  expect(focusStyle).toMatchObject({
    outlineStyle: 'none',
    stroke: 'none',
    filter: 'none',
  })

  await page.screenshot({ path: testInfo.outputPath('stats-date-selected.png'), fullPage: true })
})
