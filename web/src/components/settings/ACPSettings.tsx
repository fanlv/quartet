import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import './ACPSettings.css';

interface EnvVar {
  key: string;
  value: string;
  enabled: boolean;
}

interface AgentOption {
  agent_id: string;
  type: string;
  env_key?: string;
  display_name: string;
}

const DEFAULT_ENV_VARS: EnvVar[] = [
  { key: 'http_proxy', value: 'http://127.0.0.1:8890', enabled: false },
  { key: 'https_proxy', value: 'http://127.0.0.1:8890', enabled: false },
];

export function ACPSettings() {
  const { t } = useTranslation();
  const [agents, setAgents] = useState<AgentOption[]>([]);
  const [activeAgent, setActiveAgent] = useState('');
  const [envMap, setEnvMap] = useState<Record<string, EnvVar[]>>({});
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

      const acpAgents: AgentOption[] = (agentData.agent_list || [])
        .map((a: { agent_id: string; type: string; env_key?: string; display_name: string }) => ({
          agent_id: a.agent_id,
          type: a.type,
          env_key: a.env_key || a.type,
          display_name: a.display_name,
        }));
      setAgents(acpAgents);

      const savedVars: Record<string, Array<{ key: string; value: string; enabled: boolean }>> =
        settingsData.code === 0 && settingsData.settings?.acp_env_vars
          ? settingsData.settings.acp_env_vars
          : {};

      const newEnvMap: Record<string, EnvVar[]> = {};
      for (const agent of acpAgents) {
        const envKey = agent.env_key || agent.type;
        const vars = savedVars[envKey] || savedVars[agent.type];
        if (vars && vars.length > 0) {
          newEnvMap[envKey] = vars.map((v) => ({
            key: v.key,
            value: v.value,
            enabled: v.enabled,
          }));
        } else {
          newEnvMap[envKey] = DEFAULT_ENV_VARS.map((v) => ({ ...v }));
        }
      }

      for (const [agentType, vars] of Object.entries(savedVars)) {
        if (!newEnvMap[agentType]) {
          newEnvMap[agentType] = vars.map((v) => ({
            key: v.key,
            value: v.value,
            enabled: v.enabled,
          }));
          acpAgents.push({ agent_id: agentType, type: agentType, display_name: agentType });
        }
      }

      setEnvMap(newEnvMap);
      if (acpAgents.length > 0) {
        setActiveAgent(acpAgents[0].env_key || acpAgents[0].type);
      }
    } catch (err) {
      console.error('Failed to load ACP settings:', err);
    } finally {
      setLoading(false);
    }
  };

  const getEnvVars = (): EnvVar[] => envMap[activeAgent] || [{ key: '', value: '', enabled: true }];

  const updateEnvVars = (vars: EnvVar[]) => {
    setEnvMap((prev) => ({ ...prev, [activeAgent]: vars }));
  };

  const handleAdd = () => {
    updateEnvVars([...getEnvVars(), { key: '', value: '', enabled: true }]);
  };

  const handleRemove = (index: number) => {
    const updated = getEnvVars().filter((_, i) => i !== index);
    if (updated.length === 0) updated.push({ key: '', value: '', enabled: true });
    updateEnvVars(updated);
  };

  const handleChange = (index: number, field: 'key' | 'value', val: string) => {
    const updated = [...getEnvVars()];
    updated[index] = { ...updated[index], [field]: val };
    updateEnvVars(updated);
  };

  const handleToggle = (index: number) => {
    const updated = [...getEnvVars()];
    updated[index] = { ...updated[index], enabled: !updated[index].enabled };
    updateEnvVars(updated);
  };

  const handleSave = async () => {
    setSaving(true);
    setMessage(null);

    try {
      const active = agents.find((agent) => (agent.env_key || agent.type) === activeAgent);
      if (!active) throw new Error(`Agent ${activeAgent} is not available`);
      const entries = getEnvVars()
        .filter((env) => env.key.trim())
        .map((env) => ({ key: env.key.trim(), value: env.value, enabled: env.enabled }));
      const res = await fetch(`/api/v1/config/settings/agent/${encodeURIComponent(active.agent_id)}/env`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ entries }),
      });
      const raw = await res.text();
      let data: { code?: number; msg?: string; error?: string; warning?: string } = {};
      if (raw) {
        try {
          data = JSON.parse(raw) as typeof data;
        } catch (err) {
          throw new Error(`HTTP ${res.status}: invalid JSON response: ${String(err)}\n${raw}`);
        }
      }
      if (res.ok && data.code === 0) {
        setMessage({
          type: data.warning ? 'error' : 'success',
          text: data.warning
            ? `${t('common.saveSuccess')}\n${String(data.warning)}`
            : t('common.saveSuccess'),
        });
      } else {
        throw new Error(data.msg || data.error || `HTTP ${res.status}: ${raw || res.statusText}`);
      }
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
          <h3 className="settings-section-title">{t('settings.acp.sectionTitle')}</h3>
          <p className="settings-section-desc">
            {t('settings.acp.noAgents')}
          </p>
        </section>
      </div>
    );
  }

  const currentVars = getEnvVars();

  return (
    <div className="account-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.acp.sectionTitle')}</h3>
        <p className="settings-section-desc">
          {t('settings.acp.sectionDesc')}
        </p>

        <div className="acp-agent-tabs">
          {agents.map((agent) => (
            <div
              key={agent.type}
              className={`acp-agent-tab ${activeAgent === (agent.env_key || agent.type) ? 'active' : ''}`}
              onClick={() => setActiveAgent(agent.env_key || agent.type)}
            >
              {agent.display_name}
            </div>
          ))}
        </div>

        <div className="acp-env-list">
          {currentVars.map((env, index) => (
            <div key={index} className={`acp-env-row ${!env.enabled ? 'acp-env-disabled' : ''}`}>
              <label className="acp-env-toggle" title={env.enabled ? t('settings.acp.activeTooltip') : t('settings.acp.inactiveTooltip')}>
                <input
                  type="checkbox"
                  checked={env.enabled}
                  onChange={() => handleToggle(index)}
                />
                <span className="acp-env-toggle-slider"></span>
              </label>
              <input
                type="text"
                className="settings-input acp-env-key"
                value={env.key}
                onChange={(e) => handleChange(index, 'key', e.target.value)}
                placeholder={t('settings.acp.varKeyPlaceholder')}
              />
              <span className="acp-env-eq">=</span>
              <input
                type="text"
                className="settings-input acp-env-value"
                value={env.value}
                onChange={(e) => handleChange(index, 'value', e.target.value)}
                placeholder={t('settings.acp.varValuePlaceholder')}
              />
              <button
                className="settings-btn settings-btn-danger acp-env-remove"
                onClick={() => handleRemove(index)}
                title={t('common.delete')}
              >
                ✕
              </button>
            </div>
          ))}
        </div>

        <div style={{ marginTop: '12px' }}>
          <button className="settings-btn settings-btn-secondary" onClick={handleAdd}>
            {t('settings.acp.addEnvVar')}
          </button>
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
