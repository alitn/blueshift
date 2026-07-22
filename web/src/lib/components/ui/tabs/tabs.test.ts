import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import TabsFixture from './TabsFixture.svelte';

describe('Tabs (vendored, bits-ui)', () => {
  it('shows the initial tab panel and marks its trigger active', () => {
    render(TabsFixture);
    expect(screen.getByText('Reels panel')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'REELS' })).toHaveAttribute('data-state', 'active');
    expect(screen.getByRole('tab', { name: 'TELEGRAM' })).toHaveAttribute('data-state', 'inactive');
  });

  it('switches tabs with the keyboard (ArrowRight)', async () => {
    const user = userEvent.setup();
    render(TabsFixture);

    const reels = screen.getByRole('tab', { name: 'REELS' });
    reels.focus();
    await user.keyboard('{ArrowRight}');

    const telegram = screen.getByRole('tab', { name: 'TELEGRAM' });
    expect(telegram).toHaveAttribute('data-state', 'active');
    expect(screen.getByText('Telegram panel')).toBeInTheDocument();
  });
});
