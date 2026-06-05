import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { AgentInfo } from '../ChatPage';
import { isImageUrl } from '../../utils/url';

interface AgentConfigState {
  agent_type: string;
  model_id: string;
  acp_mode: string;
}

const emptyAgentConfig: AgentConfigState = { agent_type: '', model_id: '', acp_mode: '' };

interface GeneralSettingsProps {
  onSettingsChanged?: () => void;
}

export function GeneralSettings({ onSettingsChanged }: GeneralSettingsProps) {
  const { t, i18n } = useTranslation();
  const [username, setUsername] = useState('User');
  const [avatarUrl, setAvatarUrl] = useState('');
  const [titleAgent, setTitleAgent] = useState<AgentConfigState>(emptyAgentConfig);
  const [messageAgent, setMessageAgent] = useState<AgentConfigState>(emptyAgentConfig);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  const [titleAgentDropdownOpen, setTitleAgentDropdownOpen] = useState(false);
  const [titleModelDropdownOpen, setTitleModelDropdownOpen] = useState(false);
  const [titleModeDropdownOpen, setTitleModeDropdownOpen] = useState(false);
  const titleAgentRef = useRef<HTMLDivElement>(null);
  const titleModelRef = useRef<HTMLDivElement>(null);
  const titleModeRef = useRef<HTMLDivElement>(null);

  const [msgAgentDropdownOpen, setMsgAgentDropdownOpen] = useState(false);
  const [msgModelDropdownOpen, setMsgModelDropdownOpen] = useState(false);
  const [msgModeDropdownOpen, setMsgModeDropdownOpen] = useState(false);
  const msgAgentRef = useRef<HTMLDivElement>(null);
  const msgModelRef = useRef<HTMLDivElement>(null);
  const msgModeRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    fetchSettings();
    fetchAgents();
  }, []);

  useEffect(() => {
    const anyOpen = titleAgentDropdownOpen || titleModelDropdownOpen || titleModeDropdownOpen
      || msgAgentDropdownOpen || msgModelDropdownOpen || msgModeDropdownOpen;
    if (!anyOpen) return;
    const handleClick = (e: MouseEvent) => {
      const t = e.target as Node;
      if (titleAgentDropdownOpen && titleAgentRef.current && !titleAgentRef.current.contains(t)) setTitleAgentDropdownOpen(false);
      if (titleModelDropdownOpen && titleModelRef.current && !titleModelRef.current.contains(t)) setTitleModelDropdownOpen(false);
      if (titleModeDropdownOpen && titleModeRef.current && !titleModeRef.current.contains(t)) setTitleModeDropdownOpen(false);
      if (msgAgentDropdownOpen && msgAgentRef.current && !msgAgentRef.current.contains(t)) setMsgAgentDropdownOpen(false);
      if (msgModelDropdownOpen && msgModelRef.current && !msgModelRef.current.contains(t)) setMsgModelDropdownOpen(false);
      if (msgModeDropdownOpen && msgModeRef.current && !msgModeRef.current.contains(t)) setMsgModeDropdownOpen(false);
    };
    document.addEventListener('mousedown', handleClick);
    return () => document.removeEventListener('mousedown', handleClick);
  }, [titleAgentDropdownOpen, titleModelDropdownOpen, titleModeDropdownOpen, msgAgentDropdownOpen, msgModelDropdownOpen, msgModeDropdownOpen]);

  const fetchSettings = async () => {
    try {
      const res = await fetch('/api/v1/config/settings/get');
      const data = await res.json();
      if (data.code === 0 && data.settings) {
        setUsername(data.settings.username || 'User');
        setAvatarUrl(data.settings.avatar_url || '');
        if (data.settings.title_agent) {
          setTitleAgent({
            agent_type: data.settings.title_agent.agent_type || '',
            model_id: data.settings.title_agent.model_id || '',
            acp_mode: data.settings.title_agent.acp_mode || '',
          });
        }
        if (data.settings.message_agent) {
          setMessageAgent({
            agent_type: data.settings.message_agent.agent_type || '',
            model_id: data.settings.message_agent.model_id || '',
            acp_mode: data.settings.message_agent.acp_mode || '',
          });
        }
      }
    } catch (err) {
      console.error('Failed to load settings:', err);
    } finally {
      setLoading(false);
    }
  };

  const fetchAgents = async () => {
    try {
      const res = await fetch('/api/v1/agent/list');
      const data = await res.json();
      setAgents(data.agent_list || []);
    } catch (err) {
      console.error('Failed to load agents:', err);
    }
  };

  const handleSave = async () => {
    setSaving(true);
    setMessage(null);
    try {
      const settingsRes = await fetch('/api/v1/config/settings/get');
      const settingsData = await settingsRes.json();
      const currentSettings = settingsData.code === 0 && settingsData.settings ? settingsData.settings : {};

      const ta = titleAgent.agent_type ? titleAgent : undefined;
      const ma = messageAgent.agent_type ? messageAgent : undefined;

      const res = await fetch('/api/v1/config/settings/save', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...currentSettings,
          username,
          avatar_url: avatarUrl,
          title_agent: ta,
          message_agent: ma,
        }),
      });
      const data = await res.json();
      if (data.code === 0) {
        setMessage({ type: 'success', text: t('common.saveSuccess') });
        onSettingsChanged?.();
      } else {
        setMessage({ type: 'error', text: data.msg || t('common.saveFailed') });
      }
    } catch {
      setMessage({ type: 'error', text: t('common.saveFailed') });
    } finally {
      setSaving(false);
    }
  };

  const findAgent = (cfg: AgentConfigState): AgentInfo | undefined => {
    if (!cfg.agent_type) return undefined;
    return agents.find((a) => a.type === cfg.agent_type && (a.model_id === cfg.model_id || a.models?.availableModels.some((m) => m.modelId === cfg.model_id)));
  };

  const agentDisplayName = (cfg: AgentConfigState): string => {
    const agent = findAgent(cfg);
    return agent ? agent.display_name : cfg.agent_type ? cfg.agent_type : t('common.notSet');
  };

  const handleSelectAgent = (
    agent: AgentInfo,
    setter: (v: AgentConfigState) => void,
    closeDropdown: () => void,
  ) => {
    setter({
      agent_type: agent.type,
      model_id: agent.models?.currentModelId || agent.model_id,
      acp_mode: agent.modes?.currentModeId || '',
    });
    closeDropdown();
  };

  const handleClearAgent = (setter: (v: AgentConfigState) => void, closeDropdown: () => void) => {
    setter(emptyAgentConfig);
    closeDropdown();
  };

  const renderAgentIcon = (agent: AgentInfo) => {
    if (!agent.icon_url) return null;
    if (isImageUrl(agent.icon_url)) {
      return <img src={agent.icon_url} alt="" className="model-tag-icon" referrerPolicy="no-referrer" />;
    }
    return <span className="model-tag-emoji">{agent.icon_url}</span>;
  };

  const renderAgentSelector = (
    label: string,
    desc: string,
    cfg: AgentConfigState,
    setCfg: (v: AgentConfigState) => void,
    agentOpen: boolean,
    setAgentOpen: (v: boolean) => void,
    agentRef: React.RefObject<HTMLDivElement | null>,
    modelOpen: boolean,
    setModelOpen: (v: boolean) => void,
    modelRef: React.RefObject<HTMLDivElement | null>,
    modeOpen: boolean,
    setModeOpen: (v: boolean) => void,
    modeRef: React.RefObject<HTMLDivElement | null>,
  ) => {
    const selected = findAgent(cfg);
    const availableModels = selected?.models?.availableModels || [];
    const availableModes = selected?.modes?.availableModes || [];
    const showModel = selected && availableModels.length > 1;
    const showMode = selected && availableModes.length > 1;
    const currentModelName = availableModels.find((m) => m.modelId === cfg.model_id)?.name || cfg.model_id || t('common.default');
    const currentModeName = availableModes.find((m) => m.id === cfg.acp_mode)?.name || cfg.acp_mode || t('common.default');

    return (
      <div className="settings-form-group">
        <label className="settings-label">{label}</label>
        <div className="settings-agent-selectors">
          <div className="settings-model-selector" ref={agentRef}>
            <div className="settings-model-tag" onClick={() => setAgentOpen(!agentOpen)}>
              {selected && renderAgentIcon(selected)}
              <span className="settings-model-tag-text">{agentDisplayName(cfg)}</span>
              <svg className="settings-model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M6 9l6 6 6-6" />
              </svg>
            </div>
            {agentOpen && (
              <div className="settings-model-dropdown">
                <div
                  className={`settings-model-dropdown-item${!cfg.agent_type ? ' active' : ''}`}
                  onClick={() => handleClearAgent(setCfg, () => setAgentOpen(false))}
                >
                  <div className="settings-model-dropdown-info">
                    <span className="settings-model-dropdown-name">{t('common.notSet')}</span>
                  </div>
                  {!cfg.agent_type && (
                    <svg className="settings-model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <path d="M20 6L9 17l-5-5" />
                    </svg>
                  )}
                </div>
                {agents.map((agent) => {
                  const isActive = cfg.agent_type === agent.type && (cfg.model_id === agent.model_id || !!agent.models?.availableModels.some((m) => m.modelId === cfg.model_id));
                  return (
                    <div
                      key={`${agent.type}-${agent.model_id}`}
                      className={`settings-model-dropdown-item${isActive ? ' active' : ''}`}
                      onClick={() => handleSelectAgent(agent, setCfg, () => setAgentOpen(false))}
                    >
                      {agent.icon_url ? (
                        isImageUrl(agent.icon_url)
                          ? <img src={agent.icon_url} alt="" className="model-dropdown-icon" referrerPolicy="no-referrer" />
                          : <span className="model-dropdown-emoji">{agent.icon_url}</span>
                      ) : (
                        <div className="model-dropdown-icon-placeholder" />
                      )}
                      <div className="settings-model-dropdown-info">
                        <span className="settings-model-dropdown-name">{agent.display_name}</span>
                      </div>
                      {isActive && (
                        <svg className="settings-model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M20 6L9 17l-5-5" />
                        </svg>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </div>

          {showModel && (
            <div className="settings-model-selector" ref={modelRef}>
              <div className="settings-model-tag" onClick={() => setModelOpen(!modelOpen)}>
                <span className="settings-model-tag-text">{currentModelName}</span>
                <svg className="settings-model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M6 9l6 6 6-6" />
                </svg>
              </div>
              {modelOpen && (
                <div className="settings-model-dropdown">
                  {availableModels.map((m) => {
                  const isActive = cfg.model_id === m.modelId;
                  return (
                    <div
                      key={m.modelId}
                      className={`settings-model-dropdown-item${isActive ? ' active' : ''}`}
                      onClick={() => { setCfg({ ...cfg, model_id: m.modelId }); setModelOpen(false); }}
                      >
                        <div className="settings-model-dropdown-info">
                          <span className="settings-model-dropdown-name">{m.name}</span>
                        </div>
                        {isActive && (
                          <svg className="settings-model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                            <path d="M20 6L9 17l-5-5" />
                          </svg>
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          )}

          {showMode && (
            <div className="settings-model-selector" ref={modeRef}>
              <div className="settings-model-tag" onClick={() => setModeOpen(!modeOpen)}>
                <span className="settings-model-tag-text">{currentModeName}</span>
                <svg className="settings-model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M6 9l6 6 6-6" />
                </svg>
              </div>
              {modeOpen && (
                <div className="settings-model-dropdown">
                  {availableModes.map((m) => {
                    const isActive = cfg.acp_mode === m.id;
                    return (
                      <div
                        key={m.id}
                        className={`settings-model-dropdown-item${isActive ? ' active' : ''}`}
                        onClick={() => { setCfg({ ...cfg, acp_mode: m.id }); setModeOpen(false); }}
                      >
                        <div className="settings-model-dropdown-info">
                          <span className="settings-model-dropdown-name">{m.name}</span>
                        </div>
                        {isActive && (
                          <svg className="settings-model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                            <path d="M20 6L9 17l-5-5" />
                          </svg>
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          )}
        </div>
        <span className="settings-switch-desc">{desc}</span>
      </div>
    );
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

        {renderAgentSelector(
          t('settings.general.builtinModel'),
          t('settings.general.builtinModelDesc'),
          titleAgent,
          setTitleAgent,
          titleAgentDropdownOpen, setTitleAgentDropdownOpen, titleAgentRef,
          titleModelDropdownOpen, setTitleModelDropdownOpen, titleModelRef,
          titleModeDropdownOpen, setTitleModeDropdownOpen, titleModeRef,
        )}

        {renderAgentSelector(
          t('settings.general.messageModel'),
          t('settings.general.messageModelDesc'),
          messageAgent,
          setMessageAgent,
          msgAgentDropdownOpen, setMsgAgentDropdownOpen, msgAgentRef,
          msgModelDropdownOpen, setMsgModelDropdownOpen, msgModelRef,
          msgModeDropdownOpen, setMsgModeDropdownOpen, msgModeRef,
        )}
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
