import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { GraphConfig, GraphRun, GraphWorkflow, GraphWorkflowSummary } from '../types/graph';
import i18n from '../i18n';
import { GraphWorkflowPage } from './GraphWorkflowPage';

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function makeConfig(uniqueNodeId: string): GraphConfig {
  return {
    nodes: [
      { id: 'start', type: 'start', layout: { x: 0, y: 0 } },
      { id: uniqueNodeId, type: 'shell', title: uniqueNodeId, config: { script: 'echo hi' }, layout: { x: 220, y: 0 } },
      { id: 'end', type: 'end', layout: { x: 440, y: 0 } },
    ],
    edges: [
      { id: 'e1', sourceNodeId: 'start', targetNodeId: uniqueNodeId },
      { id: 'e2', sourceNodeId: uniqueNodeId, targetNodeId: 'end' },
    ],
    variables: {},
    disabledVars: [],
    runConfig: { concurrencyLimit: 8 },
  };
}

const workflowA: GraphWorkflow = {
  id: 'wf-a',
  name: 'Workflow A',
  type: 'user',
  config: makeConfig('unique-node-a'),
  createdAt: '2026-01-01T00:00:00Z',
  updatedAt: '2026-01-01T00:00:00Z',
};
const workflowB: GraphWorkflow = {
  id: 'wf-b',
  name: 'Workflow B',
  type: 'user',
  config: makeConfig('unique-node-b'),
  createdAt: '2026-01-02T00:00:00Z',
  updatedAt: '2026-01-02T00:00:00Z',
};

function summaryOf(wf: GraphWorkflow): GraphWorkflowSummary {
  return {
    id: wf.id,
    name: wf.name,
    type: wf.type,
    createdAt: wf.createdAt,
    updatedAt: wf.updatedAt,
    nodeCount: wf.config.nodes.length,
    edgeCount: wf.config.edges.length,
  };
}

function stubBaseFetch(extra?: (url: string) => Response | null) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input instanceof Request ? input.url : String(input);
      const hit = extra?.(url);
      if (hit) return hit;
      if (url.endsWith('/api/v1/graph/workflow/list')) {
        return jsonResponse({ workflows: [summaryOf(workflowA), summaryOf(workflowB)] });
      }
      if (url.endsWith('/api/v1/graph/workflow/wf-a')) return jsonResponse({ workflow: workflowA });
      if (url.endsWith('/api/v1/graph/workflow/wf-b')) return jsonResponse({ workflow: workflowB });
      if (url.endsWith('/api/v1/agent/list')) return jsonResponse({ code: 0, agent_list: [] });
      if (url.endsWith('/api/v1/workspace/list')) return jsonResponse({ workspaces: [] });
      if (url.includes('/api/v1/job/') && url.endsWith('/viewer-state')) {
        return jsonResponse({ code: 0, applied: true });
      }
      throw new Error(`Unexpected fetch in test: ${url}`);
    }),
  );
}

function renderPage() {
  return render(<GraphWorkflowPage workspaceId="ws-1" onClose={() => {}} onRunStarted={() => {}} />);
}

afterEach(() => {
  window.history.replaceState({}, '', '/');
});

describe('GraphWorkflowPage JSON view lifecycle', () => {
  it('resets the JSON view when switching to another workflow', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true));
    stubBaseFetch();
    renderPage();

    fireEvent.click(await screen.findByTestId('graph-workflow-row-wf-a'));
    await waitFor(() => expect(screen.getByTestId('graph-name-input')).toHaveValue('Workflow A'));

    fireEvent.click(screen.getByTestId('graph-view-json'));
    const textarea = screen.getByTestId('graph-json-textarea') as HTMLTextAreaElement;
    expect(textarea.value).toContain('unique-node-a');

    fireEvent.click(screen.getByTestId('graph-workflow-row-wf-b'));
    await waitFor(() => expect(screen.getByTestId('graph-name-input')).toHaveValue('Workflow B'));

    // Workflow A's JSON draft must not survive into workflow B's editor: an
    // "Apply to canvas" on the stale text would persist A's graph under B's name.
    expect(screen.queryByTestId('graph-json-textarea')).not.toBeInTheDocument();
    expect(screen.queryByTestId('graph-dirty-badge')).not.toBeInTheDocument();
  });

  it('resets the JSON view when starting a new workflow', async () => {
    stubBaseFetch();
    renderPage();

    fireEvent.click(await screen.findByTestId('graph-workflow-row-wf-a'));
    await waitFor(() => expect(screen.getByTestId('graph-name-input')).toHaveValue('Workflow A'));

    fireEvent.click(screen.getByTestId('graph-view-json'));
    expect((screen.getByTestId('graph-json-textarea') as HTMLTextAreaElement).value).toContain('unique-node-a');

    fireEvent.click(screen.getByRole('button', { name: 'New workflow' }));

    expect(screen.getByTestId('graph-name-input')).toHaveValue(i18n.t('graph.defaultName'));
    expect(screen.queryByTestId('graph-json-textarea')).not.toBeInTheDocument();
    expect(screen.queryByTestId('graph-dirty-badge')).not.toBeInTheDocument();
  });
});

describe('GraphWorkflowPage run view', () => {
  function stubRunFetch() {
    const runConfig = makeConfig('run-node');
    const completedRun: GraphRun = {
      id: 'run-1',
      jobId: 'job-1',
      status: 'completed',
      baseSnapshot: { config: runConfig, capturedAt: 0 },
      versions: [{ version: 1, config: runConfig, createdAt: 0 }],
      currentVersion: 1,
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    };
    stubBaseFetch((url) => {
      if (url.endsWith('/api/v1/job/job-1/graph-run')) {
        return jsonResponse({
          run: completedRun,
          progress: { totalCount: 0, completedCount: 0, failedCount: 0, skippedCount: 0, interruptedCount: 0, runningCount: 0 },
          instances: [],
          edges: [],
        });
      }
      if (url.endsWith('/api/v1/job/job-1/graph-run/hooks')) return jsonResponse({ results: [] });
      return null;
    });
  }

  it('locks the view toggle in read-only replay', async () => {
    window.history.replaceState({}, '', '/?graphEditJob=job-1');
    stubRunFetch();
    renderPage();

    // A completed run is read-only: openRunInEdit loads it and reports why.
    const stripMessage = await screen.findByTestId('graph-run-strip-message');
    expect(stripMessage).toHaveTextContent('This run is no longer editable.');

    // Pure replay must not offer the Canvas/JSON editing views.
    expect(screen.getByTestId('graph-view-json')).toBeDisabled();
    expect(screen.getByTestId('graph-view-canvas')).toBeDisabled();
  });

  it('clears runMessage when exiting run replay', async () => {
    window.history.replaceState({}, '', '/?graphEditJob=job-1');
    stubRunFetch();
    renderPage();

    // A completed run is read-only: openRunInEdit loads it and reports why.
    const stripMessage = await screen.findByTestId('graph-run-strip-message');
    expect(stripMessage).toHaveTextContent('This run is no longer editable.');

    fireEvent.click(screen.getByTestId('graph-exit-run'));

    // The run-scoped message must not leak into the editor status area.
    await waitFor(() => expect(screen.queryByTestId('graph-run-strip-status')).not.toBeInTheDocument());
    expect(screen.queryByTestId('graph-run-message')).not.toBeInTheDocument();
  });
});
