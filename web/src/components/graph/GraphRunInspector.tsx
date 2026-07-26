import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import type { GraphHookResult, GraphInstanceState, GraphNode } from '../../types/graph';
import { kindOf, labelOf } from './nodes/kinds';

interface GraphRunInspectorProps {
  node: GraphNode | null;
  // The selected node's run instance, if any. End nodes record NO instance, so
  // this is undefined for them — the hook section still renders from hookResult.
  instance?: GraphInstanceState;
  // The selected node's latest hook execution result, if the node ran a hook.
  hookResult?: GraphHookResult;
  drawerOpen?: boolean;
  onDrawerToggle?: () => void;
}

// OutputBlock renders a labelled, scrollable monospace block for captured
// stdout/stderr. `defaultOpen` expands it on mount (used for a failed hook's
// stderr, the thing a user most wants to see).
function OutputBlock({ label, text, defaultOpen }: { label: string; text: string; defaultOpen?: boolean }) {
  if (!text) return null;
  return (
    <details className="gri-output" open={defaultOpen}>
      <summary>{label}</summary>
      <pre className="gri-pre">{text}</pre>
    </details>
  );
}

// GraphRunInspector is the READ-ONLY node-detail panel shown during run
// playback, where the editable GraphInspector is hidden. It surfaces what a node
// did at runtime: its instance status + error (stderr/exitCode), and its node
// hook (§ 节点 Hook) execution result. Node title/type are taken from the hook
// result payload first (so an End node, which has no instance, still labels
// itself) and fall back to the editor node.
export function GraphRunInspector({ node, instance, hookResult, drawerOpen = true, onDrawerToggle }: GraphRunInspectorProps) {
  const { t } = useTranslation();
  // Same mobile bottom-drawer expand/collapse as GraphInspector; desktop hides
  // the button and ignores this state.
  const [drawerFull, setDrawerFull] = useState(false);
  const asideClassName = `graph-inspector ${drawerOpen ? 'drawer-open' : 'drawer-collapsed'}${drawerFull ? ' drawer-full' : ''}`;

  // Derive node identity from the most reliable source available. In run-view the
  // editor `node` may be stale or absent (the canvas is driven by the run's replay
  // config), whereas the hook result and the instance carry identity captured from
  // the run itself, so prefer those.
  const nodeType = hookResult?.nodeType || instance?.nodeType || node?.type;
  const nodeTitle = hookResult?.nodeTitle || instance?.nodeTitle || node?.title || instance?.nodeId || node?.id || '';
  const k = nodeType ? kindOf(nodeType) : null;

  const DrawerHeader = (
    <div className="gi-drawer-bar">
      <button
        type="button"
        className="gi-drawer-handle"
        data-testid="graph-run-inspector-drawer-toggle"
        aria-expanded={drawerOpen}
        onClick={onDrawerToggle}
        disabled={!onDrawerToggle}
      >
        <span className="gi-drawer-grip" aria-hidden="true" />
        <span className="gi-drawer-title">
          <span>{t('graph.runInspector.title')}</span>
          <small>{nodeTitle}</small>
        </span>
        <span className="gi-drawer-chevron" aria-hidden="true">
          {drawerOpen ? '⌄' : '⌃'}
        </span>
      </button>
      <button
        type="button"
        className="gi-drawer-expand"
        data-testid="graph-run-inspector-drawer-expand"
        aria-label={drawerFull ? t('graph.inspector.collapse') : t('graph.inspector.expand')}
        aria-pressed={drawerFull}
        title={drawerFull ? t('graph.inspector.collapse') : t('graph.inspector.expand')}
        onClick={() => {
          if (!drawerOpen) {
            setDrawerFull(true);
            onDrawerToggle?.();
          } else {
            setDrawerFull((v) => !v);
          }
        }}
      >
        {drawerFull ? (
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M8 3v3a2 2 0 0 1-2 2H3m18 0h-3a2 2 0 0 1-2-2V3m0 18v-3a2 2 0 0 1 2-2h3M3 16h3a2 2 0 0 1 2 2v3" />
          </svg>
        ) : (
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M8 3H5a2 2 0 0 0-2 2v3m18 0V5a2 2 0 0 0-2-2h-3m0 18h3a2 2 0 0 0 2-2v-3M3 16v3a2 2 0 0 0 2 2h3" />
          </svg>
        )}
      </button>
    </div>
  );

  if (!node && !instance && !hookResult) {
    return (
      <aside className={asideClassName} data-testid="graph-run-inspector">
        {DrawerHeader}
        <div className="gi-scroll">
          <div className="gi-desc">{t('graph.runInspector.selectNode')}</div>
        </div>
      </aside>
    );
  }

  const hookFailed = hookResult?.status === 'failed';

  return (
    <aside className={asideClassName} data-testid="graph-run-inspector">
      {DrawerHeader}
      <div className="gi-scroll">
        <h3>{t('graph.runInspector.title')}</h3>
        {k && nodeType && (
          <div className="gi-kind-badge" style={{ background: `${k.color}22`, color: k.color, borderColor: `${k.color}55` }}>
            {k.icon} {labelOf(t, nodeType)}
          </div>
        )}

        {/* Instance status + error. End nodes record no instance, so this is
            absent for them; the hook section below is the meaningful content. */}
        {instance ? (
          <div className="gi-section">
            <div className="gi-field">
              <label>{t('graph.runInspector.nodeStatus')}</label>
              <span className={`gri-status status-${instance.status}`}>
                {t(`graph.runInspector.instanceStatus.${instance.status}`)}
              </span>
            </div>
            {instance.error && (
              <div className="gi-field">
                <label>{t('graph.runInspector.instanceError')}</label>
                {instance.error.message && <pre className="gri-pre gri-pre-error">{instance.error.message}</pre>}
                {typeof instance.error.exitCode === 'number' && (
                  <div className="gi-desc">{t('graph.runInspector.hookExitCode')}: {instance.error.exitCode}</div>
                )}
                <OutputBlock label={t('graph.runInspector.hookStderr')} text={instance.error.stderr || ''} defaultOpen />
                <OutputBlock label={t('graph.runInspector.hookStdout')} text={instance.error.stdout || ''} />
              </div>
            )}
          </div>
        ) : (
          !hookResult && <div className="gi-desc">{t('graph.runInspector.noInstance')}</div>
        )}

        {/* Hook execution result (§ 节点 Hook). */}
        <div className="gi-section">
          <h3>{t('graph.runInspector.hookResult')}</h3>
          {hookResult ? (
            <>
              <div className="gi-field">
                <span className={`gri-status ${hookFailed ? 'status-failed' : 'status-succeeded'}`}>
                  {hookFailed ? t('graph.runInspector.hookStatusFailed') : t('graph.runInspector.hookStatusCompleted')}
                </span>
              </div>
              {hookResult.source && (
                <div className="gi-desc">
                  {t('graph.runInspector.hookSource')}: {t(`graph.runInspector.hookSourceValue.${hookResult.source}`, hookResult.source)}
                </div>
              )}
              {typeof hookResult.exitCode === 'number' && (
                <div className="gi-desc">{t('graph.runInspector.hookExitCode')}: {hookResult.exitCode}</div>
              )}
              {hookFailed && hookResult.message && <pre className="gri-pre gri-pre-error">{hookResult.message}</pre>}
              <OutputBlock label={t('graph.runInspector.hookStderr')} text={hookResult.stderr || ''} defaultOpen={hookFailed} />
              <OutputBlock label={t('graph.runInspector.hookStdout')} text={hookResult.stdout || ''} defaultOpen={!hookFailed} />
            </>
          ) : (
            <div className="gi-desc">{t('graph.runInspector.noHook')}</div>
          )}
        </div>
      </div>
    </aside>
  );
}
