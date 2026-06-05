import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { AuthGate } from './AuthGate'

const AUTH_TOKEN_STORAGE_KEY = 'quartet.x_auth_token'

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

function textResponse(body: string, init: ResponseInit = {}): Response {
  return new Response(body, init)
}

function mockFetchByRoute(routes: Record<string, Response | (() => Response | Promise<Response>)>) {
  vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL) => {
    const url = input instanceof Request ? input.url : String(input)
    const path = url.startsWith('http') ? new URL(url).pathname : url.split('?')[0]
    const route = routes[path]
    if (!route) throw new Error(`Unexpected fetch in AuthGate test: ${url}`)
    return typeof route === 'function' ? route() : route
  })
}

describe('AuthGate', () => {
  it('renders children when backend does not require auth', async () => {
    mockFetchByRoute({
      '/api/v1/health': jsonResponse({ authRequired: false }, { status: 200 }),
    })

    render(<AuthGate><div>App Ready</div></AuthGate>)

    expect(await screen.findByText('App Ready')).toBeTruthy()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(fetch).toHaveBeenCalledWith('/api/v1/health', { cache: 'no-store' })
  })

  it('shows token form when auth is required and no token exists', async () => {
    localStorage.removeItem(AUTH_TOKEN_STORAGE_KEY)
    mockFetchByRoute({
      '/api/v1/health': jsonResponse({ authRequired: true }, { status: 200 }),
    })

    render(<AuthGate><div>App Ready</div></AuthGate>)

    expect(await screen.findByText('This instance requires authentication. Please enter your access token to continue.')).toBeTruthy()
    expect(screen.getByPlaceholderText('Token')).toBeTruthy()
    expect(screen.queryByText('App Ready')).toBeNull()
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('shows invalid-token branch when saved token is rejected', async () => {
    localStorage.setItem(AUTH_TOKEN_STORAGE_KEY, 'bad-token')
    mockFetchByRoute({
      '/api/v1/health': jsonResponse({ authRequired: true }, { status: 200 }),
      '/api/v1/agent/list': textResponse('forbidden', { status: 403 }),
    })

    render(<AuthGate><div>App Ready</div></AuthGate>)

    expect(await screen.findByText('The saved token was rejected by the server. Please re-enter a valid token.')).toBeTruthy()
    expect(screen.getByPlaceholderText('Token')).toBeTruthy()
    expect(screen.queryByText('App Ready')).toBeNull()
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('shows probe-failed branch when health probe fails and retries successfully', async () => {
    let healthCalls = 0
    mockFetchByRoute({
      '/api/v1/health': () => {
        healthCalls += 1
        return healthCalls === 1
          ? textResponse('backend unavailable', { status: 503 })
          : jsonResponse({ authRequired: false }, { status: 200 })
      },
    })

    render(<AuthGate><div>App Ready</div></AuthGate>)

    expect(await screen.findByText('Could not reach the server at /api/v1/health. Check that the backend is running, then retry.')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))

    expect(await screen.findByText('App Ready')).toBeTruthy()
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('stores submitted token and renders children after protected probe succeeds', async () => {
    localStorage.removeItem(AUTH_TOKEN_STORAGE_KEY)
    mockFetchByRoute({
      '/api/v1/health': () => jsonResponse({ authRequired: true }, { status: 200 }),
      '/api/v1/agent/list': () => jsonResponse({ agents: [] }, { status: 200 }),
    })

    render(<AuthGate><div>App Ready</div></AuthGate>)

    await screen.findByPlaceholderText('Token')
    await userEvent.type(screen.getByPlaceholderText('Token'), 'valid-token')
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))

    expect(await screen.findByText('App Ready')).toBeTruthy()
    await waitFor(() => expect(localStorage.getItem(AUTH_TOKEN_STORAGE_KEY)).toBe('valid-token'))
    expect(fetch).toHaveBeenCalledWith('/api/v1/agent/list', { cache: 'no-store' })
  })
})
