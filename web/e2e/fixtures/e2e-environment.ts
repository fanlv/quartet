import type { ChildProcessWithoutNullStreams } from 'node:child_process'
import { spawn } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

export const e2eAuthToken = process.env.QUARTET_E2E_AUTH_TOKEN || 'quartet-e2e-token'

const backendPort = Number(process.env.QUARTET_E2E_BACKEND_PORT || 18090)
const frontendPort = Number(process.env.VITE_E2E_PORT || 5174)
const backendURL = process.env.VITE_E2E_BACKEND_URL || `http://127.0.0.1:${backendPort}`
const frontendURL = `http://127.0.0.1:${frontendPort}`

// E2E now drives the REAL model flow (no replay model, no QUARTET_E2E mode).
// The isolated backend therefore needs real provider credentials, supplied
// via env. We seed an isolated models.json + settings.json into the temp
// LOCAL_MEMORY so the run is self-contained but still calls a live model.
// Missing credentials fail the run loudly rather than silently skipping —
// a green E2E must mean the real chat/tool/loop links actually ran.
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
  for (const dir of ['workspaces', 'knowledge', 'agent', 'bin', 'shell', 'im']) {
    fs.mkdirSync(path.join(localMemory, dir), { recursive: true })
  }
  fs.chmodSync(path.join(localMemory, 'workspaces'), 0o777)
}

// seedModelConfig writes an isolated models.json + settings.json into the
// temp LOCAL_MEMORY so the backend has a usable REAL model without going
// through the online provider-validation UI. Credentials come from env; if
// the API key is missing we fail the whole run with an explicit message so
// nobody mistakes a skipped real-model run for a passing one.
function seedModelConfig(localMemory: string) {
  if (!e2eModelAPIKey) {
    throw new Error(
      'E2E real-model run requires QUARTET_E2E_MODEL_API_KEY (and optionally ' +
        'QUARTET_E2E_MODEL_CLASS / _MODEL_NAME / _BASE_URL / _MODEL_ID). ' +
        'No credentials were provided, so the chat / tool / loop links cannot ' +
        'run against a live model. Set the credentials or run `make test-web` ' +
        '(component + Go layers) which need no model.',
    )
  }
  if (!e2eModelName) {
    throw new Error(
      'E2E real-model run requires QUARTET_E2E_MODEL_NAME (the upstream model ' +
        'identifier, e.g. the provider model id). It was empty.',
    )
  }

  const now = Date.now()
  const models = {
    models: [
      {
        id: Number(e2eModelID),
        model_class: e2eModelClass,
        display_name: e2eModelDisplayName,
        connection: {
          api_key: e2eModelAPIKey,
          base_url: e2eModelBaseURL || undefined,
          model: e2eModelName,
        },
        status: 1,
        created_at: now,
        updated_at: now,
      },
    ],
  }
  fs.writeFileSync(path.join(localMemory, 'agent', 'models.json'), `${JSON.stringify(models, null, 2)}\n`)

  const settings = {
    username: 'Quartet E2E',
    avatar_url: '',
    title_agent: { agent_type: 'eino', model_id: e2eModelID },
    message_agent: { agent_type: 'eino', model_id: e2eModelID },
    im_workspace_id: 'ws-1',
  }
  fs.writeFileSync(path.join(localMemory, 'agent', 'settings.json'), `${JSON.stringify(settings, null, 2)}\n`)
}

function lastRunPassed() {
  try {
    const raw = fs.readFileSync(path.join(e2eDir, 'test-results', 'artifacts', '.last-run.json'), 'utf8')
    return JSON.parse(raw)?.status === 'passed'
  } catch {
    return false
  }
}

function cleanupPassedRunOnExit(runDir: string) {
  let cleaned = false
  const cleanup = () => {
    if (cleaned) return
    cleaned = true
    if (lastRunPassed()) {
      fs.rmSync(runDir, { recursive: true, force: true })
    } else {
      console.log(`[e2e] artifacts retained at ${runDir}`)
    }
  }
  process.once('beforeExit', cleanup)
  process.once('exit', cleanup)
}

async function globalSetup() {
  validatePort('QUARTET_E2E_BACKEND_PORT', backendPort)
  validatePort('VITE_E2E_PORT', frontendPort)

  const runDir = createRunDir()
  process.env.QUARTET_E2E_RUN_DIR = runDir
  const logDir = path.join(runDir, 'logs')
  const localMemory = path.join(runDir, 'local-memory')
  fs.mkdirSync(logDir, { recursive: true })
  prepareLocalMemory(localMemory)
  seedModelConfig(localMemory)

  fs.writeFileSync(path.join(runDir, 'env.json'), `${JSON.stringify({
    backendURL,
    frontendURL,
    localMemory,
    repoRoot,
    webDir,
    pid: process.pid,
    platform: os.platform(),
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
        X_AGENT_AUTH: e2eAuthToken,
        QUARTET_LISTEN_ADDR: `127.0.0.1:${backendPort}`,
      },
      logDir,
    })
    processes.push(backend)
    await waitForHTTP(`${backendURL}/api/v1/health`, 30_000, processes)

    const frontend = startProcess({
      name: 'frontend',
      command: 'npm',
      args: ['run', 'dev', '--', '--host', '127.0.0.1'],
      cwd: webDir,
      env: {
        ...process.env,
        VITE_E2E_BACKEND_URL: backendURL,
        VITE_E2E_PORT: String(frontendPort),
      },
      logDir,
    })
    processes.push(frontend)
    await waitForHTTP(frontendURL, 30_000, processes)
  } catch (err) {
    await Promise.all(processes.map(stopProcess))
    console.error(`[e2e] startup failed; artifacts retained at ${runDir}`)
    throw err
  }

  return async () => {
    await Promise.all(processes.map(stopProcess))
    const keepArtifacts = process.env.QUARTET_E2E_KEEP_ARTIFACTS === '1'
    if (keepArtifacts) {
      console.log(`[e2e] artifacts retained at ${runDir}`)
    } else {
      // Playwright writes .last-run.json after global teardown. Defer the
      // pass/fail cleanup decision until process exit so successful runs can
      // remove the isolated LOCAL_MEMORY/log directory while failed runs keep
      // it for debugging.
      cleanupPassedRunOnExit(runDir)
    }
  }
}

export default globalSetup
