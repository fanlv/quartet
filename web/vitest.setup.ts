import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, beforeEach, vi } from 'vitest'
import i18n from './src/i18n'

const AUTH_TOKEN_STORAGE_KEY = 'quartet.x_auth_token'
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

  localStorage.clear()
  sessionStorage.clear()
  localStorage.setItem(AUTH_TOKEN_STORAGE_KEY, 'test-token')
  localStorage.setItem('quartet-language', DEFAULT_TEST_LANGUAGE)
  await i18n.changeLanguage(DEFAULT_TEST_LANGUAGE)
  document.documentElement.lang = DEFAULT_TEST_LANGUAGE
})

afterEach(() => {
  cleanup()
  vi.clearAllTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})
