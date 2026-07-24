import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import ReprocessEpisodeDialog from './ReprocessEpisodeDialog.svelte';
import type { Episode } from '$lib/episodes';

const ZWNJ = '‌';

const episode: Episode = {
  id: 'ep_target',
  title: `گفت${ZWNJ}وگوی نمونه`,
  sourceFilename: 'sample.mp4',
  language: 'fa',
  status: 'ready',
  hasMaster: true,
  uploadedAt: '2026-07-21T00:00:00Z'
};

// The reprocessor prop is the injection seam: the dialog's flow runs without a
// real fetch, and each test controls the outcome.
function setup(reprocessor: (id: string) => Promise<boolean>) {
  const onReprocessed = vi.fn();
  const utils = render(ReprocessEpisodeDialog, {
    props: { open: true, episode, onReprocessed, reprocessor }
  });
  return { onReprocessed, ...utils };
}

describe('ReprocessEpisodeDialog', () => {
  it('shows the episode (RTL title, ZWNJ verbatim) and neutral copy stating only missing steps run', () => {
    setup(vi.fn(async () => true));
    expect(screen.getByText('Reprocess episode')).toBeInTheDocument();
    // Neutral, non-destructive copy — only the missing steps run.
    expect(screen.getByText(/only the steps that haven't run yet will run/i)).toBeInTheDocument();
    const bdi = screen.getByText(episode.title);
    expect(bdi.textContent).toContain(ZWNJ);
    expect(bdi.closest('[dir="rtl"]')).not.toBeNull();
    expect(screen.getByText('sample.mp4')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
    expect(screen.getByTestId('reprocess-confirm')).toHaveTextContent('REPROCESS');
  });

  it('confirms: calls the reprocessor with the id and reports onReprocessed on success', async () => {
    const reprocessor = vi.fn(async () => true);
    const { onReprocessed } = setup(reprocessor);

    await userEvent.setup().click(screen.getByTestId('reprocess-confirm'));

    await waitFor(() => expect(reprocessor).toHaveBeenCalledWith('ep_target'));
    expect(onReprocessed).toHaveBeenCalledTimes(1);
    expect(onReprocessed).toHaveBeenCalledWith('ep_target');
  });

  it('failure keeps the dialog open with an alert and no onReprocessed', async () => {
    const { onReprocessed } = setup(vi.fn(async () => false));

    await userEvent.setup().click(screen.getByTestId('reprocess-confirm'));

    expect(await screen.findByRole('alert')).toHaveTextContent(/reprocess failed/i);
    expect(onReprocessed).not.toHaveBeenCalled();
    // Still open and re-tryable.
    expect(screen.getByTestId('reprocess-confirm')).toBeEnabled();
  });

  it('a rejected reprocessor (network error) degrades to the same alert', async () => {
    const { onReprocessed } = setup(vi.fn(async () => Promise.reject(new Error('net'))));

    await userEvent.setup().click(screen.getByTestId('reprocess-confirm'));

    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(onReprocessed).not.toHaveBeenCalled();
  });

  it('cancel never calls the reprocessor', async () => {
    const reprocessor = vi.fn(async () => true);
    const { onReprocessed } = setup(reprocessor);

    await userEvent.setup().click(screen.getByRole('button', { name: 'Cancel' }));

    expect(reprocessor).not.toHaveBeenCalled();
    expect(onReprocessed).not.toHaveBeenCalled();
  });
});
