import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { useTranslation } from "react-i18next";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useAuthPrincipal } from "../auth";
import { fetchAvailableAgentList } from "../api/agents";
import { type AgentInfo } from "../types";
import {
  chatAPI,
  hideSystemReminder,
  type BusinessAgent,
  type ChatHit,
  type ChatMessage,
  type ChatSend,
  type ChatSession,
  type ChatSettings,
  type ChatSnapshot,
} from "../types/personalChat";
import { PersonalSchedules } from "./PersonalSchedules";
import { MessagePresetHistoryMenu } from "./MessagePresetHistoryMenu";
import { HomeNavigation } from "./HomeNavigation";
import { useIsMobile } from "../hooks/useIsMobile";
import { useACPThoughtLevels } from "../hooks/useACPThoughtLevels";
import { AgentUsageCard } from "./AgentUsageCard";
import { AgentIdentityIcon } from "./AgentIdentityIcon";
import { ConfigurationIcon } from "./ComposerIcons";
import { copyToClipboard } from "../utils/clipboard";
import { uploadChatAttachment } from "../hooks/usePendingAttachments";
import {
  buildServeFileUrl as fileUrl,
  buildFilePreviewUrl,
} from "../utils/file";
import "./MessageItem.css";
import "./ChatInput.css";
import "./PersonalChat.css";

interface Props {
  onOpenSettings?: () => void;
  onOpenGraph?: () => void;
  onOpenStats?: () => void;
  onSelectJob?: (jobId: string, workspaceId?: string) => void;
  [key: string]: unknown;
}
const newID = () =>
  globalThis.crypto?.randomUUID?.() ??
  `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;

function ChatIcon({ kind }: { kind: "send" | "attach" | "stop" | "menu" | "search" | "copy" | "reply" | "chevron" }) {
  return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {kind === "send" && <><path d="m5 12 7-7 7 7M12 5v14" /></>}
    {kind === "attach" && <path d="m21.4 11.6-8.9 8.9a6 6 0 0 1-8.5-8.5l9.2-9.2a4 4 0 0 1 5.7 5.7l-9.2 9.2a2 2 0 0 1-2.8-2.8l8.5-8.5" />}
    {kind === "stop" && <rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor" stroke="none" />}
    {kind === "menu" && <><path d="M4 6h16M4 12h16M4 18h16" /></>}
    {kind === "search" && <><circle cx="10.5" cy="10.5" r="6.5" /><path d="m16 16 4 4" /></>}
    {kind === "copy" && <><rect x="8" y="8" width="12" height="12" rx="2" /><path d="M16 8V4H4v12h4" /></>}
    {kind === "reply" && <path d="m9 5-6 6 6 6M3 11h10a7 7 0 0 1 7 7v2" />}
    {kind === "chevron" && <path d="m9 5 7 7-7 7" />}
  </svg>;
}

function MessageRow({
  message,
  agent,
  debug,
  session,
  onReply,
  onWithdraw,
}: {
  message: ChatMessage;
  agent?: BusinessAgent;
  debug: boolean;
  session?: ChatSession;
  onReply: () => void;
  onWithdraw: () => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [detail, setDetail] = useState<ChatMessage>();
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!open && !debug) return;
    const controller = new AbortController();
    chatAPI<{ message: ChatMessage }>(
      `/messages/${encodeURIComponent(message.id)}`,
      undefined,
      undefined,
      controller.signal,
    )
      .then((r) => {
        setDetail(r.message);
        setError("");
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [
    open,
    message.id,
    message.revision,
    message.role,
    message.rawContent,
    debug,
  ]);
  const rawContent = detail?.rawContent || message.rawContent;
  const body =
    debug && rawContent
      ? rawContent
      : debug
        ? (detail?.content ?? message.content)
        : hideSystemReminder(message.content);
  return (
    <article
      className={`personal-message personal-message-${message.role} ${message.role === "user" ? "user-message" : "assistant-message"}`}
      data-message-id={message.id}
    >
      <div className="personal-message-main">
        <div className={`personal-message-body ${message.role === "user" ? "message-bubble user-bubble" : "assistant-bubble"}`}>
        <div className={`personal-message-byline ${message.role === "user" ? "" : "assistant-bubble-header"}`}>
          {message.role !== "user" && <AgentIdentityIcon displayName={agent?.name || "Quartet"} className="personal-message-identity" />}
          <strong>
            {message.role === "notification"
              ? t("personal.notification")
              : message.role === "user"
                ? t("personal.you")
                : agent?.main && agent.name === "主 Agent"
                  ? t("personal.mainAgent")
                  : (agent?.name ?? message.agentId)}
          </strong>
          <time dateTime={new Date(message.createdAt).toISOString()}>
            {new Date(message.createdAt).toLocaleTimeString([], {
              hour: "2-digit",
              minute: "2-digit",
            })}
          </time>
          {message.source.platform && (
            <span className="personal-message-source">
              {[message.source.platform, message.source.accountId, message.source.chatId, message.source.senderId].filter(Boolean).join(" / ")}
            </span>
          )}
          {message.internal && <span className="personal-internal">{t("personal.internalMessage")}</span>}
          {message.state !== "completed" && (
            <span className={`personal-state personal-state-${message.state}`}>
              {t(`personal.states.${message.state}`, {
                defaultValue: message.state,
              })}
            </span>
          )}
        </div>
        {body ? (
          debug ? (
            <pre className="personal-raw">{body}</pre>
          ) : (
            <div className="personal-markdown">
              <ReactMarkdown
                remarkPlugins={[remarkGfm]}
                components={{
                  a: ({ href, children }) => (
                    <a
                      href={
                        href?.startsWith("/") && !href.startsWith("/api/")
                          ? buildFilePreviewUrl(href)
                          : href
                      }
                      target="_blank"
                      rel="noreferrer"
                    >
                      {children}
                    </a>
                  ),
                  img: ({ src, alt }) => (
                    <img
                      src={
                        typeof src === "string" &&
                        src.startsWith("/") &&
                        !src.startsWith("/api/")
                          ? fileUrl(src)
                          : src
                      }
                      alt={alt || ""}
                      loading="lazy"
                      style={{ maxWidth: "100%" }}
                    />
                  ),
                }}
              >
                {body}
              </ReactMarkdown>
            </div>
          )
        ) : (
          message.role === "assistant" && (
            <p className="personal-pending" role="status">
              {t(`personal.states.${message.state}`, {
                defaultValue: message.state,
              })}
            </p>
          )
        )}
        {message.error && (
          <pre className="personal-error" role="alert">
            {message.error}
          </pre>
        )}
        </div>
        <div className="personal-message-actions">
          <button title={t(copied ? "personal.copied" : "personal.copy")} aria-label={t(copied ? "personal.copied" : "personal.copy")} onClick={() => {
            void copyToClipboard(body).then(() => setCopied(true)).catch((e) => setError(String(e)));
          }}><ChatIcon kind="copy" /></button>
          {message.taskId && (
            <button title={t("personal.replyTask")} aria-label={t("personal.replyTask")} onClick={onReply}><ChatIcon kind="reply" /></button>
          )}
          {message.role === "user" && message.state === "queued" && (
            <button onClick={onWithdraw}>{t("personal.withdraw")}</button>
          )}
          {message.role === "assistant" && (
            <button aria-expanded={open} onClick={() => setOpen(!open)}>
              <ChatIcon kind="chevron" />{t("personal.details")}
              {message.toolCount > 0 ? ` (${message.toolCount})` : ""}{" "}
            </button>
          )}
        </div>
        {open && (
          <section className="personal-details">
            <span>
              {session?.harnessId} {session?.meta.model_id} ·{" "}
              {message.sessionId}
            </span>
            {detail?.thought && (
              <div>
                <h3>{t("personal.thought")}</h3>
                <pre>
                  {debug ? detail.thought : hideSystemReminder(detail.thought)}
                </pre>
              </div>
            )}
            {detail?.tools?.map((tool) => (
              <details key={tool.id}>
                <summary>
                  {tool.name} ·{" "}
                  {t(`personal.states.${tool.status}`, {
                    defaultValue: tool.status,
                  })}
                </summary>
                <pre>{debug ? tool.args : hideSystemReminder(tool.args)}</pre>
                <pre>
                  {debug ? tool.result : hideSystemReminder(tool.result)}
                </pre>
              </details>
            ))}
          </section>
        )}
        {error && <pre className="personal-error" role="alert">{error}</pre>}
      </div>
    </article>
  );
}

export function PersonalChat({
  onOpenSettings,
  onOpenGraph,
  onOpenStats,
  onSelectJob,
}: Props) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const isMobile = useIsMobile();
  const canExecute = !!principal?.permissions.includes("job.execute");
  const canManage = !!principal?.permissions.includes("agent.manage");
  const canConfigure = !!principal?.permissions.includes("config.write");
  const [snapshot, setSnapshot] = useState<ChatSnapshot>();
  const [older, setOlder] = useState<ChatMessage[]>([]);
  const [harnesses, setHarnesses] = useState<AgentInfo[]>([]);
  const [agentID, setAgentID] = useState("main");
  const [taskID, setTaskID] = useState("");
  const [harnessID, setHarnessID] = useState("");
  const [modelID, setModelID] = useState("");
  const [mode, setMode] = useState("");
  const [thought, setThought] = useState("");
  const [content, setContent] = useState("");
  const [independent, setIndependent] = useState(false);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const [panel, setPanel] = useState<"agents" | "history" | "settings" | "schedules" | "">(
    "",
  );
  const [editingAgent, setEditingAgent] = useState<BusinessAgent>();
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<ChatHit[]>([]);
  const [hitsTotal, setHitsTotal] = useState(0);
  const [searchTools, setSearchTools] = useState(false);
  const [searchAgent, setSearchAgent] = useState("");
  const [searchAfter, setSearchAfter] = useState("");
  const [searchBefore, setSearchBefore] = useState("");
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState(false);
  const searchVersion = useRef(0);
  const committedSearch = useRef("");
  const [history, setHistory] = useState<ChatMessage[]>([]);
  const [mention, setMention] = useState(false);
  const [files, setFiles] = useState<{ path: string; name: string }[]>([]);
  const [uploading, setUploading] = useState(false);
  const retry = useRef<{ signature: string; request: ChatSend } | undefined>(
    undefined,
  );
  const scroller = useRef<HTMLDivElement>(null);
  const viewport = useRef({ width: 0, height: 0 });
  const userScrollUntil = useRef(0);
  const follow = useRef(true);
  const viewerID = useRef(newID());
  const fileInput = useRef<HTMLInputElement>(null);
  const editor = useRef<HTMLTextAreaElement>(null);
  const refreshRef = useRef<() => void>(() => undefined);
  const [olderCursor, setOlderCursor] = useState<number>();
  const [hasOlder, setHasOlder] = useState(false);
  const alive = useRef(true);
  const lastSnapshot = useRef<{ seq: number; epoch: string }>({
    seq: 0,
    epoch: "",
  });
  const refreshVersion = useRef(0);
  const windowStart = useRef(0);
  const loadingOlder = useRef(false);
  useEffect(() => {
    const element = scroller.current;
    if (!element) return;
    const resize = new ResizeObserver(() => {
      viewport.current = { width: element.clientWidth, height: element.clientHeight };
      if (follow.current) element.scrollTop = element.scrollHeight;
    });
    resize.observe(element);
    return () => resize.disconnect();
  }, []);
  const refresh = useCallback(async () => {
    const version = ++refreshVersion.current;
    try {
      const value = await chatAPI<ChatSnapshot>(
        `?since=${lastSnapshot.current.seq}&windowStart=${windowStart.current}&taskId=${encodeURIComponent(taskID || `default-${agentID}`)}`,
      );
      if (!alive.current || version !== refreshVersion.current) return;
      const previous = lastSnapshot.current;
      if (value.epoch === previous.epoch && value.seq < previous.seq) return;
      const reset = value.epoch !== previous.epoch && value.seq < previous.seq;
      if (reset) windowStart.current = 0;
      lastSnapshot.current = { seq: value.seq, epoch: value.epoch };
      setOlder((old) => {
        const rows = new Map<string, ChatMessage>();
        if (!reset) for (const m of old) rows.set(m.id, m);
        for (const m of value.messages) {
          const prior = rows.get(m.id);
          if (!prior || prior.revision <= m.revision) rows.set(m.id, m);
        }
        return [...rows.values()];
      });
      for (const m of value.messages)
        if (!windowStart.current || m.order < windowStart.current)
          windowStart.current = m.order;
      setSnapshot(value);
      setOlderCursor((old) => (reset ? value.before : (old ?? value.before)));
      setHasOlder((old) =>
        reset || olderCursor === undefined ? value.hasMore : old,
      );
    } catch (e) {
      if (alive.current && version === refreshVersion.current)
        setError(String(e));
    }
  }, [olderCursor, taskID, agentID]);
  useEffect(() => {
    refreshRef.current = () => {
      void refresh();
    };
  }, [refresh]);
  useEffect(() => {
    alive.current = true;
    let events: EventSource | undefined;
    let pending: ReturnType<typeof setTimeout> | undefined;
    const sync = () => {
      if (pending) return;
      pending = setTimeout(() => {
        pending = undefined;
        refreshRef.current();
      }, 100);
    };
    const connect = () => {
      events?.close();
      events = undefined;
      if (document.visibilityState === "hidden") return;
      refreshRef.current();
      events = new EventSource(
        `/api/v1/chat/events?viewerId=${encodeURIComponent(viewerID.current)}&taskId=${encodeURIComponent(taskID || `default-${agentID}`)}`,
      );
      events.onmessage = sync;
      events.onerror = () => {
        sync();
      };
    };
    connect();
    document.addEventListener("visibilitychange", connect);
    const controller = new AbortController();
    const loadHarnesses = () => {
      void fetchAvailableAgentList()
        .then((result) => {
          if (alive.current) setHarnesses(result.agents);
        })
        .catch((e) => {
          if (!controller.signal.aborted) setError(String(e));
        });
    };
    loadHarnesses();
    const catalogTimer = setInterval(loadHarnesses, 30000);
    return () => {
      alive.current = false;
      events?.close();
      clearTimeout(pending);
      controller.abort();
      clearInterval(catalogTimer);
      document.removeEventListener("visibilitychange", connect);
    };
  }, [taskID, agentID]);
  const selectedTask = snapshot?.tasks.find(
    (task) => task.id === (taskID || `default-${agentID}`),
  );
  const selectedSession = snapshot?.sessions.find(
    (session) => session.id === selectedTask?.sessionId,
  );
  const effectiveHarness =
    harnessID ||
    selectedSession?.harnessId ||
    snapshot?.agents.find((agent) => agent.id === agentID)?.harnessId ||
    snapshot?.defaultHarnessId ||
    harnesses[0]?.agent_id ||
    "";
  const harness = harnesses.find(
    (item) =>
      item.agent_id === effectiveHarness || item.type === effectiveHarness,
  );
  const effectiveModel =
    modelID ||
    (selectedSession?.harnessId === effectiveHarness
      ? selectedSession.meta.model_id
      : undefined) ||
    harness?.models?.currentModelId ||
    "";
  const linkThought =
    !!harness &&
    !!effectiveModel &&
    effectiveModel !== harness.models?.currentModelId;
  const thoughtLink = useACPThoughtLevels(
    effectiveHarness,
    effectiveModel,
    linkThought,
    harness?.thoughtLevels ?? null,
  );
  const thoughtState = linkThought
    ? thoughtLink.error
      ? null
      : thoughtLink.state
    : harness?.thoughtLevels;
  const storedThought =
    selectedSession?.harnessId === effectiveHarness &&
    selectedSession.meta.model_id === effectiveModel
      ? selectedSession.meta.acp_thought_level
      : undefined;
  const requestedThought =
    thought || storedThought || thoughtState?.currentThoughtLevelId;
  const effectiveThought = thoughtState?.availableThoughtLevels.some(
    (level) => level.id === requestedThought,
  )
    ? requestedThought
    : undefined;
  const messages = useMemo(() => {
    const rows = new Map<string, ChatMessage>();
    for (const m of older) rows.set(m.id, m);
    for (const m of snapshot?.messages ?? []) rows.set(m.id, m);
    return [...rows.values()]
      .filter((m) => !m.internal || snapshot?.settings.debug)
      .sort((a, b) => a.order - b.order);
  }, [older, snapshot]);
  useEffect(() => {
    if (follow.current)
      scroller.current?.scrollTo({ top: scroller.current.scrollHeight });
  }, [snapshot?.seq]);
  const activeAgents = snapshot?.agents.filter((a) => !a.deleted) ?? [];
  const running = snapshot?.runs.some(
    (run) =>
      run.taskId === selectedTask?.id &&
      ["running", "stopping"].includes(run.state),
  );
  const chooseAgent = (id: string) => {
    setAgentID(id);
    setTaskID("");
    setHarnessID("");
    setModelID("");
    setMode("");
    setThought("");
  };
  const reply = (message: ChatMessage) => {
    chooseAgent(message.agentId);
    setTaskID(message.taskId);
    setIndependent(false);
  };
  const action = async (taskId: string, verb: string, runId?: string) => {
    try {
      await chatAPI("/tasks/control", { taskId, action: verb, runId });
      await refresh();
    } catch (e) {
      setError(String(e));
    }
  };
  const send = async (stopAndSend = false) => {
    if (
      sending ||
      !canExecute ||
      (!content.trim() && files.length === 0) ||
      (linkThought && thoughtLink.loading)
    )
      return;
    const explicitMention = activeAgents.find((a) => content.startsWith(`@${a.name} `));
    const targetAgent = explicitMention?.id || agentID;
    const text =
      content +
      files.map((file) => `\n\n[${file.name}](${file.path})`).join("");
    const payload = {
      content: text,
      agentId: targetAgent,
      taskId: targetAgent === agentID ? taskID || undefined : undefined,
      harnessId: targetAgent === agentID ? effectiveHarness || undefined : explicitMention?.harnessId || snapshot?.defaultHarnessId || undefined,
      modelId: targetAgent === agentID ? effectiveModel || undefined : undefined,
      mode: targetAgent !== agentID ? undefined :
        mode ||
        (selectedSession?.harnessId === effectiveHarness
          ? selectedSession.meta.acp_mode
          : undefined) ||
        harness?.modes?.currentModeId ||
        undefined,
      thoughtLevel: targetAgent === agentID ? effectiveThought : undefined,
      independent,
      stopAndSend,
    };
    const signature = JSON.stringify(payload);
    const request =
      retry.current?.signature === signature
        ? retry.current.request
        : { ...payload, clientMessageId: newID() };
    retry.current = { signature, request };
    setSending(true);
    setError("");
    try {
      await chatAPI("/messages", request);
      retry.current = undefined;
      setContent("");
      setFiles([]);
      setIndependent(false);
      follow.current = true;
      await refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setSending(false);
    }
  };
  const loadOlder = async () => {
    if (loadingOlder.current) return;
    loadingOlder.current = true;
    try {
      const element = scroller.current;
      const oldHeight = element?.scrollHeight ?? 0;
      const page = await chatAPI<ChatSnapshot>(
        `?before=${olderCursor ?? snapshot?.before ?? 0}`,
      );
      setOlder((old) => [...page.messages, ...old]);
      for (const m of page.messages)
        if (!windowStart.current || m.order < windowStart.current)
          windowStart.current = m.order;
      setOlderCursor(page.before);
      setHasOlder(page.hasMore);
      requestAnimationFrame(() => {
        if (element) element.scrollTop += element.scrollHeight - oldHeight;
      });
    } catch (e) {
      setError(String(e));
    } finally {
      loadingOlder.current = false;
    }
  };
  const saveAgent = async (event: FormEvent) => {
    event.preventDefault();
    if (!editingAgent) return;
    try {
      await chatAPI("/agents", editingAgent);
      setEditingAgent(undefined);
      setPanel("");
      await refresh();
    } catch (e) {
      setError(String(e));
    }
  };
  const saveSettings = async (settings: ChatSettings) => {
    await chatAPI("/settings", settings, "PUT");
    await refresh();
  };
  const search = async (event: FormEvent) => {
    event.preventDefault();
    const version = ++searchVersion.current;
    setSearching(true);
    const params = new URLSearchParams({ query, agentId: searchAgent, tools: String(searchTools) });
    if (searchAfter) params.set("after", String(new Date(searchAfter).getTime()));
    if (searchBefore) params.set("before", String(new Date(searchBefore).getTime()));
    try {
      const r = await chatAPI<{ hits: ChatHit[]; total: number }>(`/search?${params}`);
      if (version !== searchVersion.current) return;
      committedSearch.current = params.toString();
      setHits(r.hits);
      setHitsTotal(r.total);
      setHistory([]);
      setSearched(true);
    } catch (e) { if (version === searchVersion.current) setError(String(e)); }
    finally { if (version === searchVersion.current) setSearching(false); }
  };
  const attach = async (selected: FileList | null) => {
    if (!selected) return;
    setUploading(true);
    try {
      const uploaded: { path: string; name: string }[] = [];
      for (const file of selected)
        uploaded.push(await uploadChatAttachment(file));
      setFiles((old) => [...old, ...uploaded]);
    } catch (e) {
      setError(String(e));
    } finally {
      setUploading(false);
    }
  };
  return (
    <div className="personal-chat" data-testid="personal-chat">
      <HomeNavigation
        workdir={snapshot?.workdir}
        onHome={() => {
          setPanel("");
          setTaskID("");
        }}
        onOpenSettings={onOpenSettings}
        onOpenGraph={onOpenGraph}
        onOpenStats={onOpenStats}
        pageTitle={t("personal.title")}
        pageActions={
          <button
            className="personal-plain-button"
            aria-label={t("personal.history")}
            title={t("personal.history")}
            onClick={() => setPanel(panel === "history" ? "" : "history")}
          >
            <ChatIcon kind="search" /><span>{t("personal.history")}</span>
          </button>
        }
      />
      <div className="personal-layout">
        {sidebarOpen && <button className="personal-sidebar-scrim" aria-label={t("common.close")} onClick={() => setSidebarOpen(false)} />}
        <aside className={`personal-sidebar ${sidebarOpen ? "is-open" : ""}`} aria-label={t("personal.agents")}>
          <div className="personal-sidebar-mobile-heading"><strong>{t("personal.agents")}</strong><button onClick={() => setSidebarOpen(false)} aria-label={t("common.close")}>×</button></div>
          <div className="personal-sidebar-heading">
            <h2>{t("personal.agents")}</h2>
            {canManage && (
              <button
                aria-label={t("personal.addAgent")}
                onClick={() => {
                  setPanel("agents");
                  setEditingAgent({
                    id: "",
                    name: "",
                    systemPrompt: "",
                    main: false,
                    revision: 0,
                  });
                }}
              >
                +
              </button>
            )}
          </div>
          {activeAgents.map((agent) => (
            <div
              key={agent.id}
              className={`personal-agent-row ${agentID === agent.id && !taskID ? "selected" : ""}`}
            >
              <button
                className="personal-agent-name"
                onClick={() => { chooseAgent(agent.id); setSidebarOpen(false); }}
              >
                <span className="personal-agent-mark">
                  {agent.main ? "Q" : agent.name.slice(0, 1)}
                </span>
                <span>
                  {agent.main && agent.name === "主 Agent"
                    ? t("personal.mainAgent")
                    : agent.name}
                </span>
              </button>
              {canManage && (
                <button
                  className="personal-agent-edit"
                  aria-label={t("personal.editAgent")}
                  onClick={() => {
                    setEditingAgent(agent);
                    setPanel("agents");
                  }}
                >
                  …
                </button>
              )}
            </div>
          ))}
          <h2>{t("personal.tasks")}</h2>
          {snapshot?.tasks
            .filter((task) => !task.id.startsWith("default-"))
            .slice(-30)
            .reverse()
            .map((task) => (
              <div className="personal-task-row" key={task.id}>
                <button
                  className={taskID === task.id ? "selected" : ""}
                  onClick={() => {
                    chooseAgent(task.agentId);
                    setTaskID(task.id);
                    setIndependent(false);
                    setSidebarOpen(false);
                  }}
                >
                  {task.title}
                </button>
                {task.queue.length > 0 && <span>{task.queue.length}</span>}
                {task.paused && canExecute && (
                  <button onClick={() => void action(task.id, "resume")}>
                    {t("personal.resume")}
                  </button>
                )}
              </div>
            ))}
          <div className="personal-sidebar-bottom">
            {principal?.permissions.includes("schedule.read") && <button onClick={() => { setPanel("schedules"); setSidebarOpen(false); }}>{t("personal.panels.schedules")}</button>}
            <button
              onClick={() => setPanel(panel === "settings" ? "" : "settings")}
            >
              {t("personal.chatSettings")}
            </button>
          </div>
        </aside>
        <main className="personal-conversation">
          <div className="personal-room-header">
            <button className="personal-mobile-menu" aria-label={t("personal.agentsAndTasks")} onClick={() => setSidebarOpen(true)}><ChatIcon kind="menu" /></button>
            <div className="personal-room-title"><h1>{t("personal.groupChat")}</h1><span>{t("personal.agentCount", { count: activeAgents.length })}</span></div>
            <button className="personal-room-settings" onClick={() => setPanel(panel === "settings" ? "" : "settings")}>{t("personal.chatSettings")}</button>
          </div>
          <div
            ref={scroller}
            className="personal-timeline"
            tabIndex={0}
            onWheel={(event) => {
              userScrollUntil.current = Date.now() + 1200;
              if (event.deltaY < 0) follow.current = false;
            }}
            onTouchMove={() => {
              userScrollUntil.current = Date.now() + 1200;
              follow.current = false;
            }}
            onPointerDown={() => {
              userScrollUntil.current = Date.now() + 1200;
              follow.current = false;
            }}
            onKeyDown={(event) => {
              if (["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(event.key)) {
                userScrollUntil.current = Date.now() + 1200;
                if (["ArrowUp", "PageUp", "Home"].includes(event.key)) follow.current = false;
              }
            }}
            onLoadCapture={() => {
              if (follow.current && scroller.current) scroller.current.scrollTop = scroller.current.scrollHeight;
            }}
            onScroll={() => {
              const e = scroller.current;
              if (e) {
                if (viewport.current.width !== e.clientWidth || viewport.current.height !== e.clientHeight) {
                  viewport.current = { width: e.clientWidth, height: e.clientHeight };
                  if (follow.current) e.scrollTop = e.scrollHeight;
                  return;
                }
                // Layout, browser scroll restoration and composer resizing
                // also emit scroll events. Only a user's browsing gesture
                // changes whether new messages should follow the bottom.
                if (Date.now() <= userScrollUntil.current) follow.current =
                  e.scrollHeight - e.scrollTop - e.clientHeight < 100;
              }
            }}
          >
            {!snapshot && <p role="status">{t("personal.loading")}</p>}
            {hasOlder && (
              <button
                className="personal-load-older"
                onClick={() => void loadOlder()}
              >
                {t("personal.loadOlder")}
              </button>
            )}
            {snapshot && messages.length === 0 && (
              <div className="personal-empty">
                <span className="personal-wordmark">Q</span>
                <h2>{t("personal.emptyTitle")}</h2>
                <p>{t("personal.emptyDescription")}</p>
              </div>
            )}
            {snapshot && (snapshot.warnings.length > 0 || snapshot.runs.some((r) => r.deliveryError)) && <div className="personal-diagnostics" aria-label={t("personal.diagnostics")}>            {snapshot?.warnings.map((warning, i) => (
              <pre key={i} className="personal-error">
                {warning}
              </pre>
            ))}
            {snapshot?.runs
              .filter((run) => run.deliveryError)
              .map((run) => (
                <div key={run.id}>
                  <pre className="personal-error">{run.deliveryError}</pre>
                  <button
                    onClick={() =>
                      void action(run.taskId, "retry-delivery", run.id)
                    }
                  >
                    {t("personal.retryDelivery")}
                  </button>
                </div>
              ))}
</div>}
            {messages.map((message) => (
              <MessageRow
                key={message.id}
                message={message}
                agent={snapshot?.agents.find((a) => a.id === message.agentId)}
                session={snapshot?.sessions.find(
                  (s) => s.id === message.sessionId,
                )}
                debug={!!snapshot?.settings.debug}
                onReply={() => reply(message)}
                onWithdraw={() =>
                  void action(message.taskId, "withdraw", message.runId)
                }
              />
            ))}
          </div>
          <div className="personal-composer-area">
            {thoughtLink.error && (
              <pre className="personal-error">{thoughtLink.error}</pre>
            )}
            {error && (
              <div role="alert" className="personal-error-banner">
                <pre>{error}</pre>
                <button
                  aria-label={t("common.close")}
                  onClick={() => setError("")}
                >
                  ×
                </button>
              </div>
            )}
            {taskID && (
              <div className="personal-reply-context">
                <span>
                  {t("personal.replyingTo", {
                    task: selectedTask?.title ?? taskID,
                  })}
                </span>
                <button
                  onClick={() => setTaskID("")}
                  aria-label={t("common.close")}
                >
                  ×
                </button>
              </div>
            )}
            {selectedTask?.awaitingInput && (
              <div className="personal-reply-context">
                <span>
                  {t("personal.awaitingInput")} {selectedTask.waitReason}
                </span>
              </div>
            )}
            {selectedTask?.paused && (
              <div className="personal-reply-context">
                <span>{t("personal.queuePaused")}</span>
                <button onClick={() => void action(selectedTask.id, "resume")}>
                  {t("personal.resume")}
                </button>
              </div>
            )}
            <form
              className="personal-composer chat-input-wrapper"
              onSubmit={(e) => {
                e.preventDefault();
                void send();
              }}
            >
              {mention && (
                <div className="personal-mentions model-dropdown" role="listbox" aria-label={t("personal.agent")}>
                  {activeAgents.filter((a) => a.name.toLowerCase().includes(content.match(/@([^\s]*)$/)?.[1]?.toLowerCase() || "")).map((a) => (
                    <button
                      type="button"
                      role="option"
                      aria-selected={agentID === a.id}
                      key={a.id}
                      onClick={() => {
                        chooseAgent(a.id);
                        setContent((old) =>
                          old.replace(/@[^\s]*$/, `@${a.name} `),
                        );
                        setMention(false);
                        editor.current?.focus();
                      }}
                    >
                      @{" "}
                      {a.main && a.name === "主 Agent"
                        ? t("personal.mainAgent")
                        : a.name}
                    </button>
                  ))}
                </div>
              )}
              <textarea
                ref={editor}
                rows={2}
                aria-label={t("personal.message")}
                placeholder={t("personal.placeholder")}
                value={content}
                disabled={!canExecute}
                onChange={(e) => {
                  setContent(e.target.value);
                  e.target.style.height = "auto";
                  e.target.style.height = `${Math.min(180, Math.max(64, e.target.scrollHeight))}px`;
                  setMention(/(?:^|\s)@[^\s]*$/.test(e.target.value));
                }}
                onKeyDown={(e) => {
                  if (e.key === "Escape") { setMention(false); return; }
                  if (mention && e.key === "Enter" && !e.nativeEvent.isComposing) {
                    const term = content.match(/@([^\s]*)$/)?.[1]?.toLowerCase() || "";
                    const match = activeAgents.find((a) => a.name.toLowerCase().includes(term));
                    if (match) { e.preventDefault(); chooseAgent(match.id); setContent((old) => old.replace(/@[^\s]*$/, `@${match.name} `)); setMention(false); return; }
                  }
                  if (
                    e.key === "Enter" &&
                    !e.shiftKey &&
                    !e.nativeEvent.isComposing
                  ) {
                    e.preventDefault();
                    void send();
                  }
                }}
              />
              {files.map((file) => (
                <span className="personal-file" key={file.path}>
                  {file.name}
                  <button
                    type="button"
                    aria-label={t("personal.removeFile")}
                    onClick={() =>
                      setFiles((old) => old.filter((f) => f.path !== file.path))
                    }
                  >
                    ×
                  </button>
                </span>
              ))}
              <div className="personal-composer-controls chat-input-footer">
                <div className="personal-runtime-controls">
                  <label className="personal-agent-control model-tag" title={t("personal.agent")}>
                    <span aria-hidden="true">@</span>
                    <select
                      aria-label={t("personal.agent")}
                      value={agentID}
                      onChange={(e) => chooseAgent(e.target.value)}
                    >
                      {activeAgents.map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.main && a.name === "主 Agent"
                            ? t("personal.mainAgent")
                            : a.name}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="model-tag personal-harness-control" title={t("personal.harness")}>
                    <ConfigurationIcon kind="model" />
                    <select
                      aria-label={t("personal.harness")}
                      value={effectiveHarness}
                      onChange={(e) => {
                        setHarnessID(e.target.value);
                        setModelID("");
                        setMode("");
                        setThought("");
                      }}
                    >
                      {!harnesses.length && (
                        <option value="">{t("personal.noHarness")}</option>
                      )}
                      {harnesses.map((h) => (
                        <option key={h.agent_id} value={h.agent_id}>
                          {h.display_name}
                        </option>
                      ))}
                    </select>
                  </label>
                  {!!harness?.models?.availableModels.length && (
                    <label className="model-tag personal-model-control" title={t("personal.model")}>
                      <ConfigurationIcon kind="model" />
                      <select
                        aria-label={t("personal.model")}
                        value={
                          modelID ||
                          (selectedSession?.harnessId === effectiveHarness
                            ? selectedSession.meta.model_id
                            : "") ||
                          harness.models.currentModelId
                        }
                        onChange={(e) => {
                          setModelID(e.target.value);
                          setThought("");
                        }}
                      >
                        {harness.models.availableModels.map((m) => (
                          <option key={m.modelId} value={m.modelId}>
                            {m.name}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                  {!!harness?.modes?.availableModes.length && (
                    <label className="model-tag personal-mode-control" title={t("personal.mode")}>
                      <ConfigurationIcon kind="mode" />
                      <select
                        aria-label={t("personal.mode")}
                        value={
                          mode ||
                          (selectedSession?.harnessId === effectiveHarness
                            ? selectedSession.meta.acp_mode
                            : undefined) ||
                          harness.modes.currentModeId
                        }
                        onChange={(e) => setMode(e.target.value)}
                      >
                        {harness.modes.availableModes.map((m) => (
                          <option key={m.id} value={m.id}>
                            {m.name}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                  {!!thoughtState?.availableThoughtLevels.length && (
                    <label className="model-tag personal-thought-control" title={t("personal.thoughtLevel")}>
                      <ConfigurationIcon kind="thought" />
                      <select
                        aria-label={t("personal.thoughtLevel")}
                        disabled={linkThought && thoughtLink.loading}
                        value={effectiveThought || ""}
                        onChange={(e) => setThought(e.target.value)}
                      >
                        {thoughtState.availableThoughtLevels.map((m) => (
                          <option key={m.id} value={m.id}>
                            {m.name}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                </div>
                <div className="personal-send-controls">
                  <MessagePresetHistoryMenu
                    disabled={!canExecute}
                    loadPresets={!!principal?.permissions.includes("config.read")}
                    isMobile={isMobile}
                    currentInput={content}
                    historyItems={messages.filter((m) => m.role === "user" && !m.internal && m.state !== "withdrawn").slice(-30).reverse().map((m) => ({ id: m.id, ts: m.createdAt, content: hideSystemReminder(m.content) }))}
                    onApplyPreset={(text, mode) => { setContent((old) => mode === "append" && old ? `${old}\n\n${text}` : text); editor.current?.focus(); }}
                    onApplyHistory={(item) => { setContent(item.content); editor.current?.focus(); }}
                  />
                  <input
                    type="file"
                    multiple
                    ref={fileInput}
                    hidden
                    onChange={(e) => void attach(e.target.files)}
                  />
                  <button
                    className="chat-btn upload-btn"
                    type="button"
                    title={t("personal.attach")}
                    aria-label={t("personal.attach")}
                    disabled={uploading || !canExecute}
                    onClick={() => fileInput.current?.click()}
                  >
                    {uploading ? "…" : <ChatIcon kind="attach" />}
                  </button>
                  <label className="personal-independent model-tag" title={t("personal.newTaskDescription")}>
                    <input
                      type="checkbox"
                      checked={independent}
                      onChange={(e) => setIndependent(e.target.checked)}
                    />
                    {t("personal.newTask")}
                  </label>
                  {running && (
                    <button
                      className="personal-stop chat-btn stop-btn"
                      type="button"
                      title={content.trim() ? t("personal.stopAndSend") : t("personal.stop")}
                      aria-label={content.trim() ? t("personal.stopAndSend") : t("personal.stop")}
                      onClick={() =>
                        content.trim()
                          ? void send(true)
                          : void action(selectedTask!.id, "stop")
                      }
                    >
                      <ChatIcon kind="stop" />{content.trim() && <span>{t("personal.stopAndSend")}</span>}
                    </button>
                  )}
                  <button
                    className="personal-send chat-btn send-btn"
                    aria-label={sending ? t("personal.sending") : running ? t("personal.queueSend") : t("personal.send")}
                    title={running ? t("personal.queueSend") : t("personal.send")}
                    type="submit"
                    disabled={
                      !canExecute ||
                      sending ||
                      (!content.trim() && files.length === 0) ||
                      uploading ||
                      (linkThought && thoughtLink.loading)
                    }
                  >
                    {sending ? <span className="user-send-spinner" /> : <ChatIcon kind="send" />}
                  </button>
                </div>
              </div>
            </form>
            <div className="personal-composer-meta"><span>{t("personal.sendHint")}</span>
            {effectiveHarness && (
              <AgentUsageCard
                agentType={effectiveHarness}
                displayName={harness?.display_name}
              />
            )}</div>
          </div>
        </main>
        {panel && (
          <aside className="personal-panel" aria-label={t(`personal.panels.${panel}`)}>
            <header>
              <h2>{t(`personal.panels.${panel}`)}</h2>
              <button
                aria-label={t("common.close")}
                onClick={() => setPanel("")}
              >
                ×
              </button>
            </header>
            {panel === "schedules" && <PersonalSchedules onSelectJob={onSelectJob} />}
            {panel === "settings" && snapshot && (
              <PersonalChatPreferences
                settings={snapshot.settings}
                disabled={!canConfigure}
                onSave={saveSettings}
              />
            )}
            {panel === "agents" && editingAgent && (
              <form
                onSubmit={(e) => void saveAgent(e)}
                className="personal-agent-form"
              >
                <label>
                  {t("personal.name")}
                  <input
                    value={editingAgent.name}
                    onChange={(e) =>
                      setEditingAgent({ ...editingAgent, name: e.target.value })
                    }
                    required
                  />
                </label>
                <label>
                  {t("personal.systemPrompt")}
                  <textarea
                    value={editingAgent.systemPrompt}
                    onChange={(e) =>
                      setEditingAgent({
                        ...editingAgent,
                        systemPrompt: e.target.value,
                      })
                    }
                    rows={12}
                  />
                </label>
                <label>
                  {t("personal.defaultHarness")}
                  <select
                    aria-label={t("personal.defaultHarness")}
                    value={editingAgent.harnessId || ""}
                    onChange={(e) =>
                      setEditingAgent({
                        ...editingAgent,
                        harnessId: e.target.value,
                      })
                    }
                  >
                    <option value="">{t("personal.systemDefault")}</option>
                    {harnesses.map((h) => (
                      <option key={h.agent_id} value={h.agent_id}>
                        {h.display_name}
                      </option>
                    ))}
                  </select>
                </label>
                <button className="personal-primary" type="submit">{t("personal.save")}</button>
                {!editingAgent.main && editingAgent.id && (
                  <button
                    type="button"
                    onClick={() => {
                      void chatAPI("/agents", {
                        ...editingAgent,
                        deleted: true,
                      })
                        .then(() => {
                          setEditingAgent(undefined);
                          setPanel("");
                          void refresh();
                        })
                        .catch((e) => setError(String(e)));
                    }}
                  >
                    {t("personal.deleteAgent")}
                  </button>
                )}
              </form>
            )}
            {panel === "history" && (
              <div className="personal-history">
                <form onSubmit={(e) => void search(e)}>
                  <label>
                    {t("personal.searchHistory")}
                    <input
                      value={query}
                      onChange={(e) => setQuery(e.target.value)}
                      required
                    />
                  </label>
                  <label>{t("personal.filterAgent")}
                    <select aria-label={t("personal.filterAgent")} value={searchAgent} onChange={(e) => setSearchAgent(e.target.value)}>
                      <option value="">{t("personal.allAgents")}</option>
                      {snapshot?.agents.map((a) => <option value={a.id} key={a.id}>{a.name}</option>)}
                    </select>
                  </label>
                  <label className="personal-search-tools"><input type="checkbox" checked={searchTools} onChange={(e) => setSearchTools(e.target.checked)} />{t("personal.includeTools")}</label>
                  <div className="personal-history-dates">
                    <label>{t("personal.fromTime")}<input type="datetime-local" value={searchAfter} onChange={(e) => setSearchAfter(e.target.value)} /></label>
                    <label>{t("personal.untilTime")}<input type="datetime-local" value={searchBefore} onChange={(e) => setSearchBefore(e.target.value)} /></label>
                  </div>
                  <button type="submit" className="personal-primary" disabled={searching}>{t(searching ? "personal.searching" : "personal.search")}</button>
                </form>
                {searched && !searching && <p className="personal-search-summary" role="status">{t(hitsTotal ? "personal.searchCount" : "personal.noResults", { count: hitsTotal })}</p>}
                {hits.map((hit) => (
                  <button
                    className="personal-hit"
                    key={hit.messageId}
                    onClick={() => {
                      void chatAPI<{ messages: ChatMessage[] }>(
                        `/history?sessionId=${encodeURIComponent(hit.sessionId)}&messageId=${encodeURIComponent(hit.messageId)}&tools=${searchTools}`,
                      )
                        .then((r) => setHistory(r.messages))
                        .catch((e) => setError(String(e)));
                    }}
                  >
                    <span>
                      {new Date(hit.createdAt).toLocaleString()} ·{" "}
                      {snapshot?.agents.find((a) => a.id === hit.agentId)?.name}
                    </span>
                    {hit.role === "summary" && <span>{t("personal.summaryHit")}</span>}
                    <p>{hit.excerpt}</p>
                  </button>
                ))}
                {hits.length < hitsTotal && (
                  <button
                    onClick={() => {
                      void chatAPI<{ hits: ChatHit[] }>(
                        `/search?${committedSearch.current}&offset=${hits.length}`,
                      )
                        .then((r) => setHits((old) => [...old, ...r.hits]))
                        .catch((e) => setError(String(e)));
                    }}
                  >
                    {t("personal.moreResults")}
                  </button>
                )}
                {history.map((m) => (
                  <article key={m.id}>
                    <strong>
                      {m.role === "user"
                        ? t("personal.you")
                        : snapshot?.agents.find((a) => a.id === m.agentId)
                            ?.name}
                    </strong>
                    <pre>{m.content}</pre>
                    {m.tools?.map((tool) => <details key={tool.id}><summary>{tool.name}</summary><pre>{tool.args}</pre><pre>{tool.result}</pre></details>)}
                    <button
                      onClick={() => {
                        reply(m);
                        setPanel("");
                      }}
                    >
                      {t("personal.replyTask")}
                    </button>
                  </article>
                ))}
              </div>
            )}
          </aside>
        )}
      </div>
    </div>
  );
}

export function PersonalChatPreferences({
  settings,
  disabled,
  onSave,
}: {
  settings: ChatSettings;
  disabled?: boolean;
  onSave: (settings: ChatSettings) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState(settings);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState("");
  const { debug, idleMinutes, maxConcurrent } = settings;
  useEffect(
    () => setDraft({ debug, idleMinutes, maxConcurrent }),
    [debug, idleMinutes, maxConcurrent],
  );
  return (
    <form
      className="personal-preferences"
      onChange={() => setSaved(false)}
      onSubmit={(e) => {
        e.preventDefault();
        setSaving(true);
        setSaved(false);
        setError("");
        void onSave(draft).then(() => setSaved(true)).catch((e) => setError(String(e))).finally(() => setSaving(false));
      }}
    >
      <label>
        {t("personal.displayMode")}
        <select
          aria-label={t("personal.displayMode")}
          disabled={disabled}
          value={draft.debug ? "debug" : "normal"}
          onChange={(e) =>
            setDraft({ ...draft, debug: e.target.value === "debug" })
          }
        >
          <option value="normal">{t("personal.normalMode")}</option>
          <option value="debug">{t("personal.debugMode")}</option>
        </select>
      </label>
      <p>{t("personal.debugDescription")}</p>
      <label>
        {t("personal.idleMinutes")}
        <input
          type="number"
          min={1}
          max={525600}
          disabled={disabled}
          value={draft.idleMinutes}
          onChange={(e) =>
            setDraft({ ...draft, idleMinutes: Number(e.target.value) })
          }
        />
      </label>
      <label>
        {t("personal.maxConcurrent")}
        <input
          type="number"
          min={1}
          max={64}
          disabled={disabled}
          value={draft.maxConcurrent}
          onChange={(e) =>
            setDraft({ ...draft, maxConcurrent: Number(e.target.value) })
          }
        />
      </label>
      {error && <pre className="personal-error" role="alert">{error}</pre>}
      {saved && <span className="personal-search-summary" role="status">{t("personal.saved")}</span>}
      <button className="personal-primary" disabled={disabled || saving} type="submit">
        {t("personal.save")}
      </button>
    </form>
  );
}
