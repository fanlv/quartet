import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';

export function LarkSettings() {
  const { t } = useTranslation();
  const [appId, setAppId] = useState('');
  const [appSecret, setAppSecret] = useState('');
  const [imAdminSenderId, setImAdminSenderId] = useState('');
  const [imSophiaSenderId, setImSophiaSenderId] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  useEffect(() => {
    fetchSettings();
  }, []);

  const fetchSettings = async () => {
    try {
      const res = await fetch('/api/v1/config/settings/get');
      const data = await res.json();
      if (data.code === 0 && data.settings) {
        setAppId(data.settings.lark_app_id || '');
        setAppSecret(data.settings.lark_app_secret || '');
        setImAdminSenderId(data.settings.lark_im_admin_sender_id || '');
        setImSophiaSenderId(data.settings.lark_im_sophia_sender_id || '');
      }
    } catch (err) {
      console.error('Failed to load Lark settings:', err);
    } finally {
      setLoading(false);
    }
  };

  const handleSave = async () => {
    setSaving(true);
    setMessage(null);
    try {
      const settingsRes = await fetch('/api/v1/config/settings/get');
      const settingsData = await settingsRes.json();
      const currentSettings = settingsData.code === 0 && settingsData.settings ? settingsData.settings : {};

      const res = await fetch('/api/v1/config/settings/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...currentSettings,
          lark_app_id: appId,
          lark_app_secret: appSecret,
          lark_im_admin_sender_id: imAdminSenderId,
          lark_im_sophia_sender_id: imSophiaSenderId,
        }),
      });
      const data = await res.json();
      if (data.code === 0) {
        setMessage({ type: 'success', text: t('common.saveSuccess') });
      } else {
        setMessage({ type: 'error', text: data.msg || t('common.saveFailed') });
      }
    } catch {
      setMessage({ type: 'error', text: t('common.saveFailed') });
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <div className="account-settings"><p>{t('common.loading')}</p></div>;
  }

  return (
    <div className="account-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.lark.sectionTitle')}</h3>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.lark.appId')}</label>
          <input
            type="text"
            className="settings-input"
            value={appId}
            onChange={(e) => setAppId(e.target.value)}
            placeholder={t('settings.lark.appIdPlaceholder')}
          />
          <span className="settings-switch-desc">
            {t('settings.lark.appIdDesc')}
          </span>
        </div>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.lark.appSecret')}</label>
          <input
            type="password"
            className="settings-input"
            value={appSecret}
            onChange={(e) => setAppSecret(e.target.value)}
            placeholder={t('settings.lark.appSecretPlaceholder')}
          />
          <span className="settings-switch-desc">
            {t('settings.lark.appSecretDesc')}
          </span>
        </div>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.lark.imAdminId')}</label>
          <input
            type="text"
            className="settings-input"
            value={imAdminSenderId}
            onChange={(e) => setImAdminSenderId(e.target.value)}
            placeholder={t('settings.lark.imAdminIdPlaceholder')}
          />
          <span className="settings-switch-desc">
            {t('settings.lark.imAdminIdDesc')}
          </span>
        </div>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.lark.groupBotId')}</label>
          <input
            type="text"
            className="settings-input"
            value={imSophiaSenderId}
            onChange={(e) => setImSophiaSenderId(e.target.value)}
            placeholder={t('settings.lark.groupBotIdPlaceholder')}
          />
          <span className="settings-switch-desc">
            {t('settings.lark.groupBotIdDesc')}
          </span>
        </div>

        {message && (
          <div className={`settings-message ${message.type}`}>
            {message.text}
          </div>
        )}

        <div className="settings-btn-group">
          <button
            className="settings-btn settings-btn-primary"
            onClick={handleSave}
            disabled={saving}
          >
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </section>
    </div>
  );
}
