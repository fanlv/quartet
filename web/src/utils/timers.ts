export async function timerFetch<T>(path: string, method = 'GET', body?: object, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method, signal,
    ...(body && { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  });
  const raw = await response.text();
  if (!response.ok) throw new Error(`${method} ${path} · HTTP ${response.status}\n${raw}`);
  return JSON.parse(raw) as T;
}

export function timerDate(value: string | number, zone?: string) {
  return new Date(value).toLocaleString(undefined, {
    dateStyle: 'medium', timeStyle: 'short', ...(zone && zone !== 'Local' && { timeZone: zone }),
  });
}
