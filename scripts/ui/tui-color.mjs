// TUI colour theme review shots (docs/design/tui-color.md §10.3, C5): the
// screens 17–22 do not cover in the default theme, and inbox and chat on every
// other rung. One demo hub serves every run; each run is a fresh `vecnet tui`
// in its own tmux with its own colour environment (scripts/ui/tmux.mjs).
//
// Shots, in docs/plans/shots/oui:
//   tc01-tui-inbox[-variant]  inbox list, detail and event log, approval and DLQ rows
//   tc02-tui-chat[-variant]   chat with both sidebar groups and a selected message
//   tc03-tui-bars-*           INSERT, COMMAND, an ok toast and an error toast
//   tc04-tui-help             help (a full-screen pane, so focused)
//   tc05-tui-prefs[-error]    preferences, and its failed load
//   tc06-tui-header-notice    a notice row under the header of a local hub
//   tc07-tui-picker[-confirm] the operator picker and its confirm step
// Variants: -nocolor (NO_COLOR=1), -ansi16 (TERM=xterm), -256 (TERM=xterm-256color,
// no COLORTERM) and -light (truecolor, VECNET_TUI_BG=light on a white page).
import { assert, bootDemo, cleanBuild, run } from './lib.mjs';
import { withTui } from './tmux.mjs';

const tmux = (...args) => run('tmux', ['-L', `oui-${process.pid}`, ...args], { env: { ...process.env, TMUX: '' } });
const pane = () => tmux('capture-pane', '-t', 'ui:0.0', '-p');

// openThread walks the chat list to the thread, as tui-chat.mjs does: the list
// may reorder between looks, so each pass starts from the top.
async function openThread({ keys, asPeer }, id, header) {
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

// inboxAndChat is the pair every variant shows: the inbox as it opens, then the
// hark thread with an earlier message selected, so its fill shows.
async function inboxAndChat(tui, suffix) {
  const { keys, waitFor, shot, demo } = tui;
  await waitFor(' live ');
  await waitFor('apr 2');
  await waitFor('dlq 3');
  await waitFor('press a to answer');
  await waitFor('codex-hark');
  await shot(`tc01-tui-inbox${suffix}`, ['DLQ', 'APR']);
  await keys('2');
  await waitFor('threads [ ]');
  await openThread(tui, demo.threads.hark, '━ hark B196: radio end-phrase bug');
  await waitFor('Your Threads');
  await waitFor('Other Threads');
  await waitFor('Push classifier fix to hark master and release?');
  await keys('k', 'k');
  await shot(`tc02-tui-chat${suffix}`, ['Your Threads', 'Other Threads', 'hark-listen.log']);
}

let env;
try {
  env = await bootDemo();
  for (const [suffix, opts] of [['-nocolor', { colour: 'none' }], ['-ansi16', { colour: '16' }], ['-256', { colour: '256' }], ['-light', { bg: 'light' }]]) {
    await withTui({ env, ...opts }, tui => inboxAndChat(tui, suffix));
  }

  await withTui({ env }, async tui => {
    const { keys, waitFor, shot, fail, clear, asPeer } = tui;
    await inboxAndChat(tui, '');

    // The bars (design §8.6). The draft is typed in INSERT, then left there.
    await keys('i');
    await waitFor('INSERT');
    await keys('Patched on x-tuf too');
    await shot('tc03-tui-bars-insert', ['INSERT', 'Patched on x-tuf too']);
    await keys('Escape');
    await waitFor('NORMAL');

    await keys(':retry all');
    await shot('tc03-tui-bars-command', ['COMMAND', ':retry all']);
    await keys('C-c');
    await waitFor('NORMAL');

    // A whole toast line cannot match the command input's leading colon.
    await keys(':presence working', 'Enter');
    await shot('tc03-tui-bars-toast-ok', /(?:^|\n)\s*presence working\s*(?:\n|$)/);
    await keys(':retyr', 'Enter');
    await shot('tc03-tui-bars-toast-error', 'unknown command: retyr');

    await keys('?');
    await waitFor('global keys');
    await shot('tc04-tui-help', ['global keys', ':retry all']);
    await keys('x');
    await waitFor('NORMAL');

    await keys(':prefs', 'Enter');
    await waitFor('━ preferences');
    await waitFor('retention');
    await shot('tc05-tui-prefs', ['━ preferences', 'retention']);
    await fail('/v1/admin/prefs', 503, '1h');
    try {
      await keys('r');
      await shot('tc05-tui-prefs-error', 'could not load preferences');
    } finally { await clear('fail'); }
    await keys('1');
    await waitFor('your event log');

    // Last: a notice stands until it expires, and later runs share this hub.
    const now = Date.now();
    const posted = await asPeer('operator', 'POST', '/v1/admin/notices', { kind: 'moving', message: 'Moving to Fly tonight', new_url: 'https://vecnet.xk.io', at: new Date(now + 18 * 3600e3).toISOString() });
    assert(posted.status === 201, `post notice: ${posted.status}`);
    await waitFor('! MOVING');
    await shot('tc06-tui-header-notice', ['! MOVING', ' local ']);
  });

  // The picker runs before the shell: a second operator profile makes it list
  // two. Its secret is never used; the run goes on as demo.
  const second = '\n[profiles.fly-operator]\nhub = "https://vecnet.example"\nsecret = "vn_operator_SHOTONLY"\n';
  await withTui({ env, args: ['--pick'], config: second }, async ({ keys, waitFor, shot }) => {
    await waitFor('choose an operator profile');
    await waitFor('fly-operator');
    await shot('tc07-tui-picker', ['> demo', 'local', 'fly-operator']);
    await keys('Enter');
    await waitFor('make this the default operator_profile?');
    await shot('tc07-tui-picker-confirm', ['run demo for this session', 'local']);
    await keys('n');
    await waitFor(' live ');
  });
  console.log('TUI colour shots passed: inbox and chat on every rung, bars, help, prefs, notice header, picker');
} finally {
  try { await env?.close(); } finally { await cleanBuild(); }
}
