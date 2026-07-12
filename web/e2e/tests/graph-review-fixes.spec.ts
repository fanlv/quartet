import fs from 'node:fs/promises'
import path from 'node:path'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'

import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
import { e2eAuthToken } from '../fixtures/e2e-environment'

// This suite verifies the code-review fixes (docs feature task "代码评审修复
// 2026-06-24"). Like graph-canvas.spec.ts it drives the REAL backend with
// pure-Shell graphs (no model credentials needed) and asserts structural /
// state signals through both the UI and the backend API.
//
// Covered review items:
//   #1 JSON view disables global actions (Validate/Save/Run) + switch-back guard
//   #2 Run button concurrency guard (no duplicate Graph Jobs on double click)
//   #3 invalid config does not leave an empty Graph Job behind
//   #4 a run launched from a saved workflow binds its source workflow metadata
//   #5 dirty guard confirms before leaving with unsaved changes / beforeunload
//   #6 run replay shows edge state (pruned branch) on the canvas
//   #8 edge ID empty/duplicate validation
//   #9 corrupt workflow files surface as warnings instead of vanishing
//   #10 edge delete remains clickable near React Flow handles
//   #11 RunConfig instance/snapshot limits are exposed and persisted
//   #12 workflow update uses updatedAt optimistic locking
//   #13 GraphLoop exposes step-stop
//   #14 run-version edit allows unfrozen structural repair
//   #15 refreshing workflow list does not advance the open document token
//   #16 dirty delete is guarded before the delete-confirm dialog opens
//   #17 non-JSON Graph errors are shown with their raw response body
//   #18 direct-open corrupt workflow shows the parse error, not "not found"
//   #19 embedded GraphLoop loop creation includes entry/exit markers
//   #21 saved workflow reopen seeds node/edge ID generation
//   #22 run/start with a stale workflowId fails instead of silently running ad-hoc
//   #23 corrupted run.json surfaces as a load error, not graph run not found
//   #24 workflow record/config workspaceId are normalized on save
//   #25 embedded GraphLoop preserves loop nesting, cascades delete, and refreshes condition vars
//   #26 workflowId-only run/start resolves workflow workspace before creating the Job
//   #27 delete confirmation is guarded while DELETE is in flight
//   #28 graph form controls expose reliable accessible names
//   #29 workflow list dates include the year for older workflows
//   #30 E2E Go temp output stays outside retained test-results artifacts
//   #31 parentId must point to an existing loop node
//   #32 completed runs reject run-version updates
//   #33 delete uses the open workflow version, not the refreshed list version
//   #34 save/create/delete success messages survive workflow-list refresh
//   #35 embedded GraphLoop edit keeps structured validation errors and dirty cancel guard
//   #36 browser Back/Forward is guarded by Graph dirty state in App
//   #37 embedded GraphLoop edits global variables while keeping run config locked
//   #38 corrupted run.json surfaces full load errors for version/resume/delete
//   #39 JSON draft dirty guard covers Back/New/select-other workflow
//   #40 run/start rejects stale workflowUpdatedAt with 409
//   #41 integer fields reject decimal drafts instead of clearing to unset
//   #42 schedule modal surfaces graph workflow list warnings
//   #43 JSON draft state badge and textarea keyboard focus are visible
//   #44 condition builder controls expose reliable accessible names
//   #45 undo/redo clears stale validation errors
//   #46 workflow library filters by workspace and shows workflow workspace
//   #47 run/start workspace/workdir request errors return 400
//   #48 frozen run-version nodes keep display title editable
//   #49 schedule modal shows full save / trigger errors
//   #50 new unsaved workflow shows Draft, not Saved
//   #51 main canvas can add/delete extra start/end while preserving the last controls
//   #52 mounted Graph page refreshes workflow list when workspace changes
//   #53 run/start without workflowId and config returns 400 without creating a Job
//   #54 cross-workspace save keeps the open workflow in update/delete mode
//   #55 graph-run control action reason is honored
//   #56 dirty snapshot run explains and enforces the workflow version lock
//   #57 validate/save in-flight locks editing controls
//   #58 full-page run-version edit locks globals/run config
//   #59 canvas connect-by-click blocks invalid edges before save
//   #60 run/start invalid workflowId returns 400
//   #61 deleting an already-deleted workflow no longer returns success
//   #62 workflow list returns summary fields, not full graph config
//   #63 unchanged run-version edits do not append duplicate versions
//   #64 saving a workflow keeps canvas edges visible after the editor unlocks

const AUTH_HEADERS = { 'X-AGENT-AUTH': e2eAuthToken }

type GraphConfig = {
  name?: string
  nodes: Array<Record<string, unknown>>
  edges: Array<Record<string, unknown>>
  variables?: Record<string, string>
  disabledVars?: string[]
  runConfig?: Record<string, unknown>
  workspaceId?: string
  workdir?: string
  canvas?: Record<string, unknown>
}

type GraphWorkspace = {
  workspaceId: string
  workdir: string
}

type E2ERunInfo = {
  localMemory: string
  repoRoot?: string
  goTmp?: string
}

async function getE2ERunInfo(): Promise<E2ERunInfo> {
  const runDir = process.env.QUARTET_E2E_RUN_DIR
  if (!runDir) throw new Error('QUARTET_E2E_RUN_DIR is not set; E2E global setup did not run')
  const raw = await fs.readFile(path.join(runDir, 'env.json'), 'utf8')
  return JSON.parse(raw) as E2ERunInfo
}

async function fileExists(filePath: string) {
  try {
    await fs.stat(filePath)
    return true
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'ENOENT') return false
    throw err
  }
}

async function waitForHTTP(url: string, timeoutMs: number) {
  const deadline = Date.now() + timeoutMs
  let lastError = ''
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url, { cache: 'no-store' })
      if (res.ok) return
      lastError = `HTTP ${res.status} ${await res.text()}`
    } catch (err) {
      lastError = String(err)
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new Error(`Timed out waiting for ${url}: ${lastError}`)
}

async function readSSEUntil(
  url: string,
  headers: Record<string, string>,
  predicate: (chunk: string) => boolean,
  timeoutMs: number,
) {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  let accumulated = ''
  try {
    const response = await fetch(url, { headers, signal: controller.signal })
    if (!response.ok) {
      throw new Error(`SSE connect failed: ${response.status} ${await response.text()}`)
    }
    expect(response.body).toBeTruthy()
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
    if (!data) continue
    try {
      const parsed = JSON.parse(data)
      if (parsed && typeof parsed === 'object') events.push(parsed as Record<string, unknown>)
    } catch {
      // Ignore keep-alives and partial diagnostic frames.
    }
  }
  return events
}

async function startReplayBackend(runInfo: E2ERunInfo, port: number): Promise<ChildProcessWithoutNullStreams> {
  if (!runInfo.repoRoot) throw new Error('repoRoot missing from E2E env.json')
  const logDir = path.join(process.env.QUARTET_E2E_RUN_DIR || '.', 'logs')
  await fs.mkdir(logDir, { recursive: true })
  const stdout = await fs.open(path.join(logDir, `replay-backend-${port}.stdout.log`), 'a')
  const stderr = await fs.open(path.join(logDir, `replay-backend-${port}.stderr.log`), 'a')
  const proc = spawn('go', ['run', './cmd/web'], {
    cwd: runInfo.repoRoot,
    env: {
      ...process.env,
      LOCAL_MEMORY: runInfo.localMemory,
      GOCACHE: path.join(process.env.QUARTET_E2E_RUN_DIR || '.', `go-build-cache-replay-${port}`),
      GOTMPDIR: runInfo.goTmp,
      X_AGENT_AUTH: e2eAuthToken,
      QUARTET_LISTEN_ADDR: `127.0.0.1:${port}`,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  proc.stdout.pipe(stdout.createWriteStream())
  proc.stderr.pipe(stderr.createWriteStream())
  await waitForHTTP(`http://127.0.0.1:${port}/api/v1/health`, 30_000)
  return proc
}

async function stopProcess(proc: ChildProcessWithoutNullStreams) {
  if (proc.exitCode !== null || proc.signalCode !== null) return
  proc.kill('SIGTERM')
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      if (proc.exitCode === null && proc.signalCode === null) proc.kill('SIGKILL')
      resolve()
    }, 1_500)
    proc.once('exit', () => {
      clearTimeout(timer)
      resolve()
    })
  })
}

async function createGraphWorkspace(request: APIRequestContext, name: string): Promise<GraphWorkspace> {
  const { localMemory } = await getE2ERunInfo()
  const workdir = path.join(localMemory, `graph-fix-${name}-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const res = await request.post('/api/v1/workspace/create', {
    headers: AUTH_HEADERS,
    data: { title: `E2E GraphFix ${name}`, description: 'E2E graph review-fix workspace', workdir },
  })
  expect(res.ok(), `workspace create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const created = await res.json()
  expect(created.id).toMatch(/^ws-/)
  return { workspaceId: created.id as string, workdir }
}

function linearShellConfig(workspace: GraphWorkspace): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'shell', type: 'shell', title: 'Echo', config: { script: 'echo graph-e2e-ok' }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [
      { id: 'edge-start-shell', sourceNodeId: 'start', targetNodeId: 'shell' },
      { id: 'edge-shell-end', sourceNodeId: 'shell', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }
}

// A graph with one if-else whose condition is statically false (flag != "yes"),
// so the YES out-edge is pruned and the NO out-edge is active. Both branches are
// pure-Shell so the run reaches `completed`. Used to assert edge run-state in
// replay: the YES edge must render as pruned.
function branchingShellConfig(workspace: GraphWorkspace): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 40, y: 200 } },
      { id: 'gate', type: 'ifElse', title: 'Gate', config: { condition: '{{flag}} == "yes"' }, layout: { x: 240, y: 200 } },
      { id: 'yes', type: 'shell', title: 'Yes', config: { script: 'echo took-yes' }, layout: { x: 460, y: 100 } },
      { id: 'no', type: 'shell', title: 'No', config: { script: 'echo took-no' }, layout: { x: 460, y: 300 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 700, y: 200 } },
    ],
    edges: [
      { id: 'e-start-gate', sourceNodeId: 'start', targetNodeId: 'gate' },
      { id: 'e-gate-yes', sourceNodeId: 'gate', targetNodeId: 'yes', sourcePort: 'yes' },
      { id: 'e-gate-no', sourceNodeId: 'gate', targetNodeId: 'no', sourcePort: 'no' },
      { id: 'e-yes-end', sourceNodeId: 'yes', targetNodeId: 'end' },
      { id: 'e-no-end', sourceNodeId: 'no', targetNodeId: 'end' },
    ],
    variables: { flag: 'no' },
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }
}

function sleepingShellConfig(workspace: GraphWorkspace): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'slow', type: 'shell', title: 'Slow', config: { script: 'sleep 2\necho graph-e2e-slow-done' }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [
      { id: 'edge-start-slow', sourceNodeId: 'start', targetNodeId: 'slow' },
      { id: 'edge-slow-end', sourceNodeId: 'slow', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 1 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }
}

function loopShellConfig(workspace: GraphWorkspace): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 40, y: 160 } },
      { id: 'boom', type: 'shell', title: 'Boom', config: { script: 'exit 1' }, layout: { x: 220, y: 160 } },
      { id: 'loop-1', type: 'loop', title: 'Loop', config: { loopMode: 'fixed', fixedCount: 1 }, layout: { x: 420, y: 80, width: 560, height: 320 } },
      { id: 'start-1', type: 'start', title: 'Loop entry', parentId: 'loop-1', layout: { x: 0, y: 147 } },
      { id: 'shell-1', type: 'shell', title: 'Loop shell', parentId: 'loop-1', config: { script: 'echo loop', outputVariables: ['loop_out'] }, layout: { x: 160, y: 130 } },
      { id: 'end-1', type: 'end', title: 'Loop exit', parentId: 'loop-1', layout: { x: 498, y: 147 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 1120, y: 160 } },
    ],
    edges: [
      { id: 'edge-start-boom', sourceNodeId: 'start', targetNodeId: 'boom' },
      { id: 'edge-boom-loop', sourceNodeId: 'boom', targetNodeId: 'loop-1' },
      { id: 'edge-loop-entry-shell', sourceNodeId: 'start-1', targetNodeId: 'shell-1' },
      { id: 'edge-loop-shell-exit', sourceNodeId: 'shell-1', targetNodeId: 'end-1' },
      { id: 'edge-loop-end', sourceNodeId: 'loop-1', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 1 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }
}

function failingShellConfig(workspace: GraphWorkspace): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'boom', type: 'shell', title: 'Boom', config: { script: 'exit 1' }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [
      { id: 'edge-start-boom', sourceNodeId: 'start', targetNodeId: 'boom' },
      { id: 'edge-boom-end', sourceNodeId: 'boom', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 1 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }
}

function frozenTitleConfig(workspace: GraphWorkspace): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'done', type: 'shell', title: 'Frozen Title Before', config: { script: 'echo done' }, layout: { x: 300, y: 160 } },
      { id: 'boom', type: 'shell', title: 'Boom', config: { script: 'exit 1' }, layout: { x: 520, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 740, y: 160 } },
    ],
    edges: [
      { id: 'edge-start-done', sourceNodeId: 'start', targetNodeId: 'done' },
      { id: 'edge-done-boom', sourceNodeId: 'done', targetNodeId: 'boom' },
      { id: 'edge-boom-end', sourceNodeId: 'boom', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 1 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }
}

async function dataTransferForNode(page: Page, type: string) {
  return page.evaluateHandle((nodeType) => {
    const dt = new DataTransfer()
    dt.setData('application/quartet-node', nodeType)
    return dt
  }, type)
}

async function dropNodeOnElement(page: Page, dragLabel: RegExp | string, targetSelector: string, type: string) {
  const chip = page.getByRole('button', { name: dragLabel }).first()
  await expect(chip).toBeVisible()
  const target = page.locator(targetSelector).first()
  await expect(target).toBeVisible()
  const box = await target.boundingBox()
  expect(box, `target ${targetSelector} has no bounding box`).toBeTruthy()
  const dataTransfer = await dataTransferForNode(page, type)
  await chip.dispatchEvent('dragstart', { dataTransfer })
  await target.dispatchEvent('drop', {
    dataTransfer,
    clientX: Math.round(box!.x + box!.width / 2),
    clientY: Math.round(box!.y + box!.height / 2),
  })
  await dataTransfer.dispose()
}

async function openGraphCanvas(page: Page, request: APIRequestContext, name: string): Promise<GraphWorkspace> {
  const workspace = await createGraphWorkspace(request, name)
  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)
  await expect(page.getByTestId('graph-node-start')).toBeVisible()
  await expect(page.getByTestId('graph-validate')).toBeVisible()
  return workspace
}

async function applyJsonConfig(page: Page, config: GraphConfig) {
  await page.getByTestId('graph-view-json').click()
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify(config))
  await page.getByTestId('graph-json-apply').click()
}

async function waitForRunStatus(
  request: APIRequestContext,
  jobId: string,
  terminal: string[],
  timeoutMs = 30_000,
): Promise<{ status: string; progress?: { totalCount: number; completedCount: number } }> {
  const deadline = Date.now() + timeoutMs
  let last = 'unknown'
  while (Date.now() < deadline) {
    const res = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
    expect(res.ok(), `run status fetch failed: ${res.status()} ${await res.text()}`).toBeTruthy()
    const body = await res.json()
    last = body.run?.status ?? 'unknown'
    if (terminal.includes(last)) {
      return { status: last, progress: body.progress ?? body.run?.progress }
    }
    await new Promise((r) => setTimeout(r, 400))
  }
  throw new Error(`job ${jobId} graph run did not reach ${terminal.join('/')} within ${timeoutMs}ms (last=${last})`)
}

async function countGraphJobs(request: APIRequestContext, workspaceId: string): Promise<number> {
  const res = await request.get(`/api/v1/job/list?workspaceId=${encodeURIComponent(workspaceId)}`, { headers: AUTH_HEADERS })
  expect(res.ok(), `job list failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const body = await res.json()
  const jobs: Array<{ mode?: string }> = body.jobs ?? body.data?.jobs ?? []
  return jobs.filter((j) => j.mode === 'graph').length
}

async function waitForGraphEdgeStatus(
  request: APIRequestContext,
  jobId: string,
  edgeId: string,
  expected: string,
  timeoutMs = 10_000,
) {
  const deadline = Date.now() + timeoutMs
  let last = 'missing'
  while (Date.now() < deadline) {
    const res = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
    expect(res.ok(), `run status fetch failed: ${res.status()} ${await res.text()}`).toBeTruthy()
    const body = await res.json()
    last = (body.edges ?? []).find((edge: { edgeId?: string }) => edge.edgeId === edgeId)?.status ?? 'missing'
    if (last === expected) return
    await new Promise((r) => setTimeout(r, 250))
  }
  throw new Error(`edge ${edgeId} did not reach ${expected} within ${timeoutMs}ms (last=${last})`)
}

async function createWorkflow(request: APIRequestContext, workspace: GraphWorkspace, name: string, config = linearShellConfig(workspace)) {
  const res = await request.post('/api/v1/graph/workflow', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name, workspaceId: workspace.workspaceId, config },
  })
  expect(res.ok(), `workflow create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const body = await res.json()
  expect(body.workflow?.id).toBeTruthy()
  return body.workflow as { id: string; name: string; updatedAt: string; config: GraphConfig }
}

function updatedLinearConfig(workspace: GraphWorkspace, script: string): GraphConfig {
  const cfg = linearShellConfig(workspace)
  cfg.nodes = cfg.nodes.map((node) => (
    node.id === 'shell'
      ? { ...node, config: { script } }
      : node
  ))
  return cfg
}

// ---------------------------------------------------------------------------
// #1 — JSON view disables global actions; switching back guards an unapplied draft
// ---------------------------------------------------------------------------

test('graph review #1: JSON view disables Validate/Save/Run and guards unapplied draft', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'json-guard')
  await page.getByTestId('graph-name-input').fill(`e2e-json-guard-${Date.now()}`)

  // Switch to JSON view: the canvas-bound global actions must be disabled, since
  // they read canvas state and would silently ignore the JSON draft.
  await page.getByTestId('graph-view-json').click()
  await expect(page.getByTestId('graph-validate')).toBeDisabled()
  await expect(page.getByTestId('graph-save')).toBeDisabled()
  await expect(page.getByTestId('graph-run')).toBeDisabled()

  // Edit the draft without applying, then try to switch back to canvas: a
  // confirm dialog guards the unapplied change. Dismissing it keeps JSON view.
  const jsonTextarea = page.getByTestId('graph-json-textarea')
  await jsonTextarea.fill(JSON.stringify({ nodes: [], edges: [], variables: { drafted: '1' } }))
  await expect(page.getByTestId('graph-dirty-badge')).toHaveText('Unapplied JSON draft')
  await expect(page.getByTestId('graph-clean-badge')).toHaveCount(0)

  await jsonTextarea.focus()
  await expect(jsonTextarea).toBeFocused()
  await expect(jsonTextarea).toHaveCSS('box-shadow', 'rgba(45, 212, 191, 0.16) 0px 0px 0px 3px')

  page.once('dialog', (d) => void d.dismiss())
  await page.getByTestId('graph-view-canvas').click()
  await expect(jsonTextarea).toBeVisible()

  // Accepting the confirm discards the draft and returns to the canvas.
  page.once('dialog', (d) => void d.accept())
  await page.getByTestId('graph-view-canvas').click()
  await expect(page.getByTestId('graph-node-start')).toBeVisible()
  // Back on canvas the actions are enabled again.
  await expect(page.getByTestId('graph-run')).toBeEnabled()
})

test('graph review #39: JSON draft dirty guard covers Back, New, and selecting another workflow', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'json-draft-leave')
  const first = await createWorkflow(request, workspace, `e2e-json-leave-a-${Date.now()}`)
  const second = await createWorkflow(request, workspace, `e2e-json-leave-b-${Date.now()}`)

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${first.id}`)).toBeVisible({ timeout: 10_000 })
  await page.getByTestId(`graph-workflow-row-${first.id}`).click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  await page.getByTestId('graph-view-json').click()
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify({ nodes: [], edges: [], variables: { jsonDraft: 'back' } }))
  let backDialog = ''
  page.once('dialog', (d) => {
    backDialog = d.message()
    void d.dismiss()
  })
  await page.getByRole('button', { name: 'Back' }).click()
  await expect.poll(() => backDialog).toContain('unsaved')
  await expect(page.getByTestId('graph-json-textarea')).toBeVisible()

  await page.getByTestId('graph-json-textarea').fill(JSON.stringify({ nodes: [], edges: [], variables: { jsonDraft: 'new' } }))
  let newDialog = ''
  page.once('dialog', (d) => {
    newDialog = d.message()
    void d.dismiss()
  })
  await page.getByRole('button', { name: 'New workflow' }).click()
  await expect.poll(() => newDialog).toContain('unsaved')
  await expect(page.getByTestId('graph-json-textarea')).toBeVisible()

  await page.getByTestId('graph-json-textarea').fill(JSON.stringify({ nodes: [], edges: [], variables: { jsonDraft: 'select' } }))
  let selectDialog = ''
  page.once('dialog', (d) => {
    selectDialog = d.message()
    void d.dismiss()
  })
  await page.getByTestId(`graph-workflow-row-${second.id}`).click()
  await expect.poll(() => selectDialog).toContain('unsaved')
  await expect(page.getByTestId('graph-json-textarea')).toContainText('jsonDraft')
  await expect(page.getByTestId('graph-name-input')).toHaveValue(first.name)
})

// ---------------------------------------------------------------------------
// #2 — Run concurrency guard: a double-click must not create two Graph Jobs
// ---------------------------------------------------------------------------

test('graph review #2: Run disables during launch and creates exactly one Graph Job', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'run-guard')
  await page.getByTestId('graph-name-input').fill(`e2e-run-guard-${Date.now()}`)

  let startCalls = 0
  page.on('request', (req) => {
    if (req.url().includes('/api/v1/graph/run/start') && req.method() === 'POST') startCalls += 1
  })

  // startRun does `await validate()` before POSTing run/start. Delay the
  // validate response so the "launch in flight" window is wide enough to
  // deterministically observe the guard: the Run button must be disabled the
  // whole time, so a second click in that window is impossible.
  await page.route('**/api/v1/graph/workflow/validate', async (route) => {
    await new Promise((r) => setTimeout(r, 1200))
    await route.continue()
  })

  const runBtn = page.getByTestId('graph-run')
  await runBtn.click()
  // Inside the (delayed) validate window: the guard has disabled Run.
  await expect(runBtn).toBeDisabled()

  const startResp = await page.waitForResponse(
    (r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST',
  )
  expect(startResp.ok()).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)

  await waitForRunStatus(request, jobId, ['completed', 'failed', 'timedOut'])
  // The single click produced exactly one start call and exactly one graph job;
  // the guard prevented any duplicate launch.
  expect(startCalls, `expected 1 run/start call, got ${startCalls}`).toBe(1)
  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(1)
})

// ---------------------------------------------------------------------------
// #3 — invalid config does not leave an empty Graph Job behind
// ---------------------------------------------------------------------------

test('graph review #3: invalid run/start returns 400 and leaves no empty Graph Job', async ({ request }) => {
  // Build an isolated workspace via API (no UI needed for this API-level check).
  const workspace = await createGraphWorkspace(request, 'empty-job')
  const before = await countGraphJobs(request, workspace.workspaceId)

  // A structurally invalid config (shell cannot reach end: no out-edge) sent
  // straight to the API — the page would normally pre-validate, but the API must
  // also refuse without persisting a phantom job.
  const invalid: GraphConfig = {
    nodes: [
      { id: 'start', type: 'start', title: 'Start' },
      { id: 'shell', type: 'shell', title: 'Echo', config: { script: 'echo hi' } },
      { id: 'end', type: 'end', title: 'End' },
    ],
    edges: [{ id: 'edge-start-shell', sourceNodeId: 'start', targetNodeId: 'shell' }],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }

  const res = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workspaceId: workspace.workspaceId, workdir: workspace.workdir, config: invalid },
  })
  expect(res.status(), `expected 400, got ${res.status()}: ${await res.text()}`).toBe(400)
  const body = await res.json()
  expect(Array.isArray(body.errors), 'invalid config should return located errors').toBeTruthy()

  // No graph job was left behind by the failed start (rollback of the freshly
  // created job).
  const after = await countGraphJobs(request, workspace.workspaceId)
  expect(after, `expected no new graph job, before=${before} after=${after}`).toBe(before)
})

// ---------------------------------------------------------------------------
// #4 — a run launched from a saved workflow binds its source workflow metadata
// ---------------------------------------------------------------------------

test('graph review #4: running a saved workflow binds workflowId + workflowName', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'bind-source')
  const name = `e2e-bind-${Date.now()}`

  await applyJsonConfig(page, linearShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(name)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  // Find the saved workflow id.
  const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(listRes.ok()).toBeTruthy()
  const wf = (await listRes.json()).workflows.find((w: { name: string }) => w.name === name)
  expect(wf, 'saved workflow not found').toBeTruthy()

  // Run it from the canvas (sends both workflowId and the live config).
  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok()).toBeTruthy()
  const run = (await startResp.json()).run
  const jobId: string = run?.jobId
  expect(jobId).toMatch(/^job-/)

  // The run binds the source workflow metadata even though config was sent too.
  expect(run?.workflowId, 'run must bind source workflowId').toBe(wf.id)
  const statusRes = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(statusRes.ok()).toBeTruthy()
  const persisted = (await statusRes.json()).run
  expect(persisted?.workflowId).toBe(wf.id)
  expect(persisted?.baseSnapshot?.workflowName).toBe(name)
})

test('graph review #4b: dirty saved workflow run prompts and executes the unsaved snapshot', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'dirty-run-snapshot')
  const name = `e2e-dirty-run-${Date.now()}`

  await applyJsonConfig(page, linearShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(name)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  const saved = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(saved.ok()).toBeTruthy()
  const wf = (await saved.json()).workflows.find((w: { name: string }) => w.name === name)
  expect(wf, 'saved workflow not found').toBeTruthy()

  const dirtyConfig = updatedLinearConfig(workspace, 'echo unsaved-snapshot-ran')
  await applyJsonConfig(page, dirtyConfig)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()

  let promptText = ''
  page.once('dialog', (d) => {
    promptText = d.message()
    void d.dismiss()
  })
  await page.getByTestId('graph-run').click()
  await expect.poll(() => promptText).toContain('unsaved')
  await expect(page).toHaveURL(/view=graph/)

  page.once('dialog', (d) => void d.accept())
  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const run = (await startResp.json()).run
  expect(run?.workflowId).toBe(wf.id)
  const shell = (run?.baseSnapshot?.config?.nodes ?? []).find((n: { id?: string }) => n.id === 'shell')
  expect(shell?.config?.script).toBe('echo unsaved-snapshot-ran')

  const persistedWf = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(wf.id)}`, { headers: AUTH_HEADERS })
  expect(persistedWf.ok()).toBeTruthy()
  const persistedShell = (await persistedWf.json()).workflow?.config?.nodes?.find((n: { id?: string }) => n.id === 'shell')
  expect(persistedShell?.config?.script).not.toBe('echo unsaved-snapshot-ran')
})

test('graph review #57: validate and save in-flight lock editing controls', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'inflight-lock')
  await page.getByTestId('graph-name-input').fill(`e2e-inflight-lock-${Date.now()}`)

  let releaseValidate: (() => void) | undefined
  await page.route('**/api/v1/graph/workflow/validate', async (route) => {
    await new Promise<void>((resolve) => {
      releaseValidate = resolve
    })
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        valid: true,
        errors: [],
      }),
    })
  })

  await page.getByTestId('graph-validate').click()
  await expect(page.getByTestId('graph-name-input')).toBeDisabled()
  await expect(page.getByTestId('graph-save')).toBeDisabled()
  await expect(page.getByRole('button', { name: /Shell Script/ }).first()).toHaveCount(0)
  releaseValidate?.()
  await expect(page.getByTestId('graph-name-input')).toBeEnabled({ timeout: 10_000 })
  await expect(page.getByTestId('graph-error-list')).toHaveCount(0)
  await page.unroute('**/api/v1/graph/workflow/validate')

  let releaseSave: (() => void) | undefined
  await page.route('**/api/v1/graph/workflow', async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    const requestBody = JSON.parse(route.request().postData() || '{}') as { name?: string; description?: string; config?: GraphConfig }
    await new Promise<void>((resolve) => {
      releaseSave = resolve
    })
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        workflow: {
          id: `gwf-stale-${Date.now()}`,
          name: requestBody.name,
          description: requestBody.description || '',
          config: requestBody.config,
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        },
      }),
    })
  })

  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-name-input')).toBeDisabled()
  releaseSave?.()
  await expect(page.getByTestId('graph-name-input')).toBeEnabled({ timeout: 10_000 })
  await expect(page.getByTestId('graph-message')).toContainText('Workflow created.', { timeout: 10_000 })
  await expect(page.getByTestId('graph-save')).toContainText('Save')
  await expect(page.locator('.react-flow__edge')).toHaveCount(2)
  await expect(page.locator('.react-flow__edge[data-id="edge-start-shell"]')).toBeVisible()
  await expect(page.locator('.react-flow__edge[data-id="edge-shell-end"]')).toBeVisible()
})

test('graph review #56: dirty snapshot run rejects stale source workflow after confirm', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'dirty-run-stale-source')
  const name = `e2e-dirty-run-stale-${Date.now()}`

  await applyJsonConfig(page, linearShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(name)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  const saved = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(saved.ok()).toBeTruthy()
  const wf = (await saved.json()).workflows.find((w: { name: string }) => w.name === name)
  expect(wf, 'saved workflow not found').toBeTruthy()

  await applyJsonConfig(page, updatedLinearConfig(workspace, 'echo stale-local-snapshot'))
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()

  const newer = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(wf.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${name}-newer`, config: updatedLinearConfig(workspace, 'echo newer-saved'), updatedAt: wf.updatedAt },
  })
  expect(newer.ok(), `workflow update failed: ${newer.status()} ${await newer.text()}`).toBeTruthy()
  const before = await countGraphJobs(request, workspace.workspaceId)

  let promptText = ''
  page.once('dialog', (d) => {
    promptText = d.message()
    void d.accept()
  })
  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  await expect.poll(() => promptText).toContain('saved workflow must still be current')
  expect(startResp.status(), `expected stale workflowUpdatedAt 409, got ${startResp.status()}: ${await startResp.text()}`).toBe(409)
  await expect(page.getByTestId('graph-message')).toContainText('graph workflow has been modified', { timeout: 10_000 })
  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(before)
})

// ---------------------------------------------------------------------------
// #8 — edge ID empty / duplicate validation
// ---------------------------------------------------------------------------

test('graph review #8: duplicate and empty edge IDs are reported by validation', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'edge-id')

  // Two edges share id "dup", and one edge has an empty id — both illegal.
  const cfg: GraphConfig = {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'a', type: 'shell', title: 'A', config: { script: 'echo a' }, layout: { x: 280, y: 160 } },
      { id: 'b', type: 'shell', title: 'B', config: { script: 'echo b' }, layout: { x: 480, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 680, y: 160 } },
    ],
    edges: [
      { id: 'dup', sourceNodeId: 'start', targetNodeId: 'a' },
      { id: 'dup', sourceNodeId: 'a', targetNodeId: 'b' },
      { id: '', sourceNodeId: 'b', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }

  // Validate via the API (deterministic) — the canvas pre-validates on apply too.
  const res = await request.post('/api/v1/graph/workflow/validate', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { config: cfg },
  })
  expect(res.ok()).toBeTruthy()
  const body = await res.json()
  expect(body.valid).toBe(false)
  const messages: string = (body.errors ?? []).map((e: { message: string }) => e.message).join('\n')
  expect(messages).toMatch(/duplicate edge ID/i)
  expect(messages).toMatch(/empty ID/i)

  // The same config in the UI surfaces a located error list (no crash).
  await applyJsonConfig(page, cfg)
  await page.getByTestId('graph-validate').click()
  await expect(page.getByTestId('graph-error-list')).toBeVisible({ timeout: 10_000 })
})

test('graph review #8b: edge and global validation errors have working click feedback', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'error-focus')
  const cfg: GraphConfig = {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'shell', type: 'shell', title: 'Echo', config: { script: 'echo hi' }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [
      { id: 'dup-edge', sourceNodeId: 'start', targetNodeId: 'shell' },
      { id: 'dup-edge', sourceNodeId: 'shell', targetNodeId: 'end' },
    ],
    variables: { _reserved: 'bad' },
    disabledVars: [],
    runConfig: { concurrencyLimit: 0 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }

  await applyJsonConfig(page, cfg)
  await page.getByTestId('graph-validate').click()
  await expect(page.getByTestId('graph-error-list')).toBeVisible({ timeout: 10_000 })

  const edgeError = page.getByTestId('graph-error-link').filter({ hasText: 'edge=dup-edge' }).first()
  await edgeError.click()
  await expect(page.getByTestId('graph-inspector')).toContainText(/Node config|Shell Script|Start/)

  const globalError = page.getByTestId('graph-error-link').filter({ hasText: /config=|var=_reserved/ }).first()
  await globalError.click()
  await expect(page.getByTestId('graph-inspector')).toContainText('Global variables')
})

test('graph review #59: connect-by-click blocks invalid edges before save', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'invalid-connect')
  await page.getByRole('button', { name: 'Connect by click' }).click()

  await page.getByTestId('graph-node-end').click()
  const shellTarget = page.locator('.graph-connect-targets').getByRole('button', { name: 'Shell' })
  await expect(shellTarget).toBeDisabled()
  await expect(shellTarget).toHaveAttribute('title', /End node .* cannot have outgoing edges/)
  await page.getByTestId('graph-node-shell').click()
  await expect(page.locator('.graph-connect-error')).toContainText('End node')

  await page.getByTestId('graph-node-end').click()
  await page.getByTestId('graph-node-shell').click()
  const startTarget = page.locator('.graph-connect-targets').getByRole('button', { name: 'Start' })
  await expect(startTarget).toBeDisabled()
  await expect(startTarget).toHaveAttribute('title', /Start node .* cannot have incoming edges/)

  await page.getByTestId('graph-node-start').click()
  await expect(page.locator('.graph-connect-error')).toContainText('Start node')
})

// ---------------------------------------------------------------------------
// #6 — run replay shows edge state: a pruned branch renders as pruned
// ---------------------------------------------------------------------------

test('graph review #6: run replay marks the pruned if-else branch edge', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'edge-state')
  await applyJsonConfig(page, branchingShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-edge-state-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['completed', 'failed', 'timedOut'])
  expect(status).toBe('completed')
  await waitForGraphEdgeStatus(request, jobId, 'e-gate-yes', 'pruned')

  // Reopen the run on the canvas via the deep-link (read-only replay).
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)
  // The NO branch ran; the YES branch was pruned (flag != "yes"). The replay
  // overlays edge state onto the canvas: the YES edge carries data-run-display
  // "pruned"; the NO edge is not pruned (active/done).
  const yesEdge = page.locator('.react-flow__edge[data-id="e-gate-yes"] [data-run-display]')
  await expect(yesEdge).toHaveAttribute('data-run-display', 'pruned', { timeout: 15_000 })
  const noEdge = page.locator('.react-flow__edge[data-id="e-gate-no"] [data-run-display]')
  await expect(noEdge).not.toHaveAttribute('data-run-display', 'pruned')
})

// ---------------------------------------------------------------------------
// #9 — corrupt workflow files surface as warnings instead of vanishing
// ---------------------------------------------------------------------------

test('graph review #9: a corrupt workflow file surfaces as a warning in the UI', async ({ page, request }) => {
  // Write a malformed JSON file directly into the graph-workflows dir so the
  // list must skip it AND report it.
  const { localMemory } = await getE2ERunInfo()
  const workflowsDir = path.join(localMemory, 'quartet', 'config', 'graph-workflows')
  await fs.mkdir(workflowsDir, { recursive: true })
  const corruptName = `corrupt-${Date.now()}.json`
  await fs.writeFile(path.join(workflowsDir, corruptName), '{ this is not valid json', 'utf8')

  // The list API reports the corrupt file as a warning (and still returns ok).
  const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(listRes.ok()).toBeTruthy()
  const listBody = await listRes.json()
  const warnings: Array<{ file: string; error: string }> = listBody.warnings ?? []
  expect(warnings.some((w) => w.file.includes(corruptName)), `corrupt file not in warnings: ${JSON.stringify(warnings)}`).toBeTruthy()

  // The UI shows the warning block (corrupt file does not silently vanish).
  const workspace = await createGraphWorkspace(request, 'corrupt-ui')
  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId('graph-node-start')).toBeVisible()
  const warningBlock = page.getByTestId('graph-workflow-warnings')
  await expect(warningBlock).toBeVisible({ timeout: 10_000 })
  await expect(warningBlock).toContainText(corruptName)
})

test('graph review #42: schedule modal surfaces graph workflow list warnings', async ({ page, request }) => {
  const { localMemory } = await getE2ERunInfo()
  const workflowsDir = path.join(localMemory, 'quartet', 'config', 'graph-workflows')
  await fs.mkdir(workflowsDir, { recursive: true })
  const corruptName = `schedule-corrupt-${Date.now()}.json`
  await fs.writeFile(path.join(workflowsDir, corruptName), '{ this is not valid json', 'utf8')

  const workspace = await createGraphWorkspace(request, 'schedule-warning')
  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}`)
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)
  await expect(page.getByTestId('home-content')).toBeVisible({ timeout: 10_000 })

  await page.getByRole('button', { name: /New Task/ }).click()
  await expect(page.getByRole('heading', { name: 'New Scheduled Task' })).toBeVisible()
  const warningBlock = page.getByTestId('schedule-graph-workflow-warnings')
  await expect(warningBlock).toBeVisible({ timeout: 10_000 })
  await expect(warningBlock).toContainText(corruptName)
  await expect(warningBlock).toContainText(/invalid character|JSON/i)
})

test('graph review #45: undo clears stale validation errors after restoring the canvas', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'undo-validation')

  await page.getByTestId('graph-edge-delete-edge-start-shell').click()
  await page.getByTestId('graph-validate').click()
  await expect(page.getByTestId('graph-error-list')).toBeVisible({ timeout: 10_000 })

  await page.getByTestId('graph-undo').click()
  await expect(page.getByTestId('graph-error-list')).toHaveCount(0)
  await expect(page.locator('.react-flow__edge[data-id="edge-start-shell"]')).toBeVisible()
})

test('graph review #46: workflow list is global and shows each workflow workspace', async ({ page, request }) => {
  const workspaceA = await createGraphWorkspace(request, 'workflow-filter-a')
  const workspaceB = await createGraphWorkspace(request, 'workflow-filter-b')
  const wfA = await createWorkflow(request, workspaceA, `e2e-workflow-filter-a-${Date.now()}`)
  const wfB = await createWorkflow(request, workspaceB, `e2e-workflow-filter-b-${Date.now()}`)

  // Workflows are global: the list returns every workflow regardless of the
  // requesting workspace, so a workflow created under B is still visible when
  // listing while focused on A.
  const list = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(list.ok(), `workflow list failed: ${list.status()} ${await list.text()}`).toBeTruthy()
  const listBody = await list.json()
  expect((listBody.workflows ?? []).some((wf: { id: string }) => wf.id === wfA.id)).toBe(true)
  expect((listBody.workflows ?? []).some((wf: { id: string }) => wf.id === wfB.id)).toBe(true)

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  // Open the library while focused on workspace A; both workflows show up, each
  // tagged with its own workspace.
  await page.goto(`/?workspaceId=${workspaceA.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${wfA.id}`)).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId(`graph-workflow-row-${wfA.id}`)).toContainText(`E2E GraphFix workflow-filter-a`)
  await expect(page.getByTestId(`graph-workflow-row-${wfB.id}`)).toBeVisible()
  await expect(page.getByTestId(`graph-workflow-row-${wfB.id}`)).toContainText(`E2E GraphFix workflow-filter-b`)
})

test('graph review #54: cross-workspace save keeps the open workflow editable', async ({ page, request }) => {
  const workspaceA = await createGraphWorkspace(request, 'cross-workspace-save-a')
  await createGraphWorkspace(request, 'cross-workspace-save-b')

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspaceA.workspaceId}&view=graph`)
  await expect(page.getByTestId('graph-workspace-trigger')).toBeVisible({ timeout: 10_000 })

  await applyJsonConfig(page, linearShellConfig(workspaceA))
  await page.getByTestId('graph-name-input').fill(`e2e-cross-workspace-save-${Date.now()}`)
  await page.getByTestId('graph-workspace-trigger').click()
  await page.locator('[data-testid="graph-workspace-item"]').filter({ hasText: `E2E GraphFix cross-workspace-save-b` }).click()

  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-message')).toContainText('Workflow created.', { timeout: 10_000 })
  await expect(page.getByTestId('graph-save')).toContainText('Save')
  await expect(page.getByTestId('graph-save')).not.toContainText('Create')
  await expect(page.getByRole('button', { name: /^Delete$/ })).toBeVisible()

  // The workflow was saved under workspace B even though it was opened from A.
  // The list is global, so it is visible regardless of focus; assert it carries
  // workspace B as its recorded workspace.
  const list = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(list.ok()).toBeTruthy()
  const listBody = await list.json()
  const savedSummary = (listBody.workflows ?? []).find((wf: { name?: string }) => wf.name?.startsWith('e2e-cross-workspace-save-'))
  expect(savedSummary, 'cross-workspace-saved workflow not found in global list').toBeTruthy()

  await page.getByTestId('graph-name-input').fill(`e2e-cross-workspace-save-updated-${Date.now()}`)
  const [saveResp] = await Promise.all([
    page.waitForResponse((r) => /\/api\/v1\/graph\/workflow\/gwf-/.test(r.url()) && r.request().method() === 'PUT'),
    page.getByTestId('graph-save').click(),
  ])
  expect(saveResp.ok(), `update after cross-workspace save failed: ${saveResp.status()} ${await saveResp.text()}`).toBeTruthy()
})

test('graph review #47: run/start workspace and workdir request errors return 400 without creating a Job', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'run-start-bad-request')
  const before = await countGraphJobs(request, workspace.workspaceId)

  const missingWorkspace = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {
      workspaceId: `ws-missing-${Date.now()}`,
      config: { ...linearShellConfig(workspace), workspaceId: `ws-missing-${Date.now()}` },
    },
  })
  expect(missingWorkspace.status(), `expected missing workspace 400, got ${missingWorkspace.status()}: ${await missingWorkspace.text()}`).toBe(400)
  expect((await missingWorkspace.json()).msg).toContain('workspace')

  const invalidWorkdir = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {
      workspaceId: workspace.workspaceId,
      workdir: path.join(workspace.workdir, 'missing-dir'),
      config: linearShellConfig(workspace),
    },
  })
  expect(invalidWorkdir.status(), `expected invalid workdir 400, got ${invalidWorkdir.status()}: ${await invalidWorkdir.text()}`).toBe(400)
  expect((await invalidWorkdir.json()).msg).toContain('workdir')

  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(before)
})

test('graph review #60: run/start invalid workflowId returns 400 without creating a Job', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'run-start-invalid-workflow-id')
  const before = await countGraphJobs(request, workspace.workspaceId)

  const res = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {
      workflowId: '../bad',
      workspaceId: workspace.workspaceId,
      workdir: workspace.workdir,
    },
  })
  expect(res.status(), `expected invalid workflowId 400, got ${res.status()}: ${await res.text()}`).toBe(400)
  const body = await res.json()
  expect(body.msg || body.error).toContain('workflowId')
  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(before)
})

test('graph review #53: run/start without workflowId and config returns 400 without creating a Job', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'run-start-missing-source')
  const before = await countGraphJobs(request, workspace.workspaceId)

  const res = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workspaceId: workspace.workspaceId, workdir: workspace.workdir },
  })
  expect(res.status(), `expected missing workflow/config 400, got ${res.status()}: ${await res.text()}`).toBe(400)
  const body = await res.json()
  expect(body.msg).toContain('workflowId or config is required')
  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(before)
})

test('graph review #50: new unsaved workflow shows Draft instead of Saved', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'draft-badge')

  await expect(page.getByTestId('graph-draft-badge')).toHaveText('Draft')
  await expect(page.getByTestId('graph-clean-badge')).toHaveCount(0)

  await page.getByTestId('graph-name-input').fill(`e2e-draft-${Date.now()}`)
  await expect(page.getByTestId('graph-dirty-badge')).toHaveText('Unsaved changes')
  await expect(page.getByTestId('graph-draft-badge')).toHaveCount(0)
})

test('graph review #51: canvas manages multiple main start/end nodes without deleting the last controls', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'multi-controls')
  await page.addStyleTag({ content: '.graph-minimap { display: none !important; }' })
  const name = `e2e-multi-controls-${Date.now()}`

  const config: GraphConfig = {
    nodes: [
      { id: 'start-a', type: 'start', title: 'Start A', layout: { x: 40, y: 80 } },
      { id: 'start-b', type: 'start', title: 'Start B', layout: { x: 40, y: 260 } },
      { id: 'shell-a', type: 'shell', title: 'A', config: { script: 'echo a' }, layout: { x: 260, y: 80 } },
      { id: 'shell-b', type: 'shell', title: 'B', config: { script: 'echo b' }, layout: { x: 260, y: 260 } },
      { id: 'end-a', type: 'end', title: 'End A', layout: { x: 500, y: 80 } },
      { id: 'end-b', type: 'end', title: 'End B', layout: { x: 500, y: 260 } },
    ],
    edges: [
      { id: 'edge-start-a-shell-a', sourceNodeId: 'start-a', targetNodeId: 'shell-a' },
      { id: 'edge-shell-a-end-a', sourceNodeId: 'shell-a', targetNodeId: 'end-a' },
      { id: 'edge-start-b-shell-b', sourceNodeId: 'start-b', targetNodeId: 'shell-b' },
      { id: 'edge-shell-b-end-b', sourceNodeId: 'shell-b', targetNodeId: 'end-b' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }

  await applyJsonConfig(page, config)
  await page.getByTestId('graph-name-input').fill(name)
  await expect(page.getByTestId('graph-node-start-a')).toBeVisible()
  await expect(page.getByTestId('graph-node-start-b')).toBeVisible()
  await expect(page.getByTestId('graph-node-end-a')).toBeVisible()
  await expect(page.getByTestId('graph-node-end-b')).toBeVisible()

  await page.getByTestId('graph-node-start-b').click()
  await expect(page.getByRole('button', { name: 'Delete node' })).toBeVisible()
  await page.getByRole('button', { name: 'Delete node' }).click()
  await expect(page.getByTestId('graph-node-start-b')).toHaveCount(0)
  await expect(page.getByTestId('graph-node-start-a')).toBeVisible()

  await page.getByTestId('graph-node-start-a').click()
  await expect(page.getByRole('button', { name: 'Delete node' })).toHaveCount(0)

  await page.getByTestId('graph-node-end-b').click()
  await expect(page.getByRole('button', { name: 'Delete node' })).toBeVisible()
  await page.getByRole('button', { name: 'Delete node' }).click()
  await expect(page.getByTestId('graph-node-end-b')).toHaveCount(0)
  await expect(page.getByTestId('graph-node-end-a')).toBeVisible()

  await page.getByTestId('graph-node-end-a').click()
  await expect(page.getByRole('button', { name: 'Delete node' })).toHaveCount(0)

  await expect(page.locator('[data-testid^="graph-node-start-"]')).toHaveCount(1)
  await page.locator('.graph-palette .graph-node-chip', { hasText: 'Start' }).click()
  await expect(page.locator('[data-testid^="graph-node-start-"]')).toHaveCount(2)

  await expect(page.locator('[data-testid^="graph-node-end-"]')).toHaveCount(1)
  await page.locator('.graph-palette .graph-node-chip', { hasText: 'End' }).click()
  await expect(page.locator('[data-testid^="graph-node-end-"]')).toHaveCount(2)
})

test('graph review #52: mounted Graph page reloads workflow list when workspace changes', async ({ page, request }) => {
  const workspaceA = await createGraphWorkspace(request, 'mounted-switch-a')
  const workspaceB = await createGraphWorkspace(request, 'mounted-switch-b')
  const workflowA = await createWorkflow(request, workspaceA, `e2e-mounted-switch-a-${Date.now()}`)
  const workflowB = await createWorkflow(request, workspaceB, `e2e-mounted-switch-b-${Date.now()}`)

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspaceA.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${workflowA.id}`)).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId(`graph-workflow-row-${workflowB.id}`)).toHaveCount(0)

  await page.evaluate((workspaceId) => {
    const url = new URL(window.location.href)
    url.searchParams.set('workspaceId', workspaceId)
    url.searchParams.set('view', 'graph')
    window.history.pushState({}, '', url.toString())
    window.dispatchEvent(new PopStateEvent('popstate'))
  }, workspaceB.workspaceId)

  await expect(page.getByTestId(`graph-workflow-row-${workflowB.id}`)).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId(`graph-workflow-row-${workflowA.id}`)).toHaveCount(0)
  await expect(page.locator('.graph-kicker')).toHaveText(`E2E GraphFix mounted-switch-b`)
})

// ---------------------------------------------------------------------------
// #5 — dirty guard confirms before leaving with unsaved changes
// ---------------------------------------------------------------------------

test('graph review #5: leaving with unsaved changes prompts a confirm', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'dirty-guard')

  // Make the canvas dirty by editing the name (a tracked field).
  await page.getByTestId('graph-name-input').fill(`e2e-dirty-${Date.now()}`)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()

  // Clicking Back triggers a discard confirm. Dismiss it -> stay on the page.
  let dialogSeen = false
  page.once('dialog', (d) => {
    dialogSeen = true
    void d.dismiss()
  })
  await page.getByRole('button', { name: 'Back' }).click()
  await expect.poll(() => dialogSeen).toBe(true)
  // Dismissed: still on the graph page (canvas visible, did not navigate away).
  await expect(page.getByTestId('graph-node-start')).toBeVisible()
})

test('graph review #5b: dirty graph registers a browser beforeunload guard', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'beforeunload')

  await page.getByTestId('graph-name-input').fill(`e2e-beforeunload-${Date.now()}`)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()

  const prevented = await page.evaluate(() => {
    const event = new Event('beforeunload', { cancelable: true }) as BeforeUnloadEvent
    window.dispatchEvent(event)
    return event.defaultPrevented || event.returnValue === ''
  })
  expect(prevented).toBe(true)
})

// ---------------------------------------------------------------------------
// #10 — edge delete remains clickable near React Flow handles
// ---------------------------------------------------------------------------

test('graph review #10: default edge delete button is clickable', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'edge-delete')

  await expect(page.locator('.react-flow__edge')).toHaveCount(2)
  await page.getByTestId('graph-edge-delete-edge-start-shell').click()
  await expect(page.locator('.react-flow__edge')).toHaveCount(1)
  await expect(page.locator('.react-flow__edge[data-id="edge-start-shell"]')).toHaveCount(0)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()
})

// ---------------------------------------------------------------------------
// #11 — RunConfig instance/snapshot limits are visible and persisted
// ---------------------------------------------------------------------------

test('graph review #11: RunConfig instance and snapshot limits persist from the inspector', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'run-config-limits')
  const name = `e2e-run-config-${Date.now()}`
  await page.getByTestId('graph-name-input').fill(name)

  await page.locator('.gi-field', { hasText: 'Instance limit' }).locator('input').fill('123')
  await page.locator('.gi-field', { hasText: 'Snapshot byte limit' }).locator('input').fill('456789')
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(listRes.ok()).toBeTruthy()
  const wf = (await listRes.json()).workflows.find((w: { name: string }) => w.name === name)
  expect(wf, 'saved workflow not found').toBeTruthy()

  const getRes = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(wf.id)}`, { headers: AUTH_HEADERS })
  expect(getRes.ok()).toBeTruthy()
  const persisted = await getRes.json()
  expect(persisted.workflow?.config?.runConfig?.instanceLimit).toBe(123)
  expect(persisted.workflow?.config?.runConfig?.snapshotByteLimit).toBe(456789)
})

test('graph review #41: integer fields reject decimal input instead of clearing saved value', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'integer-decimal')
  const name = `e2e-integer-decimal-${Date.now()}`
  await page.getByTestId('graph-name-input').fill(name)

  const concurrency = page.locator('.gi-field', { hasText: 'Concurrency' }).locator('input')
  await concurrency.fill('7')
  await expect(concurrency).toHaveValue('7')
  await concurrency.fill('1.5')
  await expect(concurrency).toHaveValue('7')

  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(listRes.ok()).toBeTruthy()
  const wf = (await listRes.json()).workflows.find((w: { name: string }) => w.name === name)
  expect(wf, 'saved workflow not found').toBeTruthy()

  const getRes = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(wf.id)}`, { headers: AUTH_HEADERS })
  expect(getRes.ok()).toBeTruthy()
  const persisted = await getRes.json()
  expect(persisted.workflow?.config?.runConfig?.concurrencyLimit).toBe(7)
})

// ---------------------------------------------------------------------------
// #12 — stale updatedAt update returns 409 instead of silent overwrite
// ---------------------------------------------------------------------------

test('graph review #12: stale workflow update is rejected with 409', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'workflow-conflict')
  const original = await createWorkflow(request, workspace, `e2e-conflict-${Date.now()}`)

  const first = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${original.name}-first`, config: original.config, updatedAt: original.updatedAt },
  })
  expect(first.ok(), `first update failed: ${first.status()} ${await first.text()}`).toBeTruthy()

  const stale = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${original.name}-stale`, config: original.config, updatedAt: original.updatedAt },
  })
  expect(stale.status(), `expected stale update 409, got ${stale.status()}: ${await stale.text()}`).toBe(409)
  const body = await stale.json()
  expect(body.msg).toContain('graph workflow has been modified')

  const getRes = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, { headers: AUTH_HEADERS })
  expect(getRes.ok()).toBeTruthy()
  const current = await getRes.json()
  expect(current.workflow?.name).toBe(`${original.name}-first`)
})

test('graph review #12b: workflow update requires updatedAt and delete rejects stale updatedAt', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'workflow-delete-conflict')
  const original = await createWorkflow(request, workspace, `e2e-delete-conflict-${Date.now()}`)

  const missingToken = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${original.name}-missing-token`, config: original.config },
  })
  expect(missingToken.status(), `expected missing updatedAt 400, got ${missingToken.status()}: ${await missingToken.text()}`).toBe(400)
  expect((await missingToken.json()).msg).toContain('updatedAt is required')

  const first = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${original.name}-newer`, config: original.config, updatedAt: original.updatedAt },
  })
  expect(first.ok(), `first update failed: ${first.status()} ${await first.text()}`).toBeTruthy()
  const newer = (await first.json()).workflow as { id: string; updatedAt: string }

  const staleDelete = await request.delete(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { updatedAt: original.updatedAt },
  })
  expect(staleDelete.status(), `expected stale delete 409, got ${staleDelete.status()}: ${await staleDelete.text()}`).toBe(409)

  const validDelete = await request.delete(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { updatedAt: newer.updatedAt },
  })
  expect(validDelete.ok(), `valid delete failed: ${validDelete.status()} ${await validDelete.text()}`).toBeTruthy()
})

test('graph review #12c: whitespace name and invalid workflow id return 400', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'bad-request')

  const blankName = await request.post('/api/v1/graph/workflow', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: '   ', workspaceId: workspace.workspaceId, config: linearShellConfig(workspace) },
  })
  expect(blankName.status(), `expected blank name 400, got ${blankName.status()}: ${await blankName.text()}`).toBe(400)
  expect((await blankName.json()).msg).toContain('name is required')

  const invalidID = await request.get('/api/v1/graph/workflow/bad..id', { headers: AUTH_HEADERS })
  expect(invalidID.status(), `expected invalid id 400, got ${invalidID.status()}: ${await invalidID.text()}`).toBe(400)
  expect((await invalidID.json()).msg).toContain('invalid graph workflow request')
})

// ---------------------------------------------------------------------------
// #13 — GraphLoop exposes and calls step-stop
// ---------------------------------------------------------------------------

test('graph review #13: GraphLoop Step Stop button calls the bound job endpoint', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'step-stop')
  await applyJsonConfig(page, sleepingShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-step-stop-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)

  await expect(page.getByTestId('graph-loop-progress')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByRole('button', { name: 'Step Stop' })).toBeEnabled({ timeout: 10_000 })

  const [stepResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/job/${jobId}/graph-run/step-stop`) && r.request().method() === 'POST'),
    page.getByRole('button', { name: 'Step Stop' }).click(),
  ])
  expect(stepResp.ok(), `step-stop failed: ${stepResp.status()} ${await stepResp.text()}`).toBeTruthy()

  await expect.poll(async () => {
    const statusRes = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
    expect(statusRes.ok()).toBeTruthy()
    return (await statusRes.json()).run?.status
  }, { timeout: 6_000 }).toMatch(/^(stepStopping|stepStopped|completed)$/)
})

test('graph review #55: graph-run stop honors request reason', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'control-reason')
  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workspaceId: workspace.workspaceId, workdir: workspace.workdir, config: sleepingShellConfig(workspace) },
  })
  expect(start.ok(), `run start failed: ${start.status()} ${await start.text()}`).toBeTruthy()
  const jobId: string = (await start.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)

  const reason = `manual check ${Date.now()}`
  const stop = await request.post(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run/stop`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { reason },
  })
  expect(stop.ok(), `stop failed: ${stop.status()} ${await stop.text()}`).toBeTruthy()

  await waitForRunStatus(request, jobId, ['stopped'], 10_000)
  const statusRes = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(statusRes.ok(), `run status fetch failed: ${statusRes.status()} ${await statusRes.text()}`).toBeTruthy()
  const body = await statusRes.json()
  const interrupted = (body.instances ?? []).find((inst: { status?: string; blockedReason?: string }) => inst.status === 'interrupted')
  expect(interrupted?.blockedReason).toBe(reason)
})

// ---------------------------------------------------------------------------
// #14 — run-version edit allows unfrozen structural repair
// ---------------------------------------------------------------------------

test('graph review #14: failed run version edit can repair unfrozen structure and save', async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const workspace = await openGraphCanvas(page, request, 'run-structure-edit')
  await applyJsonConfig(page, failingShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-structure-edit-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)
  // This case is about structural editing. Hide the minimap overlay so a node
  // added near the viewport center cannot be obscured on CI browser geometry.
  await page.addStyleTag({ content: '.graph-minimap { display: none !important; }' })
  await expect(page.getByTestId('graph-run-editing-badge')).toBeVisible({ timeout: 10_000 })

  // The failed shell node is not frozen. Delete it, add a replacement shell,
  // configure it, then reconnect start -> replacement -> end with the click
  // connect tool. This proves the editor is not structure-locked while the
  // backend remains responsible for validating the resulting version.
  await page.getByTestId('graph-node-boom').click()
  await page.getByRole('button', { name: 'Delete node' }).click()
  await expect(page.getByTestId('graph-node-boom')).toHaveCount(0)

  await page.getByRole('button', { name: /Shell Script/ }).click()
  const replacement = page.locator('[data-testid^="graph-node-shell-"]').last()
  await expect(replacement).toBeVisible()
  await page.locator('.gi-field', { hasText: 'Shell script' }).locator('textarea').fill('echo repaired')

  await page.getByRole('button', { name: 'Connect by click' }).click()
  await page.getByTestId('graph-node-start').click()
  await replacement.click()
  await replacement.click()
  await page.getByTestId('graph-node-end').click()

  const [versionResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/job/${jobId}/graph-run/version`) && r.request().method() === 'PUT'),
    page.getByTestId('graph-save-run-version').click(),
  ])
  expect(versionResp.ok(), `version save failed: ${versionResp.status()} ${await versionResp.text()}`).toBeTruthy()

  const after = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(after.ok()).toBeTruthy()
  const run = (await after.json()).run
  expect(run.currentVersion).toBeGreaterThan(1)
  const latest = (run.versions ?? []).find((v: { version: number }) => v.version === run.currentVersion)
  expect((latest?.config?.nodes ?? []).some((n: { id?: string }) => n.id === 'boom')).toBe(false)
  expect((latest?.config?.edges ?? []).some((e: { targetNodeId?: string }) => e.targetNodeId === 'end')).toBe(true)
})

test('graph review #48: frozen run-version node title remains editable and saves', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'frozen-title')
  await applyJsonConfig(page, frozenTitleConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-frozen-title-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)
  await expect(page.getByTestId('graph-run-editing-badge')).toBeVisible({ timeout: 15_000 })
  await page.getByTestId('graph-node-done').click()
  await expect(page.getByTestId('gi-frozen-banner')).toBeVisible({ timeout: 10_000 })

  const titleInput = page.getByLabel('Node name')
  await expect(titleInput).toBeEnabled()
  await titleInput.fill('Frozen Title After')

  await expect(page.getByLabel('Shell script')).toBeDisabled()

  const [saveResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/job/${jobId}/graph-run/version`) && r.request().method() === 'PUT'),
    page.getByTestId('graph-save-run-version').click(),
  ])
  expect(saveResp.ok(), `save run version failed: ${saveResp.status()} ${await saveResp.text()}`).toBeTruthy()
  const saved = await saveResp.json()
  const done = saved.run?.versions?.at(-1)?.config?.nodes?.find((node: { id?: string }) => node.id === 'done')
  expect(done?.title).toBe('Frozen Title After')
})

test('graph review #58: full-page run-version editor edits global variables but locks run config', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'fullpage-run-lock-globals')
  await applyJsonConfig(page, failingShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-fullpage-lock-globals-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)
  await expect(page.getByTestId('graph-run-editing-badge')).toBeVisible({ timeout: 15_000 })
  await page.locator('.react-flow__pane').click({ position: { x: 10, y: 10 } })
  const inspector = page.getByTestId('graph-inspector')
  await expect(inspector.getByRole('spinbutton', { name: 'Concurrency' })).toBeDisabled()
  await expect(inspector.getByLabel('Code value')).toBeEnabled()
  await expect(inspector.getByLabel('Doc value')).toBeEnabled()
  await expect(inspector.getByRole('button', { name: /Add variable/i })).toBeVisible()
})

// ---------------------------------------------------------------------------
// #15 — workflow list refresh must not advance the open document updatedAt token
// ---------------------------------------------------------------------------

test('graph review #15: refreshing the workflow list keeps stale open-document token and save returns 409', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'workflow-refresh-token')
  const name = `e2e-refresh-token-${Date.now()}`
  const original = await createWorkflow(request, workspace, name)

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${original.id}`)).toBeVisible({ timeout: 10_000 })
  await page.getByTestId(`graph-workflow-row-${original.id}`).click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  // User A makes an unsaved local edit. User B saves a newer workflow version.
  await page.getByTestId('graph-name-input').fill(`${name}-local-stale`)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()
  const external = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${name}-external`, config: updatedLinearConfig(workspace, 'echo external'), updatedAt: original.updatedAt },
  })
  expect(external.ok(), `external update failed: ${external.status()} ${await external.text()}`).toBeTruthy()

  // Refreshing the library updates the sidebar list item, but must not replace
  // the compare token for the still-open, dirty document.
  await page.getByTitle('Refresh').click()
  await expect(page.locator('.graph-workflow-row-title', { hasText: `${name}-external` })).toBeVisible({ timeout: 10_000 })

  const [saveResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/graph/workflow/${original.id}`) && r.request().method() === 'PUT'),
    page.getByTestId('graph-save').click(),
  ])
  expect(saveResp.status(), `expected stale UI save 409, got ${saveResp.status()}: ${await saveResp.text()}`).toBe(409)
  await expect(page.getByTestId('graph-message')).toContainText('graph workflow has been modified')

  const current = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, { headers: AUTH_HEADERS })
  expect(current.ok()).toBeTruthy()
  expect((await current.json()).workflow?.name).toBe(`${name}-external`)
})

// ---------------------------------------------------------------------------
// #16 — deleting the selected workflow is guarded by the dirty prompt
// ---------------------------------------------------------------------------

test('graph review #16: deleting a dirty selected workflow asks to discard before delete confirmation', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'dirty-delete')
  const name = `e2e-dirty-delete-${Date.now()}`
  await applyJsonConfig(page, linearShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(name)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  await page.getByTestId('graph-name-input').fill(`${name}-unsaved`)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()

  let dialogSeen = false
  page.once('dialog', (d) => {
    dialogSeen = true
    void d.dismiss()
  })
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect.poll(() => dialogSeen).toBe(true)
  await expect(page.locator('.delete-confirm-dialog')).toHaveCount(0)

  page.once('dialog', (d) => void d.accept())
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.locator('.delete-confirm-dialog')).toBeVisible()
})

// ---------------------------------------------------------------------------
// #17 — non-JSON Graph errors keep their raw body in the UI
// ---------------------------------------------------------------------------

test('graph review #17: workflow save displays raw plain-text error responses', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'plain-text-error')
  const rawError = `plain-text-save-error-${Date.now()}`

  await page.route('**/api/v1/graph/workflow', async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    return route.fulfill({
      status: 500,
      contentType: 'text/plain',
      body: rawError,
    })
  })

  await page.getByTestId('graph-name-input').fill(`e2e-plain-text-${Date.now()}`)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-message')).toContainText(rawError)
})

// ---------------------------------------------------------------------------
// #18 — direct-open corrupt workflow shows JSON parse error, not not found
// ---------------------------------------------------------------------------

test('graph review #18: opening a corrupt workflow surfaces the parse error', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'corrupt-open')
  const workflow = await createWorkflow(request, workspace, `e2e-corrupt-open-${Date.now()}`)

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  // The row exists while the file is still healthy. Corrupt it after the list
  // has loaded so selecting the row exercises the direct GetWorkflow path.
  await expect(page.getByTestId(`graph-workflow-row-${workflow.id}`)).toBeVisible({ timeout: 10_000 })
  const { localMemory } = await getE2ERunInfo()
  const fp = path.join(localMemory, 'quartet', 'config', 'graph-workflows', `${workflow.id}.json`)
  await fs.writeFile(fp, '{ this is not valid json', 'utf8')
  await page.getByTestId(`graph-workflow-row-${workflow.id}`).click()

  await expect(page.getByTestId('graph-message')).toContainText('load graph workflow', { timeout: 10_000 })
  await expect(page.getByTestId('graph-message')).toContainText(/invalid character|JSON/i)
  await expect(page.getByTestId('graph-message')).not.toContainText('graph workflow not found')
})

// ---------------------------------------------------------------------------
// #19 — embedded GraphLoop editor creates legal loop entry/exit markers
// ---------------------------------------------------------------------------

test('graph review #19: embedded run-version editor adds loop entry and exit markers', async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const workspace = await openGraphCanvas(page, request, 'embedded-loop')
  await applyJsonConfig(page, failingShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-embedded-loop-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  // Open the Chat page for the Graph Job and use GraphLoopProgress' embedded
  // run-version editor, not the full GraphWorkflowPage edit deep-link.
  await page.goto(`/?workspaceId=${workspace.workspaceId}&jobId=${jobId}`)
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-mode', 'graph', { timeout: 10_000 })
  await expect(page.getByTestId('graph-loop-progress')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: 'Edit' }).click()
  await expect(page.getByTestId('graph-loop-editor')).toBeVisible()

  await page.getByTestId('graph-loop-editor').getByRole('button', { name: /Loop/ }).click()
  const embeddedLoop = page.getByTestId('graph-loop-editor')
  await expect(embeddedLoop.locator('[data-testid^="graph-loop-port-start-"]')).toBeVisible()
  await expect(embeddedLoop.locator('[data-testid^="graph-loop-port-end-"]')).toBeVisible()
})

test('graph review #20: completed run replay keeps workflow metadata read-only', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'replay-readonly-meta')
  await applyJsonConfig(page, linearShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-replay-readonly-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['completed', 'failed', 'timedOut'])
  expect(status).toBe('completed')

  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)
  await expect(page.getByTestId('graph-exit-run')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('graph-name-input')).toBeDisabled()
  await expect(page.getByTestId('graph-description-input')).toBeDisabled()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible()
})

// ---------------------------------------------------------------------------
// #21 — saved workflow reopen seeds ID generation before adding nodes/edges
// ---------------------------------------------------------------------------

test('graph review #21: reopening a workflow avoids duplicate generated node and edge IDs', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'id-seed')
  const name = `e2e-id-seed-${Date.now()}`
  const workflow = await createWorkflow(request, workspace, name, {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'shell-1', type: 'shell', title: 'Existing shell', config: { script: 'echo existing' }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [
      { id: 'edge-start-shell-2', sourceNodeId: 'start', targetNodeId: 'shell-1' },
      { id: 'edge-shell-end-3', sourceNodeId: 'shell-1', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  })

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${workflow.id}`)).toBeVisible({ timeout: 10_000 })
  await page.getByTestId(`graph-workflow-row-${workflow.id}`).click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  await page.getByRole('button', { name: /Shell Script/ }).click()
  await page.locator('.gi-field', { hasText: 'Shell script' }).locator('textarea').fill('echo added')

  const shellTestIds = await page.locator('[data-testid^="graph-node-shell-"]').evaluateAll((els) =>
    els.map((el) => el.getAttribute('data-testid')).filter(Boolean),
  )
  expect(shellTestIds.length).toBeGreaterThanOrEqual(2)
  expect(new Set(shellTestIds).size, `duplicate shell test ids: ${shellTestIds.join(', ')}`).toBe(shellTestIds.length)
  expect(shellTestIds.filter((id) => id === 'graph-node-shell-1')).toHaveLength(1)
  expect(shellTestIds.some((id) => /^graph-node-shell-\d+$/.test(id || '') && id !== 'graph-node-shell-1')).toBe(true)
})

// ---------------------------------------------------------------------------
// #22 — stale workflowId run/start fails instead of silently becoming ad-hoc
// ---------------------------------------------------------------------------

test('graph review #22: run/start with deleted workflowId and config returns an error', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'stale-workflow-run')
  const workflow = await createWorkflow(request, workspace, `e2e-stale-run-${Date.now()}`)

  const del = await request.delete(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { updatedAt: workflow.updatedAt },
  })
  expect(del.ok(), `delete failed: ${del.status()} ${await del.text()}`).toBeTruthy()
  const before = await countGraphJobs(request, workspace.workspaceId)

  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {
      workflowId: workflow.id,
      workspaceId: workspace.workspaceId,
      workdir: workspace.workdir,
      config: linearShellConfig(workspace),
    },
  })
  expect(start.status(), `expected deleted workflow source to fail, got ${start.status()}: ${await start.text()}`).toBe(404)
  expect((await start.json()).msg).toContain('graph workflow not found')
  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(before)
})

test('graph review #40: run/start with stale workflowUpdatedAt returns 409 and creates no Job', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'stale-workflow-run-token')
  const workflow = await createWorkflow(request, workspace, `e2e-stale-run-token-${Date.now()}`)
  const newer = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${workflow.name}-newer`, config: updatedLinearConfig(workspace, 'echo newer'), updatedAt: workflow.updatedAt },
  })
  expect(newer.ok(), `workflow update failed: ${newer.status()} ${await newer.text()}`).toBeTruthy()
  const before = await countGraphJobs(request, workspace.workspaceId)

  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {
      workflowId: workflow.id,
      workflowUpdatedAt: workflow.updatedAt,
      workspaceId: workspace.workspaceId,
      workdir: workspace.workdir,
      config: workflow.config,
    },
  })
  expect(start.status(), `expected stale workflowUpdatedAt 409, got ${start.status()}: ${await start.text()}`).toBe(409)
  expect((await start.json()).msg).toContain('graph workflow has been modified')
  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(before)
})

// ---------------------------------------------------------------------------
// #23 — corrupted run.json is not hidden as "graph run not found"
// ---------------------------------------------------------------------------

test('graph review #23: corrupted run metadata returns a full load error', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'corrupt-run')
  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workspaceId: workspace.workspaceId, workdir: workspace.workdir, config: linearShellConfig(workspace) },
  })
  expect(start.ok(), `run start failed: ${start.status()} ${await start.text()}`).toBeTruthy()
  const run = (await start.json()).run as { id: string; jobId: string }
  expect(run.jobId).toMatch(/^job-/)
  await waitForRunStatus(request, run.jobId, ['completed', 'failed', 'timedOut'])

  const { localMemory } = await getE2ERunInfo()
  const runFile = path.join(localMemory, 'quartet', 'data', 'workspaces', workspace.workspaceId, 'jobs', run.jobId, 'graph_run', 'run.json')
  await fs.writeFile(runFile, '{ broken run json', 'utf8')

  const status = await request.get(`/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(status.status(), `expected corrupt run status 500, got ${status.status()}: ${await status.text()}`).toBe(500)
  const msg = (await status.json()).msg as string
  expect(msg).toContain('load graph run')
  expect(msg).toMatch(/invalid character|JSON/i)
  expect(msg).not.toBe('graph run not found')
})

test('graph review #23b: corrupted event log surfaces through disk replay SSE error event', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'corrupt-events-replay')
  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workspaceId: workspace.workspaceId, workdir: workspace.workdir, config: linearShellConfig(workspace) },
  })
  expect(start.ok(), `run start failed: ${start.status()} ${await start.text()}`).toBeTruthy()
  const run = (await start.json()).run as { id: string; jobId: string }
  expect(run.jobId).toMatch(/^job-/)
  await waitForRunStatus(request, run.jobId, ['completed', 'failed', 'timedOut'])

  const runInfo = await getE2ERunInfo()
  const eventsFile = path.join(runInfo.localMemory, 'quartet', 'data', 'workspaces', workspace.workspaceId, 'jobs', run.jobId, 'graph_run', 'events.jsonl')
  await fs.writeFile(eventsFile, '{"type":"log"\n', 'utf8')

  const replayPort = 18191
  const replayBackend = await startReplayBackend(runInfo, replayPort)
  try {
    const stream = await readSSEUntil(
      `http://127.0.0.1:${replayPort}/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run/events`,
      AUTH_HEADERS,
      (chunk) => chunk.includes('replay list events failed') && chunk.includes('unmarshal graph event'),
      15_000,
    )
    const events = parseSSEMessageEvents(stream)
    const errorEvent = events.find((event) => event.type === 'error')
    expect(errorEvent, `missing graph error event in SSE stream:\n${stream}`).toBeTruthy()
    expect(errorEvent?.message).toContain('replay list events failed')
    expect(errorEvent?.message).toContain('unmarshal graph event')
    expect((errorEvent?.error as { message?: string } | undefined)?.message).toContain('unmarshal graph event')
  } finally {
    await stopProcess(replayBackend)
  }
})

// ---------------------------------------------------------------------------
// #24 — workflow workspaceId is normalized between record and config
// ---------------------------------------------------------------------------

test('graph review #24: workflow create/update normalizes record and config workspaceId', async ({ request }) => {
  const workspaceA = await createGraphWorkspace(request, 'workspace-norm-a')
  const workspaceB = await createGraphWorkspace(request, 'workspace-norm-b')
  const cfgA = linearShellConfig(workspaceA)
  cfgA.workspaceId = workspaceB.workspaceId
  cfgA.workdir = workspaceB.workdir

  const created = await request.post('/api/v1/graph/workflow', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `e2e-workspace-norm-${Date.now()}`, workspaceId: workspaceA.workspaceId, config: cfgA },
  })
  expect(created.ok(), `workflow create failed: ${created.status()} ${await created.text()}`).toBeTruthy()
  const workflow = (await created.json()).workflow as { id: string; updatedAt: string; workspaceId: string; config: GraphConfig }
  expect(workflow.workspaceId).toBe(workspaceA.workspaceId)
  expect(workflow.config.workspaceId).toBe(workspaceA.workspaceId)

  const cfgB = linearShellConfig(workspaceA)
  cfgB.workspaceId = workspaceB.workspaceId
  cfgB.workdir = workspaceB.workdir
  const updated = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: 'e2e-workspace-norm-updated', workspaceId: workspaceB.workspaceId, config: cfgB, updatedAt: workflow.updatedAt },
  })
  expect(updated.ok(), `workflow update failed: ${updated.status()} ${await updated.text()}`).toBeTruthy()
  const after = (await updated.json()).workflow as { workspaceId: string; config: GraphConfig }
  expect(after.workspaceId).toBe(workspaceB.workspaceId)
  expect(after.config.workspaceId).toBe(workspaceB.workspaceId)

  // The list is global; locate the workflow and confirm its recorded workspace
  // was normalized to B by the update.
  const list = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(list.ok(), `workflow list failed: ${list.status()} ${await list.text()}`).toBeTruthy()
  const summary = ((await list.json()).workflows ?? []).find((wf: { id?: string }) => wf.id === workflow.id)
  expect(summary, 'workflow not found in global list').toBeTruthy()
  expect(summary.workspaceId).toBe(workspaceB.workspaceId)
})

// ---------------------------------------------------------------------------
// #25 — embedded editor preserves loop nesting, cascades delete, refreshes vars
// ---------------------------------------------------------------------------

test('graph review #25: embedded editor handles loop nesting, deletion cascade, and live condition vars', async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const workspace = await openGraphCanvas(page, request, 'embedded-loop-nesting')
  await applyJsonConfig(page, loopShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-embedded-nesting-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  await waitForRunStatus(request, jobId, ['completed', 'failed', 'timedOut'])

  await page.goto(`/?workspaceId=${workspace.workspaceId}&jobId=${jobId}`)
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-mode', 'graph', { timeout: 10_000 })
  await expect(page.getByTestId('graph-loop-progress')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: 'Edit' }).click()
  await expect(page.getByTestId('graph-loop-editor')).toBeVisible()
  const embedded = page.getByTestId('graph-loop-editor')

  await dropNodeOnElement(page, /Shell Script/, '[data-testid="graph-loop-loop-1"]', 'shell')
  const addedShell = embedded.locator('[data-testid^="graph-node-shell-"]').filter({ hasNotText: 'Loop shell' }).last()
  await expect(addedShell).toBeVisible()
  await embedded.locator('[data-testid^="graph-node-shell-"]').filter({ hasNotText: 'Loop shell' }).last().click()
  await page.locator('.gi-field', { hasText: 'Output variable declaration' }).locator('input').fill('fresh_loop_var')

  const loopBox = await embedded.locator('[data-testid="graph-loop-loop-1"]').boundingBox()
  expect(loopBox, 'loop container should have a visible bounding box').toBeTruthy()
  await page.mouse.click(loopBox!.x + 32, loopBox!.y + 24)
  await page.getByRole('button', { name: 'Until condition' }).click()
  const conditionVar = page.locator('.gi-cond-var').first()
  await expect(conditionVar).toBeVisible()
  const datalistId = await conditionVar.getAttribute('list')
  expect(datalistId, 'condition input should point at a datalist').toBeTruthy()
  await expect(page.locator(`datalist#${datalistId} option[value="fresh_loop_var"]`)).toHaveCount(1)

  const addedShellTestId = await addedShell.getAttribute('data-testid')
  expect(addedShellTestId).toBeTruthy()
  await page.mouse.click(loopBox!.x + 32, loopBox!.y + 24)
  await page.getByRole('button', { name: 'Delete node' }).click()
  await expect(embedded.locator('[data-testid="graph-loop-loop-1"]')).toHaveCount(0)
  await expect(embedded.locator('[data-testid="graph-node-shell-1"]')).toHaveCount(0)
  await expect(embedded.locator(`[data-testid="${addedShellTestId}"]`)).toHaveCount(0)
  await expect(embedded.locator('[data-testid="graph-loop-port-start-1"]')).toHaveCount(0)
  await expect(embedded.locator('[data-testid="graph-loop-port-end-1"]')).toHaveCount(0)
})

// ---------------------------------------------------------------------------
// #26 — workflowId-only run/start resolves workflow workspace before Job create
// ---------------------------------------------------------------------------

test('graph review #26: workflowId-only run/start creates the Job in the workflow workspace', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'workflow-id-only')
  const workflow = await createWorkflow(request, workspace, `e2e-workflow-id-only-${Date.now()}`)
  const beforeDefault = await countGraphJobs(request, 'ws-1')
  const beforeWorkflow = await countGraphJobs(request, workspace.workspaceId)

  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workflowId: workflow.id },
  })
  expect(start.ok(), `run start failed: ${start.status()} ${await start.text()}`).toBeTruthy()
  const run = (await start.json()).run as { jobId: string; workflowId: string; workspaceId: string; baseSnapshot?: { config?: GraphConfig } }
  expect(run.workflowId).toBe(workflow.id)
  expect(run.workspaceId).toBe(workspace.workspaceId)
  expect(run.baseSnapshot?.config?.workspaceId).toBe(workspace.workspaceId)
  expect(run.baseSnapshot?.config?.workdir).toBe(workspace.workdir)

  await waitForRunStatus(request, run.jobId, ['completed', 'failed', 'timedOut'])
  expect(await countGraphJobs(request, workspace.workspaceId)).toBe(beforeWorkflow + 1)
  expect(await countGraphJobs(request, 'ws-1')).toBe(beforeDefault)
})

// ---------------------------------------------------------------------------
// #27 — delete confirmation is guarded while DELETE is in flight
// ---------------------------------------------------------------------------

test('graph review #27: delete confirmation cannot submit duplicate DELETE requests', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'delete-inflight')
  const name = `e2e-delete-inflight-${Date.now()}`
  await page.getByTestId('graph-name-input').fill(name)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  const workflow = await createWorkflow(request, workspace, `e2e-delete-control-${Date.now()}`)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${workflow.id}`)).toBeVisible({ timeout: 10_000 })
  await page.getByTestId(`graph-workflow-row-${workflow.id}`).click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  let deleteCalls = 0
  await page.route(`**/api/v1/graph/workflow/${workflow.id}`, async (route) => {
    if (route.request().method() === 'DELETE') {
      deleteCalls += 1
      await new Promise((r) => setTimeout(r, 800))
    }
    await route.continue()
  })

  await page.getByRole('button', { name: /^Delete$/ }).click()
  await expect(page.locator('.delete-confirm-dialog')).toBeVisible()
  const confirm = page.locator('.delete-confirm-dialog').getByRole('button', { name: /^Delete$/ })
  const [deleteResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/graph/workflow/${workflow.id}`) && r.request().method() === 'DELETE'),
    confirm.dblclick(),
  ])
  expect(deleteResp.ok(), `delete failed: ${deleteResp.status()} ${await deleteResp.text()}`).toBeTruthy()
  await expect(page.locator('.delete-confirm-dialog')).toHaveCount(0)
  expect(deleteCalls).toBe(1)
})

test('graph review #61: deleting an already-deleted workflow no longer returns success', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'delete-already-deleted')
  const workflow = await createWorkflow(request, workspace, `e2e-delete-already-${Date.now()}`)

  const first = await request.delete(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { updatedAt: workflow.updatedAt },
  })
  expect(first.ok(), `first delete failed: ${first.status()} ${await first.text()}`).toBeTruthy()

  const second = await request.delete(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { updatedAt: workflow.updatedAt },
  })
  expect([404, 409], `second delete should not be success, got ${second.status()}: ${await second.text()}`).toContain(second.status())
})

// ---------------------------------------------------------------------------
// #28 — key form controls expose reliable accessible names
// ---------------------------------------------------------------------------

test('graph review #28: workflow and inspector fields have accessible names', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'a11y-names')
  await page.addStyleTag({ content: '.graph-minimap { display: none !important; }' })

  await expect(page.getByRole('textbox', { name: 'Workflow name' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Description (optional)' })).toBeVisible()
  await expect(page.getByRole('spinbutton', { name: 'Concurrency' })).toBeVisible()
  await expect(page.getByRole('spinbutton', { name: 'Job total timeout' })).toBeVisible()
  await expect(page.getByRole('spinbutton', { name: 'Instance limit' })).toBeVisible()
  await expect(page.getByRole('spinbutton', { name: 'Snapshot byte limit' })).toBeVisible()

  await page.getByTestId('graph-node-shell').click()
  await expect(page.getByRole('textbox', { name: 'Node name' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Shell script' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Output variable declaration (optional, comma-separated)' })).toBeVisible()
  await expect(page.getByRole('spinbutton', { name: 'Node-level timeout' })).toBeVisible()

  await applyJsonConfig(page, branchingShellConfig(workspace))
  await page.getByTestId('graph-node-gate').click()
  await expect(page.getByRole('combobox', { name: 'Left variable' })).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Comparison operator' })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Right comparison value' })).toBeVisible()

  await page.getByRole('checkbox', { name: 'Compare with a variable' }).check()
  await expect(page.getByRole('combobox', { name: 'Right comparison variable' })).toBeVisible()

  await page.getByRole('button', { name: 'Switch to advanced' }).click()
  await expect(page.getByRole('textbox', { name: 'Advanced condition expression' })).toBeVisible()
})

// ---------------------------------------------------------------------------
// #29 — workflow list dates use locale formatting and show year across years
// ---------------------------------------------------------------------------

test('graph review #29: workflow list date includes year for older workflow', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'date-year')
  const workflow = await createWorkflow(request, workspace, `e2e-date-year-${Date.now()}`)
  const { localMemory } = await getE2ERunInfo()
  const workflowFile = path.join(localMemory, 'quartet', 'config', 'graph-workflows', `${workflow.id}.json`)
  const raw = JSON.parse(await fs.readFile(workflowFile, 'utf8')) as { updatedAt: string }
  raw.updatedAt = '2024-01-02T12:00:00Z'
  await fs.writeFile(workflowFile, `${JSON.stringify(raw, null, 2)}\n`, 'utf8')

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${workflow.id}`)).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId(`graph-workflow-row-${workflow.id}`).locator('.graph-workflow-row-date')).toContainText('2024')
})

// ---------------------------------------------------------------------------
// #30 — E2E Go build temp output is kept outside retained test-results
// ---------------------------------------------------------------------------

test('graph review #30: E2E GOTMPDIR is outside the retained test-results run directory', async () => {
  const runDir = process.env.QUARTET_E2E_RUN_DIR
  expect(runDir, 'QUARTET_E2E_RUN_DIR should be set by global setup').toBeTruthy()
  const info = await getE2ERunInfo()
  expect(info.goTmp, 'env.json should record the external Go temp dir').toBeTruthy()
  expect(path.resolve(info.goTmp!)).not.toContain(path.resolve(runDir!))
  expect(await fileExists(path.join(runDir!, 'go-tmp'))).toBe(false)
})

// ---------------------------------------------------------------------------
// #31 — parentId must reference a real loop node
// ---------------------------------------------------------------------------

test('graph review #31: parentId pointing to a ghost or non-loop node is rejected', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'parent-scope')
  const base = linearShellConfig(workspace)

  const ghostScope: GraphConfig = {
    ...base,
    nodes: [
      ...base.nodes,
      { id: 'ghost-start', type: 'start', parentId: 'ghost', title: 'Ghost entry', layout: { x: 0, y: 0 } },
      { id: 'ghost-shell', type: 'shell', parentId: 'ghost', title: 'Ghost shell', config: { script: 'echo orphan' }, layout: { x: 120, y: 0 } },
      { id: 'ghost-end', type: 'end', parentId: 'ghost', title: 'Ghost exit', layout: { x: 260, y: 0 } },
    ],
    edges: [
      ...base.edges,
      { id: 'edge-ghost-start-shell', sourceNodeId: 'ghost-start', targetNodeId: 'ghost-shell' },
      { id: 'edge-ghost-shell-end', sourceNodeId: 'ghost-shell', targetNodeId: 'ghost-end' },
    ],
  }

  const ghostRes = await request.post('/api/v1/graph/workflow/validate', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { config: ghostScope },
  })
  expect(ghostRes.ok()).toBeTruthy()
  const ghostBody = await ghostRes.json()
  expect(ghostBody.valid).toBe(false)
  const ghostMessages = (ghostBody.errors ?? []).map((e: { message?: string; nodeId?: string }) => `${e.nodeId}: ${e.message}`).join('\n')
  expect(ghostMessages).toContain('parentId "ghost" does not exist')
  expect(ghostMessages).toContain('ghost-shell')

  const nonLoopParent: GraphConfig = {
    ...base,
    nodes: base.nodes.map((node) => (
      node.id === 'shell'
        ? { ...node, parentId: 'start' }
        : node
    )),
  }
  const nonLoopRes = await request.post('/api/v1/graph/workflow/validate', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { config: nonLoopParent },
  })
  expect(nonLoopRes.ok()).toBeTruthy()
  const nonLoopBody = await nonLoopRes.json()
  expect(nonLoopBody.valid).toBe(false)
  const nonLoopMessages = (nonLoopBody.errors ?? []).map((e: { message?: string; nodeId?: string }) => `${e.nodeId}: ${e.message}`).join('\n')
  expect(nonLoopMessages).toContain('parentId "start" must point to a loop node')
})

// ---------------------------------------------------------------------------
// #32 — completed runs are frozen replays and reject version updates
// ---------------------------------------------------------------------------

test('graph review #32: completed GraphRun rejects run-version update with 409', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'completed-version')
  const config = linearShellConfig(workspace)
  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workspaceId: workspace.workspaceId, workdir: workspace.workdir, config },
  })
  expect(start.ok(), `run start failed: ${start.status()} ${await start.text()}`).toBeTruthy()
  const run = (await start.json()).run as { jobId: string; currentVersion: number }
  expect(run.jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, run.jobId, ['completed', 'failed', 'timedOut'])
  expect(status).toBe('completed')

  const edit = await request.put(`/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run/version`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { config: updatedLinearConfig(workspace, 'echo should-not-append') },
  })
  expect(edit.status(), `expected completed run version edit 409, got ${edit.status()}: ${await edit.text()}`).toBe(409)
  expect((await edit.json()).msg).toContain('graph run cannot be edited')

  const after = await request.get(`/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(after.ok()).toBeTruthy()
  const afterRun = (await after.json()).run
  expect(afterRun.currentVersion).toBe(run.currentVersion)
  expect(afterRun.versions).toHaveLength(1)
})

test('graph review #63: unchanged failed-run version save does not append duplicate versions', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'run-version-noop')
  await applyJsonConfig(page, failingShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-run-version-noop-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  const before = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(before.ok()).toBeTruthy()
  const beforeRun = (await before.json()).run as {
    currentVersion: number
    versions: Array<{ version: number; config: GraphConfig }>
    baseSnapshot?: { config?: GraphConfig }
  }

  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)
  await expect(page.getByTestId('graph-run-editing-badge')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('graph-save-run-version')).toBeDisabled()

  const sameConfig = structuredClone(
    beforeRun.versions.find((version) => version.version === beforeRun.currentVersion)?.config ??
    beforeRun.baseSnapshot?.config,
  ) as GraphConfig
  sameConfig.canvas = {}
  sameConfig.variables = {}
  sameConfig.disabledVars = []
  const direct = await request.put(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run/version`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { config: sameConfig },
  })
  expect(direct.ok(), `no-op version update failed: ${direct.status()} ${await direct.text()}`).toBeTruthy()
  const directRun = (await direct.json()).run as { currentVersion: number; versions: unknown[] }
  expect(directRun.currentVersion).toBe(beforeRun.currentVersion)
  expect(directRun.versions).toHaveLength(beforeRun.versions.length)
})

// ---------------------------------------------------------------------------
// #33 — delete uses the open workflow version after list refresh
// ---------------------------------------------------------------------------

test('graph review #33: refreshed list version cannot delete a stale open workflow', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'delete-open-token')
  const name = `e2e-delete-open-token-${Date.now()}`
  const original = await createWorkflow(request, workspace, name)

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId(`graph-workflow-row-${original.id}`)).toBeVisible({ timeout: 10_000 })
  await page.getByTestId(`graph-workflow-row-${original.id}`).click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  const external = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: `${name}-external`, config: updatedLinearConfig(workspace, 'echo external-delete'), updatedAt: original.updatedAt },
  })
  expect(external.ok(), `external update failed: ${external.status()} ${await external.text()}`).toBeTruthy()

  await page.getByTitle('Refresh').click()
  await expect(page.locator('.graph-workflow-row-title', { hasText: `${name}-external` })).toBeVisible({ timeout: 10_000 })

  await page.getByRole('button', { name: /^Delete$/ }).click()
  await expect(page.locator('.delete-confirm-dialog')).toBeVisible()
  const [deleteResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/graph/workflow/${original.id}`) && r.request().method() === 'DELETE'),
    page.locator('.delete-confirm-dialog').getByRole('button', { name: /^Delete$/ }).click(),
  ])
  expect(deleteResp.status(), `expected stale delete 409, got ${deleteResp.status()}: ${await deleteResp.text()}`).toBe(409)
  await expect(page.getByTestId('graph-message')).toContainText('graph workflow has been modified')

  const current = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, { headers: AUTH_HEADERS })
  expect(current.ok()).toBeTruthy()
  expect((await current.json()).workflow?.deleted).not.toBe(true)
})

// ---------------------------------------------------------------------------
// #34 — success messages remain visible after list reloads
// ---------------------------------------------------------------------------

test('graph review #34: create/save/delete success messages survive workflow list refresh', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'success-message')
  const name = `e2e-success-message-${Date.now()}`
  await page.getByTestId('graph-name-input').fill(name)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('graph-message')).toContainText('Workflow created.')

  await page.getByTestId('graph-description-input').fill('updated description')
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('graph-message')).toContainText('Workflow saved.')

  await page.getByRole('button', { name: /^Delete$/ }).click()
  await expect(page.locator('.delete-confirm-dialog')).toBeVisible()
  await page.locator('.delete-confirm-dialog').getByRole('button', { name: /^Delete$/ }).click()
  await expect(page.locator('.delete-confirm-dialog')).toHaveCount(0)
  await expect(page.getByTestId('graph-message')).toContainText('Workflow deleted.', { timeout: 10_000 })
})

// ---------------------------------------------------------------------------
// #35 — embedded edit keeps structured validation errors and dirty cancel guard
// ---------------------------------------------------------------------------

test('graph review #35: embedded edit shows clickable validation errors and guards dirty cancel', async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  const workspace = await openGraphCanvas(page, request, 'embedded-validation-dirty')
  await applyJsonConfig(page, failingShellConfig(workspace))
  await page.getByTestId('graph-name-input').fill(`e2e-embedded-validation-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  await page.goto(`/?workspaceId=${workspace.workspaceId}&jobId=${jobId}`)
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-mode', 'graph', { timeout: 10_000 })
  await expect(page.getByTestId('graph-loop-progress')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: 'Edit' }).click()
  await expect(page.getByTestId('graph-loop-editor')).toBeVisible()

  // Dirty cancel: editing a node config and pressing Cancel must ask first.
  await page.getByTestId('graph-loop-editor').getByTestId('graph-node-boom').click()
  await page.locator('.graph-loop-inspector .gi-field', { hasText: 'Shell script' }).locator('textarea').fill('echo dirty')
  let cancelDialogSeen = false
  page.once('dialog', (d) => {
    cancelDialogSeen = true
    void d.dismiss()
  })
  await page.getByRole('button', { name: 'Cancel edit' }).click()
  await expect.poll(() => cancelDialogSeen).toBe(true)
  await expect(page.getByTestId('graph-loop-editor')).toBeVisible()

  // Re-enter a known-invalid graph by deleting the edge to end, then save.
  await page.locator('.graph-loop-inspector .gi-field', { hasText: 'Shell script' }).locator('textarea').fill('exit 1')
  await page.getByTestId('graph-edge-delete-edge-boom-end').click()
  const [versionResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/job/${jobId}/graph-run/version`) && r.request().method() === 'PUT'),
    page.getByRole('button', { name: 'Save run version' }).click(),
  ])
  expect(versionResp.status(), `expected embedded invalid save 400, got ${versionResp.status()}: ${await versionResp.text()}`).toBe(400)
  await expect(page.getByTestId('graph-loop-error-list')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('graph-loop-error-link').first()).toContainText(/node=boom|edge=/)

  await page.getByTestId('graph-loop-error-link').first().click()
  await expect(page.getByTestId('graph-loop-editor').getByTestId('graph-node-boom')).toHaveClass(/has-error|selected/)
})

// ---------------------------------------------------------------------------
// #36 — App-level browser history navigation must respect Graph dirty state
// ---------------------------------------------------------------------------

test('graph review #36: browser Back is guarded when Graph has unsaved changes', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'browser-back-guard')
  await page.getByTestId('graph-name-input').fill(`e2e-browser-back-${Date.now()}`)
  await page.evaluate((workspaceId) => {
    window.history.pushState({}, '', `/?workspaceId=${workspaceId}`)
    window.history.pushState({}, '', `/?workspaceId=${workspaceId}&view=graph`)
  }, workspace.workspaceId)

  let dialogSeen = false
  page.once('dialog', (d) => {
    dialogSeen = true
    void d.dismiss()
  })
  await page.evaluate(() => window.history.back())
  await expect.poll(() => dialogSeen).toBe(true)
  await expect(page).toHaveURL(/view=graph/)
  await expect(page.getByTestId('graph-name-input')).toHaveValue(/e2e-browser-back-/)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()

  page.once('dialog', (d) => void d.accept())
  await page.evaluate(() => window.history.back())
  await expect(page).not.toHaveURL(/view=graph/)
  await expect(page.getByTestId('graph-node-start')).toHaveCount(0)
  expect(workspace.workspaceId).toMatch(/^ws-/)
})

// ---------------------------------------------------------------------------
// #37 — embedded GraphLoop edit persists global variables but locks run config
// ---------------------------------------------------------------------------

test('graph review #37: embedded run-version editor edits global variables and locks run config', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'embedded-global-lock')
  const config = failingShellConfig(workspace)
  config.variables = { Code: workspace.workdir, custom: 'value' }
  config.runConfig = { concurrencyLimit: 3, defaultNodeTimeoutSec: 12 }
  await applyJsonConfig(page, config)
  await page.getByTestId('graph-name-input').fill(`e2e-embedded-global-lock-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  await page.goto(`/?workspaceId=${workspace.workspaceId}&jobId=${jobId}`)
  await expect(page.getByTestId('job-chat')).toHaveAttribute('data-job-mode', 'graph', { timeout: 10_000 })
  await expect(page.getByTestId('graph-loop-progress')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: 'Edit' }).click()
  await expect(page.getByTestId('graph-loop-editor')).toBeVisible()

  // No selected node -> GraphInspector shows the global variables/run config
  // panel. Global variables are part of the run-version payload; run config
  // remains locked because it has separate runtime semantics.
  await page.getByTestId('graph-loop-editor').locator('.react-flow__pane').click({ position: { x: 10, y: 10 } })
  const inspector = page.locator('.graph-loop-inspector')
  await expect(inspector.getByRole('spinbutton', { name: 'Concurrency' })).toBeDisabled()
  const customValue = inspector.getByLabel('Variable custom value')
  await expect(customValue).toBeEnabled()
  await customValue.fill('edited')
  await expect(inspector.getByRole('button', { name: /Add variable/i })).toBeVisible()

  const [saveResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes(`/api/v1/job/${jobId}/graph-run/version`) && r.request().method() === 'PUT'),
    page.getByRole('button', { name: /Save run version/i }).click(),
  ])
  expect(saveResp.ok(), `save run version failed: ${saveResp.status()} ${await saveResp.text()}`).toBeTruthy()
  const saved = await saveResp.json()
  expect(saved.run?.versions?.at(-1)?.config?.variables?.custom).toBe('edited')
})

// ---------------------------------------------------------------------------
// #38 — corrupted GraphRun metadata errors are not collapsed to not found
// ---------------------------------------------------------------------------

test('graph review #38: corrupted run metadata returns full load errors for version, resume, and delete', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'corrupt-run-control')
  const start = await request.post('/api/v1/graph/run/start', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { workspaceId: workspace.workspaceId, workdir: workspace.workdir, config: failingShellConfig(workspace) },
  })
  expect(start.ok(), `run start failed: ${start.status()} ${await start.text()}`).toBeTruthy()
  const run = (await start.json()).run as { jobId: string; id: string }
  expect(run.jobId).toMatch(/^job-/)
  await waitForRunStatus(request, run.jobId, ['failed', 'completed', 'timedOut'])

  const { localMemory } = await getE2ERunInfo()
  const runFile = path.join(localMemory, 'quartet', 'data', 'workspaces', workspace.workspaceId, 'jobs', run.jobId, 'graph_run', 'run.json')
  await fs.writeFile(runFile, '{ broken run json', 'utf8')

  const version = await request.put(`/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run/version`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { config: updatedLinearConfig(workspace, 'echo repaired') },
  })
  expect(version.status(), `expected corrupt version update 500, got ${version.status()}: ${await version.text()}`).toBe(500)
  const versionMsg = (await version.json()).msg as string
  expect(versionMsg).toContain(`load graph run ${run.id} failed`)
  expect(versionMsg).not.toBe('graph run not found')
  expect(versionMsg).toMatch(/invalid character|unexpected|unmarshal/i)

  const resume = await request.post(`/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run/resume`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {},
  })
  expect(resume.status(), `expected corrupt resume 500, got ${resume.status()}: ${await resume.text()}`).toBe(500)
  const resumeMsg = (await resume.json()).msg as string
  expect(resumeMsg).toContain(`load graph run ${run.id} failed`)
  expect(resumeMsg).not.toBe('graph run not found')
  expect(resumeMsg).toMatch(/invalid character|unexpected|unmarshal/i)

  const del = await request.delete(`/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(del.status(), `expected corrupt delete 500, got ${del.status()}: ${await del.text()}`).toBe(500)
  const deleteMsg = (await del.json()).msg as string
  expect(deleteMsg).toContain(`load graph run ${run.id} failed`)
  expect(deleteMsg).not.toBe('graph run not found')
  expect(deleteMsg).toMatch(/invalid character|unexpected|unmarshal/i)
})

test('graph review #49: schedule modal displays full save and trigger errors', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'schedule-full-errors')
  const workflow = await createWorkflow(request, workspace, `e2e-schedule-errors-${Date.now()}`)

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)

  await page.route('**/api/v1/schedule/create', async (route) => {
    await route.fulfill({
      status: 500,
      contentType: 'text/plain',
      body: 'plain schedule create failure: keep this exact text',
    })
  })
  await page.goto(`/?workspaceId=${workspace.workspaceId}`)
  await expect(page.getByTestId('home-content')).toBeVisible({ timeout: 10_000 })
  await page.getByRole('button', { name: /New Task/ }).click()
  await expect(page.getByRole('heading', { name: 'New Scheduled Task' })).toBeVisible()
  await page.locator('.schedule-select').first().selectOption(workflow.id)
  await expect(page.locator('.schedule-config-preview')).toContainText(`E2E GraphFix schedule-full-errors`)
  await page.locator('.schedule-modal-body input[type="text"]').fill(`e2e-schedule-save-error-${Date.now()}`)
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.locator('.schedule-error')).toContainText('plain schedule create failure: keep this exact text')
  await page.unroute('**/api/v1/schedule/create')
  await page.getByRole('button', { name: 'Cancel' }).click()

  const createSchedule = await request.post('/api/v1/schedule/create', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {
      name: `e2e-schedule-trigger-error-${Date.now()}`,
      cronExpr: '0 9 * * *',
      enabled: true,
      maxConcurrent: 1,
      timeout: 0,
      graphWorkflowId: workflow.id,
      workspaceId: workspace.workspaceId,
    },
  })
  expect(createSchedule.ok(), `schedule create failed: ${createSchedule.status()} ${await createSchedule.text()}`).toBeTruthy()
  const schedule = (await createSchedule.json()).schedule as { id: string; name: string }

  await page.goto(`/?workspaceId=${workspace.workspaceId}`)
  await expect(page.getByText(schedule.name)).toBeVisible({ timeout: 10_000 })
  await page.getByText(schedule.name).click()
  await expect(page.getByRole('heading', { name: 'Edit Scheduled Task' })).toBeVisible()
  await page.route(`**/api/v1/schedule/${schedule.id}/run`, async (route) => {
    await route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'json trigger failure from error field' }),
    })
  })
  await page.getByRole('button', { name: 'Run Now' }).click()
  await expect(page.locator('.schedule-error')).toContainText('json trigger failure from error field')
})

test('graph review #49b: schedule Trigger Now is disabled while the request is pending', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'schedule-trigger-pending')
  const workflow = await createWorkflow(request, workspace, `e2e-schedule-pending-${Date.now()}`)
  const createSchedule = await request.post('/api/v1/schedule/create', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: {
      name: `e2e-schedule-trigger-pending-${Date.now()}`,
      cronExpr: '0 9 * * *',
      enabled: true,
      maxConcurrent: 1,
      timeout: 0,
      graphWorkflowId: workflow.id,
      workspaceId: workspace.workspaceId,
    },
  })
  expect(createSchedule.ok(), `schedule create failed: ${createSchedule.status()} ${await createSchedule.text()}`).toBeTruthy()
  const schedule = (await createSchedule.json()).schedule as { id: string; name: string }

  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)

  let runCalls = 0
  let releaseRun: (() => void) | null = null
  await page.route(`**/api/v1/schedule/${schedule.id}/run`, async (route) => {
    runCalls += 1
    await new Promise<void>((resolve) => { releaseRun = resolve })
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ status: 'triggered', jobId: 'job-e2e-pending-trigger' }),
    })
  })

  await page.goto(`/?workspaceId=${workspace.workspaceId}`)
  await expect(page.getByText(schedule.name)).toBeVisible({ timeout: 10_000 })
  await page.getByText(schedule.name).click()
  await expect(page.getByRole('heading', { name: 'Edit Scheduled Task' })).toBeVisible()

  const trigger = page.getByRole('button', { name: 'Run Now' })
  await trigger.click()
  await expect(page.getByRole('button', { name: 'Running...' })).toBeDisabled()
  await expect.poll(() => runCalls).toBe(1)
  await page.getByRole('button', { name: 'Running...' }).click({ force: true })
  await expect.poll(() => runCalls).toBe(1)

  releaseRun?.()
  await expect(page.getByRole('heading', { name: 'Edit Scheduled Task' })).toHaveCount(0, { timeout: 10_000 })
  expect(runCalls).toBe(1)
})

test('graph review #62: workflow list returns summary fields without full graph config', async ({ request }) => {
  const workspace = await createGraphWorkspace(request, 'workflow-list-summary')
  const name = `e2e-workflow-list-summary-${Date.now()}`
  const cfg = linearShellConfig(workspace)
  const shell = cfg.nodes.find((node) => node.id === 'shell') as { config?: { script?: string } } | undefined
  expect(shell).toBeTruthy()
  shell!.config = { ...(shell!.config ?? {}), script: `echo ${'large-prompt-marker-'.repeat(200)}` }

  const created = await request.post('/api/v1/graph/workflow', {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name, workspaceId: workspace.workspaceId, config: cfg },
  })
  expect(created.ok(), `workflow create failed: ${created.status()} ${await created.text()}`).toBeTruthy()
  const workflow = (await created.json()).workflow as { id: string }

  const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(listRes.ok(), `workflow list failed: ${listRes.status()} ${await listRes.text()}`).toBeTruthy()
  const listBody = await listRes.json()
  const summary = (listBody.workflows ?? []).find((wf: { id?: string }) => wf.id === workflow.id)
  expect(summary, 'workflow summary not found').toBeTruthy()
  expect(summary).toMatchObject({
    id: workflow.id,
    name,
    workspaceId: workspace.workspaceId,
    nodeCount: cfg.nodes.length,
    edgeCount: cfg.edges.length,
  })
  expect(summary.config).toBeUndefined()
  expect(JSON.stringify(summary)).not.toContain('large-prompt-marker')

  const detail = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, { headers: AUTH_HEADERS })
  expect(detail.ok()).toBeTruthy()
  expect(JSON.stringify(await detail.json())).toContain('large-prompt-marker')
})
