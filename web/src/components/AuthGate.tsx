import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { AuthPrincipal } from '../auth';
import { AUTH_EXPIRED_EVENT, setAuthPrincipal } from '../auth';
import { markBootStage } from '../utils/boot';
import { setAuthForwarderEnabled } from '../utils/frontend-log';
import './AuthGate.css';

type GateStage = 'probing' | 'ready' | 'initialize' | 'login' | 'changePassword' | 'recovery' | 'probeFailed';
type HealthPayload = { authState?: 'uninitialized' | 'ready' | 'recovery'; authError?: string };

class HTTPResponseError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

function hasPublicShareToken(): boolean {
  if (typeof window === 'undefined') return false;
  const params = new URLSearchParams(window.location.search);
  const publicJob = !!params.get('shareToken') && !!params.get('jobId');
  const publicFile = params.get('view') === 'file-preview' && !!params.get('fileShareToken');
  return publicJob || publicFile;
}

function errorDetail(error: unknown): string {
  return error instanceof Error ? error.stack || error.message : String(error);
}

async function readResponseError(endpoint: string, response: Response): Promise<string> {
  const body = await response.text().catch((error) => `Failed to read response body: ${errorDetail(error)}`);
  let message = body;
  try {
    const parsed = JSON.parse(body) as { msg?: string; error?: string };
    message = parsed.msg || parsed.error || body;
  } catch { /* preserve full response */ }
  return `${endpoint} returned HTTP ${response.status}${response.statusText ? ` ${response.statusText}` : ''}${message ? `\n${message}` : ''}`;
}

async function fetchPrincipal(): Promise<AuthPrincipal> {
  const response = await fetch('/api/v1/auth/me', { cache: 'no-store' });
  if (!response.ok) throw new HTTPResponseError(response.status, await readResponseError('GET /api/v1/auth/me', response));
  return response.json() as Promise<AuthPrincipal>;
}

interface AuthGateProps { children: React.ReactNode }

export function AuthGate({ children }: AuthGateProps) {
  const { t } = useTranslation();
  const skipGate = useMemo(() => hasPublicShareToken(), []);
  const [stage, setStage] = useState<GateStage>(skipGate ? 'ready' : 'probing');
  const [username, setUsername] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [currentPassword, setCurrentPassword] = useState('');
  const [initCode, setInitCode] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [detail, setDetail] = useState('');
  const [canReportLogs, setCanReportLogs] = useState(false);

  useEffect(() => {
    setAuthForwarderEnabled(stage === 'ready' && canReportLogs);
    markBootStage('auth-gate-rendered', `stage=${stage}`);
  }, [canReportLogs, stage]);

  const probe = useCallback(async () => {
    setStage('probing');
    setDetail('');
    try {
      const healthResponse = await fetch('/api/v1/health', { cache: 'no-store' });
      if (!healthResponse.ok) throw new Error(await readResponseError('GET /api/v1/health', healthResponse));
      const health = await healthResponse.json() as HealthPayload;
      if (health.authState === 'uninitialized') { setStage('initialize'); return; }
      if (health.authState === 'recovery') { setDetail(health.authError || 'Authentication configuration requires recovery.'); setStage('recovery'); return; }
      try {
        const principal = await fetchPrincipal();
        setAuthPrincipal(principal);
        setCanReportLogs(principal.permissions.includes('logs.report'));
        setStage(principal.user.mustChangePassword ? 'changePassword' : 'ready');
      } catch (error) {
        setAuthPrincipal(null);
        setCanReportLogs(false);
        if (error instanceof HTTPResponseError && error.status === 401) {
          setDetail('');
          setStage('login');
        } else {
          setDetail(errorDetail(error));
          setStage('probeFailed');
        }
      }
    } catch (error) {
      setDetail(errorDetail(error));
      setStage('probeFailed');
    }
  }, []);

  useEffect(() => { if (!skipGate) void probe(); }, [skipGate, probe]);
  useEffect(() => {
    if (skipGate) return;
    const expired = () => { setCanReportLogs(false); setDetail(''); setStage('login'); };
    window.addEventListener(AUTH_EXPIRED_EVENT, expired);
    return () => window.removeEventListener(AUTH_EXPIRED_EVENT, expired);
  }, [skipGate]);

  const submit = useCallback(async (kind: 'init' | 'login' | 'password') => {
    setSubmitting(true); setDetail('');
    const endpoint = kind === 'init' ? '/api/v1/auth/init' : kind === 'login' ? '/api/v1/auth/login' : '/api/v1/auth/password';
    const body = kind === 'init'
      ? { initCode, username, displayName, password, confirmPassword }
      : kind === 'login' ? { username, password } : { currentPassword, newPassword: password };
    try {
      const response = await fetch(endpoint, { method: kind === 'password' ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      if (!response.ok) throw new Error(await readResponseError(`${kind === 'password' ? 'PUT' : 'POST'} ${endpoint}`, response));
      const principal = await response.json() as AuthPrincipal;
      setAuthPrincipal(principal);
      setCanReportLogs(principal.permissions.includes('logs.report'));
      setPassword(''); setConfirmPassword(''); setCurrentPassword('');
      setStage(principal.user.mustChangePassword ? 'changePassword' : 'ready');
    } catch (error) { setDetail(errorDetail(error)); } finally { setSubmitting(false); }
  }, [confirmPassword, currentPassword, displayName, initCode, password, username]);

  const logout = useCallback(async () => {
    setSubmitting(true);
    setDetail('');
    try {
      const response = await fetch('/api/v1/auth/logout', { method: 'POST' });
      if (!response.ok) throw new Error(await readResponseError('POST /api/v1/auth/logout', response));
      setAuthPrincipal(null);
      setCanReportLogs(false);
      setStage('login');
    } catch (error) {
      setDetail(errorDetail(error));
    } finally {
      setSubmitting(false);
    }
  }, []);

  if (stage === 'ready') return <>{children}</>;

  const title = stage === 'initialize' ? t('auth.initializeTitle') : stage === 'changePassword' ? t('auth.changePasswordTitle') : stage === 'recovery' ? t('auth.recoveryTitle') : t('auth.loginTitle');
  return (
    <div className="auth-gate" data-testid="auth-gate" data-stage={stage}>
      <div className="auth-gate-card">
        <div className="auth-gate-icon" aria-hidden="true">Q</div>
        <h1 className="auth-gate-title">{title}</h1>
        {stage === 'probing' && <p className="auth-gate-desc">{t('auth.probing')}</p>}
        {stage === 'probeFailed' && <p className="auth-gate-desc">{t('auth.probeFailed')}</p>}
        {stage === 'recovery' && <p className="auth-gate-desc">{t('auth.recoveryDesc')}</p>}
        {(stage === 'initialize' || stage === 'login') && (
          <div className="auth-gate-form">
            {stage === 'initialize' && <input className="auth-gate-input" data-testid="auth-gate-init-code" value={initCode} onChange={(event) => setInitCode(event.target.value)} placeholder={t('auth.initCode')} autoComplete="one-time-code" />}
            <input className="auth-gate-input" data-testid="auth-gate-username" value={username} onChange={(event) => setUsername(event.target.value)} placeholder={t('auth.username')} autoComplete="username" autoFocus />
            {stage === 'initialize' && <input className="auth-gate-input" value={displayName} onChange={(event) => setDisplayName(event.target.value)} placeholder={t('auth.displayName')} autoComplete="name" />}
            <input className="auth-gate-input" data-testid="auth-gate-password" type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder={t('auth.password')} autoComplete={stage === 'initialize' ? 'new-password' : 'current-password'} />
            {stage === 'initialize' && <input className="auth-gate-input" type="password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} placeholder={t('auth.confirmPassword')} autoComplete="new-password" />}
            <button className="auth-gate-btn auth-gate-btn-primary" data-testid="auth-gate-submit-button" disabled={submitting || !username.trim() || !password || (stage === 'initialize' && (!initCode.trim() || password !== confirmPassword))} onClick={() => void submit(stage === 'initialize' ? 'init' : 'login')}>{submitting ? t('auth.submitting') : stage === 'initialize' ? t('auth.initialize') : t('auth.login')}</button>
          </div>
        )}
        {stage === 'changePassword' && (
          <div className="auth-gate-form">
            <p className="auth-gate-desc">{t('auth.changePasswordDesc')}</p>
            <input className="auth-gate-input" data-testid="auth-gate-current-password" type="password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} placeholder={t('auth.currentPassword')} autoComplete="current-password" />
            <input className="auth-gate-input" data-testid="auth-gate-new-password" type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder={t('auth.newPassword')} autoComplete="new-password" />
            <button className="auth-gate-btn auth-gate-btn-primary" data-testid="auth-gate-change-password" disabled={submitting || !currentPassword || !password} onClick={() => void submit('password')}>{submitting ? t('auth.submitting') : t('auth.changePassword')}</button>
            <button className="auth-gate-btn auth-gate-btn-secondary" data-testid="auth-gate-logout" disabled={submitting} onClick={() => void logout()}>{t('auth.logout')}</button>
          </div>
        )}
        {detail && <pre className="auth-gate-error-detail">{detail}</pre>}
        {(stage === 'probeFailed' || stage === 'recovery') && <div className="auth-gate-actions"><button className="auth-gate-btn auth-gate-btn-secondary" data-testid="auth-gate-retry-button" onClick={() => void probe()}>{t('common.retry')}</button></div>}
      </div>
    </div>
  );
}
