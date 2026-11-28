import { GeneralSettings } from './GeneralSettings';
import { AccountManagement } from './AccountManagement';
import { EinoSettings } from './EinoSettings';
import { PromptSettings } from './PromptSettings';
import { SkillSettings } from './SkillSettings';
import { AgentManagement } from './AgentManagement';
import { IMSettings } from './IMSettings';
import { WorkspacesSettings } from './WorkspacesSettings';
import { LogsSettings } from './LogsSettings';
import { MessagePresetSettings } from './MessagePresetSettings';
import type { SettingsNavTab, SettingsTab } from './Settings';
import './WeChatSettings.css';
import './AuthManagement.css';

interface SettingsPanelProps {
  activeTab: SettingsNavTab;
  initialTab: SettingsTab;
  workspaceId?: string;
  onSettingsChanged?: () => void;
  onMessagePresetsDirtyChange: (dirty: boolean) => void;
}

export function SettingsPanel({ activeTab, initialTab, workspaceId, onSettingsChanged, onMessagePresetsDirtyChange }: SettingsPanelProps) {
  switch (activeTab) {
    case 'general':
      return <GeneralSettings onSettingsChanged={onSettingsChanged} />;
    case 'workspace':
      return <WorkspacesSettings />;
    case 'message-presets':
      return <MessagePresetSettings onDirtyChange={onMessagePresetsDirtyChange} />;
    case 'account':
      return <AccountManagement initialTab={initialTab === 'users' || initialTab === 'roles' ? initialTab : 'account'} />;
    case 'eino':
      return <EinoSettings />;
    case 'prompt':
      return <PromptSettings />;
    case 'skill':
      return <SkillSettings workspaceId={workspaceId} />;
    case 'agents':
      return <AgentManagement onChanged={onSettingsChanged} />;
    case 'im':
      return <IMSettings initialTab={initialTab === 'wechat' ? 'wechat' : 'lark'} />;
    case 'logs':
      return <LogsSettings />;
  }
}
