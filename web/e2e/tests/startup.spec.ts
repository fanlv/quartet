import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
import { e2eAuthToken } from '../fixtures/e2e-environment'

// This suite drives the REAL model flow. There is no QUARTET_E2E mode, no
// replay model, and no /api/v1/e2e/* control or fixture API. Test data is
// created through the same business APIs a user hits, and assertions check
// structural / state signals (message nodes appear, stream reaches a terminal
// state, list ordering, rename/delete persistence) rather than fixed model
// text — a live model's wording is not deterministic.
//
// Fault links that a real model cannot trigger (HTTP send failure, SSE auth
// rejection, resume 410 recovery, event-buffer GC) are covered at the
// component layer (web/src/utils/sse-client.test.ts) and in Go unit tests
// (services/job/event_buffer_test.go), not here.

const MODEL_ID = process.env.QUARTET_E2E_MODEL_ID || '1000001'

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

// createInteractiveJob creates a real interactive job through the public API,
// returning its id. No scenario header — the job runs against the live model.
async function createInteractiveJob(request: APIRequestContext, workspaceId = 'ws-1', title?: string) {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const res = await request.post('/api/v1/job/create', {
    headers,
    data: { agentType: 'eino', modelId: MODEL_ID, workspaceId, mode: 'interactive' },
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
