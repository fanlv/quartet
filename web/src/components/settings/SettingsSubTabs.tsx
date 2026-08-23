interface SettingsSubTab<T extends string> {
  key: T;
  label: string;
}

interface SettingsSubTabsProps<T extends string> {
  activeTab: T;
  ariaLabel: string;
  group: string;
  onSelect: (tab: T) => void;
  tabs: SettingsSubTab<T>[];
}

export function SettingsSubTabs<T extends string>({
  activeTab,
  ariaLabel,
  group,
  onSelect,
  tabs,
}: SettingsSubTabsProps<T>) {
  return (
    <div className="settings-subtabs" role="tablist" aria-label={ariaLabel}>
      {tabs.map((tab) => (
        <button
          key={tab.key}
          type="button"
          role="tab"
          aria-selected={activeTab === tab.key}
          className={`settings-subtab ${activeTab === tab.key ? 'active' : ''}`}
          onClick={() => onSelect(tab.key)}
          data-settings-subtab={tab.key}
          data-settings-subtab-group={group}
        >
          {tab.label}
        </button>
      ))}
    </div>
  );
}
