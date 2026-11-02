import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
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
    for (const name of ['ansi.mjs', 'lib.mjs', 'smoke.mjs', 'tmux.mjs', 'recipes.test.mjs']) {
      await writeFile(join(dir, 'scripts/ui', name), '');
    }
    await writeFile(join(dir, 'bin/build2'), '#!/usr/bin/env bash\nprintf "%s\\n" "$2" >> "$CALL_LOG"\nexit "$CHECK_STATUS"\n', { mode: 0o700 });
    for (const status of [23, 0]) {
      const log = join(dir, `calls-${status}`);
      const result = spawnSync('bash', ['-c', script], {
        cwd: dir, encoding: 'utf8',
        env: { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, CALL_LOG: log, CHECK_STATUS: String(status) },
      });
      assert.equal(result.status, status, `screen exit ${status}: ${result.stderr}`);
      assert.equal(await readFile(log, 'utf8'), 'scripts/ui/smoke.mjs\n');
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});
