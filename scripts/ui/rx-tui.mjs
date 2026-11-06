// Reactions Task 7: ASCII chips and :react in the TUI chat, against a real
// demo hub in tmux. Two agents react to an operator message through asPeer
// before the thread opens; the operator then toggles check from the command
// line, and an unknown name is refused with the line kept.
import { assert, bootDemo, cleanBuild, run } from './lib.mjs';
import { withTui } from './tmux.mjs';

// Read-only looks at the private server tmux.mjs started for this process.
const tmux = (...args) => run('tmux', ['-L', `oui-${process.pid}`, ...args], { env: { ...process.env, TMUX: '' } });
const pane = () => tmux('capture-pane', '-t', 'ui:0.0', '-p');

const CHECK = '✅', PLUS = '\u{1F44D}';
const text = 'Shipping the classifier fix tonight, react if you agree';

let env;
try {
  env = await bootDemo();
  await withTui({ env }, async ({ demo, keys, waitFor, shot, asPeer }) => {
    const hark = demo.threads.hark;
    // Fixtures first: the thread opens with its chips already there.
    const posted = await asPeer('operator', 'POST', '/v1/messages', { idempotency_key: 'rx07-op', thread_id: hark, priority: 'hold', content: { text } });
    assert(posted.ok, `operator message ${posted.status}`);
    const { message_id: target } = await posted.json();
    for (const [peer, emoji] of [['codex', CHECK], ['sleuth', PLUS]]) {
      const reacted = await asPeer(peer, 'POST', '/v1/reactions', { message_id: target, emoji, op: 'add', priority: 'hold' });
      assert(reacted.ok, `${peer} reaction ${reacted.status}`);
    }

    // The list may reorder between looks, so this goes to the top, walks
    // down by the list's own order, and checks where it landed.
    async function openThread(id, header) {
      for (let walk = 0; ; walk++) {
        assert(walk < 20, 'thread did not open');
        const listed = await asPeer('operator', 'GET', '/v1/admin/threads?limit=200');
        assert(listed.ok, `thread list ${listed.status}`);
        const { threads } = await listed.json();
        const at = threads.findIndex(row => row.thread.thread_id === id);
        assert(at >= 0, 'thread is not listed');
        await keys(...Array(threads.length).fill('['));
        if (at) await keys(...Array(at).fill(']'));
        for (let look = 0; look < 50; look++) {
          if ((await pane()).includes(header)) return;
          await new Promise(resolve => setTimeout(resolve, 100));
        }
      }
    }

    await waitFor(' live ');
    await keys('2');
    await waitFor('threads [ ]');
    await openThread(hark, '━ hark B196: radio end-phrase bug');
    await waitFor(text);

    // Initial: the operator message is the newest, so it is selected and
    // names its reactors; one step up shows the counts under it instead.
    await waitFor('[+1: pag] [check: codex]');
    await keys('k');
    await waitFor('[+1 1] [check 1]');
    await shot('rx07-tui-initial', '[+1 1] [check 1]');

    // Progress: back on the message, names again, with the command typed.
    await keys('j');
    await waitFor('[+1: pag] [check: codex]');
    await keys(':', 'react check');
    await waitFor(':react check');
    await shot('rx07-tui-progress', ':react check');

    // Success: the operator's check joins codex's, marked with *.
    await keys('Enter');
    await waitFor('[check: codex, you]');
    await keys('k');
    await waitFor('[check 2*]');
    await shot('rx07-tui-success', '[check 2*]');
    const history = await asPeer('operator', 'GET', `/v1/threads/${hark}/messages?limit=200`);
    assert(history.ok, `history ${history.status}`);
    const reacted = (await history.json()).messages.find(m => m.message_id === target);
    const check = reacted?.reactions?.find(g => g.emoji === CHECK);
    assert(check && check.peers.length === 2 && check.peers.includes(demo.operator_peer), 'the hub does not hold the operator check');

    // Error: an unknown name is refused before any request; the line comes
    // back with the reason, which lists the set.
    await keys(':', 'react 1 smile', 'Enter');
    await waitFor(':react 1 smile');
    await waitFor('unknown reaction smile');
    await shot('rx07-tui-error', ':react 1 smile');
    const after = await pane();
    assert(after.includes('heart') && after.includes('bang'), 'the refusal does not list the names');
    await keys('Escape');
  });
  console.log('TUI reactions passed: chips with counts and names, :react check toggled on with the * marker, unknown name refused with the line kept');
} finally {
  try { await env?.close(); } finally { await cleanBuild(); }
}
