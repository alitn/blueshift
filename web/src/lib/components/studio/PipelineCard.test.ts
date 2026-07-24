import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import PipelineCard from './PipelineCard.svelte';
import type { PipelineDetails } from '$lib/pipelineDetails';

// A finished four-stage run plus an unreached render — the seeded-sample shape.
const DETAILS: PipelineDetails = {
  stages: [
    { name: 'ingest', status: 'done', durationMs: 1500, engine: 'bs-media-1' },
    { name: 'transcribe', status: 'done', durationMs: 102000, engine: 'bs-asr-2', costCents: 4 },
    { name: 'diarize', status: 'active', engine: 'bs-lm-1' },
    { name: 'moments', status: 'unreached' }
  ],
  queuedMs: 2000,
  totalMs: 103500
};

describe('PipelineCard', () => {
  it('renders five rows with display names, dots, durations, engines, and cost', () => {
    render(PipelineCard, { props: { details: DETAILS } });
    const rows = screen.getAllByTestId('pipeline-card-row');
    expect(rows).toHaveLength(5);
    expect(rows.map((r) => r.getAttribute('data-stage'))).toEqual([
      'ingest',
      'transcribe',
      'diarize',
      'moments',
      'render'
    ]);
    // Display names: product terms, SPEAKERS for diarize.
    expect(screen.getByText('INGEST')).toBeInTheDocument();
    expect(screen.getByText('TRANSCRIBE')).toBeInTheDocument();
    expect(screen.getByText('SPEAKERS')).toBeInTheDocument();
    expect(screen.getByText('MOMENTS')).toBeInTheDocument();
    expect(screen.getByText('RENDER')).toBeInTheDocument();
    // Statuses drive the dots (token-class mapping asserted via data-status).
    expect(rows[0].getAttribute('data-status')).toBe('done');
    expect(rows[2].getAttribute('data-status')).toBe('active');
    expect(rows[3].getAttribute('data-status')).toBe('unreached');
    // Durations, right-column mono values.
    expect(screen.getByText('1M 42S')).toBeInTheDocument();
    // Engine labels render in the faint card form.
    expect(screen.getByText('BS·ASR 2')).toBeInTheDocument();
    expect(screen.getByText('BS·MEDIA 1')).toBeInTheDocument();
    // Per-stage cost when known.
    expect(screen.getByText('$0.04')).toBeInTheDocument();
    // The active (in-flight) stage shows no duration.
    expect(rows[2].querySelector('[data-testid="pipeline-card-duration"]')).toBeNull();
  });

  it('renders the QUEUED and TOTAL footer with the total cost when known', () => {
    render(PipelineCard, { props: { details: DETAILS } });
    expect(screen.getByText('QUEUED')).toBeInTheDocument();
    expect(screen.getByTestId('pipeline-card-queued')).toHaveTextContent('2S');
    expect(screen.getByTestId('pipeline-card-total')).toHaveTextContent('1M 43S');
    // Only transcribe carries a cost -> total cost $0.04.
    expect(screen.getByTestId('pipeline-card-total')).toHaveTextContent('$0.04');
  });

  it('degrades gracefully for a legacy episode: statuses only, em-dash footer', () => {
    render(PipelineCard, {
      props: {
        details: {
          stages: [
            { name: 'ingest', status: 'done' },
            { name: 'transcribe', status: 'done' },
            { name: 'diarize', status: 'done' },
            { name: 'moments', status: 'done' }
          ]
        }
      }
    });
    expect(screen.queryAllByTestId('pipeline-card-duration')).toHaveLength(0);
    expect(screen.queryAllByTestId('pipeline-card-engine')).toHaveLength(0);
    expect(screen.getByTestId('pipeline-card-queued')).toHaveTextContent('—');
    expect(screen.getByTestId('pipeline-card-total')).toHaveTextContent('—');
  });

  it('shows skeleton lines while loading and the neutral copy on error', () => {
    const { unmount } = render(PipelineCard, { props: { loading: true } });
    expect(screen.getByTestId('pipeline-card-loading')).toBeInTheDocument();
    expect(screen.queryAllByTestId('pipeline-card-row')).toHaveLength(0);
    unmount();

    render(PipelineCard, { props: { error: true } });
    expect(screen.getByTestId('pipeline-card-error')).toHaveTextContent('DETAILS UNAVAILABLE');
    expect(screen.queryAllByTestId('pipeline-card-row')).toHaveLength(0);
  });
});
