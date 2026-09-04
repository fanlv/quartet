import { expect, test, type Page } from '../fixtures/test'
import {
  e2ePagedHistoryFirstAnswer,
  e2ePagedHistoryFirstQuestion,
  e2ePagedHistoryJobID,
  e2ePagedHistoryLastAnswer,
  e2ePagedHistorySessionID,
} from '../fixtures/e2e-environment'

// The chat page loads only the newest history page and pulls earlier ones in
// as the user scrolls up. On a terminal job every SSE reconnect re-reads that
// newest page and merges it back, which is where the conversation used to get
// corrupted: everything the page did not cover was dropped, and live-only
// bubbles were swept to the end of the list so an older bubble rendered below
// the newest message.

const historyURLFragment = `/sessions/${e2ePagedHistorySessionID}/messages`

/**
 * Serves the job event stream as a response that completes immediately, so the
 * SSE client keeps reconnecting on its own. Each successful reconnect runs the
 * production recovery path (onReconnect -> newest-page reload) without needing
 * a live agent or a real network fault. Must be installed before navigation:
 * routes only apply to requests started after they are added.
 */
async function stubFlappingEventStream(page: Page) {
  await page.route('**/api/v1/job/*/events*', async (route) => {
    await route.fulfill({
      status: 200,
      headers: { 'Cache-Control': 'no-store' },
      contentType: 'text/event-stream',
      body: ': keep-alive\n\n',
    })
  })
}

async function openPagedHistoryJob(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('quartet-language', 'en')
  })
  await page.goto(`/?workspaceId=ws-1&jobId=${e2ePagedHistoryJobID}`)
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)
  await expect(page.getByTestId('job-chat')).toBeVisible({ timeout: 30_000 })
  await expect(page.getByText(e2ePagedHistoryLastAnswer, { exact: true })).toBeVisible({ timeout: 30_000 })
}

/** Scrolls the transcript to the top until the opening question is rendered. */
async function pageBackToFirstQuestion(page: Page) {
  const list = page.getByTestId('message-list')
  const firstQuestion = page.getByText(e2ePagedHistoryFirstQuestion, { exact: true })
  await expect.poll(async () => {
    await list.evaluate((el) => { el.scrollTop = 0 })
    await page.waitForTimeout(200)
    return await firstQuestion.count()
  }, { timeout: 40_000, message: 'backwards paging never reached the opening question' }).toBeGreaterThan(0)
}

function renderedMessageIDs(page: Page) {
  return page.locator('[data-testid="message-item"]').evaluateAll(
    (nodes) => nodes.map((node) => (node as HTMLElement).dataset.messageId ?? ''),
  )
}

/** Waits for the next reconnect-driven newest-page reload to land. */
async function waitForNewestPageReload(page: Page) {
  await page.waitForRequest(
    (request) => request.url().includes(historyURLFragment),
    { timeout: 60_000 },
  )
  // Let the merged list commit before asserting on the DOM.
  await page.waitForTimeout(1_500)
}

test('keeps paged-in history when a reconnect reloads the newest page', async ({ page }) => {
  test.setTimeout(180_000)
  await stubFlappingEventStream(page)
  await openPagedHistoryJob(page)

  // The opening question is far outside the newest page, so it only appears
  // after the upward scroll has pulled earlier pages in.
  await expect(page.getByText(e2ePagedHistoryFirstQuestion, { exact: true })).toHaveCount(0)
  await pageBackToFirstQuestion(page)
  const renderedBefore = (await renderedMessageIDs(page)).length

  await waitForNewestPageReload(page)

  // Everything the user paged in must survive the reload, exactly once.
  await expect(page.getByText(e2ePagedHistoryFirstQuestion, { exact: true })).toHaveCount(1)
  await expect(page.getByText(e2ePagedHistoryFirstAnswer, { exact: true })).toHaveCount(1)
  await expect(page.getByText(e2ePagedHistoryLastAnswer, { exact: true })).toHaveCount(1)
  expect((await renderedMessageIDs(page)).length).toBeGreaterThanOrEqual(renderedBefore)
})

test('newest-page reload keeps transcript order and renders no message twice', async ({ page }) => {
  test.setTimeout(180_000)
  await stubFlappingEventStream(page)
  await openPagedHistoryJob(page)
  await pageBackToFirstQuestion(page)
  const orderBefore = await renderedMessageIDs(page)

  await waitForNewestPageReload(page)
  const orderAfter = await renderedMessageIDs(page)

  // No id may render twice, and everything that was on screen before the
  // reload must still be in the same relative order — an older bubble
  // re-appearing below a newer one is exactly the reported symptom.
  expect(new Set(orderAfter).size).toBe(orderAfter.length)
  const survivors = orderBefore.filter((id) => orderAfter.includes(id))
  expect(survivors.length).toBeGreaterThan(0)
  expect(orderAfter.filter((id) => survivors.includes(id))).toEqual(survivors)
})

test('an empty newest page does not blank the transcript', async ({ page }) => {
  test.setTimeout(180_000)
  await stubFlappingEventStream(page)
  await openPagedHistoryJob(page)
  const renderedBefore = (await renderedMessageIDs(page)).length
  expect(renderedBefore).toBeGreaterThan(0)

  // A newest page that comes back empty (transcript not flushed yet) must
  // leave the rendered conversation alone rather than clearing it.
  await page.route(`**${historyURLFragment}*`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ messages: [], page: { hasMoreBefore: false } }),
    })
  })

  await waitForNewestPageReload(page)

  await expect(page.getByText(e2ePagedHistoryLastAnswer, { exact: true })).toHaveCount(1)
  expect((await renderedMessageIDs(page)).length).toBe(renderedBefore)
})

test('the chat page does not loop resolving an unresolvable agent reference', async ({ page }) => {
  test.setTimeout(60_000)
  let resolveCalls = 0
  page.on('request', (request) => {
    if (request.url().includes('/api/v1/agent/display-info/resolve')) resolveCalls += 1
  })

  // The seeded session references an Agent that is not installed here, so the
  // page can never resolve it. Re-asking forever starved the browser's
  // per-origin connection pool and kept the page re-rendering.
  await openPagedHistoryJob(page)
  await page.waitForTimeout(5_000)
  const afterIdle = resolveCalls
  await page.waitForTimeout(5_000)

  expect(resolveCalls).toBeLessThanOrEqual(afterIdle + 1)
  expect(resolveCalls).toBeLessThan(10)
})
