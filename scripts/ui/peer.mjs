// Peer detail (#peer/<id>) and copyable ids, in a real browser: a peer opened
// from the dashboard, sections loading and failing on their own, copying an
// elided id (selection and button, Clipboard API and the plain-http
// fallback), a disabled peer, a hostile name, and peer links from the thread,
// its aside, the inbox and the sidebar.
import { assert, cleanBuild, scrollIntoViewStable, withDemo } from './lib.mjs';

const WRITE = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);
const SHELL_WRITES = /\/v1\/(ack|presence)(\?|$)|\/v1\/admin\/threads\/[^/]+\/read$/;

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, fail, allowConsole, countRequests } = ctx;
    allowConsole(/status of 500/);
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
    const rowText = await codexRow.evaluate(tr => { const r = document.createRange(); r.selectNodeContents(tr); const s = getSelection(); s.removeAllRanges(); s.addRange(r); return s.toString(); });
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
    assert(/Kind\s+session/.test(identity) && /Credential\s+active/.test(identity) && /enabled/.test(identity), `identity: ${identity}`);
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
    await pv.locator('.pv-subs .none, .pv-subs table').first().waitFor();
    assert(await pv.locator('.empty.error').count() === 1, 'the failure spread beyond its section');
    await page.locator('#main').evaluate(el => { el.scrollTop += document.getElementById('pv-principal').getBoundingClientRect().top - 140; });
    await shot('pv-error');

    // Read-only: no write left the page.
    assert(writes() === writesBefore, `peer view sent ${writes() - writesBefore} write(s)`);
  });
  console.log('Peer detail passed: dashboard to peer, independent sections, full-id copy (selection, button, keyboard, http fallback), disabled, hostile, unknown, peer links in thread/aside/inbox/sidebar, isolated failure');
} finally { await cleanBuild(); }
