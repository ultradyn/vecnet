// Reactions in the web thread (PROTOCOL §17, reactions plan Task 8): chips
// with cleaned titles, the react button and picker surviving a live rebuild,
// a priority override, removing by chip, a reserved chips row, Escape and
// outside click returning focus (a click on a control keeps it), Tab out, the
// picker's priority tip in view, and a rate-limited reaction's toast. Fixtures are built here through
// asPeer; the demo seed is unchanged.
import { assert, cleanBuild, withDemo } from './lib.mjs';

const CHECK = '✅', PLUS = '👍', EYES = '👀', HEART = '❤️', Q = '❓';
const CONTROLS = /[\u0000-\u001f\u007f-\u009f‪-‮⁦-⁩]/;

try {
  await withDemo({}, async ctx => {
    const { page, shot, demo, asPeer, hold, allowConsole } = ctx;
    allowConsole(/status of 429/);
    // lib.mjs hold() unroutes as it releases; a second route keeps Playwright's
    // interception on so the held handler's own continue still works.
    await page.route('**/__keep-interception__', route => route.continue());
    const api = async (as, method, path, body) => {
      const r = await asPeer(as, method, path, body);
      if (!r.ok) throw new Error(`${as} ${method} ${path}: ${r.status} ${await r.text()}`);
      return r.json();
    };
    const react = (as, message_id, emoji, op = 'add') => api(as, 'POST', '/v1/reactions', { message_id, emoji, op });
    const P = demo.peers;
    const names = new Map((await api('operator', 'GET', '/v1/peers')).peers.map(p => [p.peer_id, p.name]));
    const nameOf = key => names.get(P[key].id);

    // A thread whose question gets an answer from codex; the answer collects
    // the reactions.
    const q = await api('operator', 'POST', '/v1/messages', {
      idempotency_key: 'rx-q', recipients: [P.codex.id, P.claw.id, P.hostile.id, P.sleuth.id], thread_title: 'Reactions',
      priority: 'wake', content: { text: 'Is the **B196** radio fix ready to ship?' },
    });
    const thr = q.thread_id;
    const ans = await api('codex', 'POST', '/v1/messages', { idempotency_key: 'rx-a', thread_id: thr, in_reply_to: q.message_id, priority: 'wake', content: { text: 'Yes: staging is green and the finalize counter is steady.' } });
    const target = ans.message_id;
    // Seeds: two agents put ✅, one puts 👍, and the operator's own 👀.
    await react('claw', target, CHECK);
    await react('hostile', target, CHECK);
    await react('sleuth', target, PLUS);
    await react('operator', target, EYES);

    await page.locator('#conn.live').waitFor();
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, thr);
    const msg = page.locator(`[id="m-${target}"]`);
    const chip = emoji => msg.locator('.rx-row .rx-chip', { hasText: emoji });
    await chip(EYES).waitFor();
    // shown: the hovered message's react button has finished fading in.
    const shown = () => page.waitForFunction(id => getComputedStyle(document.getElementById('rx-btn-' + id)).opacity === '1', target);

    // Chips: fixed order, counts, titles (hostile name cleaned), mine marked.
    const rows = await msg.locator('.rx-chip').evaluateAll(cs => cs.map(c => ({ text: c.textContent, title: c.title, pressed: c.getAttribute('aria-pressed'), mine: c.classList.contains('mine') })));
    const hostileClean = nameOf('hostile').replace(new RegExp(CONTROLS.source, 'g'), '');
    assert(rows.length === 3, `want 3 chips, got ${JSON.stringify(rows)}`);
    assert(rows[0].text === PLUS + '1' && rows[0].title === nameOf('sleuth') && !rows[0].mine && rows[0].pressed === 'false', 'the 👍 chip is wrong: ' + JSON.stringify(rows[0]));
    assert(rows[1].text === EYES + '1' && rows[1].title === 'you' && rows[1].mine && rows[1].pressed === 'true', 'the own 👀 chip is wrong: ' + JSON.stringify(rows[1]));
    assert(rows[2].text === CHECK + '2' && rows[2].title === nameOf('claw') + ', ' + hostileClean && !rows[2].mine, 'the ✅ chip is wrong: ' + JSON.stringify(rows[2]));
    assert(!CONTROLS.test(rows[2].title) && rows[2].title.includes('<img src=x onerror=alert(1)>'), 'the hostile name is not cleaned literal text');
    assert(await page.locator('#view img').count() === 0, 'a hostile name became an element');
    // Reserved height (Review Focus 5): every message has its chips row, the
    // react button first, so the first chip arriving on the question and the
    // last one leaving it move neither it nor the answer below it.
    const qMsg = page.locator(`[id="m-${q.message_id}"]`);
    assert(await qMsg.locator('.rx-row .rx-btn').count() === 1 && await qMsg.locator('.rx-chip').count() === 0, 'the question lacks its empty chips row');
    const geo = () => page.evaluate(([a, b]) => {
      const r = id => document.getElementById('m-' + id).getBoundingClientRect();
      return { h: r(a).height, top: r(b).top, row: document.querySelector(`[id="m-${a}"] .rx-row`).getBoundingClientRect().height };
    }, [q.message_id, target]);
    const g0 = await geo();
    await react('sleuth', q.message_id, CHECK);
    await qMsg.locator('.rx-chip').waitFor();
    const g1 = await geo();
    await react('sleuth', q.message_id, CHECK, 'remove');
    await qMsg.locator('.rx-chip').waitFor({ state: 'detached' });
    const g2 = await geo();
    assert(g0.row >= 26 && g0.row < 34, 'the empty chips row height is not reserved: ' + g0.row);
    assert([g1, g2].every(g => g.h === g0.h && g.top === g0.top), 'a chip coming or going moved the thread: ' + JSON.stringify([g0, g1, g2]));
    await msg.hover();
    await shown();
    await shot('rx08-thread-initial');

    // The picker: open, focus on the first emoji, markers from the defaults.
    const btn = msg.locator(`[id="rx-btn-${target}"]`);
    assert(await btn.getAttribute('aria-haspopup') === 'dialog' && await btn.getAttribute('aria-expanded') === 'false', 'the react button lacks its popup attributes');
    await btn.click();
    const picker = page.locator('.rx-pick[role=dialog][aria-label="React"]');
    await picker.waitFor({ state: 'visible' });
    assert(await page.locator('.rx-pick').count() === 1 && await picker.evaluate(p => !p.closest('.msgs')), 'the picker is not one node outside the message list');
    const emo = picker.locator('.rx-e');
    assert(await emo.count() === 8, 'the picker does not hold 8 emoji');
    // A rebuild replaces the button; its replacement takes focus on the next
    // frame, so wait for the state rather than reading it once.
    const backOnButton = () => page.waitForFunction(id => document.activeElement && document.activeElement.id === 'rx-btn-' + id, target, { timeout: 5000 }).then(() => true, () => false);
    const focusIsFirst = () => page.evaluate(() => document.activeElement === document.querySelector('.rx-pick .rx-e'));
    assert(await focusIsFirst(), 'focus did not go to the first emoji');
    assert(await page.locator(`[id="rx-btn-${target}"]`).getAttribute('aria-expanded') === 'true', 'the open button is not expanded');
    const labels = await emo.evaluateAll(bs => bs.map(b => [b.textContent, b.getAttribute('aria-label'), b.dataset.p]));
    const label = e => labels.find(l => l[0] === e);
    assert(label(CHECK)[1] === 'check, wakes if idle' && label(CHECK)[2] === 'wake_idle', 'check is not marked wake_idle: ' + label(CHECK));
    assert(label(PLUS)[1] === '+1, no wake' && label(PLUS)[2] === 'hold', '+1 is not marked hold: ' + label(PLUS));
    assert(label(EYES)[1] === 'eyes, remove yours', 'the own 👀 is not marked as yours: ' + label(EYES));
    const pb = await picker.boundingBox(), bb = await btn.boundingBox();
    assert(Math.abs(pb.x - bb.x) < 2 && (pb.y >= bb.y + bb.height || pb.y + pb.height <= bb.y), 'the picker is not beside the button: ' + JSON.stringify({ pb, bb }));

    // A peer reacts while the picker is open: the thread rebuilds, the picker
    // stays open with focus kept, and the new chip appears.
    const before = await msg.elementHandle();
    await react('claw', target, HEART);
    await chip(HEART).waitFor();
    assert(await before.evaluate(el => !el.isConnected), 'the message was not rebuilt by the event');
    assert(await picker.isVisible() && await focusIsFirst(), 'the picker lost its open state or focus on a rebuild');
    assert(await page.locator(`[id="rx-btn-${target}"]`).getAttribute('aria-expanded') === 'true', 'the rebuilt button is not expanded');
    await shot('rx08-thread-progress');

    // Pick ❓ with hold selected: the request carries priority hold, and the
    // picker's buttons are disabled while it is in flight.
    await picker.locator('.seg button[value="hold"]').click();
    assert(await picker.locator('.seg button[value="hold"]').getAttribute('class') === 'on', 'hold is not selected');
    assert(await emo.filter({ hasText: Q }).getAttribute('aria-label') === 'q, no wake', 'the override did not relabel ❓');
    const release = await hold('**/v1/reactions');
    const sent = page.waitForRequest(req => req.method() === 'POST' && req.url().endsWith('/v1/reactions'));
    await emo.filter({ hasText: Q }).click();
    const body = (await sent).postDataJSON();
    assert(body.message_id === target && body.emoji === Q && body.op === 'add' && body.priority === 'hold', 'the pick sent ' + JSON.stringify(body));
    assert(await emo.evaluateAll(bs => bs.every(b => b.disabled)), 'the picker buttons are enabled while the request is in flight');
    await release();
    await chip(Q).waitFor();
    await picker.waitFor({ state: 'hidden' });
    assert(await chip(Q).getAttribute('class') === 'rx-chip mine' && await chip(Q).getAttribute('title') === 'you', 'the new ❓ chip is not highlighted as yours');
    assert(await backOnButton(), 'focus did not return to the react button after the pick');
    await msg.hover();
    await shown();
    await shot('rx08-thread-success');

    // Clicking its own chip removes it, with no priority.
    const removed = page.waitForRequest(req => req.method() === 'POST' && req.url().endsWith('/v1/reactions'));
    await chip(Q).click();
    const rm = (await removed).postDataJSON();
    assert(rm.op === 'remove' && rm.emoji === Q && !('priority' in rm), 'the chip sent ' + JSON.stringify(rm));
    await chip(Q).waitFor({ state: 'detached' });

    // Escape closes the picker and returns focus to the react button; so does
    // a click outside it, and the override resets on close.
    await page.locator(`[id="rx-btn-${target}"]`).click();
    await picker.waitFor({ state: 'visible' });
    assert(await picker.locator('.seg button.on').count() === 0, 'the override survived the close');
    await page.keyboard.press('Escape');
    await picker.waitFor({ state: 'hidden' });
    assert(await backOnButton(), 'Escape did not return focus to the react button');
    assert(await page.locator(`[id="rx-btn-${target}"]`).getAttribute('aria-expanded') === 'false', 'the closed button is still expanded');
    await page.locator(`[id="rx-btn-${target}"]`).click();
    await picker.waitFor({ state: 'visible' });
    await page.locator('.thead h1').click();
    await picker.waitFor({ state: 'hidden' });
    assert(await backOnButton(), 'an outside click did not return focus to the react button');
    // A click on a control closes it too, but keeps the focus the click gave,
    // so the operator can type (the plan's recorded exception).
    const isOn = sel => page.evaluate(s => !!document.activeElement && document.activeElement.matches(s), sel);
    await page.locator(`[id="rx-btn-${target}"]`).click();
    await picker.waitFor({ state: 'visible' });
    await page.locator('.composer textarea').click();
    await picker.waitFor({ state: 'hidden' });
    assert(await isOn('.composer textarea'), 'a click on the composer did not keep its focus');
    assert(await page.locator(`[id="rx-btn-${target}"]`).getAttribute('aria-expanded') === 'false', 'the composer click left the button expanded');
    // Tabbing out of it closes it where focus went.
    await page.locator(`[id="rx-btn-${target}"]`).click();
    await picker.waitFor({ state: 'visible' });
    await page.keyboard.press('Shift+Tab');
    await picker.waitFor({ state: 'hidden' });
    assert(!(await isOn('.rx-pick *')), 'Shift+Tab left focus in the hidden picker');
    // The priority tip opens leftwards from the picker's right edge, inside
    // the thread column (it used to run under the aside).
    await page.locator(`[id="rx-btn-${target}"]`).click();
    await picker.waitFor({ state: 'visible' });
    await picker.locator('.seg button[value="hold"]').hover();
    const tip = picker.locator('.ptip[data-p="hold"]');
    await tip.waitFor({ state: 'visible' });
    const tb = await tip.boundingBox(), vb = await page.locator('#view').boundingBox();
    assert(tb.x >= vb.x && tb.x + tb.width <= vb.x + vb.width + 0.5, 'the picker tip leaves the thread column: ' + JSON.stringify({ tb, vb }));
    await page.keyboard.press('Escape');
    await page.keyboard.press('Escape');
    await picker.waitFor({ state: 'hidden' });

    // A rate-limited reaction: the toast names the en-AU time to retry at.
    await page.route('**/v1/reactions', route => route.fulfill({ status: 429, contentType: 'application/json', body: JSON.stringify({ error: { code: 'rate_limited', message: 'rate limited', details: { retry_after_seconds: 90 } } }) }));
    const t0 = Date.now();
    await chip(PLUS).click();
    const toast = page.locator('#toast.on.bad');
    await toast.waitFor();
    const text = await toast.textContent();
    const m = /Try again at (\d\d):(\d\d):(\d\d)\.$/.exec(text);
    assert(m, 'the toast does not name a time: ' + text);
    // Any second between the click and the toast is right.
    const want = await page.evaluate(([a, b]) => {
      const out = [];
      for (let t = a; t <= b + 999; t += 1000) out.push(new Date(t + 90000).toLocaleTimeString('en-AU', { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }));
      return out;
    }, [t0, Date.now()]);
    assert(want.includes(`${m[1]}:${m[2]}:${m[3]}`), `the toast time in "${text}" is not one of ${want}`);
    assert(await chip(PLUS).textContent() === PLUS + '1', 'a refused reaction changed the chip');
    await page.waitForFunction(() => new DOMMatrix(getComputedStyle(document.getElementById('toast')).transform).m42 === 0);
    await shot('rx08-thread-error');
    await page.unroute('**/v1/reactions');
  });
  console.log('ok - rx-thread: chips, react button, picker under churn, override, remove, focus, rate-limit toast');
} finally {
  await cleanBuild();
}
