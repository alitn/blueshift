import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { cssProp, isolate, libraryRow, tokenRgb, uploadFixture } from './helpers';

// Reprocess a finished (READY) episode, end to end against the real demo stack:
// a freshly uploaded row that reached READY through the full chain is re-entered
// via the Library's rest-invisible row action + accent confirm, the POST is
// accepted (200 — legal only from ready/failed), and it fast-forwards back to
// READY because every stage's output already exists (skip-if-output-exists — the
// cost-safety guarantee that makes reprocess safe & cheap). The spec uploads its
// own throwaway row; the seeded sample must survive for every other spec, so it
// is never reprocessed here.

test('reprocess a READY row — accent confirm, 200, fast-forwards back to READY', async ({
  page
}, testInfo) => {
  // Unique per project and per run (shared demo DB, two viewport projects).
  const nonce = `${testInfo.project.name}-${Date.now()}`;
  const title = `E2E Reprocess ${nonce}`;

  // Upload a tiny real master and let it reach READY via ingest → transcribe →
  // diarize → moments (all fake engines in the demo active chain).
  const dialog = page.getByRole('dialog');
  await page.goto('/');
  await page.getByRole('button', { name: 'UPLOAD MASTER' }).click();
  await page.locator('input[type="file"]').setInputFiles(uploadFixture);
  await page.locator('#upload-title').fill(title);
  await page.getByRole('button', { name: 'UPLOAD', exact: true }).click();
  await expect(dialog).toBeHidden();

  await isolate(page, nonce);
  const row = libraryRow(page, title);
  await expect(row).toBeVisible();
  await expect(row.getByText('READY')).toBeVisible({ timeout: 60_000 });

  // The reprocess action is rest-invisible (committed baselines stay untouched)
  // but keyboard-reachable at rest: focus + Enter opens the confirm.
  const reprocess = row.getByTestId('episode-reprocess');
  await reprocess.focus();
  await page.keyboard.press('Enter');
  await expect(dialog.getByText('Reprocess episode')).toBeVisible();
  await expect(dialog.getByText(title)).toBeVisible();
  // Neutral copy: only the missing steps run.
  await expect(dialog.getByText(/only the steps that haven't run yet will run/i)).toBeVisible();

  // Cancel first: nothing happens, the row stays READY.
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(row.getByText('READY')).toBeVisible();

  // Reopen by mouse: the action reveals on row hover.
  await row.hover();
  await reprocess.click();
  await expect(dialog.getByText('Reprocess episode')).toBeVisible();

  // The confirm is the ACCENT primary (a safe, non-destructive action) — never
  // the danger style Remove uses.
  const confirm = dialog.getByTestId('reprocess-confirm');
  const accentRgb = await tokenRgb(page, '--accent');
  expect(await cssProp(confirm, 'background-color')).toBe(accentRgb);
  expect(await cssProp(confirm, 'background-color')).not.toBe(await tokenRgb(page, '--danger'));

  // No new critical a11y violations with the confirm open.
  const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
  expect(
    axe.violations.filter((v) => v.impact === 'critical').map((v) => `${v.id}: ${v.help}`)
  ).toEqual([]);

  // Confirm: the server answers 200 (a legal ready→uploaded transition; it would
  // 409 otherwise) and the dialog closes.
  const [res] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === 'POST' && r.url().includes('/reprocess')
    ),
    confirm.click()
  ]);
  expect(res.status()).toBe(200);
  await expect(dialog).toBeHidden();

  // It re-enters the pipeline and fast-forwards back to READY: ingest re-runs,
  // transcribe/diarize/moments all skip (their outputs already exist), so the row
  // returns to READY without a duplicate row appearing.
  await expect(row.getByText('READY')).toBeVisible({ timeout: 60_000 });
  await expect(libraryRow(page, title)).toHaveCount(1);
});
