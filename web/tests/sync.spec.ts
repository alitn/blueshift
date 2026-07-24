import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import {
  cssProp,
  openSampleEpisode,
  tokenRgb,
  TRANSCRIPT_FIXTURE,
  waitForFonts,
  ZWNJ
} from './helpers';

// Two-way player ↔ transcript sync (m1-transcript-sync), against the demo
// stack's REAL seeded transcript (fake engines, deterministic). The signed
// proxy is swapped for a local ~6s clip (proxy: 'fixture') because the seeded
// sample's real proxy is ~2s — playback could never reach the second segment.
//
// Timings are read from the transcript API at run time rather than hard-coded,
// so these specs survive reseeded fixtures as long as ≥2 segments exist.

type SegTiming = { start_ms: number; end_ms: number };

/** seededTimings fetches the open episode's segment timings via the API. */
async function seededTimings(page: Page): Promise<SegTiming[]> {
  const id = /\/episode\/([^/?#]+)/.exec(page.url())?.[1];
  expect(id, 'episode id in URL').toBeTruthy();
  const res = await page.request.get(`/api/episodes/${id}/transcript`);
  expect(res.ok()).toBe(true);
  const body = (await res.json()) as { segments: SegTiming[] };
  expect(body.segments.length).toBeGreaterThanOrEqual(2);
  return body.segments;
}

const video = (page: Page) => page.getByTestId('proxy-video');
const segments = (page: Page) => page.getByTestId('transcript-segment');
const active = (page: Page) => page.locator('[data-testid="transcript-segment"][aria-current]');

/** play starts muted playback (autoplay-safe in headless) at 1x. */
async function play(page: Page): Promise<void> {
  await video(page).evaluate((el: HTMLVideoElement) => {
    el.muted = true;
    return el.play();
  });
}

/** scrubTo sets currentTime directly — the "manual scrub" path. */
async function scrubTo(page: Page, seconds: number): Promise<void> {
  await video(page).evaluate((el: HTMLVideoElement, s) => {
    el.currentTime = s;
  }, seconds);
}

const currentTime = (page: Page) => video(page).evaluate((el: HTMLVideoElement) => el.currentTime);
const isPaused = (page: Page) => video(page).evaluate((el: HTMLVideoElement) => el.paused);

test('at rest (t=0, paused) the first seeded segment is current and highlighted', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  await expect(segments(page)).toHaveCount(2);

  // Seeded segment 0 starts at 0ms → current at t=0 per the mapping policy.
  await expect(segments(page).first()).toHaveAttribute('aria-current', 'true');
  await expect(active(page)).toHaveCount(1);
  expect(await isPaused(page)).toBe(true);
});

test('video → transcript: playback advances the highlight across ≥2 segments', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  const timings = await seededTimings(page);
  await expect(segments(page).first()).toHaveAttribute('aria-current', 'true');

  await play(page);

  // The highlight hands off to the second segment when playback crosses its
  // start (seeded: 2.6s) — segment 0 → segment 1 is the ≥2-segment advance.
  await expect(segments(page).nth(1)).toHaveAttribute('aria-current', 'true', {
    timeout: Math.ceil(timings[1].start_ms) + 15_000
  });
  await expect(active(page)).toHaveCount(1);
  expect(await currentTime(page)).toBeGreaterThanOrEqual(timings[1].start_ms / 1000);
});

test('video → transcript: a manual scrub lands on the correct segment (gap keeps the previous)', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  const timings = await seededTimings(page);
  const [s0, s1] = timings;

  // Scrub into the middle of segment 1 → segment 1 is current.
  await scrubTo(page, (s1.start_ms + s1.end_ms) / 2000);
  await expect(segments(page).nth(1)).toHaveAttribute('aria-current', 'true');
  await expect(active(page)).toHaveCount(1);

  // Scrub back into segment 0 → the highlight returns.
  await scrubTo(page, (s0.start_ms + s0.end_ms) / 2000);
  await expect(segments(page).first()).toHaveAttribute('aria-current', 'true');

  // Scrub into the silence gap between the turns (if the seed has one): the
  // PREVIOUS segment stays highlighted — no flicker, no dead zone.
  if (s1.start_ms > s0.end_ms) {
    await scrubTo(page, (s0.end_ms + s1.start_ms) / 2000);
    await expect(segments(page).first()).toHaveAttribute('aria-current', 'true');
    await expect(active(page)).toHaveCount(1);
  }

  // Scrubbing never disturbed the paused transport.
  expect(await isPaused(page)).toBe(true);
});

test('before the first segment nothing is highlighted', async ({ page }) => {
  // The fixture transcript's first segment starts at 12s, so the whole 6s
  // fixture clip sits in the "before first segment" zone.
  await openSampleEpisode(page, { transcript: TRANSCRIPT_FIXTURE, proxy: 'fixture' });
  await expect(segments(page)).toHaveCount(2);
  await expect(active(page)).toHaveCount(0);

  await scrubTo(page, 5);
  await expect(active(page)).toHaveCount(0);
});

test('transcript → video: clicking a later segment while PAUSED seeks and STAYS paused', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  const timings = await seededTimings(page);
  expect(await isPaused(page)).toBe(true);

  await segments(page).nth(1).click();

  // The playhead jumped to the segment start…
  await expect
    .poll(() => currentTime(page))
    .toBeCloseTo(timings[1].start_ms / 1000, 1);
  // …the clicked segment is highlighted…
  await expect(segments(page).nth(1)).toHaveAttribute('aria-current', 'true');
  // …and the transport is exactly as it was: paused, and it does not creep.
  expect(await isPaused(page)).toBe(true);
  await page.waitForTimeout(400);
  expect(await isPaused(page)).toBe(true);
  expect(await currentTime(page)).toBeCloseTo(timings[1].start_ms / 1000, 1);
});

test('transcript → video: clicking a segment while PLAYING keeps playing from there', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  const timings = await seededTimings(page);

  await play(page);
  await expect.poll(() => currentTime(page)).toBeGreaterThan(0.3);

  await segments(page).nth(1).click();

  // Still playing (never paused by the seek), from the segment start onwards.
  expect(await isPaused(page)).toBe(false);
  await expect.poll(() => currentTime(page)).toBeGreaterThanOrEqual(timings[1].start_ms / 1000);
  await expect(segments(page).nth(1)).toHaveAttribute('aria-current', 'true');

  // And the clock keeps advancing — playback truly continued.
  const t1 = await currentTime(page);
  await expect.poll(() => currentTime(page)).toBeGreaterThan(t1);
  expect(await isPaused(page)).toBe(false);
});

test('transcript → video: keyboard activation (Enter on a focused segment) seeks, stays paused', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  const timings = await seededTimings(page);

  await segments(page).nth(1).focus();
  await page.keyboard.press('Enter');

  await expect.poll(() => currentTime(page)).toBeCloseTo(timings[1].start_ms / 1000, 1);
  await expect(segments(page).nth(1)).toHaveAttribute('aria-current', 'true');
  expect(await isPaused(page)).toBe(true);
});

test('the active highlight resolves to the design tokens, RTL edge on the reading-start side', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  await waitForFonts(page);
  const activeSeg = segments(page).first();
  const idleSeg = segments(page).nth(1);
  await expect(activeSeg).toHaveAttribute('aria-current', 'true');

  // Highlight fill: accent-wash-14 (the DESIGN.md transcript highlight family),
  // not a sibling wash — the pair is sensitive to a corrupted token.
  const bg = await cssProp(activeSeg, 'background-color');
  expect(bg).toBe(await tokenRgb(page, '--accent-wash-14'));
  expect(bg).not.toBe(await tokenRgb(page, '--accent-wash-18'));

  // Accent edge on the reading-start side of the RTL block: the RIGHT border
  // carries 2px of --accent; the left stays transparent-width-0 territory.
  await expect(activeSeg).toHaveAttribute('dir', 'rtl');
  expect(await cssProp(activeSeg, 'border-right-width')).toBe('2px');
  expect(await cssProp(activeSeg, 'border-right-color')).toBe(await tokenRgb(page, '--accent'));
  expect(await cssProp(activeSeg, 'border-left-width')).toBe('0px');

  // A non-active segment carries neither the wash nor the edge.
  expect(await cssProp(idleSeg, 'background-color')).not.toBe(
    await tokenRgb(page, '--accent-wash-14')
  );

  // Subtle hover affordance from tokens on a non-active segment (polled: the
  // background eases in over the motion-hover transition).
  await idleSeg.hover();
  await expect.poll(() => cssProp(idleSeg, 'background-color')).toBe(
    await tokenRgb(page, '--hover-row')
  );
  expect(await cssProp(idleSeg, 'cursor')).toBe('pointer');
});

test('RTL and the verbatim ZWNJ survive on a highlighted segment', async ({ page }) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  const timings = await seededTimings(page);

  // Highlight the second seeded turn (its text carries the ZWNJ) by clicking it.
  await segments(page).nth(1).click();
  await expect.poll(() => currentTime(page)).toBeCloseTo(timings[1].start_ms / 1000, 1);
  const highlighted = segments(page).nth(1);
  await expect(highlighted).toHaveAttribute('aria-current', 'true');

  await expect(highlighted).toHaveAttribute('dir', 'rtl');
  expect(await highlighted.evaluate((el) => getComputedStyle(el).direction)).toBe('rtl');
  const body = highlighted.getByTestId('segment-text');
  expect(await body.locator('bdi').count()).toBe(1);
  expect(await body.textContent()).toContain(ZWNJ);
});

test('the Episode view with an active highlight has no critical accessibility violations', async ({
  page
}) => {
  // proxy: 'none' keeps the captionless test <video> out of the scan (as the
  // existing episode axe smoke does); segment 0 is still current at rest, so
  // the highlighted, interactive transcript is exactly what gets scanned.
  await openSampleEpisode(page, { proxy: 'none' });
  await expect(segments(page).first()).toHaveAttribute('aria-current', 'true');
  await waitForFonts(page);
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
  const critical = results.violations
    .filter((v) => v.impact === 'critical')
    .map((v) => `${v.id}: ${v.help}`);
  expect(critical).toEqual([]);
});
