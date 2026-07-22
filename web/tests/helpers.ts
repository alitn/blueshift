import { expect, type Page } from '@playwright/test';
import { fileURLToPath } from 'node:url';

// tests/.auth/ (gitignored) holds the saved session and the generated upload
// fixture. Shared here so the setup project and the flow spec agree on paths
// without either importing the other's test-registration module.
export const authDir = fileURLToPath(new URL('./.auth', import.meta.url));
export const storageStatePath = `${authDir}/approver.json`;
export const uploadFixture = `${authDir}/upload-sample.mp4`;

// Dev identities seeded by fixtures/dev-seed.sql; the dev password is the demo
// default (tools/demo/lib.sh). Neutral, non-personal, offline-only.
export const APPROVER = { email: 'dev-approver@blueshift.local', password: 'blueshift-dev' };

// The deterministic sample episode (cmd/demoseed). Its source filename doubles
// as a unique search term to isolate its row from any E2E-uploaded rows.
export const SAMPLE = {
  title: 'گفت‌وگوی نمونه', // Persian, contains U+200C ZWNJ
  sourceFilename: 'sample-interview.mp4',
  search: 'sample-interview'
};

// U+200C, the zero-width non-joiner. It must survive verbatim from ASR/seed to
// the rendered DOM (verbatim invariant).
export const ZWNJ = '‌';

/** login drives the real /login UI as the given user and lands on the Library. */
export async function login(page: Page, user = APPROVER): Promise<void> {
  await page.goto('/login');
  await page.getByLabel('Email').fill(user.email);
  await page.getByLabel('Password').fill(user.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL('**/');
  await expect(page.getByRole('button', { name: 'UPLOAD MASTER' })).toBeVisible();
}

/** libraryRow returns the row locator for an episode by its (Persian) title. */
export function libraryRow(page: Page, title: string) {
  return page.getByTestId('episode-row').filter({ has: page.getByText(title, { exact: true }) });
}

/** isolate types a search term so exactly the matching row(s) render. */
export async function isolate(page: Page, term: string): Promise<void> {
  await page.getByLabel('Search episodes').fill(term);
}

/** waitForFonts resolves once webfonts are ready, so screenshots are stable. */
export async function waitForFonts(page: Page): Promise<void> {
  await page.evaluate(() => document.fonts.ready);
}
