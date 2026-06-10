import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FlowNode, JobProgress } from '../types';
import { computeLoopSessionPlan, locateInSessionPlan } from './LoopConfigPanel/utils';
import './LoopProgress.css';

interface LoopProgressProps {
  progress: JobProgress;
  status: 'idle' | 'running' | 'completed' | 'stopped' | 'failed';
  flow?: FlowNode[];
  // onStop receives graceful=true to finish the current step before stopping,
  // or graceful=false (default) for an immediate hard stop.
  onStop?: (graceful?: boolean) => void;
  // True when a graceful "stop after step" has been requested but the loop has
  // not yet reached the boundary where it stops. Swaps the stop buttons for a
  // single "keep running" button wired to onCancelStop.
  stopPending?: boolean;
  onCancelStop?: () => void;
  onContinue?: () => void;
  onEdit?: () => void;
}

function pathToLabel(path: number[]): string {
  if (!path || path.length === 0) return '-';
  return path.map((p) => p + 1).join('.');
}

export function LoopProgress({ progress, status, flow, onStop, stopPending, onCancelStop, onContinue, onEdit }: LoopProgressProps) {
  const { t } = useTranslation();
  const { totalSteps, completedCount, failedCount, currentPath, results, lastError, groupActualIterations } = progress;
  const done = completedCount + failedCount;
  const percent = totalSteps > 0 ? Math.round((done / totalSteps) * 100) : 0;
  const hasResults = results && results.length > 0;
  const [collapsed, setCollapsed] = useState(true);
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null);
  const [lastErrorExpanded, setLastErrorExpanded] = useState(false);

  // Derive per-session / per-step position from the flow tree. When the flow
  // is unavailable (legacy job, hydration not yet done) we fall back to the
  // global "done / totalSteps" text.
  const sessionPlan = flow && flow.length > 0 ? computeLoopSessionPlan(flow, groupActualIterations) : null;
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

  // Button enablement is derived purely from status + stopPending so the button
  // states always track the loop state. Buttons are never hidden — inapplicable
  // ones are disabled. The first stop slot toggles between "Stop after step" and
  // "Keep running" (the two faces of the graceful-stop request).
  const isRunning = status === 'running';
  const gracefulActive = isRunning && !!stopPending;
  const canContinue = status === 'stopped' || status === 'failed';

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
          {onEdit && (
            <button className="loop-edit-btn" onClick={(e) => { e.stopPropagation(); onEdit(); }} data-testid="loop-edit-config-button" title="Edit Config">Edit</button>
          )}
          {onStop && (
            gracefulActive ? (
              <button
                className="loop-stop-btn loop-stop-btn-keep-running"
                onClick={(e) => { e.stopPropagation(); onCancelStop?.(); }}
                data-testid="loop-keep-running-button"
                title="Cancel the pending stop and keep the loop running"
              >
                Keep running
              </button>
            ) : (
              <button
                className="loop-stop-btn loop-stop-btn-graceful"
                onClick={(e) => { e.stopPropagation(); onStop(true); }}
                disabled={!isRunning}
                data-testid="loop-stop-graceful-button"
                title="Finish the current step, then stop"
              >
                Stop after step
              </button>
            )
          )}
          {onStop && (
            <button
              className="loop-stop-btn"
              onClick={(e) => { e.stopPropagation(); onStop(false); }}
              disabled={!isRunning}
              data-testid="loop-stop-button"
              title="Stop immediately"
            >
              Stop now
            </button>
          )}
          {onContinue && (
            <button
              className="loop-stop-btn loop-continue-btn"
              onClick={(e) => { e.stopPropagation(); onContinue(); }}
              disabled={!canContinue}
              data-testid="loop-continue-button"
              title="Continue from the next step"
            >
              Continue
            </button>
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
