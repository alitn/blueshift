import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  contentTypeFor,
  displayState,
  isTerminal,
  listEpisodes,
  retryEpisode,
  fetchProxyUrl,
  CONTENT_TYPES
} from './episodes';

afterEach(() => vi.restoreAllMocks());

describe('contentTypeFor', () => {
  it('resolves the container MIME from a known extension', () => {
    expect(contentTypeFor(new File([], 'a.mp4'))).toBe(CONTENT_TYPES.mp4);
    expect(contentTypeFor(new File([], 'a.MOV'))).toBe(CONTENT_TYPES.mov);
    expect(contentTypeFor(new File([], 'a.mxf'))).toBe(CONTENT_TYPES.mxf);
  });

  it('returns null for an unsupported container', () => {
    expect(contentTypeFor(new File([], 'notes.txt', { type: 'text/plain' }))).toBeNull();
    expect(contentTypeFor(new File([], 'noext'))).toBeNull();
  });
});

describe('isTerminal', () => {
  it('treats ready/failed as terminal and the rest as live', () => {
    expect(isTerminal('ready')).toBe(true);
    expect(isTerminal('failed')).toBe(true);
    expect(isTerminal('uploaded')).toBe(false);
    expect(isTerminal('processing')).toBe(false);
  });
});

function mockFetch(handler: (url: string) => { ok: boolean; status?: number; body?: unknown }) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      const r = handler(url);
      return {
        ok: r.ok,
        status: r.status ?? (r.ok ? 200 : 500),
        json: async () => r.body
      } as Response;
    })
  );
}

describe('displayState', () => {
  it('reports awaiting_upload only for an uploaded row with no master', () => {
    expect(displayState({ status: 'uploaded', hasMaster: false })).toBe('awaiting_upload');
    expect(displayState({ status: 'uploaded', hasMaster: true })).toBe('uploaded');
    expect(displayState({ status: 'processing', hasMaster: false })).toBe('processing');
    expect(displayState({ status: 'ready', hasMaster: true })).toBe('ready');
    expect(displayState({ status: 'failed', hasMaster: true })).toBe('failed');
  });
});

describe('listEpisodes', () => {
  it('maps the snake_case DTO to the camelCase Episode', async () => {
    mockFetch(() => ({
      ok: true,
      body: {
        episodes: [
          {
            id: 'ep_a',
            title: 'x',
            source_filename: 'a.mp4',
            language: 'fa',
            status: 'ready',
            has_master: true,
            duration_ms: 1000,
            size_bytes: 2048,
            uploaded_at: '2026-07-01T00:00:00Z'
          }
        ]
      }
    }));
    const eps = await listEpisodes();
    expect(eps[0]).toEqual({
      id: 'ep_a',
      title: 'x',
      sourceFilename: 'a.mp4',
      language: 'fa',
      status: 'ready',
      hasMaster: true,
      durationMs: 1000,
      sizeBytes: 2048,
      uploadedAt: '2026-07-01T00:00:00Z'
    });
  });

  it('throws on a non-OK response', async () => {
    mockFetch(() => ({ ok: false, status: 503 }));
    await expect(listEpisodes()).rejects.toThrow();
  });
});

describe('retryEpisode / fetchProxyUrl', () => {
  it('reports retry success from a 200', async () => {
    mockFetch(() => ({ ok: true, body: {} }));
    expect(await retryEpisode('ep_a')).toBe(true);
  });

  it('reports retry failure from a 409', async () => {
    mockFetch(() => ({ ok: false, status: 409 }));
    expect(await retryEpisode('ep_a')).toBe(false);
  });

  it('returns the signed proxy url, or null when not ready', async () => {
    mockFetch(() => ({ ok: true, body: { url: '/signed', expires_at: 'x' } }));
    expect(await fetchProxyUrl('ep_a')).toBe('/signed');
    mockFetch(() => ({ ok: false, status: 404 }));
    expect(await fetchProxyUrl('ep_a')).toBeNull();
  });
});
