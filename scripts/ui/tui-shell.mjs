// Task 17: the TUI shell against a real demo hub, in tmux.
import { readFile } from 'node:fs/promises';
import { assert, cleanBuild, run } from './lib.mjs';
import { withTui } from './tmux.mjs';

// tmux.mjs starts its command before a script can touch the demo, and the first
// load would win the race. This shell waits for Enter, so the hub can be stalled
// first. The CLI sits beside VECNET_CONFIG in the demo's temp dir.
const launch = ['sh', '-c', 'echo press enter to start; read -r _; exec "$(dirname "$VECNET_CONFIG")/vecnet" tui'];

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

// tmux.mjs keeps the demo's handle to itself, and the demo is this process's
// only child named demo. The hub dies at once, as a crash would, with no drain.
async function killDemo() {
  const children = (await run('pgrep', ['-P', String(process.pid)])).split('\n').filter(Boolean);
  for (const pid of children) {
    const [path] = (await readFile(`/proc/${pid}/cmdline`, 'utf8').catch(() => '')).split('\0');
    if (path.endsWith('/demo')) { process.kill(Number(pid), 'SIGKILL'); return; }
  }
  throw new Error('demo process not found');
}

try {
  await withTui({ command: launch }, async ({ keys, waitFor, shot, stall, clear, controlStatus }) => {
    await waitFor('press enter to start');
    await stall('1h');
    try {
      await keys('Enter');
      await until('the first request to reach the stall', async () => (await controlStatus()).stalled_requests > 0);
      await waitFor('connecting to');
      await shot('17-tui-initial');
      await waitFor('first load: waiting on inbox');
      await shot('17-tui-progress');
    } finally { await clear('stall'); }

    await waitFor(' live ');
    await waitFor('vecnet | 1:inbox 2:chat 3:dash 4:subs');
    await waitFor('max@vecnet');
    await waitFor('NORMAL');
    await shot('17-tui-success');

    await killDemo();
    await waitFor('offline, retrying');
    await waitFor('max@vecnet'); // the last data stays on screen
    await shot('17-tui-error');
  });
  console.log('TUI shell passed: connecting, first load, live, offline with data kept');
} finally { await cleanBuild(); }
