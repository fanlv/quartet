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

export function getStatsOpenFromUrl(): boolean {
  return query().get('view') === 'stats';
}

export function getGraphOpenFromUrl(): boolean {
  return query().get('view') === 'graph';
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

export function updateUrlWithStats(open: boolean): void {
  const url = new URL(window.location.href);
  if (open) {
    url.searchParams.set('view', 'stats');
    window.history.pushState({}, '', url.toString());
  } else {
    url.searchParams.delete('view');
    window.history.replaceState({}, '', url.toString());
  }
}

export function updateUrlWithGraph(open: boolean): void {
  const url = new URL(window.location.href);
  if (open) {
    url.searchParams.delete('jobId');
    url.searchParams.delete('sessionId');
    url.searchParams.set('view', 'graph');
    window.history.pushState({}, '', url.toString());
  } else {
    url.searchParams.delete('view');
    window.history.replaceState({}, '', url.toString());
  }
}

export function graphUrl(): string {
  const url = new URL(window.location.href);
  url.searchParams.delete('jobId');
  url.searchParams.delete('sessionId');
  url.searchParams.set('view', 'graph');
  return url.toString();
}
