import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ACPSettings } from './ACPSettings';
import { AgentDefaultsSettings } from './AgentDefaultsSettings';

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  });
}

const agentListResponse = {
  code: 0,
  agent_list: [{
    agent_id: 'agent-one',
    type: 'agent-one',
    model_id: '',
    display_name: 'Agent One',
    icon_url: '',
    available: true,
    models: { availableModels: [], currentModelId: '' },
  }],
};

describe('Agent settings load guards', () => {
  it('keeps Agent defaults read-only when the settings snapshot fails to load', async () => {
    const user = userEvent.setup();
    let settingsAttempts = 0;
    vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      if (init?.method === 'PUT') throw new Error(`Unexpected write request: ${url}`);
      if (url === '/api/v1/agent/list') return jsonResponse(agentListResponse);
      if (url === '/api/v1/config/settings/get') {
        settingsAttempts += 1;
        if (settingsAttempts === 1) {
          return jsonResponse({ code: -1, msg: 'read settings file failed: injected' }, { status: 500 });
        }
        return jsonResponse({ code: 0, settings: { agent_prefs: {} } });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    });

    render(<AgentDefaultsSettings />);

    expect(await screen.findByRole('alert')).toHaveTextContent('read settings file failed: injected');
    expect(screen.queryByTestId('agent-defaults-save-button')).toBeNull();
    expect(vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);

    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByTestId('agent-defaults-save-button')).toBeDisabled();
  });

  it('does not create a saveable environment template after a settings business error', async () => {
    vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      if (init?.method === 'PUT') throw new Error(`Unexpected write request: ${url}`);
      if (url === '/api/v1/agent/list') return jsonResponse(agentListResponse);
      if (url === '/api/v1/config/settings/get') {
        return jsonResponse({ code: -1, msg: 'settings snapshot unavailable' });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    });

    render(<ACPSettings />);

    expect(await screen.findByRole('alert')).toHaveTextContent('settings snapshot unavailable');
    expect(screen.queryByTestId('acp-settings-save-button')).toBeNull();
    expect(screen.queryByDisplayValue('http_proxy')).toBeNull();
    await waitFor(() => {
      expect(vi.mocked(fetch).mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);
    });
  });

  it('saves only the Agent whose defaults were changed', async () => {
    const user = userEvent.setup();
    const writes: string[] = [];
    vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      if (url === '/api/v1/agent/list') {
        return jsonResponse({
          code: 0,
          agent_list: [
            {
              ...agentListResponse.agent_list[0],
              modes: {
                currentModeId: 'manual',
                availableModes: [
                  { id: 'manual', name: 'Manual' },
                  { id: 'auto', name: 'Auto' },
                ],
              },
            },
            {
              ...agentListResponse.agent_list[0],
              agent_id: 'agent-two',
              type: 'agent-two',
              display_name: 'Agent Two',
            },
          ],
        });
      }
      if (url === '/api/v1/config/settings/get') {
        return jsonResponse({ code: 0, settings: { agent_prefs: {} } });
      }
      if (init?.method === 'PUT') {
        writes.push(url);
        return jsonResponse({ code: 0 });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    });

    render(<AgentDefaultsSettings />);

    const save = await screen.findByTestId('agent-defaults-save-button');
    expect(save).toBeDisabled();
    await user.selectOptions(screen.getByRole('combobox'), 'auto');
    expect(save).toBeEnabled();
    await user.click(save);

    expect(await screen.findByText('Saved successfully')).toBeTruthy();
    expect(writes).toEqual(['/api/v1/config/settings/agent/agent-one/prefs']);
    expect(save).toBeDisabled();
  });

  it('keeps only failed Agent defaults dirty after a partial save', async () => {
    const user = userEvent.setup();
    const writes: string[] = [];
    let failAgentTwo = true;
    vi.mocked(fetch).mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input instanceof Request ? input.url : String(input);
      if (url === '/api/v1/agent/list') {
        return jsonResponse({
          code: 0,
          agent_list: [
            {
              ...agentListResponse.agent_list[0],
              modes: {
                currentModeId: 'manual',
                availableModes: [
                  { id: 'manual', name: 'Manual' },
                  { id: 'auto', name: 'Auto' },
                ],
              },
            },
            {
              ...agentListResponse.agent_list[0],
              agent_id: 'agent-two',
              type: 'agent-two',
              display_name: 'Agent Two',
              modes: {
                currentModeId: 'manual',
                availableModes: [
                  { id: 'manual', name: 'Manual' },
                  { id: 'auto', name: 'Auto' },
                ],
              },
            },
          ],
        });
      }
      if (url === '/api/v1/config/settings/get') {
        return jsonResponse({ code: 0, settings: { agent_prefs: {} } });
      }
      if (init?.method === 'PUT') {
        writes.push(url);
        if (url.endsWith('/agent-two/prefs') && failAgentTwo) {
          return jsonResponse({ code: -1, msg: 'agent two save failed' }, { status: 500 });
        }
        return jsonResponse({ code: 0 });
      }
      throw new Error(`Unexpected fetch: ${url}`);
    });

    render(<AgentDefaultsSettings />);

    const save = await screen.findByTestId('agent-defaults-save-button');
    await user.selectOptions(screen.getByRole('combobox'), 'auto');
    await user.click(screen.getByText('Agent Two'));
    await user.selectOptions(screen.getByRole('combobox'), 'auto');
    await user.click(save);

    expect(await screen.findByText(/agent two save failed/)).toBeTruthy();
    expect(writes).toEqual([
      '/api/v1/config/settings/agent/agent-one/prefs',
      '/api/v1/config/settings/agent/agent-two/prefs',
    ]);

    failAgentTwo = false;
    await user.click(save);
    expect(await screen.findByText('Saved successfully')).toBeTruthy();
    expect(writes).toEqual([
      '/api/v1/config/settings/agent/agent-one/prefs',
      '/api/v1/config/settings/agent/agent-two/prefs',
      '/api/v1/config/settings/agent/agent-two/prefs',
    ]);
    expect(save).toBeDisabled();
  });
});
