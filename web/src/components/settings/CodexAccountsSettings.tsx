import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { codexRequest, fetchCodexAccounts, fetchCodexAccountUsage, fetchSavedCodexAccountUsage, mutateCodexAccount, newCodexOperationID, redeemCodexReset } from '../../api/codexAccounts';
import type { CodexAccount, CodexAccountsResponse, CodexAccountUsageResponse, CodexLogin, CodexResetCard } from '../../api/codexAccounts';
import { codexPlanLabel, notifyCodexAccountChanged, notifyCodexUsageChanged } from '../../utils/agentUsage';
import { copyToClipboard } from '../../utils/clipboard';
import { CodexAccountQuota, CodexAccountUsagePanel } from './CodexAccountUsagePanel';
import './CodexAccountsSettings.css';

const baseKey = 'settings.codexAccounts';

interface AccountForm {
  mode: 'import' | 'capture' | 'update' | 'login';
  account?: CodexAccount;
  name: string;
  note: string;
  auth?: File;
  config?: File;
  removeConfig: boolean;
}

interface AccountUsageState {
  key: string;
  snapshot?: CodexAccountUsageResponse;
  error?: string;
}

interface ResetIntent {
  card: CodexResetCard;
  revision: string;
  accountName: string;
  workspaceId?: string;
  operationId: string;
}

type AccountConfirmation = { action: 'restore' } | { action: 'delete'; account: CodexAccount };

function AccountErrorDetails({ detail, summary }: { detail: string; summary: string }) {
  const { t } = useTranslation();
  const [copyError, setCopyError] = useState('');
  return <details className="codex-account-error">
    <summary>{summary}</summary>
    <pre>{detail}</pre>
    <button type="button" onClick={() => {
      setCopyError('');
      void copyToClipboard(detail).catch((err) => setCopyError(String(err)));
    }}>{t(`${baseKey}.copyError`)}</button>
    {copyError && <pre role="alert">{copyError}</pre>}
  </details>;
}

interface CodexAccountRowProps {
  account: CodexAccount;
  name: string;
  current?: CodexAccountsResponse['current'];
  revision: string;
  refresh: number;
  showWorkspace: boolean;
  disabled: boolean;
  action?: ReactNode;
  children: ReactNode;
  formatTime: (unix: number) => string;
  onRedeem?: (card: CodexResetCard) => void;
}

function CodexAccountRow({ account, name, current, revision, refresh, showWorkspace, disabled, action, children, formatTime, onRedeem }: CodexAccountRowProps) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [usage, setUsage] = useState<AccountUsageState | null>(null);
  const detailsID = useId();
  const accountID = account.id;
  const isCurrent = !!current;
  const highlighted = isCurrent && !current.blocked_reason;
  const requestKey = account.auth_type === 'chatgpt' && !current?.blocked_reason
    ? `${isCurrent ? 'current' : accountID}/${revision}/${refresh}` : undefined;
  const shownUsage = usage?.key === requestKey ? usage : null;
  const loading = !!requestKey && !shownUsage;

  useEffect(() => {
    if (!requestKey) return;
    const controller = new AbortController();
    const request = isCurrent ? fetchCodexAccountUsage(revision, controller.signal)
      : fetchSavedCodexAccountUsage(accountID, revision, controller.signal);
    void request.then((snapshot) => {
      if (!controller.signal.aborted) setUsage({ key: requestKey, snapshot });
    }).catch((err) => {
      if (!controller.signal.aborted) setUsage({ key: requestKey, error: String(err) });
    });
    return () => controller.abort();
  }, [accountID, isCurrent, requestKey, revision]);

  const snapshot = shownUsage?.snapshot;
  const email = account.email?.toLowerCase() === name.toLowerCase() ? undefined : account.email;
  const plan = codexPlanLabel(snapshot?.usage.plan_type, t);
  const subtitle = [email, plan || (account.auth_type === 'api_key' ? t(`${baseKey}.apiKey`) : undefined)].filter(Boolean).join(' · ');
  const quotaStatus = loading ? t('agentUsage.loading') : t(`${baseKey}.${current?.blocked_reason ? 'usageBlocked'
    : account.auth_type === 'api_key' ? 'usageAPIKey' : 'quotaError'}`);

  return <li className="codex-account-item" data-current={highlighted || undefined}>
    <div className="codex-account-row-main">
      <div className="codex-account-identity">
        <div className="codex-account-name">
          <h4>{name}</h4>
          {highlighted && <span className="codex-account-badge">{t(`${baseKey}.active`)}</span>}
        </div>
        {subtitle && <p>{subtitle}</p>}
        {showWorkspace && <code className="codex-account-detail">{account.workspace_id}</code>}
      </div>
      <div className="codex-account-quota" aria-busy={loading}>
        {snapshot ? <CodexAccountQuota usage={snapshot.usage} /> : <span className="codex-account-detail">{quotaStatus}</span>}
      </div>
      <div className="codex-account-row-actions">
        {action}
        <button type="button" className="codex-account-disclosure" aria-expanded={expanded} aria-controls={detailsID}
          aria-label={t(`${baseKey}.detailsFor`, { name })} onClick={() => setExpanded(!expanded)}>{t(`${baseKey}.accountDetails`)}</button>
      </div>
    </div>
    <div id={detailsID} className="codex-account-expanded codex-account-technical" hidden={!expanded}>
      {expanded && <>
        {shownUsage?.error && <AccountErrorDetails detail={shownUsage.error} summary={t(`${baseKey}.usageError`)} />}
        {snapshot && <CodexAccountUsagePanel snapshot={snapshot} formatTime={formatTime} disabled={disabled} onRedeem={onRedeem} />}
        {snapshot?.reset_cards_error && <AccountErrorDetails detail={snapshot.reset_cards_error} summary={t(`${baseKey}.resetDetailsUnavailable`)} />}
        {children}
      </>}
    </div>
  </li>;
}

interface AccountFilePickerProps {
  label: string;
  accept: string;
  file?: File;
  disabled: boolean;
  autoFocus?: boolean;
  onChange: (file?: File) => void;
}

function AccountFilePicker({ label, accept, file, disabled, autoFocus, onChange }: AccountFilePickerProps) {
  const { t } = useTranslation();
  const input = useRef<HTMLInputElement>(null);
  const remove = t(`${baseKey}.removeFile`);

  return <div className="codex-account-file">
    <input ref={input} type="file" accept={accept} aria-label={label} hidden disabled={disabled} onChange={(event) => {
      const next = event.currentTarget.files?.[0];
      event.currentTarget.value = '';
      if (next) onChange(next);
    }} />
    <button type="button" autoFocus={autoFocus} disabled={disabled} onClick={() => input.current?.click()}>{label}</button>
    {file && <>
      <span role="status">{file.name}</span>
      <button type="button" aria-label={`${remove} ${file.name}`} disabled={disabled} onClick={() => onChange()}>{remove}</button>
    </>}
  </div>;
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
  const [refreshing, setRefreshing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [form, setForm] = useState<AccountForm | null>(null);
  const [switching, setSwitching] = useState<CodexAccount | null>(null);
  const [applyConfig, setApplyConfig] = useState(false);
  const [forceSwitch, setForceSwitch] = useState(false);
  const [confirmation, setConfirmation] = useState<AccountConfirmation | null>(null);
  const [resetIntent, setResetIntent] = useState<ResetIntent | null>(null);
  const [resetError, setResetError] = useState('');
  const [usageRefresh, setUsageRefresh] = useState(0);
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
  const label = (key: string) => t(`${baseKey}.${key}`);
  const current = data?.current;
  const usageRevision = current?.account?.auth_type === 'chatgpt' && !current.blocked_reason ? data?.live_revision : undefined;

  const refresh = useCallback(async (signal?: AbortSignal) => {
    if (busyRef.current) return;
    const token = ++generation.current;
    setRefreshing(true);
    try {
      const next = await fetchCodexAccounts(signal);
      if (!mounted.current || signal?.aborted || token !== generation.current) return;
      setData(next);
      setError('');
      if (next.active_login) setLogin(next.active_login);
    } catch (err) {
      if (mounted.current && !signal?.aborted && token === generation.current) setError(String(err));
    } finally {
      if (mounted.current && !signal?.aborted && token === generation.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, []);

  const refreshAccountAndUsage = async () => {
    await refresh();
    if (mounted.current) setUsageRefresh((value) => value + 1);
  };

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

  const dialogOpen = !!form || !!switching || loginOpen || !!confirmation || !!resetIntent;
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
    setForm(null); setSwitching(null); setLoginOpen(false); setConfirmation(null); setResetIntent(null); setDialogError('');
  };
  const setPending = (value: boolean) => { busyRef.current = value; setBusy(value); };

  const setSelectedFile = (field: 'auth' | 'config', file?: File) => {
    setDialogError('');
    setForm((current) => current && {
      ...current,
      [field]: file,
      removeConfig: field === 'config' && file ? false : current.removeConfig,
    });
  };

  const dropFiles = (files: File[]) => {
    if (busyRef.current || !files.length) return;
    const auth = files.filter((file) => /\.json$/i.test(file.name));
    const config = files.filter((file) => /\.toml$/i.test(file.name));
    if (auth.length > 1 || config.length > 1 || auth.length + config.length !== files.length) {
      setDialogError(t(`${baseKey}.dropError`, { files: files.map((file) => file.name).join(', ') }));
      return;
    }
    setDialogError('');
    setForm((current) => current && {
      ...current,
      auth: auth[0] ?? current.auth,
      config: config[0] ?? current.config,
      removeConfig: config.length ? false : current.removeConfig,
    });
  };

  const run = async (action: string, id?: string, payload: Record<string, unknown> = {}) => {
    if (!data || busyRef.current) return;
    const inDialog = dialogOpen;
    setPending(true); setError(''); setDialogError(''); setNotice(''); setResetError(''); generation.current++;
    try {
      const result = await mutateCodexAccount(inDialog ? intentState.current || data : data, action, id, payload);
      if (!mounted.current) return;
      setData(result); setForm(null); setSwitching(null); setConfirmation(null);
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

  const startConfirmation = (next: AccountConfirmation) => {
    intentState.current = data;
    setDialogError('');
    setConfirmation(next);
  };

  const confirmReset = async () => {
    if (!resetIntent || busyRef.current) return;
    if (usageRevision !== resetIntent.revision) {
      setDialogError(label('resetAccountChanged'));
      return;
    }
    setPending(true);
    generation.current++;
    setNotice('');
    setResetError('');
    try {
      const result = await redeemCodexReset(resetIntent.revision, resetIntent.card.id, resetIntent.operationId);
      if (result.outcome === 'reset') notifyCodexUsageChanged();
      if (!mounted.current) return;
      switch (result.outcome) {
        case 'reset':
          setNotice(result.windows_reset > 0
            ? t(`${baseKey}.resetSuccessWindows`, { count: result.windows_reset })
            : label('resetSuccess'));
          break;
        case 'nothing_to_reset':
        case 'no_credit':
        case 'credit_unavailable':
        case 'already_redeemed':
          setNotice(label(`resetOutcome.${result.outcome}`));
          break;
        default:
          setResetError(t(`${baseKey}.resetUnknown`, { outcome: result.outcome }));
      }
    } catch (err) {
      if (mounted.current) setResetError(String(err));
    } finally {
      setPending(false);
      if (mounted.current) {
        setResetIntent(null);
        void refreshAccountAndUsage();
      }
    }
  };

  const importing = form?.mode === 'import';
  const canSave = importing ? !!form.auth : !!form?.name.trim();

  const submit = async () => {
    if (!form || !canSave || busyRef.current) return;
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
  const usageTime = (unix: number) => new Date(unix * 1000).toLocaleString(i18n.language, { dateStyle: 'medium', timeStyle: 'short' });
  const accountName = (account?: CodexAccount) => account?.name || account?.email || label(account?.auth_type === 'api_key' ? 'apiKey' : 'localAccount');
  const openSwitch = (account: CodexAccount, apply: boolean) => {
    intentState.current = data;
    setSwitching(account);
    setApplyConfig(apply);
    setForceSwitch(false);
    setDialogError('');
  };
  const appliesCurrentConfig = !!switching && switching.id === intentState.current?.current.managed_id;
  const dialogLabel = form?.mode || (resetIntent ? 'useReset'
    : confirmation ? confirmation.action
      : switching ? (appliesCurrentConfig ? 'apply' : 'activate') : 'login');
  const showFiles = importing || form?.mode === 'update';
  const filePickerLabel = (file: string, requirement: string) => `${t(`${baseKey}.chooseFile`, { file })} ${label(requirement)}`;
  const accountDetails = form && <>
    <label>
      {label('name')}{importing && ` ${label('optional')}`}
      <input autoFocus={!importing} required={!importing} value={form.name} disabled={busy}
        placeholder={importing ? label('defaultNameHint') : undefined}
        onChange={(event) => setForm({ ...form, name: event.target.value })} />
    </label>
    <label>{label('note')}<textarea value={form.note} disabled={busy} onChange={(event) => setForm({ ...form, note: event.target.value })} /></label>
  </>;

  const savedAccounts = [...(data?.accounts ?? [])].sort((left, right) =>
    Number(right.id === current?.managed_id) - Number(left.id === current?.managed_id));
  const localAccount = current?.managed_id ? undefined : current?.account;
  const currentErrors = [current?.error, current?.blocked_reason, current?.validation_error].filter(Boolean).join('\n\n');
  const currentDetails = current && <>
    {current.account && current.status !== 'managed' && <p>{label(`currentStatus.${current.status}`)}</p>}
    <p>{label('availability')} · {label(`availabilityStatus.${current.availability}`)}</p>
    <code className="codex-account-path">{current.home}</code>
    <p>auth.json · {label(current.has_auth ? 'present' : 'missing')}</p>
    <p>config.toml · {label(current.has_config ? 'present' : 'missing')}</p>
    {!!current.validated_at && <p>{label('validatedAt')} · {time(current.validated_at)}</p>}
    <p>{label('scopeHint')}</p>
    {currentErrors && <AccountErrorDetails detail={currentErrors} summary={label('statusError')} />}
  </>;

  const renderAccount = (account: CodexAccount, saved: boolean) => {
    if (!data || !current) return null;
    const active = !saved || current.managed_id === account.id;
    const name = accountName(account);
    const showWorkspace = !!account.workspace_id && savedAccounts.some((item) =>
      item.id !== account.id && accountName(item) === name && item.email === account.email);
    const action = !saved ? <button type="button" disabled={busy} aria-label={label('capture')}
      onClick={() => openForm('capture')}>{label('save')}</button> : !active ? <button type="button" disabled={busy}
      aria-label={t(`${baseKey}.switchTo`, { name })} onClick={() => openSwitch(account, false)}>{label('switch')}</button> : undefined;

    return <CodexAccountRow key={saved ? account.id : 'local'} account={account} name={name} current={active ? current : undefined}
      revision={active ? data.live_revision : data.revision} refresh={usageRefresh} showWorkspace={showWorkspace}
      disabled={busy || refreshing} action={action} formatTime={usageTime} onRedeem={active && usageRevision ? (card) => {
        setDialogError('');
        setResetIntent({
          card, revision: usageRevision, accountName: name,
          workspaceId: current.account?.workspace_id, operationId: newCodexOperationID(),
        });
      } : undefined}>
      {account.workspace_id && !showWorkspace && <p>{label('workspace')} · <code>{account.workspace_id}</code></p>}
      {saved && <>
        {account.note && <p className="codex-account-note">{account.note}</p>}
        <p>{label(account.has_config ? 'withConfig' : 'authOnly')}{account.model ? ` · ${account.model}` : ''}{account.provider ? ` · ${account.provider}` : ''}</p>
        <p>{label('updated')} · {time(account.updated_at)}</p>
        <p>{label('lastUsed')} · {time(account.last_used_at)}</p>
      </>}
      {active && currentDetails}
      {saved && <div className="codex-account-actions">
        {active && account.has_config && <button type="button" disabled={busy} onClick={() => openSwitch(account, true)}>{label('apply')}</button>}
        {active && <button type="button" disabled={busy} onClick={() => openForm('capture')}>{label('capture')}</button>}
        <button type="button" disabled={busy} onClick={() => openForm('update', account)}>{label('edit')}</button>
        <button type="button" className="codex-account-danger" disabled={busy}
          onClick={() => startConfirmation({ action: 'delete', account })}>{label('delete')}</button>
      </div>}
    </CodexAccountRow>;
  };

  return <section className="codex-accounts" aria-label={label('title')}>
    <div className="codex-accounts-heading">
      <div className="codex-account-toolbar">
        {data && <>
          <button type="button" className="codex-account-primary" disabled={busy}
            onClick={() => { if (login?.status === 'running') { setLoginOpen(true); setLoginPolling(true); } else openForm('login'); }}>
            {label(login?.status === 'running' ? 'resumeLogin' : 'login')}
          </button>
          <button type="button" disabled={busy} onClick={() => openForm('import')}>{label('import')}</button>
        </>}
      </div>
      <button type="button" disabled={busy || refreshing}
        onClick={() => { void refreshAccountAndUsage(); }}>{label(refreshing ? 'refreshing' : 'refresh')}</button>
    </div>
    {error && <AccountErrorDetails detail={error} summary={label('loadError')} />}
    {resetError && <AccountErrorDetails detail={resetError} summary={label('resetError')} />}
    {notice && <p className="codex-account-notice" role="status">{notice}</p>}
    {loading && <p role="status">{label('loading')}</p>}
    {data && current && <>
      {localAccount || savedAccounts.length > 0
        ? <ul className="codex-account-list" role="list">
          {localAccount && renderAccount(localAccount, false)}
          {savedAccounts.map((account) => renderAccount(account, true))}
        </ul>
        : <p className="codex-account-empty">{label('emptyTitle')}</p>}
      {(!current.account || data.has_backup) && (
        <div className="codex-account-footer">
          {!current.account && <details className="codex-account-technical">
            <summary>{label(`currentStatus.${current.status}`)}</summary>
            <div className="codex-account-technical-body">{currentDetails}</div>
          </details>}
          {data.has_backup && <button className="codex-account-restore" type="button" disabled={busy}
            onClick={() => startConfirmation({ action: 'restore' })}>{label('restore')}</button>}
        </div>
      )}
    </>}
    <dialog ref={dialog} className="codex-account-dialog" aria-labelledby={dialogTitle} onCancel={(event) => { event.preventDefault(); closeDialog(); }}>
      <div className="codex-account-dialog-heading"><h3 id={dialogTitle}>{label(dialogLabel)}</h3><button type="button" aria-label={label('close')} disabled={busy} onClick={closeDialog}>×</button></div>
      {form && <form onSubmit={(event) => { event.preventDefault(); void submit(); }}>
        {!importing && accountDetails}
        {showFiles && <>
          <div className="codex-account-drop" onDragOver={(event) => event.preventDefault()} onDrop={(event) => {
            event.preventDefault();
            dropFiles([...event.dataTransfer.files]);
          }}>
            <p>{label('dropHint')}</p>
            <AccountFilePicker
              label={filePickerLabel('auth.json', importing ? 'required' : 'keepExisting')}
              accept=".json,application/json"
              file={form.auth}
              disabled={busy}
              autoFocus={importing}
              onChange={(file) => setSelectedFile('auth', file)}
            />
            <AccountFilePicker
              label={filePickerLabel('config.toml', 'optional')}
              accept=".toml,text/plain"
              file={form.config}
              disabled={busy}
              onChange={(file) => setSelectedFile('config', file)}
            />
          </div>
          <p className="codex-account-hint">{label(form.mode === 'update' ? 'updateHint' : 'importHint')}</p>
          {form.mode === 'update' && form.account?.has_config && <label className="codex-account-check"><input type="checkbox" checked={form.removeConfig} disabled={busy || !!form.config} onChange={(event) => setForm({ ...form, removeConfig: event.target.checked })} />{label('removeConfig')}</label>}
        </>}
        {importing && accountDetails}
        {form.mode === 'capture' && <p className="codex-account-hint">{label('captureHint')}</p>}
        {form.mode === 'login' && <p className="codex-account-hint">{label('loginHint')}</p>}
        {dialogError && <pre className="codex-account-error" role="alert">{dialogError}</pre>}
        <div className="codex-account-dialog-footer"><button type="button" disabled={busy} onClick={closeDialog}>{label('close')}</button><button className="codex-account-primary" type="submit" disabled={busy || !canSave}>{label(busy ? 'working' : form.mode === 'login' ? 'startLogin' : 'save')}</button></div>
      </form>}
      {switching && <>
        <p>{t(`${baseKey}.${appliesCurrentConfig ? 'applyConfirm' : 'activateConfirm'}`, { name: switching.name })}</p>
        <p className="codex-account-hint">{label('switchHint')}</p>
        {switching.has_config && !appliesCurrentConfig && <label className="codex-account-check"><input type="checkbox" checked={applyConfig} disabled={busy} onChange={(event) => setApplyConfig(event.target.checked)} />{label('applyConfig')}</label>}
        <p className="codex-account-hint">{label(applyConfig ? 'replaceConfigHint' : 'keepConfigHint')}</p>
        <details className="codex-account-technical codex-account-advanced">
          <summary>{label('advanced')}</summary>
          <label className="codex-account-check"><input type="checkbox" checked={forceSwitch} disabled={busy}
            onChange={(event) => setForceSwitch(event.target.checked)} />{label('forceActivate')}</label>
          {forceSwitch && <p className="codex-account-hint">{label('forceActivateHint')}</p>}
        </details>
        {dialogError && <pre className="codex-account-error" role="alert">{dialogError}</pre>}
        <div className="codex-account-dialog-footer">
          <button type="button" disabled={busy} onClick={closeDialog}>{label('close')}</button>
          <button type="button" className="codex-account-primary" disabled={busy}
            onClick={() => { void run('activate', switching.id, { apply_config: applyConfig, force: forceSwitch }); }}>
            {label(busy ? 'working' : forceSwitch ? 'forceActivate' : appliesCurrentConfig ? 'apply' : 'switch')}
          </button>
        </div>
      </>}
      {confirmation && <>
        {confirmation.action === 'delete' && <strong>{accountName(confirmation.account)}</strong>}
        <p>{label(confirmation.action === 'restore' ? 'restoreConfirm'
          : intentState.current?.current.managed_id === confirmation.account.id ? 'deleteActiveConfirm' : 'deleteConfirm')}</p>
        {dialogError && <pre className="codex-account-error" role="alert">{dialogError}</pre>}
        <div className="codex-account-dialog-footer">
          <button type="button" disabled={busy} onClick={closeDialog}>{label('close')}</button>
          <button type="button" className={confirmation.action === 'delete' ? 'codex-account-danger' : 'codex-account-primary'}
            disabled={busy} onClick={() => { void run(confirmation.action, confirmation.action === 'delete' ? confirmation.account.id : undefined); }}>
            {label(busy ? 'working' : confirmation.action)}
          </button>
        </div>
      </>}
      {resetIntent && <>
        <p>{t(`${baseKey}.resetConfirm`, { name: resetIntent.accountName })}</p>
        {resetIntent.workspaceId && <p className="codex-account-detail">{label('workspace')} · <code>{resetIntent.workspaceId}</code></p>}
        {resetIntent.card.expires_at && <p className="codex-account-detail">{t('agentUsage.creditExpiry', { time: usageTime(resetIntent.card.expires_at) })}</p>}
        {dialogError && <pre className="codex-account-error" role="alert">{dialogError}</pre>}
        <div className="codex-account-dialog-footer">
          <button type="button" disabled={busy} onClick={closeDialog}>{label('close')}</button>
          <button type="button" className="codex-account-primary" disabled={busy} onClick={() => { void confirmReset(); }}>
            {label(busy ? 'working' : 'confirmReset')}
          </button>
        </div>
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
