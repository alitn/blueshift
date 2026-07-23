import { describe, expect, it } from 'vitest';
import {
  pipelineView,
  formatDuration,
  formatSize,
  formatUploaded,
  STEP_BG,
  TONE_TEXT
} from './pipeline';

describe('pipelineView (M0 status -> 5 bars)', () => {
  it('awaiting_upload = all pending + AWAITING UPLOAD (upload step 1 not done)', () => {
    const v = pipelineView('awaiting_upload');
    expect(v.steps).toEqual(['pending', 'pending', 'pending', 'pending', 'pending']);
    expect(v.label).toBe('AWAITING UPLOAD');
    expect(v.tone).toBe('muted');
  });

  it('uploaded = 1 done + QUEUED', () => {
    const v = pipelineView('uploaded');
    expect(v.steps).toEqual(['done', 'pending', 'pending', 'pending', 'pending']);
    expect(v.label).toBe('QUEUED');
    expect(v.tone).toBe('muted');
  });

  it('processing = 1 done, 2nd active + INGEST…', () => {
    const v = pipelineView('processing');
    expect(v.steps).toEqual(['done', 'active', 'pending', 'pending', 'pending']);
    expect(v.label).toBe('INGEST…');
    expect(v.tone).toBe('accent');
  });

  it('ready = all 5 done + READY (ok)', () => {
    const v = pipelineView('ready');
    expect(v.steps).toEqual(['done', 'done', 'done', 'done', 'done']);
    expect(v.label).toBe('READY');
    expect(v.tone).toBe('ok');
  });

  it('failed = 2nd danger + FAILED — INGEST (danger)', () => {
    const v = pipelineView('failed');
    expect(v.steps).toEqual(['done', 'failed', 'pending', 'pending', 'pending']);
    expect(v.label).toBe('FAILED — INGEST');
    expect(v.tone).toBe('danger');
  });

  it('maps step/tone classes to the DESIGN tokens', () => {
    expect(STEP_BG).toEqual({
      done: 'bg-step-done',
      active: 'bg-accent',
      pending: 'bg-border-default',
      failed: 'bg-danger'
    });
    expect(TONE_TEXT.ok).toBe('text-ok');
    expect(TONE_TEXT.danger).toBe('text-danger');
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
