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
//   #5 dirty guard confirms before leaving with unsaved changes
//   #6 run replay shows edge state (pruned branch) on the canvas
//   #8 edge ID empty/duplicate validation
//   #9 corrupt workflow files surface as warnings instead of vanishing

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
}

async function getE2ERunInfo(): Promise<E2ERunInfo> {
  const runDir = process.env.QUARTET_E2E_RUN_DIR
  if (!runDir) throw new Error('QUARTET_E2E_RUN_DIR is not set; E2E global setup did not run')
  const raw = await fs.readFile(path.join(runDir, 'env.json'), 'utf8')
  return JSON.parse(raw) as E2ERunInfo
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

test('graph review #2: rapid double-click Run creates exactly one Graph Job', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'run-guard')
  await page.getByTestId('graph-name-input').fill(`e2e-run-guard-${Date.now()}`)

  const startCalls: number[] = []
  page.on('request', (req) => {
    if (req.url().includes('/api/v1/graph/run/start') && req.method() === 'POST') startCalls.push(Date.now())
  })

  // Fire two clicks back-to-back. The button disables on the first (startingRun),
  // so the second must be a no-op.
  const runBtn = page.getByTestId('graph-run')
  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    runBtn.click(),
    runBtn.click({ force: true }).catch(() => {}),
  ])
  expect(startResp.ok()).toBeTruthy()
  const jobId: string = (await startResp.json()).run?.jobId
  expect(jobId).toMatch(/^job-/)

  await waitForRunStatus(request, jobId, ['completed', 'failed', 'timedOut'])
  // Exactly one start request fired, and exactly one graph job exists.
  expect(startCalls.length, `expected 1 run/start call, got ${startCalls.length}`).toBe(1)
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
