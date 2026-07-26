import { useEffect, useState } from 'react';

/**
 * useGitBranch resolves the current git branch for a directory via the backend
 * (`/api/v1/git-branch`) so the composer's workspace tag can show it next to
 * the path. Returns '' when the directory is not a git repo, is on a detached
 * HEAD, the request fails, or `enabled` is false.
 *
 * `enabled` should be false in shared/read-only views: the endpoint sits behind
 * the agent-auth middleware, so a public viewer would only get a 401 anyway.
 *
 * Refetches when the window regains focus so switching branches in a terminal
 * and tabbing back updates the badge without a reload.
 */
export function useGitBranch(path: string | undefined, enabled = true): string {
  const [branch, setBranch] = useState('');

  useEffect(() => {
    if (!enabled || !path) {
      setBranch('');
      return;
    }

    let cancelled = false;
    const load = async () => {
      try {
        // Auth header is injected by the global fetch interceptor in main.tsx.
        const res = await fetch(`/api/v1/git-branch?path=${encodeURIComponent(path)}`);
        if (!res.ok) {
          if (!cancelled) setBranch('');
          return;
        }
        const data = await res.json();
        if (!cancelled) setBranch(typeof data?.branch === 'string' ? data.branch : '');
      } catch {
        if (!cancelled) setBranch('');
      }
    };

    void load();
    const onFocus = () => { void load(); };
    window.addEventListener('focus', onFocus);
    return () => {
      cancelled = true;
      window.removeEventListener('focus', onFocus);
    };
  }, [path, enabled]);

  return branch;
}
