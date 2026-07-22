import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const goto = vi.fn();
vi.mock('$app/navigation', () => ({ goto: (...args: unknown[]) => goto(...args) }));

import LoginPage from './+page.svelte';

function mockFetch(status: number, body: unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: status >= 200 && status < 300,
      status,
      json: async () => body
    } as Response)
  );
}

beforeEach(() => {
  goto.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('login page', () => {
  it('renders the wordmark and credential fields', () => {
    render(LoginPage);
    expect(screen.getByText('BLUE SHIFT')).toBeInTheDocument();
    expect(screen.getByLabelText('Email')).toBeInTheDocument();
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeInTheDocument();
  });

  it('redirects to / on a successful sign-in', async () => {
    mockFetch(200, {
      user: { email: 'dev-approver@blueshift.local', name: 'Dev Approver' },
      org: { name: 'Pilot' },
      role: 'approver'
    });
    const user = userEvent.setup();
    render(LoginPage);

    await user.type(screen.getByLabelText('Email'), 'dev-approver@blueshift.local');
    await user.type(screen.getByLabelText('Password'), 'blueshift-dev');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    expect(goto).toHaveBeenCalledWith('/');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('shows a neutral error and stays on the page when credentials are rejected', async () => {
    mockFetch(401, { error: 'auth_failed' });
    const user = userEvent.setup();
    render(LoginPage);

    await user.type(screen.getByLabelText('Email'), 'dev-approver@blueshift.local');
    await user.type(screen.getByLabelText('Password'), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Incorrect email or password.');
    expect(goto).not.toHaveBeenCalled();
  });
});
