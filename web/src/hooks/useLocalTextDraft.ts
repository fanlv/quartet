import { useCallback, useEffect, useState } from 'react';

interface LocalTextDraftState {
  storageKey: string | null;
  value: string;
}

function readLocalTextDraft(storageKey: string | null): string {
  if (!storageKey) return '';
  try {
    return localStorage.getItem(storageKey) ?? '';
  } catch {
    return '';
  }
}

function writeLocalTextDraft(storageKey: string | null, value: string) {
  if (!storageKey) return;
  try {
    if (value.length > 0) {
      localStorage.setItem(storageKey, value);
    } else {
      localStorage.removeItem(storageKey);
    }
  } catch {
    // Ignore unavailable storage and quota errors; the in-memory draft remains usable.
  }
}

/**
 * Keeps a text input in localStorage while isolating it by the supplied page scope.
 * Passing null keeps the same API but disables persistence.
 */
export function useLocalTextDraft(storageKey: string | null): [string, (value: string) => void, () => void] {
  const [state, setState] = useState<LocalTextDraftState>(() => ({
    storageKey,
    value: readLocalTextDraft(storageKey),
  }));

  const value = state.storageKey === storageKey
    ? state.value
    : readLocalTextDraft(storageKey);

  useEffect(() => {
    setState((current) => current.storageKey === storageKey
      ? current
      : { storageKey, value: readLocalTextDraft(storageKey) });
  }, [storageKey]);

  const setValue = useCallback((nextValue: string) => {
    writeLocalTextDraft(storageKey, nextValue);
    setState({ storageKey, value: nextValue });
  }, [storageKey]);

  const clearValue = useCallback(() => {
    writeLocalTextDraft(storageKey, '');
    setState({ storageKey, value: '' });
  }, [storageKey]);

  return [value, setValue, clearValue];
}
