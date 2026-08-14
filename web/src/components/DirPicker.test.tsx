import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { DirPicker } from './DirPicker'

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

function listDirResponse(current: string, dirs: string[]) {
  return {
    json: async () => ({ code: 0, current, parent: '/', dirs, files: [] }),
  } as Response
}

describe('DirPicker navigation races', () => {
  it('ignores a slow response for an earlier path instead of overwriting newer navigation', async () => {
    const pending = new Map<string, Deferred<Response>>()
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://localhost')
      if (url.pathname === '/api/v1/recent-dirs') {
        return Promise.resolve({ json: async () => ({ code: 0, dirs: [] }) } as Response)
      }
      if (url.pathname === '/api/v1/list-dir') {
        const path = url.searchParams.get('path') ?? ''
        const d = deferred<Response>()
        pending.set(path, d)
        return d.promise
      }
      return Promise.reject(new Error(`unexpected fetch: ${url}`))
    }))

    const { container } = render(
      <DirPicker initialPath="/a" onConfirm={vi.fn()} onCancel={vi.fn()} />,
    )
    const input = container.querySelector('.dirpicker-path-input') as HTMLInputElement

    // Initial fetch for /a fires on mount and stays in flight (slow).
    await waitFor(() => expect(pending.has('/a')).toBe(true))

    // User navigates to /b before /a answers.
    fireEvent.change(input, { target: { value: '/b' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(pending.has('/b')).toBe(true))

    // /b resolves first and is rendered.
    pending.get('/b')!.resolve(listDirResponse('/b', ['b-child']))
    expect(await screen.findByText('b-child')).toBeTruthy()
    expect(input.value).toBe('/b')

    // The stale /a response arrives late; it must not clobber the /b view.
    pending.get('/a')!.resolve(listDirResponse('/a', ['a-child']))
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByText('a-child')).toBeNull()
    expect(screen.getByText('b-child')).toBeTruthy()
    expect(input.value).toBe('/b')
  })
})
