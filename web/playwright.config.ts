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
      maxDiffPixelRatio: 0.01,
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
