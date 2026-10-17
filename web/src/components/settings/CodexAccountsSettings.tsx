import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { codexRequest, fetchCodexAccounts, mutateCodexAccount, newCodexOperationID } from '../../api/codexAccounts';
import type { CodexAccount, CodexAccountsResponse, CodexLogin } from '../../api/codexAccounts';
import { notifyCodexAccountChanged } from '../../utils/agentUsage';
import './CodexAccountsSettings.css';

interface AccountForm {
  mode: 'import' | 'capture' | 'update' | 'login';
  account?: CodexAccount;
  name: string;
  note: string;
  auth?: File;
  config?: File;
  removeConfig: boolean;
}

function LoginOutput({ text }: { text: string }) {
  const plain = text.split(String.fromCharCode(27)).map((part) => part.replace(/^\[[0-9;]*m/, '')).join('');
  return <pre className="codex-login-output">{plain.split(/(https?:\/\/[^\s<>"']+)/g).map((part, index) => {
    if (/^https?:\/\//.test(part)) return <a key={index} href={part} target="_blank" rel="noopener noreferrer">{part}</a>;
    return part;
  })}</pre>;
}

export function CodexAccountsSettings({ onChanged }: { onChanged?: () => void }) {
  const { t, i18n } = useTranslation();
  const [data, setData] = useState<CodexAccountsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [form, setForm] = useState<AccountForm | null>(null);
  const [switching, setSwitching] = useState<CodexAccount | null>(null);
  const [applyConfig, setApplyConfig] = useState(false);
  const [login, setLogin] = useState<CodexLogin | null>(null);
  const [loginOpen, setLoginOpen] = useState(false);
  const [loginError, setLoginError] = useState('');
  const [loginPolling, setLoginPolling] = useState(true);
  const mounted = useRef(false);
  const busyRef = useRef(false);
  const generation = useRef(0);
  const dialog = useRef<HTMLDialogElement>(null);
  const dialogTitle = useId();
  const previousFocus = useRef<HTMLElement | null>(null);
  const intentState = useRef<CodexAccountsResponse | null>(null);
  const [dialogError, setDialogError] = useState('');
  const baseKey = 'settings.codexAccounts';
  const label = (key: string) => t(`${baseKey}.${key}`);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    if (busyRef.current) return;
    const token = ++generation.current;
    try {
      const next = await fetchCodexAccounts(signal);
      if (!mounted.current || signal?.aborted || token !== generation.current) return;
      setData(next);
      if (next.active_login) setLogin(next.active_login);
      setLoading(false);
    } catch (err) {
      if (mounted.current && !signal?.aborted && token === generation.current) { setError(String(err)); setLoading(false); }
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    const controller = new AbortController();
    void refresh(controller.signal);
    const onFocus = () => { if (document.visibilityState === 'visible') void refresh(); };
    window.addEventListener('focus', onFocus);
    document.addEventListener('visibilitychange', onFocus);
    return () => {
      mounted.current = false;
      controller.abort();
      window.removeEventListener('focus', onFocus);
      document.removeEventListener('visibilitychange', onFocus);
    };
  }, [refresh]);

  const dialogOpen = !!form || !!switching || loginOpen;
  useEffect(() => {
    if (dialogOpen && !dialog.current?.open) {
      previousFocus.current = document.activeElement as HTMLElement | null;
      dialog.current?.showModal();
    } else if (!dialogOpen && dialog.current?.open) {
      dialog.current?.close();
      previousFocus.current?.focus();
    }
  }, [dialogOpen]);

  useEffect(() => {
    if (!data || !['pending_validation', 'validating'].includes(data.current.availability)) return;
    const timer = window.setTimeout(() => { void refresh(); }, 2500);
    return () => window.clearTimeout(timer);
  }, [data, refresh]);

  const loginID = login?.id;
  const loginStatus = login?.status;
  useEffect(() => {
    if (!loginID || loginStatus !== 'running' || !loginPolling) return;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const result = await codexRequest<{ login: CodexLogin }>(`/login/${encodeURIComponent(loginID)}`);
        if (stopped || !mounted.current) return;
        setLogin(result.login);
        if (result.login.status === 'saved') { setNotice(t('settings.codexAccounts.loginSaved')); void refresh(); }
        if (result.login.status === 'running') timer = setTimeout(poll, 1200);
      } catch (err) {
        if (!stopped && mounted.current) { setLoginError(String(err)); setLoginPolling(false); }
      }
    };
    timer = setTimeout(poll, 800);
    return () => { stopped = true; clearTimeout(timer); };
  }, [loginID, loginStatus, loginPolling, refresh, t]);

  const openForm = (mode: AccountForm['mode'], account?: CodexAccount) => {
    intentState.current = data;
    setDialogError(''); setError(''); setNotice('');
    setForm({ mode, account, name: account?.name || '', note: account?.note || '', removeConfig: false });
  };
  const closeDialog = () => {
    if (busyRef.current) return;
    setForm(null); setSwitching(null); setLoginOpen(false); setDialogError('');
  };
  const setPending = (value: boolean) => { busyRef.current = value; setBusy(value); };

  const run = async (action: string, id?: string, payload: Record<string, unknown> = {}) => {
    if (!data || busyRef.current) return;
    const inDialog = dialogOpen;
    setPending(true); setError(''); setDialogError(''); setNotice(''); generation.current++;
    try {
      const result = await mutateCodexAccount(inDialog ? intentState.current || data : data, action, id, payload);
      if (!mounted.current) return;
      setData(result); setForm(null); setSwitching(null);
      setNotice(result.warning || label(action === 'activate' || action === 'restore' ? 'applied' : 'saved'));
      if (action === 'activate' || action === 'restore' || action === 'update' && data.current.managed_id === id && ('auth_json' in payload || 'config_toml' in payload || payload.remove_config)) {
        notifyCodexAccountChanged(); onChanged?.();
      }
    } catch (err) {
      if (mounted.current) {
        if (inDialog) setDialogError(String(err)); else setError(String(err));
      }
    } finally {
      setPending(false);
      if (mounted.current) void refresh();
    }
  };

  const submit = async () => {
    if (!form || busyRef.current) return;
    if (form.mode === 'login') {
      setPending(true); setDialogError('');
      try {
        const result = await codexRequest<{ login: CodexLogin }>('/login', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: form.name, note: form.note, operation_id: newCodexOperationID() }),
        });
        if (mounted.current) { setForm(null); setLogin(result.login); setLoginOpen(true); setLoginError(''); setLoginPolling(true); }
      } catch (err) { if (mounted.current) setDialogError(String(err)); }
      finally { setPending(false); }
      return;
    }
    // File reads stay in memory, are cleared when the form closes, and are
    // never sent through Quartet's ordinary attachment/file-share routes.
    setPending(true);
    try {
      const payload: Record<string, unknown> = { name: form.name, note: form.note, remove_config: form.removeConfig };
      if (form.auth) payload.auth_json = await form.auth.text();
      if (form.config) payload.config_toml = await form.config.text();
      setPending(false);
      await run(form.mode, form.account?.id, payload);
    } catch (err) { setPending(false); setDialogError(String(err)); }
  };

  const cancelLogin = async () => {
    if (!login || busyRef.current) return;
    setPending(true);
    try {
      await codexRequest(`/login/${encodeURIComponent(login.id)}/cancel`, { method: 'POST' });
      if (mounted.current) { setLoginPolling(true); setLoginError(''); }
    } catch (err) { if (mounted.current) setLoginError(String(err)); }
    finally { setPending(false); }
  };

  const time = (value?: number) => value ? new Date(value).toLocaleString(i18n.language) : label('never');
  const showFiles = form?.mode === 'import' || form?.mode === 'update';
  const canSave = !!form?.name.trim() && (form.mode !== 'import' || !!form.auth);

  return <section className="codex-accounts" aria-label={label('title')}>
    <div className="codex-accounts-heading">
      <div><h3>{label('title')}</h3><p>{label('description')}</p></div>
      <button type="button" disabled={busy} onClick={() => { setError(''); void refresh(); }}>{label('refresh')}</button>
    </div>
    {error && <div className="codex-account-error" role="alert"><pre>{error}</pre><button onClick={() => { void navigator.clipboard.writeText(error).catch((err) => setError(`${error}\n${String(err)}`)); }}>{label('copyError')}</button></div>}
    {notice && <p className="codex-account-notice" role="status">{notice}</p>}
    {loading && <p role="status">{label('loading')}</p>}
    {data && <>
      <div className="codex-current-account">
        <div className="codex-account-row"><strong>{label('current')}</strong><span className="codex-account-badge">{label(`currentStatus.${data.current.status}`)}</span></div>
        <h4>{data.current.account?.name || data.current.account?.email || label('localAccount')}</h4>
        {data.current.account?.email && <p>{data.current.account.email}</p>}
        <code className="codex-account-path">{data.current.home}</code>
        <p className="codex-account-files">auth.json: {label(data.current.has_auth ? 'present' : 'missing')} · config.toml: {label(data.current.has_config ? 'present' : 'missing')}</p>
        <p>{label('availability')}: {label(`availabilityStatus.${data.current.availability}`)}{data.current.validated_at ? ` · ${time(data.current.validated_at)}` : ''}</p>
        {data.current.error && <pre className="codex-account-error" role="alert">{data.current.error}</pre>}
        {data.current.blocked_reason && <pre className="codex-account-error" role="alert">{data.current.blocked_reason}</pre>}
        {data.current.validation_error && <pre className="codex-account-error" role="alert">{data.current.validation_error}</pre>}
        <p className="codex-account-hint">{label('scopeHint')}</p>
      </div>
      <div className="codex-account-toolbar">
        <button className="codex-account-primary" disabled={busy} onClick={() => openForm('import')}>{label('import')}</button>
        <button disabled={busy} onClick={() => { if (login?.status === 'running') { setLoginOpen(true); setLoginPolling(true); } else openForm('login'); }}>{label(login?.status === 'running' ? 'resumeLogin' : 'login')}</button>
        <button disabled={busy || !data.current.has_auth} onClick={() => openForm('capture')}>{label('capture')}</button>
        {data.has_backup && <button disabled={busy} onClick={() => { if (window.confirm(label('restoreConfirm'))) void run('restore'); }}>{label('restore')}</button>}
      </div>
      {!data.accounts.length ? <div className="codex-account-empty"><strong>{label('emptyTitle')}</strong><p>{label('emptyHint')}</p></div> : <div className="codex-account-list">
        {data.accounts.map((account) => <article className="codex-account-item" key={account.id}>
          <div className="codex-account-row"><h4>{account.name}</h4>{data.current.managed_id === account.id && <span className="codex-account-badge">{label('active')}</span>}</div>
          <p>{account.email || label(account.auth_type === 'api_key' ? 'apiKey' : 'chatgpt')}</p>
          {account.workspace_id && <p className="codex-account-detail">{label('workspace')}: <code>{account.workspace_id}</code></p>}
          {account.note && <p className="codex-account-note">{account.note}</p>}
          <p className="codex-account-detail">{label(account.has_config ? 'withConfig' : 'authOnly')}{account.model ? ` · ${account.model}` : ''}{account.provider ? ` · ${account.provider}` : ''}</p>
          <p className="codex-account-detail">{label('updated')}: {time(account.updated_at)} · {label('lastUsed')}: {time(account.last_used_at)}</p>
          <div className="codex-account-actions">
            {(data.current.managed_id !== account.id || account.has_config) && <button className="codex-account-primary" disabled={busy} onClick={() => { intentState.current = data; setSwitching(account); setApplyConfig(data.current.managed_id === account.id); setDialogError(''); }}>{label(data.current.managed_id === account.id ? 'apply' : 'activate')}</button>}
            <button disabled={busy} onClick={() => openForm('update', account)}>{label('edit')}</button>
            <button className="codex-account-danger" disabled={busy} onClick={() => { if (window.confirm(label(data.current.managed_id === account.id ? 'deleteActiveConfirm' : 'deleteConfirm'))) void run('delete', account.id); }}>{label('delete')}</button>
          </div>
        </article>)}
      </div>}
    </>}
    <dialog ref={dialog} className="codex-account-dialog" aria-labelledby={dialogTitle} onCancel={(event) => { event.preventDefault(); closeDialog(); }}>
      <div className="codex-account-dialog-heading"><h3 id={dialogTitle}>{label(form?.mode || (switching ? 'activate' : 'login'))}</h3><button type="button" aria-label={label('close')} disabled={busy} onClick={closeDialog}>×</button></div>
      {form && <form onSubmit={(event) => { event.preventDefault(); void submit(); }}>
        <label>{label('name')}<input autoFocus required value={form.name} disabled={busy} onChange={(event) => setForm({ ...form, name: event.target.value })} /></label>
        <label>{label('note')}<textarea value={form.note} disabled={busy} onChange={(event) => setForm({ ...form, note: event.target.value })} /></label>
        {showFiles && <>
          <div className="codex-account-drop" onDragOver={(event) => event.preventDefault()} onDrop={(event) => {
            event.preventDefault(); if (busy) return;
            const files = [...event.dataTransfer.files];
            setForm({ ...form, auth: files.find((file) => /\.json$/i.test(file.name)) || form.auth, config: files.find((file) => /\.toml$/i.test(file.name)) || form.config, removeConfig: false });
          }}>
            <p>{label('dropHint')}</p>
            <label>auth.json {form.mode === 'import' ? label('required') : label('keepExisting')}<input type="file" accept=".json,application/json" disabled={busy} onChange={(event) => setForm({ ...form, auth: event.target.files?.[0] })} /></label>
            <label>config.toml {label('optional')}<input type="file" accept=".toml,text/plain" disabled={busy} onChange={(event) => setForm({ ...form, config: event.target.files?.[0], removeConfig: false })} /></label>
          </div>
          <p className="codex-account-hint">{label(form.mode === 'update' ? 'updateHint' : 'importHint')}</p>
          {form.mode === 'update' && form.account?.has_config && <label className="codex-account-check"><input type="checkbox" checked={form.removeConfig} disabled={busy || !!form.config} onChange={(event) => setForm({ ...form, removeConfig: event.target.checked })} />{label('removeConfig')}</label>}
        </>}
        {form.mode === 'capture' && <p className="codex-account-hint">{label('captureHint')}</p>}
        {form.mode === 'login' && <p className="codex-account-hint">{label('loginHint')}</p>}
        {dialogError && <pre className="codex-account-error" role="alert">{dialogError}</pre>}
        <div className="codex-account-dialog-footer"><button type="button" disabled={busy} onClick={closeDialog}>{label('close')}</button><button className="codex-account-primary" type="submit" disabled={busy || !canSave}>{label(busy ? 'working' : form.mode === 'login' ? 'startLogin' : 'save')}</button></div>
      </form>}
      {switching && <>
        <p>{t(`${baseKey}.activateConfirm`, { name: switching.name })}</p>
        <p className="codex-account-hint">{label('switchHint')}</p>
        {switching.has_config && <label className="codex-account-check"><input type="checkbox" checked={applyConfig} disabled={busy} onChange={(event) => setApplyConfig(event.target.checked)} />{label('applyConfig')}</label>}
        <p className="codex-account-hint">{label(applyConfig ? 'replaceConfigHint' : 'keepConfigHint')}</p>
        {dialogError && <pre className="codex-account-error" role="alert">{dialogError}</pre>}
        <div className="codex-account-dialog-footer"><button disabled={busy} onClick={closeDialog}>{label('close')}</button><button className="codex-account-primary" disabled={busy} onClick={() => { void run('activate', switching.id, { apply_config: applyConfig }); }}>{label(busy ? 'working' : 'activate')}</button></div>
      </>}
      {loginOpen && login && <>
        <p>{label(`loginStatus.${login.status}`)}</p>
        <LoginOutput text={login.output} />
        {login.error && <pre className="codex-account-error" role="alert">{login.error}</pre>}
        {loginError && <pre className="codex-account-error" role="alert">{loginError}</pre>}
        <div className="codex-account-dialog-footer">
          {login.status === 'running' && <button disabled={busy} onClick={() => { void cancelLogin(); }}>{label('cancelLogin')}</button>}
          {login.status === 'running' && !loginPolling && <button onClick={() => { setLoginError(''); setLoginPolling(true); }}>{label('retry')}</button>}
          <button disabled={busy} onClick={closeDialog}>{label('close')}</button>
        </div>
      </>}
    </dialog>
  </section>;
}
