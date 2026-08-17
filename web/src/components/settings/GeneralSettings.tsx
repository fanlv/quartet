import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';

interface GeneralSettingsProps {
  onSettingsChanged?: () => void;
}

export function GeneralSettings({ onSettingsChanged }: GeneralSettingsProps) {
  const { t, i18n } = useTranslation();
  const [username, setUsername] = useState('User');
  const [avatarUrl, setAvatarUrl] = useState('');
  const [graphEndHookScript, setGraphEndHookScript] = useState('');
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
        setUsername(data.settings.username || 'User');
        setAvatarUrl(data.settings.avatar_url || '');
        setGraphEndHookScript(data.settings.graph_end_hook_script || '');
      }
    } catch (err) {
      console.error('Failed to load settings:', err);
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
          username,
          avatar_url: avatarUrl,
          graph_end_hook_script: graphEndHookScript,
        }),
      });
      const data = await res.json();
      if (data.code !== 0) {
        setMessage({ type: 'error', text: data.msg || t('common.saveFailed') });
        return;
      }

      setMessage({ type: 'success', text: t('common.saveSuccess') });
      onSettingsChanged?.();
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
    <div className="account-settings" data-testid="general-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.general.sectionTitle')}</h3>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.general.language')}</label>
          <select
            className="settings-input"
            value={i18n.language.startsWith('zh') ? 'zh' : 'en'}
            onChange={(e) => i18n.changeLanguage(e.target.value)}
            data-testid="settings-language-select"
          >
            <option value="en">English</option>
            <option value="zh">中文</option>
          </select>
          <span className="settings-switch-desc">
            {t('settings.general.languageDesc')}
          </span>
        </div>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.general.username')}</label>
          <input
            type="text"
            className="settings-input"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="User"
          />
          <span className="settings-switch-desc">
            {t('settings.general.usernameDesc')}
          </span>
        </div>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.general.avatar')}</label>
          <input
            type="text"
            className="settings-input"
            value={avatarUrl}
            onChange={(e) => setAvatarUrl(e.target.value)}
            placeholder="https://example.com/avatar.png"
          />
          <span className="settings-switch-desc">
            {t('settings.general.avatarDesc')}
          </span>
        </div>

        <div className="settings-form-group">
          <label className="settings-label">{t('settings.general.graphEndHookScript')}</label>
          <textarea
            className="settings-input"
            value={graphEndHookScript}
            onChange={(e) => setGraphEndHookScript(e.target.value)}
            placeholder={t('settings.general.graphEndHookScriptPlaceholder')}
            rows={6}
            style={{ fontFamily: 'monospace', resize: 'vertical' }}
          />
          <span className="settings-switch-desc">
            {t('settings.general.graphEndHookScriptDesc')}
          </span>
        </div>
      </section>

      <section className="settings-section">
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
            data-testid="settings-save-button"
          >
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </section>
    </div>
  );
}
