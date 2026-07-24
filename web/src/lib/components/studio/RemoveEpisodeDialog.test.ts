import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import RemoveEpisodeDialog from './RemoveEpisodeDialog.svelte';
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

// The remover prop is the injection seam: the dialog's flow is exercised
// without a real fetch, and each test controls the outcome.
function setup(remover: (id: string) => Promise<boolean>) {
  const onRemoved = vi.fn();
  const utils = render(RemoveEpisodeDialog, {
    props: { open: true, episode, onRemoved, remover }
  });
  return { onRemoved, ...utils };
}

describe('RemoveEpisodeDialog', () => {
  it('shows the episode (RTL title, ZWNJ verbatim) and a danger-confirm pair', () => {
    setup(vi.fn(async () => true));
    expect(screen.getByText('Remove episode')).toBeInTheDocument();
    const bdi = screen.getByText(episode.title);
    expect(bdi.textContent).toContain(ZWNJ);
    expect(bdi.closest('[dir="rtl"]')).not.toBeNull();
    expect(screen.getByText('sample.mp4')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument();
    expect(screen.getByTestId('remove-confirm')).toHaveTextContent('REMOVE');
  });

  it('confirms: calls the remover with the id and reports onRemoved on success', async () => {
    const remover = vi.fn(async () => true);
    const { onRemoved } = setup(remover);

    await userEvent.setup().click(screen.getByTestId('remove-confirm'));

    await waitFor(() => expect(remover).toHaveBeenCalledWith('ep_target'));
    expect(onRemoved).toHaveBeenCalledTimes(1);
    expect(onRemoved).toHaveBeenCalledWith('ep_target');
  });

  it('failure keeps the dialog open with an alert and no onRemoved', async () => {
    const { onRemoved } = setup(vi.fn(async () => false));

    await userEvent.setup().click(screen.getByTestId('remove-confirm'));

    expect(await screen.findByRole('alert')).toHaveTextContent(/remove failed/i);
    expect(onRemoved).not.toHaveBeenCalled();
    // Still open and re-tryable.
    expect(screen.getByTestId('remove-confirm')).toBeEnabled();
  });

  it('a rejected remover (network error) degrades to the same alert', async () => {
    const { onRemoved } = setup(vi.fn(async () => Promise.reject(new Error('net'))));

    await userEvent.setup().click(screen.getByTestId('remove-confirm'));

    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(onRemoved).not.toHaveBeenCalled();
  });

  it('cancel never calls the remover', async () => {
    const remover = vi.fn(async () => true);
    const { onRemoved } = setup(remover);

    await userEvent.setup().click(screen.getByRole('button', { name: 'Cancel' }));

    expect(remover).not.toHaveBeenCalled();
    expect(onRemoved).not.toHaveBeenCalled();
  });
});
