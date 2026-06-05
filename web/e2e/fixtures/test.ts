import { expect, test as base, type APIRequestContext, type APIResponse, type ConsoleMessage, type Page, type Request, type Response, type TestInfo } from '@playwright/test'
import fs from 'node:fs/promises'
import path from 'node:path'

type ConsoleEntry = {
  timestamp: string
  type: string
  text: string
  location: ReturnType<ConsoleMessage['location']>
}

type PageErrorEntry = {
  timestamp: string
  message: string
  stack?: string
}

type NetworkEntry = {
  timestamp: string
  method: string
  url: string
  resourceType?: string
  status?: number
  statusText?: string
  ok?: boolean
  failure?: string | null
}

type FailedResponseEntry = NetworkEntry & {
  bodyFile?: string
  bodyTruncated?: boolean
}

type E2EDiagnostics = {
  console: ConsoleEntry[]
  pageErrors: PageErrorEntry[]
  network: NetworkEntry[]
  failedResponses: FailedResponseEntry[]
  pendingWrites: Promise<void>[]
}

const maxEntries = 500
const maxBodyLength = 20_000

function now() {
  return new Date().toISOString()
}

function pushCapped<T>(entries: T[], entry: T) {
  entries.push(entry)
  if (entries.length > maxEntries) entries.shift()
}

function truncate(text: string) {
  if (text.length <= maxBodyLength) return { text, truncated: false }
  return { text: `${text.slice(0, maxBodyLength)}\n...[truncated ${text.length - maxBodyLength} chars]`, truncated: true }
}

function safeName(input: string) {
  return input.replace(/[^a-zA-Z0-9._-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 120) || 'test'
}

function diagnosticsRoot(testInfo: TestInfo) {
  const runDir = process.env.QUARTET_E2E_RUN_DIR
  const titlePath = 'titlePath' in testInfo && Array.isArray(testInfo.titlePath)
    ? testInfo.titlePath.join(' ')
    : testInfo.title
  if (runDir) return path.join(runDir, 'diagnostics', safeName(titlePath))
  return path.join(testInfo.outputDir, 'diagnostics')
}

async function writeJSON(filePath: string, value: unknown) {
  await fs.writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`, 'utf8')
}

async function captureResponseBody(response: Response | APIResponse) {
  try {
    const raw = await response.text()
    const { text, truncated } = truncate(raw)
    return { text, truncated }
  } catch (err) {
    return { text: `[failed to read response body] ${String(err)}`, truncated: false }
  }
}

async function persistDiagnostics(testInfo: TestInfo, diagnostics: E2EDiagnostics) {
  await Promise.allSettled(diagnostics.pendingWrites)
  const root = diagnosticsRoot(testInfo)
  await fs.mkdir(root, { recursive: true })

  const failedResponseDir = path.join(root, 'failed-response-bodies')
  if (diagnostics.failedResponses.length > 0) {
    await fs.mkdir(failedResponseDir, { recursive: true })
  }

  await Promise.all([
    writeJSON(path.join(root, 'console-summary.json'), {
      messages: diagnostics.console,
      pageErrors: diagnostics.pageErrors,
    }),
    writeJSON(path.join(root, 'network-summary.json'), diagnostics.network),
    writeJSON(path.join(root, 'failed-responses.json'), diagnostics.failedResponses),
  ])

  await testInfo.attach('e2e-console-summary', { path: path.join(root, 'console-summary.json'), contentType: 'application/json' })
  await testInfo.attach('e2e-network-summary', { path: path.join(root, 'network-summary.json'), contentType: 'application/json' })
  await testInfo.attach('e2e-failed-responses', { path: path.join(root, 'failed-responses.json'), contentType: 'application/json' })
}

async function recordFailedPageResponse(response: Response, diagnostics: E2EDiagnostics, testInfo: TestInfo) {
  const status = response.status()
  if (status < 400) return
  const request = response.request()
  const body = await captureResponseBody(response)
  const root = diagnosticsRoot(testInfo)
  const failedResponseDir = path.join(root, 'failed-response-bodies')
  await fs.mkdir(failedResponseDir, { recursive: true })
  const bodyFileName = `${String(diagnostics.failedResponses.length + 1).padStart(3, '0')}-${safeName(`${request.method()}-${status}-${new URL(response.url()).pathname}`)}.txt`
  await fs.writeFile(path.join(failedResponseDir, bodyFileName), body.text, 'utf8')
  pushCapped(diagnostics.failedResponses, {
    timestamp: now(),
    method: request.method(),
    url: response.url(),
    resourceType: request.resourceType(),
    status,
    statusText: response.statusText(),
    ok: response.ok(),
    bodyFile: path.join('failed-response-bodies', bodyFileName),
    bodyTruncated: body.truncated,
  })
}

function attachPageDiagnostics(page: Page, diagnostics: E2EDiagnostics, testInfo: TestInfo) {
  page.on('console', (message) => {
    pushCapped(diagnostics.console, {
      timestamp: now(),
      type: message.type(),
      text: message.text(),
      location: message.location(),
    })
  })
  page.on('pageerror', (error) => {
    pushCapped(diagnostics.pageErrors, {
      timestamp: now(),
      message: error.message,
      stack: error.stack,
    })
  })
  page.on('request', (request: Request) => {
    pushCapped(diagnostics.network, {
      timestamp: now(),
      method: request.method(),
      url: request.url(),
      resourceType: request.resourceType(),
    })
  })
  page.on('requestfailed', (request: Request) => {
    pushCapped(diagnostics.network, {
      timestamp: now(),
      method: request.method(),
      url: request.url(),
      resourceType: request.resourceType(),
      failure: request.failure()?.errorText ?? 'unknown request failure',
    })
  })
  page.on('response', (response: Response) => {
    const request = response.request()
    pushCapped(diagnostics.network, {
      timestamp: now(),
      method: request.method(),
      url: response.url(),
      resourceType: request.resourceType(),
      status: response.status(),
      statusText: response.statusText(),
      ok: response.ok(),
    })
    const pending = recordFailedPageResponse(response, diagnostics, testInfo).catch((err) => {
      pushCapped(diagnostics.pageErrors, {
        timestamp: now(),
        message: `failed to persist response body for ${response.url()}: ${String(err)}`,
      })
    })
    diagnostics.pendingWrites.push(pending)
  })
}

async function recordAPIResponse(opts: {
  method: string
  url: string
  requestBody?: string | null
  response: APIResponse
  diagnostics: E2EDiagnostics
  testInfo: TestInfo
}) {
  if (opts.response.status() < 400) return

  const body = await captureResponseBody(opts.response)
  const root = diagnosticsRoot(opts.testInfo)
  const failedResponseDir = path.join(root, 'failed-response-bodies')
  await fs.mkdir(failedResponseDir, { recursive: true })
  const bodyFileName = `${String(opts.diagnostics.failedResponses.length + 1).padStart(3, '0')}-${safeName(`${opts.method}-${opts.response.status()}-${new URL(opts.url, 'http://127.0.0.1').pathname}`)}.txt`
  await fs.writeFile(path.join(failedResponseDir, bodyFileName), body.text, 'utf8')
  pushCapped(opts.diagnostics.failedResponses, {
    timestamp: now(),
    method: opts.method,
    url: opts.url,
    status: opts.response.status(),
    statusText: opts.response.statusText(),
    ok: opts.response.ok(),
    bodyFile: path.join('failed-response-bodies', bodyFileName),
    bodyTruncated: body.truncated,
  })
}

function requestBodyFromOptions(options: unknown) {
  if (!options || typeof options !== 'object') return null
  const maybeData = (options as { data?: unknown; form?: unknown; multipart?: unknown }).data ??
    (options as { form?: unknown }).form ??
    (options as { multipart?: unknown }).multipart
  if (maybeData === undefined) return null
  try {
    return typeof maybeData === 'string' ? maybeData : JSON.stringify(maybeData)
  } catch {
    return '[unserializable request body]'
  }
}

function wrapRequestContext(request: APIRequestContext, diagnostics: E2EDiagnostics, testInfo: TestInfo): APIRequestContext {
  return new Proxy(request, {
    get(target, prop, receiver) {
      const original = Reflect.get(target, prop, receiver)
      if (typeof original !== 'function') return original
      const methodName = String(prop).toUpperCase()
      if (!['DELETE', 'FETCH', 'GET', 'HEAD', 'PATCH', 'POST', 'PUT'].includes(methodName)) {
        return original.bind(target)
      }
      return async (...args: unknown[]) => {
        const url = String(args[0] ?? '')
        const requestMethod = methodName === 'FETCH'
          ? String((args[1] as { method?: string } | undefined)?.method ?? 'GET').toUpperCase()
          : methodName
        const requestBody = requestBodyFromOptions(args[1])
        const response = await original.apply(target, args) as APIResponse
        await recordAPIResponse({ method: requestMethod, url, requestBody, response, diagnostics, testInfo })
        return response
      }
    },
  }) as APIRequestContext
}

export const test = base.extend<{ diagnostics: E2EDiagnostics }>({
  diagnostics: async ({}, use, testInfo) => {
    const diagnostics: E2EDiagnostics = {
      console: [],
      pageErrors: [],
      network: [],
      failedResponses: [],
      pendingWrites: [],
    }
    await use(diagnostics)
    await persistDiagnostics(testInfo, diagnostics)
  },
  page: async ({ page, diagnostics }, use, testInfo) => {
    attachPageDiagnostics(page, diagnostics, testInfo)
    await use(page)
  },
  request: async ({ request, diagnostics }, use, testInfo) => {
    await use(wrapRequestContext(request, diagnostics, testInfo))
  },
})

export { expect }
export type { APIRequestContext, Page }
