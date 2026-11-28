import { spawn, execFileSync, type ChildProcess } from 'node:child_process'
import { createHash, randomInt, randomUUID } from 'node:crypto'
import { once } from 'node:events'
import fs from 'node:fs/promises'
import net from 'node:net'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { request as playwrightRequest, type APIRequestContext, type APIResponse, type TestInfo } from '@playwright/test'
import { test, expect } from '../fixtures/test'
import { e2eBackendURL } from '../fixtures/e2e-environment'

const endpoint = '/api/v1/schedule'
const nextMinute = () => {
  const now = Date.now()
  return new Date(Math.ceil((now + 8000) / 60_000) * 60_000)
}

async function json<T>(result: Promise<APIResponse>): Promise<T> {
  const response = await result
  const raw = await response.text()
  expect(response.ok(), `${response.url()}: ${response.status()} ${raw}`).toBeTruthy()
  return JSON.parse(raw) as T
}

type Schedule = {
  id: string; name: string; approvalPreview: string; planState: string; runCount: number;
  workspaceId: string; workdir: string; nextRunAt?: number;
}
type Execution = { id: string; scheduleId: string; jobId?: string; status: string; error?: string }
type Workflow = { id: string; updatedAt: string; config: ReturnType<typeof graph> }

function graph(marker: string, delay = false) {
  return {
    nodes: [
      { id: 'start', type: 'start' },
      { id: 'shell', title: 'Timer shell', type: 'shell', config: { script: `${delay ? 'sleep 3\n' : ''}echo ${marker}` } },
      { id: 'end', type: 'end' },
    ],
    edges: [
      { id: 'first', sourceNodeId: 'start', targetNodeId: 'shell' },
      { id: 'second', sourceNodeId: 'shell', targetNodeId: 'end' },
    ],
  }
}

async function newGraph(api: APIRequestContext, delay = false) {
  const { workflow } = await json<{ workflow: Workflow }>(api.post('/api/v1/graph/workflow', {
    data: { name: `Timer E2E ${randomUUID()}`, config: graph('graph-timer-success', delay) },
  }))
  return workflow
}

async function newTimer(api: APIRequestContext, data: object, enabled = true) {
  const spec = { name: `Timer E2E ${randomUUID()}`, cronExpr: '0 0 1 1 *', timeZone: 'UTC', ...data, enabled }
  const preview = await json<Schedule>(api.post(`${endpoint}/preview`, { data: spec }))
  return (await json<{ schedule: Schedule }>(api.post(`${endpoint}/create`, {
    data: { ...spec, expectedApproval: preview.approvalPreview },
  }))).schedule
}

async function executions(api: APIRequestContext, scheduleId: string, executionId = '') {
  return (await json<{ executions: Execution[] }>(api.get(`${endpoint}/executions`, { params: { scheduleId, executionId } }))).executions
}

async function completed(api: APIRequestContext, scheduleId: string, executionId: string) {
  await expect.poll(async () => (await executions(api, scheduleId, executionId))[0]?.status, { timeout: 20_000 }).toBe('completed')
  return (await executions(api, scheduleId, executionId))[0]
}

async function createAgent(api: APIRequestContext) {
  const fixture = fileURLToPath(new URL('../fixtures/timer-agent.cjs', import.meta.url))
  const { agent } = await json<{ agent: { agent_id: string } }>(api.post('/api/v1/agent/custom', {
    data: {
      display_name: 'Timer deterministic ACP',
      definition: { bin: process.execPath, acp_program: process.execPath, acp_args: [fixture] },
    },
  }))
  return agent.agent_id
}

test('creates and runs a Prompt timer in Web and opens its archived Job from the execution list', async ({ page, request }) => {
  test.setTimeout(65_000)
  const agentId = await createAgent(request)
  const name = `Browser Prompt ${randomUUID()}`
  await page.addInitScript(() => localStorage.setItem('quartet-language', 'en'))
  await page.goto('/')
  await expect(page.getByTestId('home-job-history')).toBeVisible()
  await expect(page.getByText('Scheduled Tasks', { exact: true })).toHaveCount(0)
  await expect(page.getByLabel('Hide scheduled tasks')).toHaveCount(0)
  await page.getByRole('button', { name: 'Timer management' }).click()
  await page.getByRole('button', { name: 'New timer' }).click()
  const modal = page.getByRole('dialog')
  await modal.getByLabel('Name', { exact: true }).fill(name)
  await modal.getByLabel('Execution type').selectOption('prompt')
  await modal.getByLabel('Prompt', { exact: true }).fill('Summarize the timer test')
  await modal.getByLabel('Agent', { exact: true }).fill(agentId)
  await modal.getByLabel('Model', { exact: true }).fill('timer-test-model')
  await modal.getByRole('combobox', { name: 'Frequency' }).selectOption('')
  await modal.getByLabel('Cron expression').fill('0 0 1 1 *')
  await modal.getByLabel('Execution limit').fill('2')
  await modal.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(modal).toContainText('Confirm execution settings')
  await expect(modal).toContainText('Summarize the timer test')
  const creation = page.waitForResponse(response => response.url().endsWith('/schedule/create') && response.ok())
  await modal.getByRole('button', { name: 'Confirm and enable' }).click()
  const { schedule } = await (await creation).json() as { schedule: Schedule }
  const plan = page.getByRole('button', { name: new RegExp(name) })
  await expect(plan).toContainText('Enabled')

  const results: Execution[] = []
  for (let n = 0; n < 2; n++) {
    if (n > 0) {
      await page.getByRole('button', { name: 'Timers', exact: true }).click()
      if (await plan.getAttribute('aria-expanded') !== 'true') await plan.click()
    }
    const launch = page.waitForResponse(response => response.url().endsWith('/run') && response.ok())
    await page.getByRole('button', { name: 'Run now', exact: true }).click()
    const entry = await (await launch).json() as Execution
    results.push(await completed(request, schedule.id, entry.id))
  }
  expect(results[0].jobId).not.toBe(results[1].jobId)
  const saved = await json<Schedule>(request.get(`${endpoint}/${schedule.id}`))
  expect(saved).toMatchObject({ planState: 'ended', runCount: 2 })

  await json(request.delete(`${endpoint}/${schedule.id}`))
  await page.goto(`/?view=timers&timerTab=executions&timerScheduleId=${schedule.id}`)
  await expect(page.locator('.timer-tabs').getByRole('button')).toHaveText(['Timers', 'Executions'])
  await expect(page.locator('.timer-columns > span')).toHaveText(['Name', 'Job', 'Triggered at', 'Trigger', 'Status'])
  const jobLinks = page.getByRole('link', { name: 'View Job' })
  await expect(jobLinks).toHaveCount(2)
  await expect(jobLinks.nth(1)).toHaveAttribute('href', new RegExp(`jobId=${results[0].jobId}&`))
  const [jobPage] = await Promise.all([page.waitForEvent('popup'), jobLinks.first().click()])
  await expect(jobPage.getByTestId('job-chat')).toHaveAttribute('data-job-id', results[1].jobId!)
  await expect(jobPage.getByText('Timer received: Summarize the timer test', { exact: false }).last()).toBeVisible()
  await expect(jobLinks).toHaveCount(2)
  await jobPage.close()
  await page.getByRole('button', { name: new RegExp(name) }).first().click()
  await expect(page.locator('.timer-execution-detail')).toBeVisible()
  const measured = await executions(request, schedule.id)
  expect(measured.map(entry => entry.id)).toEqual(results.map(entry => entry.id).reverse())
  await page.goto('/')
  await expect(page.getByTestId('home-job-history')).toBeVisible()
  await expect(page.getByText(name, { exact: true })).toHaveCount(0)
})

async function configureRole(api: APIRequestContext, permissions: string[], baseURL = e2eBackendURL) {
  const username = `timer-${randomUUID().slice(0, 8)}`
  const password = `Test-${randomUUID()}`
  const { role } = await json<{ role: { id: string } }>(api.post('/api/v1/roles', {
    data: { name: username, permissions },
  }))
  await json(api.post('/api/v1/users', { data: { username, displayName: username, password, roleIds: [role.id] } }))
  const context = await playwrightRequest.newContext({ baseURL })
  try {
    const { csrfToken } = await json<{ csrfToken: string }>(context.post('/api/v1/auth/login', { data: { username, password } }))
    const headers = { 'X-CSRF-Token': csrfToken }
    await json(context.put('/api/v1/auth/password', {
      headers, data: { currentPassword: password, newPassword: `Permanent-${randomUUID()}` },
    }))
    const principal = await json<{ csrfToken: string }>(context.get('/api/v1/auth/me'))
    return { context, headers: { 'X-CSRF-Token': principal.csrfToken } }
  } catch (err) {
    await context.dispose()
    throw err
  }
}

test('checks timer permissions, freezes the version, and deduplicates concurrent requests', async ({ request, page }) => {
  test.setTimeout(65_000)
  const workflow = await newGraph(request, true)
  const timer = await newTimer(request, { graphWorkflowId: workflow.id })
  const { context: limited, headers } = await configureRole(request, ['schedule.write', 'schedule.read', 'workflow.write', 'workflow.read', 'workspace.read'])
  try {
    const body = { name: 'Config-only Graph timer', cronExpr: '0 0 1 1 *', kind: 'graph', graphWorkflowId: workflow.id }
    expect((await limited.post(`${endpoint}/create`, { headers, data: { ...body, enabled: true } })).status()).toBe(403)
    const disabled = await json<{ schedule: Schedule }>(limited.post(`${endpoint}/create`, { headers, data: { ...body, enabled: false } }))
    expect(disabled.schedule.planState).toBe('disabled')
    expect((await limited.put(`${endpoint}/${disabled.schedule.id}`, { headers, data: { enabled: true } })).status()).toBe(403)
    expect((await limited.put(`${endpoint}/${timer.id}`, { headers, data: { maxRuns: 3 } })).status()).toBe(403)
    expect((await limited.post(`${endpoint}/${timer.id}/run`, { headers, data: { clientRequestId: 'forbidden' } })).status()).toBe(403)
    expect((await limited.get(`${endpoint}/sessions`)).status()).toBe(403)

    await json(limited.put(`/api/v1/graph/workflow/${workflow.id}`, {
      headers, data: { name: 'Revised Timer Graph', updatedAt: workflow.updatedAt, config: graph('updated-timer-graph', true) },
    }))
    let current = await json<Schedule>(request.get(`${endpoint}/${timer.id}`))
    expect(current.planState).toBe('pendingApproval')
    expect((await request.post(`${endpoint}/${timer.id}/run`, { data: { clientRequestId: 'pending' } })).status()).toBe(409)
    expect((await request.put(`${endpoint}/${timer.id}`, { data: { enabled: true, expectedApproval: timer.approvalPreview } })).status()).toBe(409)
    current = await json<Schedule>(request.put(`${endpoint}/${timer.id}`, { data: { enabled: true, expectedApproval: current.approvalPreview } }))
    expect(current.planState).toBe('enabled')

    const run = (id: string) => request.post(`${endpoint}/${timer.id}/run`, { data: { clientRequestId: id } })
    const first = await Promise.all([run('same-submission'), run('same-submission')])
    const [a, b] = await Promise.all(first.map(reply => json<Execution>(Promise.resolve(reply))))
    expect(a.id).toBe(b.id)
    expect((await run('concurrency')).status()).toBe(409)
    const record = await completed(request, timer.id, a.id)
    expect((await executions(request, timer.id))).toHaveLength(1)
    expect(record.jobId).toBeTruthy()
    const sessions = await json<{ sessions: { nodeId: string }[] }>(request.get(`${endpoint}/sessions`, { params: { executionId: a.id } }))
    expect(sessions.sessions.map(session => session.nodeId)).toContain('shell')
    await page.addInitScript(() => localStorage.setItem('quartet-language', 'en'))
    await page.goto(`/?view=timers&timerTab=executions&timerScheduleId=${timer.id}&timerExecutionId=${a.id}`)
    const [jobPage] = await Promise.all([page.waitForEvent('popup'), page.getByRole('link', { name: 'View Job' }).click()])
    await expect(jobPage.getByTestId('job-chat')).toHaveAttribute('data-job-id', record.jobId!)
    await expect(jobPage.getByTestId('graph-run-progress')).toBeVisible()
    await jobPage.close()
  } finally {
    await limited.dispose()
  }
})

async function allocatePort() {
  // Stay below the OS's ephemeral client-port range. The backend releases
  // its pre-bind listener briefly before Hertz installs the final listener.
  for (let attempt = 0; attempt < 20; attempt++) {
    const port = randomInt(26000, 34000)
    const server = net.createServer()
    server.listen(port, '127.0.0.1')
    try {
      await once(server, 'listening')
      await new Promise<void>((resolve, reject) => {
        server.close(error => error ? reject(error) : resolve())
      })
      return port
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code !== 'EADDRINUSE') throw err
    }
  }
  throw new Error('No free timer E2E loopback port')
}

async function isolatedTimerBackend(info: TestInfo, label = 'isolated') {
  const { backendBinary, repoRoot } = JSON.parse(await fs.readFile(path.join(process.env.QUARTET_E2E_RUN_DIR!, 'env.json'), 'utf8')) as {
    backendBinary: string; repoRoot: string;
  }
  const localMemory = info.outputPath(label, 'memory')
  await fs.mkdir(localMemory, { recursive: true })
  const certs = info.outputPath(label, 'certs-empty')
  await fs.mkdir(certs, { recursive: true })
  const port = await allocatePort()
  const address = `http://127.0.0.1:${port}`
  const logPath = info.outputPath(label, 'backend.log')
  const log = await fs.open(logPath, 'a')
  let backend: ChildProcess | undefined
  const stop = async () => {
    if (!backend || backend.exitCode !== null || backend.signalCode !== null) return
    const exited = once(backend, 'exit')
    backend.kill('SIGKILL')
    await exited
  }
  const start = async () => {
    const logStart = (await log.stat()).size
    backend = spawn(backendBinary, [], {
      cwd: repoRoot, stdio: ['ignore', log.fd, log.fd],
      env: { ...process.env, LOCAL_MEMORY: localMemory, QUARTET_CERTS_DIR: certs, QUARTET_LISTEN_ADDR: `127.0.0.1:${port}` },
    })
    // The backend briefly reserves its port before the HTTP listener starts.
    // Wait for the actual listener before opening any network connection.
    await expect.poll(async () => {
      const recentLog = (await fs.readFile(logPath)).subarray(logStart).toString('utf8')
      return recentLog.includes(`Server is running on ${address}`)
    }, { timeout: 15_000 }).toBe(true)
  }
  const close = async () => {
    await stop()
    await log.close()
  }
  try {
    await start()
  } catch (err) {
    await close()
    throw err
  }
  return { address, localMemory, repoRoot, start, stop, close }
}

async function initIsolated(api: APIRequestContext) {
  const credentials = { username: 'timer-admin', password: 'Timer-Test-Password-1' }
  return json<{ csrfToken: string }>(api.post('/api/v1/auth/init', {
    data: { ...credentials, confirmPassword: credentials.password },
  }))
}

async function writeJSON(filename: string, data: object) {
  await fs.mkdir(path.dirname(filename), { recursive: true })
  await fs.writeFile(filename, JSON.stringify(data))
}

// eslint-disable-next-line no-empty-pattern
test('recovers accepted Jobs and releases ambiguous runs after a real restart', async ({}, info) => {
  test.setTimeout(100_000)
  const server = await isolatedTimerBackend(info)
  const guest = await playwrightRequest.newContext({ baseURL: server.address })
  let api: APIRequestContext | undefined
  try {
    const principal = await initIsolated(guest)
    api = await playwrightRequest.newContext({
      baseURL: server.address, storageState: await guest.storageState(), extraHTTPHeaders: { 'X-CSRF-Token': principal.csrfToken },
    })
    const workflow = await newGraph(api)
    const repeat = await newTimer(api, { graphWorkflowId: workflow.id, cronExpr: '0 0 1 1 *' })
    const agentId = await createAgent(api)
    const prompt = await newTimer(api, {
      kind: 'prompt', prompt: 'Continue the saved Prompt Job', agentId, modelId: 'timer-test-model',
    })
    const launch = await json<Execution>(api.post(`${endpoint}/${repeat.id}/run`, { data: { clientRequestId: 'launched-before-restart' } }))
    const alreadyLaunched = await completed(api, repeat.id, launch.id)
    const storage = path.join(server.localMemory, 'var', 'quartet', 'state', 'schedules', 'timers')
    const repeatFile = path.join(storage, repeat.id + '.json')
    const ledger = JSON.parse(await fs.readFile(repeatFile, 'utf8'))
    const promptFile = path.join(storage, prompt.id + '.json')
    const promptLedger = JSON.parse(await fs.readFile(promptFile, 'utf8'))
    const observed = ledger.entries.find((entry: Execution) => entry.id === alreadyLaunched.id)
    observed.status = 'dispatching'
    delete observed.jobId
    const acceptedID = 'run-restart-before-job'
    const pendingID = 'run-restart-before-dispatch'
    const ambiguousID = 'run-restart-unknown'
    const fixtureJobID = (id: string) => 'job-idem-' + createHash('sha256').update('timer:' + id).digest('hex').slice(0, 32)
    const originalTime = new Date(Date.now() - 5000).toISOString()
    const seed = (id: string, status: string) => ({
      id, scheduleId: repeat.id, kind: 'graph', name: repeat.name, workflowId: workflow.id, workspaceId: repeat.workspaceId,
      trigger: 'manual', triggeredAt: originalTime, status, target: ledger.approvedTarget,
    })
    await server.stop()
    ledger.entries = [
      observed,
      seed(acceptedID, 'accepted'),
      seed(pendingID, 'dispatching'),
      seed(ambiguousID, 'dispatching'),
    ]
    // The production Job writer has persisted the Job, but no graph is bound yet.
    const jobID = fixtureJobID(pendingID)
    const jobDir = path.join(server.localMemory, 'quartet', 'data', 'workspaces', repeat.workspaceId, 'jobs', jobID)
    await writeJSON(path.join(jobDir, '.meta', 'job.json'), {
      id: jobID, scheduleId: repeat.id, title: 'Prior timer Job', mode: 'graph', status: 'pending',
      workspaceId: repeat.workspaceId, workdir: repeat.workdir, createdAt: originalTime, updatedAt: originalTime,
      creationClientMessageId: pendingID, creationPayloadHash: pendingID,
    })
    await writeJSON(repeatFile, ledger)

    const promptID = 'run-restart-prompt-before-dispatch'
    const promptJobID = fixtureJobID(promptID)
    promptLedger.entries = [{
      ...seed(promptID, 'accepted'), scheduleId: prompt.id, name: prompt.name, kind: 'prompt',
      workspaceId: prompt.workspaceId, workflowId: undefined, target: promptLedger.approvedTarget,
    }]
    const promptJobDir = path.join(server.localMemory, 'quartet', 'data', 'workspaces', prompt.workspaceId, 'jobs', promptJobID)
    await writeJSON(path.join(promptJobDir, '.meta', 'job.json'), {
      id: promptJobID, scheduleId: prompt.id, title: 'Prior Prompt Job', mode: 'interactive', status: 'pending',
      workspaceId: prompt.workspaceId, workdir: prompt.workdir, createdAt: originalTime, updatedAt: originalTime,
      creationClientMessageId: promptID, creationPayloadHash: promptID,
    })
    await writeJSON(promptFile, promptLedger)

    await server.start()
    expect((await completed(api, repeat.id, alreadyLaunched.id)).jobId).toBe(alreadyLaunched.jobId)
    expect((await completed(api, prompt.id, promptID)).jobId).toBe(promptJobID)
    expect((await json<Schedule>(api.get(`${endpoint}/${prompt.id}`))).runCount).toBe(1)
    expect((await completed(api, repeat.id, pendingID)).jobId).toBe(jobID)
    expect((await completed(api, repeat.id, acceptedID)).jobId).toBe(fixtureJobID(acceptedID))
    const interrupted = (await executions(api, repeat.id, ambiguousID))[0]
    expect(interrupted.status).toBe('interrupted')
    expect(interrupted.error).toContain('could not confirm')
    const followUp = await json<Execution>(api.post(`${endpoint}/${repeat.id}/run`, { data: { clientRequestId: 'after-restart' } }))
    await completed(api, repeat.id, followUp.id)
    expect((await json<Schedule>(api.get(`${endpoint}/${repeat.id}`))).runCount).toBe(5)
    const files = await fs.readdir(path.dirname(jobDir))
    expect(files.filter(id => id === jobID)).toHaveLength(1)

    const cli = info.outputPath('quartet-cli')
    execFileSync('go', ['build', '-o', cli, './cmd/quartet-cli'], { cwd: server.repoRoot })
    const cliSessionFile = info.outputPath('cli-session.json')
    const { cookies } = await guest.storageState()
    const cookie = cookies.find(item => item.name === 'quartet_session')?.value
    expect(cookie).toBeTruthy()
    await fs.writeFile(cliSessionFile, JSON.stringify({
      sessions: { [server.address]: { cookie, csrfToken: principal.csrfToken } },
    }), { mode: 0o600 })
    const cliEnv = { ...process.env, QUARTET_BASE_URL: server.address, QUARTET_CLI_SESSION_FILE: cliSessionFile }
    const fromCLI = JSON.parse(execFileSync(cli, ['schedule', 'create', '--name', 'CLI timer', '--cron', '0 8 * * *',
      '--workflow', workflow.id, '--timezone', 'UTC', '--disabled'], { env: cliEnv, encoding: 'utf8' })) as Schedule
    expect((await json<Schedule>(api.get(`${endpoint}/${fromCLI.id}`))).planState).toBe('disabled')

    const listed = JSON.parse(execFileSync(cli, ['schedule', 'get', repeat.id], { env: cliEnv, encoding: 'utf8' })) as Schedule
    expect(listed.runCount).toBe(5)
    await fs.rm(cliSessionFile)

    const sameUser = await configureRole(api, [
      'schedule.write', 'schedule.read', 'schedule.execute', 'workflow.read', 'workspace.read',
      'job.execute', 'job.read', 'agent.read',
    ], server.address)
    try {
      const promptOnly = await json<Execution>(sameUser.context.post(`${endpoint}/${prompt.id}/run`, {
        headers: sameUser.headers, data: { clientRequestId: 'prompt-without-graph-permission' },
      }))
      await completed(api, prompt.id, promptOnly.id)
      expect((await sameUser.context.post(`${endpoint}/${repeat.id}/run`, {
        headers: sameUser.headers, data: { clientRequestId: 'graph-without-graph-permission' },
      })).status()).toBe(403)
    } finally {
      await sameUser.context.dispose()
    }
  } finally {
    await api?.dispose()
    await guest.dispose()
    await server.close()
  }
})

test('fires a one-time plan once and records a different one-time plan missed during downtime', async ({ request }, info) => {
  test.setTimeout(170_000)
  const server = await isolatedTimerBackend(info)
  const guest = await playwrightRequest.newContext({ baseURL: server.address })
  let offlineAPI: APIRequestContext | undefined
  try {
    const principal = await initIsolated(guest)
    offlineAPI = await playwrightRequest.newContext({
      baseURL: server.address, storageState: await guest.storageState(), extraHTTPHeaders: { 'X-CSRF-Token': principal.csrfToken },
    })
    const [onlineWorkflow, offlineWorkflow] = await Promise.all([newGraph(request), newGraph(offlineAPI)])
    const at = nextMinute()
    const [online, offline, manual] = await Promise.all([
      newTimer(request, { graphWorkflowId: onlineWorkflow.id, runAt: at.toISOString() }),
      newTimer(offlineAPI, { graphWorkflowId: offlineWorkflow.id, runAt: at.toISOString() }),
      newTimer(request, { graphWorkflowId: onlineWorkflow.id, runAt: at.toISOString() }),
    ])
    expect(online.nextRunAt).toBe(at.getTime())
    expect(offline.nextRunAt).toBe(at.getTime())
    const launch = await json<Execution>(request.post(`${endpoint}/${manual.id}/run`, { data: { clientRequestId: 'early-single' } }))
    await server.stop()
    await expect.poll(async () => (await executions(request, online.id))[0]?.status, {
      timeout: 85_000, intervals: [250, 1_000, 2_000],
    }).toBe('completed')
    const automatic = (await executions(request, online.id))[0]
    expect(automatic.jobId).toBeTruthy()
    expect((await executions(request, manual.id)).map(entry => entry.id)).toEqual([launch.id])
    expect((await json<Schedule>(request.get(`${endpoint}/${online.id}`))).runCount).toBe(1)

    // A missed minute is the whole minute, not just the first instant in it.
    // The isolated backend stays down until that minute has elapsed.
    await expect.poll(() => Date.now(), { timeout: 65_000, intervals: [1_000, 2_000] }).toBeGreaterThanOrEqual(at.getTime() + 60_000)
    await server.start()
    expect((await executions(offlineAPI, offline.id)).map(entry => entry.status)).toEqual(['missed'])
    expect(await json<Schedule>(offlineAPI.get(`${endpoint}/${offline.id}`))).toMatchObject({ runCount: 0, planState: 'ended' })
  } finally {
    await offlineAPI?.dispose()
    await guest.dispose()
    await server.close()
  }
})

// eslint-disable-next-line no-empty-pattern
test('keeps activation local when a definition is synchronized between backends', async ({}, info) => {
  test.setTimeout(45_000)
  const first = await isolatedTimerBackend(info, 'first')
  let second: Awaited<ReturnType<typeof isolatedTimerBackend>> | undefined
  const contexts: APIRequestContext[] = []
  const connect = async (address: string) => {
    const guest = await playwrightRequest.newContext({ baseURL: address })
    contexts.push(guest)
    const principal = await initIsolated(guest)
    const api = await playwrightRequest.newContext({
      baseURL: address, storageState: await guest.storageState(), extraHTTPHeaders: { 'X-CSRF-Token': principal.csrfToken },
    })
    contexts.push(api)
    return api
  }
  try {
    second = await isolatedTimerBackend(info, 'second')
    const [a, b] = await Promise.all([connect(first.address), connect(second.address)])
    const workflow = await newGraph(a)
    const timer = await newTimer(a, { graphWorkflowId: workflow.id })
    const copyConfig = async (from: string, to: string, folder: string, id: string) => {
      const source = path.join(from, 'quartet', 'config', folder, id + '.json')
      const target = path.join(to, 'quartet', 'config', folder, id + '.json')
      await writeJSON(target, JSON.parse(await fs.readFile(source, 'utf8')))
    }
    await copyConfig(first.localMemory, second.localMemory, 'graph-workflows', workflow.id)
    await copyConfig(first.localMemory, second.localMemory, 'schedules', timer.id)
    expect(await json<Schedule>(b.get(`${endpoint}/${timer.id}`))).toMatchObject({ planState: 'disabled', runCount: 0 })

    const limited = await configureRole(b, ['schedule.write', 'schedule.read', 'workflow.read', 'workspace.read'], second.address)
    contexts.push(limited.context)
    await json(limited.context.put(`${endpoint}/${timer.id}`, { headers: limited.headers, data: { maxRuns: 3 } }))
    await copyConfig(second.localMemory, first.localMemory, 'schedules', timer.id)
    const pending = await json<Schedule>(a.get(`${endpoint}/${timer.id}`))
    expect(pending.planState).toBe('pendingApproval')
    await first.stop()
    await first.start()
    expect(await json<Schedule>(a.get(`${endpoint}/${timer.id}`))).toMatchObject({ planState: 'pendingApproval', runCount: 0 })
    expect((await a.post(`${endpoint}/${timer.id}/run`, { data: { clientRequestId: 'pending-sync' } })).status()).toBe(409)
    expect(await executions(a, timer.id)).toHaveLength(0)
    await json(a.put(`${endpoint}/${timer.id}`, { data: { enabled: true, expectedApproval: pending.approvalPreview } }))
    const launch = await json<Execution>(a.post(`${endpoint}/${timer.id}/run`, { data: { clientRequestId: 'confirmed-sync' } }))
    await completed(a, timer.id, launch.id)
  } finally {
    for (const context of contexts) await context.dispose()
    await second?.close()
    await first.close()
  }
})
