// Hub notices Task 8: the TUI's notice banner and its restarting connection
// state, against a real demo hub in tmux. Every wait is on pane text or hub
// state, never on elapsed time.
import { assert, cleanBuild } from './lib.mjs';
import { withTui } from './tmux.mjs';

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

// The pane's top border straight under the top bar: no banner row between.
const noBanner = /1:inbox[^\n]*\n┌─ inbox/;
const moving = { kind: 'moving', message: 'Moving to Fly tonight', new_url: 'https://vecnet.xk.io' };
const maintenance = { kind: 'maintenance', message: 'Database vacuum, expect slow reads' };

try {
  await withTui({}, async ({ waitFor, shot, restart, fail, clear, asPeer }) => {
    const post = async body => {
      const response = await asPeer('operator', 'POST', '/v1/admin/notices', body);
      assert(response.status === 201, `post ${body.kind} notice: ${response.status}`);
      return (await response.json()).notice;
    };
    const hubNow = Date.now();
    const at = hours => new Date(hubNow + hours * 3600e3).toISOString();

    await waitFor(' live ');
    await waitFor('max@vecnet');
    await waitFor(noBanner);
    await shot('nt08-tui-initial', '┌─ inbox');

    // A notice whose fetch fails draws no banner, and the command line says so.
    await fail('/v1/hub/notices', 500, '1h');
    try {
      await post({ ...maintenance, at: at(2) });
      await waitFor('reload notices');
      await waitFor(noBanner);
      await shot('nt08-tui-error', 'reload notices');
    } finally { await clear('fail'); }

    // The tail replays the event once the hub answers again.
    await waitFor('! MAINTENANCE');
    await waitFor(' live ');
    await waitFor(': command  ? help'); // the failure's toast has run its course
    await post({ ...moving, at: at(3) });
    await waitFor(/! MOVING \d\d:\d\d -> vecnet\.xk\.io {2}Moving to Fly tonight {2}\(\+1 more\)/);
    await shot('nt08-tui-success', '(+1 more)');

    // A restart: the parked long-poll answers with hub_restarting, and the hub
    // then refuses requests for a while, as a real restart would. The top bar
    // says restarting throughout and never offline. A restart announced while
    // the tail sits between polls reaches no one, so announce until a parked
    // poll takes it.
    await until('a parked poll to take the restart', async () => (await restart(4000)).streams > 0);
    await fail('/v1/', 503, '8s');
    try {
      await waitFor(' restarting ');
      await shot('nt08-tui-progress', ' restarting ');
      // The outage after the hint still reads as the restart.
      await until('the restarted hub to answer', async () => {
        const response = await asPeer('operator', 'GET', '/v1/hub/notices');
        return response.ok;
      });
    } finally { await clear('fail'); }
    await waitFor(' live ');
    await waitFor('(+1 more)'); // the banner survives the restart
  });
  console.log('TUI notice passed: no banner, failed fetch, moving + maintenance banner, restarting state');
} finally { await cleanBuild(); }
