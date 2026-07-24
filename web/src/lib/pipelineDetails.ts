/**
 * Client for the neutral per-stage pipeline provenance endpoint
 * (GET /api/episodes/{id}/pipeline) behind the Library's pipeline hover card.
 * Fetched LAZILY on hover/focus and cached per episode until the episode's
 * status/stage changes, so the Library poll payload is untouched. Everything
 * here is Blueshift-neutral: stage names are product terms and `engine` is the
 * public versioned engine label (e.g. bs-asr-2) — nothing names the stack.
 */

import type { Episode } from './episodes';
import { STAGE_ORDER } from './pipeline';

export type PipelineStageStatus = 'done' | 'active' | 'failed' | 'pending' | 'unreached';

/** One stage row of the hover card, camelCase view of the wire DTO. */
export type PipelineStageDetail = {
  name: string;
  status: PipelineStageStatus;
  /** Derived run duration; absent until the stage finished. */
  durationMs?: number;
  /** Public versioned engine label (bs-media-1 / bs-asr-2 / bs-lm-1). */
  engine?: string;
  /** Stage cost in integer cents; absent when unknown. */
  costCents?: number;
};

export type PipelineDetails = {
  stages: PipelineStageDetail[];
  /** Upload -> first stage start; absent for legacy episodes with no runs. */
  queuedMs?: number;
  /** Sum of the finished stages' durations; absent when nothing finished. */
  totalMs?: number;
};

/** The raw wire DTO (snake_case). */
type PipelineDTO = {
  stages: {
    name: string;
    status: PipelineStageStatus;
    duration_ms?: number;
    engine?: string;
    cost_cents?: number;
  }[];
  queued_ms?: number;
  total_ms?: number;
};

function fromDTO(d: PipelineDTO): PipelineDetails {
  return {
    stages: (d.stages ?? []).map((s) => ({
      name: s.name,
      status: s.status,
      durationMs: s.duration_ms,
      engine: s.engine,
      costCents: s.cost_cents
    })),
    queuedMs: d.queued_ms,
    totalMs: d.total_ms
  };
}

/** Cache entry: the details plus the episode state they were fetched under. */
type CacheEntry = { key: string; details: PipelineDetails };

const cache = new Map<string, CacheEntry>();

/** cacheKey pins a cached response to the episode state it described: any
 *  status or stage transition invalidates it on the next open. */
function cacheKey(ep: Pick<Episode, 'status' | 'stage'>): string {
  return `${ep.status}:${ep.stage ?? ''}`;
}

/**
 * fetchPipelineDetails resolves the hover card's data for one episode, cached
 * per episode until its status/stage changes. Throws on a non-OK response (the
 * card renders a neutral error state).
 */
export async function fetchPipelineDetails(
  ep: Pick<Episode, 'id' | 'status' | 'stage'>
): Promise<PipelineDetails> {
  const key = cacheKey(ep);
  const hit = cache.get(ep.id);
  if (hit && hit.key === key) return hit.details;
  const res = await fetch(`/api/episodes/${encodeURIComponent(ep.id)}/pipeline`, {
    credentials: 'same-origin'
  });
  if (!res.ok) throw new Error('pipeline_details_failed');
  const details = fromDTO((await res.json()) as PipelineDTO);
  cache.set(ep.id, { key, details });
  return details;
}

/** clearPipelineDetailsCache empties the per-episode cache (tests). */
export function clearPipelineDetailsCache(): void {
  cache.clear();
}

/**
 * Hover-card display names for the neutral stage names. SPEAKERS/MOMENTS are
 * the product terms for the diarize/moments stages.
 */
export const STAGE_DISPLAY: Record<string, string> = {
  ingest: 'INGEST',
  transcribe: 'TRANSCRIBE',
  diarize: 'SPEAKERS',
  moments: 'MOMENTS',
  render: 'RENDER'
};

/**
 * cardRows merges the fetched stages onto the canonical five-stage order so
 * the card always renders five rows; a stage the response does not carry
 * renders honestly as unreached.
 */
export function cardRows(details?: PipelineDetails): PipelineStageDetail[] {
  return STAGE_ORDER.map(
    (name) => details?.stages.find((s) => s.name === name) ?? { name, status: 'unreached' }
  );
}

/**
 * formatStageDuration renders a derived duration as the card's compact mono
 * form: "3D" (a very long queue wait), "1H 02M", "1M 42S", "42S", and "<1S"
 * for a sub-second run. Em dash for an absent value.
 */
export function formatStageDuration(ms?: number): string {
  if (ms === undefined || ms < 0) return '—';
  const totalSeconds = Math.floor(ms / 1000);
  if (totalSeconds === 0) return '<1S';
  const pad = (n: number) => n.toString().padStart(2, '0');
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  if (h >= 48) return `${Math.floor(h / 24)}D`;
  if (h > 0) return `${h}H ${pad(m)}M`;
  if (m > 0) return `${m}M ${pad(s)}S`;
  return `${s}S`;
}

/** formatCents renders integer cents as dollars ("$0.04"); em dash if absent. */
export function formatCents(cents?: number): string {
  if (cents === undefined || cents < 0) return '—';
  return `$${(cents / 100).toFixed(2)}`;
}

/**
 * engineDisplay renders a public engine label for the card: "bs-asr-2" ->
 * "BS·ASR 2" (mono, faint). Unknown shapes are just uppercased.
 */
export function engineDisplay(label: string): string {
  const parts = label.split('-');
  if (parts.length < 2) return label.toUpperCase();
  const last = parts[parts.length - 1];
  const names = /^\d+$/.test(last) ? parts.slice(0, -1) : parts;
  const version = /^\d+$/.test(last) ? ` ${last}` : '';
  return names.map((p) => p.toUpperCase()).join('·') + version;
}

/** totalCostCents sums the per-stage costs; undefined when none are known. */
export function totalCostCents(details?: PipelineDetails): number | undefined {
  if (!details) return undefined;
  let sum = 0;
  let known = false;
  for (const s of details.stages) {
    if (s.costCents !== undefined) {
      sum += s.costCents;
      known = true;
    }
  }
  return known ? sum : undefined;
}
