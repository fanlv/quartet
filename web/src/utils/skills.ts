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

let cachedSkills: SkillInfo[] | null = null;
let inflight: Promise<SkillInfo[]> | null = null;

async function fetchScope(global: boolean): Promise<SkillInfo[]> {
  try {
    const res = await fetch(`/api/v1/skills/list?global=${global}`);
    const data = await res.json();
    if (data?.code === 0 && Array.isArray(data.skills)) {
      return data.skills as SkillInfo[];
    }
  } catch {
    // Network/parse failures must never break typing — treat as "no skills".
  }
  return [];
}

export function fetchSkills(): Promise<SkillInfo[]> {
  if (cachedSkills) return Promise.resolve(cachedSkills);
  if (inflight) return inflight;
  inflight = (async () => {
    const [projectSkills, globalSkills] = await Promise.all([
      fetchScope(false),
      fetchScope(true),
    ]);
    // Dedupe by name; project scope wins over global on conflicts.
    const byName = new Map<string, SkillInfo>();
    for (const s of [...globalSkills, ...projectSkills]) {
      if (s && typeof s.name === 'string') byName.set(s.name, s);
    }
    cachedSkills = [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
    return cachedSkills;
  })().finally(() => {
    inflight = null;
  });
  return inflight;
}

/** Test hook: drop the module-level cache so each test re-fetches. */
export function __resetSkillsCacheForTest(): void {
  cachedSkills = null;
  inflight = null;
}
