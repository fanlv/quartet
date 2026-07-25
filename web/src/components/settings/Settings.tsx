import { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { GeneralSettings } from './GeneralSettings';
import { TokenSettings } from './TokenSettings';
import { EinoSettings } from './EinoSettings';
import { PromptSettings } from './PromptSettings';
import { SkillSettings } from './SkillSettings';
import { ACPSettings } from './ACPSettings';
import { AgentDefaultsSettings } from './AgentDefaultsSettings';
import { LarkSettings } from './LarkSettings';
import { WeChatSettings } from './WeChatSettings';
import { WorkspacesSettings } from './WorkspacesSettings';
import { LogsSettings } from './LogsSettings';
import './Settings.css';
import './WeChatSettings.css';

type SettingsTab = 'general' | 'workspace' | 'token' | 'eino' | 'prompt' | 'skill' | 'acp' | 'agentDefaults' | 'lark' | 'wechat' | 'logs';

interface SettingsProps {
  onClose: () => void;
  onSettingsChanged?: () => void;
}

const tabDefs: { key: SettingsTab; labelKey: string; icon: string }[] = [
  { key: 'general', labelKey: 'settings.tabs.general', icon: '⚙️' },
  { key: 'workspace', labelKey: 'settings.tabs.workspace', icon: '🗂️' },
  { key: 'token', labelKey: 'settings.tabs.token', icon: '🔑' },
  { key: 'eino', labelKey: 'settings.tabs.eino', icon: '🤖' },
  { key: 'prompt', labelKey: 'settings.tabs.prompt', icon: '📝' },
  { key: 'skill', labelKey: 'settings.tabs.skill', icon: '🧩' },
  { key: 'acp', labelKey: 'settings.tabs.acp', icon: '🔌' },
  { key: 'agentDefaults', labelKey: 'settings.tabs.agentDefaults', icon: '⭐' },
  { key: 'lark', labelKey: 'settings.tabs.lark', icon: '💬' },
  { key: 'wechat', labelKey: 'settings.tabs.wechat', icon: '💚' },
  { key: 'logs', labelKey: 'settings.tabs.logs', icon: '📋' },
];

export function Settings({ onClose, onSettingsChanged }: SettingsProps) {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<SettingsTab>('general');
  const [keyboardOpen, setKeyboardOpen] = useState(false);
  const contentRef = useRef<HTMLDivElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);

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
      case 'token':
        return <TokenSettings />;
      case 'eino':
        return <EinoSettings />;
      case 'prompt':
        return <PromptSettings />;
      case 'skill':
        return <SkillSettings />;
      case 'acp':
        return <ACPSettings />;
      case 'agentDefaults':
        return <AgentDefaultsSettings />;
      case 'lark':
        return <LarkSettings />;
      case 'wechat':
        return <WeChatSettings />;
      case 'logs':
        return <LogsSettings />;
      default:
        return null;
    }
  };

  return (
    <div className="settings-overlay" onClick={onClose} data-testid="settings-overlay">
      <div
        className={`settings-modal ${keyboardOpen ? 'settings-modal-keyboard-open' : ''}`}
        ref={modalRef}
        onClick={(e) => e.stopPropagation()}
        data-testid="settings-modal"
      >
        <div className="settings-header">
          <h2 className="settings-title">{t('settings.title')}</h2>
          <button className="settings-close-btn" onClick={onClose} data-testid="settings-close-button">
            ×
          </button>
        </div>

        <div className="settings-body">
          <nav className="settings-nav">
            {tabDefs.map((tab) => (
              <div
                key={tab.key}
                className={`settings-nav-item ${activeTab === tab.key ? 'active' : ''}`}
                onClick={() => setActiveTab(tab.key)}
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
