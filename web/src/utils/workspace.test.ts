import { describe, expect, it, vi } from 'vitest';
import {
  DEFAULT_WORKSPACE_ID,
  getLastUsedWorkspaceId,
  isDefaultWorkspace,
  loadWorkspacePrefs,
  registerWorkspaceColors,
  saveWorkspacePrefs,
  setLastUsedWorkspaceId,
  workspaceColor,
} from './workspace';

describe('workspace utils', () => {
  it('detects the fixed default workspace id', () => {
    expect(isDefaultWorkspace(DEFAULT_WORKSPACE_ID)).toBe(true);
    expect(isDefaultWorkspace('ws-other')).toBe(false);
    expect(isDefaultWorkspace(undefined)).toBe(false);
  });

  it('prefers server and registered colors and falls back to stable colors', () => {
    expect(workspaceColor(undefined)).toBe('#cbd5e1');
    expect(workspaceColor({ id: 'ws-server', color: '#123456' })).toBe('#123456');

    registerWorkspaceColors([{ id: 'ws-registered', color: '#abcdef' }]);
    expect(workspaceColor('ws-registered')).toBe('#abcdef');
    expect(workspaceColor({ id: 'ws-registered' })).toBe('#abcdef');

    const first = workspaceColor('ws-hash-only');
    const second = workspaceColor('ws-hash-only');
    expect(first).toMatch(/^#[0-9a-f]{6}$/);
    expect(second).toBe(first);
  });

  it('loads, saves and removes per-workspace preferences', () => {
    expect(loadWorkspacePrefs('ws-prefs')).toEqual({});

    saveWorkspacePrefs('ws-prefs', { defaultAgent: 'quartet', defaultModel: 'opus' });
    expect(loadWorkspacePrefs('ws-prefs')).toEqual({ defaultAgent: 'quartet', defaultModel: 'opus' });

    saveWorkspacePrefs('ws-prefs', {});
    expect(loadWorkspacePrefs('ws-prefs')).toEqual({});
  });

  it('ignores invalid preference payloads and storage errors', () => {
    localStorage.setItem('workspacePrefs_ws-bad', '{bad json');
    expect(loadWorkspacePrefs('ws-bad')).toEqual({});

    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('storage unavailable');
    });
    expect(loadWorkspacePrefs('ws-error')).toEqual({});
    getItem.mockRestore();

    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('storage unavailable');
    });
    expect(() => setLastUsedWorkspaceId('ws-error')).not.toThrow();
    setItem.mockRestore();
  });

  it('stores and restores last used workspace id', () => {
    expect(getLastUsedWorkspaceId()).toBeUndefined();

    setLastUsedWorkspaceId('ws-last');
    expect(getLastUsedWorkspaceId()).toBe('ws-last');
  });
});
