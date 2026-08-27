// Slash ("/") completion logic shared by the chat-page input (ChatInput) and
// the home-page input (ChatPage): prefix-triggered floater listing built-in
// commands + installed skills, keyboard navigation, and the skill-name chip
// highlight painted by the transparent-text textarea's backdrop layer.
//
// Trigger rule (both inputs): the whole value starts with "/" and contains no
// space yet. Selecting an item inserts its name as plain text ("/help " or
// "/pptx ") — unknown slash text falls through to the agent as a regular
// message, so no backend change is needed for skills.
//
// Presentational pieces (SlashFloater / SkillBackdrop) live in
// components/SlashCompletion.tsx; this file holds the non-component logic.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { Dispatch, RefObject, SetStateAction } from 'react';
import { COMMANDS } from './commands';
import { fetchSkills } from './skills';
import type { SkillInfo } from './skills';

/** One row in the "/" completion floater: a built-in command or an installed skill. */
export interface SlashItem {
  kind: 'command' | 'skill';
  /** Text inserted into the input on selection, e.g. "/help" or "/pptx". */
  name: string;
  /** Secondary line: command description or skill path. */
  description?: string;
  /** Skill scope badge ("project" / "global"). */
  scope?: string;
}

/** One backdrop segment: chip=true marks an installed-skill token. */
export interface SkillSegment {
  text: string;
  chip: boolean;
}

const SKILL_TOKEN_RE = /^\/[\w-]+$/;

/** Split input text into segments, flagging installed-skill tokens (e.g.
 *  "/pptx") for chip rendering. Skill names match case-insensitively; unknown
 *  "/paths" (e.g. /etc/hosts) stay plain text. */
export function splitSkillSegments(text: string, skillNames: ReadonlySet<string>): SkillSegment[] {
  return text.split(/(\s+)/).map((part) => ({
    text: part,
    chip: SKILL_TOKEN_RE.test(part) && skillNames.has(part.slice(1).toLowerCase()),
  }));
}

export interface SlashCompletion {
  /** Non-null while the floater is open; the raw input value used as prefix. */
  slashPrefix: string | null;
  /** Grouped rows: built-in commands first, then skills. Derived (not stored)
   *  so a late-arriving skill fetch refreshes the open floater. */
  slashItems: SlashItem[];
  slashActiveIdx: number;
  setSlashActiveIdx: Dispatch<SetStateAction<number>>;
  /** Call from the input's onChange with the new value and whether an
   *  @-mention is currently active (mention wins, slash closes). */
  updateSlash: (val: string, mentionActive: boolean) => void;
  /** Insert the picked item into the input and close the floater. */
  applySlashItem: (item: SlashItem) => void;
  /** Close the floater without inserting (Esc, send, disabled). */
  closeSlash: () => void;
  /** Lower-cased installed skill names, for the backdrop chip tokenizer. */
  skillNameSet: ReadonlySet<string>;
  /** True while an IME composition is active. Un-committed composing text
   *  lives only in the textarea (React state updates on compositionend), so
   *  the transparent-text backdrop trick would hide it — callers add the
   *  `composing` class to .chat-input-editor to swap painting back to the
   *  textarea for the duration. */
  imeComposing: boolean;
  /** Spread onto the textarea to drive {@link imeComposing}. */
  compositionHandlers: {
    onCompositionStart: () => void;
    onCompositionEnd: () => void;
  };
}

export function useSlashCompletion(opts: {
  setInput: (v: string) => void;
  textareaRef: RefObject<HTMLTextAreaElement | null>;
  /** Show built-in commands (/help, /workspace, …) alongside skills.
   *  Default true; the home page passes false (skills only) since the
   *  built-in commands operate on an existing chat. */
  includeCommands?: boolean;
  /** Workspace whose project-scope skills should be listed alongside the
   *  global ones — the agent runs in this workspace's workdir, so those are
   *  exactly the skills it can load. Omit to list global skills only. */
  workspaceId?: string;
}): SlashCompletion {
  const { setInput, textareaRef, includeCommands = true, workspaceId } = opts;
  const [slashPrefix, setSlashPrefix] = useState<string | null>(null);
  const [slashActiveIdx, setSlashActiveIdx] = useState(0);
  // Installed skills, loaded lazily the first time "/" is typed. Also drives
  // the skill-name chip highlight in the input backdrop.
  const [skills, setSkills] = useState<SkillInfo[]>([]);
  // Workspace the current `skills` were loaded for; switching workspace swaps
  // the applicable project scope, so the list has to be re-fetched.
  const loadedForRef = useRef<string | null>(null);
  const skillNameSet = useMemo(
    () => new Set(skills.map((s) => s.name.toLowerCase())),
    [skills],
  );
  useEffect(() => {
    setSlashActiveIdx(0);
  }, [slashPrefix]);

  const slashItems = useMemo<SlashItem[]>(() => {
    if (slashPrefix == null) return [];
    const prefix = slashPrefix.toLowerCase();
    const cmdItems: SlashItem[] = [];
    if (includeCommands) {
      for (const c of COMMANDS) {
        if (c.name.startsWith(prefix)) {
          cmdItems.push({ kind: 'command', name: c.name, description: c.description });
        }
        for (const alias of c.aliases ?? []) {
          if (alias.startsWith(prefix)) {
            cmdItems.push({ kind: 'command', name: alias, description: c.description });
          }
        }
      }
    }
    const skillItems: SlashItem[] = skills
      .filter((s) => `/${s.name.toLowerCase()}`.startsWith(prefix))
      .map((s) => ({ kind: 'skill', name: `/${s.name}`, description: s.path, scope: s.scope }));
    return [...cmdItems, ...skillItems];
  }, [slashPrefix, skills, includeCommands]);

  const updateSlash = useCallback((val: string, mentionActive: boolean) => {
    if (!mentionActive && val.startsWith('/') && !val.includes(' ')) {
      const loadKey = workspaceId || '';
      if (loadedForRef.current !== loadKey) {
        loadedForRef.current = loadKey;
        fetchSkills(workspaceId).then(setSkills).catch(() => {
          loadedForRef.current = null;
        });
      }
      setSlashPrefix(val);
    } else {
      setSlashPrefix(null);
    }
  }, [workspaceId]);

  const applySlashItem = useCallback((item: SlashItem) => {
    setInput(item.name + ' ');
    setSlashPrefix(null);
    requestAnimationFrame(() => {
      const ta = textareaRef.current;
      if (ta) {
        ta.focus();
        // Drop the caret after the inserted "/name " — a controlled-value
        // update otherwise keeps the caret at its old (pre-completion) index.
        const pos = item.name.length + 1;
        ta.selectionStart = ta.selectionEnd = pos;
      }
    });
  }, [setInput, textareaRef]);

  const closeSlash = useCallback(() => setSlashPrefix(null), []);

  const [imeComposing, setImeComposing] = useState(false);
  const compositionHandlers = useMemo(() => ({
    onCompositionStart: () => setImeComposing(true),
    onCompositionEnd: () => setImeComposing(false),
  }), []);

  return {
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
  };
}

/** Keyboard navigation for the slash floater. Returns true when the key was
 *  consumed (caller must skip its own Enter/Arrow handling). */
export function slashCompletionKeyDown(
  e: { key: string; shiftKey: boolean; metaKey: boolean; ctrlKey: boolean; preventDefault: () => void },
  items: SlashItem[],
  activeIdx: number,
  setActiveIdx: Dispatch<SetStateAction<number>>,
  pick: (item: SlashItem) => void,
  close: () => void,
): boolean {
  if (items.length === 0) return false;
  if (e.key === 'ArrowDown') {
    e.preventDefault();
    setActiveIdx((i) => (i + 1) % items.length);
    return true;
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault();
    setActiveIdx((i) => (i - 1 + items.length) % items.length);
    return true;
  }
  if (e.key === 'Escape') {
    e.preventDefault();
    close();
    return true;
  }
  if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && !e.metaKey && !e.ctrlKey)) {
    e.preventDefault();
    const item = items[activeIdx] ?? items[0];
    if (item) pick(item);
    return true;
  }
  return false;
}
