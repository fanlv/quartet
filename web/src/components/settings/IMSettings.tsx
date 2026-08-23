import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAuthPrincipal } from '../../auth';
import { LarkSettings } from './LarkSettings';
import { SettingsSubTabs } from './SettingsSubTabs';
import { WeChatSettings } from './WeChatSettings';

export type IMSettingsTab = 'lark' | 'wechat';

interface IMSettingsProps {
  initialTab?: IMSettingsTab;
}

export function IMSettings({ initialTab = 'lark' }: IMSettingsProps) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const tabs = useMemo(() => [
    ...(principal?.permissions.includes('config.write')
      ? [{ key: 'lark' as const, label: t('settings.tabs.lark') }]
      : []),
    ...(principal?.permissions.includes('im.manage')
      ? [{ key: 'wechat' as const, label: t('settings.tabs.wechat') }]
      : []),
  ], [principal, t]);
  const [activeTab, setActiveTab] = useState<IMSettingsTab>(() =>
    tabs.some((tab) => tab.key === initialTab) ? initialTab : tabs[0]?.key ?? 'lark',
  );

  useEffect(() => {
    if (!tabs.some((tab) => tab.key === activeTab) && tabs[0]) setActiveTab(tabs[0].key);
  }, [activeTab, tabs]);

  return (
    <div className="settings-subtab-layout">
      <SettingsSubTabs
        activeTab={activeTab}
        ariaLabel={t('settings.tabs.im')}
        group="im"
        onSelect={setActiveTab}
        tabs={tabs}
      />
      <div className="settings-subtab-content" role="tabpanel" data-active-subtab={activeTab}>
        {activeTab === 'lark' && <LarkSettings />}
        {activeTab === 'wechat' && <WeChatSettings />}
      </div>
    </div>
  );
}
