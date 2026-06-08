import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FlowNode, JobProgress } from '../types';
import { computeLoopSessionPlan, locateInSessionPlan } from './LoopConfigPanel/utils';
import './LoopProgress.css';

interface LoopProgressProps {
  progress: JobProgress;
  status: 'idle' | 'running' | 'completed' | 'stopped' | 'failed';
  flow?: FlowNode[];
  onStop?: () => void;
  onContinue?: () => void;
}

function pathToLabel(path: number[]): string {
  if (!path || path.length === 0) return '-';
  return path.map((p) => p + 1).join('.');
}

export function LoopProgress({ progress, status, flow, onStop, onContinue }: LoopProgressProps) {
  const { t } = useTranslation();
  const { totalSteps, completedCount, failedCount, currentPath, results, lastError, lastJudgeDecision, conditionalActualIterations } = progress;
  const done = completedCount + failedCount;
  const percent = totalSteps > 0 ? Math.round((done / totalSteps) * 100) : 0;
  const hasResults = results && results.length > 0;
  const [collapsed, setCollapsed] = useState(true);
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null);
  const [lastErrorExpanded, setLastErrorExpanded] = useState(false);
  const [judgeExpanded, setJudgeExpanded] = useState(false);

  // Derive per-session / per-step position from the flow tree. When the flow
  // is unavailable (legacy job, hydration not yet done) we fall back to the
  // global "done / totalSteps" text.
  const sessionPlan = flow && flow.length > 0 ? computeLoopSessionPlan(flow, conditionalActualIterations) : null;
  const loc = sessionPlan ? locateInSessionPlan(sessionPlan, currentPath) : null;
  const showSessionStats = !!(sessionPlan && sessionPlan.totalSessions > 0);

  const statusLabel = {
    idle: 'Waiting',
    running: 'Running',
    completed: 'Completed',
    stopped: 'Stopped',
    failed: 'Failed',
  }[status];

  const statusClass = `loop-status-${status}`;
  const showLastError = !!lastError && (failedCount > 0 || status === 'failed');

  return (
    <div className={`loop-progress${collapsed ? ' collapsed' : ''}`} data-testid="loop-progress" data-loop-status={status} data-current-path={currentPath?.join('.') || ''}>
      <div className="loop-progress-header" onClick={() => hasResults && setCollapsed(!collapsed)} style={hasResults ? { cursor: 'pointer' } : undefined} data-testid="loop-progress-header">
        <div className="loop-progress-title">
          <span className={`loop-progress-status ${statusClass}`} data-testid="loop-progress-status">{statusLabel}</span>
          <span className="loop-progress-info">
            {showSessionStats && loc ? (
              <>
                <span className="loop-progress-session" data-testid="loop-progress-session">
                  {t('loop.progress.session', { current: Math.max(loc.sessionNumber, 0), total: loc.totalSessions })}
                </span>
                <span className="loop-progress-step" data-testid="loop-progress-step">
                  {t('loop.progress.step', { current: Math.max(loc.stepInSession, 0), total: loc.stepsInCurrentSession })}
                </span>
              </>
            ) : (
              currentPath && currentPath.length > 0 && (
                <span className="loop-progress-current-path">当前: {pathToLabel(currentPath)}</span>
              )
            )}
          </span>
        </div>
        <div className="loop-progress-stats">
          {!showSessionStats && (
            <span className="loop-progress-done">{done} / {totalSteps}</span>
          )}
          {hasResults && (
            <span className={`loop-progress-toggle ${collapsed ? 'collapsed' : ''}`}>▾</span>
          )}
          {status === 'running' && onStop && (
            <button className="loop-stop-btn" onClick={(e) => { e.stopPropagation(); onStop(); }} data-testid="loop-stop-button">Stop</button>
          )}
          {(status === 'stopped' || status === 'failed') && onContinue && (
            <button className="loop-stop-btn" onClick={(e) => { e.stopPropagation(); onContinue(); }} data-testid="loop-continue-button">Continue</button>
          )}
        </div>
      </div>

      <div className="loop-progress-bar-wrapper">
        <div
          className={`loop-progress-bar ${statusClass}`}
          data-testid="loop-progress-bar"
          style={{ width: `${percent}%` }}
        />
      </div>

      {failedCount > 0 && (
        <div className="loop-progress-fail-count">
          {failedCount} failed
        </div>
      )}

      {showLastError && (
        <div className="loop-progress-last-error" data-testid="loop-progress-error">
          <div
            className="loop-progress-last-error-header"
            onClick={(e) => { e.stopPropagation(); setLastErrorExpanded((v) => !v); }}
          >
            <span className="loop-progress-last-error-label">
              {t('loop.progress.lastError', 'Last error')}
            </span>
            <span
              className={`loop-progress-last-error-preview${lastErrorExpanded ? ' expanded' : ''}`}
              title={lastError}
            >
              {lastError}
            </span>
            <span className={`loop-progress-last-error-toggle${lastErrorExpanded ? ' expanded' : ''}`}>▾</span>
          </div>
          {lastErrorExpanded && (
            <pre className="loop-progress-last-error-full">{lastError}</pre>
          )}
        </div>
      )}

      {lastJudgeDecision && (
        <div className="loop-progress-judge" data-testid="loop-progress-judge" data-judge-stop={lastJudgeDecision.stop ? 'true' : 'false'}>
          <div
            className="loop-progress-judge-header"
            onClick={(e) => { e.stopPropagation(); setJudgeExpanded((v) => !v); }}
          >
            <span className="loop-progress-judge-label">
              {t('loop.progress.judge.label')}
            </span>
            <span className="loop-progress-judge-round">
              {t('loop.progress.judge.round', {
                current: lastJudgeDecision.iteration,
                total: lastJudgeDecision.maxIterations,
              })}
            </span>
            {lastJudgeDecision.stop && (
              <span className="loop-progress-judge-decision stop">
                {t('loop.progress.judge.stop')}
              </span>
            )}
            {lastJudgeDecision.reason && (
              <span className={`loop-progress-judge-toggle${judgeExpanded ? ' expanded' : ''}`}>▾</span>
            )}
          </div>
          {judgeExpanded && lastJudgeDecision.reason && (
            <pre className="loop-progress-judge-reason">{lastJudgeDecision.reason}</pre>
          )}
        </div>
      )}

      {!collapsed && hasResults && (
        <div className="loop-results-list">
          {results.map((r, idx) => (
            <div key={idx} className="loop-result-entry" data-testid="loop-result-entry" data-loop-path={r.path.join('.')} data-loop-success={r.success ? 'true' : 'false'}>
              <div
                className={`loop-result-item ${r.success ? 'success' : 'failed'}`}
                onClick={!r.success && (r.error || r.content) ? () => setExpandedIdx(expandedIdx === idx ? null : idx) : undefined}
                style={!r.success && (r.error || r.content) ? { cursor: 'pointer' } : undefined}
              >
                <span className="loop-result-icon">{r.success ? '✓' : '✗'}</span>
                <span className="loop-result-label">{pathToLabel(r.path)}</span>
                <span className="loop-result-duration">{(r.durationMs / 1000).toFixed(1)}s</span>
                {r.tokens > 0 && <span className="loop-result-tokens">{r.tokens >= 1000 ? `${(r.tokens / 1000).toFixed(1)}K` : r.tokens} tok</span>}
                {r.error && <span className="loop-result-error">error</span>}
              </div>
              {expandedIdx === idx && !r.success && (r.error || r.content) && (
                <div className="loop-result-error-detail">
                  {r.error && <div className="loop-result-error-msg">{r.error}</div>}
                  {r.content && <pre className="loop-result-error-output">{r.content}</pre>}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
