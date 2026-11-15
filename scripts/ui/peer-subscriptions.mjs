// Disposable seeded hub only. Run through just bb on x-boss; no live hub,
// callback receiver, credentials file, npm install, or committed screenshots.
import { assert, cleanBuild, networkQuiet, shotPath, withDemo } from './lib.mjs';

const LIST = '/v1/admin/subscriptions';
const WRITE = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);
const SHELL_WRITE = /^\/v1\/(ack|presence|receipts)$/;
const gate = () => {
  let release;
  const promise = new Promise(resolve => { release = resolve; });
  return { promise, release };
};

// Reviewer regressions use the real page/store lifecycle and intercepted
// local API transport. Run all cases on RED so one defect cannot mask another.
async function reviewRegressions() {
  const failures = [];
  await withDemo({}, async ({ page, demo, asPeer, quiet, ignore, allowConsole }) => {
    page.setDefaultTimeout(15000);
    allowConsole(/Failed to load resource: the server responded with a status of 500/);
    const origin = new URL(demo.url).origin;
    const foreign = [];
    await page.context().route('**/*', async route => {
      if (new URL(route.request().url()).origin === origin) await route.fallback();
      else { foreign.push(route.request().method()); await route.abort('blockedbyclient'); }
    });
    const response = await asPeer('operator', 'GET', LIST);
    assert(response.ok, 'review fixture could not read disposable subscriptions');
    const seed = (await response.json()).filter(s => s.peer_id === demo.peers.dot.id).slice(0, 3)
      .map(s => ({ ...s, health: 'ok', health_note: '', paused_reason: null }));
    assert(seed.length === 3, 'review regressions require three seed subscriptions');
    const [a, b, c] = seed.map(s => s.sub_id);
    let rows, mode, readGate, deleteGate;
    const listMatch = url => url.pathname === LIST;
    const deleteMatch = url => url.pathname.startsWith(LIST + '/');
    await page.route(listMatch, async route => {
      if (route.request().method() !== 'GET') return route.fallback();
      const snapshot = structuredClone(rows);
      const fail = mode === 'fail';
      if (mode === 'hold') { mode = 'fail'; await readGate.promise; }
      await route.fulfill(fail
        ? { status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'internal', message: 'Synthetic refresh failure' } }) }
        : { status: 200, contentType: 'application/json', body: JSON.stringify(snapshot) });
    });
    await page.route(deleteMatch, async route => {
      if (route.request().method() !== 'DELETE') return route.fallback();
      assert(seed.some(s => new URL(route.request().url()).pathname === LIST + '/' + s.sub_id), 'review DELETE must use an exact seeded ID');
      assert(route.request().postData() === null, 'review DELETE must have no body');
      if (deleteGate) await deleteGate.promise;
      await route.fulfill({ status: 204 });
    });
    const section = page.locator('section.pv-subs');
    const dialog = page.locator('dialog.pv-sub-confirm');
    const card = id => section.locator(`.pv-sub-card[data-sub="${id}"]`);
    const remove = id => card(id).getByRole('button', { name: 'Remove subscription ' + id, exact: true });
    const ids = () => section.locator('.pv-sub-card').evaluateAll(nodes => nodes.map(n => n.dataset.sub));
    const focusedID = () => page.evaluate(() => document.activeElement?.closest('.pv-sub-card')?.dataset.sub || null);
    const open = async id => {
      await remove(id).click();
      await page.getByRole('dialog', { name: 'Remove subscription?', exact: true }).waitFor();
    };
    const confirm = () => dialog.getByRole('button', { name: 'Remove subscription', exact: true }).click();
    const refresh = async () => {
      const read = page.waitForResponse(r => new URL(r.url()).pathname === LIST && r.request().method() === 'GET');
      await page.clock.fastForward(30001);
      await read;
      await quiet();
    };
    const reset = async () => {
      readGate?.release(); deleteGate?.release();
      rows = structuredClone(seed); mode = 'ok'; readGate = null; deleteGate = null;
      await page.reload({ waitUntil: 'load' });
      await page.evaluate(id => { location.hash = '#peer/' + id; }, demo.peers.dot.id);
      await quiet();
      assert(JSON.stringify(await ids()) === JSON.stringify([a, b, c]), 'review fixture failed to establish A/B/C visible order');
    };
    const check = async (name, fn) => {
      try { await reset(); await fn(); console.log('PASS REVIEW ' + name); }
      catch (error) { failures.push(name + ': ' + error.message); console.log('FAIL REVIEW ' + name + ': ' + error.message); }
      finally { readGate?.release(); deleteGate?.release(); }
    };
    await page.clock.install();
    await check('R1 remount before late DELETE, stale GET, failed follow-up', async () => {
      deleteGate = gate();
      const unignore = ignore(req => new URL(req.url()).pathname.startsWith(LIST));
      try {
        await open(a);
        const sent = page.waitForRequest(req => req.method() === 'DELETE' && new URL(req.url()).pathname === LIST + '/' + a);
        await confirm(); await sent;
        await page.evaluate(id => { location.hash = '#peer/' + id; }, demo.peers.codex.id);
        await dialog.waitFor({ state: 'detached' });
        await quiet();
        mode = 'hold'; readGate = gate();
        const staleRead = page.waitForRequest(req => req.method() === 'GET' && new URL(req.url()).pathname === LIST);
        await page.evaluate(id => { location.hash = '#peer/' + id; }, demo.peers.dot.id);
        await staleRead;
        const deleted = page.waitForResponse(r => r.request().method() === 'DELETE' && new URL(r.url()).pathname === LIST + '/' + a);
        deleteGate.release();
        assert((await deleted).status() === 204, 'late deletion did not succeed');
        await quiet();
        const failedRefresh = page.waitForResponse(r => new URL(r.url()).pathname === LIST && r.status() === 500);
        readGate.release();
        await failedRefresh;
        await quiet();
        await section.getByRole('button', { name: 'Retry', exact: true }).waitFor();
        const visible = await ids();
        assert(JSON.stringify(visible) === JSON.stringify([b, c]), `confirmed deleted ID resurrected after remount: ${JSON.stringify(visible)}`);
      } finally { unignore(); }
    });
    await check('R2 bare IPv6 host retained in card and modal', async () => {
      const ipv6 = '2606:4700:4700::1111';
      rows[0].url = ipv6;
      await refresh();
      await open(a);
      const host = await card(a).locator('.pv-sub-host').innerText();
      const modal = await dialog.innerText();
      assert(host.includes(ipv6) && modal.includes(ipv6) && !modal.includes('Unknown host'),
        `bare IPv6 identification lost: card=${JSON.stringify(host)}, modalHasIPv6=${modal.includes(ipv6)}`);
      await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
    });
    await check('R3 reordered focused row disappears externally', async () => {
      rows[2].health = 'failing';
      await refresh();
      assert(JSON.stringify(await ids()) === JSON.stringify([c, a, b]), 'health change did not reorder C/A/B');
      await remove(c).focus();
      rows = rows.filter(s => s.sub_id !== c);
      await refresh();
      assert(await focusedID() === a, `external removal must focus next visible A, got ${await focusedID()}`);
    });
    await check('R3 modal target reorders before confirmed removal', async () => {
      await open(c);
      rows[2].health = 'failing';
      await refresh();
      assert(JSON.stringify(await ids()) === JSON.stringify([c, a, b]), 'open modal health change did not reorder C/A/B');
      assert(await dialog.isVisible(), 'reordering closed confirmation');
      rows = rows.filter(s => s.sub_id !== c);
      await confirm();
      await dialog.waitFor({ state: 'detached' });
      await quiet();
      assert(await focusedID() === a, `confirmed removal must use current visible position and focus A, got ${await focusedID()}`);
    });
    assert(!foreign.length, 'review regressions attempted a cross-origin request');
  });
  assert(!failures.length, 'Browser review regressions failed:\n' + failures.join('\n'));
}

try {
  await reviewRegressions();
  if (process.env.PEER_SUB_REVIEW_ONLY !== '1') await withDemo({}, async ctx => {
    const { page, demo, asPeer, quiet, countRequests, ignore, allowConsole } = ctx;
    const origin = new URL(demo.url).origin;
    const foreign = [];
    // Enforce the boundary even if a regression tries fetching a callback URL.
    await page.context().route('**/*', async route => {
      if (new URL(route.request().url()).origin !== origin) {
        foreign.push(route.request().method());
        await route.abort('blockedbyclient');
      } else await route.fallback();
    });
    allowConsole(/Failed to load resource: the server responded with a status of (500|404)/);
    page.setDefaultTimeout(15000);
    const section = page.locator('section.pv-subs');
    const cards = section.locator('.pv-sub-card[data-sub]');
    const dialog = page.locator('dialog.pv-sub-confirm');
    const cancel = () => dialog.getByRole('button', { name: 'Cancel', exact: true });
    const confirm = () => dialog.getByRole('button', { name: 'Remove subscription', exact: true });
    const card = id => section.locator(`.pv-sub-card[data-sub="${id}"]`);
    const remove = id => card(id).getByRole('button', { name: 'Remove subscription ' + id, exact: true });
    const ids = () => cards.evaluateAll(nodes => nodes.map(n => n.dataset.sub));
    const writes = () => countRequests(req => WRITE.has(req.method()) && !SHELL_WRITE.test(new URL(req.url()).pathname));
    const deletes = id => countRequests(req => req.method() === 'DELETE' && new URL(req.url()).pathname === LIST + '/' + id);
    const list = async () => {
      const response = await asPeer('operator', 'GET', LIST);
      assert(response.ok, `local subscriptions GET: ${response.status}`);
      return response.json();
    };
    const navigate = async id => {
      await page.evaluate(id => { location.hash = '#peer/' + id; }, id);
      await page.locator('.pv-head h1').waitFor();
      await quiet();
    };
    const waitCount = async n => {
      await page.waitForFunction(n => document.querySelectorAll('section.pv-subs .pv-sub-card[data-sub]').length === n, n);
      const heading = (await section.locator('h2').textContent()).replace(/\s+/g, ' ').trim();
      assert(heading === 'Subscriptions ' + n, `heading count must be ${n}, got ${heading}`);
    };
    const focused = async selector => {
      await page.waitForFunction(selector => document.activeElement?.matches(selector), selector);
    };
    const focusedRemove = id => focused(`.pv-sub-card[data-sub="${id}"] button.pv-sub-remove`);
    const open = async (id, keyboard = false) => {
      const before = writes();
      if (keyboard) { await remove(id).focus(); await page.keyboard.press('Enter'); }
      else await remove(id).click();
      await dialog.waitFor();
      assert(await page.getByRole('dialog', { name: 'Remove subscription?', exact: true }).count() === 1,
        'native dialog must have the agreed accessible name');
      assert(await dialog.evaluate(el => el.open && el.matches(':modal')), 'confirmation is not a native modal');
      await focused('dialog.pv-sub-confirm button:first-of-type');
      assert(await cancel().evaluate(el => el === document.activeElement), 'Cancel must have default focus');
      assert(writes() === before, 'opening confirmation sent a write');
    };
    const dismiss = async (id, keyboard = false) => {
      const before = writes();
      if (keyboard) await page.keyboard.press('Escape'); else await cancel().click();
      await dialog.waitFor({ state: 'detached' });
      await focusedRemove(id);
      assert(writes() === before, 'cancelling confirmation sent a write');
    };
    const checkImpact = async row => {
      const text = await dialog.innerText();
      assert(text.includes(row.sub_id) && text.includes(row.url), 'confirmation must identify the exact ID and host');
      for (const pattern of [/stop sending events/i, /queued callback attempts/i, /already in flight/i,
        /peer.*messages.*history.*stay/i, /active client.*subscribe again/i]) {
        assert(pattern.test(text), `confirmation missing impact: ${pattern}`);
      }
    };
    const fit = async selector => {
      const bad = await page.locator(selector).evaluateAll(nodes => nodes.flatMap(el => {
        const r = el.getBoundingClientRect();
        return r.left < -1 || r.right > innerWidth + 1 || el.scrollWidth > el.clientWidth + 1
          ? [el.className] : [];
      }));
      assert(!bad.length, `horizontal overflow at ${page.viewportSize().width}px: ${bad.join(', ')}`);
    };
    const screenshot = async (locator, name) => {
      if (process.env.PEER_SUB_SKIP_SHOTS === '1') return;
      await page.evaluate(() => document.fonts.ready);
      const path = await shotPath(name);
      // Chromium can composite the offscreen fixed skip link into tall
      // element crops. Temporarily hide that unrelated shell affordance
      // with its existing hidden rule; injected styles violate the real CSP.
      const skip = page.locator('.skip');
      const hidden = locator === section ? await skip.evaluate(el => { const old = el.hidden; el.hidden = true; return old; }) : null;
      try { await locator.screenshot({ path, animations: 'disabled' }); }
      finally { if (hidden !== null) await skip.evaluate((el, old) => { el.hidden = old; }, hidden); }
      console.log('SCREENSHOT ' + path);
    };

    // Install before mounting the peer: existing native timers cannot be
    // retroactively controlled by Playwright's clock.
    await page.clock.install();
    await navigate(demo.peers.dot.id);
    assert(await cards.count() === 6,
      'expected six subscription cards with removal controls; old read-only table is not acceptable');
    await waitCount(6);
    const initial = (await list()).filter(s => s.peer_id === demo.peers.dot.id);
    assert(initial.length === 6, 'seed must provide six dot subscriptions');
    const byID = new Map(initial.map(s => [s.sub_id, s]));
    const ordered = await ids();
    let healthy = false;
    for (const id of ordered) {
      const row = byID.get(id);
      assert(row, 'card is not an exact seeded subscription');
      if (row.health === 'ok') healthy = true;
      else assert(!healthy, 'a problem subscription appears after a healthy subscription');
      assert(await remove(id).count() === 1, 'missing exact accessible Remove subscription ID');
      assert(await remove(id).innerText() === 'Remove', 'card action must say Remove');
      assert(await card(id).locator('.pv-sub-note').isVisible(), 'health explanation is only available on hover');
      const text = await card(id).innerText();
      assert(text.includes(row.sub_id) && text.includes(row.name) && text.includes(row.url), 'card lacks full ID, event or host');
      if (row.health_note) assert(text.includes(row.health_note), 'card omits the server health explanation');
      for (const label of ['Expires', 'Last delivery', 'Attempts']) assert(text.includes(label), `card lacks ${label}`);
    }
    const attention = initial.filter(s => s.health !== 'ok').length;
    assert((await section.locator('.pv-sub-summary').innerText()).includes(String(attention)), 'attention summary is not aggregated');
    console.log('PASS six cards, exact IDs and labels, all visible health notes, facts, host and problem-first order');

    // Screenshots capture the entire section (all six cards), plus the modal
    // in its page context, at both phone widths and desktop in both themes.
    const first = ordered[0];
    for (const theme of ['dark', 'light']) {
      await page.emulateMedia({ colorScheme: theme });
      await page.waitForFunction(theme => document.documentElement.dataset.theme === theme, theme);
      for (const width of [1440, 390, 320]) {
        await page.setViewportSize({ width, height: width === 1440 ? 1000 : 900 });
        await fit('section.pv-subs, .pv-sub-card, .pv-sub-remove');
        await screenshot(section, `peer-subs-${theme}-${width}-cards`);
        await open(first, true);
        await checkImpact(byID.get(first));
        await fit('dialog.pv-sub-confirm, .pv-sub-confirm button');
        await screenshot(page, `peer-subs-${theme}-${width}-confirm`);
        // Native dialogs also permit browser-chrome focus at the boundary;
        // verify the actions' order and that page content is inert.
        await page.keyboard.press('Tab');
        assert(await confirm().evaluate(el => el === document.activeElement), 'Tab did not reach confirmation');
        await page.keyboard.press('Shift+Tab');
        assert(await cancel().evaluate(el => el === document.activeElement), 'Shift+Tab did not return to Cancel');
        await remove(first).evaluate(el => el.focus());
        assert(await cancel().evaluate(el => el === document.activeElement), 'background page is not inert');
        await dismiss(first, true);
      }
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.emulateMedia({ colorScheme: 'dark' });
    await open(first);
    await dismiss(first);
    console.log('PASS desktop/390/320 dark/light layout, modal identity and impact, default Cancel, keyboard order, inert background and cancellation without writes');

    // Control the existing 30-second refresh, without wall-clock sleeps.
    const refresh = async () => {
      const read = page.waitForRequest(req => req.method() === 'GET' && new URL(req.url()).pathname === LIST);
      await page.clock.fastForward(30001);
      await read;
      await quiet();
    };
    await remove(first).focus();
    await refresh();
    await focusedRemove(first);
    await open(first);
    const modalNode = await dialog.elementHandle();
    await refresh();
    assert(await modalNode.evaluate(el => el.isConnected && el.open), 'unrelated refresh replaced the modal');
    assert(await cancel().evaluate(el => el === document.activeElement), 'unrelated refresh stole modal focus');
    await dismiss(first);
    console.log('PASS background refresh preserves keyed button and modal focus');

    // Real endpoint: delete one broken and one healthy subscription, leaving
    // every other exact ID and the peer/history intact; reload proves storage.
    for (const health of ['broken', 'healthy']) {
      const before = await ids();
      const id = before.find(id => (byID.get(id).health === 'ok') === (health === 'healthy'));
      assert(id, `no ${health} seed available for real removal`);
      const index = before.indexOf(id);
      const next = before[index + 1] || before[index - 1];
      await open(id, true);
      const response = page.waitForResponse(r => r.request().method() === 'DELETE' && new URL(r.url()).pathname === LIST + '/' + id);
      await confirm().focus();
      await page.keyboard.press('Enter');
      assert((await response).status() === 204, 'real DELETE must return 204');
      await dialog.waitFor({ state: 'detached' });
      await waitCount(before.length - 1);
      await focusedRemove(next);
      await quiet();
      assert(JSON.stringify(await ids()) === JSON.stringify(before.filter(x => x !== id)), 'real deletion removed or reordered another row');
      const stored = (await list()).filter(s => s.peer_id === demo.peers.dot.id).map(s => s.sub_id);
      assert(stored.length === before.length - 1 && !stored.includes(id) && stored.every(x => before.includes(x)), 'real removal did not persist exact-ID isolation');
      const status = section.locator('.pv-sub-status[role=status]');
      assert(await status.isVisible() && /removed/i.test(await status.innerText()), 'success status is not persistent and announced');
      await refresh();
      assert(await status.isVisible(), 'background refresh cleared the removal status');
      await page.reload({ waitUntil: 'load' });
      await quiet();
      await waitCount(before.length - 1);
      assert(!((await ids()).includes(id)), 'removed subscription returned after page reload');
      console.log(`PASS real ${health} DELETE 204, exact-row isolation, count, focus, persistent status, storage and reload`);
    }

    // Deterministic transport fixtures: no backend deletion or callback
    // request is needed to prove duplicate/error/404/stale-read semantics.
    let fixture = (await list()).filter(s => s.peer_id === demo.peers.dot.id);
    let getMode = 'ok', getGate = null, getSeen = null;
    const listRoute = async route => {
      if (route.request().method() !== 'GET') return route.fallback();
      const snapshot = structuredClone(fixture);
      const mode = getMode, pending = getGate;
      getSeen?.release();
      if (pending) await pending.promise;
      await route.fulfill(mode === 'error'
        ? { status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'internal', message: 'unsafe read backend detail' } }) }
        : { status: 200, contentType: 'application/json', body: JSON.stringify(snapshot) });
    };
    const listMatch = url => url.pathname === LIST;
    await page.route(listMatch, listRoute);
    let deleteMode = 'error', deleteGate = null, deleteSeen = null;
    const deleteRoute = async route => {
      if (route.request().method() !== 'DELETE') return route.fallback();
      const id = decodeURIComponent(new URL(route.request().url()).pathname.slice(LIST.length + 1));
      assert(fixture.some(s => s.sub_id === id), 'DELETE used something other than an exact subscription ID');
      assert(route.request().postData() === null, 'DELETE contains an unexpected body');
      const mode = deleteMode, pending = deleteGate;
      deleteSeen?.release();
      if (pending) await pending.promise;
      if (mode === 'real') return route.continue();
      await route.fulfill(mode === 'error'
        ? { status: 500, contentType: 'application/json', body: JSON.stringify({ error: { code: 'internal', message: 'https://user:credential@callback.invalid/private <img src=x onerror=alert(1)>' } }) }
        : mode === 'absent'
          ? { status: 404, contentType: 'application/json', body: JSON.stringify({ error: { code: 'not_found', message: 'no such subscription' } }) }
          : { status: 204 });
    };
    const deleteMatch = url => url.pathname.startsWith(LIST + '/');
    await page.route(deleteMatch, deleteRoute);
    const releaseAll = () => { getGate?.release(); deleteGate?.release(); };
    try {
      const id = (await ids())[0];
      deleteGate = gate(); deleteSeen = gate();
      await open(id);
      const count = deletes(id);
      await confirm().dblclick();
      await deleteSeen.promise;
      await dialog.getByRole('button', { name: 'Removing…', exact: true }).waitFor();
      assert(await dialog.getAttribute('aria-busy') === 'true', 'pending confirmation is not marked busy');
      assert(await cancel().getAttribute('aria-disabled') === 'true', 'pending Cancel lacks disabled semantics');
      await cancel().click({ force: true });
      await page.keyboard.press('Escape');
      await dialog.locator('button').last().click({ force: true });
      assert(await dialog.isVisible() && deletes(id) === count + 1, 'pending cancel/Escape/duplicate activation escaped the guard');
      deleteGate.release(); deleteGate = null;
      await dialog.locator('.pv-sub-error', { hasText: 'Could not remove this subscription. Try again.' }).waitFor();
      assert((await dialog.locator('.pv-sub-error').innerText()) === 'Could not remove this subscription. Try again.', 'unsafe server error reached inline copy');
      assert(await card(id).count() === 1 && await dialog.isVisible(), 'failed deletion removed the row or closed the modal');
      assert(!((await page.locator('body').innerText()).includes('callback.invalid')), 'backend error leaked callback data');
      await screenshot(page, 'peer-subs-delete-error');
      console.log('PASS delayed 500, pending duplicate/cancel/Escape guard, fixed inline error, row retained');

      // Retry returns 404: it is success. Hold an older GET until after that
      // success, then return the stale snapshot on subsequent reads as well.
      getGate = gate(); getSeen = gate();
      const ignored = ignore(req => req.method() === 'GET' && new URL(req.url()).pathname === LIST);
      await page.clock.fastForward(30001);
      await getSeen.promise;
      deleteMode = 'absent';
      const before = await ids();
      const next = before[1];
      const response = page.waitForResponse(r => r.request().method() === 'DELETE' && new URL(r.url()).pathname === LIST + '/' + id);
      await confirm().click();
      assert((await response).status() === 404, '404 fixture did not reach the page');
      await dialog.waitFor({ state: 'detached' });
      await focusedRemove(next);
      getGate.release(); getGate = null; ignored();
      await quiet();
      await waitCount(before.length - 1);
      await refresh();
      assert(await card(id).count() === 0, 'stale GET resurrected the removed subscription');
      assert(/already removed/i.test(await section.locator('.pv-sub-status').innerText()), '404 absence is not announced');
      fixture = fixture.filter(s => s.sub_id !== id);
      console.log('PASS retry, DELETE 404 as absent, next focus, held stale GET and repeated stale reads cannot resurrect');

      // A read failure retains cached cards and offers a real retry.
      getMode = 'error';
      const retained = await ids();
      await refresh();
      await section.getByRole('button', { name: 'Retry', exact: true }).waitFor();
      assert(JSON.stringify(await ids()) === JSON.stringify(retained), 'read failure discarded cached subscriptions');
      assert(/last loaded|stale/i.test(await section.innerText()), 'retained cache is not visibly stale');
      getMode = 'ok';
      await section.getByRole('button', { name: 'Retry', exact: true }).click();
      await quiet();
      assert(await section.getByRole('button', { name: 'Retry', exact: true }).isHidden(), 'successful retry did not clear read error');
      console.log('PASS failed refresh retains visibly stale cards, successful Retry clears the error');

      // Model another tab's removal by changing the next server snapshot.
      const other = (await ids())[0];
      await open(other);
      fixture = fixture.filter(s => s.sub_id !== other);
      const beforeWrites = writes();
      await refresh();
      await dialog.waitFor({ state: 'detached' });
      assert(writes() === beforeWrites && await card(other).count() === 0, 'external disappearance caused a write or retained target');
      await focusedRemove((await ids())[0]);
      assert(/elsewhere/i.test(await section.locator('.pv-sub-status').innerText()), 'external removal is not explained');
      console.log('PASS externally disappeared modal target closes safely without another write');

      // Remove the last row first (previous focus), then the only remaining
      // row (heading focus). These synthetic successes keep the seed intact.
      deleteMode = 'success';
      for (;;) {
        const remaining = await ids();
        if (!remaining.length) break;
        const id = remaining.at(-1);
        await open(id, true);
        await confirm().click();
        await dialog.waitFor({ state: 'detached' });
        fixture = fixture.filter(s => s.sub_id !== id);
        await waitCount(remaining.length - 1);
        if (remaining.length > 1) await focusedRemove(remaining.at(-2));
        else await focused('section.pv-subs h2');
      }
      assert(/No subscriptions/i.test(await section.locator('.pv-sub-summary').innerText()), 'empty summary missing');
      await screenshot(section, 'peer-subs-empty');
      console.log('PASS previous-row focus, last-row heading focus and empty summary');

      // Restore the real list on a fresh page, and delay a real write until
      // navigation. Late completion must update storage/shared dashboard and
      // must leave the replacement page's focus and DOM alone.
    } finally {
      releaseAll();
      await page.unroute(listMatch, listRoute);
      await page.unroute(deleteMatch, deleteRoute);
    }
    await page.reload({ waitUntil: 'load' });
    await quiet();
    const realRows = await list();
    const lateID = (await ids())[0];
    assert(lateID && realRows.some(s => s.sub_id === lateID), 'missing real target for late completion');
    const lateGate = gate(), lateSeen = gate();
    const unignoreLate = ignore(req => req.method() === 'DELETE' && new URL(req.url()).pathname === LIST + '/' + lateID);
    const lateRoute = async route => { lateSeen.release(); await lateGate.promise; await route.continue(); };
    await page.route(demo.url + LIST + '/' + lateID, lateRoute);
    try {
      await open(lateID);
      const response = page.waitForResponse(r => r.request().method() === 'DELETE' && new URL(r.url()).pathname === LIST + '/' + lateID);
      await confirm().click();
      await lateSeen.promise;
      await navigate(demo.peers.codex.id);
      await dialog.waitFor({ state: 'detached' });
      const heading = page.locator('.pv-head h1');
      // The heading is routinely replaced when directory facts refresh.
      // The peer's persistent New message action is a real keyboard target.
      const replacementFocus = page.locator('.pv-head .pv-new');
      await replacementFocus.focus();
      const before = await heading.innerText();
      lateGate.release();
      assert((await response).status() === 204, 'late real removal failed');
      await quiet();
      assert(await heading.innerText() === before && await replacementFocus.evaluate(el => el === document.activeElement), 'late completion touched replacement page or stole focus');
      assert(await dialog.count() === 0 && await section.locator('.pv-sub-status').innerText() === '', 'late completion wrote to replacement section');
      await navigate(demo.peers.dot.id);
      assert(await card(lateID).count() === 0 && !(await list()).some(s => s.sub_id === lateID), 'late removal did not invalidate data or persist');
      console.log('PASS navigation unmount closes pending modal; late real 204 preserves replacement focus and fresh data');
    } finally { lateGate.release(); unignoreLate(); await page.unroute(demo.url + LIST + '/' + lateID, lateRoute); }
    // Real two-tab event integration: no polling-clock advancement or GET
    // fixture. The first tab must consume operator_view(reason=subscription)
    // and refresh its own section after the second tab's real DELETE.
    const otherPage = await page.context().newPage();
    otherPage.setDefaultTimeout(15000);
    const otherNet = networkQuiet(otherPage);
    const otherErrors = [];
    otherPage.on('pageerror', error => otherErrors.push(error.message));
    try {
      await otherPage.goto(demo.url + '/ui/#peer/' + demo.peers.dot.id, { waitUntil: 'load' });
      await otherPage.locator('#conn.live').waitFor();
      await otherNet.quiet();
      await page.locator('#conn.live').waitFor();
      const target = (await ids())[0];
      assert(target, 'missing real subscription for second-tab test');
      await open(target);
      const primaryDeletes = deletes(target);
      const event = page.waitForResponse(async response => {
        if (new URL(response.url()).pathname !== '/v1/events' || response.status() !== 200) return false;
        try {
          return (await response.json()).events?.some(e => e.type === 'operator_view' && e.data?.reason === 'subscription');
        } catch { return false; }
      });
      const fresh = page.waitForResponse(response => new URL(response.url()).pathname === LIST && response.request().method() === 'GET' && response.status() === 200);
      await otherPage.locator(`.pv-sub-card[data-sub="${target}"]`).getByRole('button', { name: 'Remove subscription ' + target, exact: true }).click();
      const otherDialog = otherPage.getByRole('dialog', { name: 'Remove subscription?', exact: true });
      await otherDialog.getByRole('button', { name: 'Remove subscription', exact: true }).click();
      await otherDialog.waitFor({ state: 'detached' });
      await event;
      const refreshed = await (await fresh).json();
      assert(!refreshed.some(s => s.sub_id === target), 'event refresh returned the removed ID');
      await dialog.waitFor({ state: 'detached' });
      await card(target).waitFor({ state: 'detached' });
      assert(deletes(target) === primaryDeletes, 'first tab wrote after external removal');
      await focusedRemove((await ids())[0]);
      assert(/elsewhere/i.test(await section.locator('.pv-sub-status').innerText()), 'real external disappearance is not explained');
      assert(!otherErrors.length, 'second tab had a page error');
      console.log('PASS real second-tab DELETE emits operator_view reason=subscription; first tab GET refresh removes row and safely closes modal without a write');
    } finally { await otherPage.close(); }
    assert(!foreign.length, 'page attempted a cross-origin request');
    console.log('Peer subscription acceptance passed; disposable local requests only.');
  });
} finally { await cleanBuild(); }
