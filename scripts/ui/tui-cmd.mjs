// Task 22: the command line and help overlay against a real demo hub, in tmux.
import { assert, cleanBuild } from './lib.mjs';
import { withTui } from './tmux.mjs';

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

try {
  await withTui({}, async ({ keys, waitFor, shot, stall, clear, fail, controlStatus }) => {
    await waitFor(' live ');
    await waitFor('dlq 3');

    await keys('?');
    await waitFor('global keys');
    await waitFor(':presence <idle|working|blocked|offline>');
    await waitFor(':retry all');
    await shot('22-tui-cmd-initial');

    // Any key closes help, and tmux keeps keys in order, so no wait is needed.
    // Not Escape: a key right behind it would be read as alt+key. COMMAND shows
    // help has gone, because the overlay also holds the text waited on next.
    await keys('x');

    // Real terminal keys: tab, up and down arrive by name, which the fake cannot prove.
    await keys(':', 'ret', 'Tab');
    await waitFor('COMMAND');
    await waitFor(':retry ');
    await keys('C-c');

    await keys(':presence blocked', 'Enter');
    // A whole toast line cannot match the command input's leading colon.
    await waitFor(/(?:^|\n)\s*presence blocked\s*(?:\n|$)/);
    await waitFor('max@vecnet · blocked');

    await keys(':no-such-command', 'Enter');
    await waitFor('unknown command: no-such-command');
    await waitFor('dlq 3');
    await shot('22-tui-cmd-error');

    // Every attempt fails, and every listed dead letter is still attempted.
    await fail('/v1/admin/dead-letters', 500, '1h');
    await keys(':retry all', 'Enter');
    await waitFor('0 ok, 3 failed');
    await clear('fail');

    await keys(':', 'Up');
    await waitFor(':retry all');
    await keys('Up');
    await waitFor(':no-such-command');
    await keys('Down');
    await waitFor(':retry all');

    await stall('1h');
    try {
      await keys('Enter');
      await until('the retry to reach the stall', async () => (await controlStatus()).stalled_requests > 0);
      await waitFor('retrying dead letters: 3');
      await shot('22-tui-cmd-progress');
    } finally { await clear('stall'); }
    // The completion toast expires on the TUI clock. Capture the frame that
    // satisfies the wait, before browser rendering can outlast the toast.
    await shot('22-tui-cmd-success', '3 ok, 0 failed');
  });
  console.log('TUI command line passed: help, completion, history, presence, retry all, unknown command');
} finally { await cleanBuild(); }
