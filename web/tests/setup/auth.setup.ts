import { test as setup, expect } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { mkdirSync, existsSync } from 'node:fs';
import { APPROVER, authDir, storageStatePath, uploadFixture } from '../helpers';

// Log in once through the real UI and persist the session. Every authed spec
// reuses this state, so the whole run makes a single login POST here plus the
// flow spec's own login — comfortably under the per-IP login rate limit.
setup('authenticate as approver', async ({ page }) => {
  mkdirSync(authDir, { recursive: true });

  await page.goto('/login');
  await page.getByLabel('Email').fill(APPROVER.email);
  await page.getByLabel('Password').fill(APPROVER.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL('**/');
  await expect(page.getByRole('button', { name: 'UPLOAD MASTER' })).toBeVisible();

  await page.context().storageState({ path: storageStatePath });
});

// Generate the tiny, real MP4 the flow spec uploads. It must be a valid clip
// the REAL worker can ingest (a bogus file would fail ingest, not reach READY),
// so we synthesise a 1s H.264+AAC clip with ffmpeg (present wherever the demo
// runs). Deterministic and offline; regenerated only if missing.
setup('generate upload fixture', async () => {
  mkdirSync(authDir, { recursive: true });
  if (existsSync(uploadFixture)) return;
  execFileSync('ffmpeg', [
    '-nostdin', '-y', '-loglevel', 'error',
    '-f', 'lavfi', '-i', 'testsrc2=duration=1:size=640x360:rate=30',
    '-f', 'lavfi', '-i', 'sine=frequency=330:duration=1',
    '-c:v', 'libx264', '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-shortest',
    uploadFixture
  ]);
});
