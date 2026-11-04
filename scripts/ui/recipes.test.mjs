import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFile, mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';

// Run the actual shebang recipe with a stub build2: no demo, browser or builds.
// tmux.mjs sorts after the failing check and takes the helper-skip branch.
test('ui-shots propagates screen failure even when the last glob entry is a helper', async () => {
  const justfile = await readFile(new URL('../../justfile', import.meta.url), 'utf8');
  const body = justfile.match(/^ui-shots:\n((?:[ \t].*\n|\n)+)/m)?.[1];
  assert.ok(body, 'ui-shots recipe exists');
  const script = body.replace(/^ {4}/gm, '');
  const dir = await mkdtemp(join(tmpdir(), 'vecnet-ui-recipe-'));
  try {
    await mkdir(join(dir, 'scripts/ui'), { recursive: true });
    await mkdir(join(dir, 'bin'));
    for (const name of ['ansi.mjs', 'lib.mjs', 'smoke.mjs', 'tmux.mjs', 'recipes.test.mjs', 'shots-index.mjs']) {
      await writeFile(join(dir, 'scripts/ui', name), '');
    }
    // The preliminary lib.test.mjs check always passes; every screen script
    // and the final index exit CHECK_STATUS. Each call logs its node args.
    await writeFile(join(dir, 'bin/build2'), `#!/usr/bin/env bash
shift
printf "%s\\n" "$*" >> "$CALL_LOG"
if [[ "$1" == --test ]]; then exit 0; fi
exit "$CHECK_STATUS"
`, { mode: 0o700 });
    const check = '--test --test-concurrency=2 scripts/ui/lib.test.mjs\n';
    for (const status of [23, 0]) {
      const log = join(dir, `calls-${status}`);
      const result = spawnSync('bash', ['-c', script], {
        cwd: dir, encoding: 'utf8',
        env: { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, CALL_LOG: log, CHECK_STATUS: String(status) },
      });
      assert.equal(result.status, status, `screen exit ${status}: ${result.stderr}`);
      // A failing screen stops the loop and skips the final index; success runs it.
      const want = check + 'scripts/ui/smoke.mjs\n' + (status === 0 ? 'scripts/ui/shots-index.mjs\n' : '');
      assert.equal(await readFile(log, 'utf8'), want, `calls for screen exit ${status}`);
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

// A late screen writes after att-journey; the final gate must inspect SHOTS_DIR.
test('ui-shots checks the completed configured output for extra and missing PNGs', async () => {
  const justfile = await readFile(new URL('../../justfile', import.meta.url), 'utf8');
  const body = justfile.match(/^ui-shots:\n((?:[ \t].*\n|\n)+)/m)?.[1];
  assert.ok(body, 'ui-shots recipe exists');
  const script = body.replace(/^ {4}/gm, '');
  const dir = await mkdtemp(join(tmpdir(), 'vecnet-ui-index-'));
  try {
    await mkdir(join(dir, 'scripts/ui'), { recursive: true });
    await mkdir(join(dir, 'docs/plans/shots/oui'), { recursive: true });
    await mkdir(join(dir, 'output'));
    await mkdir(join(dir, 'bin'));
    for (const name of ['att-journey.mjs', 'shell.mjs']) await writeFile(join(dir, 'scripts/ui', name), '');
    // The canonical shots are complete; only the configured output is wrong.
    await writeFile(join(dir, 'docs/plans/shots/oui/README.md'), '[expected.png](expected.png)\n');
    await writeFile(join(dir, 'docs/plans/shots/oui/expected.png'), '');
    await copyFile(new URL('shots-index.mjs', import.meta.url), join(dir, 'scripts/ui/shots-index.mjs'));
    await writeFile(join(dir, 'bin/build2'), `#!/usr/bin/env bash
if [[ "$2" == scripts/ui/shots-index.mjs ]]; then exec "$NODE" "$2"; fi
if [[ "$2" == scripts/ui/shell.mjs ]]; then
  if [[ "$SCENARIO" != missing ]]; then touch "$SHOTS_DIR/expected.png"; fi
  if [[ "$SCENARIO" == extra ]]; then touch "$SHOTS_DIR/late.png"; fi
fi
`, { mode: 0o700 });
    for (const scenario of ['extra', 'missing', 'duplicate', 'complete']) {
      await writeFile(join(dir, 'docs/plans/shots/oui/README.md'), '[expected.png](expected.png)\n'.repeat(scenario === 'duplicate' ? 2 : 1));
      await rm(join(dir, 'output'), { recursive: true, force: true });
      await mkdir(join(dir, 'output'));
      const result = spawnSync('bash', ['-c', script], {
        cwd: dir, encoding: 'utf8',
        env: { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, NODE: process.execPath, SHOTS_DIR: join(dir, 'output'), SCENARIO: scenario },
      });
      if (scenario === 'complete') assert.equal(result.status, 0, result.stderr);
      else {
        assert.notEqual(result.status, 0, `${scenario} PNG in configured output must fail after all screen scripts`);
        assert.match(result.stderr, scenario === 'duplicate' ? /more than once/ : scenario === 'extra' ? /late\.png/ : /expected\.png/);
      }
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});
