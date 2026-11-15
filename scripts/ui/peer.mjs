// Peer detail (#peer/<id>) and copyable ids, in a real browser: a peer opened
// from the dashboard, sections loading and failing on their own, copying an
// elided id (selection and button, Clipboard API and the plain-http
// fallback), a disabled peer, a hostile name, and peer links from the thread,
// its aside, the inbox and the sidebar.
import { assert, cleanBuild, scrollIntoViewStable, withDemo } from './lib.mjs';

const WRITE = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);
// The shell's own writes, and the thread view's read receipts as it is read.
const SHELL_WRITES = /\/v1\/(ack|presence|receipts)(\?|$)/;

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, fail, allowConsole, countRequests, quiet } = ctx;
    allowConsole(/status of 500/);
    allowConsole(/status of 422/); // the route Verify that fails on purpose below
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    const id = key => demo.peers[key].id;
    const clip = () => page.evaluate(() => navigator.clipboard.readText());
    const setClip = text => page.evaluate(t => navigator.clipboard.writeText(t), text);
    const ok = async (r, what) => { assert(r.ok, `${what}: ${r.status} ${await r.text()}`); };
    const route = async hash => {
      await page.evaluate(h => { location.hash = h; }, hash);
      await page.waitForFunction(h => location.hash === h, hash);
    };

    // Fixture: scopes and limits on codex, claw disabled, and one thread the
    // operator is not in (for the sidebar's participants line).
    await ok(await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: 'peer-1', priority: 'hold', recipients: [id('grok'), id('sleuth')], thread_title: 'Peer-only review', content: { text: 'No operator here.' } }), 'peer-only send');
    await ok(await asPeer('operator', 'PUT', `/v1/admin/peers/${id('codex')}/scopes`, { allow: [id('grok'), id('sleuth')], deny: [id('triage')] }), 'scopes');
    await ok(await asPeer('operator', 'PUT', `/v1/admin/peers/${id('codex')}/limits`, { sends_per_min: 45, threads_per_hour: 12 }), 'limits');
    await ok(await asPeer('operator', 'POST', `/v1/admin/peers/${id('claw')}/disable`), 'disable');

    await page.locator('#conn.live').waitFor();
    await page.locator('#threads a').first().waitFor();
    const writes = () => countRequests(req => WRITE.has(req.method()) && !SHELL_WRITES.test(new URL(req.url()).pathname));
    const writesBefore = writes();

    // ---- dashboard: links and copyable ids ----
    await route('#dash');
    const peers = page.locator('.dash-peers');
    await peers.locator('tbody tr').first().waitFor();
    // Every section drawn, so no first-load redraw replaces nodes under the mouse.
    await page.waitForFunction(() => !document.querySelector('.dash .loading') && document.querySelector('.dash-audit .audit'));
    const codexRow = peers.locator('tbody tr', { has: page.locator(`a.plink[href="#peer/${id('codex')}"]`) });
    const idv = codexRow.locator('.idv').first();
    // Visually short (peer_…X6Z8B), though the whole id is in the text.
    const idSel = `.dash-peers tr:has(a.plink[href="#peer/${id('codex')}"]) .idv`;
    const width = () => page.evaluate(sel => { const el = document.querySelector(sel); return el ? el.getBoundingClientRect().width : -1; }, idSel);
    await page.waitForFunction(sel => { const el = document.querySelector(sel); return el && el.getBoundingClientRect().width > 0; }, idSel, { timeout: 5000 }).catch(() => {});
    const w = await width();
    assert(w > 40 && w < 120, `elided id is ${w}px wide`);
    assert(await idv.getAttribute('title') === id('codex'), 'full id not in the title');

    // One click selects the whole id; a copy holds the full id, no ellipsis.
    await setClip('');
    await idv.click();
    const sel = await page.evaluate(() => getSelection().toString());
    assert(sel === id('codex'), `selection after click: ${JSON.stringify(sel)}`);
    await page.keyboard.press('Control+C');
    let copied = await clip();
    assert(copied === id('codex') && !copied.includes('…'), `Ctrl+C copied ${JSON.stringify(copied)}`);
    // A drag across the whole row also copies whole ids.
    // Live redraws can replace the row mid-check, so select the current row and retry briefly.
    let rowText = '';
    for (let i = 0; i < 20 && !rowText.includes(id('codex')); i++) {
      if (i) await page.waitForTimeout(100);
      rowText = await codexRow.evaluate(tr => { const r = document.createRange(); r.selectNodeContents(tr); const s = getSelection(); s.removeAllRanges(); s.addRange(r); return s.toString(); });
    }
    assert(rowText.includes(id('codex')) && !rowText.includes('…'), `row selection: ${JSON.stringify(rowText)}`);
    await page.evaluate(() => getSelection().removeAllRanges());

    // The copy button: hidden until hover, then a brief copied state.
    const btn = codexRow.locator('button.cpy').first();
    await page.mouse.move(700, 5);
    await page.waitForFunction(sel => getComputedStyle(document.querySelector(sel)).opacity === '0', `.dash-peers tr:has(a.plink[href="#peer/${id('codex')}"]) button.cpy`, { timeout: 3000 })
      .catch(() => { throw new Error('copy button shows without hover'); });
    await codexRow.locator('.idc').first().hover();
    await page.waitForFunction(sel => getComputedStyle(document.querySelector(sel)).opacity === '1', `.dash-peers tr:has(a.plink[href="#peer/${id('codex')}"]) button.cpy`);
    assert(await btn.getAttribute('aria-label') === 'Copy peer id', 'copy button label');
    await shot('pv-dash-hover');
    await setClip('');
    await btn.click();
    await codexRow.locator('button.cpy.done').waitFor();
    assert(await btn.getAttribute('aria-label') === 'Copied', 'copied state not announced');
    assert(await clip() === id('codex'), 'copy button did not copy the full id');
    await codexRow.locator('button.cpy.done').waitFor({ state: 'detached', timeout: 5000 });

    // Keyboard: the button is reachable and shows on focus.
    await btn.focus();
    assert(await btn.evaluate(b => getComputedStyle(b).opacity) === '1', 'copy button hidden on keyboard focus');
    await setClip('');
    await page.keyboard.press('Enter');
    await codexRow.locator('button.cpy.done').waitFor();
    assert(await clip() === id('codex'), 'Enter on the copy button did not copy');

    // Plain http (no Clipboard API): the textarea fallback still copies.
    const threadRow = page.locator('.dash-threads tbody tr').first();
    const thrId = await threadRow.locator('.idv').first().getAttribute('title');
    await page.evaluate(() => {
      window.__copied = [];
      document.addEventListener('copy', () => window.__copied.push(getSelection().toString() || document.activeElement.value));
      Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
    });
    await threadRow.locator('.idc').first().hover();
    await threadRow.locator('button.cpy').first().click();
    await threadRow.locator('button.cpy.done').waitFor();
    assert((await page.evaluate(() => window.__copied)).includes(thrId), 'fallback copy did not carry the full thread id');
    assert(await page.locator('textarea.copybuf').count() === 0, 'fallback textarea left behind');
    await page.evaluate(() => { delete window.isSecureContext; });

    // ---- loading on their own, then the detail ----
    // See shell.mjs: a second route keeps interception on as holds unroute.
    await page.route('**/__keep-interception__', route => route.continue());
    const releases = {};
    const byParticipant = u => u.pathname === '/v1/admin/threads' && u.searchParams.has('participant');
    for (const [key, glob] of Object.entries({ principals: '**/v1/admin/principals', threads: byParticipant, routes: '**/v1/admin/routes' })) releases[key] = await hold(glob);
    await codexRow.locator('a.plink').first().click();
    await page.waitForFunction(h => location.hash === h && document.body.dataset.route === 'peer', '#peer/' + id('codex'));
    const pv = page.locator('.pv');
    await pv.locator('.pv-head h1', { hasText: 'codex-hark' }).waitFor();
    await pv.locator('.pv-presence dl').waitFor();
    await pv.locator('.pv-health .none, .pv-health table').first().waitFor();
    for (const key of ['principal', 'routes', 'threads']) assert(await pv.locator(`.pv-${key} .loading`).count() === 1, `${key} not loading while held`);
    assert(await pv.locator('.empty.error').count() === 0, 'a section failed while loading');
    await shot('pv-progress');
    for (const r of Object.values(releases)) await r();
    await pv.locator('.pv-principal dl').waitFor();
    await pv.locator('.pv-threads tbody tr').first().waitFor();
    await pv.locator('.pv-routes .none, .pv-routes table').first().waitFor();
    assert(await pv.locator('.loading').count() === 0, 'a section is stuck loading');

    // Everything the operator can see about the peer.
    const headId = await pv.locator('.pv-head .pv-id .pid').textContent();
    assert(headId === id('codex'), `header id ${headId}`);
    await setClip('');
    await pv.locator('.pv-head .pv-id button.cpy').click();
    assert(await clip() === id('codex'), 'header copy');
    const text = sel => pv.locator(sel).innerText();
    const identity = await text('.pv-identity');
    assert(/Kind\s+Agent session/.test(identity) && /Credential\s+active/.test(identity) && /enabled/.test(identity), `identity: ${identity}`);
    const presence = await text('.pv-presence');
    assert(/working/.test(presence) && /Capabilities\s+demo/.test(presence) && /Location\s+demo/.test(presence), `presence: ${presence}`);
    const access = await text('.pv-access');
    assert(access.includes('grok-reviewer') && access.includes('pag-hark-sleuth') && /Deny[\s\S]*grok-triage/.test(access) && access.includes('45 / min') && access.includes('12 / hour'), `access: ${access}`);
    assert(await pv.locator(`.pv-access a.plink[href="#peer/${id('grok')}"]`).count() === 1, 'scope target is not a peer link');
    assert(/Name\s+codex-hark/.test(await text('.pv-principal')), 'implicit principal not shown');
    // The principal link scrolls to its section and stays on this route.
    await pv.locator('.pv-identity dd a', { hasText: 'codex-hark' }).click();
    assert(await page.evaluate(() => location.hash) === '#peer/' + id('codex') && await page.evaluate(() => document.body.dataset.route) === 'peer', 'principal link left the peer view');
    // Recent threads: only this peer's, linked; participants are peer links.
    const hub = await (await asPeer('operator', 'GET', `/v1/admin/threads?participant=${id('codex')}&limit=200`)).json();
    const want = new Set(hub.threads.map(r => r.thread.thread_id));
    const hrefs = await pv.locator('.pv-threads tbody tr a.tl').evaluateAll(as => as.map(a => a.getAttribute('href')));
    assert(hrefs.length === Math.min(want.size, 25) && hrefs.every(h => want.has(h.slice('#thread/'.length))), `thread links ${hrefs.length}/${want.size}`);
    assert(await pv.locator(`.pv-threads td.pps a.plink[href="#peer/${id('codex')}"]`).count() >= 1, 'participants are not links');
    await page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    await shot('pv-success');

    // A peer with a route and delivery trouble (grok-triage).
    await route('#peer/' + id('triage'));
    await pv.locator('.pv-head h1', { hasText: 'grok-triage' }).waitFor();
    await pv.locator('.pv-routes tbody tr').first().waitFor();
    await pv.locator('.pv-health h3', { hasText: 'Blocked' }).waitFor();
    await pv.locator('.pv-health h3', { hasText: 'Failed' }).waitFor();
    assert((await text('.pv-routes')).includes('this peer'), 'own route not attributed');
    await scrollIntoViewStable(page, '.pv-routes h2');
    await page.locator('#main').evaluate(el => { el.scrollTop += document.getElementById('pv-routes').getBoundingClientRect().top - 120; });
    await shot('pv-routes');

    // ---- disabled ----
    await route('#peer/' + id('claw'));
    await pv.locator('.pv-head.off h1', { hasText: 'claw-ops' }).waitFor();
    await pv.locator('.pv-head .chip', { hasText: 'disabled' }).waitFor();
    await pv.locator('.pv-head .note', { hasText: 'Disabled' }).waitFor();
    assert(/Status\s+disabled/.test(await text('.pv-identity')), 'disabled status');
    await page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    await shot('pv-disabled');

    // ---- hostile name: inert text, controls stripped ----
    await route('#peer/' + id('hostile'));
    await pv.locator('.pv-head h1 .nm', { hasText: '<img src=x onerror=alert(1)>' }).waitFor();
    const nm = await pv.locator('.pv-head h1 .nm').textContent();
    assert(!/[\u0000-\u001f\u007f-\u009f‪-‮⁦-⁩]/.test(nm), `controls survived: ${JSON.stringify(nm)}`);
    assert(await page.locator('#view img').count() === 0, 'a peer name became an element');
    await pv.locator('.pv-threads .none, .pv-threads table').first().waitFor();
    await pv.locator('.pv-principal dl').waitFor();
    // Every section, the principal (named after the peer) included.
    const all = await page.locator('#view').textContent();
    assert(!/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/.test(all), 'a control or bidi override reached the peer view');
    await page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    await shot('pv-hostile');

    // ---- unknown id ----
    await route('#peer/peer_01JD0000000000000000000000');
    await pv.locator('.pv-head h1', { hasText: 'Unknown peer' }).waitFor();
    await pv.locator('.pv-head .empty', { hasText: 'No peer with this id' }).waitFor();

    // ---- peer links elsewhere ----
    // Thread view: the sender's name opens the peer; so does the aside.
    await route('#thread/' + hub.threads[0].thread.thread_id);
    await page.locator(`#aside .pp a.plink[href="#peer/${id('codex')}"]`).waitFor({ state: 'attached' });
    const sender = page.locator('.msg header b a.plink').last();
    await sender.waitFor();
    const senderHref = await sender.getAttribute('href');
    await sender.click();
    await page.waitForFunction(h => location.hash === h, senderHref);
    // Inbox: the sender inside a row link opens the peer, the row the thread.
    await route('#inbox');
    const rowSender = page.locator('a.item span.plink[data-peer]').first();
    await rowSender.waitFor();
    const senderId = await rowSender.getAttribute('data-peer');
    await rowSender.click();
    await page.waitForFunction(h => location.hash === h, '#peer/' + senderId);
    await route('#inbox');
    const inboxRow = page.locator('a.item').first();
    await inboxRow.locator('.ex').click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/'));
    // Sidebar: a name in Other Threads' participants line opens the peer.
    const side = page.locator(`#others a span.plink[data-peer="${id('grok')}"]`).first();
    await side.waitFor();
    await side.click();
    await page.waitForFunction(h => location.hash === h, '#peer/' + id('grok'));
    await pv.locator('.pv-head h1', { hasText: 'grok-reviewer' }).waitFor();

    // ---- one failing section, the rest render ----
    await fail('**/v1/admin/routes', 500, 'internal', 'Routes query failed');
    await route('#peer/' + id('codex'));
    await page.reload({ waitUntil: 'load' });
    await pv.locator('.pv-routes .empty.error', { hasText: 'Routes unavailable' }).waitFor();
    await pv.locator('.pv-routes .empty.error', { hasText: 'Routes query failed' }).waitFor();
    await pv.locator('.pv-presence dl').waitFor();
    await pv.locator('.pv-principal dl').waitFor();
    await pv.locator('.pv-threads tbody tr').first().waitFor();
    await pv.locator('.pv-sub-summary').waitFor();
    assert(await pv.locator('.empty.error').count() === 1, 'the failure spread beyond its section');
    await page.locator('#main').evaluate(el => { el.scrollTop += document.getElementById('pv-principal').getBoundingClientRect().top - 140; });
    await shot('pv-error');

    // Read-only: no write left the page.
    assert(writes() === writesBefore, `peer view sent ${writes() - writesBefore} write(s)`);
    await page.unroute('**/v1/admin/routes');

    // ---- push route setup from the peer page ----
    // A Grok Bot that enrolled with no routine: the header and the Routes
    // section call it out, and Set up opens Add route on the Grok Bot kind.
    const ROUTINE = 'https://api2.cursor.sh/automations/webhook/123e4567-e89b-12d3-a456-426614174000';
    const ROUTINE2 = 'https://api2.cursor.sh/automations/webhook/89abcdef-0123-4567-89ab-cdef01234567';
    const KEY = 'crsr_peer-page-canary-0123456789', KEY2 = 'crsr_peer-page-canary-rotated-987';
    const added = await (await asPeer('operator', 'POST', '/v1/admin/peers', { kind: 'grokbot', name: 'grok-no-hook' })).json();
    const grokId = added.peer_id;
    assert(/^peer_/.test(grokId), `added grok ${JSON.stringify(Object.keys(added))}`);
    await route('#peer/' + grokId);
    await pv.locator('.pv-head .pv-setup', { hasText: 'Setup incomplete' }).waitFor();
    const routes = pv.locator('.pv-routes');
    await routes.locator('.pv-rt-missing', { hasText: 'No Grok Bot routine is set up' }).waitFor();
    assert(await routes.locator('.pv-rt-add').innerText() === 'Set up Grok Bot routine', 'set-up button label');
    await page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    await shot('pv-route-missing');
    await pv.locator('.pv-head .pv-setup a').click();
    const form = routes.locator('form.pv-rt-f');
    await form.waitFor();
    assert(await form.locator('select').inputValue() === 'grokbot', 'Add route did not start on Grok Bot');
    assert(await page.evaluate(() => document.activeElement && document.activeElement.type) === 'url', 'URL field not focused');
    // Checked before sending: a foreign URL and a missing key say so, and send nothing.
    const routeWrites = () => countRequests(req => WRITE.has(req.method()) && /\/v1\/admin\/routes/.test(new URL(req.url()).pathname));
    const w0 = routeWrites();
    await form.locator('input[type=url]').fill('https://example.com/hook');
    await form.locator('input[type=password]').fill(KEY);
    await form.locator('button[type=submit]').click();
    await form.locator('.err', { hasText: 'A Grok Bot routine URL is' }).waitFor();
    await form.locator('input[type=url]').fill(ROUTINE);
    await form.locator('input[type=password]').fill('');
    await form.locator('button[type=submit]').click();
    await form.locator('.err', { hasText: 'Enter the routine key' }).waitFor();
    assert(routeWrites() === w0, 'an invalid form was sent');
    // Bearer-prefixed paste is accepted, as onboarding does.
    await form.locator('input[type=password]').fill('Bearer ' + KEY);
    await shot('pv-route-form');
    await form.locator('button[type=submit]').click();
    await routes.locator('.pv-rt-msg', { hasText: 'Grok Bot route added' }).waitFor();
    await routes.locator('tbody tr', { hasText: 'grokbot' }).waitFor();
    await routes.locator('.pv-rt-missing').waitFor({ state: 'hidden' });
    await pv.locator('.pv-head .pv-setup').waitFor({ state: 'detached' });
    let listed = await (await asPeer('operator', 'GET', '/v1/admin/routes')).json();
    let grokRoute = listed.routes.find(r => r.owner_id === grokId);
    assert(grokRoute && grokRoute.kind === 'grokbot' && grokRoute.verified_at, `grok route ${JSON.stringify(grokRoute)}`);
    const grokRouteId = grokRoute.route_id;
    // Focus lands on the new route's Edit, not on the page body.
    await page.waitForFunction(() => document.activeElement && document.activeElement.classList.contains('pv-rt-edit'));
    // One route of its own: Add steps aside for Edit.
    assert(await routes.locator('.pv-rt-add').isHidden(), 'Add route offered beside an existing own route');
    // The key never reaches the DOM, an attribute or a stored value.
    const leaked = await page.evaluate(k => document.documentElement.outerHTML.includes(k) || [...document.querySelectorAll('input')].some(i => i.value.includes(k)), KEY);
    assert(!leaked, 'the routine key is in the page');

    // Edit: a new key alone; the URL stays.
    await routes.locator('.pv-rt-edit').click();
    await form.waitFor();
    assert(await form.locator('select').isDisabled(), 'kind is editable on an existing route');
    await form.locator('input[type=password]').fill(KEY2);
    await form.locator('button[type=submit]').click();
    await routes.locator('.pv-rt-msg', { hasText: 'Route updated.' }).waitFor();
    // Edit: the URL alone. Still live: a routine has no challenge.
    await routes.locator('.pv-rt-edit').click();
    await form.locator('input[type=url]').fill(ROUTINE2);
    await form.locator('button[type=submit]').click();
    await routes.locator('.pv-rt-msg', { hasText: 'Route updated.' }).waitFor();
    listed = await (await asPeer('operator', 'GET', '/v1/admin/routes')).json();
    grokRoute = listed.routes.find(r => r.owner_id === grokId);
    assert(grokRoute && grokRoute.route_id === grokRouteId && grokRoute.verified_at && listed.routes.filter(r => r.owner_id === grokId).length === 1, 'grok route was re-created, duplicated or went unverified');
    assert(!(await page.evaluate(k => document.documentElement.outerHTML.includes(k), KEY2)), 'the edited routine key is in the page');
    // Edit then Save keeps keyboard focus on the row's Edit.
    assert(await page.evaluate(() => document.activeElement && document.activeElement.classList.contains('pv-rt-edit')), 'focus left the row after Save');
    // An empty edit is refused before sending.
    await routes.locator('.pv-rt-edit').click();
    const w1 = routeWrites();
    await form.locator('button[type=submit]').click();
    await form.locator('.err', { hasText: 'Enter a new URL, a new key, or both' }).waitFor();
    assert(routeWrites() === w1, 'an empty edit was sent');
    await form.locator('button', { hasText: 'Cancel' }).click();
    await form.waitFor({ state: 'detached' });

    // A session peer with a signed webhook: Add shows the whsec_ once;
    // Verify against a receiver that echoes the challenge; a new URL that
    // does not echo is unverified and its Verify fails with a fixed sentence.
    const { createServer } = await import('node:http');
    const receiver = createServer((req, res) => {
      let body = '';
      req.on('data', c => { body += c; });
      req.on('end', () => {
        let challenge = '';
        try { challenge = JSON.parse(body).challenge || ''; } catch { /* a push, not a challenge */ }
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ challenge }));
      });
    });
    await new Promise(resolve => receiver.listen(0, '127.0.0.1', resolve));
    const hookURL = `http://127.0.0.1:${receiver.address().port}/hook`;
    try {
      await route('#peer/' + id('sleuth'));
      await pv.locator('.pv-head h1', { hasText: 'pag-hark-sleuth' }).waitFor();
      await routes.locator('.pv-rt-add', { hasText: 'Add route' }).click();
      assert(await form.locator('select').inputValue() === 'http', 'a session peer did not start on Webhook');
      assert(await form.locator('input[type=password]').isHidden(), 'the key field shows for a webhook');
      await form.locator('input[type=url]').fill(hookURL);
      await form.locator('button[type=submit]').click();
      const secret = routes.locator('.pv-rt-secret');
      await secret.waitFor();
      const whsec = await secret.textContent();
      assert(/^whsec_/.test(whsec), 'no whsec_ shown');
      await routes.locator('tbody tr', { hasText: 'unverified' }).waitFor();
      await setClip('');
      await routes.locator('.pv-rt-fresh button', { hasText: 'Copy secret' }).click();
      assert(await clip() === whsec, 'Copy secret');
      await shot('pv-route-secret');
      await routes.locator('.pv-rt-fresh button', { hasText: 'Done' }).click();
      await secret.waitFor({ state: 'detached' });
      assert(!(await page.evaluate(w => document.documentElement.outerHTML.includes(w), whsec)), 'the secret outlived Done');
      // Keyboard Verify: focus stays on the button through the request.
      await routes.locator('.pv-rt-verify', { hasText: 'Verify' }).focus();
      await page.keyboard.press('Enter');
      await routes.locator('.pv-rt-msg', { hasText: 'Verified' }).waitFor();
      await routes.locator('tbody tr .bad', { hasText: 'unverified' }).waitFor({ state: 'detached' });
      assert(await page.evaluate(() => document.activeElement && document.activeElement.classList.contains('pv-rt-verify')), 'focus left Verify');
      // An armed Confirm survives a redraw with the same routes. Hold the
      // page's next routes read (its 30 s reload), arm Remove with focus
      // elsewhere, then let the read land: the section redraws, Remove stays armed.
      const routesPath = u => u.pathname === '/v1/admin/routes';
      const reads = countRequests(req => routesPath(new URL(req.url())));
      const releaseRoutes = await hold(routesPath);
      for (const t = Date.now(); countRequests(req => routesPath(new URL(req.url()))) === reads; ) {
        assert(Date.now() - t < 40000, 'the 30 s reload did not re-read routes');
        await page.waitForTimeout(100);
      }
      await routes.locator('.pv-rt-remove').click();
      await routes.locator('.pv-rt-remove', { hasText: 'Confirm remove' }).waitFor();
      await page.evaluate(() => document.activeElement && document.activeElement.blur());
      await releaseRoutes();
      await quiet();
      assert(await routes.locator('.pv-rt-remove', { hasText: 'Confirm remove' }).count() === 1, 'a redraw disarmed Confirm remove');
      await page.waitForTimeout(3200); // let it fall back unconfirmed
      await routes.locator('.pv-rt-remove', { hasText: 'Remove' }).waitFor();
      // Edit to a receiver that answers but does not echo: unverified again.
      await routes.locator('.pv-rt-edit').click();
      await form.locator('.pv-rt-hint', { hasText: 'stops pushes until it is verified again' }).waitFor();
      await form.locator('input[type=url]').fill(demo.url + '/__demo/sink/peer-page');
      await form.locator('button[type=submit]').click();
      await routes.locator('.pv-rt-msg', { hasText: 'Press Verify' }).waitFor();
      await routes.locator('tbody tr .bad', { hasText: 'unverified' }).waitFor();
      await routes.locator('.pv-rt-verify').click();
      await routes.locator('.err', { hasText: 'did not echo the challenge' }).waitFor();
      assert(!(await routes.locator('.err').innerText()).includes('__demo'), 'the verify error quotes the URL');
      await page.locator('#main').evaluate(el => { el.scrollTop += document.getElementById('pv-routes').getBoundingClientRect().top - 120; });
      await shot('pv-route-verify-failed');
      // New secret (confirm), then Remove (confirm): the peer pulls again.
      await routes.locator('.pv-rt-rotate').click();
      await routes.locator('.pv-rt-rotate', { hasText: 'Confirm new secret' }).click();
      await routes.locator('.pv-rt-fresh', { hasText: 'old one keeps working for 24 hours' }).waitFor();
      const rotated = await routes.locator('.pv-rt-secret').textContent();
      await routes.locator('.pv-rt-fresh button', { hasText: 'Done' }).click();
      assert(!(await page.evaluate(w => document.documentElement.outerHTML.includes(w), rotated)), 'the rotated secret outlived Done');
      assert(await page.evaluate(() => document.activeElement && document.activeElement !== document.body), 'Done left focus on the body');
      // Phone width: the actions are on screen, no sideways scroll.
      await page.setViewportSize({ width: 390, height: 844 });
      const fit = await page.evaluate(() => {
        const sec = document.getElementById('pv-routes').getBoundingClientRect();
        return [...document.querySelectorAll('#pv-routes .pv-rt-btns button')].map(b => { const r = b.getBoundingClientRect(); return r.right <= Math.min(sec.right, innerWidth) + 0.5 && r.left >= sec.left - 0.5; });
      });
      assert(fit.length >= 3 && fit.every(Boolean), `route actions off screen at 390px: ${JSON.stringify(fit)}`);
      await page.locator('#pv-routes').evaluate(el => el.scrollIntoView({ block: 'start' }));
      await shot('pv-route-phone');
      await page.setViewportSize({ width: 1440, height: 900 });
      await routes.locator('.pv-rt-remove').click();
      await routes.locator('.pv-rt-remove', { hasText: 'Confirm remove' }).click();
      await routes.locator('.pv-rt-msg', { hasText: 'Route removed' }).waitFor();
      await routes.locator('.none', { hasText: 'This peer pulls its deliveries' }).waitFor();
      listed = await (await asPeer('operator', 'GET', '/v1/admin/routes')).json();
      assert(!listed.routes.some(r => r.owner_id === id('sleuth')), 'the route was not removed');
    } finally {
      await new Promise(resolve => receiver.close(resolve));
    }
    // The operator and sources get no route controls.
    await route('#peer/' + id('gh'));
    await routes.locator('.none, table').first().waitFor();
    assert(await routes.locator('.pv-rt-add:visible, .pv-rt-edit').count() === 0, 'route controls on a source');
  });
  console.log('Peer detail passed: dashboard to peer, independent sections, full-id copy (selection, button, keyboard, http fallback), disabled, hostile, unknown, peer links in thread/aside/inbox/sidebar, isolated failure; push routes: Grok Bot missing-routine callout and setup, form checks, key never in the page, edit key/URL in place, webhook whsec_ once, verify, re-verify after a URL change, fixed verify error, new secret, remove');
} finally { await cleanBuild(); }
