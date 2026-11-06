// Task 9: real web and terminal reaction clients on one disposable hub.
// No screenshots; rx-thread.mjs and rx-tui.mjs cover the visual states.
import { readFile } from 'node:fs/promises';
import { assert, cleanBuild, withDemo } from './lib.mjs';
import { withTui } from './tmux.mjs';

// Keep the journey discoverable from the operator's entry points.
async function checkGuide() {
  const guide = await readFile(new URL('../../docs/OPERATOR.md', import.meta.url), 'utf8');
  const readme = await readFile(new URL('../../README.md', import.meta.url), 'utf8');
  assert(/^## Reactions$/m.test(guide), 'operator guide needs a Reactions section');
  assert(readme.includes('docs/OPERATOR.md#reactions'), 'README needs a link to the reactions guide');
}

try {
  await checkGuide();
  await withDemo({}, async ctx => {
    const { page, demo, asPeer } = ctx;
    const api = async (peer, method, path, body) => {
      const response = await asPeer(peer, method, path, body);
      assert(response.ok, `${peer} ${method} ${path}: ${response.status} ${response.ok ? '' : await response.text()}`);
      return response.status === 204 ? null : response.json();
    };
    const react = (peer, message_id, op) => api(peer, 'POST', '/v1/reactions', { message_id, emoji: '✅', op, priority: 'wake' });
    const events = (peer, after) => api(peer, 'GET', `/v1/events?peer=${peer === 'operator' ? demo.operator_peer : demo.peers[peer].id}&after=${encodeURIComponent(after)}&wait=0`);
    // Explicit cursors isolate this journey from seed events and the clients'
    // shared ack. Drain every page to make exact delivery counts meaningful.
    const since = async (peer, after) => {
      const out = [];
      for (;;) {
        const batch = await events(peer, after);
        out.push(...batch.events);
        after = batch.next_cursor;
        if (!batch.events.length) return out;
      }
    };
    const deliveries = batch => batch.filter(e => e.type === 'delivery' && e.data.message.kind === 'reaction');
    const inbox = () => api('operator', 'GET', '/v1/admin/inbox?limit=200');

    await page.locator('#conn.live').waitFor();
    const agentMessage = await api('codex', 'POST', '/v1/messages', {
      idempotency_key: 'rx09-agent', recipients: [demo.operator_peer],
      thread_title: 'Reaction journey', priority: 'wake', content: { text: 'Reaction journey agent target' },
    });
    const thread = agentMessage.thread_id;
    await page.evaluate(id => { location.hash = '#thread/' + id; window.__reactionJourney = true; }, thread);
    const message = id => page.locator(`[id="m-${id}"]`);
    const chip = (id, emoji) => message(id).locator('.rx-chip', { hasText: emoji });
    await message(agentMessage.message_id).waitFor();
    const navigations = [];
    page.on('request', request => {
      if (request.isNavigationRequest() && request.frame() === page.mainFrame()) navigations.push(request.url());
    });

    await withTui({ env: ctx }, async ({ demo: terminalDemo, keys, waitFor }) => {
      assert(terminalDemo.url === demo.url, 'both clients must share one demo hub');
      await waitFor(' live ');
      await waitFor('Reaction journey agent target');
      await keys(':');
      await waitFor('COMMAND');
      await keys('thread 1', 'Enter');
      await waitFor('━ Reaction journey');
      await waitFor('Reaction journey agent target');
      const agentCursor = (await events('codex', 'head')).next_cursor;

      // 1. The real web picker supplies the per-emoji wake_idle default.
      const target = agentMessage.message_id;
      await message(target).hover();
      await message(target).locator(`[id="rx-btn-${target}"]`).click();
      const picker = page.locator('.rx-pick[role=dialog]');
      await picker.waitFor();
      const posted = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === '/v1/reactions');
      await picker.locator('.rx-e', { hasText: '✅' }).click();
      const response = await posted;
      const body = response.request().postDataJSON();
      assert(response.ok() && body.message_id === target && body.emoji === '✅' && body.op === 'add' && body.priority === 'wake_idle', 'web check must use default wake_idle');
      const added = await response.json();
      assert(added.changed && added.wake?.recipient === demo.peers.codex.id, 'web check must create a wake for the target sender');
      await chip(target, '✅').waitFor();
      await waitFor('[check: you]');
      const first = deliveries(await since('codex', agentCursor));
      assert(first.length === 1 && first[0].data.message.in_reply_to === target && first[0].data.delivery_id === added.wake.delivery_id, 'agent must receive exactly one reaction Delivery referring to its target');
      console.log('ok - web default check reaches the live TUI and one agent reaction delivery');

      // 2. The operator's own chip removes the row without a second wake.
      const removedResponse = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === '/v1/reactions');
      await chip(target, '✅').click();
      const rm = await removedResponse;
      const removal = await rm.json();
      assert(rm.ok() && rm.request().postDataJSON().op === 'remove' && removal.changed && removal.wake === null, 'chip toggle must remove without waking');
      await chip(target, '✅').waitFor({ state: 'detached' });
      await waitFor(/^(?![\s\S]*\[check: you\])/);
      assert(deliveries(await since('codex', agentCursor)).length === 1, 'removing the web check must not send another Delivery');
      console.log('ok - web chip removes check without another delivery');

      // 3. TUI eyes uses hold and appears in the already-open browser.
      await keys(':');
      await waitFor('COMMAND');
      await keys('react 1 eyes', 'Enter');
      await waitFor('[eyes: you]');
      await chip(target, '👀').waitFor();
      assert(await chip(target, '👀').getAttribute('aria-pressed') === 'true', 'TUI eyes must be marked as the operator reaction in the web');
      assert(deliveries(await since('codex', agentCursor)).length === 1, 'TUI eyes must not create a Delivery (default hold)');
      console.log('ok - TUI eyes reaches the live web chip without waking the agent');

      // 4. Accept the first agent wake before removing it: queued wakes are
      // withdrawn on remove (§17.5), while accepted ones remain in the inbox.
      const operatorMessage = await api('operator', 'POST', '/v1/messages', {
        idempotency_key: 'rx09-operator', thread_id: thread, priority: 'hold', content: { text: 'Reaction journey operator target' },
      });
      const opTarget = operatorMessage.message_id;
      await message(opTarget).waitFor();
      await waitFor('Reaction journey operator target');
      const opCursor = (await events('operator', 'head')).next_cursor;
      const countInbox = snapshot => snapshot.deliveries.filter(d => d.kind === 'reaction' && d.thread_id === thread).length;
      const before = countInbox(await inbox());
      const wake = await react('codex', opTarget, 'add');
      assert(wake.changed && wake.wake?.recipient === demo.operator_peer, 'agent check must wake the operator');
      await chip(opTarget, '✅').waitFor();
      await waitFor('[check: codex]');
      const batch = await events('operator', opCursor);
      await api('operator', 'POST', '/v1/ack', { cursor: batch.next_cursor });
      const removed = await react('codex', opTarget, 'remove');
      assert(removed.changed && removed.wake === null && removed.superseded.length === 0, 'removing an accepted wake must neither supersede nor wake');
      await chip(opTarget, '✅').waitFor({ state: 'detached' });
      await waitFor(/^(?![\s\S]*\[check: codex\])/);
      const readded = await react('codex', opTarget, 'add');
      assert(readded.changed && readded.wake === null && readded.superseded.length === 0, 'agent re-add must respect once-ever wake eligibility');
      await chip(opTarget, '✅').waitFor();
      await waitFor('[check: codex]');
      const opDeliveries = deliveries(await since('operator', opCursor));
      assert(opDeliveries.length === 1 && opDeliveries[0].data.message.in_reply_to === opTarget && opDeliveries[0].data.delivery_id === wake.wake.delivery_id, 'agent add/remove/re-add must produce exactly one operator reaction Delivery');
      assert(countInbox(await inbox()) === before + 1, 'operator inbox must gain exactly one reaction Delivery');
      console.log('ok - agent check survives remove/re-add in both clients with one operator inbox delivery');

      // 5. A reaction wake is never a reply target.
      const rejected = await asPeer('codex', 'POST', '/v1/messages', {
        idempotency_key: 'rx09-reply', thread_id: thread, in_reply_to: wake.wake.message_id,
        priority: 'hold', content: { text: 'Do not create a reaction loop' },
      });
      const error = await rejected.json();
      assert(rejected.status === 422 && error.error?.code === 'not_replyable', `reaction reply must be 422 not_replyable, got ${rejected.status} ${JSON.stringify(error)}`);
      assert(navigations.length === 0 && await page.evaluate(() => window.__reactionJourney === true), 'reaction synchronization must not reload the web client');
      console.log('ok - replying to a reaction is refused with 422 not_replyable; web stayed loaded');
    });
  });
  console.log('Reaction journey passed');
} finally { await cleanBuild(); }
