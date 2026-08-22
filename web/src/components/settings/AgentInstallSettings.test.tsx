import { describe, expect, it } from 'vitest';
import { clearDeletedAgentLocalPreferences } from '../../utils/workspace';

describe('clearDeletedAgentLocalPreferences', () => {
  it('clears every adjacent workspace preference for the deleted Agent', () => {
    localStorage.setItem('workspacePrefs_ws-a', JSON.stringify({
      defaultAgent: 'custom-deleted',
      defaultModel: 'model-a',
    }));
    localStorage.setItem('workspacePrefs_ws-b', JSON.stringify({
      defaultAgent: 'custom-deleted',
      defaultModel: 'model-b',
    }));
    localStorage.setItem('workspacePrefs_ws-c', JSON.stringify({
      defaultAgent: 'custom-kept',
      defaultModel: 'model-c',
    }));

    clearDeletedAgentLocalPreferences('custom-deleted');

    expect(localStorage.getItem('workspacePrefs_ws-a')).toBeNull();
    expect(localStorage.getItem('workspacePrefs_ws-b')).toBeNull();
    expect(localStorage.getItem('workspacePrefs_ws-c')).toBe(JSON.stringify({
      defaultAgent: 'custom-kept',
      defaultModel: 'model-c',
    }));
  });
});
