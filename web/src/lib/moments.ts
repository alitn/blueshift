/**
 * Client for the neutral moments endpoints:
 *   GET  /api/episodes/{id}/moments
 *   POST /api/episodes/{id}/moments/{rank}/status
 *   POST /api/episodes/{id}/moments/compose   (free-prompt, ephemeral results)
 *   POST /api/episodes/{id}/moments/keep      (persist one composed result)
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
 * ComposedMoment is one EPHEMERAL free-prompt result: moments-shaped (rank
 * within the result set, span, word-accurate window, texts) but unreviewed
 * and unpersisted — it exists only in the response until kept or discarded.
 */
export type ComposedMoment = {
  rank: number;
  startIdx: number;
  endIdx: number;
  startMs: number;
  endMs: number;
  rationaleEn: string;
  /** Verbatim Persian quote — ZWNJ and every other byte preserved. */
  quoteFa: string;
};

/** The raw composed-moment DTO (snake_case) as returned by the API. */
type ComposedMomentDTO = {
  rank: number;
  start_idx: number;
  end_idx: number;
  start_ms: number;
  end_ms: number;
  rationale_en: string;
  quote_fa: string;
};

/**
 * composeMoments runs one free prompt over the episode's transcript and
 * resolves to the ephemeral ranked results. An EMPTY array is a valid "no
 * matches" answer, not an error. Throws with generic copy on a non-OK
 * response (400 bad prompt, 404, 409 not transcribed, 429 rate limited, 503).
 */
export async function composeMoments(id: string, prompt: string): Promise<ComposedMoment[]> {
  const res = await fetch(`/api/episodes/${encodeURIComponent(id)}/moments/compose`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ prompt })
  });
  if (!res.ok) throw new Error('compose_failed');
  const body = (await res.json()) as { episode_id: string; moments: ComposedMomentDTO[] };
  return body.moments.map((d) => ({
    rank: d.rank,
    startIdx: d.start_idx,
    endIdx: d.end_idx,
    startMs: d.start_ms,
    endMs: d.end_ms,
    rationaleEn: d.rationale_en,
    quoteFa: d.quote_fa
  }));
}

/**
 * keepComposedMoment persists one composed result as a real moment
 * (approve-to-keep) and resolves to the persisted moment — approved, at the
 * episode's next free rank; from then on it behaves like any other moment.
 * Only the span and texts are sent: the server re-validates the quote against
 * the current transcript and re-derives the times (nothing client-supplied is
 * believed for timing or placement). Throws with generic copy on a non-OK
 * response (409 when the transcript has changed under the result).
 */
export async function keepComposedMoment(id: string, m: ComposedMoment): Promise<Moment> {
  const res = await fetch(`/api/episodes/${encodeURIComponent(id)}/moments/keep`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      start_idx: m.startIdx,
      end_idx: m.endIdx,
      rationale_en: m.rationaleEn,
      quote_fa: m.quoteFa
    })
  });
  if (!res.ok) throw new Error('keep_failed');
  const body = (await res.json()) as MomentDTO;
  return momentFromDTO(body);
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
