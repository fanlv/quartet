import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FlowNode, JobProgress } from '../types';
import { computeLoopSessionPlan, locateInSessionPlan } from '../utils/loopSessionPlan';
import './LoopProgress.css';

interface LoopProgressProps {
  progress: JobProgress;
  status: 'idle' | 'running' | 'completed' | 'stopped' | 'failed';
  flow?: FlowNode[];
  // Read-only archive: loop mode is retired, so this component renders
  // historical progress only — no stop / continue / edit actions.
  // Hook-level error surfaced by useJobChat. Distinct from progress.lastError,
  // which is the backend-persisted job execution failure. JobChat hides its
  // top error banner in loop mode, so without this these errors would be
  // invisible.
  error?: string;
}

function pathToLabel(path: number[]): string {
  if (!path || path.length === 0) return '-';
  return path.map((p) => p + 1).join('.');
}

export function LoopProgress({ progress, status, flow, error }: LoopProgressProps) {
  const { t } = useTranslation();
  const { totalSteps, completedCount, failedCount, currentPath, results, lastError, persistWarnings, groupActualIterations, groupActualLeafCounts, skippedPaths } = progress;
  const done = completedCount + failedCount;
  const skippedCount = skippedPaths ? Object.keys(skippedPaths).length : 0;
  // totalSteps can legitimately reach 0 when every step was empty-prompt
  // skipped — a Completed 0/0 run is fully done, not 0% done.
  const percent = totalSteps > 0
    ? Math.round((done / totalSteps) * 100)
    : (status === 'completed' ? 100 : 0);
  const hasResults = results && results.length > 0;
  const [collapsed, setCollapsed] = useState(true);
  const [expandedIdx, setExpandedIdx] = useState<number | null>(null);
  const [lastErrorExpanded, setLastErrorExpanded] = useState(false);

  // Derive per-session / per-step position from the flow tree. When the flow
  // is unavailable (legacy job, hydration not yet done) we fall back to the
  // global "done / totalSteps" text.
  const sessionPlan = flow && flow.length > 0 ? computeLoopSessionPlan(flow, groupActualIterations, groupActualLeafCounts, skippedPaths) : null;
  let loc = sessionPlan ? locateInSessionPlan(sessionPlan, currentPath) : null;
  // A completed run with no currentPath means the cursor was cleared past the
  // final leaf (e.g. the tail steps were empty-prompt skips, which clear it).
  // Everything in the plan is done — snap the location to the last leaf so the
  // summary reads "Session Y / Y · Step N / N" instead of "Session 0 / Y".
  if (loc && sessionPlan && status === 'completed' && (!currentPath || currentPath.length === 0) && loc.sessionNumber === 0 && sessionPlan.totalSessions > 0) {
    const lastSession = sessionPlan.totalSessions;
    const lastCount = sessionPlan.sessionStepCounts[lastSession - 1] || 0;
    loc = {
      sessionNumber: lastSession,
      totalSessions: lastSession,
      stepInSession: lastCount,
      stepsInCurrentSession: lastCount,
    };
  }
  // Degrade to the global "done / total" counter when currentPath cannot be
  // located in the filtered plan (e.g. it points at a leaf the skip filter
  // removed): a zeroed "Session 0 / Y · Step 0 / N" reads as broken state.
  // An empty currentPath on a non-terminal run (not started yet) keeps the
  // session summary with its zeroed values hidden by Math.max below.
  const locMissed = !!(loc && currentPath && currentPath.length > 0 && loc.sessionNumber === 0);
  const showSessionStats = !!(sessionPlan && sessionPlan.totalSessions > 0) && !locMissed;

  const statusLabel = {
    idle: t('loop.progress.status.idle'),
    running: t('loop.progress.status.running'),
    completed: t('loop.progress.status.completed'),
    stopped: t('loop.progress.status.stopped'),
    failed: t('loop.progress.status.failed'),
  }[status];

  const statusClass = `loop-status-${status}`;
  const showLastError = !!lastError && (failedCount > 0 || status === 'failed');
  const showPersistWarnings = !!persistWarnings && persistWarnings.length > 0;

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
        </div>
      </div>

      <div className="loop-progress-bar-wrapper">
        <div
          className={`loop-progress-bar ${statusClass}`}
          data-testid="loop-progress-bar"
          style={{ width: `${percent}%` }}
        />
      </div>

      {skippedCount > 0 && (
        <div className="loop-progress-skip-count" data-testid="loop-progress-skip-count">
          {t('loop.progress.skippedCount', { count: skippedCount })}
        </div>
      )}

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

      {showPersistWarnings && (
        <div className="loop-progress-persist-warning" data-testid="loop-progress-persist-warning" role="alert">
          <div className="loop-progress-persist-warning-label">
            {t('loop.progress.persistWarnings', 'Persistence warnings')}
          </div>
          <ul className="loop-progress-persist-warning-list">
            {persistWarnings.map((warning, idx) => (
              <li key={`${idx}-${warning}`}>{warning}</li>
            ))}
          </ul>
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
