/**
 * Display state -> five-bar pipeline view. The Library shows a fixed five-bar
 * pipeline, but today (M1) only one real stage exists: ingest. So bar 1 tracks
 * the ingest stage's actual state, and bars 2-5 stand for the future stages
 * (transcribe, moments, etc.) that have not been built or reached yet — they are
 * honestly "not reached", never falsely "done". This replaces the earlier
 * all-done READY mapping, which rendered five identical bars.
 *
 * Colors are token classes, resolved by DESIGN.md's pipeline-step spec:
 *   done -> step-done · active -> accent · pending -> border-default · failed -> danger
 * DESIGN.md defines no separate "not-reached" fill (the prototype paints every
 * not-yet-reached bar with border-default), so `unreached` reuses border-default;
 * a distinct token would be an Architect-authorised DESIGN.md change.
 */
import type { DisplayState } from './episodes';

export type StepState = 'done' | 'active' | 'pending' | 'unreached' | 'failed';
export type LabelTone = 'ok' | 'accent' | 'danger' | 'muted';

export type PipelineView = {
  steps: StepState[]; // always length 5
  label: string;
  tone: LabelTone;
};

const D: StepState = 'done';
const A: StepState = 'active';
const P: StepState = 'pending';
const U: StepState = 'unreached';
const F: StepState = 'failed';

/**
 * pipelineView maps a display state to its five bars, stage label, and tone.
 * Bar 1 = the ingest stage; bars 2-5 = downstream stages not reached in M1.
 */
export function pipelineView(state: DisplayState): PipelineView {
  switch (state) {
    case 'awaiting_upload':
      // No master landed, so ingest cannot start: every bar is unreached and the
      // row reads honestly as awaiting the master rather than queued.
      return { steps: [U, U, U, U, U], label: 'AWAITING UPLOAD', tone: 'muted' };
    case 'uploaded':
      // Master landed; ingest is queued (pending) but has not run.
      return { steps: [P, U, U, U, U], label: 'QUEUED', tone: 'muted' };
    case 'processing':
      // Ingest is running.
      return { steps: [A, U, U, U, U], label: 'INGEST…', tone: 'accent' };
    case 'ready':
      // Ingest is done; downstream stages do not exist yet, so they stay unreached.
      return { steps: [D, U, U, U, U], label: 'READY', tone: 'ok' };
    case 'failed':
      // Ingest failed; downstream stages were never reached.
      return { steps: [F, U, U, U, U], label: 'FAILED — INGEST', tone: 'danger' };
  }
}

/**
 * Tailwind background class for each step state (token-backed, no raw hex).
 * `unreached` shares border-default with `pending` because DESIGN.md defines no
 * separate not-reached fill; done/active/pending are the three distinct fills.
 */
export const STEP_BG: Record<StepState, string> = {
  done: 'bg-step-done',
  active: 'bg-accent',
  pending: 'bg-border-default',
  unreached: 'bg-border-default',
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
