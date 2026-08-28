import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';

interface GeneralSettingsProps {
  onSettingsChanged?: () => void;
}

// The end hook's env vars split by trigger point: what every hook gets, what only
// a graph node hook gets, and what only an interactive round gets. Variable names
// are not translatable, so only the group label goes through i18n.
const END_HOOK_ENV_GROUPS = [
  {
    labelKey: 'settings.general.endHookEnvShared',
    vars: ['$QUARTET_HOOK_SOURCE', '$QUARTET_JOB_TITLE', '$QUARTET_JOB_ID', '$QUARTET_LAST_ASSISTANT'],
  },
  {
    labelKey: 'settings.general.endHookEnvGraph',
    vars: ['$QUARTET_RUN_ID', '$QUARTET_NODE_ID', '$QUARTET_NODE_TITLE', '$QUARTET_NODE_TYPE'],
  },
  {
    labelKey: 'settings.general.endHookEnvChat',
    vars: [
      '$QUARTET_SESSION_ID',
      '$QUARTET_JOB_MODE',
      '$QUARTET_JOB_STATUS',
      '$QUARTET_RUN_OUTCOME',
      '$QUARTET_ERROR_MESSAGE',
    ],
  },
];

export function GeneralSettings({ onSettingsChanged }: GeneralSettingsProps) {
  const { t, i18n } = useTranslation();
  const [avatarUrl, setAvatarUrl] = useState('');
  const [endHookScript, setEndHookScript] = useState('');
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
        setAvatarUrl(data.settings.avatar_url || '');
        setEndHookScript(data.settings.end_hook_script || '');
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
          avatar_url: avatarUrl,
          end_hook_script: endHookScript,
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
          <label className="settings-label">{t('settings.general.endHookScript')}</label>
          <textarea
            className="settings-input"
            value={endHookScript}
            onChange={(e) => setEndHookScript(e.target.value)}
            placeholder={t('settings.general.endHookScriptPlaceholder')}
            rows={6}
            style={{ fontFamily: 'monospace', resize: 'vertical' }}
          />
          <span className="settings-switch-desc">
            {t('settings.general.endHookScriptDesc')}
          </span>
          <div className="settings-hook-doc" data-testid="end-hook-doc">
            <div className="settings-hook-doc-block">
              <span className="settings-hook-doc-title">{t('settings.general.endHookTriggers')}</span>
              <ul className="settings-hook-doc-list">
                <li>{t('settings.general.endHookTriggerGraph')}</li>
                <li>{t('settings.general.endHookTriggerChat')}</li>
              </ul>
            </div>
            <div className="settings-hook-doc-block">
              <span className="settings-hook-doc-title">{t('settings.general.endHookEnv')}</span>
              <ul className="settings-hook-doc-list">
                {END_HOOK_ENV_GROUPS.map((group) => (
                  <li key={group.labelKey}>
                    <span className="settings-hook-doc-tag">{t(group.labelKey)}</span>
                    <span className="settings-hook-doc-vars">
                      {group.vars.map((name) => (
                        <code key={name}>{name}</code>
                      ))}
                    </span>
                  </li>
                ))}
              </ul>
              <span className="settings-hook-doc-note">{t('settings.general.endHookSourceNote')}</span>
            </div>
          </div>
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
