// Both real clients share one disposable hub and operator identity.
import { assert, cleanBuild, run, withDemo } from './lib.mjs';
import { withTui } from './tmux.mjs';

const tmux = (...args) => run('tmux', ['-L', `oui-${process.pid}`, ...args], { env: { ...process.env, TMUX: '' } });
const pane = () => tmux('capture-pane', '-t', 'ui:0.0', '-p');
async function until(label, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${label}`);
    await new Promise(resolve => setTimeout(resolve, 100));
  }
}

try {
  await withDemo({ signedIn: false }, async ctx => {
    const { page, demo, asPeer, advance, hold, fail, countRequests, allowConsole, shot } = ctx;
    // Install before signing in: no reload, and every application timer uses
    // this clock.
    // The TUI remains the real binary with a real heartbeat, shortened to 5 s through withTui({ beatEvery }).
    await page.clock.install();
    await page.locator('#secret').fill(demo.operator_secret);
    await page.locator('#login-form button[type="submit"]').click();
    await page.locator('#conn.live').waitFor();
    const navigations = [];
    page.on('framenavigated', frame => { if (frame === page.mainFrame()) navigations.push(frame.url()); });

    await withTui({ env: ctx, beatEvery: '5s' }, async tui => {
      assert(tui.demo.url === demo.url, 'journey clients must share the same demo hub');
      const { keys, waitFor } = tui;
      const json = async (peer, method, path, body) => {
        const response = await asPeer(peer, method, path, body);
        assert(response.ok, `${method} ${path}: ${response.status}`);
        return response.status === 204 ? null : response.json();
      };
      const inbox = () => json('operator', 'GET', '/v1/admin/inbox?limit=200');
      const card = id => page.locator(`#view .item[data-id="${id}"]`);
      const noApr = text => until(`closed APR ${text}`, async () => !(await pane()).split('\n').some(line => line.includes('APR') && line.includes(text)));
      const check = async (name, fn) => { await fn(); console.log('ok - ' + name); };
      // Observe the browser's actual event response, rather than poll snapshots
      // to make a stale view look current. Task 24's invalidation must arrive.
      const operatorView = (reason, thread = null) => page.waitForResponse(async response => {
        if (new URL(response.url()).pathname !== '/v1/events' || response.status() !== 200) return false;
        const batch = await response.json();
        return batch.events.some(event => event.type === 'operator_view' && event.data.reason === reason && event.data.thread_id === thread);
      });
      await waitFor(' live ');
      await waitFor('apr 2');
      await shot('23-journey-initial');

      await check('TUI approval leaves web inbox without reload', async () => {
        const first = (await inbox()).approvals[0];
        await card(first.approval_id).waitFor();
        await keys('a');
        await waitFor('any other key cancels');
        await tui.shot('23-journey-progress');
        const invalidation = operatorView('sent', first.thread_id);
        await keys('1');
        await invalidation;
        await waitFor('apr 1');
        await card(first.approval_id).waitFor({ state: 'detached' });
        assert(navigations.length === 0, 'approval required a browser navigation');
      });

      await check('web thread read drops TUI unread count', async () => {
        const before = await inbox();
        assert(before.unread > 0, 'seed must have unread deliveries');
        await waitFor(` · ${before.unread} unread · `);
        const invalidation = operatorView('read', demo.threads.hosting);
        // The thread's one unread message is on screen, so it is read by its
        // own receipt a second after opening (CoS unread ruling).
        const read = page.waitForResponse(response => response.request().method() === 'POST' && new URL(response.url()).pathname === '/v1/receipts' && response.ok());
        await page.locator(`#view a.item[href="#thread/${demo.threads.hosting}"]`).first().click();
        await read;
        await invalidation;
        const after = await inbox();
        assert(after.unread < before.unread, 'web read did not lower the shared unread count');
        await waitFor(` · ${after.unread} unread · `);
      });
      await page.locator('a[href="#inbox"]').first().click();
      await card(demo.approvals[1]).waitFor();

      await check('peer wake reaches both clients', async () => {
        await json('codex', 'POST', '/v1/messages', {
          idempotency_key: 'journey-wake', recipients: [demo.operator_peer],
          thread_title: 'Two-client wake', priority: 'wake', content: { text: 'Journey wake reaches both clients' },
        });
        await page.locator('#view a.item', { hasText: 'Journey wake reaches both clients' }).waitFor();
        await waitFor('Journey wake reaches both clients');
      });

      await check('presence follows across clients and both heartbeats keep it', async () => {
        const invalidation = operatorView('presence');
        await page.locator('#me button.ptog', { hasText: 'Set idle' }).click();
        await invalidation;
        await page.locator('#me .who', { hasText: /^max is idle$/ }).waitFor();
        await waitFor('max@vecnet · idle');
        const isBeat = req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/presence';
        const beat = page.waitForRequest(isBeat);
        const posted = page.waitForResponse(response => isBeat(response.request()) && response.status() === 204);
        await page.clock.fastForward('20:00');
        assert((await beat).postDataJSON().state === 'idle', 'web heartbeat overwrote idle');
        await posted;
        // pauseAt advances to its target. Keep that target ahead of the
        // running clock while Playwright transports the command, or a busy
        // build slice can turn an exact-now target into a past timestamp.
        await page.clock.pauseAt(new Date(await page.evaluate(() => Date.now() + 60000)));
        const afterWeb = await inbox();
        assert(afterWeb.me.presence === 'idle', 'web heartbeat did not preserve idle');

        // Subscribe a real peer to presence. With browser timers paused and no
        // more test presence writes, its next operator report can only come
        // from the real TUI heartbeat. Long-poll the event log, never snapshots.
        await json('codex', 'PATCH', '/v1/peers/me', { subscribe_presence: 'all' });
        const started = Date.now();
        const deadline = started + 60000;
        let cursor = 'head', report;
        console.log('waiting for the real TUI presence heartbeat (5 s beat, up to 60 s)');
        while (!report && Date.now() < deadline) {
          const batch = await json('codex', 'GET', `/v1/events?after=${encodeURIComponent(cursor)}&wait=25`);
          cursor = batch.next_cursor;
          report = batch.events.find(event => event.type === 'presence' && event.data.peer_id === demo.operator_peer);
        }
        assert(report, 'real TUI heartbeat did not arrive');
        console.log(`heartbeat after ${(Date.now() - started) / 1000}s`);
        assert(report.data.state === 'idle', 'TUI heartbeat overwrote idle');
        assert(report.data.at !== afterWeb.me.presence_at, 'TUI heartbeat did not refresh its timestamp');
        assert((await inbox()).me.presence === 'idle', 'TUI heartbeat did not preserve the hub state');
        await page.locator('#me .who', { hasText: /^max is idle$/ }).waitFor();
        await waitFor('max@vecnet · idle');
      });

      await check('hub advance closes approval in both clients with expired history', async () => {
        const approvalID = 'apr_01J00000000000000000002323';
        const now = Date.parse((await inbox()).now);
        const ask = await json('codex', 'POST', '/v1/messages', {
          idempotency_key: 'journey-expiry', recipients: [demo.operator_peer],
          thread_title: 'Journey expiry', priority: 'wake', kind: 'approval_request', content: { text: 'Journey approval must expire' },
          approval_request: { approval_id: approvalID, question: 'Journey approval must expire', options: ['yes', 'no'], expires_at: new Date(now + 600000).toISOString() },
        });
        await card(approvalID).waitFor();
        await waitFor('Journey approval must expire');
        await advance('15m');
        await card(approvalID).waitFor({ state: 'detached' });
        await noApr('Journey approval must expire');
        await page.evaluate(id => { location.hash = '#thread/' + id; }, ask.thread_id);
        await page.locator('#view .st', { hasText: /^expired$/ }).waitFor();
        const history = await json('operator', 'GET', `/v1/threads/${ask.thread_id}/messages?limit=200`);
        assert(history.messages.some(m => m.approval_response?.approval_id === approvalID && m.approval_response.decision === 'expired'), 'hub expired response missing');
        await keys(':');
        await waitFor('COMMAND');
        await keys('thread 1', 'Enter');
        await waitFor('approval expired');
        await shot('23-journey-success');
      });

      await check('exactly one events 401 returns to sign-in, no retries', async () => {
        allowConsole(/status of 401/);
        const rejected = [];
        page.on('response', response => {
          if (new URL(response.url()).pathname === '/v1/events' && response.status() === 401) rejected.push(response);
        });
        // Keep interception enabled when hold's release removes its own route.
        await page.route('**/__keep-interception__', route => route.continue());
        const release = await hold('**/v1/events*');
        const parked = page.waitForRequest(req => new URL(req.url()).pathname === '/v1/events');
        await json('codex', 'POST', '/v1/messages', { idempotency_key: 'journey-auth-nudge', thread_id: demo.threads.hosting, priority: 'hold', content: { text: 'Wake the next web poll' } });
        await parked;
        await fail('**/v1/events*', 401, 'unauthenticated');
        await release();
        await page.locator('#login:not([hidden])').waitFor();
        assert(rejected.length === 1, `expected one failed events call, got ${rejected.length}`);
        const eventsAfter = countRequests('/v1/events');
        await page.clock.fastForward('10:00');
        assert(countRequests('/v1/events') === eventsAfter, 'events retried after 401');
        assert(await page.evaluate(() => localStorage.getItem('vecnet.ui.secret')) === null, 'rejected secret retained');
        await shot('23-journey-error');
      });
      console.log('Two-client journey passed');
    });
  });
} finally { await cleanBuild(); }
