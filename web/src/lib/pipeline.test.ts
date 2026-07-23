import { describe, expect, it } from 'vitest';
import {
  pipelineView,
  formatDuration,
  formatSize,
  formatUploaded,
  STEP_BG,
  TONE_TEXT
} from './pipeline';

describe('pipelineView (M1 one-stage: bar 1 = ingest, bars 2-5 = not reached)', () => {
  it('awaiting_upload = all unreached + AWAITING UPLOAD (ingest cannot start)', () => {
    const v = pipelineView('awaiting_upload');
    expect(v.steps).toEqual([
      'unreached',
      'unreached',
      'unreached',
      'unreached',
      'unreached'
    ]);
    expect(v.label).toBe('AWAITING UPLOAD');
    expect(v.tone).toBe('muted');
  });

  it('uploaded = ingest pending, rest unreached + QUEUED', () => {
    const v = pipelineView('uploaded');
    expect(v.steps).toEqual(['pending', 'unreached', 'unreached', 'unreached', 'unreached']);
    expect(v.label).toBe('QUEUED');
    expect(v.tone).toBe('muted');
  });

  it('processing = ingest active, rest unreached + INGEST…', () => {
    const v = pipelineView('processing');
    expect(v.steps).toEqual(['active', 'unreached', 'unreached', 'unreached', 'unreached']);
    expect(v.label).toBe('INGEST…');
    expect(v.tone).toBe('accent');
  });

  it('ready = ingest done, bars 2-5 not reached + READY (ok) — not five identical bars', () => {
    const v = pipelineView('ready');
    expect(v.steps).toEqual(['done', 'unreached', 'unreached', 'unreached', 'unreached']);
    expect(v.label).toBe('READY');
    expect(v.tone).toBe('ok');
    // The reported bug: a READY row must not render five identical bars. Bar 1
    // (done) differs from bars 2-5 (unreached).
    expect(v.steps[0]).not.toBe(v.steps[1]);
    expect(new Set(v.steps).size).toBeGreaterThan(1);
  });

  it('failed = ingest failed, rest unreached + FAILED — INGEST (danger)', () => {
    const v = pipelineView('failed');
    expect(v.steps).toEqual(['failed', 'unreached', 'unreached', 'unreached', 'unreached']);
    expect(v.label).toBe('FAILED — INGEST');
    expect(v.tone).toBe('danger');
  });

  it('maps step/tone classes to the DESIGN tokens (no raw hex)', () => {
    expect(STEP_BG).toEqual({
      done: 'bg-step-done',
      active: 'bg-accent',
      pending: 'bg-border-default',
      unreached: 'bg-border-default',
      failed: 'bg-danger'
    });
    expect(TONE_TEXT.ok).toBe('text-ok');
    expect(TONE_TEXT.danger).toBe('text-danger');
  });

  it('the three pipeline fills (done/active/pending) are three distinct token classes', () => {
    // Token conformance at the mapping layer: DESIGN.md defines exactly two greys
    // (step-done, border-default) plus accent, so done/active/pending resolve to
    // three distinct token-derived fills. unreached reuses border-default because
    // DESIGN.md has no separate not-reached fill (see pipeline.ts).
    const distinct = new Set([STEP_BG.done, STEP_BG.active, STEP_BG.pending]);
    expect(distinct.size).toBe(3);
    expect(STEP_BG.unreached).toBe(STEP_BG.pending);
    // Every fill is a token-backed Tailwind class, never a raw/arbitrary hex.
    for (const cls of Object.values(STEP_BG)) {
      expect(cls).toMatch(/^bg-[a-z-]+$/);
    }
  });
});

describe('formatters', () => {
  it('formats duration as HH:MM:SS, em dash when unknown', () => {
    expect(formatDuration(0)).toBe('—');
    expect(formatDuration(undefined)).toBe('—');
    expect(formatDuration(41 * 60 * 1000 + 8 * 1000)).toBe('00:41:08');
    expect(formatDuration(3600_000 + 24 * 60_000 + 30_000)).toBe('01:24:30');
  });

  it('formats size in GB/MB, em dash when unknown', () => {
    expect(formatSize(undefined)).toBe('—');
    expect(formatSize(6_100_000_000)).toBe('5.7 GB');
    expect(formatSize(84 * 1024 * 1024)).toBe('84 MB');
  });

  it('formats uploaded_at as MON DD in UTC, em dash when invalid', () => {
    expect(formatUploaded('')).toBe('—');
    expect(formatUploaded('not-a-date')).toBe('—');
    expect(formatUploaded('2026-07-21T10:00:00Z')).toBe('JUL 21');
  });
});
