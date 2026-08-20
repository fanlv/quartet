import { describe, expect, it } from 'vitest';
import { readAPIResponse } from './apiResponse';

describe('readAPIResponse', () => {
  it('returns a successful API envelope', async () => {
    const result = await readAPIResponse(new Response(JSON.stringify({ code: 0, value: 1 }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));

    expect(result.value).toBe(1);
  });

  it('surfaces the complete business error from an HTTP failure', async () => {
    await expect(readAPIResponse(new Response(JSON.stringify({
      code: -1,
      msg: 'read settings file failed: disk unavailable',
    }), { status: 500 }))).rejects.toThrow('read settings file failed: disk unavailable');
  });

  it('surfaces the raw response when JSON parsing fails', async () => {
    await expect(readAPIResponse(new Response('upstream returned HTML', { status: 502 })))
      .rejects.toThrow('upstream returned HTML');
  });
});
