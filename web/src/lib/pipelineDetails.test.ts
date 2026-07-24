import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  cardRows,
  clearPipelineDetailsCache,
  engineDisplay,
  fetchPipelineDetails,
  formatCents,
  formatStageDuration,
  totalCostCents,
  STAGE_DISPLAY,
  type PipelineDetails
} from './pipelineDetails';

// The wire fixture: a mid-pipeline episode with two finished stages, one
// active, and per-stage engine/cost detail — the neutral DTO shape.
const WIRE = {
  stages: [
    { name: 'ingest', status: 'done', duration_ms: 1500, engine: 'bs-media-1' },
    { name: 'transcribe', status: 'done', duration_ms: 102000, engine: 'bs-asr-2', cost_cents: 4 },
    { name: 'diarize', status: 'active', engine: 'bs-lm-1' },
    { name: 'moments', status: 'unreached' }
  ],
  queued_ms: 2000,
  total_ms: 103500
};

function mockFetchOnce(body: unknown, ok = true) {
  const fn = vi.fn().mockResolvedValue({ ok, json: async () => body });
  vi.stubGlobal('fetch', fn);
  return fn;
}

beforeEach(() => clearPipelineDetailsCache());
afterEach(() => vi.unstubAllGlobals());

describe('fetchPipelineDetails', () => {
  const ep = { id: 'ep_x', status: 'processing', stage: 'diarize' } as const;

  it('maps the snake_case DTO to the camelCase view', async () => {
    mockFetchOnce(WIRE);
    const d = await fetchPipelineDetails(ep);
    expect(d.stages[1]).toEqual({
      name: 'transcribe',
      status: 'done',
      durationMs: 102000,
      engine: 'bs-asr-2',
      costCents: 4
    });
    expect(d.queuedMs).toBe(2000);
    expect(d.totalMs).toBe(103500);
  });

  it('caches per episode until the status/stage changes (lazy, no poll bloat)', async () => {
    const fn = mockFetchOnce(WIRE);
    await fetchPipelineDetails(ep);
    await fetchPipelineDetails(ep);
    expect(fn).toHaveBeenCalledTimes(1); // same state -> served from cache

    // A stage transition invalidates the cached entry.
    await fetchPipelineDetails({ ...ep, stage: 'moments' });
    expect(fn).toHaveBeenCalledTimes(2);
    // A status transition does too.
    await fetchPipelineDetails({ ...ep, status: 'ready', stage: 'moments' });
    expect(fn).toHaveBeenCalledTimes(3);
  });

  it('throws on a non-OK response (the card renders the neutral error)', async () => {
    mockFetchOnce({}, false);
    await expect(fetchPipelineDetails(ep)).rejects.toThrow('pipeline_details_failed');
  });
});

describe('cardRows', () => {
  it('always yields the five canonical rows, padding missing stages as unreached', () => {
    const details: PipelineDetails = {
      stages: [{ name: 'ingest', status: 'done', durationMs: 900 }]
    };
    const rows = cardRows(details);
    expect(rows.map((r) => r.name)).toEqual([
      'ingest',
      'transcribe',
      'diarize',
      'moments',
      'render'
    ]);
    expect(rows[0].status).toBe('done');
    expect(rows.slice(1).every((r) => r.status === 'unreached')).toBe(true);
  });

  it('yields five unreached rows with no details at all (loading fallback)', () => {
    expect(cardRows(undefined)).toHaveLength(5);
  });
});

describe('display names', () => {
  it('maps diarize -> SPEAKERS and moments -> MOMENTS (product terms)', () => {
    expect(STAGE_DISPLAY.diarize).toBe('SPEAKERS');
    expect(STAGE_DISPLAY.moments).toBe('MOMENTS');
    expect(STAGE_DISPLAY.ingest).toBe('INGEST');
    expect(STAGE_DISPLAY.transcribe).toBe('TRANSCRIBE');
  });
});

describe('formatStageDuration', () => {
  it('renders the compact mono forms', () => {
    expect(formatStageDuration(102000)).toBe('1M 42S');
    expect(formatStageDuration(42000)).toBe('42S');
    expect(formatStageDuration(3_720_000)).toBe('1H 02M');
    expect(formatStageDuration(400)).toBe('<1S');
    expect(formatStageDuration(0)).toBe('<1S');
    expect(formatStageDuration(undefined)).toBe('—');
  });

  it('renders very long waits in days (a stale demo queue is honest, not silly)', () => {
    expect(formatStageDuration(3 * 24 * 3600 * 1000)).toBe('3D');
  });
});

describe('formatCents / totalCostCents', () => {
  it('renders integer cents as dollars', () => {
    expect(formatCents(4)).toBe('$0.04');
    expect(formatCents(1234)).toBe('$12.34');
    expect(formatCents(undefined)).toBe('—');
  });

  it('sums only known per-stage costs; undefined when none are known', () => {
    const d: PipelineDetails = {
      stages: [
        { name: 'ingest', status: 'done' },
        { name: 'transcribe', status: 'done', costCents: 4 },
        { name: 'diarize', status: 'done', costCents: 2 }
      ]
    };
    expect(totalCostCents(d)).toBe(6);
    expect(totalCostCents({ stages: [{ name: 'ingest', status: 'done' }] })).toBeUndefined();
    expect(totalCostCents(undefined)).toBeUndefined();
  });
});

describe('engineDisplay', () => {
  it('renders public labels as the faint card form, spelling out the leading bs token', () => {
    expect(engineDisplay('bs-asr-2')).toBe('BLUESHIFT·ASR 2');
    expect(engineDisplay('bs-lm-1')).toBe('BLUESHIFT·LM 1');
    expect(engineDisplay('bs-media-1')).toBe('BLUESHIFT·MEDIA 1');
    expect(engineDisplay('custom')).toBe('CUSTOM');
    // Only the FIRST token expands; a later `bs` keeps the uppercase form.
    expect(engineDisplay('asr-bs-2')).toBe('ASR·BS 2');
  });
});
