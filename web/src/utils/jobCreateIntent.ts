const STORAGE_KEY = 'quartet.job-create-intents.v1';

interface StoredIntent {
  fingerprint: string;
  clientMessageId: string;
}

type IntentMap = Record<string, StoredIntent>;

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value && typeof value === 'object') {
    const source = value as Record<string, unknown>;
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(source).sort()) {
      if (source[key] !== undefined) out[key] = canonicalize(source[key]);
    }
    return out;
  }
  return value;
}

function fingerprint(value: unknown): string {
  return JSON.stringify(canonicalize(value));
}

function readIntents(): IntentMap {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === 'object' ? parsed as IntentMap : {};
  } catch {
    return {};
  }
}

function writeIntents(intents: IntentMap): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(intents));
  } catch {
    // localStorage can be unavailable in privacy-restricted contexts. The
    // caller still receives a request ID; only reload recovery is degraded.
  }
}

function newIntentID(): string {
  const id = crypto.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
  return `web-create-v1-${id}`;
}

// claimJobCreateIntent returns the same key while one logical create request
// remains unresolved. Changing any semantic field replaces the pending intent.
export function claimJobCreateIntent(scope: string, semantics: unknown, preferredID?: string): string {
  const intents = readIntents();
  const nextFingerprint = fingerprint(semantics);
  const existing = intents[scope];
  if (existing) {
    if (existing.fingerprint === nextFingerprint && existing.clientMessageId) {
      return existing.clientMessageId;
    }
    // A changed payload is a new user intent even when a replayed command
    // supplies its old preferred action key. Reusing that key would correctly
    // trigger a server conflict but leave the user unable to proceed.
    preferredID = undefined;
  }
  const clientMessageId = preferredID || newIntentID();
  intents[scope] = { fingerprint: nextFingerprint, clientMessageId };
  writeIntents(intents);
  return clientMessageId;
}

export function clearJobCreateIntentScope(scope: string): void {
  const intents = readIntents();
  if (!(scope in intents)) return;
  delete intents[scope];
  writeIntents(intents);
}

// clearJobCreateIntent clears only the intent that produced a successful
// response. An older request completing late cannot erase a newer intent.
export function clearJobCreateIntent(scope: string, clientMessageId: string): void {
  const intents = readIntents();
  if (intents[scope]?.clientMessageId !== clientMessageId) return;
  delete intents[scope];
  writeIntents(intents);
}


