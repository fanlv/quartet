import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AgentInstallSettings } from './AgentInstallSettings';
import { ACPSettings } from './ACPSettings';
import { AgentDefaultsSettings } from './AgentDefaultsSettings';
import { AgentRoleSettings } from './AgentRoleSettings';
import './AgentManagement.css';

type AgentManagementTab = 'catalog' | 'env' | 'defaults' | 'roles';

export function AgentManagement() {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<AgentManagementTab>('catalog');

  const tabs: { key: AgentManagementTab; labelKey: string }[] = [
    { key: 'catalog', labelKey: 'settings.agentManagement.tabs.catalog' },
    { key: 'env', labelKey: 'settings.agentManagement.tabs.env' },
    { key: 'defaults', labelKey: 'settings.agentManagement.tabs.defaults' },
    { key: 'roles', labelKey: 'settings.agentManagement.tabs.roles' },
  ];

  return (
    <div className="agent-management">
      <div className="agent-management-tabs">
        {tabs.map((tab) => (
          <div
            key={tab.key}
            className={`agent-management-tab ${activeTab === tab.key ? 'active' : ''}`}
            onClick={() => setActiveTab(tab.key)}
          >
            {t(tab.labelKey)}
          </div>
        ))}
      </div>
      <div className="agent-management-content">
        {activeTab === 'catalog' && <AgentInstallSettings />}
        {activeTab === 'env' && <ACPSettings />}
        {activeTab === 'defaults' && <AgentDefaultsSettings />}
        {activeTab === 'roles' && <AgentRoleSettings />}
      </div>
    </div>
  );
}
