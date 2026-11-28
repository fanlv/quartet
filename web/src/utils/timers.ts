export async function timerFetch<T>(path: string, method = 'GET', body?: object, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method, signal,
    ...(body && { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  });
  const raw = await response.text();
  if (!response.ok) throw new Error(`${method} ${path} · HTTP ${response.status}\n${raw}`);
  return JSON.parse(raw) as T;
}

export function timerDate(value: string | number, zone?: string, locale?: string, timeStyle: 'short' | 'medium' = 'short') {
  const date = new Date(value);
  try {
    return date.toLocaleString(locale, {
      dateStyle: 'medium', timeStyle, ...(zone && zone !== 'Local' && { timeZone: zone }),
    });
  } catch (error) {
    if (!(error instanceof RangeError)) throw error;
    return Number.isNaN(date.getTime()) ? String(value) : date.toISOString();
  }
}
