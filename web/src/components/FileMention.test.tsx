import { act, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { FileMention } from './FileMention'

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

function searchResponse(files: Array<{ path: string; name: string; dir: string }>) {
  return { json: async () => ({ code: 0, files }) } as Response
}

function renderMention(keyword: string) {
  const props = {
    workdir: '/ws',
    onSelect: vi.fn(),
    onClose: vi.fn(),
    activeIndex: 0,
    onActiveIndexChange: vi.fn(),
  }
  const utils = render(<FileMention {...props} keyword={keyword} />)
  return {
    ...utils,
    rerenderWith: (kw: string) => utils.rerender(<FileMention {...props} keyword={kw} />),
  }
}

describe('FileMention search request ordering', () => {
  it('invalidates the old response as soon as the keyword changes, before the debounce fires', async () => {
    const pending = new Map<string, Deferred<Response>>()
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      const kw = url.searchParams.get('keyword') ?? ''
      const d = deferred<Response>()
      pending.set(kw, d)
      return d.promise
    }))

    const { rerenderWith } = renderMention('old')
    await waitFor(() => expect(pending.has('old')).toBe(true))

    // Changing the keyword starts a new logical search immediately, even
    // though its network request is still waiting for the 200ms debounce.
    rerenderWith('new')
    await act(async () => {
      pending.get('old')!.resolve(searchResponse([
        { path: '/ws/old.txt', name: 'old.txt', dir: '/ws' },
      ]))
      await Promise.resolve()
    })

    expect(screen.queryByText('old.txt')).toBeNull()

    await waitFor(() => expect(pending.has('new')).toBe(true))
    await act(async () => {
      pending.get('new')!.resolve(searchResponse([]))
    })
  })

  it('ignores a slow response for an older keyword instead of overwriting newer results', async () => {
    const pending = new Map<string, Deferred<Response>>()
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      const kw = url.searchParams.get('keyword') ?? ''
      const d = deferred<Response>()
      pending.set(kw, d)
      return d.promise
    }))

    const { rerenderWith } = renderMention('a')
    // Debounce is 200ms — wait for the first request to actually fire.
    await waitFor(() => expect(pending.has('a')).toBe(true))

    rerenderWith('ab')
    await waitFor(() => expect(pending.has('ab')).toBe(true))

    // The newer keyword's response lands first and is rendered.
    pending.get('ab')!.resolve(searchResponse([{ path: '/ws/ab.txt', name: 'ab.txt', dir: '/ws' }]))
    expect(await screen.findByText('ab.txt')).toBeTruthy()

    // The stale response for the older keyword arrives late; it must not
    // clobber the list for the current keyword.
    pending.get('a')!.resolve(searchResponse([{ path: '/ws/a.txt', name: 'a.txt', dir: '/ws' }]))
    await waitFor(() => expect(pending.get('a')!.promise).toBeTruthy()) // noop flush guard
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByText('a.txt')).toBeNull()
    expect(screen.getByText('ab.txt')).toBeTruthy()
  })

  it('keeps showing the loading state until the latest keyword request settles', async () => {
    const pending = new Map<string, Deferred<Response>>()
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      const kw = url.searchParams.get('keyword') ?? ''
      const d = deferred<Response>()
      pending.set(kw, d)
      return d.promise
    }))

    const { rerenderWith } = renderMention('x')
    await waitFor(() => expect(pending.has('x')).toBe(true))
    rerenderWith('xy')
    await waitFor(() => expect(pending.has('xy')).toBe(true))

    // The stale request settles first with an empty result. While the latest
    // request is still in flight the popup must not flash "No files found".
    pending.get('x')!.resolve(searchResponse([]))
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByText('No files found')).toBeNull()

    pending.get('xy')!.resolve(searchResponse([]))
    expect(await screen.findByText('No files found')).toBeTruthy()
  })
})
