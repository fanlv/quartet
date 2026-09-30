import i18n, { LANGUAGE_STORAGE_KEY } from '../i18n';

export type SupportedLanguage = 'zh' | 'en';

// normalizeLanguage maps any i18next style tag ("zh", "zh-CN", "en-US") onto
// the two languages this app ships; anything else is not a usable preference.
export function normalizeLanguage(value: unknown): SupportedLanguage | '' {
  if (typeof value !== 'string') return '';
  const lower = value.trim().toLowerCase();
  if (lower.startsWith('zh')) return 'zh';
  if (lower.startsWith('en')) return 'en';
  return '';
}

function readStoredLanguage(): SupportedLanguage | '' {
  try {
    return normalizeLanguage(localStorage.getItem(LANGUAGE_STORAGE_KEY));
  } catch {
    return '';
  }
}

export async function saveLanguagePreference(language: SupportedLanguage): Promise<void> {
  const res = await fetch('/api/v1/config/settings/language', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ language }),
  });
  const data = (await res.json().catch(() => null)) as { code?: number; msg?: string } | null;
  if (!res.ok || data?.code !== 0) {
    throw new Error(data?.msg || `PUT /api/v1/config/settings/language returned HTTP ${res.status}`);
  }
}

let syncPromise: Promise<void> | null = null;

// syncLanguageFromServer runs once per page load, after the session is
// authenticated and before the app UI paints. The server value wins so the
// preference follows the account instead of the browser origin; when the
// server has none yet, an explicit local choice is uploaded so the first
// domain that ever set it seeds the shared value.
export function syncLanguageFromServer(): Promise<void> {
  if (!syncPromise) syncPromise = doSync();
  return syncPromise;
}

async function doSync(): Promise<void> {
  try {
    const res = await fetch('/api/v1/config/settings/get', { cache: 'no-store' });
    const data = (await res.json().catch(() => null)) as
      | { code?: number; settings?: { language?: string } }
      | null;
    if (!res.ok || data?.code !== 0) return;

    const serverLanguage = normalizeLanguage(data.settings?.language);
    if (serverLanguage) {
      if (normalizeLanguage(i18n.language) !== serverLanguage) {
        await i18n.changeLanguage(serverLanguage);
      }
      return;
    }

    const stored = readStoredLanguage();
    if (stored) await saveLanguagePreference(stored);
  } catch (err) {
    console.error('Failed to sync language preference with the server:', err);
  }
}
