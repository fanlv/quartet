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
): [LocalComposerDraft, (update: DraftUpdate) => void, () => void] {
  const [state, setState] = useState<LocalComposerDraftState>(() => ({
    storageKey,
    value: readLocalComposerDraft(storageKey),
  }));
  const stateRef = useRef(state);

  const value = state.storageKey === storageKey
    ? state.value
    : readLocalComposerDraft(storageKey);

  useEffect(() => {
    if (stateRef.current.storageKey === storageKey) return;
    const nextState = { storageKey, value: readLocalComposerDraft(storageKey) };
    stateRef.current = nextState;
    setState(nextState);
  }, [storageKey]);

  const setValue = useCallback((update: DraftUpdate) => {
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
    const nextState = { storageKey, value: emptyDraft() };
    writeLocalComposerDraft(storageKey, nextState.value);
    stateRef.current = nextState;
    setState(nextState);
  }, [storageKey]);

  return [value, setValue, clearValue];
}
