export async function readAPIResponse(res: Response): Promise<Record<string, unknown>> {
  const raw = await res.text().catch((err) => {
    throw new Error(`HTTP ${res.status}: failed to read response: ${String(err)}`);
  });

  let data: Record<string, unknown> | null = null;
  if (raw) {
    try {
      data = JSON.parse(raw) as Record<string, unknown>;
    } catch (err) {
      throw new Error(`HTTP ${res.status}: invalid JSON response: ${String(err)}\n${raw}`);
    }
  }

  if (!res.ok || data?.code !== 0) {
    const detail = [data?.msg, data?.message, data?.error]
      .find((value): value is string => typeof value === 'string' && value !== '');
    throw new Error(detail || `HTTP ${res.status}${raw ? `\n${raw}` : ''}`);
  }

  return data;
}
