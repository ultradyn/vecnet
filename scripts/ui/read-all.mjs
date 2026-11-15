// Bulk read for thread spam, in a real browser: one agent opens a pile of
// one-message threads. Its peer page offers "Mark all from <agent> read"
// (two clicks), which clears exactly its unread (not another sender's) and
// then goes away; the sidebar pills follow. A thread's own "Mark thread
// read" clears that thread. The inbox's "Mark all read" clears the rest
// but leaves an open decision, and hides once nothing is unread. Every
// read goes through receipts (POST /v1/admin/inbox/read or
// /v1/admin/threads/:id/read); nothing is marked without a confirming click.
// Shots: ra-peer (the peer header offer), ra-inbox (the inbox button).
import { randomInt } from 'node:crypto';
import { assert, cleanBuild, withDemo } from './lib.mjs';

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
    const { page, shot, demo, asPeer, countRequests, quiet } = ctx;
    const api = async (method, path, body, as = 'operator') => {
      const r = await asPeer(as, method, path, body);
      const text = await r.text();
      assert(r.ok, `${as} ${method} ${path}: ${r.status} ${text}`);
      return text ? JSON.parse(text) : null;
    };
    let serial = 0;
    const send = (as, body) => api('POST', '/v1/messages', { idempotency_key: `ra-${++serial}`, priority: 'hold', ...body }, as);
    const head = () => api('GET', '/v1/admin/inbox?limit=1');
    const op = demo.operator_peer, spammer = demo.peers.sleuth.id;
    const bulk = req => req.method() === 'POST' && /\/v1\/admin\/(inbox|threads\/[^/]+)\/read$/.test(new URL(req.url()).pathname);
    const until = async (why, fn, ms = 30000) => {
      for (const t = Date.now(); !(await fn()); ) {
        assert(Date.now() - t < ms, why);
        await page.waitForTimeout(100);
      }
    };

    // Thread spam: six one-message threads from one agent, one from another.
    const spam = [];
    for (let i = 1; i <= 6; i++) spam.push(await send('sleuth', { thread_title: `Status ping ${i}`, recipients: [op], content: { text: `ping ${i}` } }));
    const other = await send('codex', { thread_title: 'Real question', recipients: [op], content: { text: 'Can you look at this?' } });
    await page.locator('#conn.live').waitFor();
    await until('spam not counted', async () => ((await head()).unread_by_sender || {})[spammer] >= 6);
    const before = await head();
    assert(before.unread_by_sender[spammer] >= 6 && before.unread_by_sender[demo.peers.codex.id] >= 1, `unread_by_sender ${JSON.stringify(before.unread_by_sender)}`);
    const bulk0 = countRequests(bulk);

    // ---- peer page: mark all from this agent read ----
    await page.evaluate(h => { location.hash = h; }, '#peer/' + spammer);
    const btn = page.locator('.pv-acts .pv-read-all');
    await btn.waitFor();
    assert((await btn.innerText()).startsWith('Mark all from '), `label ${await btn.innerText()}`);
    await page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    await shot('ra-peer');
    // One click arms; nothing is sent.
    await btn.click();
    await page.locator('.pv-acts .pv-read-all', { hasText: 'Confirm mark read' }).waitFor();
    assert(countRequests(bulk) === bulk0, 'one click marked read');
    await page.locator('.pv-acts .pv-read-all').click();
    await page.locator('#toast.on', { hasText: /Marked \d+ read/ }).waitFor();
    await until('the spammer still has unread', async () => !((await head()).unread_by_sender || {})[spammer]);
    const afterPeer = await head();
    assert(afterPeer.unread_by_sender[demo.peers.codex.id] >= 1, 'another sender was read too');
    await btn.waitFor({ state: 'detached' });
    for (const s of spam) await page.locator(`#threads a[href="#thread/${s.thread_id}"] .ur`).waitFor({ state: 'detached' });
    assert(await page.locator(`#threads a[href="#thread/${other.thread_id}"] .ur`).count() === 1, 'the other sender lost its pill');
    // Codex still has unread, so its page offers it; the operator's own page never does.
    await page.evaluate(h => { location.hash = h; }, '#peer/' + demo.peers.codex.id);
    await page.locator('.pv-acts .pv-read-all').waitFor();
    await page.evaluate(h => { location.hash = h; }, '#peer/' + op);
    await page.locator('.pv-head h1').waitFor();
    await quiet();
    assert(await page.locator('.pv-read-all').count() === 0, 'the operator is offered to mark its own messages read');

    // ---- thread view: mark this thread read ----
    await send('codex', { thread_id: other.thread_id, content: { text: 'And one more detail.' } });
    await page.evaluate(h => { location.hash = h; }, '#thread/' + other.thread_id);
    // Unfocused, so the reader does not read it by dwell before the button.
    await page.evaluate(() => { Document.prototype.hasFocus = () => false; window.dispatchEvent(new Event('blur')); });
    const tbtn = page.locator('.thead .thread-read-all');
    await tbtn.waitFor();
    await tbtn.click();
    await page.locator('.thead .thread-read-all', { hasText: 'Confirm mark read' }).waitFor();
    await tbtn.click();
    await page.locator('#toast.on', { hasText: /Marked \d+ read/ }).waitFor();
    await until('thread still unread', async () => !((await head()).unread_by_thread || {})[other.thread_id]);
    await tbtn.waitFor({ state: 'detached' });

    // ---- inbox: mark all read, open decision kept ----
    const apr = newId('apr');
    const now = Date.parse((await head()).now);
    await send('codex', {
      thread_title: 'Needs an answer', recipients: [op], kind: 'approval_request', priority: 'wake', content: { text: 'Ship it?' },
      approval_request: { approval_id: apr, question: 'Ship it?', options: ['yes', 'no'], expires_at: new Date(now + 60 * 60_000).toISOString() },
    });
    await send('codex', { thread_title: 'Leftover', recipients: [op], content: { text: 'left over' } });
    await page.evaluate(() => { location.hash = '#inbox'; });
    const ib = page.locator('.ib-head .ib-read-all');
    await ib.waitFor();
    await page.locator('#main').evaluate(el => { el.scrollTop = 0; });
    await shot('ra-inbox');
    assert((await head()).approvals.some(a => a.approval_id === apr), 'the new approval is not open');
    await ib.click();
    await page.locator('.ib-head .ib-read-all', { hasText: 'Confirm mark all read' }).waitFor();
    await ib.click();
    await page.locator('#toast.on', { hasText: /Marked \d+ read|Nothing unread/ }).waitFor();
    await until('unread left after Mark all read', async () => (await head()).unread === 0);
    assert((await head()).approvals.some(a => a.approval_id === apr), 'Mark all read closed an open decision');
    await page.locator(`#view .item.apr[data-id="${apr}"]`).waitFor();
    await until('Mark all read still shown', async () => await ib.isHidden());
    assert(await page.locator('#inbox-count').textContent() === '', 'the badge still counts');
  });
  console.log('Read all passed: per-agent mark read on the peer page (two clicks, that sender only, then gone, sidebar follows), never for the operator itself, Mark thread read in the thread, inbox Mark all read keeps open decisions and hides when done');
} finally {
  await cleanBuild();
}
