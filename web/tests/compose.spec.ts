import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { fileURLToPath } from 'node:url';
import { mkdirSync } from 'node:fs';
import {
  cssProp,
  isolate,
  libraryRow,
  openSampleEpisode,
  tokenRgb,
  uploadFixture,
  waitForFonts,
  ZWNJ
} from './helpers';

// Free-prompt moment composition (m1-prompt-moments), against the demo
// stack's REAL compose endpoint: the app server replays the committed
// deterministic compose recording through the real engine seam (validation,
// word-accurate derivation, llm_calls audit), so the results below are real
// API output, not stubs. The recording proposes exactly ONE result over the
// sample transcript — deliberately below the pipeline stage's minimum window,
// proving the compose path has no min-count clamp end to end.
//
// The quote the committed recording selects: segment 1's tail, ZWNJ included.
const COMPOSED_QUOTE = `خوش${ZWNJ}حالم که اینجا هستم`;

// Screenshot evidence for the Reviewer (design/ comparison), captured from the
// live flow into the task's evidence directory.
const shotsDir = fileURLToPath(new URL('../../.artifacts/screens/m1-prompt-moments/', import.meta.url));

async function evidence(page: Page, name: string, testInfo: { project: { name: string } }) {
  mkdirSync(shotsDir, { recursive: true });
  await page.screenshot({ path: `${shotsDir}${name}-${testInfo.project.name}.png` });
}

const composeInput = (page: Page) => page.getByTestId('compose-input');
const composedCards = (page: Page) => page.getByTestId('composed-card');

/** momentsCount reads the episode's PERSISTED moment count from the API. */
async function momentsCount(page: Page): Promise<number> {
  const id = /\/episode\/([^/?#]+)/.exec(page.url())?.[1];
  expect(id, 'episode id in URL').toBeTruthy();
  const res = await page.request.get(`/api/episodes/${id}/moments`);
  expect(res.ok()).toBe(true);
  return ((await res.json()) as { moments: unknown[] }).moments.length;
}

test('compose renders real PROMPT RESULTS (verbatim, word-accurate, tokens); seek works; discard drops; nothing persists', async ({
  page
}, testInfo) => {
  await openSampleEpisode(page, { proxy: 'fixture' });
  const before = await momentsCount(page);

  // Submit by keyboard: type the prompt, Enter submits the form.
  await composeInput(page).fill('پیدا کردن لحظه‌ی شادی مهمان');
  await composeInput(page).press('Enter');

  // The results group renders the recording's single result.
  await expect(page.getByTestId('compose-results')).toBeVisible();
  await expect(page.getByTestId('compose-summary')).toHaveText('1 MATCH');
  await expect(composedCards(page)).toHaveCount(1);
  const card = composedCards(page).first();
  await expect(card.getByTestId('composed-rank')).toHaveText('#1');
  expect(await card.getByTestId('composed-range').textContent()).toMatch(/^\d{2}:\d{2}–\d{2}:\d{2}$/);

  // Verbatim invariant: the rendered quote is the recording's quote
  // byte-for-byte — ZWNJ included — RTL in a <bdi>.
  const quote = card.getByTestId('composed-quote');
  expect(await quote.textContent()).toBe(COMPOSED_QUOTE);
  expect(await quote.textContent()).toContain(ZWNJ);
  await expect(quote).toHaveAttribute('dir', 'rtl');
  expect(await quote.locator('bdi').count()).toBe(1);

  // Token conformance FIRST, while the card is unfocused (focus applies the
  // accent wash + a color transition, which would race a computed-style read).
  const cardBg = await cssProp(card, 'background-color');
  expect(cardBg).toBe(await tokenRgb(page, '--bg-4'));
  const keepBtn = card.getByTestId('composed-keep');
  expect(await cssProp(keepBtn, 'background-color')).toBe(await tokenRgb(page, '--accent'));
  expect(await cssProp(keepBtn, 'color')).toBe(await tokenRgb(page, '--text-on-accent'));
  expect(await cssProp(quote, 'font-family')).toContain('Vazirmatn');
  expect(await cssProp(quote, 'color')).toBe(await tokenRgb(page, '--text-muted'));
  expect(
    await cssProp(page.getByText('PROMPT RESULTS', { exact: true }), 'font-family')
  ).toContain('Archivo');

  // Word-accurate bounds: the API derived the window from the quote's first
  // word (2.96s), inside segment 1 (2.6s..4.6s) — activating the card seeks
  // the player there, play state preserved.
  const video = page.getByTestId('proxy-video');
  const isPaused = () => video.evaluate((el: HTMLVideoElement) => el.paused);
  const currentTime = () => video.evaluate((el: HTMLVideoElement) => el.currentTime);
  expect(await isPaused()).toBe(true);
  await card.focus();
  await page.keyboard.press('Enter'); // keyboard path: Enter on the focused card seeks
  await expect
    .poll(currentTime, { message: 'playhead at the composed word-accurate start' })
    .toBeCloseTo(2.96, 1);
  expect(await isPaused()).toBe(true);

  // Accessibility smoke with the compose surface open.
  await waitForFonts(page);
  const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
  const critical = results.violations
    .filter((v) => v.impact === 'critical')
    .map((v) => `${v.id}: ${v.help}`);
  expect(critical).toEqual([]);

  await evidence(page, 'compose-results', testInfo);

  // DISCARD drops the result from view; the group goes with the last card.
  await card.getByTestId('composed-discard').click();
  await expect(page.getByTestId('compose-results')).toHaveCount(0);

  // Ephemeral: nothing was persisted by compose or discard.
  expect(await momentsCount(page)).toBe(before);
});

test('an empty compose result shows the neutral no-matches line, not an error', async ({
  page
}) => {
  await openSampleEpisode(page, { proxy: 'none' });
  // Stub only this endpoint (client-side): the empty set is a VALID answer the
  // UI must render neutrally. The glob does not touch the other moment routes.
  await page.route('**/api/episodes/*/moments/compose', (route) =>
    route.fulfill({ json: { episode_id: 'ep_stub', moments: [] } })
  );

  await composeInput(page).fill('چیزی که در این قسمت نیست');
  await composeInput(page).press('Enter');

  await expect(page.getByTestId('compose-empty')).toBeVisible();
  await expect(page.getByTestId('compose-empty')).toContainText('No matches');
  await expect(page.getByTestId('compose-error')).toHaveCount(0);
  await expect(page.getByTestId('compose-results')).toHaveCount(0);
});

test('KEEP persists a composed result: it joins the rail approved at the next rank and survives reload', async ({
  page
}, testInfo) => {
  // A fresh episode via the real upload → four fake stages, so the keep below
  // never mutates the shared seeded sample (other specs assert its 2-moment
  // state). Unique title per project+run, like the flow spec.
  const nonce = `${testInfo.project.name}-compose-${Date.now()}`;
  const uploadTitle = `E2E Compose ${nonce}`;
  await page.goto('/');
  await page.getByRole('button', { name: 'UPLOAD MASTER' }).click();
  await page.locator('input[type="file"]').setInputFiles(uploadFixture);
  await page.locator('#upload-title').fill(uploadTitle);
  await page.getByRole('button', { name: 'UPLOAD', exact: true }).click();
  await isolate(page, nonce);
  const row = libraryRow(page, uploadTitle);
  await expect(row).toBeVisible();
  await expect(row.getByText('READY')).toBeVisible({ timeout: 60_000 });
  await row.focus();
  await page.keyboard.press('Enter');
  await page.waitForURL('**/episode/**');

  // The fake pipeline proposed its 2 ranked moments; compose finds 1 more.
  await expect(page.getByTestId('moment-card')).toHaveCount(2);
  await composeInput(page).fill('پیدا کردن لحظه‌ی شادی مهمان');
  await composeInput(page).press('Enter');
  await expect(composedCards(page)).toHaveCount(1);

  // KEEP = approve-to-persist: the card leaves the group and joins the rail
  // as an APPROVED moment at the next free rank (#3).
  await composedCards(page).first().getByTestId('composed-keep').click();
  await expect(page.getByTestId('compose-results')).toHaveCount(0);
  const cards = page.getByTestId('moment-card');
  await expect(cards).toHaveCount(3);
  const kept = cards.nth(2);
  await expect(kept.getByTestId('moment-rank')).toHaveText('#3');
  await expect(kept).toHaveAttribute('data-status', 'approved');
  await expect(kept.getByTestId('moment-status')).toHaveText('APPROVED');
  expect(await kept.getByTestId('moment-quote').textContent()).toBe(COMPOSED_QUOTE);

  await evidence(page, 'compose-kept', testInfo);

  // Persisted: a fresh load renders it from the database, still approved.
  await page.reload();
  await expect(page.getByTestId('moment-card')).toHaveCount(3);
  const reloaded = page.getByTestId('moment-card').nth(2);
  await expect(reloaded.getByTestId('moment-rank')).toHaveText('#3');
  await expect(reloaded).toHaveAttribute('data-status', 'approved');

  // …and behaves like any moment: UNDO flips it back to proposed via the
  // normal review state machine, then re-approve restores it.
  await reloaded.getByTestId('moment-undo').click();
  await expect(reloaded).toHaveAttribute('data-status', 'proposed');
  await reloaded.getByTestId('moment-approve').click();
  await expect(reloaded).toHaveAttribute('data-status', 'approved');
});
