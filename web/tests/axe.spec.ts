import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

// axe-core smoke on the two M0 pages. The gate is "no new CRITICAL violations";
// lower-severity findings are tracked separately, not failed here.

async function criticalViolations(page: import('@playwright/test').Page) {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa'])
    .analyze();
  return results.violations
    .filter((v) => v.impact === 'critical')
    .map((v) => `${v.id}: ${v.help}`);
}

test('Library has no critical accessibility violations', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: 'UPLOAD MASTER' })).toBeVisible();
  expect(await criticalViolations(page)).toEqual([]);
});

test('Login has no critical accessibility violations', async ({ page }) => {
  await page.goto('/login');
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
  expect(await criticalViolations(page)).toEqual([]);
});
