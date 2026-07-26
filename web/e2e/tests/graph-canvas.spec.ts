import fs from 'node:fs/promises'
import path from 'node:path'

import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
import { e2eAuthToken } from '../fixtures/e2e-environment'

// This suite closes out the "独立 React Flow 画布" module (feature doc tasks 18
// and 19) with real end-to-end verification:
//
//   Task 18 — create / save / run / error-locate closure.
//   Task 19 — layout save-reopen + historical run replay stability.
//
// It drives the REAL backend (no QUARTET_E2E mode, no replay model). The graphs
// used here are pure-Shell (start -> Shell(echo) -> end), which run to a
// terminal `completed` status with NO model/ACP credentials — the harness seeds
// none by default. Assertions check structural / state signals (run reaches a
// terminal status, persisted layout round-trips, validation errors locate to a
// node) rather than model text.

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

type GraphWorkflowSummary = {
  id: string
  name: string
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
  const workdir = path.join(localMemory, `graph-${name}-${Date.now()}`)
  await fs.mkdir(workdir, { recursive: true })
  const res = await request.post('/api/v1/workspace/create', {
    headers: AUTH_HEADERS,
    data: { title: `E2E Graph ${name}`, description: 'E2E graph workspace', workdir },
  })
  expect(res.ok(), `workspace create failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  const created = await res.json()
  expect(created.id).toMatch(/^ws-/)
  return { workspaceId: created.id as string, workdir }
}

// A minimal, valid pure-Shell graph: start -> Shell(echo) -> end. Node ids are
// stable so the test can assert canvas selectors and persisted layout.
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

async function openGraphCanvas(page: Page, request: APIRequestContext, name = 'canvas'): Promise<GraphWorkspace> {
  const workspace = await createGraphWorkspace(request, name)
  await page.addInitScript((token) => {
    localStorage.setItem('quartet.x_auth_token', token)
    localStorage.setItem('quartet-language', 'en')
  }, e2eAuthToken)
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph`)
  await expect(page.getByTestId('auth-gate')).toHaveCount(0)
  // The default config (start -> Shell -> end) renders on the canvas at mount.
  await expect(page.getByTestId('graph-node-start')).toBeVisible()
  // graph-save stays visible on both layouts; secondary actions (validate etc.)
  // collapse into the "⋯" overflow menu on narrow (mobile) viewports.
  await expect(page.getByTestId('graph-save')).toBeVisible()
  return workspace
}

async function applyJsonConfig(page: Page, config: GraphConfig) {
  await page.getByTestId('graph-view-json').click()
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify(config))
  await page.getByTestId('graph-json-apply').click()
}

async function findWorkflowByName(request: APIRequestContext, name: string): Promise<GraphWorkflowSummary> {
  const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(listRes.ok()).toBeTruthy()
  const list = await listRes.json()
  const workflow = (list.workflows ?? []).find((w: GraphWorkflowSummary) => w.name === name)
  expect(workflow, `workflow ${name} not found`).toBeTruthy()
  return workflow
}

// Poll the run status API until it reaches a terminal status (or times out).
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

// ---------------------------------------------------------------------------
// Task 18 — create / save / run / error-locate
// ---------------------------------------------------------------------------

test('graph canvas: create and save a workflow, then it appears in the library', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'create')

  const unique = `e2e-create-${Date.now()}`
  await page.getByTestId('graph-name-input').fill(unique)

  // Saving a brand-new workflow uses the primary "Create" button.
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()
  await page.getByTestId('graph-save').click()

  // Saved -> the dirty badge clears and the workflow shows up in the sidebar.
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('.graph-workflow-row-title', { hasText: unique })).toBeVisible()
})

test('graph canvas: run a pure-Shell workflow to completion', async ({ page, request }) => {
  await openGraphCanvas(page, request, 'run')
  await page.getByTestId('graph-name-input').fill(`e2e-run-${Date.now()}`)

  // Capture the run id from the start response so we can poll the backend.
  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const startBody = await startResp.json()
  const runId: string = startBody.run?.id
  expect(runId, 'run start returned no run id').toBeTruthy()
  // The launch must have created and bound a Graph-type Job.
  const jobId: string = startBody.run?.jobId
  expect(jobId, 'run start returned no jobId').toMatch(/^job-/)

  // Starting a run jumps into the Chat page for the bound Graph Job (like
  // startloop): the URL gains the jobId, drops ?view=graph, and the GraphLoop
  // progress panel (with its embedded mini canvas) renders there.
  await expect(page).toHaveURL(new RegExp(`jobId=${jobId}`), { timeout: 10_000 })
  await expect(page).toHaveURL(/^(?!.*view=graph).*$/)
  await expect(page.getByTestId('graph-loop-progress')).toBeVisible({ timeout: 10_000 })
  await page.getByTestId('graph-loop-progress').click()
  await expect(page.getByTestId('graph-loop-canvas')).toBeVisible({ timeout: 10_000 })

  // The run reaches `completed`; a pure-Shell graph needs no credentials.
  const { status, progress } = await waitForRunStatus(request, jobId, ['completed', 'failed', 'timedOut'])
  expect(status, 'pure-shell graph should complete').toBe('completed')
  expect(progress, 'completed run should report progress').toBeTruthy()
  expect(progress!.completedCount).toBe(progress!.totalCount)
  expect(progress!.totalCount).toBeGreaterThan(0)

  // The embedded mini canvas highlights the shell node as succeeded once the
  // run finishes (live SSE refresh drives the per-node run status).
  const miniShell = page.getByTestId('graph-loop-canvas').getByTestId('graph-node-shell')
  await expect(miniShell).toBeAttached({ timeout: 15_000 })
  await expect(miniShell).toHaveClass(/run-succeeded/, { timeout: 15_000 })
})

test('graph canvas: invalid config surfaces located errors and focuses the node', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'invalid')

  // Build an invalid graph via the JSON view: the shell node has no outgoing
  // edge, so it cannot reach `end` — a structural violation that pins to a node.
  const invalid: GraphConfig = {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'shell', type: 'shell', title: 'Echo', config: { script: 'echo hi' }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [{ id: 'edge-start-shell', sourceNodeId: 'start', targetNodeId: 'shell' }],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }

  await page.getByTestId('graph-view-json').click()
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify(invalid))
  await page.getByTestId('graph-json-apply').click()

  // Back on the canvas, validate -> the error list renders located entries.
  await page.getByTestId('graph-validate').click()
  await expect(page.getByTestId('graph-error-list')).toBeVisible({ timeout: 10_000 })
  const errorLinks = page.getByTestId('graph-error-link')
  expect(await errorLinks.count()).toBeGreaterThan(0)

  // Clicking an error link focuses the canvas (no crash; node stays present).
  await errorLinks.first().click()
  await expect(page.getByTestId('graph-node-shell')).toBeAttached()
})

test('graph canvas: applying JSON refreshes validation and clears stale errors after edits', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'json-validation')

  const invalid: GraphConfig = {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'shell', type: 'shell', title: 'Echo', config: { script: 'echo hi' }, layout: { x: 320, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
    ],
    edges: [{ id: 'edge-start-shell', sourceNodeId: 'start', targetNodeId: 'shell' }],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 4 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }

  await applyJsonConfig(page, invalid)
  await expect(page.getByTestId('graph-error-list')).toBeVisible({ timeout: 10_000 })

  await page.getByTestId('graph-name-input').fill(`e2e-stale-cleared-${Date.now()}`)
  await expect(page.getByTestId('graph-error-list')).toHaveCount(0)

  await applyJsonConfig(page, linearShellConfig(workspace))
  await expect(page.getByTestId('graph-message')).toContainText('Validation passed', { timeout: 10_000 })
  await expect(page.getByTestId('graph-error-list')).toHaveCount(0)
})

// ---------------------------------------------------------------------------
// Task 19 — layout save-reopen + historical run replay
// ---------------------------------------------------------------------------

test('graph canvas: layout persists across save and reopen', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'layout')

  // Apply a config with a distinctive shell position via the JSON view so the
  // assertion does not depend on flaky drag coordinates.
  const cfg = linearShellConfig(workspace)
  cfg.nodes[1] = { ...cfg.nodes[1], layout: { x: 444, y: 222 } }
  const unique = `e2e-layout-${Date.now()}`
  cfg.name = unique

  await page.getByTestId('graph-view-json').click()
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify(cfg))
  await page.getByTestId('graph-json-apply').click()
  await page.getByTestId('graph-name-input').fill(unique)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })

  // Find the persisted workflow id via the list API, then read it back and
  // assert the shell node layout round-tripped exactly.
  const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  expect(listRes.ok()).toBeTruthy()
  const list = await listRes.json()
  const wf = (list.workflows ?? []).find((w: { name: string }) => w.name === unique)
  expect(wf, `workflow ${unique} not found in list`).toBeTruthy()

  const getRes = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(wf.id)}`, { headers: AUTH_HEADERS })
  expect(getRes.ok()).toBeTruthy()
  const detail = await getRes.json()
  const shell = (detail.workflow?.config?.nodes ?? []).find((n: { id: string }) => n.id === 'shell')
  expect(shell, 'shell node missing after reopen').toBeTruthy()
  expect(shell.layout?.x).toBe(444)
  expect(shell.layout?.y).toBe(222)

  // Reopen in the UI: selecting the workflow row restores the canvas cleanly.
  await page.getByTestId(`graph-workflow-row-${wf.id}`).click()
  await expect(page.getByTestId('graph-node-shell')).toBeAttached()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible()
})

test('graph canvas: historical run replays read-only via ?graphEditJob deep-link', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'deeplink')
  await page.getByTestId('graph-name-input').fill(`e2e-replay-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok()).toBeTruthy()
  const startBody = await startResp.json()
  const runId: string = startBody.run?.id
  const jobId: string = startBody.run?.jobId
  expect(runId).toBeTruthy()
  expect(jobId).toMatch(/^job-/)

  await waitForRunStatus(request, jobId, ['completed', 'failed', 'timedOut'])

  // Starting the run navigated to the Chat page. The canvas page no longer
  // browses runs inline; the only way back to a run on the canvas is the
  // ?graphEditJob deep-link (the GraphLoop "Edit" button uses it). Navigating
  // there opens the run in read-only replay (a completed run is frozen).
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)
  // The shell node re-renders carrying its run-state class (run replay). React
  // Flow may not have run fitView yet, so assert attachment + run-state rather
  // than viewport visibility.
  const shellNode = page.getByTestId('graph-node-shell')
  await expect(shellNode).toBeAttached({ timeout: 10_000 })
  await expect(shellNode).toHaveClass(/run-succeeded/)
  // In run view, editing controls are replaced by the "exit run" button.
  await expect(page.getByTestId('graph-exit-run')).toBeVisible()
  await expect(page.getByTestId('graph-run')).toHaveCount(0)
  // A naturally completed run is frozen: no run-version edit entry.
  await expect(page.getByTestId('graph-edit-run')).toHaveCount(0)
})

// ---------------------------------------------------------------------------
// Task 20 — configuration management parity:
// copy, import/export, reset, dirty prompts, and import
// validation are all verified through the real UI and backend.
// ---------------------------------------------------------------------------

test('graph config management: save-as-new creates an independent copy', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'save-as')

  const originalName = `e2e-copy-source-${Date.now()}`
  const copyName = `e2e-copy-target-${Date.now()}`
  const cfg = linearShellConfig(workspace)
  cfg.nodes[1] = { ...cfg.nodes[1], config: { script: 'echo original-copy' }, layout: { x: 380, y: 180 } }

  await applyJsonConfig(page, cfg)
  await page.getByTestId('graph-name-input').fill(originalName)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  const original = await findWorkflowByName(request, originalName)

  const editedCopy = linearShellConfig(workspace)
  editedCopy.nodes[1] = { ...editedCopy.nodes[1], config: { script: 'echo copied-workflow' }, layout: { x: 620, y: 240 } }
  await applyJsonConfig(page, editedCopy)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()
  await page.getByTestId('graph-name-input').fill(copyName)
  await page.getByTestId('graph-save-as-new').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  const copy = await findWorkflowByName(request, copyName)
  expect(copy.id).not.toBe(original.id)

  const [originalRes, copyRes] = await Promise.all([
    request.get(`/api/v1/graph/workflow/${encodeURIComponent(original.id)}`, { headers: AUTH_HEADERS }),
    request.get(`/api/v1/graph/workflow/${encodeURIComponent(copy.id)}`, { headers: AUTH_HEADERS }),
  ])
  expect(originalRes.ok()).toBeTruthy()
  expect(copyRes.ok()).toBeTruthy()
  const originalBody = await originalRes.json()
  const copyBody = await copyRes.json()
  const originalShell = originalBody.workflow.config.nodes.find((n: { id: string }) => n.id === 'shell')
  const copyShell = copyBody.workflow.config.nodes.find((n: { id: string }) => n.id === 'shell')
  expect(originalShell.config.script).toBe('echo original-copy')
  expect(originalShell.layout.x).toBe(380)
  expect(copyShell.config.script).toBe('echo copied-workflow')
  expect(copyShell.layout.x).toBe(620)
})

test('graph config management: dirty badge responds to config edits and reset restores saved state', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'reset')

  const name = `e2e-reset-${Date.now()}`
  const cfg = linearShellConfig(workspace)
  cfg.nodes[1] = { ...cfg.nodes[1], config: { script: 'echo saved-state' }, layout: { x: 300, y: 160 } }
  await applyJsonConfig(page, cfg)
  await page.getByTestId('graph-name-input').fill(name)
  await page.getByTestId('graph-save').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  const workflow = await findWorkflowByName(request, name)

  const edited = linearShellConfig(workspace)
  edited.nodes[1] = { ...edited.nodes[1], config: { script: 'echo dirty-state' }, layout: { x: 300, y: 160 } }
  edited.variables = { dirty_var: '1' }
  edited.runConfig = { concurrencyLimit: 2 }
  await applyJsonConfig(page, edited)
  await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()
  await page.getByTestId('graph-reset').click()
  await expect(page.getByTestId('graph-clean-badge')).toBeVisible()

  const afterReset = await request.get(`/api/v1/graph/workflow/${encodeURIComponent(workflow.id)}`, { headers: AUTH_HEADERS })
  expect(afterReset.ok()).toBeTruthy()
  const body = await afterReset.json()
  const shell = body.workflow.config.nodes.find((n: { id: string }) => n.id === 'shell')
  expect(shell.config.script).toBe('echo saved-state')
  expect(body.workflow.config.variables?.dirty_var).toBeUndefined()
  expect(body.workflow.config.runConfig?.concurrencyLimit).toBe(4)

  await page.getByTestId('graph-view-json').click()
  await expect(page.getByTestId('graph-json-textarea')).toContainText('echo saved-state')
  await expect(page.getByTestId('graph-json-textarea')).not.toContainText('dirty-state')
})

// ---------------------------------------------------------------------------
// Run-time version editing (feature doc tasks 16/18/21 frontend closure):
// an editable (failed / resumable) run can be edited in place and saved as a
// new GraphRun version via PUT /run/:id/version.
// ---------------------------------------------------------------------------

// A graph whose shell exits non-zero, so the run reaches `failed` — a resumable
// (editable) terminal state, unlike a naturally `completed` run.
function failingShellConfig(workspace: GraphWorkspace): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
      { id: 'shell', type: 'shell', title: 'Boom', config: { script: 'exit 1' }, layout: { x: 320, y: 160 } },
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

test('graph run: edit a failed run in place and save a new version', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'run-edit')

  // Seed a failing graph via JSON and launch it.
  await page.getByTestId('graph-view-json').click()
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify(failingShellConfig(workspace)))
  await page.getByTestId('graph-json-apply').click()
  await page.getByTestId('graph-name-input').fill(`e2e-run-edit-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  const startBody = await startResp.json()
  const runId: string = startBody.run?.id
  const jobId: string = startBody.run?.jobId
  expect(runId, 'run start returned no run id').toBeTruthy()
  expect(jobId, 'run start returned no jobId').toMatch(/^job-/)

  // Wait for the run to fail (resumable/editable), then reselect for fresh state.
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status, 'a shell that exits 1 should fail the run').toBe('failed')

  // Confirm the baseline version before editing.
  const before = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(before.ok()).toBeTruthy()
  const baselineVersion: number = (await before.json()).run?.currentVersion
  expect(baselineVersion).toBe(1)

  // Launching navigated to the Chat page. Reopen the failed run via the
  // deep-link (the canvas no longer browses runs inline). A failed run is
  // editable, so this lands directly in edit mode.
  await page.goto(`/?workspaceId=${workspace.workspaceId}&view=graph&graphEditJob=${jobId}`)

  // The failed run is editable: the run-version edit badge + save are present.
  await expect(page.getByTestId('graph-run-editing-badge')).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('graph-save-run-version')).toBeVisible()

  // Repair the failed shell before saving. Unchanged snapshots are intentionally
  // treated as no-ops and covered separately by review test #63.
  const repaired = failingShellConfig(workspace)
  repaired.nodes[1] = {
    ...repaired.nodes[1],
    config: { script: 'echo repaired-run-version' },
  }
  await applyJsonConfig(page, repaired)

  // Save the edited run version -> PUT returns 200 and the run advances to a
  // new version that future instances would use.
  const [versionResp] = await Promise.all([
    page.waitForResponse(
      (r) => r.url().includes(`/api/v1/job/${jobId}/graph-run/version`) && r.request().method() === 'PUT',
    ),
    page.getByTestId('graph-save-run-version').click(),
  ])
  expect(versionResp.ok(), `version save failed: ${versionResp.status()} ${await versionResp.text()}`).toBeTruthy()

  // The editing badge clears and the persisted run carries the new version.
  await expect(page.getByTestId('graph-run-editing-badge')).toHaveCount(0)
  const after = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(after.ok()).toBeTruthy()
  const afterBody = await after.json()
  expect(afterBody.run?.currentVersion).toBe(baselineVersion + 1)
  expect((afterBody.run?.versions ?? []).length).toBeGreaterThanOrEqual(2)
})

test('graph run: version edit rejects changing a succeeded node config (backend safety net)', async ({ page, request }) => {
  const workspace = await openGraphCanvas(page, request, 'frozen')

  // start -> shellA (succeeds) -> shellB (fails) -> end. The run fails, but
  // shellA has a succeeded (frozen) instance. The frontend disables shellA's
  // fields; the backend still rejects a config change to it with a located error.
  const cfg: GraphConfig = {
    nodes: [
      { id: 'start', type: 'start', title: 'Start', layout: { x: 60, y: 160 } },
      { id: 'shellA', type: 'shell', title: 'OK', config: { script: 'echo ok' }, layout: { x: 260, y: 160 } },
      { id: 'shellB', type: 'shell', title: 'Boom', config: { script: 'exit 1' }, layout: { x: 460, y: 160 } },
      { id: 'end', type: 'end', title: 'End', layout: { x: 660, y: 160 } },
    ],
    edges: [
      { id: 'e1', sourceNodeId: 'start', targetNodeId: 'shellA' },
      { id: 'e2', sourceNodeId: 'shellA', targetNodeId: 'shellB' },
      { id: 'e3', sourceNodeId: 'shellB', targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 1 },
    workspaceId: workspace.workspaceId,
    workdir: workspace.workdir,
  }

  await page.getByTestId('graph-view-json').click()
  await page.getByTestId('graph-json-textarea').fill(JSON.stringify(cfg))
  await page.getByTestId('graph-json-apply').click()
  await page.getByTestId('graph-name-input').fill(`e2e-frozen-${Date.now()}`)

  const [startResp] = await Promise.all([
    page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
    page.getByTestId('graph-run').click(),
  ])
  expect(startResp.ok()).toBeTruthy()
  const startBody = await startResp.json()
  const runId: string = startBody.run?.id
  const jobId: string = startBody.run?.jobId
  expect(runId).toBeTruthy()
  expect(jobId).toMatch(/^job-/)
  const { status } = await waitForRunStatus(request, jobId, ['failed', 'completed', 'timedOut'])
  expect(status).toBe('failed')

  // Read the run's effective config, mutate the succeeded node's script, and PUT
  // it back. The route must return 400 with an error located to shellA.
  const statusRes = await request.get(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run`, { headers: AUTH_HEADERS })
  expect(statusRes.ok()).toBeTruthy()
  const run = (await statusRes.json()).run
  const versions = run.versions ?? []
  const effective = versions.find((v: { version: number }) => v.version === run.currentVersion)?.config ?? run.baseSnapshot?.config
  const edited = JSON.parse(JSON.stringify(effective))
  const a = edited.nodes.find((n: { id: string }) => n.id === 'shellA')
  a.config = { ...(a.config ?? {}), script: 'echo changed' }

  const putRes = await request.put(`/api/v1/job/${encodeURIComponent(jobId)}/graph-run/version`, {
    headers: { ...AUTH_HEADERS, 'Content-Type': 'application/json' },
    data: { config: edited },
  })
  expect(putRes.status(), `expected 400, got ${putRes.status()}: ${await putRes.text()}`).toBe(400)
  const body = await putRes.json()
  expect(Array.isArray(body.errors)).toBeTruthy()
  expect(body.errors.some((e: { nodeId?: string }) => e.nodeId === 'shellA')).toBeTruthy()
})

// ---------------------------------------------------------------------------
// Task 22 — mobile adaptation: the inspector is a real bottom drawer with an
// explicit collapse / expand control, and selecting a node reopens it.
// ---------------------------------------------------------------------------

test('graph mobile: inspector bottom drawer can collapse and reopens on node selection', async ({ page, request }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await openGraphCanvas(page, request, 'mobile')

  // Secondary actions collapse into the "⋯" overflow menu on mobile: hidden
  // until opened, visible inside the menu, hidden again after it closes.
  const validate = page.getByTestId('graph-validate')
  await expect(validate).toBeHidden()
  await page.getByTestId('graph-actions-more').click()
  await expect(validate).toBeVisible()
  await page.getByTestId('graph-actions-more').click()
  await expect(validate).toBeHidden()

  const inspector = page.getByTestId('graph-inspector')
  const toggle = page.getByTestId('graph-inspector-drawer-toggle')

  // The drawer starts collapsed on mobile so the canvas stays fully visible.
  await expect(inspector).toBeVisible()
  await expect(inspector).toHaveClass(/drawer-collapsed/)
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')

  await toggle.click()
  await expect(inspector).toHaveClass(/drawer-open/)
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')

  await toggle.click()
  await expect(inspector).toHaveClass(/drawer-collapsed/)
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')

  await page.getByTestId('graph-node-shell').click()
  await expect(inspector).toHaveClass(/drawer-open/)
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(toggle).toContainText('Shell')
})

test('graph mobile: maximized inspector drawer keeps its restore control reachable', async ({ page, request }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await openGraphCanvas(page, request, 'mobile')

  const inspector = page.getByTestId('graph-inspector')
  const toggle = page.getByTestId('graph-inspector-drawer-toggle')
  const expand = page.getByTestId('graph-inspector-drawer-expand')

  await toggle.click()
  await expect(inspector).toHaveClass(/drawer-open/)

  // Maximize, then restore. These clicks double as a regression check for the
  // drawer bar sliding under the sticky page header while maximized: if the
  // header covered it, Playwright's actionability check would fail the click.
  await expand.click()
  await expect(inspector).toHaveClass(/drawer-full/)
  await expect(expand).toHaveAttribute('aria-pressed', 'true')

  await expand.click()
  await expect(inspector).not.toHaveClass(/drawer-full/)
  await expect(expand).toHaveAttribute('aria-pressed', 'false')

  await toggle.click()
  await expect(inspector).toHaveClass(/drawer-collapsed/)
})
