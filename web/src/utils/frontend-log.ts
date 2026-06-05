// frontend-log.ts
//
// Capture browser console.warn / console.error and forward them to the
// backend ring buffer so operators can see frontend + backend issues in one
// feed under Settings → 日志.
//
// Why only warn/error: log/info/debug are extremely chatty in dev (SSE
// reconnects, render diagnostics) and would dominate the buffer. Warn and
// error are the signals worth correlating with backend events.

interface FrontendLogEntry {
  timestamp: number;
  level: 'WARN' | 'ERROR';
  source: string;
  message: string;
}

const FLUSH_INTERVAL_MS = 1500;
const MAX_QUEUE = 200;

const queue: FrontendLogEntry[] = [];
let flushTimer: number | null = null;
let installed = false;
// Until AuthGate confirms the token is accepted (or that the server doesn't
// require one), we MUST NOT POST to /api/v1/logs/frontend — the endpoint
// lives behind agentAuthMiddleware and would otherwise contribute to the
// 403 storm the gate exists to prevent. Default to disabled; AuthGate
// flips this to true once it resolves to the "ready" stage.
let forwarderEnabled = false;

// setAuthForwarderEnabled is called by AuthGate. While disabled, console
// warn/error are still captured into the in-memory queue (capped by
// MAX_QUEUE) and remain visible in the browser console. If the gate later
// reaches "ready", queued entries flush on the next interval after enabling.
// If the gate never reaches "ready" (missing/invalid token or failed probe),
// they intentionally stay browser-local instead of hammering the protected
// report endpoint with requests that are expected to fail.
export function setAuthForwarderEnabled(enabled: boolean) {
  forwarderEnabled = enabled;
  if (enabled && queue.length > 0 && flushTimer == null) {
    flushTimer = window.setTimeout(flush, FLUSH_INTERVAL_MS);
  }
}

function enqueue(entry: FrontendLogEntry) {
  queue.push(entry);
  if (queue.length > MAX_QUEUE) {
    queue.splice(0, queue.length - MAX_QUEUE);
  }
  if (flushTimer == null) {
    flushTimer = window.setTimeout(flush, FLUSH_INTERVAL_MS);
  }
}

async function flush() {
  flushTimer = null;
  if (queue.length === 0) return;
  // While the AuthGate is up, the POST would 403. Hold the queue (still
  // bounded by MAX_QUEUE on the producer side) and wait for the gate to
  // re-arm us — flushing at 403 would just convert the auth gate's job
  // into a steady drip of "[auth] reject POST /api/v1/logs/frontend"
  // entries in the backend ring buffer. In permanent non-ready states the
  // queue remains best-effort/browser-local by design.
  if (!forwarderEnabled) return;
  const batch = queue.splice(0, queue.length);
  try {
    await fetch('/api/v1/logs/frontend', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ entries: batch }),
      // Ignore network errors — losing browser logs is not worth retrying
      // from inside an error handler (could cause infinite loops).
      keepalive: true,
    });
  } catch {
    /* swallow — frontend logs are best-effort */
  }
}

function safeStringify(arg: unknown): string {
  if (arg instanceof Error) {
    return `${arg.name}: ${arg.message}${arg.stack ? '\n' + arg.stack : ''}`;
  }
  if (typeof arg === 'string') return arg;
  if (arg === null || arg === undefined) return String(arg);
  if (typeof arg === 'object') {
    try {
      return JSON.stringify(arg);
    } catch {
      return String(arg);
    }
  }
  return String(arg);
}

// formatConsoleArgs mirrors util.format / the browser's console formatter just
// enough to substitute the common placeholders React uses in its warnings
// (`%s`, `%o`, `%O`, `%d`, `%i`, `%f`, `%c`). Without this, React warnings of
// the form `console.error('duplicate key: %s', keyValue)` would reach the
// backend as the literal "%s" string, hiding the actual key value.
function formatConsoleArgs(args: unknown[]): string {
  if (args.length === 0) return '';
  const first = args[0];
  if (typeof first !== 'string' || !first.includes('%')) {
    return args.map(safeStringify).join(' ');
  }
  const rest = args.slice(1);
  let ri = 0;
  const formatted = first.replace(/%[sdifoOc%]/g, token => {
    if (token === '%%') return '%';
    if (token === '%c') {
      // CSS styling arg — consumed but not rendered in a plain-text log.
      ri += 1;
      return '';
    }
    if (ri >= rest.length) return token;
    const v = rest[ri++];
    switch (token) {
      case '%s': return typeof v === 'string' ? v : safeStringify(v);
      case '%d':
      case '%i': return Number.isFinite(v as number) ? String(Math.trunc(v as number)) : safeStringify(v);
      case '%f': return Number.isFinite(v as number) ? String(v) : safeStringify(v);
      case '%o':
      case '%O': return safeStringify(v);
      default: return token;
    }
  });
  const tail = rest.slice(ri).map(safeStringify).join(' ');
  return tail ? `${formatted} ${tail}` : formatted;
}

// captureStack returns a short stack trace for a warn/error site, skipping the
// two inner frames (this helper + the console override) so the first visible
// frame is the caller that triggered the warning.
function captureStack(): string {
  const raw = new Error().stack;
  if (!raw) return '';
  const lines = raw.split('\n');
  // Drop the "Error" header + this frame + the console override frame.
  const sliced = lines.slice(3, 12).join('\n').trim();
  return sliced ? '\n' + sliced : '';
}

function detectSource(message: string): string {
  // Most frontend logs already self-tag with [Foo] prefixes (SSEClient,
  // JobEvents, etc.). Pull the first bracketed token as the source and let
  // the backend prefix "frontend/" so it shows up grouped in the UI.
  const m = message.match(/^\[([^\]]+)\]/);
  if (m) return m[1].toLowerCase();
  return 'browser';
}

export function installFrontendLogForwarder() {
  if (installed) return;
  installed = true;

  const origWarn = console.warn.bind(console);
  const origError = console.error.bind(console);

  console.warn = (...args: unknown[]) => {
    origWarn(...args);
    const message = formatConsoleArgs(args) + captureStack();
    enqueue({
      timestamp: Date.now(),
      level: 'WARN',
      source: detectSource(message),
      message,
    });
  };

  console.error = (...args: unknown[]) => {
    origError(...args);
    const message = formatConsoleArgs(args) + captureStack();
    enqueue({
      timestamp: Date.now(),
      level: 'ERROR',
      source: detectSource(message),
      message,
    });
  };

  window.addEventListener('error', (event: ErrorEvent) => {
    const msg = event.error
      ? safeStringify(event.error)
      : `${event.message} (${event.filename}:${event.lineno}:${event.colno})`;
    enqueue({
      timestamp: Date.now(),
      level: 'ERROR',
      source: 'window.error',
      message: msg,
    });
  });

  window.addEventListener('unhandledrejection', (event: PromiseRejectionEvent) => {
    enqueue({
      timestamp: Date.now(),
      level: 'ERROR',
      source: 'unhandledrejection',
      message: safeStringify(event.reason),
    });
  });
}
