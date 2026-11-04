// Add agent (dashboard, Peers; the sidebar) in a real browser: the button, the form, a
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
    assert(text === inviteBlock({ owner: res.hub_owner, hubURL: res.hub_url, profile: 'web-repo-claude', token: res.enrol_token, expires: res.enrol_expires_at }), `invite text:\n${text}`);
    assert(text.split('\n').length <= 6 && !/[$;]/.test(text), 'the invite is not a short plain block');
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

    // An open name: the agent picks it at enrol; the field is a suggestion.
    await open.click();
    await dlg.waitFor();
    await dlg.getByLabel('Let the agent choose its name at enrol').check();
    await dlg.locator('label', { hasText: 'Suggested name (optional)' }).waitFor();
    await dlg.locator('#inv-name').fill('helper bot');
    const openAnswer = page.waitForResponse(r => r.url().endsWith('/v1/admin/peers') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    const ores = await (await openAnswer).json();
    assert(ores.name_open === true && ores.name === 'helper bot', `open answer ${JSON.stringify({ ...ores, enrol_token: '…' })}`);
    const otext = await dlg.locator('pre.inv-block').textContent();
    assert(otext === inviteBlock({ owner: ores.hub_owner, hubURL: ores.hub_url, profile: 'helper-bot', token: ores.enrol_token, expires: ores.enrol_expires_at, open: true, name: 'helper bot' }), `open invite text:\n${otext}`);
    await dlg.locator('.inv-meta', { hasText: 'profile helper-bot · name chosen at enrol' }).waitFor();
    await shot('inv-5-open');
    await dlg.getByRole('button', { name: 'Done' }).click();
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    const named = await fetch(demo.url + '/v1/enrol', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token: ores.enrol_token, name: 'Helper Bot 2' }) });
    assert(named.status === 200 && (await named.json()).name === 'Helper Bot 2', `enrol with a chosen name: ${named.status}`);
    await peers.locator('tbody tr', { hasText: 'Helper Bot 2' }).waitFor();

    // The sidebar's Add agent, under the route links: the same dialog from
    // any view, opened by keyboard over a thread that stays mounted (its
    // draft survives), with focus back on the button when it closes.
    const navAdd = page.locator('#nav-add');
    const navIds = await page.locator('nav.side > .nav').first().locator(':scope > a, :scope > button').evaluateAll(els => els.map(e => e.id));
    assert(navIds.join() === 'nav-inbox,nav-new,nav-dash,nav-add', `sidebar nav ${navIds}`);
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, demo.threads.hark);
    await page.locator('.msg').first().waitFor();
    const draft = page.locator('.composer textarea');
    await draft.fill('draft kept under the dialog');
    await page.evaluate(() => { document.querySelector('.composer textarea').dataset.mounted = 'nav-add'; });
    assert(await navAdd.isEnabled(), 'sidebar Add agent is disabled once signed in');
    await shot('inv-6-nav');
    await navAdd.focus();
    await page.keyboard.press('Enter');
    await dlg.waitFor();
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'inv-name', 'focus is not in the name field');
    assert(await page.evaluate(() => location.hash.startsWith('#thread/')), 'opening Add agent changed the route');
    await dlg.locator('#inv-name').fill('nav agent');
    await shot('inv-7-nav-thread');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'nav-add', 'focus did not return to the sidebar button');
    assert(await page.locator('.composer textarea[data-mounted="nav-add"]').inputValue() === 'draft kept under the dialog', 'the thread remounted or lost its draft');

    // Created from the sidebar: the session's api, the same invite, and the
    // directory picks the peer up.
    await navAdd.click();
    await dlg.locator('#inv-name').fill('nav agent');
    const navAnswer = page.waitForResponse(r => r.url().endsWith('/v1/admin/peers') && r.request().method() === 'POST');
    await dlg.getByRole('button', { name: 'Create invite' }).click();
    const nres = await (await navAnswer).json();
    const ntext = await dlg.locator('pre.inv-block').textContent();
    assert(ntext === inviteBlock({ owner: nres.hub_owner, hubURL: nres.hub_url, profile: 'nav-agent', token: nres.enrol_token, expires: nres.enrol_expires_at }), `sidebar invite text:\n${ntext}`);
    await dlg.getByRole('button', { name: 'Done' }).click();
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
    assert(!(await page.content()).includes(nres.enrol_token), 'the sidebar invite token is still in the page');
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id) === 'nav-add', 'focus did not return after Done');
    await page.waitForFunction(async ({ secret, id }) => {
      const r = await fetch('/v1/peers', { headers: { Authorization: `Bearer ${secret}` } });
      return ((await r.json()).peers || []).some(p => p.peer_id === id);
    }, { secret: demo.operator_secret, id: nres.peer_id }, { polling: 200 });

    // Narrow: the sidebar stacks above the view; the button and dialog fit.
    await page.setViewportSize({ width: 390, height: 844 });
    await draft.fill('');
    await page.evaluate(() => { document.getElementById('main').scrollTop = 0; window.scrollTo(0, 0); });
    const box = await navAdd.boundingBox();
    assert(box && box.x >= 0 && box.x + box.width <= 390, `sidebar Add agent off screen at 390 px: ${JSON.stringify(box)}`);
    await shot('inv-8-nav-narrow');
    await navAdd.click();
    await dlg.waitFor();
    const dbox = await dlg.boundingBox();
    assert(dbox && dbox.x >= 0 && dbox.x + dbox.width <= 390, `dialog off screen at 390 px: ${JSON.stringify(dbox)}`);
    await shot('inv-9-nav-narrow-dialog');
    await page.keyboard.press('Escape');
    await page.locator('dialog.inv').waitFor({ state: 'detached' });
  });
  console.log('Invite passed: form, client and server errors, CLI-identical text, copy, no token kept, enrol once, open name chosen at enrol, sidebar Add agent over a mounted thread with focus return, narrow');
} finally { await cleanBuild(); }
