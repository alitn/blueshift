import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { cssProp, openSampleEpisode, tokenRgb, waitForFonts, ZWNJ } from './helpers';

// The Moments rail (m1-moments-rail), against the demo stack's REAL seeded
// moments: the demo chain runs the fake four-stage pipeline (ingest →
// transcribe → diarize → moments), so the sample episode carries a
// deterministic two-moment ranked proposal set. Review flips hit the real
// status API and persist in the shared demo database, so every mutating test
// resets the sample's moments to 'proposed' before and after itself — the
// suite leaves the seeded state exactly as it found it.

type MomentDTO = { rank: number; start_ms: number; quote_fa: string; status: string };

function episodeIdOf(page: Page): string {
  const id = /\/episode\/([^/?#]+)/.exec(page.url())?.[1];
  expect(id, 'episode id in URL').toBeTruthy();
  return id!;
}

/** momentsOf reads the open episode's moments straight from the API. */
async function momentsOf(page: Page): Promise<MomentDTO[]> {
  const res = await page.request.get(`/api/episodes/${episodeIdOf(page)}/moments`);
  expect(res.ok()).toBe(true);
  return ((await res.json()) as { moments: MomentDTO[] }).moments;
}

/** resetMoments undoes any lingering verdicts (idempotent; 409s ignored). */
async function resetMoments(page: Page): Promise<void> {
  const id = episodeIdOf(page);
  for (const m of await momentsOf(page)) {
    if (m.status !== 'proposed') {
      await page.request.post(`/api/episodes/${id}/moments/${m.rank}/status`, {
        data: { status: 'proposed' }
      });
    }
  }
}

const cards = (page: Page) => page.getByTestId('moment-card');
const video = (page: Page) => page.getByTestId('proxy-video');

test('the seeded sample renders its ranked moments from real data, quotes byte-exact vs the API', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'none' });
  await resetMoments(page);
  await page.reload(); // render the reset state, not a stale pre-reset DOM

  const rail = page.getByTestId('moments-rail');
  await expect(rail).toBeVisible();
  await expect(page.getByTestId('moments-summary')).toHaveText('2 CANDIDATES · RANKED');

  await expect(cards(page)).toHaveCount(2);
  await expect(page.getByTestId('moment-rank').nth(0)).toHaveText('#1');
  await expect(page.getByTestId('moment-rank').nth(1)).toHaveText('#2');
  // mm:ss–mm:ss quote windows, mono-formatted.
  for (const range of await page.getByTestId('moment-range').all()) {
    expect(await range.textContent()).toMatch(/^\d{2}:\d{2}–\d{2}:\d{2}$/);
  }

  // Verbatim invariant: each rendered quote equals the API's quote_fa
  // byte-for-byte, and the rank-1 quote carries the seeded ZWNJ.
  const api = await momentsOf(page);
  expect(api).toHaveLength(2);
  for (let i = 0; i < api.length; i++) {
    const quote = page.getByTestId('moment-quote').nth(i);
    expect(await quote.textContent()).toBe(api[i].quote_fa);
    await expect(quote).toHaveAttribute('dir', 'rtl');
    expect(await quote.locator('bdi').count()).toBe(1);
  }
  expect(api[0].quote_fa).toContain(ZWNJ);
  expect(await page.getByTestId('moment-quote').nth(0).textContent()).toContain(ZWNJ);

  // Every seeded proposal starts reviewable: APPROVE buttons, no verdict chips.
  await expect(page.getByTestId('moment-approve')).toHaveCount(2);
  await expect(page.getByTestId('moment-status')).toHaveCount(0);
});

test('approve via the button persists, and UNDO reverses it to proposed', async ({ page }) => {
  await openSampleEpisode(page, { proxy: 'none' });
  await resetMoments(page);
  await page.reload();
  await expect(cards(page)).toHaveCount(2);

  const first = cards(page).first();
  await first.getByTestId('moment-approve').click();
  await expect(first).toHaveAttribute('data-status', 'approved');
  await expect(first.getByTestId('moment-status')).toHaveText('APPROVED');
  await expect(first.getByTestId('moment-approve')).toHaveCount(0);

  // Persisted: a fresh load still shows the verdict (not just optimistic state).
  await page.reload();
  await expect(cards(page).first()).toHaveAttribute('data-status', 'approved');

  // Undo: back to proposed, buttons return, and it persists too.
  await cards(page).first().getByTestId('moment-undo').click();
  await expect(cards(page).first()).toHaveAttribute('data-status', 'proposed');
  await page.reload();
  const reloaded = cards(page).first();
  await expect(reloaded).toHaveAttribute('data-status', 'proposed');
  await expect(reloaded.getByTestId('moment-approve')).toBeVisible();
});

test('single-key review: A approves and D dismisses the focused card; dismissed renders faint', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'none' });
  await resetMoments(page);
  await page.reload();
  await expect(cards(page)).toHaveCount(2);

  // A approves the focused card (SPEC-M1 single-key approve).
  const first = cards(page).first();
  await first.focus();
  await page.keyboard.press('a');
  await expect(first).toHaveAttribute('data-status', 'approved');
  await expect(first.getByTestId('moment-status')).toHaveText('APPROVED');

  // D dismisses the second card; its content sinks to the faint treatment.
  const second = cards(page).nth(1);
  await second.focus();
  await page.keyboard.press('d');
  await expect(second).toHaveAttribute('data-status', 'dismissed');
  await expect(second.getByTestId('moment-status')).toHaveText('DISMISSED');
  const faded = second.getByTestId('moment-rationale').locator('..');
  expect(Number(await cssProp(faded, 'opacity'))).toBeCloseTo(0.35, 2);

  // A on the approved card is an illegal transition — the UI refuses locally
  // and the API state stays approved.
  await first.focus();
  await page.keyboard.press('d');
  await expect(first).toHaveAttribute('data-status', 'approved');

  // Leave the seeded state as found.
  await resetMoments(page);
  await page.reload();
  await expect(page.getByTestId('moment-approve')).toHaveCount(2);
});

test('clicking a moment card seeks the player to the moment start (play state preserved)', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  await resetMoments(page);
  await expect(cards(page)).toHaveCount(2);
  const api = await momentsOf(page);

  // Paused at rest; the card click must move the playhead without starting
  // playback (the sync seek preserves play state).
  const isPaused = () => video(page).evaluate((el: HTMLVideoElement) => el.paused);
  const currentTime = () => video(page).evaluate((el: HTMLVideoElement) => el.currentTime);
  expect(await isPaused()).toBe(true);

  await cards(page).first().click();
  await expect
    .poll(currentTime, { message: 'playhead at the rank-1 moment start' })
    .toBeCloseTo(api[0].start_ms / 1000, 1);
  expect(await isPaused()).toBe(true);

  // The transcript highlight follows the new playhead: the rank-1 moment is
  // quote-aligned inside seeded segment idx 1, so that segment goes current.
  await expect(page.locator('[data-testid="transcript-segment"][aria-current]')).toHaveAttribute(
    'data-seg-idx',
    '1'
  );
});

test('an episode with no proposals shows the neutral awaiting state, not an error', async ({
  page
}) => {
  await openSampleEpisode(page, {
    moments: { episode_id: 'ep_awaiting', moments: [] },
    proxy: 'none'
  });
  await expect(page.getByTestId('moments-empty')).toBeVisible();
  await expect(page.getByTestId('moments-empty')).toContainText('AWAITING MOMENTS');
  await expect(page.getByTestId('moments-error')).toHaveCount(0);
});

test('rail, cards, and actions resolve to the design tokens', async ({ page }) => {
  await openSampleEpisode(page, { proxy: 'none' });
  await resetMoments(page);
  await page.reload();
  await expect(cards(page)).toHaveCount(2);

  // The rail is a bg-3 side panel (unlike the transcript's bg-2 canvas).
  const railBg = await cssProp(page.getByTestId('moments-rail'), 'background-color');
  expect(railBg).toBe(await tokenRgb(page, '--bg-3'));
  expect(railBg).not.toBe(await tokenRgb(page, '--bg-2'));

  // Cards sit raised on bg-4, never the panel surface.
  const cardBg = await cssProp(cards(page).first(), 'background-color');
  expect(cardBg).toBe(await tokenRgb(page, '--bg-4'));
  expect(cardBg).not.toBe(await tokenRgb(page, '--bg-3'));

  // APPROVE is the primary button: accent fill (not accent-bright) + on-accent text.
  const approve = page.getByTestId('moment-approve').first();
  const approveBg = await cssProp(approve, 'background-color');
  expect(approveBg).toBe(await tokenRgb(page, '--accent'));
  expect(approveBg).not.toBe(await tokenRgb(page, '--accent-bright'));
  expect(await cssProp(approve, 'color')).toBe(await tokenRgb(page, '--text-on-accent'));

  // The Persian quote renders in the fa content stack at the muted level; the
  // rank chip is mono data; the panel label is the UI stack.
  const quote = page.getByTestId('moment-quote').first();
  expect(await cssProp(quote, 'font-family')).toContain('Vazirmatn');
  expect(await cssProp(quote, 'color')).toBe(await tokenRgb(page, '--text-muted'));
  expect(await cssProp(page.getByTestId('moment-rank').first(), 'font-family')).toContain(
    'IBM Plex Mono'
  );
  expect(await cssProp(page.getByText('MOMENTS', { exact: true }), 'font-family')).toContain(
    'Archivo'
  );
});

test('the Episode view with the rail has no critical accessibility violations', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'none' });
  await resetMoments(page);
  await page.reload();
  await expect(cards(page)).toHaveCount(2);
  await waitForFonts(page);
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
  const critical = results.violations
    .filter((v) => v.impact === 'critical')
    .map((v) => `${v.id}: ${v.help}`);
  expect(critical).toEqual([]);
});
