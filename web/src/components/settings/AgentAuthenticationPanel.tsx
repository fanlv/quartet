import { useCallback, useEffect, useId, useRef, useState } from 'react';
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
  onConfigureEnvironment?: () => void;
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

export function AgentAuthenticationPanel({ agentId, name, request, onChanged, onClose, onConfigureEnvironment }: Props) {
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
  const [notice, setNotice] = useState('');
  const baseURL = `/api/v1/agent/${encodeURIComponent(agentId)}`;
  const active = isActive(attempt);
  const attemptId = attempt?.id;
  const method = info?.methods.find((item) => item.id === selectedId);
  changedRef.current = onChanged;

  useEffect(() => {
    let disposed = false;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialogRef.current?.focus();
    request(`${baseURL}/authentication`).then((data) => {
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
      setVerified(nextAttempt?.status === 'available');
    }).catch((err: unknown) => {
      if (!disposed) setError(err instanceof Error ? err.message : String(err));
    }).finally(() => { if (!disposed) setPending(false); });
    return () => {
      disposed = true;
      previousFocus?.focus();
    };
  }, [baseURL, request]);

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

  const close = useCallback(async () => {
    if (pending && info) return;
    if (active && attempt) {
      try {
        await request(`${baseURL}/authentication/${encodeURIComponent(attempt.id)}/cancel`, { method: 'POST' });
      } catch (err) { setError(err instanceof Error ? err.message : String(err)); return; }
    }
    onClose();
  }, [pending, info, active, attempt, baseURL, request, onClose]);

  const authenticate = async () => {
    if (!info || !method || method.type !== 'agent') return;
    setPending(true); setError(''); setNotice(''); setVerified(false);
    try {
      const data = await request(`${baseURL}/authentication`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ revision: info.revision, method_id: method.id }),
      });
      setAttempt(readAttempt(data.attempt));
    } catch (err) { setError(err instanceof Error ? err.message : String(err)); }
    finally { setPending(false); }
  };

  const verify = async () => {
    if (!info) return;
    setPending(true); setError(''); setNotice(''); setAttempt(null); setVerified(false);
    try {
      const data = await request(`${baseURL}/revalidate`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ revision: info.revision }),
      });
      const validation = data.validation as { success?: boolean; error?: string; authentication_required?: boolean } | undefined;
      if (!validation || typeof validation.success !== 'boolean') throw new Error(JSON.stringify(data, null, 2));
      setVerified(validation.success);
      if (!validation.success) setError(validation.error || JSON.stringify(data, null, 2));
      if (typeof data.warning === 'string') setNotice(data.warning);
    } catch (err) { setError(err instanceof Error ? err.message : String(err)); }
    finally { setPending(false); changedRef.current(); }
  };

  return (
    <div className="agent-auth-overlay" onClick={(event) => { if (event.target === event.currentTarget) void close(); }}>
      <div className="agent-auth-panel" role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1} ref={dialogRef}
        onKeyDown={(event) => {
          if (event.key === 'Escape') { event.stopPropagation(); void close(); }
          if (event.key === 'Tab') {
            const controls = dialogRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled), select:not(:disabled), a[href]');
            const first = controls?.[0]; const last = controls?.[controls.length - 1];
            if (event.shiftKey && (document.activeElement === first || document.activeElement === dialogRef.current)) { event.preventDefault(); last?.focus(); }
            else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
          }
        }}>
        <div className="agent-auth-heading">
          <h3 id={titleId}>{t('settings.agents.auth.title', { name })}</h3>
          <button type="button" className="settings-btn settings-btn-secondary" onClick={() => void close()} disabled={pending && info !== null} aria-label={t('common.close')}>×</button>
        </div>
        <ol className="agent-auth-steps" aria-label={t('settings.agents.auth.flow')}>
          <li className="complete">{t('settings.agents.auth.install')}</li>
          <li className={verified ? 'complete' : 'current'}>{t('settings.agents.auth.login')}</li>
          <li className={verified ? 'complete' : ''}>{t('settings.agents.auth.verify')}</li>
        </ol>
        <p>{t('settings.agents.auth.reuse')}</p>
        {agentId === 'agy' && <p className="agent-auth-note">{t('settings.agents.auth.antigravity')}</p>}
        {pending && <p role="status">{t('common.loading')}</p>}
        {info && info.methods.length > 0 && <label className="agent-auth-method">
          <span>{t('settings.agents.auth.method')}</span>
          <select value={selectedId} onChange={(event) => setSelectedId(event.target.value)} disabled={pending || active}>
            {info.methods.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
        </label>}
        {info?.methods.length === 0 && <p>{t('settings.agents.auth.manual')}</p>}
        {method?.description && <p>{method.description}</p>}
        {method?.type === 'terminal' && <div className="agent-auth-guide">
          <p>{t('settings.agents.auth.terminal')}</p>
          {method.command && <pre className="agent-auth-output">{method.command}</pre>}
        </div>}
        {!!method?.environment?.length && <p className="agent-auth-note">{t('settings.agents.auth.environment', { keys: method.environment.join(', ') })}</p>}
        {method?.type === 'agent' && <p>{t('settings.agents.auth.browser')}</p>}
        {active && <p className="agent-auth-progress" role="status">{t(`settings.agents.auth.progress.${attempt?.status}`)}</p>}
        {verified && <p className="agent-auth-success" role="status">{t('settings.agents.auth.ready')}</p>}
        {attempt?.status === 'cancelled' && <p>{t('settings.agents.auth.progress.cancelled')}</p>}
        {active && attempt?.links?.map((link) => {
          let safe = false;
          try { safe = ['https:', 'http:'].includes(new URL(link.url).protocol); } catch { /* keep raw diagnostic in output */ }
          return safe ? <div className="agent-auth-link" key={link.url}>
            {link.message && <p>{link.message}</p>}
            <a href={link.url} target="_blank" rel="noopener noreferrer">{t('settings.agents.auth.openLink')}</a>
          </div> : null;
        })}
        {(error || attempt?.error) && <pre className="agent-auth-output error" role="alert">{[error, attempt?.error].filter(Boolean).join('\n\n')}</pre>}
        {notice && <pre className="agent-auth-output">{notice}</pre>}
        {attempt?.output && <details open={active || attempt.status === 'error'}><summary>{t('settings.agents.auth.output')}</summary><pre className="agent-auth-output">{attempt.output}</pre></details>}
        <div className="agent-auth-actions">
          {onConfigureEnvironment && <button type="button" className="settings-btn settings-btn-secondary" disabled={pending || active} onClick={onConfigureEnvironment}>{t('settings.agents.auth.configureEnvironment')}</button>}
          {method?.type === 'agent' && <button type="button" className="settings-btn settings-btn-primary" disabled={pending || active} onClick={() => void authenticate()}>{t('settings.agents.auth.start')}</button>}
          <button type="button" className="settings-btn settings-btn-secondary" disabled={!info || pending || active} onClick={() => void verify()}>{t('settings.agents.auth.verifyAction')}</button>
          {active && <button type="button" className="settings-btn settings-btn-secondary" disabled={pending} onClick={() => void close()}>{t('settings.agents.auth.cancel')}</button>}
        </div>
      </div>
    </div>
  );
}
