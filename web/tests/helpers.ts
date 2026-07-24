import { expect, type Page, type Locator } from '@playwright/test';
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

// A test-only fixture transcript for specs that need exact content control
// (known summary text, and the mixed diarized + un-diarized shape the real
// three-stage seed can no longer produce — every seeded segment now carries a
// speaker key). It is stubbed at the network boundary and never enters the
// demo/product data; specs asserting the real three-stage seed use the live
// transcript instead. Persian, RTL, with a verbatim ZWNJ (U+200C) and one
// diarized + one un-diarized turn.
// Word count = 5 + 4 = 9 (words are the verbatim source of truth for the count).
export const TRANSCRIPT_FIXTURE = {
  episode_id: 'ep_fixturetranscript',
  language: 'fa',
  segments: [
    {
      idx: 0,
      start_ms: 12000,
      end_ms: 16400,
      text: 'سلام، به برنامه‌ی ما خوش‌آمدید', // ZWNJ in برنامه‌ی and خوش‌آمدید
      speaker_key: 'S1',
      words: [
        ['سلام،', 12000, 12600, 0.99],
        ['به', 12650, 12900, 0.99],
        ['برنامه‌ی', 12950, 13700, 0.98],
        ['ما', 13750, 14000, 0.99],
        ['خوش‌آمدید', 14050, 16400, 0.97]
      ]
    },
    {
      idx: 1,
      start_ms: 47000,
      end_ms: 51000,
      text: 'ممنون از دعوت شما.',
      speaker_key: null,
      words: [
        ['ممنون', 47000, 47600, 0.99],
        ['از', 47650, 47850, 0.99],
        ['دعوت', 47900, 48400, 0.98],
        ['شما.', 48450, 51000, 0.97]
      ]
    }
  ]
};

/** The header summary the fixture renders: language label + total word count. */
export const TRANSCRIPT_FIXTURE_SUMMARY = 'FA · 9 WORDS';

type OpenEpisodeOpts = {
  /** Stub GET .../transcript with this JSON body (a fixture transcript). */
  transcript?: unknown;
  /** Or stub .../transcript with this status only (drives the error state). */
  transcriptStatus?: number;
  /** 'none' stubs .../proxy 404 so no <video> renders (deterministic shots,
   *  and no video-caption noise for the axe smoke). Default leaves it real. */
  proxy?: 'none';
};

/**
 * openSampleEpisode opens the deterministic demo sample's Episode view by the
 * keyboard path (focus the Ready row + Enter), optionally stubbing the transcript
 * and/or proxy endpoints first. Everything else hits the real demo stack.
 */
export async function openSampleEpisode(page: Page, opts: OpenEpisodeOpts = {}): Promise<void> {
  if (opts.transcript !== undefined) {
    await page.route('**/api/episodes/*/transcript', (route) =>
      route.fulfill({ json: opts.transcript })
    );
  } else if (opts.transcriptStatus !== undefined) {
    await page.route('**/api/episodes/*/transcript', (route) =>
      route.fulfill({ status: opts.transcriptStatus, contentType: 'application/json', body: '{}' })
    );
  }
  if (opts.proxy === 'none') {
    await page.route('**/api/episodes/*/proxy', (route) =>
      route.fulfill({ status: 404, contentType: 'application/json', body: '{}' })
    );
  }
  await page.goto('/');
  await isolate(page, SAMPLE.search);
  const sample = libraryRow(page, SAMPLE.title);
  await expect(sample).toBeVisible();
  await sample.focus();
  await page.keyboard.press('Enter');
  await page.waitForURL('**/episode/**');
}

/** tokenRgb resolves a CSS custom property to the browser's normalised rgb(a)
 *  form via a throwaway probe, so a hex token compares equal to a computed rgb
 *  value without any literal colour appearing in a spec. */
export function tokenRgb(page: Page, name: string): Promise<string> {
  return page.evaluate((n) => {
    const value = getComputedStyle(document.documentElement).getPropertyValue(n).trim();
    const probe = document.createElement('span');
    probe.style.color = value;
    document.body.appendChild(probe);
    const rgb = getComputedStyle(probe).color;
    probe.remove();
    return rgb;
  }, name);
}

/** cssProp reads one computed style property off a located element. */
export function cssProp(el: Locator, prop: string): Promise<string> {
  return el.evaluate((node, p) => getComputedStyle(node).getPropertyValue(p), prop);
}
