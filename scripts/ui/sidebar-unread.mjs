// Sidebar unread counts: each Your Threads row shows the operator's unread
// deliveries in that thread (the inbox head's unread_by_thread, PROTOCOL
// §2.13), so the rows sum to the inbox badge. Zero shows nothing; Other
// Threads show none. The counts follow the hub live: a new delivery raises
// one, and opening the thread (from the sidebar, from an inbox item, or in
// another tab) or a read receipt drops it, with no reload. Shots: the
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
    // Waits until every row's pill is what the hub's map says and the badge
    // is its total, then returns both sides.
    const agrees = async (why, check = () => {}) => {
      const deadline = Date.now() + 30000;
      let ui, hub;
      for (;;) {
        hub = await head();
        ui = await shown();
        const wantRows = ui.rows.map(r => ({ id: r.id, ur: hub.unread_by_thread[r.id] ? String(hub.unread_by_thread[r.id]) : '' }));
        const ok = ui.rows.length > 0 && ui.rows.every((r, i) => r.ur === wantRows[i].ur && r.bold === !!r.ur) && ui.badge === (hub.unread ? String(hub.unread) : '');
        if (ok) { check(ui, hub); return { ui, hub }; }
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

    let { ui, hub } = await agrees('initial counts');
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
    const zero = demo.threads['delivery-0'];
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
    // and the counts stay on the first line inside the row.
    const fits = async width => {
      const bad = await page.evaluate(() => [...document.querySelectorAll('#threads a[href^="#thread/"]')].flatMap(a => {
        const r = a.getBoundingClientRect(), tt = a.querySelector('.tt').getBoundingClientRect(), nums = a.querySelector('.nums').getBoundingClientRect();
        const ur = a.querySelector('.ur')?.getBoundingClientRect();
        const out = [];
        if (nums.right > r.right + 0.5 || tt.right > nums.left + 0.5) out.push('overlap');
        if (ur && (ur.top > tt.bottom || ur.bottom < tt.top || ur.right > r.right + 0.5)) out.push('pill off line one');
        return out.length ? [a.getAttribute('href') + ': ' + out.join(', ')] : [];
      }));
      assert(!bad.length, `at ${width}px: ${bad.join('; ')}`);
      const longRow = mineList.locator(`a[href="#thread/${long.thread_id}"] .tt`);
      assert(await longRow.evaluate(el => el.scrollWidth > el.clientWidth), `long title not ellipsized at ${width}px`);
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
    // One column: the sidebar is on top and the page scrolls; frame Your Threads.
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
    ({ ui, hub } = await agrees('after new deliveries'));
    assert(Number(pill(ui, trace.thread_id)) === before + 1 && pill(ui, zero) === '1', `rise ${JSON.stringify(ui.rows)}`);
    const risen = hub.unread;

    // Drop: open the long thread from the sidebar; the thread marks itself read.
    await mineList.locator(`a[href="#thread/${long.thread_id}"]`).click();
    await page.locator('#view .msg', { hasText: 'Backup job scheduled.' }).waitFor();
    ({ ui, hub } = await agrees('after opening a thread', u => assert(pill(u, long.thread_id) === '', 'still counted')));
    assert(hub.unread === risen - 3 && ui.badge === String(risen - 3), `drop by 3: ${hub.unread} from ${risen}`);
    assert(await mineList.locator(`a.on[href="#thread/${long.thread_id}"] .ur`).count() === 0, 'the open row still has a pill');

    // From the inbox: an inbox item opens its thread, which is read.
    await page.evaluate(() => { location.hash = '#inbox'; });
    await page.getByRole('heading', { name: 'Inbox', exact: true }).waitFor();
    await page.locator(`#view a.item[href="#thread/${zero}"]`).first().click();
    await page.locator('#view .msg', { hasText: 'Retry window is open again.' }).waitFor();
    ({ ui, hub } = await agrees('after opening from the inbox', u => assert(pill(u, zero) === '', 'zero row still counted')));

    // A read elsewhere: one receipt (as the TUI posts) drops a count by one.
    await page.evaluate(() => { location.hash = '#inbox'; });
    const one = (await api('GET', '/v1/admin/inbox?unread=1&limit=200')).deliveries.find(d => d.thread_id === trace.thread_id);
    const tracePill = Number(pill(ui, trace.thread_id));
    await api('POST', '/v1/receipts', { delivery_id: one.delivery_id, state: 'read' });
    ({ ui, hub } = await agrees('after a receipt', u => assert(Number(pill(u, trace.thread_id)) === tracePill - 1, 'receipt did not drop the count')));

    // Another tab reads a thread; this tab's row clears live.
    const digest = demo.threads.digest;
    assert(pill(ui, digest), `digest has no count ${JSON.stringify(ui.rows)}`);
    const tab = await page.context().newPage();
    await tab.goto(`${demo.url}/ui/#thread/${digest}`, { waitUntil: 'load' });
    await tab.locator('#view .msg', { hasText: 'Held until you return' }).waitFor();
    ({ ui, hub } = await agrees('after another tab read', u => assert(pill(u, digest) === '', 'digest still counted')));
    await tab.close();

    // Other Threads are not the operator's: no counts, ever.
    assert(ui.others === 0, 'an Other Threads row has an unread count');
    const final = ui.rows.reduce((n, r) => n + (Number(r.ur) || 0), 0);
    assert(String(final) === ui.badge || (final === 0 && ui.badge === ''), `final sum ${final} badge ${ui.badge}`);
  });
  console.log('Sidebar unread passed: per-thread counts match the hub and sum to the badge; zero shows nothing; they rise on delivery and drop on read from the sidebar, the inbox, a receipt and another tab; the pill fits at 1440, 1000 and 390 px');
} finally {
  await cleanBuild();
}
