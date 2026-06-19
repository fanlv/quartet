import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './i18n'
import './index.css'
import App from './App.tsx'
import { AuthGate } from './components/AuthGate'
import { installFrontendLogForwarder } from './utils/frontend-log'

// ?clear=1 self-rescue: mobile users hitting a white-screen state can append
// the query to wipe local persistence without digging through Chrome's
// settings menu. We do this BEFORE anything else so a corrupt storage entry
// can't crash the rest of boot before the cleanup runs.
if (typeof window !== 'undefined' && /[?&]clear=1\b/.test(window.location.search)) {
  try { localStorage.clear() } catch { /* ignore */ }
  try { sessionStorage.clear() } catch { /* ignore */ }
  const url = new URL(window.location.href)
  url.searchParams.delete('clear')
  window.location.replace(url.toString())
}

/* ── iOS / iPad Chrome viewport fixes ─────────────────────────────────
 * On iOS Safari & Chrome, the virtual keyboard does NOT shrink the
 * layout viewport — it pushes content up by scrolling the visual
 * viewport. This can also happen during rapid content updates (loop
 * mode) where smooth-scroll animations cause the visual viewport to
 * drift. We compensate by ALWAYS tracking visualViewport.offsetTop
 * and positioning #root accordingly.
 * ------------------------------------------------------------------- */
function setupViewportFixes() {
  const ua = navigator.userAgent
  const isIPhone = /iPhone|iPod/.test(ua)

  // ① Prevent browser-level viewport scrolling on touch devices.
  document.addEventListener('touchmove', (e) => {
    let el = e.target as HTMLElement | null
    while (el && el !== document.documentElement) {
      const { overflowX, overflowY } = window.getComputedStyle(el)
      if ((overflowY === 'auto' || overflowY === 'scroll') && el.scrollHeight > el.clientHeight) {
        return
      }
      if ((overflowX === 'auto' || overflowX === 'scroll') && el.scrollWidth > el.clientWidth) {
        return
      }
      el = el.parentElement
    }
    e.preventDefault()
  }, { passive: false })

  // Helper: aggressively reset any viewport scroll offset
  const resetScroll = () => {
    window.scrollTo(0, 0)
    document.documentElement.scrollTop = 0
    document.body.scrollTop = 0
  }

  // ② Sync #root height to the visual viewport ONLY when virtual keyboard
  //    is open. Otherwise, let CSS handle sizing (100dvh).
  //    On iPad Chrome, visualViewport.height excludes the bottom toolbar,
  //    which makes the root shorter than the visible area — causing a gap.
  //    By only applying JS sizing when the keyboard is open, we avoid that.
  const root = document.getElementById('root')
  const vv = window.visualViewport
  if (root && vv) {
    // baseHeight needs to be the largest height we've ever seen so the
    // keyboard-open delta is meaningful. Capturing it at module load can
    // miss the URL bar collapse on iPhone Chrome and cause false positives.
    let baseHeight = Math.max(vv.height, window.innerHeight || 0)

    const syncRootToViewport = () => {
      // Detect keyboard: viewport shrinks significantly (>100px) from base
      const heightDiff = baseHeight - vv.height
      const isKeyboardOpen = heightDiff > 100

      if (isKeyboardOpen) {
        root.style.height = `${vv.height}px`
      } else {
        // No keyboard — clear inline height, let CSS 100dvh handle it
        root.style.height = ''
        baseHeight = Math.max(baseHeight, vv.height)
      }

      // Compensate for visual viewport offset on iPad Chrome, where the
      // visual viewport can scroll independently and shift #root off-screen.
      // iPhone Chrome's bottom URL bar produces sub-pixel offsetTop drift
      // that ends up pushing #root downward into a white gap, so skip it.
      if (!isIPhone) {
        const offset = vv.offsetTop
        root.style.top = offset > 1 ? `${offset}px` : ''
      } else {
        root.style.top = ''
      }

      resetScroll()
    }
    // Reset baseHeight on orientation change so portrait→landscape rotation
    // doesn't permanently false-detect the keyboard as open.
    window.addEventListener('orientationchange', () => {
      setTimeout(() => { baseHeight = vv.height }, 200)
    })

    vv.addEventListener('resize', syncRootToViewport)
    vv.addEventListener('scroll', syncRootToViewport)
  }

  // ③ focusout path — extra safety net for keyboard dismiss via blur
  document.addEventListener('focusout', (e) => {
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) {
      requestAnimationFrame(() => {
        if (document.activeElement instanceof HTMLInputElement ||
            document.activeElement instanceof HTMLTextAreaElement) {
          return
        }
        resetScroll()
        setTimeout(resetScroll, 80)
        setTimeout(resetScroll, 300)
      })
    }
  })

  // ④ Prevent window-level scroll drift on iPad.
  //    On iPad Safari/Chrome, the outer window can scroll even when
  //    html/body have overflow:hidden, especially during rapid content
  //    updates (loop mode). Immediately reset any window scroll.
  let scrollResetRaf = 0
  window.addEventListener('scroll', () => {
    if (window.scrollY !== 0 || window.scrollX !== 0) {
      resetScroll()
      // Also schedule a rAF reset — on iOS, synchronous scrollTo inside
      // a scroll handler sometimes doesn't stick.
      cancelAnimationFrame(scrollResetRaf)
      scrollResetRaf = requestAnimationFrame(resetScroll)
    }
  }, { passive: true })
}
setupViewportFixes()

// Visible boot-error overlay. White-screen reports on mobile Chrome are hard
// to diagnose without remote DevTools — this paints any uncaught error or
// rejected promise straight onto the page so the user can read it without a
// console.
function installBootErrorOverlay() {
  const show = (label: string, detail: string) => {
    let host = document.getElementById('boot-error-overlay') as HTMLDivElement | null
    if (!host) {
      host = document.createElement('div')
      host.id = 'boot-error-overlay'
      host.style.cssText = [
        'position:fixed', 'inset:0', 'z-index:2147483647',
        'background:#fff', 'color:#b00020',
        'font:12px/1.5 -apple-system,Menlo,monospace',
        'padding:16px', 'overflow:auto', 'white-space:pre-wrap',
        'word-break:break-all', '-webkit-user-select:text', 'user-select:text',
      ].join(';')
      document.body.appendChild(host)
      const btn = document.createElement('button')
      btn.textContent = 'Clear localStorage & reload'
      btn.style.cssText = 'margin:8px 0 16px;padding:8px 12px;font-size:13px'
      btn.onclick = () => {
        try { localStorage.clear() } catch { /* ignore */ }
        try { sessionStorage.clear() } catch { /* ignore */ }
        location.reload()
      }
      host.appendChild(btn)
    }
    const block = document.createElement('div')
    block.style.cssText = 'margin-bottom:12px;padding-top:12px;border-top:1px solid #eee'
    block.textContent = `[${label}] ${detail}`
    host.appendChild(block)
  }
  window.addEventListener('error', (e) => {
    // "ResizeObserver loop completed with undelivered notifications" is a
    // benign, self-recovering browser notice (commonly emitted by React Flow's
    // canvas resize observation). It is NOT a crash — never paint the fatal
    // overlay for it, or the Graph canvas becomes unusable after a resize.
    if (isBenignResizeObserverError(e.message)) return
    show('error', `${e.message}\n${e.filename}:${e.lineno}:${e.colno}\n${e.error?.stack || ''}`)
  })
  window.addEventListener('unhandledrejection', (e) => {
    const r = e.reason
    show('unhandledrejection', r?.stack || r?.message || String(r))
  })
  // React in dev does not rethrow render errors to window.onerror; it logs
  // them to console.error. Intercept that so a render-time crash inside
  // <App /> still paints to the overlay instead of leaving a white screen.
  //
  // CRITICAL: filter aggressively. The overlay is a fixed/inset:0/z-index:max
  // white sheet — paint it once and the entire UI is unusable until reload.
  // We must NOT trigger it for:
  //   - React dev warnings (duplicate key, prop-types, missing alt, etc.)
  //   - Business catch-block console.error (network blip, SSE retry, 403
  //     during AuthGate probe, etc. — all recoverable by design)
  //   - React StrictMode double-invoke side-effect diagnostics
  // We DO want to trigger it for genuine render crashes, which React surfaces
  // either as a bare Error first-arg or with the "The above error occurred"
  // / "Uncaught" prefix (still emitted in dev when no error boundary catches).
  const origConsoleError = console.error.bind(console)
  console.error = (...args: unknown[]) => {
    try {
      if (isFatalRenderErrorLog(args)) {
        const text = args.map((a) => {
          if (a instanceof Error) return a.stack || a.message
          if (typeof a === 'string') return a
          try { return JSON.stringify(a) } catch { return String(a) }
        }).join(' ')
        show('console.error', text)
      }
    } catch { /* never let the overlay path break console */ }
    origConsoleError(...args)
  }
}

// isFatalRenderErrorLog returns true only for console.error payloads that look
// like an unrecovered React render crash. React's render-error log shape is
// stable across 18/19: either the first arg is the thrown Error itself, or the
// message starts with one of a small set of known prefixes. Everything else
// (dev warnings with %s/%o templates, app-level catch logs) is intentionally
// excluded so the overlay does not hijack the UI on recoverable failures.
function isFatalRenderErrorLog(args: unknown[]): boolean {
  if (args.length === 0) return false
  const first = args[0]
  if (first instanceof Error) return true
  if (typeof first !== 'string') return false
  if (isBenignResizeObserverError(first)) return false
  if (first.startsWith('The above error occurred')) return true
  if (first.startsWith('Uncaught ')) return true
  // React 19 reports unhandled errors from concurrent renders with this
  // prefix when no error boundary intercepts.
  if (first.startsWith('An error occurred in the <')) return true
  return false
}

// isBenignResizeObserverError matches the well-known, self-recovering browser
// notice that fires when a ResizeObserver callback schedules another layout
// pass. React Flow's canvas triggers it on resize; it is noise, not a crash.
function isBenignResizeObserverError(message: unknown): boolean {
  return typeof message === 'string' && message.includes('ResizeObserver loop')
}
installBootErrorOverlay()

// Capture console.warn/error and unhandled errors so the Settings → 日志 tab
// can show frontend issues alongside backend logs.
installFrontendLogForwarder()

const AUTH_TOKEN_STORAGE_KEY = 'quartet.x_auth_token'
const AUTH_HEADER_NAME = 'X-AGENT-AUTH'

const originalFetch: typeof window.fetch = window.fetch.bind(window)

function getAuthToken(): string {
  return (localStorage.getItem(AUTH_TOKEN_STORAGE_KEY) ?? '').trim()
}

function mergeHeaders(base?: HeadersInit, extra?: HeadersInit): Headers {
  const headers = new Headers(base)
  if (extra) {
    new Headers(extra).forEach((value, key) => {
      headers.set(key, value)
    })
  }
  return headers
}

window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
  const token = getAuthToken()
  if (!token) return originalFetch(input, init)

  if (input instanceof Request) {
    const headers = mergeHeaders(input.headers, init?.headers)
    headers.set(AUTH_HEADER_NAME, token)
    const req = new Request(input, { ...init, headers })
    return originalFetch(req)
  }

  const headers = mergeHeaders(init?.headers)
  headers.set(AUTH_HEADER_NAME, token)
  return originalFetch(input, { ...init, headers })
}) as typeof window.fetch

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AuthGate>
      <App />
    </AuthGate>
  </StrictMode>,
)
