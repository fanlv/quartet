import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'
import i18n from './src/i18n'
import { setAuthPrincipal } from './src/auth'

const DEFAULT_TEST_LANGUAGE = 'en'

class MockEventSource extends EventTarget {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 2

  readonly CONNECTING = MockEventSource.CONNECTING
  readonly OPEN = MockEventSource.OPEN
  readonly CLOSED = MockEventSource.CLOSED

  readonly url: string | URL
  readonly withCredentials: boolean
  readyState = MockEventSource.CONNECTING
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null

  constructor(url: string | URL, eventSourceInitDict?: EventSourceInit) {
    super()
    this.url = url
    this.withCredentials = Boolean(eventSourceInitDict?.withCredentials)
  }

  close(): void {
    this.readyState = MockEventSource.CLOSED
  }

  emitOpen(): void {
    this.readyState = MockEventSource.OPEN
    const event = new Event('open')
    this.onopen?.(event)
    this.dispatchEvent(event)
  }

  emitMessage(data: unknown, init: Omit<MessageEventInit, 'data'> = {}): void {
    const event = new MessageEvent('message', {
      ...init,
      data: typeof data === 'string' ? data : JSON.stringify(data),
    })
    this.onmessage?.(event)
    this.dispatchEvent(event)
  }

  emitError(): void {
    const event = new Event('error')
    this.onerror?.(event)
    this.dispatchEvent(event)
  }
}

function unexpectedFetch(input: RequestInfo | URL): Promise<Response> {
  const target = input instanceof Request ? input.url : String(input)
  return Promise.reject(new Error(`Unexpected fetch in test: ${target}`))
}

beforeEach(async () => {
  // Component tests render feature pages directly instead of mounting the
  // production AuthGate. Give those pages the same administrator principal
  // they would receive after a successful test login; authorization-specific
  // tests can replace it explicitly.
  setAuthPrincipal({
    user: {
      id: 'test-admin',
      username: 'admin',
      displayName: 'Admin',
      roleIds: ['admin'],
      status: 'active',
      mustChangePassword: false,
      version: 1,
      createdAt: '',
      updatedAt: '',
    },
    permissions: [
      'workspace.read', 'workspace.write', 'job.read', 'job.execute', 'job.manage', 'job.share',
      'workflow.read', 'workflow.write', 'workflow.execute', 'schedule.read', 'schedule.write', 'schedule.execute',
      'file.read', 'file.write', 'file.share', 'agent.read', 'agent.manage', 'config.read', 'config.write',
      'im.read', 'im.manage', 'im.send', 'stats.read', 'logs.read', 'logs.manage', 'logs.report',
      'skills.read', 'skills.manage', 'system.manage', 'users.read', 'users.manage', 'roles.read', 'roles.manage',
    ],
    csrfToken: 'test-csrf-token',
  })
  vi.stubGlobal('fetch', vi.fn(unexpectedFetch))
  vi.stubGlobal('EventSource', MockEventSource)
  vi.stubGlobal('ResizeObserver', class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  })
  vi.stubGlobal('matchMedia', vi.fn((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })))
  vi.stubGlobal('scrollTo', vi.fn())

  localStorage.clear()
  sessionStorage.clear()
  localStorage.setItem('quartet-language', DEFAULT_TEST_LANGUAGE)
  await i18n.changeLanguage(DEFAULT_TEST_LANGUAGE)
  document.documentElement.lang = DEFAULT_TEST_LANGUAGE
})

afterEach(() => {
  cleanup()
  setAuthPrincipal(null)
  vi.clearAllTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})
