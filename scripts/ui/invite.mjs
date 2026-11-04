// Add agent (dashboard, Peers) in a real browser: the button, the form, a
// server error and a client one, and the success panel whose invite text
// matches the CLI's (invite-model.js, pinned by internal/cli/testdata/
// invite.json), enrols for real, and leaves no token behind once closed.
import { assert, cleanBuild, scrollIntoViewStable, withDemo } from './lib.mjs';
import { inviteBlock } from '../../internal/ui/static/invite-model.js';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, allowConsole } = ctx;
    allowConsole(/status of 422/);
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.locator('#conn.live').waitFor();
    await page.evaluate(() => { location.hash = '#dash'; });
    const peers = page.locator('.dash-peers');
    await peers.locator('tbody tr').first().waitFor();
    const before = await peers.locator('tbody tr').count();
    const open = peers.getByRole('button', { name: 'Add agent' });
    await open.waitFor();
    await scrollIntoViewStable(page, '.dash-peers h2');
    await shot('inv-1-button');

    // The form: kind defaults to session and never offers operator or dot.
    await open.click();
    const dlg = page.locator('dialog.inv[open]');
    await dlg.waitFor();
    const kinds = await dlg.locator('#inv-kind option').evaluateAll(os => os.map(o => o.value));
    assert(kinds[0] === 'session' && !kinds.includes('operator') && !kinds.includes('dot'), `kinds ${kinds}`);
    assert(await dlg.locator('#inv-ttl').inputValue() === '86400', 'default TTL is not 24 h');
    // A blank name is refused before any request.
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    await dlg.locator('.err', { hasText: 'A name is required.' }).waitFor();
    await dlg.locator('#inv-name').fill('web-repo Claude');
    await dlg.locator('#inv-ttl').selectOption('3600');
    await shot('inv-2-form');

    // A server refusal shows its code and message and keeps the form.
    const refuse = async route => route.request().method() === 'POST'
      ? route.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ error: { code: 'invalid_value', message: 'principal limit reached', details: { field: 'name' } } }) })
      : route.continue();
    await page.route('**/v1/admin/peers', refuse);
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    await dlg.locator('.err', { hasText: 'invalid_value: principal limit reached' }).waitFor();
    assert(await dlg.getByRole('button', { name: 'Create invite' }).isEnabled(), 'create stays disabled after an error');
    await shot('inv-3-error');
    await page.unroute('**/v1/admin/peers', refuse);

    // Success: the panel shows the CLI's text for the hub's answer.
    const answer = page.waitForResponse(r => r.url().endsWith('/v1/admin/peers') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    const res = await (await answer).json();
    const pre = dlg.locator('pre.inv-block');
    await pre.waitFor();
    const text = await pre.textContent();
    assert(res.hub_url === demo.url, `hub_url ${res.hub_url}`);
    assert(text === inviteBlock(res.hub_url, 'web-repo-claude', res.enrol_token, res.enrol_expires_at), `invite text:\n${text}`);
    await dlg.locator('.inv-warn', { hasText: 'Shown once' }).waitFor();
    await dlg.locator('.inv-meta time', { hasText: res.enrol_expires_at }).waitFor();
    await dlg.getByRole('button', { name: 'Copy invite' }).click();
    await dlg.getByRole('button', { name: 'Copied' }).waitFor();
    assert(await page.evaluate(() => navigator.clipboard.readText()) === text, 'clipboard does not hold the invite');
    await shot('inv-4-success');

    // Nothing kept client-side; closing removes the token from the page.
    const stored = await page.evaluate(() => JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }));
    assert(!stored.includes(res.enrol_token), 'the token reached web storage');
    await dlg.getByRole('button', { name: 'Done' }).click();
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    assert(!(await page.content()).includes(res.enrol_token), 'the token is still in the page');
    await page.waitForFunction(n => document.querySelectorAll('.dash-peers tbody tr').length > n, before);

    // The pasted token enrols, once.
    const enrol = () => fetch(demo.url + '/v1/enrol', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: res.enrol_token }) });
    assert((await enrol()).status === 200, 'the invite token does not enrol');
    assert((await enrol()).status === 401, 'the invite token enrolled twice');

    // Esc closes a fresh dialog too, with an empty form.
    await open.click();
    await dlg.waitFor();
    assert(await dlg.locator('#inv-name').inputValue() === '', 'the form kept the last name');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
  });
  console.log('Invite passed: form, client and server errors, CLI-identical text, copy, no token kept, enrol once');
} finally { await cleanBuild(); }
