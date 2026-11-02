// Task 21: subscription health against a real demo hub, in tmux.
import { assert, cleanBuild } from './lib.mjs';
import { withTui } from './tmux.mjs';

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

// The pane title with nothing after the row count: no reload is running.
const settled = '6 rows ─';

try {
  await withTui({}, async ({ keys, waitFor, shot, stall, clear, controlStatus, fail, advance, asPeer }) => {
    const response = await asPeer('operator', 'GET', '/v1/admin/subscriptions');
    assert(response.ok, `subscriptions: ${response.status}`);
    const rows = await response.json();
    assert(rows.length === 6, `demo has ${rows.length} subscriptions`);
    const notOk = rows.filter(row => row.health !== 'ok').length;
    assert(notOk > 0, 'demo has no unhealthy subscription');

    // Row order comes from random ids, so walk to a row by its index.
    const select = async health => {
      const at = rows.findIndex(row => row.health === health);
      assert(at >= 0, `no ${health} row`);
      await keys('g', ...Array(at).fill('j'));
      const { health_note: note } = rows[at];
      await waitFor(note ? note.slice(0, 60) : 'no note');
      return rows[at];
    };

    await waitFor(' live ');
    await keys('4');
    await waitFor(settled);
    await waitFor(`4:subs ${notOk}`);
    for (const { health } of rows) await waitFor(health);
    await waitFor('expires in');
    await waitFor('j/k move  read-only');
    await shot('21-tui-subs-initial');

    // Paused rows name their reason, and every row's note reaches the detail pane.
    for (const { health, paused_reason: reason } of rows) {
      await select(health);
      if (reason) await waitFor(`paused: ${reason}`);
    }

    await select('paused_token');
    await stall('1h');
    try {
      await keys('C-l');
      await until('a request to reach the stall', async () => (await controlStatus()).stalled_requests > 0);
      await waitFor('reloading');
      await waitFor('paused: token_expired'); // the selected row stays while it runs
      await shot('21-tui-subs-progress');
    } finally { await clear('stall'); }
    await waitFor(settled);

    await select('paused_failed');
    await waitFor('paused: delivery_failed');
    await shot('21-tui-subs-success');

    // The hub's clock moves and the client's does not: the row due for refresh
    // runs out, so this client can only learn that from the hub.
    await advance('3m');
    await keys('C-l');
    await waitFor(`expired${' '.repeat(10)}expired `); // health, then expires in, on one row

    await fail('/v1/admin/subscriptions', 500, '1h');
    await keys('C-l');
    await waitFor('load failed');
    await waitFor('demo_failure');
    await waitFor('showing the last data');
    await waitFor('paused_failed'); // the last data stays on screen
    await shot('21-tui-subs-error');
    await clear('fail');
  });
  console.log('TUI subs passed: table, notes, reload, hub clock, load failure');
} finally { await cleanBuild(); }
