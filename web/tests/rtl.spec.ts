import { test, expect } from '@playwright/test';
import { isolate, SAMPLE, ZWNJ } from './helpers';

// RTL + verbatim ZWNJ: the Persian sample title renders right-to-left, wrapped
// in <bdi>, with its zero-width non-joiner preserved byte-exact from seed to DOM.

test('Persian title renders RTL in <bdi> with ZWNJ preserved verbatim', async ({ page }) => {
  await page.goto('/');
  await isolate(page, SAMPLE.search);

  const title = page.getByTestId('episode-title');
  await expect(title).toBeVisible();

  // Declared and computed direction are both RTL.
  await expect(title).toHaveAttribute('dir', 'rtl');
  expect(await title.evaluate((el) => getComputedStyle(el).direction)).toBe('rtl');

  // The title is wrapped in <bdi> for correct bidi isolation in the LTR cell.
  const bdi = title.locator('bdi');
  await expect(bdi).toHaveCount(1);

  // Byte-exact text, ZWNJ (U+200C) included — the verbatim invariant.
  const text = await bdi.textContent();
  expect(text).toBe(SAMPLE.title);
  expect(text).toContain(ZWNJ);
});
