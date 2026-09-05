import { expect, test, type Page } from '../fixtures/test'
import {
  e2ePagedHistoryFirstAnswer,
  e2ePagedHistoryFirstQuestion,
  e2ePagedHistoryJobID,
  e2ePagedHistoryLastAnswer,
  e2ePagedHistoryRecordCount,
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

// The message queue reports the message the backend is currently running as
// `active`, and the run persists it before the agent produces anything. On a
// turn long enough to push it out of the newest page it is therefore on disk
// but ABOVE the loaded window, and treating "not in the rendered list" as "not
// sent yet" appended the user's own question below the replies to it.
const runningQueueActiveID = `${e2ePagedHistorySessionID}-seed-0`

async function stubQueueRunningTheOpeningQuestion(page: Page) {
  await page.route('**/api/v1/job/*/message-queue*', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        code: 0,
        queue: {
          jobId: e2ePagedHistoryJobID,
          version: 1,
          paused: false,
          willContinue: true,
          items: [],
          active: {
            id: runningQueueActiveID,
            state: 'processing',
            // Fixed epoch timestamp: unambiguously older than every record the
            // newest page carries, whatever wall clock the fixture seeded with.
            createdAt: 1,
            messages: [{ content: e2ePagedHistoryFirstQuestion }],
          },
        },
      }),
    })
  })
}

test('the running message stays above the loaded window instead of below its replies', async ({ page }) => {
  test.setTimeout(180_000)
  await stubQueueRunningTheOpeningQuestion(page)
  await stubFlappingEventStream(page)
  await openPagedHistoryJob(page)

  // Pinned to the front of the window, not appended after the newest reply.
  await expect.poll(async () => (await renderedMessageIDs(page))[0], {
    timeout: 30_000,
    message: 'the running message never rendered above the loaded window',
  }).toBe(runningQueueActiveID)
  const idsOnLoad = await renderedMessageIDs(page)
  expect(idsOnLoad[idsOnLoad.length - 1]).not.toBe(runningQueueActiveID)
  await expect(page.getByText(e2ePagedHistoryFirstQuestion, { exact: true })).toHaveCount(1)

  // A newest-page reload must not move it back to the end of the list.
  await waitForNewestPageReload(page)
  expect((await renderedMessageIDs(page))[0]).toBe(runningQueueActiveID)

  // Paging back brings in the real record. Poll on the opening ANSWER, not the
  // question: the pinned stand-in already renders the question's text, so
  // waiting on that would pass without any paging happening at all.
  const list = page.getByTestId('message-list')
  const firstAnswer = page.getByText(e2ePagedHistoryFirstAnswer, { exact: true })
  await expect.poll(async () => {
    await list.evaluate((el) => { el.scrollTop = 0 })
    await page.waitForTimeout(200)
    return await firstAnswer.count()
  }, { timeout: 60_000, message: 'backwards paging never reached the opening exchange' }).toBeGreaterThan(0)

  // The real record supersedes the stand-in: still exactly one opening
  // question, still at the top, and nothing is pinned any more.
  await expect(page.getByText(e2ePagedHistoryFirstQuestion, { exact: true })).toHaveCount(1)
  await expect(page.locator('[data-round-head-pinned="true"]')).toHaveCount(0)
  const idsAfterPaging = await renderedMessageIDs(page)
  expect(idsAfterPaging[0]).toBe(runningQueueActiveID)
  expect(new Set(idsAfterPaging).size).toBe(idsAfterPaging.length)
})

// Scrolling up must never leave the user stalled at the top of what is loaded,
// and a page that arrives while they read must not push content they were
// already looking at into the un-rendered region above the window.
test('keeps two pages buffered and never un-renders the top while paging back', async ({ page }) => {
  test.setTimeout(180_000)
  await openPagedHistoryJob(page)

  // Two pages are buffered right after the first paint, without the user
  // having to reach the top.
  await expect.poll(async () => (await renderedMessageIDs(page)).length, {
    timeout: 30_000,
    message: 'the second page was never primed',
  }).toBeGreaterThan(120)

  const list = page.getByTestId('message-list')
  const firstQuestion = page.getByText(e2ePagedHistoryFirstQuestion, { exact: true })
  await expect.poll(async () => {
    await list.evaluate((el) => { el.scrollTop = 0 })
    await page.waitForTimeout(200)
    return await firstQuestion.count()
  }, { timeout: 40_000, message: 'backwards paging never reached the opening question' }).toBeGreaterThan(0)

  // The whole transcript is loaded now: nothing may be hidden above the top,
  // which is what silently dropped the opening exchange out of the render.
  const ids = await renderedMessageIDs(page)
  expect(ids[0]).toBe(`${e2ePagedHistorySessionID}-seed-0`)
  expect(ids).toHaveLength(e2ePagedHistoryRecordCount)
  await expect(page.getByTestId('message-history-loader')).toHaveCount(0)
})
