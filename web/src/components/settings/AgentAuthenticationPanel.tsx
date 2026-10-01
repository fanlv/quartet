import { Fragment, useCallback, useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import './AgentAuthenticationPanel.css';

interface AuthMethod {
  id: string;
  name: string;
  description?: string;
  type: 'agent' | 'terminal' | 'env_var';
  command?: string;
  environment?: string[];
}

interface AuthAttempt {
  id: string;
  revision: string;
  method_id?: string;
  status: string;
  output?: string;
  error?: string;
  links?: Array<{ url: string; message?: string }>;
}

interface AuthInfo {
  revision: string;
  methods: AuthMethod[];
  attempt?: AuthAttempt;
}

interface Props {
  agentId: string;
  name: string;
  request: (url: string, init?: RequestInit) => Promise<Record<string, unknown>>;
  onChanged: () => void;
  onClose: () => void;
}

const isActive = (attempt: AuthAttempt | null) => !!attempt
  && ['checking', 'authenticating', 'validating'].includes(attempt.status);

function readAttempt(value: unknown): AuthAttempt {
  if (!value || typeof value !== 'object') throw new Error(JSON.stringify(value));
  const attempt = value as AuthAttempt;
  if (typeof attempt.id !== 'string' || typeof attempt.revision !== 'string'
    || !['checking', 'authenticating', 'validating', 'available', 'error', 'cancelled'].includes(attempt.status)
    || (attempt.output !== undefined && typeof attempt.output !== 'string')
    || (attempt.error !== undefined && typeof attempt.error !== 'string')
    || (attempt.links !== undefined && (!Array.isArray(attempt.links)
      || !attempt.links.every((link) => link && typeof link.url === 'string'
        && (link.message === undefined || typeof link.message === 'string'))))) {
    throw new Error(JSON.stringify(value, null, 2));
  }
  return attempt;
}

function outputLinks(output: string) {
  return [...output.matchAll(/https?:\/\/[^\s<>"'\p{C}]+/gu)].flatMap((match) => {
    const url = match[0].replace(/[),.;]+$/, '');
    try {
      const parsed = new URL(url);
      return parsed.hostname ? [{ url, start: match.index, end: match.index + url.length }] : [];
    } catch { return []; }
  });
}

function AuthenticationOutput({ output }: { output: string }) {
  const links = outputLinks(output);
  let cursor = 0;
  return <pre className="agent-auth-output">{links.map((link) => {
    const before = output.slice(cursor, link.start);
    cursor = link.end;
    return <Fragment key={link.start}>{before}<a href={link.url} target="_blank" rel="noopener noreferrer">{link.url}</a></Fragment>;
  })}{output.slice(cursor)}</pre>;
}

function isSafeLink(url: string) {
  try { return ['https:', 'http:'].includes(new URL(url).protocol); }
  catch { return false; }
}

function hasLocalCallback(url: string) {
  const isLocal = (value: string) => {
    try { return ['localhost', '127.0.0.1', '[::1]'].includes(new URL(value).hostname); }
    catch { return false; }
  };
  try { return isLocal(url) || [...new URL(url).searchParams.values()].some(isLocal); }
  catch { return false; }
}

export function AgentAuthenticationPanel({ agentId, name, request, onChanged, onClose }: Props) {
  const { t } = useTranslation();
  const titleId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const changedRef = useRef(onChanged);
  const [info, setInfo] = useState<AuthInfo | null>(null);
  const [selectedId, setSelectedId] = useState('');
  const [attempt, setAttempt] = useState<AuthAttempt | null>(null);
  const [pending, setPending] = useState(true);
  const [error, setError] = useState('');
  const [verified, setVerified] = useState(false);
  const [needsLogin, setNeedsLogin] = useState(false);
  const [notice, setNotice] = useState('');
  const [loadKey, setLoadKey] = useState(0);
  const checkingRef = useRef<AbortController | null>(null);
  const mountedRef = useRef(false);
  const baseURL = `/api/v1/agent/${encodeURIComponent(agentId)}`;
  const active = isActive(attempt);
  const attemptId = attempt?.id;
  const method = info?.methods.find((item) => item.id === selectedId);
  const loginLinks = [...new Map<string, { url: string; message?: string }>([
    ...outputLinks(attempt?.output || '').map(({ url }) => ({ url })),
    ...(attempt?.links || []).filter((link) => isSafeLink(link.url)),
  ].map((link) => [link.url, link])).values()];
  changedRef.current = onChanged;

  const verify = useCallback(async (currentInfo: AuthInfo, signal?: AbortSignal) => {
    if (checkingRef.current) return;
    const controller = new AbortController();
    const abort = () => controller.abort();
    signal?.addEventListener('abort', abort, { once: true });
    checkingRef.current = controller;
    setPending(true); setError(''); setNotice(''); setAttempt(null); setVerified(false); setNeedsLogin(false);
    try {
      const data = await request(`${baseURL}/revalidate`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, signal: controller.signal,
        body: JSON.stringify({ revision: currentInfo.revision }),
      });
      if (!mountedRef.current || controller.signal.aborted) return;
      const validation = data.validation as { success?: boolean; error?: string; authentication_required?: boolean } | undefined;
      if (!validation || typeof validation.success !== 'boolean'
        || (validation.error !== undefined && typeof validation.error !== 'string')
        || (validation.authentication_required !== undefined && typeof validation.authentication_required !== 'boolean')) {
        throw new Error(JSON.stringify(data, null, 2));
      }
      setVerified(validation.success);
      setNeedsLogin(!validation.success && validation.authentication_required === true);
      if (!validation.success) setError(validation.error || JSON.stringify(data, null, 2));
      if (typeof data.warning === 'string') setNotice(data.warning);
    } catch (err) {
      if (mountedRef.current && !controller.signal.aborted) setError(err instanceof Error ? err.message : String(err));
    } finally {
      signal?.removeEventListener('abort', abort);
      if (checkingRef.current === controller) checkingRef.current = null;
      if (mountedRef.current && !controller.signal.aborted) {
        setPending(false);
        changedRef.current();
      }
    }
  }, [baseURL, request]);

  useEffect(() => {
    let disposed = false;
    const controller = new AbortController();
    mountedRef.current = true;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialogRef.current?.focus();
    setPending(true);
    setError(''); setNotice(''); setInfo(null); setVerified(false); setNeedsLogin(false); setAttempt(null);
    request(`${baseURL}/authentication`, { signal: controller.signal }).then(async (data) => {
      if (disposed) return;
      if (typeof data.revision !== 'string' || !Array.isArray(data.methods)
        || !data.methods.every((item) => item && typeof item.id === 'string'
          && typeof item.name === 'string' && ['agent', 'terminal', 'env_var'].includes(item.type)
          && (item.command === undefined || typeof item.command === 'string')
          && (item.description === undefined || typeof item.description === 'string')
          && (item.environment === undefined || (Array.isArray(item.environment)
            && item.environment.every((key: unknown) => typeof key === 'string'))))) {
        throw new Error(JSON.stringify(data, null, 2));
      }
      const nextAttempt = data.attempt ? readAttempt(data.attempt) : null;
      const nextInfo = data as unknown as AuthInfo;
      setInfo(nextInfo);
      setSelectedId(nextInfo.methods.find((item) => item.id === nextAttempt?.method_id)?.id || nextInfo.methods[0]?.id || '');
      setAttempt(nextAttempt);
      if (!isActive(nextAttempt)) await verify(nextInfo, controller.signal);
    }).catch((err: unknown) => {
      if (!disposed) setError(err instanceof Error ? err.message : String(err));
    }).finally(() => { if (!disposed) setPending(false); });
    return () => {
      disposed = true;
      mountedRef.current = false;
      controller.abort();
      checkingRef.current?.abort();
      checkingRef.current = null;
      previousFocus?.focus();
    };
  }, [baseURL, request, verify, loadKey]);

  useEffect(() => {
    if (!active || !attemptId) return;
    let disposed = false;
    let timeout: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const data = await request(`${baseURL}/authentication/${encodeURIComponent(attemptId)}`);
        if (disposed) return;
        const next = readAttempt(data.attempt);
        setAttempt(next);
        setError('');
        if (!isActive(next)) {
          setVerified(next.status === 'available');
          setNeedsLogin(false);
          changedRef.current();
          return;
        }
      } catch (err) {
        if (disposed) return;
        setError(err instanceof Error ? err.message : String(err));
      }
      timeout = setTimeout(() => void poll(), 1000);
    };
    timeout = setTimeout(() => void poll(), 500);
    return () => { disposed = true; clearTimeout(timeout); };
  }, [active, attemptId, baseURL, request]);

  useEffect(() => {
    if (!needsLogin || pending || active || !info) return;
    const checkOnReturn = () => {
      if (document.visibilityState === 'visible') void verify(info);
    };
    window.addEventListener('focus', checkOnReturn);
    document.addEventListener('visibilitychange', checkOnReturn);
    return () => {
      window.removeEventListener('focus', checkOnReturn);
      document.removeEventListener('visibilitychange', checkOnReturn);
    };
  }, [needsLogin, pending, active, info, verify]);

  const close = useCallback(async () => {
    if (pending && !checkingRef.current && info) return;
    if (active && attempt) {
      setPending(true);
      try {
        await request(`${baseURL}/authentication/${encodeURIComponent(attempt.id)}/cancel`, { method: 'POST' });
      } catch (err) { setError(err instanceof Error ? err.message : String(err)); setPending(false); return; }
    }
    onClose();
  }, [pending, info, active, attempt, baseURL, request, onClose]);

  const authenticate = async () => {
    if (!info || !method || method.type !== 'agent') return;
    setPending(true); setError(''); setNotice(''); setVerified(false); setNeedsLogin(false);
    try {
      const data = await request(`${baseURL}/authentication`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ revision: info.revision, method_id: method.id }),
      });
      if (!mountedRef.current) return;
      const next = readAttempt(data.attempt);
      setAttempt(next);
      if (!isActive(next)) {
        setVerified(next.status === 'available');
        changedRef.current();
      }
    } catch (err) { setError(err instanceof Error ? err.message : String(err)); }
    finally { if (mountedRef.current) setPending(false); }
  };

  const continueConnection = () => {
    if (verified) void close();
    else if (method?.type === 'agent' && (needsLogin || attempt?.status === 'error' || attempt?.status === 'cancelled')) void authenticate();
    else if (info) void verify(info);
    else setLoadKey((current) => current + 1);
  };
  const busy = pending || active;
  const failed = !busy && !verified && !needsLogin && !!(error || attempt?.error);
  const statusKey = busy ? `progress.${active ? attempt?.status : 'checking'}`
    : verified ? 'ready' : needsLogin ? 'required' : attempt?.status === 'cancelled' ? 'progress.cancelled' : 'failed';

  return (
    <div className="agent-auth-overlay" onClick={(event) => { if (event.target === event.currentTarget) void close(); }}>
      <div className="agent-auth-panel" role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} ref={dialogRef}
        onKeyDown={(event) => {
          if (event.key === 'Escape') { event.stopPropagation(); void close(); }
          if (event.key === 'Tab') {
            const controls = dialogRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled), select:not(:disabled), a[href], summary');
            const first = controls?.[0]; const last = controls?.[controls.length - 1];
            if (event.shiftKey && (document.activeElement === first || document.activeElement === dialogRef.current)) { event.preventDefault(); last?.focus(); }
            else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
          }
        }}>
        <div className="agent-auth-heading">
          <h3 id={titleId}>{t('settings.agents.auth.title', { name })}</h3>
          <button type="button" className="agent-auth-close" disabled={pending && !checkingRef.current && info !== null} onClick={() => void close()} aria-label={t('common.close')}>×</button>
        </div>
        <div className={`agent-auth-status ${busy ? 'checking' : verified ? 'success' : needsLogin ? 'login' : failed ? 'error' : ''}`} role="status" aria-live="polite">
          <span className="agent-auth-status-icon" aria-hidden="true">{busy ? <span className="agent-check-spinner" /> : verified ? '✓' : needsLogin ? '↗' : '!'}</span>
          <div>
            <strong>{t(`settings.agents.auth.${statusKey}`)}</strong>
            {(verified || needsLogin) && <p>{t(`settings.agents.auth.${verified ? 'readyHint' : 'loginHint'}`)}</p>}
          </div>
        </div>
        {needsLogin && info && info.methods.length > 1 && <label className="agent-auth-method">
          <span>{t('settings.agents.auth.method')}</span>
          <select value={selectedId} onChange={(event) => setSelectedId(event.target.value)} disabled={pending || active}>
            {info.methods.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
        </label>}
        {needsLogin && info?.methods.length === 0 && <p>{t('settings.agents.auth.manual')}</p>}
        {needsLogin && method?.type === 'terminal' && <div className="agent-auth-guide">
          <p>{t('settings.agents.auth.terminal')}</p>
          {method.command && <pre className="agent-auth-output">{method.command}</pre>}
        </div>}
        {needsLogin && !!method?.environment?.length && <p className="agent-auth-note">{t('settings.agents.auth.environment', { keys: method.environment.join(', ') })}</p>}
        {active && loginLinks.length > 0 && <div className="agent-auth-links">{loginLinks.map((link) => <div className="agent-auth-link" key={link.url}>
            {link.message && <p>{link.message}</p>}
            <a href={link.url} title={link.url} target="_blank" rel="noopener noreferrer"><span>{t('settings.agents.auth.openLink')}</span><span aria-hidden="true">↗</span></a>
          </div>)}</div>}
        {active && loginLinks.some((link) => hasLocalCallback(link.url)) && <p className="agent-auth-note">{t('settings.agents.auth.localCallback')}</p>}
        {(error || attempt?.error) && <pre className="agent-auth-output error" role="alert">{[error, attempt?.error].filter(Boolean).join('\n\n')}</pre>}
        {notice && <pre className="agent-auth-output">{notice}</pre>}
        {attempt?.output && <details className="agent-auth-log" open={active || attempt.status === 'error'}><summary>{t('settings.agents.auth.output')}</summary><AuthenticationOutput output={attempt.output} /></details>}
        {(needsLogin || failed) && <details className="agent-auth-help"><summary>{t('settings.agents.auth.help')}</summary>
          {method?.description && <p>{method.description}</p>}
          <p>{t('settings.agents.auth.reuse')}</p>
          {method?.type === 'agent' && <p>{t('settings.agents.auth.browser')}</p>}
          {agentId === 'agy' && <p>{t('settings.agents.auth.antigravity')}</p>}
        </details>}
        <div className="agent-auth-actions">
          {verified && info && <button type="button" className="agent-auth-recheck" onClick={() => void verify(info)}>{t('settings.agents.auth.recheck')}</button>}
          {active && <button type="button" className="settings-btn settings-btn-secondary" disabled={pending} onClick={() => void close()}>{t('settings.agents.auth.cancel')}</button>}
          {!active && <button type="button" className="settings-btn settings-btn-primary" disabled={pending} onClick={continueConnection}>
            {t(verified ? 'settings.agents.auth.done' : pending ? 'settings.agents.checkingAvailability' : needsLogin && method?.type === 'agent' ? 'settings.agents.auth.start' : needsLogin && method?.type !== 'env_var' ? 'settings.agents.auth.continue' : 'common.retry')}
          </button>}
        </div>
      </div>
    </div>
  );
}
