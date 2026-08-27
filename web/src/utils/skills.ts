// Installed-skill list for the chat input's "/" completion and skill-name
// highlighting. Backed by GET /api/v1/skills/list, which is scoped: the global
// scope is shared by every agent run, while the project scope belongs to one
// workspace directory (ACP agents run with the workspace workdir as their cwd,
// so only that directory's project skills are loadable).
//
// Results are cached at module level, keyed by workspace, so remounting
// ChatInput never re-runs the (npx-based, comparatively slow) backend call.
// Settings drops the cache after installing / uninstalling / updating skills so
// the completion list does not keep serving a stale snapshot until reload.

export interface SkillInfo {
  name: string;
  path: string;
  scope: string;
  agents: string[];
  source?: string;
  sourceUrl?: string;
  sourceType?: string;
}

export interface SkillScopeResult {
  skills: SkillInfo[];
  /** False while the backend's first listing for this scope is still running. */
  ready: boolean;
  /** Full text of the backend's last failed listing attempt, if any. */
  error: string;
}

const cachedSkills = new Map<string, SkillInfo[]>();
const inflight = new Map<string, Promise<SkillInfo[]>>();

const READY_RETRY_DELAY_MS = 400;
const READY_RETRY_ATTEMPTS = 25;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

/** One raw scope read. Pass workspaceId for the project scope; the backend
 *  rejects a project-scope request without it. */
export async function fetchSkillScope(global: boolean, workspaceId?: string): Promise<SkillScopeResult> {
  const params = new URLSearchParams({ global: String(global) });
  if (!global && workspaceId) params.set('workspaceId', workspaceId);
  try {
    const res = await fetch(`/api/v1/skills/list?${params.toString()}`, { cache: 'no-store' });
    const data = await res.json();
    if (data?.code === 0 && Array.isArray(data.skills)) {
      return {
        skills: data.skills as SkillInfo[],
        ready: data.ready !== false,
        error: typeof data.error === 'string' ? data.error : '',
      };
    }
    return { skills: [], ready: true, error: data?.msg || `unexpected response (HTTP ${res.status})` };
  } catch (err) {
    return { skills: [], ready: true, error: err instanceof Error ? err.message : String(err) };
  }
}

/** Merge global + project scopes into one deduped, sorted list. Project scope
 *  wins on name conflicts because that is what the agent's own resolution does. */
function mergeScopes(global: SkillInfo[], project: SkillInfo[]): SkillInfo[] {
  const byName = new Map<string, SkillInfo>();
  for (const s of [...global, ...project]) {
    if (s && typeof s.name === 'string') byName.set(s.name, s);
  }
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** Skills visible to an agent running in workspaceId: global plus that
 *  workspace's project skills. Without a workspaceId only global is returned —
 *  guessing a project directory would list skills the agent cannot load. */
export function fetchSkills(workspaceId?: string): Promise<SkillInfo[]> {
  const key = workspaceId || '';
  const cached = cachedSkills.get(key);
  if (cached) return Promise.resolve(cached);
  const pending = inflight.get(key);
  if (pending) return pending;

  const run = (async () => {
    for (let attempt = 0; attempt < READY_RETRY_ATTEMPTS; attempt++) {
      const [global, project] = await Promise.all([
        fetchSkillScope(true),
        workspaceId
          ? fetchSkillScope(false, workspaceId)
          : Promise.resolve<SkillScopeResult>({ skills: [], ready: true, error: '' }),
      ]);
      if (global.ready && project.ready) {
        const merged = mergeScopes(global.skills, project.skills);
        cachedSkills.set(key, merged);
        return merged;
      }
      await sleep(READY_RETRY_DELAY_MS);
    }
    throw new Error('skill list cache is not ready');
  })().finally(() => {
    inflight.delete(key);
  });
  inflight.set(key, run);
  return run;
}

/** Drop every cached scope. Call after any skill install / uninstall / update. */
export function invalidateSkillsCache(): void {
  cachedSkills.clear();
  inflight.clear();
}

export function prefetchSkills(workspaceId?: string): void {
  void fetchSkills(workspaceId).catch(() => {
    // Best-effort warmup; interactive "/" completion will retry on demand.
  });
}

/** Test hook: drop the module-level cache so each test re-fetches. */
export function __resetSkillsCacheForTest(): void {
  invalidateSkillsCache();
}
