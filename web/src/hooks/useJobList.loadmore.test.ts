import { renderHook, waitFor, act } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { useJobList, type JobSummary } from './useJobList'

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason?: unknown) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function makeJob(id: string, updatedAt: number): JobSummary {
  return { id, title: id, status: 'completed', createdAt: updatedAt, updatedAt, sessionCount: 1 }
}

function listResponse(body: unknown) {
  return {
    ok: true,
    status: 200,
    headers: { get: () => null },
    json: async () => body,
  } as unknown as Response
}

// Regression probe for the suspected "poll invalidates in-flight loadMore"
// race: while an append request is in flight, poll ticks keep firing. The
// append response must still land (page appended, cursor advanced).
describe('useJobList loadMore vs polling', () => {
  it('deduplicates repeated loadMore calls while the same cursor is in flight', async () => {
    const page1 = { jobs: [makeJob('j1', 300)], nextCursor: 'c2', hasMore: true }
    const page2 = { jobs: [makeJob('j2', 200)], nextCursor: '', hasMore: false }
    const appendCall = deferred<Response>()
    let appendFetchCount = 0

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const raw = input instanceof Request ? input.url : String(input)
      const url = new URL(raw, 'http://localhost')
      if (url.pathname !== '/api/v1/job/list') {
        return Promise.reject(new Error(`unexpected fetch: ${url}`))
      }
      if (url.searchParams.get('cursor') === 'c2') {
        appendFetchCount += 1
        return appendCall.promise
      }
      return Promise.resolve(listResponse(page1))
    }))

    const { result } = renderHook(() =>
      useJobList({ pageSize: 1, pollIntervalMs: 0, refetchOnFocus: false }),
    )
    await waitFor(() => expect(result.current.hasMore).toBe(true))

    let first!: Promise<void>
    let second!: Promise<void>
    act(() => {
      first = result.current.loadMore()
      second = result.current.loadMore()
    })
    await waitFor(() => expect(appendFetchCount).toBeGreaterThan(0))

    // VirtualList can re-arm after leaving the bottom threshold. If that
    // happens before this request settles, the hook must not fetch the same
    // cursor again.
    expect(appendFetchCount).toBe(1)

    await act(async () => {
      appendCall.resolve(listResponse(page2))
      await Promise.all([first, second])
    })
    expect(result.current.jobs.map((job) => job.id)).toEqual(['j1', 'j2'])
  })

  it('appends a load-more page even while background poll ticks fire', async () => {
    const page1 = { jobs: [makeJob('j1', 300), makeJob('j2', 200)], nextCursor: 'c2', hasMore: true }
    const page2 = { jobs: [makeJob('j3', 100), makeJob('j4', 50)], nextCursor: '', hasMore: false }
    let appendCall: Deferred<Response> | null = null
    let pollCount = 0

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const raw = input instanceof Request ? input.url : String(input)
      const url = new URL(raw, 'http://localhost')
      if (url.pathname !== '/api/v1/job/list') {
        return Promise.reject(new Error(`unexpected fetch: ${url}`))
      }
      const cursor = url.searchParams.get('cursor')
      if (cursor === 'c2') {
        appendCall = deferred<Response>()
        return appendCall.promise
      }
      pollCount += 1
      return Promise.resolve(listResponse(page1))
    }))

    const { result } = renderHook(() =>
      useJobList({ pageSize: 2, pollIntervalMs: 30, activePollIntervalMs: 0, refetchOnFocus: false }),
    )

    // Initial page loads.
    await waitFor(() => expect(result.current.jobs.map((j) => j.id)).toEqual(['j1', 'j2']))
    expect(result.current.hasMore).toBe(true)

    // Start loadMore; keep the append response in flight while poll ticks fire.
    let loadMorePromise!: Promise<void>
    act(() => {
      loadMorePromise = result.current.loadMore()
    })
    await waitFor(() => expect(appendCall).not.toBeNull())

    // Let several poll ticks fire during the in-flight append window. (Each
    // tick is skipped by the in-flight guard before hitting fetch, so
    // pollCount stays frozen here — that guard is precisely why the claimed
    // "poll invalidates the append" race cannot happen.)
    const pollsBefore = pollCount
    await act(async () => {
      await new Promise((r) => setTimeout(r, 120))
    })

    // Resolve the append — it must land despite the concurrent polling.
    await act(async () => {
      appendCall!.resolve(listResponse(page2))
      await loadMorePromise
    })

    expect(result.current.jobs.map((j) => j.id)).toEqual(['j1', 'j2', 'j3', 'j4'])
    expect(result.current.hasMore).toBe(false)

    // After the append settles, the poller is still alive (fetch count grows
    // again) and the merged list is not clobbered by subsequent first-page
    // polls (j3/j4 are past the first page and must survive).
    await act(async () => {
      await new Promise((r) => setTimeout(r, 100))
    })
    expect(pollCount).toBeGreaterThan(pollsBefore)
    expect(result.current.jobs.map((j) => j.id)).toEqual(['j1', 'j2', 'j3', 'j4'])
  })

  it('keeps the appended page when a slower in-flight poll resolves late', async () => {
    const page1 = { jobs: [makeJob('j1', 300), makeJob('j2', 200)], nextCursor: 'c2', hasMore: true }
    const page2 = { jobs: [makeJob('j3', 100)], nextCursor: '', hasMore: false }
    let slowPoll: Deferred<Response> | null = null
    let firstPageCalls = 0

    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const raw = input instanceof Request ? input.url : String(input)
      const url = new URL(raw, 'http://localhost')
      if (url.pathname !== '/api/v1/job/list') {
        return Promise.reject(new Error(`unexpected fetch: ${url}`))
      }
      const cursor = url.searchParams.get('cursor')
      if (cursor === 'c2') {
        return Promise.resolve(listResponse(page2))
      }
      firstPageCalls += 1
      if (firstPageCalls === 2) {
        // The first background poll hangs so it resolves AFTER the append.
        slowPoll = deferred<Response>()
        return slowPoll.promise
      }
      return Promise.resolve(listResponse(page1))
    }))

    const { result } = renderHook(() =>
      useJobList({ pageSize: 2, pollIntervalMs: 30, activePollIntervalMs: 0, refetchOnFocus: false }),
    )

    await waitFor(() => expect(result.current.jobs.map((j) => j.id)).toEqual(['j1', 'j2']))

    // Wait for the slow poll to be in flight, then start loadMore.
    await waitFor(() => expect(slowPoll).not.toBeNull())
    await act(async () => {
      await result.current.loadMore()
    })
    expect(result.current.jobs.map((j) => j.id)).toEqual(['j1', 'j2', 'j3'])

    // The stale poll response arrives late: it is older than the append (its
    // request was sequenced before it), so it must not drop j3 — the poll
    // merge keeps items past the first page anyway.
    await act(async () => {
      slowPoll!.resolve(listResponse(page1))
      await Promise.resolve()
    })
    expect(result.current.jobs.map((j) => j.id)).toEqual(['j1', 'j2', 'j3'])
  })
})
