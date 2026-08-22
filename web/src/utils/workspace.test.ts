import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  DEFAULT_WORKSPACE_ID,
  getLastUsedWorkspaceId,
  isDefaultWorkspace,
  loadWorkspacePrefs,
  migrateWorkspacePrefsToServer,
  registerWorkspacePrefs,
  registerWorkspaceColors,
  saveWorkspacePrefs,
  setLastUsedWorkspaceId,
  workspaceColor,
} from './workspace';

describe('workspace utils', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.restoreAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

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

  it('migrates legacy local prefs to the server and removes local storage on success', async () => {
    localStorage.setItem('workspacePrefs_ws-migrate', JSON.stringify({
      defaultAgent: 'legacy-agent',
      defaultModel: 'legacy-model',
    }));

    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          id: 'ws-migrate',
          title: 'Workspace',
          description: 'Desc',
          workdir: '/tmp/ws-migrate',
          defaultAgent: '',
          defaultModel: '',
        }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          id: 'ws-migrate',
          defaultAgent: 'agent-canonical',
          defaultModel: 'legacy-model',
        }),
      });
    vi.stubGlobal('fetch', fetchMock);

    const migrated = await migrateWorkspacePrefsToServer('ws-migrate', (value) => (
      value === 'legacy-agent' ? 'agent-canonical' : undefined
    ));

    expect(migrated).toEqual({
      defaultAgent: 'agent-canonical',
      defaultModel: 'legacy-model',
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/workspace/ws-migrate');
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/workspace/ws-migrate', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: 'Workspace',
        description: 'Desc',
        workdir: '/tmp/ws-migrate',
        defaultAgent: 'agent-canonical',
        defaultModel: 'legacy-model',
      }),
    });
    expect(localStorage.getItem('workspacePrefs_ws-migrate')).toBeNull();
    expect(loadWorkspacePrefs('ws-migrate')).toEqual({
      defaultAgent: 'agent-canonical',
      defaultModel: 'legacy-model',
    });
  });

  it('keeps legacy local prefs when server update fails', async () => {
    localStorage.setItem('workspacePrefs_ws-fail', JSON.stringify({
      defaultAgent: 'legacy-agent',
      defaultModel: 'legacy-model',
    }));

    const fetchMock = vi.fn()
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          id: 'ws-fail',
          title: 'Workspace',
          description: 'Desc',
          workdir: '/tmp/ws-fail',
          defaultAgent: '',
          defaultModel: '',
        }),
      })
      .mockResolvedValueOnce({
        ok: false,
      });
    vi.stubGlobal('fetch', fetchMock);

    const migrated = await migrateWorkspacePrefsToServer('ws-fail');

    expect(migrated).toEqual({
      defaultAgent: 'legacy-agent',
      defaultModel: 'legacy-model',
    });
    expect(localStorage.getItem('workspacePrefs_ws-fail')).toBe(JSON.stringify({
      defaultAgent: 'legacy-agent',
      defaultModel: 'legacy-model',
    }));
  });

  it('prefers server shared prefs and clears obsolete local storage without rewriting', async () => {
    registerWorkspacePrefs('ws-server', {
      defaultAgent: 'server-agent',
      defaultModel: 'server-model',
    });
    localStorage.setItem('workspacePrefs_ws-server', JSON.stringify({
      defaultAgent: 'legacy-agent',
      defaultModel: 'legacy-model',
    }));

    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({
        id: 'ws-server',
        title: 'Workspace',
        description: 'Desc',
        workdir: '/tmp/ws-server',
        defaultAgent: 'server-agent',
        defaultModel: 'server-model',
      }),
    });
    vi.stubGlobal('fetch', fetchMock);

    const migrated = await migrateWorkspacePrefsToServer('ws-server');

    expect(migrated).toEqual({
      defaultAgent: 'server-agent',
      defaultModel: 'server-model',
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem('workspacePrefs_ws-server')).toBeNull();
  });
});
