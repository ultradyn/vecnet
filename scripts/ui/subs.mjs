// Task 13: subscription health in the dashboard and the inbox, in a real browser.
import { assert, cleanBuild, withDemo } from './lib.mjs';
import { expiresIn } from '../../internal/ui/static/subs-model.js';

const SEEDED = ['ok', 'late', 'paused_token', 'paused_failed', 'paused_auth', 'paused_operator'];
const RESUME = '**/v1/admin/subscriptions/*/resume';

try {
  await withDemo({}, async ctx => {
    const { page, shot, asPeer, hold, fail, allowConsole, countRequests } = ctx;
    // The error shot provokes this one failed resume on purpose.
    allowConsole(/status of 500/);
    // lib.mjs hold() unroutes as it releases; a second route keeps Playwright's
    // interception on, as in shell.mjs.
    await page.route('**/__keep-interception__', route => route.continue());

    const hubSubs = async () => {
      const r = await asPeer('operator', 'GET', '/v1/admin/subscriptions');
      assert(r.ok, `subscriptions: ${r.status}`);
      return r.json();
    };
    const seeded = await hubSubs();
    const byHealth = Object.fromEntries(seeded.map(s => [s.health, s]));
    assert(seeded.length === 6, `demo has ${seeded.length} subscriptions`);
    for (const health of SEEDED) assert(byHealth[health], `demo has no ${health} subscription`);

    // Dashboard: every seeded health, read-only.
    await page.evaluate(() => { location.hash = '#dash'; });
    await page.getByRole('heading', { name: 'Dashboard', exact: true }).waitFor();
    const section = page.locator('section.subs');
    await section.locator('tr.sub').nth(5).waitFor();
    assert(await section.locator('tr.sub').count() === 6, 'dashboard does not show six subscriptions');
    for (const health of SEEDED) {
      const s = byHealth[health];
      const tr = section.locator(`tr.sub[data-sub="${s.sub_id}"]`);
      assert(await tr.locator(`.chip.health[data-health="${health}"]`).count() === 1, `${health} row has no ${health} chip`);
      assert(await tr.getAttribute('title') === (s.health_note || null), `${health} row tooltip is not its health_note`);
      const note = section.locator(`tr.note[data-sub="${s.sub_id}"]`);
      if (health === 'ok') assert(await note.count() === 0, 'ok row shows a note');
      else {
        assert(await note.isVisible(), `${health} note is not visible`);
        assert(await note.textContent() === s.health_note, `${health} note text`);
      }
      if (health.startsWith('paused_')) {
        assert(/^paused\b/.test(await tr.locator('.chip.health').textContent()), `${health} chip does not read as paused`);
      }
    }
    for (const col of ['Peer', 'Host', 'Health', 'Expires', 'Last refresh', 'Last delivery', 'Attempts', 'Cursor']) {
      assert(await section.locator('th', { hasText: col }).count() === 1, `no ${col} column`);
    }
    // Expires is relative to the hub's clock, which runs in real time from the
    // seed, so the expected text comes from hub data, not fixed windows: the
    // cell must equal expiresIn(expires_at, t) for some hub instant t between
    // a read just before and just after, widened by one 15 s redraw tick and
    // a little clock-offset slack. This holds however slow the slice is.
    const hubNowMs = async () => {
      const r = await asPeer('operator', 'GET', '/v1/admin/inbox?limit=1');
      assert(r.ok, `inbox: ${r.status}`);
      const t = Date.parse((await r.json()).now);
      assert(Number.isFinite(t), 'inbox has no now');
      return t;
    };
    const expTxt = async health => section.locator(`tr.sub[data-sub="${byHealth[health].sub_id}"] .exp`).textContent();
    const t0 = await hubNowMs();
    const shown = {};
    for (const health of ['ok', 'late', 'paused_token']) shown[health] = await expTxt(health);
    const t1 = await hubNowMs();
    for (const health of ['ok', 'late', 'paused_token']) {
      const exp = byHealth[health].expires_at;
      const want = new Set();
      for (let t = t0 - 17000; t <= t1 + 2000; t += 500) want.add(expiresIn(exp, t));
      assert(want.has(shown[health]), `${health} expires: ${shown[health]}, want one of ${[...want].join(' | ')}`);
    }
    // paused_token is seeded already expired, so it must read as expired.
    assert(/^expired /.test(shown.paused_token), `paused_token expires: ${shown.paused_token}`);
    // Named assertion: dashboard section has no buttons.
    assert(await section.locator('button, [role=button], input, select, textarea').count() === 0, 'dashboard section has buttons');
    assert(await page.locator('#view button').count() === 0, 'dashboard has buttons');
    // On the full dashboard the section sits below Peers and Threads; bring it into the shot.
    await section.scrollIntoViewIfNeeded();
    await shot('13-subs-initial');

    // Inbox attention: Resume only where the hub offers it.
    await page.evaluate(() => { location.hash = '#inbox'; });
    const attn = page.locator('section.attn');
    await attn.getByRole('heading', { name: /^Subscriptions/ }).waitFor();
    const item = health => attn.locator(`.attn-item[data-sub="${byHealth[health].sub_id}"]`);
    for (const health of SEEDED.filter(h => h !== 'ok')) await item(health).waitFor();
    assert(await attn.locator(`.attn-item[data-sub="${byHealth.ok.sub_id}"]`).count() === 0, 'ok subscription asks for attention');
    const inboxRes = await asPeer('operator', 'GET', '/v1/admin/inbox?limit=1');
    assert(inboxRes.ok, `inbox: ${inboxRes.status}`);
    const attention = (await inboxRes.json()).attention;
    for (const health of SEEDED.filter(h => h !== 'ok')) {
      const want = attention.find(a => a.id === byHealth[health].sub_id);
      assert(want, `hub has no attention item for ${health}`);
      const got = await item(health).locator('.sum').textContent();
      assert(got === want.summary, `${health} summary: ${got}`);
      assert((want.action === 'resume') === (health === 'paused_failed' || health === 'paused_operator'), `hub action for ${health}: ${want.action}`);
    }

    // Named assertion (Review Focus 5): paused_token and paused_auth rows have no Resume button anywhere.
    const tokenId = byHealth.paused_token.sub_id, authId = byHealth.paused_auth.sub_id;
    for (const id of [tokenId, authId]) {
      assert(await page.locator(`[data-sub="${id}"] button, button[data-sub="${id}"]`).count() === 0, `${id} has a button`);
    }
    assert(await item('late').locator('button').count() === 0, 'late item has a button');
    const resumes = page.getByRole('button', { name: 'Resume' });
    assert(await resumes.count() === 2, `page shows ${await resumes.count()} Resume buttons`);
    assert(await item('paused_failed').getByRole('button', { name: 'Resume' }).count() === 1, 'paused_failed has no Resume');
    assert(await item('paused_operator').getByRole('button', { name: 'Resume' }).count() === 1, 'paused_operator has no Resume');

    // Error: the resume fails, the item stays, and the button comes back.
    await fail(RESUME, 500, 'demo_failure', 'Callback still refusing');
    await item('paused_failed').getByRole('button', { name: 'Resume' }).click();
    await item('paused_failed').locator('.err', { hasText: 'Callback still refusing' }).waitFor();
    const retry = item('paused_failed').getByRole('button', { name: 'Resume' });
    await retry.waitFor();
    assert(await retry.isEnabled(), 'Resume did not come back after a failure');
    assert((await hubSubs()).find(s => s.sub_id === byHealth.paused_failed.sub_id).paused_reason === 'delivery_failed', 'failed resume changed the hub');
    await page.locator('#toast.on', { hasText: 'Resume failed' }).waitFor();
    await shot('13-subs-error');
    await page.unroute(RESUME);

    // Progress: Resume held. One click sends one request, and the button stays down.
    const release = await hold(RESUME);
    const sent = () => countRequests(req => req.method() === 'POST' && /\/resume$/.test(req.url()));
    const before = sent();
    await item('paused_failed').getByRole('button', { name: 'Resume' }).click();
    const busy = item('paused_failed').getByRole('button', { name: 'Resuming…' });
    await busy.waitFor();
    assert(await busy.isDisabled(), 'held Resume is clickable');
    await busy.click({ force: true });
    assert(sent() === before + 1, `held Resume sent ${sent() - before} requests`);
    assert(await item('paused_failed').locator('.err').count() === 0, 'old error survived a new attempt');
    await page.locator('#toast.on').waitFor({ state: 'hidden' });
    await shot('13-subs-progress');
    await release();

    // Named assertion: paused_failed Resume clears it from the inbox.
    await item('paused_failed').waitFor({ state: 'detached' });
    assert((await hubSubs()).find(s => s.sub_id === byHealth.paused_failed.sub_id).paused_reason === null, 'paused_failed still paused at the hub');

    // Named assertion: paused_operator Resume clears it from the inbox.
    await item('paused_operator').getByRole('button', { name: 'Resume' }).click();
    await item('paused_operator').waitFor({ state: 'detached' });
    assert((await hubSubs()).find(s => s.sub_id === byHealth.paused_operator.sub_id).paused_reason === null, 'paused_operator still paused at the hub');

    // What is left offers no Resume at all.
    await item('paused_token').waitFor();
    await item('paused_auth').waitFor();
    assert(await page.getByRole('button', { name: /^Resum/ }).count() === 0, 'a Resume button is left');
    await page.locator('#toast.on').waitFor({ state: 'hidden' });
    await page.evaluate(() => Promise.all(document.getElementById('toast').getAnimations().map(a => a.finished)));
    await shot('13-subs-success');

    // Bodies are untrusted: nothing in either view became an element.
    assert(await page.locator('#view img').count() === 0, 'a summary became an element');
  });
  console.log('Subs passed: six health states, read-only dashboard, Resume only for resume items, failure and recovery');
} finally { await cleanBuild(); }
