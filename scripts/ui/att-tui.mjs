// Attachments Task 5: :attach and :detach in the TUI chat, against a real demo
// hub in tmux. Fixtures are written under the demo's temp dir at run time.
import { open, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { assert, bootDemo, cleanBuild, run } from './lib.mjs';
import { withTui } from './tmux.mjs';

// Read-only looks at the private server tmux.mjs started for this process.
const tmux = (...args) => run('tmux', ['-L', `oui-${process.pid}`, ...args], { env: { ...process.env, TMUX: '' } });
const pane = () => tmux('capture-pane', '-t', 'ui:0.0', '-p');

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 50));
  }
}

const text = 'Notes from the operator, file attached';
const kept = 'this draft stays put';

let env;
try {
  env = await bootDemo();
  const notes = join(env.dir, 'notes.txt');
  await writeFile(notes, 'Operator notes for the hark thread.\n'.repeat(40));
  // ~20 MiB, sparse: the bytes are zeros, and only the upload reads them.
  const big = join(env.dir, 'big.bin');
  const handle = await open(big, 'w');
  await handle.truncate(20 << 20);
  await handle.close();
  const missing = join(env.dir, 'missing.txt');

  await withTui({ env }, async ({ demo, keys, waitFor, shot, asPeer, stall, clear, controlStatus }) => {
    // The list and the thread may reorder between looks, so this goes to the
    // top, walks down by the list's own order, and checks where it landed.
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
    // A key right behind Escape would read as alt+key, so wait for Normal.
    async function leaveInsert() {
      await keys('Escape');
      await waitFor('NORMAL');
    }

    await waitFor(' live ');
    await keys('2');
    await waitFor('threads [ ]');
    await openThread(demo.threads.hark, '─ hark B196: radio end-phrase bug');
    await waitFor('Push classifier fix to hark master and release?');

    // Initial: one chip uploaded, a draft beside it.
    await keys(':', `attach ${notes}`, 'Enter');
    await waitFor('[+] notes.txt 1.4 KiB');
    await keys('i');
    await waitFor('INSERT');
    await keys(text);
    await waitFor(`${text}█`);
    await shot('att05-tui-initial', '[+] notes.txt');
    await leaveInsert();

    // Progress: every /v1/ request waits, so the upload has read only what
    // the socket buffers hold. The marker is asserted, not a percentage.
    await stall('60s');
    try {
      await keys(':', `attach ${big}`, 'Enter');
      await until('the upload to reach the stall', async () => (await controlStatus()).stalled_requests > 0);
      await waitFor(/\[\.\.\] big\.bin \d{1,2}%/);
      await shot('att05-tui-progress', '[..] big.bin');
    } finally { await clear('stall'); }
    await waitFor('[+] big.bin 20.0 MiB');
    await keys(':', 'detach 2', 'Enter');
    await waitFor('detached big.bin');
    assert(!(await pane()).includes('big.bin 20.0 MiB'), 'the detached chip is still drawn');

    // Success: the send lands with its file row in the thread.
    await keys('i');
    await waitFor('INSERT');
    await keys('Enter');
    await waitFor(`<max> ${text}`);
    await waitFor('file notes.txt');
    await until('the chip row to go', async () => !(await pane()).includes('[+] notes.txt'));
    await shot('att05-tui-success', 'file notes.txt');
    await leaveInsert();

    // Error: a missing file is refused before upload, the draft intact. A
    // toast lasts seconds, so it counts only if it is still up after the shot.
    await keys('i');
    await waitFor('INSERT');
    await keys(kept);
    await waitFor(`${kept}█`);
    await leaveInsert();
    for (let shown = false, tries = 0; !shown; tries++) {
      assert(tries < 10, 'the missing-file toast never lasted through a shot');
      await keys(':', `attach ${missing}`, 'Enter');
      await waitFor('no such file');
      await shot('att05-tui-error', kept);
      shown = (await pane()).includes('no such file');
    }
    const after = await pane();
    assert(after.includes(kept), 'the draft was not kept');
    assert(!after.includes('[+]') && !after.includes('[..]') && !after.includes('[x]'), 'a refused attach drew a chip');
  });
  console.log('TUI attachments passed: chip uploaded, progress under a stall, detach, send with a file row, missing file refused with the draft kept');
} finally {
  try { await env?.close(); } finally { await cleanBuild(); }
}
