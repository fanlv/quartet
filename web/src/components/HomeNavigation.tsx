import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { useAuthPrincipal } from '../auth';
import { useConnectionStatus } from '../contexts/ConnectionStatus';
import { AgentsLocalEditor } from './AgentsLocalEditor';
import { FileBrowser } from './FileBrowser';
import './JobChat.css';
import './ChatPage.css';

const WEB_RESTART_POLL_INTERVAL_MS = 500;
const WEB_RESTART_PROBE_TIMEOUT_MS = 3000;
const WEB_RESTART_TIMEOUT_MS = 180_000;

interface WebHealthProbe {
  ok: boolean;
  instanceId: string;
}

export interface HomeNavigationProps {
  workspaceTitle?: string;
  workdir?: string;
  refreshKey?: number;
  activeView?: 'home' | 'stats' | 'graph';
  className?: string;
  pageTitle?: string;
  pageMark?: ReactNode;
  onBack?: () => void;
  backLabel?: string;
  pageActions?: ReactNode;
  onHome?: () => void;
  onOpenSettings?: () => void;
  onOpenStats?: () => void;
  onOpenGraph?: () => void;
}

async function probeWebHealth(): Promise<WebHealthProbe> {
  try {
    const probeUrl = `/api/v1/health?restartProbe=${Date.now()}`;
    const res = await fetch(probeUrl, {
      cache: 'no-store',
      signal: AbortSignal.timeout(WEB_RESTART_PROBE_TIMEOUT_MS),
    });
    if (!res.ok) return { ok: false, instanceId: '' };

    const body = await res.json().catch(() => null);
    return {
      ok: true,
      instanceId: typeof body?.instanceId === 'string' ? body.instanceId : '',
    };
  } catch {
    return { ok: false, instanceId: '' };
  }
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

async function waitForWebRestart(previousHealth: WebHealthProbe): Promise<boolean> {
  const deadline = Date.now() + WEB_RESTART_TIMEOUT_MS;
  let sawUnavailable = false;

  while (Date.now() < deadline) {
    await wait(WEB_RESTART_POLL_INTERVAL_MS);
    const currentHealth = await probeWebHealth();
    if (!currentHealth.ok) {
      sawUnavailable = true;
      continue;
    }

    const instanceChanged = !!previousHealth.instanceId
      && !!currentHealth.instanceId
      && currentHealth.instanceId !== previousHealth.instanceId;
    const instanceAdded = previousHealth.ok
      && !previousHealth.instanceId
      && !!currentHealth.instanceId;
    if (instanceChanged || instanceAdded || sawUnavailable) return true;
  }

  return false;
}

async function fetchUserSettings(): Promise<{ avatarUrl: string }> {
  try {
    const res = await fetch('/api/v1/config/settings/get');
    const data = await res.json().catch(() => null);
    if (data?.code === 0 && data.settings) {
      return { avatarUrl: data.settings.avatar_url || '' };
    }
  } catch (err) {
    console.error('Failed to fetch user settings:', err);
  }
  return { avatarUrl: '' };
}

export function HomeNavigation({
  workspaceTitle,
  workdir = '',
  refreshKey,
  activeView = 'home',
  className,
  pageTitle,
  pageMark,
  onBack,
  backLabel,
  pageActions,
  onHome,
  onOpenSettings,
  onOpenStats,
  onOpenGraph,
}: HomeNavigationProps) {
  const principal = useAuthPrincipal();
  const canReadConfig = principal?.permissions.includes('config.read') ?? false;
  const canReadFiles = principal?.permissions.includes('file.read') ?? false;
  const canWriteFiles = principal?.permissions.includes('file.write') ?? false;
  const canManageSystem = principal?.permissions.includes('system.manage') ?? false;
  const { buildTime } = useConnectionStatus();
  const { t, i18n } = useTranslation();
  const [userAvatarUrl, setUserAvatarUrl] = useState('');
  const [fileBrowserOpen, setFileBrowserOpen] = useState(false);
  const [agentsEditorOpen, setAgentsEditorOpen] = useState(false);
  const [webRestarting, setWebRestarting] = useState(false);
  const [restartConfirmOpen, setRestartConfirmOpen] = useState(false);

  useEffect(() => {
    if (pageTitle || !canReadConfig) {
      setUserAvatarUrl('');
      return;
    }
    let cancelled = false;
    void fetchUserSettings().then(({ avatarUrl }) => {
      if (!cancelled) setUserAvatarUrl(avatarUrl);
    });
    return () => { cancelled = true; };
  }, [canReadConfig, pageTitle, refreshKey]);

  const localizedBuildTime = useMemo(() => {
    if (!buildTime) return { full: '', compact: '' };
    const parsed = new Date(buildTime);
    if (Number.isNaN(parsed.getTime())) return { full: buildTime, compact: buildTime };
    const locale = i18n.resolvedLanguage || i18n.language;
    return {
      full: new Intl.DateTimeFormat(locale, {
        dateStyle: 'short',
        timeStyle: 'medium',
      }).format(parsed),
      compact: new Intl.DateTimeFormat(locale, {
        hour: '2-digit',
        minute: '2-digit',
        hour12: false,
      }).format(parsed),
    };
  }, [buildTime, i18n.language, i18n.resolvedLanguage]);

  const navigationMark = pageTitle ? (
    pageMark
  ) : userAvatarUrl ? (
    <img src={userAvatarUrl} alt="" className="header-user-avatar" referrerPolicy="no-referrer" />
  ) : (
    <svg className="header-logo-mark" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="4" y="8" width="16" height="12" rx="3" />
      <path d="M12 4v4" />
      <circle cx="12" cy="3" r="1" />
      <path d="M9.5 13h.01M14.5 13h.01" />
    </svg>
  );

  const handleRestartWeb = async () => {
    if (webRestarting) return;
    setRestartConfirmOpen(false);
    setWebRestarting(true);
    try {
      const previousHealth = await probeWebHealth();
      const res = await fetch('/api/v1/system/restart-web', { method: 'POST' });
      const data = await res.json().catch(() => null);
      if (!res.ok || data?.code !== 0) {
        throw new Error(data?.msg || `HTTP ${res.status}`);
      }
      const restarted = await waitForWebRestart(previousHealth);
      if (!restarted) {
        throw new Error(t('system.restartWebTimeout', {
          logPath: data?.log_path || '/tmp/quartet-web-restart.log',
        }));
      }
      window.location.reload();
    } catch (err) {
      console.error('Failed to restart web services:', err);
      setWebRestarting(false);
      window.alert(t('system.restartWebFailed', { message: err instanceof Error ? err.message : String(err) }));
    }
  };

  return (
    <>
      <header className={`chatbot-header${className ? ` ${className}` : ''}`} data-testid={activeView === 'home' ? 'home-header' : 'home-navigation'}>
        <div className="header-left">
          {onBack && (
            <button className="back-button" onClick={onBack} aria-label={backLabel}>
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
                <path d="M15 18l-6-6 6-6" />
              </svg>
            </button>
          )}
          <span className="header-logo">
            {onHome ? (
              <button
                type="button"
                className="header-home-button"
                onClick={onHome}
                title={t('chat.headerActions.home')}
                aria-label={t('chat.headerActions.home')}
              >
                {navigationMark}
              </button>
            ) : (
              navigationMark
            )}
            {' '}<span className="header-logo-text">{pageTitle || workspaceTitle || principal?.user.displayName || 'Quartet'}</span>
            {!pageTitle && localizedBuildTime.full && (
              <span className="home-build-time" title={`${t('home.buildTime')}: ${buildTime}`} data-testid="home-build-time">
                <span className="home-build-time-full">{t('home.buildTimeValue', { time: localizedBuildTime.full })}</span>
                <span className="home-build-time-compact">{localizedBuildTime.compact}</span>
              </span>
            )}
          </span>
        </div>
        <nav className="header-nav" aria-label={t('chat.headerActions.navigation')}>
          {pageActions}
          {canManageSystem && (
            <button
              className={`header-settings-btn header-restart-btn ${webRestarting ? 'restarting' : ''}`}
              onClick={() => setRestartConfirmOpen(true)}
              disabled={webRestarting}
              title={webRestarting ? t('system.restartWebRunning') : t('system.restartWeb')}
              aria-label={webRestarting ? t('system.restartWebRunning') : t('system.restartWeb')}
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M21 12a9 9 0 1 1-2.64-6.36" />
                <polyline points="21 3 21 9 15 9" />
              </svg>
            </button>
          )}
          {onOpenStats && (
            <button
              className={`header-filebrowser-btn ${activeView === 'stats' ? 'active' : ''}`}
              onClick={activeView === 'stats' ? undefined : onOpenStats}
              title={t('stats.topbarTooltip')}
              aria-label={t('stats.topbarTooltip')}
              aria-current={activeView === 'stats' ? 'page' : undefined}
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <line x1="18" y1="20" x2="18" y2="10" />
                <line x1="12" y1="20" x2="12" y2="4" />
                <line x1="6" y1="20" x2="6" y2="14" />
              </svg>
            </button>
          )}
          {onOpenGraph && (
            <button
              className={`header-filebrowser-btn ${activeView === 'graph' ? 'active' : ''}`}
              onClick={activeView === 'graph' ? undefined : onOpenGraph}
              title={t('chat.headerActions.graph')}
              aria-label={t('chat.headerActions.graph')}
              aria-current={activeView === 'graph' ? 'page' : undefined}
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <circle cx="5" cy="12" r="2" />
                <circle cx="12" cy="5" r="2" />
                <circle cx="19" cy="12" r="2" />
                <circle cx="12" cy="19" r="2" />
                <path d="M6.5 10.5 10.5 6.5" />
                <path d="M13.5 6.5 17.5 10.5" />
                <path d="M17.5 13.5 13.5 17.5" />
                <path d="M10.5 17.5 6.5 13.5" />
              </svg>
            </button>
          )}
          {canWriteFiles && (
            <button
              className={`header-filebrowser-btn ${agentsEditorOpen ? 'active' : ''}`}
              onClick={() => setAgentsEditorOpen((open) => !open)}
              title={t('chat.headerActions.agents')}
              aria-label={t('chat.headerActions.agents')}
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
                <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
                <polyline points="14 2 14 8 20 8" />
                <line x1="16" y1="13" x2="8" y2="13" />
                <line x1="16" y1="17" x2="8" y2="17" />
                <polyline points="10 9 9 9 8 9" />
              </svg>
            </button>
          )}
          {canReadFiles && (
            <button
              className={`header-filebrowser-btn ${fileBrowserOpen ? 'active' : ''}`}
              onClick={() => setFileBrowserOpen((open) => !open)}
              title={t('chat.headerActions.files')}
              aria-label={t('chat.headerActions.files')}
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true">
                <path d="M22 19a2 2 0 01-2 2H4a2 2 0 01-2-2V5a2 2 0 012-2h5l2 3h9a2 2 0 012 2z" />
              </svg>
            </button>
          )}
          {onOpenSettings && (
            <button
              className="header-settings-btn"
              onClick={onOpenSettings}
              title={t('chat.headerActions.settings')}
              aria-label={t('chat.headerActions.settings')}
              data-testid="settings-open-button"
            >
              <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <circle cx="12" cy="12" r="3" />
                <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09A1.65 1.65 0 0 0 19.4 15z" />
              </svg>
            </button>
          )}
        </nav>
      </header>

      {canReadFiles && fileBrowserOpen && createPortal(
        <FileBrowser rootPath={workdir} onClose={() => setFileBrowserOpen(false)} />,
        document.body,
      )}

      {canWriteFiles && agentsEditorOpen && workdir && createPortal(
        <AgentsLocalEditor workdir={workdir} onClose={() => setAgentsEditorOpen(false)} />,
        document.body,
      )}

      {canManageSystem && restartConfirmOpen && (
        <div className="delete-confirm-overlay" onClick={() => setRestartConfirmOpen(false)}>
          <div className="delete-confirm-dialog restart-confirm-dialog" onClick={(event) => event.stopPropagation()}>
            <div className="delete-confirm-title">{t('system.restartWebConfirmTitle')}</div>
            <div className="delete-confirm-message">{t('system.restartWebConfirmMessage')}</div>
            <div className="delete-confirm-actions">
              <button className="delete-confirm-cancel" onClick={() => setRestartConfirmOpen(false)}>
                {t('system.restartWebCancel')}
              </button>
              <button className="delete-confirm-ok restart-confirm-ok" onClick={handleRestartWeb}>
                {t('system.restartWebConfirm')}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
