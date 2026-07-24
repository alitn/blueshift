import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import {
  openSampleEpisode,
  cssProp,
  tokenRgb,
  waitForFonts,
  ZWNJ,
  TRANSCRIPT_FIXTURE,
  TRANSCRIPT_FIXTURE_SUMMARY
} from './helpers';

// The Episode view (prototype screen 01, transcript slice): opened from the
// Library by the keyboard path, it renders the proxy player beside the RTL
// Persian transcript. The demo seed now runs the four-stage chain (fake ASR +
// fake LLM diarize + fake LLM moments), so the sample carries a REAL
// deterministic transcript with speaker keys (and ranked moments in the DB —
// the rail renders them in its own task); the fixture stub is used only where
// a spec needs exact content control (a single/mixed chip state, known summary).

test('opens from the Library by keyboard and renders the transcript verbatim', async ({ page }) => {
  await openSampleEpisode(page, { transcript: TRANSCRIPT_FIXTURE });

  // Neutral header summary: language label + total word count (no provider text).
  await expect(page.getByTestId('transcript-summary')).toHaveText(TRANSCRIPT_FIXTURE_SUMMARY);

  // One block per segment, in order.
  const segments = page.getByTestId('transcript-segment');
  await expect(segments).toHaveCount(2);

  // First turn: LTR metadata row (timecode + speaker chip) above the RTL body.
  const first = segments.first();
  await expect(first).toHaveAttribute('dir', 'rtl');
  expect(await first.evaluate((el) => getComputedStyle(el).direction)).toBe('rtl');
  await expect(first.getByTestId('segment-timecode')).toHaveText('00:12');

  // Speaker chip renders only for the diarized (speaker_key) turn, raw label.
  await expect(page.getByTestId('speaker-chip')).toHaveCount(1);
  await expect(page.getByTestId('speaker-chip')).toHaveText('S1');

  // Verbatim: the ZWNJ (U+200C) survives byte-exact into the rendered DOM.
  const firstBody = page.getByTestId('segment-text').first();
  const bodyText = await firstBody.textContent();
  expect(bodyText).toContain(ZWNJ);
  expect(await firstBody.locator('bdi').count()).toBe(1);
});

test('the seeded sample renders its transcribed segments from real data', async ({ page }) => {
  // No transcript stub: the demo seed drives the full fake four-stage chain
  // (ingest → transcribe → diarize → moments), so the real sample carries a
  // populated (offline fixture) transcript AND deterministic speaker keys.
  // Proxy stubbed away for a video-free shot.
  await openSampleEpisode(page, { proxy: 'none' });
  await expect(page.getByTestId('transcript-summary')).toBeVisible();
  await expect(page.getByTestId('transcript-segment')).toHaveCount(2);
  await expect(page.getByTestId('transcript-empty')).toHaveCount(0);
  await expect(page.getByTestId('transcript-error')).toHaveCount(0);

  // Speaker chips are live: the seeded sample is diarized by the deterministic
  // fake grouping — one chip per turn, two distinct episode-local keys (host S1,
  // guest S2), raw labels only.
  const chips = page.getByTestId('speaker-chip');
  await expect(chips).toHaveCount(2);
  await expect(chips.nth(0)).toHaveText('S1');
  await expect(chips.nth(1)).toHaveText('S2');
});

test('an episode with no segments shows the neutral awaiting state, not an error', async ({
  page
}) => {
  // The awaiting state (e.g. an episode still mid-processing) — every ready demo
  // episode is now transcribed, so it is exercised with a stubbed empty transcript.
  await openSampleEpisode(page, {
    transcript: { episode_id: 'ep_awaiting', language: 'fa', segments: [] },
    proxy: 'none'
  });
  await expect(page.getByTestId('transcript-empty')).toBeVisible();
  await expect(page.getByTestId('transcript-error')).toHaveCount(0);
});

test('transcript body and header resolve to the design tokens', async ({ page }) => {
  await openSampleEpisode(page, { transcript: TRANSCRIPT_FIXTURE });

  // Persian body: the font-fa (Vazirmatn) stack in the text-body colour token.
  const body = page.getByTestId('segment-text').first();
  expect(await cssProp(body, 'font-family')).toContain('Vazirmatn');
  const bodyColor = await cssProp(body, 'color');
  expect(bodyColor).toBe(await tokenRgb(page, '--text-body'));
  expect(bodyColor).not.toBe(await tokenRgb(page, '--text-primary'));

  // Panel header label: the UI (Archivo) stack in the muted colour token.
  const label = page.getByText('TRANSCRIPT', { exact: true });
  expect(await cssProp(label, 'font-family')).toContain('Archivo');
  expect(await cssProp(label, 'color')).toBe(await tokenRgb(page, '--text-muted'));

  // The transcript is main content, so the panel sits on the bg-2 canvas (screen
  // 01 sets no background there) — never the bg-3 side-panel surface.
  const bg = await cssProp(page.getByTestId('transcript-pane'), 'background-color');
  expect(bg).toBe(await tokenRgb(page, '--bg-2'));
  expect(bg).not.toBe(await tokenRgb(page, '--bg-3'));
});

test('the Episode view has no critical accessibility violations', async ({ page }) => {
  // Stub the proxy 404 so no captionless <video> is present — the transcript is
  // the changed surface under test here.
  await openSampleEpisode(page, { transcript: TRANSCRIPT_FIXTURE, proxy: 'none' });
  await waitForFonts(page);
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
  const critical = results.violations
    .filter((v) => v.impact === 'critical')
    .map((v) => `${v.id}: ${v.help}`);
  expect(critical).toEqual([]);
});
