import { defineConfig, devices } from '@playwright/test';
import { fileURLToPath } from 'node:url';

// The demo stack is the single system-under-test: `make demo` boots the app
// (embedded SPA + seeded Postgres + local blob) offline. Playwright manages its
// lifecycle unless one is already running (a human's `make demo`), in which case
// it is reused. The Go binary always serves the SAME build the human demos.
const repoRoot = fileURLToPath(new URL('..', import.meta.url));
const baseURL = process.env.BS_BASE_URL ?? 'http://127.0.0.1:8080';

export default defineConfig({
  testDir: './tests',
  // One shared server + a seeded database + a per-IP login rate limit mean the
  // suite runs serially. This is a correctness harness, not a load test.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? [['github'], ['list']] : [['list']],

  // Committed visual baselines live in tests/__screenshots__/<project>/.
  // Platform-scoped because pixel output differs by OS: the canonical set is
  // generated on the CI (Linux) runner where `make demo` runs; a local darwin
  // run writes alongside without clobbering. Updating baselines is an explicit,
  // Architect-authorised act (never a side effect of a passing run):
  // `bunx --bun=false playwright test --update-snapshots`.
  snapshotPathTemplate: '{testDir}/__screenshots__/{projectName}/{arg}-{platform}{ext}',

  use: {
    baseURL,
    trace: 'on-first-retry',
    // Bundled Chromium only — never the operator's installed browser.
    channel: undefined,
    headless: true
  },

  expect: {
    toHaveScreenshot: {
      // Two knobs, both load-bearing for a gate that must catch token misuse on
      // a DARK theme.
      //
      // threshold (pixelmatch per-pixel YIQ distance, 0..1; a pixel counts as
      // changed only when its squared colour delta exceeds 35215*threshold^2):
      // the Playwright/pixelmatch default 0.2 needs ~20% of the full
      // black->white range before it counts a pixel at all. Our UI sits at the
      // bottom of that range, so real drift never crosses the bar and the pixel
      // budget below never gets to count it. Worked examples over bg-2
      // (#141414): an 18% accent (#4e7fc2) wash composites to ~rgb(30,39,51),
      // YIQ delta ~184/35215 -> ~7% linear distance; a text-faint(#8c8880)->
      // accent recolour of glyph cores is ~1091/35215 -> ~18%. BOTH sit under
      // the 0.2 default (cutoff ~1409), which is exactly why the two proof
      // drifts passed regardless of maxDiffPixels. 0.05 (cutoff ~88, i.e. 5%
      // linear) counts both with margin (~2x on the wash, the smaller signal)
      // while staying above the same-platform noise floor: comparisons are
      // linux-vs-linux only (baselines are the CI set, see snapshotPathTemplate),
      // animations are disabled and the caret hidden, and pixelmatch's default
      // AA detection already skips anti-aliased edge pixels, so an identical
      // build renders near-zero diff. Lower buys sensitivity to sub-5% drift we
      // have no evidence of, at rising flakiness risk; 0.06+ starts to miss the
      // low end of the ~6-12% wash band. 0.05 is the sweet spot.
      threshold: 0.05,
      //
      // maxDiffPixels is an absolute budget, NOT a ratio. A 0.01 ratio on a
      // 1440x900 fullPage shot permits ~13k changed pixels, so a ~3k-px
      // token-misuse drift sails through; a ratio also scales with page height,
      // silently inflating the budget on taller pages. 150 px is height-
      // invariant, rejects any visible drift, and leaves a wide margin below the
      // smallest known real drift now that threshold actually lets those pixels
      // be counted.
      maxDiffPixels: 150,
      animations: 'disabled',
      caret: 'hide'
    }
  },

  projects: [
    // Logs in once through the real UI and saves the session for the authed
    // specs, keeping login POSTs well under the per-IP rate limit.
    { name: 'setup', testMatch: /setup\/.*\.setup\.ts$/ },

    {
      name: 'desktop-1440',
      dependencies: ['setup'],
      use: {
        ...devices['Desktop Chrome'],
        channel: undefined,
        viewport: { width: 1440, height: 900 },
        storageState: 'tests/.auth/approver.json'
      }
    },
    {
      name: 'laptop-1280',
      dependencies: ['setup'],
      use: {
        ...devices['Desktop Chrome'],
        channel: undefined,
        viewport: { width: 1280, height: 800 },
        storageState: 'tests/.auth/approver.json'
      }
    }
  ],

  webServer: {
    command: 'make demo',
    cwd: repoRoot,
    url: baseURL,
    reuseExistingServer: !process.env.CI,
    timeout: 240_000,
    stdout: 'pipe',
    stderr: 'pipe'
  }
});
