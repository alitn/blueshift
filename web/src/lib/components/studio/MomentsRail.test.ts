import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import MomentsRail from './MomentsRail.svelte';
import type { ComposedMoment, EpisodeMoments, Moment, MomentStatus } from '$lib/moments';

// U+200C ZERO WIDTH NON-JOINER — must survive verbatim from the API to the DOM.
const ZWNJ = '‌';
const persianQuote = `خیلی خوش${ZWNJ}حالم که اینجا هستم`;

function moment(rank: number, status: MomentStatus = 'proposed', overrides: Partial<Moment> = {}): Moment {
  return {
    rank,
    startIdx: 1,
    endIdx: 1,
    startMs: 2600,
    endMs: 4600,
    rationaleEn: `Rationale ${rank}`,
    quoteFa: persianQuote,
    status,
    ...overrides
  };
}

function episode(moments: Moment[]): EpisodeMoments {
  return { episodeId: 'ep_test', moments };
}

/** loader resolves a fixed moment set, for the loaded/empty states. */
const loader = (m: EpisodeMoments) => () => Promise.resolve(m);

/** savedOk resolves like the API: the updated moment with the new status. */
const savedOk = () =>
  vi.fn((id: string, rank: number, status: MomentStatus) =>
    Promise.resolve(moment(rank, status))
  );

describe('MomentsRail header and states', () => {
  it('shows the ranked-candidates summary once loaded', async () => {
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1), moment(2)])) }
    });
    expect(await screen.findByTestId('moments-summary')).toHaveTextContent('2 CANDIDATES · RANKED');
  });

  it('shows a neutral loading placeholder before the moments resolve', () => {
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: () => new Promise<EpisodeMoments>(() => {}) }
    });
    expect(screen.getByTestId('moments-loading')).toBeInTheDocument();
    expect(screen.queryByTestId('moments-summary')).not.toBeInTheDocument();
  });

  it('shows the neutral "AWAITING MOMENTS" placeholder (not an error) for zero proposals', async () => {
    render(MomentsRail, { props: { episodeId: 'ep_x', load: loader(episode([])) } });
    const empty = await screen.findByTestId('moments-empty');
    expect(empty).toHaveTextContent('AWAITING MOMENTS');
    expect(screen.queryByTestId('moments-error')).not.toBeInTheDocument();
  });

  it('shows a neutral inline error when the fetch rejects', async () => {
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: () => Promise.reject(new Error('moments_failed')) }
    });
    const err = await screen.findByTestId('moments-error');
    expect(err).toHaveTextContent('MOMENTS UNAVAILABLE');
  });
});

describe('MomentsRail cards', () => {
  it('renders one card per moment in rank order with rank chip and mm:ss–mm:ss range', async () => {
    const m1 = moment(1, 'proposed', { startMs: 2600, endMs: 4600 });
    const m2 = moment(2, 'proposed', { startMs: 0, endMs: 2200, rationaleEn: 'Cold open.' });
    render(MomentsRail, { props: { episodeId: 'ep_x', load: loader(episode([m1, m2])) } });
    await screen.findByTestId('moments-summary');

    const cards = screen.getAllByTestId('moment-card');
    expect(cards).toHaveLength(2);
    expect(within(cards[0]).getByTestId('moment-rank')).toHaveTextContent('#1');
    expect(within(cards[1]).getByTestId('moment-rank')).toHaveTextContent('#2');
    expect(within(cards[0]).getByTestId('moment-range')).toHaveTextContent('00:02–00:04');
    expect(within(cards[1]).getByTestId('moment-range')).toHaveTextContent('00:00–00:02');
    // Rank chips are mono data; the rationale reads LTR above the RTL quote.
    const meta = within(cards[0]).getByTestId('moment-rank').closest('[dir]');
    expect(meta?.getAttribute('dir')).toBe('ltr');
    expect(within(cards[0]).getByTestId('moment-rationale')).toHaveTextContent('Rationale 1');
  });

  it('renders the Persian quote RTL in a <bdi> with tokens, ZWNJ preserved byte-exact', async () => {
    render(MomentsRail, { props: { episodeId: 'ep_x', load: loader(episode([moment(1)])) } });
    await screen.findByTestId('moments-summary');

    const quote = screen.getByTestId('moment-quote');
    expect(quote.getAttribute('dir')).toBe('rtl');
    expect(quote.querySelector('bdi')).not.toBeNull();
    // Byte-exact: the rendered quote equals the input and still carries ZWNJ.
    expect(quote.textContent).toBe(persianQuote);
    expect(quote.textContent).toContain(ZWNJ);
    expect(quote.className).toContain('font-fa');
    expect(quote.className).toContain('text-text-muted');
  });

  it('visually distinguishes the three statuses (proposed default, approved accent, dismissed faint)', async () => {
    render(MomentsRail, {
      props: {
        episodeId: 'ep_x',
        load: loader(episode([moment(1, 'proposed'), moment(2, 'approved'), moment(3, 'dismissed')]))
      }
    });
    await screen.findByTestId('moments-summary');
    const cards = screen.getAllByTestId('moment-card');

    // Proposed: default border, action buttons, no status chip.
    expect(cards[0].className).toContain('border-border-default');
    expect(within(cards[0]).queryByTestId('moment-status')).toBeNull();
    expect(within(cards[0]).getByTestId('moment-approve')).toBeInTheDocument();
    expect(within(cards[0]).getByTestId('moment-dismiss')).toBeInTheDocument();
    expect(within(cards[0]).queryByTestId('moment-undo')).toBeNull();

    // Approved: accent border + APPROVED chip + UNDO, no approve/dismiss.
    expect(cards[1].className).toContain('border-accent-border');
    expect(within(cards[1]).getByTestId('moment-status')).toHaveTextContent('APPROVED');
    expect(within(cards[1]).getByTestId('moment-undo')).toBeInTheDocument();
    expect(within(cards[1]).queryByTestId('moment-approve')).toBeNull();
    expect(within(cards[1]).queryByTestId('moment-dismiss')).toBeNull();

    // Dismissed: faint (disabled-emphasis content) + DISMISSED chip + UNDO.
    expect(within(cards[2]).getByTestId('moment-status')).toHaveTextContent('DISMISSED');
    expect(within(cards[2]).getByTestId('moment-undo')).toBeInTheDocument();
    const faded = within(cards[2]).getByTestId('moment-rationale').parentElement;
    expect(faded?.className).toContain('opacity-[0.35]');
    expect(cards[2].getAttribute('data-status')).toBe('dismissed');
  });
});

describe('MomentsRail seek (moments → video)', () => {
  it('clicking a card calls onSeek with the moment start; clicking APPROVE does not seek', async () => {
    const onSeek = vi.fn();
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), save: savedOk(), onSeek }
    });
    await screen.findByTestId('moments-summary');

    await fireEvent.click(screen.getByTestId('moment-card'));
    expect(onSeek).toHaveBeenCalledExactlyOnceWith(2600);

    await fireEvent.click(screen.getByTestId('moment-approve'));
    expect(onSeek).toHaveBeenCalledTimes(1); // the button click never seeks
  });

  it('cards are focusable; Enter and Space on the card itself seek', async () => {
    const onSeek = vi.fn();
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), onSeek }
    });
    await screen.findByTestId('moments-summary');

    const card = screen.getByTestId('moment-card');
    expect(card).toHaveAttribute('tabindex', '0');
    card.focus();
    expect(document.activeElement).toBe(card);

    await fireEvent.keyDown(card, { key: 'Enter' });
    expect(onSeek).toHaveBeenCalledExactlyOnceWith(2600);
    await fireEvent.keyDown(card, { key: ' ' });
    expect(onSeek).toHaveBeenCalledTimes(2);
  });
});

describe('MomentsRail review actions (optimistic)', () => {
  it('APPROVE button flips the card optimistically and posts the transition', async () => {
    let resolve!: (m: Moment) => void;
    const save = vi.fn(() => new Promise<Moment>((r) => (resolve = r)));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), save }
    });
    await screen.findByTestId('moments-summary');

    await fireEvent.click(screen.getByTestId('moment-approve'));
    // Optimistic: the card is approved BEFORE the API resolves.
    expect(save).toHaveBeenCalledExactlyOnceWith('ep_x', 1, 'approved');
    expect(screen.getByTestId('moment-card').getAttribute('data-status')).toBe('approved');
    expect(screen.getByTestId('moment-status')).toHaveTextContent('APPROVED');

    resolve(moment(1, 'approved'));
    await vi.waitFor(() =>
      expect(screen.getByTestId('moment-card').getAttribute('data-status')).toBe('approved')
    );
  });

  it('reverts the optimistic flip when the API refuses', async () => {
    let reject!: (e: Error) => void;
    const save = vi.fn(() => new Promise<Moment>((_, r) => (reject = r)));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), save }
    });
    await screen.findByTestId('moments-summary');

    await fireEvent.click(screen.getByTestId('moment-approve'));
    // Optimistically approved while the request is in flight…
    expect(screen.getByTestId('moment-card').getAttribute('data-status')).toBe('approved');
    // …then the refusal puts the previous status back.
    reject(new Error('moment_status_failed'));
    await vi.waitFor(() =>
      expect(screen.getByTestId('moment-card').getAttribute('data-status')).toBe('proposed')
    );
    // Back to reviewable: the action buttons returned.
    expect(screen.getByTestId('moment-approve')).toBeInTheDocument();
  });

  it('single-key review: A approves, D dismisses the focused card', async () => {
    const save = savedOk();
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1), moment(2)])), save }
    });
    await screen.findByTestId('moments-summary');
    const cards = screen.getAllByTestId('moment-card');

    cards[0].focus();
    await fireEvent.keyDown(cards[0], { key: 'a' });
    expect(save).toHaveBeenLastCalledWith('ep_x', 1, 'approved');
    expect(cards[0].getAttribute('data-status')).toBe('approved');

    cards[1].focus();
    await fireEvent.keyDown(cards[1], { key: 'D' }); // case-insensitive
    expect(save).toHaveBeenLastCalledWith('ep_x', 2, 'dismissed');
    expect(cards[1].getAttribute('data-status')).toBe('dismissed');

    // Other keys do nothing; modified keys pass through (browser shortcuts).
    await fireEvent.keyDown(cards[0], { key: 'x' });
    await fireEvent.keyDown(cards[0], { key: 'a', metaKey: true });
    expect(save).toHaveBeenCalledTimes(2);
  });

  it('never posts an illegal transition (A on an approved card is a no-op)', async () => {
    const save = savedOk();
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1, 'approved')])), save }
    });
    await screen.findByTestId('moments-summary');

    const card = screen.getByTestId('moment-card');
    card.focus();
    await fireEvent.keyDown(card, { key: 'a' });
    await fireEvent.keyDown(card, { key: 'd' }); // approved -> dismissed skips the undo
    expect(save).not.toHaveBeenCalled();
    expect(card.getAttribute('data-status')).toBe('approved');
  });

  it('UNDO reverses a verdict back to proposed', async () => {
    const save = savedOk();
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1, 'dismissed')])), save }
    });
    await screen.findByTestId('moments-summary');

    await fireEvent.click(screen.getByTestId('moment-undo'));
    expect(save).toHaveBeenCalledExactlyOnceWith('ep_x', 1, 'proposed');
    expect(screen.getByTestId('moment-card').getAttribute('data-status')).toBe('proposed');
    expect(screen.getByTestId('moment-approve')).toBeInTheDocument();
  });
});

// --- compose (m1-prompt-moments) ---------------------------------------------

function composedMoment(rank: number, overrides: Partial<ComposedMoment> = {}): ComposedMoment {
  return {
    rank,
    startIdx: 1,
    endIdx: 1,
    startMs: 2960,
    endMs: 4600,
    rationaleEn: `Prompt match ${rank}`,
    quoteFa: persianQuote,
    ...overrides
  };
}

/** submitPrompt types the prompt and submits the compose form. */
async function submitPrompt(text: string): Promise<void> {
  await fireEvent.input(screen.getByTestId('compose-input'), { target: { value: text } });
  await fireEvent.submit(screen.getByTestId('compose-form'));
}

describe('MomentsRail compose flow', () => {
  it('submits the trimmed prompt, shows the loading line, then renders the PROMPT RESULTS group', async () => {
    let resolve!: (r: ComposedMoment[]) => void;
    const compose = vi.fn(() => new Promise<ComposedMoment[]>((r) => (resolve = r)));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), compose }
    });
    await screen.findByTestId('moments-summary');

    await submitPrompt('  find the joy  ');
    expect(compose).toHaveBeenCalledExactlyOnceWith('ep_x', 'find the joy');
    expect(screen.getByTestId('compose-loading')).toHaveTextContent('COMPOSING…');

    resolve([composedMoment(1)]);
    const results = await screen.findByTestId('compose-results');
    expect(within(results).getByText('PROMPT RESULTS')).toBeInTheDocument();
    expect(screen.getByTestId('compose-summary')).toHaveTextContent('1 MATCH');
    const card = screen.getByTestId('composed-card');
    expect(within(card).getByTestId('composed-rank')).toHaveTextContent('#1');
    expect(within(card).getByTestId('composed-range')).toHaveTextContent('00:02–00:04');
    expect(within(card).getByTestId('composed-rationale')).toHaveTextContent('Prompt match 1');
    // Verbatim RTL quote in a <bdi>, ZWNJ byte-exact — same rules as the rail.
    const quote = within(card).getByTestId('composed-quote');
    expect(quote.getAttribute('dir')).toBe('rtl');
    expect(quote.querySelector('bdi')).not.toBeNull();
    expect(quote.textContent).toBe(persianQuote);
    expect(quote.textContent).toContain(ZWNJ);
    // The ranked list is untouched underneath.
    expect(screen.getByTestId('moment-card')).toBeInTheDocument();
  });

  it('ignores a blank prompt', async () => {
    const compose = vi.fn(() => Promise.resolve([composedMoment(1)]));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), compose }
    });
    await screen.findByTestId('moments-summary');
    await submitPrompt('   ');
    expect(compose).not.toHaveBeenCalled();
    expect(screen.queryByTestId('compose-loading')).not.toBeInTheDocument();
  });

  it('renders the neutral "no matches" line (not an error) for an empty result set', async () => {
    const compose = vi.fn(() => Promise.resolve([] as ComposedMoment[]));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), compose }
    });
    await screen.findByTestId('moments-summary');
    await submitPrompt('nothing matches this');
    const empty = await screen.findByTestId('compose-empty');
    expect(empty).toHaveTextContent('No matches');
    expect(screen.queryByTestId('compose-error')).not.toBeInTheDocument();
    expect(screen.queryByTestId('compose-results')).not.toBeInTheDocument();
  });

  it('renders a neutral error line when the compose call fails', async () => {
    const compose = vi.fn(() => Promise.reject(new Error('compose_failed')));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), compose }
    });
    await screen.findByTestId('moments-summary');
    await submitPrompt('boom');
    const err = await screen.findByTestId('compose-error');
    expect(err).toHaveTextContent('Compose failed');
    expect(screen.queryByTestId('compose-results')).not.toBeInTheDocument();
  });

  it('KEEP persists: the card leaves the group and the kept moment joins the ranked list in rank order', async () => {
    const keptMoment = moment(3, 'approved', { rationaleEn: 'Prompt match 1' });
    const compose = vi.fn(() => Promise.resolve([composedMoment(1)]));
    const keep = vi.fn(() => Promise.resolve(keptMoment));
    render(MomentsRail, {
      props: {
        episodeId: 'ep_x',
        load: loader(episode([moment(1), moment(2)])),
        compose,
        keep
      }
    });
    await screen.findByTestId('moments-summary');
    await submitPrompt('find the joy');
    await screen.findByTestId('compose-results');

    await fireEvent.click(screen.getByTestId('composed-keep'));
    expect(keep).toHaveBeenCalledExactlyOnceWith('ep_x', composedMoment(1));

    // The group emptied and disappeared; the ranked list gained rank 3, approved.
    await vi.waitFor(() =>
      expect(screen.queryByTestId('compose-results')).not.toBeInTheDocument()
    );
    const cards = screen.getAllByTestId('moment-card');
    expect(cards).toHaveLength(3);
    expect(within(cards[2]).getByTestId('moment-rank')).toHaveTextContent('#3');
    expect(cards[2].getAttribute('data-status')).toBe('approved');
    expect(within(cards[2]).getByTestId('moment-status')).toHaveTextContent('APPROVED');
    // From here it is an ordinary moment: UNDO is offered like any approved card.
    expect(within(cards[2]).getByTestId('moment-undo')).toBeInTheDocument();
  });

  it('a refused keep leaves the card in place with a neutral line', async () => {
    const compose = vi.fn(() => Promise.resolve([composedMoment(1)]));
    const keep = vi.fn(() => Promise.reject(new Error('keep_failed')));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), compose, keep }
    });
    await screen.findByTestId('moments-summary');
    await submitPrompt('find the joy');
    await screen.findByTestId('compose-results');

    await fireEvent.click(screen.getByTestId('composed-keep'));
    const err = await screen.findByTestId('compose-keep-error');
    expect(err).toHaveTextContent('Couldn’t keep that moment');
    expect(screen.getByTestId('composed-card')).toBeInTheDocument();
    expect(screen.getAllByTestId('moment-card')).toHaveLength(1); // nothing joined the list
  });

  it('DISCARD drops the card; the group disappears with the last one', async () => {
    const compose = vi.fn(() => Promise.resolve([composedMoment(1), composedMoment(2)]));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([moment(1)])), compose }
    });
    await screen.findByTestId('moments-summary');
    await submitPrompt('find the joy');
    await screen.findByTestId('compose-results');
    expect(screen.getAllByTestId('composed-card')).toHaveLength(2);

    await fireEvent.click(screen.getAllByTestId('composed-discard')[0]);
    expect(screen.getAllByTestId('composed-card')).toHaveLength(1);
    await fireEvent.click(screen.getByTestId('composed-discard'));
    expect(screen.queryByTestId('compose-results')).not.toBeInTheDocument();
    expect(screen.queryByTestId('compose-empty')).not.toBeInTheDocument(); // idle, not "no matches"
  });

  it('keyboard on a focused result card: Enter seeks, K keeps, D discards; KEEP click never seeks', async () => {
    const onSeek = vi.fn();
    const compose = vi.fn(() => Promise.resolve([composedMoment(1), composedMoment(2)]));
    const keep = vi.fn(() => Promise.resolve(moment(2, 'approved')));
    render(MomentsRail, {
      props: { episodeId: 'ep_x', load: loader(episode([])), compose, keep, onSeek }
    });
    await screen.findByTestId('moments-empty');
    await submitPrompt('find the joy');
    await screen.findByTestId('compose-results');

    const cards = screen.getAllByTestId('composed-card');
    expect(cards[0]).toHaveAttribute('tabindex', '0');
    cards[0].focus();
    await fireEvent.keyDown(cards[0], { key: 'Enter' });
    expect(onSeek).toHaveBeenCalledExactlyOnceWith(2960);

    await fireEvent.keyDown(cards[0], { key: 'k' });
    expect(keep).toHaveBeenCalledExactlyOnceWith('ep_x', composedMoment(1));
    await vi.waitFor(() => expect(screen.getAllByTestId('composed-card')).toHaveLength(1));

    const last = screen.getByTestId('composed-card');
    last.focus();
    await fireEvent.keyDown(last, { key: 'd' });
    expect(screen.queryByTestId('compose-results')).not.toBeInTheDocument();

    // Seek count unchanged by keep/discard/button interactions.
    expect(onSeek).toHaveBeenCalledTimes(1);
  });
});
