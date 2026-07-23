import { test, expect, type Page, type Locator } from '@playwright/test';
import { isolate, libraryRow, SAMPLE } from './helpers';

// Token conformance: the computed styles of the chrome match the values in
// tokens.css, read at runtime from the CSS custom properties — never compared
// against hard-coded hex (the hex gate covers web/src; these tests stay
// token-derived by construction).
//
// The Tailwind theme emits var(--token), so an element and "its own" token are
// runtime-linked: asserting only element === token would be tautological and
// could never catch a corrupted value. Each check therefore also asserts the
// element does NOT equal a sibling token. That makes the pair sensitive to a
// real tokens.css break: e.g. setting --accent to --accent-bright's value keeps
// button === --accent true but flips button !== --accent-bright to false, so
// the test goes red (the red/green proof exercised for this task).

/** tokenRgb resolves a CSS custom property to the browser's normalised rgb(a)
 *  form via a throwaway probe element, so a hex token and a computed rgb value
 *  compare equal without any literal colour in this file. */
async function tokenRgb(page: Page, name: string): Promise<string> {
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
function cssProp(el: Locator, prop: string): Promise<string> {
  return el.evaluate((node, p) => getComputedStyle(node).getPropertyValue(p), prop);
}

test.describe('token conformance', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('button', { name: 'UPLOAD MASTER' })).toBeVisible();
  });

  test('top bar and status bar sit on the bg-3 surface (not the bg-4 card)', async ({ page }) => {
    const bg3 = await tokenRgb(page, '--bg-3');
    const bg4 = await tokenRgb(page, '--bg-4');
    for (const bar of ['header', 'footer']) {
      const got = await cssProp(page.locator(bar), 'background-color');
      expect(got).toBe(bg3);
      expect(got).not.toBe(bg4);
    }
  });

  test('primary button uses accent fill (not accent-bright) and on-accent text', async ({ page }) => {
    const button = page.getByRole('button', { name: 'UPLOAD MASTER' });
    const bg = await cssProp(button, 'background-color');
    expect(bg).toBe(await tokenRgb(page, '--accent'));
    expect(bg).not.toBe(await tokenRgb(page, '--accent-bright'));
    expect(await cssProp(button, 'color')).toBe(await tokenRgb(page, '--text-on-accent'));
  });

  test('avatar surface uses the bg-4 raised fill (not bg-3)', async ({ page }) => {
    const avatar = page.getByRole('button', { name: 'Account menu' });
    const bg = await cssProp(avatar, 'background-color');
    expect(bg).toBe(await tokenRgb(page, '--bg-4'));
    expect(bg).not.toBe(await tokenRgb(page, '--bg-3'));
  });

  test('chrome fonts resolve to the token stacks', async ({ page }) => {
    // Status bar is the mono stack; the primary button is the UI stack.
    expect(await cssProp(page.locator('footer'), 'font-family')).toContain('IBM Plex Mono');
    const button = page.getByRole('button', { name: 'UPLOAD MASTER' });
    expect(await cssProp(button, 'font-family')).toContain('Archivo');
  });

  test('pipeline bars use three distinct token fills; a READY row is not five identical bars', async ({
    page
  }) => {
    // The three pipeline fills DESIGN.md defines are mutually distinct token
    // colours (step-done done, accent active, border-default pending/unreached).
    const stepDone = await tokenRgb(page, '--step-done');
    const accent = await tokenRgb(page, '--accent');
    const borderDefault = await tokenRgb(page, '--border-default');
    expect(new Set([stepDone, accent, borderDefault]).size).toBe(3);

    // The reported bug: a READY row must render bar 1 (ingest, done) distinct
    // from bars 2-5 (not reached) — never five identical greys.
    await isolate(page, SAMPLE.search);
    const sample = libraryRow(page, SAMPLE.title);
    await expect(sample.getByText('READY')).toBeVisible();
    const bars = sample.getByTestId('pipeline-bar');
    await expect(bars).toHaveCount(5);
    expect(await cssProp(bars.nth(0), 'background-color')).toBe(stepDone);
    for (let i = 1; i < 5; i++) {
      expect(await cssProp(bars.nth(i), 'background-color')).toBe(borderDefault);
    }
  });
});
