import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import type { AgentInfo, SessionThoughtLevelState } from '../ChatPage';
import { isImageUrl } from '../../utils/url';
import { useACPThoughtLevels } from '../../hooks/useACPThoughtLevels';

interface AgentConfigState {
  agent_type: string;
  model_id: string;
  acp_mode: string;
  acp_thought_level: string;
}

const emptyAgentConfig: AgentConfigState = { agent_type: '', model_id: '', acp_mode: '', acp_thought_level: '' };

interface GeneralSettingsProps {
  onSettingsChanged?: () => void;
}

export function GeneralSettings({ onSettingsChanged }: GeneralSettingsProps) {
  const { t, i18n } = useTranslation();
  const [username, setUsername] = useState('User');
  const [avatarUrl, setAvatarUrl] = useState('');
  const [graphEndHookScript, setGraphEndHookScript] = useState('');
  const [titleAgent, setTitleAgent] = useState<AgentConfigState>(emptyAgentConfig);
  const [messageAgent, setMessageAgent] = useState<AgentConfigState>(emptyAgentConfig);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  const [titleAgentDropdownOpen, setTitleAgentDropdownOpen] = useState(false);
  const [titleModelDropdownOpen, setTitleModelDropdownOpen] = useState(false);
  const [titleModeDropdownOpen, setTitleModeDropdownOpen] = useState(false);
  const [titleThoughtLevelDropdownOpen, setTitleThoughtLevelDropdownOpen] = useState(false);
  const titleAgentRef = useRef<HTMLDivElement>(null);
  const titleModelRef = useRef<HTMLDivElement>(null);
  const titleModeRef = useRef<HTMLDivElement>(null);
  const titleThoughtLevelRef = useRef<HTMLDivElement>(null);

  const [msgAgentDropdownOpen, setMsgAgentDropdownOpen] = useState(false);
  const [msgModelDropdownOpen, setMsgModelDropdownOpen] = useState(false);
  const [msgModeDropdownOpen, setMsgModeDropdownOpen] = useState(false);
  const [msgThoughtLevelDropdownOpen, setMsgThoughtLevelDropdownOpen] = useState(false);
  const msgAgentRef = useRef<HTMLDivElement>(null);
  const msgModelRef = useRef<HTMLDivElement>(null);
  const msgModeRef = useRef<HTMLDivElement>(null);
  const msgThoughtLevelRef = useRef<HTMLDivElement>(null);

  const titleSelectedAgent = titleAgent.agent_type
    ? agents.find((agent) => agent.type === titleAgent.agent_type)
    : undefined;
  const messageSelectedAgent = messageAgent.agent_type
    ? agents.find((agent) => agent.type === messageAgent.agent_type)
    : undefined;
  const titleThoughtLevelLink = useACPThoughtLevels(
    titleSelectedAgent?.type || '',
    titleAgent.model_id || titleSelectedAgent?.models?.currentModelId || '',
    Boolean(titleSelectedAgent?.models),
    !titleAgent.model_id || titleAgent.model_id === titleSelectedAgent?.models?.currentModelId
      ? titleSelectedAgent?.thoughtLevels || null
      : null,
  );
  const messageThoughtLevelLink = useACPThoughtLevels(
    messageSelectedAgent?.type || '',
    messageAgent.model_id || messageSelectedAgent?.models?.currentModelId || '',
    Boolean(messageSelectedAgent?.models),
    !messageAgent.model_id || messageAgent.model_id === messageSelectedAgent?.models?.currentModelId
      ? messageSelectedAgent?.thoughtLevels || null
      : null,
  );

  useEffect(() => {
    fetchSettings();
    fetchAgents();
  }, []);

  useEffect(() => {
    const anyOpen = titleAgentDropdownOpen || titleModelDropdownOpen || titleModeDropdownOpen || titleThoughtLevelDropdownOpen
      || msgAgentDropdownOpen || msgModelDropdownOpen || msgModeDropdownOpen || msgThoughtLevelDropdownOpen;
    if (!anyOpen) return;
    const handleClick = (e: MouseEvent) => {
      const t = e.target as Node;
      if (titleAgentDropdownOpen && titleAgentRef.current && !titleAgentRef.current.contains(t)) setTitleAgentDropdownOpen(false);
      if (titleModelDropdownOpen && titleModelRef.current && !titleModelRef.current.contains(t)) setTitleModelDropdownOpen(false);
      if (titleModeDropdownOpen && titleModeRef.current && !titleModeRef.current.contains(t)) setTitleModeDropdownOpen(false);
      if (titleThoughtLevelDropdownOpen && titleThoughtLevelRef.current && !titleThoughtLevelRef.current.contains(t)) setTitleThoughtLevelDropdownOpen(false);
      if (msgAgentDropdownOpen && msgAgentRef.current && !msgAgentRef.current.contains(t)) setMsgAgentDropdownOpen(false);
      if (msgModelDropdownOpen && msgModelRef.current && !msgModelRef.current.contains(t)) setMsgModelDropdownOpen(false);
      if (msgModeDropdownOpen && msgModeRef.current && !msgModeRef.current.contains(t)) setMsgModeDropdownOpen(false);
      if (msgThoughtLevelDropdownOpen && msgThoughtLevelRef.current && !msgThoughtLevelRef.current.contains(t)) setMsgThoughtLevelDropdownOpen(false);
    };
    document.addEventListener('mousedown', handleClick);
    return () => document.removeEventListener('mousedown', handleClick);
  }, [titleAgentDropdownOpen, titleModelDropdownOpen, titleModeDropdownOpen, titleThoughtLevelDropdownOpen, msgAgentDropdownOpen, msgModelDropdownOpen, msgModeDropdownOpen, msgThoughtLevelDropdownOpen]);

  useEffect(() => {
    const state = titleThoughtLevelLink.state;
    if (!state) return;
    setTitleAgent((current) => {
      if (current.agent_type !== titleSelectedAgent?.type) return current;
      const modelId = current.model_id || titleSelectedAgent?.models?.currentModelId || '';
      if (modelId !== (titleAgent.model_id || titleSelectedAgent?.models?.currentModelId || '')) return current;
      const currentStillAvailable = state.availableThoughtLevels.some((level) => level.id === current.acp_thought_level);
      const nextThoughtLevel = currentStillAvailable ? current.acp_thought_level : state.currentThoughtLevelId;
      return nextThoughtLevel === current.acp_thought_level
        ? current
        : { ...current, acp_thought_level: nextThoughtLevel };
    });
  }, [titleAgent.model_id, titleSelectedAgent, titleThoughtLevelLink.state]);

  useEffect(() => {
    const state = messageThoughtLevelLink.state;
    if (!state) return;
    setMessageAgent((current) => {
      if (current.agent_type !== messageSelectedAgent?.type) return current;
      const modelId = current.model_id || messageSelectedAgent?.models?.currentModelId || '';
      if (modelId !== (messageAgent.model_id || messageSelectedAgent?.models?.currentModelId || '')) return current;
      const currentStillAvailable = state.availableThoughtLevels.some((level) => level.id === current.acp_thought_level);
      const nextThoughtLevel = currentStillAvailable ? current.acp_thought_level : state.currentThoughtLevelId;
      return nextThoughtLevel === current.acp_thought_level
        ? current
        : { ...current, acp_thought_level: nextThoughtLevel };
    });
  }, [messageAgent.model_id, messageSelectedAgent, messageThoughtLevelLink.state]);

  const fetchSettings = async () => {
    try {
      const res = await fetch('/api/v1/config/settings/get');
      const data = await res.json();
      if (data.code === 0 && data.settings) {
        setUsername(data.settings.username || 'User');
        setAvatarUrl(data.settings.avatar_url || '');
        setGraphEndHookScript(data.settings.graph_end_hook_script || '');
        if (data.settings.title_agent) {
          setTitleAgent({
            agent_type: data.settings.title_agent.agent_type || '',
            model_id: data.settings.title_agent.model_id || '',
            acp_mode: data.settings.title_agent.acp_mode || '',
            acp_thought_level: data.settings.title_agent.acp_thought_level || '',
          });
        }
        if (data.settings.message_agent) {
          setMessageAgent({
            agent_type: data.settings.message_agent.agent_type || '',
            model_id: data.settings.message_agent.model_id || '',
            acp_mode: data.settings.message_agent.acp_mode || '',
            acp_thought_level: data.settings.message_agent.acp_thought_level || '',
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
          graph_end_hook_script: graphEndHookScript,
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
      acp_thought_level: agent.thoughtLevels?.currentThoughtLevelId || '',
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
    thoughtLevelOpen: boolean,
    setThoughtLevelOpen: (v: boolean) => void,
    thoughtLevelRef: React.RefObject<HTMLDivElement | null>,
    linkedThoughtLevels: SessionThoughtLevelState | null,
    thoughtLevelLinking: boolean,
    thoughtLevelLinkError: string,
  ) => {
    const selected = findAgent(cfg);
    const availableModels = selected?.models?.availableModels || [];
    const availableModes = selected?.modes?.availableModes || [];
    const availableThoughtLevels = linkedThoughtLevels?.availableThoughtLevels || [];
    const showModel = selected && availableModels.length > 1;
    const showMode = selected && availableModes.length > 1;
    const showThoughtLevel = selected && availableThoughtLevels.length > 1;
    const currentModelName = availableModels.find((m) => m.modelId === cfg.model_id)?.name || cfg.model_id || t('common.default');
    const currentModeName = availableModes.find((m) => m.id === cfg.acp_mode)?.name || cfg.acp_mode || t('common.default');
    const currentThoughtLevelName = availableThoughtLevels.find((m) => m.id === cfg.acp_thought_level)?.name || cfg.acp_thought_level || t('common.default');

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

          {showThoughtLevel && (
            <div className="settings-model-selector" ref={thoughtLevelRef}>
              <div className="settings-model-tag" onClick={() => setThoughtLevelOpen(!thoughtLevelOpen)}>
                <span className="settings-model-tag-text">{currentThoughtLevelName}</span>
                <svg className="settings-model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M6 9l6 6 6-6" />
                </svg>
              </div>
              {thoughtLevelOpen && (
                <div className="settings-model-dropdown">
                  {availableThoughtLevels.map((m) => {
                    const isActive = cfg.acp_thought_level === m.id;
                    return (
                      <div
                        key={m.id}
                        className={`settings-model-dropdown-item${isActive ? ' active' : ''}`}
                        onClick={() => { setCfg({ ...cfg, acp_thought_level: m.id }); setThoughtLevelOpen(false); }}
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
        {thoughtLevelLinking && <span className="settings-switch-desc">{t('common.loading')}</span>}
        {thoughtLevelLinkError && <div className="settings-message error" role="alert">{thoughtLevelLinkError}</div>}
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
          titleThoughtLevelDropdownOpen, setTitleThoughtLevelDropdownOpen, titleThoughtLevelRef,
          titleThoughtLevelLink.state,
          titleThoughtLevelLink.loading,
          titleThoughtLevelLink.error,
        )}

        {renderAgentSelector(
          t('settings.general.messageModel'),
          t('settings.general.messageModelDesc'),
          messageAgent,
          setMessageAgent,
          msgAgentDropdownOpen, setMsgAgentDropdownOpen, msgAgentRef,
          msgModelDropdownOpen, setMsgModelDropdownOpen, msgModelRef,
          msgModeDropdownOpen, setMsgModeDropdownOpen, msgModeRef,
          msgThoughtLevelDropdownOpen, setMsgThoughtLevelDropdownOpen, msgThoughtLevelRef,
          messageThoughtLevelLink.state,
          messageThoughtLevelLink.loading,
          messageThoughtLevelLink.error,
        )}

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
