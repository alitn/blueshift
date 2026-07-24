/**
 * Client for the neutral transcript endpoint (GET /api/episodes/{id}/transcript).
 * The browser only ever sees Blueshift-neutral content — verbatim text, word
 * timings, and an episode-local speaker_key — and opaque, prefixed ids. Nothing
 * here names the underlying stack. Mirrors the shape/error conventions of
 * episodes.ts.
 */

/**
 * TranscriptWord is one recognised token as the positional wire tuple
 * [text, startMs, endMs, conf]. The API sends a positional array (compact for a
 * long interview's thousands of words); this is the parsed, named view.
 */
export type TranscriptWord = {
  text: string;
  startMs: number;
  endMs: number;
  conf: number;
};

/** TranscriptSegment is one contiguous utterance with its word-level timing. */
export type TranscriptSegment = {
  idx: number;
  startMs: number;
  endMs: number;
  text: string;
  /** Episode-local diarization label (e.g. "S1"), or null until diarized. */
  speakerKey: string | null;
  words: TranscriptWord[];
};

/** Transcript is the camelCase view the transcript editor renders. */
export type Transcript = {
  episodeId: string;
  language: string;
  segments: TranscriptSegment[];
};

/** The positional word tuple exactly as it arrives on the wire. */
type WordTupleDTO = [text: string, startMs: number, endMs: number, conf: number];

/** The raw per-segment DTO (snake_case) as returned by the API. */
type TranscriptSegmentDTO = {
  idx: number;
  start_ms: number;
  end_ms: number;
  text: string;
  speaker_key: string | null;
  words: WordTupleDTO[];
};

/** The raw transcript DTO (snake_case) as returned by the API. */
type TranscriptDTO = {
  episode_id: string;
  language: string;
  segments: TranscriptSegmentDTO[];
};

function wordFromTuple(t: WordTupleDTO): TranscriptWord {
  return { text: t[0], startMs: t[1], endMs: t[2], conf: t[3] };
}

function segmentFromDTO(d: TranscriptSegmentDTO): TranscriptSegment {
  return {
    idx: d.idx,
    startMs: d.start_ms,
    endMs: d.end_ms,
    text: d.text,
    speakerKey: d.speaker_key,
    words: d.words.map(wordFromTuple)
  };
}

function fromDTO(d: TranscriptDTO): Transcript {
  return {
    episodeId: d.episode_id,
    language: d.language,
    segments: d.segments.map(segmentFromDTO)
  };
}

/**
 * fetchTranscript returns an episode's transcript (segments ordered by idx). An
 * episode with no segments yet resolves to an empty `segments` array (the
 * "awaiting transcript" state), not an error. Throws on a non-OK response
 * (404 for an unknown/foreign episode, 401 when unauthenticated) with generic
 * copy — the caller decides how to render it.
 */
export async function fetchTranscript(id: string): Promise<Transcript> {
  const res = await fetch(`/api/episodes/${encodeURIComponent(id)}/transcript`, {
    credentials: 'same-origin'
  });
  if (!res.ok) throw new Error('transcript_failed');
  const body = (await res.json()) as TranscriptDTO;
  return fromDTO(body);
}
