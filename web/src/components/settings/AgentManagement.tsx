import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AgentInstallSettings } from './AgentInstallSettings';
import { ACPSettings } from './ACPSettings';
import { AgentDefaultsSettings } from './AgentDefaultsSettings';
import { AgentRoleSettings } from './AgentRoleSettings';
import { CodexAccountsSettings } from './CodexAccountsSettings';
import './AgentManagement.css';

type AgentManagementTab = 'catalog' | 'codex' | 'env' | 'defaults' | 'roles';

export function AgentManagement({ onChanged }: { onChanged?: () => void }) {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<AgentManagementTab>('catalog');
  const tabId = useId();

  const tabs: { key: AgentManagementTab; labelKey: string }[] = [
    { key: 'catalog', labelKey: 'settings.agentManagement.tabs.catalog' },
    { key: 'codex', labelKey: 'settings.codexAccounts.title' },
    { key: 'env', labelKey: 'settings.agentManagement.tabs.env' },
    { key: 'defaults', labelKey: 'settings.agentManagement.tabs.defaults' },
    { key: 'roles', labelKey: 'settings.agentManagement.tabs.roles' },
  ];

  return (
    <div className="agent-management">
      <div className="agent-management-tabs" role="tablist" aria-label={t('settings.tabs.agents')}>
        {tabs.map((tab) => (
          <button
            type="button"
            role="tab"
            id={`${tabId}-${tab.key}`}
            aria-controls={`${tabId}-panel`}
            aria-selected={activeTab === tab.key}
            tabIndex={activeTab === tab.key ? 0 : -1}
            key={tab.key}
            className={`agent-management-tab ${activeTab === tab.key ? 'active' : ''}`}
            onClick={() => setActiveTab(tab.key)}
            onKeyDown={(event) => {
              const index = tabs.findIndex((item) => item.key === tab.key);
              const next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : event.key === 'ArrowLeft' ? (index + tabs.length - 1) % tabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : -1;
              if (next < 0) return;
              event.preventDefault(); setActiveTab(tabs[next].key);
              document.getElementById(`${tabId}-${tabs[next].key}`)?.focus();
            }}
          >
            {t(tab.labelKey)}
          </button>
        ))}
      </div>
      <div className="agent-management-content" role="tabpanel" id={`${tabId}-panel`} aria-labelledby={`${tabId}-${activeTab}`}>
        {activeTab === 'catalog' && <AgentInstallSettings />}
        {activeTab === 'codex' && <CodexAccountsSettings onChanged={onChanged} />}
        {activeTab === 'env' && <ACPSettings />}
        {activeTab === 'defaults' && <AgentDefaultsSettings />}
        {activeTab === 'roles' && <AgentRoleSettings />}
      </div>
    </div>
  );
}
