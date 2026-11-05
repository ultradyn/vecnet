// Task 8: the web inbox in a real browser: approvals, dead letters, hostile text.
import { randomInt } from 'node:crypto';
import { assert, cleanBuild, withDemo } from './lib.mjs';

const HOSTILE = '<img src=x onerror=alert(1)>';
const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';

// ids.go: "<prefix>_" and a 26-character Crockford base32 ULID.
function newId(prefix) {
  let t = Date.now(), s = '';
  for (let i = 0; i < 10; i++) { s = CROCKFORD[t % 32] + s; t = Math.floor(t / 32); }
  for (let i = 0; i < 16; i++) s += CROCKFORD[randomInt(32)];
  return prefix + '_' + s;
}

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, advance, hold, allowConsole, countRequests, quiet } = ctx;
    // Chromium logs every non-2xx fetch as a console error. The late answer is
    // the only one this script provokes on purpose.
    allowConsole(/status of 422/);

    let dialogs = 0;
    page.on('dialog', dialog => { dialogs++; dialog.dismiss().catch(() => {}); });
    // lib.mjs hold() unroutes as it releases; a second route keeps Playwright's
    // interception on so the held handler's own continue still works.
    await page.route('**/__keep-interception__', route => route.continue());

    const snapshot = async () => {
      const r = await asPeer('operator', 'GET', '/v1/admin/inbox?limit=1');
      assert(r.ok, `inbox snapshot: ${r.status}`);
      return r.json();
    };
    const card = id => page.locator(`#view .item[data-id="${id}"]`);
    const posts = id => countRequests(req => req.method() === 'POST' && req.url().endsWith('/v1/messages') && (req.postData() || '').includes(id));
    const toastSettled = () => page.evaluate(() => Promise.all(document.getElementById('toast').getAnimations().map(a => a.finished)));
    const enabledIn = loc => loc.locator('button:enabled').count();

    // ---- initial: the seeded page, against shots/a-inbox.png ----
    const [aprA, aprB] = demo.approvals;
    const [dl503, dl410, dlNet] = demo.dead_letters;
    await page.locator('#conn.live').waitFor();
    await card(aprB).waitFor();
    await card(dlNet).waitFor();
    await page.locator('#inbox-held').waitFor();
    const headings = await page.locator('#view h2 > span:first-child').allTextContents();
    assert(headings.join('|') === 'Needs a decision|Failed deliveries|Subscriptions|Woke you|Held', `section order ${headings.join('|')}`);
    assert(await page.locator('#view .item.apr').count() === 2, 'two open approvals expected');
    assert(await page.locator('#inbox-approvals .n').textContent() === '2 open', 'approval count label');
    assert(await page.locator('#inbox-dead_letters .n').textContent() === '3 dead letters', 'dead letter count label');
    const line503 = await card(dl503).locator('.meta').textContent();
    // The delivery id shows short (dly_…8J0M2) but its text is the whole id,
    // so a copy is whole (dom.js idText); the ellipsis is CSS.
    assert(/^503 · retries exhausted · attempts 8\/8 · \d\d:\d\d · dly_[0-9A-Z]{26}$/.test(line503), `503 line: ${line503}`);
    assert(/^410 · terminal 4xx · attempts 1\/8 · /.test(await card(dl410).locator('.meta').textContent()), '410 line');
    assert(/^network error · attempts 8\/8 · /.test(await card(dlNet).locator('.meta').textContent()), 'network line');
    assert(/^expires in \d\d:\d\d$/.test(await card(aprA).locator('.exp').textContent()), 'countdown text');
    // Every item links to its thread; the inbox never opens a thread inline.
    assert(await card(aprA).locator('a.tl').getAttribute('href') === `#thread/${demo.threads.hark}`, 'approval does not link to its thread');
    const rowLinks = await page.locator('#view a.item').evaluateAll(as => as.map(a => a.getAttribute('href')));
    assert(rowLinks.length > 0 && rowLinks.every(href => /^#thread\/thr_[0-9A-Z]{26}$/.test(href)), `row links ${rowLinks.slice(0, 3)}`);
    assert(await page.locator('#view .item', { hasText: 'sent as hold: kept for your next look' }).count() > 0, 'held rows explain why');
    const onPage = await page.locator('aside .onpage a').allTextContents();
    assert(onPage.join('|') === 'Needs a decision|Failed deliveries|Subscriptions|Woke you|Held', `on this page: ${onPage.join('|')}`);
    await quiet();
    await shot('08-inbox-initial');

    // ---- double-click sends one POST (progress, then success) ----
    {
      const releasePost = await hold('**/v1/messages');
      const sent = page.waitForRequest(req => req.method() === 'POST' && req.url().endsWith('/v1/messages'));
      await card(aprA).getByRole('button', { name: 'Yes', exact: true }).dblclick();
      await sent;
      assert(await enabledIn(card(aprA)) === 0, 'options stayed enabled while the answer was in flight');
      assert(posts(aprA) === 1, `double-click sent ${posts(aprA)} POSTs`);
      await quiet();
      await shot('08-inbox-progress');
      await releasePost();
      await page.locator('#toast.on', { hasText: 'Answered “yes”' }).waitFor();
      assert(await card(aprA).count() === 0, 'answered card is still listed');
      assert(await page.locator('#inbox-approvals .n').textContent() === '1 open', 'approval count did not drop');
      await toastSettled();
      await quiet();
      await shot('08-inbox-success');
      assert(posts(aprA) === 1, `double-click sent ${posts(aprA)} POSTs in all`);
      console.log('ok - double-click sends one POST');
    }

    // ---- answered elsewhere, then click: closed toast, one POST, no retry ----
    {
      // Review Focus 2. Hold the event poll so this page never hears about the
      // other client's answer, and its card stays stale and clickable.
      const releaseEvents = await hold('**/v1/events*');
      const parked = page.waitForRequest(req => req.url().includes('/v1/events'));
      const nudge = await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: 'inbox-nudge', thread_id: demo.threads.hosting, priority: 'hold', content: { text: 'Hosting numbers follow.' } });
      assert(nudge.ok, `nudge: ${nudge.status}`);
      await parked; // the poll after the nudge is the held one
      const b = (await snapshot()).approvals.find(a => a.approval_id === aprB);
      assert(b, 'second approval is not open on the hub');
      const elsewhere = await asPeer('operator', 'POST', '/v1/messages', {
        idempotency_key: 'inbox-elsewhere', thread_id: b.thread_id, in_reply_to: b.message_id, kind: 'approval_response', priority: 'wake',
        content: { text: 'no' }, approval_response: { approval_id: aprB, decision: 'no' },
      });
      assert(elsewhere.ok, `answer from the other client: ${elsewhere.status}`);
      assert(await enabledIn(card(aprB)) === 2, 'stale card is not clickable');
      await card(aprB).getByRole('button', { name: 'Yes', exact: true }).click();
      await page.locator('#toast.on', { hasText: 'Already closed' }).waitFor();
      assert(await card(aprB).count() === 0, 'closed card survived the reload');
      assert(await page.locator('#inbox-approvals').count() === 0, 'empty decisions section still shown');
      assert(await page.locator('#toast').textContent() === 'Already closed', 'late click was reported as landed');
      assert(posts(aprB) === 1, `late click sent ${posts(aprB)} POSTs`);
      await toastSettled();
      await quiet();
      await shot('08-inbox-error');
      assert(posts(aprB) === 1, `late click was retried: ${posts(aprB)} POSTs`);
      await releaseEvents();
      console.log('ok - answered elsewhere, then click → closed toast, one POST, no retry');
    }

    // ---- advance past expiry: buttons disabled, card leaves after reload ----
    {
      const aprC = newId('apr');
      const now = Date.parse((await snapshot()).now);
      const ask = await asPeer('codex', 'POST', '/v1/messages', {
        idempotency_key: 'inbox-expiry', thread_id: demo.threads.hosting, recipients: [demo.operator_peer], kind: 'approval_request', priority: 'wake',
        content: { text: 'Rotate the backup key tonight?' },
        approval_request: { approval_id: aprC, question: 'Rotate the backup key tonight?', options: ['yes', 'no'], expires_at: new Date(now + 10 * 60_000).toISOString() },
      });
      assert(ask.ok, `approval request: ${ask.status}`);
      await card(aprC).waitFor();
      // Move the hub to 25 s before expiry; the reload brings its clock over.
      const c = (await snapshot()).approvals.find(a => a.approval_id === aprC);
      assert(c, 'new approval is not open on the hub');
      const left = Date.parse(c.expires_at) - Date.parse((await snapshot()).now);
      // The page's snapshot route is in place before the clock moves, so
      // gating it is a flag flip in this process, not a round trip to the
      // browser: once the card shows its countdown, every later snapshot (the
      // one expiry triggers among them) waits, and the expired card stays on
      // screen however slow the page is.
      let open, gating = false;
      const gate = new Promise(resolve => { open = resolve; });
      const pageSnapshots = url => url.pathname.endsWith('/v1/admin/inbox') && url.searchParams.get('limit') === '50';
      const gated = async route => {
        if (gating) await gate;
        await route.continue();
      };
      await page.route(pageSnapshots, gated);
      try {
        await advance(`${left - 25_000}ms`);
        await page.reload({ waitUntil: 'load' });
        await card(aprC).locator('.exp', { hasText: /^expires in 00:[0-2]\d$/ }).waitFor();
        gating = true;
        await card(aprC).locator('.exp', { hasText: /^expired$/ }).waitFor();
        assert(await enabledIn(card(aprC)) === 0, 'expired card still offers its options');
        assert(await card(aprC).locator('button:disabled').count() === 2, 'expired card lost its options');
      } finally {
        open();
        await page.unroute(pageSnapshots, gated);
      }
      await card(aprC).waitFor({ state: 'detached' });
      assert(posts(aprC) === 0, 'expiry sent an answer');
      console.log('ok - advance past expiry → buttons disabled, card leaves after reload');
    }

    // ---- retry moves a dead letter out; discard needs a second click ----
    {
      await card(dl410).getByRole('button', { name: 'Retry', exact: true }).click();
      await page.locator('#toast.on', { hasText: 'Retry queued' }).waitFor();
      await card(dl410).waitFor({ state: 'detached' });

      const discards = () => countRequests(req => req.method() === 'POST' && req.url().endsWith(`/v1/admin/dead-letters/${dlNet}/discard`));
      // Confirm straight after arming: confirmButton disarms after 3 s, and the
      // shared build slice can be slow. The armed "Confirm discard" button
      // proves the first click armed rather than discarded, and one POST in
      // all proves it sent nothing.
      await card(dlNet).getByRole('button', { name: 'Discard', exact: true }).click();
      await card(dlNet).locator('button.armed', { hasText: 'Confirm discard' }).click();
      await page.locator('#toast.on', { hasText: 'Discarded' }).waitFor();
      await card(dlNet).waitFor({ state: 'detached' });
      assert(discards() === 1, `discard sent ${discards()} POSTs`);
      assert(await card(dl503).count() === 1, 'an untouched dead letter left too');
      console.log('ok - retry moves a dead letter out; discard needs a second click');
    }

    // ---- hostile body renders as literal text and no dialog fires ----
    {
      // Review Focus 1: the seeded hostile peer's body, title and name.
      const row = page.locator('#view a.item', { hasText: HOSTILE }).first();
      await row.waitFor();
      assert((await row.locator('.ex').textContent()).includes(HOSTILE), 'hostile body is not literal');
      assert((await row.locator('.tl').textContent()).includes(HOSTILE), 'hostile title is not literal');
      assert((await row.locator('.who > span:first-child').textContent()).includes(HOSTILE), 'hostile name is not literal');
      // design: a bidi override or control in the name cannot reorder the UI's
      // own words (" in ") or draw tofu: clean() drops them, as tui.Clean does.
      assert(!/[\u0000-\u0008\u000b-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/.test(await row.textContent()), 'a control or bidi override reached the page');
      assert(await row.locator('.who .muted').textContent() === 'in', 'the UI word beside the name changed');
      assert(await page.locator('#view img, aside img').count() === 0, 'a body became an element');
      assert(dialogs === 0, `${dialogs} dialogs fired`);
      console.log('ok - hostile body renders as literal text and no dialog fires');
    }

    // ---- "Load older" pages stay whole while new deliveries shift the head ----
    {
      // next_before is exclusive and names the last row of a page, so each new
      // delivery pushes a row off the head; the older pages must follow it.
      // Each note opens its own thread: replies in one thread run into its
      // depth and budget limits (§6).
      const say = async (key, text) => {
        const r = await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: key, recipients: [demo.operator_peer], thread_title: text, priority: 'hold', content: { text } });
        assert(r.ok, `${key}: ${r.status} ${r.ok ? '' : JSON.stringify((await r.json()).error)}`);
      };
      // PROTOCOL §16.2 caps a peer at 20 new threads an hour on the hub clock,
      // so move the clock past the window before each batch.
      for (let i = 0; i < 60; i++) {
        if (i % 20 === 0) await advance('61m');
        await say(`inbox-older-${i}`, `Older note ${i}`);
      }
      const hubRows = async () => {
        const r = await asPeer('operator', 'GET', '/v1/admin/inbox?limit=200');
        assert(r.ok, `full inbox: ${r.status}`);
        const v = await r.json();
        assert(!v.next_before, 'more than 200 deliveries; the check needs a smaller seed');
        // The page shows what woke you, then what was held, each newest first.
        const ids = section => v.deliveries.filter(d => (d.section === 'woke') === (section === 'woke')).map(d => d.delivery_id);
        return [...ids('woke'), ...ids('held')];
      };
      const shownRows = () => page.locator('#view a.item').evaluateAll(as => as.map(a => a.dataset.id));
      // The hub's own workers add deliveries too (each clock move can expire
      // something, and its notice arrives on the next sweep), so the page is
      // compared with the hub once it has caught up with a hub snapshot that
      // held still across the comparison. A row the page never shows fails.
      const matchHub = async (rows, outOfOrder) => {
        for (let round = 0; ; round++) {
          assert(round < 5, 'the hub inbox never held still for the comparison');
          try {
            await page.waitForFunction(ids => ids.every(id => document.querySelector(`#view a.item[data-id="${id}"]`)), rows);
          } catch {
            const shownNow = await shownRows();
            assert(false, `rows went missing after the head moved: ${rows.filter(id => !shownNow.includes(id)).join(', ')}`);
          }
          const shown = await shownRows(), now = await hubRows();
          if (now.join() !== rows.join()) { rows = now; continue; }
          assert(shown.join() === rows.join(), outOfOrder);
          return rows;
        }
      };
      const more = page.getByRole('button', { name: 'Load older', exact: true });
      await page.locator('#view a.item', { hasText: 'Older note 59' }).waitFor();
      await more.waitFor();
      // One click loads the older page even when the head reloads while that
      // page is in flight: hold its read, move the head with a new delivery,
      // then let it answer. The pass under the new head reads the page the
      // click asked for (inbox-model.js olderPager), so the button goes.
      const olderRead = url => url.pathname.endsWith('/v1/admin/inbox') && url.searchParams.has('before');
      let releaseOlder, heldOlder = false;
      const olderGate = new Promise(resolve => { releaseOlder = resolve; });
      const gateOlder = async route => {
        if (!heldOlder) { heldOlder = true; await olderGate; }
        await route.continue();
      };
      await page.route(olderRead, gateOlder);
      try {
        const asked = page.waitForRequest(req => olderRead(new URL(req.url())));
        await more.click();
        await asked;
        const headReload = page.waitForResponse(r => { const u = new URL(r.url()); return u.pathname.endsWith('/v1/admin/inbox') && !u.searchParams.has('before') && r.ok(); });
        await advance('61m');
        await say('inbox-older-mid', 'Arrived mid-load');
        await headReload;
      } finally { releaseOlder(); }
      await more.waitFor({ state: 'detached' });
      await page.unroute(olderRead, gateOlder);
      const before = await hubRows();
      assert(before.length > 50 && before.length <= 100, `${before.length} deliveries; the check wants two pages`);
      await page.locator(`#view a.item[data-id="${before[before.length - 1]}"]`).waitFor();
      await matchHub(before, 'older page did not join the head');

      await advance('61m');
      await say('inbox-older-new', 'Newest note');
      await page.locator('#view a.item', { hasText: 'Newest note' }).waitFor();
      await matchHub(await hubRows(), 'rows out of order after the head moved');
      console.log('ok - load older keeps every row while new deliveries shift the head');

      // Reading an older-page delivery from another client moves no cursor,
      // yet its row must stop showing unread without a reload (§2.13).
      const deep = page.locator('#view a.item:not(.read)', { hasText: /^.*Older note \d+/ }).last();
      const deepId = await deep.getAttribute('data-id');
      const hubDeep = (await (await asPeer('operator', 'GET', '/v1/admin/inbox?limit=200')).json()).deliveries.find(d => d.delivery_id === deepId);
      assert(hubDeep && !hubDeep.read_at, 'the older-page row is not unread on the hub');
      const headIds = (await (await asPeer('operator', 'GET', '/v1/admin/inbox?limit=50')).json()).deliveries.map(d => d.delivery_id);
      assert(!headIds.includes(deepId), 'the check wants a row below the head page');
      const marked = await asPeer('operator', 'POST', `/v1/admin/threads/${hubDeep.thread_id}/read`);
      assert(marked.ok, `mark read: ${marked.status}`);
      await page.locator(`#view a.item.read[data-id="${deepId}"]`).waitFor();
      console.log('ok - a read in another client reaches a loaded older page');
    }
  });
  console.log('Inbox passed: one POST per answer, closed approvals, expiry, dead-letter actions, hostile text, load older, no CSP violations');
} finally { await cleanBuild(); }
