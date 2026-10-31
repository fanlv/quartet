export interface BusinessAgent {
  id: string;
  name: string;
  systemPrompt: string;
  harnessId?: string;
  main: boolean;
  revision: number;
  deleted?: boolean;
}
export interface ChatSettings {
  idleMinutes: number;
  debug: boolean;
  maxConcurrent: number;
}
export interface ChatTool {
  id: string;
  name: string;
  args: string;
  result: string;
  status: string;
}
export interface ChatMessage {
  id: string;
  order: number;
  role: string;
  agentId: string;
  taskId: string;
  sessionId: string;
  runId: string;
  content: string;
  rawContent?: string;
  thought?: string;
  tools?: ChatTool[];
  toolCount: number;
  state: string;
  error?: string;
  internal?: boolean;
  source: { platform?: string; accountId?: string; chatId?: string; senderId?: string };
  createdAt: number;
  revision: number;
}
export interface ChatTask {
  awaitingInput?: boolean;
  waitReason?: string;
  id: string;
  agentId: string;
  title: string;
  parentTaskId?: string;
  parentRunId?: string;
  sessionId: string;
  queue: string[];
  paused: boolean;
  createdAt: number;
}
export interface ChatRun {
  id: string;
  messageId: string;
  replyId: string;
  taskId: string;
  sessionId: string;
  agentId: string;
  state: string;
  error?: string;
  deliveryError?: string;
  createdAt: number;
}
export interface ChatSession {
  id: string;
  taskId: string;
  agentId: string;
  harnessId: string;
  summary?: string;
  summaryError?: string;
  meta: { model_id?: string; acp_mode?: string; acp_thought_level?: string };
  createdAt: number;
}
export interface ChatSnapshot {
  epoch: string;
  defaultHarnessId: string;
  workdir: string;
  code: number;
  seq: number;
  agents: BusinessAgent[];
  tasks: ChatTask[];
  sessions: ChatSession[];
  runs: ChatRun[];
  messages: ChatMessage[];
  settings: ChatSettings;
  before: number;
  hasMore: boolean;
  warnings: string[];
}
export interface ChatSend {
  clientMessageId: string;
  content: string;
  agentId: string;
  taskId?: string;
  harnessId?: string;
  modelId?: string;
  mode?: string;
  thoughtLevel?: string;
  independent?: boolean;
  stopAndSend?: boolean;
}
export interface ChatHit {
  sessionId: string;
  messageId: string;
  agentId: string;
  role: string;
  excerpt: string;
  createdAt: number;
}

export async function chatAPI<T>(
  path: string,
  body?: unknown,
  method?: string,
  signal?: AbortSignal,
): Promise<T> {
  const verb = method ?? (body === undefined ? "GET" : "POST");
  const response = await fetch(`/api/v1/chat${path}`, {
    method: verb,
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  const raw = await response.text();
  if (!response.ok)
    throw new Error(
      `${verb} ${response.url || `/api/v1/chat${path}`}\nHTTP ${response.status}\n${raw}`,
    );
  try {
    return JSON.parse(raw) as T;
  } catch {
    throw new Error(
      `${verb} /api/v1/chat${path}\nInvalid JSON response\n${raw}`,
    );
  }
}

export function hideSystemReminder(text: string): string {
  let depth = 0,
    cursor = 0,
    result = "";
  for (const match of text.matchAll(/<\/?system-reminder>/g)) {
    const index = match.index;
    if (!depth) result += text.slice(cursor, index);
    if (match[0] === "<system-reminder>") depth++;
    else if (depth) depth--;
    cursor = index + match[0].length;
  }
  if (!depth) result += text.slice(cursor);
  const opening = "<system-reminder>";
  for (let n = opening.length - 1; n > 0; n--)
    if (result.endsWith(opening.slice(0, n))) return result.slice(0, -n);
  return result;
}
