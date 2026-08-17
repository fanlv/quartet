import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import { execFileSync, spawn } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export const e2eAuthToken = process.env.QUARTET_E2E_AUTH_TOKEN || 'quartet-e2e-token'
export const e2eLegacyFirstModelJobID = 'job-e2e-legacy-first-model'
export const e2eLegacyFirstModelID = 'e2e-legacy-first-model'
export const e2eInterruptedRunningJobID = 'job-e2e-interrupted-running'
export const e2ePersistWarningJobID = 'job-e2e-persist-warning-without-last-error'

const backendPort = Number(process.env.QUARTET_E2E_BACKEND_PORT || 18090)
const frontendPort = Number(process.env.VITE_E2E_PORT || 5174)
const backendURL = process.env.VITE_E2E_BACKEND_URL || `http://127.0.0.1:${backendPort}`
const frontendURL = `http://127.0.0.1:${frontendPort}`
export const e2eBackendURL = backendURL

// E2E drives REAL agent links (no replay model, no QUARTET_E2E mode). The
// primary chat-link coverage runs against an installed ACP agent discovered
// at runtime from the backend's own probe list (/api/v1/agent/list), so the
// default run needs NO model credentials — the ACP subprocess carries its own
// login state in $HOME.
//
// Seeding an eino-cli model is OPTIONAL: only when QUARTET_E2E_MODEL_API_KEY
// is supplied do we build eino-cli onto the backend's PATH and write an
// isolated model catalog (EINO_HOME) so the eino-cli chat/title link can also
// be exercised. Without it the run still boots and the ACP + link-agnostic
// specs run normally against whatever ACP agents the machine has installed.
//
// e2eAgentType is the ACP serve command of the in-repo eino-cli agent — the
// value Session.Type carries for it. Job-record fixtures use it as a
// realistic stand-in agent type.
export const e2eAgentType = 'eino-cli acp'
export const e2eModelClass = process.env.QUARTET_E2E_MODEL_CLASS || 'ark'
export const e2eModelID = process.env.QUARTET_E2E_MODEL_ID || '1000001'
const e2eModelDisplayName = process.env.QUARTET_E2E_MODEL_DISPLAY_NAME || 'Quartet E2E Model'
const e2eModelName = process.env.QUARTET_E2E_MODEL_NAME || ''
const e2eModelAPIKey = process.env.QUARTET_E2E_MODEL_API_KEY || ''
const e2eModelBaseURL = process.env.QUARTET_E2E_MODEL_BASE_URL || ''

const thisFile = fileURLToPath(import.meta.url)
const fixturesDir = path.dirname(thisFile)
const e2eDir = path.resolve(fixturesDir, '..')
const webDir = path.resolve(e2eDir, '..')
const repoRoot = path.resolve(webDir, '..')
const runRoot = path.join(e2eDir, 'test-results', 'runs')

type ManagedProcess = {
  name: string
  proc: ChildProcessWithoutNullStreams
  stdoutPath: string
  stderrPath: string
  stdout: string[]
  stderr: string[]
}

function validatePort(name: string, port: number) {
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`Invalid ${name}: ${port}`)
  }
}

function appendChunk(chunks: string[], chunk: Buffer) {
  chunks.push(chunk.toString())
  if (chunks.length > 80) chunks.shift()
}

function startProcess(opts: {
  name: string
  command: string
  args: string[]
  cwd: string
  env: NodeJS.ProcessEnv
  logDir: string
}): ManagedProcess {
  const stdoutPath = path.join(opts.logDir, `${opts.name}.stdout.log`)
  const stderrPath = path.join(opts.logDir, `${opts.name}.stderr.log`)
  const stdoutStream = fs.createWriteStream(stdoutPath, { flags: 'a' })
  const stderrStream = fs.createWriteStream(stderrPath, { flags: 'a' })
  const proc = spawn(opts.command, opts.args, {
    cwd: opts.cwd,
    env: opts.env,
    detached: process.platform !== 'win32',
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  const managed: ManagedProcess = { name: opts.name, proc, stdoutPath, stderrPath, stdout: [], stderr: [] }

  proc.stdout.on('data', (chunk: Buffer) => {
    appendChunk(managed.stdout, chunk)
    stdoutStream.write(chunk)
  })
  proc.stderr.on('data', (chunk: Buffer) => {
    appendChunk(managed.stderr, chunk)
    stderrStream.write(chunk)
  })
  proc.on('close', () => {
    stdoutStream.end()
    stderrStream.end()
  })
  proc.on('error', (err) => {
    stderrStream.write(`\n[process error] ${err.stack || err.message}\n`)
  })

  return managed
}

function processTail(p: ManagedProcess) {
  return [
    `--- ${p.name} stdout tail (${p.stdoutPath}) ---`,
    p.stdout.join('').trim() || '<empty>',
    `--- ${p.name} stderr tail (${p.stderrPath}) ---`,
    p.stderr.join('').trim() || '<empty>',
  ].join('\n')
}
async function stopProcess(p: ManagedProcess) {
  if (p.proc.exitCode !== null || p.proc.signalCode !== null) return
  const pid = p.proc.pid
  if (!pid) return
  const waitForExit = async (timeoutMs: number) => {
    if (p.proc.exitCode !== null || p.proc.signalCode !== null) return true
    return await new Promise<boolean>((resolve) => {
      const timer = setTimeout(() => {
        p.proc.off('exit', onExit)
        resolve(false)
      }, timeoutMs)
      const onExit = () => {
        clearTimeout(timer)
        resolve(true)
      }
      p.proc.once('exit', onExit)
    })
  }
  try {
    if (process.platform === 'win32') {
      p.proc.kill('SIGTERM')
    } else {
      process.kill(-pid, 'SIGTERM')
    }
  } catch {
    // already exited
  }
  if (await waitForExit(1_000)) return
  try {
    if (process.platform === 'win32') {
      p.proc.kill('SIGKILL')
    } else {
      process.kill(-pid, 'SIGKILL')
    }
  } catch {
    // already exited
  }
  await waitForExit(1_000)
}

async function waitForHTTP(url: string, timeoutMs: number, processes: ManagedProcess[]) {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (Date.now() < deadline) {
    for (const p of processes) {
      if (p.proc.exitCode !== null) {
        throw new Error(`${p.name} exited before ${url} became ready, exitCode=${p.proc.exitCode}\n${processTail(p)}`)
      }
    }
    try {
      const res = await fetch(url, { cache: 'no-store' })
      if (res.ok) return
      lastError = new Error(`HTTP ${res.status} ${await res.text()}`)
    } catch (err) {
      lastError = err
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new Error(`Timed out waiting for ${url}: ${String(lastError)}\n${processes.map(processTail).join('\n')}`)
}

function createRunDir() {
  fs.mkdirSync(runRoot, { recursive: true })
  const stamp = new Date().toISOString().replace(/[:.]/g, '-')
  return fs.mkdtempSync(path.join(runRoot, `${stamp}-`))
}

function prepareLocalMemory(localMemory: string) {
  for (const dir of [
    'quartet/config/prompts',
    'quartet/config/templates',
    'quartet/config/graph-workflows',
    'quartet/config/schedules',
    'quartet/data/usage-stats',
    'quartet/data/workspaces',
    'quartet/data/im',
    'quartet/data/uploads/im-media',
    'quartet/data/user-input',
    'quartet/data/wechat',
    'var/quartet/state/schedules',
    'var/quartet/state/sandbox/compose',
    'var/quartet/cache/im-media',
    'var/quartet/tmp/shell',
    'knowledge',
  ]) {
    fs.mkdirSync(path.join(localMemory, dir), { recursive: true })
  }
  fs.chmodSync(path.join(localMemory, 'quartet', 'data', 'workspaces'), 0o777)
  fs.writeFileSync(path.join(localMemory, 'quartet', 'layout.json'), `${JSON.stringify({
    version: 1,
    status: 'complete',
    batchId: 'playwright-e2e-layout-v1',
    completedAt: new Date().toISOString(),
  }, null, 2)}\n`)
}

// seedAgentConfig writes settings.json (always) and, when E2E model
// credentials are supplied, an isolated model into the eino-cli model catalog
// (einoHome/models.json — eino-cli's own store, not quartet's).
//
//   - settings.json always carries the E2E username + default IM workspace.
//   - When QUARTET_E2E_MODEL_API_KEY (and _MODEL_NAME) are set, an eino-cli
//     model is seeded and title/message agents point at it (`eino-cli acp`),
//     enabling the headless `eino-cli -p` title link.
//   - Otherwise the eino-cli catalog stays empty: the default run relies on an installed
//     ACP agent (discovered at runtime) for chat-link coverage. The ACP
//     subprocess uses its own login state in $HOME, so the run must NOT fail
//     for lack of eino-cli model credentials.
function seedAgentConfig(localMemory: string, einoHome: string) {
  const settings: Record<string, unknown> = {
    username: 'Quartet E2E',
    avatar_url: '',
    im_workspace_id: 'ws-1',
  }

  if (e2eModelAPIKey) {
    if (!e2eModelName) {
      throw new Error(
        'QUARTET_E2E_MODEL_API_KEY was set but QUARTET_E2E_MODEL_NAME (the ' +
          'upstream model identifier) was empty — cannot seed an eino-cli model. ' +
          'Set both, or unset the API key to run the default ACP-only flow.',
      )
    }
    const now = Date.now()
    const models = [
      {
        id: e2eModelID,
        model_class: e2eModelClass,
        display_name: e2eModelDisplayName,
        connection: {
          api_key: e2eModelAPIKey,
          base_url: e2eModelBaseURL || undefined,
          model: e2eModelName,
        },
        created_at: now,
        updated_at: now,
      },
    ]
    fs.mkdirSync(einoHome, { recursive: true })
    fs.writeFileSync(path.join(einoHome, 'models.json'), `${JSON.stringify(models, null, 2)}\n`, { mode: 0o600 })
    settings.agent_role_settings_version = 1
    settings.title_generation_agent = { agent_id: 'eino-cli' }
    settings.group_reply_agent = { agent_id: 'eino-cli' }
    settings.im_session_agent = { agent_id: 'eino-cli', model_id: e2eModelID }
  }

  fs.writeFileSync(path.join(localMemory, 'quartet', 'data', 'settings.json'), `${JSON.stringify(settings, null, 2)}\n`)
}

function seedLegacyFirstModelIDFixture(localMemory: string) {
  const jobID = e2eLegacyFirstModelJobID
  const deletedSessionID = 'session-e2e-legacy-deleted'
  const liveSessionID = 'session-e2e-legacy-live'
  const now = new Date().toISOString()
  const jobDir = path.join(localMemory, 'quartet', 'data', 'workspaces', 'ws-1', 'jobs', jobID)
  const jobMetaDir = path.join(jobDir, '.meta')
  fs.mkdirSync(jobMetaDir, { recursive: true })
  fs.writeFileSync(path.join(jobMetaDir, 'job.json'), `${JSON.stringify({
    id: jobID,
    title: 'E2E Legacy FirstModelID Prefill',
    createdAt: now,
    updatedAt: now,
    mode: 'interactive',
    workspaceId: 'ws-1',
    status: 'completed',
    sessionIds: [deletedSessionID, liveSessionID],
    // Deliberately omit firstModelId to emulate legacy records persisted
    // before the denormalized Job list cache existed.
  }, null, 2)}\n`)

  for (const session of [
    { id: deletedSessionID, title: 'deleted legacy session', model_id: 'deleted-model', deleted: true },
    { id: liveSessionID, title: 'live legacy session', model_id: e2eLegacyFirstModelID },
  ]) {
    const metaDir = path.join(jobDir, 'sessions', session.id, '.meta')
    fs.mkdirSync(metaDir, { recursive: true })
    fs.writeFileSync(path.join(metaDir, 'meta.json'), `${JSON.stringify({
      id: session.id,
      title: session.title,
      created_at: now,
      updated_at: now,
      deleted: session.deleted || undefined,
      model_id: session.model_id,
      type: e2eAgentType,
      job_id: jobID,
      workspace_id: 'ws-1',
    }, null, 2)}\n`)
  }
}

function seedInterruptedRunningJobFixture(localMemory: string) {
  const now = new Date().toISOString()
  const jobMetaDir = path.join(localMemory, 'quartet', 'data', 'workspaces', 'ws-1', 'jobs', e2eInterruptedRunningJobID, '.meta')
  fs.mkdirSync(jobMetaDir, { recursive: true })
  fs.writeFileSync(path.join(jobMetaDir, 'job.json'), `${JSON.stringify({
    id: e2eInterruptedRunningJobID,
    title: 'E2E Interrupted Running Job',
    createdAt: now,
    updatedAt: now,
    mode: 'loop',
    workspaceId: 'ws-1',
    status: 'running',
    sessionIds: [],
    loopConfig: {
      flow: [
        {
          id: 'e2e-interrupted-step',
          type: 'step',
          message: 'This job was running before backend startup',
          repeatCount: 1,
          roundMode: 'beforeRound',
          roundType: 'prompt',
        },
      ],
    },
    // Deliberately omit progress to exercise startup reconciliation of legacy
    // records and interrupted in-flight jobs.
  }, null, 2)}\n`)
}

function seedPersistWarningJobFixture(localMemory: string) {
  const now = new Date().toISOString()
  const warning = 'persist failed after iteration_started: injected e2e disk warning'
  const jobMetaDir = path.join(localMemory, 'quartet', 'data', 'workspaces', 'ws-1', 'jobs', e2ePersistWarningJobID, '.meta')
  fs.mkdirSync(jobMetaDir, { recursive: true })
  fs.writeFileSync(path.join(jobMetaDir, 'job.json'), `${JSON.stringify({
    id: e2ePersistWarningJobID,
    title: 'E2E Persist Warning Without LastError',
    createdAt: now,
    updatedAt: now,
    mode: 'loop',
    workspaceId: 'ws-1',
    status: 'completed',
    sessionIds: [],
    loopConfig: {
      flow: [
        {
          id: 'e2e-persist-warning-step',
          type: 'step',
          message: 'This fixture has a persistence warning but no run failure',
          repeatCount: 1,
          roundMode: 'beforeRound',
          roundType: 'prompt',
        },
      ],
    },
    progress: {
      totalSteps: 1,
      currentPath: [0, 0],
      completedCount: 1,
      failedCount: 0,
      results: [
        {
          path: [0, 0],
          success: true,
          durationMs: 0,
          content: 'completed before a best-effort persist warning was recorded',
        },
      ],
      persistWarnings: [warning],
      // Deliberately omit lastError: persistence warnings must stay separate
      // from the user-visible run failure reason.
    },
  }, null, 2)}\n`)
}

function lastRunPassed() {
  try {
    const raw = fs.readFileSync(path.join(e2eDir, 'test-results', 'artifacts', '.last-run.json'), 'utf8')
    return JSON.parse(raw)?.status === 'passed'
  } catch {
    return false
  }
}

function cleanupPassedRunOnExit(runDir: string, extraDirs: string[] = []) {
  let cleaned = false
  const cleanup = () => {
    if (cleaned) return
    cleaned = true
    for (const dir of extraDirs) fs.rmSync(dir, { recursive: true, force: true })
    if (lastRunPassed()) {
      fs.rmSync(runDir, { recursive: true, force: true })
    } else {
      console.log(`[e2e] artifacts retained at ${runDir}`)
    }
  }
  process.once('beforeExit', cleanup)
  process.once('exit', cleanup)
}

function createExternalTempDir(prefix: string) {
  return fs.mkdtempSync(path.join(os.tmpdir(), prefix))
}

async function globalSetup() {
  validatePort('QUARTET_E2E_BACKEND_PORT', backendPort)
  validatePort('VITE_E2E_PORT', frontendPort)

  const runDir = createRunDir()
  process.env.QUARTET_E2E_RUN_DIR = runDir
  const logDir = path.join(runDir, 'logs')
  const localMemory = path.join(runDir, 'local-memory')
  const goCache = path.join(runDir, 'go-build-cache')
  const viteCache = path.join(runDir, 'vite-cache')
  const certsDir = path.join(runDir, 'certs-empty')
  const goTmp = createExternalTempDir('quartet-e2e-go-tmp-')
  fs.mkdirSync(logDir, { recursive: true })
  fs.mkdirSync(goCache, { recursive: true })
  fs.mkdirSync(viteCache, { recursive: true })
  fs.mkdirSync(certsDir, { recursive: true })

  // Build the in-repo eino-cli and put it on the backend's PATH ONLY when E2E
  // model credentials are supplied (seedAgentConfig then also seeds its model
  // catalog). eino-cli tops the probe list, so without a seeded model it would
  // become the homepage default agent and every chat would fail with
  // "no model configured"; without credentials the run must fall back to an
  // installed ACP agent, exactly like a machine without eino-cli. EINO_HOME
  // points at an isolated per-run dir so eino-cli's own store (model catalog,
  // sessions) never touches the developer's real ~/.eino.
  const einoOnPath = Boolean(e2eModelAPIKey)
  const einoBinDir = path.join(runDir, 'eino-bin')
  const einoHome = path.join(runDir, 'eino-home')
  fs.mkdirSync(einoHome, { recursive: true })
  if (einoOnPath) {
    fs.mkdirSync(einoBinDir, { recursive: true })
    execFileSync('go', ['build', '-o', path.join(einoBinDir, 'eino-cli'), './cmd/eino-cli'], {
      cwd: repoRoot,
      env: { ...process.env, GOCACHE: goCache, GOTMPDIR: goTmp },
      stdio: 'inherit',
    })
  }

  prepareLocalMemory(localMemory)
  seedAgentConfig(localMemory, einoHome)
  seedLegacyFirstModelIDFixture(localMemory)
  seedInterruptedRunningJobFixture(localMemory)
  seedPersistWarningJobFixture(localMemory)

  fs.writeFileSync(path.join(runDir, 'env.json'), `${JSON.stringify({
    backendURL,
    frontendURL,
    localMemory,
    repoRoot,
    webDir,
    pid: process.pid,
    platform: os.platform(),
    goTmp,
  }, null, 2)}\n`)
  console.log(`[e2e] run artifacts: ${runDir}`)

  const processes: ManagedProcess[] = []
  try {
    const backend = startProcess({
      name: 'backend',
      command: 'go',
      args: ['run', './cmd/web'],
      cwd: repoRoot,
      env: {
        ...process.env,
        LOCAL_MEMORY: localMemory,
        GOCACHE: goCache,
        GOTMPDIR: goTmp,
        // In-repo eino-cli on PATH (probe discovery) + isolated eino-cli home —
        // only when E2E model credentials seeded its catalog (see above).
        ...(einoOnPath
          ? { PATH: `${einoBinDir}${path.delimiter}${process.env.PATH || ''}`, EINO_HOME: einoHome }
          : {}),
        X_AGENT_AUTH: e2eAuthToken,
        QUARTET_LISTEN_ADDR: `127.0.0.1:${backendPort}`,
        // The repository may contain production certs. E2E always exercises
        // its isolated loopback backend over plain HTTP.
        QUARTET_CERTS_DIR: certsDir,
      },
      logDir,
    })
    processes.push(backend)
    // ACP discovery refreshes asynchronously, so an empty isolated cache must
    // not delay backend health readiness.
    await waitForHTTP(`${backendURL}/api/v1/health`, 30_000, processes)

    const frontend = startProcess({
      name: 'frontend',
      command: 'npm',
      args: ['run', 'dev', '--', '--host', '127.0.0.1', '--configLoader', 'runner'],
      cwd: webDir,
      env: {
        ...process.env,
        VITE_E2E_BACKEND_URL: backendURL,
        VITE_E2E_PORT: String(frontendPort),
        VITE_CACHE_DIR: viteCache,
      },
      logDir,
    })
    processes.push(frontend)
    await waitForHTTP(frontendURL, 30_000, processes)
  } catch (err) {
    await Promise.all(processes.map(stopProcess))
    fs.rmSync(goTmp, { recursive: true, force: true })
    console.error(`[e2e] startup failed; artifacts retained at ${runDir}`)
    throw err
  }

  return async () => {
    await Promise.all(processes.map(stopProcess))
    const keepArtifacts = process.env.QUARTET_E2E_KEEP_ARTIFACTS === '1'
    fs.rmSync(goTmp, { recursive: true, force: true })
    if (keepArtifacts) {
      console.log(`[e2e] artifacts retained at ${runDir}`)
    } else {
      // Playwright writes .last-run.json after global teardown. Defer the
      // pass/fail cleanup decision until process exit so successful runs can
      // remove the isolated LOCAL_MEMORY/log directory while failed runs keep
      // it for debugging.
      cleanupPassedRunOnExit(runDir, [goTmp])
    }
  }
}

export default globalSetup
