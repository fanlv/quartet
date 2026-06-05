import { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { PROVIDER_ICONS } from '../../utils/models';
import './ModelSettings.css';

interface ConnectionInfo {
  api_key?: string;
  base_url?: string;
  model: string;
  ark?: { region?: string };
  openai?: { by_azure?: boolean; api_version?: string };
  gemini?: { backend?: string; project?: string; location?: string };
}

interface ModelInstance {
  id: number;
  model_class: string;
  display_name: string;
  connection: ConnectionInfo;
  thinking_type?: string;
  enable_base64_url?: boolean;
  status: number;
  created_at: number;
}

interface ProviderInfo {
  model_class: string;
  name: string;
  description: string;
  icon_url: string;
}

interface ProviderModelList {
  provider: ProviderInfo;
  model_list: ModelInstance[];
}

const API_BASE = '/api/v1/config/model';

const PROVIDER_ICONS_FALLBACK: Record<string, string> = {
  ark: '🔥',
  openai: '🤖',
  claude: '🎭',
  deepseek: '🔍',
  gemini: '✨',
  ollama: '🦙',
  qwen: '☁️',
};

const THINKING_TYPE_OPTIONS = [
  { value: '', labelKey: 'common.default' },
  { value: 'enable', labelKey: 'common.enabled' },
  { value: 'disable', labelKey: 'common.disabled' },
  { value: 'auto', labelKey: 'common.auto' },
];

const SUPPORTS_THINKING = ['ark', 'claude', 'gemini', 'qwen', 'ollama'];

const PROVIDER_EXAMPLES: Record<string, {
  displayName: string;
  model: string;
  hasApiKey: boolean;
}> = {
  ark: { displayName: 'Doubao Seed 1.6', model: 'doubao-seed-1-6-250615', hasApiKey: true },
  openai: { displayName: 'GPT-4o', model: 'gpt-4o', hasApiKey: true },
  claude: { displayName: 'Claude 3.5 Sonnet', model: 'claude-3-5-sonnet-20241022', hasApiKey: true },
  deepseek: { displayName: 'DeepSeek V3', model: 'deepseek-chat', hasApiKey: true },
  gemini: { displayName: 'Gemini 2.0 Flash', model: 'gemini-2.0-flash-exp', hasApiKey: true },
  ollama: { displayName: 'Qwen 2.5 7B', model: 'qwen2.5:7b', hasApiKey: false },
  qwen: { displayName: 'Qwen Max', model: 'qwen-max', hasApiKey: true },
};

function maskApiKey(key?: string, notConfiguredText?: string): string {
  if (!key) return notConfiguredText || 'Not configured';
  if (key.length <= 10) return '***';
  return key.slice(0, 6) + '***' + key.slice(-4);
}

export function ModelSettings() {
  const { t, i18n } = useTranslation();
  const [providers, setProviders] = useState<ProviderModelList[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [addingProvider, setAddingProvider] = useState<ProviderInfo | null>(null);
  const [formData, setFormData] = useState({
    displayName: '',
    model: '',
    apiKey: '',
    baseUrl: '',
    enableBase64Url: false,
    thinkingType: '',
    arkRegion: '',
    openaiByAzure: false,
    openaiApiVersion: '',
    geminiBackend: '',
    geminiProject: '',
    geminiLocation: '',
  });
  const [saving, setSaving] = useState(false);
  const modalBodyRef = useRef<HTMLDivElement>(null);
  const modalContentRef = useRef<HTMLDivElement>(null);
  const currentLang = (i18n.resolvedLanguage || i18n.language || 'en').startsWith('zh') ? 'zh' : 'en';

  // iPad/手机端：使用 visualViewport API 检测键盘弹出，动态调整弹窗高度
  useEffect(() => {
    if (!addingProvider) return;
    const vv = window.visualViewport;
    if (!vv) return;

    const handleResize = () => {
      const modalContent = modalContentRef.current;
      const overlay = modalContent?.parentElement;
      if (!modalContent || !overlay) return;
      const keyboardHeight = window.innerHeight - vv.height;
      if (keyboardHeight > 100) {
        const h = vv.height;
        modalContent.style.height = `${h - 20}px`;
        modalContent.style.maxHeight = `${h - 20}px`;
        overlay.style.alignItems = 'flex-start';
        overlay.style.paddingTop = `${vv.offsetTop + 10}px`;
      } else {
        modalContent.style.height = '';
        modalContent.style.maxHeight = '';
        overlay.style.alignItems = '';
        overlay.style.paddingTop = '';
      }
    };

    vv.addEventListener('resize', handleResize);
    vv.addEventListener('scroll', handleResize);
    return () => {
      vv.removeEventListener('resize', handleResize);
      vv.removeEventListener('scroll', handleResize);
    };
  }, [addingProvider]);

  // 输入框获得焦点时滚动到可视区域
  useEffect(() => {
    const container = modalBodyRef.current;
    if (!container) return;

    const handleFocus = (e: Event) => {
      const target = e.target as HTMLElement;
      if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT') {
        setTimeout(() => {
          target.scrollIntoView({ behavior: 'smooth', block: 'center' });
        }, 400);
      }
    };

    container.addEventListener('focusin', handleFocus);
    return () => container.removeEventListener('focusin', handleFocus);
  }, [addingProvider]);

  const loadModels = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const resp = await fetch(`${API_BASE}/list?lang=${currentLang}`);
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('common.loadFailed'));
      }
      setProviders(data.provider_model_list || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.model.loadModelListFailed'));
    } finally {
      setLoading(false);
    }
  }, [currentLang, t]);

  useEffect(() => {
    loadModels();
  }, [loadModels]);

  const handleDelete = async (id: number) => {
    if (!confirm(t('settings.model.confirmDelete'))) return;
    try {
      const resp = await fetch(`${API_BASE}/delete`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: String(id) }),
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

  const handleOpenAddModal = (provider: ProviderInfo) => {
    setAddingProvider(provider);
    setFormData({
      displayName: '',
      model: '',
      apiKey: '',
      baseUrl: '',
      enableBase64Url: false,
      thinkingType: '',
      arkRegion: '',
      openaiByAzure: false,
      openaiApiVersion: '',
      geminiBackend: '',
      geminiProject: '',
      geminiLocation: '',
    });
  };

  const handleCloseModal = () => {
    setAddingProvider(null);
  };

  const handleSave = async () => {
    if (!addingProvider) return;
    if (!formData.displayName || !formData.model) {
      alert(t('settings.model.fillRequired'));
      return;
    }
    if (addingProvider.model_class !== 'ollama' && !formData.apiKey) {
      alert(t('settings.model.fillApiKey'));
      return;
    }

    setSaving(true);
    try {
      const connection: Record<string, unknown> = {
        model: formData.model,
      };
      if (formData.apiKey) connection.api_key = formData.apiKey;
      if (formData.baseUrl) connection.base_url = formData.baseUrl;

      if (addingProvider.model_class === 'ark' && formData.arkRegion) {
        connection.ark = { region: formData.arkRegion };
      }
      if (addingProvider.model_class === 'openai') {
        if (formData.openaiByAzure || formData.openaiApiVersion) {
          connection.openai = {
            by_azure: formData.openaiByAzure,
            api_version: formData.openaiApiVersion || undefined,
          };
        }
      }
      if (addingProvider.model_class === 'gemini') {
        if (formData.geminiBackend || formData.geminiProject || formData.geminiLocation) {
          connection.gemini = {
            backend: formData.geminiBackend || undefined,
            project: formData.geminiProject || undefined,
            location: formData.geminiLocation || undefined,
          };
        }
      }

      const payload = {
        model_class: addingProvider.model_class,
        display_name: formData.displayName,
        connection,
        thinking_type: formData.thinkingType || undefined,
        enable_base64_url: formData.enableBase64Url || undefined,
      };

      const resp = await fetch(`${API_BASE}/create`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(data.msg || t('settings.model.createFailed'));
      }
      handleCloseModal();
      loadModels();
    } catch (err) {
      alert(err instanceof Error ? err.message : t('settings.model.createFailed'));
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="model-settings">
        <div className="model-loading">{t('common.loading')}</div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="model-settings">
        <div className="model-error">
          <p>{error}</p>
          <button className="settings-btn settings-btn-primary" onClick={loadModels}>
            {t('common.retry')}
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="model-settings">
      <section className="settings-section">
        <div className="model-settings-header">
          <h3 className="settings-section-title">{t('settings.model.sectionTitle')}</h3>
          <span className="model-provider-count">{t('settings.model.providerCount', { count: providers.length })}</span>
        </div>

        <div className="provider-list">
          {providers.map((pm) => (
            <div key={pm.provider.model_class} className="provider-card">
              <div className="provider-header">
                <div className="provider-info">
                  <span className="provider-icon">
                    <img 
                      src={PROVIDER_ICONS[pm.provider.model_class]} 
                      alt={pm.provider.name}
                      referrerPolicy="no-referrer"
                      style={{ width: '32px', height: '32px', objectFit: 'contain' }}
                      onError={(e) => {
                        e.currentTarget.style.display = 'none';
                        const next = e.currentTarget.nextElementSibling as HTMLSpanElement | null;
                        if (next) {
                          next.style.display = 'inline';
                        }
                      }}
                    />
                    <span style={{ display: 'none', fontSize: '32px', lineHeight: '32px' }}>
                      {PROVIDER_ICONS_FALLBACK[pm.provider.model_class] || '🤖'}
                    </span>
                  </span>
                  <div className="provider-meta">
                    <span className="provider-name">{pm.provider.name}</span>
                    <span className="provider-desc">{pm.provider.description}</span>
                  </div>
                </div>
                <button
                  className="settings-btn settings-btn-primary"
                  onClick={() => handleOpenAddModal(pm.provider)}
                >
                  {t('settings.model.addModel')}
                </button>
              </div>

              <div className="provider-body">
                <div className="provider-model-count">
                  {t('settings.model.configuredCount', { count: pm.model_list.length })}
                </div>
                {pm.model_list.map((model) => (
                  <div key={model.id} className="model-card">
                    <div className="model-card-header">
                      <span className="model-name">{model.display_name}</span>
                      <div className="model-actions">
                        <span
                          className={`model-status ${model.status === 1 ? 'active' : ''}`}
                        >
                          {model.status === 1 ? t('settings.model.status.enabled') : t('settings.model.status.disabled')}
                        </span>
                        <button
                          className="model-action-btn model-action-btn-danger"
                          onClick={() => handleDelete(model.id)}
                        >
                          {t('common.delete')}
                        </button>
                      </div>
                    </div>
                    <div className="model-card-details">
                      <div className="model-detail-row">
                        <span className="detail-label">ID:</span>
                        <span className="detail-value">{model.id}</span>
                      </div>
                      <div className="model-detail-row">
                        <span className="detail-label">{t('settings.model.model')}:</span>
                        <span className="detail-value">{model.connection.model}</span>
                      </div>
                      {pm.provider.model_class !== 'ollama' && (
                        <div className="model-detail-row">
                          <span className="detail-label">API Key:</span>
                          <span className="detail-value">
                            {maskApiKey(model.connection.api_key, t('settings.model.notConfigured'))}
                          </span>
                        </div>
                      )}
                      {model.connection.base_url && (
                        <div className="model-detail-row">
                          <span className="detail-label">Endpoint:</span>
                          <span className="detail-value detail-url">
                            {model.connection.base_url}
                          </span>
                        </div>
                      )}
                      {model.enable_base64_url && (
                        <div className="model-detail-row">
                          <span className="detail-label">Base64 URL:</span>
                          <span className="detail-value">{t('common.enabled')}</span>
                        </div>
                      )}
                      {SUPPORTS_THINKING.includes(pm.provider.model_class) &&
                        model.thinking_type && (
                          <div className="model-detail-row">
                            <span className="detail-label">{t('settings.model.thinkingModeLabel')}</span>
                            <span className="detail-value">{model.thinking_type}</span>
                          </div>
                        )}
                      {pm.provider.model_class === 'ark' && model.connection.ark?.region && (
                        <div className="model-detail-row">
                          <span className="detail-label">Region:</span>
                          <span className="detail-value">{model.connection.ark.region}</span>
                        </div>
                      )}
                      {pm.provider.model_class === 'openai' && model.connection.openai && (
                        <>
                          <div className="model-detail-row">
                            <span className="detail-label">{t('settings.model.azure')}:</span>
                            <span className="detail-value">
                              {model.connection.openai.by_azure ? t('common.yes') : t('common.no')}
                            </span>
                          </div>
                          {model.connection.openai.api_version && (
                            <div className="model-detail-row">
                              <span className="detail-label">API Version:</span>
                              <span className="detail-value">
                                {model.connection.openai.api_version}
                              </span>
                            </div>
                          )}
                        </>
                      )}
                      {pm.provider.model_class === 'gemini' && model.connection.gemini && (
                        <>
                          {model.connection.gemini.backend && (
                            <div className="model-detail-row">
                              <span className="detail-label">Backend:</span>
                              <span className="detail-value">
                                {model.connection.gemini.backend}
                              </span>
                            </div>
                          )}
                          {model.connection.gemini.project && (
                            <div className="model-detail-row">
                              <span className="detail-label">Project:</span>
                              <span className="detail-value">
                                {model.connection.gemini.project}
                              </span>
                            </div>
                          )}
                          {model.connection.gemini.location && (
                            <div className="model-detail-row">
                              <span className="detail-label">Location:</span>
                              <span className="detail-value">
                                {model.connection.gemini.location}
                              </span>
                            </div>
                          )}
                        </>
                      )}
                    </div>
                  </div>
                ))}
                {pm.model_list.length === 0 && (
                  <div className="no-models">{t('settings.model.noModels')}</div>
                )}
              </div>
            </div>
          ))}
        </div>
      </section>

      {addingProvider && (
        <div className="modal-overlay" onClick={handleCloseModal}>
          <div className="modal-content" ref={modalContentRef} onClick={(e) => e.stopPropagation()}>
            <div className="modal-header">
              <span className="modal-title">{t('settings.model.addModelTitle', { provider: addingProvider.name })}</span>
              <button className="modal-close" onClick={handleCloseModal}>
                ×
              </button>
            </div>
            <div className="modal-body" ref={modalBodyRef}>
              <div className="form-group">
                <label className="form-label">
                  {t('settings.model.displayName')} <span className="required">*</span>
                </label>
                <input
                  type="text"
                  className="form-input"
                  value={formData.displayName}
                  onChange={(e) =>
                    setFormData({ ...formData, displayName: e.target.value })
                  }
                  placeholder={
                    t('settings.model.placeholders.displayName', { example: PROVIDER_EXAMPLES[addingProvider.model_class]?.displayName || 'GPT-4o' })
                  }
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
                  placeholder={
                    t('settings.model.placeholders.model', { example: PROVIDER_EXAMPLES[addingProvider.model_class]?.model || 'gpt-4o' })
                  }
                />
              </div>
              {addingProvider.model_class !== 'ollama' && (
                <div className="form-group">
                  <label className="form-label">
                    {t('settings.model.apiKey')} <span className="required">*</span>
                  </label>
                  <input
                    type="password"
                    className="form-input"
                    value={formData.apiKey}
                    onChange={(e) => setFormData({ ...formData, apiKey: e.target.value })}
                    placeholder={
                      t('settings.model.placeholders.apiKey')
                    }
                  />
                </div>
              )}
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
              <div className="form-group form-group-checkbox">
                <input
                  type="checkbox"
                  id="enableBase64Url"
                  checked={formData.enableBase64Url}
                  onChange={(e) =>
                    setFormData({ ...formData, enableBase64Url: e.target.checked })
                  }
                />
                <label htmlFor="enableBase64Url">{t('settings.model.enableBase64Url')}</label>
              </div>
              {SUPPORTS_THINKING.includes(addingProvider.model_class) && (
                <div className="form-group">
                  <label className="form-label">{t('settings.model.thinkingMode')}</label>
                  <select
                    className="form-select"
                    value={formData.thinkingType}
                    onChange={(e) =>
                      setFormData({ ...formData, thinkingType: e.target.value })
                    }
                  >
                    {THINKING_TYPE_OPTIONS.map((opt) => (
                      <option key={opt.value} value={opt.value}>
                        {t(opt.labelKey)}
                      </option>
                    ))}
                  </select>
                </div>
              )}
              {addingProvider.model_class === 'ark' && (
                <div className="form-group">
                  <label className="form-label">{t('settings.model.region')}</label>
                  <input
                    type="text"
                    className="form-input"
                    value={formData.arkRegion}
                    onChange={(e) =>
                      setFormData({ ...formData, arkRegion: e.target.value })
                    }
                    placeholder={t('settings.model.regionPlaceholder')}
                  />
                </div>
              )}
              {addingProvider.model_class === 'openai' && (
                <>
                  <div className="form-group form-group-checkbox">
                    <input
                      type="checkbox"
                      id="openaiByAzure"
                      checked={formData.openaiByAzure}
                      onChange={(e) =>
                        setFormData({ ...formData, openaiByAzure: e.target.checked })
                      }
                    />
                    <label htmlFor="openaiByAzure">{t('settings.model.useAzure')}</label>
                  </div>
                  <div className="form-group">
                    <label className="form-label">{t('settings.model.apiVersion')}</label>
                    <input
                      type="text"
                      className="form-input"
                      value={formData.openaiApiVersion}
                      onChange={(e) =>
                        setFormData({ ...formData, openaiApiVersion: e.target.value })
                      }
                      placeholder={t('settings.model.apiVersionPlaceholder')}
                    />
                  </div>
                </>
              )}
              {addingProvider.model_class === 'gemini' && (
                <>
                  <div className="form-group">
                    <label className="form-label">{t('settings.model.backend')}</label>
                    <select
                      className="form-select"
                      value={formData.geminiBackend}
                      onChange={(e) =>
                        setFormData({ ...formData, geminiBackend: e.target.value })
                      }
                    >
                      <option value="">{t('settings.model.backendDefault')}</option>
                      <option value="gemini">Gemini API</option>
                      <option value="vertex">Vertex AI</option>
                    </select>
                  </div>
                  <div className="form-group">
                    <label className="form-label">{t('settings.model.project')}</label>
                    <input
                      type="text"
                      className="form-input"
                      value={formData.geminiProject}
                      onChange={(e) =>
                        setFormData({ ...formData, geminiProject: e.target.value })
                      }
                      placeholder={t('settings.model.projectPlaceholder')}
                    />
                  </div>
                  <div className="form-group">
                    <label className="form-label">{t('settings.model.location')}</label>
                    <input
                      type="text"
                      className="form-input"
                      value={formData.geminiLocation}
                      onChange={(e) =>
                        setFormData({ ...formData, geminiLocation: e.target.value })
                      }
                      placeholder={t('settings.model.locationPlaceholder')}
                    />
                  </div>
                </>
              )}
            </div>
            <div className="modal-footer">
              <button
                className="settings-btn settings-btn-secondary"
                onClick={handleCloseModal}
              >
                {t('common.cancel')}
              </button>
              <button
                className="settings-btn settings-btn-primary"
                onClick={handleSave}
                disabled={saving}
              >
                {saving ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
