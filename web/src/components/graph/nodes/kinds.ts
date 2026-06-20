import type { TFunction } from 'i18next';
import type { GraphNodeType } from '../../../types/graph';

export interface NodeKind {
  /** i18n key for the localized label. */
  labelKey: string;
  /** i18n key for the localized palette subtitle. */
  subKey: string;
  icon: string;
  color: string;
}

// Visual language ported from public/workflow-builder-rf.html so the canvas
// matches the original design exploration. Labels/subtitles are resolved at
// render time via i18n (see labelOf / subOf).
export const KINDS: Record<GraphNodeType, NodeKind> = {
  start: { labelKey: 'graph.kinds.startLabel', subKey: 'graph.kinds.startSub', icon: '▶', color: '#2ea043' },
  end: { labelKey: 'graph.kinds.endLabel', subKey: 'graph.kinds.endSub', icon: '■', color: '#f85149' },
  shell: { labelKey: 'graph.kinds.shellLabel', subKey: 'graph.kinds.shellSub', icon: '$', color: '#f0883e' },
  prompt: { labelKey: 'graph.kinds.promptLabel', subKey: 'graph.kinds.promptSub', icon: '✦', color: '#58a6ff' },
  evaluator: { labelKey: 'graph.kinds.evaluatorLabel', subKey: 'graph.kinds.evaluatorSub', icon: '⚖', color: '#a371f7' },
  ifElse: { labelKey: 'graph.kinds.ifElseLabel', subKey: 'graph.kinds.ifElseSub', icon: '◆', color: '#e3b341' },
  loop: { labelKey: 'graph.kinds.loopLabel', subKey: 'graph.kinds.loopSub', icon: '🔁', color: '#56d4dd' },
};

// Order shown in the palette. start/end are intentionally excluded: every
// workflow already ships with exactly one start and one end node, both of which
// are protected from deletion. Letting users add duplicates would leave them
// stuck with undeletable extra control nodes.
export const PALETTE_ORDER: GraphNodeType[] = [
  'shell',
  'prompt',
  'evaluator',
  'ifElse',
  'loop',
];

export function kindOf(type: GraphNodeType): NodeKind {
  return KINDS[type] || KINDS.shell;
}

export function labelOf(t: TFunction, type: GraphNodeType): string {
  return t(kindOf(type).labelKey);
}

export function subOf(t: TFunction, type: GraphNodeType): string {
  return t(kindOf(type).subKey);
}
