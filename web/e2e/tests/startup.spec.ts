import fs from 'node:fs/promises'
import path from 'node:path'

import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
import {
  e2eAgentType,
  e2eAuthHeaders,
  e2eInterruptedRunningJobID,
  e2eLegacyFirstModelID,
  e2eLegacyFirstModelJobID,
  e2eModelID,
  e2ePassword,
  e2ePersistWarningJobID,
  e2eUsername,
  installE2EAuthCookie,
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
// state (eino-cli reads isolated LOCAL_MEMORY config and EINO_HOME sessions;
// other agents use $HOME). If
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
  await page.addInitScript(() => {
    localStorage.setItem('quartet-language', 'en')
  })
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
  const headers = e2eAuthHeaders()
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
  const headers = e2eAuthHeaders()
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

test('boots isolated backend and frontend with a user session', async ({ page }) => {
  await openAppWithAuth(page)
  await expectHomeReady(page)
})

test('auth gate asks for credentials when the session cookie is absent', async ({ page }) => {
  await page.context().clearCookies()
  await page.goto('/')

  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'login')
  await expect(page.getByTestId('auth-gate-submit-button')).toBeDisabled()

  await page.getByTestId('auth-gate-username').fill(e2eUsername)
  await page.getByTestId('auth-gate-password').fill(e2ePassword)
  await expect(page.getByTestId('auth-gate-submit-button')).toBeEnabled()
  await page.getByTestId('auth-gate-submit-button').click()

  await expectHomeReady(page)
  await expect.poll(async () => (await page.context().cookies()).some((cookie) => cookie.name === 'quartet_session')).toBe(true)
})

test('auth gate rejects a wrong password and recovers with valid credentials', async ({ page }) => {
  await page.context().clearCookies()
  await page.goto('/')
  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'login')
  await page.getByTestId('auth-gate-username').fill(e2eUsername)
  await page.getByTestId('auth-gate-password').fill('wrong-password1')
  await page.getByTestId('auth-gate-submit-button').click()
  await expect(page.getByText(/invalid username or password/)).toBeVisible()
  await page.getByTestId('auth-gate-password').fill(e2ePassword)
  await page.getByTestId('auth-gate-submit-button').click()
  await expectHomeReady(page)
  await installE2EAuthCookie(page)
})

test('private APIs require the session cookie and CSRF token', async ({ request }) => {
  const unauthenticated = await request.get('/api/v1/auth/me', {
    headers: { Cookie: '', 'X-CSRF-Token': '' },
  })
  expect(unauthenticated.status()).toBe(401)
  expect(await unauthenticated.text()).toContain('authentication required')

  const missingCSRF = await request.post('/api/v1/auth/logout', {
    headers: { 'X-CSRF-Token': '' },
  })
  expect(missingCSRF.status()).toBe(403)
  expect(await missingCSRF.text()).toContain('invalid CSRF token')

  const legacyHeader = await request.get('/api/v1/auth/me', {
    headers: { Cookie: '', 'X-CSRF-Token': '', 'X-AGENT-AUTH': 'legacy-token-must-not-work' },
  })
  expect(legacyHeader.status()).toBe(401)
})

test('RBAC applies immediately through forced password change and logout', async ({ request }) => {
  const suffix = Date.now().toString(36)
  const username = `viewer-${suffix}`
  const temporaryPassword = `temporary-${suffix}-1`
  const permanentPassword = `permanent-${suffix}-2`

  const createRole = await request.post('/api/v1/roles', {
    data: {
      name: `Workspace reader ${suffix}`,
      description: 'E2E workspace-only role',
      permissions: ['workspace.read'],
    },
  })
  expect(createRole.ok(), await createRole.text()).toBeTruthy()
  const createdRole = (await createRole.json()).role as { id: string; version: number }
  const roleID = createdRole.id

  const createUser = await request.post('/api/v1/users', {
    data: { username, displayName: username, password: temporaryPassword, roleIds: [roleID] },
  })
  expect(createUser.ok(), await createUser.text()).toBeTruthy()
  const userID = (await createUser.json()).user.id as string

  const login = await request.post('/api/v1/auth/login', {
    data: { username, password: temporaryPassword },
  })
  expect(login.ok(), await login.text()).toBeTruthy()
  const loginPrincipal = await login.json() as { csrfToken: string; user: { mustChangePassword: boolean } }
  expect(loginPrincipal.user.mustChangePassword).toBe(true)
  const loginCookie = login.headers()['set-cookie']?.match(/quartet_session=([^;]+)/)?.[1]
  expect(loginCookie).toBeTruthy()

  const temporaryHeaders = {
    Cookie: `quartet_session=${loginCookie}`,
    'X-CSRF-Token': loginPrincipal.csrfToken,
  }
  const blockedBeforePasswordChange = await request.get('/api/v1/workspace/list', { headers: temporaryHeaders })
  expect(blockedBeforePasswordChange.status()).toBe(403)
  expect(await blockedBeforePasswordChange.text()).toContain('password change required')

  const changePassword = await request.put('/api/v1/auth/password', {
    headers: temporaryHeaders,
    data: { currentPassword: temporaryPassword, newPassword: permanentPassword },
  })
  expect(changePassword.ok(), await changePassword.text()).toBeTruthy()
  const principal = await changePassword.json() as { csrfToken: string; user: { mustChangePassword: boolean } }
  expect(principal.user.mustChangePassword).toBe(false)
  const sessionCookie = changePassword.headers()['set-cookie']?.match(/quartet_session=([^;]+)/)?.[1]
  expect(sessionCookie).toBeTruthy()
  const userHeaders = { Cookie: `quartet_session=${sessionCookie}`, 'X-CSRF-Token': principal.csrfToken }

  expect((await request.get('/api/v1/workspace/list', { headers: userHeaders })).ok()).toBe(true)
  const deniedJobs = await request.get('/api/v1/job/list', { headers: userHeaders })
  expect(deniedJobs.status()).toBe(403)
  expect(await deniedJobs.text()).toContain('job.read is required')

  const updateRole = await request.put(`/api/v1/roles/${roleID}`, {
    data: {
      version: createdRole.version,
      name: `Workspace reader ${suffix}`,
      description: 'E2E workspace and job reader role',
      permissions: ['job.read', 'workspace.read'],
    },
  })
  expect(updateRole.ok(), await updateRole.text()).toBeTruthy()
  expect((await request.get('/api/v1/job/list', { headers: userHeaders })).ok()).toBe(true)

  expect((await request.post('/api/v1/auth/logout', { headers: userHeaders })).ok()).toBe(true)
  expect((await request.get('/api/v1/auth/me', { headers: userHeaders })).status()).toBe(401)

  const relogin = await request.post('/api/v1/auth/login', { data: { username, password: permanentPassword } })
  expect(relogin.ok(), await relogin.text()).toBeTruthy()
  const reloginPrincipal = await relogin.json() as { csrfToken: string }
  const reloginCookie = relogin.headers()['set-cookie']?.match(/quartet_session=([^;]+)/)?.[1]
  expect(reloginCookie).toBeTruthy()
  const reloginHeaders = { Cookie: `quartet_session=${reloginCookie}`, 'X-CSRF-Token': reloginPrincipal.csrfToken }

  const currentUser = await request.get(`/api/v1/users/${userID}`)
  expect(currentUser.ok(), await currentUser.text()).toBeTruthy()
  const currentVersion = (await currentUser.json()).user.version as number
  expect((await request.put(`/api/v1/users/${userID}`, { data: { version: currentVersion, status: 'disabled' } })).ok()).toBe(true)
  expect((await request.get('/api/v1/auth/me', { headers: reloginHeaders })).status()).toBe(401)

  const disabledUser = await request.get(`/api/v1/users/${userID}`)
  expect(disabledUser.ok(), await disabledUser.text()).toBeTruthy()
  const disabledVersion = (await disabledUser.json()).user.version as number
  expect((await request.delete(`/api/v1/users/${userID}`, { data: { version: disabledVersion } })).ok()).toBe(true)
  const updatedRoleVersion = (await updateRole.json()).role.version as number
  expect((await request.delete(`/api/v1/roles/${roleID}`, { data: { version: updatedRoleVersion } })).ok()).toBe(true)
})

test('admin can create a role and user in the UI and the user completes forced password change', async ({ page, request }) => {
  const suffix = Date.now().toString(36)
  const roleName = `Browser Reader ${suffix}`
  const username = `browser-${suffix}`
  const temporaryPassword = `temporary-${suffix}-1`
  const permanentPassword = `permanent-${suffix}-2`

  await openAppWithAuth(page)
  await page.getByTestId('settings-open-button').click()
  await expect(page.locator('[data-settings-tab="roles"]')).toHaveCount(0)
  await expect(page.locator('[data-settings-tab="users"]')).toHaveCount(0)
  await page.locator('[data-settings-tab="account"]').click()
  await expect(page.locator('[data-settings-subtab="account"]')).toBeVisible()
  await expect(page.locator('[data-settings-subtab="roles"]')).toBeVisible()
  await expect(page.locator('[data-settings-subtab="users"]')).toBeVisible()
  await page.locator('[data-settings-subtab="roles"]').click()
  await page.getByTestId('role-name-input').fill(roleName)
  await page.locator('[data-permission-id="workspace.read"]').check()
  const roleCreated = page.waitForResponse((response) =>
    response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/roles',
  )
  await page.getByTestId('role-save-button').click()
  expect((await roleCreated).ok()).toBe(true)
  await expect(page.locator('.auth-admin-list button').filter({ hasText: roleName })).toBeVisible()

  await page.locator('[data-settings-subtab="users"]').click()
  await page.getByTestId('user-username-input').fill(username)
  await page.getByTestId('user-display-name-input').fill(`Browser user ${suffix}`)
  await page.getByTestId('user-password-input').fill(temporaryPassword)
  await page.locator('[data-role-id="member"]').uncheck()
  const rolesResponse = await request.get('/api/v1/roles')
  expect(rolesResponse.ok(), await rolesResponse.text()).toBeTruthy()
  const browserRole = ((await rolesResponse.json()).roles as Array<{ id: string; name: string }>).find((role) => role.name === roleName)
  expect(browserRole).toBeTruthy()
  await page.locator(`[data-role-id="${browserRole!.id}"]`).check()
  const userCreated = page.waitForResponse((response) =>
    response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/users',
  )
  await page.getByTestId('user-create-button').click()
  expect((await userCreated).ok()).toBe(true)
  await expect(page.locator('.auth-admin-list button').filter({ hasText: username })).toBeVisible()

  await page.locator('[data-settings-subtab="roles"]').click()
  await expect(page.locator('.auth-admin-list button').filter({ hasText: roleName })).toContainText('1 个用户')

  await page.context().clearCookies()
  await page.reload()
  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'login')

  await page.getByTestId('auth-gate-username').fill(username)
  await page.getByTestId('auth-gate-password').fill(temporaryPassword)
  await page.getByTestId('auth-gate-submit-button').click()
  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'changePassword')
  await page.getByTestId('auth-gate-current-password').fill(temporaryPassword)
  await page.getByTestId('auth-gate-new-password').fill(permanentPassword)
  await page.getByTestId('auth-gate-change-password').click()
  await expectHomeReady(page)

  await page.getByTestId('settings-open-button').click()
  await expect(page.locator('[data-settings-tab="account"]')).toBeVisible()
  await expect(page.locator('[data-settings-tab="users"]')).toHaveCount(0)
  await expect(page.locator('[data-settings-tab="roles"]')).toHaveCount(0)
  await page.locator('[data-settings-tab="account"]').click()
  await expect(page.locator('[data-settings-subtab="account"]')).toBeVisible()
  await expect(page.locator('[data-settings-subtab="users"]')).toHaveCount(0)
  await expect(page.locator('[data-settings-subtab="roles"]')).toHaveCount(0)
  await page.getByRole('button', { name: '退出登录' }).click()
  await expect(page.getByTestId('auth-gate')).toHaveAttribute('data-stage', 'login')

  // The browser and API fixtures start with the same administrator session.
  // Logging out through the UI correctly revokes it, so obtain a fresh admin
  // session for fixture cleanup instead of weakening the logout assertion.
  const adminLogin = await request.post('/api/v1/auth/login', {
    data: { username: e2eUsername, password: e2ePassword },
  })
  expect(adminLogin.ok(), await adminLogin.text()).toBeTruthy()
  const adminPrincipal = await adminLogin.json() as { csrfToken: string }
  const adminCookie = adminLogin.headers()['set-cookie']?.match(/quartet_session=([^;]+)/)?.[1]
  expect(adminCookie).toBeTruthy()
  const adminHeaders = { Cookie: `quartet_session=${adminCookie}`, 'X-CSRF-Token': adminPrincipal.csrfToken }

  const users = await request.get('/api/v1/users', { headers: adminHeaders })
  expect(users.ok(), await users.text()).toBeTruthy()
  const user = ((await users.json()).users as Array<{ id: string; username: string; version: number }>).find((item) => item.username === username)
  expect(user).toBeTruthy()
  expect((await request.delete(`/api/v1/users/${user!.id}`, { headers: adminHeaders, data: { version: user!.version } })).ok()).toBe(true)
  const roles = await request.get('/api/v1/roles', { headers: adminHeaders })
  expect(roles.ok(), await roles.text()).toBeTruthy()
  const role = ((await roles.json()).roles as Array<{ id: string; name: string; version: number }>).find((item) => item.name === roleName)
  expect(role).toBeTruthy()
  expect((await request.delete(`/api/v1/roles/${role!.id}`, { headers: adminHeaders, data: { version: role!.version } })).ok()).toBe(true)
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

  await page.addInitScript(() => {
    localStorage.setItem('quartet-language', 'en')
  })

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

  await expect(page.locator('[data-settings-tab="lark"]')).toHaveCount(0)
  await expect(page.locator('[data-settings-tab="wechat"]')).toHaveCount(0)
  await page.locator('[data-settings-tab="im"]').click()
  await expect(page.locator('[data-settings-subtab="lark"]')).toBeVisible()
  await expect(page.locator('[data-settings-subtab="wechat"]')).toBeVisible()
  await expect(page.locator('[data-active-subtab="lark"]')).toBeVisible()
  await page.locator('[data-settings-subtab="wechat"]').click()
  await expect(page.locator('[data-active-subtab="wechat"]')).toBeVisible()
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
  const headers = e2eAuthHeaders()

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
  const headers = e2eAuthHeaders()

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
  const headers = e2eAuthHeaders()
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
  const headers = e2eAuthHeaders()
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
  const headers = e2eAuthHeaders()
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
  const headers = e2eAuthHeaders()
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
  const headers = e2eAuthHeaders()
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

const scheduleHeaders = e2eAuthHeaders()

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
