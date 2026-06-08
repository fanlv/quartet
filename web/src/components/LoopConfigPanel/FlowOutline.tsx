import { useTranslation } from 'react-i18next';
import type React from 'react';
import type { TFunction } from 'i18next';
import { FlowNode } from '../../types';
import { calcTotalSteps, MAX_DEPTH } from './utils';
import { NumberStepper } from './NumberStepper';

export interface FlowIssue {
  nodeId: string;
  message: string;
}

interface FlowOutlineProps {
  nodes: FlowNode[];
  selectedNodeId: string | null;
  collapsedGroupIds: Set<string>;
  issuesByNode: Map<string, FlowIssue>;
  onSelect: (nodeId: string) => void;
  onToggleGroup: (nodeId: string) => void;
  onAddStep: (targetGroupId: string | null) => void;
  onAddGroup: (targetGroupId: string | null) => void;
  onDuplicate: (nodeId: string) => void;
  onMove: (nodeId: string, direction: -1 | 1) => void;
  onDelete: (nodeId: string) => void;
  onUpdateIterationCount: (nodeId: string, count: number) => void;
  onUpdateRepeatCount: (nodeId: string, count: number) => void;
}

function fallbackStepTitle(node: FlowNode, index: number, t: TFunction): string {
  return node.label?.trim() || t('loop.outline.emptyStep', { index: index + 1 });
}

function renderNodeStatus(issue: FlowIssue | undefined, t: TFunction) {
  if (issue) {
    return (
      <span className="loop-outline-status error" title={issue.message}>
        !
      </span>
    );
  }
  return (
    <span className="loop-outline-status ready" title={t('loop.step.status.ready')}>
      OK
    </span>
  );
}

export function FlowOutline({
  nodes,
  selectedNodeId,
  collapsedGroupIds,
  issuesByNode,
  onSelect,
  onToggleGroup,
  onAddStep,
  onAddGroup,
  onDuplicate,
  onMove,
  onDelete,
  onUpdateIterationCount,
  onUpdateRepeatCount,
}: FlowOutlineProps) {
  const { t } = useTranslation();

  const renderNodes = (items: FlowNode[], depth: number): React.ReactNode => (
    <div className={depth === 0 ? 'loop-outline-list' : 'loop-outline-children'}>
      {items.map((node, index) => {
        const isGroup = node.type === 'group';
        const isCollapsed = isGroup && collapsedGroupIds.has(node.id);
        const issue = issuesByNode.get(node.id);
        const canMoveUp = index > 0;
        const canMoveDown = index < items.length - 1;
        const canDelete = items.length > 1 || depth > 0;
        const isSelected = selectedNodeId === node.id;
        const rowStyle = { '--outline-depth': depth } as React.CSSProperties;

        if (isGroup) {
          const title = node.label || (depth === 0
            ? t('loop.group.mainLabel')
            : t('loop.group.defaultLabel', { index: index + 1 }));
          const canAddGroup = depth + 1 < MAX_DEPTH;
          const childrenCount = node.children?.length || 0;
          const toggleTitle = isCollapsed ? t('loop.outline.expandGroup') : t('loop.outline.collapseGroup');

          return (
            <div className="loop-outline-node" key={node.id}>
              <button
                className={`loop-outline-row loop-outline-row-group${isSelected ? ' selected' : ''}${issue ? ' has-error' : ''}${isCollapsed ? ' collapsed' : ''}`}
                style={rowStyle}
                onClick={() => onSelect(node.id)}
                type="button"
                aria-expanded={!isCollapsed}
              >
                <span
                  className={`loop-outline-caret${isCollapsed ? ' collapsed' : ''}${childrenCount === 0 ? ' disabled' : ''}`}
                  title={toggleTitle}
                  onClick={(event) => {
                    event.stopPropagation();
                    onToggleGroup(node.id);
                  }}
                >
                  ▾
                </span>
                <span className="loop-outline-type group">G</span>
                <span className="loop-outline-main">
                  <span className="loop-outline-title">
                    {title}
                    {node.completionCondition?.trim() && (
                      <span className="loop-outline-conditional-badge" title={node.completionCondition}>
                        {t('loop.group.conditionalBadge')}
                      </span>
                    )}
                  </span>
                  <span className="loop-outline-subtitle">
                    {node.completionCondition?.trim()
                      ? t('loop.outline.groupMetaConditional', {
                          max: node.iterationCount || 1,
                          steps: calcTotalSteps([node]),
                        })
                      : t('loop.outline.groupMeta', {
                          iterations: node.iterationCount || 1,
                          steps: calcTotalSteps([node]),
                        })}
                  </span>
                </span>
                <span
                  className="loop-outline-iteration"
                  onClick={(e) => e.stopPropagation()}
                  title={node.completionCondition?.trim() ? t('loop.group.maxIteration') : t('loop.group.iteration')}
                >
                  <NumberStepper
                    value={node.iterationCount || 1}
                    onChange={(n) => onUpdateIterationCount(node.id, n)}
                  />
                </span>
                {renderNodeStatus(issue, t)}
              </button>
              {isSelected && (
                <div className="loop-outline-actions" style={rowStyle}>
                  <button onClick={() => onAddStep(node.id)} type="button">{t('loop.actions.addMenuStep')}</button>
                  {canAddGroup && <button onClick={() => onAddGroup(node.id)} type="button">{t('loop.actions.addMenuGroup')}</button>}
                  <button onClick={() => onDuplicate(node.id)} type="button">{t('loop.actions.duplicate')}</button>
                  <button disabled={!canMoveUp} onClick={() => onMove(node.id, -1)} type="button">↑</button>
                  <button disabled={!canMoveDown} onClick={() => onMove(node.id, 1)} type="button">↓</button>
                  <button className="danger" disabled={!canDelete} onClick={() => onDelete(node.id)} type="button">{t('common.delete')}</button>
                </div>
              )}
              {!isCollapsed && node.children && node.children.length > 0 && renderNodes(node.children, depth + 1)}
            </div>
          );
        }

        const isShell = (node.roundType || 'prompt') === 'shell';
        return (
          <div className="loop-outline-node" key={node.id}>
            <button
              className={`loop-outline-row loop-outline-row-step${isSelected ? ' selected' : ''}${issue ? ' has-error' : ''}`}
              style={rowStyle}
              onClick={() => onSelect(node.id)}
              type="button"
            >
              <span className={`loop-outline-type ${isShell ? 'shell' : 'prompt'}`}>{isShell ? 'S' : 'P'}</span>
              <span className="loop-outline-main">
                <span className="loop-outline-title">{fallbackStepTitle(node, index, t)}</span>
                <span className="loop-outline-subtitle">
                  {t(`loop.step.roundMode.${node.roundMode || 'none'}.label`)}
                </span>
              </span>
              <span className="loop-outline-iteration" onClick={(e) => e.stopPropagation()}>
                <NumberStepper
                  value={node.repeatCount || 1}
                  onChange={(n) => onUpdateRepeatCount(node.id, n)}
                />
              </span>
              {renderNodeStatus(issue, t)}
            </button>
            {isSelected && (
              <div className="loop-outline-actions" style={rowStyle}>
                <button onClick={() => onAddStep(null)} type="button">{t('loop.actions.addMenuStep')}</button>
                <button onClick={() => onAddGroup(null)} type="button">{t('loop.actions.addMenuGroup')}</button>
                <button onClick={() => onDuplicate(node.id)} type="button">{t('loop.actions.duplicate')}</button>
                <button disabled={!canMoveUp} onClick={() => onMove(node.id, -1)} type="button">↑</button>
                <button disabled={!canMoveDown} onClick={() => onMove(node.id, 1)} type="button">↓</button>
                <button className="danger" disabled={!canDelete} onClick={() => onDelete(node.id)} type="button">{t('common.delete')}</button>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );

  return <>{renderNodes(nodes, 0)}</>;
}
