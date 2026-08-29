import { useEffect, useRef, useState } from 'react';
import type { SessionThoughtLevelState } from '../types';
import { relinkACPThoughtLevels } from '../utils/acpConfig';

interface LinkedThoughtLevels {
  key: string;
  state: SessionThoughtLevelState | null;
  loading: boolean;
  error: string;
}

const emptyResult: LinkedThoughtLevels = {
  key: '',
  state: null,
  loading: false,
  error: '',
};

// Refreshes the model-linked thought-level list whenever an ACP selector picks
// a concrete agent/model pair. Results are keyed so a response from an older
// selection is never rendered for the current one.
export function useACPThoughtLevels(
  agentType: string,
  modelId: string,
  enabled = true,
  cachedState: SessionThoughtLevelState | null = null,
): LinkedThoughtLevels {
  const key = enabled && agentType && modelId ? `${agentType}::${modelId}` : '';
  const cachedStateRef = useRef(cachedState);
  cachedStateRef.current = cachedState;
  const [result, setResult] = useState<LinkedThoughtLevels>(emptyResult);

  useEffect(() => {
    if (!key) {
      setResult(emptyResult);
      return;
    }

    let cancelled = false;
    setResult((previous) => ({
      key,
      state: previous.key === key ? previous.state : cachedStateRef.current,
      loading: true,
      error: '',
    }));
    void relinkACPThoughtLevels(agentType, modelId).then((state) => {
      if (!cancelled) setResult({ key, state, loading: false, error: '' });
    }).catch((err) => {
      if (!cancelled) {
        setResult((previous) => ({
          key,
          state: previous.key === key ? previous.state : cachedStateRef.current,
          loading: false,
          error: err instanceof Error ? err.message : String(err),
        }));
      }
    });

    return () => {
      cancelled = true;
    };
  }, [agentType, key, modelId]);

  return result.key === key ? result : { ...emptyResult, key, state: cachedState };
}
