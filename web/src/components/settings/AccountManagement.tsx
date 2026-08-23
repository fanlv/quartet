import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuthPrincipal } from '../../auth';
import { AccountSettings } from './AccountSettings';
import { RoleManagement } from './RoleManagement';
import { SettingsSubTabs } from './SettingsSubTabs';
import { UserManagement } from './UserManagement';

export type AccountManagementTab = 'account' | 'roles' | 'users';

interface AccountManagementProps {
  initialTab?: AccountManagementTab;
}

export function AccountManagement({ initialTab = 'account' }: AccountManagementProps) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const tabs = useMemo(() => [
    { key: 'account' as const, label: t('settings.tabs.account') },
    ...(principal?.permissions.includes('roles.read')
      ? [{ key: 'roles' as const, label: t('settings.tabs.roles') }]
      : []),
    ...(principal?.permissions.includes('users.read')
      ? [{ key: 'users' as const, label: t('settings.tabs.users') }]
      : []),
  ], [principal, t]);
  const [activeTab, setActiveTab] = useState<AccountManagementTab>(() =>
    tabs.some((tab) => tab.key === initialTab) ? initialTab : 'account',
  );

  useEffect(() => {
    if (!tabs.some((tab) => tab.key === activeTab)) setActiveTab('account');
  }, [activeTab, tabs]);

  return (
    <div className="settings-subtab-layout">
      <SettingsSubTabs
        activeTab={activeTab}
        ariaLabel={t('settings.tabs.accountManagement')}
        group="account"
        onSelect={setActiveTab}
        tabs={tabs}
      />
      <div className="settings-subtab-content" role="tabpanel" data-active-subtab={activeTab}>
        {activeTab === 'account' && <AccountSettings />}
        {activeTab === 'roles' && <RoleManagement />}
        {activeTab === 'users' && <UserManagement />}
      </div>
    </div>
  );
}
