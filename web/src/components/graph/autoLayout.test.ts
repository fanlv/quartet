import { describe, expect, it } from 'vitest';
import type { QuartetFlowEdge, QuartetFlowNode } from './graphFlowAdapter';
import { LOOP_DEFAULT_HEIGHT, LOOP_DEFAULT_WIDTH } from './graphFlowAdapter';
import { computeAutoLayout } from './autoLayout';

// A loop container followed by a downstream node: the downstream column's x is
// origin(40) + column-0 width(the loop box) + H_GAP(90).
function loopThenNode(loop: Partial<QuartetFlowNode>): {
  nodes: QuartetFlowNode[];
  edges: QuartetFlowEdge[];
} {
  const loopNode: QuartetFlowNode = {
    id: 'loop-1',
    type: 'loopGroup',
    position: { x: 0, y: 0 },
    data: { kind: 'loop', graphNode: { id: 'loop-1', type: 'loop', title: 'L' } },
    ...loop,
  } as QuartetFlowNode;
  const shellNode: QuartetFlowNode = {
    id: 'shell-1',
    type: 'quartet',
    position: { x: 0, y: 0 },
    data: { kind: 'shell', graphNode: { id: 'shell-1', type: 'shell', title: 'S' } },
  } as QuartetFlowNode;
  const edge: QuartetFlowEdge = { id: 'e1', source: 'loop-1', target: 'shell-1' };
  return { nodes: [loopNode, shellNode], edges: [edge] };
}

describe('computeAutoLayout loop container size', () => {
  it('uses the post-resize node.width/height over style', () => {
    const { nodes, edges } = loopThenNode({
      // NodeResizer writes the live size to width/height; style keeps the seed.
      width: 1000,
      height: 800,
      style: { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT },
    });
    const positions = computeAutoLayout(nodes, edges);
    expect(positions.get('shell-1')?.x).toBe(40 + 1000 + 90);
  });

  it('falls back to measured size when no explicit width/height', () => {
    const { nodes, edges } = loopThenNode({
      measured: { width: 900, height: 700 },
      style: { width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT },
    });
    const positions = computeAutoLayout(nodes, edges);
    expect(positions.get('shell-1')?.x).toBe(40 + 900 + 90);
  });

  it('falls back to style size when never resized', () => {
    const { nodes, edges } = loopThenNode({
      style: { width: 640, height: 400 },
    });
    const positions = computeAutoLayout(nodes, edges);
    expect(positions.get('shell-1')?.x).toBe(40 + 640 + 90);
  });
});
