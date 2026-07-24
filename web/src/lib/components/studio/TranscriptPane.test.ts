import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { tick } from 'svelte';
import TranscriptPane from './TranscriptPane.svelte';
import { FOLLOW_SUSPEND_MS } from '$lib/transcriptSync';
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

describe('TranscriptPane segment activation (transcript → video)', () => {
  const three = () =>
    transcript([seg(0, 0, 'اول', 'S1'), seg(1, 2600, 'دوم', 'S2'), seg(2, 5000, 'سوم', 'S1')]);

  it('clicking a segment calls onSelect with its idx', async () => {
    const onSelect = vi.fn();
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(three()), onSelect } });
    await screen.findByTestId('transcript-summary');

    const segs = screen.getAllByTestId('transcript-segment');
    await fireEvent.click(segs[1]);
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(1);
    await fireEvent.click(segs[0]);
    expect(onSelect).toHaveBeenLastCalledWith(0);
  });

  it('segments are focusable buttons, activatable with Enter and Space', async () => {
    const onSelect = vi.fn();
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(three()), onSelect } });
    await screen.findByTestId('transcript-summary');

    const segs = screen.getAllByTestId('transcript-segment');
    expect(segs[0]).toHaveAttribute('role', 'button');
    expect(segs[0]).toHaveAttribute('tabindex', '0');

    segs[2].focus();
    expect(document.activeElement).toBe(segs[2]);

    await fireEvent.keyDown(segs[2], { key: 'Enter' });
    expect(onSelect).toHaveBeenLastCalledWith(2);
    await fireEvent.keyDown(segs[1], { key: ' ' });
    expect(onSelect).toHaveBeenLastCalledWith(1);
    // Other keys do not activate.
    await fireEvent.keyDown(segs[0], { key: 'a' });
    expect(onSelect).toHaveBeenCalledTimes(2);
  });

  it('reports the loaded transcript up via onLoaded (the host maps timings)', async () => {
    const onLoaded = vi.fn();
    const t = three();
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(t), onLoaded } });
    await screen.findByTestId('transcript-summary');
    expect(onLoaded).toHaveBeenCalledExactlyOnceWith(t);
  });
});

describe('TranscriptPane active-segment highlight (video → transcript)', () => {
  const three = () =>
    transcript([seg(0, 0, 'اول', 'S1'), seg(1, 2600, 'دوم', 'S2'), seg(2, 5000, 'سوم', 'S1')]);

  it('marks exactly the active segment: wash background, accent start edge, aria-current', async () => {
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(three()), activeIdx: 1 } });
    await screen.findByTestId('transcript-summary');

    const segs = screen.getAllByTestId('transcript-segment');
    expect(segs[1]).toHaveAttribute('aria-current', 'true');
    expect(segs[1].className).toContain('bg-accent-wash-14');
    // Accent edge on the reading-start side (inline-start of the RTL block).
    expect(segs[1].className).toContain('border-s-accent');

    for (const other of [segs[0], segs[2]]) {
      expect(other).not.toHaveAttribute('aria-current');
      expect(other.className).not.toContain('bg-accent-wash-14');
      expect(other.className).toContain('border-s-transparent');
    }
  });

  it('highlights nothing before the first segment (activeIdx = -1, the default)', async () => {
    render(TranscriptPane, { props: { episodeId: 'ep_x', load: loader(three()) } });
    await screen.findByTestId('transcript-summary');
    for (const el of screen.getAllByTestId('transcript-segment')) {
      expect(el).not.toHaveAttribute('aria-current');
      expect(el.className).not.toContain('bg-accent-wash-14');
    }
  });

  it('moves the highlight when activeIdx changes', async () => {
    const { rerender } = render(TranscriptPane, {
      props: { episodeId: 'ep_x', load: loader(three()), activeIdx: 0 }
    });
    await screen.findByTestId('transcript-summary');
    expect(screen.getAllByTestId('transcript-segment')[0]).toHaveAttribute('aria-current', 'true');

    await rerender({ activeIdx: 2 });
    const segs = screen.getAllByTestId('transcript-segment');
    expect(segs[0]).not.toHaveAttribute('aria-current');
    expect(segs[2]).toHaveAttribute('aria-current', 'true');
  });
});

describe('TranscriptPane auto-follow (scroll into view + ~4s manual-scroll suspension)', () => {
  const three = () =>
    transcript([seg(0, 0, 'اول', 'S1'), seg(1, 2600, 'دوم', 'S2'), seg(2, 5000, 'سوم', 'S1')]);

  // jsdom has no layout: give the scroll region a real-looking viewport and
  // push every segment below it, so the "outside the viewport" branch runs.
  function stubRects() {
    return vi
      .spyOn(Element.prototype, 'getBoundingClientRect')
      .mockImplementation(function (this: Element) {
        const isSegment = (this as HTMLElement).dataset?.segIdx !== undefined;
        const top = isSegment ? 2000 : 0;
        const bottom = isSegment ? 2100 : 500;
        return {
          top,
          bottom,
          left: 0,
          right: 100,
          width: 100,
          height: bottom - top,
          x: 0,
          y: top,
          toJSON: () => ({})
        } as DOMRect;
      });
  }

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
    // scrollIntoView is assigned (jsdom lacks it), not spied — remove it so no
    // other suite inherits a stale mock.
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });

  it('smooth-scrolls an out-of-view active segment into view on segment change', async () => {
    stubRects();
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;

    const { rerender } = render(TranscriptPane, {
      props: { episodeId: 'ep_x', load: loader(three()), activeIdx: -1 }
    });
    await screen.findByTestId('transcript-summary');

    await rerender({ activeIdx: 1 });
    await tick();
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'nearest', behavior: 'smooth' });
  });

  it('does not scroll when the active segment is already fully visible', async () => {
    // No rect stub: jsdom's zero rects read as "fully in view".
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;

    const { rerender } = render(TranscriptPane, {
      props: { episodeId: 'ep_x', load: loader(three()), activeIdx: -1 }
    });
    await screen.findByTestId('transcript-summary');
    await rerender({ activeIdx: 1 });
    await tick();
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it('a manual wheel scroll suspends auto-follow; it resumes on the next segment change after ~4s idle', async () => {
    vi.useFakeTimers();
    stubRects();
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;

    const { rerender } = render(TranscriptPane, {
      props: { episodeId: 'ep_x', load: loader(three()), activeIdx: -1 }
    });
    await vi.waitFor(() => screen.getByTestId('transcript-summary'));

    // The user scrolls the pane, then playback reaches the next segment: the
    // pane must NOT fight the user.
    const box = screen.getByTestId('transcript-pane').querySelector('[tabindex]')!;
    await fireEvent.wheel(box);
    await rerender({ activeIdx: 1 });
    await tick();
    expect(scrollIntoView).not.toHaveBeenCalled();

    // Still inside the suspension window on the next change: stays suspended.
    vi.advanceTimersByTime(FOLLOW_SUSPEND_MS - 500);
    await rerender({ activeIdx: 2 });
    await tick();
    expect(scrollIntoView).not.toHaveBeenCalled();

    // Idle elapsed → the next segment change follows again.
    vi.advanceTimersByTime(1000);
    await rerender({ activeIdx: 0 });
    await tick();
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
  });

  it('clicking a segment resumes auto-follow immediately (explicit jump beats suspension)', async () => {
    vi.useFakeTimers();
    stubRects();
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;

    const { rerender } = render(TranscriptPane, {
      props: { episodeId: 'ep_x', load: loader(three()), activeIdx: -1, onSelect: () => {} }
    });
    await vi.waitFor(() => screen.getByTestId('transcript-summary'));

    const box = screen.getByTestId('transcript-pane').querySelector('[tabindex]')!;
    await fireEvent.wheel(box);

    // The click clears the suspension (its pointerdown re-suspends first, then
    // the activation wins), so the host-driven activeIdx move may follow.
    const segs = screen.getAllByTestId('transcript-segment');
    await fireEvent.pointerDown(segs[1]);
    await fireEvent.click(segs[1]);
    await rerender({ activeIdx: 1 });
    await tick();
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
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
