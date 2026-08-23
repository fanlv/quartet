import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { AuthGate } from './AuthGate'

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' }, ...init })
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

const principal = {
  user: { id: 'user-1', username: 'admin', displayName: 'Admin', roleIds: ['admin'], status: 'active', mustChangePassword: false, version: 1, createdAt: '', updatedAt: '' },
  permissions: ['users.manage'],
  csrfToken: 'csrf-test',
}

describe('AuthGate', () => {
  it('renders children for a valid cookie session', async () => {
    mockFetchByRoute({ '/api/v1/health': jsonResponse({ authState: 'ready' }), '/api/v1/auth/me': jsonResponse(principal) })
    render(<AuthGate><div>App Ready</div></AuthGate>)
    expect(await screen.findByText('App Ready')).toBeTruthy()
  })

  it('shows administrator initialization when there are no users', async () => {
    mockFetchByRoute({ '/api/v1/health': jsonResponse({ authState: 'uninitialized' }) })
    render(<AuthGate><div>App Ready</div></AuthGate>)
    expect(await screen.findByText('Initialize administrator')).toBeTruthy()
    expect(screen.getByPlaceholderText('Initialization code from backend logs')).toBeTruthy()
  })

  it('logs in with username and password', async () => {
    mockFetchByRoute({ '/api/v1/health': jsonResponse({ authState: 'ready' }), '/api/v1/auth/me': jsonResponse({ error: 'authentication required' }, { status: 401 }), '/api/v1/auth/login': jsonResponse(principal) })
    render(<AuthGate><div>App Ready</div></AuthGate>)
    await userEvent.type(await screen.findByPlaceholderText('Username'), 'admin')
    await userEvent.type(screen.getByPlaceholderText('Password'), 'password1')
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect(await screen.findByText('App Ready')).toBeTruthy()
  })

  it('shows recovery details and retries', async () => {
    let calls = 0
    mockFetchByRoute({ '/api/v1/health': () => { calls += 1; return calls === 1 ? jsonResponse({ authState: 'recovery', authError: 'broken role file' }) : jsonResponse({ authState: 'ready' }) }, '/api/v1/auth/me': jsonResponse(principal) })
    render(<AuthGate><div>App Ready</div></AuthGate>)
    expect(await screen.findByText('broken role file')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('App Ready')).toBeTruthy()
  })
})
