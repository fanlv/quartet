export type WorkspaceView = 'stats' | 'timers' | 'graph';

function query(): URLSearchParams {
  return new URLSearchParams(window.location.search);
}

export function getJobIdFromUrl(): string | undefined {
  return query().get('jobId') || undefined;
}

export function getSessionIdFromUrl(): string | undefined {
  return query().get('sessionId') || undefined;
}

export function getWorkspaceIdFromUrl(): string | undefined {
  return query().get('workspaceId') || undefined;
}

export function getShareTokenFromUrl(): string | undefined {
  return query().get('shareToken') || undefined;
}

export function getWorkspaceViewFromUrl(): WorkspaceView | undefined {
  const view = query().get('view');
  if (view === 'stats' || view === 'timers' || view === 'graph') return view;
}

export function updateUrlWithJobId(jobId: string, keepSessionId = false): void {
  const url = new URL(window.location.href);
  if (!keepSessionId) url.searchParams.delete('sessionId');
  url.searchParams.delete('view');
  url.searchParams.set('jobId', jobId);
  window.history.pushState({}, '', url.toString());
}

export function updateUrlWithWorkspaceId(workspaceId: string): void {
  const url = new URL(window.location.href);
  url.searchParams.delete('jobId');
  url.searchParams.delete('sessionId');
  url.searchParams.delete('view');
  url.searchParams.set('workspaceId', workspaceId);
  window.history.pushState({}, '', url.toString());
}

export function updateUrlWithWorkspaceView(
  view?: WorkspaceView,
  { keepJob = false, workspaceId }: { keepJob?: boolean; workspaceId?: string } = {},
): void {
  const url = new URL(window.location.href);
  if (view) url.searchParams.set('view', view);
  else url.searchParams.delete('view');
  if (workspaceId) url.searchParams.set('workspaceId', workspaceId);
  if (!keepJob) {
    for (const key of ['jobId', 'sessionId', 'timerTab', 'timerScheduleId', 'timerExecutionId']) {
      url.searchParams.delete(key);
    }
  }
  url.searchParams.delete('graphEditJob');
  if (url.href !== window.location.href) window.history.pushState({}, '', url.href);
}
