import { afterEach, describe, expect, it, vi } from 'vitest';
import { fetchTranscript } from './transcript';

afterEach(() => vi.restoreAllMocks());

// A literal U+200C ZERO WIDTH NON-JOINER between two morphemes; the client must
// carry it through verbatim (no normalization).
const zwnj = 'خوش‌حالم';

function mockFetch(handler: (url: string) => { ok: boolean; status?: number; body?: unknown }) {
  const spy = vi.fn(async (url: string) => {
    const r = handler(url);
    return {
      ok: r.ok,
      status: r.status ?? (r.ok ? 200 : 500),
      json: async () => r.body
    } as Response;
  });
  vi.stubGlobal('fetch', spy);
  return spy;
}

describe('fetchTranscript', () => {
  it('maps the snake_case DTO (positional words, nullable speaker_key, ZWNJ) to camelCase', async () => {
    mockFetch(() => ({
      ok: true,
      body: {
        episode_id: 'ep_abc',
        language: 'fa',
        segments: [
          {
            idx: 0,
            start_ms: 0,
            end_ms: 900,
            text: 'سلام',
            speaker_key: 'S1',
            words: [['سلام', 0, 520, 0.98]]
          },
          {
            idx: 1,
            start_ms: 1000,
            end_ms: 1600,
            text: `خیلی ${zwnj}`,
            speaker_key: null,
            words: [
              ['خیلی', 1000, 1200, 0.96],
              [zwnj, 1240, 1600, 0.95]
            ]
          }
        ]
      }
    }));

    const t = await fetchTranscript('ep_abc');
    expect(t.episodeId).toBe('ep_abc');
    expect(t.language).toBe('fa');
    expect(t.segments).toHaveLength(2);

    // Segment 0: diarized, single positional word tuple parsed to a named word.
    expect(t.segments[0]).toEqual({
      idx: 0,
      startMs: 0,
      endMs: 900,
      text: 'سلام',
      speakerKey: 'S1',
      words: [{ text: 'سلام', startMs: 0, endMs: 520, conf: 0.98 }]
    });

    // Segment 1: un-diarized -> speakerKey null; ZWNJ preserved byte-for-byte.
    expect(t.segments[1].speakerKey).toBeNull();
    expect(t.segments[1].text).toBe(`خیلی ${zwnj}`);
    expect(t.segments[1].text).toContain('‌');
    expect(t.segments[1].words[1]).toEqual({ text: zwnj, startMs: 1240, endMs: 1600, conf: 0.95 });
  });

  it('resolves an episode with no segments to an empty array (not an error)', async () => {
    mockFetch(() => ({ ok: true, body: { episode_id: 'ep_x', language: 'fa', segments: [] } }));
    const t = await fetchTranscript('ep_x');
    expect(t.segments).toEqual([]);
  });

  it('requests the transcript path with the id percent-encoded', async () => {
    const spy = mockFetch(() => ({ ok: true, body: { episode_id: 'ep_x', language: 'fa', segments: [] } }));
    await fetchTranscript('ep_x');
    expect(spy).toHaveBeenCalledWith('/api/episodes/ep_x/transcript', { credentials: 'same-origin' });
  });

  it('throws on a non-OK response (404 unknown/foreign, 401 unauthenticated)', async () => {
    mockFetch(() => ({ ok: false, status: 404 }));
    await expect(fetchTranscript('ep_missing')).rejects.toThrow();
    mockFetch(() => ({ ok: false, status: 401 }));
    await expect(fetchTranscript('ep_x')).rejects.toThrow();
  });
});
