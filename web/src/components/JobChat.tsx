import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useJobChat } from '../hooks/useJobChat';
import { useJobList, type JobSummary } from '../hooks/useJobList';
import { MessageList } from './MessageList';
import { phaseLabel } from '../utils/chatPhase';
import { ChatInput } from './ChatInput';
import { LoopProgress } from './LoopProgress';
import { GraphLoopProgress } from './GraphLoopProgress';
import { LoopSessionSidebar } from './LoopSessionSidebar';
import { StepOutline } from './StepOutline';
import { FileBrowser } from './FileBrowser';
import { AgentsLocalEditor } from './AgentsLocalEditor';
import { AgentInfo } from './ChatPage';
import { MessageRoleEnum, MessageStatusEnum, type UserMessage } from '../types';
import { ServerClockProvider } from '../contexts/ServerClock';
import { VirtualList } from './VirtualList';
import { registerWorkspaceColors } from '../utils/workspace';
import { fetchAgentPrefs, type AgentPrefsMap } from '../utils/agentPrefs';
import { relinkACPThoughtLevels, setACPConfig, type ACPConfigState, type ACPConfigTarget } from '../utils/acpConfig';
import './JobChat.css';

// Must match the backend limit in cmd/web/handler/job.go (jobTitleMaxLen).
const JOB_TITLE_MAX_LEN = 200;

async function fetchAgentList(shareToken?: string, jobId?: string): Promise<{ agents: AgentInfo[]; workdir: string; jobEnable: boolean; error?: string }> {
  try {
    const url = new URL(shareToken ? '/api/v1/public/agent/list' : '/api/v1/agent/list', window.location.origin);
    if (shareToken) {
      url.searchParams.set('shareToken', shareToken);
      if (jobId) url.searchParams.set('jobId', jobId);
    }
    const res = await fetch(url.pathname + url.search);
    const data = await res.json().catch(() => null);
    if (!data || data.code !== 0 || !data.agent_list) {
      // Keep the server's message when there is one so the banner can show
      // the real cause (auth failure, probe error, …) instead of a generic
      // "empty list".
      const detail = (data && typeof data.msg === 'string' && data.msg) || `HTTP ${res.status}`;
      // The private route already passed agentAuthMiddleware, so a malformed
      // business response does not revoke write access. Public shares remain
      // read-only and intentionally report jobEnable=false.
      return { agents: [], workdir: '', jobEnable: !shareToken, error: detail };
    }
    return { agents: data.agent_list as AgentInfo[], workdir: data.workdir || '', jobEnable: !!data.job_enable };
  } catch (err) {
    console.error('Failed to fetch agent list:', err);
    return {
      agents: [],
      workdir: '',
      jobEnable: !shareToken,
      error: err instanceof Error ? err.message : String(err),
    };
  }
}

interface JobChatProps {
  existingJobId: string;
  initialMessage?: string | null;
  initialImageUrls?: string[];
  initialWorkdir?: string;
  initialSessionId?: string;
  initialModelId?: string;
  initialAgentType?: string;
  initialAcpMode?: string;
  initialAcpThoughtLevel?: string;
  workspaceId?: string;
  shareToken?: string;
  isReadonly?: boolean;
  onBack?: () => void;
  onJobCreated?: (jobId: string) => void;
  onSelectJob?: (jobId: string, workspaceId?: string) => void;
  onOpenSettings?: () => void;
  onOpenStats?: () => void;
  /** Open the Graph Workflows page. Mirrors the home page's button so both
   *  toolbars expose the same shared actions. */
  onOpenGraph?: () => void;
  onStartNewChat?: (modelId: string, agentType: string, workdir?: string) => void;
  /** Switch to another workspace from within the chat page: the callback
   *  reuses / creates an empty Job in the target workspace and navigates to
   *  it. When omitted, the footer Workspace tag stays informational. */
  onSwitchWorkspaceChat?: (ws: { id: string; title: string; description: string; workdir: string; color?: string }) => void;
  /** Fired when the existing Job 404s on /job/:id (deleted or never
   *  existed). Lets the parent clear the stale jobId from URL + state and
   *  route the user back to the workspace home. */
  onJobNotFound?: (jobId: string) => void;
}

interface JobInfo {
  id: string;
  title: string;
  status: string;
  mode?: 'interactive' | 'loop' | 'graph';
  workdir?: string;
  updatedAt: number;
  scheduleId?: string;
}

// JOB_ROW_HEIGHT matches the fixed row height declared in .header-joblist-item
// (see JobChat.css). The VirtualList uses this to compute the slice of rows
// that overlap the viewport, so it must stay in sync with the CSS.
const JOB_ROW_HEIGHT = 36;

export function JobChat(props: JobChatProps) {
  const { existingJobId, initialMessage, initialImageUrls, initialWorkdir, initialSessionId, initialModelId, initialAgentType, initialAcpMode, initialAcpThoughtLevel, workspaceId, shareToken, isReadonly, onBack, onJobCreated, onSelectJob, onOpenSettings, onOpenStats, onOpenGraph, onStartNewChat, onSwitchWorkspaceChat, onJobNotFound } = props;
  const { t } = useTranslation();

  // Read the workspace title from the cross-page cache populated by App.tsx
  // on first boot. Falls back to the id when missing. Keeps the chat-page
  // workdir row responsive to workspace switches without a round-trip.
  // State (not useMemo) so the settings-panel "quartet:workspace-updated"
  // event can force a refresh when the user renames / changes workdir —
  // otherwise a rename in Settings wouldn't show up until full reload.
  const readWsMeta = useCallback((id: string | undefined): { title?: string; workdir?: string } => {
    if (!id) return {};
    try {
      const raw = localStorage.getItem(`workspace_${id}`);
      if (raw) {
        const parsed = JSON.parse(raw);
        return {
          title: typeof parsed?.title === 'string' ? parsed.title : undefined,
          workdir: typeof parsed?.workdir === 'string' ? parsed.workdir : undefined,
        };
      }
    } catch { /* ignore */ }
    return {};
  }, []);
  const [workspaceMeta, setWorkspaceMeta] = useState<{ title?: string; workdir?: string }>(() => readWsMeta(workspaceId));
  useEffect(() => {
    setWorkspaceMeta(readWsMeta(workspaceId));
  }, [workspaceId, readWsMeta]);
  useEffect(() => {
    const onUpdated = (e: Event) => {
      const ws = (e as CustomEvent).detail as { id?: string; title?: string; workdir?: string } | null;
      if (!ws?.id || ws.id !== workspaceId) return;
      setWorkspaceMeta({ title: ws.title, workdir: ws.workdir });
    };
    window.addEventListener('quartet:workspace-updated', onUpdated);
    return () => window.removeEventListener('quartet:workspace-updated', onUpdated);
  }, [workspaceId]);
  const workspaceTitle = workspaceMeta.title;

  // The home page already has the complete user message before this page
  // mounts. Seed it into chat state immediately instead of making the bubble
  // wait for job hydration, agent refresh and SSE readiness.
  const [initialUserMessage] = useState<UserMessage | undefined>(() => {
    if (!initialMessage) return undefined;
    const id = crypto.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
    return {
      id,
      role: MessageRoleEnum.USER,
      content: initialMessage,
      createdAt: Date.now(),
      status: MessageStatusEnum.Finished,
      clientMessageId: id,
      pending: true,
      failed: false,
      deliveryStatus: 'sending',
      imageUrls: initialImageUrls?.length ? initialImageUrls : undefined,
    };
  });

  const {
    jobId,
    jobTitle,
    setJobTitle,
    jobShareToken: initialShareToken,
    messages,
    isLoading,
    isLoadingHistory,
    activePhase,
    error,
    sessionModelId,
    sessionType,
    sessionACPMode,
    sessionACPThoughtLevel,
    totalTokens,
    roundStartedAt,
    roundFinishedAt,
    interactiveAccumulatedMs,
    sessionWorkdir,
    isLoop,
    isGraph,
    graphRunId,
    graphRunStatusSnapshot,
    graphStreamError,
    applyGraphRunStatusSnapshot,
    loopProgress,
    loopStatus,
    loopFlow,
    loopSessions,
    activeSessionId,
    setActiveSessionId,
    endedSessionIds,
    loadedSessionIds,
    sendMessage,
    queueMessage,
    cancelQueuedMessage,
    queuedMessages,
    stopGeneration,
    clearMessages,
    eventsReady,
    getSessionMeta,
    getServerNow,
  } = useJobChat({ existingJobId, initialSessionId, shareToken, initialUserMessage, onJobNotFound });

  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [agentPrefs, setAgentPrefs] = useState<AgentPrefsMap>({});
  const [workdir, setWorkdir] = useState(initialWorkdir || '');
  const [selectedAgentIndex, setSelectedAgentIndex] = useState<number>(0);
  const [hasUserSelected, setHasUserSelected] = useState(false);
  // Per-dimension manual-control flags. Once the user picks a mode /
  // thought_level, the session-reported value (sessionACPMode /
  // sessionACPThoughtLevel) must stop pinning that selector — mirrors
  // hasUserSelected for model. Tracked separately so switching mode does not
  // freeze the model selector's session-follow (and vice versa). Reset on job
  // switch via the component's key={jobId} remount.
  const [hasUserSelectedMode, setHasUserSelectedMode] = useState(false);
  const [hasUserSelectedThoughtLevel, setHasUserSelectedThoughtLevel] = useState(false);
  const [acpConfigError, setAcpConfigError] = useState<string | null>(null);
  const [initialAgentRefreshPending, setInitialAgentRefreshPending] = useState(false);
  const [jobEnable, setJobEnable] = useState(false);
  const [loopSidebarOpen, setLoopSidebarOpen] = useState(false);
  const [fileBrowserOpen, setFileBrowserOpen] = useState(false);
  const [agentsEditorOpen, setAgentsEditorOpen] = useState(false);
  const [jobListOpen, setJobListOpen] = useState(false);
  const [headerMoreOpen, setHeaderMoreOpen] = useState(false);
  // Step outline panel (right side): lists thinking / tool-call / assistant
  // steps with per-step duration; clicking a row scrolls the bubble into view.
  const [outlineOpen, setOutlineOpen] = useState(false);
  // Workspaces list for the Workspace-tag dropdown in the footer. Fetched once
  // when the chat page mounts (outside readonly share mode). Kept in state so
  // newly-created workspaces show up after a Settings-dialog refresh.
  const [allWorkspaces, setAllWorkspaces] = useState<Array<{ id: string; title: string; description: string; workdir: string; color?: string }>>([]);
  useEffect(() => {
    if (isReadonly) return;
    let cancelled = false;
    (async () => {
      try {
        const res = await fetch('/api/v1/workspace/list');
        if (!res.ok) return;
        const data = await res.json();
        if (cancelled) return;
        const list = (data?.workspaces || []) as Array<{ id: string; title: string; description: string; workdir: string; color?: string }>;
        registerWorkspaceColors(list);
        setAllWorkspaces(list);
      } catch { /* ignore */ }
    })();
    return () => { cancelled = true; };
  }, [isReadonly]);
  // Patch the workspaces list on the fly when Settings edits / deletes a
  // workspace. Avoids stale titles in the footer dropdown.
  useEffect(() => {
    if (isReadonly) return;
    const onUpdated = (e: Event) => {
      const ws = (e as CustomEvent).detail as { id: string; title: string; description: string; workdir: string; color?: string } | null;
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
    window.addEventListener('quartet:workspace-updated', onUpdated);
    window.addEventListener('quartet:workspace-deleted', onDeleted);
    return () => {
      window.removeEventListener('quartet:workspace-updated', onUpdated);
      window.removeEventListener('quartet:workspace-deleted', onDeleted);
    };
  }, [isReadonly]);
  // Job list is fetched lazily — only when the dropdown is open — so the
  // hook doesn't poll in the background for chat pages where the menu is
  // never expanded. We swap the poll intervals to 0 while the menu is
  // closed so the hook's tight poll loop isn't even scheduled.
  const {
    jobs,
    hasMore: hasMoreJobs,
    isLoadingMore: isLoadingMoreJobs,
    loadMore: loadMoreJobs,
    refresh: refreshJobs,
    patchJob: patchJobInList,
  } = useJobList({
    workspaceId,
    pollIntervalMs: jobListOpen ? 60_000 : 0,
    activePollIntervalMs: jobListOpen ? 5_000 : 0,
    refetchOnFocus: jobListOpen,
    disabled: !!isReadonly,
  });
  const [userAvatarUrl, setUserAvatarUrl] = useState('');
  const [jobShareToken, setJobShareToken] = useState<string | null>(null);
  const [shareCopied, setShareCopied] = useState(false);
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [editingTitleValue, setEditingTitleValue] = useState('');
  const [savingTitle, setSavingTitle] = useState(false);
  const [titleEditError, setTitleEditError] = useState<string | null>(null);
  const titleInputRef = useRef<HTMLInputElement | null>(null);
  const initialMessageSent = useRef(false);
  const [initialDispatchPending, setInitialDispatchPending] = useState(!!initialMessage);
  // Failure exit for the home-page first-message handoff: when the agent list
  // can never yield a selected agent (fetch failed / no agents configured) the
  // dispatch effect below can never run, so surface the cause and release the
  // composer instead of leaving it disabled forever. Stored as an i18n key +
  // params so the banner re-translates on language switch.
  const [agentListError, setAgentListError] = useState<{ key: string; params?: Record<string, string> } | null>(null);
  const refreshedAgentModelsRef = useRef<Set<string>>(new Set());
  const jobListRef = useRef<HTMLDivElement | null>(null);
  const headerMoreRef = useRef<HTMLDivElement | null>(null);

  // Sync share token from loaded job data
  useEffect(() => {
    if (initialShareToken) setJobShareToken(initialShareToken);
  }, [initialShareToken]);

  useEffect(() => {
    if (jobId && onJobCreated) {
      onJobCreated(jobId);
    }
  }, [jobId, onJobCreated]);

  // Update document title based on job title or first user message
  useEffect(() => {
    if (jobTitle) {
      const short = jobTitle.length > 50 ? jobTitle.slice(0, 50) + '...' : jobTitle;
      document.title = `${short} - Quartet`;
    } else {
      const firstUserMsg = messages.find((m) => m.role === 'user');
      if (firstUserMsg?.content) {
        const text = firstUserMsg.content.trim();
        const short = text.length > 50 ? text.slice(0, 50) + '...' : text;
        document.title = `${short} - Quartet`;
      } else if (isLoop) {
        document.title = 'Loop Task - Quartet';
      } else {
        document.title = 'Chat - Quartet';
      }
    }
  }, [messages, isLoop, jobTitle]);

  // When the job's title changes in the chat header we patch the cached list
  // so the menu reflects the new title without waiting for the next poll.
  useEffect(() => {
    const targetId = jobId || existingJobId;
    if (!targetId || !jobTitle) return;
    patchJobInList(targetId, { title: jobTitle });
  }, [existingJobId, jobId, jobTitle, patchJobInList]);

  useEffect(() => {
    if (isReadonly) return;
    fetch('/api/v1/config/settings/get')
      .then((res) => res.json())
      .then((data) => {
        if (data?.code === 0 && data.settings?.avatar_url) {
          setUserAvatarUrl(data.settings.avatar_url);
        }
      })
      .catch(() => {});
  }, [isReadonly]);

  // Per-agent favorites for the model dropdown grouping. Skip in readonly/
  // shared views (no settings access). Defaults are not applied here — they
  // were resolved on ChatPage before this Job existed.
  useEffect(() => {
    if (isReadonly) return;
    fetchAgentPrefs().then(setAgentPrefs).catch(() => {});
  }, [isReadonly]);

  useEffect(() => {
    let cancelled = false;
    void fetchAgentList(shareToken, existingJobId).then(({ agents: list, jobEnable: je, error: listError }) => {
      if (cancelled) return;
      setInitialAgentRefreshPending(false);
      // A pending first message from the home page can only be dispatched once
      // an agent is selected. An empty list means the dispatch can never run —
      // release the composer and surface the cause instead of spinning forever.
      if (initialMessage && list.length === 0 && !initialMessageSent.current) {
        setInitialDispatchPending(false);
        setAgentListError(
          listError
            ? { key: 'chat.loadAgentsFailed', params: { error: listError } }
            : { key: 'chat.noAgentsAvailable' }
        );
      }
      // Apply initial modelId/acpMode to the matching agent so the initial
      // message uses the model the user selected on the ChatPage.
      let finalList = list;
      if (initialModelId || initialAcpMode || initialAcpThoughtLevel) {
        finalList = list.map((agent) => {
          // Identify the target agent by type when a specific ACP agent is
          // known (model id can collide across ACP agents); otherwise fall back
          // to model id.
          const isTarget = initialAgentType
            ? agent.type === initialAgentType
            : (initialModelId
              ? agent.model_id === initialModelId || agent.models?.availableModels.some((m) => m.modelId === initialModelId)
              : false);
          if (!isTarget) return agent;
          let updated = agent;
          if (initialModelId && updated.models && updated.models.availableModels.some((m) => m.modelId === initialModelId) && updated.models.currentModelId !== initialModelId) {
            updated = { ...updated, models: { ...updated.models, currentModelId: initialModelId } };
          }
          if (initialAcpMode && updated.modes && updated.modes.currentModeId !== initialAcpMode) {
            updated = { ...updated, modes: { ...updated.modes, currentModeId: initialAcpMode } };
          }
          if (initialAcpThoughtLevel && updated.thoughtLevels && updated.thoughtLevels.currentThoughtLevelId !== initialAcpThoughtLevel) {
            updated = { ...updated, thoughtLevels: { ...updated.thoughtLevels, currentThoughtLevelId: initialAcpThoughtLevel } };
          }
          return updated;
        });
      }

      let selectedIdx = 0;
      if (finalList.length > 0) {
        // Match by type first for ACP agents (unique identifier); model id may
        // collide across ACP agents. Fall back to model id when no type.
        if (initialAgentType) {
          const idx = finalList.findIndex((a) => a.type === initialAgentType);
          if (idx >= 0) selectedIdx = idx;
        }
        if (!initialAgentType && initialModelId) {
          const idx = finalList.findIndex((a) => a.model_id === initialModelId || a.models?.availableModels.some((m) => m.modelId === initialModelId));
          if (idx >= 0) selectedIdx = idx;
        }

        const selected = finalList[selectedIdx];
        const selectedModelId = selected.models?.currentModelId;
        setAgents(finalList);
        setJobEnable(je);
        setSelectedAgentIndex(selectedIdx);
        if (!isReadonly && selectedModelId) {
          const refreshKey = `${selected.type}::${selectedModelId}`;
          refreshedAgentModelsRef.current.add(refreshKey);
          setInitialAgentRefreshPending(true);
          void relinkACPThoughtLevels(selected.type, selectedModelId).then((state) => {
            if (cancelled) return;
            let thoughtLevels = state;
            if (initialAcpThoughtLevel && thoughtLevels.availableThoughtLevels.some((level) => level.id === initialAcpThoughtLevel)) {
              thoughtLevels = { ...thoughtLevels, currentThoughtLevelId: initialAcpThoughtLevel };
            }
            setAgents((prev) => prev.map((agent, agentIndex) =>
              agentIndex === selectedIdx && agent.type === selected.type && agent.models?.currentModelId === selectedModelId
                ? { ...agent, thoughtLevels }
                : agent
            ));
          }).catch((err) => {
            if (cancelled) return;
            const msg = err instanceof Error ? err.message : String(err);
            setAcpConfigError(msg);
            console.error('[JobChat] refresh initial ACP thought levels failed:', err);
          }).finally(() => {
            if (!cancelled) setInitialAgentRefreshPending(false);
          });
        }
      } else {
        setAgents(finalList);
        setJobEnable(je);
        setSelectedAgentIndex(0);
      }
    });
    return () => {
      cancelled = true;
    };
  }, [existingJobId, shareToken, initialMessage, initialAgentType, initialModelId, initialAcpMode, initialAcpThoughtLevel, isReadonly]);

  useEffect(() => {
    if (sessionWorkdir) setWorkdir(sessionWorkdir);
  }, [sessionWorkdir]);

  useEffect(() => {
    if (!sessionModelId && !sessionACPMode && !sessionACPThoughtLevel) return;
    if (agents.length === 0) return;

    setAgents((prev) => prev.map((agent, idx) => {
      const shouldApplySessionACP = sessionType
        ? agent.type === sessionType
        : idx === selectedAgentIndex;
      if (!shouldApplySessionACP) return agent;

      let updated = agent;
      if (!hasUserSelected && sessionModelId && updated.models && updated.models.availableModels.some((m) => m.modelId === sessionModelId) && updated.models.currentModelId !== sessionModelId) {
        updated = { ...updated, models: { ...updated.models, currentModelId: sessionModelId } };
      }
      if (!hasUserSelectedMode && sessionACPMode && updated.modes && updated.modes.currentModeId !== sessionACPMode) {
        updated = { ...updated, modes: { ...updated.modes, currentModeId: sessionACPMode } };
      }
      if (!hasUserSelectedThoughtLevel && sessionACPThoughtLevel && updated.thoughtLevels && updated.thoughtLevels.currentThoughtLevelId !== sessionACPThoughtLevel) {
        updated = { ...updated, thoughtLevels: { ...updated.thoughtLevels, currentThoughtLevelId: sessionACPThoughtLevel } };
      }
      return updated;
    }));
  }, [sessionModelId, sessionACPMode, sessionACPThoughtLevel, sessionType, agents.length, selectedAgentIndex, hasUserSelected, hasUserSelectedMode, hasUserSelectedThoughtLevel]);

  // Refresh on open + close-on-outside-click. The useJobList hook handles
  // initial load + background polling; here we only refresh eagerly when the
  // user opens the menu so they see the latest state without waiting.
  useEffect(() => {
    if (!jobListOpen) return;

    void refreshJobs();

    const handlePointerDown = (event: MouseEvent) => {
      if (!jobListRef.current?.contains(event.target as Node)) {
        setJobListOpen(false);
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    return () => document.removeEventListener('mousedown', handlePointerDown);
  }, [jobListOpen, refreshJobs]);

  useEffect(() => {
    if (!headerMoreOpen) return;

    const handlePointerDown = (event: MouseEvent) => {
      if (!headerMoreRef.current?.contains(event.target as Node)) {
        setHeaderMoreOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setHeaderMoreOpen(false);
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [headerMoreOpen]);

  useEffect(() => {
    // In loop mode, always update agent index when active session changes.
    // In interactive mode, skip if user has manually selected an agent.
    if (hasUserSelected && !isLoop) return;
    if (agents.length === 0) return;

    let matched = false;

    // For ACP agents, `type` (the serve command) is the unique, precise
    // identifier — and multiple ACP agents can expose the SAME model id
    // (e.g. both codex and traex advertise `gpt-5.5`), so a model-id match
    // first would pick whichever happens to be listed earlier. Match by type
    // first.
    if (sessionType) {
      const idx = agents.findIndex((a) => a.type === sessionType);
      if (idx >= 0) { setSelectedAgentIndex(idx); matched = true; }
    }

    // When type is unavailable, fall back to model id, the stable per-agent
    // identifier.
    if (!matched && sessionModelId) {
      const idx = agents.findIndex((a) => a.model_id === sessionModelId || a.models?.availableModels.some((m) => m.modelId === sessionModelId));
      if (idx >= 0) setSelectedAgentIndex(idx);
    }
  }, [existingJobId, hasUserSelected, sessionModelId, sessionType, agents, isLoop]);

  const headerTitle = (() => {
    if (jobTitle) {
      return jobTitle.length > 40 ? jobTitle.slice(0, 40) + '...' : jobTitle;
    }
    const firstUserMsg = messages.find((m) => m.role === 'user');
    if (firstUserMsg?.content) {
      const text = firstUserMsg.content.trim();
      return text.length > 40 ? text.slice(0, 40) + '...' : text;
    }
    if (isLoop) return 'Loop Task';
    return 'Quartet';
  })();

  const selectedAgent = agents[selectedAgentIndex] ?? null;

  // Existing jobs learn their real agent/model from session history after the
  // agent list has already loaded. Refresh that concrete pair once as soon as
  // it becomes selected so ChatInput never combines a restored model with the
  // probe cache's thought-level list from another model.
  useEffect(() => {
    if (isReadonly || !selectedAgent) return;
    if (sessionType && selectedAgent.type !== sessionType) return;

    const modelId = !hasUserSelected && sessionModelId
      ? sessionModelId
      : selectedAgent.models?.currentModelId;
    if (!modelId || !selectedAgent.models?.availableModels.some((model) => model.modelId === modelId)) return;

    const refreshKey = `${selectedAgent.type}::${modelId}`;
    if (refreshedAgentModelsRef.current.has(refreshKey)) return;
    refreshedAgentModelsRef.current.add(refreshKey);

    let cancelled = false;
    setAcpConfigError(null);
    void relinkACPThoughtLevels(selectedAgent.type, modelId).then((state) => {
      if (cancelled) return;
      const preferred = hasUserSelectedThoughtLevel
        ? selectedAgent.thoughtLevels?.currentThoughtLevelId
        : sessionACPThoughtLevel || initialAcpThoughtLevel;
      const thoughtLevels = preferred && state.availableThoughtLevels.some((level) => level.id === preferred)
        ? { ...state, currentThoughtLevelId: preferred }
        : state;
      setAgents((prev) => prev.map((agent) =>
        agent.type === selectedAgent.type
          ? {
              ...agent,
              models: agent.models ? { ...agent.models, currentModelId: modelId } : agent.models,
              thoughtLevels,
            }
          : agent
      ));
    }).catch((err) => {
      if (cancelled) return;
      const msg = err instanceof Error ? err.message : String(err);
      setAcpConfigError(msg);
      console.error('[JobChat] refresh restored ACP thought levels failed:', err);
    });
    return () => {
      cancelled = true;
    };
  }, [
    hasUserSelected,
    hasUserSelectedThoughtLevel,
    initialAcpThoughtLevel,
    isReadonly,
    selectedAgent,
    sessionACPThoughtLevel,
    sessionModelId,
    sessionType,
  ]);

  // Resolve agent icon/name for a specific session. Falls back to selectedAgent.
  const resolveAgentForSession = useCallback((sessionId?: string): { iconUrl?: string; displayName?: string } => {
    if (!sessionId || agents.length === 0) {
      return { iconUrl: selectedAgent?.icon_url, displayName: selectedAgent?.display_name };
    }
    const meta = getSessionMeta(sessionId);
    if (!meta) {
      return { iconUrl: selectedAgent?.icon_url, displayName: selectedAgent?.display_name };
    }
    // Match by type first: for ACP agents `type` is the unique identifier,
    // and multiple ACP agents may share one model id (e.g. codex + traex both
    // expose `gpt-5.5`), so matching model id first would resolve the wrong
    // agent's icon/name. Fall back to model id when type is unavailable.
    let matched: AgentInfo | undefined;
    if (meta.type) {
      matched = agents.find((a) => a.type === meta.type);
    }
    if (!matched && meta.modelId) {
      matched = agents.find((a) => a.model_id === meta.modelId || a.models?.availableModels.some((m) => m.modelId === meta.modelId));
    }
    if (matched) {
      return { iconUrl: matched.icon_url, displayName: matched.display_name };
    }
    return { iconUrl: selectedAgent?.icon_url, displayName: selectedAgent?.display_name };
  }, [agents, selectedAgent, getSessionMeta]);

  // applyACPConfig pushes a live-config switch to the backend and merges the
  // refreshed selector lists back into the selected agent. When a session is
  // active it switches on that session (and the backend persists it); before a
  // session exists (interactive job not yet sent) it falls back to a Home
  // preview against agentType so the lists still refresh. Rolls back the
  // optimistic pick on failure.
  const applyACPConfig = useCallback(
    async (
      target: ACPConfigTarget,
      change: { model?: string; mode?: string; thoughtLevel?: string },
      rollback: () => void,
    ) => {
      const agent = agents[selectedAgentIndex];
      if (!agent) return;
      setAcpConfigError(null);
      try {
        const state: ACPConfigState = await setACPConfig({
          target,
          sessionId: activeSessionId || undefined,
          agentType: activeSessionId ? undefined : agent.type,
          model: change.model ?? agent.models?.currentModelId,
          mode: target === 'model' ? undefined : change.mode ?? agent.modes?.currentModeId,
          thoughtLevel: target === 'model' ? undefined : change.thoughtLevel ?? agent.thoughtLevels?.currentThoughtLevelId,
        });
        setAgents((prev) => prev.map((a, i) =>
          i === selectedAgentIndex
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
        console.error(`[JobChat] set ACP ${target} failed:`, err);
      }
    },
    [agents, selectedAgentIndex, activeSessionId],
  );

  const handleSelectModel = useCallback((modelId: string) => {
    // Mark manual control so the session-reported model (sessionModelId) stops
    // pinning the selector. Without this, once a session reports its model the
    // tag/dropdown freeze on it and both display and send ignore new picks.
    const agent = agents[selectedAgentIndex];
    const prevModelId = agent?.models?.currentModelId;
    if (!agent?.models || modelId === prevModelId) return;
    refreshedAgentModelsRef.current.add(`${agent.type}::${modelId}`);
    setHasUserSelected(true);
    setAgents((prev) => prev.map((a, idx) =>
      idx === selectedAgentIndex && a.models
        ? { ...a, models: { ...a.models, currentModelId: modelId } }
        : a
    ));
    void applyACPConfig('model', { model: modelId }, () => {
      setAgents((prev) => prev.map((a, idx) =>
        idx === selectedAgentIndex && a.models
          ? { ...a, models: { ...a.models, currentModelId: prevModelId ?? '' } }
          : a
      ));
    });
  }, [agents, selectedAgentIndex, applyACPConfig]);

  const handleSelectMode = useCallback((modeId: string) => {
    const agent = agents[selectedAgentIndex];
    const prevModeId = agent?.modes?.currentModeId;
    if (!agent?.modes || modeId === prevModeId) return;
    setHasUserSelectedMode(true);
    setAgents((prev) => prev.map((a, idx) =>
      idx === selectedAgentIndex && a.modes
        ? { ...a, modes: { ...a.modes, currentModeId: modeId } }
        : a
    ));
    void applyACPConfig('mode', { mode: modeId }, () => {
      setAgents((prev) => prev.map((a, idx) =>
        idx === selectedAgentIndex && a.modes
          ? { ...a, modes: { ...a.modes, currentModeId: prevModeId ?? '' } }
          : a
      ));
    });
  }, [agents, selectedAgentIndex, applyACPConfig]);

  const handleSelectThoughtLevel = useCallback((thoughtLevelId: string) => {
    const agent = agents[selectedAgentIndex];
    const prevLevelId = agent?.thoughtLevels?.currentThoughtLevelId;
    if (!agent?.thoughtLevels || thoughtLevelId === prevLevelId) return;
    setHasUserSelectedThoughtLevel(true);
    setAgents((prev) => prev.map((a, idx) =>
      idx === selectedAgentIndex && a.thoughtLevels
        ? { ...a, thoughtLevels: { ...a.thoughtLevels, currentThoughtLevelId: thoughtLevelId } }
        : a
    ));
    void applyACPConfig('thoughtLevel', { thoughtLevel: thoughtLevelId }, () => {
      setAgents((prev) => prev.map((a, idx) =>
        idx === selectedAgentIndex && a.thoughtLevels
          ? { ...a, thoughtLevels: { ...a.thoughtLevels, currentThoughtLevelId: prevLevelId ?? '' } }
          : a
      ));
    });
  }, [agents, selectedAgentIndex, applyACPConfig]);

  const latestLoopSessionId = loopSessions.length > 0 ? loopSessions[loopSessions.length - 1].sessionId : null;
  const shouldFollowMessageListBottom = (!isLoop && !isGraph) || !activeSessionId || activeSessionId === latestLoopSessionId;
  const messageListScrollContextKey = (isLoop || isGraph)
    ? `${isGraph ? 'graph' : 'loop'}:${existingJobId}:${activeSessionId ?? 'none'}`
    : `chat:${existingJobId}`;

  // In Loop / Graph mode the footer duration badge should reflect the whole
  // job, not just the current run that `roundStartedAt` anchors to. Aggregate
  // the same way the Sessions sidebar header does: sum of finished session
  // durations plus a live delta for any still-running session. This keeps the
  // footer consistent with the sidebar and, crucially, keeps the badge in its
  // "running" (non-greyed) state while any session is still in flight — the
  // job-level roundStartedAt/roundFinishedAt fallback greys out as soon as a
  // single node session sets jobFinishedAt, even if other sessions are still
  // running.
  const loopAggregateDuration = useMemo(() => {
    if (!isLoop && !isGraph) return null;
    let baseMs = 0;
    const runningStartedAts: number[] = [];
    for (const s of loopSessions) {
      if (s.durationMs != null) baseMs += s.durationMs;
      if (s.status === 'running' && s.startedAt != null) {
        runningStartedAts.push(s.startedAt);
      }
    }
    return { baseMs, runningStartedAts };
  }, [isLoop, isGraph, loopSessions]);

  const agentEffectiveModelId = selectedAgent ? (selectedAgent.models?.currentModelId || selectedAgent.model_id) : null;
  const effectiveModelId = hasUserSelected
    ? agentEffectiveModelId
    : sessionModelId ?? agentEffectiveModelId;

  const handleSendMessage = useCallback(
    (content: string, imageUrls?: string[]) => {
      const targetSessionId = (isLoop || isGraph) ? activeSessionId : null;
      // Only interactive mode queues. Graph discussion sends must keep their
      // explicit node sessionId, which the generic queue does not retain.
      if (!isLoop && !isGraph && isLoading) {
        queueMessage({
          content,
          imageUrls,
          modelId: effectiveModelId,
          acpMode: selectedAgent?.modes?.currentModeId,
          agentType: selectedAgent?.type,
          acpThoughtLevel: selectedAgent?.thoughtLevels?.currentThoughtLevelId,
        });
        return;
      }
      sendMessage(content, effectiveModelId, targetSessionId, imageUrls, selectedAgent?.modes?.currentModeId, selectedAgent?.type, selectedAgent?.thoughtLevels?.currentThoughtLevelId);
    },
    [sendMessage, queueMessage, isLoading, effectiveModelId, isLoop, isGraph, activeSessionId, selectedAgent]
  );

  // Send initial message — only after SSE connection is ready
  useEffect(() => {
    if (initialMessageSent.current) return;
    // A hook-level error (SSE connect failure, history load failure, …) blocks
    // the dispatch below and never clears itself. The error banner already
    // shows the cause, so release the composer here instead of leaving it
    // disabled forever; if the error later clears this effect re-runs and the
    // dispatch can still proceed.
    if (error) {
      setInitialDispatchPending(false);
      return;
    }
    if (!eventsReady) return;
    if (initialAgentRefreshPending) return;
    if (isLoadingHistory) return;

    // Interactive mode: send first message (wait for agents to load so agentType is available).
    // bypassCommand=true: the home page builds a Job then hands off to us —
    // if the user typed `/help` on the home page, it must become a normal
    // first message here, not a command dispatch. Commands only apply to
    // messages the user types INSIDE an existing chat.
    if (initialMessage && selectedAgent) {
      initialMessageSent.current = true;
      sendMessage(initialMessage, effectiveModelId, null, initialImageUrls, selectedAgent?.modes?.currentModeId, selectedAgent?.type, selectedAgent?.thoughtLevels?.currentThoughtLevelId, {
        bypassCommand: true,
        optimisticMessageId: initialUserMessage?.id,
      }).catch((err) => {
        console.error('Failed to send initial message:', err);
      }).finally(() => setInitialDispatchPending(false));
    }
  }, [effectiveModelId, error, eventsReady, initialAgentRefreshPending, initialMessage, initialImageUrls, initialUserMessage, isLoadingHistory, sendMessage, selectedAgent]);

  const handleNewChat = () => {
    clearMessages();
    if (onBack) onBack();
  };

  const handleNewJob = () => {
    if (!onStartNewChat || !selectedAgent) return;
    onStartNewChat(selectedAgent.models?.currentModelId || selectedAgent.model_id, selectedAgent.type, workdir || undefined);
  };

  const handleShare = useCallback(async () => {
    if (!existingJobId) return;
    try {
      const res = await fetch(`/api/v1/job/${existingJobId}/share`, { method: 'POST' });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      const token = data.shareToken;
      setJobShareToken(token);
      // Build share URL
      const url = new URL(window.location.href);
      url.searchParams.set('shareToken', token);
      url.searchParams.delete('sessionId');
      await navigator.clipboard.writeText(url.toString());
      setShareCopied(true);
      setTimeout(() => setShareCopied(false), 2000);
    } catch (err) {
      console.error('Failed to share job:', err);
    }
  }, [existingJobId]);

  const handleCopyShareLink = useCallback(async () => {
    if (!jobShareToken) return;
    const url = new URL(window.location.href);
    url.searchParams.set('shareToken', jobShareToken);
    url.searchParams.delete('sessionId');
    await navigator.clipboard.writeText(url.toString());
    setShareCopied(true);
    setTimeout(() => setShareCopied(false), 2000);
  }, [jobShareToken]);

  const handleUnshare = useCallback(async () => {
    if (!existingJobId) return;
    try {
      const res = await fetch(`/api/v1/job/${existingJobId}/unshare`, { method: 'POST' });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      setJobShareToken(null);
    } catch (err) {
      console.error('Failed to unshare job:', err);
    }
  }, [existingJobId]);

  // Title editing: user-initiated rename. Complements the async title
  // auto-generation path — once a user has edited manually we still accept
  // future server-side auto updates the same way, there's no lock-in.
  const startEditTitle = useCallback(() => {
    if (!existingJobId || isReadonly) return;
    setEditingTitleValue(jobTitle || '');
    setTitleEditError(null);
    setIsEditingTitle(true);
  }, [existingJobId, isReadonly, jobTitle]);

  const cancelEditTitle = useCallback(() => {
    setIsEditingTitle(false);
    setEditingTitleValue('');
    setTitleEditError(null);
    setSavingTitle(false);
  }, []);

  const saveEditTitle = useCallback(async () => {
    if (!existingJobId) return;
    const trimmed = editingTitleValue.trim();
    if (!trimmed) {
      setTitleEditError(t('chat.titleEmpty'));
      return;
    }
    if ([...trimmed].length > JOB_TITLE_MAX_LEN) {
      setTitleEditError(t('chat.titleTooLong', { max: JOB_TITLE_MAX_LEN }));
      return;
    }
    if (trimmed === (jobTitle || '').trim()) {
      cancelEditTitle();
      return;
    }
    setSavingTitle(true);
    setTitleEditError(null);
    try {
      const res = await fetch(`/api/v1/job/${existingJobId}/title`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: trimmed }),
      });
      if (!res.ok) {
        const body = await res.text();
        throw new Error(body || `HTTP ${res.status}`);
      }
      const data = await res.json().catch(() => ({}));
      const next = typeof data?.title === 'string' && data.title ? data.title : trimmed;
      setJobTitle(next);
      patchJobInList(existingJobId, { title: next });
      setIsEditingTitle(false);
      setEditingTitleValue('');
    } catch (err) {
      setTitleEditError(err instanceof Error ? err.message : String(err));
    } finally {
      setSavingTitle(false);
    }
  }, [existingJobId, editingTitleValue, jobTitle, t, cancelEditTitle, setJobTitle, patchJobInList]);

  useEffect(() => {
    if (!isEditingTitle) return;
    // Autofocus + select-all matches the doc ("auto-focuses and selects the
    // original content") so the user can immediately overwrite or extend.
    const el = titleInputRef.current;
    if (el) {
      el.focus();
      el.select();
    }
  }, [isEditingTitle]);

  // Abort any in-flight edit if we navigate to a different job.
  useEffect(() => {
    setIsEditingTitle(false);
    setEditingTitleValue('');
    setTitleEditError(null);
    setSavingTitle(false);
  }, [existingJobId]);

  const handleSelectJob = (nextJobId: string) => {
    setJobListOpen(false);
    if (nextJobId === existingJobId) return;
    onSelectJob?.(nextJobId);
  };

  // Scroll a specific message bubble into view from the step-outline panel.
  // Bubbles carry data-message-id (see MessageItem); we briefly flash the
  // target so the user sees where they landed.
  const handleJumpToStep = useCallback((messageId: string) => {
    const el = document.querySelector(`[data-message-id="${CSS.escape(messageId)}"]`);
    if (!el) return;
    el.scrollIntoView({ block: 'center', behavior: 'smooth' });
    el.classList.add('step-jump-flash');
    window.setTimeout(() => el.classList.remove('step-jump-flash'), 1200);
  }, []);

  const getJobTitle = (job: JobInfo) => {
    const title = job.title?.trim();
    return title ? title : 'undefined title';
  };

  const getJobIcon = (job: JobInfo) => {
    if (job.mode === 'graph') return (
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="6" cy="6" r="2.5" />
        <circle cx="18" cy="6" r="2.5" />
        <circle cx="12" cy="18" r="2.5" />
        <path d="M8.2 7.5 11 15.7" />
        <path d="M15.8 7.5 13 15.7" />
        <path d="M8.5 6h7" />
      </svg>
    );
    if (job.mode === 'loop') return (
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <polyline points="23 4 23 10 17 10" />
        <polyline points="1 20 1 14 7 14" />
        <path d="M3.51 9a9 9 0 0114.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0020.49 15" />
      </svg>
    );
    return (
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M21 15a2 2 0 01-2 2H7l-4 4V5a2 2 0 012-2h14a2 2 0 012 2z" />
      </svg>
    );
  };

  return (
    <ServerClockProvider getServerNow={getServerNow}>
    <div className="chatbot-container" data-testid="job-chat" data-job-id={jobId || existingJobId || ''} data-job-mode={isGraph ? 'graph' : isLoop ? 'loop' : 'interactive'} data-loading={isLoading ? 'true' : 'false'}>
      <header className="chatbot-header" data-testid="job-chat-header">
        <div className="header-left">
          {!isReadonly && (
            <button className="back-button" onClick={handleNewChat} title="Back to Home">
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M19 12H5M12 19l-7-7 7-7" />
              </svg>
            </button>
          )}
          <span className="header-logo" onClick={isReadonly || isEditingTitle ? undefined : handleNewChat} style={{ cursor: isReadonly || isEditingTitle ? 'default' : 'pointer' }}>
            {userAvatarUrl ? (
              <img src={userAvatarUrl} alt="" className="header-user-avatar" referrerPolicy="no-referrer" />
            ) : (
              '🤖'
            )}
            {' '}
            {isEditingTitle ? (
              <span className="header-title-edit" onClick={(e) => e.stopPropagation()}>
                <input
                  ref={titleInputRef}
                  className={`header-title-input${titleEditError ? ' has-error' : ''}`}
                  data-testid="job-title-input"
                  value={editingTitleValue}
                  onChange={(e) => {
                    setEditingTitleValue(e.target.value);
                    if (titleEditError) setTitleEditError(null);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault();
                      void saveEditTitle();
                    } else if (e.key === 'Escape') {
                      e.preventDefault();
                      cancelEditTitle();
                    }
                  }}
                  disabled={savingTitle}
                  maxLength={JOB_TITLE_MAX_LEN * 4}
                  aria-label={t('chat.renameTitle')}
                />
                <button
                  type="button"
                  className="header-title-action save"
                  onClick={(e) => { e.stopPropagation(); void saveEditTitle(); }}
                  disabled={savingTitle || !editingTitleValue.trim()}
                  title={t('chat.titleSave')}
                  data-testid="job-title-save-button"
                >
                  {savingTitle ? (
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <circle cx="12" cy="12" r="9" strokeDasharray="40 20" />
                    </svg>
                  ) : (
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                      <path d="M20 6L9 17l-5-5" />
                    </svg>
                  )}
                </button>
                <button
                  type="button"
                  className="header-title-action cancel"
                  onClick={(e) => { e.stopPropagation(); cancelEditTitle(); }}
                  disabled={savingTitle}
                  title={t('chat.titleCancel')}
                  data-testid="job-title-cancel-button"
                >
                  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                    <line x1="18" y1="6" x2="6" y2="18" />
                    <line x1="6" y1="6" x2="18" y2="18" />
                  </svg>
                </button>
                {titleEditError && <span className="header-title-error" data-testid="job-title-error">{titleEditError}</span>}
              </span>
            ) : (
              <>
                <span className="header-logo-text">{headerTitle}</span>
                {!isReadonly && existingJobId && (
                  <button
                    type="button"
                    className="header-edit-title-btn"
                    onClick={(e) => { e.stopPropagation(); startEditTitle(); }}
                    title={t('chat.renameTitle')}
                    aria-label={t('chat.renameTitle')}
                    data-testid="job-title-edit-button"
                  >
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <path d="M12 20h9" />
                      <path d="M16.5 3.5a2.121 2.121 0 013 3L7 19l-4 1 1-4 12.5-12.5z" />
                    </svg>
                  </button>
                )}
              </>
            )}
          </span>
        </div>
        <nav className="header-nav">
          {!isReadonly && (
            <>
              {/* Page-specific buttons (left), kept in their existing relative order. */}
              {(isLoop || isGraph) && (
                <button className="loop-sidebar-toggle" onClick={() => setLoopSidebarOpen(!loopSidebarOpen)} title="Sessions" data-testid="loop-session-toggle">
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <rect x="3" y="3" width="18" height="18" rx="2" />
                    <line x1="9" y1="3" x2="9" y2="21" />
                  </svg>
                </button>
              )}
              <div className="header-joblist" ref={jobListRef}>
                <button
                  className={`header-filebrowser-btn ${jobListOpen ? 'active' : ''}`}
                  onClick={() => {
                    setHeaderMoreOpen(false);
                    setJobListOpen((open) => !open);
                  }}
                  title="Job List"
                >
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01" />
                  </svg>
                </button>
                {jobListOpen && (
                  <div className="header-joblist-menu">
                    <div className="header-joblist-title">Job List</div>
                    <VirtualList<JobSummary>
                      className="header-joblist-items"
                      items={jobs}
                      itemHeight={JOB_ROW_HEIGHT}
                      getKey={(job) => job.id}
                      onEndReached={hasMoreJobs ? () => { void loadMoreJobs(); } : undefined}
                      empty={<div className="header-joblist-empty">No jobs</div>}
                      footer={hasMoreJobs ? (
                        <div className="header-joblist-loadmore">
                          <button
                            className="header-joblist-loadmore-btn"
                            onClick={() => { void loadMoreJobs(); }}
                            disabled={isLoadingMoreJobs}
                          >
                            {isLoadingMoreJobs ? '加载中…' : '加载更多'}
                          </button>
                        </div>
                      ) : null}
                      renderItem={(job) => {
                        const jobUrl = (() => {
                          const url = new URL(window.location.href);
                          url.searchParams.delete('sessionId');
                          url.searchParams.delete('view');
                          url.searchParams.set('jobId', job.id);
                          return url.toString();
                        })();
                        return (
                          <a
                            className={`header-joblist-item ${job.id === existingJobId ? 'active' : ''}`}
                            href={jobUrl}
                            onClick={(e) => {
                              if (e.metaKey || e.ctrlKey) {
                                e.stopPropagation();
                                return;
                              }
                              e.preventDefault();
                              handleSelectJob(job.id);
                            }}
                            onAuxClick={(e) => {
                              if (e.button === 1) e.stopPropagation();
                            }}
                            title={getJobTitle(job)}
                          >
                            <span className="header-joblist-item-icon">{getJobIcon(job)}</span>
                            <span className="header-joblist-item-title">{getJobTitle(job)}</span>
                          </a>
                        );
                      }}
                    />
                  </div>
                )}
              </div>
              <button
                className="header-filebrowser-btn"
                onClick={handleNewJob}
                title="New Chat"
              >
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M12 5v14M5 12h14" />
                </svg>
              </button>
              <button
                className={`header-filebrowser-btn header-action-overflow ${outlineOpen ? 'active' : ''}`}
                onClick={() => setOutlineOpen((v) => !v)}
                title={t('chat.outline.title')}
                data-testid="step-outline-toggle"
              >
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                  <line x1="8" y1="6" x2="21" y2="6" />
                  <line x1="8" y1="12" x2="21" y2="12" />
                  <line x1="8" y1="18" x2="21" y2="18" />
                  <line x1="3" y1="6" x2="3.01" y2="6" />
                  <line x1="3" y1="12" x2="3.01" y2="12" />
                  <line x1="3" y1="18" x2="3.01" y2="18" />
                </svg>
              </button>
              {/* Share button */}
              {jobShareToken ? (
                <>
                  <button
                    className="header-filebrowser-btn header-action-overflow"
                    onClick={handleCopyShareLink}
                    title={shareCopied ? 'Copied!' : 'Copy share link'}
                  >
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke={shareCopied ? '#22c55e' : 'currentColor'} strokeWidth="2">
                      {shareCopied ? (
                        <path d="M20 6L9 17l-5-5" />
                      ) : (
                        <>
                          <circle cx="18" cy="5" r="3" />
                          <circle cx="6" cy="12" r="3" />
                          <circle cx="18" cy="19" r="3" />
                          <line x1="8.59" y1="13.51" x2="15.42" y2="17.49" />
                          <line x1="15.41" y1="6.51" x2="8.59" y2="10.49" />
                        </>
                      )}
                    </svg>
                  </button>
                  <button
                    className="header-filebrowser-btn header-action-overflow"
                    onClick={handleUnshare}
                    title="Stop sharing"
                  >
                    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                      <line x1="18" y1="6" x2="6" y2="18" />
                      <line x1="6" y1="6" x2="18" y2="18" />
                    </svg>
                  </button>
                </>
              ) : (
                <button
                  className="header-filebrowser-btn header-action-overflow"
                  onClick={handleShare}
                  title={shareCopied ? 'Link copied!' : 'Share'}
                >
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke={shareCopied ? '#22c55e' : 'currentColor'} strokeWidth="2">
                    {shareCopied ? (
                      <path d="M20 6L9 17l-5-5" />
                    ) : (
                      <>
                        <circle cx="18" cy="5" r="3" />
                        <circle cx="6" cy="12" r="3" />
                        <circle cx="18" cy="19" r="3" />
                        <line x1="8.59" y1="13.51" x2="15.42" y2="17.49" />
                        <line x1="15.41" y1="6.51" x2="8.59" y2="10.49" />
                      </>
                    )}
                  </svg>
                </button>
              )}
              {/* Shared buttons (right): same order as the home page's header. */}
              {onOpenStats && (
                <button
                  className="header-filebrowser-btn header-action-overflow"
                  onClick={onOpenStats}
                  title={t('stats.topbarTooltip')}
                  aria-label={t('stats.topbarTooltip')}
                >
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <line x1="18" y1="20" x2="18" y2="10" />
                    <line x1="12" y1="20" x2="12" y2="4" />
                    <line x1="6" y1="20" x2="6" y2="14" />
                  </svg>
                </button>
              )}
              {onOpenGraph && (
                <button
                  className="header-filebrowser-btn header-action-overflow"
                  onClick={onOpenGraph}
                  title="Graph Workflows"
                  aria-label="Graph Workflows"
                >
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <circle cx="5" cy="12" r="2" />
                    <circle cx="12" cy="5" r="2" />
                    <circle cx="19" cy="12" r="2" />
                    <circle cx="12" cy="19" r="2" />
                    <path d="M6.5 10.5 10.5 6.5" />
                    <path d="M13.5 6.5 17.5 10.5" />
                    <path d="M17.5 13.5 13.5 17.5" />
                    <path d="M10.5 17.5 6.5 13.5" />
                  </svg>
                </button>
              )}
              <button
                className={`header-filebrowser-btn header-action-overflow ${agentsEditorOpen ? 'active' : ''}`}
                onClick={() => setAgentsEditorOpen(!agentsEditorOpen)}
                title="AGENTS.md"
              >
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
                  <polyline points="14 2 14 8 20 8" />
                  <line x1="16" y1="13" x2="8" y2="13" />
                  <line x1="16" y1="17" x2="8" y2="17" />
                  <polyline points="10 9 9 9 8 9" />
                </svg>
              </button>
              <button
                className={`header-filebrowser-btn header-action-overflow ${fileBrowserOpen ? 'active' : ''}`}
                onClick={() => setFileBrowserOpen(!fileBrowserOpen)}
                title="File Browser"
              >
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
                </svg>
              </button>
              {onOpenSettings && (
                <button className="header-settings-btn header-action-overflow" onClick={onOpenSettings} title="Settings" data-testid="settings-open-button">
                  ⚙️
                </button>
              )}
              <div className="header-more" ref={headerMoreRef}>
                <button
                  type="button"
                  className={`header-filebrowser-btn header-more-trigger ${headerMoreOpen ? 'active' : ''}`}
                  onClick={() => {
                    setJobListOpen(false);
                    setHeaderMoreOpen((open) => !open);
                  }}
                  title={t('chat.headerActions.more')}
                  aria-label={t('chat.headerActions.more')}
                  aria-haspopup="menu"
                  aria-expanded={headerMoreOpen}
                >
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                    <circle cx="12" cy="12" r="1" />
                    <circle cx="19" cy="12" r="1" />
                    <circle cx="5" cy="12" r="1" />
                  </svg>
                </button>
                {headerMoreOpen && (
                  <div className="header-more-menu" role="menu">
                    <button
                      type="button"
                      className={`header-more-item ${outlineOpen ? 'active' : ''}`}
                      role="menuitem"
                      onClick={() => {
                        setHeaderMoreOpen(false);
                        setOutlineOpen((open) => !open);
                      }}
                    >
                      <span className="header-more-icon">
                        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                          <line x1="8" y1="6" x2="21" y2="6" />
                          <line x1="8" y1="12" x2="21" y2="12" />
                          <line x1="8" y1="18" x2="21" y2="18" />
                          <line x1="3" y1="6" x2="3.01" y2="6" />
                          <line x1="3" y1="12" x2="3.01" y2="12" />
                          <line x1="3" y1="18" x2="3.01" y2="18" />
                        </svg>
                      </span>
                      <span>{t('chat.outline.title')}</span>
                    </button>
                    {jobShareToken ? (
                      <>
                        <button
                          type="button"
                          className="header-more-item"
                          role="menuitem"
                          onClick={() => {
                            setHeaderMoreOpen(false);
                            void handleCopyShareLink();
                          }}
                        >
                          <span className="header-more-icon">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke={shareCopied ? '#22c55e' : 'currentColor'} strokeWidth="2">
                              {shareCopied ? (
                                <path d="M20 6L9 17l-5-5" />
                              ) : (
                                <>
                                  <circle cx="18" cy="5" r="3" />
                                  <circle cx="6" cy="12" r="3" />
                                  <circle cx="18" cy="19" r="3" />
                                  <line x1="8.59" y1="13.51" x2="15.42" y2="17.49" />
                                  <line x1="15.41" y1="6.51" x2="8.59" y2="10.49" />
                                </>
                              )}
                            </svg>
                          </span>
                          <span>{shareCopied ? t('common.copySuccess') : t('chat.headerActions.copyShareLink')}</span>
                        </button>
                        <button
                          type="button"
                          className="header-more-item"
                          role="menuitem"
                          onClick={() => {
                            setHeaderMoreOpen(false);
                            void handleUnshare();
                          }}
                        >
                          <span className="header-more-icon">
                            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                              <line x1="18" y1="6" x2="6" y2="18" />
                              <line x1="6" y1="6" x2="18" y2="18" />
                            </svg>
                          </span>
                          <span>{t('chat.headerActions.stopSharing')}</span>
                        </button>
                      </>
                    ) : (
                      <button
                        type="button"
                        className="header-more-item"
                        role="menuitem"
                        onClick={() => {
                          setHeaderMoreOpen(false);
                          void handleShare();
                        }}
                      >
                        <span className="header-more-icon">
                          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke={shareCopied ? '#22c55e' : 'currentColor'} strokeWidth="2">
                            {shareCopied ? (
                              <path d="M20 6L9 17l-5-5" />
                            ) : (
                              <>
                                <circle cx="18" cy="5" r="3" />
                                <circle cx="6" cy="12" r="3" />
                                <circle cx="18" cy="19" r="3" />
                                <line x1="8.59" y1="13.51" x2="15.42" y2="17.49" />
                                <line x1="15.41" y1="6.51" x2="8.59" y2="10.49" />
                              </>
                            )}
                          </svg>
                        </span>
                        <span>{shareCopied ? t('common.copySuccess') : t('chat.headerActions.share')}</span>
                      </button>
                    )}
                    {onOpenStats && (
                      <button
                        type="button"
                        className="header-more-item"
                        role="menuitem"
                        onClick={() => {
                          setHeaderMoreOpen(false);
                          onOpenStats();
                        }}
                      >
                        <span className="header-more-icon">
                          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                            <line x1="18" y1="20" x2="18" y2="10" />
                            <line x1="12" y1="20" x2="12" y2="4" />
                            <line x1="6" y1="20" x2="6" y2="14" />
                          </svg>
                        </span>
                        <span>{t('chat.headerActions.stats')}</span>
                      </button>
                    )}
                    {onOpenGraph && (
                      <button
                        type="button"
                        className="header-more-item"
                        role="menuitem"
                        onClick={() => {
                          setHeaderMoreOpen(false);
                          onOpenGraph();
                        }}
                      >
                        <span className="header-more-icon">
                          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                            <circle cx="5" cy="12" r="2" />
                            <circle cx="12" cy="5" r="2" />
                            <circle cx="19" cy="12" r="2" />
                            <circle cx="12" cy="19" r="2" />
                            <path d="M6.5 10.5 10.5 6.5" />
                            <path d="M13.5 6.5 17.5 10.5" />
                            <path d="M17.5 13.5 13.5 17.5" />
                            <path d="M10.5 17.5 6.5 13.5" />
                          </svg>
                        </span>
                        <span>{t('chat.headerActions.graph')}</span>
                      </button>
                    )}
                    <button
                      type="button"
                      className={`header-more-item ${agentsEditorOpen ? 'active' : ''}`}
                      role="menuitem"
                      onClick={() => {
                        setHeaderMoreOpen(false);
                        setAgentsEditorOpen((open) => !open);
                      }}
                    >
                      <span className="header-more-icon">
                        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
                          <polyline points="14 2 14 8 20 8" />
                          <line x1="16" y1="13" x2="8" y2="13" />
                          <line x1="16" y1="17" x2="8" y2="17" />
                          <polyline points="10 9 9 9 8 9" />
                        </svg>
                      </span>
                      <span>{t('chat.headerActions.agents')}</span>
                    </button>
                    <button
                      type="button"
                      className={`header-more-item ${fileBrowserOpen ? 'active' : ''}`}
                      role="menuitem"
                      onClick={() => {
                        setHeaderMoreOpen(false);
                        setFileBrowserOpen((open) => !open);
                      }}
                    >
                      <span className="header-more-icon">
                        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
                        </svg>
                      </span>
                      <span>{t('chat.headerActions.files')}</span>
                    </button>
                    {onOpenSettings && (
                      <button
                        type="button"
                        className="header-more-item"
                        role="menuitem"
                        onClick={() => {
                          setHeaderMoreOpen(false);
                          onOpenSettings();
                        }}
                      >
                        <span className="header-more-icon" aria-hidden="true">⚙️</span>
                        <span>{t('chat.headerActions.settings')}</span>
                      </button>
                    )}
                  </div>
                )}
              </div>
            </>
          )}
          {isReadonly && (
            <button
              className={`header-filebrowser-btn ${outlineOpen ? 'active' : ''}`}
              onClick={() => setOutlineOpen((v) => !v)}
              title={t('chat.outline.title')}
              data-testid="step-outline-toggle"
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <line x1="8" y1="6" x2="21" y2="6" />
                <line x1="8" y1="12" x2="21" y2="12" />
                <line x1="8" y1="18" x2="21" y2="18" />
                <line x1="3" y1="6" x2="3.01" y2="6" />
                <line x1="3" y1="12" x2="3.01" y2="12" />
                <line x1="3" y1="18" x2="3.01" y2="18" />
              </svg>
            </button>
          )}
        </nav>
      </header>

      {/* Non-loop: top error banner. Loop mode surfaces hook-level errors via
          LoopProgress's `error` prop instead, so the banner stays hidden here. */}
      {error && !isLoop && (
        <div className="error-banner" data-testid="job-error-banner">
          <span>{error}</span>
          <button onClick={() => { window.location.href = '/'; }}>Back</button>
        </div>
      )}

      {/* Loop progress bar (read-only archive of historical loop jobs) */}
      {isLoop && loopProgress && (
        <LoopProgress
          progress={loopProgress}
          status={loopStatus}
          flow={loopFlow ?? undefined}
          error={error ?? undefined}
        />
      )}

      {isGraph && (
        <GraphLoopProgress
          jobId={existingJobId}
          runId={graphRunId}
          snapshot={graphRunStatusSnapshot}
          streamError={graphStreamError}
          onSnapshot={applyGraphRunStatusSnapshot}
          readOnly={isReadonly}
          shareToken={shareToken}
          agents={agents}
          canEdit={!isReadonly}
        />
      )}

      {isLoadingHistory && !initialUserMessage && (
        <div className="chatbot-body">
          <div className="chatbot-main">
            <div className="jobchat-loading">
              <svg className="jobchat-loading-spinner" width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
                <path d="M21 12a9 9 0 11-6.219-8.56" />
              </svg>
              <span className="jobchat-loading-text">Loading…</span>
            </div>
          </div>
        </div>
      )}

      {(!isLoadingHistory || initialUserMessage) && (
      <div className={`chatbot-body ${(isLoop || isGraph) ? 'loop-layout' : ''} ${loopSidebarOpen ? 'loop-sidebar-open' : ''}`} data-testid="job-chat-body">
        {/* Session sidebar for loop + graph modes (per-iteration / per-node) */}
        {(isLoop || isGraph) && (
          <>
            <div className="loop-sidebar-overlay" onClick={() => setLoopSidebarOpen(false)} />
            <LoopSessionSidebar
              sessions={loopSessions}
              loopStatus={loopStatus}
              activeSessionId={activeSessionId}
              onSelectSession={(id) => {
                setActiveSessionId(id);
                setLoopSidebarOpen(false);
                // Update URL with sessionId for sharing
                const url = new URL(window.location.href);
                url.searchParams.set('sessionId', id);
                window.history.replaceState({}, '', url.toString());
              }}
            />
          </>
        )}
        <div className="chatbot-main">
          {(isLoop || isGraph) && activeSessionId && !loadedSessionIds.has(activeSessionId) ? (
            <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100%', color: '#888' }}>
              Loading session messages...
            </div>
          ) : (
          <MessageList
            messages={messages}
            isLoading={isLoading || initialDispatchPending}
            loadingLabel={phaseLabel(activePhase)}
            onSendMessage={isReadonly ? undefined : handleSendMessage}
            agentIconUrl={selectedAgent?.icon_url}
            agentDisplayName={selectedAgent?.display_name}
            resolveAgentForSession={resolveAgentForSession}
            jobId={jobId || undefined}
            workdir={workdir || undefined}
            shareToken={shareToken}
            followBottom={shouldFollowMessageListBottom}
            scrollContextKey={messageListScrollContextKey}
          />
          )}
          {acpConfigError && (
            <div className="acp-config-error" data-testid="acp-config-error" role="alert">
              <span>{acpConfigError}</span>
              <button type="button" onClick={() => setAcpConfigError(null)} aria-label="dismiss">×</button>
            </div>
          )}
          {agentListError && (
            <div className="acp-config-error" data-testid="agent-list-error" role="alert">
              <span>{t(agentListError.key, agentListError.params)}</span>
              <button type="button" onClick={() => setAgentListError(null)} aria-label="dismiss">×</button>
            </div>
          )}
          <ChatInput
            onSend={handleSendMessage}
            onStop={isReadonly ? undefined : stopGeneration}
            isLoading={isLoading}
            // NOTE: deliberately NOT disabled on transient SSE disconnect
            // (!connected). A DOM-disabled textarea blurs itself (killing any
            // in-progress IME composition) and silently eats clicks until the
            // health poll / SSE retry flips connected back — the user just sees
            // "click does nothing". The send path already waits for the event
            // stream (ensureEventStreamReady, 15s timeout) and surfaces errors,
            // so the composer stays editable and only the run is gated.
            disabled={initialDispatchPending || ((isLoop || isGraph) && !(activeSessionId && endedSessionIds.has(activeSessionId)))}
            readOnly={!!isReadonly}
            placeholder={isGraph && !(activeSessionId && endedSessionIds.has(activeSessionId)) ? 'Graph workflow run' : isReadonly ? 'Read-only mode' : undefined}
            localHistoryKey={`${workspaceId || 'default'}`}
            totalTokens={totalTokens}
            roundStartedAt={interactiveAccumulatedMs > 0 ? undefined : roundStartedAt}
            roundFinishedAt={interactiveAccumulatedMs > 0 ? undefined : roundFinishedAt}
            totalDurationBaseMs={
              loopAggregateDuration?.baseMs ??
              (interactiveAccumulatedMs > 0
                ? interactiveAccumulatedMs + (roundFinishedAt && roundStartedAt ? roundFinishedAt - roundStartedAt : 0)
                : undefined)
            }
            totalDurationRunningStartedAts={
              loopAggregateDuration?.runningStartedAts ??
              (interactiveAccumulatedMs > 0 && isLoading && roundStartedAt ? [roundStartedAt] : undefined)
            }
            agents={agents}
            selectedAgentIndex={selectedAgentIndex}
            workdir={workdir}
            workspaceTitle={workspaceTitle}
            workspaceId={workspaceId}
            switchableWorkspaces={isReadonly ? undefined : allWorkspaces}
            onSwitchWorkspace={isReadonly ? undefined : onSwitchWorkspaceChat}
            jobEnable={jobEnable}
            queuedMessages={isReadonly ? undefined : queuedMessages}
            onCancelQueuedMessage={isReadonly ? undefined : cancelQueuedMessage}
            canQueueWhileRunning={!isLoop && !isGraph && !isReadonly}
            onSelectModel={selectedAgent?.models ? handleSelectModel : undefined}
            onSelectMode={selectedAgent?.modes ? handleSelectMode : undefined}
            onSelectThoughtLevel={selectedAgent?.thoughtLevels ? handleSelectThoughtLevel : undefined}
            favoriteModelIds={selectedAgent ? agentPrefs[selectedAgent.type]?.favorite_model_ids : undefined}
            overrideModelId={hasUserSelected ? undefined : sessionModelId}
            overrideModeId={hasUserSelectedMode ? undefined : sessionACPMode}
            overrideThoughtLevelId={hasUserSelectedThoughtLevel ? undefined : sessionACPThoughtLevel}
          />
        </div>

        {outlineOpen && (
          <>
            <div className="step-outline-overlay" onClick={() => setOutlineOpen(false)} />
            <StepOutline
              messages={messages}
              onJump={handleJumpToStep}
              onClose={() => setOutlineOpen(false)}
            />
          </>
        )}

        {!isReadonly && fileBrowserOpen && createPortal(
          <FileBrowser
            rootPath={workdir}
            jobId={jobId || undefined}
            onClose={() => setFileBrowserOpen(false)}
          />,
          document.body
        )}

        {!isReadonly && agentsEditorOpen && workdir && createPortal(
          <AgentsLocalEditor
            workdir={workdir}
            jobId={jobId || undefined}
            onClose={() => setAgentsEditorOpen(false)}
          />,
          document.body
        )}
      </div>
      )}
    </div>
    </ServerClockProvider>
  );
}
