import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { cssProp, isolate, libraryRow, tokenRgb } from './helpers';

// Episode removal, end to end against the real demo stack: a freshly created
// row is removed through the Library's row action + danger confirm, disappears
// optimistically on the 204, and stays gone after a reload (server-side soft
// delete). The spec creates its own throwaway row via the API — the seeded
// sample must survive for every other spec, so it is never deleted here.

test('remove a row via the danger confirm — gone optimistically and after reload', async ({
  page
}, testInfo) => {
  // Unique per project and per run (shared demo DB, two viewport projects).
  const nonce = `${testInfo.project.name}-${Date.now()}`;
  const title = `E2E Delete ${nonce}`;

  // Seed a fresh 'uploaded' row through the real API (session cookies come from
  // the saved storageState). No upload bytes are needed — any row can be
  // removed, and this one renders as AWAITING UPLOAD.
  const created = await page.request.post('/api/episodes', {
    data: {
      title,
      source_filename: 'delete-me.mp4',
      size_bytes: 1024,
      content_type: 'video/mp4'
    }
  });
  expect(created.status()).toBe(201);
  const createdID = ((await created.json()) as { episode: { id: string } }).episode.id;

  await page.goto('/');
  await isolate(page, nonce);
  const row = libraryRow(page, title);
  await expect(row).toBeVisible();

  // The remove action is rest-invisible (committed baselines stay untouched)
  // but keyboard-reachable at rest: focus + Enter opens the confirm.
  const remove = row.getByTestId('episode-remove');
  await remove.focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByText('Remove episode')).toBeVisible();
  await expect(dialog.getByText(title)).toBeVisible();

  // Cancel first: nothing happens, the row stays.
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toBeHidden();
  await expect(row).toBeVisible();

  // Reopen by mouse: the action reveals on row hover.
  await row.hover();
  await remove.click();
  await expect(dialog.getByText('Remove episode')).toBeVisible();

  // The confirm is danger-styled from tokens (not the accent primary).
  const confirm = dialog.getByTestId('remove-confirm');
  const dangerRgb = await tokenRgb(page, '--danger');
  expect(await cssProp(confirm, 'color')).toBe(dangerRgb);
  expect(await cssProp(confirm, 'color')).not.toBe(await tokenRgb(page, '--accent'));

  // No new critical a11y violations with the new dialog open.
  const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
  expect(
    axe.violations.filter((v) => v.impact === 'critical').map((v) => `${v.id}: ${v.help}`)
  ).toEqual([]);

  // Confirm: the server answers 204 and the row drops out with NO reload
  // (optimistic local removal).
  const [res] = await Promise.all([
    page.waitForResponse(
      (r) => r.request().method() === 'DELETE' && r.url().includes('/api/episodes/')
    ),
    confirm.click()
  ]);
  expect(res.status()).toBe(204);
  await expect(dialog).toBeHidden();
  await expect(row).toHaveCount(0);

  // Reload: the deleted row never renders again (server-side exclusion), and
  // the list API no longer carries it.
  await page.reload();
  await isolate(page, nonce);
  await expect(libraryRow(page, title)).toHaveCount(0);
  const list = (await (await page.request.get('/api/episodes')).json()) as {
    episodes: { id: string }[];
  };
  expect(list.episodes.map((e) => e.id)).not.toContain(createdID);

  // Idempotent API: a repeat DELETE of the same id is still a 204.
  const again = await page.request.delete(`/api/episodes/${createdID}`);
  expect(again.status()).toBe(204);
});
