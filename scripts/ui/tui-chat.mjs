// Task 19: the TUI chat tab against a real demo hub, in tmux.
import { assert, cleanBuild, run } from './lib.mjs';
import { withTui } from './tmux.mjs';

// Read-only looks at the private server tmux.mjs started for this process.
const tmux = (...args) => run('tmux', ['-L', `oui-${process.pid}`, ...args], { env: { ...process.env, TMUX: '' } });
const pane = () => tmux('capture-pane', '-t', 'ui:0.0', '-p');
const paneTitle = async () => (await tmux('display-message', '-p', '-t', 'ui:0.0', '#{pane_title}')).trim();

const reply = 'Reply from the operator TUI';
const refused = 'This cannot land, the budget is spent';

try {
  await withTui({}, async ({ demo, keys, waitFor, shot, asPeer }) => {
    // The list and the thread may reorder between looks, so this goes to the
    // top, walks down by the list's own order, and checks where it landed.
    async function openThread(id, header) {
      const deadline = Date.now() + 60000;
      for (;;) {
        const listed = await asPeer('operator', 'GET', '/v1/admin/threads?limit=200');
        assert(listed.ok, `thread list ${listed.status}`);
        const { threads } = await listed.json();
        const at = threads.findIndex(row => row.thread.thread_id === id);
        assert(at >= 0, 'thread is not listed');
        await keys(...Array(threads.length).fill('['));
        if (at) await keys(...Array(at).fill(']'));
        // The walk lands somewhere soon after its last key. Wherever that is,
        // the next pass starts again from the list as it stands then.
        for (let look = 0; look < 50; look++) {
          if ((await pane()).includes(header)) return;
          await new Promise(resolve => setTimeout(resolve, 100));
        }
        assert(Date.now() < deadline, 'thread did not open');
      }
    }

    await waitFor(' live ');
    await keys('2');
    await waitFor('threads [ ]');
    const title = await paneTitle();

    // The hostile thread first: nothing in it may reach the terminal.
    await openThread(demo.threads.hostile, '─ <img src=x onerror=alert(1)>pwn');
    await waitFor('<img src=x onerror=alert(1)>pwn');
    const hostile = await pane();
    assert(await paneTitle() === title, 'the pane title changed');
    assert(hostile.includes('vecnet | 1:inbox 2:chat 3:dash 4:subs'), 'the frame did not survive');
    assert(!hostile.includes(']52;') && !hostile.includes('[2J') && !hostile.includes('‮'), 'an escape reached the pane');

    await openThread(demo.threads.hark, '─ hark B196: radio end-phrase bug');
    await waitFor('Push classifier fix to hark master and release?');
    await waitFor('[1]yes');
    await waitFor('hark-listen.log');
    await waitFor('spilled');
    await waitFor('job job_');
    await waitFor('approval expired');
    await shot('19-tui-chat-initial');

    await keys('m');
    await waitFor('delivery matrix');
    await keys('Escape');
    await waitFor('Push classifier fix to hark master and release?');

    // The spilled message is second in the thread, through the real blob route.
    await keys(...Array(40).fill('k'));
    await keys('j', 'e');
    await waitFor('message.md · pag-hark-sleuth · lines 1-');
    await waitFor('Trace: listen loop waiting for the end phrase.');
    await keys('Escape');
    await waitFor('threads [ ]'); // a key right behind Escape would read as alt+key
    await keys(...Array(40).fill('j'));
    await waitFor('[1]yes');

    await keys('i');
    await waitFor('INSERT');
    await keys(reply);
    await waitFor(`${reply}█`);
    await shot('19-tui-chat-progress');

    await keys('Enter');
    await waitFor(`<max> ${reply}`);
    const lookup = await asPeer('operator', 'GET', `/v1/admin/threads/${demo.threads.hark}/deliveries?limit=200`);
    assert(lookup.ok, `matrix ${lookup.status}`);
    const { rows } = await lookup.json();
    const mine = rows.at(-1);
    assert(mine.excerpt.includes(reply), 'the newest matrix row is not the reply');
    await waitFor('pag·');
    for (const [name, state] of [['sleuth', 'processed'], ['codex', 'processed'], ['grok', 'processed']]) {
      const cell = mine.cells.find(c => c.recipient === demo.peers[name].id);
      assert(cell, `no delivery to ${name}`);
      const receipt = await asPeer(name, 'POST', '/v1/receipts', { delivery_id: cell.delivery_id, state });
      assert(receipt.ok, `receipt from ${name}: ${receipt.status}`);
    }
    await waitFor('pag✓✓');
    await waitFor('codex✓✓');
    await shot('19-tui-chat-success');
    await keys('Escape');
    await waitFor('NORMAL');

    await openThread(demo.threads.quota, '─ Quota exhausted');
    await keys('i');
    await waitFor('INSERT');
    await keys(refused);
    await waitFor(`${refused}█`);
    // A toast lasts seconds, and a loaded box can take longer than that to draw:
    // it counts only if it is still up after the shot, so resend until it is.
    for (let shown = false; !shown;) {
      await keys('Enter');
      await waitFor('thread budget exhausted');
      await shot('19-tui-chat-error');
      shown = (await pane()).includes('thread budget exhausted');
    }
    assert((await pane()).includes(`${refused}█`), 'the text was not kept');
  });
  console.log('TUI chat passed: thread view, hostile thread inert, matrix, spill, send, receipts, budget error with text kept');
} finally { await cleanBuild(); }
