// Bare `vecnet` from the operator's terminal (PROTOCOL §13.7, §2.2 step 11),
// end to end with the real binary: under a pty and a clean environment it
// sends as the operator labelled "Operator on <host> CLI" (plus "via ssh");
// the same command without a terminal, or with an agent marker, is refused
// before any request. The web thread view then shows the label beside the
// sender, at desktop and narrow widths.
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { hostname, tmpdir } from 'node:os';
import { join } from 'node:path';
import { assert, buildCLI, cleanBuild, scrollMessageIntoView, withDemo } from './lib.mjs';

const quote = s => `'${String(s).replace(/'/g, `'\\''`)}'`;

// cli runs the binary under env -i (no agent marker can leak in from the
// shell that runs this script), on a pty via util-linux script(1) when tty.
async function cli(bin, conf, home, args, { tty, extra = {} } = {}) {
  const env = { PATH: '/usr/bin:/bin', HOME: home, VECNET_CONFIG: conf, XDG_CACHE_HOME: join(home, 'cache'), TERM: 'xterm-256color', ...extra };
  const line = ['env', '-i', ...Object.entries(env).map(([k, v]) => `${k}=${v}`), bin, ...args].map(quote).join(' ');
  const child = tty
    ? spawn('script', ['-qec', line, '/dev/null'], { stdio: ['ignore', 'pipe', 'pipe'] })
    : spawn('sh', ['-c', line], { stdio: ['ignore', 'pipe', 'pipe'] });
  let out = '';
  child.stdout.on('data', d => { out += d; });
  child.stderr.on('data', d => { out += d; });
  const [code] = await once(child, 'close');
  return { code, out: out.replace(/\r/g, '') };
}

const dir = await mkdtemp(join(tmpdir(), 'vecnet-cli-label-'));
try {
  const bin = await buildCLI(dir);
  await withDemo({}, async ({ page, shot, demo, asPeer }) => {
    const conf = join(dir, 'config.toml');
    // Max's shape: an agent profile under the legacy default, and the
    // operator profile named by operator_profile.
    await writeFile(conf, `default = "agent"\noperator_profile = "op"\n[profiles.agent]\nhub = ${JSON.stringify(demo.url)}\nsecret = ${JSON.stringify(demo.peers.codex.secret)}\n[profiles.op]\nhub = ${JSON.stringify(demo.url)}\nsecret = ${JSON.stringify(demo.operator_secret)}\n`, { mode: 0o600 });
    const to = demo.peers.codex.id;
    const host = hostname().replace(/[^A-Za-z0-9._-]/g, '').slice(0, 64) || 'unknown';

    // Refusals: no terminal, and a terminal with an agent marker.
    for (const [why, opts] of [['no terminal', { tty: false }], ['CLAUDECODE', { tty: true, extra: { CLAUDECODE: '1' } }], ['GROK_AGENT', { tty: true, extra: { GROK_AGENT: '1' } }]]) {
      const r = await cli(bin, conf, dir, ['send', to, `refused (${why})`, '--hold'], opts);
      assert(r.code === 1, `${why}: exit ${r.code}: ${r.out}`);
      assert(r.out.includes('refusing a bare run') && r.out.includes('--net <profile>'), `${why}: ${r.out}`);
    }

    // A human at a terminal: sends as the operator, labelled.
    const sent = await cli(bin, conf, dir, ['--json', 'send', to, 'Sent from a plain terminal: bare vecnet speaks as the operator.', '--wake'], { tty: true });
    assert(sent.code === 0, `human send: exit ${sent.code}: ${sent.out}`);
    const first = JSON.parse(sent.out.slice(sent.out.indexOf('{')));
    const viaSSH = await cli(bin, conf, dir, ['--json', 'send', to, 'And this one came in over ssh.', '--hold', '--thread', first.thread_id], { tty: true, extra: { SSH_CONNECTION: '10.0.0.5 51234 10.0.0.9 22' } });
    assert(viaSSH.code === 0, `ssh send: exit ${viaSSH.code}: ${viaSSH.out}`);
    const second = JSON.parse(viaSSH.out.slice(viaSSH.out.indexOf('{')));
    // An agent naming the operator profile explicitly is allowed, marked.
    const agentOp = await cli(bin, conf, dir, ['--json', '--net', 'op', 'send', to, 'An agent passed --net op explicitly.', '--hold', '--thread', first.thread_id], { tty: false, extra: { CLAUDECODE: '1' } });
    assert(agentOp.code === 0, `agent --net op: exit ${agentOp.code}: ${agentOp.out}`);
    const third = JSON.parse(agentOp.out.slice(agentOp.out.indexOf('{')));
    // The agent profile, by --net, carries no label; the codex peer replies.
    const reply = await asPeer('codex', 'POST', '/v1/messages', { idempotency_key: 'cli-label-reply', thread_id: first.thread_id, in_reply_to: first.message_id, priority: 'wake', content: { text: 'Got it. My reply goes to your operator inbox.' } });
    assert(reply.ok, `reply ${reply.status}`);

    const r = await asPeer('operator', 'GET', `/v1/threads/${first.thread_id}/messages?limit=10`);
    assert(r.ok, `history ${r.status}`);
    const byId = new Map((await r.json()).messages.map(m => [m.message_id, m]));
    const want = new Map([
      [first.message_id, `Operator on ${host} CLI`],
      [second.message_id, `Operator on ${host} CLI via ssh`],
      [third.message_id, `Operator on ${host} CLI (agent suspected)`],
    ]);
    for (const [id, label] of want) {
      const m = byId.get(id);
      assert(m && m.sender === demo.operator_peer, `${id} sender ${m && m.sender}`);
      const c = m.metadata['vecnet.client'];
      assert(c && c.label === label, `${id} label ${JSON.stringify(c)}, want ${label}`);
      assert(!('ssh_from' in c), 'the client address was recorded');
    }

    await page.locator('#conn.live').waitFor();
    await page.evaluate(id => { location.hash = `#thread/${id}`; }, first.thread_id);
    for (const [id, label] of want) {
      const chip = page.locator(`[id="m-${id}"] header .cli`);
      await chip.waitFor();
      assert(await chip.textContent() === label, `web chip for ${id}`);
      assert(await chip.evaluate(el => el.classList.contains('sus')) === label.endsWith('(agent suspected)'), `suspect marking for ${id}`);
    }
    await scrollMessageIntoView(page, first.message_id);
    await shot('cli-label-thread-desktop');
    await page.setViewportSize({ width: 600, height: 900 });
    await scrollMessageIntoView(page, first.message_id);
    await shot('cli-label-thread-narrow');
  });
} finally {
  await cleanBuild();
  await rm(dir, { recursive: true, force: true });
}
