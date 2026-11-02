// Task 11: the delivery matrix dialog in a real browser. Every wait is on page
// state, never on elapsed time: the build slice is shared.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const OPENER = 'mx-opener';
const TERMINAL = ['processed', 'failed', 'dead_letter', 'expired'];
const deliveries = url => url.pathname.endsWith('/deliveries');
const ok = name => console.log('ok - ' + name);

// Task 10's side panel owns the real "Delivery matrix" button, which calls
// ctx.nav('#matrix/<id>'). Until it is on the page, a stand-in that does the
// same is added beside the view (outside the parts that re-render).
async function opener(page, threadId) {
  const real = page.locator('#aside').getByRole('button', { name: /Delivery matrix/ });
  if (await real.count()) return real.first();
  await page.evaluate(({ id, name }) => {
    document.getElementById(name)?.remove();
    const b = document.createElement('button');
    b.id = name;
    b.type = 'button';
    b.className = 'btn';
    b.textContent = 'Delivery matrix';
    b.addEventListener('click', () => { location.hash = '#matrix/' + id; });
    document.getElementById('main').append(b);
  }, { id: threadId, name: OPENER });
  return page.locator('#' + OPENER);
}

const focused = locator => locator.evaluate(el => el === document.activeElement);

// The scale seed (63 peers, 500 messages) shares a two-core slice with other
// agents and can overrun lib.mjs's fixed startup window. That one failure is
// retried; anything else, or a second timeout, fails the run.
async function withScaleDemo(fn) {
  for (let attempt = 1; ; attempt++) {
    try { return await withDemo({ scale: true }, fn); } catch (e) {
      if (attempt >= 2 || !/demo startup timed out/.test(e && e.message)) throw e;
      console.log('# scale demo startup timed out; retrying once');
    }
  }
}

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, fail, allowConsole } = ctx;
    allowConsole(/status of 503/);
    // See shell.mjs: hold() unroutes on release, and an empty route list makes
    // Playwright drop interception under a parked handler.
    await page.route('**/__keep-interception__', route => route.continue());

    const hark = demo.threads.hark;
    await page.evaluate(id => { location.hash = '#thread/' + id; }, hark);
    await page.locator('.msg').first().waitFor();
    await page.locator('#conn.live').waitFor();
    const open = await opener(page, hark);
    assert(await page.locator('dialog.mx').count() === 0, 'the matrix is on the page before anyone opened it');

    // Progress: the load is held, so the dialog shows its skeleton.
    const release = await hold(deliveries);
    await open.click();
    await page.locator('dialog.mx[open] .mx-sk').waitFor();
    assert(await page.evaluate(() => location.hash) === '#matrix/' + hark, 'opening did not route to #matrix');
    assert(await page.locator('dialog.mx .mx-grid').count() === 0, 'grid drawn before its load');
    await shot('11-matrix-progress');
    await release();

    // Initial: the matrix the hub reports, cell for cell.
    await page.locator('dialog.mx .mx-grid').waitFor();
    const before = await (await asPeer('operator', 'GET', `/v1/admin/threads/${hark}/deliveries`)).json();
    assert(await page.locator('dialog.mx .mx-h').count() === before.recipients.length, 'one column per recipient');
    assert(await page.locator('dialog.mx .mx-m').count() === before.rows.length, 'one row per message');
    assert(await page.locator('dialog.mx .mx-grid').evaluate(el => el.style.getPropertyValue('--cols')) === String(before.recipients.length), '--cols is not the recipient count');
    const cells = before.rows.flatMap(r => r.cells);
    for (const c of cells) {
      const st = await page.locator(`dialog.mx .mx-b[data-dly="${c.delivery_id}"]`).getAttribute('data-state');
      assert(st === c.display, `cell ${c.delivery_id} shows ${st}, hub says ${c.display}`);
    }
    const selfCells = before.rows.reduce((n, r) => n + (before.recipients.includes(r.sender) ? 1 : 0), 0);
    assert(selfCells > 0 && await page.locator('dialog.mx .mx-b.st-self').count() === selfCells, 'sender cells are not hatched');
    for (const label of ['queued', 'pushed', 'accepted', 'read', 'processed', 'dead letter', 'sender']) {
      await page.locator('dialog.mx .mx-legend span', { hasText: label }).first().waitFor();
    }
    // A cell click names the delivery, its attempt, last status and update time.
    const pick = cells.find(c => c.state !== 'queued') || cells[0];
    await page.locator(`dialog.mx .mx-b[data-dly="${pick.delivery_id}"]`).click();
    const shown = await page.locator('dialog.mx .mx-detail').textContent();
    for (const want of [pick.delivery_id, 'attempt', 'last status', 'updated']) assert(shown.includes(want), `detail lacks ${want}: ${shown}`);
    await shot('11-matrix-initial');

    await page.keyboard.press('Escape');
    await page.locator('dialog.mx').waitFor({ state: 'detached' });
    await page.waitForFunction(id => location.hash === '#thread/' + id, hark);
    assert(await focused(open), 'focus did not return to the opener');
    assert(await page.locator('.composer textarea').count() === 1, 'closing the matrix left the thread');
    ok('Esc closes and focus returns to the button');

    // A receipt from a peer reaches the open matrix through the event pump.
    const target = before.rows.filter(r => r.sender === demo.operator_peer).flatMap(r => r.cells)
      .find(c => c.recipient === demo.peers.codex.id && !TERMINAL.includes(c.state));
    assert(target, 'no open delivery from the operator to codex in hark');
    await open.click();
    const cell = page.locator(`dialog.mx .mx-b[data-dly="${target.delivery_id}"]`);
    await cell.waitFor();
    assert(await cell.getAttribute('data-state') !== 'processed', 'target cell already processed');
    await cell.click();
    await page.evaluate(() => { document.querySelector('dialog.mx').dataset.probe = 'kept'; });
    const sent = await asPeer('codex', 'POST', '/v1/receipts', { delivery_id: target.delivery_id, state: 'processed' });
    assert(sent.status === 204, `receipt failed: ${sent.status}`);
    await page.locator(`dialog.mx .mx-b[data-dly="${target.delivery_id}"][data-state="processed"]`).waitFor();
    assert(await page.locator('dialog.mx').getAttribute('data-probe') === 'kept', 'the dialog was rebuilt instead of refreshed');
    await page.locator('dialog.mx .mx-detail dd', { hasText: 'processed' }).waitFor();
    assert((await page.locator('dialog.mx .mx-detail').textContent()).includes(target.delivery_id), 'selection lost on refresh');
    await shot('11-matrix-success');
    ok('a receipt from asPeer recolours its cell while open');
    await page.getByRole('button', { name: 'Close' }).click();
    await page.locator('dialog.mx').waitFor({ state: 'detached' });
    assert(await focused(open), 'focus did not return after ×');

    // Error: the load fails, Retry recovers once the hub answers.
    await fail(deliveries, 503, 'unavailable', 'Hub restarting');
    await open.click();
    await page.locator('dialog.mx .empty.error', { hasText: 'Hub restarting' }).waitFor();
    await page.locator('dialog.mx').getByRole('button', { name: 'Retry' }).waitFor();
    await shot('11-matrix-error');
    await page.unroute(deliveries);
    await page.locator('dialog.mx').getByRole('button', { name: 'Retry' }).click();
    await page.locator('dialog.mx .mx-grid').waitFor();
    await page.keyboard.press('Escape');
    await page.locator('dialog.mx').waitFor({ state: 'detached' });

    // Bodies are untrusted: a hostile excerpt is inert text in the grid.
    await page.evaluate(id => { location.hash = '#matrix/' + id; }, demo.threads.hostile);
    await page.locator('dialog.mx .mx-ex', { hasText: '<img src=x onerror=alert(1)>' }).first().waitFor();
    assert(await page.locator('img').count() === 0, 'an excerpt became an element');
    await page.keyboard.press('Escape');
    await page.locator('dialog.mx').waitFor({ state: 'detached' });
  });

  await withScaleDemo(async ctx => {
    const { page, demo, asPeer } = ctx;
    const wide = demo.threads.participants;
    // One peer writes back, so the operator is a recipient too: 64 columns.
    const posted = await asPeer('scale-00', 'POST', '/v1/messages', { idempotency_key: 'mx-wide', thread_id: wide, priority: 'hold', content: { text: 'Back to the operator.' } });
    assert(posted.ok, `scale send failed: ${posted.status}`);
    await page.locator('#conn.live').waitFor();
    await page.evaluate(id => { location.hash = '#matrix/' + id; }, wide);
    await page.locator('dialog.mx .mx-grid').waitFor();
    assert(await page.locator('dialog.mx .mx-h').count() === 64, 'not 64 recipient columns');
    assert(await page.locator('dialog.mx .mx-grid').evaluate(el => el.style.getPropertyValue('--cols')) === '64', '--cols is not 64');
    const wideMatrix = await (await asPeer('operator', 'GET', `/v1/admin/threads/${wide}/deliveries`)).json();
    assert(wideMatrix.recipients.includes(demo.peers['scale-00'].id), 'the writing peer is not a recipient');
    const wideSelf = wideMatrix.rows.filter(r => wideMatrix.recipients.includes(r.sender)).length;
    assert(wideSelf >= 2 && await page.locator('dialog.mx .mx-b.st-self').count() === wideSelf, 'sender cells are not hatched');
    const fit = () => page.evaluate(() => {
      const s = document.querySelector('dialog.mx .mx-scroll'), d = document.querySelector('dialog.mx').getBoundingClientRect();
      const de = document.documentElement;
      return { sw: s.scrollWidth, cw: s.clientWidth, pw: de.scrollWidth, ph: de.scrollHeight, iw: de.clientWidth, ih: de.clientHeight, d: [d.left, d.top, d.right, d.bottom] };
    });
    const m = await fit();
    assert(m.sw > m.cw, `64 columns do not scroll inside the dialog (${m.sw} <= ${m.cw})`);
    assert(m.pw <= m.iw && m.ph <= m.ih, `page overflows: ${m.pw}x${m.ph} in ${m.iw}x${m.ih}`);
    assert(m.d[0] >= 0 && m.d[1] >= 0 && m.d[2] <= 1440 && m.d[3] <= 900, `dialog leaves the viewport: ${m.d}`);
    // Sticky first column: scrolled right, the message column stays put.
    const left = () => page.locator('dialog.mx .mx-m').first().evaluate(el => el.getBoundingClientRect().left);
    const x0 = await left();
    await page.locator('dialog.mx .mx-scroll').evaluate(el => { el.scrollLeft = el.scrollWidth; });
    await page.waitForFunction(() => document.querySelector('dialog.mx .mx-scroll').scrollLeft > 0);
    assert(Math.abs(await left() - x0) < 1, 'first column scrolled away');
    assert(await page.locator('dialog.mx .mx-h').last().isVisible(), 'last column unreachable');
    ok('64 recipients scroll inside the dialog with no page overflow');

    // Load more pages with next_after; moving to another matrix replaces this one.
    await page.evaluate(id => { location.hash = '#matrix/' + id; }, demo.threads.long);
    await page.waitForFunction(() => document.querySelectorAll('dialog.mx .mx-m').length === 200);
    assert(await page.locator('dialog.mx').count() === 1, 'two matrices open at once');
    const more = page.locator('dialog.mx').getByRole('button', { name: 'Load more' });
    await more.click();
    await page.waitForFunction(() => document.querySelectorAll('dialog.mx .mx-m').length === 400);
    await more.click();
    await page.waitForFunction(() => document.querySelectorAll('dialog.mx .mx-m').length === 500);
    await more.waitFor({ state: 'hidden' });
    // Sticky header: scrolled down, the recipient row stays at the top.
    const top = () => page.locator('dialog.mx .mx-h').first().evaluate(el => el.getBoundingClientRect().top);
    const y0 = await top();
    await page.locator('dialog.mx .mx-scroll').evaluate(el => { el.scrollTop = el.scrollHeight; });
    await page.waitForFunction(() => document.querySelector('dialog.mx .mx-scroll').scrollTop > 0);
    assert(Math.abs(await top() - y0) < 1, 'header row scrolled away');
    ok('Load more pages with next_after; header and first column are sticky');
  });
} finally {
  await cleanBuild();
}
