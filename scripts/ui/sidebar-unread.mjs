// Sidebar unread counts: each Your Threads row shows the operator's unread
// deliveries in that thread (the inbox head's unread_by_thread, PROTOCOL
// §2.13), so the rows sum to the inbox badge. Zero shows nothing; Other
// Threads show none. The counts follow the hub live: a new delivery raises
// one, and reading the thread (opened from the sidebar, from an inbox item,
// or in another tab: each message on screen is read by its own receipt, CoS
// unread ruling) or a receipt from elsewhere drops it, with no reload. Shots: the
// sidebar with counts, a zero row and a long title at desktop, narrow
// (220px sidebar) and phone width.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const LONG = 'Release checklist for hark 0.9: radio end-phrase fix, classifier rollout and the nightly off-host backup';

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer } = ctx;
    const mineList = page.locator('#threads');
    const api = async (method, path, body, as = 'operator') => {
      const r = await asPeer(as, method, path, body);
      const text = await r.text();
      assert(r.ok, `${as} ${method} ${path}: ${r.status} ${text}`);
      return text ? JSON.parse(text) : null;
    };
    let serial = 0;
    const send = (as, body) => api('POST', '/v1/messages', { idempotency_key: `sb-unread-${++serial}`, priority: 'hold', ...body }, as);
    const p = name => demo.peers[name].id;
    const head = () => api('GET', '/v1/admin/inbox?limit=1');
    // What the UI shows: each Your Threads row's pill (or '' for none), and the badge.
    const shown = () => page.evaluate(() => ({
      rows: [...document.querySelectorAll('#threads a[href^="#thread/"]')].map(a => ({
        id: a.getAttribute('href').slice('#thread/'.length),
        ur: a.querySelector('.ur')?.textContent || '', bold: a.classList.contains('unread'), count: a.querySelector('.cnt').textContent,
      })),
      badge: document.getElementById('inbox-count').textContent,
      others: document.querySelectorAll('#others .ur, #others a.unread').length,
    }));
    // Waits until every row's pill is what the hub's map says, the badge is
    // its total, and want(ui, hub) holds, then returns both sides. want is
    // part of the wait: UI and hub agree before a read lands as well as after.
    const agrees = async (why, want = () => true) => {
      const deadline = Date.now() + 30000;
      let ui, hub;
      for (;;) {
        hub = await head();
        ui = await shown();
        const wantRows = ui.rows.map(r => ({ id: r.id, ur: hub.unread_by_thread[r.id] ? String(hub.unread_by_thread[r.id]) : '' }));
        const ok = ui.rows.length > 0 && ui.rows.every((r, i) => r.ur === wantRows[i].ur && r.bold === !!r.ur) && ui.badge === (hub.unread ? String(hub.unread) : '');
        if (ok && want(ui, hub)) return { ui, hub };
        assert(Date.now() < deadline, `${why}: UI ${JSON.stringify(ui)} vs hub ${JSON.stringify({ unread: hub.unread, by: hub.unread_by_thread })}`);
        await page.waitForTimeout(100);
      }
    };
    const pill = (ui, id) => (ui.rows.find(r => r.id === id) || {}).ur;

    await page.locator('#conn.live').waitFor();
    await page.locator('#threads a').first().waitFor();

    // A long-titled thread with three unread, and one with twelve for a
    // two-digit pill. The seeded delivery-health threads are the operator's
    // own sends, so they stay at zero.
    const long = await send('codex', { thread_title: LONG, recipients: [demo.operator_peer, p('sleuth')], content: { text: 'Checklist below.' } });
    await send('sleuth', { thread_id: long.thread_id, content: { text: 'Classifier fix is in.' } });
    await send('codex', { thread_id: long.thread_id, content: { text: 'Backup job scheduled.' } });
    const trace = await send('sleuth', { thread_title: 'Trace review', recipients: [demo.operator_peer], content: { text: 'Trace 1 of 12.' } });
    for (let i = 2; i <= 12; i++) await send('sleuth', { thread_id: trace.thread_id, content: { text: `Trace ${i} of 12.` } });
    // A thread the operator is not in, for Other Threads.
    const peerOnly = await send('grok', { thread_title: 'Push route review', recipients: [p('claw')], content: { text: 'Can you look at the push route?' } });
    await page.locator(`#others a[href="#thread/${peerOnly.thread_id}"]`).waitFor();

    // Your Threads reloads apart from the badge, so wait for the new threads too.
    const zero = demo.threads['delivery-0'];
    let { ui, hub } = await agrees('initial counts', u => pill(u, long.thread_id) === '3' && pill(u, trace.thread_id) === '12' && u.rows.some(r => r.id === zero));
    // The hub's split matches the unread deliveries it lists, thread by thread.
    const listed = await api('GET', '/v1/admin/inbox?unread=1&limit=200');
    assert(listed.next_before === null, 'more unread than one page');
    const grouped = {};
    for (const d of listed.deliveries) grouped[d.thread_id] = (grouped[d.thread_id] || 0) + 1;
    assert(JSON.stringify(Object.entries(grouped).sort()) === JSON.stringify(Object.entries(hub.unread_by_thread).sort()), `unread_by_thread ${JSON.stringify(hub.unread_by_thread)} vs unread deliveries ${JSON.stringify(grouped)}`);
    // Every thread with unread is listed, so the rows sum to the badge.
    const sum = ui.rows.reduce((n, r) => n + (Number(r.ur) || 0), 0);
    assert(sum === hub.unread && String(sum) === ui.badge, `rows sum ${sum}, badge ${ui.badge}, hub ${hub.unread}`);
    assert(pill(ui, long.thread_id) === '3' && pill(ui, trace.thread_id) === '12', `new threads ${JSON.stringify(ui.rows)}`);
    assert(pill(ui, zero) === '', `zero row shows a count ${JSON.stringify(ui.rows)}`);
    assert(ui.rows.filter(r => r.ur).length >= 4 && ui.rows.some(r => !r.ur), `want several counted rows and a zero row ${JSON.stringify(ui.rows)}`);
    // The pill is the row's, never the inbox badge's style.
    const styles = await page.evaluate(() => {
      const s = el => { const c = getComputedStyle(el); return { bg: c.backgroundColor, fg: c.color, border: c.borderTopStyle }; };
      return { badge: s(document.getElementById('inbox-count')), pill: s(document.querySelector('#threads .ur')) };
    });
    assert(styles.badge.bg !== styles.pill.bg && styles.badge.fg !== styles.pill.fg && styles.pill.border === 'solid', `pill looks like the badge ${JSON.stringify(styles)}`);
    // The pill takes the message count's place; a zero row keeps its count.
    const counts = await page.evaluate(() => [...document.querySelectorAll('#threads a[href^="#thread/"]')].map(a => ({ ur: !!a.querySelector('.ur'), cnt: a.querySelector('.cnt').checkVisibility() })));
    assert(counts.every(c => c.ur !== c.cnt), `message count and pill shown together ${JSON.stringify(counts)}`);

    // Fit: at every sidebar width the title ellipsizes before the counts,
    // and the counts stay on the first line inside the row. Every row is
    // rebuilt on each resource notify (renderNav), so a locator's handle can
    // be detached by the time it is evaluated, and a detached title measures
    // 0 by 0. All measuring therefore happens in one page task, on rows read
    // from the live DOM in that task, after fonts and two frames.
    const fits = async width => {
      const m = await page.evaluate(async longId => {
        await document.fonts.ready;
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        // Synchronous from here: no render can land between query and measure.
        const bad = [...document.querySelectorAll('#threads a[href^="#thread/"]')].flatMap(a => {
          const r = a.getBoundingClientRect(), tt = a.querySelector('.tt').getBoundingClientRect(), nums = a.querySelector('.nums').getBoundingClientRect();
          const ur = a.querySelector('.ur')?.getBoundingClientRect();
          const out = [];
          if (nums.right > r.right + 0.5 || tt.right > nums.left + 0.5) out.push('overlap');
          if (ur && (ur.top > tt.bottom || ur.bottom < tt.top || ur.right > r.right + 0.5)) out.push('pill off line one');
          return out.length ? [a.getAttribute('href') + ': ' + out.join(', ')] : [];
        });
        const tt = document.querySelector(`#threads a[href="#thread/${longId}"] .tt`);
        const long = tt && { connected: tt.isConnected, sw: tt.scrollWidth, cw: tt.clientWidth, overflow: getComputedStyle(tt).textOverflow };
        return { bad, long };
      }, long.thread_id);
      assert(!m.bad.length, `at ${width}px: ${m.bad.join('; ')}`);
      const t = m.long;
      assert(t && t.connected && t.cw > 0 && t.overflow === 'ellipsis' && t.sw > t.cw, `long title not ellipsized at ${width}px: ${JSON.stringify(t)}`);
    };
    // Bring the long and counted rows into one frame.
    const top = () => page.evaluate(() => { document.querySelector('nav.side').scrollTop = 0; window.scrollTo(0, 0); });
    await top();
    await fits(1440);
    await shot('sb-unread-desktop');
    await page.setViewportSize({ width: 1000, height: 900 });
    await top();
    await fits(1000);
    await shot('sb-unread-narrow');
    await page.setViewportSize({ width: 390, height: 844 });
    // One column: the sidebar is on top and the page scrolls. Open the folded
    // thread lists (Threads), then frame Your Threads.
    await page.locator('#side-tog').click();
    assert(await page.locator('#side-tog').getAttribute('aria-expanded') === 'true' && await mineList.isVisible(), 'Threads did not open the lists at 390 px');
    await page.locator('nav.side .grp', { hasText: 'Your Threads' }).evaluate(el => el.scrollIntoView({ block: 'start' }));
    await fits(390);
    await shot('sb-unread-phone');
    await page.setViewportSize({ width: 1440, height: 900 });
    await top();

    // Rise: a delivery into a counted thread adds one; into a zero row, it
    // gains a pill. No reload, no navigation.
    const before = Number(pill(ui, trace.thread_id));
    await send('sleuth', { thread_id: trace.thread_id, content: { text: 'Trace 13: one more.' } });
    await send('triage', { thread_id: zero, content: { text: 'Retry window is open again.' } });
    ({ ui, hub } = await agrees('after new deliveries', u => Number(pill(u, trace.thread_id)) === before + 1 && pill(u, zero) === '1'));
    assert(Number(pill(ui, trace.thread_id)) === before + 1 && pill(ui, zero) === '1', `rise ${JSON.stringify(ui.rows)}`);
    const risen = hub.unread;

    // No reflow: every row has one box read or unread, so a row going read
    // moves nothing: not the rows under it, not a row's left edge or width.
    // Measured in one page task on the live rows, keyed by thread, after
    // fonts and frames. Text widths are left out: a relative time ticking
    // from "now" to "1m" between the two reads is not a reflow.
    const boxes = () => page.evaluate(async () => {
      await document.fonts.ready;
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      const out = {};
      for (const a of document.querySelectorAll('#threads a[href^="#thread/"], #others a[href^="#thread/"]')) {
        const r = a.getBoundingClientRect(), pp = a.querySelector('.pp').getBoundingClientRect();
        out[a.getAttribute('href')] = { unread: a.classList.contains('unread'), box: [r.left, r.top + document.querySelector('nav.side').scrollTop, r.width, r.height, pp.left, pp.top - r.top].map(v => Math.round(v * 10) / 10) };
      }
      return { rows: out };
    });
    const heights = Object.values((await boxes()).rows);
    assert(heights.some(r => r.unread) && heights.some(r => !r.unread), 'want read and unread rows to compare');
    assert(new Set(heights.map(r => r.box[3])).size === 1, `rows differ in height read vs unread: ${JSON.stringify(heights)}`);

    // Drop: open the long thread from the sidebar; its three messages are all
    // on screen, so each is read by its own receipt a second later.
    const opened = await boxes();
    assert(opened.rows[`#thread/${long.thread_id}`]?.unread, 'the long thread is not unread before opening');
    await mineList.locator(`a[href="#thread/${long.thread_id}"]`).click();
    await page.locator('#view .msg', { hasText: 'Backup job scheduled.' }).waitFor();
    ({ ui, hub } = await agrees('after opening a thread', u => pill(u, long.thread_id) === ''));
    const read = await boxes();
    const moved = Object.keys(opened.rows).filter(k => read.rows[k] && JSON.stringify(read.rows[k].box) !== JSON.stringify(opened.rows[k].box));
    assert(!moved.length, `rows moved when a thread went read: ${moved.map(k => k + ' ' + JSON.stringify(opened.rows[k].box) + ' -> ' + JSON.stringify(read.rows[k].box)).join('; ')}`);
    assert(hub.unread === risen - 3 && ui.badge === String(risen - 3), `drop by 3: ${hub.unread} from ${risen}`);
    assert(await mineList.locator(`a.on[href="#thread/${long.thread_id}"] .ur`).count() === 0, 'the open row still has a pill');

    // From the inbox: an inbox item opens its thread, which is read.
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    await page.locator(`#view a.item[href="#thread/${zero}"]`).first().click();
    await page.locator('#view .msg', { hasText: 'Retry window is open again.' }).waitFor();
    ({ ui, hub } = await agrees('after opening from the inbox', u => pill(u, zero) === ''));

    // A read elsewhere: one receipt (as the TUI posts) drops a count by one.
    await page.evaluate(() => { location.hash = '#inbox'; });
    const one = (await api('GET', '/v1/admin/inbox?unread=1&limit=200')).deliveries.find(d => d.thread_id === trace.thread_id);
    const tracePill = Number(pill(ui, trace.thread_id));
    await api('POST', '/v1/receipts', { delivery_id: one.delivery_id, state: 'read' });
    ({ ui, hub } = await agrees('after a receipt', u => Number(pill(u, trace.thread_id)) === tracePill - 1));

    // Another tab reads a thread; this tab's row clears live.
    const digest = demo.threads.digest;
    assert(pill(ui, digest), `digest has no count ${JSON.stringify(ui.rows)}`);
    const tab = await page.context().newPage();
    await tab.goto(`${demo.url}/ui/#thread/${digest}`, { waitUntil: 'load' });
    await tab.locator('#view .msg', { hasText: 'Held until you return' }).waitFor();
    ({ ui, hub } = await agrees('after another tab read', u => pill(u, digest) === ''));
    await tab.close();

    // Other Threads are not the operator's: no counts, ever.
    assert(ui.others === 0, 'an Other Threads row has an unread count');
    const final = ui.rows.reduce((n, r) => n + (Number(r.ur) || 0), 0);
    assert(String(final) === ui.badge || (final === 0 && ui.badge === ''), `final sum ${final} badge ${ui.badge}`);
  });
  console.log('Sidebar unread passed: per-thread counts match the hub and sum to the badge; zero shows nothing; a row is one height read or unread and reading moves no row; they rise on delivery and drop on read from the sidebar, the inbox, a receipt and another tab; the pill fits at 1440, 1000 and 390 px');
} finally {
  await cleanBuild();
}
