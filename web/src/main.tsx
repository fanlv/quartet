import { lazy, StrictMode, Suspense } from 'react'
import { createRoot } from 'react-dom/client'
import './i18n'
import './index.css'
import './native-theme.css'
import App from './App.tsx'
import { AuthGate } from './components/AuthGate'
import { BootComplete } from './components/BootComplete'
import { markBootStage, reportBootFailure } from './utils/boot'
import { installFrontendLogForwarder } from './utils/frontend-log'
import { AUTH_EXPIRED_EVENT, getCSRFToken, setAuthPrincipal } from './auth'

markBootStage('main-module-executing')

const SOFT_KEYBOARD_INPUT_TYPES = new Set([
  'email',
  'number',
  'password',
  'search',
  'tel',
  'text',
  'url',
])

function isTextEditingElement(element: Element | null): element is HTMLElement {
  if (element instanceof HTMLTextAreaElement) {
    return !element.disabled && !element.readOnly
  }
  if (element instanceof HTMLInputElement) {
    return !element.disabled && !element.readOnly && SOFT_KEYBOARD_INPUT_TYPES.has(element.type)
  }
  return element instanceof HTMLElement && element.isContentEditable
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
  // WebKit Safari (iOS, iPadOS and macOS) reports window.innerHeight as the
  // *large* viewport: the height the page would get once the browser chrome
  // collapses. Our shell never scrolls the document (body is overflow:hidden),
  // so on iPhone the URL bar and on iPad the tab bar never collapse, and a root
  // sized to innerHeight always keeps its bottom edge — the composer — hidden
  // behind that chrome. visualViewport is the only value that tracks the region
  // the user can actually see there. Chromium/Gecko keep innerHeight in sync
  // with the visible area, and iOS Chrome additionally needs the max() fallback
  // below, so the override is scoped to Safari.
  const isWebKitSafari = /AppleWebKit/.test(ua)
    && / Version\/\d/.test(ua)
    && !/CriOS|FxiOS|EdgiOS|OPiOS|Chrome|Chromium|Android/.test(ua)

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

  // ② Keep #root aligned with the usable browser viewport. iOS Chrome can
  //    leave 100dvh or visualViewport.height one toolbar-height too short
  //    after its chrome or keyboard animates, so neither value is sufficient
  //    on its own.
  const root = document.getElementById('root')
  const vv = window.visualViewport
  if (root && vv) {
    // Keep the latest non-keyboard viewport as the baseline. Browser chrome
    // expanding can also shrink visualViewport by more than 100px, so a
    // historical maximum is not a reliable keyboard signal.
    let baseHeight = vv.height

    const layoutViewportHeight = () => {
      if (isWebKitSafari) {
        // Multiply out the pinch-zoom factor so zooming in does not shrink the
        // app shell to the magnified slice the user happens to be looking at.
        const scale = vv.scale > 0 ? vv.scale : 1
        const visible = vv.height * scale
        if (Number.isFinite(visible) && visible > 0) return visible
      }
      const heights = [window.innerHeight, document.documentElement.clientHeight, vv.height]
        .filter((height) => Number.isFinite(height) && height > 0)
      return heights.length > 0 ? Math.max(...heights) : vv.height
    }

    const restoreRootHeight = () => {
      root.style.height = `${layoutViewportHeight()}px`
    }

    const syncRootToViewport = () => {
      const editingText = isTextEditingElement(document.activeElement)
      const heightDiff = baseHeight - vv.height
      // Mobile browser toolbars can consume roughly 100px on their own. Only
      // treat a shrink as the soft keyboard while a real text editor is
      // focused, and scale the threshold for taller phone/tablet viewports.
      const keyboardThreshold = Math.max(100, baseHeight * 0.2)
      const isKeyboardOpen = editingText && heightDiff > keyboardThreshold

      if (isKeyboardOpen) {
        root.style.height = `${vv.height}px`
      } else {
        // Do not hand sizing back to 100dvh here. On iOS Chrome it can stay
        // one toolbar-height too short even after visualViewport recovers.
        restoreRootHeight()
        if (!editingText) baseHeight = layoutViewportHeight()
      }

      // Ignore sub-pixel toolbar drift, but compensate real viewport panning on every device.
      const offset = vv.offsetTop
      root.style.top = offset > 1 ? `${offset}px` : ''

      resetScroll()
    }

    // Opening the page through another iOS app leaves the browser chrome in
    // transition after pageshow. WebKit may update its viewport metrics
    // without emitting resize, so sample through the whole transition.
    const stabilizationDelays = [0, 50, 150, 300, 600, 1000, 1600, 2400]
    let stabilizationGeneration = 0
    const stabilizeViewport = () => {
      const generation = ++stabilizationGeneration
      for (const delay of stabilizationDelays) {
        window.setTimeout(() => {
          if (generation === stabilizationGeneration) syncRootToViewport()
        }, delay)
      }
    }

    // Capture the viewport immediately before the keyboard opens. Moving
    // between two editors keeps the existing baseline because the keyboard
    // may already be visible during that focus transfer.
    document.addEventListener('focusin', (event) => {
      if (!isTextEditingElement(event.target as Element | null)) return
      if (!isTextEditingElement(event.relatedTarget as Element | null)) {
        restoreRootHeight()
        baseHeight = layoutViewportHeight()
      }
      requestAnimationFrame(syncRootToViewport)
    })

    // Keyboard dismissal emits its final viewport values after focusout on
    // some iOS versions, sometimes without a resize event.
    document.addEventListener('focusout', (event) => {
      if (!isTextEditingElement(event.target as Element | null)) return

      const restoreAfterKeyboard = () => {
        if (isTextEditingElement(document.activeElement)) return
        restoreRootHeight()
        baseHeight = layoutViewportHeight()
        resetScroll()
      }

      requestAnimationFrame(restoreAfterKeyboard)
      setTimeout(restoreAfterKeyboard, 80)
      setTimeout(restoreAfterKeyboard, 300)
      stabilizeViewport()
    })

    // Reset baseHeight on orientation change so portrait→landscape rotation
    // doesn't permanently false-detect the keyboard as open.
    window.addEventListener('orientationchange', () => {
      restoreRootHeight()
      setTimeout(() => {
        baseHeight = layoutViewportHeight()
        stabilizeViewport()
      }, 200)
    })

    // Returning from another tab/app can restore stale viewport metrics.
    const restoreVisibleViewport = () => {
      if (document.visibilityState === 'hidden') return
      if (!isTextEditingElement(document.activeElement)) {
        restoreRootHeight()
        baseHeight = layoutViewportHeight()
      }
      stabilizeViewport()
    }
    window.addEventListener('pageshow', restoreVisibleViewport)
    window.addEventListener('focus', stabilizeViewport)
    window.addEventListener('resize', syncRootToViewport)
    document.addEventListener('visibilitychange', restoreVisibleViewport)

    vv.addEventListener('resize', syncRootToViewport)
    vv.addEventListener('scroll', syncRootToViewport)
    stabilizeViewport()
  }

  // ③ Prevent window-level scroll drift on iPad.
  //    On iPad Safari/Chrome, the outer window can scroll even when
  //    html/body have overflow:hidden, especially during rapid content
  //    updates. Immediately reset any window scroll.
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

// The HTML bootstrap already owns window.error, unhandledrejection, the
// startup timeout, and the recovery UI. The main bundle only needs to bridge
// fatal React console errors into that bootstrap. Keeping the bootstrap in
// index.html means this path still works when this module never loads.
function installBootErrorOverlay() {
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
        reportBootFailure('REACT_RENDER_ERROR', text)
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

const FilePreviewPage = lazy(() =>
  import('./components/FilePreviewPage').then((module) => ({ default: module.FilePreviewPage })),
)

const originalFetch: typeof window.fetch = window.fetch.bind(window)

function mergeHeaders(base?: HeadersInit, extra?: HeadersInit): Headers {
  const headers = new Headers(base)
  if (extra) {
    new Headers(extra).forEach((value, key) => {
      headers.set(key, value)
    })
  }
  return headers
}

function isSameOrigin(url: string): boolean {
  try {
    return new URL(url, window.location.href).origin === window.location.origin
  } catch {
    return false
  }
}

window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
  if (input instanceof Request) {
    // Request.url is already absolute, resolved against the document.
    if (!isSameOrigin(input.url)) return originalFetch(input, init)
    const headers = mergeHeaders(input.headers, init?.headers)
    const method = (init?.method || input.method || 'GET').toUpperCase()
    const csrf = getCSRFToken()
    if (csrf && !['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', csrf)
    const req = new Request(input, { ...init, headers, credentials: init?.credentials ?? 'include' })
    return originalFetch(req).then(handleAuthResponse)
  }

  if (!isSameOrigin(String(input))) return originalFetch(input, init)

  const headers = mergeHeaders(init?.headers)
  const method = (init?.method || 'GET').toUpperCase()
  const csrf = getCSRFToken()
  if (csrf && !['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', csrf)
  return originalFetch(input, { ...init, headers, credentials: init?.credentials ?? 'include' }).then(handleAuthResponse)
}) as typeof window.fetch

function handleAuthResponse(response: Response): Response {
  if (response.status === 401) {
    setAuthPrincipal(null)
    window.dispatchEvent(new Event(AUTH_EXPIRED_EVENT))
  }
  return response
}

markBootStage('react-render-start')
const isFilePreviewRoute = new URLSearchParams(window.location.search).get('view') === 'file-preview'
const isPublicFilePreview = isFilePreviewRoute && new URLSearchParams(window.location.search).has('fileShareToken')
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BootComplete />
    {isPublicFilePreview ? (
      <Suspense fallback={<div style={{ padding: 24 }}>正在加载文件预览…</div>}>
        <FilePreviewPage />
      </Suspense>
    ) : (
      <AuthGate>
        {isFilePreviewRoute ? (
          <Suspense fallback={<div style={{ padding: 24 }}>正在加载文件预览…</div>}>
            <FilePreviewPage />
          </Suspense>
        ) : <App />}
      </AuthGate>
    )}
  </StrictMode>,
)
