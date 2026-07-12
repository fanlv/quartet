import fs from 'node:fs/promises'
import path from 'node:path'

import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
import {
  e2eAuthToken,
  e2eBackendURL,
  e2eCleanupWorkspaceID,
  e2eFreshShellTempName,
  e2eInterruptedRunningJobID,
  e2eLegacyFirstModelID,
  e2eLegacyFirstModelJobID,
  e2eLegacyRoundsJobID,
  e2ePersistWarningJobID,
  e2eShellAWSSecretAccessKey,
  e2eShellOpenAIAPIKey,
  e2eShellStaleControl,
  e2eStaleControlTempName,
  e2eStaleShellTempName,
} from '../fixtures/e2e-environment'

// This suite drives REAL agent links. There is no QUARTET_E2E mode, no replay
// model, and no /api/v1/e2e/* control or fixture API. Test data is created
// through the same business APIs a user hits, and assertions check structural
// / state signals (message nodes appear, stream reaches a terminal state, list
// ordering, rename/delete persistence) rather than fixed model text — a live
// agent's wording is not deterministic.
//
// The primary chat-link coverage runs against an ACP agent discovered at
// runtime from the backend's own probe list (GET /api/v1/agent/list). The user
// primarily uses the ACP path, and ACP needs no models.json — the subprocess
// carries its own login state in $HOME. If no ACP agent is installed the chat
// spec skips itself rather than failing.
//
// Fault links that a real agent cannot trigger (HTTP send failure, SSE auth
// rejection, resume 410 recovery, event-buffer GC) are covered at the
// component layer (web/src/utils/sse-client.test.ts) and in Go unit tests
// (services/job/event_buffer_test.go), not here.

const MODEL_ID = process.env.QUARTET_E2E_MODEL_ID || '1000001'

type E2ERunInfo = {
  localMemory: string
}

type E2EFlowNode = {
  id: string
  type: 'step' | 'group'
  message?: string
  repeatCount?: number
  roundMode?: 'beforeRound' | 'eachRepeat' | 'none'
  roundType?: 'prompt' | 'shell' | 'evaluator'
  iterationCount?: number
  children?: E2EFlowNode[]
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

function shellSingleQuote(value: string) {
  return `'${value.replace(/'/g, `'"'"'`)}'`
}

async function readSSEUntil(url: string, headers: Record<string, string>, predicate: (chunk: string) => boolean, timeoutMs: number, onOpen?: () => void) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  let accumulated = ''
  try {
    const response = await fetch(url, { headers, signal: controller.signal })
    if (!response.ok) {
      throw new Error(`SSE connect failed: ${response.status} ${await response.text()}`)
    }
    expect(response.body).toBeTruthy()
    onOpen?.()
    const reader = response.body!.getReader()
    const decoder = new TextDecoder()
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      accumulated += decoder.decode(value, { stream: true })
      if (predicate(accumulated)) {
        controller.abort()
        break
      }
    }
    return accumulated
  } finally {
    clearTimeout(timer)
    controller.abort()
  }
}

function parseSSEMessageEvents(text: string): Array<Record<string, unknown>> {
  const events: Array<Record<string, unknown>> = []
  for (const block of text.split(/\r?\n\r?\n/)) {
    const dataLines = block
      .split(/\r?\n/)
      .map((line) => line.trimStart())
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trim())
    if (dataLines.length === 0) continue
    const data = dataLines.join('\n')
    if (!data || data === '[DONE]') continue
    try {
      const parsed = JSON.parse(data)
      if (parsed && typeof parsed === 'object') events.push(parsed as Record<string, unknown>)
    } catch {
      // Ignore keep-alive / diagnostic frames that are not JSON event payloads.
    }
  }
  if (events.length === 0) {
    for (const line of text.split(/\r?\n/)) {
      const jsonStart = line.indexOf('{')
      if (jsonStart < 0) continue
      try {
        const parsed = JSON.parse(line.slice(jsonStart).trim())
        if (parsed && typeof parsed === 'object') events.push(parsed as Record<string, unknown>)
      } catch {
        // Ignore non-event lines.
      }
    }
  }
  return events
}

function eventTimestamp(event: Record<string, unknown>) {
  expect(typeof event.timestamp).toBe('number')
  return event.timestamp as number
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

async function createLoopJob(request: APIRequestContext, workspaceId = 'ws-1', title?: string) {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const res = await request.post('/api/v1/job/create', {
    headers,
    data: {
      agentType: 'eino',
      modelId: MODEL_ID,
      workspaceId,
      mode: 'loop',
      loopConfig: {
        flow: [
          {
            id: 'e2e-persist-warning-step',
            type: 'step',
            message: 'E2E persisted warning snapshot step',
            repeatCount: 1,
            roundMode: 'beforeRound',
            roundType: 'prompt',
          },
        ],
      },
    },
  })
  expect(res.ok(), `loop job create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const created = await res.json()
  expect(created.jobId).toMatch(/^job-/)
  if (title) {
    const titleRes = await request.put(`/api/v1/job/${created.jobId}/title`, { headers, data: { title } })
    expect(titleRes.ok()).toBeTruthy()
  }
  return { jobId: created.jobId as string, headers }
}

async function createLoopJobWithFlow(request: APIRequestContext, flow: E2EFlowNode[], workspaceId = 'ws-1', title?: string) {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const res = await request.post('/api/v1/job/create', {
    headers,
    data: {
      agentType: 'eino',
      modelId: MODEL_ID,
      workspaceId,
      mode: 'loop',
      loopConfig: { flow },
    },
  })
  expect(res.ok(), `loop job create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
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

async function createShellLoopJob(request: APIRequestContext, script: string, workspaceId = 'ws-1', title?: string) {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const res = await request.post('/api/v1/job/create', {
    headers,
    data: {
      agentType: 'eino',
      modelId: MODEL_ID,
      workspaceId,
      mode: 'loop',
      loopConfig: {
        flow: [
          {
            id: 'e2e-shell-step',
            type: 'step',
            message: script,
            repeatCount: 1,
            roundMode: 'beforeRound',
            roundType: 'shell',
          },
        ],
      },
    },
  })
  expect(res.ok(), `shell loop job create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const created = await res.json()
  expect(created.jobId).toMatch(/^job-/)
  if (title) {
    const titleRes = await request.put(`/api/v1/job/${created.jobId}/title`, { headers, data: { title } })
    expect(titleRes.ok()).toBeTruthy()
  }
  return { jobId: created.jobId as string, headers }
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

async function getSessionTranscript(request: APIRequestContext, sessionIds: string[] | undefined, headers: Record<string, string>) {
  const chunks: string[] = []
  for (const sessionId of sessionIds || []) {
    const messagesRes = await request.get(`/api/v1/sessions/${sessionId}/messages`, { headers })
    expect(messagesRes.ok(), `session messages failed: ${messagesRes.status()} ${await messagesRes.text()}`).toBeTruthy()
    const messages = await messagesRes.json()
    chunks.push(...(messages.messages as Array<{ role?: string; content?: string }>).map((message) => message.content || ''))
  }
  return chunks.join('\n')
}

async function getAssistantTranscript(request: APIRequestContext, sessionIds: string[] | undefined, headers: Record<string, string>) {
  const chunks: string[] = []
  for (const sessionId of sessionIds || []) {
    const messagesRes = await request.get(`/api/v1/sessions/${sessionId}/messages`, { headers })
    expect(messagesRes.ok(), `session messages failed: ${messagesRes.status()} ${await messagesRes.text()}`).toBeTruthy()
    const messages = await messagesRes.json()
    const assistantMessages = (messages.messages as Array<{ role?: string; content?: string }>)
      .filter((message) => message.role === 'assistant')
      .map((message) => message.content || '')
    chunks.push(...assistantMessages)
  }
  return chunks.join('\n')
}

function countOccurrences(text: string, needle: string) {
  return text.split(needle).length - 1
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

test('legacy rounds-only loop config starts by migrating to flow once', async ({ request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }

  const beforeStart = await getJobSnapshot(request, e2eLegacyRoundsJobID, headers)
  expect(beforeStart.status).toBe('pending')
  expect(beforeStart.loopConfig?.flow || []).toEqual([])
  expect(beforeStart.loopConfig?.rounds?.[0]?.message).toContain('legacy-rounds-migrated-e2e')

  const startRes = await request.post(`/api/v1/job/${e2eLegacyRoundsJobID}/start`, { headers })
  expect(startRes.ok(), `legacy rounds start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, e2eLegacyRoundsJobID, headers, 'completed')

  const completed = await getJobSnapshot(request, e2eLegacyRoundsJobID, headers)
  expect(completed.loopConfig?.flow?.length).toBe(1)
  expect(completed.loopConfig?.flow?.[0]?.type).toBe('group')
  expect(completed.loopConfig?.flow?.[0]?.children?.[0]?.roundType).toBe('shell')
  expect(completed.progress?.totalSteps).toBe(1)
  expect(completed.progress?.completedCount).toBe(1)
  expect(completed.progress?.failedCount || 0).toBe(0)
  expect(completed.progress?.results?.[0]?.content).toContain('legacy-rounds-migrated-e2e')

  const transcript = await getAssistantTranscript(request, completed.sessionIds, headers)
  expect(transcript).toContain('legacy-rounds-migrated-e2e')
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

test('startup cleanup removes stale shell temp files without blocking backend readiness', async ({ request }) => {
  const headers = { 'X-AGENT-AUTH': e2eAuthToken }
  const { localMemory } = await getE2ERunInfo()
  const workdir = path.join(localMemory, 'e2e-startup-cleanup-workdir')
  const staleShell = path.join(workdir, e2eStaleShellTempName)
  const staleControl = path.join(workdir, e2eStaleControlTempName)
  const freshShell = path.join(workdir, e2eFreshShellTempName)

  // The backend is already serving authenticated API traffic while cleanup runs
  // asynchronously in the background.
  const workspaceRes = await request.get(`/api/v1/workspace/${e2eCleanupWorkspaceID}`, { headers })
  expect(workspaceRes.ok(), `workspace get failed: ${workspaceRes.status()} ${await workspaceRes.text()}`).toBeTruthy()
  const workspace = await workspaceRes.json()
  expect(workspace.workdir).toBe(workdir)

  await expect.poll(async () => await pathExists(staleShell), { timeout: 10_000 }).toBe(false)
  await expect.poll(async () => await pathExists(staleControl), { timeout: 10_000 }).toBe(false)
  expect(await pathExists(freshShell)).toBe(true)
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

test('SSE stream wakes from a pending read and can be cancelled cleanly', async ({ request }) => {
  const script = ['echo "sse wake start"', 'sleep 0.2', 'echo "sse wake event"'].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E SSE Wake Cancellation')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()

  const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
  const streamPromise = readSSEUntil(
    `${e2eBackendURL}/api/v1/job/${job.jobId}/events`,
    { 'X-AGENT-AUTH': e2eAuthToken, Accept: 'text/event-stream', 'Last-Event-ID': String(snapshot.lastEventSeq || 0) },
    (text) => text.includes('sse wake event') || text.includes('JOB_COMPLETED'),
    15_000,
  )

  const chunks = await streamPromise
  expect(chunks).toMatch(/sse wake event|JOB_COMPLETED/)
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const detail = await getJobSnapshot(request, job.jobId, job.headers)
  expect(detail.id).toBe(job.jobId)
})

test('SSE RUN_ERROR carries a structured SHELL error code for shell failures', async ({ request }) => {
  const script = ['echo "structured-shell-error-e2e-before"', 'sleep 0.5', 'exit 7'].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Structured Shell Error Code')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  const runningSnapshot = await getJobSnapshot(request, job.jobId, job.headers)

  let resolveOpened!: () => void
  const opened = new Promise<void>((resolve) => {
    resolveOpened = resolve
  })
  const streamPromise = readSSEUntil(
    `${e2eBackendURL}/api/v1/job/${job.jobId}/events`,
    { 'X-AGENT-AUTH': e2eAuthToken, Accept: 'text/event-stream', 'Last-Event-ID': String(runningSnapshot.lastEventSeq || 0) },
    (text) => text.includes('"type":"RUN_ERROR"') && text.includes('"type":"JOB_FAILED"'),
    15_000,
    resolveOpened,
  )
  await opened

  const chunks = await streamPromise
  const events = parseSSEMessageEvents(chunks)
  const runError = events.find((event) => event.type === 'RUN_ERROR')
  if (!runError) {
    throw new Error(`RUN_ERROR event not found. Parsed events: ${JSON.stringify(events)}\nRaw SSE:\n${chunks}`)
  }
  expect(runError?.code).toBe('SHELL')
  expect(runError?.message).toContain('exit status 7')

  await waitForJobStatus(request, job.jobId, job.headers, 'failed')
  const failedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(failedSnapshot.progress?.failedCount).toBe(1)
  expect(failedSnapshot.progress?.results?.[0]?.error).toContain('exit status 7')
})

test('SSE terminal failure event timestamp matches the persisted job FinishedAt', async ({ request }) => {
  const script = ['echo "terminal-timestamp-failure-e2e-before"', 'sleep 0.2', 'exit 9'].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Terminal Failure Timestamp')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  const runningSnapshot = await getJobSnapshot(request, job.jobId, job.headers)

  const chunks = await readSSEUntil(
    `${e2eBackendURL}/api/v1/job/${job.jobId}/events`,
    { 'X-AGENT-AUTH': e2eAuthToken, Accept: 'text/event-stream', 'Last-Event-ID': String(runningSnapshot.lastEventSeq || 0) },
    (text) => text.includes('"type":"RUN_ERROR"') && text.includes('"type":"JOB_FAILED"'),
    15_000,
  )

  const events = parseSSEMessageEvents(chunks)
  const runError = events.find((event) => event.type === 'RUN_ERROR')
  const jobFailed = events.find((event) => event.type === 'JOB_FAILED')
  if (!runError || !jobFailed) {
    throw new Error(`Expected RUN_ERROR and JOB_FAILED events. Parsed events: ${JSON.stringify(events)}\nRaw SSE:\n${chunks}`)
  }

  await waitForJobStatus(request, job.jobId, job.headers, 'failed')
  const failedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(failedSnapshot.finishedAt).toBeGreaterThan(0)
  expect(eventTimestamp(jobFailed)).toBe(failedSnapshot.finishedAt)
  expect(eventTimestamp(runError)).toBeLessThanOrEqual(eventTimestamp(jobFailed))
  expect(failedSnapshot.progress?.failedCount).toBe(1)
  expect(failedSnapshot.progress?.results?.[0]?.error).toContain('exit status 9')
})

test('SSE terminal completion event timestamp matches the persisted job FinishedAt', async ({ request }) => {
  const script = ['echo "terminal-timestamp-complete-e2e-before"', 'sleep 1', 'echo "terminal-timestamp-complete-e2e-after"'].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Terminal Completion Timestamp')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await expect.poll(async () => {
    const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
    return snapshot.status as string
  }, { timeout: 10_000 }).toBe('running')
  const runningSnapshot = await getJobSnapshot(request, job.jobId, job.headers)

  const chunks = await readSSEUntil(
    `${e2eBackendURL}/api/v1/job/${job.jobId}/events`,
    { 'X-AGENT-AUTH': e2eAuthToken, Accept: 'text/event-stream', 'Last-Event-ID': String(runningSnapshot.lastEventSeq || 0) },
    (text) => text.includes('"type":"RUN_FINISHED"') && text.includes('"type":"ITERATION_COMPLETED"') && text.includes('"type":"JOB_COMPLETED"'),
    15_000,
  )

  const events = parseSSEMessageEvents(chunks)
  const runFinished = events.find((event) => event.type === 'RUN_FINISHED')
  const iterationCompleted = events.find((event) => event.type === 'ITERATION_COMPLETED')
  const jobCompleted = events.find((event) => event.type === 'JOB_COMPLETED')
  if (!runFinished || !iterationCompleted || !jobCompleted) {
    throw new Error(`Expected RUN_FINISHED, ITERATION_COMPLETED and JOB_COMPLETED events. Parsed events: ${JSON.stringify(events)}\nRaw SSE:\n${chunks}`)
  }

  await waitForJobStatus(request, job.jobId, job.headers, 'completed')
  const completedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(completedSnapshot.finishedAt).toBeGreaterThan(0)
  expect(eventTimestamp(jobCompleted)).toBe(completedSnapshot.finishedAt)
  expect(eventTimestamp(runFinished)).toBeLessThanOrEqual(eventTimestamp(jobCompleted))
  expect(completedSnapshot.progress?.completedCount).toBe(1)
  expect(completedSnapshot.progress?.failedCount || 0).toBe(0)
  expect(completedSnapshot.progress?.results?.[0]?.success).toBe(true)
  expect(completedSnapshot.progress?.results?.[0]?.content).toContain('terminal-timestamp-complete-e2e-after')
})

test('loop progress renders persistence warnings separately from last error', async ({ page, request }) => {
  const job = await createLoopJob(request, 'ws-1', 'E2E Persist Warning Loop')
  const persistWarnings = [
    'persist failed after record_iteration_result: injected e2e disk warning',
    'persist failed after attach_session: injected e2e follow-up warning',
  ]

  await page.route(`**/api/v1/job/${job.jobId}`, async (route) => {
    const response = await route.fetch()
    const snapshot = await response.json()
    await route.fulfill({
      status: response.status(),
      contentType: 'application/json',
      body: JSON.stringify({
        ...snapshot,
        status: 'failed',
        lastRunOutcome: 'failed',
        progress: {
          ...(snapshot.progress || {}),
          totalSteps: 1,
          completedCount: 0,
          failedCount: 1,
          currentPath: [0, 0],
          lastError: 'iteration failed before warning was recorded',
          persistWarnings,
        },
      }),
    })
  })

  await openAppWithAuth(page, `/?workspaceId=ws-1&jobId=${encodeURIComponent(job.jobId)}`)

  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-mode', 'loop')
  await expect(page.getByTestId('loop-progress')).toBeVisible()
  await expect(page.getByTestId('loop-progress-error')).toContainText('iteration failed before warning was recorded')

  const warningBox = page.getByTestId('loop-progress-persist-warning')
  await expect(warningBox).toBeVisible()
  await expect(warningBox).toContainText('Persistence warnings')
  await expect(warningBox).toContainText(persistWarnings[0])
  await expect(warningBox).toContainText(persistWarnings[1])
})

test('shell step env sanitization matches default passthrough and filtering rules', async ({ request }) => {
  const script = [
    'echo "OPENAI_API_KEY=${OPENAI_API_KEY:-}"',
    'if [ -z "${AWS_SECRET_ACCESS_KEY+x}" ]; then echo "AWS_SECRET_ACCESS_KEY_FILTERED=yes"; else echo "AWS_SECRET_ACCESS_KEY_FILTERED=no"; fi',
    `if [ "$QUARTET_CONTROL" = "${e2eShellStaleControl}" ]; then echo "QUARTET_CONTROL_IS_STALE=yes"; else echo "QUARTET_CONTROL_IS_STALE=no"; fi`,
    'quartet_set env_passthrough "$OPENAI_API_KEY"',
    'echo "<<SET_VAR:legacy_only=from_stdout>>"',
    'echo "<<SET_VAR:env_passthrough=legacy_should_not_override_control>>"',
  ].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Shell Env Sanitization')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const jobRes = await request.get(`/api/v1/job/${job.jobId}`, { headers: job.headers })
  expect(jobRes.ok()).toBeTruthy()
  const snapshot = await jobRes.json()
  expect(snapshot.loopConfig?.variables?.env_passthrough).toBe(e2eShellOpenAIAPIKey)
  expect(snapshot.loopConfig?.variables?.legacy_only).toBe('from_stdout')
  expect(snapshot.sessionIds?.length).toBeGreaterThan(0)

  const messagesRes = await request.get(`/api/v1/sessions/${snapshot.sessionIds[0]}/messages`, { headers: job.headers })
  expect(messagesRes.ok(), `session messages failed: ${messagesRes.status()} ${await messagesRes.text()}`).toBeTruthy()
  const messages = await messagesRes.json()
  const transcript = (messages.messages as Array<{ role?: string; content?: string }>)
    .filter((m) => m.role === 'assistant')
    .map((m) => m.content || '')
    .join('\n')
  expect(transcript).toContain(`OPENAI_API_KEY=${e2eShellOpenAIAPIKey}`)
  expect(transcript).toContain('AWS_SECRET_ACCESS_KEY_FILTERED=yes')
  expect(transcript).toContain('QUARTET_CONTROL_IS_STALE=no')
  expect(transcript).not.toContain(e2eShellAWSSecretAccessKey)
  expect(transcript).not.toContain(e2eShellStaleControl)
})

test('shell control vars and workdir temp files stay consistent through real job execution', async ({ request }) => {
  const runInfo = await getE2ERunInfo()
  const workdir = path.join(runInfo.localMemory, `e2e-shell-runtime-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const workspace = await createWorkspace(request, 'E2E Shell Runtime Temp Workspace', workdir)
  const flow: E2EFlowNode[] = [
    {
      id: 'e2e-shell-control-writer',
      type: 'step',
      message: [
        'echo "script_file=$0"',
        'echo "control_file=$QUARTET_CONTROL"',
        'test -f "$0" && echo "script_exists_during_run=yes"',
        'test -f "$QUARTET_CONTROL" && echo "control_exists_during_run=yes"',
        'quartet_set e2e_control_value "value=from control file"',
      ].join('\n'),
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
    {
      id: 'e2e-shell-control-reader',
      type: 'step',
      message: 'echo "control_value={{e2e_control_value}}"',
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
  ]
  const job = await createLoopJobWithFlow(request, flow, workspace.workspaceId, 'E2E Shell Control Tempfiles')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(snapshot.loopConfig?.variables?.e2e_control_value).toBe('value=from control file')
  expect(snapshot.progress?.completedCount).toBe(2)
  expect(snapshot.progress?.failedCount || 0).toBe(0)

  const transcript = await getAssistantTranscript(request, snapshot.sessionIds, job.headers)
  expect(transcript).toContain('script_exists_during_run=yes')
  expect(transcript).toContain('control_exists_during_run=yes')
  expect(transcript).toContain('control_value=value=from control file')

  const remaining = await fs.readdir(workdir)
  expect(remaining.filter((name) => name.startsWith('.quartet-shell-') || name.startsWith('.quartet-ctrl-'))).toEqual([])
})

test('shell step persists a self-consistent timing window for history replay', async ({ request }) => {
  const script = [
    'echo "timestamp-e2e-start"',
    'sleep 0.05',
    'echo "timestamp-e2e-end"',
  ].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Shell Timestamp Consistency')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
  const result = snapshot.progress?.results?.[0]
  expect(result?.durationMs).toBeGreaterThanOrEqual(0)
  expect(result?.content).toContain('timestamp-e2e-end')
  expect(snapshot.sessionIds?.length).toBeGreaterThan(0)

  const messagesRes = await request.get(`/api/v1/sessions/${snapshot.sessionIds[0]}/messages`, { headers: job.headers })
  expect(messagesRes.ok(), `session messages failed: ${messagesRes.status()} ${await messagesRes.text()}`).toBeTruthy()
  const messages = await messagesRes.json()
  const shellMessage = (messages.messages as Array<{ role?: string; content?: string; isShellOutput?: boolean; startedAt?: number; finishedAt?: number }>)
    .find((message) => message.role === 'assistant' && message.isShellOutput)
  expect(shellMessage?.content).toContain('timestamp-e2e-end')
  expect(shellMessage?.finishedAt).toBeGreaterThanOrEqual(shellMessage?.startedAt || 0)
  expect(result?.durationMs).toBe((shellMessage?.finishedAt || 0) - (shellMessage?.startedAt || 0))
})

test('shell step drains oversized stderr and still completes stdout persistence', async ({ request }) => {
  const script = [
    'printf "oversized-stderr-start" >&2',
    'head -c 1200000 /dev/zero | tr "\\0" "x" >&2',
    'printf "\\n" >&2',
    'echo "after oversized stderr"',
  ].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Oversized Stderr Drain')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(snapshot.progress?.completedCount).toBe(1)
  expect(snapshot.progress?.failedCount || 0).toBe(0)
  expect(snapshot.progress?.results?.[0]?.content).toContain('after oversized stderr')
  expect(snapshot.sessionIds?.length).toBeGreaterThan(0)

  const transcript = await getAssistantTranscript(request, snapshot.sessionIds, job.headers)
  expect(transcript).toContain('after oversized stderr')
})

test('shell message persistence failure is surfaced as a persistence warning', async ({ page, request }) => {
  const title = `E2E Shell Persist Warning ${Date.now()}`
  const titlePattern = shellSingleQuote(title)
  const script = [
    'echo "persist-warning-e2e-before"',
    `JOB_DIR=$(grep -R -l ${titlePattern} "$LOCAL_MEMORY/quartet/data/workspaces/ws-1/jobs"/*/.meta/job.json | sed 's#/.meta/job.json$##' | head -n 1 || true)`,
    'if [ -z "$JOB_DIR" ]; then echo "persist warning job dir not found"; exit 1; fi',
    'SESSION_DIR=$(find "$JOB_DIR/sessions" -mindepth 1 -maxdepth 1 -type d | head -n 1 || true)',
    'if [ -z "$SESSION_DIR" ]; then echo "persist warning session dir not found"; exit 1; fi',
    'mkdir -p "$SESSION_DIR/.meta/messages.jsonl"',
    'echo "persist-warning-e2e-after"',
  ].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', title)

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(snapshot.status).toBe('completed')
  expect(snapshot.progress?.lastError || '').toBe('')
  expect(snapshot.progress?.results?.[0]?.content).toContain('persist-warning-e2e-after')
  expect(snapshot.progress?.persistWarnings || []).toEqual(
    expect.arrayContaining([expect.stringContaining('persist failed after persist_shell_messages: append shell messages')]),
  )

  const { localMemory } = await getE2ERunInfo()
  const sessionId = snapshot.sessionIds?.[0]
  if (!sessionId) throw new Error('expected shell job to create a session')
  // The test intentionally made messages.jsonl a directory to force the append
  // failure. Remove that injected fault before opening the UI so this assertion
  // focuses on the user-visible persistence warning instead of read-side error
  // handling for a corrupted messages path.
  await fs.rm(path.join(localMemory, 'quartet', 'data', 'workspaces', 'ws-1', 'jobs', job.jobId, 'sessions', sessionId, '.meta', 'messages.jsonl'), {
    recursive: true,
    force: true,
  })

  await openAppWithAuth(page, `/?workspaceId=ws-1&jobId=${encodeURIComponent(job.jobId)}`)
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-id', job.jobId)
  await expect(page.getByTestId('loop-progress')).toBeVisible()
  const warningBox = page.getByTestId('loop-progress-persist-warning')
  await expect(warningBox).toBeVisible()
  await expect(warningBox).toContainText('Persistence warnings')
  await expect(warningBox).toContainText('persist_shell_messages')
})

test('shell step interruption persists streamed output before stopping', async ({ request }) => {
  const script = [
    'echo "interrupt-e2e-before"',
    'sleep 5',
    'echo "interrupt-e2e-after"',
  ].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Shell Interrupted Output')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await expect.poll(async () => {
    const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
    return snapshot.status as string
  }, { timeout: 10_000 }).toBe('running')

  await new Promise((resolve) => setTimeout(resolve, 300))
  const stopRes = await request.post(`/api/v1/job/${job.jobId}/stop`, { headers: job.headers })
  expect(stopRes.ok(), `job stop failed: ${stopRes.status()} ${await stopRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'stopped')

  const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(snapshot.resume?.nextPath).toEqual([0, 0])
  expect(snapshot.sessionIds?.length).toBeGreaterThan(0)

  await expect.poll(async () => {
    const current = await getJobSnapshot(request, job.jobId, job.headers)
    return await getAssistantTranscript(request, current.sessionIds, job.headers)
  }, { timeout: 10_000 }).toContain('interrupt-e2e-before')

  const transcript = await getAssistantTranscript(request, snapshot.sessionIds, job.headers)
  expect(transcript).not.toContain('interrupt-e2e-after')
})

test('hard stop terminates shell background subprocesses as a process group', async ({ request }) => {
  const runInfo = await getE2ERunInfo()
  const workdir = path.join(runInfo.localMemory, `e2e-shell-pgroup-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const workspace = await createWorkspace(request, 'E2E Shell Process Group Workspace', workdir)
  const leakMarker = path.join(workdir, 'background-child-leaked.txt')
  const script = [
    `(sleep 1.5; echo leaked > ${shellSingleQuote(leakMarker)}) &`,
    'echo "background-child-started"',
    'sleep 10',
    'echo "background-parent-finished"',
  ].join('\n')
  const job = await createShellLoopJob(request, script, workspace.workspaceId, 'E2E Shell Process Group Stop')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  const runningSnapshot = await getJobSnapshot(request, job.jobId, job.headers)

  const chunks = await readSSEUntil(
    `${e2eBackendURL}/api/v1/job/${job.jobId}/events`,
    { 'X-AGENT-AUTH': e2eAuthToken, Accept: 'text/event-stream', 'Last-Event-ID': String(runningSnapshot.lastEventSeq || 0) },
    (text) => text.includes('background-child-started'),
    10_000,
  )
  expect(chunks).toContain('background-child-started')

  const stopRes = await request.post(`/api/v1/job/${job.jobId}/stop`, { headers: job.headers })
  expect(stopRes.ok(), `job stop failed: ${stopRes.status()} ${await stopRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'stopped')

  await new Promise((resolve) => setTimeout(resolve, 2_000))
  expect(await pathExists(leakMarker)).toBe(false)

  const stoppedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(stoppedSnapshot.resume?.nextPath).toEqual([0, 0])
  await expect.poll(async () => {
    const current = await getJobSnapshot(request, job.jobId, job.headers)
    return await getAssistantTranscript(request, current.sessionIds, job.headers)
  }, { timeout: 10_000 }).toContain('background-child-started')

  const transcript = await getAssistantTranscript(request, stoppedSnapshot.sessionIds, job.headers)
  expect(transcript).toContain('background-child-started')
  expect(transcript).not.toContain('background-parent-finished')
})

test('graceful stop at a non-tail shell step preserves resume and continue runs the next step', async ({ request }) => {
  const flow: E2EFlowNode[] = [
    {
      id: 'e2e-graceful-first-step',
      type: 'step',
      message: 'echo "graceful-e2e-first-start"\nsleep 1\necho "graceful-e2e-first-done"',
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
    {
      id: 'e2e-graceful-second-step',
      type: 'step',
      message: 'echo "graceful-e2e-second-ran"',
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
  ]
  const job = await createLoopJobWithFlow(request, flow, 'ws-1', 'E2E Graceful Stop Boundary')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await expect.poll(async () => {
    const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
    return snapshot.status as string
  }, { timeout: 10_000 }).toBe('running')

  const gracefulStopRes = await request.post(`/api/v1/job/${job.jobId}/stop`, {
    headers: job.headers,
    data: { graceful: true },
  })
  expect(gracefulStopRes.ok(), `graceful stop failed: ${gracefulStopRes.status()} ${await gracefulStopRes.text()}`).toBeTruthy()
  expect((await gracefulStopRes.json()).status).toBe('stopping')

  await expect.poll(async () => {
    const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
    return Boolean(snapshot.progress?.gracefulStopPending)
  }, { timeout: 10_000 }).toBe(true)
  await waitForJobStatus(request, job.jobId, job.headers, 'stopped')

  const stoppedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(stoppedSnapshot.resume?.nextPath).toEqual([1, 0])
  expect(stoppedSnapshot.progress?.completedCount).toBe(1)
  expect(stoppedSnapshot.progress?.failedCount || 0).toBe(0)
  expect(stoppedSnapshot.progress?.gracefulStopPending || false).toBe(false)
  let transcript = await getAssistantTranscript(request, stoppedSnapshot.sessionIds, job.headers)
  expect(transcript).toContain('graceful-e2e-first-done')
  expect(transcript).not.toContain('graceful-e2e-second-ran')

  const continueRes = await request.post(`/api/v1/job/${job.jobId}/continue`, { headers: job.headers })
  expect(continueRes.ok(), `job continue failed: ${continueRes.status()} ${await continueRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const completedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(completedSnapshot.resume).toBeFalsy()
  expect(completedSnapshot.progress?.completedCount).toBe(2)
  expect(completedSnapshot.progress?.failedCount || 0).toBe(0)
  transcript = await getAssistantTranscript(request, completedSnapshot.sessionIds, job.headers)
  expect(transcript).toContain('graceful-e2e-first-done')
  expect(transcript).toContain('graceful-e2e-second-ran')
})

test('graceful stop publishes transient pending state over live SSE', async ({ request }) => {
  const flow: E2EFlowNode[] = [
    {
      id: 'e2e-graceful-sse-first-step',
      type: 'step',
      message: 'echo "graceful-sse-e2e-first-start"\nsleep 0.8\necho "graceful-sse-e2e-first-done"',
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
    {
      id: 'e2e-graceful-sse-second-step',
      type: 'step',
      message: 'echo "graceful-sse-e2e-second-must-not-run-before-continue"',
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
  ]
  const job = await createLoopJobWithFlow(request, flow, 'ws-1', 'E2E Graceful Stop SSE Pending')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await expect.poll(async () => {
    const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
    return snapshot.status as string
  }, { timeout: 10_000 }).toBe('running')
  const runningSnapshot = await getJobSnapshot(request, job.jobId, job.headers)

  let stopPromise: Promise<void> | undefined
  const chunks = await readSSEUntil(
    `${e2eBackendURL}/api/v1/job/${job.jobId}/events`,
    { 'X-AGENT-AUTH': e2eAuthToken, Accept: 'text/event-stream', 'Last-Event-ID': String(runningSnapshot.lastEventSeq || 0) },
    (text) => text.includes('"name":"graceful_stop_pending"') && text.includes('"pending":true') && text.includes('"pending":false'),
    15_000,
    () => {
      stopPromise = request.post(`/api/v1/job/${job.jobId}/stop`, {
        headers: job.headers,
        data: { graceful: true },
      }).then(async (res) => {
        expect(res.ok(), `graceful stop failed: ${res.status()} ${await res.text()}`).toBeTruthy()
        expect((await res.json()).status).toBe('stopping')
      })
    },
  )
  await stopPromise

  const events = parseSSEMessageEvents(chunks)
  const pendingEvents = events.filter((event) => event.type === 'CUSTOM' && event.name === 'graceful_stop_pending')
  expect(pendingEvents.map((event) => (event.value as { pending?: boolean })?.pending)).toEqual([true, false])

  await waitForJobStatus(request, job.jobId, job.headers, 'stopped')
  const stoppedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(stoppedSnapshot.resume?.nextPath).toEqual([1, 0])
  expect(stoppedSnapshot.progress?.gracefulStopPending || false).toBe(false)
})

test('graceful stop requested during the tail shell step is consumed and the job completes', async ({ request }) => {
  const script = [
    'echo "graceful-tail-e2e-start"',
    'sleep 1',
    'echo "graceful-tail-e2e-done"',
  ].join('\n')
  const job = await createShellLoopJob(request, script, 'ws-1', 'E2E Graceful Stop Tail Completion')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await expect.poll(async () => {
    const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
    return snapshot.status as string
  }, { timeout: 10_000 }).toBe('running')

  const gracefulStopRes = await request.post(`/api/v1/job/${job.jobId}/stop`, {
    headers: job.headers,
    data: { graceful: true },
  })
  expect(gracefulStopRes.ok(), `graceful stop failed: ${gracefulStopRes.status()} ${await gracefulStopRes.text()}`).toBeTruthy()
  expect((await gracefulStopRes.json()).status).toBe('stopping')

  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const completedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(completedSnapshot.resume).toBeFalsy()
  expect(completedSnapshot.progress?.completedCount).toBe(1)
  expect(completedSnapshot.progress?.failedCount || 0).toBe(0)
  expect(completedSnapshot.progress?.gracefulStopPending || false).toBe(false)
  const transcript = await getAssistantTranscript(request, completedSnapshot.sessionIds, job.headers)
  expect(transcript).toContain('graceful-tail-e2e-done')
})

test('continue after a stopped post-group shell step resumes past the early-broken group', async ({ request }) => {
  const flow: E2EFlowNode[] = [
    {
      id: 'e2e-resume-group',
      type: 'group',
      iterationCount: 3,
      children: [
        {
          id: 'e2e-resume-group-before-break',
          type: 'step',
          message: 'echo "resume-e2e-group-before-break"',
          repeatCount: 1,
          roundMode: 'beforeRound',
          roundType: 'shell',
        },
        {
          id: 'e2e-resume-group-break',
          type: 'step',
          message: 'echo "resume-e2e-group-break"\nquartet_break',
          repeatCount: 1,
          roundMode: 'none',
          roundType: 'shell',
        },
        {
          id: 'e2e-resume-group-skipped',
          type: 'step',
          message: 'echo "resume-e2e-skipped-sibling-must-not-run"',
          repeatCount: 1,
          roundMode: 'none',
          roundType: 'shell',
        },
      ],
    },
    {
      id: 'e2e-resume-after-group',
      type: 'step',
      message: 'echo "resume-e2e-after-group-start"\nsleep 10\necho "resume-e2e-after-group-done"',
      repeatCount: 1,
      roundMode: 'none',
      roundType: 'shell',
    },
  ]
  const job = await createLoopJobWithFlow(request, flow, 'ws-1', 'E2E Resume Past Broken Group')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()

  await expect.poll(async () => {
    const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
    return `${snapshot.status}:${JSON.stringify(snapshot.resume?.nextPath || null)}`
  }, { timeout: 15_000 }).toBe('running:[1,0]')

  const stopRes = await request.post(`/api/v1/job/${job.jobId}/stop`, { headers: job.headers })
  expect(stopRes.ok(), `job stop failed: ${stopRes.status()} ${await stopRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'stopped')

  const stoppedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(stoppedSnapshot.resume?.nextPath).toEqual([1, 0])
  expect(stoppedSnapshot.progress?.groupActualIterations?.['0']).toBe(1)
  expect(stoppedSnapshot.progress?.groupActualLeafCounts?.['0']).toBe(2)

  const continueRes = await request.post(`/api/v1/job/${job.jobId}/continue`, { headers: job.headers })
  expect(continueRes.ok(), `job continue failed: ${continueRes.status()} ${await continueRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const completedSnapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(completedSnapshot.status).toBe('completed')
  expect(completedSnapshot.resume).toBeFalsy()
  expect(completedSnapshot.progress?.totalSteps).toBe(3)
  expect(completedSnapshot.progress?.completedCount).toBe(3)
  expect(completedSnapshot.progress?.failedCount || 0).toBe(0)

  const results = completedSnapshot.progress?.results as Array<{ path: number[]; success: boolean }> | undefined
  expect(results?.map((result) => result.path)).toEqual([[0, 0, 0, 0], [0, 0, 1, 0], [1, 0]])
  expect(results?.every((result) => result.success)).toBeTruthy()

  const transcript = await getAssistantTranscript(request, completedSnapshot.sessionIds, job.headers)
  expect(countOccurrences(transcript, 'resume-e2e-group-before-break')).toBe(1)
  expect(countOccurrences(transcript, 'resume-e2e-group-break')).toBe(1)
  expect(transcript).not.toContain('resume-e2e-skipped-sibling-must-not-run')
  expect(countOccurrences(transcript, 'resume-e2e-after-group-start')).toBeGreaterThanOrEqual(2)
  expect(countOccurrences(transcript, 'resume-e2e-after-group-done')).toBe(1)
})

test('nested shell group stops only the innermost group and backfills progress', async ({ request }) => {
  const flow: E2EFlowNode[] = [
    {
      id: 'e2e-outer-group',
      type: 'group',
      iterationCount: 3,
      children: [
        {
          id: 'e2e-before-break',
          type: 'step',
          message: 'echo "before inner break"',
          repeatCount: 1,
          roundMode: 'beforeRound',
          roundType: 'shell',
        },
        {
          id: 'e2e-break-inner-group',
          type: 'step',
          message: 'echo "breaking inner group"\nquartet_break',
          repeatCount: 1,
          roundMode: 'none',
          roundType: 'shell',
        },
        {
          id: 'e2e-should-be-skipped',
          type: 'step',
          message: 'echo "this skipped sibling must not run"',
          repeatCount: 1,
          roundMode: 'none',
          roundType: 'shell',
        },
      ],
    },
    {
      id: 'e2e-after-group',
      type: 'step',
      message: 'echo "after group still runs"',
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
  ]
  const job = await createLoopJobWithFlow(request, flow, 'ws-1', 'E2E Nested Shell Group Stop')

  const startRes = await request.post(`/api/v1/job/${job.jobId}/start`, { headers: job.headers })
  expect(startRes.ok(), `job start failed: ${startRes.status()} ${await startRes.text()}`).toBeTruthy()
  await waitForJobStatus(request, job.jobId, job.headers, 'completed')

  const snapshot = await getJobSnapshot(request, job.jobId, job.headers)
  expect(snapshot.status).toBe('completed')
  expect(snapshot.progress?.totalSteps).toBe(3)
  expect(snapshot.progress?.completedCount).toBe(3)
  expect(snapshot.progress?.failedCount || 0).toBe(0)
  expect(snapshot.progress?.groupActualIterations?.['0']).toBe(1)
  expect(snapshot.progress?.groupActualLeafCounts?.['0']).toBe(2)

  const results = snapshot.progress?.results as Array<{ path: number[]; success: boolean; content?: string }> | undefined
  expect(results?.map((result) => result.path)).toEqual([[0, 0, 0, 0], [0, 0, 1, 0], [1, 0]])
  expect(results?.every((result) => result.success)).toBeTruthy()
  const combinedContent = await getSessionTranscript(request, snapshot.sessionIds, job.headers)
  expect(combinedContent).toContain('before inner break')
  expect(combinedContent).toContain('breaking inner group')
  expect(combinedContent).toContain('after group still runs')
  expect(combinedContent).not.toContain('this skipped sibling must not run')
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

// ---------------------------------------------------------------------------
// Loop template persistence: backend LoopConfig validation, create-only Save
// semantics, and schedule-follows-template trigger behavior.
//
// These exercise the real template/schedule APIs (no model needed — flows use
// shell steps) plus the UI error-handling for failed template saves/loads.
// ---------------------------------------------------------------------------

const templateHeaders = { 'X-AGENT-AUTH': e2eAuthToken }

// A minimal valid loop flow (single shell step) — enough to pass
// NormalizeAndValidateLoopConfig without needing a model or agent.
function validShellFlow(marker: string): E2EFlowNode[] {
  return [
    {
      id: `e2e-tmpl-step-${marker}`,
      type: 'step',
      message: `echo ${marker}`,
      repeatCount: 1,
      roundMode: 'beforeRound',
      roundType: 'shell',
    },
  ]
}

async function saveTemplate(request: APIRequestContext, name: string, flow: E2EFlowNode[]) {
  return await request.post('/api/v1/template/save', {
    headers: templateHeaders,
    data: { name, config: { flow } },
  })
}

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
    const res = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: templateHeaders })
    expect(res.ok(), `schedule get failed: ${res.status()} ${await res.text()}`).toBeTruthy()
    const schedule = await res.json()
    return schedule.lastStatus as string
  }, { timeout: 30_000 }).toBe(expected)
}

test('template save rejects an invalid loop config with 400 and full error', async ({ request }) => {
  // Empty flow is structurally invalid; the backend must reject it rather than
  // silently persisting a broken template that only fails later at run time.
  const res = await request.post('/api/v1/template/save', {
    headers: templateHeaders,
    data: { name: `E2E Invalid Template ${Date.now()}`, config: { flow: [] } },
  })
  expect(res.status(), `expected 400, got ${res.status()}: ${await res.text()}`).toBe(400)
  const body = await res.json()
  // Errors are surfaced in full (AGENTS.md) — the message must carry the real
  // validation reason, not a generic stand-in.
  expect(String(body.msg)).toContain('flow')

  // And it must not have leaked into the list.
  const listRes = await request.get('/api/v1/template/list', { headers: templateHeaders })
  expect(listRes.ok()).toBeTruthy()
  const list = await listRes.json()
  const names = (list.templates as Array<{ name: string }>).map((t) => t.name)
  expect(names.some((n) => n.startsWith('E2E Invalid Template'))).toBeFalsy()
})

test('template update rejects an invalid loop config with 400', async ({ request }) => {
  const name = `E2E Update Validate ${Date.now()}`
  const saveRes = await saveTemplate(request, name, validShellFlow('update-validate'))
  expect(saveRes.ok(), `save failed: ${saveRes.status()} ${await saveRes.text()}`).toBeTruthy()
  const saved = await saveRes.json()
  const id = saved.template.id as string

  const updateRes = await request.put(`/api/v1/template/${id}`, {
    headers: templateHeaders,
    data: { name, config: { flow: [] } },
  })
  expect(updateRes.status(), `expected 400, got ${updateRes.status()}: ${await updateRes.text()}`).toBe(400)

  // The original config must survive a rejected update.
  const getRes = await request.get('/api/v1/template/list', { headers: templateHeaders })
  const list = await getRes.json()
  const found = (list.templates as Array<{ id: string; config: { flow?: unknown[] } }>).find((t) => t.id === id)
  expect(found?.config.flow?.length).toBe(1)
})

test('template save always allocates a fresh id and never overwrites an existing template', async ({ request }) => {
  const first = await saveTemplate(request, `E2E NoOverwrite A ${Date.now()}`, validShellFlow('first'))
  expect(first.ok(), `first save failed: ${first.status()} ${await first.text()}`).toBeTruthy()
  const firstTmpl = (await first.json()).template as { id: string }

  // Attempt to overwrite by replaying the first template's id. The backend
  // ignores the client-supplied id, so this creates a brand-new template and
  // leaves the original untouched.
  const second = await request.post('/api/v1/template/save', {
    headers: templateHeaders,
    data: { id: firstTmpl.id, name: `E2E NoOverwrite B ${Date.now()}`, config: { flow: validShellFlow('second') } },
  })
  expect(second.ok(), `second save failed: ${second.status()} ${await second.text()}`).toBeTruthy()
  const secondTmpl = (await second.json()).template as { id: string }

  expect(secondTmpl.id).not.toBe(firstTmpl.id)

  const listRes = await request.get('/api/v1/template/list', { headers: templateHeaders })
  const list = await listRes.json()
  const byId = new Map((list.templates as Array<{ id: string; config: { flow: Array<{ message?: string }> } }>).map((t) => [t.id, t]))
  // Original still present and unchanged.
  expect(byId.get(firstTmpl.id)?.config.flow[0]?.message).toBe('echo first')
  expect(byId.get(secondTmpl.id)?.config.flow[0]?.message).toBe('echo second')
})

test('scheduled graph workflow triggers, releases concurrency, and records missing workflow failures', async ({ request }) => {
  const { localMemory } = await getE2ERunInfo()
  const workdir = path.join(localMemory, `e2e-graph-schedule-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const workspace = await createWorkspace(request, 'E2E Graph Schedule Workspace', workdir)
  const marker = `graph-schedule-${Date.now()}`
  const workflowNamePrefix = `E2E Graph Schedule Workflow ${Date.now()}`

  const createWorkflowRes = await request.post('/api/v1/graph/workflow', {
    headers: templateHeaders,
    data: {
      name: `${workflowNamePrefix} A`,
      workspaceId: workspace.workspaceId,
      config: validShellGraphConfig(marker, workspace.workspaceId, workdir),
    },
  })
  expect(createWorkflowRes.ok(), `graph workflow create failed: ${createWorkflowRes.status()} ${await createWorkflowRes.text()}`).toBeTruthy()
  const workflow = (await createWorkflowRes.json()).workflow as { id: string; updatedAt: string }

  const createScheduleRes = await request.post('/api/v1/schedule/create', {
    headers: templateHeaders,
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

  const firstRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: templateHeaders })
  expect(firstRunRes.ok(), `first schedule run failed: ${firstRunRes.status()} ${await firstRunRes.text()}`).toBeTruthy()
  const firstJobID = (await firstRunRes.json()).jobId as string
  expect(firstJobID).toMatch(/^job-/)

  await waitForJobStatus(request, firstJobID, templateHeaders, 'completed')
  await waitForScheduleStatus(request, scheduleId, 'completed')

  const firstJob = await getJobSnapshot(request, firstJobID, templateHeaders)
  expect(firstJob.mode).toBe('graph')
  expect(firstJob.scheduleId).toBe(scheduleId)
  expect(firstJob.graphRunId).toMatch(/^grun-/)

  const afterFirstRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: templateHeaders })
  const afterFirst = await afterFirstRes.json()
  expect(afterFirst.lastRunJobID).toBe(firstJobID)
  expect(afterFirst.lastTriggerError || '').toBe('')
  expect(afterFirst.runCount).toBeGreaterThanOrEqual(1)

  const secondRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: templateHeaders })
  expect(secondRunRes.ok(), `second schedule run should prove concurrency was released: ${secondRunRes.status()} ${await secondRunRes.text()}`).toBeTruthy()
  const secondJobID = (await secondRunRes.json()).jobId as string
  await waitForJobStatus(request, secondJobID, templateHeaders, 'completed')
  await waitForScheduleStatus(request, scheduleId, 'completed')
  const afterSecondRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: templateHeaders })
  expect(afterSecondRes.ok(), `schedule get failed: ${afterSecondRes.status()} ${await afterSecondRes.text()}`).toBeTruthy()
  const afterSecond = await afterSecondRes.json()

  const secondJob = await getJobSnapshot(request, secondJobID, templateHeaders)
  expect(secondJob.mode).toBe('graph')
  expect(secondJob.graphRunId).toMatch(/^grun-/)
  expect(secondJob.graphRunId).not.toBe(firstJob.graphRunId)

  const deleteWorkflowRes = await request.delete(`/api/v1/graph/workflow/${workflow.id}`, {
    headers: templateHeaders,
    data: { updatedAt: workflow.updatedAt },
  })
  expect(deleteWorkflowRes.ok(), `workflow delete failed: ${deleteWorkflowRes.status()} ${await deleteWorkflowRes.text()}`).toBeTruthy()

  const failedRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: templateHeaders })
  expect(failedRunRes.status(), `expected missing workflow trigger to fail, got ${failedRunRes.status()}: ${await failedRunRes.text()}`).toBe(409)
  const failureText = await failedRunRes.text()
  expect(failureText).toContain(workflow.id)

  const failedScheduleRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: templateHeaders })
  expect(failedScheduleRes.ok(), `schedule get failed: ${failedScheduleRes.status()} ${await failedScheduleRes.text()}`).toBeTruthy()
  const failedSchedule = await failedScheduleRes.json()
  expect(failedSchedule.lastStatus).toBe('failed')
  expect(failedSchedule.lastRunJobID).toBe(secondJobID)
  expect(failedSchedule.lastTriggerError).toContain(workflow.id)
  expect(failedSchedule.lastTriggerError).toContain('workflow')
  expect(failedSchedule.runCount).toBe(afterSecond.runCount + 1)

  const retryFailureRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: templateHeaders })
  expect(retryFailureRes.status(), `expected repeated missing workflow trigger to fail without a leaked concurrency slot, got ${retryFailureRes.status()}: ${await retryFailureRes.text()}`).toBe(409)
  expect(await retryFailureRes.text()).toContain(workflow.id)

  const replacementWorkflowRes = await request.post('/api/v1/graph/workflow', {
    headers: templateHeaders,
    data: {
      name: `${workflowNamePrefix} B`,
      workspaceId: workspace.workspaceId,
      config: validShellGraphConfig(`${marker}-replacement`, workspace.workspaceId, workdir),
    },
  })
  expect(replacementWorkflowRes.ok(), `replacement graph workflow create failed: ${replacementWorkflowRes.status()} ${await replacementWorkflowRes.text()}`).toBeTruthy()
  const replacementWorkflow = (await replacementWorkflowRes.json()).workflow as { id: string }

  const updateScheduleRes = await request.put(`/api/v1/schedule/${scheduleId}`, {
    headers: templateHeaders,
    data: {
      graphWorkflowId: replacementWorkflow.id,
    },
  })
  expect(updateScheduleRes.ok(), `schedule update failed: ${updateScheduleRes.status()} ${await updateScheduleRes.text()}`).toBeTruthy()

  const recoveryRunRes = await request.post(`/api/v1/schedule/${scheduleId}/run`, { headers: templateHeaders })
  expect(recoveryRunRes.ok(), `schedule run after replacing workflow failed: ${recoveryRunRes.status()} ${await recoveryRunRes.text()}`).toBeTruthy()
  const recoveryJobID = (await recoveryRunRes.json()).jobId as string
  await waitForJobStatus(request, recoveryJobID, templateHeaders, 'completed')
  await waitForScheduleStatus(request, scheduleId, 'completed')

  const recoveredScheduleRes = await request.get(`/api/v1/schedule/${scheduleId}`, { headers: templateHeaders })
  expect(recoveredScheduleRes.ok(), `schedule get failed: ${recoveredScheduleRes.status()} ${await recoveredScheduleRes.text()}`).toBeTruthy()
  const recoveredSchedule = await recoveredScheduleRes.json()
  expect(recoveredSchedule.lastRunJobID).toBe(recoveryJobID)
  expect(recoveredSchedule.lastTriggerError || '').toBe('')
  expect(recoveredSchedule.graphWorkflowId).toBe(replacementWorkflow.id)
})

test('template save dialog keeps the panel open and shows the backend error on failure', async ({ page }) => {
  // Force the save endpoint to fail with a structured error so we can assert
  // the UI surfaces the full message and does NOT close the dialog or clear
  // the unsaved (dirty) state — the regression this guards against.
  await page.route('**/api/v1/template/save', async (route) => {
    await route.fulfill({
      status: 400,
      contentType: 'application/json',
      body: JSON.stringify({ code: -1, msg: 'E2E forced template save failure' }),
    })
  })

  await openAppWithAuth(page, '/?workspaceId=ws-1')
  await expectHomeReady(page)

  const openButton = page.getByTestId('loop-config-open-button')
  // The loop button requires a connected agent; skip cleanly if none is wired.
  if (await openButton.isDisabled()) {
    test.skip(true, 'loop config entry is disabled (no agent available in this run)')
  }
  await openButton.click()

  // Give the single default step a message so the config becomes valid/saveable.
  await page.getByTestId('loop-step-message-input').first().fill('echo hello from e2e')

  // Open the save dialog, name the template, and attempt the (forced-failing) save.
  await page.getByRole('button', { name: /save as template/i }).first().click()
  await page.getByTestId('loop-template-save-name-input').fill('E2E UI Save Failure')
  await page.getByTestId('loop-template-save-confirm').click()

  // The backend error is shown verbatim and the dialog stays open.
  await expect(page.getByTestId('loop-template-save-error')).toHaveText(/E2E forced template save failure/)
  await expect(page.getByTestId('loop-template-save-name-input')).toBeVisible()
  // The dirty indicator must persist (save did not succeed).
  await expect(page.getByText(/unsaved/i)).toBeVisible()
})
