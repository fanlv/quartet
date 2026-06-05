import { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { DEFAULT_WORKSPACE_ID } from '../../utils/workspace';
import './PromptSettings.css';

const API_BASE = '/api/v1/prompt';

type TabKey =
  | 'system_prompt'
  | 'group_chat_prompt'
  | 'home_agents_md'
  | 'SOUL'
  | 'USER'
  | 'MEMORY';

const GROUP_CHAT_VARIABLES = [
  { key: 'Platform', descKey: 'settings.prompt.vars.Platform' },
  { key: 'MessageID', descKey: 'settings.prompt.vars.MessageID' },
  { key: 'ParentID', descKey: 'settings.prompt.vars.ParentID' },
  { key: 'RootID', descKey: 'settings.prompt.vars.RootID' },
  { key: 'ChatID', descKey: 'settings.prompt.vars.ChatID' },
  { key: 'ChatType', descKey: 'settings.prompt.vars.ChatType' },
  { key: 'SenderID', descKey: 'settings.prompt.vars.SenderID' },
  { key: 'MessageType', descKey: 'settings.prompt.vars.MessageType' },
  { key: 'Content', descKey: 'settings.prompt.vars.Content' },
  { key: 'CreateTime', descKey: 'settings.prompt.vars.CreateTime' },
  { key: 'UpdateTime', descKey: 'settings.prompt.vars.UpdateTime' },
  { key: 'Mentions', descKey: 'settings.prompt.vars.Mentions' },
  { key: 'MentionsCount', descKey: 'settings.prompt.vars.MentionsCount' },
  { key: 'RawEvent', descKey: 'settings.prompt.vars.RawEvent' },
];

const PROMPT_REFERENCE_VARIABLES = [
  { key: 'SOUL', token: '{{SOUL.MD}}', descKey: 'settings.prompt.refs.SOUL' },
  { key: 'USER', token: '{{USER.MD}}', descKey: 'settings.prompt.refs.USER' },
  { key: 'MEMORY', token: '{{MEMORY.MD}}', descKey: 'settings.prompt.refs.MEMORY' },
];

type TabDef = {
  key: TabKey;
  labelKey: string;
  icon: string;
  titleKey: string;
  placeholderKey: string;
};

type TabGroup = {
  titleKey: string;
  tabs: TabDef[];
};

const TAB_GROUPS: TabGroup[] = [
  {
    titleKey: 'settings.prompt.groups.conversation',
    tabs: [
      {
        key: 'system_prompt',
        labelKey: 'settings.prompt.tabs.systemPrompt',
        icon: '💬',
        titleKey: 'settings.prompt.titles.systemPrompt',
        placeholderKey: 'settings.prompt.placeholders.systemPrompt',
      },
      {
        key: 'group_chat_prompt',
        labelKey: 'settings.prompt.tabs.groupChatPrompt',
        icon: '👥',
        titleKey: 'settings.prompt.titles.groupChatPrompt',
        placeholderKey: 'settings.prompt.placeholders.groupChatPrompt',
      },
    ],
  },
  {
    titleKey: 'settings.prompt.groups.workspace',
    tabs: [
      {
        key: 'home_agents_md',
        labelKey: 'settings.prompt.tabs.homeAgentsMd',
        icon: '🏠',
        titleKey: 'settings.prompt.titles.homeAgentsMd',
        placeholderKey: 'settings.prompt.placeholders.homeAgentsMd',
      },
    ],
  },
  {
    titleKey: 'settings.prompt.groups.memory',
    tabs: [
      {
        key: 'SOUL',
        labelKey: 'settings.prompt.tabs.soul',
        icon: '✨',
        titleKey: 'settings.prompt.titles.soul',
        placeholderKey: 'settings.prompt.placeholders.soul',
      },
      {
        key: 'USER',
        labelKey: 'settings.prompt.tabs.user',
        icon: '👤',
        titleKey: 'settings.prompt.titles.user',
        placeholderKey: 'settings.prompt.placeholders.user',
      },
      {
        key: 'MEMORY',
        labelKey: 'settings.prompt.tabs.memory',
        icon: '🧠',
        titleKey: 'settings.prompt.titles.memory',
        placeholderKey: 'settings.prompt.placeholders.memory',
      },
    ],
  },
];

const ALL_TABS: TabDef[] = TAB_GROUPS.flatMap((g) => g.tabs);

export function PromptSettings() {
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState<TabKey>('system_prompt');
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [savedTip, setSavedTip] = useState(false);
  const [defaultWorkdir, setDefaultWorkdir] = useState<string>('');
  const [promptPath, setPromptPath] = useState<string>('');
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const fetchSeqRef = useRef(0);

  const currentTab = useMemo(
    () => ALL_TABS.find((t) => t.key === activeTab) || ALL_TABS[0],
    [activeTab],
  );

  const promptReferenceVariables = useMemo(
    () => PROMPT_REFERENCE_VARIABLES.filter((variable) => variable.key !== activeTab),
    [activeTab],
  );

  const showPromptReferenceAssist = !['SOUL', 'USER', 'MEMORY'].includes(activeTab);

  const fetchPrompt = useCallback(async (key: TabKey) => {
    const seq = ++fetchSeqRef.current;
    try {
      setLoading(true);
      // Clear promptPath up-front so the info card doesn't briefly show
      // the previous tab's path while the new fetch is in flight.
      setPromptPath('');
      const resp = await fetch(`${API_BASE}/get`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key }),
      });
      const data = await resp.json();
      if (seq !== fetchSeqRef.current) return;
      if (data.code !== 0) {
        throw new Error(t('settings.prompt.loadFailed'));
      }
      setContent(data.prompt || '');
      setPromptPath(data.path || '');
    } catch (e) {
      console.error('Failed to fetch prompt:', e);
      if (seq === fetchSeqRef.current) {
        setContent('');
        setPromptPath('');
      }
    } finally {
      if (seq === fetchSeqRef.current) {
        setLoading(false);
      }
    }
  }, [t]);

  useEffect(() => {
    fetchPrompt(activeTab);
  }, [activeTab, fetchPrompt]);

  // Fetch the default workspace's workdir so the AGENTS.md (默认工作空间) tab
  // can show the real on-disk paths the content is saved to.
  useEffect(() => {
    let cancelled = false;
    fetch('/api/v1/workspace/list')
      .then((r) => r.json())
      .then((d) => {
        if (cancelled) return;
        const list: Array<{ id: string; workdir: string }> = d?.workspaces || [];
        const def = list.find((ws) => ws.id === DEFAULT_WORKSPACE_ID);
        setDefaultWorkdir(def?.workdir || '');
      })
      .catch(() => {
        if (!cancelled) setDefaultWorkdir('');
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const handleInsertText = useCallback((insertText: string) => {
    const textarea = textareaRef.current;
    if (textarea) {
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      // Read the latest content from setContent's callback form so the
      // insertion always sees the up-to-date value instead of the one
      // captured by the useCallback closure. This lets us drop `content`
      // from the deps array, keeping handleInsertText stable across
      // keystrokes and avoiding useless re-renders of every child that
      // takes it as a prop (variable-assist tag buttons etc.).
      setContent((prev) => prev.slice(0, start) + insertText + prev.slice(end));
      requestAnimationFrame(() => {
        textarea.focus();
        const pos = start + insertText.length;
        textarea.setSelectionRange(pos, pos);
      });
      return;
    }
    setContent((prev) => prev + insertText);
  }, []);

  const handleInsertVariable = useCallback(
    (varKey: string) => {
      handleInsertText(`{{${varKey}}}`);
    },
    [handleInsertText],
  );

  const handleSave = async () => {
    try {
      setSaving(true);
      const resp = await fetch(`${API_BASE}/save`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ key: activeTab, prompt: content }),
      });
      const data = await resp.json();
      if (data.code !== 0) {
        throw new Error(t('settings.prompt.saveFailed'));
      }
      setSavedTip(true);
      setTimeout(() => setSavedTip(false), 2000);
    } catch (e) {
      console.error('Failed to save prompt:', e);
      alert(t('settings.prompt.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  // Render description / file path as structured info, not just a sentence.
  const renderInfo = () => {
    switch (activeTab) {
      case 'system_prompt':
        return {
          desc: t('settings.prompt.descriptions.systemPrompt'),
          path: null,
        };
      case 'group_chat_prompt':
        return {
          desc: t('settings.prompt.descriptions.groupChatPrompt'),
          path: null,
        };
      case 'home_agents_md':
        return {
          desc: t('settings.prompt.descriptions.homeAgentsMd'),
          path: defaultWorkdir
            ? `${defaultWorkdir}/AGENTS.md`
            : null,
        };
      case 'SOUL':
        return {
          desc: t('settings.prompt.descriptions.soul'),
          path: promptPath || null,
        };
      case 'USER':
        return {
          desc: t('settings.prompt.descriptions.user'),
          path: promptPath || null,
        };
      case 'MEMORY':
        return {
          desc: t('settings.prompt.descriptions.memory'),
          path: promptPath || null,
        };
    }
  };

  const info = renderInfo();
  const charCount = content.length;
  const lineCount = content ? content.split('\n').length : 0;

  return (
    <div className="prompt-settings">
      <div className="prompt-tab-groups">
        {TAB_GROUPS.map((group) => (
          <div key={group.titleKey} className="prompt-tab-group">
            <div className="prompt-tab-group-title">{t(group.titleKey)}</div>
            <div className="prompt-tab-group-items">
              {group.tabs.map((tab) => (
                <button
                  key={tab.key}
                  className={`prompt-tab-pill ${activeTab === tab.key ? 'active' : ''}`}
                  onClick={() => setActiveTab(tab.key)}
                  type="button"
                >
                  <span className="prompt-tab-pill-icon">{tab.icon}</span>
                  <span className="prompt-tab-pill-label">{t(tab.labelKey)}</span>
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>

      <section className="settings-section prompt-section">
        <div className="prompt-info-card">
          <div className="prompt-info-card-main">
            <div className="prompt-info-card-title">
              <span className="prompt-info-card-icon">{currentTab.icon}</span>
              <span>{t(currentTab.titleKey)}</span>
            </div>
            <div className="prompt-info-card-desc">{info.desc}</div>
          </div>
          {info.path && (
            <div className="prompt-info-card-path" title={info.path}>
              <span className="prompt-info-card-path-icon">📄</span>
              <code>{info.path}</code>
            </div>
          )}
        </div>

        <div className="prompt-editor">
          {loading ? (
            <div className="prompt-loading">{t('common.loading')}</div>
          ) : (
            <>
              <div className="prompt-editor-header">
                <span className="prompt-editor-header-label">{t(currentTab.labelKey)}</span>
                <span className="prompt-editor-header-stats">
                  {t('settings.prompt.linesAndChars', { lines: lineCount, chars: charCount })}
                </span>
              </div>
              <textarea
                ref={textareaRef}
                className="prompt-textarea"
                value={content}
                onChange={(e) => setContent(e.target.value)}
                placeholder={t(currentTab.placeholderKey)}
                spellCheck={false}
              />
              {(activeTab === 'group_chat_prompt' || (showPromptReferenceAssist && promptReferenceVariables.length > 0)) && (
                <div className="prompt-variable-assist">
                  {showPromptReferenceAssist && promptReferenceVariables.length > 0 && (
                    <div className="prompt-variable-assist-section">
                      <div className="prompt-variable-assist-top">
                        <span className="prompt-variable-assist-label">{t('settings.prompt.memoryReference')}</span>
                        <span className="prompt-variable-assist-hint">
                          {t('settings.prompt.clickToInsert')}
                        </span>
                      </div>
                      <div className="prompt-var-tags">
                        {promptReferenceVariables.map((variable) => (
                          <button
                            key={variable.key}
                            className="prompt-var-tag"
                            onClick={() => handleInsertText(variable.token)}
                            title={`${t(variable.descKey)} - ${variable.token}`}
                            type="button"
                          >
                            <span className="prompt-var-tag-key">{variable.token}</span>
                            <span className="prompt-var-tag-desc">{t(variable.descKey)}</span>
                          </button>
                        ))}
                      </div>
                    </div>
                  )}
                  {activeTab === 'group_chat_prompt' && (
                    <div className="prompt-variable-assist-section">
                      <div className="prompt-variable-assist-top">
                        <span className="prompt-variable-assist-label">{t('settings.prompt.groupChatVariables')}</span>
                        <span className="prompt-variable-assist-hint">
                          {t('settings.prompt.clickToInsert')}
                        </span>
                      </div>
                      <div className="prompt-var-tags">
                        {GROUP_CHAT_VARIABLES.map((variable) => (
                          <button
                            key={variable.key}
                            className="prompt-var-tag"
                            onClick={() => handleInsertVariable(variable.key)}
                            title={`${t(variable.descKey)} - {{${variable.key}}}`}
                            type="button"
                          >
                            <span className="prompt-var-tag-key">{`{{${variable.key}}}`}</span>
                            <span className="prompt-var-tag-desc">{t(variable.descKey)}</span>
                          </button>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              )}
            </>
          )}
        </div>

        <div className="prompt-footer">
          <div className="prompt-footer-left">
            {savedTip && <span className="prompt-saved-tip">{t('settings.prompt.saved')}</span>}
          </div>
          <button
            className="settings-btn settings-btn-primary"
            onClick={handleSave}
            disabled={saving || loading}
          >
            {saving ? t('common.saving') : t('settings.prompt.saveChanges')}
          </button>
        </div>
      </section>
    </div>
  );
}
