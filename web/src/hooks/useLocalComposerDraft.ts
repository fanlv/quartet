import { useCallback, useEffect, useRef, useState } from 'react';
import type { FileAttachment } from '../types';

export interface LocalComposerDraft {
  text: string;
  imageUrls: string[];
  fileAttachments: FileAttachment[];
}

interface LocalComposerDraftPayload extends LocalComposerDraft {
  v: 1;
}

interface LocalComposerDraftState {
  storageKey: string | null;
  value: LocalComposerDraft;
}

type DraftUpdate = LocalComposerDraft | ((current: LocalComposerDraft) => LocalComposerDraft);

const SERIALIZED_DRAFT_PREFIX = 'quartet-composer-draft-v1:';

function emptyDraft(): LocalComposerDraft {
  return { text: '', imageUrls: [], fileAttachments: [] };
}

function isEmptyDraft(value: LocalComposerDraft): boolean {
  return value.text.length === 0 && value.imageUrls.length === 0 && value.fileAttachments.length === 0;
}

export interface LocalComposerDraftOptions {
  /** When the storage scope changes, keep the current draft if the new scope has nothing stored. */
  keepDraftWhenStoredEmpty?: boolean;
}

function normalizeFileAttachment(value: unknown): FileAttachment | null {
  if (!value || typeof value !== 'object') return null;
  const candidate = value as Partial<FileAttachment>;
  if (typeof candidate.path !== 'string' || typeof candidate.name !== 'string') return null;
  return {
    path: candidate.path,
    name: candidate.name,
    ...(typeof candidate.mimeType === 'string' ? { mimeType: candidate.mimeType } : {}),
    ...(typeof candidate.size === 'number' ? { size: candidate.size } : {}),
  };
}

function readLocalComposerDraft(storageKey: string | null): LocalComposerDraft {
  if (!storageKey) return emptyDraft();
  try {
    const raw = localStorage.getItem(storageKey);
    if (raw === null) return emptyDraft();

    // Drafts created before attachments were persisted contain the text as a
    // bare string. Keep accepting that format so an upgrade never discards an
    // existing unsubmitted message.
    if (raw.startsWith(SERIALIZED_DRAFT_PREFIX)) {
      try {
        const parsed = JSON.parse(raw.slice(SERIALIZED_DRAFT_PREFIX.length)) as Partial<LocalComposerDraftPayload> | null;
        if (parsed && typeof parsed === 'object' && parsed.v === 1 && typeof parsed.text === 'string') {
          return {
            text: parsed.text,
            imageUrls: Array.isArray(parsed.imageUrls)
              ? parsed.imageUrls.filter((url): url is string => typeof url === 'string')
              : [],
            fileAttachments: Array.isArray(parsed.fileAttachments)
              ? parsed.fileAttachments.map(normalizeFileAttachment).filter((file): file is FileAttachment => file !== null)
              : [],
          };
        }
      } catch {
        // Preserve a malformed value as text rather than silently discarding it.
      }
    }
    return { ...emptyDraft(), text: raw };
  } catch {
    return emptyDraft();
  }
}

function writeLocalComposerDraft(storageKey: string | null, value: LocalComposerDraft) {
  if (!storageKey) return;
  try {
    if (value.text.length === 0 && value.imageUrls.length === 0 && value.fileAttachments.length === 0) {
      localStorage.removeItem(storageKey);
      return;
    }
    const payload: LocalComposerDraftPayload = { v: 1, ...value };
    localStorage.setItem(storageKey, `${SERIALIZED_DRAFT_PREFIX}${JSON.stringify(payload)}`);
  } catch {
    // Ignore unavailable storage and quota errors; the in-memory draft remains usable.
  }
}

/** Keeps the complete unsubmitted composer state isolated by page scope. */
export function useLocalComposerDraft(
  storageKey: string | null,
  options?: LocalComposerDraftOptions,
): [LocalComposerDraft, (update: DraftUpdate) => void, () => void] {
  const keepDraftWhenStoredEmpty = options?.keepDraftWhenStoredEmpty ?? false;
  const [state, setState] = useState<LocalComposerDraftState>(() => ({
    storageKey,
    value: readLocalComposerDraft(storageKey),
  }));
  const stateRef = useRef(state);
  const pendingPersistRef = useRef<LocalComposerDraft | null>(null);

  if (state.storageKey !== storageKey) {
    const incoming = readLocalComposerDraft(storageKey);
    const keep = keepDraftWhenStoredEmpty
      && storageKey !== null
      && !isEmptyDraft(state.value)
      && isEmptyDraft(incoming);
    const nextState = { storageKey, value: keep ? state.value : incoming };
    stateRef.current = nextState;
    pendingPersistRef.current = keep ? nextState.value : null;
    setState(nextState);
  } else {
    stateRef.current = state;
  }

  useEffect(() => {
    const pending = pendingPersistRef.current;
    if (!pending || !storageKey) return;
    if (stateRef.current.storageKey !== storageKey || stateRef.current.value !== pending) return;
    pendingPersistRef.current = null;
    writeLocalComposerDraft(storageKey, pending);
  }, [storageKey, state]);

  const value = stateRef.current.storageKey === storageKey ? stateRef.current.value : state.value;

  const setValue = useCallback((update: DraftUpdate) => {
    pendingPersistRef.current = null;
    const current = stateRef.current.storageKey === storageKey
      ? stateRef.current.value
      : readLocalComposerDraft(storageKey);
    const nextValue = typeof update === 'function' ? update(current) : update;
    const nextState = { storageKey, value: nextValue };
    writeLocalComposerDraft(storageKey, nextValue);
    stateRef.current = nextState;
    setState(nextState);
  }, [storageKey]);

  const clearValue = useCallback(() => {
    pendingPersistRef.current = null;
    const nextState = { storageKey, value: emptyDraft() };
    writeLocalComposerDraft(storageKey, nextState.value);
    stateRef.current = nextState;
    setState(nextState);
  }, [storageKey]);

  return [value, setValue, clearValue];
}
