import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useJobChat } from '../hooks/useJobChat';
import { useJobList, type JobSummary } from '../hooks/useJobList';
import { MessageList } from './MessageList';
import { ChatInput } from './ChatInput';
import { LoopProgress } from './LoopProgress';
import { LoopConfigPanel } from './LoopConfigPanel';
import { LoopSessionSidebar } from './LoopSessionSidebar';
import { FileBrowser } from './FileBrowser';
import { AgentsLocalEditor } from './AgentsLocalEditor';
import { AgentInfo } from './ChatPage';
import { LoopConfig } from '../types';
import { useConnectionStatus } from '../contexts/ConnectionStatus';
import { ServerClockProvider } from '../contexts/ServerClock';
import { VirtualList } from './VirtualList';
import { registerWorkspaceColors } from '../utils/workspace';
import './JobChat.css';

// Must match the backend limit in cmd/web/handler/job.go (jobTitleMaxLen).
const JOB_TITLE_MAX_LEN = 200;

async function fetchAgentList(shareToken?: string, jobId?: string): Promise<{ agents: AgentInfo[]; workdir: string; jobEnable: boolean }> {
  try {
    const url = new URL(shareToken ? '/api/v1/public/agent/list' : '/api/v1/agent/list', window.location.origin);
    if (shareToken) {
      url.searchParams.set('shareToken', shareToken);
      if (jobId) url.searchParams.set('jobId', jobId);
    }
    const res = await fetch(url.pathname + url.search);
    const data = await res.json().catch(() => null);
    if (!data || data.code !== 0 || !data.agent_list) {
      return { agents: [], workdir: '', jobEnable: false };
    }
    return { agents: data.agent_list as AgentInfo[], workdir: data.workdir || '', jobEnable: !!data.job_enable };
  } catch (err) {
    console.error('Failed to fetch agent list:', err);
    return { agents: [], workdir: '', jobEnable: false };
  }
}

interface JobChatProps {
  existingJobId: string;
  initialMessage?: string | null;
  initialImageUrls?: string[];
  initialWorkdir?: string;
  initialLoopConfig?: LoopConfig;
  initialSessionId?: string;
  initialModelId?: string;
  initialAgentType?: string;
  initialAcpMode?: string;
  workspaceId?: string;
  shareToken?: string;
  isReadonly?: boolean;
  onBack?: () => void;
  onJobCreated?: (jobId: string) => void;
  onSelectJob?: (jobId: string, workspaceId?: string) => void;
  onOpenSettings?: () => void;
  onOpenStats?: () => void;
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
  mode?: 'interactive' | 'loop';
  workdir?: string;
  updatedAt: number;
  scheduleId?: string;
}

// JOB_ROW_HEIGHT matches the fixed row height declared in .header-joblist-item
// (see JobChat.css). The VirtualList uses this to compute the slice of rows
// that overlap the viewport, so it must stay in sync with the CSS.
const JOB_ROW_HEIGHT = 36;

export function JobChat(props: JobChatProps) {
  const { existingJobId, initialMessage, initialImageUrls, initialWorkdir, initialLoopConfig, initialSessionId, initialModelId, initialAgentType, initialAcpMode, workspaceId, shareToken, isReadonly, onBack, onJobCreated, onSelectJob, onOpenSettings, onOpenStats, onStartNewChat, onSwitchWorkspaceChat, onJobNotFound } = props;
  const { connected } = useConnectionStatus();
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
  const workspaceWorkdir = workspaceMeta.workdir;

  const {
    jobId,
    jobTitle,
    setJobTitle,
    jobShareToken: initialShareToken,
    messages,
    isLoading,
    isLoadingHistory,
    error,
    sessionModelId,
    sessionType,
    sessionACPMode,
    totalTokens,
    roundStartedAt,
    roundFinishedAt,
    interactiveAccumulatedMs,
    sessionWorkdir,
    isLoop,
    loopProgress,
    loopStatus,
    stopPending,
    loopFlow,
    loopVariables,
    loopSessions,
    activeSessionId,
    setActiveSessionId,
    endedSessionIds,
    loadedSessionIds,
    sendMessage,
    queueMessage,
    cancelQueuedMessage,
    queuedMessages,
    startLoop,
    continueLoop,
    stopLoop,
    cancelStop,
    updateLoopConfig,
    stopGeneration,
    clearMessages,
    eventsReady,
    getSessionMeta,
    getServerNow,
  } = useJobChat({ existingJobId, initialSessionId, shareToken, onJobNotFound });

  const [agents, setAgents] = useState<AgentInfo[]>([]);
  const [workdir, setWorkdir] = useState(initialWorkdir || '');
  const [selectedAgentIndex, setSelectedAgentIndex] = useState<number>(0);
  const [hasUserSelected, setHasUserSelected] = useState(false);
  const [allowEinoSelection, setAllowEinoSelection] = useState<boolean | null>(null);
  const [jobEnable, setJobEnable] = useState(false);
  const [loopSidebarOpen, setLoopSidebarOpen] = useState(false);
  // Loop config editor (edit an existing job's LoopConfig in place).
  const [loopEditorOpen, setLoopEditorOpen] = useState(false);
  const [loopEditError, setLoopEditError] = useState('');
  const [fileBrowserOpen, setFileBrowserOpen] = useState(false);
  const [agentsEditorOpen, setAgentsEditorOpen] = useState(false);
  const [jobListOpen, setJobListOpen] = useState(false);
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
  const jobListRef = useRef<HTMLDivElement | null>(null);

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

  useEffect(() => {
    fetchAgentList(shareToken, existingJobId).then(({ agents: list, jobEnable: je }) => {
      // Apply initial modelId/acpMode to the matching agent so the initial
      // message uses the model the user selected on the ChatPage.
      let finalList = list;
      if (initialModelId || initialAcpMode) {
        finalList = list.map((agent) => {
          const isTarget = initialModelId
            ? agent.model_id === initialModelId || agent.models?.availableModels.some((m) => m.modelId === initialModelId)
            : (initialAgentType && initialAgentType !== 'eino' ? agent.type === initialAgentType : false);
          if (!isTarget) return agent;
          let updated = agent;
          if (initialModelId && updated.models && updated.models.availableModels.some((m) => m.modelId === initialModelId) && updated.models.currentModelId !== initialModelId) {
            updated = { ...updated, models: { ...updated.models, currentModelId: initialModelId } };
          }
          if (initialAcpMode && updated.modes && updated.modes.currentModeId !== initialAcpMode) {
            updated = { ...updated, modes: { ...updated.modes, currentModeId: initialAcpMode } };
          }
          return updated;
        });
      }

      setAgents(finalList);
      setJobEnable(je);
      if (finalList.length > 0) {
        if (initialModelId) {
          const idx = finalList.findIndex((a) => a.model_id === initialModelId || a.models?.availableModels.some((m) => m.modelId === initialModelId));
          if (idx >= 0) { setSelectedAgentIndex(idx); return; }
        }
        if (initialAgentType && initialAgentType !== 'eino') {
          const idx = finalList.findIndex((a) => a.type === initialAgentType);
          if (idx >= 0) setSelectedAgentIndex(idx);
        }
      }
    });
  }, [existingJobId, shareToken, initialAgentType, initialModelId, initialAcpMode]);

  useEffect(() => {
    if (sessionWorkdir) setWorkdir(sessionWorkdir);
  }, [sessionWorkdir]);

  useEffect(() => {
    if (!sessionModelId && !sessionACPMode) return;
    if (agents.length === 0) return;

    setAgents((prev) => prev.map((agent, idx) => {
      const shouldApplySessionACP = sessionType
        ? agent.type === sessionType
        : idx === selectedAgentIndex;
      if (!shouldApplySessionACP) return agent;

      let updated = agent;
      if (sessionModelId && updated.models && updated.models.availableModels.some((m) => m.modelId === sessionModelId) && updated.models.currentModelId !== sessionModelId) {
        updated = { ...updated, models: { ...updated.models, currentModelId: sessionModelId } };
      }
      if (sessionACPMode && updated.modes && updated.modes.currentModeId !== sessionACPMode) {
        updated = { ...updated, modes: { ...updated.modes, currentModeId: sessionACPMode } };
      }
      return updated;
    }));
  }, [sessionModelId, sessionACPMode, sessionType, agents.length, selectedAgentIndex]);

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
    // In loop mode, always update agent index when active session changes.
    // In interactive mode, skip if user has manually selected an agent.
    if (hasUserSelected && !isLoop) return;
    if (agents.length === 0) return;

    let matched = false;

    // `sessionModelId` is the only stable identifier for a concrete agent.
    if (sessionModelId) {
      const idx = agents.findIndex((a) => a.model_id === sessionModelId || a.models?.availableModels.some((m) => m.modelId === sessionModelId));
      if (idx >= 0) { setSelectedAgentIndex(idx); matched = true; }
    }

    // `eino` is too generic in current backend semantics, so only use non-eino
    // types as a fallback when model id is unavailable.
    if (!matched && sessionType && sessionType !== 'eino') {
      const idx = agents.findIndex((a) => a.type === sessionType);
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

  // Resolve agent icon/name for a specific session. Falls back to selectedAgent.
  const resolveAgentForSession = useCallback((sessionId?: string): { iconUrl?: string; displayName?: string } => {
    if (!sessionId || agents.length === 0) {
      return { iconUrl: selectedAgent?.icon_url, displayName: selectedAgent?.display_name };
    }
    const meta = getSessionMeta(sessionId);
    if (!meta) {
      return { iconUrl: selectedAgent?.icon_url, displayName: selectedAgent?.display_name };
    }
    // Try to match by modelId first, then by type
    let matched: AgentInfo | undefined;
    if (meta.modelId) {
      matched = agents.find((a) => a.model_id === meta.modelId || a.models?.availableModels.some((m) => m.modelId === meta.modelId));
    }
    if (!matched && meta.type) {
      matched = agents.find((a) => a.type === meta.type);
    }
    if (matched) {
      return { iconUrl: matched.icon_url, displayName: matched.display_name };
    }
    return { iconUrl: selectedAgent?.icon_url, displayName: selectedAgent?.display_name };
  }, [agents, selectedAgent, getSessionMeta]);

  const einoAgents = agents.filter((agent) => agent.type === 'eino');
  const selectedEinoIndex = selectedAgent
    ? einoAgents.findIndex((agent) => agent.model_id === selectedAgent.model_id && agent.type === selectedAgent.type)
    : -1;

  const handleSelectModel = useCallback((modelId: string) => {
    setAgents((prev) => prev.map((agent, idx) =>
      idx === selectedAgentIndex && agent.models
        ? { ...agent, models: { ...agent.models, currentModelId: modelId } }
        : agent
    ));
  }, [selectedAgentIndex]);

  const handleSelectMode = useCallback((modeId: string) => {
    setAgents((prev) => prev.map((agent, idx) =>
      idx === selectedAgentIndex && agent.modes
        ? { ...agent, modes: { ...agent.modes, currentModeId: modeId } }
        : agent
    ));
  }, [selectedAgentIndex]);

  // Lock selector behavior once when entering chat page:
  // only sessions that start with an eino agent can switch among eino agents.
  useEffect(() => {
    if (allowEinoSelection !== null) return;
    if (isLoadingHistory) return;
    if (agents.length === 0) return;

    let entryType: string | null = null;
    if (sessionModelId) {
      const matched = agents.find((a) => a.model_id === sessionModelId || a.models?.availableModels.some((m) => m.modelId === sessionModelId));
      entryType = matched?.type ?? null;
    }
    if (!entryType && sessionType) {
      entryType = sessionType;
    }
    if (!entryType) {
      entryType = selectedAgent?.type ?? null;
    }

    if (entryType) {
      setAllowEinoSelection(entryType === 'eino');
    }
  }, [allowEinoSelection, isLoadingHistory, agents, sessionModelId, sessionType, selectedAgent]);

  const chatInputAgents = allowEinoSelection ? einoAgents : agents;
  const chatInputSelectedAgentIndex = allowEinoSelection
    ? (selectedEinoIndex >= 0 ? selectedEinoIndex : 0)
    : selectedAgentIndex;
  const latestLoopSessionId = loopSessions.length > 0 ? loopSessions[loopSessions.length - 1].sessionId : null;
  const shouldFollowMessageListBottom = !isLoop || !activeSessionId || activeSessionId === latestLoopSessionId;
  const messageListScrollContextKey = isLoop
    ? `loop:${existingJobId}:${activeSessionId ?? 'none'}`
    : `chat:${existingJobId}`;

  // In Loop mode the footer duration badge should reflect the whole job, not
  // just the current run that `roundStartedAt` anchors to. Aggregate the same
  // way the Sessions sidebar header does: sum of finished session durations
  // plus a live delta for any still-running session.
  const loopAggregateDuration = useMemo(() => {
    if (!isLoop) return null;
    let baseMs = 0;
    const runningStartedAts: number[] = [];
    for (const s of loopSessions) {
      if (s.durationMs != null) baseMs += s.durationMs;
      if (s.status === 'running' && s.startedAt != null) {
        runningStartedAts.push(s.startedAt);
      }
    }
    return { baseMs, runningStartedAts };
  }, [isLoop, loopSessions]);

  const agentEffectiveModelId = selectedAgent ? (selectedAgent.models?.currentModelId || selectedAgent.model_id) : null;
  const effectiveModelId = hasUserSelected
    ? agentEffectiveModelId
    : sessionModelId ?? agentEffectiveModelId;
  const canContinueLoop = true;

  const handleSendMessage = useCallback(
    (content: string, imageUrls?: string[]) => {
      const targetSessionId = isLoop ? activeSessionId : null;
      // Interactive mode with a run already in flight → queue instead of sending.
      // Loop mode never queues (input is disabled during loop runs anyway).
      if (!isLoop && isLoading) {
        queueMessage({
          content,
          imageUrls,
          modelId: effectiveModelId,
          acpMode: selectedAgent?.modes?.currentModeId,
          agentType: selectedAgent?.type,
        });
        return;
      }
      sendMessage(content, effectiveModelId, targetSessionId, imageUrls, selectedAgent?.modes?.currentModeId, selectedAgent?.type);
    },
    [sendMessage, queueMessage, isLoading, effectiveModelId, isLoop, activeSessionId, selectedAgent]
  );

  // Send initial message or start loop — only after SSE connection is ready
  useEffect(() => {
    if (initialMessageSent.current) return;
    if (!eventsReady) return;
    if (isLoadingHistory || error) return;

    // Loop mode: auto-start
    if (initialLoopConfig && jobId) {
      initialMessageSent.current = true;
      startLoop();
      return;
    }

    // Interactive mode: send first message (wait for agents to load so agentType is available).
    // bypassCommand=true: the home page builds a Job then hands off to us —
    // if the user typed `/help` on the home page, it must become a normal
    // first message here, not a command dispatch. Commands only apply to
    // messages the user types INSIDE an existing chat.
    if (initialMessage && messages.length === 0 && selectedAgent) {
      initialMessageSent.current = true;
      sendMessage(initialMessage, effectiveModelId, null, initialImageUrls, selectedAgent?.modes?.currentModeId, selectedAgent?.type, { bypassCommand: true }).catch((err) => {
        console.error('Failed to send initial message:', err);
      });
    }
  }, [effectiveModelId, error, eventsReady, initialMessage, initialImageUrls, initialLoopConfig, isLoadingHistory, jobId, messages.length, sendMessage, startLoop, selectedAgent]);

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

  const getJobTitle = (job: JobInfo) => {
    const title = job.title?.trim();
    return title ? title : 'undefined title';
  };

  const getJobIcon = (job: JobInfo) => {
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
    <div className="chatbot-container" data-testid="job-chat" data-job-id={jobId || existingJobId || ''} data-job-mode={isLoop ? 'loop' : 'interactive'} data-loading={isLoading ? 'true' : 'false'}>
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
              {isLoop && (
                <button className="loop-sidebar-toggle" onClick={() => setLoopSidebarOpen(!loopSidebarOpen)} title="Sessions" data-testid="loop-session-toggle">
                  <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <rect x="3" y="3" width="18" height="18" rx="2" />
                    <line x1="9" y1="3" x2="9" y2="21" />
                  </svg>
                </button>
              )}
              {onOpenStats && (
                <button
                  className="header-filebrowser-btn"
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
              <div className="header-joblist" ref={jobListRef}>
                <button
                  className={`header-filebrowser-btn ${jobListOpen ? 'active' : ''}`}
                  onClick={() => setJobListOpen((open) => !open)}
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
                className={`header-filebrowser-btn ${agentsEditorOpen ? 'active' : ''}`}
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
                className={`header-filebrowser-btn ${fileBrowserOpen ? 'active' : ''}`}
                onClick={() => setFileBrowserOpen(!fileBrowserOpen)}
                title="File Browser"
              >
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
                </svg>
              </button>
              {/* Share button */}
              {jobShareToken ? (
                <>
                  <button
                    className="header-filebrowser-btn"
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
                    className="header-filebrowser-btn"
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
                  className="header-filebrowser-btn"
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
              {onOpenSettings && (
                <button className="header-settings-btn" onClick={onOpenSettings} title="Settings" data-testid="settings-open-button">
                  ⚙️
                </button>
              )}
            </>
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

      {/* Loop progress bar */}
      {isLoop && loopProgress && (
        <LoopProgress
          progress={loopProgress}
          status={loopStatus}
          flow={loopFlow ?? initialLoopConfig?.flow ?? undefined}
          onStop={isReadonly ? undefined : stopLoop}
          stopPending={stopPending}
          onCancelStop={isReadonly ? undefined : cancelStop}
          onContinue={!isReadonly && canContinueLoop ? continueLoop : undefined}
          onEdit={isReadonly ? undefined : () => { setLoopEditError(''); setLoopEditorOpen(true); }}
          error={error ?? undefined}
        />
      )}

      {isLoop && loopEditorOpen && (
        <LoopConfigPanel
          agents={agents}
          initialConfig={{
            flow: loopFlow ?? initialLoopConfig?.flow ?? [],
            // Prefer variables hydrated from the fetched job (loopVariables) so a
            // job opened from the list / after refresh shows its saved variables.
            // `undefined` means "not hydrated yet" and may fall back to the
            // brand-new-loop initial config; `{}` means "saved empty" and must
            // not resurrect stale initial variables after deleting the last one.
            ...(loopVariables !== undefined
              ? { variables: loopVariables }
              : initialLoopConfig?.variables
                ? { variables: initialLoopConfig.variables }
                : {}),
          }}
          runningLock={loopStatus === 'running'}
          saveError={loopEditError}
          onSave={async (config) => {
            try {
              await updateLoopConfig(config);
              setLoopEditorOpen(false);
              setLoopEditError('');
            } catch (err) {
              setLoopEditError(err instanceof Error ? err.message : String(err));
              throw err;
            }
          }}
          onConfirm={() => { /* unused in edit mode */ }}
          onCancel={() => { setLoopEditorOpen(false); setLoopEditError(''); }}
        />
      )}

      {isLoadingHistory && (
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

      {!isLoadingHistory && (
      <div className={`chatbot-body ${isLoop ? 'loop-layout' : ''} ${loopSidebarOpen ? 'loop-sidebar-open' : ''}`} data-testid="job-chat-body">
        {/* Session sidebar for loop mode */}
        {isLoop && (
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
          {isLoop && activeSessionId && !loadedSessionIds.has(activeSessionId) ? (
            <div style={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100%', color: '#888' }}>
              Loading session messages...
            </div>
          ) : (
          <MessageList
            messages={messages}
            isLoading={isLoading}
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
          <ChatInput
            onSend={handleSendMessage}
            onStop={isReadonly ? undefined : stopGeneration}
            isLoading={isLoading}
            disabled={(isLoop && !(activeSessionId && endedSessionIds.has(activeSessionId))) || !connected}
            readOnly={!!isReadonly}
            placeholder={isReadonly ? 'Read-only mode' : undefined}
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
            agents={chatInputAgents}
            selectedAgentIndex={chatInputSelectedAgentIndex}
            workdir={workdir}
            workspaceTitle={workspaceTitle}
            workspaceId={workspaceId}
            displayWorkdir={selectedAgent?.type === 'eino' ? (workspaceWorkdir || workdir) : workdir}
            switchableWorkspaces={isReadonly ? undefined : allWorkspaces}
            onSwitchWorkspace={isReadonly ? undefined : onSwitchWorkspaceChat}
            jobEnable={jobEnable}
            queuedMessages={isReadonly ? undefined : queuedMessages}
            onCancelQueuedMessage={isReadonly ? undefined : cancelQueuedMessage}
            canQueueWhileRunning={!isLoop && !isReadonly}
            onSelectAgent={allowEinoSelection ? (idx) => {
              const agent = einoAgents[idx];
              if (!agent) return;

              const originIdx = agents.findIndex((a) => a.model_id === agent.model_id && a.type === agent.type);
              if (originIdx < 0) return;

              setHasUserSelected(true);
              setSelectedAgentIndex(originIdx);
            } : undefined}
            onSelectModel={selectedAgent?.models ? handleSelectModel : undefined}
            onSelectMode={selectedAgent?.modes ? handleSelectMode : undefined}
            overrideModelId={sessionModelId}
            overrideModeId={sessionACPMode}
          />
        </div>

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
