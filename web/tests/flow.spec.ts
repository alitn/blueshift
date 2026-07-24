import { test, expect } from '@playwright/test';
import { login, libraryRow, isolate, SAMPLE, uploadFixture } from './helpers';

// AC5: the upload-to-Ready flow, driven end to end against the real demo stack
// (real API, real worker ingest → transcribe, real proxy playback). Transcribe is
// now in the demo active chain (fake engine), so upload → READY traverses TWO
// stages and both the seeded sample and the uploaded episode carry a real
// transcript. Starts unauthenticated so the login UI itself is exercised here.
test.use({ storageState: { cookies: [], origins: [] } });

test('login → sample READY → upload → READY via ingest+transcribe → transcripts render', async ({
  page
}, testInfo) => {
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
  // worker was triggered by the app (WORKER_TRIGGER=exec) on upload-complete and
  // now traverses TWO stages — ingest, then by auto-advance transcribe (fake) —
  // before it reads READY. Isolate by the unique nonce so exactly this run's row
  // matches.
  await isolate(page, nonce);
  const uploaded = libraryRow(page, uploadTitle);
  await expect(uploaded).toBeVisible();
  await expect(uploaded.getByText('READY')).toBeVisible({ timeout: 60_000 });

  // PROVE the auto-advance fake path — the exact regression this task fixes: the
  // uploaded episode reached READY by auto-advancing ingest → transcribe, so its
  // transcript is populated by the fake engine, not just the explicitly-seeded
  // sample. Open it and confirm real segments rendered.
  await uploaded.focus();
  await page.keyboard.press('Enter');
  await page.waitForURL('**/episode/**');
  await expect(page.getByTestId('transcript-summary')).toBeVisible();
  await expect(page.getByTestId('transcript-segment')).toHaveCount(2);
  await page.getByRole('link', { name: 'LIBRARY' }).click();
  await page.waitForURL(/\/$/);

  // Open the seeded sample's Episode view via keyboard (focus the Ready row +
  // Enter) — the episode-open interaction now routes to /episode/{id}.
  await isolate(page, SAMPLE.search);
  await sample.focus();
  await page.keyboard.press('Enter');
  await page.waitForURL('**/episode/**');

  // The proxy plays a real signed source beside the transcript pane. The seed now
  // runs the fake transcribe stage too, so the sample renders a real (offline
  // fixture) transcript rather than the neutral awaiting state.
  const video = page.getByTestId('proxy-video');
  await expect(video).toBeVisible();
  await expect(video).toHaveAttribute('src', /.+/);
  await expect(page.getByTestId('transcript-empty')).toHaveCount(0);
  await expect(page.getByTestId('transcript-segment')).toHaveCount(2);

  // The top-bar breadcrumb routes back to the Library by keyboard (a real link).
  await page.getByRole('link', { name: 'LIBRARY' }).click();
  await page.waitForURL(/\/$/);
  await expect(page.getByRole('button', { name: 'UPLOAD MASTER' })).toBeVisible();
});
