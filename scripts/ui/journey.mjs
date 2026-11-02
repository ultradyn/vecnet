// Both real clients share one disposable hub and operator identity.
import { assert, cleanBuild, run, withDemo } from './lib.mjs';
import { withTui } from './tmux.mjs';

const tmux = (...args) => run('tmux', ['-L', `oui-${process.pid}`, ...args], { env: { ...process.env, TMUX: '' } });
const pane = () => tmux('capture-pane', '-t', 'ui:0.0', '-p');
async function until(label, check, timeout = 60000) {
  const deadline = Date.now() + timeout;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${label}`);
    await new Promise(resolve => setTimeout(resolve, 100));
  }
}

try {
  await withDemo({}, async ctx => {
    const { page, demo, asPeer, advance, hold, fail, countRequests, allowConsole, shot } = ctx;
    await withTui({ env: ctx }, async tui => {
      assert(tui.demo.url === demo.url, 'journey clients must share the same demo hub');
      const { keys, waitFor } = tui;
      const json = async (peer, method, path, body) => {
        const response = await asPeer(peer, method, path, body);
        assert(response.ok, `${method} ${path}: ${response.status}${response.ok ? "" : " " + await response.text()}`);
        return response.status === 204 ? null : response.json();
      };
      const inbox = () => json('operator', 'GET', '/v1/admin/inbox?limit=200');
      const card = id => page.locator(`#view .item[data-id="${id}"]`);
      const noApr = text => until(`closed APR ${text}`, async () => !(await pane()).split('\n').some(line => line.includes('APR') && line.includes(text)));
      const failures = [];
      const check = async (name, fn) => {
        try { await fn(); console.log('ok - ' + name); }
        catch (error) { failures.push({ name, message: error.message }); console.error('FAIL - ' + name + ': ' + error.message); }
      };
      // Install before reloading so all app timers use the controlled clock.
      await page.clock.install();
      await page.reload({ waitUntil: 'load' });
      const navigations = [];
      page.on('framenavigated', frame => { if (frame === page.mainFrame()) navigations.push(frame.url()); });
      await page.locator('#conn.live').waitFor();
      await waitFor(' live ');
      await waitFor('apr 2');
      await shot('23-journey-initial');

      // Answer using real TUI keys; the browser reacts through its event tail.
      await check('TUI approval leaves web inbox without reload', async () => {
        const first = (await inbox()).approvals[0];
        await card(first.approval_id).waitFor();
        await keys('a');
        await waitFor('any other key cancels');
        await tui.shot('23-journey-progress');
        await keys('1');
        await waitFor('apr 1');
        await card(first.approval_id).waitFor({ state: 'detached' });
        assert(navigations.length === 0, 'approval required a browser reload');
      });

      // Reading from the browser changes the shared unread count in the TUI.
      await check('web thread read drops TUI unread count', async () => {
        const before = await inbox();
        assert(before.unread > 0, 'seed must have unread deliveries');
        await page.locator(`#view a.item[href="#thread/${demo.threads.hosting}"]`).first().click();
        await until('hub read state', async () => (await inbox()).unread < before.unread);
        const after = await inbox();
        await waitFor(`unread ${after.unread}`);
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

      // A changed state must reach the other status bar before any heartbeat.
      await check('presence follows across clients and both heartbeats keep it', async () => {
        await page.locator('#me button.ptog', { hasText: 'Set idle' }).click();
        await page.locator('#me .who', { hasText: /^max is idle$/ }).waitFor();
        await waitFor('max@vecnet · idle');
        const isBeat = req => req.method() === 'POST' && new URL(req.url()).pathname === '/v1/presence';
        const beat = page.waitForRequest(isBeat);
        await page.clock.fastForward('20:00');
        assert((await beat).postDataJSON().state === 'idle', 'web heartbeat overwrote idle');
        await until('web heartbeat persisted', async () => (await inbox()).me.presence === 'idle');
        // The real TUI heartbeat runs on its wall clock. Freeze browser timers
        // after its own beat, then wait for a persisted report from the TUI.
        await page.clock.pauseAt(new Date(await page.evaluate(() => Date.now())));
        const presenceAt = (await inbox()).me.presence_at;
        await until('TUI heartbeat persisted', async () => (await inbox()).me.presence_at !== presenceAt, 21 * 60000);
        assert((await inbox()).me.presence === 'idle', 'TUI heartbeat overwrote idle');
        await waitFor('max@vecnet · idle');
      });

      // Each check has its own preconditions, so one failure does not hide
      // unrelated failures in this final integration gate.
      await check('hub advance closes approval in both clients with expired history', async () => {
        // Create a fresh approval on the hub clock.
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
      // Open the newest thread through the real command line, without marking
      // it read in the TUI before the browser's read assertion above.
      await keys(':');
      await waitFor('COMMAND');
      await keys('thread 1', 'Enter');
      await waitFor('approval expired');
      await shot('23-journey-success');
      });

      // Hold the next poll before injecting 401. A peer write wakes any poll
      // that was already in flight. Virtual time proves no retries follow.
      await check('exactly one events 401 returns to sign-in, no retries', async () => {
        allowConsole(/status of 401/);
        const rejected = [];
        page.on('response', response => {
          if (new URL(response.url()).pathname === '/v1/events' && response.status() === 401) rejected.push(response);
        });
        await page.route('**/__keep-interception__', route => route.continue());
        const release = await hold('**/v1/events*');
        const parked = page.waitForRequest(req => req.url().includes('/v1/events'));
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
      assert(failures.length === 0, 'two-client journey failures: ' + JSON.stringify(failures));
    });
  });
  console.log('Two-client journey passed');
} finally { await cleanBuild(); }
