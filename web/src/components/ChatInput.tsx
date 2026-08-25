import { useCallback, useEffect, useRef, useState, KeyboardEvent } from 'react';
import type { CSSProperties } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { AgentInfo } from './ChatPage';
import { AgentUsageCard } from './AgentUsageCard';
import { FileMention, FileResult } from './FileMention';
import { SlashFloater, SkillBackdrop } from './SlashCompletion';
import { slashCompletionKeyDown, useSlashCompletion } from '../utils/slashCompletion';
import { isKnownCommand, isReadOnlyCommand } from '../utils/commands';
import { copyToClipboard } from '../utils/clipboard';
import { useIsMobile } from '../hooks/useIsMobile';
import { useGitBranch } from '../hooks/useGitBranch';
import { uploadChatAttachment, usePendingAttachments, type UploadedAttachment } from '../hooks/usePendingAttachments';
import { PendingAttachmentPreviews, UploadedFilePreviews } from './AttachmentPreviews';
import type { FileAttachment } from '../types';
import { workspaceColor } from '../utils/workspace';
import { formatTokenCount } from '../utils/statsFormat';
import { isImeComposing } from '../utils/keyboard';
import { isImageUrl, resolveIconSrc } from '../utils/url';
import { showToast } from '../utils/toast';
import { splitFavoriteModels } from '../utils/agentPrefs';
import { DurationBadge } from './DurationBadge';
import { MessagePresetHistoryMenu, type SentMessageHistoryItem } from './MessagePresetHistoryMenu';
import './ChatInput.css';

function toImagePreviewUrl(path: string): string {
  if (path.startsWith('http://') || path.startsWith('https://') || path.startsWith('data:') || path.startsWith('blob:')) return path;
  return `/api/v1/serve-file?path=${encodeURIComponent(path)}`;
}

type LocalSentMessage = SentMessageHistoryItem;

interface LocalSentMessagePayload {
  v: 1;
  items: LocalSentMessage[];
}

const LOCAL_SENT_MESSAGE_LIMIT = 50;

function safeJsonParse<T>(raw: string): T | null {
  try {
    return JSON.parse(raw) as T;
  } catch {
    return null;
  }
}

function genLocalId(): string {
  try {
    // modern browsers
    return crypto.randomUUID();
  } catch {
    return `${Date.now()}_${Math.random().toString(16).slice(2)}`;
  }
}

function readLocalSentMessages(storageKey: string): LocalSentMessage[] {
  try {
    const raw = localStorage.getItem(storageKey);
    if (!raw) return [];
    const parsed = safeJsonParse<LocalSentMessagePayload | LocalSentMessage[]>(raw);
    if (!parsed) return [];
    const items = Array.isArray(parsed)
      ? parsed
      : (parsed as LocalSentMessagePayload).v === 1 && Array.isArray((parsed as LocalSentMessagePayload).items)
      ? (parsed as LocalSentMessagePayload).items
      : [];
    return items
      .filter((it) => it && typeof it.content === 'string')
      .map((it) => ({
        id: typeof it.id === 'string' ? it.id : genLocalId(),
        ts: typeof it.ts === 'number' ? it.ts : Date.now(),
        content: String(it.content ?? ''),
        imageUrls: Array.isArray((it as LocalSentMessage).imageUrls)
          ? (it as LocalSentMessage).imageUrls!.filter((u) => typeof u === 'string')
          : undefined,
        fileAttachments: Array.isArray((it as LocalSentMessage).fileAttachments)
          ? (it as LocalSentMessage).fileAttachments!.filter((file) => file && typeof file.path === 'string' && typeof file.name === 'string')
          : undefined,
      }))
      .slice(0, LOCAL_SENT_MESSAGE_LIMIT);
  } catch {
    return [];
  }
}

function writeLocalSentMessages(storageKey: string, items: LocalSentMessage[]) {
  try {
    const payload: LocalSentMessagePayload = { v: 1, items: items.slice(0, LOCAL_SENT_MESSAGE_LIMIT) };
    localStorage.setItem(storageKey, JSON.stringify(payload));
  } catch {
    // ignore quota / private mode failures
  }
}

function appendLocalSentMessage(storageKey: string, item: Omit<LocalSentMessage, 'id' | 'ts'> & { id?: string; ts?: number }): LocalSentMessage[] {
  const nextItem: LocalSentMessage = {
    id: item.id || genLocalId(),
    ts: item.ts || Date.now(),
    content: item.content,
    imageUrls: item.imageUrls,
    fileAttachments: item.fileAttachments,
  };
  const prev = readLocalSentMessages(storageKey);
  const next = [nextItem, ...prev].slice(0, LOCAL_SENT_MESSAGE_LIMIT);
  writeLocalSentMessages(storageKey, next);
  return next;
}

interface QueuedMessageView {
  id: string;
  content: string;
  imageUrls?: string[];
  fileAttachments?: FileAttachment[];
  state?: 'queued' | 'blocked' | 'processing';
  error?: string;
}

interface ChatInputProps {
  onSend: (message: string, imageUrls?: string[], fileAttachments?: FileAttachment[]) => void;
  onStop?: () => void;
  isLoading: boolean;
  disabled?: boolean;
  readOnly?: boolean;
  placeholder?: string;
  agents?: AgentInfo[];
  selectedAgentIndex?: number;
  onSelectAgent?: (index: number) => void;
  onSelectModel?: (modelId: string) => void;
  onSelectMode?: (modeId: string) => void;
  onSelectThoughtLevel?: (thoughtLevelId: string) => void;
  /** Favorite model ids for the currently selected agent. When non-empty the
   *  model dropdown renders a pinned "favorites" group on top, then the rest.
   *  Undefined/empty → flat list (preserves shared/readonly behaviour). */
  favoriteModelIds?: string[];
  /** Override model ID display (per-session, takes priority over agent.models.currentModelId) */
  overrideModelId?: string | null;
  /** Override mode ID display (per-session, takes priority over agent.modes.currentModeId) */
  overrideModeId?: string | null;
  /** Override thought-level ID display (per-session, takes priority over agent.thoughtLevels.currentThoughtLevelId) */
  overrideThoughtLevelId?: string | null;
  totalTokens?: number;
  /** Timestamp of the first message in the current round (for total duration display). */
  roundStartedAt?: number;
  /** Timestamp when the last message in the current round finished. Undefined = still running. */
  roundFinishedAt?: number;
  /** Loop mode: accumulated duration in ms from all completed sessions.
   *  When provided (together with {@link totalDurationRunningStartedAts}),
   *  the footer badge shows the aggregate across the whole Loop job instead
   *  of a single run's elapsed — matching the Sessions sidebar header. */
  totalDurationBaseMs?: number;
  /** Loop mode: startedAt timestamps of currently-running session entries.
   *  Each value contributes a live delta on top of {@link totalDurationBaseMs}. */
  totalDurationRunningStartedAts?: number[];
  workdir?: string;
  /** Workspace metadata surfaced in the footer tag (replaces the old
   *  Workdir(Local/sandbox) text with Workspace({name}):{path}). */
  workspaceTitle?: string;
  workspaceId?: string;
  /** Path to render in the footer tag. Lets callers show the workspace
   *  path even when `workdir` points at an internal runtime location.
   *  Falls back to `workdir` when omitted. */
  displayWorkdir?: string;
  /** Available workspaces for the in-tag switcher. When provided along with
   *  `onSwitchWorkspace`, the Workspace tag becomes a clickable dropdown
   *  (chat-page behaviour: pick a workspace → reuse/create empty Job + switch).
   *  Leave empty to keep the tag purely informational (e.g. in shared-view
   *  readonly mode). */
  switchableWorkspaces?: Array<{ id: string; title: string; description: string; workdir: string; color?: string }>;
  onSwitchWorkspace?: (ws: { id: string; title: string; description: string; workdir: string; color?: string }) => void;
  jobEnable?: boolean;
  /** Pending messages waiting to be sent after the current run finishes (interactive mode). */
  queuedMessages?: QueuedMessageView[];
  /** Cancel a specific queued message by id. */
  onCancelQueuedMessage?: (id: string) => void | Promise<void>;
  messageQueuePaused?: boolean;
  messageQueuePauseReason?: string;
  onContinueMessageQueue?: () => void | Promise<void>;
  /** If true, allow composing & sending while isLoading is true — the send will be queued. */
  canQueueWhileRunning?: boolean;
  /** localStorage key scope for sent-message history. */
  localHistoryKey?: string;
}

/** Check if there's an active @mention being typed at cursor position */
function detectMention(text: string, cursorPos: number): { keyword: string; start: number } | null {
  const before = text.slice(0, cursorPos);
  const match = before.match(/@([^\s@]*)$/);
  if (match) {
    return { keyword: match[1], start: before.length - match[0].length };
  }
  return null;
}

export function ChatInput({
  onSend,
  onStop,
  isLoading,
  disabled = false,
  readOnly = false,
  placeholder = 'What can I help you?',
  agents,
  selectedAgentIndex,
  onSelectAgent,
  onSelectModel,
  onSelectMode,
  onSelectThoughtLevel,
  favoriteModelIds,
  overrideModelId,
  overrideModeId,
  overrideThoughtLevelId,
  totalTokens = 0,
  roundStartedAt,
  roundFinishedAt,
  totalDurationBaseMs,
  totalDurationRunningStartedAts,
  workdir,
  workspaceTitle,
  workspaceId,
  displayWorkdir,
  switchableWorkspaces,
  onSwitchWorkspace,
  jobEnable = true,
  queuedMessages,
  onCancelQueuedMessage,
  messageQueuePaused = false,
  messageQueuePauseReason,
  onContinueMessageQueue,
  canQueueWhileRunning = false,
  localHistoryKey,
}: ChatInputProps) {
  const { t } = useTranslation();
  const [input, setInput] = useState('');
  const [showDropdown, setShowDropdown] = useState(false);
  const [showModelDropdown, setShowModelDropdown] = useState(false);
  const [showModeDropdown, setShowModeDropdown] = useState(false);
  const [showThoughtLevelDropdown, setShowThoughtLevelDropdown] = useState(false);
  const isMobile = useIsMobile();
  const isTabletLayout = useIsMobile(1024);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const modelDropdownRef = useRef<HTMLDivElement>(null);
  const modeDropdownRef = useRef<HTMLDivElement>(null);
  const thoughtLevelDropdownRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [tabletBottomGap, setTabletBottomGap] = useState(0);

  const { pendingAttachments, addAttachments, removeAttachment, clearAttachments } = usePendingAttachments(uploadChatAttachment);

  // Local cache of "sent" messages (recorded on click send, regardless of server success/failure)
  const localHistoryStorageKey = `quartet:sent_history:${localHistoryKey || 'global'}`;
  const [historyItems, setHistoryItems] = useState<LocalSentMessage[]>(() => readLocalSentMessages(localHistoryStorageKey));
  const historyCursorRef = useRef<number | null>(null);
  const historyDraftRef = useRef<{ input: string; pickedImageUrls: string[]; pickedFileAttachments: FileAttachment[] } | null>(null);
  const [pickedImageUrls, setPickedImageUrls] = useState<string[]>([]);
  const [pickedFileAttachments, setPickedFileAttachments] = useState<FileAttachment[]>([]);
  const [deletingQueuedIds, setDeletingQueuedIds] = useState<Set<string>>(new Set());

  const [mentionState, setMentionState] = useState<{ keyword: string; start: number } | null>(null);
  const [mentionActiveIndex, setMentionActiveIndex] = useState(0);
  // Slash ("/") completion: grouped floater of built-in commands + installed
  // skills (chat page only). Shared implementation with the home-page input —
  // see SlashCompletion.tsx.
  const {
    slashPrefix,
    slashItems,
    slashActiveIdx,
    setSlashActiveIdx,
    updateSlash,
    applySlashItem,
    closeSlash,
    skillNameSet,
    imeComposing,
    compositionHandlers,
  } = useSlashCompletion({ setInput, textareaRef });
  const backdropRef = useRef<HTMLDivElement>(null);
  // Workspace switcher (chat page). Opens a dropdown anchored to the
  // Workspace tag in the footer. Only rendered when the parent supplies a
  // switchableWorkspaces list + onSwitchWorkspace callback.
  const [wsSwitchOpen, setWsSwitchOpen] = useState(false);
  const wsSwitchRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!wsSwitchOpen) return;
    const onDocClick = (e: MouseEvent) => {
      if (!wsSwitchRef.current?.contains(e.target as Node)) setWsSwitchOpen(false);
    };
    document.addEventListener('mousedown', onDocClick);
    return () => document.removeEventListener('mousedown', onDocClick);
  }, [wsSwitchOpen]);
  const canSwitchWorkspace = !!(
    switchableWorkspaces && switchableWorkspaces.length > 0 && onSwitchWorkspace
  );
  // Current git branch of the footer workdir (skipped in read-only/shared views
  // where the private endpoint would reject the request).
  const gitBranch = useGitBranch(displayWorkdir || workdir, !readOnly);
  const interactionDisabled = disabled || readOnly || !jobEnable;
  const controlsDisabled = readOnly || !jobEnable;
  // When the current run is in flight but we're in interactive mode, the user can still
  // compose and submit: the submission is queued and auto-sent when the run finishes.
  const canQueue = canQueueWhileRunning && !interactionDisabled;
  const composerLocked = interactionDisabled || (isLoading && !canQueue);
  const hasQueued = (!!queuedMessages && queuedMessages.length > 0) || messageQueuePaused;

  useEffect(() => {
    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      // Don't close dropdowns when clicking inside mobile portal overlays
      if (target.closest?.('.mobile-dropdown-overlay')) return;

      if (dropdownRef.current && !dropdownRef.current.contains(target)) {
        setShowDropdown(false);
      }
      if (modelDropdownRef.current && !modelDropdownRef.current.contains(target)) {
        setShowModelDropdown(false);
      }
      if (modeDropdownRef.current && !modeDropdownRef.current.contains(target)) {
        setShowModeDropdown(false);
      }
      if (thoughtLevelDropdownRef.current && !thoughtLevelDropdownRef.current.contains(target)) {
        setShowThoughtLevelDropdown(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  useEffect(() => {
    if (!interactionDisabled) return;
    setShowDropdown(false);
    setShowModelDropdown(false);
    setShowModeDropdown(false);
    setShowThoughtLevelDropdown(false);
    setMentionState(null);
    closeSlash();
  }, [interactionDisabled, closeSlash]);

  useEffect(() => {
    // When switching job/workspace, load the corresponding history.
    setHistoryItems(readLocalSentMessages(localHistoryStorageKey));
    historyCursorRef.current = null;
    historyDraftRef.current = null;
    setPickedImageUrls([]);
  }, [localHistoryStorageKey]);

  useEffect(() => {
    if (!isTabletLayout) {
      setTabletBottomGap(0);
      return;
    }

    const viewport = window.visualViewport;
    if (!viewport) {
      setTabletBottomGap(0);
      return;
    }

    let wasKeyboardVisible = false;

    const updateBottomGap = () => {
      const keyboardOffset = Math.max(0, window.innerHeight - viewport.height - viewport.offsetTop);
      const isKeyboardVisible = keyboardOffset > 0;
      setTabletBottomGap(isKeyboardVisible ? 6 : 0);

      // Reset scroll position when keyboard hides to fix iOS viewport shift
      if (wasKeyboardVisible && !isKeyboardVisible) {
        const resetScroll = () => {
          window.scrollTo(0, 0);
          document.body.scrollTop = 0;
          document.documentElement.scrollTop = 0;
        };
        resetScroll();
        setTimeout(resetScroll, 50);
        setTimeout(resetScroll, 150);
      }

      wasKeyboardVisible = isKeyboardVisible;
    };

    updateBottomGap();
    viewport.addEventListener('resize', updateBottomGap);
    viewport.addEventListener('scroll', updateBottomGap);
    window.addEventListener('orientationchange', updateBottomGap);

    return () => {
      viewport.removeEventListener('resize', updateBottomGap);
      viewport.removeEventListener('scroll', updateBottomGap);
      window.removeEventListener('orientationchange', updateBottomGap);
    };
  }, [isTabletLayout]);

  const handleSend = () => {
    const hasContent = input.trim() || pickedImageUrls.length > 0 || pendingAttachments.length > 0 || pickedFileAttachments.length > 0;
    const allUploaded = pendingAttachments.every((attachment) => attachment.uploaded && !attachment.uploading);
    if (!hasContent || interactionDisabled) return;
    if (isLoading && !canQueue) return;
    if (isLoading && isKnownCommand(input) && !isReadOnlyCommand(input)) {
      showToast(t('chat.commandUnavailableWhileRunning'));
      return;
    }
    if (!allUploaded) {
      const failedUpload = pendingAttachments.find((attachment) => attachment.error)?.error;
      if (failedUpload) {
        showToast(t('chat.attachmentUploadFailed', { error: failedUpload }));
      }
      return;
    }

    const imageUrls = [...pickedImageUrls];
    const uploadedFiles = pendingAttachments
      .map((attachment) => attachment.uploaded)
      .filter((attachment): attachment is UploadedAttachment => !!attachment);
    const imageAttachments = uploadedFiles.filter((attachment) => attachment.isImage);
    const fileAttachments = [
      ...pickedFileAttachments,
      ...uploadedFiles.filter((attachment) => !attachment.isImage).map(({ isImage: _isImage, ...attachment }) => attachment),
    ];
    imageUrls.push(...imageAttachments.map((attachment) => attachment.path));
    const contentToSend = input.trim() || (fileAttachments.length > 0 ? '[file]' : '[image]');

    // Record locally on send click, regardless of server result.
    const nextHistory = appendLocalSentMessage(localHistoryStorageKey, {
      content: contentToSend,
      imageUrls: imageUrls.length > 0 ? imageUrls : undefined,
      fileAttachments: fileAttachments.length > 0 ? fileAttachments : undefined,
    });
    setHistoryItems(nextHistory);

    if (fileAttachments.length > 0) {
      onSend(contentToSend, imageUrls.length > 0 ? imageUrls : undefined, fileAttachments);
    } else {
      onSend(contentToSend, imageUrls.length > 0 ? imageUrls : undefined);
    }
    setInput('');
    setPickedImageUrls([]);
    setPickedFileAttachments([]);
    clearAttachments();
    setMentionState(null);
    closeSlash();
    historyCursorRef.current = null;
    historyDraftRef.current = null;
  };

  const handleImageSelect = useCallback(async (files: FileList | null) => {
    if (interactionDisabled) return;
    await addAttachments(files);
  }, [addAttachments, interactionDisabled]);

  const handleRemovePickedFile = useCallback((path: string) => {
    if (interactionDisabled) return;
    setPickedFileAttachments((previous) => previous.filter((file) => file.path !== path));
  }, [interactionDisabled]);

  const handleRemovePickedImage = useCallback((url: string) => {
    if (interactionDisabled) return;
    setPickedImageUrls((prev) => prev.filter((u) => u !== url));
  }, [interactionDisabled, setPickedImageUrls]);

  const handlePaste = useCallback((e: React.ClipboardEvent) => {
    if (interactionDisabled) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    const imageFiles: File[] = [];
    for (let i = 0; i < items.length; i++) {
      if (items[i].type.startsWith('image/')) {
        const file = items[i].getAsFile();
        if (file) imageFiles.push(file);
      }
    }
    if (imageFiles.length > 0) {
      const dt = new DataTransfer();
      imageFiles.forEach((f) => dt.items.add(f));
      handleImageSelect(dt.files);
    }
  }, [handleImageSelect, interactionDisabled]);

  const handleMentionSelect = (file: FileResult) => {
    if (interactionDisabled) return;
    if (!mentionState) return;
    const before = input.slice(0, mentionState.start);
    const after = input.slice(mentionState.start + 1 + mentionState.keyword.length); // +1 for @
    const newInput = before + '@' + file.path + ' ' + after;
    setInput(newInput);
    setMentionState(null);
    // Focus back to textarea and scroll to bottom
    requestAnimationFrame(() => {
      if (textareaRef.current) {
        const pos = before.length + 1 + file.path.length + 1;
        textareaRef.current.focus();
        textareaRef.current.selectionStart = textareaRef.current.selectionEnd = pos;
        textareaRef.current.scrollTop = textareaRef.current.scrollHeight;
      }
    });
  };

  const handleInputChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    if (interactionDisabled) return;
    const val = e.target.value;
    setInput(val);
    // Any manual edits exit history navigation.
    historyCursorRef.current = null;
    historyDraftRef.current = null;
    const cursorPos = e.target.selectionStart ?? val.length;
    const mention = detectMention(val, cursorPos);
    setMentionState(mention);
    if (mention) {
      setMentionActiveIndex(0);
    }

    // Slash completion (chat page): if the value is `/xxx` with no space,
    // show built-in commands + installed skills matching the prefix.
    updateSlash(val, !!mention);
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (interactionDisabled) return;
    // Ignore Enter during IME composition (CJK input methods)
    if (isImeComposing(e)) return;
    // Slash completion navigation takes priority over plain Enter.
    if (slashCompletionKeyDown(e, slashItems, slashActiveIdx, setSlashActiveIdx, applySlashItem, closeSlash)) {
      return;
    }
    // If mention popup is open, handle navigation keys
    if (mentionState) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setMentionActiveIndex(i => i + 1);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setMentionActiveIndex(i => Math.max(0, i - 1));
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        setMentionState(null);
        return;
      }
      // Enter or Tab selects the active item — handled via a ref trick
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        // Trigger selection via dispatching a custom event
        const popup = document.querySelector('.file-mention-item.active') as HTMLElement;
        if (popup) {
          popup.click();
        }
        return;
      }
    }

    // Local sent-message history navigation (like common chat apps):
    // ArrowUp at beginning loads previous; ArrowDown restores draft.
    if (slashPrefix == null && !mentionState && (e.key === 'ArrowUp' || e.key === 'ArrowDown') && !e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey) {
      const textarea = e.currentTarget;
      const selStart = textarea.selectionStart ?? 0;
      const selEnd = textarea.selectionEnd ?? 0;
      const caretAtStart = selStart === 0 && selEnd === 0;
      const cursor = historyCursorRef.current;

      const allowEnterHistory = caretAtStart && input.length === 0 && pendingAttachments.length === 0 && pickedImageUrls.length === 0 && pickedFileAttachments.length === 0;
      if (e.key === 'ArrowUp' && (cursor != null || allowEnterHistory)) {
        if (historyItems.length > 0) {
          e.preventDefault();
          if (cursor == null) {
            historyDraftRef.current = { input, pickedImageUrls, pickedFileAttachments };
            historyCursorRef.current = 0;
            const it = historyItems[0];
            const nextInput = (it.content === '[image]' || it.content === '[file]') && ((it.imageUrls?.length ?? 0) + (it.fileAttachments?.length ?? 0) > 0) ? '' : it.content;
            setInput(nextInput);
            setPickedImageUrls(it.imageUrls || []);
            setPickedFileAttachments(it.fileAttachments || []);
            // clear pending uploads when recalling history
            clearAttachments();
            requestAnimationFrame(() => {
              textareaRef.current?.focus();
              if (textareaRef.current) {
                const pos = (nextInput || '').length;
                textareaRef.current.selectionStart = textareaRef.current.selectionEnd = pos;
              }
            });
          } else {
            const nextIdx = Math.min(historyItems.length - 1, cursor + 1);
            historyCursorRef.current = nextIdx;
            const it = historyItems[nextIdx];
            const nextInput = (it.content === '[image]' || it.content === '[file]') && ((it.imageUrls?.length ?? 0) + (it.fileAttachments?.length ?? 0) > 0) ? '' : it.content;
            setInput(nextInput);
            setPickedImageUrls(it.imageUrls || []);
            setPickedFileAttachments(it.fileAttachments || []);
            clearAttachments();
            requestAnimationFrame(() => {
              textareaRef.current?.focus();
              if (textareaRef.current) {
                const pos = (nextInput || '').length;
                textareaRef.current.selectionStart = textareaRef.current.selectionEnd = pos;
              }
            });
          }
        }
      }
      if (e.key === 'ArrowDown' && cursor != null) {
        e.preventDefault();
        const nextIdx = cursor - 1;
        if (nextIdx < 0) {
          historyCursorRef.current = null;
          const draft = historyDraftRef.current;
          historyDraftRef.current = null;
          if (draft) {
            setInput(draft.input);
            setPickedImageUrls(draft.pickedImageUrls);
            setPickedFileAttachments(draft.pickedFileAttachments);
          } else {
            setInput('');
            setPickedImageUrls([]);
            setPickedFileAttachments([]);
          }
        } else {
          historyCursorRef.current = nextIdx;
          const it = historyItems[nextIdx];
          const nextInput = (it.content === '[image]' || it.content === '[file]') && ((it.imageUrls?.length ?? 0) + (it.fileAttachments?.length ?? 0) > 0) ? '' : it.content;
          setInput(nextInput);
          setPickedImageUrls(it.imageUrls || []);
          setPickedFileAttachments(it.fileAttachments || []);
          clearAttachments();
          requestAnimationFrame(() => {
            textareaRef.current?.focus();
            if (textareaRef.current) {
              const pos = (nextInput || '').length;
              textareaRef.current.selectionStart = textareaRef.current.selectionEnd = pos;
            }
          });
        }
        return;
      }
    }

    if (e.key === 'Enter') {
      if (e.metaKey || e.ctrlKey) {
        e.preventDefault();
        const textarea = e.currentTarget;
        const { selectionStart, selectionEnd } = textarea;
        const newValue = input.slice(0, selectionStart) + '\n' + input.slice(selectionEnd);
        setInput(newValue);
        requestAnimationFrame(() => {
          textarea.selectionStart = textarea.selectionEnd = selectionStart + 1;
        });
      } else if (!e.shiftKey) {
        e.preventDefault();
        handleSend();
      }
    }
  };

  const selectedAgent = agents && selectedAgentIndex != null ? agents[selectedAgentIndex] ?? null : null;
  const canSelect = !!agents && agents.length > 0 && !!onSelectAgent && !interactionDisabled;
  const canSelectModel = !!selectedAgent?.models && (selectedAgent.models.availableModels?.length ?? 0) > 0 && !!onSelectModel && !controlsDisabled;
  const canSelectMode = !!selectedAgent?.modes && (selectedAgent.modes.availableModes?.length ?? 0) > 1 && !!onSelectMode && !controlsDisabled;
  const canSelectThoughtLevel = !!selectedAgent?.thoughtLevels && (selectedAgent.thoughtLevels.availableThoughtLevels?.length ?? 0) > 1 && !!onSelectThoughtLevel && !controlsDisabled;
  // Effective model/mode ID: prefer per-session override over agent-level value
  const effectiveModelId = overrideModelId ?? selectedAgent?.models?.currentModelId;
  const effectiveModeId = overrideModeId ?? selectedAgent?.modes?.currentModeId;
  const effectiveThoughtLevelId = overrideThoughtLevelId ?? selectedAgent?.thoughtLevels?.currentThoughtLevelId;
  const containerStyle = isTabletLayout
    ? ({ '--tablet-bottom-gap': `${tabletBottomGap}px` } as CSSProperties)
    : undefined;

  // Model dropdown content: when favorites are configured for this agent the
  // list is split into a pinned group + the rest, with a small group label
  // between. renderModelItem keeps a single source of truth for item markup
  // across the favorites/rest groups and the mobile/desktop variants.
  const renderModelItem = (m: { modelId: string; name: string; description?: string }) => (
    <div
      key={m.modelId}
      className={`model-dropdown-item ${m.modelId === effectiveModelId ? 'active' : ''}`}
      onClick={() => {
        onSelectModel?.(m.modelId);
        setShowModelDropdown(false);
      }}
    >
      <div className="model-dropdown-info">
        <span className="model-dropdown-name">{m.name}</span>
        {m.description && <span className="model-dropdown-provider">{m.description}</span>}
      </div>
      {m.modelId === effectiveModelId && (
        <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M20 6L9 17l-5-5" />
        </svg>
      )}
    </div>
  );

  const renderModelDropdownItems = () => {
    const all = selectedAgent?.models?.availableModels ?? [];
    const { favorites, rest } = splitFavoriteModels(all, favoriteModelIds);
    if (favorites.length === 0) {
      return all.map(renderModelItem);
    }
    return (
      <>
        <div className="model-dropdown-group-label">{t('chat.favoriteModels')}</div>
        {favorites.map(renderModelItem)}
        {rest.length > 0 && <div className="model-dropdown-group-label">{t('chat.otherModels')}</div>}
        {rest.map(renderModelItem)}
      </>
    );
  };

  return (
    <div className="chat-input-container" style={containerStyle} data-testid="chat-input-container" data-loading={isLoading ? 'true' : 'false'} data-readonly={readOnly ? 'true' : 'false'}>
      <div className="chat-input-wrapper" style={{ position: 'relative' }}>
        {mentionState && workdir && !interactionDisabled && (
          <FileMention
            keyword={mentionState.keyword}
            workdir={workdir}
            onSelect={handleMentionSelect}
            onClose={() => setMentionState(null)}
            activeIndex={mentionActiveIndex}
            onActiveIndexChange={setMentionActiveIndex}
          />
        )}
        {hasQueued && (
          <div className="chat-queued-panel" data-testid="chat-queued-list">
            {messageQueuePaused && (
              <div className="chat-queued-header">
                <span>{messageQueuePauseReason === 'blocked' ? t('chat.queueBlocked') : t('chat.queuePaused')}</span>
                {messageQueuePauseReason !== 'blocked' && onContinueMessageQueue && (
                  <button
                    type="button"
                    onClick={() => { void Promise.resolve(onContinueMessageQueue()).catch(() => {}); }}
                  >{t('chat.continueQueue')}</button>
                )}
              </div>
            )}
            <div className="chat-queued-row" role="list" aria-label={t('chat.waitingToSend')}>
              {(queuedMessages ?? []).map((q, index) => {
                const preview = q.content.trim()
                  || (q.imageUrls && q.imageUrls.length > 0 ? t('chat.imagesCount', { count: q.imageUrls.length }) : '')
                  || (q.fileAttachments && q.fileAttachments.length > 0 ? t('chat.filesCount', { count: q.fileAttachments.length }) : '');
                const deleting = deletingQueuedIds.has(q.id);
                return (
                  <div key={q.id} className={`chat-queued-pill ${q.state === 'blocked' ? 'blocked' : ''}`} role="listitem" title={q.error || preview || t('chat.waitingToSend')} data-testid="chat-queued-item" data-queued-id={q.id}>
                    <span className="chat-queued-index">{index + 1}</span>
                    <span className="chat-queued-text">{preview || t('chat.waitingToSend')}</span>
                    {q.imageUrls && q.imageUrls.length > 0 && <span className="chat-queued-images">{t('chat.imagesCount', { count: q.imageUrls.length })}</span>}
                    {q.fileAttachments && q.fileAttachments.length > 0 && <span className="chat-queued-images">{t('chat.filesCount', { count: q.fileAttachments.length })}</span>}
                    {q.error && (
                      <button
                        type="button"
                        className="chat-queued-error"
                        title={q.error}
                        aria-label={q.error}
                        onClick={() => { void copyToClipboard(q.error!); showToast(t('common.copySuccess')); }}
                      >!</button>
                    )}
                    {onCancelQueuedMessage && (
                      <button
                        type="button"
                        className="chat-queued-cancel"
                        disabled={deleting}
                        onClick={() => {
                          setDeletingQueuedIds((prev) => new Set(prev).add(q.id));
                          void Promise.resolve(onCancelQueuedMessage(q.id)).catch(() => {
                            // The owner surfaces the full server error. Consume the
                            // rejection here so a failed delete is not reported as an
                            // unhandled browser promise.
                          }).finally(() => {
                            setDeletingQueuedIds((prev) => { const next = new Set(prev); next.delete(q.id); return next; });
                          });
                        }}
                        title={q.error || t('chat.cancelQueue')}
                        aria-label={t('chat.cancelQueue')}
                      >{deleting ? '…' : '×'}</button>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        )}
        {pickedImageUrls.length > 0 && (
          <div className="chat-image-preview-row">
            {pickedImageUrls.map((url) => (
              <div key={url} className="chat-image-preview-item">
                <img src={toImagePreviewUrl(url)} alt="" className="chat-image-preview-thumb" />
                <button className="chat-image-preview-remove" onClick={() => handleRemovePickedImage(url)}>×</button>
              </div>
            ))}
          </div>
        )}
        <UploadedFilePreviews attachments={pickedFileAttachments} onRemove={handleRemovePickedFile} />
        <PendingAttachmentPreviews attachments={pendingAttachments} onRemove={removeAttachment} />
        <SlashFloater items={slashItems} activeIdx={slashActiveIdx} onPick={applySlashItem} onActiveIdxChange={setSlashActiveIdx} />
        <div className={`chat-input-editor${imeComposing ? ' composing' : ''}`}>
          <SkillBackdrop input={input} skillNameSet={skillNameSet} backdropRef={backdropRef} />
          <textarea
            ref={textareaRef}
            className="chat-input"
            data-testid="chat-input"
            value={input}
            onChange={handleInputChange}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
            {...compositionHandlers}
            onScroll={(e) => {
              if (backdropRef.current) {
                backdropRef.current.scrollTop = e.currentTarget.scrollTop;
              }
            }}
            onBlur={() => {
              // On iOS Chrome, force scroll reset after keyboard dismiss
              // to prevent residual viewport offset
              const resetScroll = () => {
                window.scrollTo(0, 0);
                document.body.scrollTop = 0;
                document.documentElement.scrollTop = 0;
              };
              resetScroll();
              setTimeout(resetScroll, 50);
              setTimeout(resetScroll, 150);
            }}
            placeholder={placeholder}
            disabled={composerLocked}
            rows={1}
          />
        </div>
        <input
          ref={fileInputRef}
          type="file"
          multiple
          style={{ display: 'none' }}
          onChange={(e) => { handleImageSelect(e.target.files); e.target.value = ''; }}
        />
        <div className="chat-input-footer" data-testid="chat-input-footer">
          <div className="chat-input-options">
            {agents && (
              <div className="chat-model-selector" ref={dropdownRef}>
                <div
                  className={`model-tag ${canSelect ? '' : 'disabled'}`}
                  onClick={() => {
                    if (canSelect) {
                      setShowDropdown(!showDropdown);
                    }
                  }}
                >
                  {selectedAgent?.icon_url && (
                    isImageUrl(selectedAgent.icon_url)
                      ? <img src={resolveIconSrc(selectedAgent.icon_url)} alt="" className="model-tag-icon" referrerPolicy="no-referrer" />
                      : <span className="model-tag-emoji">{selectedAgent.icon_url}</span>
                  )}
                  <span>{selectedAgent ? selectedAgent.display_name : t('sidebar.selectAgent')}</span>
                  <svg className="model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </div>
                {showDropdown && (
                  isMobile ? createPortal(
                    <div className="mobile-dropdown-overlay" onClick={() => setShowDropdown(false)}>
                      <div className="mobile-dropdown-sheet" onClick={e => e.stopPropagation()}>
                        {agents.length === 0 ? (
                          <div className="model-dropdown-empty">{t('sidebar.noAgentsAvailable')}</div>
                        ) : (
                          agents.map((agent, idx) => (
                            <div
                              key={`${agent.type}-${agent.model_id}`}
                              className={`model-dropdown-item ${idx === selectedAgentIndex ? 'active' : ''}`}
                              onClick={() => {
                                onSelectAgent?.(idx);
                                setShowDropdown(false);
                              }}
                            >
                              {agent.icon_url ? (
                                isImageUrl(agent.icon_url)
                                  ? <img src={resolveIconSrc(agent.icon_url)} alt="" className="model-dropdown-icon" referrerPolicy="no-referrer" />
                                  : <span className="model-dropdown-emoji">{agent.icon_url}</span>
                              ) : (
                                <div className="model-dropdown-icon-placeholder" />
                              )}
                              <div className="model-dropdown-info">
                                <span className="model-dropdown-name">{agent.display_name}</span>
                              </div>
                              {idx === selectedAgentIndex && (
                                <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                  <path d="M20 6L9 17l-5-5" />
                                </svg>
                              )}
                            </div>
                          ))
                        )}
                      </div>
                    </div>,
                    document.body
                  ) : (
                  <div className="model-dropdown">
                    {agents.length === 0 ? (
                      <div className="model-dropdown-empty">{t('sidebar.noAgentsAvailable')}</div>
                    ) : (
                      agents.map((agent, idx) => (
                        <div
                          key={`${agent.type}-${agent.model_id}`}
                          className={`model-dropdown-item ${idx === selectedAgentIndex ? 'active' : ''}`}
                          onClick={() => {
                            onSelectAgent?.(idx);
                            setShowDropdown(false);
                          }}
                        >
                          {agent.icon_url ? (
                            isImageUrl(agent.icon_url)
                              ? <img src={resolveIconSrc(agent.icon_url)} alt="" className="model-dropdown-icon" referrerPolicy="no-referrer" />
                              : <span className="model-dropdown-emoji">{agent.icon_url}</span>
                          ) : (
                            <div className="model-dropdown-icon-placeholder" />
                          )}
                          <div className="model-dropdown-info">
                            <span className="model-dropdown-name">{agent.display_name}</span>
                          </div>
                          {idx === selectedAgentIndex && (
                            <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                              <path d="M20 6L9 17l-5-5" />
                            </svg>
                          )}
                        </div>
                      ))
                    )}
                  </div>
                  )
                )}
              </div>
            )}
            {selectedAgent?.models && (selectedAgent.models.availableModels?.length ?? 0) > 0 && onSelectModel && (
              <div className="chat-model-selector" ref={modelDropdownRef}>
                <div
                  className={`model-tag ${canSelectModel ? '' : 'disabled'}`}
                  onClick={() => {
                    if (canSelectModel) {
                      setShowModelDropdown(!showModelDropdown);
                    }
                  }}
                >
                  <span>{selectedAgent.models.availableModels.find(m => m.modelId === effectiveModelId)?.name || effectiveModelId}</span>
                  <svg className="model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </div>
                {showModelDropdown && (
                  isMobile ? createPortal(
                    <div className="mobile-dropdown-overlay" onClick={() => setShowModelDropdown(false)}>
                      <div className="mobile-dropdown-sheet" onClick={e => e.stopPropagation()}>
                        {renderModelDropdownItems()}
                      </div>
                    </div>,
                    document.body
                  ) : (
                  <div className="model-dropdown">
                    {renderModelDropdownItems()}
                  </div>
                  )
                )}
              </div>
            )}
            {selectedAgent?.modes && (selectedAgent.modes.availableModes?.length ?? 0) > 1 && onSelectMode && (
              <div className="chat-model-selector" ref={modeDropdownRef}>
                <div
                  className={`model-tag ${canSelectMode ? '' : 'disabled'}`}
                  onClick={() => {
                    if (canSelectMode) {
                      setShowModeDropdown(!showModeDropdown);
                    }
                  }}
                >
                  <span>{selectedAgent.modes.availableModes.find(m => m.id === effectiveModeId)?.name || effectiveModeId}</span>
                  <svg className="model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </div>
                {showModeDropdown && (
                  isMobile ? createPortal(
                    <div className="mobile-dropdown-overlay" onClick={() => setShowModeDropdown(false)}>
                      <div className="mobile-dropdown-sheet" onClick={e => e.stopPropagation()}>
                        {selectedAgent.modes.availableModes.map((m) => (
                          <div
                            key={m.id}
                            className={`model-dropdown-item ${m.id === effectiveModeId ? 'active' : ''}`}
                            onClick={() => {
                              onSelectMode(m.id);
                              setShowModeDropdown(false);
                            }}
                          >
                            <div className="model-dropdown-info">
                              <span className="model-dropdown-name">{m.name}</span>
                              {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                            </div>
                            {m.id === effectiveModeId && (
                              <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                <path d="M20 6L9 17l-5-5" />
                              </svg>
                            )}
                          </div>
                        ))}
                      </div>
                    </div>,
                    document.body
                  ) : (
                  <div className="model-dropdown">
                    {selectedAgent.modes.availableModes.map((m) => (
                      <div
                        key={m.id}
                        className={`model-dropdown-item ${m.id === effectiveModeId ? 'active' : ''}`}
                        onClick={() => {
                          onSelectMode(m.id);
                          setShowModeDropdown(false);
                        }}
                      >
                        <div className="model-dropdown-info">
                          <span className="model-dropdown-name">{m.name}</span>
                          {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                        </div>
                        {m.id === effectiveModeId && (
                          <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                            <path d="M20 6L9 17l-5-5" />
                          </svg>
                        )}
                      </div>
                    ))}
                  </div>
                  )
                )}
              </div>
            )}
            {selectedAgent?.thoughtLevels && (selectedAgent.thoughtLevels.availableThoughtLevels?.length ?? 0) > 1 && onSelectThoughtLevel && (
              <div className="chat-model-selector" ref={thoughtLevelDropdownRef}>
                <div
                  className={`model-tag ${canSelectThoughtLevel ? '' : 'disabled'}`}
                  onClick={() => {
                    if (canSelectThoughtLevel) {
                      setShowThoughtLevelDropdown(!showThoughtLevelDropdown);
                    }
                  }}
                >
                  <span>{selectedAgent.thoughtLevels.availableThoughtLevels.find(m => m.id === effectiveThoughtLevelId)?.name || effectiveThoughtLevelId}</span>
                  <svg className="model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </div>
                {showThoughtLevelDropdown && (
                  isMobile ? createPortal(
                    <div className="mobile-dropdown-overlay" onClick={() => setShowThoughtLevelDropdown(false)}>
                      <div className="mobile-dropdown-sheet" onClick={e => e.stopPropagation()}>
                        {selectedAgent.thoughtLevels.availableThoughtLevels.map((m) => (
                          <div
                            key={m.id}
                            className={`model-dropdown-item ${m.id === effectiveThoughtLevelId ? 'active' : ''}`}
                            onClick={() => {
                              onSelectThoughtLevel(m.id);
                              setShowThoughtLevelDropdown(false);
                            }}
                          >
                            <div className="model-dropdown-info">
                              <span className="model-dropdown-name">{m.name}</span>
                              {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                            </div>
                            {m.id === effectiveThoughtLevelId && (
                              <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                <path d="M20 6L9 17l-5-5" />
                              </svg>
                            )}
                          </div>
                        ))}
                      </div>
                    </div>,
                    document.body
                  ) : (
                  <div className="model-dropdown">
                    {selectedAgent.thoughtLevels.availableThoughtLevels.map((m) => (
                      <div
                        key={m.id}
                        className={`model-dropdown-item ${m.id === effectiveThoughtLevelId ? 'active' : ''}`}
                        onClick={() => {
                          onSelectThoughtLevel(m.id);
                          setShowThoughtLevelDropdown(false);
                        }}
                      >
                        <div className="model-dropdown-info">
                          <span className="model-dropdown-name">{m.name}</span>
                          {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                        </div>
                        {m.id === effectiveThoughtLevelId && (
                          <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                            <path d="M20 6L9 17l-5-5" />
                          </svg>
                        )}
                      </div>
                    ))}
                  </div>
                  )
                )}
              </div>
            )}
            <span className="token-usage" data-testid="chat-token-usage" title={t('chat.tokenUsageHint')}>
              Tokens: {formatTokenCount(totalTokens)}
            </span>
            {/* Duration badge:
               - Loop mode passes `totalDuration*` so the badge reflects the
                 whole Loop job (sum of session durations + live delta of any
                 running sessions) — matches the Sessions sidebar header.
               - Interactive mode falls back to the per-round start/finish,
                 which is what the old badge always showed.
               - Backward-compat: old history rounds may miss `roundFinishedAt`.
                 Only show a running total while a run is active. */}
            {(totalDurationBaseMs != null || (totalDurationRunningStartedAts && totalDurationRunningStartedAts.length > 0)) ? (
              ((totalDurationBaseMs ?? 0) > 0 || (totalDurationRunningStartedAts && totalDurationRunningStartedAts.length > 0)) && (
                <DurationBadge
                  startedAt={totalDurationRunningStartedAts}
                  baseMs={totalDurationBaseMs ?? 0}
                  variant="total"
                />
              )
            ) : (
              roundStartedAt != null && (isLoading || roundFinishedAt != null) && (
                <DurationBadge startedAt={roundStartedAt} endedAt={roundFinishedAt} variant="total" />
              )
            )}
            <button
              className="chat-btn upload-btn"
              onClick={() => fileInputRef.current?.click()}
              disabled={composerLocked}
              title={t('chat.uploadAttachment')}
            >
              <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="m21.4 11.6-8.9 8.9a6 6 0 0 1-8.5-8.5l9.2-9.2a4 4 0 0 1 5.7 5.7l-9.2 9.2a2 2 0 0 1-2.8-2.8l8.5-8.5" />
              </svg>
            </button>
            {selectedAgent && (
              <AgentUsageCard
                agentType={selectedAgent.type}
                displayName={selectedAgent.display_name}
              />
            )}
          </div>
          <div className="chat-input-actions">
            <MessagePresetHistoryMenu
              workspaceId={workspaceId}
              disabled={interactionDisabled}
              isMobile={isMobile}
              currentInput={input}
              historyItems={historyItems}
              onApplyPreset={(content, mode) => {
                const nextInput = mode === 'append' && input ? `${input}\n\n${content}` : content;
                setInput(nextInput);
                closeSlash();
                historyCursorRef.current = null;
                historyDraftRef.current = null;
                requestAnimationFrame(() => {
                  textareaRef.current?.focus();
                  if (textareaRef.current) textareaRef.current.selectionStart = textareaRef.current.selectionEnd = nextInput.length;
                });
              }}
              onApplyHistory={(item) => {
                const nextInput = (item.content === '[image]' || item.content === '[file]') && ((item.imageUrls?.length ?? 0) + (item.fileAttachments?.length ?? 0) > 0) ? '' : item.content;
                setInput(nextInput);
                setPickedImageUrls(item.imageUrls || []);
                setPickedFileAttachments(item.fileAttachments || []);
                clearAttachments();
                closeSlash();
                historyCursorRef.current = null;
                historyDraftRef.current = null;
                requestAnimationFrame(() => {
                  textareaRef.current?.focus();
                  if (textareaRef.current) textareaRef.current.selectionStart = textareaRef.current.selectionEnd = nextInput.length;
                });
              }}
            />
            {isLoading && (
              <button className="chat-btn stop-btn" onClick={onStop} disabled={controlsDisabled || !onStop} title={hasQueued ? t('chat.stopAndPauseQueue') : t('chat.stopGeneration')} data-testid="chat-stop-button">
                <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
                  <rect x="6" y="6" width="12" height="12" rx="2" />
                </svg>
              </button>
            )}
            {(!isLoading || canQueue) && (
            <button
              className="chat-btn send-btn"
              onClick={handleSend}
              disabled={composerLocked || (!input.trim() && pendingAttachments.length === 0 && pickedImageUrls.length === 0 && pickedFileAttachments.length === 0) || pendingAttachments.some((attachment) => attachment.uploading)}
              title={isLoading && canQueue ? t('chat.queueSend') : t('chat.sendMessage')}
              data-testid="chat-send-button"
            >
              <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor">
                <path d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z" />
              </svg>
              </button>
            )}
          </div>
          {(workdir || displayWorkdir || workspaceId) && (
            <div
              ref={wsSwitchRef}
              className={`workdir-row footer-workdir ${interactionDisabled ? 'disabled' : ''} ${canSwitchWorkspace ? 'switchable' : ''}`}
              data-testid="workspace-footer"
              data-workspace-id={workspaceId || ''}
              data-workdir={displayWorkdir || workdir || ''}
              title={
                canSwitchWorkspace
                  ? t('chat.switchWorkspace')
                  : `${workspaceTitle || workspaceId || ''} : ${displayWorkdir || workdir}`
              }
              onClick={
                interactionDisabled
                  ? undefined
                  : canSwitchWorkspace
                  ? () => setWsSwitchOpen((v) => !v)
                  : (displayWorkdir || workdir)
                  ? () => {
                    const p = displayWorkdir || workdir;
                    if (p) showToast(p);
                  }
                  : undefined
              }
            >
              {workspaceId && (
                <span
                  className="workdir-ws-strip"
                  style={{ backgroundColor: workspaceColor(workspaceId) }}
                  aria-hidden
                />
              )}
              <span className="workdir-icon">🗂️</span>
              <span className="workdir-label">
                Workspace({workspaceTitle || workspaceId || '—'}) :
              </span>
              <span className="workdir-path-branch">
                <code className="workdir-path">
                  {displayWorkdir || workdir || '—'}
                </code>
                {gitBranch && (
                  <span className="workdir-branch" title={`git: ${gitBranch}`}>
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
                      <circle cx="6" cy="6" r="2.5" />
                      <circle cx="6" cy="18" r="2.5" />
                      <circle cx="18" cy="7" r="2.5" />
                      <path d="M6 8.5v7" />
                      <path d="M18 9.5c0 3-3 3.5-6 4.5" />
                    </svg>
                    <span className="workdir-branch-name">{gitBranch}</span>
                  </span>
                )}
              </span>
              {canSwitchWorkspace && (
                <span className="workdir-switch-caret" aria-hidden>
                  <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </span>
              )}
              <button
                className="workdir-copy"
                onClick={(e) => {
                  e.stopPropagation();
                  const pathToCopy = displayWorkdir || workdir;
                  if (!interactionDisabled && pathToCopy) copyToClipboard(pathToCopy);
                }}
                title={t('common.copyPath')}
                disabled={interactionDisabled || !(displayWorkdir || workdir)}
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                  <path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1" />
                </svg>
              </button>
              {canSwitchWorkspace && wsSwitchOpen && wsSwitchRef.current && createPortal(
                (() => {
                  const rect = wsSwitchRef.current.getBoundingClientRect();
                  return (
                    <div
                      className="workdir-switch-dropdown"
                      data-testid="workspace-switch-dropdown"
                      onMouseDown={(e) => e.stopPropagation()}
                      onClick={(e) => e.stopPropagation()}
                      style={{
                        position: 'fixed',
                        left: rect.left,
                        bottom: window.innerHeight - rect.top + 4,
                        zIndex: 9999,
                      }}
                    >
                      {switchableWorkspaces!.map((ws) => (
                        <div
                          key={ws.id}
                          className={`workdir-switch-item ${ws.id === workspaceId ? 'active' : ''}`}
                          data-testid="workspace-switch-item"
                          data-workspace-id={ws.id}
                          data-workdir={ws.workdir}
                          onClick={() => {
                            setWsSwitchOpen(false);
                            if (ws.id !== workspaceId) onSwitchWorkspace!(ws);
                          }}
                          title={ws.workdir}
                        >
                          <span
                            className="workdir-switch-item-color"
                            style={{ backgroundColor: workspaceColor(ws) }}
                          />
                          <span className="workdir-switch-item-title">{ws.title || ws.id}</span>
                          <span className="workdir-switch-item-path">{ws.workdir}</span>
                        </div>
                      ))}
                    </div>
                  );
                })(),
                document.body
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
