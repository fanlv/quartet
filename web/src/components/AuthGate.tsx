// AuthGate
//
// Boots a small probe before rendering <App /> so that protected requests
// (workspace/list, job/list, /events SSE, /logs/frontend, etc.) are NEVER
// fired from a state where we already know they will be rejected with 403.
// Without this gate the UI starts a wave of authed requests on first paint
// and the backend access log fills with "[auth] reject ... tokenPrefix=<empty>"
// the moment the page loads on a fresh browser.
//
// Boot sequence:
//   1. GET /api/v1/health (no auth header). The endpoint is public; its
//      response now carries authRequired: bool.
//   2. authRequired === false → render children immediately.
//   3. authRequired === true && localStorage has a token →
//      probe a cheap protected endpoint (/api/v1/auth/verify) to validate
//      the token. 200 → render children. 4xx → render the token form.
//   4. authRequired === true && no token → render the token form.
//
// While the probe is in flight we render a minimal "verifying access" pane
// so the UI never flashes the un-authed App at the user.
//
// shareToken short-circuit: public read-only routes (?shareToken=...) are
// validated by the share token, not the agent token, so we skip the gate
// entirely in that case.
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { markBootStage } from '../utils/boot';
import { setAuthForwarderEnabled } from '../utils/frontend-log';
import './AuthGate.css';

const AUTH_TOKEN_STORAGE_KEY = 'quartet.x_auth_token';

type GateStage =
  | 'probing'
  | 'ready'
  | 'needToken'
  | 'invalidToken'
  | 'probeFailed';

function readToken(): string {
  return (localStorage.getItem(AUTH_TOKEN_STORAGE_KEY) ?? '').trim();
}

function hasShareToken(): boolean {
  if (typeof window === 'undefined') return false;
  const params = new URLSearchParams(window.location.search);
  return !!params.get('shareToken');
}

function fullErrorDetail(error: unknown): string {
  if (error instanceof Error) return error.stack || `${error.name}: ${error.message}`;
  return String(error);
}

async function fullHttpErrorDetail(endpoint: string, response: Response): Promise<string> {
  let body = '';
  try {
    body = await response.text();
  } catch (error) {
    body = `Failed to read response body: ${fullErrorDetail(error)}`;
  }
  const status = `${response.status}${response.statusText ? ` ${response.statusText}` : ''}`;
  return `GET ${endpoint} returned HTTP ${status}${body ? `\n${body}` : ''}`;
}

interface AuthGateProps {
  children: React.ReactNode;
}

export function AuthGate({ children }: AuthGateProps) {
  const { t } = useTranslation();
  // Public share-token routes do not pass through agentAuthMiddleware, so the
  // gate is unnecessary. Resolved once at mount — the URL doesn't change
  // share status mid-session.
  const skipGate = useMemo(() => hasShareToken(), []);
  const [stage, setStage] = useState<GateStage>(skipGate ? 'ready' : 'probing');
  const [tokenInput, setTokenInput] = useState('');
  const [showToken, setShowToken] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [probeError, setProbeError] = useState('');

  // The token-forwarder in main.tsx attaches X-AGENT-AUTH from localStorage
  // for every fetch. The frontend-log forwarder runs independently and would
  // otherwise spam /api/v1/logs/frontend with 403s while the gate is still
  // up. Disable forwarding until the gate resolves to "ready".
  useEffect(() => {
    setAuthForwarderEnabled(stage === 'ready');
    markBootStage('auth-gate-rendered', `stage=${stage}`);
  }, [stage]);

  const probe = useCallback(async () => {
    setStage('probing');
    setProbeError('');
    markBootStage('health-request-start');

    let authRequired = false;
    try {
      // Use the raw fetch — health is unauthenticated, no need to thread the
      // X-AGENT-AUTH header. Cache-bust so the probe never reads a stale
      // service-worker / proxy response.
      const res = await fetch('/api/v1/health', { cache: 'no-store' });
      markBootStage('health-request-end', `status=${res.status}`);
      if (!res.ok) {
        // Surface the HTTP error so operators can tell "backend down" (no
        // response / 5xx) apart from "endpoint missing" (404 — old binary).
        // The frontend log forwarder is still disabled at this point. The
        // entry is immediately visible in the browser console and will flush
        // to the backend only if the gate later reaches 'ready'. If probing
        // never recovers, keeping it browser-local is intentional: the report
        // endpoint is protected and would otherwise add auth noise.
        const detail = await fullHttpErrorDetail('/api/v1/health', res);
        console.error(`[AuthGate] ${detail}`);
        setProbeError(detail);
        setStage('probeFailed');
        return;
      }
      const rawBody = await res.text();
      let body: { authRequired?: boolean };
      try {
        body = JSON.parse(rawBody) as { authRequired?: boolean };
      } catch (error) {
        throw new Error(`GET /api/v1/health returned invalid JSON\n${rawBody}`, { cause: error });
      }
      authRequired = !!body?.authRequired;
    } catch (err) {
      const detail = fullErrorDetail(err);
      markBootStage('health-request-failed', detail);
      console.error('[AuthGate] /api/v1/health probe threw', err);
      setProbeError(detail);
      setStage('probeFailed');
      return;
    }

    if (!authRequired) {
      setStage('ready');
      return;
    }

    const token = readToken();
    if (!token) {
      setStage('needToken');
      return;
    }

    // The fetch wrapper in main.tsx will inject the header from localStorage
    // automatically. auth/verify is a lightweight endpoint that does not
    // probe ACP agents — it only validates the token against the middleware.
    try {
      markBootStage('auth-verify-request-start');
      const res = await fetch('/api/v1/auth/verify', { cache: 'no-store' });
      markBootStage('auth-verify-request-end', `status=${res.status}`);
      if (res.ok) {
        setStage('ready');
        return;
      }
      if (res.status === 403 || res.status === 401) {
        setProbeError(await fullHttpErrorDetail('/api/v1/auth/verify', res));
        setStage('invalidToken');
        return;
      }
      // Any other status (5xx, 404 if route shape ever changes, etc.) is
      // surfaced as a probe failure rather than silently letting App boot
      // into a broken state.
      const detail = await fullHttpErrorDetail('/api/v1/auth/verify', res);
      console.error(`[AuthGate] ${detail}`);
      setProbeError(detail);
      setStage('probeFailed');
    } catch (err) {
      const detail = fullErrorDetail(err);
      markBootStage('auth-verify-request-failed', detail);
      console.error('[AuthGate] /api/v1/auth/verify probe threw', err);
      setProbeError(detail);
      setStage('probeFailed');
    }
  }, []);

  useEffect(() => {
    if (skipGate) return;
    void probe();
  }, [skipGate, probe]);

  const handleSubmit = useCallback(async () => {
    const next = tokenInput.trim();
    if (!next) return;
    setSubmitting(true);
    try {
      localStorage.setItem(AUTH_TOKEN_STORAGE_KEY, next);
      await probe();
    } finally {
      setSubmitting(false);
    }
  }, [tokenInput, probe]);

  const handleVerifyExistingToken = useCallback(async () => {
    setSubmitting(true);
    try {
      await probe();
    } finally {
      setSubmitting(false);
    }
  }, [probe]);

  const handlePaste = useCallback(async () => {
    try {
      const text = await navigator.clipboard.readText();
      if (text) setTokenInput(text.trim());
    } catch (err) {
      // iOS / older browsers may reject readText without user gesture or
      // without clipboard-read permission. Fall back to leaving the input
      // empty so the user can long-press the field and paste manually.
      console.warn('[AuthGate] clipboard read failed', err);
    }
  }, []);

  if (stage === 'ready') {
    return <>{children}</>;
  }

  // Inline render so the gate is self-contained and does not depend on the
  // settings panel CSS / layout (which itself is part of <App /> and must
  // not boot before the gate resolves).
  return (
    <div className="auth-gate" data-testid="auth-gate" data-stage={stage}>
      <div className="auth-gate-card">
        <div className="auth-gate-icon">
          <svg width="48" height="48" viewBox="0 0 48 48" fill="none" xmlns="http://www.w3.org/2000/svg">
            <rect x="8" y="22" width="32" height="22" rx="4" stroke="currentColor" strokeWidth="2.5" fill="none" />
            <path d="M16 22V16a8 8 0 1 1 16 0v6" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" fill="none" />
            <circle cx="24" cy="33" r="3" fill="currentColor" />
          </svg>
        </div>
        <h1 className="auth-gate-title">{t('settings.token.gateTitle')}</h1>

        {stage === 'probing' && (
          <p className="auth-gate-desc" data-testid="auth-gate-status">{t('settings.token.gateProbing')}</p>
        )}

        {stage === 'probeFailed' && (
          <>
            <p className="auth-gate-desc" data-testid="auth-gate-error">{t('settings.token.gateProbeFailed')}</p>
            {probeError && <pre className="auth-gate-error-detail">{probeError}</pre>}
            <div className="auth-gate-actions">
              <button
                className="auth-gate-btn auth-gate-btn-primary"
                onClick={handleVerifyExistingToken}
                disabled={submitting}
                data-testid="auth-gate-retry-button"
              >
                {t('common.retry')}
              </button>
            </div>
          </>
        )}

        {(stage === 'needToken' || stage === 'invalidToken') && (
          <>
            <p className="auth-gate-desc" data-testid="auth-gate-error">
              {stage === 'invalidToken'
                ? t('settings.token.gateDescInvalidToken')
                : t('settings.token.gateDescNeedToken')}
            </p>
            {stage === 'invalidToken' && probeError && (
              <pre className="auth-gate-error-detail">{probeError}</pre>
            )}
            <div className="auth-gate-input-row">
              <input
                type={showToken ? 'text' : 'password'}
                className="auth-gate-input"
                value={tokenInput}
                onChange={(e) => setTokenInput(e.target.value)}
                placeholder="Token"
                autoComplete="off"
                spellCheck={false}
                data-testid="auth-gate-token-input"
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !submitting) handleSubmit();
                }}
              />
              <button
                type="button"
                className="auth-gate-btn auth-gate-btn-secondary"
                onClick={() => setShowToken((v) => !v)}
              >
                {showToken ? t('settings.token.hide') : t('settings.token.show')}
              </button>
            </div>
            <div className="auth-gate-actions">
              <button
                type="button"
                className="auth-gate-btn auth-gate-btn-secondary auth-gate-btn-paste"
                onClick={handlePaste}
                disabled={submitting}
              >
                {t('settings.token.paste')}
              </button>
              <button
                className="auth-gate-btn auth-gate-btn-primary"
                onClick={handleSubmit}
                disabled={submitting || !tokenInput.trim()}
                data-testid="auth-gate-submit-button"
              >
                {submitting ? t('common.saving') : t('settings.token.gateContinue')}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
