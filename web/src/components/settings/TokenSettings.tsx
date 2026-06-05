import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

const AUTH_TOKEN_STORAGE_KEY = 'quartet.x_auth_token';

export function TokenSettings() {
  const { t } = useTranslation();
  const [token, setToken] = useState(() => localStorage.getItem(AUTH_TOKEN_STORAGE_KEY) ?? '');
  const [showToken, setShowToken] = useState(false);
  const [lastSavedAt, setLastSavedAt] = useState<number | null>(null);

  const trimmed = useMemo(() => token.trim(), [token]);
  const statusText = useMemo(() => {
    if (!trimmed) return t('settings.token.statusNotSet');
    return t('settings.token.statusSet', { length: trimmed.length });
  }, [trimmed, t]);

  const handleSave = () => {
    if (trimmed) {
      localStorage.setItem(AUTH_TOKEN_STORAGE_KEY, trimmed);
    } else {
      localStorage.removeItem(AUTH_TOKEN_STORAGE_KEY);
    }
    setLastSavedAt(Date.now());
  };

  const handleClear = () => {
    setToken('');
    localStorage.removeItem(AUTH_TOKEN_STORAGE_KEY);
    setLastSavedAt(Date.now());
  };

  return (
    <div className="account-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.token.sectionTitle')}</h3>
        <p className="settings-section-desc">
          {t('settings.token.sectionDesc')}
        </p>

        <div className="settings-form-group">
          <label className="settings-label">Token</label>
          <input
            type={showToken ? 'text' : 'password'}
            className="settings-input"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={t('settings.token.placeholder')}
            autoComplete="off"
            spellCheck={false}
          />
        </div>

        <div className="settings-btn-group">
          <button className="settings-btn settings-btn-primary" onClick={handleSave}>
            {t('common.save')}
          </button>
          <button
            className="settings-btn settings-btn-secondary"
            onClick={() => setShowToken((v) => !v)}
          >
            {showToken ? t('settings.token.hide') : t('settings.token.show')}
          </button>
          <button className="settings-btn settings-btn-danger" onClick={handleClear}>
            {t('settings.token.clear')}
          </button>
        </div>

        <p className="settings-section-desc settings-section-desc-after-actions">{statusText}</p>
        {lastSavedAt && (
          <p className="settings-section-desc settings-section-desc-after-actions">{t('settings.token.lastSaved')}{new Date(lastSavedAt).toLocaleString()}</p>
        )}
      </section>
    </div>
  );
}
