import { act, renderHook, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { useJobChat } from './useJobChat'

const JOB_ID = 'job-1'

interface PostedMessageBody {
  messages?: Array<{ content?: string }>
  sessionId?: string
}

interface MockApi {
  job: Record<string, unknown>
  graphRun?: Record<string, unknown>
  failEvents: boolean
  eventsFetchCount: number
  postMessageBodies: PostedMessageBody[]
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
      api.job = {
        ...api.job,
        status: 'running',
        lastRunOutcome: '',
      }
      return jsonResponse({ code: 0, status: 'started' })
    }
    if (url.includes(`/job/${JOB_ID}/stop`)) return jsonResponse({ code: 0 })
    if (url.includes(`/job/${JOB_ID}/graph-run`)) return jsonResponse(api.graphRun ?? {})
    if (url.endsWith(`/api/v1/job/${JOB_ID}`)) return jsonResponse(api.job)
    if (url.includes('/sessions/')) return jsonResponse({ messages: [] })
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

describe('useJobChat queued-message drain', () => {
  it('drains the whole queue in order even when a queued slash command takes the fast path', async () => {
    const api: MockApi = { job: runningInteractiveJob(), failEvents: false, eventsFetchCount: 0, postMessageBodies: [] }
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.eventsReady).toBe(true))
    await waitFor(() => expect(result.current.isLoading).toBe(true))

    // While the run is in flight, queue a known slash command and a normal message.
    act(() => {
      result.current.queueMessage({ content: '/help' })
      result.current.queueMessage({ content: '总结一下' })
    })
    expect(result.current.queuedMessages).toHaveLength(2)

    // The run ends → the queue drains. The slash command takes sendMessage's
    // fast path, which never flips isLoading — the dispatch lock must still be
    // released so the next entry is sent.
    api.job = interactiveJob()
    await act(async () => { await result.current.stopGeneration() })

    await waitFor(() => expect(messageContents(api)).toContain('总结一下'))
    const contents = messageContents(api)
    expect(contents.indexOf('/help')).toBeGreaterThanOrEqual(0)
    expect(contents.indexOf('/help')).toBeLessThan(contents.indexOf('总结一下'))
    await waitFor(() => expect(result.current.queuedMessages).toHaveLength(0))
  })
})

describe('useJobChat initial SSE connect failure', () => {
  it('retries with the 410-aligned backoff budget and lets a later send force a fresh subscription', async () => {
    const api: MockApi = { job: interactiveJob(), failEvents: true, eventsFetchCount: 0, postMessageBodies: [] }
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
      postMessageBodies: [],
    }
    installFetchMock(api)
    const { result } = renderHook(() => useJobChat({ existingJobId: JOB_ID }))

    await waitFor(() => expect(result.current.isGraph).toBe(true))
    await waitFor(() => expect(result.current.isLoadingHistory).toBe(false))

    act(() => { result.current.queueMessage({ content: 'graph queued msg' }) })
    expect(result.current.queuedMessages).toHaveLength(1)

    // Give the drain effect every chance to fire — it must not: the queue
    // drain sends with a null sessionId, which the graph backend contract
    // deliberately does not support.
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 200)) })
    expect(api.postMessageBodies).toHaveLength(0)
    expect(result.current.queuedMessages).toHaveLength(1)
  })
})
