import { act, renderHook, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { useJobChat } from './useJobChat'

const JOB_ID = 'job-1'

interface PostedMessageBody {
  messages?: Array<{ content?: string }>
  sessionId?: string
  clientMessageId?: string
}

interface MockApi {
  job: Record<string, unknown>
  graphRun?: Record<string, unknown>
  failEvents: boolean
  eventsFetchCount: number
  jobFetchCount: number
  historyFetchCount: number
  postMessageBodies: PostedMessageBody[]
  messageResponse?: Record<string, unknown>
  // Newest history page served for every /sessions/:id/messages request.
  history?: Record<string, unknown>
  // Message queue snapshot served for /job/:id/message-queue.
  queue?: Record<string, unknown>
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

// An SSE response whose stream opens successfully but never emits or closes,
// modelling a connected-but-quiet events stream.
function sseStreamResponse(): Response {
  const stream = new ReadableStream<Uint8Array>({ start() { /* stays open */ } })
  return new Response(stream, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

function installFetchMock(api: MockApi) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const method = init?.method ?? 'GET'
    if (url.includes(`/job/${JOB_ID}/events`)) {
      api.eventsFetchCount += 1
      if (api.failEvents) throw new Error('Failed to fetch')
      return sseStreamResponse()
    }
    if (url.includes(`/job/${JOB_ID}/viewer-state`) && method === 'POST') {
      return jsonResponse({ code: 0, applied: true })
    }
    if (url.includes(`/job/${JOB_ID}/message`) && method === 'POST') {
      const body = JSON.parse(String(init?.body ?? '{}')) as PostedMessageBody
      api.postMessageBodies.push(body)
      const content = body.messages?.[0]?.content ?? ''
      if (content.startsWith('/')) {
        return jsonResponse({
          code: 0,
          status: 'command_dispatched',
          event: { type: 'command_system_message', command: content, text: `${content} result`, present: 'inline' },
        })
      }
      if (api.messageResponse) return jsonResponse(api.messageResponse)
      api.job = {
        ...api.job,
        status: 'running',
        lastRunOutcome: '',
      }
      return jsonResponse({ code: 0, status: 'started' })
    }
    if (url.includes(`/job/${JOB_ID}/message-queue`)) {
      if (api.queue) return jsonResponse({ code: 0, queue: api.queue })
      return jsonResponse({ code: 0, queue: { jobId: JOB_ID, version: 0, paused: false, willContinue: api.job.status === 'running', items: [] } })
    }
    if (url.includes(`/job/${JOB_ID}/stop`)) return jsonResponse({ code: 0 })
    if (url.includes(`/job/${JOB_ID}/graph-run`)) return jsonResponse(api.graphRun ?? {})
    if (url.endsWith(`/api/v1/job/${JOB_ID}`)) {
      api.jobFetchCount += 1
      return jsonResponse(api.job)
    }
    if (url.includes('/sessions/')) {
      api.historyFetchCount += 1
      return jsonResponse(api.history ?? { messages: [] })
    }
    throw new Error(`Unexpected fetch in test: ${method} ${url}`)
  }))
}

function interactiveJob(): Record<string, unknown> {
  return {
    id: JOB_ID,
    title: 'Test Job',
    status: 'completed',
    mode: 'interactive',
    sessionIds: [],
    lastEventSeq: 0,
    createdAt: 1,
    updatedAt: 1,
  }
}

function runningInteractiveJob(): Record<string, unknown> {
  return {
    ...interactiveJob(),
    status: 'running',
    lastRunOutcome: '',
  }
}

function messageContents(api: MockApi): string[] {
  return api.postMessageBodies.map((b) => b.messages?.[0]?.content ?? '')
}

describe('useJobChat server message queue submission', () => {
  it('submits messages immediately so the server owns scheduling order', async () => {
    const api: MockApi = { job: runningInteractiveJob(), failEvents: false, eventsFetchCount: 0, jobFetchCount: 0, historyFetchCount: 0, postMessageBodies: [] }
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.eventsReady).toBe(true))
    await waitFor(() => expect(result.current.isLoading).toBe(true))

    // While the run is in flight, queue a known slash command and a normal message.
    act(() => {
      result.current.queueMessage({ content: '/help' })
      result.current.queueMessage({ content: '总结一下' })
    })
    await waitFor(() => expect(messageContents(api)).toContain('总结一下'))
    const contents = messageContents(api)
    expect(contents.indexOf('/help')).toBeGreaterThanOrEqual(0)
    expect(contents.indexOf('/help')).toBeLessThan(contents.indexOf('总结一下'))
  })
})

describe('useJobChat slash command idempotency', () => {
  it('sends a stable clientMessageId with a slash command', async () => {
    const api: MockApi = { job: runningInteractiveJob(), failEvents: false, eventsFetchCount: 0, jobFetchCount: 0, historyFetchCount: 0, postMessageBodies: [] }
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.eventsReady).toBe(true))
    await act(async () => { await result.current.sendMessage('/help') })

    expect(api.postMessageBodies).toHaveLength(1)
    expect(api.postMessageBodies[0].clientMessageId).toEqual(expect.any(String))
    expect(api.postMessageBodies[0].clientMessageId).not.toBe('')
  })
})

describe('useJobChat initial SSE connect failure', () => {
  it('retries with the 410-aligned backoff budget and lets a later send force a fresh subscription', async () => {
    const api: MockApi = { job: interactiveJob(), failEvents: true, eventsFetchCount: 0, jobFetchCount: 0, historyFetchCount: 0, postMessageBodies: [] }
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    // The initial /events fetch fails. The hook must not leave the page wedged:
    // it re-attempts the connection on the shared retry budget.
    await waitFor(() => expect(api.eventsFetchCount).toBeGreaterThanOrEqual(2), { timeout: 4000 })

    // The network comes back; a send must force a fresh subscription and go
    // through (previously the send waited out a 15s readiness timeout and failed).
    api.failEvents = false
    await act(async () => { await result.current.sendMessage('hello after outage') })

    await waitFor(() => expect(messageContents(api)).toContain('hello after outage'))
    expect(result.current.eventsReady).toBe(true)
    expect(result.current.error).toBeNull()
  }, 15_000)
})

describe('useJobChat duplicate message receipts', () => {
  it('settles a completed duplicate and reloads the job history without waiting for a new SSE event', async () => {
    const api: MockApi = {
      job: { ...runningInteractiveJob(), sessionIds: ['session-1'] },
      failEvents: false,
      eventsFetchCount: 0,
      jobFetchCount: 0,
      historyFetchCount: 0,
      postMessageBodies: [],
      messageResponse: { code: 0, status: 'duplicate', messageState: 'completed' },
    }
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.isLoadingHistory).toBe(false))
    await waitFor(() => expect(result.current.eventsReady).toBe(true))
    await waitFor(() => expect(api.jobFetchCount).toBeGreaterThanOrEqual(3))

    // Ignore initial hydration/SSE reconciliation. The open mock SSE never
    // emits a terminal event, so only the duplicate response can settle this send.
    api.jobFetchCount = 0
    api.historyFetchCount = 0
    api.job = { ...interactiveJob(), sessionIds: ['session-1'] }

    await act(async () => { await result.current.sendMessage('already completed') })

    expect(api.jobFetchCount).toBeGreaterThan(0)
    expect(api.historyFetchCount).toBeGreaterThan(0)
    expect(result.current.isLoading).toBe(false)
    expect(result.current.messages.find((message) => message.content === 'already completed')).toMatchObject({
      pending: false,
      deliveryStatus: 'sent',
    })
  })
})

describe('useJobChat graph jobs', () => {
  it('never drains the interactive queue in graph mode (graph sends require an explicit sessionId)', async () => {
    const api: MockApi = {
      job: {
        id: JOB_ID,
        title: 'Graph Job',
        status: 'completed',
        mode: 'graph',
        graphRunId: 'run-1',
        sessionIds: [],
        lastEventSeq: 0,
        createdAt: 1,
        updatedAt: 1,
      },
      graphRun: {
        run: { id: 'run-1', status: 'completed' },
        instances: [
          {
            key: { nodeId: 'n1' },
            nodeId: 'n1',
            nodeTitle: 'Agent',
            nodeType: 'prompt',
            status: 'completed',
            version: 1,
            sessionId: 'sess-g1',
            startedAt: 1,
            finishedAt: 2,
          },
        ],
      },
      failEvents: false,
      eventsFetchCount: 0,
      jobFetchCount: 0,
      historyFetchCount: 0,
      postMessageBodies: [],
    }
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.isGraph).toBe(true))
    await waitFor(() => expect(result.current.isLoadingHistory).toBe(false))

    act(() => { result.current.queueMessage({ content: 'graph queued msg' }) })

    // Give the drain effect every chance to fire — it must not: the queue
    // drain sends with a null sessionId, which the graph backend contract
    // deliberately does not support.
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 200)) })
    expect(api.postMessageBodies).toHaveLength(0)
  })
})

describe('useJobChat running message outside the newest page', () => {
  // The message queue reports the message the backend is running as `active`,
  // and the run persists it before the agent produces anything. Pagination
  // only loads the newest page, so on a long turn that message is on disk but
  // ABOVE the loaded window — "not in the list" must not be read as "not sent
  // yet", or the user's own question renders below the replies to it.
  function longTurnApi(activeCreatedAt: number): MockApi {
    return {
      job: { ...runningInteractiveJob(), sessionIds: ['session-1'] },
      failEvents: false,
      eventsFetchCount: 0,
      jobFetchCount: 0,
      historyFetchCount: 0,
      postMessageBodies: [],
      history: {
        messages: [
          { id: 'assistant-late', role: 'assistant', content: 'late answer', startedAt: 9_000 },
        ],
        page: { hasMoreBefore: true, beforeCursor: 'cursor-1' },
      },
      queue: {
        jobId: JOB_ID,
        version: 1,
        paused: false,
        willContinue: true,
        items: [],
        active: {
          id: 'initial-1',
          state: 'processing',
          createdAt: activeCreatedAt,
          messages: [{ content: '开场问题' }],
        },
      },
    }
  }

  it('pins the running message above the loaded window instead of below its replies', async () => {
    const api = longTurnApi(1_000)
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.messages.some((m) => m.id === 'initial-1')).toBe(true))
    expect(result.current.messages.map((m) => m.id)).toEqual(['initial-1', 'assistant-late'])
    expect(result.current.messages[0].roundHeadPinned).toBe(true)
  })

  it('appends a message that just started running and is newer than the loaded window', async () => {
    const api = longTurnApi(12_000)
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.messages.some((m) => m.id === 'initial-1')).toBe(true))
    expect(result.current.messages.map((m) => m.id)).toEqual(['assistant-late', 'initial-1'])
    expect(result.current.messages[1].roundHeadPinned).toBeUndefined()
  })
})
