import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import type { AgentInfo } from '../ChatPage';
import type { AgentPrefs, AgentPrefsMap } from '../../utils/agentPrefs';
import { invalidateAgentPrefs } from '../../utils/agentPrefs';
import { useACPThoughtLevels } from '../../hooks/useACPThoughtLevels';
import './ACPSettings.css';
import './AgentDefaultsSettings.css';

const emptyPref: AgentPrefs = {};

export function AgentDefaultsSettings() {
  const { t } = useTranslation();
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [activeAgent, setActiveAgent] = useState('');
  const [prefMap, setPrefMap] = useState<AgentPrefsMap>({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  useEffect(() => {
    loadData();
  }, []);

  const loadData = async () => {
    try {
      const [agentRes, settingsRes] = await Promise.all([
        fetch('/api/v1/agent/list'),
        fetch('/api/v1/config/settings/get'),
      ]);
      const agentData = await agentRes.json();
      const settingsData = await settingsRes.json();

      // Only ACP agents carry availableModels/modes/thoughtLevels.
      const acpAgents: AgentInfo[] = (agentData.agent_list || [])
        .filter((agent: AgentInfo) => agent.available !== false);
      setAgents(acpAgents);

      const saved: AgentPrefsMap =
        settingsData.code === 0 && settingsData.settings?.agent_prefs
          ? settingsData.settings.agent_prefs
          : {};
      const byAgentID: AgentPrefsMap = {};
      for (const agent of acpAgents) {
        const pref = saved[agent.agent_id] || saved[agent.type];
        if (pref) byAgentID[agent.agent_id] = pref;
      }
      setPrefMap(byAgentID);

      if (acpAgents.length > 0) {
        setActiveAgent(acpAgents[0].agent_id);
      }
    } catch (err) {
      console.error('Failed to load agent defaults:', err);
    } finally {
      setLoading(false);
    }
  };

  const currentAgent = agents.find((a) => a.agent_id === activeAgent);
  const currentPref = prefMap[activeAgent] || emptyPref;
  const availableModels = currentAgent?.models?.availableModels || [];
  const availableModes = currentAgent?.modes?.availableModes || [];
  const agentModelId = currentAgent?.models?.currentModelId || '';
  const hasModel = (modelId?: string) => !!modelId && availableModels.some((m) => m.modelId === modelId);
  const defaultModelId = hasModel(currentPref.default_model_id)
    ? currentPref.default_model_id!
    : hasModel(agentModelId)
      ? agentModelId
      : availableModels[0]?.modelId || '';
  const {
    state: thoughtLevelState,
    loading: thoughtLevelLinking,
    error: currentThoughtLevelLinkError,
  } = useACPThoughtLevels(
    currentAgent?.type || '',
    defaultModelId,
    Boolean(currentAgent?.models),
    defaultModelId === agentModelId ? currentAgent?.thoughtLevels || null : null,
  );
  const availableThoughtLevels = thoughtLevelState?.availableThoughtLevels || [];

  useEffect(() => {
    if (!currentAgent || !thoughtLevelState) return;
    const agentType = currentAgent.agent_id;
    setPrefMap((prev) => {
      const pref = prev[agentType];
      if (!pref?.default_thought_level) return prev;

      const selectedModelId = pref.default_model_id || agentModelId;
      const stillAvailable = thoughtLevelState.availableThoughtLevels.some(
        (level) => level.id === pref.default_thought_level,
      );
      if (selectedModelId !== defaultModelId || stillAvailable) return prev;

      return {
        ...prev,
        [agentType]: { ...pref, default_thought_level: undefined },
      };
    });
  }, [agentModelId, currentAgent, defaultModelId, thoughtLevelState]);

  const updatePref = (patch: Partial<AgentPrefs>) => {
    setPrefMap((prev) => ({ ...prev, [activeAgent]: { ...(prev[activeAgent] || {}), ...patch } }));
  };

  const toggleFavorite = (modelId: string) => {
    const cur = currentPref.favorite_model_ids || [];
    const next = cur.includes(modelId) ? cur.filter((id) => id !== modelId) : [...cur, modelId];
    updatePref({ favorite_model_ids: next });
  };

  const handleSave = async () => {
    setSaving(true);
    setMessage(null);

    try {
      const failures: string[] = [];
      for (const agent of agents) {
        const pref = prefMap[agent.agent_id] || emptyPref;
        const res = await fetch(`/api/v1/config/settings/agent/${encodeURIComponent(agent.agent_id)}/prefs`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ prefs: pref }),
        });
        const data = await res.json().catch(() => null);
        if (!res.ok || data?.code !== 0) {
          failures.push(data?.msg || `${agent.display_name}: HTTP ${res.status}`);
        }
      }
      if (failures.length > 0) {
        throw new Error(failures.join('\n'));
      }
      invalidateAgentPrefs();
      setMessage({ type: 'success', text: t('common.saveSuccess') });
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : String(err) });
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <div className="account-settings"><p>{t('common.loading')}</p></div>;
  }

  if (agents.length === 0) {
    return (
      <div className="account-settings">
        <section className="settings-section">
          <h3 className="settings-section-title">{t('settings.agentDefaults.sectionTitle')}</h3>
          <p className="settings-section-desc">{t('settings.agentDefaults.noAgents')}</p>
        </section>
      </div>
    );
  }

  const favoriteIds = currentPref.favorite_model_ids || [];

  return (
    <div className="account-settings" data-testid="agent-defaults-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.agentDefaults.sectionTitle')}</h3>
        <p className="settings-section-desc">{t('settings.agentDefaults.sectionDesc')}</p>

        <div className="acp-agent-tabs">
          {agents.map((agent) => (
            <div
              key={agent.agent_id}
              className={`acp-agent-tab ${activeAgent === agent.agent_id ? 'active' : ''}`}
              onClick={() => setActiveAgent(agent.agent_id)}
            >
              {agent.display_name}
            </div>
          ))}
        </div>

        {/* Favorites */}
        <div className="settings-form-group">
          <label className="settings-label">{t('settings.agentDefaults.favoritesLabel')}</label>
          <span className="settings-switch-desc">{t('settings.agentDefaults.favoritesDesc')}</span>
          {availableModels.length === 0 ? (
            <p className="settings-section-desc">{t('settings.agentDefaults.noModels')}</p>
          ) : (
            <>
              <select
                className="settings-input agent-fav-select"
                value=""
                onChange={(e) => { if (e.target.value) toggleFavorite(e.target.value); }}
              >
                <option value="">{t('settings.agentDefaults.addFavoritePlaceholder')}</option>
                {availableModels
                  .filter((m) => !favoriteIds.includes(m.modelId))
                  .map((m) => (
                    <option key={m.modelId} value={m.modelId}>{m.name}</option>
                  ))}
              </select>
              {favoriteIds.length > 0 && (
                <div className="agent-fav-chips">
                  {favoriteIds.map((id) => {
                    const m = availableModels.find((x) => x.modelId === id);
                    return (
                      <span key={id} className="agent-fav-chip">
                        <span className="agent-fav-star">★</span>
                        <span className="agent-fav-chip-name">{m?.name || id}</span>
                        <button
                          type="button"
                          className="agent-fav-chip-remove"
                          onClick={() => toggleFavorite(id)}
                          title={t('common.delete')}
                        >
                          ×
                        </button>
                      </span>
                    );
                  })}
                </div>
              )}
            </>
          )}
        </div>

        {/* Default model */}
        {availableModels.length > 0 && (
          <div className="settings-form-group">
            <label className="settings-label">{t('settings.agentDefaults.defaultModelLabel')}</label>
            <select
              className="settings-input"
              value={currentPref.default_model_id || ''}
              onChange={(e) => updatePref({ default_model_id: e.target.value })}
            >
              <option value="">{t('common.notSet')}</option>
              {availableModels.map((m) => (
                <option key={m.modelId} value={m.modelId}>{m.name}</option>
              ))}
            </select>
          </div>
        )}

        {/* Default mode */}
        {availableModes.length > 1 && (
          <div className="settings-form-group">
            <label className="settings-label">{t('settings.agentDefaults.defaultModeLabel')}</label>
            <select
              className="settings-input"
              value={currentPref.default_mode || ''}
              onChange={(e) => updatePref({ default_mode: e.target.value })}
            >
              <option value="">{t('common.notSet')}</option>
              {availableModes.map((m) => (
                <option key={m.id} value={m.id}>{m.name}</option>
              ))}
            </select>
          </div>
        )}

        {/* Default thought level */}
        {(availableThoughtLevels.length > 1 || thoughtLevelLinking || currentThoughtLevelLinkError) && (
          <div className="settings-form-group">
            <label className="settings-label">{t('settings.agentDefaults.defaultThoughtLevelLabel')}</label>
            {availableThoughtLevels.length > 1 ? (
              <select
                className="settings-input"
                value={currentPref.default_thought_level || ''}
                onChange={(e) => updatePref({ default_thought_level: e.target.value })}
              >
                <option value="">{t('common.notSet')}</option>
                {availableThoughtLevels.map((m) => (
                  <option key={m.id} value={m.id}>{m.name}</option>
                ))}
              </select>
            ) : thoughtLevelLinking ? (
              <span className="settings-switch-desc">{t('common.loading')}</span>
            ) : null}
            {thoughtLevelLinking && availableThoughtLevels.length > 1 && (
              <span className="settings-switch-desc">{t('common.loading')}</span>
            )}
            {currentThoughtLevelLinkError && <div className="settings-message error">{currentThoughtLevelLinkError}</div>}
          </div>
        )}

        {message && (
          <div className={`settings-message ${message.type}`}>{message.text}</div>
        )}

        <div className="settings-btn-group">
          <button
            className="settings-btn settings-btn-primary"
            onClick={handleSave}
            disabled={saving}
            data-testid="agent-defaults-save-button"
          >
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </section>
    </div>
  );
}
