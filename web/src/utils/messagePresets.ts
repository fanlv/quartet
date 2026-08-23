export interface MessagePreset {
  id: string;
  name?: string;
  content: string;
}

export interface MessagePresetConfig {
  schemaVersion: number;
  workspaceId?: string;
  workspaceTitle?: string;
  workspaceWorkdir?: string;
  messages: MessagePreset[];
}

export interface MessagePresetScopeResponse {
  code: number;
  revision: string;
  config: MessagePresetConfig;
}

export interface MessagePresetLoadError {
  scope: string;
  file: string;
  error: string;
}

export interface EffectiveMessagePresetsResponse {
  code: number;
  workspaceId: string;
  project: MessagePreset[];
  global: MessagePreset[];
  errors?: MessagePresetLoadError[];
}

export interface OrphanMessagePreset {
  revision: string;
  config: MessagePresetConfig;
}

export const MESSAGE_PRESETS_CHANGED_EVENT = 'quartet:message-presets-changed';

export async function responseError(response: Response): Promise<string> {
  const text = await response.text().catch((error) => String(error));
  if (!text) return `HTTP ${response.status} ${response.statusText}`;
  try {
    const parsed = JSON.parse(text) as { msg?: string; error?: string };
    return parsed.msg || parsed.error || text;
  } catch {
    return text;
  }
}

export async function fetchEffectiveMessagePresets(workspaceId: string): Promise<EffectiveMessagePresetsResponse> {
  const response = await fetch(`/api/v1/config/message-presets/effective?workspaceId=${encodeURIComponent(workspaceId)}`, { cache: 'no-store' });
  if (!response.ok) throw new Error(await responseError(response));
  return response.json() as Promise<EffectiveMessagePresetsResponse>;
}

export function dispatchMessagePresetsChanged(workspaceId?: string) {
  window.dispatchEvent(new CustomEvent(MESSAGE_PRESETS_CHANGED_EVENT, { detail: { workspaceId } }));
}
