import { render, screen, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import TranscriptPane from './TranscriptPane.svelte';
import type { Transcript, TranscriptSegment, TranscriptWord } from '$lib/transcript';

// U+200C ZERO WIDTH NON-JOINER — must survive verbatim from the API to the DOM.
const ZWNJ = '‌';

function words(n: number): TranscriptWord[] {
  return Array.from({ length: n }, (_, i) => ({
    text: `w${i}`,
    startMs: i * 100,
    endMs: i * 100 + 90,
    conf: 0.9
  }));
}

function seg(
  idx: number,
  startMs: number,
  text: string,
  speakerKey: string | null,
  wordCount = 1
): TranscriptSegment {
  return { idx, startMs, endMs: startMs + 1000, text, speakerKey, words: words(wordCount) };
}

function transcript(segments: TranscriptSegment[], language = 'fa'): Transcript {
  return { episodeId: 'ep_test', language, segments };
}

/** loader resolves a fixed transcript, for the loaded/empty states. */
const loader = (t: Transcript) => () => Promise.resolve(t);

describe('TranscriptPane header summary', () => {
  it('shows the language label and total word count across segments', async () => {
    const t = transcript([seg(0, 0, 'یک', 'S1', 3), seg(1, 2000, 'دو', null, 2)]);
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(t) } });
    expect(await screen.findByTestId('transcript-summary')).toHaveTextContent('FA · 5 WORDS');
  });

  it('renders the word count with a thousands separator', async () => {
    const t = transcript([seg(0, 0, 'متن', 'S1', 1234)]);
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(t) } });
    expect(await screen.findByTestId('transcript-summary')).toHaveTextContent('FA · 1,234 WORDS');
  });

  it('uppercases the language label (data-driven, not hard-coded fa)', async () => {
    const t = transcript([seg(0, 0, 'x', 'S1', 1)], 'ar');
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(t) } });
    expect(await screen.findByTestId('transcript-summary')).toHaveTextContent('AR · 1 WORDS');
  });
});

describe('TranscriptPane timecodes (mm:ss)', () => {
  it('formats each segment start offset as zero-padded mm:ss', async () => {
    const t = transcript([
      seg(0, 0, 'a', 'S1'),
      seg(1, 5000, 'b', 'S1'),
      seg(2, 65000, 'c', 'S1'),
      seg(3, 605000, 'd', 'S1'),
      seg(4, 3671000, 'e', 'S1') // > 1h: minutes keep counting (61:11)
    ]);
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(t) } });
    await screen.findByTestId('transcript-summary');
    const codes = screen.getAllByTestId('segment-timecode').map((el) => el.textContent?.trim());
    expect(codes).toEqual(['00:00', '00:05', '01:05', '10:05', '61:11']);
  });
});

describe('TranscriptPane speaker chip', () => {
  it('renders a mono chip with the raw label only when speaker_key is non-null', async () => {
    const t = transcript([seg(0, 0, 'diarized', 'S2', 1), seg(1, 1000, 'undiarized', null, 1)]);
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(t) } });
    await screen.findByTestId('transcript-summary');

    const chips = screen.getAllByTestId('speaker-chip');
    expect(chips).toHaveLength(1);
    expect(chips[0]).toHaveTextContent('S2');
    // Raw diarization label uses the mono stack (metadata, not a Persian name).
    expect(chips[0].className).toContain('font-mono');

    const segs = screen.getAllByTestId('transcript-segment');
    expect(within(segs[0]).queryByTestId('speaker-chip')).not.toBeNull();
    expect(within(segs[1]).queryByTestId('speaker-chip')).toBeNull();
  });
});

describe('TranscriptPane states', () => {
  it('shows a neutral loading placeholder before the transcript resolves', () => {
    // A never-settling loader keeps the pane in its initial loading state.
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: () => new Promise<Transcript>(() => {}) } });
    expect(screen.getByTestId('transcript-loading')).toBeInTheDocument();
    expect(screen.queryByTestId('transcript-summary')).not.toBeInTheDocument();
  });

  it('shows the neutral "awaiting transcript" placeholder (not an error) for zero segments', async () => {
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(transcript([])) } });
    const empty = await screen.findByTestId('transcript-empty');
    expect(empty).toHaveTextContent('AWAITING TRANSCRIPT');
    // Empty is not an error, and the summary still reports 0 words.
    expect(screen.queryByTestId('transcript-error')).not.toBeInTheDocument();
    expect(screen.getByTestId('transcript-summary')).toHaveTextContent('FA · 0 WORDS');
  });

  it('shows a neutral inline error when the fetch rejects', async () => {
    render(TranscriptPane, {
      props: { episodeId: 'ep_x', load: () => Promise.reject(new Error('transcript_failed')) }
    });
    const err = await screen.findByTestId('transcript-error');
    // Neutral, generic copy (the vendor-leak gate enforces no provider names).
    expect(err).toHaveTextContent('TRANSCRIPT UNAVAILABLE');
    expect(screen.queryByTestId('transcript-summary')).not.toBeInTheDocument();
  });
});

describe('TranscriptPane RTL + verbatim ZWNJ', () => {
  it('renders each Persian body dir=rtl in a <bdi> with tokens, ZWNJ preserved byte-exact', async () => {
    const persian = `خوش${ZWNJ}آمدید به برنامه`;
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(transcript([seg(0, 0, persian, 'S1', 3)])) } });
    await screen.findByTestId('transcript-summary');

    const segment = screen.getByTestId('transcript-segment');
    expect(segment.getAttribute('dir')).toBe('rtl');

    const body = screen.getByTestId('segment-text');
    expect(body.querySelector('bdi')).not.toBeNull();
    // Byte-exact: the rendered body equals the input and still carries the ZWNJ.
    expect(body.textContent).toBe(persian);
    expect(body.textContent).toContain(ZWNJ);
    // Body is the Persian content stack in the transcript-body colour, both tokens.
    expect(body.className).toContain('font-fa');
    expect(body.className).toContain('text-text-body');
  });

  it('keeps the metadata row LTR above the RTL body', async () => {
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(transcript([seg(0, 90000, 'سلام', 'S1', 1)])) } });
    await screen.findByTestId('transcript-summary');
    const row = screen.getByTestId('segment-timecode').closest('[dir]');
    expect(row?.getAttribute('dir')).toBe('ltr');
    expect(screen.getByTestId('segment-timecode')).toHaveTextContent('01:30');
  });
});
