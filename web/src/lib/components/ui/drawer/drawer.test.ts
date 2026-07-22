import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import DrawerFixture from './DrawerFixture.svelte';

describe('Drawer (right slide-over on the dialog primitive)', () => {
  // bits-ui's scroll-lock sets pointer-events:none on <body> while a drawer is
  // open; reset it between tests so a prior open drawer can't block the next.
  afterEach(() => {
    document.body.style.pointerEvents = '';
  });

  it('is closed initially', () => {
    render(DrawerFixture);
    expect(screen.queryByText('Drawer body content')).not.toBeInTheDocument();
  });

  it('opens on trigger and traps focus inside the content', async () => {
    const user = userEvent.setup();
    render(DrawerFixture);

    await user.click(screen.getByRole('button', { name: 'Open renders' }));

    const dialog = await screen.findByRole('dialog');
    expect(screen.getByText('Drawer body content')).toBeInTheDocument();

    // Focus-trap smoke: focus lands inside the drawer content after opening.
    await waitFor(() => {
      expect(dialog.contains(document.activeElement)).toBe(true);
    });
  });

  it('closes on Escape', async () => {
    const user = userEvent.setup();
    render(DrawerFixture);

    await user.click(screen.getByRole('button', { name: 'Open renders' }));
    await screen.findByRole('dialog');

    await user.keyboard('{Escape}');
    await waitFor(() => {
      expect(screen.queryByText('Drawer body content')).not.toBeInTheDocument();
    });
  });
});
