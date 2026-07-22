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
    render(LibraryTable, { props: { episodes: fourStates, onOpen: vi.fn(), onRetry: vi.fn() } });
    const rows = screen.getAllByTestId('episode-row');
    expect(rows).toHaveLength(4);
    expect(screen.getByText('QUEUED')).toBeInTheDocument();
    expect(screen.getByText('INGEST…')).toBeInTheDocument();
    expect(screen.getByText('READY')).toBeInTheDocument();
    expect(screen.getByText('FAILED — INGEST')).toBeInTheDocument();
  });

  it('only Ready rows are keyboard-openable; Failed rows expose RETRY', () => {
    render(LibraryTable, { props: { episodes: fourStates, onOpen: vi.fn(), onRetry: vi.fn() } });
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
        onRetry: vi.fn()
      }
    });
    expect(screen.queryByText('ep_7k9zvisibleid')).not.toBeInTheDocument();
    expect(screen.queryByText('ID')).not.toBeInTheDocument();
  });

  it('CLIPS and COST render an em dash (no data until M1)', () => {
    render(LibraryTable, { props: { episodes: [ep('EP-RD', 'ready', 'x')], onOpen: vi.fn(), onRetry: vi.fn() } });
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(2);
  });
});

describe('LibraryTable interactions', () => {
  it('opens a Ready row on click, OPEN button, and Enter', async () => {
    const onOpen = vi.fn();
    render(LibraryTable, { props: { episodes: [ep('EP-RD', 'ready', 'x')], onOpen, onRetry: vi.fn() } });
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
    render(LibraryTable, { props: { episodes: [ep('EP-FL', 'failed', 'x')], onOpen: vi.fn(), onRetry } });
    await userEvent.setup().click(screen.getByText('RETRY'));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });
});

describe('LibraryTable RTL + ZWNJ', () => {
  it('renders Persian titles dir=rtl inside a <bdi>, preserving ZWNJ verbatim', () => {
    const title = `گفت${ZWNJ}وگوی نمونه`;
    render(LibraryTable, { props: { episodes: [ep('EP-RD', 'ready', title)], onOpen: vi.fn(), onRetry: vi.fn() } });
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
