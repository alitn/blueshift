import { test, expect } from '@playwright/test';
import { login, libraryRow, isolate, SAMPLE, uploadFixture } from './helpers';

// AC5: the upload-to-Ready flow, driven end to end against the real demo stack
// (real API, real worker ingest, real proxy playback). Starts unauthenticated
// so the login UI itself is exercised here.
test.use({ storageState: { cookies: [], origins: [] } });

test('login → seeded sample READY → upload → READY → play proxy', async ({ page }, testInfo) => {
  await login(page);

  // The upload lands in the single shared demo DB, and both viewport projects
  // run this spec — so the title must be unique per project and per run, or a
  // later run/project sees ≥2 matching rows (strict-mode multi-match). This spec
  // takes no screenshots, so a non-deterministic title is safe.
  const nonce = `${testInfo.project.name}-${Date.now()}`;
  const uploadTitle = `E2E Upload ${nonce}`;

  // Keyboard: `U` opens the upload dialog (focus is on the body, not an input);
  // Escape dismisses it.
  const dialog = page.getByRole('dialog');
  await page.keyboard.press('u');
  await expect(dialog.getByText('Upload master')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();

  // The deterministic sample boots 'ready'. Isolate it and confirm READY.
  await isolate(page, SAMPLE.search);
  const sample = libraryRow(page, SAMPLE.title);
  await expect(sample).toBeVisible();
  await expect(sample.getByText('READY')).toBeVisible();

  // Upload a tiny, real master through the dialog.
  await page.getByRole('button', { name: 'UPLOAD MASTER' }).click();
  await page.locator('input[type="file"]').setInputFiles(uploadFixture);
  await page.locator('#upload-title').fill(uploadTitle);
  await page.getByRole('button', { name: 'UPLOAD', exact: true }).click();
  await expect(dialog).toBeHidden();

  // The new row advances via polling (no reload): non-terminal → READY. The
  // worker was triggered by the app (WORKER_TRIGGER=exec) on upload-complete.
  // Isolate by the unique nonce so exactly this run's row matches.
  await isolate(page, nonce);
  const uploaded = libraryRow(page, uploadTitle);
  await expect(uploaded).toBeVisible();
  await expect(uploaded.getByText('READY')).toBeVisible({ timeout: 60_000 });

  // Open the seeded sample's proxy via keyboard (focus the Ready row + Enter)
  // and assert a real proxy source is playing.
  await isolate(page, SAMPLE.search);
  await sample.focus();
  await page.keyboard.press('Enter');
  const video = page.getByTestId('proxy-video');
  await expect(video).toBeVisible();
  await expect(video).toHaveAttribute('src', /.+/);

  // Escape closes the player.
  await page.keyboard.press('Escape');
  await expect(video).toBeHidden();
});
