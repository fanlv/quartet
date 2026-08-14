import { act, render, screen } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ConnectionBanner } from './ConnectionBanner'

// Mutable connection state driving the mocked context.
const conn = vi.hoisted(() => ({ connected: true }))

vi.mock('../contexts/ConnectionStatus', () => ({
  useConnectionStatus: () => ({
    connected: conn.connected,
    buildTime: '',
    reportDisconnect: () => {},
    reportReconnect: () => {},
  }),
}))

describe('ConnectionBanner', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows a transient "restored" confirmation when mounted while offline and connectivity returns', () => {
    conn.connected = false
    const { rerender } = render(<ConnectionBanner />)

    // Offline at mount: the disconnected banner is visible.
    expect(screen.getByTestId('connection-banner')).toHaveAttribute('data-connection-state', 'disconnected')

    // Connectivity returns: the user just watched the offline banner, so the
    // recovery should be confirmed for a few seconds instead of the banner
    // silently vanishing.
    conn.connected = true
    rerender(<ConnectionBanner />)
    expect(screen.getByTestId('connection-banner')).toHaveAttribute('data-connection-state', 'recovered')

    // …and it auto-hides after ~3s.
    act(() => {
      vi.advanceTimersByTime(3100)
    })
    expect(screen.queryByTestId('connection-banner')).toBeNull()
  })

  it('keeps the existing connected→disconnected→recovered flow working', () => {
    conn.connected = true
    const { rerender } = render(<ConnectionBanner />)
    expect(screen.queryByTestId('connection-banner')).toBeNull()

    conn.connected = false
    rerender(<ConnectionBanner />)
    expect(screen.getByTestId('connection-banner')).toHaveAttribute('data-connection-state', 'disconnected')

    conn.connected = true
    rerender(<ConnectionBanner />)
    expect(screen.getByTestId('connection-banner')).toHaveAttribute('data-connection-state', 'recovered')

    act(() => {
      vi.advanceTimersByTime(3100)
    })
    expect(screen.queryByTestId('connection-banner')).toBeNull()
  })
})
