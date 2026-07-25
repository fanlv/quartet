import { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import './EinoSettings.css';

interface EinoModel {
  id: string;
  model_class: string;
  display_name: string;
  connection: { api_key?: string; base_url?: string; model: string };
  thinking_type?: string;
  created_at?: number;
  updated_at?: number;
}

const API_BASE = '/api/v1/config/eino';

const MODEL_CLASSES = ['ark', 'openai', 'claude', 'deepseek', 'gemini', 'ollama', 'qwen'];

// ark 有默认接入点，其余类展示 Base URL 输入框
const HIDE_BASE_URL = ['ark'];

const THINKING_TYPE_OPTIONS = [
  { value: 'auto', labelKey: 'common.auto' },
  { value: 'enable', labelKey: 'common.enabled' },
  { value: 'disable', labelKey: 'common.disabled' },
];

const PROVIDER_EXAMPLES: Record<string, { displayName: string; model: string }> = {
  ark: { displayName: 'Doubao Seed 1.6', model: 'doubao-seed-1-6-250615' },
  openai: { displayName: 'GPT-4o', model: 'gpt-4o' },
  claude: { displayName: 'Claude 3.5 Sonnet', model: 'claude-3-5-sonnet-20241022' },
  deepseek: { displayName: 'DeepSeek V3', model: 'deepseek-chat' },
  gemini: { displayName: 'Gemini 2.0 Flash', model: 'gemini-2.0-flash-exp' },
  ollama: { displayName: 'Qwen 2.5 7B', model: 'qwen2.5:7b' },
  qwen: { displayName: 'Qwen Max', model: 'qwen-max' },
};

const EMPTY_FORM = {
  modelClass: 'ark',
  displayName: '',
  model: '',
  apiKey: '',
  baseUrl: '',
  thinkingType: 'auto',
};

export function EinoSettings() {
  const { t } = useTranslation();
  const [models, setModels] = useState<EinoModel[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [formData, setFormData] = useState({ ...EMPTY_FORM });
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  const [systemPrompt, setSystemPrompt] = useState('');
  const [promptSaving, setPromptSaving] = useState(false);
  const [promptMessage, setPromptMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  const loadModels = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const resp = await fetch(`${API_BASE}/model/list`);
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('common.loadFailed'));
      }
      setModels(data.models || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.model.loadModelListFailed'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  const loadSystemPrompt = useCallback(async () => {
    try {
      const resp = await fetch(`${API_BASE}/system-prompt`);
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('common.loadFailed'));
      }
      setSystemPrompt(data.system_prompt || '');
    } catch (err) {
      setPromptMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.loadFailed'),
      });
    }
  }, [t]);

  useEffect(() => {
    loadModels();
    loadSystemPrompt();
  }, [loadModels, loadSystemPrompt]);

  const handleDelete = async (id: string) => {
    if (!confirm(t('settings.model.confirmDelete'))) return;
    try {
      const resp = await fetch(`${API_BASE}/model/${encodeURIComponent(id)}`, {
        method: 'DELETE',
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('settings.model.deleteFailed'));
      }
      loadModels();
    } catch (err) {
      alert(err instanceof Error ? err.message : t('settings.model.deleteFailed'));
    }
  };

  const handleToggleForm = () => {
    setShowForm((prev) => !prev);
    setFormError(null);
    setFormData({ ...EMPTY_FORM });
  };

  const handleCreate = async () => {
    if (!formData.displayName || !formData.model) {
      setFormError(t('settings.model.fillRequired'));
      return;
    }
    if (formData.modelClass !== 'ollama' && !formData.apiKey) {
      setFormError(t('settings.model.fillApiKey'));
      return;
    }

    setSaving(true);
    setFormError(null);
    try {
      const connection: Record<string, unknown> = { model: formData.model };
      if (formData.apiKey) connection.api_key = formData.apiKey;
      if (formData.baseUrl) connection.base_url = formData.baseUrl;

      const resp = await fetch(`${API_BASE}/model/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          model_class: formData.modelClass,
          display_name: formData.displayName,
          connection,
          thinking_type: formData.thinkingType || undefined,
        }),
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('settings.model.createFailed'));
      }
      setShowForm(false);
      setFormData({ ...EMPTY_FORM });
      loadModels();
    } catch (err) {
      setFormError(err instanceof Error ? err.message : t('settings.model.createFailed'));
    } finally {
      setSaving(false);
    }
  };

  const handleSaveSystemPrompt = async () => {
    setPromptSaving(true);
    setPromptMessage(null);
    try {
      const resp = await fetch(`${API_BASE}/system-prompt`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ system_prompt: systemPrompt }),
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('common.saveFailed'));
      }
      setSystemPrompt(data.system_prompt ?? systemPrompt);
      setPromptMessage({ type: 'success', text: t('common.saveSuccess') });
    } catch (err) {
      setPromptMessage({
        type: 'error',
        text: err instanceof Error ? err.message : t('common.saveFailed'),
      });
    } finally {
      setPromptSaving(false);
    }
  };

  const example = PROVIDER_EXAMPLES[formData.modelClass] || PROVIDER_EXAMPLES.openai;

  return (
    <div className="model-settings">
      <section className="settings-section">
        <div className="model-settings-header">
          <h3 className="settings-section-title">{t('settings.eino.modelSectionTitle')}</h3>
          <button className="settings-btn settings-btn-primary" onClick={handleToggleForm} data-testid="eino-add-model-toggle">
            {showForm ? t('common.cancel') : t('settings.model.addModel')}
          </button>
        </div>
        <p className="settings-section-desc">{t('settings.eino.modelSectionDesc')}</p>

        {showForm && (
          <div className="provider-card eino-add-form">
            <div className="provider-body">
              <div className="form-group">
                <label className="form-label">{t('settings.eino.modelClass')}</label>
                <select
                  className="form-select"
                  value={formData.modelClass}
                  onChange={(e) => setFormData({ ...formData, modelClass: e.target.value })}
                >
                  {MODEL_CLASSES.map((cls) => (
                    <option key={cls} value={cls}>
                      {cls}
                    </option>
                  ))}
                </select>
              </div>
              <div className="form-group">
                <label className="form-label">
                  {t('settings.model.displayName')} <span className="required">*</span>
                </label>
                <input
                  type="text"
                  className="form-input"
                  value={formData.displayName}
                  onChange={(e) => setFormData({ ...formData, displayName: e.target.value })}
                  placeholder={t('settings.model.placeholders.displayName', { example: example.displayName })}
                  data-testid="eino-form-display-name"
                />
              </div>
              <div className="form-group">
                <label className="form-label">
                  {t('settings.model.modelName')} <span className="required">*</span>
                </label>
                <input
                  type="text"
                  className="form-input"
                  value={formData.model}
                  onChange={(e) => setFormData({ ...formData, model: e.target.value })}
                  placeholder={t('settings.model.placeholders.model', { example: example.model })}
                  data-testid="eino-form-model"
                />
              </div>
              {formData.modelClass !== 'ollama' && (
                <div className="form-group">
                  <label className="form-label">
                    {t('settings.model.apiKey')} <span className="required">*</span>
                  </label>
                  <input
                    type="password"
                    className="form-input"
                    value={formData.apiKey}
                    onChange={(e) => setFormData({ ...formData, apiKey: e.target.value })}
                    placeholder={t('settings.model.placeholders.apiKey')}
                    data-testid="eino-form-api-key"
                  />
                </div>
              )}
              {!HIDE_BASE_URL.includes(formData.modelClass) && (
                <div className="form-group">
                  <label className="form-label">{t('settings.model.baseUrl')}</label>
                  <input
                    type="text"
                    className="form-input"
                    value={formData.baseUrl}
                    onChange={(e) => setFormData({ ...formData, baseUrl: e.target.value })}
                    placeholder={t('settings.model.baseUrlPlaceholder')}
                  />
                </div>
              )}
              <div className="form-group">
                <label className="form-label">{t('settings.model.thinkingMode')}</label>
                <select
                  className="form-select"
                  value={formData.thinkingType}
                  onChange={(e) => setFormData({ ...formData, thinkingType: e.target.value })}
                >
                  {THINKING_TYPE_OPTIONS.map((opt) => (
                    <option key={opt.value} value={opt.value}>
                      {t(opt.labelKey)}
                    </option>
                  ))}
                </select>
              </div>
              {formError && <div className="settings-message error" data-testid="eino-form-error">{formError}</div>}
              <div className="settings-btn-group">
                <button className="settings-btn settings-btn-secondary" onClick={handleToggleForm}>
                  {t('common.cancel')}
                </button>
                <button
                  className="settings-btn settings-btn-primary"
                  onClick={handleCreate}
                  disabled={saving}
                  data-testid="eino-form-submit"
                >
                  {saving ? t('common.saving') : t('common.save')}
                </button>
              </div>
            </div>
          </div>
        )}

        {loading ? (
          <div className="model-loading">{t('common.loading')}</div>
        ) : error ? (
          <div className="model-error" data-testid="eino-model-error">
            <p>{error}</p>
            <button className="settings-btn settings-btn-primary" onClick={loadModels}>
              {t('common.retry')}
            </button>
          </div>
        ) : models.length === 0 ? (
          <div className="no-models" data-testid="eino-no-models">{t('settings.model.noModels')}</div>
        ) : (
          models.map((model) => (
            <div key={model.id} className="model-card" data-testid="eino-model-card">
              <div className="model-card-header">
                <span className="model-name">
                  {model.display_name}
                  <span className="eino-model-class-badge">{model.model_class}</span>
                </span>
                <div className="model-actions">
                  <button
                    className="model-action-btn model-action-btn-danger"
                    onClick={() => handleDelete(model.id)}
                    data-testid="eino-model-delete"
                  >
                    {t('common.delete')}
                  </button>
                </div>
              </div>
              <div className="model-card-details">
                <div className="model-detail-row">
                  <span className="detail-label">{t('settings.model.model')}:</span>
                  <span className="detail-value">{model.connection.model}</span>
                </div>
                <div className="model-detail-row">
                  <span className="detail-label">API Key:</span>
                  <span className="detail-value">
                    {model.connection.api_key || t('settings.model.notConfigured')}
                  </span>
                </div>
                {model.connection.base_url && (
                  <div className="model-detail-row">
                    <span className="detail-label">Endpoint:</span>
                    <span className="detail-value detail-url">{model.connection.base_url}</span>
                  </div>
                )}
                {model.thinking_type && (
                  <div className="model-detail-row">
                    <span className="detail-label">{t('settings.model.thinkingModeLabel')}</span>
                    <span className="detail-value">{model.thinking_type}</span>
                  </div>
                )}
              </div>
            </div>
          ))
        )}
      </section>

      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.eino.systemPromptTitle')}</h3>
        <p className="settings-section-desc">{t('settings.eino.systemPromptDesc')}</p>
        <textarea
          className="settings-input settings-textarea"
          value={systemPrompt}
          onChange={(e) => setSystemPrompt(e.target.value)}
          placeholder={t('settings.eino.systemPromptPlaceholder')}
          data-testid="eino-system-prompt-input"
        />
        {promptMessage && (
          <div className={`settings-message ${promptMessage.type}`} data-testid="eino-prompt-message">{promptMessage.text}</div>
        )}
        <div className="settings-btn-group">
          <button
            className="settings-btn settings-btn-primary"
            onClick={handleSaveSystemPrompt}
            disabled={promptSaving}
            data-testid="eino-system-prompt-save"
          >
            {promptSaving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </section>
    </div>
  );
}
