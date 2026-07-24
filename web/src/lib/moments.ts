/**
 * Client for the neutral moments endpoints:
 *   GET  /api/episodes/{id}/moments
 *   POST /api/episodes/{id}/moments/{rank}/status
 * The browser only ever sees Blueshift-neutral content — ranked spans,
 * quote-aligned timings, an English rationale, a verbatim Persian quote, and
 * the review status. Nothing here names the underlying stack. Mirrors the
 * shape/error conventions of transcript.ts.
 */

/** The closed review-status set (mirrors the API's CHECK-constrained column). */
export type MomentStatus = 'proposed' | 'approved' | 'dismissed';

/**
 * Moment is one ranked proposal: rank 1 = best (the rank is the moment's
 * natural key within its episode), the inclusive segment-idx span, the
 * quote-aligned ASR window in ms, the verbatim texts, and the review status.
 */
export type Moment = {
  rank: number;
  startIdx: number;
  endIdx: number;
  startMs: number;
  endMs: number;
  rationaleEn: string;
  /** Verbatim Persian quote — ZWNJ and every other byte preserved. */
  quoteFa: string;
  status: MomentStatus;
};

/** EpisodeMoments is the camelCase view the moment rail renders. */
export type EpisodeMoments = {
  episodeId: string;
  moments: Moment[];
};

/** The raw per-moment DTO (snake_case) as returned by the API. */
type MomentDTO = {
  rank: number;
  start_idx: number;
  end_idx: number;
  start_ms: number;
  end_ms: number;
  rationale_en: string;
  quote_fa: string;
  status: MomentStatus;
};

/** The raw moments DTO (snake_case) as returned by the API. */
type MomentsDTO = {
  episode_id: string;
  moments: MomentDTO[];
};

function momentFromDTO(d: MomentDTO): Moment {
  return {
    rank: d.rank,
    startIdx: d.start_idx,
    endIdx: d.end_idx,
    startMs: d.start_ms,
    endMs: d.end_ms,
    rationaleEn: d.rationale_en,
    quoteFa: d.quote_fa,
    status: d.status
  };
}

/**
 * fetchMoments returns an episode's moments, rank-ordered best-first. An
 * episode whose moments stage has not produced proposals yet resolves to an
 * empty `moments` array (the "awaiting moments" state), not an error. Throws
 * on a non-OK response (404 unknown/foreign, 401 unauthenticated) with
 * generic copy — the caller decides how to render it.
 */
export async function fetchMoments(id: string): Promise<EpisodeMoments> {
  const res = await fetch(`/api/episodes/${encodeURIComponent(id)}/moments`, {
    credentials: 'same-origin'
  });
  if (!res.ok) throw new Error('moments_failed');
  const body = (await res.json()) as MomentsDTO;
  return { episodeId: body.episode_id, moments: body.moments.map(momentFromDTO) };
}

/**
 * setMomentStatus flips one moment's review status (proposed -> approved or
 * dismissed; the undo goes back to proposed) and resolves to the updated
 * moment. Throws on a non-OK response — 409 for an illegal transition, 404
 * for an unknown rank/episode — with generic copy; the optimistic caller
 * reverts on any failure.
 */
export async function setMomentStatus(
  id: string,
  rank: number,
  status: MomentStatus
): Promise<Moment> {
  const res = await fetch(
    `/api/episodes/${encodeURIComponent(id)}/moments/${encodeURIComponent(String(rank))}/status`,
    {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ status })
    }
  );
  if (!res.ok) throw new Error('moment_status_failed');
  const body = (await res.json()) as MomentDTO;
  return momentFromDTO(body);
}
