import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AgentInstallSettings } from './AgentInstallSettings';
import { ACPSettings } from './ACPSettings';
import { AgentDefaultsSettings } from './AgentDefaultsSettings';
import { AgentRoleSettings } from './AgentRoleSettings';
import { CodexAccountsSettings } from './CodexAccountsSettings';
import './AgentManagement.css';

const tabs = [
  { key: 'catalog', labelKey: 'settings.agentManagement.tabs.catalog' },
  { key: 'codex', labelKey: 'settings.codexAccounts.title' },
  { key: 'env', labelKey: 'settings.agentManagement.tabs.env' },
  { key: 'defaults', labelKey: 'settings.agentManagement.tabs.defaults' },
  { key: 'roles', labelKey: 'settings.agentManagement.tabs.roles' },
] as const;

type AgentManagementTab = (typeof tabs)[number]['key'];

export function AgentManagement({ onChanged }: { onChanged?: () => void }) {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<AgentManagementTab>('catalog');
  const tabId = useId();

  const openCodexAccounts = () => {
    setActiveTab('codex');
    document.getElementById(`${tabId}-codex`)?.focus();
  };

  const panels = {
    catalog: <AgentInstallSettings onOpenCodexAccounts={openCodexAccounts} />,
    codex: <CodexAccountsSettings onChanged={onChanged} />,
    env: <ACPSettings />,
    defaults: <AgentDefaultsSettings />,
    roles: <AgentRoleSettings />,
  };

  return (
    <div className="agent-management">
      <div className="agent-management-tabs" role="tablist" aria-label={t('settings.tabs.agents')}>
        {tabs.map((tab, index) => (
          <button
            type="button"
            role="tab"
            id={`${tabId}-${tab.key}`}
            aria-controls={`${tabId}-${tab.key}-panel`}
            aria-selected={activeTab === tab.key}
            tabIndex={activeTab === tab.key ? 0 : -1}
            key={tab.key}
            className={`agent-management-tab ${activeTab === tab.key ? 'active' : ''}`}
            onClick={() => setActiveTab(tab.key)}
            onFocus={(event) => event.currentTarget.scrollIntoView({ block: 'nearest', inline: 'nearest' })}
            onKeyDown={(event) => {
              let next: number;
              switch (event.key) {
                case 'ArrowRight': next = (index + 1) % tabs.length; break;
                case 'ArrowLeft': next = (index + tabs.length - 1) % tabs.length; break;
                case 'Home': next = 0; break;
                case 'End': next = tabs.length - 1; break;
                default: return;
              }
              event.preventDefault();
              document.getElementById(`${tabId}-${tabs[next].key}`)?.focus();
            }}
          >
            {t(tab.labelKey)}
          </button>
        ))}
      </div>
      {tabs.map(({ key }) => (
        <div key={key} className="agent-management-content" role="tabpanel" id={`${tabId}-${key}-panel`}
          aria-labelledby={`${tabId}-${key}`} hidden={key !== activeTab}>
          {key === activeTab && panels[key]}
        </div>
      ))}
    </div>
  );
}
