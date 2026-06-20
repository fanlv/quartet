import { describe, expect, it } from 'vitest';
import {
  LOOP_DEFAULT_HEIGHT,
  LOOP_DEFAULT_WIDTH,
  clearNestConstraint,
  configToFlow,
  flowToConfig,
  nestConstraint,
  runStatusByNode,
} from './graphFlowAdapter';
import type { GraphConfig, GraphInstanceState } from '../../types/graph';

const sampleConfig: GraphConfig = {
  nodes: [
    { id: 'start', type: 'start', title: '开始', layout: { x: 10, y: 20 } },
    {
      id: 'shell',
      type: 'shell',
      title: 'Shell',
      config: { script: 'echo hi', outputVariables: ['out'] },
      layout: { x: 200, y: 20 },
    },
    {
      id: 'branch',
      type: 'ifElse',
      title: '判断',
      config: { condition: '{{out}} == "ok"' },
      layout: { x: 400, y: 20 },
    },
    { id: 'okEnd', type: 'end', title: '完成', layout: { x: 600, y: 0 } },
    { id: 'noEnd', type: 'end', title: '终止', layout: { x: 600, y: 80 } },
  ],
  edges: [
    { id: 'e1', sourceNodeId: 'start', targetNodeId: 'shell' },
    { id: 'e2', sourceNodeId: 'shell', targetNodeId: 'branch' },
    { id: 'e3', sourceNodeId: 'branch', targetNodeId: 'okEnd', sourcePort: 'yes' },
    { id: 'e4', sourceNodeId: 'branch', targetNodeId: 'noEnd', sourcePort: 'no' },
  ],
  variables: { foo: 'bar' },
  disabledVars: ['baz'],
  runConfig: { concurrencyLimit: 4 },
  workspaceId: 'ws-1',
  workdir: '/tmp/work',
};

const loopConfig: GraphConfig = {
  nodes: [
    { id: 'start', type: 'start', layout: { x: 0, y: 0 } },
    {
      id: 'loop',
      type: 'loop',
      title: '循环',
      config: { loopMode: 'fixed', fixedCount: 3, maxIterations: 100 },
      layout: { x: 200, y: 0, width: 700, height: 400 },
    },
    {
      id: 'inner',
      type: 'shell',
      parentId: 'loop',
      config: { script: 'echo loop' },
      layout: { x: 40, y: 60 },
    },
    { id: 'innerStart', type: 'start', parentId: 'loop', layout: { x: 0, y: 180 } },
    { id: 'innerEnd', type: 'end', parentId: 'loop', layout: { x: 300, y: 60 } },
    { id: 'end', type: 'end', layout: { x: 1000, y: 0 } },
  ],
  edges: [
    { id: 'e1', sourceNodeId: 'start', targetNodeId: 'loop' },
    { id: 'e0', sourceNodeId: 'innerStart', targetNodeId: 'inner' },
    { id: 'e2', sourceNodeId: 'inner', targetNodeId: 'innerEnd' },
    { id: 'e3', sourceNodeId: 'loop', targetNodeId: 'end' },
  ],
};

describe('configToFlow', () => {
  it('maps nodes with positions and yes/no edge ports', () => {
    const { nodes, edges } = configToFlow(sampleConfig);
    expect(nodes).toHaveLength(5);
    const shell = nodes.find((n) => n.id === 'shell')!;
    expect(shell.type).toBe('quartet');
    expect(shell.position).toEqual({ x: 200, y: 20 });
    expect(shell.data.kind).toBe('shell');

    const yes = edges.find((e) => e.id === 'e3')!;
    expect(yes.sourceHandle).toBe('yes');
    expect(yes.label).toBe('YES');
    const no = edges.find((e) => e.id === 'e4')!;
    expect(no.sourceHandle).toBe('no');
    expect(no.label).toBe('NO');

    const plain = edges.find((e) => e.id === 'e1')!;
    expect(plain.sourceHandle).toBeUndefined();
  });

  it('maps loop containers with parentId and size; nested nodes are draggable out', () => {
    const { nodes } = configToFlow(loopConfig);
    const loop = nodes.find((n) => n.id === 'loop')!;
    expect(loop.type).toBe('loopGroup');
    expect(loop.style).toEqual({ width: 700, height: 400 });

    // A nested business node carries parentId but NO extent, so it can be
    // dragged back out of the loop (drag-stop then reparents it).
    const inner = nodes.find((n) => n.id === 'inner')!;
    expect(inner.parentId).toBe('loop');
    expect(inner.extent).toBeUndefined();

    // The loop-scoped start (entry marker) is mapped as a child of the loop and
    // pinned in place via draggable:false rather than an extent clamp.
    const innerStart = nodes.find((n) => n.id === 'innerStart')!;
    expect(innerStart.parentId).toBe('loop');
    expect(innerStart.draggable).toBe(false);
    expect(innerStart.data.kind).toBe('start');
  });

  it('falls back to default loop size when layout omits dimensions', () => {
    const cfg: GraphConfig = {
      nodes: [{ id: 'loop', type: 'loop', layout: { x: 0, y: 0 } }],
      edges: [],
    };
    const { nodes } = configToFlow(cfg);
    expect(nodes[0].style).toEqual({ width: LOOP_DEFAULT_WIDTH, height: LOOP_DEFAULT_HEIGHT });
  });

  it('lets a nested loop expand its parent while plain nodes stay unconstrained', () => {
    // A loop nested inside another loop must use expandParent (so it can move
    // and resize freely, growing the outer loop). A non-loop nested node gets
    // NO constraint, so it can be dragged out of its parent loop.
    const cfg: GraphConfig = {
      nodes: [
        { id: 'outer', type: 'loop', layout: { x: 0, y: 0, width: 700, height: 400 } },
        { id: 'inner', type: 'loop', parentId: 'outer', layout: { x: 40, y: 60, width: 400, height: 240 } },
        { id: 'body', type: 'shell', parentId: 'inner', layout: { x: 20, y: 40 } },
      ],
      edges: [],
    };
    const { nodes } = configToFlow(cfg);
    const inner = nodes.find((n) => n.id === 'inner')!;
    expect(inner.parentId).toBe('outer');
    expect(inner.expandParent).toBe(true);
    expect(inner.extent).toBeUndefined();
    // A non-loop node is a plain child: no extent, no expandParent.
    const body = nodes.find((n) => n.id === 'body')!;
    expect(body.extent).toBeUndefined();
    expect(body.expandParent).toBeUndefined();
  });

  it('nestConstraint/clearNestConstraint pick the right field per node kind', () => {
    expect(nestConstraint('loop')).toEqual({ expandParent: true });
    expect(nestConstraint('shell')).toEqual({});
    expect(nestConstraint('ifElse')).toEqual({});
    expect(nestConstraint('start')).toEqual({});
    expect(clearNestConstraint).toEqual({ extent: undefined, expandParent: undefined });
  });
});

describe('flowToConfig', () => {
  it('round-trips a config without losing node config or top-level fields', () => {
    const { nodes, edges } = configToFlow(sampleConfig);
    const rebuilt = flowToConfig(nodes, edges, sampleConfig);

    expect(rebuilt.variables).toEqual({ foo: 'bar' });
    expect(rebuilt.disabledVars).toEqual(['baz']);
    expect(rebuilt.runConfig).toEqual({ concurrencyLimit: 4 });
    expect(rebuilt.workspaceId).toBe('ws-1');
    expect(rebuilt.workdir).toBe('/tmp/work');

    const shell = rebuilt.nodes.find((n) => n.id === 'shell')!;
    expect(shell.config).toEqual({ script: 'echo hi', outputVariables: ['out'] });
    expect(shell.layout).toMatchObject({ x: 200, y: 20 });

    expect(rebuilt.edges.map((e) => e.id)).toEqual(['e1', 'e2', 'e3', 'e4']);
    const yes = rebuilt.edges.find((e) => e.id === 'e3')!;
    expect(yes.sourcePort).toBe('yes');
  });

  it('keeps node order stable across a position change', () => {
    const { nodes, edges } = configToFlow(sampleConfig);
    const moved = nodes.map((n) => (n.id === 'shell' ? { ...n, position: { x: 999, y: 999 } } : n));
    const rebuilt = flowToConfig(moved, edges, sampleConfig);
    expect(rebuilt.nodes.map((n) => n.id)).toEqual(['start', 'shell', 'branch', 'okEnd', 'noEnd']);
    const shell = rebuilt.nodes.find((n) => n.id === 'shell')!;
    expect(shell.layout).toMatchObject({ x: 999, y: 999 });
  });

  it('preserves loop parentId and writes back container size', () => {
    const { nodes, edges } = configToFlow(loopConfig);
    const rebuilt = flowToConfig(nodes, edges, loopConfig);
    const inner = rebuilt.nodes.find((n) => n.id === 'inner')!;
    expect(inner.parentId).toBe('loop');
    const loop = rebuilt.nodes.find((n) => n.id === 'loop')!;
    expect(loop.layout?.width).toBe(700);
    expect(loop.layout?.height).toBe(400);
    expect(loop.config).toEqual({ loopMode: 'fixed', fixedCount: 3, maxIterations: 100 });
  });

  it('appends brand-new nodes/edges after existing ones', () => {
    const { nodes, edges } = configToFlow(sampleConfig);
    const newNodes = [
      ...nodes,
      {
        id: 'extra',
        type: 'quartet',
        position: { x: 800, y: 200 },
        data: { kind: 'shell' as const, graphNode: { id: 'extra', type: 'shell' as const } },
      },
    ];
    const rebuilt = flowToConfig(newNodes, edges, sampleConfig);
    expect(rebuilt.nodes[rebuilt.nodes.length - 1].id).toBe('extra');
  });
});

describe('runStatusByNode', () => {
  it('picks the most advanced status per node across instances', () => {
    const instances: GraphInstanceState[] = [
      { key: { nodeId: 'a' }, nodeId: 'a', nodeType: 'shell', status: 'pending', version: 0 },
      { key: { nodeId: 'a' }, nodeId: 'a', nodeType: 'shell', status: 'succeeded', version: 0 },
      { key: { nodeId: 'b' }, nodeId: 'b', nodeType: 'shell', status: 'running', version: 0 },
    ];
    const map = runStatusByNode(instances);
    expect(map.a).toBe('succeeded');
    expect(map.b).toBe('running');
  });

  it('returns empty map for undefined input', () => {
    expect(runStatusByNode(undefined)).toEqual({});
  });
});
