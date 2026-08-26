import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { clearDeletedAgentLocalPreferences } from '../../utils/workspace';
import { StatsPage } from '../stats/StatsPage';
import { AgentInstallSettings } from './AgentInstallSettings';

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  });
}

function catalogAgent(agentId: string, overrides: Record<string, unknown> = {}) {
  return {
    agent_id: agentId,
    source: 'builtin',
    display_name: agentId.replaceAll('-', ' '),
    icon_url: '🤖',
    definition: { bin: agentId, acp_program: agentId, acp_args: [] },
    supports_headless_print: false,
    deprecated: false,
    lifecycle: 'active',
    current_revision: 'revision-1',
    install_method: 'npm',
    install_commands: [`npm install -g ${agentId}`],
    auto_installable: true,
    auto_uninstallable: true,
    installed: true,
    availability: 'available',
    ...overrides,
  };
}

function versionInfo(agentId: string, overrides: Record<string, unknown> = {}) {
  return {
    agent_id: agentId,
    components: [{
      name: `${agentId}-package`,
      kind: 'npm',
      current_version: '1.0.0',
      latest_version: '2.0.0',
      update_available: true,
    }],
    update_available: true,
    upgrade_supported: true,
    ...overrides,
  };
}

function installResult(agentId: string, overrides: Record<string, unknown> = {}) {
  return {
    agent_id: agentId,
    steps: [{
      display: `upgrade ${agentId}`,
      stdout: `${agentId} upgraded`,
      stderr: '',
      exit_code: 0,
      timed_out: false,
      duration_ms: 25,
    }],
    installed: true,
    validation: { ok: true },
    ...overrides,
  };
}

function fetchURL(input: RequestInfo | URL): string {
  return input instanceof Request ? input.url : String(input);
}

function requestPath(input: RequestInfo | URL): string {
  const url = fetchURL(input);
  return url.startsWith('http') ? `${new URL(url).pathname}${new URL(url).search}` : url;
}

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

describe('AgentInstallSettings batch upgrades', () => {
  it('offers one-click update only for installed, supported, non-deprecated built-in candidates', async () => {
    const agents = [
      catalogAgent('eligible-agent', { display_name: 'Eligible Agent' }),
      catalogAgent('custom-agent', { source: 'custom', display_name: 'Custom Agent' }),
      catalogAgent('deprecated-agent', { deprecated: true }),
      catalogAgent('missing-agent', { installed: false, availability: 'not_installed' }),
      catalogAgent('unsupported-agent'),
      catalogAgent('current-agent'),
    ];
    vi.mocked(fetch).mockImplementation(async (input) => {
      switch (requestPath(input)) {
        case '/api/v1/agent/catalog':
          return jsonResponse({ code: 0, agents });
        case '/api/v1/agent/catalog/deleted':
          return jsonResponse({ code: 0, agents: [] });
        case '/api/v1/agent/versions':
          return jsonResponse({
            code: 0,
            checked_at: 10,
            agents: [
              versionInfo('eligible-agent'),
              versionInfo('custom-agent'),
              versionInfo('deprecated-agent'),
              versionInfo('missing-agent'),
              versionInfo('unsupported-agent', { upgrade_supported: false }),
              versionInfo('current-agent', { update_available: false }),
            ],
          });
        default:
          throw new Error(`Unexpected fetch: ${requestPath(input)}`);
      }
    });

    render(<AgentInstallSettings />);

    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    expect(updateAll).toBeEnabled();
    expect(updateAll).toHaveTextContent('Update all');
    expect(screen.getByText('1 Agent update available')).toBeInTheDocument();
    expect(screen.getAllByTestId('agent-upgrade-button')).toHaveLength(1);
    expect(within(screen.getByText('Eligible Agent').closest('[data-testid="agent-install-card"]')!).getByTestId('agent-upgrade-button')).toBeInTheDocument();
  });

  it('runs candidates strictly in catalog order, preserves per-agent outcomes, and refreshes once at the batch end', async () => {
    const user = userEvent.setup();
    const firstUpgrade = deferred<Response>();
    const secondUpgrade = deferred<Response>();
    const calls: string[] = [];
    let catalogLoads = 0;
    let versionLoads = 0;
    const agents = [
      catalogAgent('first-agent', { display_name: 'First Agent' }),
      catalogAgent('second-agent', { display_name: 'Second Agent' }),
    ];

    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = requestPath(input);
      calls.push(`${init?.method || 'GET'} ${path}`);
      if (path === '/api/v1/agent/catalog') {
        catalogLoads += 1;
        return jsonResponse({ code: 0, agents });
      }
      if (path === '/api/v1/agent/catalog/deleted') {
        return jsonResponse({ code: 0, agents: [] });
      }
      if (path === '/api/v1/agent/versions' || path === '/api/v1/agent/versions?force=1') {
        versionLoads += 1;
        return jsonResponse({
          code: 0,
          checked_at: versionLoads,
          agents: [versionInfo('first-agent'), versionInfo('second-agent')],
        });
      }
      if (path === '/api/v1/agent/first-agent/upgrade') return firstUpgrade.promise;
      if (path === '/api/v1/agent/second-agent/upgrade') return secondUpgrade.promise;
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    await waitFor(() => expect(updateAll).toBeEnabled());
    expect(catalogLoads).toBe(1);
    expect(versionLoads).toBe(1);

    await user.click(updateAll);

    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('First Agent (first-agent)'));
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('Second Agent (second-agent)'));
    expect(await screen.findByText('Completed 0 / 2')).toBeInTheDocument();
    expect(screen.getByText('Updating First Agent')).toBeInTheDocument();
    expect(updateAll).toBeDisabled();
    expect(screen.getByTestId('agent-add-button')).toBeDisabled();
    expect(calls.filter((call) => call.includes('/upgrade'))).toEqual([
      'POST /api/v1/agent/first-agent/upgrade',
    ]);

    firstUpgrade.resolve(jsonResponse({
      code: 0,
      result: installResult('first-agent', {
        steps: [{
          display: 'upgrade first-agent',
          stdout: '',
          stderr: 'registry returned a broken package',
          exit_code: 1,
          timed_out: false,
          duration_ms: 25,
        }],
        install_error: 'complete first agent failure',
        validation: undefined,
      }),
    }));

    await waitFor(() => expect(calls.filter((call) => call.includes('/upgrade'))).toEqual([
      'POST /api/v1/agent/first-agent/upgrade',
      'POST /api/v1/agent/second-agent/upgrade',
    ]));
    expect(catalogLoads).toBe(1);
    expect(versionLoads).toBe(1);
    expect(screen.getByText('Updating Second Agent')).toBeInTheDocument();

    secondUpgrade.resolve(jsonResponse({ code: 0, result: installResult('second-agent') }));

    expect(await screen.findByText(/First Agent · Upgrade failed/)).toBeInTheDocument();
    expect(await screen.findByText(/Second Agent · Upgraded and validated/)).toBeInTheDocument();
    expect(screen.getByText(/First Agent · Upgrade failed/).closest('[role="alert"]')).toHaveAttribute('aria-live', 'assertive');
    expect(screen.getByText(/Second Agent · Upgraded and validated/).closest('[role="status"]')).toHaveAttribute('aria-live', 'polite');
    expect(screen.getByText('complete first agent failure')).toBeInTheDocument();
    await waitFor(() => {
      expect(catalogLoads).toBe(2);
      expect(versionLoads).toBe(2);
      expect(screen.queryByTestId('agent-batch-upgrade-progress')).not.toBeInTheDocument();
    });
    expect(calls.filter((call) => call === 'GET /api/v1/agent/versions?force=1')).toHaveLength(1);
  });

  it('does not start or refresh a batch when its summary confirmation is cancelled', async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    const agent = catalogAgent('cancelled-agent', { display_name: 'Cancelled Agent' });
    const confirm = vi.spyOn(window, 'confirm')
      .mockReturnValueOnce(false)
      .mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input, init) => {
      const path = requestPath(input);
      calls.push(`${init?.method || 'GET'} ${path}`);
      if (path === '/api/v1/agent/catalog') return jsonResponse({ code: 0, agents: [agent] });
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions') {
        return jsonResponse({ code: 0, checked_at: 10, agents: [versionInfo('cancelled-agent')] });
      }
      if (path === '/api/v1/agent/versions?force=1') {
        return jsonResponse({ code: 0, checked_at: 11, agents: [] });
      }
      if (path === '/api/v1/agent/cancelled-agent/upgrade') {
        return jsonResponse({ code: 0, result: installResult('cancelled-agent') });
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    await waitFor(() => expect(updateAll).toBeEnabled());
    await user.click(updateAll);

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(calls.some((call) => call.includes('/upgrade'))).toBe(false);
    expect(calls).not.toContain('GET /api/v1/agent/versions?force=1');
    expect(screen.queryByTestId('agent-batch-upgrade-progress')).not.toBeInTheDocument();
    expect(updateAll).toBeEnabled();

    await user.click(updateAll);
    expect(await screen.findByText(/Cancelled Agent · Upgraded and validated/)).toBeInTheDocument();
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(calls).toContain('POST /api/v1/agent/cancelled-agent/upgrade');
    expect(calls.filter((call) => call === 'GET /api/v1/agent/versions?force=1')).toHaveLength(1);
  });

  it('allows only one batch to start when the action is triggered twice in one run loop', async () => {
    const upgradeRequest = deferred<Response>();
    const agent = catalogAgent('double-trigger-agent', { display_name: 'Double Trigger Agent' });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const upgradeCalls: string[] = [];
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') return jsonResponse({ code: 0, agents: [agent] });
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions' || path === '/api/v1/agent/versions?force=1') {
        return jsonResponse({ code: 0, checked_at: 10, agents: [versionInfo('double-trigger-agent')] });
      }
      if (path === '/api/v1/agent/double-trigger-agent/upgrade') {
        upgradeCalls.push(path);
        return upgradeRequest.promise;
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    await waitFor(() => expect(updateAll).toBeEnabled());

    act(() => {
      updateAll.click();
      updateAll.click();
    });

    await waitFor(() => expect(upgradeCalls).toHaveLength(1));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(updateAll).toBeDisabled();
    expect(updateAll).toHaveClass('agent-upgrade-all-btn');
    expect(screen.getByTestId('agent-upgrade-button')).toBeDisabled();
    expect(screen.getByTestId('agent-upgrade-button')).toHaveClass('agent-upgrade-btn');
    await act(async () => {
      upgradeRequest.resolve(jsonResponse({ code: 0, result: installResult('double-trigger-agent') }));
    });
    await waitFor(() => expect(screen.queryByTestId('agent-batch-upgrade-progress')).not.toBeInTheDocument());
  });

  it('keeps request context for a transport error, continues, and stops remaining work on 409', async () => {
    const user = userEvent.setup();
    const upgradeCalls: string[] = [];
    const forcedVersionRefreshes: string[] = [];
    const agents = [
      catalogAgent('network-agent', { display_name: 'Network Agent' }),
      catalogAgent('busy-agent', { display_name: 'Busy Agent' }),
      catalogAgent('skipped-agent', { display_name: 'Skipped Agent' }),
    ];
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') return jsonResponse({ code: 0, agents });
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions' || path === '/api/v1/agent/versions?force=1') {
        if (path.endsWith('?force=1')) forcedVersionRefreshes.push(path);
        return jsonResponse({
          code: 0,
          checked_at: 10,
          agents: agents.map((agent) => versionInfo(agent.agent_id)),
        });
      }
      if (path.endsWith('/upgrade')) {
        upgradeCalls.push(path);
        if (path.includes('network-agent')) throw new Error('network link failed in full detail');
        if (path.includes('busy-agent')) {
          return jsonResponse({ code: -1, msg: 'another agent install is already in progress' }, { status: 409 });
        }
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    await user.click(await screen.findByTestId('agent-upgrade-all-button'));

    expect(await screen.findByText(/POST \/api\/v1\/agent\/network-agent\/upgrade[\s\S]*network link failed in full detail/)).toBeInTheDocument();
    expect(await screen.findByText(/POST \/api\/v1\/agent\/busy-agent\/upgrade[\s\S]*HTTP 409[\s\S]*another agent install is already in progress/)).toBeInTheDocument();
    expect(upgradeCalls).toEqual([
      '/api/v1/agent/network-agent/upgrade',
      '/api/v1/agent/busy-agent/upgrade',
    ]);
    expect(upgradeCalls).not.toContain('/api/v1/agent/skipped-agent/upgrade');
    await waitFor(() => expect(screen.queryByTestId('agent-batch-upgrade-progress')).not.toBeInTheDocument());
    expect(forcedVersionRefreshes).toHaveLength(1);
  });

  it('retains complete errors for malformed successful responses without rendering invalid results', async () => {
    const user = userEvent.setup();
    const upgradeCalls: string[] = [];
    const agents = [
      catalogAgent('missing-result-agent', { display_name: 'Missing Result Agent' }),
      catalogAgent('invalid-steps-agent', { display_name: 'Invalid Steps Agent' }),
      catalogAgent('invalid-installed-agent', { display_name: 'Invalid Installed Agent' }),
      catalogAgent('empty-agent-id-result', { display_name: 'Empty Agent ID Result' }),
      catalogAgent('wrong-agent-id', { display_name: 'Wrong Agent ID' }),
      catalogAgent('invalid-step-fields', { display_name: 'Invalid Step Fields' }),
      catalogAgent('invalid-step-error', { display_name: 'Invalid Step Error' }),
      catalogAgent('invalid-install-error', { display_name: 'Invalid Install Error' }),
      catalogAgent('invalid-validation', { display_name: 'Invalid Validation' }),
    ];
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') return jsonResponse({ code: 0, agents });
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions' || path === '/api/v1/agent/versions?force=1') {
        return jsonResponse({
          code: 0,
          checked_at: 10,
          agents: agents.map((agent) => versionInfo(agent.agent_id)),
        });
      }
      if (path.endsWith('/upgrade')) upgradeCalls.push(path);
      if (path.includes('missing-result-agent')) {
        return jsonResponse({ code: 0, diagnostic: 'missing-result-full-response' });
      }
      if (path.includes('invalid-steps-agent')) {
        return jsonResponse({
          code: 0,
          result: {
            agent_id: 'invalid-steps-agent',
            steps: 'not-an-array',
            installed: true,
            diagnostic: 'invalid-steps-full-response',
          },
        });
      }
      if (path.includes('invalid-installed-agent')) {
        return jsonResponse({
          code: 0,
          result: {
            agent_id: 'invalid-installed-agent',
            steps: [],
            installed: 'yes',
            diagnostic: 'invalid-installed-full-response',
          },
        });
      }
      if (path.includes('empty-agent-id-result')) {
        return jsonResponse({
          code: 0,
          result: installResult('   ', { diagnostic: 'empty-agent-id-full-response' }),
        });
      }
      if (path.includes('wrong-agent-id')) {
        return jsonResponse({
          code: 0,
          result: installResult('different-agent', { diagnostic: 'wrong-agent-full-response' }),
        });
      }
      if (path.includes('invalid-step-error')) {
        return jsonResponse({
          code: 0,
          result: installResult('invalid-step-error', {
            steps: [{
              display: 'upgrade invalid-step-error',
              stdout: '',
              stderr: '',
              exit_code: 0,
              timed_out: false,
              duration_ms: 25,
              error: null,
            }],
            diagnostic: 'invalid-step-error-full-response',
          }),
        });
      }
      if (path.includes('invalid-step-fields')) {
        return jsonResponse({
          code: 0,
          result: installResult('invalid-step-fields', {
            steps: [{
              display: 'upgrade invalid-step-fields',
              stdout: '',
              stderr: '',
              exit_code: 'zero',
              timed_out: false,
              duration_ms: 25,
              error: null,
            }],
            diagnostic: 'invalid-step-fields-full-response',
          }),
        });
      }
      if (path.includes('invalid-install-error')) {
        return jsonResponse({
          code: 0,
          result: installResult('invalid-install-error', {
            install_error: null,
            diagnostic: 'invalid-install-error-full-response',
          }),
        });
      }
      if (path.includes('invalid-validation')) {
        return jsonResponse({
          code: 0,
          result: installResult('invalid-validation', {
            validation: { ok: 'yes', error: null },
            diagnostic: 'invalid-validation-full-response',
          }),
        });
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    await waitFor(() => expect(updateAll).toBeEnabled());
    await user.click(updateAll);

    await waitFor(() => expect(screen.getAllByTestId('agent-install-request-feedback')).toHaveLength(9));
    const errorText = screen.getAllByTestId('agent-install-request-feedback')
      .map((feedback) => feedback.textContent)
      .join('\n');
    expect(errorText).toContain('Invalid response shape: upgrade result must be an object');
    expect(errorText).toContain('missing-result-full-response');
    expect(errorText).toContain('Invalid response shape: upgrade result.steps must be an array');
    expect(errorText).toContain('invalid-steps-full-response');
    expect(errorText).toContain('Invalid response shape: upgrade result.installed must be a boolean');
    expect(errorText).toContain('invalid-installed-full-response');
    expect(errorText).toContain('Invalid response shape: upgrade result.agent_id must be a non-empty string');
    expect(errorText).toContain('empty-agent-id-full-response');
    expect(errorText).toContain('Invalid response shape: upgrade result.agent_id must equal "wrong-agent-id"');
    expect(errorText).toContain('wrong-agent-full-response');
    expect(errorText).toContain('Invalid response shape: upgrade result.steps contain invalid fields');
    expect(errorText).toContain('invalid-step-fields-full-response');
    expect(errorText).toContain('invalid-step-error-full-response');
    expect(errorText).toContain('Invalid response shape: upgrade result.install_error must be a string when present');
    expect(errorText).toContain('invalid-install-error-full-response');
    expect(errorText).toContain('Invalid response shape: upgrade result.validation has an invalid shape');
    expect(errorText).toContain('invalid-validation-full-response');
    expect(errorText).toContain('POST /api/v1/agent/invalid-validation/upgrade');
    expect(screen.queryByText(/Upgraded and validated/)).not.toBeInTheDocument();
    expect(upgradeCalls).toEqual([
      '/api/v1/agent/missing-result-agent/upgrade',
      '/api/v1/agent/invalid-steps-agent/upgrade',
      '/api/v1/agent/invalid-installed-agent/upgrade',
      '/api/v1/agent/empty-agent-id-result/upgrade',
      '/api/v1/agent/wrong-agent-id/upgrade',
      '/api/v1/agent/invalid-step-fields/upgrade',
      '/api/v1/agent/invalid-step-error/upgrade',
      '/api/v1/agent/invalid-install-error/upgrade',
      '/api/v1/agent/invalid-validation/upgrade',
    ]);
  });

  it('clears stale update candidates when the forced refresh after a batch fails', async () => {
    const user = userEvent.setup();
    const agent = catalogAgent('stale-agent', { display_name: 'Stale Agent' });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') return jsonResponse({ code: 0, agents: [agent] });
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions') {
        return jsonResponse({ code: 0, checked_at: 10, agents: [versionInfo('stale-agent')] });
      }
      if (path === '/api/v1/agent/stale-agent/upgrade') {
        return jsonResponse({ code: 0, result: installResult('stale-agent') });
      }
      if (path === '/api/v1/agent/versions?force=1') {
        return jsonResponse(
          { code: -1, msg: 'forced version refresh failed with full detail' },
          { status: 500 },
        );
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    await waitFor(() => expect(updateAll).toBeEnabled());
    await user.click(updateAll);

    expect(await screen.findByText(/GET \/api\/v1\/agent\/versions\?force=1[\s\S]*HTTP 500[\s\S]*forced version refresh failed with full detail/)).toBeInTheDocument();
    await waitFor(() => expect(updateAll).toBeDisabled());
    expect(screen.queryByTestId('agent-upgrade-button')).not.toBeInTheDocument();
    expect(screen.queryByText('1 Agent update available')).not.toBeInTheDocument();
  });

  it('keeps completed results and request errors visible when the final catalog refresh fails', async () => {
    const user = userEvent.setup();
    const agents = [
      catalogAgent('result-agent', { display_name: 'Result Agent' }),
      catalogAgent('error-agent', { display_name: 'Error Agent' }),
    ];
    let catalogLoads = 0;
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') {
        catalogLoads += 1;
        if (catalogLoads === 1) return jsonResponse({ code: 0, agents });
        throw new Error('catalog connection failed after upgrades');
      }
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions' || path === '/api/v1/agent/versions?force=1') {
        return jsonResponse({ code: 0, checked_at: 10, agents: agents.map((agent) => versionInfo(agent.agent_id)) });
      }
      if (path === '/api/v1/agent/result-agent/upgrade') {
        return jsonResponse({ code: 0, result: installResult('result-agent') });
      }
      if (path === '/api/v1/agent/error-agent/upgrade') {
        throw new Error('upgrade transport error remains visible');
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    await waitFor(() => expect(updateAll).toBeEnabled());
    await user.click(updateAll);

    expect(await screen.findByText(/Result Agent · Upgraded and validated/)).toBeInTheDocument();
    expect(await screen.findByText(/POST \/api\/v1\/agent\/error-agent\/upgrade[\s\S]*upgrade transport error remains visible/)).toBeInTheDocument();
    expect(await screen.findByText(/GET \/api\/v1\/agent\/catalog[\s\S]*catalog connection failed after upgrades/)).toBeInTheDocument();
    expect(screen.getByText(/Result Agent · Upgraded and validated/)).toBeInTheDocument();
    expect(screen.getByText(/upgrade transport error remains visible/)).toBeInTheDocument();
  });

  it('keeps custom forms and upgrade controls mutually exclusive', async () => {
    const user = userEvent.setup();
    const upgradeRequest = deferred<Response>();
    const builtIn = catalogAgent('upgrade-agent', { display_name: 'Upgrade Agent' });
    const custom = catalogAgent('custom-agent', {
      source: 'custom',
      display_name: 'Custom Agent',
      auto_installable: false,
      auto_uninstallable: false,
    });
    const agents = [builtIn, custom];
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') return jsonResponse({ code: 0, agents });
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions' || path === '/api/v1/agent/versions?force=1') {
        return jsonResponse({ code: 0, checked_at: 10, agents: [versionInfo('upgrade-agent')] });
      }
      if (path === '/api/v1/agent/upgrade-agent/upgrade') return upgradeRequest.promise;
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const updateAll = await screen.findByTestId('agent-upgrade-all-button');
    await waitFor(() => expect(updateAll).toBeEnabled());

    await user.click(screen.getByTestId('agent-add-button'));
    expect(screen.getByTestId('agent-custom-save-button')).toBeEnabled();
    expect(updateAll).toBeDisabled();
    expect(screen.getByTestId('agent-upgrade-button')).toBeDisabled();
    expect(screen.getByTestId('agent-uninstall-button')).toBeDisabled();
    await user.click(updateAll);
    expect(confirm).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(updateAll);

    expect(await screen.findByTestId('agent-batch-upgrade-progress')).toBeInTheDocument();
    expect(screen.getByTestId('agent-add-button')).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Edit' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Check for updates' })).toBeDisabled();

    upgradeRequest.resolve(jsonResponse({ code: 0, result: installResult('upgrade-agent') }));
    await waitFor(() => expect(screen.queryByTestId('agent-batch-upgrade-progress')).not.toBeInTheDocument());
  });

  it('refreshes single upgrades after every outcome and replaces stale results before a retry', async () => {
    const user = userEvent.setup();
    const agent = catalogAgent('single-agent', { display_name: 'Single Agent' });
    let catalogLoads = 0;
    let versionLoads = 0;
    let upgradeCalls = 0;
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') {
        catalogLoads += 1;
        return jsonResponse({ code: 0, agents: [agent] });
      }
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions' || path === '/api/v1/agent/versions?force=1') {
        versionLoads += 1;
        return jsonResponse({ code: 0, checked_at: versionLoads, agents: [versionInfo('single-agent')] });
      }
      if (path === '/api/v1/agent/single-agent/upgrade') {
        upgradeCalls += 1;
        if (upgradeCalls === 1) {
          return jsonResponse({ code: 0, result: installResult('single-agent') });
        }
        throw new Error('response connection lost after server completion');
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);
    const upgradeButton = await screen.findByTestId('agent-upgrade-button');
    await user.click(upgradeButton);
    expect(await screen.findByText(/Single Agent · Upgraded and validated/)).toBeInTheDocument();
    await waitFor(() => {
      expect(catalogLoads).toBe(2);
      expect(versionLoads).toBe(2);
    });

    await user.click(upgradeButton);
    const feedback = await screen.findByTestId('agent-install-request-feedback');
    expect(feedback).toHaveTextContent('response connection lost after server completion');
    expect(screen.queryByText(/Single Agent · Upgraded and validated/)).not.toBeInTheDocument();
    await waitFor(() => {
      expect(catalogLoads).toBe(3);
      expect(versionLoads).toBe(3);
    });

    const retry = within(feedback).getByRole('button', { name: 'Try again' });
    expect(retry).toBeEnabled();
    await user.click(screen.getByTestId('agent-add-button'));
    expect(retry).toBeDisabled();
  });

  it('rejects malformed catalog entries with request context and the complete response body', async () => {
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') {
        return jsonResponse({ code: 0, agents: [{ agent_id: 'broken-catalog-agent' }], diagnostic: 'catalog-shape-full-response' });
      }
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions') return jsonResponse({ code: 0, checked_at: 10, agents: [] });
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);

    const error = await screen.findByText((content) => (
      content.includes('GET /api/v1/agent/catalog')
        && content.includes('Invalid response shape')
        && content.includes('catalog-shape-full-response')
    ));
    expect(error).toBeInTheDocument();
    expect(screen.queryByTestId('agent-install-card')).not.toBeInTheDocument();
  });

  it('rejects malformed version entries without crashing the rendered catalog', async () => {
    const agent = catalogAgent('safe-agent', { display_name: 'Safe Agent' });
    vi.mocked(fetch).mockImplementation(async (input) => {
      const path = requestPath(input);
      if (path === '/api/v1/agent/catalog') return jsonResponse({ code: 0, agents: [agent] });
      if (path === '/api/v1/agent/catalog/deleted') return jsonResponse({ code: 0, agents: [] });
      if (path === '/api/v1/agent/versions') {
        return jsonResponse({
          code: 0,
          checked_at: 10,
          agents: [{ agent_id: 'safe-agent', update_available: true, upgrade_supported: true }],
          diagnostic: 'version-shape-full-response',
        });
      }
      throw new Error(`Unexpected fetch: ${path}`);
    });

    render(<AgentInstallSettings />);

    expect(await screen.findByText('Safe Agent')).toBeInTheDocument();
    expect(await screen.findByText((content) => (
      content.includes('GET /api/v1/agent/versions')
        && content.includes('Invalid response shape')
        && content.includes('version-shape-full-response')
    ))).toBeInTheDocument();
    expect(screen.queryByTestId('agent-upgrade-button')).not.toBeInTheDocument();
  });
});

describe('StatsPage token trend accessibility', () => {
  it('keeps one daily point in the tab order and uses arrow keys to inspect adjacent days', async () => {
    const totals = (total: number, imageEstimate: number) => ({
      totalMs: 1_000,
      turnCount: 1,
      assistantCount: 1,
      thoughtCount: 0,
      toolCallCount: 0,
      tokens: {
        total,
        reported: total,
        input: total,
        output: 0,
        cachedRead: 0,
        cachedWrite: 0,
        reasoning: 0,
        imageEstimate,
        estimated: 0,
        reportedTurns: 1,
        estimatedTurns: 0,
        assistant: 0,
        thought: 0,
        toolCall: 0,
      },
    });
    vi.mocked(fetch).mockResolvedValue(jsonResponse({
      range: { from: '2026-08-24', to: '2026-08-26' },
      byWorkspace: [],
      byModel: [],
      byTool: [],
      daily: [
        { date: '2026-08-24', ...totals(100, 10), models: {} },
        { date: '2026-08-25', ...totals(200, 20), models: {} },
        { date: '2026-08-26', ...totals(300, 30), models: {} },
      ],
      note: 'stats.tokensLocalEstimateNote',
    }));

    render(<StatsPage onClose={vi.fn()} />);

    const chart = await screen.findByRole('group', { name: /Usage trend by day/ });
    const days = within(chart).getAllByRole('img');
    expect(days).toHaveLength(3);
    expect(days.map((day) => day.getAttribute('tabindex'))).toEqual(['0', '-1', '-1']);

    days[0].focus();
    expect(await screen.findByText('An explanatory subset already included in the input or estimated total.')).toBeInTheDocument();
    fireEvent.keyDown(days[0], { key: 'ArrowRight' });

    expect(document.activeElement).toBe(days[1]);
    expect(days.map((day) => day.getAttribute('tabindex'))).toEqual(['-1', '0', '-1']);
    expect(screen.getByRole('status')).toHaveTextContent('2026-08-25');
  });
});
