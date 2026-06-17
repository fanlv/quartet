import { useState, useRef, useCallback, useEffect } from 'react';
import { useTranslation, Trans } from 'react-i18next';
import { FlowNode, RoundType } from '../../types';
import { AgentInfo } from '../ChatPage';
import { ShellEditor } from '../ShellEditor';
import { ROUND_MODE_OPTIONS, isStepValid, detectShellVarsForStep, getStepPreview } from './utils';
import { isImageUrl } from '../../utils/url';

interface FlowStepEditorProps {
  node: FlowNode;
  stepIndex: number;
  isFirstStep: boolean; // true if this is the first step in the entire flow
  canRemove: boolean;
  depth: number;
  definedVars: { key: string; value: string }[];
  allShellVars: { varName: string; nodeId: string }[];
  agents: AgentInfo[];
  onUpdate: (updated: FlowNode) => void;
  onRemove: () => void;
  // Non-blocking soft warnings (e.g. evaluator config guidance, §6). Rendered as
  // a hint banner; does not block save.
  warnings?: string[];
  // Accordion mode: when provided, the parent controls expansion and at most one step is open at a time.
  isExpanded?: boolean;
  onExpandedChange?: (expanded: boolean) => void;
  // structureLocked (running job edit): disables the round-type toggle and the
  // session-mode dropdown — both change session creation, which is structural.
  // The prompt / shell-script / agent / model / mode fields stay editable.
  structureLocked?: boolean;
}

export function FlowStepEditor({
  node, stepIndex, isFirstStep, canRemove, depth: _depth,
  definedVars, allShellVars, agents, onUpdate, onRemove, warnings,
  isExpanded, onExpandedChange, structureLocked,
}: FlowStepEditorProps) {
  const { t, i18n } = useTranslation();
  const [internalCollapsed, setInternalCollapsed] = useState(false);
  const controlled = typeof isExpanded === 'boolean';
  const collapsed = controlled ? !isExpanded : internalCollapsed;
  const toggleCollapsed = () => {
    if (controlled) {
      onExpandedChange?.(!isExpanded);
    } else {
      setInternalCollapsed(!internalCollapsed);
    }
  };

  const [modeDropdownOpen, setModeDropdownOpen] = useState(false);
  const [agentDropdownOpen, setAgentDropdownOpen] = useState(false);
  const [modelDropdownOpen, setModelDropdownOpen] = useState(false);
  const [modeListOpen, setModeListOpen] = useState(false);
  const [thoughtLevelListOpen, setThoughtLevelListOpen] = useState(false);
  const [insertVarOpen, setInsertVarOpen] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const modeDropdownRef = useRef<HTMLDivElement>(null);
  const agentDropdownRef = useRef<HTMLDivElement>(null);
  const modelDropdownRef = useRef<HTMLDivElement>(null);
  const modeListRef = useRef<HTMLDivElement>(null);
  const thoughtLevelListRef = useRef<HTMLDivElement>(null);
  const insertVarRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!modeDropdownOpen && !agentDropdownOpen && !modelDropdownOpen && !modeListOpen && !thoughtLevelListOpen && !insertVarOpen) return;
    const handleClickOutside = (e: MouseEvent) => {
      if (modeDropdownOpen && modeDropdownRef.current && !modeDropdownRef.current.contains(e.target as Node)) {
        setModeDropdownOpen(false);
      }
      if (agentDropdownOpen && agentDropdownRef.current && !agentDropdownRef.current.contains(e.target as Node)) {
        setAgentDropdownOpen(false);
      }
      if (modelDropdownOpen && modelDropdownRef.current && !modelDropdownRef.current.contains(e.target as Node)) {
        setModelDropdownOpen(false);
      }
      if (modeListOpen && modeListRef.current && !modeListRef.current.contains(e.target as Node)) {
        setModeListOpen(false);
      }
      if (thoughtLevelListOpen && thoughtLevelListRef.current && !thoughtLevelListRef.current.contains(e.target as Node)) {
        setThoughtLevelListOpen(false);
      }
      if (insertVarOpen && insertVarRef.current && !insertVarRef.current.contains(e.target as Node)) {
        setInsertVarOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, [modeDropdownOpen, agentDropdownOpen, modelDropdownOpen, modeListOpen, thoughtLevelListOpen, insertVarOpen]);

  // When the job starts running mid-edit, structureLocked flips true. Disabling
  // the trigger buttons does not close a popover that was already open, and its
  // items stay clickable — so force the structural popovers shut here. The
  // structural handlers also guard internally (defense in depth), but closing
  // them removes the misleading affordance immediately.
  useEffect(() => {
    if (!structureLocked) return;
    setModeDropdownOpen(false);
  }, [structureLocked]);

  const roundType = node.roundType || 'prompt';
  const isShellRound = roundType === 'shell';
  const isEvaluatorRound = roundType === 'evaluator';
  const roundMode = node.roundMode || 'none';

  const isZh = (i18n.resolvedLanguage || i18n.language || 'en').startsWith('zh');
  const varTagExample = isZh ? '{{变量名}}' : '{{variableName}}';
  const setCmdExample = isZh ? 'quartet_set "变量名" "值"' : 'quartet_set "variableName" "value"';

  const roundModeOptions = ROUND_MODE_OPTIONS.map((o) => ({
    ...o,
    label: t(`loop.step.roundMode.${o.value}.label`),
    desc: t(`loop.step.roundMode.${o.value}.desc`),
  }));

  // First step in the flow must create a new session
  const availableRoundModes = isFirstStep
    ? roundModeOptions.filter((o) => o.value !== 'none')
    : roundModeOptions;

  const roundReady = isStepValid(node);
  const roundPreview = getStepPreview(node);

  const detectedVars = isShellRound && node.message ? detectShellVarsForStep(node.message) : [];

  // Builtin system variables injected per-round by the backend
  const builtinVars = [
    { key: '_current_time', desc: t('loop.step.builtinVars.currentTime') },
    { key: '_current_path', desc: t('loop.step.builtinVars.currentPath') },
    { key: '_last_assistant_msg', desc: t('loop.step.builtinVars.lastAssistantMsg') },
    { key: '_job_title', desc: t('loop.step.builtinVars.jobTitle') },
    { key: '_job_workdir', desc: t('loop.step.builtinVars.jobWorkdir') },
  ];

  const getRoundModeLabel = (mode: string) =>
    roundModeOptions.find((o) => o.value === mode)?.label || t('loop.step.roundMode.none.label');

  const autoResize = useCallback((el: HTMLTextAreaElement) => {
    el.style.height = 'auto';
    const lineHeight = 20;
    const minH = lineHeight * 3 + 16;
    const maxH = lineHeight * 10 + 16;
    el.style.height = Math.min(Math.max(el.scrollHeight, minH), maxH) + 'px';
  }, []);

  const handleFieldChange = (field: string, value: string | number) => {
    onUpdate({ ...node, [field]: value });
  };

  // roundMode is a structural field (backend flowStructureEqual rejects a
  // running-job save that changes it). Guard here too so an already-open
  // session-mode dropdown can't mutate the local draft after the lock engages.
  const handleRoundModeChange = (value: string) => {
    if (structureLocked) return;
    handleFieldChange('roundMode', value);
  };

  const handleRoundTypeChange = (type: RoundType) => {
    if (structureLocked) return;
    if (type === 'shell') {
      onUpdate({ ...node, roundType: 'shell', message: '' });
    } else {
      onUpdate({ ...node, roundType: 'prompt', message: '' });
    }
  };

  const handleInsertVariable = (varKey: string) => {
    const textarea = textareaRef.current;
    const insertText = `{{${varKey}}}`;
    if (textarea) {
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      const msg = node.message || '';
      const newMsg = msg.slice(0, start) + insertText + msg.slice(end);
      onUpdate({ ...node, message: newMsg });
      requestAnimationFrame(() => {
        textarea.focus();
        const pos = start + insertText.length;
        textarea.setSelectionRange(pos, pos);
      });
    } else {
      onUpdate({ ...node, message: (node.message || '') + insertText });
    }
  };

  // Agent/model override — only needed when creating a new session with a
  // prompt or evaluator step (both run against an LLM).
  const showAgentOverride = roundMode !== 'none' && !isShellRound && agents.length > 0;
  const selectedAgent = node.agentType
    ? agents.find((a) => a.type === node.agentType && (a.model_id === node.modelId || a.models?.availableModels.some((m) => m.modelId === node.modelId)))
    : undefined;
  const agentLabel = selectedAgent ? selectedAgent.display_name : t('loop.step.agentPlaceholder');
  const availableModels = selectedAgent?.models?.availableModels || [];
  const availableModes = selectedAgent?.modes?.availableModes || [];
  const availableThoughtLevels = selectedAgent?.thoughtLevels?.availableThoughtLevels || [];
  const currentModelId = node.modelId || selectedAgent?.models?.currentModelId || selectedAgent?.model_id || '';
  const currentAcpMode = node.acpMode || selectedAgent?.modes?.currentModeId || '';
  const currentAcpThoughtLevel = node.acpThoughtLevel || selectedAgent?.thoughtLevels?.currentThoughtLevelId || '';
  const selectedModelName = availableModels.find((m) => m.modelId === currentModelId)?.name || currentModelId || t('common.default');
  const selectedModeName = availableModes.find((m) => m.id === currentAcpMode)?.name || currentAcpMode || t('common.default');
  const selectedThoughtLevelName = availableThoughtLevels.find((m) => m.id === currentAcpThoughtLevel)?.name || currentAcpThoughtLevel || t('common.default');

  const handleSelectAgent = (agent: AgentInfo) => {
    onUpdate({
      ...node,
      agentType: agent.type,
      modelId: agent.models?.currentModelId || agent.model_id,
      acpMode: agent.modes?.currentModeId || undefined,
      acpThoughtLevel: agent.thoughtLevels?.currentThoughtLevelId || undefined,
    });
    setAgentDropdownOpen(false);
  };

  return (
    <div className={`loop-round-card flow-step${collapsed ? ' collapsed' : ''}`}>
      <div className="loop-round-header">
        <div className="loop-round-header-left" onClick={toggleCollapsed}>
          <span className={`loop-round-collapse-arrow${collapsed ? ' collapsed' : ''}`}>▾</span>
          <span className="loop-round-index-badge">Step {stepIndex + 1}</span>
          {node.label?.trim() && (
            <span className="loop-round-name-label" title={node.label}>{node.label.trim()}</span>
          )}
          <span
            className={`loop-round-type-icon${isShellRound ? ' shell' : isEvaluatorRound ? ' evaluator' : ' prompt'}`}
            title={isShellRound ? 'Shell' : isEvaluatorRound ? t('loop.step.evaluator.typeLabel') : 'Prompt'}
          >
            {isShellRound ? 'S' : isEvaluatorRound ? 'E' : 'P'}
          </span>
          <span
            className={`loop-round-health-dot${roundReady ? ' ready' : ''}`}
            title={roundReady ? t('loop.step.status.ready') : t('loop.step.status.todo')}
          />
          {collapsed && (
            <span className="loop-round-preview">
              {roundPreview || <span className="loop-round-preview-empty">—</span>}
            </span>
          )}
        </div>
        <div className="loop-round-header-right" onClick={(e) => e.stopPropagation()}>
          {collapsed && (
            <>
              {(node.repeatCount || 1) > 1 && (
                <span
                  className="loop-round-summary-chip"
                  title={t('loop.step.inline.repeat')}
                >
                  {t('loop.step.inline.repeat')} ×{node.repeatCount}
                </span>
              )}
              <span
                className="loop-round-summary-chip"
                title={t('loop.step.inline.session')}
              >
                {getRoundModeLabel(roundMode)}
              </span>
            </>
          )}
          {canRemove && (
            <button className="loop-round-remove" onClick={onRemove} type="button">×</button>
          )}
        </div>
      </div>

      {!collapsed && (
        <div className="loop-round-body">
          <div className="loop-round-name-field">
            <label className="loop-round-name-field-label">{t('loop.step.stepName')}</label>
            <input
              className="loop-round-name-input"
              type="text"
              value={node.label || ''}
              onChange={(e) => handleFieldChange('label', e.target.value)}
              placeholder={t('loop.step.stepNamePlaceholder', { index: stepIndex + 1 })}
            />
          </div>

          {isEvaluatorRound ? (
            <div className="loop-round-evaluator-banner" data-testid="loop-evaluator-banner">
              <span className="loop-round-type-icon evaluator">E</span>
              <span className="loop-round-evaluator-banner-text">{t('loop.step.evaluator.banner')}</span>
            </div>
          ) : (
            <div className="loop-round-type-toggle">
              <button
                className={`loop-round-type-btn${roundType === 'prompt' ? ' active' : ''}`}
                onClick={() => handleRoundTypeChange('prompt')}
                disabled={structureLocked}
                type="button"
              >
                Prompt
              </button>
              <button
                className={`loop-round-type-btn${roundType === 'shell' ? ' active' : ''}`}
                onClick={() => handleRoundTypeChange('shell')}
                disabled={structureLocked}
                type="button"
              >
                Shell
              </button>
            </div>
          )}

          {warnings && warnings.length > 0 && (
            <div className="loop-round-warnings" data-testid="loop-step-warnings">
              {warnings.map((w, wi) => (
                <div key={wi} className="loop-round-warning">{w}</div>
              ))}
            </div>
          )}

          {/* Session mode + Agent/Model override */}
          <div className="loop-round-agent-override">
            <span className="loop-round-agent-override-label">{t('loop.step.agentOverride.label')}</span>
            <div className="loop-round-agent-override-row">
              <div
                className="loop-round-agent-override-field"
                ref={modeDropdownRef}
              >
                <span className="loop-round-agent-override-field-label">{t('loop.step.inline.session')}</span>
                <button
                  className="loop-round-agent-override-btn"
                  onClick={() => setModeDropdownOpen(!modeDropdownOpen)}
                  disabled={structureLocked}
                  type="button"
                >
                  <span>{getRoundModeLabel(roundMode)}</span>
                  <svg className="loop-round-mode-arrow" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                    <path d="M6 9l6 6 6-6" />
                  </svg>
                </button>
                {modeDropdownOpen && (
                  <div className="loop-round-mode-dropdown">
                    {availableRoundModes.map((opt) => (
                      <button
                        key={opt.value}
                        className={`loop-round-mode-item${roundMode === opt.value ? ' active' : ''}`}
                        onClick={() => {
                          handleRoundModeChange(opt.value);
                          setModeDropdownOpen(false);
                        }}
                        type="button"
                      >
                        <div className="loop-round-mode-item-info">
                          <span className="loop-round-mode-item-name">{opt.label}</span>
                          <span className="loop-round-mode-item-desc">{opt.desc}</span>
                        </div>
                        {roundMode === opt.value && (
                          <svg className="loop-round-mode-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                            <path d="M20 6L9 17l-5-5" />
                          </svg>
                        )}
                      </button>
                    ))}
                  </div>
                )}
              </div>

              {showAgentOverride && (
                <>
                  <div className="loop-round-agent-override-field" ref={agentDropdownRef}>
                    <span className="loop-round-agent-override-field-label">Agent</span>
                    <button
                      className="loop-round-agent-override-btn"
                      onClick={() => setAgentDropdownOpen(!agentDropdownOpen)}
                      type="button"
                    >
                      {selectedAgent?.icon_url && (
                        isImageUrl(selectedAgent.icon_url)
                          ? <img className="loop-round-agent-override-icon" src={selectedAgent.icon_url} alt="" referrerPolicy="no-referrer" />
                          : <span className="loop-round-agent-override-emoji">{selectedAgent.icon_url}</span>
                      )}
                      <span>{agentLabel}</span>
                      <svg className="loop-round-mode-arrow" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                        <path d="M6 9l6 6 6-6" />
                      </svg>
                    </button>
                    {agentDropdownOpen && (
                      <div className="loop-round-agent-override-dropdown">
                        {agents.map((a) => {
                          const isActive = node.agentType === a.type && (node.modelId === a.model_id || !!a.models?.availableModels.some((m) => m.modelId === node.modelId));
                          return (
                            <button
                              key={`${a.type}-${a.model_id}`}
                              className={`loop-round-agent-override-item${isActive ? ' active' : ''}`}
                              onClick={() => handleSelectAgent(a)}
                              type="button"
                            >
                              {a.icon_url && (
                                isImageUrl(a.icon_url)
                                  ? <img className="loop-round-agent-override-icon" src={a.icon_url} alt="" referrerPolicy="no-referrer" />
                                  : <span className="loop-round-agent-override-emoji">{a.icon_url}</span>
                              )}
                              <span className="loop-round-agent-override-item-name">{a.display_name}</span>
                              {isActive && (
                                <svg className="loop-round-mode-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                  <path d="M20 6L9 17l-5-5" />
                                </svg>
                              )}
                            </button>
                          );
                        })}
                      </div>
                    )}
                  </div>

                  {selectedAgent && availableModels.length > 1 && (
                    <div className="loop-round-agent-override-field" ref={modelDropdownRef}>
                      <span className="loop-round-agent-override-field-label">Model</span>
                      <button
                        className="loop-round-agent-override-btn"
                        onClick={() => setModelDropdownOpen(!modelDropdownOpen)}
                        type="button"
                      >
                        <span>{selectedModelName}</span>
                        <svg className="loop-round-mode-arrow" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M6 9l6 6 6-6" />
                        </svg>
                      </button>
                      {modelDropdownOpen && (
                        <div className="loop-round-agent-override-dropdown">
                          {availableModels.map((m) => (
                            <button
                              key={m.modelId}
                              className={`loop-round-agent-override-item${currentModelId === m.modelId ? ' active' : ''}`}
                              onClick={() => {
                                onUpdate({ ...node, modelId: m.modelId });
                                setModelDropdownOpen(false);
                              }}
                              type="button"
                            >
                              <span className="loop-round-agent-override-item-name">{m.name}</span>
                              {currentModelId === m.modelId && (
                                <svg className="loop-round-mode-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                  <path d="M20 6L9 17l-5-5" />
                                </svg>
                              )}
                            </button>
                          ))}
                        </div>
                      )}
                    </div>
                  )}

                  {selectedAgent && availableModes.length > 1 && (
                    <div className="loop-round-agent-override-field" ref={modeListRef}>
                      <span className="loop-round-agent-override-field-label">Mode</span>
                      <button
                        className="loop-round-agent-override-btn"
                        onClick={() => setModeListOpen(!modeListOpen)}
                        type="button"
                      >
                        <span>{selectedModeName}</span>
                        <svg className="loop-round-mode-arrow" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M6 9l6 6 6-6" />
                        </svg>
                      </button>
                      {modeListOpen && (
                        <div className="loop-round-agent-override-dropdown">
                          {availableModes.map((m) => (
                            <button
                              key={m.id}
                              className={`loop-round-agent-override-item${currentAcpMode === m.id ? ' active' : ''}`}
                              onClick={() => {
                                onUpdate({ ...node, acpMode: m.id });
                                setModeListOpen(false);
                              }}
                              type="button"
                            >
                              <span className="loop-round-agent-override-item-name">{m.name}</span>
                              {currentAcpMode === m.id && (
                                <svg className="loop-round-mode-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                  <path d="M20 6L9 17l-5-5" />
                                </svg>
                              )}
                            </button>
                          ))}
                        </div>
                      )}
                    </div>
                  )}

                  {selectedAgent && availableThoughtLevels.length > 1 && (
                    <div className="loop-round-agent-override-field" ref={thoughtLevelListRef}>
                      <span className="loop-round-agent-override-field-label">Thought</span>
                      <button
                        className="loop-round-agent-override-btn"
                        onClick={() => setThoughtLevelListOpen(!thoughtLevelListOpen)}
                        type="button"
                      >
                        <span>{selectedThoughtLevelName}</span>
                        <svg className="loop-round-mode-arrow" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                          <path d="M6 9l6 6 6-6" />
                        </svg>
                      </button>
                      {thoughtLevelListOpen && (
                        <div className="loop-round-agent-override-dropdown">
                          {availableThoughtLevels.map((m) => (
                            <button
                              key={m.id}
                              className={`loop-round-agent-override-item${currentAcpThoughtLevel === m.id ? ' active' : ''}`}
                              onClick={() => {
                                onUpdate({ ...node, acpThoughtLevel: m.id });
                                setThoughtLevelListOpen(false);
                              }}
                              type="button"
                            >
                              <span className="loop-round-agent-override-item-name">{m.name}</span>
                              {currentAcpThoughtLevel === m.id && (
                                <svg className="loop-round-mode-check" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                                  <path d="M20 6L9 17l-5-5" />
                                </svg>
                              )}
                            </button>
                          ))}
                        </div>
                      )}
                    </div>
                  )}
                </>
              )}
            </div>
          </div>

          {!isShellRound ? (
            <div className="loop-round-field">
              <div className="loop-round-field-top">
                <label>{isEvaluatorRound ? t('loop.step.evaluator.promptLabel') : t('loop.step.message.label')}</label>
                <div className="loop-round-field-top-right">
                  <span className="loop-round-field-hint">{isEvaluatorRound ? t('loop.step.evaluator.promptHint') : t('loop.step.message.hint')}</span>
                  <div className="loop-insert-var-wrap" ref={insertVarRef}>
                    <button
                      className="loop-insert-var-btn"
                      onClick={() => setInsertVarOpen((v) => !v)}
                      type="button"
                      title={t('loop.step.insertVar.title')}
                    >
                      <span className="loop-insert-var-btn-icon">{'{{}}'}</span>
                      <span className="loop-insert-var-btn-text">{t('loop.step.insertVar.button')}</span>
                    </button>
                    {insertVarOpen && (
                      <div className="loop-insert-var-popover">
                      {definedVars.length === 0 && allShellVars.length === 0 && (
                        <div className="loop-insert-var-group">
                          <div className="loop-insert-var-empty">{t('loop.step.insertVar.empty')}</div>
                        </div>
                      )}
                      {definedVars.length > 0 && (
                        <div className="loop-insert-var-group">
                          <div className="loop-insert-var-group-label">{t('loop.step.insertVar.groupDefined')}</div>
                          <div className="loop-var-tags">
                            {definedVars.map((v, vi) => (
                              <button
                                key={`var-${vi}`}
                                className="loop-var-tag"
                                onClick={() => {
                                  handleInsertVariable(v.key.trim());
                                  setInsertVarOpen(false);
                                }}
                                title={t('loop.step.insertVarTitle', { tag: `{{${v.key.trim()}}}` })}
                                type="button"
                              >
                                {v.key.trim()}
                              </button>
                            ))}
                          </div>
                        </div>
                      )}
                      {allShellVars.filter((sv) => !definedVars.some((dv) => dv.key.trim() === sv.varName)).length > 0 && (
                        <div className="loop-insert-var-group">
                          <div className="loop-insert-var-group-label">{t('loop.step.insertVar.groupShell')}</div>
                          <div className="loop-var-tags">
                            {allShellVars
                              .filter((sv) => !definedVars.some((dv) => dv.key.trim() === sv.varName))
                              .map((sv) => (
                                <button
                                  key={`shellvar-${sv.varName}`}
                                  className="loop-var-tag loop-var-tag-shell"
                                  onClick={() => {
                                    handleInsertVariable(sv.varName);
                                    setInsertVarOpen(false);
                                  }}
                                  title={t('loop.step.insertVarTitle', { tag: `{{${sv.varName}}}` })}
                                  type="button"
                                >
                                  {sv.varName}
                                </button>
                              ))}
                          </div>
                        </div>
                      )}
                      <div className="loop-insert-var-group">
                        <div className="loop-insert-var-group-label">{t('loop.step.insertVar.groupBuiltin')}</div>
                        <div className="loop-var-tags">
                          {builtinVars.map((bv) => (
                            <button
                              key={`builtin-${bv.key}`}
                              className="loop-var-tag loop-var-tag-builtin"
                              onClick={() => {
                                handleInsertVariable(bv.key);
                                setInsertVarOpen(false);
                              }}
                              title={t('loop.step.builtinVarInsertTitle', { desc: bv.desc, tag: `{{${bv.key}}}` })}
                              type="button"
                            >
                              {bv.key}
                            </button>
                          ))}
                        </div>
                      </div>
                      </div>
                    )}
                  </div>
                </div>
              </div>
              <div className="loop-round-message-wrap">
                <textarea
                  ref={textareaRef}
                  data-testid="loop-step-message-input"
                  value={node.message || ''}
                  onChange={(e) => {
                    handleFieldChange('message', e.target.value);
                    autoResize(e.target);
                  }}
                  onFocus={(e) => {
                    autoResize(e.target);
                  }}
                  placeholder={isEvaluatorRound ? t('loop.step.evaluator.promptPlaceholder') : t('loop.step.message.placeholder')}
                  rows={3}
                />
              </div>
            </div>
          ) : (
            <div className="loop-round-field">
              <div className="loop-round-field-top">
                <label>{t('loop.step.shell.label')}</label>
                <span className="loop-round-field-hint">{t('loop.step.shell.hint')}</span>
              </div>
              <ShellEditor
                value={node.message || ''}
                onChange={(v) => handleFieldChange('message', v)}
                placeholder={t('loop.step.shell.editorPlaceholder')}
              />

              <div className="loop-round-inline-note">
                <Trans
                  i18nKey="loop.step.shell.note"
                  values={{
                    setCmd: setCmdExample,
                    varTag: varTagExample,
                    breakCmd: 'quartet_break',
                    returnCmd: 'quartet_return',
                  }}
                  components={[<code />, <code />, <code />, <code />]}
                />
              </div>

              {detectedVars.length > 0 && (
                <div className="loop-round-setvar-detected">
                  <span className="loop-round-setvar-detected-label">{t('loop.step.shell.detectedVars')}</span>
                  {detectedVars.map((v) => (
                    <span key={v} className="loop-var-tag loop-var-tag-shell loop-var-tag-readonly">
                      {v}
                    </span>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
