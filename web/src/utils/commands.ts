// Slash-command metadata shared between ChatInput's autocomplete overlay and
// MessageItem's system bubble (which uses `commandSource` to decide whether
// to turn /ws list / /job list rows into clickable links).
//
// NOTE: this must stay in sync with services/command/command.go's
// Definitions() on the backend. Adding a command means editing both places.
// If this pair drifts we'll accept the mismatch at the boundary: the backend
// is authoritative (it runs Execute), the frontend list only controls
// autocomplete and list-row click-ability — both degrade gracefully.

export interface CommandDef {
  // Canonical name, e.g. "/workspace".
  name: string;
  // Alternate forms the user may type, e.g. ["/ws"].
  aliases?: string[];
  // Shown next to the name in the autocomplete overlay.
  description: string;
}

export const COMMANDS: CommandDef[] = [
  { name: '/help', description: '查看可用命令' },
  { name: '/workspace', aliases: ['/ws'], description: '查看/切换工作空间' },
  { name: '/job', description: '查看/绑定 Job' },
  { name: '/new', description: '在当前工作空间创建新对话' },
  { name: '/status', aliases: ['/info'], description: '查看当前聊天状态' },
];

// Flat list of every name the user could type (canonical names + aliases).
// Kept as a static export so callers that just need string matching don't
// have to re-derive it.
export const ALL_COMMAND_NAMES: string[] = (() => {
  const names: string[] = [];
  for (const c of COMMANDS) {
    names.push(c.name);
    if (c.aliases) names.push(...c.aliases);
  }
  return names;
})();

// True when the given raw text looks like a slash command the backend
// recognizes. Used to decide whether to suppress the optimistic user message
// in chat sends — known commands take the command-dispatch branch on the
// server and won't produce a RUN_FINISHED event.
export function isKnownCommand(text: string): boolean {
  const trimmed = text.trim();
  if (!trimmed.startsWith('/')) return false;
  const head = trimmed.split(/\s+/, 1)[0].toLowerCase();
  return ALL_COMMAND_NAMES.includes(head);
}

export function isReadOnlyCommand(text: string): boolean {
  const trimmed = text.trim();
  const [rawHead = '', rawSub = ''] = trimmed.split(/\s+/, 2);
  const name = resolveCommandName(rawHead);
  const sub = rawSub.toLowerCase();
  if (name === '/help' || name === '/status') return true;
  if (name === '/workspace' || name === '/job') return !sub || sub === 'list' || sub === 'ls';
  return false;
}

// Resolves an alias/canonical name to its canonical form, or empty if the
// text doesn't match a known command. Mirrors services/command.ResolveName.
export function resolveCommandName(raw: string): string {
  const head = raw.trim().toLowerCase();
  if (!head) return '';
  for (const c of COMMANDS) {
    if (c.name === head) return c.name;
    if (c.aliases?.includes(head)) return c.name;
  }
  return '';
}
