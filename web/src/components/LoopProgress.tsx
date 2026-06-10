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
  // Hook-level error surfaced by useJobChat for loop actions (stop / continue /
  // cancel-stop / SSE connection). Distinct from progress.lastError, which is
  // the backend-persisted job execution failure. JobChat hides its top error
  // banner in loop mode, so without this these errors would be invisible.
  error?: string;
}

function pathToLabel(path: number[]): string {
  if (!path || path.length === 0) return '-';
  return path.map((p) => p + 1).join('.');
}

type LoopActionIconType = 'edit' | 'stop-after-step' | 'stop-now' | 'continue' | 'keep-running';

function LoopActionIcon({ type }: { type: LoopActionIconType }) {
  if (type === 'edit') {
    return (
      <svg className="loop-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M4 20h4.7L19.1 9.6a2.2 2.2 0 0 0 0-3.1l-1.6-1.6a2.2 2.2 0 0 0-3.1 0L4 15.3V20Z" />
        <path d="M13.2 6.1l4.7 4.7" />
      </svg>
    );
  }
  if (type === 'stop-after-step') {
    return (
      <svg className="loop-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M6 5v14" />
        <path d="M10 7h4a4 4 0 0 1 0 8h-4" />
        <path d="M17 17l2 2 3-4" />
      </svg>
    );
  }
  if (type === 'stop-now') {
    return (
      <svg className="loop-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <rect x="7" y="7" width="10" height="10" rx="2" />
      </svg>
    );
  }
  if (type === 'keep-running') {
    return (
      <svg className="loop-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
        <path d="M8 5v14l11-7-11-7Z" />
        <path d="M4 6v12" />
      </svg>
    );
  }
  return (
    <svg className="loop-action-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path d="M5 12a7 7 0 0 1 12.1-4.8L19 9" />
      <path d="M19 5v4h-4" />
      <path d="M19 12a7 7 0 0 1-12.1 4.8L5 15" />
      <path d="M5 19v-4h4" />
    </svg>
  );
}

export function LoopProgress({ progress, status, flow, onStop, stopPending, onCancelStop, onContinue, onEdit, error }: LoopProgressProps) {
  const { t } = useTranslation();
  const { totalSteps, completedCount, failedCount, currentPath, results, lastError, groupActualIterations, groupActualLeafCounts } = progress;
  const done = completedCount + failedCount;
  const percent = totalSteps > 0 ? Math.round((done / totalSteps) * 100) : 0;
  const hasResults = results && results.length > 0;
  const [collapsed, setCollapsed] = useState(true);
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null);
  const [lastErrorExpanded, setLastErrorExpanded] = useState(false);

  // Derive per-session / per-step position from the flow tree. When the flow
  // is unavailable (legacy job, hydration not yet done) we fall back to the
  // global "done / totalSteps" text.
  const sessionPlan = flow && flow.length > 0 ? computeLoopSessionPlan(flow, groupActualIterations, groupActualLeafCounts) : null;
  const loc = sessionPlan ? locateInSessionPlan(sessionPlan, currentPath) : null;
  const showSessionStats = !!(sessionPlan && sessionPlan.totalSessions > 0);

  const statusLabel = {
    idle: t('loop.progress.status.idle'),
    running: t('loop.progress.status.running'),
    completed: t('loop.progress.status.completed'),
    stopped: t('loop.progress.status.stopped'),
    failed: t('loop.progress.status.failed'),
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
  const hasControls = !!(onEdit || onStop || onContinue);

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
                <span className="loop-progress-current-path">{t('loop.progress.currentPath', { path: pathToLabel(currentPath) })}</span>
              )
            )}
          </span>
        </div>
        <div className="loop-progress-stats">
          <div className="loop-progress-meta">
            {!showSessionStats && (
              <span className="loop-progress-done">{done} / {totalSteps}</span>
            )}
            {hasResults && (
              <span className={`loop-progress-toggle ${collapsed ? 'collapsed' : ''}`}>▾</span>
            )}
          </div>
          {hasControls && (
            <div className="loop-progress-actions" aria-label={t('loop.progress.actionsLabel')}>
              {onEdit && (
                <button className="loop-edit-btn loop-action-btn" onClick={(e) => { e.stopPropagation(); onEdit(); }} data-testid="loop-edit-config-button" title={t('loop.actions.editConfigTitle')} aria-label={t('loop.actions.editConfigAria')}>
                  <LoopActionIcon type="edit" />
                  <span className="loop-action-label">{t('common.edit')}</span>
                </button>
              )}
              {onStop && (
                gracefulActive ? (
                  <button
                    className="loop-stop-btn loop-action-btn loop-stop-btn-keep-running"
                    onClick={(e) => { e.stopPropagation(); onCancelStop?.(); }}
                    data-testid="loop-keep-running-button"
                    title={t('loop.actions.keepRunningTitle')}
                    aria-label={t('loop.actions.keepRunning')}
                  >
                    <LoopActionIcon type="keep-running" />
                    <span className="loop-action-label">{t('loop.actions.keepRunning')}</span>
                  </button>
                ) : (
                  <button
                    className="loop-stop-btn loop-action-btn loop-stop-btn-graceful"
                    onClick={(e) => { e.stopPropagation(); onStop(true); }}
                    disabled={!isRunning}
                    data-testid="loop-stop-graceful-button"
                    title={t('loop.actions.stopAfterStepTitle')}
                    aria-label={t('loop.actions.stopAfterStepAria')}
                  >
                    <LoopActionIcon type="stop-after-step" />
                    <span className="loop-action-label">{t('loop.actions.stopAfterStep')}</span>
                  </button>
                )
              )}
              {onStop && (
                <button
                  className="loop-stop-btn loop-action-btn"
                  onClick={(e) => { e.stopPropagation(); onStop(false); }}
                  disabled={!isRunning}
                  data-testid="loop-stop-button"
                  title={t('loop.actions.stopNowTitle')}
                  aria-label={t('loop.actions.stopNow')}
                >
                  <LoopActionIcon type="stop-now" />
                  <span className="loop-action-label">{t('loop.actions.stopNow')}</span>
                </button>
              )}
              {onContinue && (
                <button
                  className="loop-stop-btn loop-action-btn loop-continue-btn"
                  onClick={(e) => { e.stopPropagation(); onContinue(); }}
                  disabled={!canContinue}
                  data-testid="loop-continue-button"
                  title={t('loop.actions.continueTitle')}
                  aria-label={t('loop.actions.continueAria')}
                >
                  <LoopActionIcon type="continue" />
                  <span className="loop-action-label">{t('loop.actions.continue')}</span>
                </button>
              )}
            </div>
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
          {t('loop.progress.failedCount', { count: failedCount })}
        </div>
      )}

      {error && (
        <div className="loop-progress-action-error" data-testid="loop-progress-action-error" role="alert">
          {error}
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
