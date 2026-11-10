// Peer header message actions (#peer/<id>), in a real browser: the quick link
// opens the operator's newest thread with the peer, or New message prefilled
// to the peer when there is none, and New message is always beside it, at
// wake_idle for a Grok Bot. Also checked: hover and keyboard focus, focus
// kept while the view redraws and when a newer thread replaces the link, the
// prefilled composer sending, a later New message starting empty, a modified
// click handing over no draft, and a source offering neither.
import { assert, cleanBuild, withDemo } from './lib.mjs';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer } = ctx;
    const id = key => demo.peers[key].id;
    const me = demo.operator_peer;
    const route = async hash => {
      await page.evaluate(h => { location.hash = h; }, hash);
      await page.waitForFunction(h => location.hash === h, hash);
    };
    const pv = page.locator('.pv');
    const acts = pv.locator('.pv-head .pv-acts');
    const open = acts.locator('.pv-open'), compose = acts.locator('a.pv-new');
    const top = () => page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    // What the hub says: the newest-activity thread the operator shares with peer.
    const newestShared = async peer => {
      const r = await asPeer('operator', 'GET', `/v1/admin/threads?participant=${peer}&limit=25`);
      assert(r.ok, `threads: ${r.status}`);
      const rows = (await r.json()).threads.filter(x => x.thread.participants.includes(me));
      rows.sort((a, b) => (a.last_message_at < b.last_message_at ? 1 : a.last_message_at > b.last_message_at ? -1 : 0));
      return rows[0] ? rows[0].thread : null;
    };
    const composer = async () => {
      await page.waitForFunction(() => location.hash === '#new' && document.body.dataset.route === 'new');
      await page.locator('.compose form').waitFor();
    };
    const picked = () => page.locator('.compose .picked .pick').evaluateAll(xs => xs.map(x => x.getAttribute('title')));
    const prio = () => page.locator('.compose .seg button.on').getAttribute('value');

    await page.locator('#conn.live').waitFor();
    await page.locator('#threads a').first().waitFor();

    // ---- has a thread: grok-reviewer (a Grok Bot) is in hark B196 with the operator ----
    const hark = await newestShared(id('grok'));
    assert(hark, 'fixture: grok-reviewer shares no thread with the operator');
    await route('#peer/' + id('grok'));
    await pv.locator('.pv-head h1', { hasText: 'grok-reviewer' }).waitFor();
    await acts.locator(`a.pv-open[href="#thread/${hark.thread_id}"]`).waitFor();
    assert((await open.innerText()).includes('Open thread'), 'quick link label');
    assert((await open.locator('.qt').innerText()).startsWith('hark B196'), 'quick link names the thread');
    assert(await compose.count() === 1, 'New message missing beside a thread');
    assert(await compose.locator('.pr').innerText() === 'wake_idle', 'a Grok Bot\'s New message does not say wake_idle');
    const box = await acts.boundingBox(), h1 = await pv.locator('.pv-head h1').boundingBox();
    assert(box && h1 && box.x > h1.x + h1.width && Math.abs(box.y - h1.y) < 24, `actions not beside the name: ${JSON.stringify({ box, h1 })}`);
    await page.mouse.move(700, 5);
    await top();
    await shot('pv-quick-thread');

    // Hover: the primary brightens; the pointer is a link's.
    await open.hover();
    await page.waitForFunction(() => getComputedStyle(document.querySelector('.pv-acts .pv-open')).filter.includes('brightness'));
    const lines = await acts.locator('a.pv-open, a.pv-open *').evaluateAll(els => els.map(el => getComputedStyle(el).textDecorationLine));
    assert(lines.every(l => l === 'none'), `hovered action is underlined: ${lines}`);
    await shot('pv-quick-hover');
    await page.mouse.move(700, 5);

    // Keyboard: Tab from the quick link reaches New message, focus ring shown,
    // and a redraw of the view (a presence report re-reads the peers) keeps it.
    await open.focus();
    await page.keyboard.press('Tab');
    assert(await compose.evaluate(el => el === document.activeElement && el.matches(':focus-visible')), 'Tab did not reach New message');
    const outline = await compose.evaluate(el => getComputedStyle(el).outlineStyle);
    assert(outline === 'solid', `no focus ring: ${outline}`);
    const heard = page.waitForResponse(r => new URL(r.url()).pathname === '/v1/admin/peers' && r.request().method() === 'GET');
    assert((await asPeer('grok', 'POST', '/v1/presence', { state: 'working', at: new Date().toISOString() })).ok, 'presence');
    await heard;
    await pv.locator('.pv-presence .pd.working').waitFor();
    assert(await compose.evaluate(el => el === document.activeElement), 'a redraw took focus off New message');
    // A new newest thread replaces the buttons; focus stays on New message.
    const posted = await asPeer('grok', 'POST', '/v1/messages', { idempotency_key: 'peer-actions-1', recipients: [me], priority: 'hold', thread_title: 'Review follow-up', content: { text: 'Second pass done.' } });
    assert(posted.ok, `grok send: ${posted.status}`);
    const follow = (await posted.json()).thread_id;
    assert((await newestShared(id('grok'))).thread_id === follow, 'the new thread is not the newest shared one');
    await acts.locator(`a.pv-open[href="#thread/${follow}"]`).waitFor();
    assert(await compose.evaluate(el => el === document.activeElement && el.matches(':focus-visible')), 'a new newest thread took focus off New message');
    await shot('pv-quick-focus');

    // The quick link goes to the thread itself.
    await open.click();
    await page.waitForFunction(h => location.hash === h && document.body.dataset.route === 'thread', '#thread/' + follow);

    // New message from the Grok Bot's page: prefilled, at wake_idle, the
    // message field focused.
    await route('#peer/' + id('grok'));
    await compose.waitFor();
    await compose.click();
    await composer();
    assert(JSON.stringify(await picked()) === JSON.stringify([id('grok')]), `grok draft recipients: ${await picked()}`);
    assert(await prio() === 'wake_idle', `grok draft priority: ${await prio()}`);
    assert(await page.evaluate(() => document.activeElement && document.activeElement.id === 'c-text'), 'the message field is not focused');
    await shot('pv-quick-compose-grokbot');

    // A later New message (the nav link) starts empty, at wake.
    await route('#dash');
    await page.locator('#nav-new').click();
    await composer();
    assert((await picked()).length === 0, 'a later New message kept the draft');
    assert(await prio() === 'wake', 'a later New message kept the priority');

    // ---- no thread: dot-research shares none with the operator ----
    assert(await newestShared(id('dot')) === null, 'fixture: dot-research already shares a thread');
    await route('#peer/' + id('dot'));
    await pv.locator('.pv-head h1', { hasText: 'dot-research' }).waitFor();
    await acts.locator('a.pv-open[href="#new"]').waitFor();
    assert((await open.innerText()).includes('Start thread'), 'no-thread quick link label');
    assert(await compose.count() === 1, 'New message missing with no thread');
    assert(await compose.locator('.pr').count() === 0, 'a dot\'s New message names a priority');
    await page.mouse.move(700, 5);
    await top();
    await shot('pv-quick-none');

    // A modified click is the browser's (a new tab): no draft is handed over,
    // so this tab's next New message is still empty.
    const tab = page.context().waitForEvent('page', { timeout: 5000 }).catch(() => null);
    await open.click({ modifiers: ['Control'] });
    const opened = await tab;
    if (opened) await opened.close();
    assert(await page.evaluate(() => location.hash) === '#peer/' + id('dot'), 'a Ctrl+click routed this tab');
    await page.locator('#nav-new').click();
    await composer();
    assert((await picked()).length === 0, 'a modified click handed over a draft');

    // The quick link opens New message to dot-research at wake; it sends.
    await route('#peer/' + id('dot'));
    await open.waitFor();
    await open.click();
    await composer();
    assert(JSON.stringify(await picked()) === JSON.stringify([id('dot')]), `dot draft recipients: ${await picked()}`);
    assert(await prio() === 'wake', `dot draft priority: ${await prio()}`);
    await page.keyboard.type('Quick link check.');
    await shot('pv-quick-compose-none');
    await page.locator('.compose button[type=submit]').click();
    await page.waitForFunction(() => location.hash.startsWith('#thread/'));
    const sent = await page.evaluate(() => location.hash.slice('#thread/'.length));
    const shared = await newestShared(id('dot'));
    assert(shared && shared.thread_id === sent, 'the send did not make a thread with dot-research');

    // Now the quick link finds that thread.
    await route('#peer/' + id('dot'));
    await acts.locator(`a.pv-open[href="#thread/${sent}"]`).waitFor();

    // ---- a source and the operator offer neither ----
    await route('#peer/' + id('gh'));
    await pv.locator('.pv-head h1', { hasText: 'gh-ultradyn' }).waitFor();
    await pv.locator('.pv-threads .none').waitFor();
    assert(await acts.locator('a, button').count() === 0, 'a source offers message actions');
    await route('#peer/' + me);
    await pv.locator('.pv-identity dl').waitFor();
    assert(await acts.locator('a, button').count() === 0, 'the operator\'s own page offers message actions');

    // Lifecycle mutations redraw in place; each slot keeps keyboard focus on
    // its replacement, not on the message quick link. Nothing later uses dot.
    await route('#peer/' + id('dot'));
    const mounted = await pv.elementHandle();
    const button = name => acts.getByRole('button', { name, exact: true });
    const activate = async name => { await button(name).focus(); await page.keyboard.press('Enter'); };
    const focused = async name => assert(await button(name).evaluate(el => el === document.activeElement), name + ' lost focus');
    await activate('Disable');
    await pv.locator('.pv-head h1 .chip', { hasText: 'disabled' }).waitFor();
    await button('Re-enable').waitFor();
    await focused('Re-enable');
    await activate('Re-enable');
    await button('Disable').waitFor();
    await focused('Disable');
    assert(await pv.locator('.pv-head h1 .chip').count() === 0, 'Re-enable left a disabled chip');
    await activate('Archive');
    await button('Confirm archive').waitFor();
    assert(await button('Unarchive').count() === 0, 'Archive skipped confirmation');
    await activate('Confirm archive');
    await pv.locator('.pv-head h1 .chip', { hasText: 'archived' }).waitFor();
    await button('Unarchive').waitFor();
    await focused('Unarchive');
    assert(await button('Disable').count() === 0 && await button('Archive').count() === 0 && await button('Re-enable').count() === 0, 'archived peer still offers active lifecycle actions');
    await activate('Unarchive');
    await pv.locator('.pv-head h1 .chip', { hasText: 'disabled' }).waitFor();
    await button('Re-enable').waitFor();
    await button('Archive').waitFor();
    await focused('Archive');
    assert(await button('Disable').count() === 0 && await button('Unarchive').count() === 0, 'Unarchive did not leave the peer disabled');
    assert(await mounted.evaluate(el => el.isConnected), 'lifecycle mutations remounted the peer page');
  });
  console.log('Peer actions passed: quick link to the newest shared thread, else New message prefilled; New message always beside it, wake_idle for a Grok Bot; hover, focus kept across a redraw, modified click, later New message empty, source and operator offer none; Disable/Re-enable and Archive/Unarchive redraw in place with focus');
} finally { await cleanBuild(); }
