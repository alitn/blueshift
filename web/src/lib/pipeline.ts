/**
 * Display state (+ pipeline stage) -> five-bar pipeline view. The Library shows a
 * fixed five-bar pipeline, one bar per M1 stage: ingest · transcribe · diarize ·
 * moments · render. The server's neutral `stage` field (episodes.current_stage)
 * says which stage is running/next while processing, or which stage a terminal
 * row reached. Bars before the current stage are `done`, the current stage is
 * `active` (processing) / `pending` (queued) / `failed`, and bars after it are
 * honestly `unreached` — never falsely "done".
 *
 * Only ingest is wired in the worker today, so in production every row's stage is
 * ingest (index 0) or absent, which collapses to exactly the earlier one-stage
 * mapping (bar 1 tracks ingest; bars 2-5 unreached). The per-stage generalisation
 * lights the later bars as those stages land, with no visual change to today's
 * single-stage rows.
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
 * STAGE_ORDER is the pipeline sequence, one entry per bar. It mirrors the worker
 * registry order and the episodes.current_stage CHECK. Stage names are neutral
 * product terms — never provider names.
 */
export const STAGE_ORDER = ['ingest', 'transcribe', 'diarize', 'moments', 'render'] as const;
export type StageName = (typeof STAGE_ORDER)[number];

/** Uppercase stage labels for the chip text ("INGEST…", "FAILED — TRANSCRIBE"). */
const STAGE_LABELS: Record<StageName, string> = {
  ingest: 'INGEST',
  transcribe: 'TRANSCRIBE',
  diarize: 'DIARIZE',
  moments: 'MOMENTS',
  render: 'RENDER'
};

/**
 * stageIndex resolves a stage name to its bar index. An absent or unknown stage
 * defaults to ingest (index 0): an unclaimed/legacy row is treated as sitting at
 * the first stage, which keeps single-stage rows rendering exactly as before.
 */
function stageIndex(stage?: string): number {
  const i = STAGE_ORDER.indexOf(stage as StageName);
  return i >= 0 ? i : 0;
}

/**
 * pipelineView maps a display state and the pipeline stage to the five bars,
 * stage label, and tone. `stage` is the server's neutral current_stage; when it
 * is absent the view falls back to the first stage (ingest).
 */
export function pipelineView(state: DisplayState, stage?: string): PipelineView {
  const n = STAGE_ORDER.length; // 5 bars, one per stage
  const idx = stageIndex(stage);
  const steps: StepState[] = Array.from({ length: n }, () => U);

  switch (state) {
    case 'awaiting_upload':
      // No master landed, so nothing can start: every bar is unreached and the
      // row reads honestly as awaiting the master rather than queued.
      return { steps, label: 'AWAITING UPLOAD', tone: 'muted' };
    case 'uploaded':
      // Master landed; the first stage (ingest) is queued (pending) but has not run.
      steps[0] = P;
      return { steps, label: 'QUEUED', tone: 'muted' };
    case 'processing':
      // Stages before the current one are done; the current stage is running.
      for (let i = 0; i < idx; i++) steps[i] = D;
      steps[idx] = A;
      return { steps, label: `${STAGE_LABELS[STAGE_ORDER[idx]]}…`, tone: 'accent' };
    case 'ready':
      // Terminal success: every stage up to and including the one reached is done;
      // any later, not-yet-wired stages stay unreached. For today's single-stage
      // rows (idx 0) that is bar 1 done, bars 2-5 unreached.
      for (let i = 0; i <= idx; i++) steps[i] = D;
      return { steps, label: 'READY', tone: 'ok' };
    case 'failed':
      // The stage that failed is marked failed; earlier stages are done, later
      // ones were never reached.
      for (let i = 0; i < idx; i++) steps[i] = D;
      steps[idx] = F;
      return { steps, label: `FAILED — ${STAGE_LABELS[STAGE_ORDER[idx]]}`, tone: 'danger' };
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
