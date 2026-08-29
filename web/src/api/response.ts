export async function readAPIResponse<T = Record<string, unknown>>(
  res: Response,
): Promise<T> {
  const raw = await res.text().catch((err) => {
    throw new Error(`HTTP ${res.status}: failed to read response: ${String(err)}`);
  });

  let data: T | null = null;
  if (raw) {
    try {
      data = JSON.parse(raw) as T;
    } catch (err) {
      throw new Error(`HTTP ${res.status}: invalid JSON response: ${String(err)}\n${raw}`);
    }
  }

  const envelope = data as Record<string, unknown> | null;
  if (!res.ok || envelope?.code !== 0) {
    const detail = [envelope?.msg, envelope?.message, envelope?.error]
      .find((value): value is string => typeof value === 'string' && value !== '');
    throw new Error(detail || `HTTP ${res.status}${raw ? `\n${raw}` : ''}`);
  }

  return (data || {}) as T;
}
