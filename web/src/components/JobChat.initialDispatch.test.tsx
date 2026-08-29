import { render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { JobChat } from './JobChat'

function jsonResponse(body: unknown, init: { ok?: boolean; status?: number } = {}) {
  return {
    ok: init.ok ?? true,
    status: init.status ?? 200,
    headers: { get: () => null },
    json: async () => body,
  } as unknown as Response
}

function sseResponse() {
  return new Response(new ReadableStream<Uint8Array>({ start() {} }), {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  })
}

describe('JobChat initial message dispatch failure', () => {
  it('surfaces the agent-list failure and stops the pending spinner instead of disabling the page forever', async () => {
    let messagePosted = false
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const raw = input instanceof Request ? input.url : String(input)
      const url = new URL(raw, 'http://localhost')
      const path = url.pathname
      if (path === '/api/v1/agent/list') {
        throw new Error('agent list boom')
      }
      if (path === '/api/v1/job/job-1') {
        if (url.search || raw.includes('/message')) {
          // no-op
        }
        return jsonResponse({ id: 'job-1', title: 'Test job', status: 'completed', sessionIds: [] })
      }
      if (path === '/api/v1/job/job-1/message') {
        messagePosted = true
        return jsonResponse({ code: 0 })
      }
      if (path === '/api/v1/job/job-1/events') {
        return sseResponse()
      }
      if (path === '/api/v1/job/job-1/viewer-state') {
        return jsonResponse({ code: 0, applied: true })
      }
      if (path === '/api/v1/job/job-1/message-queue') {
        return jsonResponse({ code: 0, queue: { jobId: 'job-1', version: 0, paused: false, willContinue: false, items: [] } })
      }
      if (path === '/api/v1/workspace/list') {
        return jsonResponse({ workspaces: [] })
      }
      if (path === '/api/v1/config/settings/get') {
        return jsonResponse({ code: 0, settings: {} })
      }
      if (path === '/api/v1/job/list') {
        return jsonResponse({ jobs: [], hasMore: false })
      }
      throw new Error(`unexpected fetch: ${url}`)
    }))

    render(<JobChat existingJobId="job-1" initialMessage="hello from home" />)

    // The cause must be surfaced (project convention: show errors in full).
    const banner = await screen.findByTestId('agent-list-error', undefined, { timeout: 3000 })
    expect(banner.textContent).toContain('agent list boom')

    // The "dispatching your first message" pending state must end: the message
    // list stops showing the perpetual loading state…
    await waitFor(() => {
      expect(screen.getByTestId('message-list')).toHaveAttribute('data-loading', 'false')
    })
    expect(screen.getByTestId('chat-input')).not.toBeDisabled()

    // …and the initial message must NOT have been dispatched without an agent.
    expect(messagePosted).toBe(false)
  })

  it('surfaces an empty agent list and re-enables the composer', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const raw = input instanceof Request ? input.url : String(input)
      const url = new URL(raw, 'http://localhost')
      const path = url.pathname
      if (path === '/api/v1/agent/list') {
        return jsonResponse({ code: 0, agent_list: [], job_enable: true })
      }
      if (path === '/api/v1/job/job-1') {
        return jsonResponse({ id: 'job-1', title: 'Test job', status: 'completed', sessionIds: [] })
      }
      if (path === '/api/v1/job/job-1/events') {
        return sseResponse()
      }
      if (path === '/api/v1/job/job-1/viewer-state') {
        return jsonResponse({ code: 0, applied: true })
      }
      if (path === '/api/v1/job/job-1/message-queue') {
        return jsonResponse({ code: 0, queue: { jobId: 'job-1', version: 0, paused: false, willContinue: false, items: [] } })
      }
      if (path === '/api/v1/workspace/list') {
        return jsonResponse({ workspaces: [] })
      }
      if (path === '/api/v1/config/settings/get') {
        return jsonResponse({ code: 0, settings: {} })
      }
      if (path === '/api/v1/job/list') {
        return jsonResponse({ jobs: [], hasMore: false })
      }
      throw new Error(`unexpected fetch: ${url}`)
    }))

    render(<JobChat existingJobId="job-1" initialMessage="hello from home" />)

    expect(await screen.findByTestId('agent-list-error')).toHaveTextContent(
      'No agents are available, so the first message was not sent.',
    )
    expect(screen.getByTestId('chat-input')).not.toBeDisabled()
  })

  it('releases the initial dispatch when the event stream fails to connect', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const raw = input instanceof Request ? input.url : String(input)
      const url = new URL(raw, 'http://localhost')
      const path = url.pathname
      if (path === '/api/v1/agent/list') {
        return jsonResponse({
          code: 0,
          agent_list: [{
            type: 'codex',
            model_id: 'default',
            display_name: 'Codex',
            icon_url: '',
          }],
          job_enable: true,
        })
      }
      if (path === '/api/v1/job/job-1') {
        return jsonResponse({ id: 'job-1', title: 'Test job', status: 'completed', sessionIds: [] })
      }
      if (path === '/api/v1/job/job-1/events') {
        throw new Error('event stream boom')
      }
      if (path === '/api/v1/job/job-1/viewer-state') {
        return jsonResponse({ code: 0, applied: true })
      }
      if (path === '/api/v1/job/job-1/message-queue') {
        return jsonResponse({ code: 0, queue: { jobId: 'job-1', version: 0, paused: false, willContinue: false, items: [] } })
      }
      if (path === '/api/v1/workspace/list') {
        return jsonResponse({ workspaces: [] })
      }
      if (path === '/api/v1/config/settings/get') {
        return jsonResponse({ code: 0, settings: {} })
      }
      if (path === '/api/v1/job/list') {
        return jsonResponse({ jobs: [], hasMore: false })
      }
      throw new Error(`unexpected fetch: ${url}`)
    }))

    render(<JobChat existingJobId="job-1" initialMessage="hello from home" />)

    expect(await screen.findByTestId('job-error-banner')).toHaveTextContent('event stream boom')
    await waitFor(() => {
      expect(screen.getByTestId('message-list')).toHaveAttribute('data-loading', 'false')
      expect(screen.getByTestId('chat-input')).not.toBeDisabled()
    })
  })
})
