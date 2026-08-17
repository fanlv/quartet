import { useState, useCallback, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { JobChat, ChatPage, GraphWorkflowPage, Settings } from './components';
import { StatsPage } from './components/stats/StatsPage';
import { ConnectionStatusProvider } from './contexts/ConnectionStatus';
import { markBootStage, reportBootFailure } from './utils/boot';
import { prefetchSkills } from './utils/skills';
import { DEFAULT_WORKSPACE_ID, getLastUsedWorkspaceId, setLastUsedWorkspaceId, loadWorkspacePrefs, registerWorkspaceColors } from './utils/workspace';
import './App.css';

interface WorkspaceInfo {
  id: string;
  title: string;
  description: string;
  workdir: string;
  color?: string;
}

function getJobIdFromUrl(): string | undefined {
  const params = new URLSearchParams(window.location.search);
  return params.get('jobId') || undefined;
}

function getSessionIdFromUrl(): string | undefined {
  const params = new URLSearchParams(window.location.search);
  return params.get('sessionId') || undefined;
}

function getWorkspaceIdFromUrl(): string | undefined {
  const params = new URLSearchParams(window.location.search);
  return params.get('workspaceId') || undefined;
}

function getShareTokenFromUrl(): string | undefined {
  const params = new URLSearchParams(window.location.search);
  return params.get('shareToken') || undefined;
}

function getStatsOpenFromUrl(): boolean {
  const params = new URLSearchParams(window.location.search);
  return params.get('view') === 'stats';
}

function getGraphOpenFromUrl(): boolean {
  const params = new URLSearchParams(window.location.search);
  return params.get('view') === 'graph';
}

function updateUrlWithJobId(jobId: string, keepSessionId = false) {
  const url = new URL(window.location.href);
  if (!keepSessionId) url.searchParams.delete('sessionId');
  url.searchParams.delete('view');
  url.searchParams.set('jobId', jobId);
  window.history.pushState({}, '', url.toString());
}

function updateUrlWithWorkspaceId(workspaceId: string) {
  const url = new URL(window.location.href);
  url.searchParams.delete('jobId');
  url.searchParams.delete('sessionId');
  url.searchParams.delete('view');
  url.searchParams.set('workspaceId', workspaceId);
  window.history.pushState({}, '', url.toString());
}

// updateUrlWithStats toggles the standalone Statistics page via the URL so
// it can be reached directly, refreshed, and bookmarked. We use pushState
// when opening (so Browser-Back closes it) and replaceState when closing
// (so we don't leave a no-op history entry behind).
function updateUrlWithStats(open: boolean) {
  const url = new URL(window.location.href);
  if (open) {
    url.searchParams.set('view', 'stats');
    window.history.pushState({}, '', url.toString());
  } else {
    url.searchParams.delete('view');
    window.history.replaceState({}, '', url.toString());
  }
}

function updateUrlWithGraph(open: boolean) {
  const url = new URL(window.location.href);
  if (open) {
    url.searchParams.delete('jobId');
    url.searchParams.delete('sessionId');
    url.searchParams.set('view', 'graph');
    window.history.pushState({}, '', url.toString());
  } else {
    url.searchParams.delete('view');
    window.history.replaceState({}, '', url.toString());
  }
}

function graphUrl(): string {
  const url = new URL(window.location.href);
  url.searchParams.delete('jobId');
  url.searchParams.delete('sessionId');
  url.searchParams.set('view', 'graph');
  return url.toString();
}

function App() {
  const { t } = useTranslation();
  const [showChat, setShowChat] = useState(() => !!getJobIdFromUrl());
  const [initialMessage, setInitialMessage] = useState<string | null>(null);
  const [initialImageUrls, setInitialImageUrls] = useState<string[] | undefined>();
  const [currentJobId, setCurrentJobId] = useState<string | undefined>(() => getJobIdFromUrl());
  const [initialSessionId, setInitialSessionId] = useState<string | undefined>(() => getSessionIdFromUrl());
  const [shareToken] = useState<string | undefined>(() => getShareTokenFromUrl());
  const isReadonly = !!shareToken;
  const [isInitializing, setIsInitializing] = useState(false);
  const [initialWorkdir, setInitialWorkdir] = useState<string | undefined>();
  const [initialModelId, setInitialModelId] = useState<string | undefined>();
  const [initialAgentType, setInitialAgentType] = useState<string | undefined>();
  const [initialAcpMode, setInitialAcpMode] = useState<string | undefined>();
  const [initialAcpThoughtLevel, setInitialAcpThoughtLevel] = useState<string | undefined>();
  const [showSettings, setShowSettings] = useState(false);
  const [showStats, setShowStats] = useState(() => getStatsOpenFromUrl());
  const [showGraph, setShowGraph] = useState(() => getGraphOpenFromUrl());
  const graphDirtyRef = useRef(false);
  const [homeRefreshKey, setHomeRefreshKey] = useState(0);
  const [missingJobNoticeId, setMissingJobNoticeId] = useState<string | null>(null);

  useEffect(() => {
    if (!isReadonly) prefetchSkills();
  }, [isReadonly]);

  // Workspace state. On first load:
  //   1. Try URL `?workspaceId=...`.
  //   2. Fall back to the last-used workspace from localStorage.
  //   3. Fall back to the default workspace (ws-1).
  // localStorage holds per-workspace cached metadata so the UI has something
  // to show before the async /workspace/list round-trip completes.
  const [currentWorkspace, setCurrentWorkspace] = useState<WorkspaceInfo | undefined>(() => {
    const wsId = getWorkspaceIdFromUrl() || getLastUsedWorkspaceId();
    if (wsId) {
      const saved = localStorage.getItem(`workspace_${wsId}`);
      if (saved) {
        try { return JSON.parse(saved); } catch { /* ignore */ }
      }
      // We know the ID even if the metadata cache isn't there yet — populate
      // a stub so routing stays consistent; handleSelectWorkspace will replace
      // this once the real workspace is fetched.
      return { id: wsId, title: '', description: '', workdir: '' };
    }
    return undefined;
  });

  useEffect(() => {
    const handlePopState = () => {
      const graphOpen = getGraphOpenFromUrl();
      if (showGraph && !graphOpen && graphDirtyRef.current && !window.confirm(t('graph.messages.discardUnsavedConfirm'))) {
        window.history.pushState({}, '', graphUrl());
        return;
      }
      const jobId = getJobIdFromUrl();
      const wsId = getWorkspaceIdFromUrl();
      const sid = getSessionIdFromUrl();
      setShowGraph(graphOpen);
      setShowStats(getStatsOpenFromUrl());
      setCurrentJobId(jobId);
      setInitialSessionId(sid);
      setShowChat(!graphOpen && !!jobId);
      if (!jobId) {
        setInitialMessage(null);
        setInitialImageUrls(undefined);
        setInitialModelId(undefined);
        setInitialAgentType(undefined);
        setInitialAcpMode(undefined);
      }
      if (!wsId) {
        setCurrentWorkspace(undefined);
      } else if (!currentWorkspace || currentWorkspace.id !== wsId) {
        const saved = localStorage.getItem(`workspace_${wsId}`);
        let ws: WorkspaceInfo | undefined;
        if (saved) {
          try { ws = JSON.parse(saved); } catch { /* ignore */ }
        }
        // Cache miss (or parse failure): keep the workspace ID in sync with the
        // URL so routing + new-job creation don't keep using a stale workspace.
        if (!ws) {
          ws = { id: wsId, title: '', description: '', workdir: '' };
        }
        setCurrentWorkspace(ws);
      }
    };
    window.addEventListener('popstate', handlePopState);
    return () => window.removeEventListener('popstate', handlePopState);
  }, [currentWorkspace, showGraph, t]);

  const handleSelectWorkspace = useCallback((ws: WorkspaceInfo) => {
    setCurrentWorkspace(ws);
    localStorage.setItem(`workspace_${ws.id}`, JSON.stringify(ws));
    setLastUsedWorkspaceId(ws.id);
    updateUrlWithWorkspaceId(ws.id);
  }, []);

  // First-boot workspace bootstrap. Resolves the current workspace against
  // the real list from the server:
  //   - URL workspace missing in list → fall back to ws-1
  //   - Stub metadata (title/workdir empty) → fill in from the server
  //   - No workspace selected at all → fall back to ws-1
  // This also guarantees the UI has a real workspace object for first-time
  // users (the server creates ws-1 on boot; we just need to fetch it).
  useEffect(() => {
    if (isReadonly) {
      markBootStage('workspace-initialization-skipped', 'public-share');
      return;
    }
    let cancelled = false;
    markBootStage('workspace-initialization-start');
    (async () => {
      try {
        const res = await fetch('/api/v1/workspace/list');
        const rawBody = await res.text();
        if (!res.ok) {
          const status = `${res.status}${res.statusText ? ` ${res.statusText}` : ''}`;
          throw new Error(`GET /api/v1/workspace/list returned HTTP ${status}${rawBody ? `\n${rawBody}` : ''}`);
        }
        let data: { workspaces?: WorkspaceInfo[] };
        try {
          data = JSON.parse(rawBody) as { workspaces?: WorkspaceInfo[] };
        } catch (error) {
          throw new Error(`GET /api/v1/workspace/list returned invalid JSON\n${rawBody}`, { cause: error });
        }
        const list: WorkspaceInfo[] = data?.workspaces || [];
        if (cancelled) return;
        if (list.length === 0) throw new Error('GET /api/v1/workspace/list returned an empty workspace list');

        registerWorkspaceColors(list);

        // Cache every workspace so later lookups (e.g. workspace tags on Job
        // rows) have something to show synchronously.
        for (const ws of list) {
          localStorage.setItem(`workspace_${ws.id}`, JSON.stringify(ws));
        }

        const urlId = getWorkspaceIdFromUrl();
        const currentId = currentWorkspace?.id;
        const targetId = urlId || currentId || getLastUsedWorkspaceId() || DEFAULT_WORKSPACE_ID;
        const found = list.find((ws) => ws.id === targetId)
          ?? list.find((ws) => ws.id === DEFAULT_WORKSPACE_ID)
          ?? list[0];
        if (!found) return;

        // If the URL is missing the workspace id, sync it so refresh keeps
        // the same context.
        if (!urlId) {
          const url = new URL(window.location.href);
          url.searchParams.set('workspaceId', found.id);
          window.history.replaceState({}, '', url.toString());
        }

        setCurrentWorkspace(found);
        localStorage.setItem(`workspace_${found.id}`, JSON.stringify(found));
        setLastUsedWorkspaceId(found.id);
      } catch (error) {
        const detail = error instanceof Error ? error.stack || error.message : String(error);
        markBootStage('workspace-initialization-failed', detail);
        reportBootFailure('WORKSPACE_INITIALIZATION_ERROR', detail);
        console.error('[App] workspace initialization failed', error);
      } finally {
        markBootStage('workspace-initialization-finished');
      }
    })();
    return () => { cancelled = true; };
    // Only runs once at mount — intentionally no deps on currentWorkspace to
    // avoid re-running when user switches workspaces.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isReadonly]);


  // Normal chat: create job then navigate
  const handleStartChat = useCallback(async (message: string, modelId: string, type: string, workdir?: string, imageUrls?: string[], acpMode?: string, acpThoughtLevel?: string) => {
    setMissingJobNoticeId(null);
    setIsInitializing(true);
    try {
      // Web-only: workspace-level default agent/model takes priority over the
      // explicit selection only when the caller didn't pick anything. In this
      // handler the caller always passes agent/model from the UI dropdown, so
      // workspace prefs only fill in when the UI hasn't made a choice.
      const prefs = loadWorkspacePrefs(currentWorkspace?.id);
      const effectiveType = type || prefs.defaultAgent || '';
      const effectiveModel = modelId || prefs.defaultModel || '';
      const body: Record<string, unknown> = {
        modelId: effectiveModel,
        agentType: effectiveType,
        mode: 'interactive',
      };
      if (workdir) body.workdir = workdir;
      // Fallback to the default workspace when currentWorkspace hasn't been
      // resolved yet (first-time users whose URL + localStorage are empty
      // while /workspace/list is still in flight). Mirrors the bootstrap
      // fallback in the workspace-list effect, so job creation never fires
      // an empty workspaceId and trips the server's strict validation.
      body.workspaceId = currentWorkspace?.id ?? DEFAULT_WORKSPACE_ID;
      if (acpMode) body.acpMode = acpMode;
      if (acpThoughtLevel) body.acpThoughtLevel = acpThoughtLevel;

      const response = await fetch('/api/v1/job/create', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!response.ok) {
        const errData = await response.json().catch(() => null);
        throw new Error(errData?.error || `HTTP ${response.status}`);
      }
      const data = await response.json();
      const jobId = data.jobId;

      updateUrlWithJobId(jobId);
      setCurrentJobId(jobId);
      setInitialMessage(message);
      setInitialImageUrls(imageUrls);
      setInitialWorkdir(workdir);
      setInitialModelId(modelId);
      setInitialAgentType(type);
      setInitialAcpMode(acpMode);
      setInitialAcpThoughtLevel(acpThoughtLevel);
      setShowChat(true);
    } catch (err) {
      console.error('Failed to create job:', err);
      const msg = err instanceof Error ? err.message : String(err);
      alert(`Failed to start chat: ${msg}`);
    } finally {
      setIsInitializing(false);
    }
  }, [currentWorkspace]);

  const handleStartNewChat = useCallback(async (modelId: string, agentType: string, workdir?: string) => {
    setMissingJobNoticeId(null);
    setIsInitializing(true);
    try {
      const body: Record<string, unknown> = {
        modelId,
        agentType,
        mode: 'interactive',
      };
      if (workdir) body.workdir = workdir;
      body.workspaceId = currentWorkspace?.id ?? DEFAULT_WORKSPACE_ID;

      const response = await fetch('/api/v1/job/create', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!response.ok) {
        const errData = await response.json().catch(() => null);
        throw new Error(errData?.error || `HTTP ${response.status}`);
      }
      const data = await response.json();
      const jobId = data.jobId;

      updateUrlWithJobId(jobId);
      setCurrentJobId(jobId);
      setInitialMessage(null);
      setInitialImageUrls(undefined);
      setInitialWorkdir(workdir);
      setInitialModelId(modelId);
      setInitialAgentType(agentType);
      setInitialAcpMode(undefined);
      setShowChat(true);
    } catch (err) {
      console.error('Failed to create new chat:', err);
      const msg = err instanceof Error ? err.message : String(err);
      alert(`Failed to start new chat: ${msg}`);
    } finally {
      setIsInitializing(false);
    }
  }, [currentWorkspace]);

  const handleNewChat = useCallback(() => {
    setMissingJobNoticeId(null);
    // Go back to ChatPage (within workspace context)
    const url = new URL(window.location.href);
    url.searchParams.delete('jobId');
    url.searchParams.delete('sessionId');
    window.history.pushState({}, '', url.toString());
    setInitialMessage(null);
    setInitialImageUrls(undefined);
    setCurrentJobId(undefined);
    setInitialModelId(undefined);
    setInitialAgentType(undefined);
    setInitialAcpMode(undefined);
    setShowChat(false);
  }, []);

  // Shared Job-selection entry. Accepts an optional workspaceId so callers
  // that already know the Job's workspace (e.g. the home-page global job list,
  // which shows jobs across workspaces) can keep currentWorkspace + the URL
  // consistent without an extra round-trip. Chat-page callers that only list
  // jobs in the current workspace can keep passing just the jobId.
  const handleSelectJob = useCallback((jobId: string, jobWorkspaceId?: string) => {
    setMissingJobNoticeId(null);
    if (jobWorkspaceId && jobWorkspaceId !== currentWorkspace?.id) {
      // Resolve workspace metadata from the localStorage cache populated by
      // the bootstrap fetch; fall back to a stub so routing stays consistent
      // while the real record loads.
      const saved = localStorage.getItem(`workspace_${jobWorkspaceId}`);
      let ws: WorkspaceInfo | undefined;
      if (saved) {
        try { ws = JSON.parse(saved); } catch { /* ignore */ }
      }
      if (!ws) {
        ws = { id: jobWorkspaceId, title: '', description: '', workdir: '' };
      }
      setCurrentWorkspace(ws);
      setLastUsedWorkspaceId(ws.id);
      const url = new URL(window.location.href);
      url.searchParams.set('workspaceId', jobWorkspaceId);
      url.searchParams.delete('sessionId');
      url.searchParams.set('jobId', jobId);
      window.history.pushState({}, '', url.toString());
    } else {
      updateUrlWithJobId(jobId);
    }
    setCurrentJobId(jobId);
    setInitialMessage(null);
    setInitialImageUrls(undefined);
    setInitialModelId(undefined);
    setInitialAgentType(undefined);
    setInitialAcpMode(undefined);
    setInitialSessionId(undefined);
    setShowChat(true);
  }, [currentWorkspace]);

  const handleJobCreated = useCallback((jobId: string) => {
    setMissingJobNoticeId(null);
    updateUrlWithJobId(jobId, true);
    setCurrentJobId(jobId);
  }, []);

  // Bubble-up handler for /job/:id 404. Triggered when a stale URL points at
  // a Job that no longer exists (deleted from the Settings panel, manually
  // edited URL, etc). Without this the user lands on an empty chat with no
  // error indicator — see useJobChat for why the .catch path silently misses
  // 404 when res.json() succeeds on the {"code":-1,"msg":"job not found"}
  // body. We clear the jobId from URL + state and route back to the
  // workspace home so the next interaction starts from a clean slate.
  const handleJobNotFound = useCallback((staleJobId: string) => {
    console.warn(`[App] job not found, clearing stale jobId from URL: ${staleJobId}`);
    const url = new URL(window.location.href);
    url.searchParams.delete('jobId');
    url.searchParams.delete('sessionId');
    // replaceState (not pushState): the user already navigated to this URL,
    // so we shouldn't push a new history entry that they can "Back" into and
    // hit the same 404 again.
    window.history.replaceState({}, '', url.toString());
    setCurrentJobId(undefined);
    setInitialSessionId(undefined);
    setInitialMessage(null);
    setInitialImageUrls(undefined);
    setInitialModelId(undefined);
    setInitialAgentType(undefined);
    setInitialAcpMode(undefined);
    setShowChat(false);
    setMissingJobNoticeId(staleJobId);
  }, []);

  // Shared "switch to workspace + reuse latest empty Job / create a new one"
  // flow used by the chat-page `/ws` command and the chat-page Workspace tag
  // dropdown. Empty Job = sessionCount === 0 (an existing job becomes non-empty
  // as soon as the first message creates a session).
  //
  // New-job agent/model resolution uses workspace-level preferences.
  // Workdir comes from the target workspace.
  //
  // Returns the target jobId when we successfully land on a Job (reused or
  // created), or null on failure. On null the caller falls back to dropping
  // the user on the workspace home.
  const reuseOrCreateJobInWorkspace = useCallback(async (ws: WorkspaceInfo): Promise<string | null> => {
    // 1. Look for the latest empty Job in the target workspace.
    //    Filters:
    //      - excludeScheduled=true: skip scheduled-task-triggered Jobs; users
    //        don't want /ws to drop them into a cron-owned chat
    //      - mode === 'interactive': loop Jobs have their own lifecycle and
    //        often have sessionCount===0 for brief windows after Create but
    //        before Start populates sessions; reusing one would hijack it
    //      - sessionCount === 0: treat "no session created yet" as "no
    //        messages yet". This approximates an empty message list closely
    //        enough for the reuse-or-create flow.
    try {
      const res = await fetch(`/api/v1/job/list?workspaceId=${encodeURIComponent(ws.id)}&limit=50&excludeScheduled=true`);
      if (res.ok) {
        const data = await res.json();
        const jobs = (data?.jobs || []) as Array<{ id: string; sessionCount: number; updatedAt: number; mode?: string; scheduleId?: string }>;
        const empty = jobs
          .filter((j) => !j.scheduleId && (j.mode ?? 'interactive') === 'interactive' && (j.sessionCount || 0) === 0)
          .sort((a, b) => b.updatedAt - a.updatedAt);
        if (empty.length > 0) return empty[0].id;
      }
    } catch (err) {
      console.error('[reuseOrCreateJob] list failed:', err);
    }

    // 2. No empty Job — create one from workspace-level preferences.
    let agentType = '';
    let modelId = '';
    const acpMode = '';
    const acpThoughtLevel = '';
    const prefs = loadWorkspacePrefs(ws.id);
    if (prefs.defaultAgent) agentType = prefs.defaultAgent;
    if (prefs.defaultModel) modelId = prefs.defaultModel;
    if (!agentType) {
      console.error('[reuseOrCreateJob] no agent_type available; configure a workspace default');
      return null;
    }

    const body: Record<string, unknown> = {
      modelId,
      agentType,
      mode: 'interactive',
      workspaceId: ws.id,
    };
    if (ws.workdir) body.workdir = ws.workdir;
    if (acpMode) body.acpMode = acpMode;
    if (acpThoughtLevel) body.acpThoughtLevel = acpThoughtLevel;

    try {
      const createRes = await fetch('/api/v1/job/create', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      if (!createRes.ok) return null;
      const created = await createRes.json();
      return created?.jobId || null;
    } catch (err) {
      console.error('[reuseOrCreateJob] create failed:', err);
      return null;
    }
  }, []);

  // Public entry: switch the chat page to the target workspace, then reuse or
  // create an empty Job in it. Used by both the `/ws` slash command (via the
  // command-action bus) and the chat-page Workspace tag dropdown.
  const handleSwitchWorkspaceChat = useCallback(async (ws: WorkspaceInfo) => {
    setIsInitializing(true);
    // Hide the current JobChat immediately. Otherwise, during the await gap the
    // old chat stays mounted while workspaceId prop switches → confusing UI.
    setCurrentJobId(undefined);
    setShowChat(false);
    try {
      handleSelectWorkspace(ws);
      const jobId = await reuseOrCreateJobInWorkspace(ws);
      if (jobId) {
        updateUrlWithJobId(jobId);
        setCurrentJobId(jobId);
        setShowChat(true);
        setInitialMessage(null);
        setInitialImageUrls(undefined);
        setInitialModelId(undefined);
        setInitialAgentType(undefined);
        setInitialAcpMode(undefined);
        setInitialSessionId(undefined);
      } else {
        // Fallback: land on the workspace home (no Job).
        const url = new URL(window.location.href);
        url.searchParams.delete('jobId');
        url.searchParams.delete('sessionId');
        // Avoid pushing a duplicate entry: handleSelectWorkspace already
        // pushed the workspace URL.
        window.history.replaceState({}, '', url.toString());

        // Make the failure visible; otherwise the user sees the command toast
        // but doesn't understand why the chat didn't switch.
        try {
          alert('切换工作空间成功，但无法创建/复用 Job，已返回首页。请稍后重试或检查服务状态。');
        } catch { /* ignore */ }
      }
    } finally {
      setIsInitializing(false);
    }
  }, [handleSelectWorkspace, reuseOrCreateJobInWorkspace]);

  // Listen for slash-command actions dispatched from the SSE handler
  // (useJobChat) and translate them into the shared public entry functions:
  //   - switch_workspace → chat-page flow: switch to the workspace and reuse
  //     its latest empty Job (or create one) so the next message continues in
  //     that workspace's context.
  //   - bind_job        → switch to the given Job (and its workspace).
  //   - new_job         → immediately create a new Job in the current
  //     workspace inheriting the current Job's agent/model, and navigate to
  //     it.
  useEffect(() => {
    const notifyError = (msg: string, err?: unknown) => {
      if (err) console.error(msg, err);
      try { alert(msg); } catch { /* ignore */ }
    };
    const handler = async (e: Event) => {
      const action = (e as CustomEvent).detail as { type?: string; workspaceId?: string; jobId?: string } | null;
      if (!action?.type) return;
      try {
        if (action.type === 'switch_workspace' && action.workspaceId) {
          const res = await fetch('/api/v1/workspace/list');
          if (!res.ok) {
            notifyError(`切换工作空间失败：获取工作空间列表失败（HTTP ${res.status}）`);
            return;
          }
          const data = await res.json();
          const list = (data?.workspaces || []) as Array<{ id: string; title: string; description: string; workdir: string; color?: string }>;
          registerWorkspaceColors(list);
          const ws = list.find((w) => w.id === action.workspaceId);
          if (!ws) {
            notifyError(`切换工作空间失败：未找到工作空间 ${action.workspaceId}`);
            return;
          }
          await handleSwitchWorkspaceChat(ws);
        } else if (action.type === 'bind_job' && action.jobId) {
          // A cross-workspace `/job <raw-id>` will carry a workspaceId
          // different from the current one. Resolve the target workspace
          // BEFORE touching URL / state — if resolution fails, abort the
          // whole switch so URL + currentWorkspace + localStorage cache
          // stay in sync. (Previously the URL was updated unconditionally
          // which left the footer showing the old workspace name while
          // the URL pointed at a new one.)
          const crossWs = !!action.workspaceId && action.workspaceId !== currentWorkspace?.id;
          let targetWs: WorkspaceInfo | undefined;
          if (crossWs) {
            const saved = localStorage.getItem(`workspace_${action.workspaceId}`);
            if (saved) {
              try { targetWs = JSON.parse(saved); } catch { /* ignore */ }
            }
            if (!targetWs) {
              try {
                const res = await fetch('/api/v1/workspace/list');
                const data = await res.json();
                const list = (data?.workspaces || []) as WorkspaceInfo[];
                registerWorkspaceColors(list);
                targetWs = list.find((w) => w.id === action.workspaceId);
                if (targetWs) {
                  localStorage.setItem(`workspace_${targetWs.id}`, JSON.stringify(targetWs));
                }
              } catch { /* ignore */ }
            }
            if (!targetWs) {
              notifyError(`切换 Job 失败：无法解析目标工作空间 ${action.workspaceId}，已取消切换。`);
              return;
            }
            setCurrentWorkspace(targetWs);
            setLastUsedWorkspaceId(targetWs.id);
          }
          const url = new URL(window.location.href);
          url.searchParams.delete('sessionId');
          url.searchParams.set('jobId', action.jobId);
          if (action.workspaceId) {
            url.searchParams.set('workspaceId', action.workspaceId);
          }
          window.history.pushState({}, '', url.toString());
          setCurrentJobId(action.jobId);
          setShowChat(true);
          // Clear per-session initial bundle so JobChat does a fresh load.
          setInitialMessage(null);
          setInitialImageUrls(undefined);
          setInitialModelId(undefined);
          setInitialAgentType(undefined);
          setInitialAcpMode(undefined);
          setInitialSessionId(undefined);
        } else if (action.type === 'new_job') {
          // Immediately create a new Job in the current workspace, inheriting
          // agent/model/workdir from the current Job so the user can continue
          // doing the same kind of work in a clean conversation.
          const targetWsId = action.workspaceId || currentWorkspace?.id;
          if (!targetWsId) {
            notifyError('创建新对话失败：当前没有可用的工作空间上下文。');
            return;
          }
          // 1. Read the current Job so we can inherit agent/model/workdir.
          let inheritedAgent = '';
          let inheritedModel = '';
          let inheritedWorkdir = '';
          if (currentJobId) {
            try {
              const jobRes = await fetch(`/api/v1/job/${encodeURIComponent(currentJobId)}`);
              if (jobRes.ok) {
                const j = await jobRes.json();
                // The first session's modelId is denormalized onto
                // job.firstModelId. The Job payload has no agentType field,
                // so the agent falls back to workspace preferences below.
                if (j?.firstModelId) inheritedModel = j.firstModelId;
                if (j?.workdir) inheritedWorkdir = j.workdir;
              }
            } catch (err) {
              console.error('[command-action] new_job: read current job failed:', err);
            }
          }
          // 2. Fall back to workspace-level preferences if the current Job
          //    didn't surface an agent (e.g. /new from a fresh empty Job).
          if (!inheritedAgent) {
            const prefs = loadWorkspacePrefs(targetWsId);
            if (prefs.defaultAgent) inheritedAgent = prefs.defaultAgent;
            if (!inheritedModel && prefs.defaultModel) inheritedModel = prefs.defaultModel;
          }
          const inheritedAcpMode = '';
          const inheritedAcpThoughtLevel = '';
          if (!inheritedAgent) {
            notifyError('创建新对话失败：未配置默认 Agent（请在设置或工作空间偏好中选择默认 Agent）。');
            return;
          }
          // 3. Fall back workdir to the workspace workdir if the current Job
          //    didn't carry one.
          if (!inheritedWorkdir) {
            const saved = localStorage.getItem(`workspace_${targetWsId}`);
            if (saved) {
              try { inheritedWorkdir = JSON.parse(saved)?.workdir || ''; } catch { /* ignore */ }
            }
          }
          // 4. Create the Job.
          const body: Record<string, unknown> = {
            modelId: inheritedModel,
            agentType: inheritedAgent,
            mode: 'interactive',
            workspaceId: targetWsId,
          };
          if (inheritedWorkdir) body.workdir = inheritedWorkdir;
          if (inheritedAcpMode) body.acpMode = inheritedAcpMode;
          if (inheritedAcpThoughtLevel) body.acpThoughtLevel = inheritedAcpThoughtLevel;
          try {
            const createRes = await fetch('/api/v1/job/create', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify(body),
            });
            if (!createRes.ok) {
              const errData = await createRes.json().catch(() => null);
              notifyError(`创建新对话失败：${errData?.error || `HTTP ${createRes.status}`}`);
              return;
            }
            const created = await createRes.json();
            const newJobId = created?.jobId;
            if (!newJobId) return;
            // 5. Navigate to the new Job (stay on the chat page).
            updateUrlWithJobId(newJobId);
            setCurrentJobId(newJobId);
            setShowChat(true);
            setInitialMessage(null);
            setInitialImageUrls(undefined);
            setInitialModelId(undefined);
            setInitialAgentType(undefined);
            setInitialAcpMode(undefined);
            setInitialSessionId(undefined);
          } catch (err) {
            notifyError('创建新对话失败：网络错误。', err);
          }
        }
      } catch (err) {
        notifyError('命令执行失败：发生未预期的错误。', err);
      }
    };
    window.addEventListener('quartet:command-action', handler);
    return () => window.removeEventListener('quartet:command-action', handler);
  }, [handleSelectWorkspace, handleSwitchWorkspaceChat, currentWorkspace, currentJobId]);

  // Listen for workspace CRUD events from the Settings panel so the
  // currently-displayed workspace metadata (title / workdir in the chat tag)
  // updates without requiring a page reload. Delete events fall back to the
  // default workspace if the deleted one was currently active.
  useEffect(() => {
    const onUpdated = (e: Event) => {
      const ws = (e as CustomEvent).detail as WorkspaceInfo | null;
      if (!ws?.id) return;
      if (currentWorkspace?.id === ws.id) {
        setCurrentWorkspace(ws);
      }
    };
    // Reseed currentWorkspace after the active workspace is deleted. Prefer
    // the localStorage cache (fast, synchronous) but fall back to fetching
    // /workspace/list when the cache is missing — otherwise the UI stays
    // wedged on the deleted workspace's metadata.
    const fallbackToDefaultWorkspace = async () => {
      const applyWs = (ws: WorkspaceInfo) => {
        setCurrentWorkspace(ws);
        setLastUsedWorkspaceId(ws.id);
        const url = new URL(window.location.href);
        url.searchParams.set('workspaceId', ws.id);
        url.searchParams.delete('jobId');
        url.searchParams.delete('sessionId');
        window.history.replaceState({}, '', url.toString());
        setCurrentJobId(undefined);
        setShowChat(false);
      };
      const saved = localStorage.getItem(`workspace_${DEFAULT_WORKSPACE_ID}`);
      if (saved) {
        try {
          applyWs(JSON.parse(saved) as WorkspaceInfo);
          return;
        } catch { /* fall through to remote fetch */ }
      }
      try {
        const res = await fetch('/api/v1/workspace/list');
        if (!res.ok) return;
        const data = await res.json();
        const list: WorkspaceInfo[] = data?.workspaces || [];
        registerWorkspaceColors(list);
        const found = list.find((w) => w.id === DEFAULT_WORKSPACE_ID) ?? list[0];
        if (!found) return;
        for (const w of list) {
          localStorage.setItem(`workspace_${w.id}`, JSON.stringify(w));
        }
        applyWs(found);
      } catch (err) {
        console.error('[workspace-deleted] fallback fetch failed:', err);
      }
    };
    const onDeleted = (e: Event) => {
      const detail = (e as CustomEvent).detail as { id?: string } | null;
      const deletedId = detail?.id;
      if (!deletedId) return;
      if (currentWorkspace?.id === deletedId) {
        void fallbackToDefaultWorkspace();
      }
    };
    window.addEventListener('quartet:workspace-updated', onUpdated);
    window.addEventListener('quartet:workspace-deleted', onDeleted);
    return () => {
      window.removeEventListener('quartet:workspace-updated', onUpdated);
      window.removeEventListener('quartet:workspace-deleted', onDeleted);
    };
  }, [currentWorkspace]);

  const handleOpenSettings = useCallback(() => setShowSettings(true), []);
  const handleCloseSettings = useCallback(() => setShowSettings(false), []);
  const handleOpenStats = useCallback(() => {
    setShowGraph(false);
    setShowStats(true);
    updateUrlWithStats(true);
  }, []);
  const handleCloseStats = useCallback(() => {
    setShowStats(false);
    updateUrlWithStats(false);
  }, []);
  const handleOpenGraph = useCallback(() => {
    setMissingJobNoticeId(null);
    setShowChat(false);
    setCurrentJobId(undefined);
    setInitialSessionId(undefined);
    setShowStats(false);
    setShowGraph(true);
    updateUrlWithGraph(true);
  }, []);
  const handleCloseGraph = useCallback(() => {
    setShowGraph(false);
    updateUrlWithGraph(false);
  }, []);
  const handleGraphDirtyChange = useCallback((dirty: boolean) => {
    graphDirtyRef.current = dirty;
  }, []);
  // Jump into the Chat page for a freshly-started Graph run's bound Job. The
  // job already exists (the backend creates it on /graph/run/start), so we
  // only flip views + URL here.
  const handleGraphRunStarted = useCallback((jobId: string) => {
    setMissingJobNoticeId(null);
    setShowGraph(false);
    setShowStats(false);
    setInitialMessage(null);
    setInitialImageUrls(undefined);
    setInitialSessionId(undefined);
    updateUrlWithJobId(jobId);
    setCurrentJobId(jobId);
    setShowChat(true);
  }, []);
  // Jump from the Stats page's "By Workspace" rows back to the matching
  // workspace's home view. We resolve the workspace from the localStorage
  // cache populated by the boot fetch — falling back to a stub when it's
  // not cached yet so the URL still updates and the next page loads it.
  const handleJumpToWorkspace = useCallback((wsId: string) => {
    if (!wsId) return;
    let target: WorkspaceInfo | undefined;
    try {
      const cached = localStorage.getItem(`workspace_${wsId}`);
      if (cached) target = JSON.parse(cached);
    } catch { /* ignore */ }
    if (!target) {
      target = { id: wsId, title: '', description: '', workdir: '' };
    }
    handleSelectWorkspace(target);
    setCurrentJobId(undefined);
    setInitialSessionId(undefined);
    setInitialMessage(null);
    setInitialImageUrls(undefined);
    setInitialWorkdir(undefined);
    setInitialModelId(undefined);
    setInitialAgentType(undefined);
    setInitialAcpMode(undefined);
    setShowChat(false);
    handleCloseStats();
  }, [handleSelectWorkspace, handleCloseStats]);
  const handleSettingsChanged = useCallback(() => {
    setHomeRefreshKey((k) => k + 1);
  }, []);

  // Update document title based on current view
  useEffect(() => {
    if (showChat && currentJobId) {
      // JobChat will manage its own title
      return;
    }
    if (currentWorkspace) {
      document.title = currentWorkspace.title;
    } else {
      document.title = 'Workspace';
    }
  }, [showChat, currentJobId, currentWorkspace]);

  // Share mode: skip workspace check, go straight to job view
  if (isReadonly && currentJobId) {
    return (
      <ConnectionStatusProvider>
      <div className="app-layout">
        <div className="app-main">
          <JobChat
            key={currentJobId}
            existingJobId={currentJobId}
            initialSessionId={initialSessionId}
            shareToken={shareToken}
            isReadonly
          />
        </div>
      </div>
      </ConnectionStatusProvider>
    );
  }

  return (
    <ConnectionStatusProvider>
    <div className="app-layout">
      {missingJobNoticeId && (
        <div className="app-missing-job-notice" data-testid="job-missing-notice" role="status">
          <span>{t('app.jobMissingNotice', { jobId: missingJobNoticeId })}</span>
          <button
            type="button"
            className="app-missing-job-notice-close"
            data-testid="job-missing-notice-close"
            aria-label={t('common.close')}
            onClick={() => setMissingJobNoticeId(null)}
          >
            ×
          </button>
        </div>
      )}
      {showStats ? (
        <div className="app-main">
          <StatsPage
            onClose={handleCloseStats}
            currentWorkspaceId={currentWorkspace?.id}
            onJumpToWorkspace={handleJumpToWorkspace}
          />
        </div>
      ) : showGraph ? (
        <div className="app-main">
          <GraphWorkflowPage
            workspaceId={currentWorkspace?.id}
            workspaceTitle={currentWorkspace?.title}
            workspaceWorkdir={currentWorkspace?.workdir}
            onClose={handleCloseGraph}
            onDirtyChange={handleGraphDirtyChange}
            onRunStarted={handleGraphRunStarted}
          />
        </div>
      ) : showChat && currentJobId ? (
        <div className="app-main">
          <JobChat
            key={currentJobId}
            existingJobId={currentJobId}
            initialMessage={initialMessage}
            initialImageUrls={initialImageUrls}
            initialWorkdir={initialWorkdir}
            initialSessionId={initialSessionId}
            initialModelId={initialModelId}
            initialAgentType={initialAgentType}
            initialAcpMode={initialAcpMode}
            initialAcpThoughtLevel={initialAcpThoughtLevel}
            workspaceId={currentWorkspace?.id}
            shareToken={shareToken}
            isReadonly={isReadonly}
            onBack={handleNewChat}
            onJobCreated={handleJobCreated}
            onSelectJob={handleSelectJob}
            onOpenSettings={handleOpenSettings}
            onOpenStats={handleOpenStats}
            onOpenGraph={handleOpenGraph}
            onStartNewChat={handleStartNewChat}
            onSwitchWorkspaceChat={handleSwitchWorkspaceChat}
            onJobNotFound={handleJobNotFound}
          />
        </div>
      ) : (
        <div className="app-main">
          <ChatPage
            onStartChat={handleStartChat}
            isInitializing={isInitializing}
            refreshKey={homeRefreshKey}
            workspaceWorkdir={currentWorkspace?.workdir}
            workspaceId={currentWorkspace?.id}
            workspaceTitle={currentWorkspace?.title}
            onSelectWorkspace={handleSelectWorkspace}
            onSelectJob={handleSelectJob}
            onOpenSettings={handleOpenSettings}
            onOpenStats={handleOpenStats}
            onOpenGraph={handleOpenGraph}
          />
        </div>
      )}
      {showSettings && <Settings onClose={handleCloseSettings} onSettingsChanged={handleSettingsChanged} />}
    </div>
    </ConnectionStatusProvider>
  );
}

export default App;
