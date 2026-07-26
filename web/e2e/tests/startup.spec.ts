import fs from 'node:fs/promises'
import path from 'node:path'

import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
import {
  e2eAgentType,
  e2eAuthToken,
  e2eInterruptedRunningJobID,
  e2eLegacyFirstModelID,
  e2eLegacyFirstModelJobID,
  e2eModelID,
  e2ePersistWarningJobID,
} from '../fixtures/e2e-environment'

// This suite drives REAL agent links. There is no QUARTET_E2E mode, no replay
// model, and no /api/v1/e2e/* control or fixture API. Test data is created
// through the same business APIs a user hits, and assertions check structural
// / state signals (message nodes appear, stream reaches a terminal state, list
// ordering, rename/delete persistence) rather than fixed model text — a live
// agent's wording is not deterministic.
//
// The primary chat-link coverage runs against an ACP agent discovered at
// runtime from the backend's own probe list (GET /api/v1/agent/list). ACP
// needs no quartet-side model config — the subprocess carries its own login
// state (eino-cli reads its isolated EINO_HOME, other agents use $HOME). If
// no ACP agent is installed the chat spec skips itself rather than failing.
//
// Fault links that a real agent cannot trigger (HTTP send failure, SSE auth
// rejection, resume 410 recovery, event-buffer GC) are covered at the
// component layer (web/src/utils/sse-client.test.ts) and in Go unit tests
// (services/job/event_buffer_test.go), not here.

const MODEL_ID = e2eModelID

type E2ERunInfo = {
  localMemory: string
}

type E2EGraphConfig = {
  nodes: Array<Record<string, unknown>>
  edges: Array<Record<string, unknown>>
  variables?: Record<string, string>
  disabledVars?: string[]
  runConfig?: Record<string, unknown>
  workspaceId?: string
  workdir?: string
}

async function openAppWithAuth(page: Page, path = '/') {
  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(path)
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)
}

async function expectHomeReady(page: Page) {
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)
  await expect(page.getByRole('textbox', { name: /ask anything/i })).toBeVisible()
}

async function getE2ERunInfo(): Promise<E2ERunInfo> {
  const runDir = process.env.QUARTET_E2E_RUN_DIR
  if (!runDir) throw new Error('QUARTET_E2E_RUN_DIR is not set; E2E global setup did not run')
  const raw = await fs.readFile(path.join(runDir, 'env.json'), 'utf8')
  return JSON.parse(raw) as E2ERunInfo
}

async function pathExists(filePath: string) {
  try {
    await fs.stat(filePath)
    return true
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'ENOENT') return false
    throw err
  }
}

// createInteractiveJob creates a real interactive job through the public API,
// returning its id. No scenario header — the job runs against the live model.
async function createInteractiveJob(request: APIRequestContext, workspaceId = 'ws-1', title?: string) {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const res = await request.post('/api/v1/job/create', {
    headers,
    data: { agentType: e2eAgentType, modelId: MODEL_ID, workspaceId, mode: 'interactive' },
  })
  expect(res.ok(), `job create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const created = await res.json()
  expect(created.jobId).toMatch(/^job-/)
  if (title) {
    const titleRes = await request.put(`/api/v1/job/${created.jobId}/title`, { headers, data: { title } })
    expect(titleRes.ok()).toBeTruthy()
  }
  return { jobId: created.jobId as string, headers }
}

async function createWorkspace(request: APIRequestContext, title: string, workdir: string) {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const res = await request.post('/api/v1/workspace/create', {
    headers,
    data: { title, description: 'E2E workspace', workdir },
  })
  expect(res.ok(), `workspace create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const created = await res.json()
  expect(created.id).toMatch(/^ws-/)
  return { workspaceId: created.id as string, headers }
}

async function waitForJobStatus(request: APIRequestContext, jobId: string, headers: Record<string, string>, expected: string) {
  return await expect.poll(async () => {
    const res = await request.get(`/api/v1/job/${jobId}`, { headers })
    expect(res.ok(), `job get failed: ${res.status()} ${await res.text()}`).toBeTruthy()
    const job = await res.json()
    return job.status as string
  }, { timeout: 30_000 }).toBe(expected)
}

async function getJobSnapshot(request: APIRequestContext, jobId: string, headers: Record<string, string>) {
  const res = await request.get(`/api/v1/job/${jobId}`, { headers })
  expect(res.ok(), `job get failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  return await res.json()
}

type E2EJobSummary = {
  id: string
  title: string
  updatedAt: number
  pinnedAt?: number
}

async function getJobSummaryFromList(request: APIRequestContext, workspaceId: string, jobId: string, headers: Record<string, string>) {
  const res = await request.get(`/api/v1/job/list?workspaceId=${encodeURIComponent(workspaceId)}&limit=100`, { headers })
  expect(res.ok(), `job list failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const list = await res.json()
  const summary = (list.jobs as E2EJobSummary[]).find((j) => j.id === jobId)
  expect(summary, `job ${jobId} missing from workspace ${workspaceId} list`).toBeTruthy()
  return summary!
}

function waitForTimestampTick() {
  return new Promise((resolve) => setTimeout(resolve, 50))
}

test('boots isolated backend and frontend with auth token', async ({ page }) => {
  await openAppWithAuth(page)
  await expectHomeReady(page)
})

test('auth gate asks for a token when browser storage is empty', async ({ page }) => {
  await page.goto('/')

  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'needToken')
  await expect(page.getByTestId('auth-gate-token-input')).toBeVisible()
  await expect(page.getByTestId('auth-gate-submit-button')).toBeDisabled()

  await page.getByTestId('auth-gate-token-input').fill(e2eAuthToken)
  await expect(page.getByTestId('auth-gate-submit-button')).toBeEnabled()
  await page.getByTestId('auth-gate-submit-button').click()

  await expectHomeReady(page)
  await expect.poll(async () => page.evaluate(() => localStorage.getItem('quartet.x_auth_token'))).toBe(e2eAuthToken)
})

test('auth gate rejects a wrong token and recovers after the correct token is entered', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('quartet.x_auth_token', 'wrong-e2e-token')
    localStorage.setItem('quartet-language', 'en')
  })

  await page.goto('/')

  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'invalidToken')
  await expect(page.getByTestId('auth-gate-token-input')).toBeVisible()

  await page.getByTestId('auth-gate-token-input').fill(e2eAuthToken)
  await page.getByTestId('auth-gate-submit-button').click()

  await expectHomeReady(page)
})

test('auth gate shows probe failure and retry can recover in a real browser', async ({ page }) => {
  // React StrictMode may run the mount effect twice in development. Fail the
  // first two health probes so the gate remains on the probe-failed branch
  // until the user-visible Retry action is clicked.
  let remainingFailedHealthProbes = 2
  await page.route('**/api/v1/health', async (route) => {
    if (remainingFailedHealthProbes > 0) {
      remainingFailedHealthProbes -= 1
      await route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'e2e injected health probe failure' }),
      })
      return
    }
    await route.fallback()
  })

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)

  await page.goto('/')

  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'probeFailed')
  await expect(page.getByTestId('auth-gate-retry-button')).toBeVisible()

  await page.getByTestId('auth-gate-retry-button').click()

  await expectHomeReady(page)
})

test('switches language from settings in real browser', async ({ page }) => {
  await openAppWithAuth(page)

  await page.getByTestId('settings-open-button').click()
  await expect(page.getByTestId('settings-modal')).toBeVisible()
  await expect(page.getByTestId('settings-content')).toHaveAttribute('data-active-tab', 'general')
  await expect(page.getByTestId('settings-language-select')).toHaveValue('en')

  await page.getByTestId('settings-language-select').selectOption('zh')

  await expect(page.getByTestId('settings-modal')).toContainText('设置')
  await expect(page.getByTestId('settings-content')).toContainText('用户配置')
  await expect(page.getByTestId('settings-language-select')).toHaveValue('zh')
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh')
  await expect.poll(async () => page.evaluate(() => localStorage.getItem('quartet-language'))).toBe('zh')
})

test('keeps composing text and does not send when Enter is pressed during IME composition', async ({ page }) => {
  let createJobRequests = 0
  await page.route('**/api/v1/job/create', async (route) => {
    createJobRequests += 1
    await route.fulfill({
      status: 500,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'chat input IME composition test should not create a job' }),
    })
  })

  await openAppWithAuth(page)

  const input = page.getByRole('textbox', { name: /ask anything/i })
  await input.fill('拼')
  await input.focus()
  await input.dispatchEvent('compositionstart', { data: '拼' })
  await input.dispatchEvent('compositionupdate', { data: '拼' })
  await input.dispatchEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 229, which: 229, isComposing: true })

  await expect(input).toHaveValue('拼')
  await expect.poll(() => createJobRequests).toBe(0)

  await input.dispatchEvent('compositionend', { data: '拼' })
  await page.keyboard.press('Enter')
  await expect.poll(() => createJobRequests).toBe(1)
})

test('home job history lists real jobs and navigates into a selected job', async ({ page, request }) => {
  // Create real jobs through the business API; ordering is by creation time.
  const first = await createInteractiveJob(request, 'ws-1', 'E2E Real Job One')
  const second = await createInteractiveJob(request, 'ws-1', 'E2E Real Job Two')

  await openAppWithAuth(page, '/?workspaceId=ws-1')
  await expect(page.getByTestId('home-job-history')).toBeVisible()

  for (const job of [first, second]) {
    const item = page.locator(`[data-testid="home-job-history-row"][data-job-id="${job.jobId}"]`)
    await expect(item).toBeVisible()
  }

  await page.locator(`[data-testid="home-job-history-row"][data-job-id="${second.jobId}"]`).click()
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-id', second.jobId)
  await expect(page.getByTestId('job-chat-header')).toContainText('E2E Real Job Two')
})

test('startup load backfills legacy job FirstModelID into job list summaries', async ({ request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }

  const detail = await getJobSnapshot(request, e2eLegacyFirstModelJobID, headers)
  expect(detail.firstModelId).toBe(e2eLegacyFirstModelID)

  const listRes = await request.get('/api/v1/job/list?workspaceId=ws-1&limit=100', { headers })
  expect(listRes.ok(), `job list failed: ${listRes.status()} ${await listRes.text()}`).toBeTruthy()
  const list = await listRes.json()
  const summary = (list.jobs as Array<{ id: string; modelId?: string; sessionCount?: number }>)
    .find((job) => job.id === e2eLegacyFirstModelJobID)
  expect(summary).toBeTruthy()
  expect(summary?.modelId).toBe(e2eLegacyFirstModelID)
  expect(summary?.sessionCount).toBe(2)
})

test('startup load reconciles interrupted running jobs and persists the repair', async ({ request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }

  const detail = await getJobSnapshot(request, e2eInterruptedRunningJobID, headers)
  expect(detail.status).toBe('failed')
  expect(detail.progress?.lastError).toBe('interrupted: process restarted while running')
  expect(detail.progress).toBeTruthy()

  const { localMemory } = await getE2ERunInfo()
  const raw = await fs.readFile(
    path.join(localMemory, 'quartet', 'data', 'workspaces', 'ws-1', 'jobs', e2eInterruptedRunningJobID, '.meta', 'job.json'),
    'utf8',
  )
  const persisted = JSON.parse(raw)
  expect(persisted.status).toBe('failed')
  expect(persisted.progress?.lastError).toBe('interrupted: process restarted while running')
})

test('startup load preserves persistence warnings without promoting them to LastError', async ({ page, request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const expectedWarning = 'persist failed after iteration_started: injected e2e disk warning'

  const detail = await getJobSnapshot(request, e2ePersistWarningJobID, headers)
  expect(detail.status).toBe('completed')
  expect(detail.progress?.lastError || '').toBe('')
  expect(detail.progress?.persistWarnings).toEqual([expectedWarning])

  await openAppWithAuth(page, `/?workspaceId=ws-1&jobId=${encodeURIComponent(e2ePersistWarningJobID)}`)

  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-id', e2ePersistWarningJobID)
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-mode', 'loop')
  await expect(page.getByTestId('loop-progress')).toBeVisible()
  await expect(page.getByTestId('loop-progress-error')).toHaveCount(0)

  const warningBox = page.getByTestId('loop-progress-persist-warning')
  await expect(warningBox).toBeVisible()
  await expect(warningBox).toContainText('Persistence warnings')
  await expect(warningBox).toContainText(expectedWarning)
})

test('home job history rename persists through the real API', async ({ page, request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const job = await createInteractiveJob(request, 'ws-1', 'E2E Rename Source')

  await openAppWithAuth(page, '/?workspaceId=ws-1')
  const item = page.locator(`[data-testid="home-job-history-row"][data-job-id="${job.jobId}"]`)
  await expect(item).toBeVisible()
  await item.click()
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-id', job.jobId)

  await page.getByTestId('job-title-edit-button').click()
  const renameInput = page.getByTestId('job-title-input')
  await expect(renameInput).toBeVisible()
  const renamedTitle = 'E2E Renamed Title'
  await renameInput.fill(renamedTitle)
  await renameInput.press('Enter')
  await expect(page.getByTestId('job-chat-header')).toContainText(renamedTitle)

  const listRes = await request.get('/api/v1/job/list?workspaceId=ws-1&limit=10', { headers })
  expect(listRes.ok()).toBeTruthy()
  const list = await listRes.json()
  expect(list.jobs.find((j: { id: string }) => j.id === job.jobId)?.title).toBe(renamedTitle)

  // Empty rename keeps the prior title.
  await page.getByTestId('job-title-edit-button').click()
  await expect(renameInput).toBeVisible()
  await renameInput.fill('   ')
  await renameInput.press('Enter')
  await expect(page.getByTestId('job-title-error')).toContainText('Title cannot be empty')
  await page.getByTestId('job-title-cancel-button').click()
  await expect(page.getByTestId('job-chat-header')).toContainText(renamedTitle)
})

test('job list ETag changes after list-affecting mutations', async ({ request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const job = await createInteractiveJob(request, 'ws-1', 'E2E List Version Source')

  const firstList = await request.get('/api/v1/job/list?workspaceId=ws-1&limit=25', { headers })
  expect(firstList.ok(), `initial job list failed: ${firstList.status()} ${await firstList.text()}`).toBeTruthy()
  const firstETag = firstList.headers()['etag']
  expect(firstETag).toBeTruthy()
  const firstBody = await firstList.json()
  const firstVersion = firstBody.version as number
  expect(firstBody.jobs.find((j: { id: string }) => j.id === job.jobId)?.title).toBe('E2E List Version Source')

  const cachedList = await request.get('/api/v1/job/list?workspaceId=ws-1&limit=25', {
    headers: { ...headers, 'If-None-Match': firstETag },
  })
  expect(cachedList.status()).toBe(304)

  const renamedTitle = 'E2E List Version Renamed'
  const renameRes = await request.put(`/api/v1/job/${job.jobId}/title`, { headers, data: { title: renamedTitle } })
  expect(renameRes.ok(), `rename failed: ${renameRes.status()} ${await renameRes.text()}`).toBeTruthy()

  const staleCachedList = await request.get('/api/v1/job/list?workspaceId=ws-1&limit=25', {
    headers: { ...headers, 'If-None-Match': firstETag },
  })
  expect(staleCachedList.ok(), `stale ETag should revalidate: ${staleCachedList.status()} ${await staleCachedList.text()}`).toBeTruthy()
  const secondETag = staleCachedList.headers()['etag']
  expect(secondETag).toBeTruthy()
  expect(secondETag).not.toBe(firstETag)
  const secondBody = await staleCachedList.json()
  expect(secondBody.version).toBeGreaterThan(firstVersion)
  expect(secondBody.jobs.find((j: { id: string }) => j.id === job.jobId)?.title).toBe(renamedTitle)

  const freshCachedList = await request.get('/api/v1/job/list?workspaceId=ws-1&limit=25', {
    headers: { ...headers, 'If-None-Match': secondETag },
  })
  expect(freshCachedList.status()).toBe(304)
})

test('pinning a job updates UpdatedAt and invalidates the real job list cache', async ({ request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const { localMemory } = await getE2ERunInfo()
  const workdir = path.join(localMemory, `e2e-pin-api-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const workspace = await createWorkspace(request, 'E2E Pin API Workspace', workdir)
  const job = await createInteractiveJob(request, workspace.workspaceId, 'E2E Pin API Target')
  const listURL = `/api/v1/job/list?workspaceId=${encodeURIComponent(workspace.workspaceId)}&limit=100`

  const firstList = await request.get(listURL, { headers })
  expect(firstList.ok(), `initial job list failed: ${firstList.status()} ${await firstList.text()}`).toBeTruthy()
  const firstETag = firstList.headers()['etag']
  expect(firstETag).toBeTruthy()
  const firstBody = await firstList.json()
  const before = (firstBody.jobs as E2EJobSummary[]).find((j) => j.id === job.jobId)
  expect(before).toBeTruthy()
  expect(before?.pinnedAt || 0).toBe(0)

  await waitForTimestampTick()
  const pinRes = await request.put(`/api/v1/job/${job.jobId}/pin`, { headers, data: { pinned: true } })
  expect(pinRes.ok(), `pin failed: ${pinRes.status()} ${await pinRes.text()}`).toBeTruthy()
  const pinBody = await pinRes.json() as { pinned: boolean; pinnedAt: number; updatedAt: number }
  expect(pinBody.pinned).toBe(true)
  expect(pinBody.pinnedAt).toBeGreaterThan(0)
  expect(pinBody.updatedAt).toBeGreaterThan(before!.updatedAt)

  const staleCachedList = await request.get(listURL, { headers: { ...headers, 'If-None-Match': firstETag } })
  expect(staleCachedList.ok(), `pin should invalidate list ETag: ${staleCachedList.status()} ${await staleCachedList.text()}`).toBeTruthy()
  expect(staleCachedList.headers()['etag']).not.toBe(firstETag)

  const afterPin = await getJobSummaryFromList(request, workspace.workspaceId, job.jobId, headers)
  expect(afterPin.pinnedAt).toBe(pinBody.pinnedAt)
  expect(afterPin.updatedAt).toBe(pinBody.updatedAt)

  await waitForTimestampTick()
  const unpinRes = await request.put(`/api/v1/job/${job.jobId}/pin`, { headers, data: { pinned: false } })
  expect(unpinRes.ok(), `unpin failed: ${unpinRes.status()} ${await unpinRes.text()}`).toBeTruthy()
  const unpinBody = await unpinRes.json() as { pinned: boolean; pinnedAt: number; updatedAt: number }
  expect(unpinBody.pinned).toBe(false)
  expect(unpinBody.pinnedAt).toBe(0)
  expect(unpinBody.updatedAt).toBeGreaterThan(afterPin.updatedAt)

  const afterUnpin = await getJobSummaryFromList(request, workspace.workspaceId, job.jobId, headers)
  expect(afterUnpin.pinnedAt || 0).toBe(0)
  expect(afterUnpin.updatedAt).toBe(unpinBody.updatedAt)
})

test('home job history uses pin response UpdatedAt when a job is unpinned', async ({ page, request }) => {
  const { localMemory } = await getE2ERunInfo()
  const workdir = path.join(localMemory, `e2e-pin-ui-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const workspace = await createWorkspace(request, 'E2E Pin UI Workspace', workdir)
  const older = await createInteractiveJob(request, workspace.workspaceId, 'E2E Pin UI Older')
  await waitForTimestampTick()
  const newer = await createInteractiveJob(request, workspace.workspaceId, 'E2E Pin UI Newer')

  const allowedJobIds = new Set([older.jobId, newer.jobId])
  await page.route('**/api/v1/job/list**', async (route) => {
    const response = await route.fetch()
    if (!response.ok()) {
      await route.fulfill({
        status: response.status(),
        headers: response.headers(),
        body: await response.text(),
      })
      return
    }
    const data = await response.json()
    await route.fulfill({
      status: response.status(),
      contentType: 'application/json',
      body: JSON.stringify({
        ...data,
        jobs: (data.jobs as E2EJobSummary[]).filter((j) => allowedJobIds.has(j.id)),
        nextCursor: '',
        hasMore: false,
        dailyStats: {},
      }),
    })
  })

  await openAppWithAuth(page)
  await expect(page.getByTestId('home-job-history')).toBeVisible()

  const rowIds = async () => await page.locator('[data-testid="home-job-history-row"]').evaluateAll((rows) =>
    rows.map((row) => row.getAttribute('data-job-id')).filter(Boolean),
  )
  await expect.poll(rowIds).toEqual([newer.jobId, older.jobId])

  const olderRow = page.locator(`[data-testid="home-job-history-row"][data-job-id="${older.jobId}"]`)
  await olderRow.getByTestId('home-job-history-row-pin').click()
  await expect(olderRow).toHaveAttribute('data-pinned', 'true')
  await expect.poll(rowIds).toEqual([older.jobId, newer.jobId])

  await waitForTimestampTick()
  await olderRow.getByTestId('home-job-history-row-pin').click()
  await expect(olderRow).toHaveAttribute('data-pinned', 'false')
  await expect.poll(rowIds).toEqual([older.jobId, newer.jobId])

  const afterUnpin = await getJobSummaryFromList(request, workspace.workspaceId, older.jobId, older.headers)
  const untouchedNewer = await getJobSummaryFromList(request, workspace.workspaceId, newer.jobId, newer.headers)
  expect(afterUnpin.updatedAt).toBeGreaterThan(untouchedNewer.updatedAt)
})

test('home job history delete requires confirmation and persists removal', async ({ page, request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const keep = await createInteractiveJob(request, 'ws-1', 'E2E Delete Keep')
  const remove = await createInteractiveJob(request, 'ws-1', 'E2E Delete Target')

  await openAppWithAuth(page, '/?workspaceId=ws-1')
  await expect(page.getByTestId('home-job-history')).toBeVisible()

  const deleteItem = page.locator(`[data-testid="home-job-history-row"][data-job-id="${remove.jobId}"]`)
  await expect(deleteItem).toBeVisible()
  await deleteItem.getByTestId('home-job-history-row-delete').click()
  await expect(page.getByTestId('delete-confirm-dialog')).toBeVisible()
  await expect(page.getByTestId('delete-confirm-dialog')).toContainText('E2E Delete Target')

  // Cancel leaves it in place.
  await page.getByTestId('delete-confirm-cancel').click()
  await expect(page.getByTestId('delete-confirm-dialog')).toHaveCount(0)
  await expect(deleteItem).toBeVisible()

  // Confirm removes it from the list and the backend.
  await deleteItem.getByTestId('home-job-history-row-delete').click()
  await page.getByTestId('delete-confirm-ok').click()
  await expect(deleteItem).toHaveCount(0)
  await expect(page.locator(`[data-testid="home-job-history-row"][data-job-id="${keep.jobId}"]`)).toBeVisible()

  const listRes = await request.get('/api/v1/job/list?workspaceId=ws-1&limit=10', { headers })
  expect(listRes.ok()).toBeTruthy()
  const list = await listRes.json()
  expect(list.jobs.find((j: { id: string }) => j.id === remove.jobId)).toBeUndefined()

  const { localMemory } = await getE2ERunInfo()
  await expect.poll(async () => {
    return await pathExists(path.join(localMemory, 'quartet', 'data', 'workspaces', 'ws-1', 'jobs', remove.jobId))
  }).toBe(false)
})

test('streams a real assistant reply through the chat UI', async ({ page }) => {
  await openAppWithAuth(page, '/?workspaceId=ws-1')

  const input = page.getByRole('textbox', { name: /ask anything/i })
  await input.fill('Reply with a short greeting.')
  await page.keyboard.press('Enter')

  // A new job is created and the chat view opens. We assert structural signals
  // only — the live model's wording is non-deterministic.
  await expect(page.getByTestId('job-chat')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-testid="message-item"][data-message-role="user"]').first())
    .toBeVisible({ timeout: 30_000 })
  await expect(page.locator('[data-testid="message-item"][data-message-role="assistant"]').first())
    .toBeVisible({ timeout: 120_000 })

  // The run reaches a non-running state (idle) once the model finishes.
  await expect(page.getByTestId('chat-send-button')).toBeVisible({ timeout: 120_000 })
})

const scheduleHeaders = { 'X-AGENT-AUTH': e2eAuthToken }

function validShellGraphConfig(marker: string, workspaceId: string, workdir: string): E2EGraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'shell', type: 'shell', title: 'Echo', config: { script: `echo ${marker}` }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [
      { id: 'edge-start-shell', sourceNodeId: 'start', targetNodeId: 'shell' },
      { id: 'edge-shell-end', sourceNodeId: 'shell', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 1 },
    workspaceId,
    workdir,
  }
}

async function waitForScheduleStatus(request: APIRequestContext, scheduleId: string, expected: string) {
  return await expect.poll(async () => {
    const res = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: scheduleHeaders })
    expect(res.ok(), `schedule get failed: ${res.status()} ${await res.text()}`).toBeTruthy()
    const schedule = await res.json()
    return schedule.lastStatus as string
  }, { timeout: 30_000 }).toBe(expected)
}

test('scheduled graph workflow triggers, releases concurrency, and records missing workflow failures', async ({ request }) => {
  const { localMemory } = await getE2ERunInfo()
  const workdir = path.join(localMemory, `e2e-graph-schedule-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const workspace = await createWorkspace(request, 'E2E Graph Schedule Workspace', workdir)
  const marker = `graph-schedule-${Date.now()}`
  const workflowNamePrefix = `E2E Graph Schedule Workflow ${Date.now()}`

  const createWorkflowRes = await request.post('/api/v1/graph/workflow', {
    headers: scheduleHeaders,
    data: {
      name: `${workflowNamePrefix} A`,
      workspaceId: workspace.workspaceId,
      config: validShellGraphConfig(marker, workspace.workspaceId, workdir),
    },
  })
  expect(createWorkflowRes.ok(), `graph workflow create failed: ${createWorkflowRes.status()} ${await createWorkflowRes.text()}`).toBeTruthy()
  const workflow = (await createWorkflowRes.json()).workflow as { id: string; updatedAt: string }

  const createScheduleRes = await request.post('/api/v1/schedule/create', {
    headers: scheduleHeaders,
    data: {
      name: `E2E Graph Schedule ${Date.now()}`,
      cronExpr: '0 0 1 1 *',
      graphWorkflowId: workflow.id,
      workspaceId: workspace.workspaceId,
      enabled: false,
      maxConcurrent: 1,
    },
  })
  expect(createScheduleRes.ok(), `schedule create failed: ${createScheduleRes.status()} ${await createScheduleRes.text()}`).toBeTruthy()
  const createdSchedule = (await createScheduleRes.json()).schedule as { id: string; graphWorkflowId: string }
  expect(createdSchedule.graphWorkflowId).toBe(workflow.id)
  const scheduleId = createdSchedule.id

  const firstRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: scheduleHeaders })
  expect(firstRunRes.ok(), `first schedule run failed: ${firstRunRes.status()} ${await firstRunRes.text()}`).toBeTruthy()
  const firstJobID = (await firstRunRes.json()).jobId as string
  expect(firstJobID).toMatch(/^job-/)

  await waitForJobStatus(request, firstJobID, scheduleHeaders, 'completed')
  await waitForScheduleStatus(request, scheduleId, 'completed')

  const firstJob = await getJobSnapshot(request, firstJobID, scheduleHeaders)
  expect(firstJob.mode).toBe('graph')
  expect(firstJob.scheduleId).toBe(scheduleId)
  expect(firstJob.graphRunId).toMatch(/^grun-/)

  const afterFirstRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: scheduleHeaders })
  const afterFirst = await afterFirstRes.json()
  expect(afterFirst.lastRunJobID).toBe(firstJobID)
  expect(afterFirst.lastTriggerError || '').toBe('')
  expect(afterFirst.runCount).toBeGreaterThanOrEqual(1)

  const secondRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: scheduleHeaders })
  expect(secondRunRes.ok(), `second schedule run should prove concurrency was released: ${secondRunRes.status()} ${await secondRunRes.text()}`).toBeTruthy()
  const secondJobID = (await secondRunRes.json()).jobId as string
  await waitForJobStatus(request, secondJobID, scheduleHeaders, 'completed')
  await waitForScheduleStatus(request, scheduleId, 'completed')
  const afterSecondRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: scheduleHeaders })
  expect(afterSecondRes.ok(), `schedule get failed: ${afterSecondRes.status()} ${await afterSecondRes.text()}`).toBeTruthy()
  const afterSecond = await afterSecondRes.json()

  const secondJob = await getJobSnapshot(request, secondJobID, scheduleHeaders)
  expect(secondJob.mode).toBe('graph')
  expect(secondJob.graphRunId).toMatch(/^grun-/)
  expect(secondJob.graphRunId).not.toBe(firstJob.graphRunId)

  const deleteWorkflowRes = await request.delete(`/api/v1/graph/workflow/${workflow.id}`, {
    headers: scheduleHeaders,
    data: { updatedAt: workflow.updatedAt },
  })
  expect(deleteWorkflowRes.ok(), `workflow delete failed: ${deleteWorkflowRes.status()} ${await deleteWorkflowRes.text()}`).toBeTruthy()

  const failedRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: scheduleHeaders })
  expect(failedRunRes.status(), `expected missing workflow trigger to fail, got ${failedRunRes.status()}: ${await failedRunRes.text()}`).toBe(409)
  const failureText = await failedRunRes.text()
  expect(failureText).toContain(workflow.id)

  const failedScheduleRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: scheduleHeaders })
  expect(failedScheduleRes.ok(), `schedule get failed: ${failedScheduleRes.status()} ${await failedScheduleRes.text()}`).toBeTruthy()
  const failedSchedule = await failedScheduleRes.json()
  expect(failedSchedule.lastStatus).toBe('failed')
  expect(failedSchedule.lastRunJobID).toBe(secondJobID)
  expect(failedSchedule.lastTriggerError).toContain(workflow.id)
  expect(failedSchedule.lastTriggerError).toContain('workflow')
  expect(failedSchedule.runCount).toBe(afterSecond.runCount + 1)

  const retryFailureRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: scheduleHeaders })
  expect(retryFailureRes.status(), `expected repeated missing workflow trigger to fail without a leaked concurrency slot, got ${retryFailureRes.status()}: ${await retryFailureRes.text()}`).toBe(409)
  expect(await retryFailureRes.text()).toContain(workflow.id)

  const replacementWorkflowRes = await request.post('/api/v1/graph/workflow', {
    headers: scheduleHeaders,
    data: {
      name: `${workflowNamePrefix} B`,
      workspaceId: workspace.workspaceId,
      config: validShellGraphConfig(`${marker}-replacement`, workspace.workspaceId, workdir),
    },
  })
  expect(replacementWorkflowRes.ok(), `replacement graph workflow create failed: ${replacementWorkflowRes.status()} ${await replacementWorkflowRes.text()}`).toBeTruthy()
  const replacementWorkflow = (await replacementWorkflowRes.json()).workflow as { id: string }

  const updateScheduleRes = await request.put(`/api/v1/schedule/${scheduleId}`, {
    headers: scheduleHeaders,
    data: {
      graphWorkflowId: replacementWorkflow.id,
    },
  })
  expect(updateScheduleRes.ok(), `schedule update failed: ${updateScheduleRes.status()} ${await updateScheduleRes.text()}`).toBeTruthy()

  const recoveryRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: scheduleHeaders })
  expect(recoveryRunRes.ok(), `schedule run after replacing workflow failed: ${recoveryRunRes.status()} ${await recoveryRunRes.text()}`).toBeTruthy()
  const recoveryJobID = (await recoveryRunRes.json()).jobId as string
  await waitForJobStatus(request, recoveryJobID, scheduleHeaders, 'completed')
  await waitForScheduleStatus(request, scheduleId, 'completed')

  const recoveredScheduleRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: scheduleHeaders })
  expect(recoveredScheduleRes.ok(), `schedule get failed: ${recoveredScheduleRes.status()} ${await recoveredScheduleRes.text()}`).toBeTruthy()
  const recoveredSchedule = await recoveredScheduleRes.json()
  expect(recoveredSchedule.lastRunJobID).toBe(recoveryJobID)
  expect(recoveredSchedule.lastTriggerError || '').toBe('')
  expect(recoveredSchedule.graphWorkflowId).toBe(replacementWorkflow.id)
})
