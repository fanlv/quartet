import { useCallback, useEffect, useRef, useState } from 'react';
import {
  fetchEffectiveMessagePresets,
  MESSAGE_PRESETS_CHANGED_EVENT,
  type MessagePreset,
  type MessagePresetLoadError,
} from '../utils/messagePresets';

export function useMessagePresets(workspaceId?: string, enabled = true) {
  const [project, setProject] = useState<MessagePreset[]>([]);
  const [global, setGlobal] = useState<MessagePreset[]>([]);
  const [errors, setErrors] = useState<MessagePresetLoadError[]>([]);
  const [loading, setLoading] = useState(false);
  const requestSequence = useRef(0);

  const refresh = useCallback(async () => {
    const request = ++requestSequence.current;
    if (!enabled || !workspaceId) {
      setProject([]);
      setGlobal([]);
      setErrors([]);
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const response = await fetchEffectiveMessagePresets(workspaceId);
      if (request !== requestSequence.current) return;
      setProject(response.project || []);
      setGlobal(response.global || []);
      setErrors(response.errors || []);
    } catch (error) {
      if (request !== requestSequence.current) return;
      setProject([]);
      setGlobal([]);
      setErrors([{ scope: 'effective', file: '', error: error instanceof Error ? error.message : String(error) }]);
    } finally {
      if (request === requestSequence.current) setLoading(false);
    }
  }, [enabled, workspaceId]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    const handleChanged = () => { void refresh(); };
    window.addEventListener(MESSAGE_PRESETS_CHANGED_EVENT, handleChanged);
    return () => window.removeEventListener(MESSAGE_PRESETS_CHANGED_EVENT, handleChanged);
  }, [refresh]);

  return { project, global, errors, loading, refresh };
}
