import { useState, useEffect, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';

interface Account {
  ilink_bot_id: string;
  ilink_user_id: string;
  status?: 'online' | 'expired';
}

interface PendingContact {
  sender_id: string;
  message_id: string;
  content_hint: string;
  received_at: string;
}

type LoginPhase = 'idle' | 'waiting' | 'scaned' | 'expired' | 'confirmed' | 'error';

// loginStatusPollBudget controls the inner poll loop lifetime. The backend
// /wechat/login/status endpoint itself long-polls for up to ~90s per call,
// so we only need to re-invoke it every time the previous call returns
// `wait`. 180s total gives the user a comfortable window before the QR
// expires on the iLink side (120s nominal).
const loginStatusPollBudgetMs = 180_000;

// qrAutoRefreshMs auto-regenerates the QR image before the iLink-side 120s
// expiry so the user never has to click "扫码登录" again while the panel is
// open (doc §4.2 / §5.2). Must be strictly less than the 120s expiry, and
// also less than loginStatusPollBudgetMs (the poll budget is refreshed on
// every call to startLogin anyway, since the inner pollStatus loop is
// aborted and restarted).
const qrAutoRefreshMs = 110_000;

export function WeChatSettings() {
  const { t } = useTranslation();
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [adminIds, setAdminIds] = useState<string[]>([]);
  const [pending, setPending] = useState<PendingContact[]>([]);
  const [loading, setLoading] = useState(true);

  const [loginPhase, setLoginPhase] = useState<LoginPhase>('idle');
  const [loginMsg, setLoginMsg] = useState('');
  const [qrImg, setQrImg] = useState('');
  const qrcodeRef = useRef('');
  const pollAbortRef = useRef<AbortController | null>(null);
  const autoRefreshTimerRef = useRef<number | null>(null);
  const pollStatusErrorLoggedRef = useRef(false);

  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  const clearAutoRefreshTimer = () => {
    if (autoRefreshTimerRef.current !== null) {
      window.clearTimeout(autoRefreshTimerRef.current);
      autoRefreshTimerRef.current = null;
    }
  };

  const refreshAll = useCallback(async () => {
    try {
      const [accRes, settingsRes, pendingRes] = await Promise.all([
        fetch('/api/v1/wechat/accounts'),
        fetch('/api/v1/config/settings/get'),
        fetch('/api/v1/wechat/pending'),
      ]);
      const accData = await accRes.json();
      const settingsData = await settingsRes.json();
      const pendingData = await pendingRes.json();

      if (accData.code === 0) setAccounts(accData.accounts || []);
      if (settingsData.code === 0 && settingsData.settings) {
        setAdminIds(settingsData.settings.wechat_admin_ids || []);
      }
      if (pendingData.code === 0) setPending(pendingData.pending || []);
    } catch (err) {
      console.error('refresh wechat settings failed:', err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refreshAll();
    const timer = window.setInterval(refreshAll, 5000);
    return () => {
      window.clearInterval(timer);
      pollAbortRef.current?.abort();
      clearAutoRefreshTimer();
    };
  }, [refreshAll]);

  const pollStatus = useCallback(async () => {
    const started = Date.now();
    while (Date.now() - started < loginStatusPollBudgetMs) {
      const controller = new AbortController();
      pollAbortRef.current = controller;
      try {
        const res = await fetch(
          `/api/v1/wechat/login/status?qrcode=${encodeURIComponent(qrcodeRef.current)}`,
          { signal: controller.signal }
        );
        pollStatusErrorLoggedRef.current = false;
        const data = await res.json();
        if (data.code !== 0) {
          clearAutoRefreshTimer();
          setLoginPhase('error');
          setLoginMsg(data.msg || t('settings.wechat.loginFailed'));
          return;
        }
        switch (data.status) {
          case 'confirmed':
            clearAutoRefreshTimer();
            setLoginPhase('confirmed');
            setLoginMsg(t('settings.wechat.loginSuccess'));
            void refreshAll();
            window.setTimeout(() => setLoginPhase('idle'), 2000);
            return;
          case 'scaned':
            setLoginPhase('scaned');
            setLoginMsg(t('settings.wechat.scanned'));
            break;
          case 'expired':
            clearAutoRefreshTimer();
            setLoginPhase('expired');
            setLoginMsg(t('settings.wechat.qrExpired'));
            return;
          case 'error':
            clearAutoRefreshTimer();
            setLoginPhase('error');
            setLoginMsg(data.msg || t('settings.wechat.loginFailed'));
            return;
          case 'wait':
          default:
            break;
        }
      } catch (err) {
        if ((err as Error).name === 'AbortError') return;
        if (!pollStatusErrorLoggedRef.current) {
          pollStatusErrorLoggedRef.current = true;
          console.warn('poll status error:', err);
        }
        await new Promise((resolve) => window.setTimeout(resolve, 2000));
      }
    }
    setLoginPhase('expired');
    setLoginMsg(t('settings.wechat.loginTimeout'));
  }, [refreshAll, t]);

  const startLogin = useCallback(async () => {
    setMessage(null);
    pollAbortRef.current?.abort();
    clearAutoRefreshTimer();
    setLoginPhase('waiting');
    setLoginMsg(t('settings.wechat.gettingQrCode'));
    setQrImg('');
    pollStatusErrorLoggedRef.current = false;
    try {
      const res = await fetch('/api/v1/wechat/login/start', { method: 'POST' });
      const data = await res.json();
      if (data.code !== 0) {
        setLoginPhase('error');
        setLoginMsg(data.msg || t('settings.wechat.loginFailed'));
        return;
      }
      qrcodeRef.current = data.qrcode;
      setQrImg(`data:image/png;base64,${data.img_base64}`);
      setLoginMsg(t('settings.wechat.scanWithWeChat'));

      autoRefreshTimerRef.current = window.setTimeout(() => {
        autoRefreshTimerRef.current = null;
        void startLogin();
      }, qrAutoRefreshMs);

      void pollStatus();
    } catch {
      setLoginPhase('error');
      setLoginMsg(t('common.networkError'));
    }
  }, [pollStatus, t]);

  const cancelLogin = () => {
    pollAbortRef.current?.abort();
    clearAutoRefreshTimer();
    pollStatusErrorLoggedRef.current = false;
    setLoginPhase('idle');
    setQrImg('');
    qrcodeRef.current = '';
  };

  const logout = async (acc: Account) => {
    if (!window.confirm(t('settings.wechat.confirmLogout', { id: acc.ilink_bot_id }))) return;
    try {
      const res = await fetch('/api/v1/wechat/logout', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ilink_bot_id: acc.ilink_bot_id,
          ilink_user_id: acc.ilink_user_id,
        }),
      });
      const data = await res.json();
      if (data.code === 0) {
        setMessage({ type: 'success', text: t('settings.wechat.loggedOut') });
        void refreshAll();
      } else {
        setMessage({ type: 'error', text: data.msg || t('settings.wechat.logoutFailed') });
      }
    } catch {
      setMessage({ type: 'error', text: t('common.networkError') });
    }
  };

  const approve = async (id: string) => {
    try {
      const res = await fetch('/api/v1/wechat/admin/add', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id }),
      });
      const data = await res.json();
      if (data.code === 0) void refreshAll();
      else setMessage({ type: 'error', text: data.msg || t('settings.wechat.addFailed') });
    } catch {
      setMessage({ type: 'error', text: t('common.networkError') });
    }
  };

  const dismiss = async (senderId: string) => {
    try {
      const res = await fetch('/api/v1/wechat/pending/dismiss', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sender_id: senderId }),
      });
      const data = await res.json();
      if (data.code === 0) void refreshAll();
      else setMessage({ type: 'error', text: data.msg || t('settings.wechat.actionFailed') });
    } catch {
      setMessage({ type: 'error', text: t('common.networkError') });
    }
  };

  const removeAdmin = async (id: string) => {
    if (!window.confirm(t('settings.wechat.confirmRemoveAdmin', { id }))) return;
    try {
      const res = await fetch('/api/v1/wechat/admin/remove', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id }),
      });
      const data = await res.json();
      if (data.code === 0) void refreshAll();
      else setMessage({ type: 'error', text: data.msg || t('settings.wechat.removeFailed') });
    } catch {
      setMessage({ type: 'error', text: t('common.networkError') });
    }
  };

  if (loading) {
    return (
      <div className="account-settings">
        <p>{t('common.loading')}</p>
      </div>
    );
  }

  const primaryAccount = accounts[0];
  const loggedIn = !!primaryAccount;

  return (
    <div className="account-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.wechat.sectionTitle')}</h3>
        <p className="settings-section-desc">
          {t('settings.wechat.sectionDesc')}
        </p>

        {loggedIn ? (
          <div className="wechat-account-card">
            <div className="wechat-account-row">
              <span className="wechat-account-label">{t('settings.wechat.ilinkBotId')}</span>
              <code className="wechat-account-code">{primaryAccount.ilink_bot_id}</code>
            </div>
            <div className="wechat-account-row">
              <span className="wechat-account-label">{t('settings.wechat.ilinkUserId')}</span>
              <code className="wechat-account-code">{primaryAccount.ilink_user_id}</code>
            </div>
            <div className="wechat-account-row">
              <span className="wechat-account-label">{t('settings.wechat.status')}</span>
              {primaryAccount.status === 'expired' ? (
                <span className="wechat-account-status expired">{t('settings.wechat.expired')}</span>
              ) : (
                <span className="wechat-account-status online">{t('settings.wechat.loggedIn')}</span>
              )}
            </div>
            <div className="settings-btn-group">
              <button
                className="settings-btn settings-btn-danger"
                onClick={() => logout(primaryAccount)}
              >
                {t('settings.wechat.logout')}
              </button>
              <button
                className="settings-btn settings-btn-secondary"
                onClick={startLogin}
                disabled={loginPhase === 'waiting'}
              >
                {t('settings.wechat.reScan')}
              </button>
            </div>
          </div>
        ) : (
          <div className="wechat-account-card">
            {loginPhase === 'idle' || loginPhase === 'expired' || loginPhase === 'error' ? (
              <>
                {loginMsg && (
                  <div
                    className={`settings-message ${
                      loginPhase === 'error' ? 'error' : loginPhase === 'expired' ? 'error' : 'success'
                    }`}
                  >
                    {loginMsg}
                  </div>
                )}
                <div className="settings-btn-group">
                  <button className="settings-btn settings-btn-primary" onClick={startLogin}>
                    {t('settings.wechat.scanLogin')}
                  </button>
                </div>
              </>
            ) : (
              <div className="wechat-qr-box">
                {qrImg && <img src={qrImg} alt={t('settings.wechat.scanWithWeChat')} className="wechat-qr-img" />}
                <div className="wechat-qr-status">{loginMsg}</div>
                <div className="settings-btn-group">
                  <button className="settings-btn settings-btn-secondary" onClick={cancelLogin}>
                    {t('common.cancel')}
                  </button>
                </div>
              </div>
            )}
          </div>
        )}

        {message && (
          <div className={`settings-message ${message.type}`}>{message.text}</div>
        )}
      </section>

      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.wechat.adminWhitelist')}</h3>
        <p className="settings-section-desc">
          {t('settings.wechat.adminWhitelistDesc')}
        </p>
        {adminIds.length === 0 ? (
          <p className="wechat-empty">{t('settings.wechat.noAdmin')}</p>
        ) : (
          <ul className="wechat-admin-list">
            {adminIds.map((id) => (
              <li key={id} className="wechat-admin-item">
                <code className="wechat-account-code">{id}</code>
                {primaryAccount && primaryAccount.ilink_user_id === id ? (
                  <span className="wechat-admin-tag">{t('settings.wechat.selfTag')}</span>
                ) : (
                  <button
                    className="settings-btn settings-btn-secondary settings-btn-small"
                    onClick={() => removeAdmin(id)}
                  >
                    ×
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.wechat.pendingApproval')}</h3>
        <p className="settings-section-desc">{t('settings.wechat.pendingDesc')}</p>
        {pending.length === 0 ? (
          <p className="wechat-empty">{t('settings.wechat.noPending')}</p>
        ) : (
          <ul className="wechat-pending-list">
            {pending.map((pc) => (
              <li key={pc.sender_id} className="wechat-pending-item">
                <div className="wechat-pending-main">
                  <code className="wechat-account-code">{pc.sender_id}</code>
                  <span className="wechat-pending-hint">{pc.content_hint || t('settings.wechat.noTextContent')}</span>
                  <time className="wechat-pending-time">{new Date(pc.received_at).toLocaleString()}</time>
                </div>
                <div className="wechat-pending-actions">
                  <button
                    className="settings-btn settings-btn-primary settings-btn-small"
                    onClick={() => approve(pc.sender_id)}
                  >
                    {t('settings.wechat.addAsAdmin')}
                  </button>
                  <button
                    className="settings-btn settings-btn-secondary settings-btn-small"
                    onClick={() => dismiss(pc.sender_id)}
                  >
                    {t('settings.wechat.dismiss')}
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
