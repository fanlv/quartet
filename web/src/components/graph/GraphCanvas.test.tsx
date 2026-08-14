import type { ReactNode } from 'react';
import { render } from '@testing-library/react';
import '@testing-library/jest-dom/vitest';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { QuartetFlowNode } from './graphFlowAdapter';

// The jsdom camera itself is out of reach, so @xyflow/react is stubbed down to
// a recording useReactFlow: this pins WHEN GraphCanvas invokes fitView, which is
// the regression surface — the viewport effect must not re-fit on every
// nodes.length change (run-version editing has no saved viewport, so each
// add/delete would reset the user's pan/zoom).
const rfMock = {
  fitView: vi.fn(),
  setViewport: vi.fn(),
  screenToFlowPosition: (p: { x: number; y: number }) => p,
  getIntersectingNodes: () => [],
};

vi.mock('@xyflow/react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@xyflow/react')>();
  return {
    ...actual,
    ReactFlowProvider: ({ children }: { children?: ReactNode }) => <>{children}</>,
    ReactFlow: () => <div data-testid="rf-stub" />,
    useReactFlow: () => rfMock,
  };
});

// Imported AFTER the mock registration so GraphCanvas sees the stubbed module.
const { GraphCanvas } = await import('./GraphCanvas');

function makeNode(id: string): QuartetFlowNode {
  return {
    id,
    type: 'quartet',
    position: { x: 0, y: 0 },
    data: { kind: 'shell', graphNode: { id, type: 'shell', title: id } },
  } as QuartetFlowNode;
}

function renderCanvas(nodes: QuartetFlowNode[]) {
  return render(
    <GraphCanvas
      nodes={nodes}
      edges={[]}
      onNodesChange={() => {}}
      onEdgesChange={() => {}}
      onConnect={() => {}}
      onNodeClick={() => {}}
      onPaneClick={() => {}}
      onAddNode={() => {}}
    />,
  );
}

describe('GraphCanvas initial viewport effect', () => {
  beforeEach(() => {
    rfMock.fitView.mockClear();
    rfMock.setViewport.mockClear();
  });

  it('fits once on first data, not again when nodes are added or removed', () => {
    const { rerender } = renderCanvas([]);

    // No fit until the first nodes arrive.
    expect(rfMock.fitView).not.toHaveBeenCalled();

    rerender(
      <GraphCanvas
        nodes={[makeNode('n1'), makeNode('n2')]}
        edges={[]}
        onNodesChange={() => {}}
        onEdgesChange={() => {}}
        onConnect={() => {}}
        onNodeClick={() => {}}
        onPaneClick={() => {}}
        onAddNode={() => {}}
      />,
    );
    expect(rfMock.fitView).toHaveBeenCalledTimes(1);

    // Adding / removing nodes must NOT re-fit and yank the user's camera.
    rerender(
      <GraphCanvas
        nodes={[makeNode('n1'), makeNode('n2'), makeNode('n3')]}
        edges={[]}
        onNodesChange={() => {}}
        onEdgesChange={() => {}}
        onConnect={() => {}}
        onNodeClick={() => {}}
        onPaneClick={() => {}}
        onAddNode={() => {}}
      />,
    );
    rerender(
      <GraphCanvas
        nodes={[makeNode('n1')]}
        edges={[]}
        onNodesChange={() => {}}
        onEdgesChange={() => {}}
        onConnect={() => {}}
        onNodeClick={() => {}}
        onPaneClick={() => {}}
        onAddNode={() => {}}
      />,
    );
    expect(rfMock.fitView).toHaveBeenCalledTimes(1);
  });
});
