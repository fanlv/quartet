# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: e2e/tests/graph-canvas.spec.ts >> graph mobile: inspector bottom drawer can collapse and reopens on node selection
- Location: e2e/tests/graph-canvas.spec.ts:498:1

# Error details

```
Error: page.goto: Protocol error (Page.navigate): Cannot navigate to invalid URL
Call log:
  - navigating to "/?workspaceId=ws-1&view=graph", waiting until "load"

```

# Test source

```ts
  1   | import { expect, test, type APIRequestContext, type Page } from '../fixtures/test'
  2   | import { e2eAuthToken } from '../fixtures/e2e-environment'
  3   | 
  4   | // This suite closes out the "独立 React Flow 画布" module (feature doc tasks 18
  5   | // and 19) with real end-to-end verification:
  6   | //
  7   | //   Task 18 — create / save / run / error-locate closure.
  8   | //   Task 19 — layout save-reopen + historical run replay stability.
  9   | //
  10  | // It drives the REAL backend (no QUARTET_E2E mode, no replay model). The graphs
  11  | // used here are pure-Shell (start -> Shell(echo) -> end), which run to a
  12  | // terminal `completed` status with NO model/ACP credentials — the harness seeds
  13  | // none by default. Assertions check structural / state signals (run reaches a
  14  | // terminal status, persisted layout round-trips, validation errors locate to a
  15  | // node) rather than model text.
  16  | 
  17  | const AUTH_HEADERS = { 'X-AGENT-AUTH': e2eAuthToken }
  18  | const WORKSPACE_ID = 'ws-1'
  19  | 
  20  | type GraphConfig = {
  21  |   name?: string
  22  |   nodes: Array<Record<string, unknown>>
  23  |   edges: Array<Record<string, unknown>>
  24  |   variables?: Record<string, string>
  25  |   disabledVars?: string[]
  26  |   runConfig?: Record<string, unknown>
  27  |   workspaceId?: string
  28  |   workdir?: string
  29  |   canvas?: Record<string, unknown>
  30  | }
  31  | 
  32  | type GraphWorkflowSummary = {
  33  |   id: string
  34  |   name: string
  35  | }
  36  | 
  37  | // A minimal, valid pure-Shell graph: start -> Shell(echo) -> end. Node ids are
  38  | // stable so the test can assert canvas selectors and persisted layout.
  39  | function linearShellConfig(): GraphConfig {
  40  |   return {
  41  |     nodes: [
  42  |       { id: 'start', type: 'start', title: 'Start', layout: { x: 80, y: 160 } },
  43  |       { id: 'shell', type: 'shell', title: 'Echo', config: { script: 'echo graph-e2e-ok' }, layout: { x: 320, y: 160 } },
  44  |       { id: 'end', type: 'end', title: 'End', layout: { x: 560, y: 160 } },
  45  |     ],
  46  |     edges: [
  47  |       { id: 'edge-start-shell', sourceNodeId: 'start', targetNodeId: 'shell' },
  48  |       { id: 'edge-shell-end', sourceNodeId: 'shell', targetNodeId: 'end' },
  49  |     ],
  50  |     variables: {},
  51  |     disabledVars: [],
  52  |     runConfig: { concurrencyLimit: 4 },
  53  |     workspaceId: WORKSPACE_ID,
  54  |   }
  55  | }
  56  | 
  57  | async function openGraphCanvas(page: Page) {
  58  |   await page.addInitScript((token) => {
  59  |     localStorage.setItem('quartet.x_auth_token', token)
  60  |     localStorage.setItem('quartet-language', 'en')
  61  |   }, e2eAuthToken)
> 62  |   await page.goto(`/?workspaceId=${WORKSPACE_ID}&view=graph`)
      |              ^ Error: page.goto: Protocol error (Page.navigate): Cannot navigate to invalid URL
  63  |   await expect(page.getByTestId('auth-gate')).toHaveCount(0)
  64  |   // The default config (start -> Shell -> end) renders on the canvas at mount.
  65  |   await expect(page.getByTestId('graph-node-start')).toBeVisible()
  66  |   await expect(page.getByTestId('graph-validate')).toBeVisible()
  67  | }
  68  | 
  69  | async function applyJsonConfig(page: Page, config: GraphConfig) {
  70  |   await page.getByTestId('graph-view-json').click()
  71  |   await page.getByTestId('graph-json-textarea').fill(JSON.stringify(config))
  72  |   await page.getByTestId('graph-json-apply').click()
  73  | }
  74  | 
  75  | async function findWorkflowByName(request: APIRequestContext, name: string): Promise<GraphWorkflowSummary> {
  76  |   const listRes = await request.get('/api/v1/graph/workflow/list', { headers: AUTH_HEADERS })
  77  |   expect(listRes.ok()).toBeTruthy()
  78  |   const list = await listRes.json()
  79  |   const workflow = (list.workflows ?? []).find((w: GraphWorkflowSummary) => w.name === name)
  80  |   expect(workflow, `workflow ${name} not found`).toBeTruthy()
  81  |   return workflow
  82  | }
  83  | 
  84  | // Poll the run status API until it reaches a terminal status (or times out).
  85  | async function waitForRunStatus(
  86  |   request: APIRequestContext,
  87  |   runId: string,
  88  |   terminal: string[],
  89  |   timeoutMs = 30_000,
  90  | ): Promise<{ status: string; progress?: { totalCount: number; completedCount: number } }> {
  91  |   const deadline = Date.now() + timeoutMs
  92  |   let last = 'unknown'
  93  |   while (Date.now() < deadline) {
  94  |     const res = await request.get(`/api/v1/graph/run/${encodeURIComponent(runId)}`, { headers: AUTH_HEADERS })
  95  |     expect(res.ok(), `run status fetch failed: ${res.status()} ${await res.text()}`).toBeTruthy()
  96  |     const body = await res.json()
  97  |     last = body.run?.status ?? 'unknown'
  98  |     if (terminal.includes(last)) {
  99  |       return { status: last, progress: body.progress ?? body.run?.progress }
  100 |     }
  101 |     await new Promise((r) => setTimeout(r, 400))
  102 |   }
  103 |   throw new Error(`run ${runId} did not reach ${terminal.join('/')} within ${timeoutMs}ms (last=${last})`)
  104 | }
  105 | 
  106 | // ---------------------------------------------------------------------------
  107 | // Task 18 — create / save / run / error-locate
  108 | // ---------------------------------------------------------------------------
  109 | 
  110 | test('graph canvas: create and save a workflow, then it appears in the library', async ({ page }) => {
  111 |   await openGraphCanvas(page)
  112 | 
  113 |   const unique = `e2e-create-${Date.now()}`
  114 |   await page.getByTestId('graph-name-input').fill(unique)
  115 | 
  116 |   // Saving a brand-new workflow uses the primary "Create" button.
  117 |   await expect(page.getByTestId('graph-dirty-badge')).toBeVisible()
  118 |   await page.getByTestId('graph-save').click()
  119 | 
  120 |   // Saved -> the dirty badge clears and the workflow shows up in the sidebar.
  121 |   await expect(page.getByTestId('graph-clean-badge')).toBeVisible({ timeout: 10_000 })
  122 |   await expect(page.locator('.graph-workflow-row-title', { hasText: unique })).toBeVisible()
  123 | })
  124 | 
  125 | test('graph canvas: run a pure-Shell workflow to completion', async ({ page, request }) => {
  126 |   await openGraphCanvas(page)
  127 |   await page.getByTestId('graph-name-input').fill(`e2e-run-${Date.now()}`)
  128 | 
  129 |   // Capture the run id from the start response so we can poll the backend.
  130 |   const [startResp] = await Promise.all([
  131 |     page.waitForResponse((r) => r.url().includes('/api/v1/graph/run/start') && r.request().method() === 'POST'),
  132 |     page.getByTestId('graph-run').click(),
  133 |   ])
  134 |   expect(startResp.ok(), `run start failed: ${startResp.status()} ${await startResp.text()}`).toBeTruthy()
  135 |   const startBody = await startResp.json()
  136 |   const runId: string = startBody.run?.id
  137 |   expect(runId, 'run start returned no run id').toBeTruthy()
  138 |   // The launch must have created and bound a Graph-type Job.
  139 |   const jobId: string = startBody.run?.jobId
  140 |   expect(jobId, 'run start returned no jobId').toMatch(/^job-/)
  141 | 
  142 |   // Starting a run jumps into the Chat page for the bound Graph Job (like
  143 |   // startloop): the URL gains the jobId, drops ?view=graph, and the GraphLoop
  144 |   // progress panel (with its embedded mini canvas) renders there.
  145 |   await expect(page).toHaveURL(new RegExp(`jobId=${jobId}`), { timeout: 10_000 })
  146 |   await expect(page).toHaveURL(/^(?!.*view=graph).*$/)
  147 |   await expect(page.getByTestId('graph-loop-progress')).toBeVisible({ timeout: 10_000 })
  148 |   await expect(page.getByTestId('graph-loop-canvas')).toBeVisible({ timeout: 10_000 })
  149 | 
  150 |   // The run reaches `completed`; a pure-Shell graph needs no credentials.
  151 |   const { status, progress } = await waitForRunStatus(request, runId, ['completed', 'failed', 'timedOut'])
  152 |   expect(status, 'pure-shell graph should complete').toBe('completed')
  153 |   expect(progress, 'completed run should report progress').toBeTruthy()
  154 |   expect(progress!.completedCount).toBe(progress!.totalCount)
  155 |   expect(progress!.totalCount).toBeGreaterThan(0)
  156 | 
  157 |   // The embedded mini canvas highlights the shell node as succeeded once the
  158 |   // run finishes (live SSE refresh drives the per-node run status).
  159 |   const miniShell = page.getByTestId('graph-loop-canvas').getByTestId('graph-node-shell')
  160 |   await expect(miniShell).toBeAttached({ timeout: 15_000 })
  161 |   await expect(miniShell).toHaveClass(/run-succeeded/, { timeout: 15_000 })
  162 | })
```