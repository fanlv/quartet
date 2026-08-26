import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useJobList, type JobSummary } from '../hooks/useJobList';
import { VirtualList } from './VirtualList';
import { useAuthPrincipal } from '../auth';
import './Sidebar.css';

type JobInfo = JobSummary;

// Fixed row height used by the VirtualList. Must stay in sync with
// .sidebar-session-item height in Sidebar.css.
const SESSION_ROW_HEIGHT = 44;

interface SidebarProps {
  currentJobId?: string;
  workspaceId?: string;
  onNewChat: () => void;
  onSelectJob: (jobId: string) => void;
  onOpenSettings: () => void;
  settingsRefreshKey?: number;
}

export function Sidebar({ currentJobId, workspaceId, onNewChat, onSelectJob, onOpenSettings, settingsRefreshKey }: SidebarProps) {
  const { t } = useTranslation();
  const principal = useAuthPrincipal();
  const canReadJobs = principal?.permissions.includes('job.read') ?? false;
  const canExecuteJobs = principal?.permissions.includes('job.execute') ?? false;
  const canManageJobs = principal?.permissions.includes('job.manage') ?? false;
  const canReadConfig = principal?.permissions.includes('config.read') ?? false;
  const {
    jobs,
    hasMore,
    isLoading,
    isLoadingMore,
    loadMore,
    removeJob,
    patchJob,
  } = useJobList({ workspaceId, disabled: !canReadJobs });
  const username = principal?.user.displayName || principal?.user.username || 'User';
  const [avatarUrl, setAvatarUrl] = useState('');
  const [editingJobId, setEditingJobId] = useState<string | null>(null);
  const [editingTitle, setEditingTitle] = useState('');
  const [renameError, setRenameError] = useState('');

  useEffect(() => {
    if (!canReadConfig) {
      setAvatarUrl('');
      return;
    }
    const fetchSettings = async () => {
      try {
        const res = await fetch('/api/v1/config/settings/get');
        const data = await res.json();
        if (data.code === 0 && data.settings) {
          setAvatarUrl(data.settings.avatar_url || '');
        }
      } catch {
        // ignore
      }
    };
    fetchSettings();
  }, [canReadConfig, settingsRefreshKey]);

  const [deleteConfirm, setDeleteConfirm] = useState<{ jobId: string; title: string } | null>(null);

  const handleDeleteClick = (e: React.MouseEvent, job: JobInfo) => {
    e.stopPropagation();
    setDeleteConfirm({ jobId: job.id, title: getJobTitle(job) });
  };

  const handleDeleteConfirm = async () => {
    if (!deleteConfirm) return;
    const { jobId } = deleteConfirm;
    setDeleteConfirm(null);
    try {
      const response = await fetch(`/api/v1/job/${jobId}`, {
        method: 'DELETE',
      });
      if (response.ok) {
        removeJob(jobId);
        if (currentJobId === jobId) {
          onNewChat();
        }
      }
    } catch (error) {
      console.error('Failed to delete job:', error);
    }
  };

  const startRename = (e: React.MouseEvent, job: JobInfo) => {
    e.preventDefault();
    e.stopPropagation();
    setRenameError('');
    setEditingJobId(job.id);
    setEditingTitle(getJobTitle(job));
  };

  const cancelRename = () => {
    setEditingJobId(null);
    setEditingTitle('');
  };

  const commitRename = async (job: JobInfo) => {
    const title = editingTitle.trim();
    if (!title || title === getJobTitle(job)) {
      cancelRename();
      return;
    }

    try {
      const response = await fetch(`/api/v1/job/${job.id}/title`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title }),
      });
      const bodyText = await response.text();
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${bodyText}`);
      }
      let savedTitle = title;
      if (bodyText) {
        try {
          const data = JSON.parse(bodyText) as { title?: string };
          savedTitle = data.title || title;
        } catch {
          // Keep the requested title if the backend ever returns a plain text body.
        }
      }
      patchJob(job.id, { title: savedTitle, updatedAt: Date.now() });
      cancelRename();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      setRenameError(`Failed to rename job ${job.id}: ${message}`);
    }
  };

  const isJobRunning = (job: JobInfo) =>
    job.status === 'running';

  const getJobIcon = (job: JobInfo) => {
    if (job.mode === 'graph') return '◇';
    return '💬';
  };

  const getJobTitle = (job: JobInfo) => {
    const title = job.title?.trim();
    return title ? title : 'undefined title';
  };

  return (
    <aside className="sidebar" data-testid="sidebar">
      <div className="sidebar-header">
        <div className="sidebar-logo">
          <span className="logo-icon">🤖</span>
          <span className="logo-text">Quartet</span>
        </div>
      </div>

      {canExecuteJobs && <div className="sidebar-new-chat" onClick={onNewChat} data-testid="sidebar-new-chat-button">
        <span className="new-chat-icon">+</span>
        <span>{t('sidebar.newChat')}</span>
      </div>}

      <div className="sidebar-section" data-testid="sidebar-job-section">
        <div className="sidebar-section-title">{t('sidebar.jobHistory')}</div>
        {isLoading && jobs.length === 0 ? (
          <div className="sidebar-loading">{t('sidebar.loading')}</div>
        ) : (
          <>
          {renameError && <div className="sidebar-error" data-testid="sidebar-error">{renameError}</div>}
          <VirtualList<JobInfo>
            className="sidebar-sessions"
            items={jobs}
            itemHeight={SESSION_ROW_HEIGHT}
            getKey={(job) => job.id}
            onEndReached={hasMore ? () => { void loadMore(); } : undefined}
            empty={<div className="sidebar-empty" data-testid="sidebar-empty">{t('sidebar.noConversations')}</div>}
            footer={hasMore ? (
              <div className="sidebar-loadmore">
                <button
                  className="sidebar-loadmore-btn"
                  onClick={() => { void loadMore(); }}
                  disabled={isLoadingMore}
                >
                  {isLoadingMore ? t('sidebar.loadingMore') : t('sidebar.loadMore')}
                </button>
              </div>
            ) : null}
            renderItem={(job) => {
              const jobUrl = (() => {
                const url = new URL(window.location.href);
                url.searchParams.delete('sessionId');
                url.searchParams.delete('view');
                url.searchParams.set('jobId', job.id);
                return url.toString();
              })();
              return (
                <a
                  className={`sidebar-session-item ${currentJobId === job.id ? 'active' : ''}`}
                  data-testid="sidebar-job-item"
                  data-job-id={job.id}
                  data-job-mode={job.mode || 'interactive'}
                  data-job-status={job.status}
                  data-active={currentJobId === job.id ? 'true' : 'false'}
                  href={jobUrl}
                  onClick={(e) => {
                    if (e.metaKey || e.ctrlKey) {
                      e.stopPropagation();
                      return;
                    }
                    e.preventDefault();
                    onSelectJob(job.id);
                  }}
                  onAuxClick={(e) => {
                    // Middle-click: let browser open in new tab
                    if (e.button === 1) e.stopPropagation();
                  }}
                >
                  <span className="session-icon">{getJobIcon(job)}</span>
                  {isJobRunning(job) && <span className="session-running-dot" />}
                  {editingJobId === job.id ? (
                    <input
                      className="session-title-input"
                      data-testid="sidebar-job-rename-input"
                      value={editingTitle}
                      autoFocus
                      onChange={(e) => setEditingTitle(e.target.value)}
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation();
                      }}
                      onKeyDown={(e) => {
                        e.stopPropagation();
                        if (e.key === 'Enter') {
                          e.preventDefault();
                          void commitRename(job);
                        }
                        if (e.key === 'Escape') {
                          e.preventDefault();
                          cancelRename();
                        }
                      }}
                      onBlur={() => { void commitRename(job); }}
                    />
                  ) : (
                    <span
                      className="session-title"
                      data-testid="sidebar-job-title"
                      title={getJobTitle(job)}
                      onDoubleClick={canManageJobs ? (e) => startRename(e, job) : undefined}
                    >
                      {getJobTitle(job)}
                    </span>
                  )}
                  {canManageJobs && <button
                    className="session-delete-btn"
                    onClick={(e) => handleDeleteClick(e, job)}
                    title={t('common.delete')}
                    data-testid="sidebar-job-delete-button"
                  >
                    ×
                  </button>}
                </a>
              );
            }}
          />
          </>
        )}
      </div>

      <div className="sidebar-footer">
        <div className="sidebar-settings" onClick={onOpenSettings} data-testid="settings-open-button">
          <span className="settings-icon">⚙️</span>
          <span className="settings-text">{t('sidebar.settings')}</span>
        </div>
        <div className="sidebar-user">
          {avatarUrl ? (
            <img className="user-avatar-img" src={avatarUrl} alt={username} />
          ) : (
            <span className="user-avatar">👤</span>
          )}
          <span className="user-name">{username}</span>
        </div>
      </div>

      {deleteConfirm && (
        <div className="delete-confirm-overlay" onClick={() => setDeleteConfirm(null)} data-testid="delete-confirm-overlay">
          <div className="delete-confirm-dialog" onClick={(e) => e.stopPropagation()} data-testid="delete-confirm-dialog">
            <div className="delete-confirm-title">{t('sidebar.deleteJob')}</div>
            <div className="delete-confirm-message">
              {t('sidebar.deleteConfirmMessage', { title: deleteConfirm.title })}
            </div>
            <div className="delete-confirm-actions">
              <button className="delete-confirm-cancel" onClick={() => setDeleteConfirm(null)} data-testid="delete-confirm-cancel">{t('common.cancel')}</button>
              <button className="delete-confirm-ok" onClick={handleDeleteConfirm} data-testid="delete-confirm-ok">{t('common.delete')}</button>
            </div>
          </div>
        </div>
      )}
    </aside>
  );
}
