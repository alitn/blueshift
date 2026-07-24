import { test, expect } from '@playwright/test';
import {
  isolate,
  libraryRow,
  openSampleEpisode,
  SAMPLE,
  waitForFonts,
  TRANSCRIPT_FIXTURE
} from './helpers';

// The repo's committed visual baselines. Captured per viewport project
// (desktop-1440, laptop-1280) into tests/__screenshots__/<project>/. Updating
// them is an Architect-authorised act, never a side effect of a passing run.

test('Library — seeded sample (visual baseline)', async ({ page }) => {
  await page.goto('/');
  // Isolate the deterministic sample so the shot is stable regardless of any
  // rows an earlier flow spec uploaded into the shared demo database.
  await isolate(page, SAMPLE.search);
  // Scope READY to the sample row: a page-scoped getByText('READY') also matches
  // the always-rendered "READY n" filter chip (strict-mode multi-match).
  const sample = libraryRow(page, SAMPLE.title);
  await expect(sample).toBeVisible();
  await expect(sample.getByText('READY')).toBeVisible();
  await waitForFonts(page);
  await expect(page).toHaveScreenshot('library.png', { fullPage: true });
});

test('Episode — transcript (visual baseline)', async ({ page }) => {
  // Deterministic populated transcript via a test-only fixture stub (the demo
  // seed is ingest-only), and the proxy stubbed away so the shot is stable.
  // NOTE: the episode-{platform}.png baselines are NOT yet committed — their
  // creation is an Architect-authorised act; this test reports a missing
  // baseline until then.
  await openSampleEpisode(page, { transcript: TRANSCRIPT_FIXTURE, proxy: 'none' });
  await expect(page.getByTestId('transcript-summary')).toBeVisible();
  await waitForFonts(page);
  await expect(page).toHaveScreenshot('episode.png', { fullPage: true });
});

test('Login (visual baseline)', async ({ page }) => {
  // /login renders bare regardless of session, so the saved storageState is
  // harmless here.
  await page.goto('/login');
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
  await waitForFonts(page);
  await expect(page).toHaveScreenshot('login.png', { fullPage: true });
});
