import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import LibraryTable from './LibraryTable.svelte';
import type { Episode } from '$lib/episodes';

const ZWNJ = '‌';

function ep(id: string, status: Episode['status'], title: string): Episode {
  return {
    id,
    title,
    sourceFilename: `${id}_master.mp4`,
    language: 'fa',
    status,
    // A row that reached 'uploaded' or beyond in these fixtures has a master;
    // the abandoned-upload case is constructed explicitly in its own test.
    hasMaster: true,
    durationMs: status === 'ready' ? 2468000 : undefined,
    sizeBytes: 6_100_000_000,
    uploadedAt: '2026-07-21T00:00:00Z'
  };
}

const fourStates: Episode[] = [
  ep('EP-UP', 'uploaded', 'اقتصاد دیجیتال'),
  ep('EP-PR', 'processing', 'بحران آب'),
  ep('EP-RD', 'ready', 'تحریم‌ها'),
  ep('EP-FL', 'failed', 'روایت مهاجرت')
];

describe('LibraryTable status -> pipeline mapping', () => {
  it('renders one row per episode with the right stage label', () => {
    render(LibraryTable, { props: { episodes: fourStates, onOpen: vi.fn(), onRetry: vi.fn(), onRemove: vi.fn() } });
    const rows = screen.getAllByTestId('episode-row');
    expect(rows).toHaveLength(4);
    expect(screen.getByText('QUEUED')).toBeInTheDocument();
    expect(screen.getByText('INGEST…')).toBeInTheDocument();
    expect(screen.getByText('READY')).toBeInTheDocument();
    expect(screen.getByText('FAILED — INGEST')).toBeInTheDocument();
  });

  it('an uploaded row whose master never landed reads AWAITING UPLOAD, not QUEUED', () => {
    const abandoned: Episode = { ...ep('EP-AB', 'uploaded', 'رها شده'), hasMaster: false };
    render(LibraryTable, {
      props: { episodes: [abandoned], onOpen: vi.fn(), onRetry: vi.fn(), onRemove: vi.fn() }
    });
    expect(screen.getByText('AWAITING UPLOAD')).toBeInTheDocument();
    expect(screen.queryByText('QUEUED')).not.toBeInTheDocument();
    // It is not openable and exposes no RETRY (still a plain 'uploaded' row).
    const row = screen.getByTestId('episode-row');
    expect(row.getAttribute('role')).toBeNull();
    expect(within(row).queryByText('RETRY')).not.toBeInTheDocument();
    expect(within(row).queryByText('OPEN')).not.toBeInTheDocument();
  });

  it('only Ready rows are keyboard-openable; Failed rows expose RETRY', () => {
    render(LibraryTable, { props: { episodes: fourStates, onOpen: vi.fn(), onRetry: vi.fn(), onRemove: vi.fn() } });
    const rows = screen.getAllByTestId('episode-row');
    const byStatus = (s: string) => rows.find((r) => r.getAttribute('data-status') === s)!;

    expect(byStatus('ready').getAttribute('role')).toBe('button');
    expect(byStatus('ready').getAttribute('tabindex')).toBe('0');
    expect(byStatus('uploaded').getAttribute('role')).toBeNull();
    expect(byStatus('processing').getAttribute('tabindex')).toBeNull();

    expect(within(byStatus('ready')).getByText('OPEN')).toBeInTheDocument();
    expect(within(byStatus('failed')).getByText('RETRY')).toBeInTheDocument();
  });

  it('never renders the raw public id (no ID column until M1 episode codes)', () => {
    render(LibraryTable, {
      props: {
        episodes: [{ ...ep('EP-RD', 'ready', 'x'), id: 'ep_7k9zvisibleid', sourceFilename: 'master.mp4' }],
        onOpen: vi.fn(),
        onRetry: vi.fn(),
        onRemove: vi.fn()
      }
    });
    expect(screen.queryByText('ep_7k9zvisibleid')).not.toBeInTheDocument();
    expect(screen.queryByText('ID')).not.toBeInTheDocument();
  });

  it('CLIPS and COST render an em dash (no data until M1)', () => {
    render(LibraryTable, { props: { episodes: [ep('EP-RD', 'ready', 'x')], onOpen: vi.fn(), onRetry: vi.fn(), onRemove: vi.fn() } });
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(2);
  });
});

describe('LibraryTable interactions', () => {
  it('opens a Ready row on click, OPEN button, and Enter', async () => {
    const onOpen = vi.fn();
    render(LibraryTable, {
      props: { episodes: [ep('EP-RD', 'ready', 'x')], onOpen, onRetry: vi.fn(), onRemove: vi.fn() }
    });
    const user = userEvent.setup();
    const row = screen.getByTestId('episode-row');

    await user.click(screen.getByText('OPEN'));
    expect(onOpen).toHaveBeenCalledTimes(1);

    row.focus();
    await user.keyboard('{Enter}');
    expect(onOpen).toHaveBeenCalledTimes(2);
  });

  it('retries a Failed row', async () => {
    const onRetry = vi.fn();
    render(LibraryTable, {
      props: { episodes: [ep('EP-FL', 'failed', 'x')], onOpen: vi.fn(), onRetry, onRemove: vi.fn() }
    });
    await userEvent.setup().click(screen.getByText('RETRY'));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it('the pipeline-details trigger never interferes with row open', async () => {
    const onOpen = vi.fn();
    render(LibraryTable, {
      props: { episodes: [ep('EP-RD', 'ready', 'x')], onOpen, onRetry: vi.fn(), onRemove: vi.fn() }
    });
    const trigger = screen.getByTestId('pipeline-cell-trigger');

    // A MOUSE click on the pipeline cell bubbles to the row exactly as before
    // (the whole Ready row opens on click).
    await userEvent.setup().click(trigger);
    expect(onOpen).toHaveBeenCalledTimes(1);

    // A KEYBOARD activation of the focused trigger (a synthesized click with
    // detail 0) is stopped: focusing the cell shows details, it must not open
    // the episode underneath.
    trigger.focus();
    trigger.click(); // jsdom element.click() dispatches with detail 0
    expect(onOpen).toHaveBeenCalledTimes(1);
  });
});

describe('LibraryTable remove action', () => {
  it('every row exposes a labelled remove action that reports its episode', async () => {
    const onRemove = vi.fn();
    render(LibraryTable, {
      props: { episodes: fourStates, onOpen: vi.fn(), onRetry: vi.fn(), onRemove }
    });
    const removes = screen.getAllByTestId('episode-remove');
    expect(removes).toHaveLength(4);
    // Accessible name carries the episode title (the visible glyph is just ×).
    expect(
      screen.getByRole('button', { name: `Remove ${fourStates[0].title}` })
    ).toBeInTheDocument();

    await userEvent.setup().click(removes[0]);
    expect(onRemove).toHaveBeenCalledTimes(1);
    expect(onRemove).toHaveBeenCalledWith(fourStates[0]);
  });

  it('remove on a Ready row never opens the episode (click and keyboard)', async () => {
    const onOpen = vi.fn();
    const onRemove = vi.fn();
    render(LibraryTable, {
      props: { episodes: [ep('EP-RD', 'ready', 'x')], onOpen, onRetry: vi.fn(), onRemove }
    });
    const user = userEvent.setup();
    const remove = screen.getByTestId('episode-remove');

    await user.click(remove);
    expect(onRemove).toHaveBeenCalledTimes(1);
    expect(onOpen).not.toHaveBeenCalled();

    // Keyboard path: the button is reachable at rest (tab order) and Enter
    // activates remove without bubbling into the row's open handler.
    remove.focus();
    await user.keyboard('{Enter}');
    expect(onRemove).toHaveBeenCalledTimes(2);
    expect(onOpen).not.toHaveBeenCalled();
  });

  it('is rest-invisible (zero footprint) so committed baselines cannot shift', () => {
    render(LibraryTable, {
      props: { episodes: [ep('EP-RD', 'ready', 'x')], onOpen: vi.fn(), onRetry: vi.fn(), onRemove: vi.fn() }
    });
    const cls = screen.getByTestId('episode-remove').className;
    // Hidden and width-less at rest; revealed by row hover or its own focus.
    expect(cls).toContain('opacity-0');
    expect(cls).toContain('w-0');
    expect(cls).toContain('group-hover:opacity-100');
    expect(cls).toContain('focus-visible:opacity-100');
  });
});

describe('LibraryTable RTL + ZWNJ', () => {
  it('renders Persian titles dir=rtl inside a <bdi>, preserving ZWNJ verbatim', () => {
    const title = `گفت${ZWNJ}وگوی نمونه`;
    render(LibraryTable, { props: { episodes: [ep('EP-RD', 'ready', title)], onOpen: vi.fn(), onRetry: vi.fn(), onRemove: vi.fn() } });
    const cell = screen.getByTestId('episode-title');

    expect(cell.getAttribute('dir')).toBe('rtl');
    expect(cell.querySelector('bdi')).not.toBeNull();
    // Byte-exact: the rendered text still contains the ZWNJ and equals the input.
    expect(cell.textContent).toBe(title);
    expect(cell.textContent).toContain(ZWNJ);
    expect(cell.className).toContain('font-fa');
    expect(cell.className).toContain('text-left');
  });
});
