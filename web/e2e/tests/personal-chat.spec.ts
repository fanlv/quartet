import fs from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'
import { createServer } from 'node:net'
import { randomUUID } from 'node:crypto'
import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
import { e2eAuthHeaders, e2eBackendURL } from '../fixtures/e2e-environment'
import type { AgentInfo } from '../../src/types'
import type { BusinessAgent, ChatMessage, ChatRun, ChatSend, ChatSnapshot } from '../../src/types/personalChat'

// Real browser -> HTTP/SSE -> JSONL -> installed ACP subprocess. No model or
// chat API is mocked. Only the explicit network-loss case interrupts a response
// AFTER the real server has accepted it, so retry/deduplication is exercised.
test.describe.configure({ mode: 'default', timeout: 180_000 })
test.use({ actionTimeout: 10_000 })
let harness: AgentInfo
let aliasHarness: string
const fastReply = 'Reply with exactly PERSONAL_E2E_OK. Do not call any tools.'
const busyReply = 'Use your Bash/shell tool to run `sleep 15` exactly once, then reply BUSY_DONE. Do not do any other work.'

async function json<T>(request: APIRequestContext, url: string, data?: unknown, method = 'POST'): Promise<T> {
  const response = data === undefined ? await request.get(url) : await request.fetch(url, { method, data })
  expect(response.ok(), `${method} ${url}: HTTP ${response.status()}\n${await response.text()}`).toBeTruthy()
  return await response.json() as T
}
const snapshot = (request: APIRequestContext) => json<ChatSnapshot>(request, '/api/v1/chat')
async function open(page: Page) {
  await page.addInitScript(() => localStorage.setItem('quartet-language', 'en'))
  await page.goto('/')
  await expect(page.getByTestId('personal-chat')).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Harness Client', exact: true })).toBeVisible()
}
async function agent(request: APIRequestContext, name: string, systemPrompt = 'Help the user. Follow their requested response format.') {
  return (await json<{ agent: BusinessAgent }>(request, '/api/v1/chat/agents', { id: '', name: `${name}-${randomUUID().slice(0, 6)}`, systemPrompt, harnessId: harness.agent_id, main: false, revision: 0 })).agent
}
async function send(request: APIRequestContext, agentId: string, content = fastReply, extra: Partial<ChatSend> = {}) {
  return (await json<{ run: ChatRun }>(request, '/api/v1/chat/messages', { clientMessageId: randomUUID(), agentId, harnessId: harness.agent_id, content, ...extra })).run
}
async function terminal(request: APIRequestContext, run: ChatRun, expected = 'completed') {
  let value: ChatRun | undefined
  await expect.poll(async () => { value = (await snapshot(request)).runs.find(r => r.id === run.id); return value?.state }, { timeout: 120_000, intervals: [250, 500, 1000] }).toBe(expected)
  return value!
}
async function control(request: APIRequestContext, run: ChatRun, action: string) {
  return json(request, '/api/v1/chat/tasks/control', { taskId: run.taskId, runId: run.id, action })
}
function message(page: Page, id: string) { return page.locator(`[data-message-id="${id}"]`) }
async function selectAgent(page: Page, a: BusinessAgent) {
  await expect(page.getByRole('combobox', { name: 'Business Agent', exact: true }).locator(`option[value="${a.id}"]`)).toHaveCount(1)
  await page.getByRole('combobox', { name: 'Business Agent', exact: true }).selectOption(a.id)
}

// A second isolated process replays copies of this run's files. It never stops
// or restarts the deployed service. Disk-tail/idle fixtures are edited ONLY in
// that copied directory before its process starts.
async function replay(request: APIRequestContext, mutate?: (journal: string, memory: string) => Promise<void>) {
  const runDir = process.env.QUARTET_E2E_RUN_DIR!
  const env = JSON.parse(await fs.readFile(path.join(runDir, 'env.json'), 'utf8')) as { localMemory: string; backendBinary: string; repoRoot: string }
  const memory = await fs.mkdtemp(path.join(os.tmpdir(), 'quartet-personal-e2e-'))
  await fs.cp(env.localMemory, memory, { recursive: true })
  const journal = path.join(memory, 'quartet/data/personal-chat/commits.jsonl')
  if (mutate) await mutate(journal, memory)
  const port = await new Promise<number>(resolve => { const s = createServer(); s.listen(0, '127.0.0.1', () => { const p = (s.address() as { port: number }).port; s.close(() => resolve(p)) }) })
  const output: string[] = []
  const proc = spawn(env.backendBinary, [], { cwd: env.repoRoot, env: { ...process.env, LOCAL_MEMORY: memory, QUARTET_LISTEN_ADDR: `127.0.0.1:${port}`, QUARTET_CERTS_DIR: path.join(memory, 'empty-certs') }, stdio: ['ignore', 'pipe', 'pipe'] })
  proc.stdout.on('data', b => output.push(String(b)))
  proc.stderr.on('data', b => output.push(String(b)))
  const base = `http://127.0.0.1:${port}`
  try {
    await expect.poll(async () => { try { return (await request.get(`${base}/api/v1/health`)).status() } catch { return 0 } }, { timeout: 30_000 }).toBe(200)
  } catch (e) { proc.kill(); await fs.rm(memory, { recursive: true, force: true }); throw new Error(`${String(e)}\n${output.join('')}`) }
  return { base, memory, async close() { await fs.writeFile(test.info().outputPath(`replay-${port}.log`),output.join('')); if (proc.exitCode === null) { proc.kill('SIGTERM'); await new Promise<void>(resolve => { proc.once('exit', () => resolve()); setTimeout(() => { proc.kill('SIGKILL'); resolve() }, 5000).unref() }) } await fs.rm(memory, { recursive: true, force: true }) } }
}

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    const result = await json<{ agent_list: AgentInfo[] }>(request, '/api/v1/agent/list')
    const available = result.agent_list.filter(a => a.available && a.agent_id !== 'eino-cli')
    harness = available.find(a => a.agent_id === 'codebuddy') || available[0]
    return !!harness
  }, { timeout: 60_000 }).toBe(true)
  const catalog = await json<{ agents: Array<{ agent_id: string; display_name: string }> }>(request, '/api/v1/agent/catalog')
  const existing = catalog.agents.find(a => a.display_name === 'Personal E2E alternate harness')
  if (existing) aliasHarness = existing.agent_id
  else {
    const created = await json<{ agent: { agent_id: string } }>(request, '/api/v1/agent/custom', { display_name: 'Personal E2E alternate harness', icon_url: '', supports_headless_print: true, definition: { bin: 'codebuddy', acp_program: 'env', acp_args: ['codebuddy', '--acp'] } })
    aliasHarness = created.agent.agent_id
  }
})

test('home keeps the existing message/composer style and exposes the default main Agent', async ({ page, request }) => {
  await open(page)
  const value = await snapshot(request)
  expect(value.agents.filter(a => a.main)).toHaveLength(1)
  expect(value.settings.idleMinutes).toBe(30)
  expect(value.workdir).toBe(os.homedir())
  await expect(page.getByRole('heading', { name: 'Group chat', exact: true })).toBeVisible()
  await expect(page.locator('[data-testid="workspace-footer"]')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeDisabled()
  expect(await page.locator('.personal-composer').evaluate(e => getComputedStyle(e).borderRadius)).toBe('16px')
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.screenshot({ path: test.info().outputPath('home-desktop.png') })
})

test('business Agent create/edit/delete works and stale revisions report the complete error', async ({ page, request }) => {
  await open(page)
  await page.getByRole('button', { name: 'Add Agent', exact: true }).click()
  const panel = page.getByRole('complementary', { name: 'Business Agents', exact: true })
  await panel.getByLabel('Name', { exact: true }).fill('UI reviewer')
  await panel.getByLabel('SystemPrompt', { exact: true }).fill('PRIVATE_PROMPT_E2E: Answer succinctly.')
  await panel.getByLabel('Default Harness Client', { exact: true }).selectOption(harness.agent_id)
  await panel.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(panel).toHaveCount(0)
  const saved = (await snapshot(request)).agents.find(a => a.name === 'UI reviewer')!
  expect(saved.systemPrompt).toContain('PRIVATE_PROMPT_E2E')
  const row = page.locator('.personal-agent-row').filter({ hasText: 'UI reviewer' })
  await row.getByRole('button', { name: 'Edit Agent', exact: true }).click()
  await panel.getByLabel('Name', { exact: true }).fill('UI reviewer renamed')
  await panel.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.locator('.personal-agent-row').filter({ hasText: 'UI reviewer renamed' })).toBeVisible()
  const stale = await request.post('/api/v1/chat/agents', { data: { ...saved, name: 'Stale update' } })
  expect(stale.status()).toBe(400)
  expect(await stale.text()).toMatch(/changed|revision|refresh/i)
  await page.locator('.personal-agent-row').filter({ hasText: 'UI reviewer renamed' }).getByRole('button', { name: 'Edit Agent', exact: true }).click()
  await panel.getByRole('button', { name: 'Delete Agent', exact: true }).click()
  await expect(panel).toHaveCount(0)
  expect((await snapshot(request)).agents.find(a => a.id === saved.id)?.deleted).toBe(true)
  const main = (await snapshot(request)).agents.find(a => a.main)!
  const denied = await request.post('/api/v1/chat/agents', { data: { ...main, deleted: true } })
  expect(denied.status()).toBe(400)
})

test('@ routes to a business Agent, real ACP replies, and later messages reuse its Session', async ({ page, request }) => {
  const a = await agent(request, 'Mention', 'PRIVATE_MENTION_PROMPT: Help the user and use the requested tools.')
  await open(page)
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill(`@${a.name}`)
  await page.getByRole('listbox', { name: 'Business Agent', exact: true }).getByRole('option').click()
  await expect(page.getByRole('combobox', { name: 'Business Agent', exact: true })).toHaveValue(a.id)
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill(fastReply)
  const accepted = page.waitForResponse(r => r.url().endsWith('/api/v1/chat/messages') && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  const first = (await (await accepted).json() as { run: ChatRun }).run
  expect(first.agentId).toBe(a.id)
  await terminal(request, first)
  await expect(message(page, first.replyId)).toContainText('PERSONAL_E2E_OK')
  await expect(message(page, first.messageId).locator('.user-bubble')).toBeVisible()
  await expect(message(page, first.replyId).locator('.assistant-bubble')).toBeVisible()
  const second = await send(request, a.id)
  await terminal(request, second)
  expect(second.sessionId).toBe(first.sessionId)
  const detail = await json<{ message: ChatMessage }>(request, `/api/v1/chat/messages/${first.messageId}`)
  expect(detail.message.rawContent).toBeFalsy()
  expect(await page.locator('.personal-timeline').innerText()).not.toContain('PRIVATE_MENTION_PROMPT')
  await page.screenshot({ path: test.info().outputPath('chat-bubbles-desktop.png') })
})

test('normal/debug display toggles reveal raw system-reminder without creating a Session or replaying messages', async ({ page, request }) => {
  const a = await agent(request, 'Debug', 'SECRET_DEBUG_PROMPT_E2E')
  const run = await send(request, a.id, `${fastReply}\n<system-reminder>HIDDEN_USER_REMINDER_E2E</system-reminder>`)
  await terminal(request, run)
  await open(page)
  const before = await snapshot(request)
  await expect(message(page, run.messageId)).not.toContainText('HIDDEN_USER_REMINDER_E2E')
  await page.getByRole('button', { name: 'Chat settings', exact: true }).last().click()
  const panel = page.getByRole('complementary', { name: 'Chat settings', exact: true })
  await panel.getByLabel('Home message display', { exact: true }).selectOption('debug')
  await panel.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(message(page, run.messageId)).toContainText('SECRET_DEBUG_PROMPT_E2E')
  await expect(message(page, run.messageId)).toContainText('HIDDEN_USER_REMINDER_E2E')
  await page.reload()
  await expect(message(page, run.messageId)).toContainText('<system-reminder>')
  const after = await snapshot(request)
  expect(after.sessions.map(s => s.id)).toEqual(before.sessions.map(s => s.id))
  expect(after.runs.map(r => r.id)).toEqual(before.runs.map(r => r.id))
  const search = await json<{ total: number }>(request, '/api/v1/chat/search?query=SECRET_DEBUG_PROMPT_E2E')
  expect(search.total).toBe(0)
  await json(request, '/api/v1/chat/settings', { ...after.settings, debug: false }, 'PUT')
  await expect(message(page, run.messageId)).not.toContainText('SECRET_DEBUG_PROMPT_E2E')
})

test('attachment-only messages upload, send and reopen the stored file', async ({ page, request }) => {
  const a = await agent(request, 'Attachments')
  await open(page)
  await selectAgent(page, a)
  await page.locator('.personal-send-controls input[type="file"]').setInputFiles({ name: 'personal-e2e-notes.txt', mimeType: 'text/plain', buffer: Buffer.from('Personal Agent attachment verification') })
  await expect(page.locator('.personal-file')).toContainText('personal-e2e-notes.txt')
  await expect(page.getByRole('button', { name: 'Send', exact: true })).toBeEnabled()
  const accepted = page.waitForResponse(r => r.url().endsWith('/api/v1/chat/messages') && r.request().method() === 'POST')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  const run = (await (await accepted).json() as { run: ChatRun }).run
  const value = await json<{ message: ChatMessage }>(request, `/api/v1/chat/messages/${run.messageId}`)
  expect(value.message.content).toContain('[personal-e2e-notes.txt](')
  await expect(message(page, run.messageId).getByRole('link', { name: 'personal-e2e-notes.txt' })).toBeVisible()
  const filePath = value.message.content.match(/\]\(([^)]+)\)/)![1]
  const file = await request.get(`/api/v1/serve-file?path=${encodeURIComponent(filePath)}`)
  expect(await file.text()).toBe('Personal Agent attachment verification')
  await terminal(request, run)
})

test('lost acceptance response keeps the draft and retries the exact immutable request once', async ({ page, request }) => {
  const a = await agent(request, 'Retry')
  await open(page)
  await selectAgent(page, a)
  const bodies: ChatSend[] = []
  let first = true
  await page.route('**/api/v1/chat/messages', async route => {
    bodies.push(route.request().postDataJSON() as ChatSend)
    if (first) { first = false; await route.fetch(); await route.abort('connectionreset') }
    else await route.continue()
  })
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill(fastReply)
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(page.getByRole('alert')).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Message', exact: true })).toHaveValue(fastReply)
  await page.getByRole('button', { name: /^(Send|Add to queue)$/ }).click()
  await expect(page.getByRole('textbox', { name: 'Message', exact: true })).toHaveValue('')
  expect(bodies).toHaveLength(2)
  expect(bodies[1]).toEqual(bodies[0])
  const runs = (await snapshot(request)).runs.filter(r => r.messageId === bodies[0].clientMessageId)
  expect(runs).toHaveLength(1)
  await terminal(request, runs[0])
})

test('FIFO queue supports withdrawal, confirmed stop-and-send, and resuming the older queue', async ({ request, page }) => {
  const a = await agent(request, 'Queue')
  const first = await send(request, a.id, busyReply)
  await expect.poll(async () => (await snapshot(request)).runs.find(r => r.id === first.id)?.state).toBe('running')
  const withdraw = await send(request, a.id)
  const queued = await send(request, a.id)
  expect(withdraw.state).toBe('queued')
  expect(queued.state).toBe('queued')
  await open(page)
  await selectAgent(page, a)
  await expect(message(page, withdraw.messageId).getByRole('button', { name: 'Withdraw', exact: true })).toBeVisible()
  await message(page, withdraw.messageId).getByRole('button', { name: 'Withdraw', exact: true }).click()
  await terminal(request, withdraw, 'withdrawn')
  const priority = await send(request, a.id, fastReply, { stopAndSend: true })
  await terminal(request, first, 'stopped')
  await terminal(request, priority)
  let value = await snapshot(request)
  expect(value.tasks.find(t => t.id === first.taskId)?.paused).toBe(true)
  expect(value.runs.find(r => r.id === queued.id)?.state).toBe('queued')
  await control(request, queued, 'resume')
  await terminal(request, queued)
  value = await snapshot(request)
  expect(value.runs.find(r => r.id === first.id)?.state).toBe('stopped')
})

test('independent tasks execute alongside the current task and mobile can return to either task', async ({ request, page }) => {
  const a = await agent(request, 'Parallel')
  const first = await send(request, a.id, busyReply)
  const other = await send(request, a.id, fastReply, { independent: true })
  expect(other.taskId).not.toBe(first.taskId)
  await expect.poll(async () => {
    const value = await snapshot(request)
    return value.runs.filter(r => [first.id, other.id].includes(r.id) && r.state === 'running').length
  }).toBe(2)
  await page.setViewportSize({ width: 390, height: 844 })
  await open(page)
  await page.getByRole('button', { name: 'Agents and tasks', exact: true }).click()
  await expect(page.locator('.personal-sidebar.is-open')).toBeVisible()
  await page.locator('.personal-task-row').filter({ hasText: fastReply }).getByRole('button').first().click()
  await expect(page.locator('.personal-reply-context')).toContainText('Replying to')
  await expect(page.locator('.personal-sidebar.is-open')).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.screenshot({ path: test.info().outputPath('chat-mobile.png') })
  await control(request, first, 'stop')
  await terminal(request, first, 'stopped')
  await terminal(request, other)
})

test('switching Harness A to B to A always creates new Sessions and preserves queued bindings', async ({ request }) => {
  const a = await agent(request, 'Harness')
  const first = await send(request, a.id, busyReply)
  const second = await send(request, a.id, fastReply, { harnessId: aliasHarness })
  const third = await send(request, a.id)
  expect(new Set([first.sessionId, second.sessionId, third.sessionId]).size).toBe(3)
  await terminal(request, first)
  await terminal(request, second)
  await terminal(request, third)
  const value = await snapshot(request)
  expect(value.sessions.find(s => s.id === second.sessionId)?.harnessId).toBe(aliasHarness)
  expect(value.sessions.find(s => s.id === third.sessionId)?.harnessId).toBe(harness.agent_id)
  expect(value.messages.find(m => m.id === second.messageId)?.sessionId).toBe(second.sessionId)
})

test('thought and tool results collapse below the reply and remain accessible in full', async ({ request, page }) => {
  const a = await agent(request, 'Tools')
  const run = await send(request, a.id, 'Use the Bash tool exactly once to run `printf \'PERSONAL_TOOL_%s\' RESULT_E2E`. Then reply TOOL_REPLY_E2E. Do not do other work.')
  await terminal(request, run)
  await open(page)
  const row = message(page, run.replyId)
  await expect(row).toContainText('TOOL_REPLY_E2E')
  await expect(row.locator('.personal-details')).toHaveCount(0)
  await row.getByRole('button', { name: /Thoughts and tools/ }).click()
  await expect(row.locator('.personal-details')).toBeVisible()
  await expect(row.locator('.personal-details details')).not.toHaveCount(0)
  await row.locator('.personal-details details').first().locator('summary').click()
  await expect(row.locator('.personal-details')).toContainText('PERSONAL_TOOL_RESULT_E2E')
  const full = (await json<{ message: ChatMessage }>(request, `/api/v1/chat/messages/${run.replyId}`)).message
  expect(full.tools?.some(t => t.result.includes('PERSONAL_TOOL_RESULT_E2E'))).toBe(true)
  const hidden = await json<{total:number}>(request,`/api/v1/chat/search?query=PERSONAL_TOOL_RESULT_E2E&agentId=${a.id}`)
  expect(hidden.total).toBe(0)
  const tools = await json<{total:number;hits:Array<{messageId:string}>}>(request,`/api/v1/chat/search?query=PERSONAL_TOOL_RESULT_E2E&agentId=${a.id}&tools=true`)
  expect(tools.total).toBe(1)
  expect(tools.hits[0].messageId).toBe(run.replyId)
  const history = await json<{messages:ChatMessage[]}>(request,`/api/v1/chat/history?sessionId=${run.sessionId}&tools=true`)
  expect(history.messages.find(m=>m.id===run.replyId)?.tools?.some(t=>t.result.includes('PERSONAL_TOOL_RESULT_E2E'))).toBe(true)
  await page.getByRole('button',{name:'Session history',exact:true}).click()
  const panel=page.getByRole('complementary',{name:'Session history',exact:true})
  await panel.getByLabel('Search session history',{exact:true}).fill('PERSONAL_TOOL_RESULT_E2E')
  await panel.getByRole('combobox',{name:'Filter by Agent',exact:true}).selectOption(a.id)
  await panel.getByLabel('Include tool arguments and results',{exact:true}).check()
  await panel.getByRole('button',{name:'Search',exact:true}).click()
  await panel.locator('.personal-hit').first().click()
  await panel.locator('article details').first().locator('summary').click()
  await expect(panel.locator('article details').first()).toContainText('PERSONAL_TOOL_RESULT_E2E')


})

test('Chinese history search supports Agent/time filters, paging, and opening the original conversation', async ({ request, page }) => {
  const a = await agent(request, 'History')
  const text = '历史检索中文验收：确认 JSONL 存储，不使用 SQLite。'+fastReply
  const run = await send(request, a.id, text)
  await terminal(request, run)
  const result = await json<{ hits: Array<{ messageId: string }>; total: number }>(request, `/api/v1/chat/search?query=${encodeURIComponent('中文验收 JSONL')}&agentId=${a.id}&after=1&limit=1`)
  expect(result.hits).toHaveLength(1)
  expect(result.hits[0].messageId).toBe(run.messageId)
  const future = await json<{ total: number }>(request, `/api/v1/chat/search?query=${encodeURIComponent('中文验收')}&after=${Date.now()+60000}`)
  expect(future.total).toBe(0)
  await open(page)
  await page.getByRole('button', { name: 'Session history', exact: true }).click()
  const panel = page.getByRole('complementary', { name: 'Session history', exact: true })
  await panel.getByLabel('Search session history', { exact: true }).fill('中文验收 JSONL')
  await panel.getByRole('button', { name: 'Search', exact: true }).click()
  await panel.locator('.personal-hit').first().click()
  await expect(panel.locator('article')).not.toHaveCount(0)
  await expect(panel.locator('article').first()).toContainText('中文验收')
  await panel.locator('article').first().getByRole('button', { name: 'Reply to task', exact: true }).click()
  await expect(panel).toHaveCount(0)
  await expect(page.locator('.personal-reply-context')).toContainText(a.name)
})

test('Agent searches previous Sessions through the real CLI capability and delegates asynchronously with one return receipt', async ({ request }) => {
  const child = await agent(request, 'Child')
  const parent = await agent(request, 'Coordinator', 'When a subtask result comes back, acknowledge with CHILD_RESULT_RECEIVED. Never delegate a returned result.')
  const historyRun = await send(request, parent.id, 'Run `quartet-cli chat search --query 中文验收` once using Bash. Then reply HISTORY_CLI_OK. Do no other work.')
  await terminal(request, historyRun)
  const historyReply = (await json<{ message: ChatMessage }>(request, `/api/v1/chat/messages/${historyRun.replyId}`)).message
  expect(historyReply.tools?.some(t => t.result.includes('中文验收'))).toBe(true)
  const run = await send(request, parent.id, `Use Bash to execute quartet-cli chat delegate --parent-run <your current execution ID from system-reminder> --agent ${child.id} --content 'Reply CHILD_E2E_DONE only. No tools.' exactly once. Do not wait. Reply DISPATCHED_E2E. When a subtask result returns, reply CHILD_RESULT_RECEIVED and do not delegate again.`)
  await terminal(request, run)
  let childRun: ChatRun | undefined
  await expect.poll(async () => { const v=await snapshot(request); const task=v.tasks.find(t=>t.parentRunId===run.id); childRun=v.runs.find(r=>r.taskId===task?.id); return !!childRun }, { timeout: 60_000 }).toBe(true)
  await terminal(request, childRun!)
  let returned: ChatRun | undefined
  await expect.poll(async () => { const v=await snapshot(request); const rows=v.runs.filter(r=>r.messageId===`result-${childRun!.id}`); expect(rows.length).toBeLessThanOrEqual(1); returned=rows[0]; return returned?.state }, { timeout: 120_000 }).toBe('completed')
  expect(returned?.taskId).toBe(run.taskId)
  const value = await snapshot(request)
  expect(value.runs.find(r=>r.id===childRun!.id)?.state).toBe('completed')
})

test('missing harness configuration exposes the full error and the task can recover', async ({ request, page }) => {
  const a = await agent(request, 'Errors')
  const failed = await send(request, a.id, fastReply, { harnessId: 'eino-cli' })
  const result = await terminal(request, failed, 'failed')
  expect(result.error).toBeTruthy()
  await open(page)
  await expect(message(page, failed.replyId).getByRole('alert')).toHaveText(result.error!)
  const recovered = await send(request, a.id)
  // A failed run intentionally pauses older queued work; resume is explicit.
  await control(request, recovered, 'resume')
  await terminal(request, recovered)
  expect(recovered.sessionId).not.toBe(failed.sessionId)
})

test('JSONL replay preserves history, repairs an incomplete tail, and isolates corrupt chat data from Graph', async ({ request }) => {
  const baseline = await snapshot(request)
  const clean = await replay(request)
  try {
    const restored = await json<ChatSnapshot>(request, `${clean.base}/api/v1/chat`)
    expect(restored.messages.map(m=>m.id)).toEqual(baseline.messages.map(m=>m.id))
    expect(restored.agents).toEqual(baseline.agents)
  } finally { await clean.close() }
  const tail = await replay(request, async file => { await fs.appendFile(file, '{"version":1,"seq":') })
  try {
    const restored = await json<ChatSnapshot>(request, `${tail.base}/api/v1/chat`)
    expect(restored.warnings.join('\n')).toContain('Incomplete uncommitted JSONL tail')
    expect(restored.messages.map(m=>m.id)).toEqual(baseline.messages.map(m=>m.id))
    const files = await fs.readdir(path.join(tail.memory,'quartet/data/personal-chat'))
    expect(files.some(f=>f.includes('incomplete'))).toBe(true)
  } finally { await tail.close() }
  const corrupt = await replay(request, async file => { const text=await fs.readFile(file,'utf8'); const lines=text.split('\n'); lines[1]='{CORRUPT_E2E'; await fs.writeFile(file,lines.join('\n')) })
  try {
    const response = await request.get(`${corrupt.base}/api/v1/chat`)
    expect(response.status()).toBe(503)
    expect(await response.text()).toMatch(/invalid|corrupt|decode|JSON/i)
    expect((await request.get(`${corrupt.base}/api/v1/graph/workflow/list`)).status()).toBe(200)
  } finally { await corrupt.close() }
})

test('idle expiry starts a new Session while awaiting-input tasks keep their existing Session', async ({ request }) => {
  const a = await agent(request, 'Idle')
  const run = await send(request, a.id)
  await terminal(request, run)
  for (const awaitingInput of [false, true]) {
    const copied = await replay(request, async file => {
      const text=await fs.readFile(file,'utf8'); const commits=text.trimEnd().split('\n').map(line=>JSON.parse(line) as { seq:number; tasks?: Array<Record<string,unknown>> })
      const task=(await snapshot(request)).tasks.find(t=>t.id===run.taskId)!
      const commit={version:1,seq:commits.at(-1)!.seq+1,tasks:[{...task,awaitingInput,idleSince:Date.now()-31*60_000}]}
      await fs.appendFile(file,JSON.stringify(commit)+'\n')
    })
    try {
      const next = await json<{ run: ChatRun }>(request, `${copied.base}/api/v1/chat/messages`, { clientMessageId:randomUUID(),agentId:a.id,harnessId:harness.agent_id,content:fastReply })
      if (awaitingInput) expect(next.run.sessionId).toBe(run.sessionId)
      else expect(next.run.sessionId).not.toBe(run.sessionId)
      await json(request, `${copied.base}/api/v1/chat/tasks/control`, {taskId:run.taskId,action:'stop'})
    } finally { await copied.close() }
  }
})

test('multiple tabs converge through one SSE per page and phone/tablet layouts remain usable', async ({ page, context, request }) => {
  await open(page)
  const other = await context.newPage()
  await other.goto('/')
  await expect(other.getByTestId('personal-chat')).toBeVisible()
  const a = await agent(request, 'TwoTabs')
  const run = await send(request, a.id)
  await terminal(request, run)
  await expect(message(page, run.replyId)).toContainText('PERSONAL_E2E_OK')
  await expect(message(other, run.replyId)).toContainText('PERSONAL_E2E_OK')
  for (const width of [1440,768,390]) {
    await page.setViewportSize({width,height:1000})
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBe(true)
    await expect(page.getByRole('textbox',{name:'Message',exact:true})).toBeVisible()
    await expect(message(page,run.replyId)).toBeInViewport()
    await page.screenshot({path:test.info().outputPath(`home-${width}.png`)})
  }
  await other.close()
})

test('new chat endpoints enforce authentication and CSRF', async ({ request }) => {
  const anonymous = await request.get('/api/v1/chat', { headers: { Cookie:'', 'X-CSRF-Token':'' } })
  expect(anonymous.status()).toBe(401)
  const denied = await request.post('/api/v1/chat/messages', { headers:{'X-CSRF-Token':''},data:{clientMessageId:randomUUID(),content:fastReply} })
  expect(denied.status()).toBe(403)
  const bad = await request.put('/api/v1/chat/settings',{data:{idleMinutes:0,maxConcurrent:0,debug:false}})
  expect(bad.status()).toBe(400)
  expect(await bad.text()).toMatch(/idle|concurrent|range|must/i)
  expect(e2eAuthHeaders().Cookie).toBeTruthy()
  expect(e2eBackendURL).toContain('127.0.0.1')
})

test('selected title harness maintains a factual summary in its own background Session', async ({ request }) => {
  const previous = await json<{ config: unknown }>(request, '/api/v1/config/settings/title-generation-agent')
  const model = harness.models?.availableModels.find(m=>/deepseek.*flash/i.test(m.name))?.modelId || harness.models?.currentModelId
  await json(request, '/api/v1/config/settings/title-generation-agent', { agent_id:harness.agent_id, model_id:model }, 'PUT')
  try {
    const a = await agent(request, 'Summary')
    const run = await send(request,a.id,'本轮已确认：数据使用 JSONL 文件保存，不使用 SQLite。后续工作是继续完善语音输入。只回复 SUMMARY_DECISION_E2E，不调用工具。')
    await terminal(request,run)
    let summary = ''
    await expect.poll(async()=>{const value=await snapshot(request);const session=value.sessions.find(s=>s.id===run.sessionId);summary=session?.summary||'';return summary.length},{timeout:150_000}).toBeGreaterThan(0)
    expect(summary).toContain('JSONL')
    expect(summary).not.toBe('SUMMARY_DECISION_E2E')
    expect([...summary].length).toBeLessThanOrEqual(500)
    const sessions = await json<{sessions:Array<{id:string;maintenance?:boolean}>}>(request,'/api/v1/chat/sessions?limit=100')
    expect(sessions.sessions.some(s=>s.maintenance)).toBe(false)
  } finally { await json(request,'/api/v1/config/settings/title-generation-agent',previous.config||{agent_id:''},'PUT') }
})

test('older JSONL messages page backwards and preserve the reading anchor during SSE updates', async ({ request, page }) => {
  const a=await agent(request,'Paging')
  const run=await send(request,a.id)
  await terminal(request,run)
  const copied=await replay(request,async file=>{
    const commits=(await fs.readFile(file,'utf8')).trimEnd().split('\n').map(l=>JSON.parse(l) as {seq:number;messages?:ChatMessage[]})
    const current=await snapshot(request)
    const template=current.messages.find(m=>m.id===run.messageId)!
    let order=Math.max(...commits.flatMap(c=>(c.messages||[]).map(m=>m.order)))
    const rows=Array.from({length:210},(_,index)=>({...template,id:`paging-e2e-${index}`,order:++order,revision:commits.at(-1)!.seq+1,content:`PAGING_E2E_${index} 历史阅读位置`,role:index%2 ? 'assistant':'user',state:'completed'}))
    await fs.appendFile(file,JSON.stringify({version:1,seq:commits.at(-1)!.seq+1,messages:rows})+'\n')
  })
  try {
    // Built SPA on the same isolated backend uses its own API/SSE, avoiding a
    // Vite proxy to the primary server during the replay/pagination check.
    await page.addInitScript(()=>localStorage.setItem('quartet-language','en'))
    await page.goto(copied.base)
    await expect(page.getByTestId('personal-chat')).toBeVisible()
    await expect(message(page,'paging-e2e-209')).toBeVisible()
    await expect(message(page,'paging-e2e-0')).toHaveCount(0)
    const scroll=page.locator('.personal-timeline')
    await scroll.hover()
    await page.mouse.wheel(0,-100000)
    await expect.poll(()=>scroll.evaluate(e=>e.scrollTop)).toBe(0)
    await page.getByRole('button',{name:'Load earlier messages',exact:true}).click()
    await expect(message(page,'paging-e2e-50')).toHaveCount(1)
    await message(page,'paging-e2e-60').scrollIntoViewIfNeeded()
    const before=await message(page,'paging-e2e-60').boundingBox()
    await json(request,`${copied.base}/api/v1/chat/settings`,{...(await json<ChatSnapshot>(request,`${copied.base}/api/v1/chat`)).settings,maxConcurrent:7},'PUT')
    await expect.poll(async()=>{
      const after=await message(page,'paging-e2e-60').boundingBox()
      return Math.abs((after?.y||0)-(before?.y||0))
    }).toBeLessThan(3)
    await scroll.hover()
    await page.mouse.wheel(0,-100000)
    await expect.poll(()=>scroll.evaluate(e=>e.scrollTop)).toBe(0)
    await page.getByRole('button',{name:'Load earlier messages',exact:true}).click()
    await expect(message(page,'paging-e2e-0')).toHaveCount(1)
  } finally { await page.goto('about:blank');await copied.close() }
})

test('composer selects model/mode/thought and changing a business prompt rotates its Session', async ({ page,request }) => {
  const a=await agent(request,'Parameters')
  await open(page)
  await selectAgent(page,a)
  const chosenModel=harness.models?.availableModels.find(m=>/deepseek.*flash/i.test(m.name)) || harness.models?.availableModels[0]
  expect(chosenModel).toBeTruthy()
  await page.getByRole('combobox',{name:'Model',exact:true}).selectOption(chosenModel!.modelId)
  const chosenMode=harness.modes?.availableModes.find(m=>/bypass/i.test(m.name)) || harness.modes?.availableModes[0]
  if(chosenMode) await page.getByRole('combobox',{name:'Mode',exact:true}).selectOption(chosenMode.id)
  const thought=page.getByRole('combobox',{name:'Thought level',exact:true})
  await expect(thought).toBeVisible({timeout:30_000})
  await expect(thought).toBeEnabled({timeout:30_000})
  const chosenThought=await thought.locator('option').first().getAttribute('value')
  if(chosenThought) await thought.selectOption(chosenThought)
  await page.getByRole('textbox',{name:'Message',exact:true}).fill(fastReply)
  const accepted=page.waitForResponse(r=>r.url().endsWith('/api/v1/chat/messages')&&r.request().method()==='POST')
  await page.getByRole('button',{name:'Send',exact:true}).click()
  const first=(await(await accepted).json() as {run:ChatRun}).run
  await terminal(request,first)
  const state=await snapshot(request)
  const session=state.sessions.find(s=>s.id===first.sessionId)!
  expect(session.meta.model_id).toBe(chosenModel!.modelId)
  if(chosenMode)expect(session.meta.acp_mode).toBe(chosenMode.id)
  if(chosenThought)expect(session.meta.acp_thought_level).toBe(chosenThought)
  const updated=await json<{agent:BusinessAgent}>(request,'/api/v1/chat/agents',{...a,systemPrompt:'Revised business identity. Answer briefly.'})
  expect(updated.agent.revision).toBeGreaterThan(a.revision)
  const second=await send(request,a.id,fastReply,{modelId:chosenModel!.modelId,mode:chosenMode?.id,thoughtLevel:chosenThought||undefined})
  await terminal(request,second)
  expect(second.sessionId).not.toBe(first.sessionId)
})


test('global presets and recent sent messages work without a workspace', async ({request,page})=>{
  const existing=await json<{revision:string;config:{messages:Array<{id:string;content:string}>}}>(request,'/api/v1/config/message-presets/global')
  await json(request,'/api/v1/config/message-presets/global',{revision:existing.revision,messages:[{id:'personal-global-e2e',name:'Personal global preset',content:'GLOBAL_PRESET_E2E_CONTENT'}]},'PUT')
  await open(page)
  await page.getByRole('button',{name:'Presets and history',exact:true}).click()
  const menu=page.getByRole('listbox',{name:'Presets and history',exact:true})
  await expect(menu).toContainText('Global presets')
  await menu.getByRole('option',{name:/Personal global preset/}).click()
  await expect(page.getByRole('textbox',{name:'Message',exact:true})).toHaveValue('GLOBAL_PRESET_E2E_CONTENT')
  await page.getByRole('textbox',{name:'Message',exact:true}).fill('')
  const a=await agent(request,'PresetHistory')
  const run=await send(request,a.id)
  await terminal(request,run)
  await page.getByRole('button',{name:'Presets and history',exact:true}).click()
  await menu.getByRole('option').filter({hasText:fastReply}).first().click()
  await expect(page.getByRole('textbox',{name:'Message',exact:true})).toHaveValue(fastReply)
  await page.setViewportSize({width:390,height:844})
  await page.getByRole('button',{name:'Presets and history',exact:true}).click()
  await expect(page.locator('.mobile-dropdown-sheet')).toBeVisible()
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true)
})

test('card-only search hits identify the summary and still open verifiable original messages',async({request})=>{
  const a=await agent(request,'CardSearch')
  const run=await send(request,a.id)
  await terminal(request,run)
  const copied=await replay(request,async file=>{
    const commits=(await fs.readFile(file,'utf8')).trimEnd().split('\n').map(l=>JSON.parse(l) as {seq:number})
    const session=(await snapshot(request)).sessions.find(s=>s.id===run.sessionId)!
    await fs.appendFile(file,JSON.stringify({version:1,seq:commits.at(-1)!.seq+1,sessions:[{...session,summary:'CARD_ONLY_E2E_TERM',summaryThrough:run.id}]})+'\n')
  })
  try{
    const result=await json<{hits:Array<{role:string;messageId:string;sessionId:string}>}>(request,`${copied.base}/api/v1/chat/search?query=CARD_ONLY_E2E_TERM&agentId=${a.id}`)
    expect(result.hits).toEqual([expect.objectContaining({role:'summary',messageId:run.replyId,sessionId:run.sessionId})])
    const source=await json<{messages:ChatMessage[]}>(request,`${copied.base}/api/v1/chat/history?sessionId=${run.sessionId}&messageId=${run.replyId}`)
    expect(source.messages.some(m=>m.id===run.messageId&&m.content===fastReply)).toBe(true)
    expect(source.messages.every(m=>!m.rawContent)).toBe(true)
  }finally{await copied.close()}
})
