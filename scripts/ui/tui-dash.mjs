// Task 20: the read-only dashboard against a real demo hub, in tmux.
import { assert, cleanBuild } from './lib.mjs';
import { withTui } from './tmux.mjs';

// The seed reports no quota, so these peers report theirs as any peer would.
// grok-triage stays silent: an inbound call would mark the seeded unreachable
// grokbot reachable. grok-reviewer is already reachable.
const reports = [
  ['codex', 'working', { weekly_remaining_pct: 44 }],
  ['sleuth', 'working', { weekly_remaining_pct: 61 }],
  ['digest', 'idle', { weekly_remaining_pct: 58 }],
  ['grok', 'blocked', { weekly_remaining_pct: 3 }],
  ['claw', 'idle', { plan: 'pro' }],
];

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

try {
  await withTui({}, async ({ demo, keys, waitFor, shot, stall, clear, controlStatus, fail, asPeer }) => {
    const threads = async query => {
      const response = await asPeer('operator', 'GET', `/v1/admin/threads?limit=200${query}`);
      assert(response.ok, `threads: ${response.status}`);
      return (await response.json()).threads.length;
    };
    const stalled = async what => {
      await until(what, async () => (await controlStatus()).stalled_requests > 0);
    };

    await waitFor(' live ');
    await keys('3');
    await waitFor('presence is self-reported');
    for (const [name, state, quota] of reports) {
      const response = await asPeer(name, 'POST', '/v1/presence', { state, at: new Date().toISOString(), quota });
      assert(response.ok, `presence for ${name}: ${response.status}`);
    }
    await waitFor('[||||||    ]  58%');
    await waitFor('[          ]   3%');

    // The shell holds the body until the first load ends, so panes load on a reload.
    await stall('1h');
    try {
      await keys('C-l');
      await stalled('the reload to reach the stall');
      await waitFor('delivery health · loading');
      await waitFor('audit tail -f · loading');
      await shot('20-tui-dash-initial');
    } finally { await clear('stall'); }
    await until('the reload to finish', async () => (await controlStatus()).stalled_requests === 0);

    const all = await threads('');
    const mine = await threads(`&participant=${demo.peers.codex.id}`);
    assert(mine < all, `codex-hark should be in fewer threads than the hub holds: ${mine} of ${all}`);
    await waitFor(`threads · ${all} `);

    await keys('j');
    await waitFor('> codex-hark');
    await stall('1h');
    try {
      await keys('Enter');
      await stalled('the narrowed request to reach the stall');
      await waitFor('threads · codex-hark · loading');
      await waitFor('loading threads for codex-hark');
      await shot('20-tui-dash-progress');
    } finally { await clear('stall'); }
    await waitFor(`threads · codex-hark · ${mine} `);

    // Escape needs its own write: tmux would turn Escape then k into alt+k.
    await keys('Escape');
    await waitFor(`threads · ${all} `);
    await keys('k');
    await waitFor('> max');
    await waitFor('unreachable');
    await waitFor('thread blocked on delivery');
    await waitFor('loop cuts / 24h');
    await waitFor('counters');
    await shot('20-tui-dash-success');

    await fail('/v1/admin/delivery-health', 500, '1h');
    try {
      await keys('C-l');
      await waitFor('delivery health load failed');
      await waitFor('last good data');
      await waitFor('peers · ');
      await waitFor(`threads · ${all} `);
      await waitFor('audit tail -f ');
      await shot('20-tui-dash-error');
    } finally { await clear('fail'); }
  });
  console.log('TUI dashboard passed: loading panes, narrowing, loaded, one pane failed');
} finally { await cleanBuild(); }
