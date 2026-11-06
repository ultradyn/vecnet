// Thread unread (CoS unread ruling): a thread opens at the operator's first
// unread message under a New divider, not at the bottom. Each message is read
// on its own, by a read receipt for its delivery (POST /v1/receipts, batched
// over 500 ms), once it has been 60% visible for a second or scrolled past
// going down, and only while the page is visible and focused. Opening never
// marks the whole thread (POST /v1/admin/threads/:id/read is never sent).
// With nothing unread it opens at the bottom, as before. A live arrival is
// unread until seen, and takes New only when it arrives unseen. A failed
// receipt is retried once, quietly. The sidebar count follows every receipt.
// Shots: tu-open (mid-history unreads under New), tu-open-phone (the same at
// 390 px), tu-partial (partly read after scrolling), tu-live (an arrival while
// away), tu-none (nothing unread: the bottom).
import { assert, cleanBuild, readThrough, withDemo } from './lib.mjs';

const READ = 8; // the first eight were read on an earlier visit
const TEXTS = [
  ['codex', 'Cutting the hark 0.9 release train today. Scope: the radio end-phrase fix, the classifier rollout, and the nightly off-host backup.'],
  ['sleuth', 'Trace for the end-phrase bug is attached to B196; the listen loop never sees `over` when the radio squelch clips it.'],
  ['codex', 'Fix is on `fix/end-phrase`: the classifier now pads 120 ms after squelch before it scores the tail.'],
  ['sleuth', 'Reproduced on the bench radio; with the pad it finalizes every time.'],
  ['codex', 'Classifier rollout: 10% of sessions from 14:00, then 50% after an hour without regressions.'],
  ['sleuth', 'Dashboards for the rollout are up: finalize rate, false stops, mean listen time.'],
  ['codex', 'Backup job draft is in `ops/backup.sh`; it ships the SQLite snapshot to the off-host bucket.'],
  ['sleuth', 'Read it. One note: run the snapshot under `.backup`, not `cp`, so a write mid-copy cannot tear it.'],
  ['codex', 'Switched the backup to `.backup` and added a restore check:\n\n1. snapshot to a temp file\n2. `PRAGMA integrity_check` on the copy\n3. upload only when it passes\n\nThe check takes about four seconds on the current database.'],
  ['sleuth', 'Rollout at 10% for forty minutes:\n\n- finalize rate 99.2% (was 93.5%)\n- false stops flat at 0.4%\n- mean listen time down 1.8 s\n\nNo regressions in the session logs.'],
  ['codex', 'Moving the classifier to 50%. I will hold there through the evening peak before going to 100%.\n\nIf finalize rate drops below 98% the rollout reverts itself; the guard is in `rollout.toml`.'],
  ['sleuth', 'One thing for the release notes: the pad changes the end-of-turn latency by 120 ms, which users of the push-to-talk mode will notice.\n\nSuggest we call it out under Changes rather than burying it in Fixes.'],
  ['codex', 'Agreed. Draft notes:\n\n- Fixes: radio end phrase is detected after squelch clipping (B196)\n- Changes: end of turn waits 120 ms longer in radio mode\n- Ops: nightly off-host backup with an integrity check\n\nPlease review before I tag.'],
  ['sleuth', 'Notes look right. Tagging is yours once the 50% window closes clean.\n\nI will watch the dashboards until then and post here if anything moves.'],
];

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, countRequests, quiet, allowConsole } = ctx;
    allowConsole(/status of 503/); // the receipt failed on purpose below
    const api = async (method, path, body, as = 'operator') => {
      const r = await asPeer(as, method, path, body);
      const text = await r.text();
      assert(r.ok, `${as} ${method} ${path}: ${r.status} ${text}`);
      return text ? JSON.parse(text) : null;
    };
    let serial = 0;
    const send = (as, body) => api('POST', '/v1/messages', { idempotency_key: `tu-${++serial}`, priority: 'hold', ...body }, as);
    const markAll = req => req.method() === 'POST' && /\/v1\/admin\/threads\/[^/]+\/read$/.test(new URL(req.url()).pathname);
    const receipts = [];
    page.on('request', req => {
      if (req.method() === 'POST' && new URL(req.url()).pathname === '/v1/receipts') receipts.push(req.postDataJSON());
    });
    // The page's focus, as the thread reads it: Playwright gives every page
    // focus, so the script stands in for the window losing and regaining it.
    const focus = on => page.evaluate(on => {
      if (!window.__vnFocusPatched) {
        window.__vnFocusPatched = true;
        const real = Document.prototype.hasFocus;
        Document.prototype.hasFocus = function () { return window.__vnFocus !== false && real.call(this); };
      }
      window.__vnFocus = on;
      window.dispatchEvent(new Event(on ? 'focus' : 'blur'));
    }, on);

    // A fourteen-message thread whose first eight the operator read earlier.
    const ids = [];
    let thread;
    for (const [i, [from, text]] of TEXTS.entries()) {
      const r = await send(from, i === 0
        ? { thread_title: 'Release train: hark 0.9', recipients: [demo.operator_peer, demo.peers.sleuth.id], content: { text } }
        : { thread_id: thread, content: { text } });
      thread = r.thread_id;
      ids.push(r.message_id);
    }
    const matrix = await api('GET', `/v1/admin/threads/${thread}/deliveries?limit=200`);
    const mine = new Map(matrix.rows.map(r => [r.message_id, r.cells.find(c => c.recipient === demo.operator_peer)]));
    for (const id of ids.slice(0, READ)) await api('POST', '/v1/receipts', { delivery_id: mine.get(id).delivery_id, state: 'read' });
    const dly = id => mine.get(id).delivery_id;
    const unreadIn = async () => (await api('GET', '/v1/admin/inbox?unread=1&limit=200')).deliveries.filter(d => d.thread_id === thread).length;
    assert(await unreadIn() === TEXTS.length - READ, 'the fixture is not six unread');

    const row = page.locator(`#threads a[href="#thread/${thread}"]`);
    // One non-waiting read. count() then textContent() races the pill being
    // removed: textContent waits out the locator timeout on a detached node
    // and throws, so the poll never sees the empty count.
    const pill = async () => (await row.locator('.ur').allTextContents())[0] ?? '';
    const pillIs = async (want, why) => {
      const deadline = Date.now() + 30000;
      for (let got = await pill(); got !== want; got = await pill()) {
        assert(Date.now() < deadline, `${why}: the sidebar pill shows '${got}', want '${want}'`);
        await page.waitForTimeout(100);
      }
    };
    // What the view shows: each message's unread bar and place, and the
    // divider. share is how much of a message the thread can see: inside the
    // column, above the composer that floats over its bottom.
    const view = () => page.evaluate(() => {
      const main = document.getElementById('main'), box = main.getBoundingClientRect();
      const floor = Math.min(box.bottom, innerHeight - document.querySelector('#view .composer').offsetHeight);
      const msgs = [...document.querySelectorAll('#view .msgs > .msg, #view .msgs > .sys')].map(el => {
        const r = el.getBoundingClientRect(), shown = Math.max(0, Math.min(r.bottom, floor) - Math.max(r.top, box.top));
        return { id: el.id.slice(2), unread: el.classList.contains('unread'), top: r.top - box.top, share: r.height ? shown / r.height : 0 };
      });
      const divs = [...document.querySelectorAll('#view .newdiv')];
      return {
        msgs, dividers: divs.length,
        divider: divs[0] ? divs[0].nextElementSibling.id.slice(2) : null,
        dividerTop: divs[0] ? divs[0].getBoundingClientRect().top - box.top : null,
        atBottom: main.scrollHeight - main.scrollTop - main.clientHeight < 2,
      };
    });
    const open = async () => {
      await page.evaluate(() => { location.hash = '#inbox'; });
      await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
      await row.click();
      await page.locator(`[id="m-${ids.at(-1)}"]`).waitFor();
      await quiet();
    };

    await page.locator('#conn.live').waitFor();
    await pillIs('6', 'before opening');

    // Opening: unfocused first, so the opening view holds still for the shot
    // and shows that nothing is read without focus.
    await focus(false);
    await open();
    let v = await view();
    assert(v.dividers === 1 && v.divider === ids[READ], `New is not above the first unread: ${JSON.stringify({ divider: v.divider, want: ids[READ] })}`);
    assert(v.dividerTop > 0 && v.dividerTop < 160, `the thread did not open at its first unread: New at ${v.dividerTop}px`);
    assert(!v.atBottom, 'the thread opened at the bottom with unread messages above it');
    assert(v.msgs.every((m, i) => m.unread === (i >= READ)), `unread bars ${JSON.stringify(v.msgs.map(m => m.unread))}`);
    assert(v.msgs[READ - 1].top < 0 || v.msgs[READ - 1].share < 1, 'what was read is not above New');
    await page.waitForTimeout(2500);
    assert(receipts.length === 0, `read while unfocused: ${JSON.stringify(receipts)}`);
    assert(await pill() === '6', 'the count moved while unfocused');
    await shot('tu-open');

    // The same opening at phone width: the page scrolls, New is still the place.
    await page.setViewportSize({ width: 390, height: 844 });
    await open();
    const phone = await page.evaluate(() => { const d = document.querySelector('#view .newdiv'); return d && d.getBoundingClientRect().top; });
    assert(phone !== null && phone >= 0 && phone < 200, `New is not near the top at phone width: ${phone}`);
    await shot('tu-open-phone');
    await page.setViewportSize({ width: 1440, height: 900 });
    await open();

    // Focus: what is on screen is read after a second; what is below is not.
    // (A message right at the 60% line may go either way: sure are well over
    // it, maybe are any part on screen.)
    const atFocus = (await view()).msgs.filter(m => m.unread);
    const sure = atFocus.filter(m => m.share >= 0.75).map(m => dly(m.id)), maybe = atFocus.filter(m => m.share > 0.45).map(m => dly(m.id));
    assert(sure.length >= 1 && maybe.length < atFocus.length, `want unread both on screen and below it at open: ${JSON.stringify(atFocus)}`);
    await focus(true);
    const deadline = Date.now() + 15000;
    while (receipts.length < sure.length) {
      assert(Date.now() < deadline, `focus read ${receipts.length} of the ${sure.length} on screen`);
      await page.waitForTimeout(100);
    }
    await page.waitForTimeout(1600);
    assert(receipts.every(r => r.state === 'read'), 'a receipt was not read');
    const readAtFocus = receipts.map(r => r.delivery_id);
    assert(sure.every(d => readAtFocus.includes(d)) && readAtFocus.every(d => maybe.includes(d)), `read ${JSON.stringify(readAtFocus)}, on screen ${JSON.stringify({ sure, maybe })}`);

    // Scrolling down reads what passes through the view; the end stays unread.
    const box = await page.locator('#main').boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 3);
    await page.mouse.wheel(0, 260);
    await page.waitForTimeout(2200);
    await quiet();
    const left = await unreadIn();
    v = await view();
    assert(left > 0 && left < TEXTS.length - READ - readAtFocus.length, `after scrolling ${left} unread are left (${readAtFocus.length} read at focus)`);
    assert(receipts.length === TEXTS.length - READ - left, `${receipts.length} receipts for ${TEXTS.length - READ - left} read`);
    await pillIs(String(left), 'after partial reading');
    const stillUnread = v.msgs.filter(m => m.unread).map(m => m.id);
    assert(stillUnread.length === left && stillUnread.every(id => ids.indexOf(id) >= ids.length - left), `unread bars ${JSON.stringify(stillUnread)} vs ${left} left`);
    assert(v.divider === ids[READ], 'New moved while reading');
    await page.locator(`[id="m-${ids[READ]}"] .rc .st-read`, { hasText: 'max read' }).waitFor();
    await shot('tu-partial');

    // Reading on to the end clears the thread; the sidebar count goes.
    await readThrough(page, unreadIn);
    await pillIs('', 'after reading to the end');

    // A live arrival while watching the bottom is read as it lands, and New stays put.
    const watched = await send('sleuth', { thread_id: thread, content: { text: 'Finalize rate holding at 99.1% on 50%.' } });
    await page.locator(`[id="m-${watched.message_id}"]`).waitFor();
    for (let t = Date.now(); !receipts.some(r => r.delivery_id === (watched.deliveries || []).find(d => d.recipient === demo.operator_peer)?.delivery_id); ) {
      assert(Date.now() - t < 15000, 'the watched arrival was not read');
      await page.waitForTimeout(100);
    }
    v = await view();
    assert(v.dividers === 1 && v.divider === ids[READ], `a watched arrival moved New to ${v.divider}`);
    await pillIs('', 'after a watched arrival');

    // Away (unfocused), an arrival is unread until seen, and New moves above it.
    await focus(false);
    const away = await send('codex', { thread_id: thread, content: { text: 'Evening peak passed clean. Tagging v0.9.0 now; release notes as drafted above.' } });
    const awayDly = away.deliveries.find(d => d.recipient === demo.operator_peer).delivery_id;
    await page.locator(`[id="m-${away.message_id}"].unread`).waitFor();
    await pillIs('1', 'after an arrival while away');
    await page.waitForTimeout(2500);
    v = await view();
    assert(v.dividers === 1 && v.divider === away.message_id, `New is not above the arrival: ${v.divider}`);
    assert(!receipts.some(r => r.delivery_id === awayDly), 'read while away');
    await quiet();
    await shot('tu-live');

    // Back with focus: read after a second. The first receipt fails; it is
    // retried once, quietly, and lands.
    let failed = 0;
    await page.route('**/v1/receipts', async route => {
      if (failed++ === 0) await route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: { code: 'unavailable', message: 'try again', details: {} } }) });
      else await route.continue();
    });
    await focus(true);
    await pillIs('', 'after returning');
    await page.unroute('**/v1/receipts');
    assert(failed === 2 && receipts.filter(r => r.delivery_id === awayDly).length === 2, `the failed receipt went out ${receipts.filter(r => r.delivery_id === awayDly).length} times`);
    assert(await page.locator('#toast.on').count() === 0, 'a failed receipt raised a toast');

    // Nothing unread: the thread opens at the bottom, with no New.
    await open();
    v = await view();
    assert(v.dividers === 0, 'New shows with nothing unread');
    assert(v.atBottom, 'a read thread did not open at the bottom');
    assert(v.msgs.every(m => !m.unread), 'an unread bar with nothing unread');
    await shot('tu-none');

    assert(countRequests(markAll) === 0, `the view posted mark-all ${countRequests(markAll)} times`);
    const once = receipts.filter(r => r.delivery_id !== awayDly).map(r => r.delivery_id);
    assert(new Set(once).size === once.length, 'a delivery was receipted twice');
  });
  console.log('Thread unread passed: opens at the first unread under New (desktop and phone), nothing read unfocused, partial reading by dwell and scroll, sidebar count follows, watched arrival read with New kept, away arrival under New, one quiet retry, nothing unread opens at the bottom, never mark-all');
} finally {
  await cleanBuild();
}
