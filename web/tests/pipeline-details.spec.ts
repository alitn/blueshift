import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { isolate, libraryRow, SAMPLE, tokenRgb, cssProp } from './helpers';

// The Library pipeline hover card: hovering (or keyboard-focusing) the
// pipeline cell opens the per-stage provenance popover — stage display names,
// status dots, derived durations, public engine labels, QUEUED/TOTAL footer —
// fed lazily by GET /api/episodes/{id}/pipeline. The demo seed drives the
// sample through the REAL four-stage worker chain, so the card shows real
// recorded runs (fake engines, deterministic), never canned UI data.
//
// Rest-invisible contract: the card exists only while hovered/focused, so the
// at-rest Library is pixel-identical to the committed baselines (visual.spec
// remains the proof; nothing here screenshots at rest).

const DURATION = /^(<1S|\d+S|\d+M \d+S|\d+H \d+M|\d+D|—)$/;

test.describe('pipeline hover card', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await isolate(page, SAMPLE.search);
    await expect(libraryRow(page, SAMPLE.title)).toBeVisible();
  });

  test('hover shows the named stages, durations, and engine labels of the seeded sample', async ({
    page
  }) => {
    const sample = libraryRow(page, SAMPLE.title);
    await sample.getByTestId('pipeline-cell-trigger').hover();
    const popover = page.getByTestId('pipeline-popover');
    await expect(popover).toBeVisible();

    // Five rows in canonical order with the product display names.
    const rows = popover.getByTestId('pipeline-card-row');
    await expect(rows).toHaveCount(5);
    for (const name of ['INGEST', 'TRANSCRIBE', 'SPEAKERS', 'MOMENTS', 'RENDER']) {
      await expect(popover.getByText(name, { exact: true })).toBeVisible();
    }

    // The four seeded stages are done with derived durations; render never ran.
    for (const stage of ['ingest', 'transcribe', 'diarize', 'moments']) {
      await expect(popover.locator(`[data-stage="${stage}"]`)).toHaveAttribute(
        'data-status',
        'done'
      );
    }
    await expect(popover.locator('[data-stage="render"]')).toHaveAttribute(
      'data-status',
      'unreached'
    );
    const durations = popover.getByTestId('pipeline-card-duration');
    expect(await durations.count()).toBeGreaterThanOrEqual(4);
    for (const text of await durations.allTextContents()) {
      expect(text.trim()).toMatch(DURATION);
    }

    // Public engine labels only — the versioned neutral names, nothing else.
    await expect(popover.getByText('BLUESHIFT·ASR 2', { exact: true })).toBeVisible();
    await expect(popover.getByText('BLUESHIFT·MEDIA 1', { exact: true })).toBeVisible();

    // Footer: QUEUED + TOTAL, both derived values.
    await expect(popover.getByText('QUEUED', { exact: true })).toBeVisible();
    expect((await popover.getByTestId('pipeline-card-queued').textContent())?.trim()).toMatch(
      DURATION
    );
    expect((await popover.getByTestId('pipeline-card-total').textContent())?.trim()).toMatch(
      DURATION
    );

    // Unhover dismisses. A real pointer emits a movement stream (the tooltip's
    // grace-area exit needs a move after leaving the trigger), so glide away in
    // steps rather than teleporting.
    await page.mouse.move(20, 60, { steps: 8 });
    await expect(popover).not.toBeVisible();
  });

  test('keyboard focus opens the card, Escape dismisses, Enter never opens the episode', async ({
    page
  }) => {
    const sample = libraryRow(page, SAMPLE.title);
    const trigger = sample.getByTestId('pipeline-cell-trigger');
    await trigger.focus();
    const popover = page.getByTestId('pipeline-popover');
    await expect(popover).toBeVisible();

    // Enter on the focused trigger must NOT open the episode underneath.
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/$/);

    // Escape dismisses the card.
    await page.keyboard.press('Escape');
    await expect(popover).not.toBeVisible();
  });

  test('a mouse click on the pipeline cell still opens the Ready episode', async ({ page }) => {
    const sample = libraryRow(page, SAMPLE.title);
    await sample.getByTestId('pipeline-cell-trigger').click();
    await page.waitForURL('**/episode/**');
  });

  test('the open card conforms to the popover tokens', async ({ page }) => {
    const sample = libraryRow(page, SAMPLE.title);
    await sample.getByTestId('pipeline-cell-trigger').hover();
    const popover = page.getByTestId('pipeline-popover');
    await expect(popover).toBeVisible();

    // DESIGN.md popover conventions: bg-4 surface, border-control hairline —
    // and each is distinct from its sibling token so a corrupted tokens.css
    // cannot pass tautologically.
    const bg = await cssProp(popover, 'background-color');
    expect(bg).toBe(await tokenRgb(page, '--bg-4'));
    expect(bg).not.toBe(await tokenRgb(page, '--bg-3'));
    const border = await cssProp(popover, 'border-top-color');
    expect(border).toBe(await tokenRgb(page, '--border-control'));
    expect(border).not.toBe(await tokenRgb(page, '--border-strong'));

    // Status dots: a done stage uses the step-done fill, not the accent.
    const doneDot = popover
      .locator('[data-stage="ingest"][data-status="done"]')
      .getByTestId('pipeline-card-dot');
    const dotBg = await cssProp(doneDot, 'background-color');
    expect(dotBg).toBe(await tokenRgb(page, '--step-done'));
    expect(dotBg).not.toBe(await tokenRgb(page, '--accent'));
  });

  test('the Library with the card open has no critical accessibility violations', async ({
    page
  }) => {
    const sample = libraryRow(page, SAMPLE.title);
    await sample.getByTestId('pipeline-cell-trigger').hover();
    await expect(page.getByTestId('pipeline-popover')).toBeVisible();
    const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
    const critical = results.violations
      .filter((v) => v.impact === 'critical')
      .map((v) => `${v.id}: ${v.help}`);
    expect(critical).toEqual([]);
  });
});
