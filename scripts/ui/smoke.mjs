import { assert, cleanBuild, withDemo } from './lib.mjs';
import { withTui } from './tmux.mjs';

try {
  await withDemo({ signedIn: false }, async ({ page, shot, demo, advance, asPeer }) => {
    await page.getByRole('heading', { name: 'Operator inbox', exact: true }).waitFor();
    assert(new URL(demo.url).port !== '45752' && new URL(demo.url).port !== '49575', 'demo used a reserved port');
    assert(!demo.peers.dot.secret, 'dot must have no bearer');
    assert(Object.keys(demo.peers).length === 10, 'expected ten peers');
    const response = await asPeer('operator', 'GET', '/v1/peers');
    assert(response.ok, 'operator credential failed');
    const first = await advance('1s'), next = await advance('15m');
    assert(Date.parse(next.now) - Date.parse(first.now) >= 900000, 'demo clock did not advance');
    await shot('02-smoke-login');
  });
  const script = "printf '\\033[1;35mvecnet terminal harness\\033[0m\\n\\033[32mSUCCESS\\033[0m  \\033[38;5;208m256 colours\\033[0m  \\033[38;2;99;102;241mtrue colour\\033[0m\\n'; exec cat";
  await withTui({ command: ['sh', '-c', script] }, async ({ waitFor, shot, keys, stall, fail, asPeer }) => {
    await waitFor('SUCCESS');
    await keys('harness input', 'Enter');
    await waitFor('harness input');
    await stall('100ms');
    let start = Date.now();
    assert((await asPeer('operator', 'GET', '/v1/peers')).ok, 'stall request failed');
    assert(Date.now() - start >= 70, 'stall did not delay the request');
    await fail('/v1/peers', 500, '100ms');
    assert((await asPeer('operator', 'GET', '/v1/peers')).status === 500, 'failure control did not apply');
    await shot('02-smoke-tmux');
  });
  console.log('UI smoke passed: login, tmux colours, credentials, clock, stall and failure controls');
} finally { await cleanBuild(); }
