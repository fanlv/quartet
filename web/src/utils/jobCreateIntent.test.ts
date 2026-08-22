import { beforeEach, describe, expect, it, vi } from 'vitest';
import { claimJobCreateIntent, clearJobCreateIntent, clearJobCreateIntentScope } from './jobCreateIntent';

describe('job create intent keys', () => {
  beforeEach(() => {
    localStorage.clear();
    let n = 0;
    vi.spyOn(crypto, 'randomUUID').mockImplementation(() => `00000000-0000-4000-8000-${String(++n).padStart(12, '0')}`);
  });

  it('keeps the first preferred action key until the create result is resolved', () => {
    const first = claimJobCreateIntent('command-new:job-1', { workspaceId: 'ws-1' }, 'server-action-1');
    expect(first).toBe('server-action-1');
    expect(claimJobCreateIntent('command-new:job-1', { workspaceId: 'ws-1' }, 'server-action-2')).toBe(first);
    const changed = claimJobCreateIntent('command-new:job-1', { workspaceId: 'ws-2' }, 'server-action-1');
    expect(changed).not.toBe(first);
    clearJobCreateIntentScope('command-new:job-1');
    expect(claimJobCreateIntent('command-new:job-1', { workspaceId: 'ws-1' }, 'server-action-2')).toBe('server-action-2');
  });

  it('reuses a key after an unknown result and changes it with request semantics', () => {
    const first = claimJobCreateIntent('start-chat', { workspaceId: 'ws-1', agentType: 'a', modelId: 'm' });
    expect(claimJobCreateIntent('start-chat', { modelId: 'm', agentType: 'a', workspaceId: 'ws-1' })).toBe(first);
    expect(claimJobCreateIntent('start-chat', { workspaceId: 'ws-1', agentType: 'a', modelId: 'other' })).not.toBe(first);
  });

  it('isolates entry points and conditionally clears only the completed intent', () => {
    const first = claimJobCreateIntent('start-chat', { workspaceId: 'ws-1' });
    const other = claimJobCreateIntent('start-new-chat', { workspaceId: 'ws-1' });
    expect(other).not.toBe(first);

    const replacement = claimJobCreateIntent('start-chat', { workspaceId: 'ws-2' });
    clearJobCreateIntent('start-chat', first);
    expect(claimJobCreateIntent('start-chat', { workspaceId: 'ws-2' })).toBe(replacement);

    clearJobCreateIntent('start-chat', replacement);
    expect(claimJobCreateIntent('start-chat', { workspaceId: 'ws-2' })).not.toBe(replacement);
  });
});
