import { useEffect, useMemo, useRef, useState } from 'react';
import type { Dispatch, SetStateAction } from 'react';
import { useTranslation } from 'react-i18next';
import type { AgentInfo } from '../ChatPage';
import { useACPThoughtLevels } from '../../hooks/useACPThoughtLevels';
import { isImageUrl, resolveIconSrc } from '../../utils/url';

// RoleConfig is the shared shape for all three agent roles. Title / group-reply
// run through the headless `bin -p` path, which honors model_id and
// acp_thought_level (eino-cli's --model/--thought) but has no session mode;
// only the IM session agent uses acp_mode.
interface RoleConfig {
  agent_id: string;
  model_id: string;
  acp_mode: string;
  acp_thought_level: string;
}

interface AgentCatalogItem {
  agent_id: string;
  supports_headless_print: boolean;
}

const emptyConfig: RoleConfig = {
  agent_id: '',
  model_id: '',
  acp_mode: '',
  acp_thought_level: '',
};

async function responseData(res: Response): Promise<Record<string, unknown>> {
  const raw: unknown = await res.json().catch(() => ({}));
  const data = raw && typeof raw === 'object' ? raw as Record<string, unknown> : {};
  if (!res.ok || data.code !== 0) {
    const detail = [data.msg, data.message, data.error].find((value): value is string => typeof value === 'string');
    throw new Error(detail || `HTTP ${res.status}`);
  }
  return data;
}

function optionIcon(iconUrl: string | undefined) {
  if (!iconUrl) return null;
  return isImageUrl(iconUrl)
    ? <img src={resolveIconSrc(iconUrl)} alt="" className="model-dropdown-icon" referrerPolicy="no-referrer" />
    : <span className="model-dropdown-emoji">{iconUrl}</span>;
}

interface PillOption {
  value: string;
  label: string;
  iconUrl?: string;
}

interface PillSelectProps {
  value: string;
  options: PillOption[];
  onSelect: (value: string) => void;
  // withIcons renders the icon column (agent picker); model/mode/level pickers
  // are text-only.
  withIcons?: boolean;
}

// PillSelect is the custom pill-style dropdown shared by every role selector,
// reusing the `.settings-model-*` styles. It owns its open state and closes on
// outside click.
function PillSelect({ value, options, onSelect, withIcons }: PillSelectProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handler = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [open]);

  const current = options.find((option) => option.value === value);

  return (
    <div className="settings-model-selector" ref={ref}>
      <div className="settings-model-tag" onClick={() => setOpen(!open)}>
        {withIcons && optionIcon(current?.iconUrl)}
        <span className="settings-model-tag-text">{current?.label ?? value}</span>
        <svg className="settings-model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </div>
      {open && (
        <div className="settings-model-dropdown">
          {options.map((option) => {
            const isActive = option.value === value;
            return (
              <div
                key={option.value || '__empty__'}
                className={`settings-model-dropdown-item${isActive ? ' active' : ''}`}
                onClick={() => { onSelect(option.value); setOpen(false); }}
              >
                {withIcons && option.value !== '' && (optionIcon(option.iconUrl) ?? <div className="model-dropdown-icon-placeholder" />)}
                <div className="settings-model-dropdown-info">
                  <span className="settings-model-dropdown-name">{option.label}</span>
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
  );
}

interface RoleAgentSelectorProps {
  label: string;
  agentPool: AgentInfo[];
  config: RoleConfig;
  onChange: Dispatch<SetStateAction<RoleConfig>>;
  showMode: boolean;
}

// RoleAgentSelector renders the agent picker plus the model / (optional) mode /
// thought-level selectors for one role. It owns the model-linked thought-level
// refresh so each role can select an agent+model pair independently.
function RoleAgentSelector({ label, agentPool, config, onChange, showMode }: RoleAgentSelectorProps) {
  const { t } = useTranslation();
  const selectedAgent = agentPool.find((agent) => agent.agent_id === config.agent_id);
  const modelID = config.model_id || selectedAgent?.models?.currentModelId || '';
  const thoughtLevelLink = useACPThoughtLevels(
    selectedAgent?.type || '',
    modelID,
    Boolean(selectedAgent?.models),
    !config.model_id || config.model_id === selectedAgent?.models?.currentModelId
      ? selectedAgent?.thoughtLevels || null
      : null,
  );

  useEffect(() => {
    const state = thoughtLevelLink.state;
    if (!state) return;
    onChange((current) => {
      if (current.agent_id !== selectedAgent?.agent_id) return current;
      const currentStillAvailable = state.availableThoughtLevels.some(
        (level) => level.id === current.acp_thought_level,
      );
      const nextThoughtLevel = currentStillAvailable
        ? current.acp_thought_level
        : state.currentThoughtLevelId;
      return nextThoughtLevel === current.acp_thought_level
        ? current
        : { ...current, acp_thought_level: nextThoughtLevel };
    });
  }, [selectedAgent?.agent_id, thoughtLevelLink.state, onChange]);

  const selectAgent = (agentID: string) => {
    const selected = agentPool.find((agent) => agent.agent_id === agentID);
    onChange(selected ? {
      agent_id: agentID,
      model_id: selected.models?.currentModelId || selected.model_id || '',
      acp_mode: showMode ? (selected.modes?.currentModeId || '') : '',
      acp_thought_level: selected.thoughtLevels?.currentThoughtLevelId || '',
    } : { ...emptyConfig, agent_id: agentID });
  };

  const models = selectedAgent?.models?.availableModels || [];
  const modes = selectedAgent?.modes?.availableModes || [];
  const thoughtLevels = thoughtLevelLink.state?.availableThoughtLevels || [];

  const agentOptions: PillOption[] = [
    { value: '', label: t('common.notSet') },
    // Preserve a saved-but-unavailable selection so it stays visible.
    ...(config.agent_id && !agentPool.some((agent) => agent.agent_id === config.agent_id)
      ? [{ value: config.agent_id, label: config.agent_id }]
      : []),
    ...agentPool.map((agent) => ({
      value: agent.agent_id,
      label: agent.display_name,
      iconUrl: agent.icon_url,
    })),
  ];

  return (
    <div className="settings-form-group">
      <label className="settings-label">{label}</label>
      <div className="settings-agent-selectors">
        <PillSelect
          value={config.agent_id}
          options={agentOptions}
          onSelect={selectAgent}
          withIcons
        />

        {selectedAgent && models.length > 0 && (
          <PillSelect
            value={modelID}
            options={models.map((model) => ({ value: model.modelId, label: model.name }))}
            onSelect={(value) => onChange((current) => ({
              ...current,
              model_id: value,
              acp_thought_level: '',
            }))}
          />
        )}

        {showMode && selectedAgent && modes.length > 0 && (
          <PillSelect
            value={config.acp_mode}
            options={[
              { value: '', label: t('common.default') },
              ...modes.map((mode) => ({ value: mode.id, label: mode.name })),
            ]}
            onSelect={(value) => onChange((current) => ({ ...current, acp_mode: value }))}
          />
        )}

        {selectedAgent && thoughtLevels.length > 0 && (
          <PillSelect
            value={config.acp_thought_level}
            options={[
              { value: '', label: t('common.default') },
              ...thoughtLevels.map((level) => ({ value: level.id, label: level.name })),
            ]}
            onSelect={(value) => onChange((current) => ({
              ...current,
              acp_thought_level: value,
            }))}
          />
        )}
      </div>
      {thoughtLevelLink.loading && <span className="settings-switch-desc">{t('common.loading')}</span>}
      {thoughtLevelLink.error && (
        <div className="settings-message error" role="alert">{thoughtLevelLink.error}</div>
      )}
    </div>
  );
}

function toRoleConfig(config: Partial<RoleConfig> | null | undefined): RoleConfig {
  return {
    agent_id: config?.agent_id || '',
    model_id: config?.model_id || '',
    acp_mode: config?.acp_mode || '',
    acp_thought_level: config?.acp_thought_level || '',
  };
}

// AgentRoleSettings renders the title / group-reply / IM-session role pickers
// and persists all three through its own save button.
export function AgentRoleSettings() {
  const { t } = useTranslation();
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [headlessAgentIDs, setHeadlessAgentIDs] = useState<Set<string>>(new Set());
  const [titleConfig, setTitleConfig] = useState<RoleConfig>(emptyConfig);
  const [groupConfig, setGroupConfig] = useState<RoleConfig>(emptyConfig);
  const [imConfig, setIMConfig] = useState<RoleConfig>(emptyConfig);
  const [loading, setLoading] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null);

  const oneShotAgents = useMemo(
    () => agents.filter((agent) => headlessAgentIDs.has(agent.agent_id)),
    [agents, headlessAgentIDs],
  );

  useEffect(() => {
    const load = async () => {
      try {
        const [agentRes, catalogRes, titleRes, groupRes, imRes] = await Promise.all([
          fetch('/api/v1/agent/list'),
          fetch('/api/v1/agent/catalog'),
          fetch('/api/v1/config/settings/title-generation-agent'),
          fetch('/api/v1/config/settings/group-reply-agent'),
          fetch('/api/v1/config/settings/im-session-agent'),
        ]);
        const [agentData, catalogData, titleData, groupData, imData] = await Promise.all([
          responseData(agentRes),
          responseData(catalogRes),
          responseData(titleRes),
          responseData(groupRes),
          responseData(imRes),
        ]);
        const agentList = Array.isArray(agentData.agent_list)
          ? (agentData.agent_list as AgentInfo[]).filter((agent) => agent.available !== false)
          : [];
        const catalogItems = Array.isArray(catalogData.agents) ? catalogData.agents as AgentCatalogItem[] : [];
        setAgents(agentList);
        setHeadlessAgentIDs(new Set(
          catalogItems
            .filter((agent) => agent.supports_headless_print)
            .map((agent) => agent.agent_id),
        ));
        setTitleConfig(toRoleConfig(titleData.config as Partial<RoleConfig> | null | undefined));
        setGroupConfig(toRoleConfig(groupData.config as Partial<RoleConfig> | null | undefined));
        setIMConfig(toRoleConfig(imData.config as Partial<RoleConfig> | null | undefined));
        setLoaded(true);
      } catch (err) {
        setLoadError(err instanceof Error ? err.message : String(err));
      } finally {
        setLoading(false);
      }
    };
    void load();
  }, []);

  const handleSave = async () => {
    if (!loaded) return;
    setSaving(true);
    setMessage(null);
    const requests: Array<[string, object]> = [
      ['/api/v1/config/settings/title-generation-agent', {
        agent_id: titleConfig.agent_id,
        model_id: titleConfig.model_id,
        acp_thought_level: titleConfig.acp_thought_level,
      }],
      ['/api/v1/config/settings/group-reply-agent', {
        agent_id: groupConfig.agent_id,
        model_id: groupConfig.model_id,
        acp_thought_level: groupConfig.acp_thought_level,
      }],
      ['/api/v1/config/settings/im-session-agent', imConfig],
    ];
    const errors: string[] = [];
    for (const [url, body] of requests) {
      try {
        const res = await fetch(url, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        await responseData(res);
      } catch (err) {
        errors.push(`${url}: ${err instanceof Error ? err.message : String(err)}`);
      }
    }
    if (errors.length > 0) {
      setMessage({ type: 'error', text: errors.join('\n') });
    } else {
      setMessage({ type: 'success', text: t('common.saveSuccess') });
    }
    setSaving(false);
  };

  if (loading) {
    return <div className="account-settings"><section className="settings-section"><p>{t('common.loading')}</p></section></div>;
  }

  return (
    <div className="account-settings" data-testid="agent-role-settings">
      <section className="settings-section">
        <h3 className="settings-section-title">{t('settings.general.agentRoles')}</h3>

        {loadError && <div className="settings-message error" role="alert">{loadError}</div>}

        <RoleAgentSelector
          label={t('settings.general.titleGenerationAgent')}
          agentPool={oneShotAgents}
          config={titleConfig}
          onChange={setTitleConfig}
          showMode={false}
        />

        <RoleAgentSelector
          label={t('settings.general.groupReplyAgent')}
          agentPool={oneShotAgents}
          config={groupConfig}
          onChange={setGroupConfig}
          showMode={false}
        />

        <RoleAgentSelector
          label={t('settings.general.imSessionAgent')}
          agentPool={agents}
          config={imConfig}
          onChange={setIMConfig}
          showMode
        />

        {message && (
          <div className={`settings-message ${message.type}`}>{message.text}</div>
        )}

        <div className="settings-btn-group">
          <button
            className="settings-btn settings-btn-primary"
            onClick={handleSave}
            disabled={saving}
            data-testid="agent-role-save-button"
          >
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </section>
    </div>
  );
}
