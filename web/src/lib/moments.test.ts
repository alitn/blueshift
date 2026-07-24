import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchMoments, setMomentStatus } from './moments';

afterEach(() => vi.restoreAllMocks());

// A literal U+200C ZERO WIDTH NON-JOINER between two morphemes; the client
// must carry the quote through verbatim (no normalization).
const zwnjQuote = 'خیلی خوش‌حالم که اینجا هستم';

type MockResponse = { ok: boolean; status?: number; body?: unknown };

function mockFetch(handler: (url: string, init?: RequestInit) => MockResponse) {
  const spy = vi.fn(async (url: string, init?: RequestInit) => {
    const r = handler(url, init);
    return {
      ok: r.ok,
      status: r.status ?? (r.ok ? 200 : 500),
      json: async () => r.body
    } as Response;
  });
  vi.stubGlobal('fetch', spy);
  return spy;
}

const dto = (rank: number, status = 'proposed', quote = zwnjQuote) => ({
  rank,
  start_idx: 1,
  end_idx: 1,
  start_ms: 2600,
  end_ms: 4600,
  rationale_en: 'The quotable beat.',
  quote_fa: quote,
  status
});

describe('fetchMoments', () => {
  it('maps the snake_case DTO to camelCase, quote verbatim (ZWNJ preserved)', async () => {
    mockFetch(() => ({
      ok: true,
      body: { episode_id: 'ep_abc', moments: [dto(1), dto(2, 'approved', 'سلام')] }
    }));

    const m = await fetchMoments('ep_abc');
    expect(m.episodeId).toBe('ep_abc');
    expect(m.moments).toHaveLength(2);
    expect(m.moments[0]).toEqual({
      rank: 1,
      startIdx: 1,
      endIdx: 1,
      startMs: 2600,
      endMs: 4600,
      rationaleEn: 'The quotable beat.',
      quoteFa: zwnjQuote,
      status: 'proposed'
    });
    // Byte-exact: the ZWNJ survives the mapping untouched.
    expect(m.moments[0].quoteFa).toContain('‌');
    expect(m.moments[1].status).toBe('approved');
  });

  it('resolves an episode with no proposals to an empty array (not an error)', async () => {
    mockFetch(() => ({ ok: true, body: { episode_id: 'ep_x', moments: [] } }));
    const m = await fetchMoments('ep_x');
    expect(m.moments).toEqual([]);
  });

  it('requests the moments path with the id percent-encoded', async () => {
    const spy = mockFetch(() => ({ ok: true, body: { episode_id: 'ep_x', moments: [] } }));
    await fetchMoments('ep_x');
    expect(spy).toHaveBeenCalledWith('/api/episodes/ep_x/moments', { credentials: 'same-origin' });
  });

  it('throws on a non-OK response (404 unknown/foreign, 401 unauthenticated)', async () => {
    mockFetch(() => ({ ok: false, status: 404 }));
    await expect(fetchMoments('ep_missing')).rejects.toThrow();
    mockFetch(() => ({ ok: false, status: 401 }));
    await expect(fetchMoments('ep_x')).rejects.toThrow();
  });
});

describe('setMomentStatus', () => {
  it('POSTs the status to the rank-addressed path and resolves the updated moment', async () => {
    const spy = mockFetch(() => ({ ok: true, body: dto(1, 'approved') }));

    const m = await setMomentStatus('ep_abc', 1, 'approved');
    expect(spy).toHaveBeenCalledWith('/api/episodes/ep_abc/moments/1/status', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ status: 'approved' })
    });
    expect(m.rank).toBe(1);
    expect(m.status).toBe('approved');
    expect(m.quoteFa).toBe(zwnjQuote);
  });

  it('throws on an illegal transition (409) and an unknown rank (404)', async () => {
    mockFetch(() => ({ ok: false, status: 409 }));
    await expect(setMomentStatus('ep_x', 1, 'dismissed')).rejects.toThrow();
    mockFetch(() => ({ ok: false, status: 404 }));
    await expect(setMomentStatus('ep_x', 99, 'approved')).rejects.toThrow();
  });
});
