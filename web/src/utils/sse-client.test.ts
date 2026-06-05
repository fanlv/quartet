import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SSEClient } from './sse-client'

// These tests cover the SSE fault links that used to be exercised end-to-end
// through the deleted /api/v1/e2e/* control API (disconnect-events, auth
// rejection, expire-resume → 410). They now live at the component layer: the
// SSEClient owns the 401/403 stop-reconnect + visible-error contract and the
// 410 → onResumePointGone(body) contract, so we drive it directly with a
// mocked fetch instead of forcing the backend into a fault mode.

const SSE_URL = '/api/v1/job/job-1/events'

function jsonResponse(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function textResponse(body: string, status: number): Response {
  return new Response(body, { status })
}

describe('SSEClient fault handling', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('auth rejection (401/403)', () => {
    it.each([401, 403])('stops reconnecting and surfaces a visible error on HTTP %i', async (status) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse({ error: 'token missing or invalid' }, status))

      const onError = vi.fn()
      const onDisconnect = vi.fn()
      const client = new SSEClient()

      await client.connectUntilReady({ url: SSE_URL, onEvent: vi.fn(), onError, onDisconnect })

      // The user-visible error preserves the HTTP status + server body.
      expect(onError).toHaveBeenCalledTimes(1)
      const message = (onError.mock.calls[0][0] as Error).message
      expect(message).toContain('SSE auth rejected')
      expect(message).toContain(`HTTP ${status}`)
      expect(message).toContain('token missing or invalid')
      expect(onDisconnect).toHaveBeenCalledTimes(1)

      // No reconnect is scheduled after an auth rejection: advancing timers
      // must not trigger another fetch.
      const fetchCallsAfterReject = vi.mocked(fetch).mock.calls.length
      await vi.advanceTimersByTimeAsync(60_000)
      expect(vi.mocked(fetch).mock.calls.length).toBe(fetchCallsAfterReject)
    })

    it('only notifies once even though authRejected guards repeated handling', async () => {
      vi.mocked(fetch).mockResolvedValue(textResponse('forbidden', 403))

      const onError = vi.fn()
      const onDisconnect = vi.fn()
      const client = new SSEClient()

      await client.connectUntilReady({ url: SSE_URL, onEvent: vi.fn(), onError, onDisconnect })
      await vi.advanceTimersByTimeAsync(60_000)

      expect(onError).toHaveBeenCalledTimes(1)
      expect(onDisconnect).toHaveBeenCalledTimes(1)
      // Raw (non-JSON) body is preserved verbatim after the status prefix.
      expect((onError.mock.calls[0][0] as Error).message).toContain('forbidden')
    })
  })

  describe('resume point gone (410)', () => {
    it('fires onResumePointGone once with the server body and stops reconnecting', async () => {
      vi.mocked(fetch).mockResolvedValue(
        jsonResponse({ error: 'event buffer no longer contains seq=5; reload snapshot' }, 410),
      )

      const onResumePointGone = vi.fn()
      const onError = vi.fn()
      const client = new SSEClient()

      await client.connectUntilReady({
        url: SSE_URL,
        onEvent: vi.fn(),
        onError,
        onResumePointGone,
        initialLastEventId: '5',
      })

      expect(onResumePointGone).toHaveBeenCalledTimes(1)
      expect(onResumePointGone.mock.calls[0][0]).toContain('HTTP 410')
      expect(onResumePointGone.mock.calls[0][0]).toContain('reload snapshot')
      // 410 is a recovery signal, not a stream error — onError must not fire.
      expect(onError).not.toHaveBeenCalled()

      // The client must not silently reconnect on the same stale resume point.
      const fetchCallsAfterGone = vi.mocked(fetch).mock.calls.length
      await vi.advanceTimersByTimeAsync(60_000)
      expect(vi.mocked(fetch).mock.calls.length).toBe(fetchCallsAfterGone)
    })

    it('forwards a raw text 410 body so the caller can surface it verbatim', async () => {
      vi.mocked(fetch).mockResolvedValue(textResponse('resume window expired', 410))

      const onResumePointGone = vi.fn()
      const client = new SSEClient()

      await client.connectUntilReady({ url: SSE_URL, onEvent: vi.fn(), onResumePointGone })

      expect(onResumePointGone).toHaveBeenCalledTimes(1)
      expect(onResumePointGone.mock.calls[0][0]).toContain('HTTP 410')
      expect(onResumePointGone.mock.calls[0][0]).toContain('resume window expired')
    })
  })

  describe('error body parsing', () => {
    it.each([
      [{ msg: 'msg-field wins' }, 'msg-field wins'],
      [{ error: 'error-field' }, 'error-field'],
      [{ message: 'message-field' }, 'message-field'],
    ])('extracts the human-readable detail from JSON envelope %j', async (body, expected) => {
      vi.mocked(fetch).mockResolvedValue(jsonResponse(body, 401))

      const onError = vi.fn()
      const client = new SSEClient()
      await client.connectUntilReady({ url: SSE_URL, onEvent: vi.fn(), onError })

      expect((onError.mock.calls[0][0] as Error).message).toContain(expected)
    })
  })
})
