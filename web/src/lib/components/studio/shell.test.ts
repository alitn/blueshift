import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import ShellFixture from './ShellFixture.svelte';
import StatusBar from './StatusBar.svelte';
import TopBar from './TopBar.svelte';

describe('AppShell / TopBar', () => {
  it('renders the wordmark', () => {
    render(TopBar);
    expect(screen.getByText('BLUE SHIFT')).toBeInTheDocument();
    expect(screen.getByText('STUDIO')).toBeInTheDocument();
  });

  it('renders a default LIBRARY breadcrumb and IDLE render indicator', () => {
    render(TopBar);
    expect(screen.getByText('LIBRARY')).toBeInTheDocument();
    expect(screen.getByText('RENDER')).toBeInTheDocument();
    expect(screen.getByText('IDLE')).toBeInTheDocument();
  });

  it('renders a supplied breadcrumb slot and child content through the shell', () => {
    render(ShellFixture);
    expect(screen.getByText('Special Interviews')).toBeInTheDocument();
    expect(screen.getByTestId('shell-child')).toHaveTextContent('Main content region');
  });
});

describe('StatusBar', () => {
  it('renders neutral placeholder telemetry with dot separators', () => {
    render(StatusBar);
    expect(screen.getByText('QUEUE 0')).toBeInTheDocument();
    expect(screen.getByText('STORAGE —')).toBeInTheDocument();
    expect(screen.getByText('ENGINE — MS')).toBeInTheDocument();
    expect(screen.getByText('v0.0.0')).toBeInTheDocument();
  });

  it('renders supplied telemetry values', () => {
    render(StatusBar, { props: { queue: 2, storage: '1.2 / 4.0 TB', engine: '412' } });
    expect(screen.getByText('QUEUE 2')).toBeInTheDocument();
    expect(screen.getByText('STORAGE 1.2 / 4.0 TB')).toBeInTheDocument();
    expect(screen.getByText('ENGINE 412 MS')).toBeInTheDocument();
  });
});
