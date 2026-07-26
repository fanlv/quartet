// Installed-skill list for the chat input's "/" completion and skill-name
// highlighting. Backed by GET /api/v1/skills/list (project + global scopes),
// cached at module level so remounting ChatInput never re-runs the (npx-based,
// comparatively slow) backend call.

export interface SkillInfo {
  name: string;
  path: string;
  scope: string;
  agents: string[];
}

interface SkillScopeResult {
  skills: SkillInfo[];
  ready: boolean;
}

let cachedSkills: SkillInfo[] | null = null;
let inflight: Promise<SkillInfo[]> | null = null;

const READY_RETRY_DELAY_MS = 400;
const READY_RETRY_ATTEMPTS = 25;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

async function fetchScope(global: boolean): Promise<SkillScopeResult> {
  try {
    const res = await fetch(`/api/v1/skills/list?global=${global}`, { cache: 'no-store' });
    const data = await res.json();
    if (data?.code === 0 && Array.isArray(data.skills)) {
      return {
        skills: data.skills as SkillInfo[],
        // Old backends do not return `ready`; treat that as ready.
        ready: data.ready !== false,
      };
    }
  } catch {
    // Network/parse failures must never break typing; preserve the old
    // behavior and treat them as a completed empty list.
  }
  return { skills: [], ready: true };
}

export function fetchSkills(): Promise<SkillInfo[]> {
  if (cachedSkills) return Promise.resolve(cachedSkills);
  if (inflight) return inflight;
  inflight = (async () => {
    for (let attempt = 0; attempt < READY_RETRY_ATTEMPTS; attempt++) {
      const [project, global] = await Promise.all([
        fetchScope(false),
        fetchScope(true),
      ]);
      if (project.ready && global.ready) {
        // Dedupe by name; project scope wins over global on conflicts.
        const byName = new Map<string, SkillInfo>();
        for (const s of [...global.skills, ...project.skills]) {
          if (s && typeof s.name === 'string') byName.set(s.name, s);
        }
        cachedSkills = [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
        return cachedSkills;
      }
      await sleep(READY_RETRY_DELAY_MS);
    }
    throw new Error('skill list cache is not ready');
  })().finally(() => {
    inflight = null;
  });
  return inflight;
}

export function prefetchSkills(): void {
  void fetchSkills().catch(() => {
    // Best-effort warmup; interactive "/" completion will retry on demand.
  });
}

/** Test hook: drop the module-level cache so each test re-fetches. */
export function __resetSkillsCacheForTest(): void {
  cachedSkills = null;
  inflight = null;
}
