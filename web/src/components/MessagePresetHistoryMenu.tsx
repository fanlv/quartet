import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useMessagePresets } from '../hooks/useMessagePresets';
import type { MessagePreset } from '../utils/messagePresets';
import type { FileAttachment } from '../types';

export interface SentMessageHistoryItem {
  id: string;
  ts: number;
  content: string;
  imageUrls?: string[];
  fileAttachments?: FileAttachment[];
}

interface Props {
  workspaceId?: string;
  disabled?: boolean;
  isMobile: boolean;
  currentInput: string;
  historyItems: SentMessageHistoryItem[];
  onApplyPreset: (content: string, mode: 'replace' | 'append') => void;
  onApplyHistory: (item: SentMessageHistoryItem) => void;
}

function previewText(content: string) {
  const value = content.replace(/\s+/g, ' ').trim();
  return value.length > 120 ? `${value.slice(0, 120)}…` : value;
}

export function MessagePresetHistoryMenu({
  workspaceId,
  disabled = false,
  isMobile,
  currentInput,
  historyItems,
  onApplyPreset,
  onApplyHistory,
}: Props) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [pendingPreset, setPendingPreset] = useState<MessagePreset | null>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const presets = useMessagePresets(workspaceId, open && !!workspaceId && !disabled);

  useEffect(() => {
    if (!open) return;
    const closeOnOutsideClick = (event: MouseEvent) => {
      const target = event.target as HTMLElement;
      if (target.closest?.('.mobile-dropdown-overlay') || target.closest?.('.preset-apply-dialog')) return;
      if (!rootRef.current?.contains(target)) setOpen(false);
    };
    document.addEventListener('mousedown', closeOnOutsideClick);
    return () => document.removeEventListener('mousedown', closeOnOutsideClick);
  }, [open]);

  useEffect(() => {
    setOpen(false);
    setPendingPreset(null);
  }, [workspaceId]);

  const selectPreset = (preset: MessagePreset) => {
    if (currentInput.length > 0) {
      setPendingPreset(preset);
      return;
    }
    onApplyPreset(preset.content, 'replace');
    setOpen(false);
  };

  const applyPending = (mode: 'replace' | 'append') => {
    if (!pendingPreset) return;
    onApplyPreset(pendingPreset.content, mode);
    setPendingPreset(null);
    setOpen(false);
  };

  const renderPresetGroup = (title: string, items: MessagePreset[], scope: 'project' | 'global') => {
    if (items.length === 0) return null;
    return <section className="chat-history-group" aria-label={title}>
      <div className="chat-history-group-title">{title}</div>
      {items.map((item) => <button
        type="button"
        role="option"
        key={`${scope}:${item.id}`}
        className="chat-history-item"
        title={item.content}
        onClick={() => selectPreset(item)}
      >
        <span className="chat-history-text">{item.name || previewText(item.content) || t('chat.presetEmpty')}</span>
        <span className={`chat-history-badge chat-history-badge-${scope}`}>{title}</span>
        {item.content.trimStart().startsWith('/') && <span className="chat-history-badge">{t('chat.commandText')}</span>}
      </button>)}
    </section>;
  };

  const content = <div className={`chat-history-dropdown${isMobile ? ' chat-history-dropdown-mobile' : ''}`} role="listbox" aria-label={t('chat.presetsAndHistory')}>
    {presets.loading && <div className="chat-history-empty">{t('common.loading')}</div>}
    {presets.errors.map((error) => <div className="chat-history-error" key={`${error.scope}:${error.file}:${error.error}`}>{error.file ? `${error.file}: ` : ''}{error.error}</div>)}
    {renderPresetGroup(t('chat.currentProjectPresets'), presets.project, 'project')}
    {renderPresetGroup(t('chat.globalPresets'), presets.global, 'global')}
    {historyItems.length > 0 && <section className="chat-history-group" aria-label={t('chat.recentSent')}>
      <div className="chat-history-group-title">{t('chat.recentSent')}</div>
      {historyItems.map((item) => {
        const imageCount = item.imageUrls?.length || 0;
        const fileCount = item.fileAttachments?.length || 0;
        return <button type="button" role="option" key={item.id} className="chat-history-item" title={item.content} onClick={() => { onApplyHistory(item); setOpen(false); }}>
          <span className="chat-history-text">{previewText(item.content) || t('chat.presetEmpty')}</span>
          {imageCount > 0 && <span className="chat-history-badge">🖼️{imageCount}</span>}
          {fileCount > 0 && <span className="chat-history-badge">📎{fileCount}</span>}
        </button>;
      })}
    </section>}
    {!presets.loading && presets.errors.length === 0 && presets.project.length === 0 && presets.global.length === 0 && historyItems.length === 0 && <div className="chat-history-empty">{t('chat.noPresetsOrHistory')}</div>}
  </div>;

  return <div className="chat-model-selector chat-history-selector" ref={rootRef}>
    <button type="button" className="chat-btn history-btn" onClick={() => !disabled && setOpen((value) => !value)} disabled={disabled} title={t('chat.presetsAndHistory')} aria-label={t('chat.presetsAndHistory')}>
      <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M12 8v5l3 2" /><circle cx="12" cy="12" r="9" /></svg>
    </button>
    {open && (isMobile ? createPortal(<div className="mobile-dropdown-overlay" onClick={() => setOpen(false)}><div className="mobile-dropdown-sheet" onClick={(event) => event.stopPropagation()}>{content}</div></div>, document.body) : content)}
    {pendingPreset && createPortal(<div className="preset-apply-overlay" onClick={() => setPendingPreset(null)}><div className="preset-apply-dialog" role="dialog" aria-modal="true" aria-label={t('chat.applyPresetTitle')} onClick={(event) => event.stopPropagation()}>
      <h3>{t('chat.applyPresetTitle')}</h3><p>{t('chat.applyPresetDescription')}</p>
      <div className="preset-apply-actions"><button type="button" onClick={() => setPendingPreset(null)}>{t('common.cancel')}</button><button type="button" onClick={() => applyPending('append')}>{t('chat.appendPreset')}</button><button type="button" className="primary" onClick={() => applyPending('replace')}>{t('chat.replacePreset')}</button></div>
    </div></div>, document.body)}
  </div>;
}
