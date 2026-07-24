import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ProxyPlayer from './ProxyPlayer.svelte';

// ProxyPlayer's sync surface (m1-transcript-sync): `ontime` playhead reports
// and the play-state-preserving `seekTo`. jsdom's media element has no real
// playback engine, so play state and time are mocked per-instance — exactly
// what the spec's "mocked video element" cases call for.

/** loader resolves a fixed proxy URL, driving the pane into its video state. */
const loader = (url: string | null) => () => Promise.resolve(url);

/**
 * mockVideo replaces the playback-engine surface of a jsdom <video>: a stored
 * currentTime, a fixed paused flag, and play/pause spies that MUST stay
 * uncalled through a seek (play-state preservation is structural: seekTo only
 * assigns currentTime).
 */
function mockVideo(video: HTMLVideoElement, { paused }: { paused: boolean }) {
  let time = 0;
  Object.defineProperty(video, 'currentTime', {
    configurable: true,
    get: () => time,
    set: (v: number) => {
      time = v;
    }
  });
  Object.defineProperty(video, 'paused', { configurable: true, get: () => paused });
  const play = vi.fn();
  const pause = vi.fn();
  Object.defineProperty(video, 'play', { configurable: true, value: play });
  Object.defineProperty(video, 'pause', { configurable: true, value: pause });
  return { play, pause };
}

async function renderPlayer(props: Record<string, unknown> = {}) {
  const utils = render(ProxyPlayer, {
    props: { episodeId: 'ep_x', loadUrl: loader('/proxy/signed.mp4'), ...props }
  });
  const video = (await screen.findByTestId('proxy-video')) as HTMLVideoElement;
  return { ...utils, video };
}

describe('ProxyPlayer states', () => {
  it('renders the <video> with the signed URL once it resolves', async () => {
    const { video } = await renderPlayer();
    expect(video).toBeInTheDocument();
    expect(video.getAttribute('src')).toBe('/proxy/signed.mp4');
  });

  it('shows the unavailable placeholder when no URL resolves', async () => {
    render(ProxyPlayer, { props: { episodeId: 'ep_x', loadUrl: loader(null) } });
    expect(await screen.findByTestId('proxy-placeholder')).toHaveTextContent('PROXY UNAVAILABLE');
  });
});

describe('ProxyPlayer ontime playhead reports', () => {
  it('reports ms on timeupdate (the ~4Hz native cadence)', async () => {
    const ontime = vi.fn();
    const { video } = await renderPlayer({ ontime });
    mockVideo(video, { paused: true });

    video.currentTime = 1.25;
    video.dispatchEvent(new Event('timeupdate'));
    expect(ontime).toHaveBeenLastCalledWith(1250);

    video.currentTime = 2.6;
    video.dispatchEvent(new Event('timeupdate'));
    expect(ontime).toHaveBeenLastCalledWith(2600);
  });

  it('reports on seeking and seeked too, so scrubbing tracks instantly', async () => {
    const ontime = vi.fn();
    const { video } = await renderPlayer({ ontime });
    mockVideo(video, { paused: true });

    video.currentTime = 3.1;
    video.dispatchEvent(new Event('seeking'));
    expect(ontime).toHaveBeenLastCalledWith(3100);

    video.currentTime = 3.2;
    video.dispatchEvent(new Event('seeked'));
    expect(ontime).toHaveBeenLastCalledWith(3200);
  });

  it('is silent (no throw) when no ontime callback is wired', async () => {
    const { video } = await renderPlayer();
    mockVideo(video, { paused: true });
    expect(() => video.dispatchEvent(new Event('timeupdate'))).not.toThrow();
  });
});

describe('ProxyPlayer seekTo — play-state preservation (the critical detail)', () => {
  it('PAUSED: seekTo moves the playhead and the video STAYS paused', async () => {
    const { component, video } = await renderPlayer();
    const spies = mockVideo(video, { paused: true });

    component.seekTo(2600);

    expect(video.currentTime).toBe(2.6);
    // Structurally preserved: the seek never touches the transport.
    expect(spies.play).not.toHaveBeenCalled();
    expect(spies.pause).not.toHaveBeenCalled();
    expect(video.paused).toBe(true);
  });

  it('PLAYING: seekTo moves the playhead and the video KEEPS playing', async () => {
    const { component, video } = await renderPlayer();
    const spies = mockVideo(video, { paused: false });

    component.seekTo(1200);

    expect(video.currentTime).toBe(1.2);
    expect(spies.pause).not.toHaveBeenCalled();
    expect(spies.play).not.toHaveBeenCalled();
    expect(video.paused).toBe(false);
  });

  it('clamps negative targets to 0', async () => {
    const { component, video } = await renderPlayer();
    mockVideo(video, { paused: true });
    component.seekTo(-500);
    expect(video.currentTime).toBe(0);
  });

  it('is a no-op before the proxy <video> exists', async () => {
    const { component } = render(ProxyPlayer, {
      props: { episodeId: 'ep_x', loadUrl: () => new Promise<string | null>(() => {}) }
    });
    expect(() => component.seekTo(1000)).not.toThrow();
  });
});
