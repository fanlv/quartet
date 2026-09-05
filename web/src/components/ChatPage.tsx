import { useState, useEffect, useRef, useCallback, useMemo, memo } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useConnectionStatus } from '../contexts/ConnectionStatus';
import { useAuthPrincipal } from '../auth';
import './ChatPage.css';
import './JobChat.css';
import './ChatInput.css';

function toImagePreviewUrl(path: string): string {
  if (path.startsWith('http://') || path.startsWith('https://') || path.startsWith('data:') || path.startsWith('blob:')) return path;
  return `/api/v1/serve-file?path=${encodeURIComponent(path)}`;
}
import { DirPicker } from './DirPicker';
import { ScheduleInfo, type AgentInfo, type FileAttachment, type WorkspaceInfo } from '../types';
import { FileMention, FileResult } from './FileMention';
import { SlashFloater, SkillBackdrop } from './SlashCompletion';
import { slashCompletionKeyDown, useSlashCompletion } from '../utils/slashCompletion';
import { ScheduleEditModal } from './ScheduleEditModal';
import { cronToHuman } from './CronInput';
import { AgentUsageCard } from './AgentUsageCard';
import { ClockIcon, ConfigurationIcon } from './ComposerIcons';
import { HomeNavigation } from './HomeNavigation';
import { MessagePresetHistoryMenu, type SentMessageHistoryItem } from './MessagePresetHistoryMenu';
import { copyToClipboard } from '../utils/clipboard';
import { useIsMobile } from '../hooks/useIsMobile';
import { useGitBranch } from '../hooks/useGitBranch';
import { uploadChatAttachment, usePendingAttachments, type UploadedAttachment } from '../hooks/usePendingAttachments';
import { PendingAttachmentPreviews, UploadedFilePreviews } from './AttachmentPreviews';
import { useJobList, type JobSummary } from '../hooks/useJobList';
import { DEFAULT_WORKSPACE_ID, workspaceColor, loadWorkspacePrefs, migrateWorkspacePrefsToServer, registerWorkspaceColors } from '../utils/workspace';
import { relinkACPThoughtLevels, setACPConfig, type ACPConfigState, type ACPConfigTarget } from '../utils/acpConfig';
import { fetchAgentPrefs, splitFavoriteModels, resolveAgentDefaults, applyDefaultsToAgent, prefsForAgent, type AgentPrefsMap } from '../utils/agentPrefs';
import { formatStatsDuration } from '../utils/statsFormat';
import { isImeComposing } from '../utils/keyboard';
import { isImageUrl, resolveIconSrc } from '../utils/url';
import { showToast } from '../utils/toast';
import { fetchAvailableAgentList } from '../api/agents';
import { useLocalTextDraft } from '../hooks/useLocalTextDraft';

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

interface ChatPageProps {
  onStartChat: (message: string, modelId: string, type: string, workdir?: string, imageUrls?: string[], fileAttachments?: FileAttachment[], acpMode?: string, acpThoughtLevel?: string) => void;
  isInitializing?: boolean;
  refreshKey?: number;
  workspaceWorkdir?: string;
  workspaceId?: string;
  workspaceTitle?: string;
  // Switch the current workspace from the home page (clicking the Workspace
  // tag or picking from the filter dropdown). The page itself doesn't build a
  // new Job — it just updates the URL + currentWorkspace state via the parent.
  onSelectWorkspace?: (ws: WorkspaceInfo) => void;
  onSelectJob?: (jobId: string, workspaceId?: string) => void;
  onOpenSettings?: () => void;
  onOpenAgentSettings?: () => void;
  onOpenStats?: () => void;
  onOpenGraph?: () => void;
}

async function fetchAgentList(): Promise<{ agents: AgentInfo[]; workdir: string; jobEnable: boolean }> {
  return fetchAvailableAgentList();
}

async function migrateStoredAgentReferences(workspaceId?: string): Promise<void> {
  try {
    const [listRes, catalogRes, workspaceRes] = await Promise.all([
      fetch('/api/v1/agent/list'),
      fetch('/api/v1/agent/catalog'),
      fetch('/api/v1/workspace/list'),
    ]);
    const [listData, catalogData, workspaceData] = await Promise.all([listRes.json(), catalogRes.json(), workspaceRes.json()]);
    const active = (Array.isArray(listData?.agent_list) ? listData.agent_list as AgentInfo[] : [])
      .filter((agent) => agent.available === true);
    const items = Array.isArray(catalogData?.agents) ? catalogData.agents as Array<{
      agent_id: string;
      lifecycle?: string;
      deprecated?: boolean;
      definition?: { bin?: string; acp_program?: string; acp_args?: string[] };
      historical_identifiers?: Array<{ value?: string }>;
    }> : [];
    const byIdentifier = new Map<string, string>();
    for (const agent of active) {
      byIdentifier.set(agent.agent_id, agent.agent_id);
      byIdentifier.set(agent.type, agent.agent_id);
    }
    for (const item of items) {
      if (item.lifecycle !== 'active' || item.deprecated) continue;
      byIdentifier.set(item.agent_id, item.agent_id);
      if (item.definition?.bin) byIdentifier.set(item.definition.bin, item.agent_id);
      if (item.definition?.acp_program) {
        const command = [item.definition.acp_program, ...(item.definition.acp_args || [])].join(' ');
        byIdentifier.set(command, item.agent_id);
      }
      for (const historical of item.historical_identifiers || []) {
        if (historical.value) byIdentifier.set(historical.value, item.agent_id);
      }
    }
    const resolve = (value: string) => byIdentifier.get(value);
    const modelBelongsToAgent = (agentID: string, modelID: string) => {
      const agent = active.find((candidate) => candidate.agent_id === agentID);
      return !!agent?.models?.availableModels.some((model) => model.modelId === modelID);
    };
    const last = localStorage.getItem('last_agent_type');
    if (last) {
      const migrated = resolve(last);
      if (migrated) localStorage.setItem('last_agent_type', migrated);
      else localStorage.removeItem('last_agent_type');
    }
    const workspaceIds = new Set<string>();
    if (workspaceId) workspaceIds.add(workspaceId);
    for (const workspace of Array.isArray(workspaceData?.workspaces) ? workspaceData.workspaces : []) {
      if (typeof workspace?.id === 'string' && workspace.id) workspaceIds.add(workspace.id);
    }
    await Promise.all([...workspaceIds].map(async (id) => {
      await migrateWorkspacePrefsToServer(id, resolve, modelBelongsToAgent);
    }));
  } catch (err) {
    console.warn('[agent-migration] stored reference migration failed:', err);
  }
}

type JobInfo = JobSummary;

// ---- Pure helpers (module-scope so they're stable references) ----

function getJobTitle(job: JobInfo): string {
  const title = job.title?.trim();
  return title ? title : 'undefined title';
}

function formatJobTime(updatedAt: number, locale?: string): string {
  if (!updatedAt) return 'Unknown time';
  return new Date(updatedAt).toLocaleString(locale || undefined, {
    hour: '2-digit',
    minute: '2-digit',
  });
}

function formatRelativeTime(ts: number, locale?: string): string {
  if (!ts) return '';
  const diffMs = ts - Date.now();
  const diffSec = Math.round(diffMs / 1000);
  const absSec = Math.abs(diffSec);
  const isZh = (locale || '').toLowerCase().startsWith('zh');
  const pastSuffix = isZh ? '前' : ' ago';
  const futurePrefix = isZh ? '' : 'in ';
  const futureSuffix = isZh ? '后' : '';
  const format = (n: number, unit: string) => {
    if (diffSec >= 0) return `${futurePrefix}${n}${unit}${futureSuffix}`;
    return `${n}${unit}${pastSuffix}`;
  };
  if (absSec < 10) return isZh ? '刚刚' : 'just now';
  if (absSec < 60) return format(absSec, 's');
  if (absSec < 3600) return format(Math.round(absSec / 60), 'm');
  if (absSec < 86400) return format(Math.round(absSec / 3600), 'h');
  if (absSec < 2592000) return format(Math.round(absSec / 86400), 'd');
  return new Date(ts).toLocaleDateString(locale || undefined, { month: 'short', day: 'numeric' });
}

function getJobDayKey(updatedAt: number): string {
  if (!updatedAt) return 'unknown';
  const date = new Date(updatedAt);
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${year}-${month}-${day}`;
}

function formatJobDayLabel(dayKey: string, locale?: string): string {
  if (dayKey === 'unknown') return 'Unknown Date';
  const date = new Date(`${dayKey}T00:00:00`);
  return date.toLocaleDateString(locale || undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    weekday: 'short',
  });
}

const GRAPH_ICON = (
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <circle cx="6" cy="6" r="2.5" />
    <circle cx="18" cy="6" r="2.5" />
    <circle cx="12" cy="18" r="2.5" />
    <path d="M8.2 7.5 11 15.7" />
    <path d="M15.8 7.5 13 15.7" />
    <path d="M8.5 6h7" />
  </svg>
);

const CHAT_ICON = (
  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
    <path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z" />
  </svg>
);

function getJobIcon(job: JobInfo) {
  if (job.mode === 'graph') return GRAPH_ICON;
  return CHAT_ICON;
}

const STATUS_ICON_RUNNING = (
  <svg className="status-icon-spinning" width="18" height="18" viewBox="0 0 24 24" fill="none" strokeWidth="2.5" strokeLinecap="round">
    <path d="M12 2a10 10 0 0 1 10 10" stroke="#16a34a" />
    <path d="M12 2a10 10 0 0 0-10 10" stroke="#b7dfc1" />
    <path d="M2 12a10 10 0 0 0 10 10" stroke="#9bd3aa" />
    <path d="M22 12a10 10 0 0 1-10 10" stroke="#34d399" />
  </svg>
);

const STATUS_ICON_COMPLETED = (
  <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
    <circle cx="12" cy="12" r="10" fill="#dcfce7" stroke="#22c55e" strokeWidth="1.5" />
    <path d="M8 12.5l2.5 2.5 5-5" stroke="#16a34a" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" fill="none" />
  </svg>
);

const STATUS_ICON_FAILED = (
  <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
    <circle cx="12" cy="12" r="10" fill="#fee2e2" stroke="#ef4444" strokeWidth="1.5" />
    <path d="M15 9l-6 6M9 9l6 6" stroke="#dc2626" strokeWidth="2.5" strokeLinecap="round" />
  </svg>
);

const STATUS_ICON_STOPPED = (
  <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
    <circle cx="12" cy="12" r="10" fill="#f3f4f6" stroke="#9ca3af" strokeWidth="1.5" />
    <rect x="8.5" y="8.5" width="7" height="7" rx="1" fill="#9ca3af" />
  </svg>
);

const STATUS_ICON_PENDING = (
  <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
    <circle cx="12" cy="12" r="10" fill="#fef9c3" stroke="#eab308" strokeWidth="1.5" />
    <circle cx="12" cy="12" r="1.5" fill="#a16207" />
    <path d="M12 7v4.5" stroke="#a16207" strokeWidth="2" strokeLinecap="round" />
    <path d="M12 12.5l3 1.5" stroke="#a16207" strokeWidth="2" strokeLinecap="round" />
  </svg>
);

const STATUS_ICON_DEFAULT = (
  <svg width="18" height="18" viewBox="0 0 24 24" fill="none">
    <circle cx="12" cy="12" r="10" fill="#f1f5f9" stroke="#cbd5e1" strokeWidth="1.5" />
  </svg>
);

function getJobStatusIcon(status: string) {
  switch (status) {
    case 'running': return STATUS_ICON_RUNNING;
    case 'completed': return STATUS_ICON_COMPLETED;
    case 'failed': return STATUS_ICON_FAILED;
    case 'stopped': return STATUS_ICON_STOPPED;
    case 'pending': return STATUS_ICON_PENDING;
    default: return STATUS_ICON_DEFAULT;
  }
}

// ---- Memoized job history row ----

interface JobHistoryRowProps {
  job: JobInfo;
  modelLabel: string | null;
  workspaceName?: string;
  onSelect: (jobId: string, workspaceId?: string) => void;
  onPin?: (e: React.MouseEvent, job: JobInfo) => void;
  onDelete?: (e: React.MouseEvent, job: JobInfo) => void;
}

const JobHistoryRow = memo(function JobHistoryRow({ job, modelLabel, workspaceName, onSelect, onPin, onDelete }: JobHistoryRowProps) {
  const { i18n } = useTranslation();
  const wsColor = job.workspaceId ? workspaceColor(job.workspaceId) : undefined;
  const isMobile = useIsMobile();
  const workspaceDisplay = workspaceName && isMobile ? Array.from(workspaceName).slice(0, 3).join('') : workspaceName;
  const title = getJobTitle(job);
  const isPinned = (job.pinnedAt ?? 0) > 0;
  const titleChars = Array.from(title);
  const titleDisplay = isMobile && titleChars.length > 10 ? titleChars.slice(0, 10).join('') + '…' : title;
  const jobUrl = (() => {
    const url = new URL(window.location.href);
    url.searchParams.delete('sessionId');
    url.searchParams.delete('view');
    url.searchParams.set('jobId', job.id);
    if (job.workspaceId) url.searchParams.set('workspaceId', job.workspaceId);
    return url.toString();
  })();
  return (
    <a
      className={`home-job-history-row ${job.status === 'running' ? 'running' : ''}`}
      data-testid="home-job-history-row"
      data-job-id={job.id}
      data-job-mode={job.mode || 'interactive'}
      data-job-status={job.status}
      data-pinned={isPinned ? 'true' : 'false'}
      href={jobUrl}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey) {
          e.stopPropagation();
          return;
        }
        e.preventDefault();
        onSelect(job.id, job.workspaceId);
      }}
      onAuxClick={(e) => {
        if (e.button === 1) e.stopPropagation();
      }}
    >
      <span className="home-job-history-row-icon">{getJobIcon(job)}</span>
      <span className={`home-job-history-row-status-icon ${job.status}`}>{getJobStatusIcon(job.status)}</span>
      <span className="home-job-history-row-title" title={title}>{titleDisplay}</span>
      <div className="home-job-history-row-meta">
        {job.scheduleId && (
          <span className="home-job-history-row-sched" title="定时任务触发" aria-label="定时任务触发">
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <circle cx="12" cy="13" r="8" />
              <path d="M12 9v4l2.5 2.5" />
              <path d="M5 3 2 6M22 6l-3-3" />
            </svg>
          </span>
        )}
        {workspaceName && (
          <span
            className="home-job-history-row-ws"
            title={workspaceName}
            style={wsColor ? ({ '--ws-color': wsColor } as React.CSSProperties) : undefined}
          >
            {workspaceDisplay}
          </span>
        )}
        {modelLabel && <span className="home-job-history-row-model" title={modelLabel}>{modelLabel}</span>}
        <span className="home-job-history-row-time">
          <ClockIcon className="home-job-history-row-time-icon" />
          {formatJobTime(job.updatedAt, i18n.language)}
        </span>
      </div>
      {onPin && <button
        className={`home-job-history-row-pin ${isPinned ? 'pinned' : ''}`}
        onClick={(e) => { e.preventDefault(); onPin(e, job); }}
        title={isPinned ? 'Unpin' : 'Pin'}
        aria-label={isPinned ? 'Unpin job' : 'Pin job'}
        data-testid="home-job-history-row-pin"
      >
        ★
      </button>}
      {onDelete && <button
        className="home-job-history-row-delete"
        onClick={(e) => { e.preventDefault(); onDelete(e, job); }}
        title="Delete"
        data-testid="home-job-history-row-delete"
      >
        ×
      </button>}
    </a>
  );
});

export function ChatPage({ onStartChat, isInitializing, refreshKey, workspaceWorkdir, workspaceId, workspaceTitle, onSelectWorkspace, onSelectJob, onOpenSettings, onOpenAgentSettings, onOpenStats, onOpenGraph }: ChatPageProps) {
  const principal = useAuthPrincipal();
  const canReadJobs = principal?.permissions.includes('job.read') ?? false;
  const canExecuteJobs = principal?.permissions.includes('job.execute') ?? false;
  const canReadAgents = principal?.permissions.includes('agent.read') ?? false;
  const canReadConfig = principal?.permissions.includes('config.read') ?? false;
  const canManageJobs = principal?.permissions.includes('job.manage') ?? false;
  const canReadSchedules = principal?.permissions.includes('schedule.read') ?? false;
  const canWriteSchedules = principal?.permissions.includes('schedule.write') ?? false;
  const canReadFiles = principal?.permissions.includes('file.read') ?? false;
  const canWriteFiles = principal?.permissions.includes('file.write') ?? false;
  const { connected } = useConnectionStatus();
  const { t, i18n } = useTranslation();
  const localDraftStorageKey = principal
    ? `quartet:composer_draft:home:${encodeURIComponent(principal.user.id)}:${encodeURIComponent(workspaceId || 'default')}`
    : null;
  const [input, setInput, clearInputDraft] = useLocalTextDraft(localDraftStorageKey);
  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [agentsLoaded, setAgentsLoaded] = useState(false);
  const [acpConfigError, setAcpConfigError] = useState<string | null>(null);
  const [agentPrefs, setAgentPrefs] = useState<AgentPrefsMap>({});
  const [workdir, setWorkdir] = useState('');
  const [showDirPicker, setShowDirPicker] = useState(false);
  const [jobEnable, setJobEnable] = useState(false);
  const [selectedIndex, setSelectedIndex] = useState<number>(0);
  const [showDropdown, setShowDropdown] = useState(false);
  const [showModelDropdown, setShowModelDropdown] = useState(false);
  const [showModeDropdown, setShowModeDropdown] = useState(false);
  const [showThoughtLevelDropdown, setShowThoughtLevelDropdown] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);
  const modelDropdownRef = useRef<HTMLDivElement>(null);
  const modeDropdownRef = useRef<HTMLDivElement>(null);
  const thoughtLevelDropdownRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const { pendingAttachments, addAttachments, removeAttachment, clearAttachments } = usePendingAttachments(uploadChatAttachment);
  const isMobile = useIsMobile();
  // Local cache of "sent" messages (recorded on click send, regardless of server success/failure)
  const localHistoryStorageKey = `quartet:sent_history:${workspaceId || 'default'}`;
  const [historyItems, setHistoryItems] = useState<LocalSentMessage[]>(() => readLocalSentMessages(localHistoryStorageKey));
  const historyCursorRef = useRef<number | null>(null);
  const historyDraftRef = useRef<{ input: string; pickedImageUrls: string[]; pickedFileAttachments: FileAttachment[] } | null>(null);
  const [pickedImageUrls, setPickedImageUrls] = useState<string[]>([]);
  const [pickedFileAttachments, setPickedFileAttachments] = useState<FileAttachment[]>([]);

  useEffect(() => {
    // When switching workspace, load the corresponding history.
    setHistoryItems(readLocalSentMessages(localHistoryStorageKey));
    historyCursorRef.current = null;
    historyDraftRef.current = null;
    setPickedImageUrls([]);
    setPickedFileAttachments([]);
  }, [localHistoryStorageKey]);

  const [hideScheduledJobs, setHideScheduledJobs] = useState<boolean>(() => {
    // Default on — most users aren't interested in scheduled-task jobs mixed
    // into the main list.
    try {
      const v = localStorage.getItem('home_hide_scheduled');
      return v === null ? true : v === '1';
    } catch { return true; }
  });
  useEffect(() => {
    try { localStorage.setItem('home_hide_scheduled', hideScheduledJobs ? '1' : '0'); } catch { /* ignore */ }
  }, [hideScheduledJobs]);

  // Job History filter: when set, /api/v1/job/list is called with a specific
  // workspaceId so the list shows only that workspace's jobs. Empty string =
  // all workspaces (the default).
  const [filterWorkspaceId, setFilterWorkspaceId] = useState<string>(() => {
    try { return localStorage.getItem('home_filter_workspace_id') || ''; } catch { return ''; }
  });
  useEffect(() => {
    try { localStorage.setItem('home_filter_workspace_id', filterWorkspaceId); } catch { /* ignore */ }
  }, [filterWorkspaceId]);
  const [wsFilterOpen, setWsFilterOpen] = useState(false);
  const wsFilterRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!wsFilterOpen) return;
    const onDocClick = (e: MouseEvent) => {
      if (!wsFilterRef.current?.contains(e.target as Node)) setWsFilterOpen(false);
    };
    document.addEventListener('mousedown', onDocClick);
    return () => document.removeEventListener('mousedown', onDocClick);
  }, [wsFilterOpen]);

  const [allWorkspaces, setAllWorkspaces] = useState<WorkspaceInfo[]>([]);
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch('/api/v1/workspace/list');
        if (!res.ok) return;
        const data = await res.json();
        if (cancelled) return;
        const list = (data?.workspaces || []) as WorkspaceInfo[];
        registerWorkspaceColors(list);
        setAllWorkspaces(list);
      } catch { /* ignore */ }
    })();
    return () => { cancelled = true; };
  }, [refreshKey]);
  // Keep the workspace list in sync with edits made in the Settings panel
  // without relying on the refreshKey round-trip (Settings modal only bumps
  // the key through onSettingsChanged for general settings).
  useEffect(() => {
    const onUpdated = (e: Event) => {
      const ws = (e as CustomEvent).detail as WorkspaceInfo | null;
      if (!ws?.id) return;
      registerWorkspaceColors([ws]);
      setAllWorkspaces((prev) => {
        const idx = prev.findIndex((w) => w.id === ws.id);
        if (idx < 0) return [...prev, ws];
        const next = prev.slice();
        next[idx] = { ...next[idx], ...ws };
        return next;
      });
    };
    const onDeleted = (e: Event) => {
      const detail = (e as CustomEvent).detail as { id?: string } | null;
      if (!detail?.id) return;
      setAllWorkspaces((prev) => prev.filter((w) => w.id !== detail.id));
    };
    const onListUpdated = (e: Event) => {
      const list = (e as CustomEvent).detail as WorkspaceInfo[] | null;
      if (!Array.isArray(list)) return;
      registerWorkspaceColors(list);
      setAllWorkspaces(list);
    };
    window.addEventListener('quartet:workspace-updated', onUpdated);
    window.addEventListener('quartet:workspace-deleted', onDeleted);
    window.addEventListener('quartet:workspace-list-updated', onListUpdated);
    return () => {
      window.removeEventListener('quartet:workspace-updated', onUpdated);
      window.removeEventListener('quartet:workspace-deleted', onDeleted);
      window.removeEventListener('quartet:workspace-list-updated', onListUpdated);
    };
  }, []);

  // Footer workspace-tag switcher state. Mirrors the chat-page ChatInput
  // behavior so the "Workspace(name) : path" row in the home compose area is
  // itself a clickable switcher (not just a viewer).
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
  const canSwitchWorkspaceInFooter = !!(onSelectWorkspace && allWorkspaces.length > 0);
  // Current git branch of the home compose workdir, shown in the workspace tag.
  const gitBranch = useGitBranch(workdir, canReadFiles);

  const wsNameById = useMemo(() => {
    const m = new Map<string, string>();
    for (const ws of allWorkspaces) m.set(ws.id, ws.title);
    return m;
  }, [allWorkspaces]);

  // Header-adjacent home data state
  const {
    jobs,
    hasMore: hasMoreJobs,
    isLoading: isLoadingJobs,
    isLoadingMore: isLoadingMoreJobs,
    loadMore: loadMoreJobs,
    refresh: refreshJobs,
    removeJob: removeJobFromList,
    patchJob,
    dailyStats,
  } = useJobList({
    workspaceId: filterWorkspaceId || undefined,
    excludeScheduled: hideScheduledJobs,
    disabled: !canReadJobs,
  });
  // Scheduled tasks state
  const [schedules, setSchedules] = useState<ScheduleInfo[]>([]);
  // Flips to true after the first fetchSchedules attempt completes (success or
  // failure). Used with isLoadingJobs to distinguish "still loading" from
  // "genuinely empty" so the home layout doesn't fall into the empty-state
  // flex-end layout while the backend is unreachable.
  const [schedulesLoaded, setSchedulesLoaded] = useState(false);
  const [scheduleModal, setScheduleModal] = useState<{ mode: 'closed' } | { mode: 'create' } | { mode: 'edit'; schedule: ScheduleInfo }>({ mode: 'closed' });
  const [scheduleDeleteConfirm, setScheduleDeleteConfirm] = useState<{ id: string; name: string } | null>(null);
  const [jobHistoryExpanded, setJobHistoryExpanded] = useState(true);
  const [schedulesExpanded, setSchedulesExpanded] = useState(!isMobile);
  const loadMoreSentinelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = loadMoreSentinelRef.current;
    if (!el || !hasMoreJobs || isLoadingMoreJobs || !jobHistoryExpanded) return;
    if (typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          void loadMoreJobs();
        }
      },
      { rootMargin: '200px 0px' }
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [hasMoreJobs, isLoadingMoreJobs, jobHistoryExpanded, loadMoreJobs]);

  // @ mention state
  const [mentionState, setMentionState] = useState<{ keyword: string; start: number } | null>(null);
  const [mentionActiveIndex, setMentionActiveIndex] = useState(0);

  // Slash ("/") completion on the home page: skills only (built-in commands
  // like /workspace operate on an existing chat), plus the skill-name chip
  // highlight backdrop. Shared with ChatInput — see SlashCompletion.tsx.
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
  } = useSlashCompletion({ setInput, textareaRef, includeCommands: false, workspaceId });
  const backdropRef = useRef<HTMLDivElement>(null);
  const handleImageSelect = useCallback(async (files: FileList | null) => {
    await addAttachments(files);
  }, [addAttachments]);

  const handlePaste = useCallback((e: React.ClipboardEvent) => {
    if (!canWriteFiles) return;
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
  }, [canWriteFiles, handleImageSelect]);

  useEffect(() => {
    if (!canExecuteJobs || !canReadAgents) {
      setAgents([]);
      setAgentsLoaded(true);
      setJobEnable(false);
      return;
    }
    let cancelled = false;
    setAgentsLoaded(false);
    void migrateStoredAgentReferences(workspaceId)
      .then(() => Promise.all([fetchAgentList(), canReadConfig ? fetchAgentPrefs() : Promise.resolve({})]))
      .then(([{ agents: list, workdir: wd, jobEnable: je }, prefsMap]) => {
      if (cancelled) return;
      setJobEnable(je);
      setAgentPrefs(prefsMap);
      setAcpConfigError(null);

      // When a workspace is active, stick with its workdir (even if it hasn't
      // been hydrated yet — the server fills it from the workspace record on
      // Job create). Falling back to the API default here would display and
      // submit the wrong directory during the hydration window for
      // direct-link / cold-start navigations.
      setWorkdir(workspaceId ? (workspaceWorkdir || '') : wd);

      if (list.length > 0) {
        // Workspace-level default agent/model takes priority over the last
        // globally-used agent. This is the only path that creates a Job from
        // the home page, so we must consume prefs here — App.handleStartChat's
        // fallback never triggers because this surface always passes a
        // non-empty agent/model down.
        const prefs = loadWorkspacePrefs(workspaceId);
        const savedType = localStorage.getItem('last_agent_type');

        const pickIdx = (t: string | undefined | null) => {
          if (!t) return -1;
          return list.findIndex((a) => a.type === t);
        };

        let idx = pickIdx(prefs.defaultAgent);
        if (idx < 0) idx = pickIdx(savedType);
        if (idx < 0) idx = 0;

        // Resolve the picked agent's starting model/mode/thought_level via the
        // shared resolver: workspace default model > per-agent default >
        // first available. Per-agent defaults replace the old opus auto-pick;
        // a stale saved default falls back to the first entry. Does not persist
        // — per-agent choice is reset each mount by design.
        const nextList = list.map((a, i) => {
          if (i !== idx) return a;
          const resolved = resolveAgentDefaults(a, prefsForAgent(prefsMap, a), { workspaceDefaultModel: prefs.defaultModel });
          return applyDefaultsToAgent(a, resolved);
        });
        const selected = nextList[idx];
        const selectedModelId = selected.models?.currentModelId;
        setAgents(nextList);
        setSelectedIndex(idx);
        if (selectedModelId) {
          void relinkACPThoughtLevels(selected.type, selectedModelId).then((state) => {
            if (cancelled) return;
            let thoughtLevels = state;
            const preferred = prefsForAgent(prefsMap, selected)?.default_thought_level;
            if (preferred && thoughtLevels.availableThoughtLevels.some((level) => level.id === preferred)) {
              thoughtLevels = { ...thoughtLevels, currentThoughtLevelId: preferred };
            }
            setAgents((prev) => prev.map((agent, agentIndex) =>
              agentIndex === idx && agent.type === selected.type && agent.models?.currentModelId === selectedModelId
                ? { ...agent, thoughtLevels }
                : agent
            ));
          }).catch((err) => {
            if (cancelled) return;
            const msg = err instanceof Error ? err.message : String(err);
            setAcpConfigError(msg);
            console.error('[ChatPage] refresh initial ACP thought levels failed:', err);
          });
        }
      } else {
        setAgents(list);
      }
      setAgentsLoaded(true);
      })
      .catch((err) => {
        if (cancelled) return;
        const message = err instanceof Error ? err.message : String(err);
        setAgents([]);
        setJobEnable(false);
        setAcpConfigError(message);
        setAgentsLoaded(true);
        console.error('Failed to fetch agent list:', err);
      });
    return () => {
      cancelled = true;
    };
  }, [canExecuteJobs, canReadAgents, canReadConfig, refreshKey, workspaceWorkdir, workspaceId]);

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

  // Fetch scheduled tasks on mount and poll. Scheduled tasks are now
  // workspace-independent (方向二-d): we always fetch the global list.
  const fetchSchedules = useCallback(async () => {
    if (!canReadSchedules) {
      setSchedules([]);
      setSchedulesLoaded(true);
      return;
    }
    try {
      const res = await fetch(`/api/v1/schedule/list`);
      if (res.ok) {
        const data = await res.json();
        setSchedules(data.schedules || []);
      }
    } catch { /* ignore */ }
    finally {
      setSchedulesLoaded(true);
    }
  }, [canReadSchedules]);

  useEffect(() => {
    fetchSchedules();
    const interval = setInterval(fetchSchedules, 60_000);
    const onFocus = () => fetchSchedules();
    window.addEventListener('focus', onFocus);
    return () => {
      clearInterval(interval);
      window.removeEventListener('focus', onFocus);
    };
  }, [fetchSchedules]);

  const handleScheduleSave = () => {
    setScheduleModal({ mode: 'closed' });
    fetchSchedules();
    // Refresh job list after a short delay to pick up newly triggered jobs
    setTimeout(() => { void refreshJobs(); }, 1500);
  };

  const handleScheduleDelete = async (id: string) => {
    try {
      const res = await fetch(`/api/v1/schedule/${id}`, { method: 'DELETE' });
      if (!res.ok) {
        console.error('Failed to delete schedule:', await res.text());
      }
      fetchSchedules();
    } catch (err) {
      console.error('Failed to delete schedule:', err);
    }
    setScheduleDeleteConfirm(null);
  };

  const handleScheduleToggle = async (id: string) => {
    try {
      const res = await fetch(`/api/v1/schedule/${id}/toggle`, { method: 'POST' });
      if (!res.ok) {
        console.error('Failed to toggle schedule:', await res.text());
      }
      fetchSchedules();
    } catch (err) {
      console.error('Failed to toggle schedule:', err);
    }
  };

  const selectedAgent = agents[selectedIndex] ?? null;

  // Switch to an agent and apply its per-agent defaults (model/mode/thought).
  // Manual switch uses per-agent defaults only (no workspace-model override) so
  // the user's explicit agent choice drives the model; stale defaults fall back
  // to the first available entry via resolveAgentDefaults. Plain function: only
  // called from inline .map() onClicks, never passed to a memoized child.
  const selectAgentAt = (idx: number) => {
    setSelectedIndex(idx);
    const agent = agents[idx];
    if (!agent) return;
    const resolved = resolveAgentDefaults(agent, prefsForAgent(agentPrefs, agent));
    // Switching Agent type must not reset that Agent's cached mode. Mode is
    // independent from the model-linked thought-level refresh and changes only
    // through the explicit mode selector.
    const selected = applyDefaultsToAgent(agent, {
      modelId: resolved.modelId,
      thoughtLevelId: resolved.thoughtLevelId,
    });
    setAgents((prev) => prev.map((a, i) => i === idx ? selected : a));
    const selectedModelId = selected.models?.currentModelId;
    if (!selectedModelId) return;

    setAcpConfigError(null);
    void relinkACPThoughtLevels(selected.type, selectedModelId).then((state) => {
      let thoughtLevels = state;
      const preferred = prefsForAgent(agentPrefs, selected)?.default_thought_level;
      if (preferred && state.availableThoughtLevels.some((level) => level.id === preferred)) {
        thoughtLevels = { ...state, currentThoughtLevelId: preferred };
      }
      setAgents((prev) => prev.map((a, i) =>
        i === idx && a.type === selected.type && a.models?.currentModelId === selectedModelId
          ? { ...a, thoughtLevels }
          : a
      ));
    }).catch((err) => {
      const msg = err instanceof Error ? err.message : String(err);
      setAcpConfigError(msg);
      console.error('[ChatPage] refresh selected ACP thought levels failed:', err);
    });
  };


  // modelId -> human-readable name map; rebuilt only when agents change.
  const modelLabelMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const agent of agents) {
      const available = agent.models?.availableModels;
      if (!available) continue;
      for (const m of available) {
        if (!map.has(m.modelId)) map.set(m.modelId, m.name);
      }
    }
    return map;
  }, [agents]);

  const getJobModelLabel = useCallback((job: JobInfo): string | null => {
    if (!job.modelId) return null;
    const label = modelLabelMap.get(job.modelId);
    return (label ?? job.modelId).replace(/\s*\([^)]*\)\s*$/, '');
  }, [modelLabelMap]);

  const jobsByDay = useMemo(() => {
    // Group by dayKey using a Map so jobs sharing a date collapse into a
    // single group even when they aren't contiguous. The list is sorted by
    // createdAt, but the dayKey is derived from updatedAt, so a job created
    // yesterday but touched today can land between today's entries and cause
    // two groups with the same dayKey — which triggers React's duplicate-key
    // warning on the <section key={dayKey}>.
    //
    // Note: jobs here are already filtered server-side — useJobList sends
    // `excludeScheduled` to /api/v1/job/list, so each page stays full even
    // when the workspace is dominated by scheduled-task jobs.
    const byDay = new Map<string, JobInfo[]>();
    for (const job of jobs) {
      const dayKey = getJobDayKey(job.updatedAt);
      const items = byDay.get(dayKey);
      if (items) items.push(job);
      else byDay.set(dayKey, [job]);
    }
    return Array.from(byDay, ([dayKey, items]) => ({ dayKey, items }));
  }, [jobs]);

  const handleJobSelect = useCallback((jobId: string, workspaceId?: string) => {
    onSelectJob?.(jobId, workspaceId);
  }, [onSelectJob]);

  const [deleteConfirm, setDeleteConfirm] = useState<{ jobId: string; title: string } | null>(null);

  const handleJobDeleteClick = useCallback((e: React.MouseEvent, job: JobInfo) => {
    e.stopPropagation();
    setDeleteConfirm({ jobId: job.id, title: getJobTitle(job) });
  }, []);

  const handleJobPinClick = useCallback(async (e: React.MouseEvent, job: JobInfo) => {
    e.stopPropagation();
    const nextPinned = !((job.pinnedAt ?? 0) > 0);
    try {
      const res = await fetch(`/api/v1/job/${job.id}/pin`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pinned: nextPinned }),
      });
      if (!res.ok) return;
      const data = await res.json() as { pinnedAt?: number; updatedAt?: number };
      const patch: Partial<JobInfo> = { pinnedAt: data.pinnedAt ?? 0 };
      if (typeof data.updatedAt === 'number' && data.updatedAt > 0) {
        patch.updatedAt = data.updatedAt;
      }
      patchJob(job.id, patch);
    } catch (err) {
      console.error('Failed to update job pin:', err);
    }
  }, [patchJob]);

  const handleJobDeleteConfirm = async () => {
    if (!deleteConfirm) return;
    const { jobId } = deleteConfirm;
    setDeleteConfirm(null);
    try {
      const res = await fetch(`/api/v1/job/${jobId}`, { method: 'DELETE' });
      if (res.ok) {
        removeJobFromList(jobId);
      }
    } catch (err) {
      console.error('Failed to delete job:', err);
    }
  };

  // applyACPConfig pushes a Home (session-less) config switch to the backend
  // and merges the refreshed selector lists back into the agent at idx. The
  // For model switches, only the model is replayed: mode / thought_level may
  // belong to the previous model and must not poison the relink request. On
  // failure it clears the optimistic pick via rollback and surfaces the error.
  const applyACPConfig = useCallback(
    async (
      target: ACPConfigTarget,
      idx: number,
      change: { model?: string; mode?: string; thoughtLevel?: string },
      rollback: () => void,
    ) => {
      const agent = agents[idx];
      if (!agent) return;
      setAcpConfigError(null);
      try {
        const state: ACPConfigState = await setACPConfig({
          target,
          agentType: agent.type,
          model: change.model ?? agent.models?.currentModelId,
          mode: target === 'model' ? undefined : change.mode ?? agent.modes?.currentModeId,
          thoughtLevel: target === 'model' ? undefined : change.thoughtLevel ?? agent.thoughtLevels?.currentThoughtLevelId,
        });
        // Merge only the lists the backend refreshed; keep current ones for
        // the nil lists (mode switches return none).
        setAgents((prev) => prev.map((a, i) =>
          i === idx
            ? {
                ...a,
                models: state.models ?? a.models,
                modes: state.modes ?? a.modes,
                thoughtLevels: target === 'model' ? state.thoughtLevels : state.thoughtLevels ?? a.thoughtLevels,
              }
            : a
        ));
      } catch (err) {
        rollback();
        const msg = err instanceof Error ? err.message : String(err);
        setAcpConfigError(msg);
        console.error(`[ChatPage] set ACP ${target} failed:`, err);
      }
    },
    [agents],
  );

  const handleSelectModel = useCallback((modelId: string) => {
    const agent = agents[selectedIndex];
    if (!agent?.models) return;
    const prevModelId = agent.models.currentModelId;
    if (modelId === prevModelId) return;
    // Optimistic: reflect the pick immediately, then push it to a throwaway
    // ACP session and merge back the refreshed (linked) lists. Roll back on
    // failure so the dropdown never shows a selection the agent rejected.
    setAgents((prev) => prev.map((a, idx) =>
      idx === selectedIndex && a.models
        ? { ...a, models: { ...a.models, currentModelId: modelId } }
        : a
    ));
    void applyACPConfig('model', selectedIndex, { model: modelId }, () => {
      setAgents((prev) => prev.map((a, idx) =>
        idx === selectedIndex && a.models
          ? { ...a, models: { ...a.models, currentModelId: prevModelId } }
          : a
      ));
    });
  }, [agents, selectedIndex, applyACPConfig]);

  const handleSelectMode = useCallback((modeId: string) => {
    const agent = agents[selectedIndex];
    if (!agent?.modes) return;
    const prevModeId = agent.modes.currentModeId;
    if (modeId === prevModeId) return;
    setAgents((prev) => prev.map((a, idx) =>
      idx === selectedIndex && a.modes
        ? { ...a, modes: { ...a.modes, currentModeId: modeId } }
        : a
    ));
    void applyACPConfig('mode', selectedIndex, { mode: modeId }, () => {
      setAgents((prev) => prev.map((a, idx) =>
        idx === selectedIndex && a.modes
          ? { ...a, modes: { ...a.modes, currentModeId: prevModeId } }
          : a
      ));
    });
  }, [agents, selectedIndex, applyACPConfig]);

  const handleSelectThoughtLevel = useCallback((thoughtLevelId: string) => {
    const agent = agents[selectedIndex];
    if (!agent?.thoughtLevels) return;
    const prevLevelId = agent.thoughtLevels.currentThoughtLevelId;
    if (thoughtLevelId === prevLevelId) return;
    setAgents((prev) => prev.map((a, idx) =>
      idx === selectedIndex && a.thoughtLevels
        ? { ...a, thoughtLevels: { ...a.thoughtLevels, currentThoughtLevelId: thoughtLevelId } }
        : a
    ));
    void applyACPConfig('thoughtLevel', selectedIndex, { thoughtLevel: thoughtLevelId }, () => {
      setAgents((prev) => prev.map((a, idx) =>
        idx === selectedIndex && a.thoughtLevels
          ? { ...a, thoughtLevels: { ...a.thoughtLevels, currentThoughtLevelId: prevLevelId } }
          : a
      ));
    });
  }, [agents, selectedIndex, applyACPConfig]);

  const handleSelectDir = () => {
    setShowDirPicker(true);
  };

  const handleDirConfirm = (dir: string) => {
    setWorkdir(dir);
    setShowDirPicker(false);
  };

  const handleSubmit = () => {
    const hasContent = input.trim() || pendingAttachments.length > 0 || pickedImageUrls.length > 0 || pickedFileAttachments.length > 0;
    const allUploaded = pendingAttachments.every((attachment) => attachment.uploaded && !attachment.uploading);
    if (!hasContent || isInitializing || !selectedAgent || !jobEnable) return;
    if (pendingAttachments.length > 0 && !allUploaded) {
      const failedUpload = pendingAttachments.find((attachment) => attachment.error)?.error;
      if (failedUpload) showToast(t('chat.attachmentUploadFailed', { error: failedUpload }));
      return;
    }

    const uploaded = pendingAttachments.map((attachment) => attachment.uploaded).filter((attachment): attachment is UploadedAttachment => !!attachment);
    const imageUrls = [...pickedImageUrls, ...uploaded.filter((attachment) => attachment.isImage).map((attachment) => attachment.path)];
    const fileAttachments = [
      ...pickedFileAttachments,
      ...uploaded.filter((attachment) => !attachment.isImage).map(({ isImage: _isImage, ...attachment }) => attachment),
    ];
    const contentToSend = input.trim() || (fileAttachments.length > 0 ? '[file]' : '[image]');

    // Record locally on send click, regardless of server result.
    const nextHistory = appendLocalSentMessage(localHistoryStorageKey, {
      content: contentToSend,
      imageUrls: imageUrls.length > 0 ? imageUrls : undefined,
      fileAttachments: fileAttachments.length > 0 ? fileAttachments : undefined,
    });
    setHistoryItems(nextHistory);

    clearInputDraft();
    onStartChat(
      contentToSend,
      selectedAgent.models?.currentModelId || selectedAgent.model_id,
      selectedAgent.type,
      workdir || undefined,
      imageUrls.length > 0 ? imageUrls : undefined,
      fileAttachments.length > 0 ? fileAttachments : undefined,
      selectedAgent.modes?.currentModeId,
      selectedAgent.thoughtLevels?.currentThoughtLevelId,
    );
    setPickedImageUrls([]);
    setPickedFileAttachments([]);
    clearAttachments();
    closeSlash();
    historyCursorRef.current = null;
    historyDraftRef.current = null;
  };

  const handleRemovePickedImage = useCallback((url: string) => {
    setPickedImageUrls((prev) => prev.filter((u) => u !== url));
  }, []);

  const handleRemovePickedFile = useCallback((path: string) => {
    setPickedFileAttachments((previous) => previous.filter((file) => file.path !== path));
  }, []);

  const handleMentionSelect = (file: FileResult) => {
    if (!mentionState) return;
    const before = input.slice(0, mentionState.start);
    const after = input.slice(mentionState.start + 1 + mentionState.keyword.length);
    const newInput = before + '@' + file.path + ' ' + after;
    setInput(newInput);
    setMentionState(null);
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
    const val = e.target.value;
    setInput(val);
    // Any manual edits exit history navigation.
    historyCursorRef.current = null;
    historyDraftRef.current = null;
    const cursorPos = e.target.selectionStart ?? val.length;
    const before = val.slice(0, cursorPos);
    const match = before.match(/@([^\s@]*)$/);
    setMentionState(match ? { keyword: match[1], start: before.length - match[0].length } : null);
    if (match) setMentionActiveIndex(0);
    // Slash completion (home page): `/xxx` with no space lists commands + skills.
    updateSlash(val, !!match);
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Ignore Enter during IME composition (CJK input methods)
    if (isImeComposing(e)) return;
    // Slash completion navigation takes priority over plain Enter.
    if (slashCompletionKeyDown(e, slashItems, slashActiveIdx, setSlashActiveIdx, applySlashItem, closeSlash)) {
      return;
    }
    if (mentionState) {
      if (e.key === 'ArrowDown') { e.preventDefault(); setMentionActiveIndex(i => i + 1); return; }
      if (e.key === 'ArrowUp') { e.preventDefault(); setMentionActiveIndex(i => Math.max(0, i - 1)); return; }
      if (e.key === 'Escape') { e.preventDefault(); setMentionState(null); return; }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        const popup = document.querySelector('.file-mention-item.active') as HTMLElement;
        if (popup) popup.click();
        return;
      }
    }

    // Local sent-message history navigation (home page):
    // ArrowUp when empty recalls previous; ArrowDown restores draft.
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
        handleSubmit();
      }
    }
  };

  // Home composer model dropdown: split into pinned favorites + the rest when
  // the selected agent has favorites configured. renderHomeModelItem keeps a
  // single source of truth across the favorites/rest groups and mobile/desktop.
  // Defined just before render so it can reference handleSelectModel et al.
  const renderHomeModelItem = (m: { modelId: string; name: string; description?: string }) => {
    const cur = selectedAgent?.models?.currentModelId;
    return (
      <div
        key={m.modelId}
        className={`model-dropdown-item ${m.modelId === cur ? 'active' : ''}`}
        onClick={() => {
          handleSelectModel(m.modelId);
          setShowModelDropdown(false);
        }}
      >
        <div className="model-dropdown-info">
          <span className="model-dropdown-name">{m.name}</span>
          {m.description && <span className="model-dropdown-provider">{m.description}</span>}
        </div>
        {m.modelId === cur && (
          <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M20 6L9 17l-5-5" />
          </svg>
        )}
      </div>
    );
  };

  const renderHomeModelItems = () => {
    const all = selectedAgent?.models?.availableModels ?? [];
    const { favorites, rest } = splitFavoriteModels(all, prefsForAgent(agentPrefs, selectedAgent)?.favorite_model_ids);
    if (favorites.length === 0) {
      return all.map(renderHomeModelItem);
    }
    return (
      <>
        <div className="model-dropdown-group-label">{t('chat.favoriteModels')}</div>
        {favorites.map(renderHomeModelItem)}
        {rest.length > 0 && <div className="model-dropdown-group-label">{t('chat.otherModels')}</div>}
        {rest.map(renderHomeModelItem)}
      </>
    );
  };

  // Loading (data not yet ready) or offline: drives the centered spinner that
  // now renders below Job History, and suppresses the "no jobs" body message so
  // the loading state doesn't read as an empty workspace.
  const homeLoadingOrOffline = jobs.length === 0 && (!schedulesLoaded || isLoadingJobs || !connected);
  return (
    <div className="home-page" data-testid="home-page">
      <HomeNavigation
        workspaceTitle={workspaceTitle}
        workdir={workdir}
        refreshKey={refreshKey}
        onOpenSettings={onOpenSettings}
        onOpenStats={onOpenStats}
        onOpenGraph={onOpenGraph}
      />

      <div
        className="home-content"
        data-testid="home-content"
        data-home-state={
          !connected
            ? 'offline'
            : (!schedulesLoaded || isLoadingJobs)
              ? 'loading'
              : jobs.length === 0
                ? 'empty'
                : 'ready'
        }
      >
        {/* Scheduled Tasks section */}
        {canReadSchedules && <div className={`home-schedule-section ${schedulesExpanded ? 'expanded' : 'collapsed'}`}>
          <div className="home-schedule-header">
            <div
              className="home-schedule-title-row"
              onClick={() => setSchedulesExpanded(!schedulesExpanded)}
              style={{ cursor: 'pointer' }}
            >
              <svg className={`home-schedule-chevron ${schedulesExpanded ? 'open' : ''}`} width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <path d="M9 18l6-6-6-6" />
              </svg>
              <svg className="home-schedule-title-icon" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <rect x="3" y="4" width="18" height="18" rx="2" ry="2" />
                <line x1="16" y1="2" x2="16" y2="6" />
                <line x1="8" y1="2" x2="8" y2="6" />
                <line x1="3" y1="10" x2="21" y2="10" />
              </svg>
              <span className="home-schedule-title">Scheduled Tasks</span>
              <span className="home-schedule-count">{schedules.length} tasks</span>
            </div>
            {canWriteSchedules && <button
              className="home-schedule-add-btn"
              onClick={(e) => { e.stopPropagation(); setScheduleModal({ mode: 'create' }); }}
            >
              <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                <line x1="12" y1="5" x2="12" y2="19" />
                <line x1="5" y1="12" x2="19" y2="12" />
              </svg>
              {t('home.newTask')}
            </button>}
          </div>
          {schedulesExpanded && (
          <div className="home-schedule-list">
            {schedules.map(s => {
              const statusClass = !s.enabled ? 'disabled' : s.lastStatus === 'failed' ? 'failed' : s.lastStatus === 'running' ? 'running' : 'enabled';
              const lastStatusClass = s.lastStatus === 'completed' ? 'success'
                : s.lastStatus === 'failed' ? 'failed'
                : s.lastStatus === 'running' ? 'running'
                : s.lastStatus === 'stopped' ? 'stopped'
                : 'never';
              const lastStatusIcon = s.lastStatus === 'completed' ? '✓'
                : s.lastStatus === 'failed' ? '✕'
                : s.lastStatus === 'running' ? '●'
                : s.lastStatus === 'stopped' ? '■'
                : '—';
              const lastTooltip = s.lastRunAt
                ? `${t('home.lastRun')}: ${new Date(s.lastRunAt).toLocaleString(i18n.language)}${s.lastStatus ? ` · ${t('home.lastStatus.' + lastStatusClass)}` : ''}${s.runCount ? ` · ${t('home.runCount', { count: s.runCount })}` : ''}`
                : t('home.neverRun');
              return (
              <div
                key={s.id}
                className={`home-schedule-row ${statusClass}`}
                onClick={() => setScheduleModal({ mode: 'edit', schedule: s })}
              >
                <span className={`home-schedule-row-dot ${statusClass}`} />
                <span className="home-schedule-row-title-wrap">
                  <span className="home-schedule-row-title" title={s.name}>{s.name}</span>
                  <span className={`home-schedule-row-last ${lastStatusClass}`} title={lastTooltip}>
                    <span className={`home-schedule-row-last-icon ${lastStatusClass}`}>{lastStatusIcon}</span>
                    <span className="home-schedule-row-last-time">
                      {s.lastRunAt ? formatRelativeTime(s.lastRunAt, i18n.language) : t('home.neverRun')}
                    </span>
                  </span>
                </span>
                <span className="home-schedule-row-cron" title={s.cronExpr}>{cronToHuman(s.cronExpr, t)}</span>
                <span className="home-schedule-row-next">
                  {!s.enabled ? t('home.disabled') : s.nextRunAt
                    ? new Date(s.nextRunAt).toLocaleString(i18n.language, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
                    : ''}
                </span>
                {canReadJobs && onSelectJob && s.lastRunJobID && <button
                  type="button"
                  className="home-schedule-row-job"
                  onClick={(e) => {
                    e.stopPropagation();
                    onSelectJob(s.lastRunJobID!, s.workspaceId || DEFAULT_WORKSPACE_ID);
                  }}
                  title={t('home.openLatestJob')}
                  aria-label={t('home.openLatestJobForSchedule', { name: s.name })}
                  data-testid={`schedule-latest-job-${s.id}`}
                >
                  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                    <path d="M7 17 17 7" />
                    <path d="M7 7h10v10" />
                  </svg>
                  <span className="home-schedule-row-job-label">Job</span>
                </button>}
                {canWriteSchedules && <button
                  className={`home-schedule-row-toggle ${s.enabled ? 'on' : ''}`}
                  onClick={(e) => { e.stopPropagation(); handleScheduleToggle(s.id); }}
                  title={s.enabled ? '在本机禁用' : '在本机启用'}
                />}
                {canWriteSchedules && <button
                  className="home-schedule-row-delete"
                  onClick={(e) => { e.stopPropagation(); setScheduleDeleteConfirm({ id: s.id, name: s.name }); }}
                  title="删除"
                >
                  ×
                </button>}
              </div>
              );
            })}
          </div>
          )}
        </div>}

        <div className={`home-job-list ${jobHistoryExpanded ? 'expanded' : 'collapsed'}`} data-testid="home-job-history" data-expanded={jobHistoryExpanded ? 'true' : 'false'}>
          <div className="home-job-list-header" onClick={() => setJobHistoryExpanded(!jobHistoryExpanded)} style={{ cursor: 'pointer' }} data-testid="home-job-history-header">
              <div className="home-job-list-title">
                <svg className={`home-job-list-chevron ${jobHistoryExpanded ? 'open' : ''}`} width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                  <path d="M9 18l6-6-6-6" />
                </svg>
                Job History
              </div>
              <div className="home-job-list-count" data-testid="home-job-history-count">{jobs.length} JOBS</div>
              <div className="home-job-list-filter" onClick={(e) => e.stopPropagation()}>
                {allWorkspaces.length > 0 && (
                  <div className="home-job-ws-filter" ref={wsFilterRef}>
                    <span className="home-job-filter-label">{isMobile ? t('home.filterByWorkspaceShort') : t('home.filterByWorkspace')}</span>
                    <button
                      type="button"
                      className={`home-job-ws-filter-trigger ${filterWorkspaceId ? 'active' : ''}`}
                      onClick={() => setWsFilterOpen((v) => !v)}
                      title={filterWorkspaceId ? (wsNameById.get(filterWorkspaceId) || filterWorkspaceId) : t('home.allWorkspaces')}
                    >
                      {filterWorkspaceId && (
                        <span
                          className="home-job-ws-filter-dot"
                          style={{ backgroundColor: workspaceColor(filterWorkspaceId) }}
                          aria-hidden
                        />
                      )}
                      <span className="home-job-ws-filter-text">
                        {filterWorkspaceId ? (wsNameById.get(filterWorkspaceId) || filterWorkspaceId) : t('home.allWorkspaces')}
                      </span>
                      <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden>
                        <path d="M6 9l6 6 6-6" />
                      </svg>
                    </button>
                    {wsFilterOpen && wsFilterRef.current && createPortal(
                      (() => {
                        const rect = wsFilterRef.current.getBoundingClientRect();
                        return (
                          <div
                            className="home-job-ws-filter-dropdown"
                            onMouseDown={(e) => e.stopPropagation()}
                            onClick={(e) => e.stopPropagation()}
                            style={{
                              position: 'fixed',
                              left: Math.max(8, rect.right - 260),
                              top: rect.bottom + 4,
                              zIndex: 9999,
                            }}
                          >
                            <div
                              className={`home-job-ws-filter-item ${!filterWorkspaceId ? 'active' : ''}`}
                              onClick={() => { setFilterWorkspaceId(''); setWsFilterOpen(false); }}
                            >
                              <span className="home-job-ws-filter-item-title">{t('home.allWorkspaces')}</span>
                            </div>
                            {allWorkspaces.map((ws) => (
                              <div
                                key={ws.id}
                                className={`home-job-ws-filter-item ${ws.id === filterWorkspaceId ? 'active' : ''}`}
                                onClick={() => { setFilterWorkspaceId(ws.id); setWsFilterOpen(false); }}
                                title={ws.workdir}
                              >
                                <span
                                  className="home-job-ws-filter-item-color"
                                  style={{ backgroundColor: workspaceColor(ws) }}
                                />
                                <span className="home-job-ws-filter-item-title">{ws.title || ws.id}</span>
                              </div>
                            ))}
                          </div>
                        );
                      })(),
                      document.body
                    )}
                  </div>
                )}
                <label className="home-job-filter-toggle">
                  <span className="home-job-filter-label">{isMobile ? t('home.hideScheduledJobsShort') : t('home.hideScheduledJobs')}</span>
                  <button
                    className={`home-job-filter-switch ${hideScheduledJobs ? 'on' : ''}`}
                    onClick={() => setHideScheduledJobs(!hideScheduledJobs)}
                  />
                </label>
              </div>
          </div>
          {jobHistoryExpanded && (
              <div className="home-job-history-body" data-testid="home-job-history-body">
                {jobsByDay.length === 0 && !homeLoadingOrOffline && (
                  <div className="home-job-history-empty">
                    {t('home.noJobsInWorkspace')}
                  </div>
                )}
                {jobsByDay.map((group) => {
                  const dayStats = dailyStats[group.dayKey];
                  const dayStatsLabel =
                    dayStats && (dayStats.totalMs > 0 || dayStats.turnCount > 0)
                      ? `${formatStatsDuration(dayStats.totalMs)} · ${t('home.dayStatsTurns', { count: dayStats.turnCount })}`
                      : '';
                  return (
                  <section key={group.dayKey} className="home-job-day-group">
                    <div className="home-job-day-divider">
                      <span className="home-job-day-divider-text">{formatJobDayLabel(group.dayKey, i18n.resolvedLanguage || i18n.language)}</span>
                      {dayStatsLabel ? (
                        <span className="home-job-day-divider-stats" title={dayStatsLabel}>
                          {dayStatsLabel}
                        </span>
                      ) : null}
                      <span className="home-job-day-divider-count">{group.items.length}</span>
                    </div>
                    <div className="home-job-history-rows">
                      {group.items.map((job) => (
                        <JobHistoryRow
                          key={job.id}
                          job={job}
                          modelLabel={getJobModelLabel(job)}
                          workspaceName={job.workspaceId ? (wsNameById.get(job.workspaceId) || undefined) : undefined}
                          onSelect={handleJobSelect}
                          onPin={canManageJobs ? handleJobPinClick : undefined}
                          onDelete={canManageJobs ? handleJobDeleteClick : undefined}
                        />
                      ))}
                    </div>
                  </section>
                  );
                })}
                {hasMoreJobs && (
                  <div className="home-job-loadmore" ref={loadMoreSentinelRef}>
                    {isLoadingMoreJobs && (
                      <svg
                        className="home-job-loadmore-spinner"
                        width="20"
                        height="20"
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="2"
                        aria-label="加载中"
                      >
                        <path d="M21 12a9 9 0 1 1-6.219-8.56" />
                      </svg>
                    )}
                  </div>
                )}
              </div>
          )}
        </div>

        {homeLoadingOrOffline && (
          <div
            className={`home-center-status ${!connected ? 'offline' : 'loading'}`}
            data-testid="home-center-status"
            data-home-center-status={!connected ? 'offline' : 'loading'}
          >
            {!connected ? (
              <>
                <svg className="home-center-icon" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <circle cx="12" cy="12" r="10" />
                  <line x1="12" y1="8" x2="12" y2="12" />
                  <line x1="12" y1="16" x2="12.01" y2="16" />
                </svg>
                <span className="home-center-text" data-testid="home-center-status-text">{t('connection.disconnected')}</span>
              </>
            ) : (
              <>
                <svg className="home-center-spinner" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
                  <path d="M21 12a9 9 0 11-6.219-8.56" />
                </svg>
                <span className="home-center-text" data-testid="home-center-status-text">{t('home.loading')}</span>
              </>
            )}
          </div>
        )}
      </div>

      <div className="home-input-container">
      {acpConfigError && (
        <div className="acp-config-error" data-testid="acp-config-error" role="alert">
          <span>{acpConfigError}</span>
          <button type="button" onClick={() => setAcpConfigError(null)} aria-label="dismiss">×</button>
        </div>
      )}
      <div className="home-input-wrapper" style={{ position: 'relative' }}>
          {canReadFiles && mentionState && workdir && (
            <FileMention
              keyword={mentionState.keyword}
              workdir={workdir}
              onSelect={handleMentionSelect}
              onClose={() => setMentionState(null)}
              activeIndex={mentionActiveIndex}
              onActiveIndexChange={setMentionActiveIndex}
            />
          )}
          <PendingAttachmentPreviews attachments={pendingAttachments} onRemove={removeAttachment} />
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
          <SlashFloater items={slashItems} activeIdx={slashActiveIdx} onPick={applySlashItem} onActiveIdxChange={setSlashActiveIdx} />
          <div className={`chat-input-editor${imeComposing ? ' composing' : ''}`}>
            <SkillBackdrop
              input={input}
              skillNameSet={skillNameSet}
              className="home-input-backdrop"
              backdropRef={backdropRef}
            />
            <textarea
              ref={textareaRef}
              className="home-input"
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
              placeholder="Ask anything (Press Shift + Enter for a new line)"
              disabled={isInitializing || !jobEnable || !connected}
              rows={1}
            />
          </div>
          {canWriteFiles && <input
            ref={fileInputRef}
            type="file"
            multiple
            style={{ display: 'none' }}
            onChange={(e) => { handleImageSelect(e.target.files); e.target.value = ''; }}
          />}
          <div className="home-input-footer">
            <div className="home-input-options">
              <div className="chat-model-selector" ref={dropdownRef}>
              {!agentsLoaded ? (
                <div className="model-tag disabled home-agent-loading" aria-live="polite">
                  <span>{t('home.loadingAgents')}</span>
                </div>
              ) : agents.length === 0 ? (
                <button
                  type="button"
                  className="model-tag home-agent-empty-trigger"
                  onClick={onOpenAgentSettings}
                  title={t('home.agentSetupHint')}
                  aria-label={t('home.agentSetupHint')}
                  data-testid="install-agent-entry"
                >
                  <svg className="home-agent-empty-icon" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
                    <circle cx="12" cy="12" r="9" />
                    <path d="M12 8v8M8 12h8" />
                  </svg>
                  <span>{t('home.agentSetupNeeded')}</span>
                  <span className="home-agent-empty-action">
                    {t('home.agentSetupAction')}
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                      <path d="m9 18 6-6-6-6" />
                    </svg>
                  </span>
                </button>
              ) : (
                <div
                  className="model-tag"
                  onClick={() => setShowDropdown(!showDropdown)}
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
              )}
              {showDropdown && agents.length > 0 && (
                isMobile ? createPortal(
                  <div className="mobile-dropdown-overlay" onClick={() => setShowDropdown(false)}>
                    <div className="mobile-dropdown-sheet" onClick={e => e.stopPropagation()}>
                      {agents.length === 0 ? (
                        <div className="model-dropdown-empty">No agents available</div>
                      ) : (
                        agents.map((agent, idx) => {
                          return (
                          <div
                            key={`${agent.type}-${agent.model_id}`}
                            className={`model-dropdown-item ${idx === selectedIndex ? 'active' : ''}`}
                            onClick={() => {
                              selectAgentAt(idx);
                              localStorage.setItem('last_agent_type', agent.type);
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
                            {idx === selectedIndex && (
                              <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                <path d="M20 6L9 17l-5-5" />
                              </svg>
                            )}
                          </div>
                          );
                        })
                      )}
                    </div>
                  </div>,
                  document.body
                ) : (
                <div className="model-dropdown">
                  {agents.length === 0 ? (
                    <div className="model-dropdown-empty">No agents available</div>
                  ) : (
                    agents.map((agent, idx) => {
                      return (
                      <div
                        key={`${agent.type}-${agent.model_id}`}
                        className={`model-dropdown-item ${idx === selectedIndex ? 'active' : ''}`}
                        onClick={() => {
                          selectAgentAt(idx);
                          localStorage.setItem('last_agent_type', agent.type);
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
                        {idx === selectedIndex && (
                          <svg className="model-dropdown-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                            <path d="M20 6L9 17l-5-5" />
                          </svg>
                        )}
                      </div>
                      );
                    })
                  )}
                </div>
                )
              )}
              </div>
              {selectedAgent?.models && (selectedAgent.models.availableModels?.length ?? 0) > 1 && (
                <div className="chat-model-selector" ref={modelDropdownRef}>
                  <div
                    className="model-tag"
                    onClick={() => setShowModelDropdown(!showModelDropdown)}
                  >
                    <ConfigurationIcon kind="model" />
                    <span>{selectedAgent.models.availableModels.find(m => m.modelId === selectedAgent.models!.currentModelId)?.name || selectedAgent.models.currentModelId}</span>
                    <svg className="model-tag-arrow" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <path d="M6 9l6 6 6-6" />
                    </svg>
                  </div>
                  {showModelDropdown && (
                    isMobile ? createPortal(
                      <div className="mobile-dropdown-overlay" onClick={() => setShowModelDropdown(false)}>
                        <div className="mobile-dropdown-sheet" onClick={e => e.stopPropagation()}>
                          {renderHomeModelItems()}
                        </div>
                      </div>,
                      document.body
                    ) : (
                    <div className="model-dropdown">
                      {renderHomeModelItems()}
                    </div>
                    )
                  )}
                </div>
              )}
              {selectedAgent?.modes && (selectedAgent.modes.availableModes?.length ?? 0) > 1 && (
                <div className="chat-model-selector" ref={modeDropdownRef}>
                  <div
                    className="model-tag"
                    onClick={() => setShowModeDropdown(!showModeDropdown)}
                  >
                    <ConfigurationIcon kind="mode" />
                    <span>{selectedAgent.modes.availableModes.find(m => m.id === selectedAgent.modes!.currentModeId)?.name || selectedAgent.modes.currentModeId}</span>
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
                              className={`model-dropdown-item ${m.id === selectedAgent.modes!.currentModeId ? 'active' : ''}`}
                              onClick={() => {
                                handleSelectMode(m.id);
                                setShowModeDropdown(false);
                              }}
                            >
                              <div className="model-dropdown-info">
                                <span className="model-dropdown-name">{m.name}</span>
                                {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                              </div>
                              {m.id === selectedAgent.modes!.currentModeId && (
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
                          className={`model-dropdown-item ${m.id === selectedAgent.modes!.currentModeId ? 'active' : ''}`}
                          onClick={() => {
                            handleSelectMode(m.id);
                            setShowModeDropdown(false);
                          }}
                        >
                          <div className="model-dropdown-info">
                            <span className="model-dropdown-name">{m.name}</span>
                            {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                          </div>
                          {m.id === selectedAgent.modes!.currentModeId && (
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
              {selectedAgent?.thoughtLevels && (selectedAgent.thoughtLevels.availableThoughtLevels?.length ?? 0) > 1 && (
                <div className="chat-model-selector" ref={thoughtLevelDropdownRef}>
                  <div
                    className="model-tag"
                    onClick={() => setShowThoughtLevelDropdown(!showThoughtLevelDropdown)}
                  >
                    <ConfigurationIcon kind="thought" />
                    <span>{selectedAgent.thoughtLevels.availableThoughtLevels.find(m => m.id === selectedAgent.thoughtLevels!.currentThoughtLevelId)?.name || selectedAgent.thoughtLevels.currentThoughtLevelId}</span>
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
                              className={`model-dropdown-item ${m.id === selectedAgent.thoughtLevels!.currentThoughtLevelId ? 'active' : ''}`}
                              onClick={() => {
                                handleSelectThoughtLevel(m.id);
                                setShowThoughtLevelDropdown(false);
                              }}
                            >
                              <div className="model-dropdown-info">
                                <span className="model-dropdown-name">{m.name}</span>
                                {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                              </div>
                              {m.id === selectedAgent.thoughtLevels!.currentThoughtLevelId && (
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
                          className={`model-dropdown-item ${m.id === selectedAgent.thoughtLevels!.currentThoughtLevelId ? 'active' : ''}`}
                          onClick={() => {
                            handleSelectThoughtLevel(m.id);
                            setShowThoughtLevelDropdown(false);
                          }}
                        >
                          <div className="model-dropdown-info">
                            <span className="model-dropdown-name">{m.name}</span>
                            {m.description && <span className="model-dropdown-provider">{m.description}</span>}
                          </div>
                          {m.id === selectedAgent.thoughtLevels!.currentThoughtLevelId && (
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
              {canWriteFiles && <button
                className="chat-btn upload-btn"
                onClick={() => fileInputRef.current?.click()}
                disabled={isInitializing || !jobEnable || !connected}
                title={t('chat.uploadAttachment')}
              >
                <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="m21.4 11.6-8.9 8.9a6 6 0 0 1-8.5-8.5l9.2-9.2a4 4 0 0 1 5.7 5.7l-9.2 9.2a2 2 0 0 1-2.8-2.8l8.5-8.5" />
                </svg>
              </button>}
              {selectedAgent && (
                <AgentUsageCard
                  agentType={selectedAgent.type}
                  displayName={selectedAgent.display_name}
                />
              )}
            </div>
            <div className="home-input-actions">
              <MessagePresetHistoryMenu
                workspaceId={workspaceId}
                disabled={!jobEnable || isInitializing || !connected}
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
              <button
                className="chat-btn send-btn"
                onClick={handleSubmit}
                disabled={(!input.trim() && pendingAttachments.length === 0 && pickedImageUrls.length === 0 && pickedFileAttachments.length === 0) || isInitializing || !selectedAgent || !jobEnable || !connected || pendingAttachments.some((attachment) => attachment.uploading)}
                aria-label={t('chat.sendMessage')}
                data-testid="home-send-button"
              >
                <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M22 2L11 13" />
                  <path d="M22 2L15 22L11 13L2 9L22 2Z" />
                </svg>
              </button>
            </div>
          </div>
          {selectedAgent && (
            <div
              ref={wsSwitchRef}
              className={`workdir-row ${canSwitchWorkspaceInFooter ? 'switchable' : ''}`}
              title={
                canSwitchWorkspaceInFooter
                  ? '切换工作空间'
                  : `${workspaceTitle || workspaceId || ''} : ${workdir}`
              }
              onClick={canSwitchWorkspaceInFooter ? () => setWsSwitchOpen((v) => !v) : undefined}
            >
              {workspaceId && (
                <span
                  className="workdir-ws-strip"
                  style={{ backgroundColor: workspaceColor(workspaceId) }}
                  aria-hidden
                />
              )}
              <span className="workdir-icon">🗂️</span>
              <span className="workdir-label">Workspace({workspaceTitle || workspaceId || '—'}) :</span>
              <span className="workdir-path-branch">
                <code
                  className={`workdir-path${!canSwitchWorkspaceInFooter && jobEnable ? ' clickable' : ''}`}
                  onClick={
                    !canSwitchWorkspaceInFooter && jobEnable
                      ? (e) => { e.stopPropagation(); handleSelectDir(); }
                      : undefined
                  }
                  style={!canSwitchWorkspaceInFooter && jobEnable ? { cursor: 'pointer' } : undefined}
                >
                  {workdir || '—'}
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
              {canSwitchWorkspaceInFooter && (
                <span className="workdir-switch-caret" aria-hidden>
                  <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </span>
              )}
              <button className="workdir-copy" onClick={(e) => { e.stopPropagation(); copyToClipboard(workdir); }} title="Copy path">
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
                  <path d="M5 15H4a2 2 0 01-2-2V4a2 2 0 012-2h9a2 2 0 012 2v1" />
                </svg>
              </button>
              {canSwitchWorkspaceInFooter && wsSwitchOpen && wsSwitchRef.current && createPortal(
                (() => {
                  const rect = wsSwitchRef.current.getBoundingClientRect();
                  return (
                    <div
                      className="workdir-switch-dropdown"
                      onMouseDown={(e) => e.stopPropagation()}
                      onClick={(e) => e.stopPropagation()}
                      style={{
                        position: 'fixed',
                        left: rect.left,
                        bottom: window.innerHeight - rect.top + 4,
                        zIndex: 9999,
                      }}
                    >
                      {allWorkspaces.map((ws) => (
                        <div
                          key={ws.id}
                          className={`workdir-switch-item ${ws.id === workspaceId ? 'active' : ''}`}
                          onClick={() => {
                            setWsSwitchOpen(false);
                            if (ws.id !== workspaceId) onSelectWorkspace?.(ws);
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

      {showDirPicker && (
        <DirPicker
          initialPath={workdir}
          basePath={workspaceWorkdir}
          onConfirm={handleDirConfirm}
          onCancel={() => setShowDirPicker(false)}
        />
      )}

      {deleteConfirm && (
        <div className="delete-confirm-overlay" onClick={() => setDeleteConfirm(null)} data-testid="delete-confirm-overlay">
          <div className="delete-confirm-dialog" onClick={(e) => e.stopPropagation()} data-testid="delete-confirm-dialog">
            <div className="delete-confirm-title">Delete Job</div>
            <div className="delete-confirm-message">
              Are you sure you want to delete "<strong>{deleteConfirm.title}</strong>"?
            </div>
            <div className="delete-confirm-actions">
              <button className="delete-confirm-cancel" onClick={() => setDeleteConfirm(null)} data-testid="delete-confirm-cancel">Cancel</button>
              <button className="delete-confirm-ok" onClick={handleJobDeleteConfirm} data-testid="delete-confirm-ok">Delete</button>
            </div>
          </div>
        </div>
      )}

      {/* Schedule delete confirmation */}
      {canWriteSchedules && scheduleDeleteConfirm && (
        <div className="delete-confirm-overlay" onClick={() => setScheduleDeleteConfirm(null)}>
          <div className="delete-confirm-dialog" onClick={(e) => e.stopPropagation()}>
            <div className="delete-confirm-title">删除定时任务</div>
            <div className="delete-confirm-message">
              确定删除定时任务 "<strong>{scheduleDeleteConfirm.name}</strong>"？
            </div>
            <div className="delete-confirm-actions">
              <button className="delete-confirm-cancel" onClick={() => setScheduleDeleteConfirm(null)}>取消</button>
              <button className="delete-confirm-ok" onClick={() => handleScheduleDelete(scheduleDeleteConfirm.id)}>删除</button>
            </div>
          </div>
        </div>
      )}

      {/* Schedule edit/create modal */}
      {canWriteSchedules && scheduleModal.mode !== 'closed' && (
        <ScheduleEditModal
          schedule={scheduleModal.mode === 'edit' ? scheduleModal.schedule : null}
          workspaceId={workspaceId}
          workspaces={allWorkspaces}
          agents={agents}
          defaultAgentIndex={selectedIndex}
          onSelectJob={handleJobSelect}
          onSave={handleScheduleSave}
          onClose={() => setScheduleModal({ mode: 'closed' })}
        />
      )}
    </div>
  );
}
