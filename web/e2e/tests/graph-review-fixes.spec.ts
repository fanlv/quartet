import fs from 'node:fs/promises'
import path from 'node:path'

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
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify({ nodes: [], edges: [], variables: { drafted: '1' } }))
  page.once('dialog', (d) => void d.dismiss())
  await page.getByTestId('graph-view-canvas').click()
  await expect(page.getByTestId('graph-json-textarea')).toBeVisible()

  // Accepting the confirm discards the draft and returns to the canvas.
  page.once('dialog', (d) => void d.accept())
  await page.getByTestId('graph-view-canvas').click()
  await expect(page.getByTestId('graph-node-start')).toBeVisible()
  // Back on canvas the actions are enabled again.
  await expect(page.getByTestId('graph-run')).toBeEnabled()
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
  // Write a malformed JSON file directly into the graph_workflows dir so the
  // list must skip it AND report it.
  const { localMemory } = await getE2ERunInfo()
  const workflowsDir = path.join(localMemory, 'agent', 'graph_workflows')
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
  const fp = path.join(localMemory, 'agent', 'graph_workflows', `${workflow.id}.json`)
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
  const runFile = path.join(localMemory, 'workspaces', workspace.workspaceId, 'jobs', run.jobId, 'graph_run', 'run.json')
  await fs.writeFile(runFile, '{ broken run json', 'utf8')

  const status = await request.get(`/api/v1/job/${encodeURIComponent(run.jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(status.status(), `expected corrupt run status 500, got ${status.status()}: ${await status.text()}`).toBe(500)
  const msg = (await status.json()).msg as string
  expect(msg).toContain('load graph run')
  expect(msg).toMatch(/invalid character|JSON/i)
  expect(msg).not.toBe('graph run not found')
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
  expect(workflow.workspaceId).toBe(workspaceB.workspaceId)
  expect(workflow.config.workspaceId).toBe(workspaceB.workspaceId)

  const cfgB = linearShellConfig(workspaceA)
  cfgB.workspaceId = workspaceA.workspaceId
  const updated = await request.put(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { name: 'e2e-workspace-norm-updated', config: cfgB, updatedAt: workflow.updatedAt },
  })
  expect(updated.ok(), `workflow update failed: ${updated.status()} ${await updated.text()}`).toBeTruthy()
  const after = (await updated.json()).workflow as { workspaceId: string; config: GraphConfig }
  expect(after.workspaceId).toBe(workspaceA.workspaceId)
  expect(after.config.workspaceId).toBe(workspaceA.workspaceId)
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

// ---------------------------------------------------------------------------
// #28 — key form controls expose reliable accessible names
// ---------------------------------------------------------------------------

test('graph review #28: workflow and inspector fields have accessible names', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'a11y-names')
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
})

// ---------------------------------------------------------------------------
// #29 — workflow list dates use locale formatting and show year across years
// ---------------------------------------------------------------------------

test('graph review #29: workflow list date includes year for older workflow', async ({ page, request }) => {
  const workspace = await createGraphWorkspace(request, 'date-year')
  const workflow = await createWorkflow(request, workspace, `e2e-date-year-${Date.now()}`)
  const { localMemory } = await getE2ERunInfo()
  const workflowFile = path.join(localMemory, 'agent', 'graph_workflows', `${workflow.id}.json`)
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
