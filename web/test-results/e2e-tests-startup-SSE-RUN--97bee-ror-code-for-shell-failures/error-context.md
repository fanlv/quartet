# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: e2e/tests/startup.spec.ts >> SSE RUN_ERROR carries a structured SHELL error code for shell failures
- Location: e2e/tests/startup.spec.ts:809:1

# Error details

```
TypeError: apiRequestContext.apply: Invalid URL
```

# Test source

```ts
  143 |     pushCapped(diagnostics.pageErrors, {
  144 |       timestamp: now(),
  145 |       message: error.message,
  146 |       stack: error.stack,
  147 |     })
  148 |   })
  149 |   page.on('request', (request: Request) => {
  150 |     pushCapped(diagnostics.network, {
  151 |       timestamp: now(),
  152 |       method: request.method(),
  153 |       url: request.url(),
  154 |       resourceType: request.resourceType(),
  155 |     })
  156 |   })
  157 |   page.on('requestfailed', (request: Request) => {
  158 |     pushCapped(diagnostics.network, {
  159 |       timestamp: now(),
  160 |       method: request.method(),
  161 |       url: request.url(),
  162 |       resourceType: request.resourceType(),
  163 |       failure: request.failure()?.errorText ?? 'unknown request failure',
  164 |     })
  165 |   })
  166 |   page.on('response', (response: Response) => {
  167 |     const request = response.request()
  168 |     pushCapped(diagnostics.network, {
  169 |       timestamp: now(),
  170 |       method: request.method(),
  171 |       url: response.url(),
  172 |       resourceType: request.resourceType(),
  173 |       status: response.status(),
  174 |       statusText: response.statusText(),
  175 |       ok: response.ok(),
  176 |     })
  177 |     const pending = recordFailedPageResponse(response, diagnostics, testInfo).catch((err) => {
  178 |       pushCapped(diagnostics.pageErrors, {
  179 |         timestamp: now(),
  180 |         message: `failed to persist response body for ${response.url()}: ${String(err)}`,
  181 |       })
  182 |     })
  183 |     diagnostics.pendingWrites.push(pending)
  184 |   })
  185 | }
  186 | 
  187 | async function recordAPIResponse(opts: {
  188 |   method: string
  189 |   url: string
  190 |   requestBody?: string | null
  191 |   response: APIResponse
  192 |   diagnostics: E2EDiagnostics
  193 |   testInfo: TestInfo
  194 | }) {
  195 |   if (opts.response.status() < 400) return
  196 | 
  197 |   const body = await captureResponseBody(opts.response)
  198 |   const root = diagnosticsRoot(opts.testInfo)
  199 |   const failedResponseDir = path.join(root, 'failed-response-bodies')
  200 |   await fs.mkdir(failedResponseDir, { recursive: true })
  201 |   const bodyFileName = `${String(opts.diagnostics.failedResponses.length + 1).padStart(3, '0')}-${safeName(`${opts.method}-${opts.response.status()}-${new URL(opts.url, 'http://127.0.0.1').pathname}`)}.txt`
  202 |   await fs.writeFile(path.join(failedResponseDir, bodyFileName), body.text, 'utf8')
  203 |   pushCapped(opts.diagnostics.failedResponses, {
  204 |     timestamp: now(),
  205 |     method: opts.method,
  206 |     url: opts.url,
  207 |     status: opts.response.status(),
  208 |     statusText: opts.response.statusText(),
  209 |     ok: opts.response.ok(),
  210 |     bodyFile: path.join('failed-response-bodies', bodyFileName),
  211 |     bodyTruncated: body.truncated,
  212 |   })
  213 | }
  214 | 
  215 | function requestBodyFromOptions(options: unknown) {
  216 |   if (!options || typeof options !== 'object') return null
  217 |   const maybeData = (options as { data?: unknown; form?: unknown; multipart?: unknown }).data ??
  218 |     (options as { form?: unknown }).form ??
  219 |     (options as { multipart?: unknown }).multipart
  220 |   if (maybeData === undefined) return null
  221 |   try {
  222 |     return typeof maybeData === 'string' ? maybeData : JSON.stringify(maybeData)
  223 |   } catch {
  224 |     return '[unserializable request body]'
  225 |   }
  226 | }
  227 | 
  228 | function wrapRequestContext(request: APIRequestContext, diagnostics: E2EDiagnostics, testInfo: TestInfo): APIRequestContext {
  229 |   return new Proxy(request, {
  230 |     get(target, prop, receiver) {
  231 |       const original = Reflect.get(target, prop, receiver)
  232 |       if (typeof original !== 'function') return original
  233 |       const methodName = String(prop).toUpperCase()
  234 |       if (!['DELETE', 'FETCH', 'GET', 'HEAD', 'PATCH', 'POST', 'PUT'].includes(methodName)) {
  235 |         return original.bind(target)
  236 |       }
  237 |       return async (...args: unknown[]) => {
  238 |         const url = String(args[0] ?? '')
  239 |         const requestMethod = methodName === 'FETCH'
  240 |           ? String((args[1] as { method?: string } | undefined)?.method ?? 'GET').toUpperCase()
  241 |           : methodName
  242 |         const requestBody = requestBodyFromOptions(args[1])
> 243 |         const response = await original.apply(target, args) as APIResponse
      |                                         ^ TypeError: apiRequestContext.apply: Invalid URL
  244 |         await recordAPIResponse({ method: requestMethod, url, requestBody, response, diagnostics, testInfo })
  245 |         return response
  246 |       }
  247 |     },
  248 |   }) as APIRequestContext
  249 | }
  250 | 
  251 | export const test = base.extend<{ diagnostics: E2EDiagnostics }>({
  252 |   diagnostics: async ({}, use, testInfo) => {
  253 |     const diagnostics: E2EDiagnostics = {
  254 |       console: [],
  255 |       pageErrors: [],
  256 |       network: [],
  257 |       failedResponses: [],
  258 |       pendingWrites: [],
  259 |     }
  260 |     await use(diagnostics)
  261 |     await persistDiagnostics(testInfo, diagnostics)
  262 |   },
  263 |   page: async ({ page, diagnostics }, use, testInfo) => {
  264 |     attachPageDiagnostics(page, diagnostics, testInfo)
  265 |     await use(page)
  266 |   },
  267 |   request: async ({ request, diagnostics }, use, testInfo) => {
  268 |     await use(wrapRequestContext(request, diagnostics, testInfo))
  269 |   },
  270 | })
  271 | 
  272 | export { expect }
  273 | export type { APIRequestContext, Page }
  274 | 
```