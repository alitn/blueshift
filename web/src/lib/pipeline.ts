/**
 * M0 status -> five-bar pipeline view. The Library shows a fixed five-step
 * pipeline; in M0 only ingest runs, so the mapping is deliberate (per the
 * Architect ruling) rather than derived from real per-stage progress. Colors are
 * token classes, resolved by DESIGN.md's pipeline-step spec:
 *   done -> step-done · active -> accent · pending -> border-default · failed -> danger
 */
import type { DisplayState } from './episodes';

export type StepState = 'done' | 'active' | 'pending' | 'failed';
export type LabelTone = 'ok' | 'accent' | 'danger' | 'muted';

export type PipelineView = {
  steps: StepState[]; // always length 5
  label: string;
  tone: LabelTone;
};

const D: StepState = 'done';
const A: StepState = 'active';
const P: StepState = 'pending';
const F: StepState = 'failed';

/** pipelineView maps a display state to its five bars, stage label, and tone. */
export function pipelineView(state: DisplayState): PipelineView {
  switch (state) {
    case 'awaiting_upload':
      // Upload never completed: step 1 (upload) is still pending, not done, so
      // the row reads honestly as awaiting the master rather than queued.
      return { steps: [P, P, P, P, P], label: 'AWAITING UPLOAD', tone: 'muted' };
    case 'uploaded':
      return { steps: [D, P, P, P, P], label: 'QUEUED', tone: 'muted' };
    case 'processing':
      return { steps: [D, A, P, P, P], label: 'INGEST…', tone: 'accent' };
    case 'ready':
      return { steps: [D, D, D, D, D], label: 'READY', tone: 'ok' };
    case 'failed':
      return { steps: [D, F, P, P, P], label: 'FAILED — INGEST', tone: 'danger' };
  }
}

/** Tailwind background class for each step state (token-backed, no raw hex). */
export const STEP_BG: Record<StepState, string> = {
  done: 'bg-step-done',
  active: 'bg-accent',
  pending: 'bg-border-default',
  failed: 'bg-danger'
};

/** Tailwind text-color class for each label tone (token-backed). */
export const TONE_TEXT: Record<LabelTone, string> = {
  ok: 'text-ok',
  accent: 'text-accent',
  danger: 'text-danger',
  muted: 'text-text-faint'
};

/** formatDuration renders ms as HH:MM:SS (tabular), or an em dash if unknown. */
export function formatDuration(ms?: number): string {
  if (!ms || ms <= 0) return '—';
  const totalSeconds = Math.floor(ms / 1000);
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `${pad(h)}:${pad(m)}:${pad(s)}`;
}

/** formatSize renders bytes as a compact GB/MB string, or em dash if unknown. */
export function formatSize(bytes?: number): string {
  if (!bytes || bytes <= 0) return '—';
  const gb = bytes / (1024 * 1024 * 1024);
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = bytes / (1024 * 1024);
  return `${mb.toFixed(0)} MB`;
}

/** formatUploaded renders an ISO timestamp as a short uppercase MON DD, or em dash. */
export function formatUploaded(iso: string): string {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const mon = d
    .toLocaleString('en-US', { month: 'short', timeZone: 'UTC' })
    .toUpperCase();
  const day = d.getUTCDate().toString().padStart(2, '0');
  return `${mon} ${day}`;
}
