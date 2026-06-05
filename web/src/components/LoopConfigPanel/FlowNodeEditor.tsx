import { useState, useRef, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { FlowNode, Script } from '../../types';
import { AgentInfo } from '../ChatPage';
import { FlowStepEditor } from './FlowStepEditor';
import { NumberStepper } from './NumberStepper';
import {
  generateId, calcTotalSteps, updateNodeInFlow, removeNodeFromFlow,
  MAX_DEPTH, DEPTH_COLORS,
} from './utils';

interface FlowNodeEditorProps {
  node: FlowNode;
  nodeIndex: number;
  isFirstStep: boolean; // true only for the very first step node in the entire flow
  depth: number;
  parentId: string | null;
  siblingCount: number;
  scripts: Script[];
  definedVars: { key: string; value: string }[];
  allShellVars: { varName: string; nodeId: string }[];
  agents: AgentInfo[];
  onUpdateTree: (updater: (nodes: FlowNode[]) => FlowNode[]) => void;
  markDirty: () => void;
  expandedStepId: string | null;
  setExpandedStepId: (id: string | null) => void;
}

export function FlowNodeEditor({
  node, nodeIndex, isFirstStep, depth, parentId: _parentId, siblingCount, scripts,
  definedVars, allShellVars, agents, onUpdateTree, markDirty,
  expandedStepId, setExpandedStepId,
}: FlowNodeEditorProps) {
  const { t } = useTranslation();
  const [isCollapsed, setIsCollapsed] = useState(false);
  const [addMenuOpen, setAddMenuOpen] = useState(false);
  const addMenuRef = useRef<HTMLDivElement>(null);
  const borderColor = DEPTH_COLORS[depth % DEPTH_COLORS.length];
  const canRemove = siblingCount > 1;

  useEffect(() => {
    if (!addMenuOpen) return;
    const handler = (e: MouseEvent) => {
      if (addMenuRef.current && !addMenuRef.current.contains(e.target as Node)) {
        setAddMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [addMenuOpen]);

  if (node.type === 'step') {
    return (
      <div className="flow-node flow-node-step" style={{ '--flow-depth-color': borderColor } as React.CSSProperties}>
        <FlowStepEditor
          node={node}
          stepIndex={nodeIndex}
          isFirstStep={isFirstStep}
          canRemove={canRemove}
          depth={depth}
          scripts={scripts}
          definedVars={definedVars}
          allShellVars={allShellVars}
          agents={agents}
          isExpanded={expandedStepId === node.id}
          onExpandedChange={(exp) => setExpandedStepId(exp ? node.id : null)}
          onUpdate={(updated) => {
            onUpdateTree((nodes) => updateNodeInFlow(nodes, node.id, () => updated));
            markDirty();
          }}
          onRemove={() => {
            onUpdateTree((nodes) => removeNodeFromFlow(nodes, node.id));
            if (expandedStepId === node.id) setExpandedStepId(null);
            markDirty();
          }}
        />
      </div>
    );
  }

  // Group node
  const children = node.children || [];
  const totalSteps = calcTotalSteps([node]);
  const canAddGroup = depth + 1 < MAX_DEPTH;

  const handleAddStep = () => {
    const newStep: FlowNode = {
      id: generateId(),
      type: 'step',
      message: '',
      repeatCount: 1,
      roundMode: 'none',
      roundType: 'prompt',
    };
    onUpdateTree((nodes) => updateNodeInFlow(nodes, node.id, (n) => ({
      ...n, children: [...(n.children || []), newStep],
    })));
    setExpandedStepId(newStep.id);
    setAddMenuOpen(false);
    markDirty();
  };

  const handleAddGroup = () => {
    const firstStepId = generateId();
    const newGroup: FlowNode = {
      id: generateId(),
      type: 'group',
      label: '',
      iterationCount: 1,
      children: [{
        id: firstStepId,
        type: 'step',
        message: '',
        repeatCount: 1,
        roundMode: 'none',
        roundType: 'prompt',
      }],
    };
    onUpdateTree((nodes) => updateNodeInFlow(nodes, node.id, (n) => ({
      ...n, children: [...(n.children || []), newGroup],
    })));
    setExpandedStepId(firstStepId);
    setAddMenuOpen(false);
    markDirty();
  };

  return (
    <div
      className={`flow-node flow-group${isCollapsed ? ' collapsed' : ''}`}
      style={{ '--flow-depth-color': borderColor } as React.CSSProperties}
    >
      <div className="flow-group-header">
        <div className="flow-group-header-left" onClick={() => setIsCollapsed(!isCollapsed)}>
          <span className={`loop-round-collapse-arrow${isCollapsed ? ' collapsed' : ''}`}>▾</span>
          <span className="flow-group-label">
            {node.label || (depth === 0
              ? t('loop.group.mainLabel')
              : t('loop.group.defaultLabel', { index: nodeIndex + 1 }))}
          </span>
          <span className="flow-group-meta">{t('loop.group.stepsMeta', { count: totalSteps })}</span>
        </div>
        <div className="flow-group-header-right" onClick={(e) => e.stopPropagation()}>
          <div className="loop-round-inline-control">
            <span className="loop-round-inline-control-label">{t('loop.group.iteration')}</span>
            <NumberStepper
              value={node.iterationCount || 1}
              onChange={(n) => {
                onUpdateTree((nodes) => updateNodeInFlow(nodes, node.id, (nd) => ({ ...nd, iterationCount: n })));
                markDirty();
              }}
            />
          </div>
          {canRemove && (
            <button
              className="loop-round-remove"
              onClick={() => {
                onUpdateTree((nodes) => removeNodeFromFlow(nodes, node.id));
                markDirty();
              }}
              type="button"
            >×</button>
          )}
        </div>
      </div>

      {!isCollapsed && (
        <div className="flow-group-children">
          {children.map((child, idx) => (
            <FlowNodeEditor
              key={child.id}
              node={child}
              nodeIndex={idx}
              isFirstStep={isFirstStep && idx === 0}
              depth={depth + 1}
              parentId={node.id}
              siblingCount={children.length}
              scripts={scripts}
              definedVars={definedVars}
              allShellVars={allShellVars}
              agents={agents}
              onUpdateTree={onUpdateTree}
              markDirty={markDirty}
              expandedStepId={expandedStepId}
              setExpandedStepId={setExpandedStepId}
            />
          ))}
          <div className="flow-add-buttons">
            <div className="flow-add-menu-wrap" ref={addMenuRef}>
              <button
                className="flow-add-btn flow-add-menu-btn"
                onClick={() => setAddMenuOpen((v) => !v)}
                type="button"
              >
                <span>{t('loop.actions.add')}</span>
                <svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                  <path d="M6 9l6 6 6-6" />
                </svg>
              </button>
              {addMenuOpen && (
                <div className="flow-add-menu">
                  <button className="flow-add-menu-item" onClick={handleAddStep} type="button">
                    {t('loop.actions.addMenuStep')}
                  </button>
                  {canAddGroup && (
                    <button className="flow-add-menu-item" onClick={handleAddGroup} type="button">
                      {t('loop.actions.addMenuGroup')}
                    </button>
                  )}
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
