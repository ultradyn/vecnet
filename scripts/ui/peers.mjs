import { assert, cleanBuild, withDemo } from './lib.mjs';
try {
 await withDemo({ many: true }, async ({ page, shot }) => {
  await page.goto(page.url().split('#')[0] + '#peers');
  const rows = page.locator('#d-peers tbody tr');
  await rows.first().waitFor();
  assert(await rows.count() === 50, 'peer browser must render the first 50 only');
  await page.locator('#d-peers').getByRole('button', { name: 'Show more', exact: true }).click();
  assert(await rows.count() > 50, 'Show more did not reveal peers');
  await page.getByRole('searchbox', { name: 'Find peers' }).fill('browse-01');
  assert(await rows.count() === 0, 'disabled peer visible by default');
  await page.getByRole('checkbox', { name: 'Show disabled', exact: true }).check();
  assert(await rows.count() === 1, 'disabled peer filter did not show match');
  await page.getByRole('searchbox', { name: 'Find peers' }).fill('browse-10');
  assert(await rows.count() === 0, 'archived peer visible by default');
  await page.getByRole('checkbox', { name: 'Show archived', exact: true }).check();
  assert(await rows.count() === 1, 'archived peer filter did not show match');
  await page.getByRole('searchbox', { name: 'Find peers' }).fill('browse-30');
  await page.waitForFunction(() => location.hash.includes('q=browse-30'));
  await rows.locator('a.pn').first().click();
  await page.waitForFunction(() => document.body.dataset.route === 'peer');
  await page.goBack();
  await page.locator('#d-peers tbody tr').first().waitFor();
  assert(await page.getByRole('searchbox', { name: 'Find peers' }).inputValue() === 'browse-30', 'Back lost peer query');
  assert(await rows.count() === 1, 'Back did not restore filtered rows');
  await page.getByRole('searchbox', { name: 'Find peers' }).fill('');
  await page.getByRole('combobox', { name: 'Kind', exact: true }).selectOption('source');
  assert(await rows.count() === 1 && (await rows.textContent()).includes('gh-ultradyn'), 'kind filter failed');
  await page.getByRole('combobox', { name: 'Kind', exact: true }).selectOption('');
  await shot('peers-dark');
  await page.evaluate(() => { document.documentElement.dataset.theme = 'light'; }); await shot('peers-light');
  await page.setViewportSize({ width: 390, height: 844 }); await shot('peers-phone');
 });
 console.log('Peers passed: 50-row bound, more, search, disabled, archived, kind filters; dark/light/phone');
} finally { await cleanBuild(); }
