import { useState, useEffect, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
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
import { useAuthPrincipal } from '../../auth';
import './Settings.css';
import './WeChatSettings.css';
import './AuthManagement.css';

export type SettingsTab = 'general' | 'workspace' | 'message-presets' | 'account' | 'users' | 'roles' | 'eino' | 'prompt' | 'skill' | 'agents' | 'im' | 'lark' | 'wechat' | 'logs';
type SettingsNavTab = Exclude<SettingsTab, 'users' | 'roles' | 'lark' | 'wechat'>;

interface SettingsProps {
  onClose: () => void;
  onSettingsChanged?: () => void;
  initialTab?: SettingsTab;
  /** Active workspace, used by the Skill tab to scope project-level skills to
   *  the directory agents actually run in. */
  workspaceId?: string;
}

const tabDefs: { key: SettingsNavTab; labelKey: string; icon: string; permission?: string; anyPermissions?: string[] }[] = [
  { key: 'general', labelKey: 'settings.tabs.general', icon: '⚙️', permission: 'config.write' },
  { key: 'workspace', labelKey: 'settings.tabs.workspace', icon: '🗂️', permission: 'workspace.write' },
  { key: 'message-presets', labelKey: 'settings.tabs.messagePresets', icon: '💬', anyPermissions: ['config.read', 'workspace.read'] },
  { key: 'account', labelKey: 'settings.tabs.accountManagement', icon: '👤' },
  { key: 'eino', labelKey: 'settings.tabs.eino', icon: '🤖', permission: 'config.write' },
  { key: 'prompt', labelKey: 'settings.tabs.prompt', icon: '📝', permission: 'config.write' },
  { key: 'skill', labelKey: 'settings.tabs.skill', icon: '🧩', permission: 'skills.read' },
  { key: 'agents', labelKey: 'settings.tabs.agents', icon: '📦', permission: 'agent.manage' },
  { key: 'im', labelKey: 'settings.tabs.im', icon: '💬', anyPermissions: ['config.write', 'im.manage'] },
  { key: 'logs', labelKey: 'settings.tabs.logs', icon: '📋', permission: 'logs.manage' },
];

export function Settings({ onClose, onSettingsChanged, initialTab = 'general', workspaceId }: SettingsProps) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const visibleTabs = useMemo(
    () => tabDefs.filter((tab) => {
      if (tab.permission && !principal?.permissions.includes(tab.permission)) return false;
      return !tab.anyPermissions || tab.anyPermissions.some((permission) => principal?.permissions.includes(permission));
    }),
    [principal],
  );
  const initialNavTab: SettingsNavTab = initialTab === 'users' || initialTab === 'roles'
    ? 'account'
    : initialTab === 'lark' || initialTab === 'wechat' ? 'im' : initialTab;
  const [activeTab, setActiveTab] = useState<SettingsNavTab>(() =>
    visibleTabs.some((tab) => tab.key === initialNavTab) ? initialNavTab : visibleTabs[0]?.key ?? 'account',
  );
  const [keyboardOpen, setKeyboardOpen] = useState(false);
  const [messagePresetsDirty, setMessagePresetsDirty] = useState(false);
  const contentRef = useRef<HTMLDivElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);

  const requestClose = () => {
    if (messagePresetsDirty && !window.confirm(t('settings.messagePresets.discardConfirm'))) return;
    onClose();
  };

  const selectTab = (tab: SettingsNavTab) => {
    if (tab === activeTab) return;
    if (activeTab === 'message-presets' && messagePresetsDirty && !window.confirm(t('settings.messagePresets.discardConfirm'))) return;
    if (activeTab === 'message-presets') setMessagePresetsDirty(false);
    setActiveTab(tab);
  };

  useEffect(() => {
    if (!visibleTabs.some((tab) => tab.key === activeTab)) {
      setActiveTab(visibleTabs[0]?.key ?? 'account');
    }
  }, [activeTab, visibleTabs]);

  // iPad/手机端：使用 visualViewport API 检测键盘弹出，动态调整模态框高度
  // 解决 position:fixed 元素不随键盘缩小导致输入框被遮挡的问题
  useEffect(() => {
    const vv = window.visualViewport;
    if (!vv) return;

    const handleResize = () => {
      const modal = modalRef.current;
      const overlay = modal?.parentElement;
      if (!modal || !overlay) return;
      const keyboardHeight = window.innerHeight - vv.height;
      const nextKeyboardOpen = keyboardHeight > 100;
      setKeyboardOpen(nextKeyboardOpen);

      if (nextKeyboardOpen) {
        // 键盘弹出 — 缩小模态框高度，overlay 顶部对齐
        const h = vv.height;
        modal.style.height = `${h - 20}px`;
        modal.style.maxHeight = `${h - 20}px`;
        overlay.style.alignItems = 'flex-start';
        overlay.style.paddingTop = `${vv.offsetTop + 10}px`;

        const activeEl = document.activeElement;
        if (activeEl instanceof HTMLInputElement || activeEl instanceof HTMLTextAreaElement || activeEl instanceof HTMLSelectElement) {
          window.setTimeout(() => {
            activeEl.scrollIntoView({ behavior: 'smooth', block: 'center' });
          }, 120);
        }
      } else {
        // 键盘收起 — 恢复默认
        modal.style.height = '';
        modal.style.maxHeight = '';
        overlay.style.alignItems = '';
        overlay.style.paddingTop = '';
      }
    };

    vv.addEventListener('resize', handleResize);
    vv.addEventListener('scroll', handleResize);
    handleResize();
    return () => {
      vv.removeEventListener('resize', handleResize);
      vv.removeEventListener('scroll', handleResize);
      setKeyboardOpen(false);
    };
  }, []);

  // 输入框获得焦点时滚动到可视区域
  useEffect(() => {
    const container = contentRef.current;
    if (!container) return;

    const handleFocus = (e: Event) => {
      const target = e.target as HTMLElement;
      if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA') {
        setTimeout(() => {
          target.scrollIntoView({ behavior: 'smooth', block: 'center' });
        }, 400);
      }
    };

    container.addEventListener('focusin', handleFocus);
    return () => container.removeEventListener('focusin', handleFocus);
  }, []);

  const renderContent = () => {
    switch (activeTab) {
      case 'general':
        return <GeneralSettings onSettingsChanged={onSettingsChanged} />;
      case 'workspace':
        return <WorkspacesSettings />;
      case 'message-presets':
        return <MessagePresetSettings onDirtyChange={setMessagePresetsDirty} />;
      case 'account':
        return <AccountManagement initialTab={initialTab === 'users' || initialTab === 'roles' ? initialTab : 'account'} />;
      case 'eino':
        return <EinoSettings />;
      case 'prompt':
        return <PromptSettings />;
      case 'skill':
        return <SkillSettings workspaceId={workspaceId} />;
      case 'agents':
        return <AgentManagement />;
      case 'im':
        return <IMSettings initialTab={initialTab === 'wechat' ? 'wechat' : 'lark'} />;
      case 'logs':
        return <LogsSettings />;
      default:
        return null;
    }
  };

  return (
    <div className="settings-overlay" onClick={requestClose} data-testid="settings-overlay">
      <div
        className={`settings-modal ${keyboardOpen ? 'settings-modal-keyboard-open' : ''}`}
        ref={modalRef}
        onClick={(e) => e.stopPropagation()}
        data-testid="settings-modal"
      >
        <div className="settings-header">
          <h2 className="settings-title">{t('settings.title')}</h2>
          <button className="settings-close-btn" onClick={requestClose} data-testid="settings-close-button">
            ×
          </button>
        </div>

        <div className="settings-body">
          <nav className="settings-nav">
            {visibleTabs.map((tab) => (
              <div
                key={tab.key}
                className={`settings-nav-item ${activeTab === tab.key ? 'active' : ''}`}
                onClick={() => selectTab(tab.key)}
                data-testid="settings-tab"
                data-settings-tab={tab.key}
                data-active={activeTab === tab.key ? 'true' : 'false'}
              >
                <span className="settings-nav-icon">{tab.icon}</span>
                <span className="settings-nav-label">{t(tab.labelKey)}</span>
              </div>
            ))}
          </nav>

          <div className="settings-content" ref={contentRef} data-testid="settings-content" data-active-tab={activeTab}>{renderContent()}</div>
        </div>
      </div>
    </div>
  );
}
