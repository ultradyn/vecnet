import test from 'node:test';
import assert from 'node:assert/strict';
import { copyFile, mkdtemp, mkdir, readFile, writeFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const scratch = fileURLToPath(new URL('../../.superpowers/sdd/2026-11-16-public-evidence/scratch/', import.meta.url));
await mkdir(scratch, { recursive: true });

// Run the actual shebang recipe with a stub build2: no demo, browser or builds.
// tmux.mjs sorts after the failing check and takes the helper-skip branch.
test('ui-shots propagates screen failure even when the last glob entry is a helper', async () => {
  const justfile = await readFile(new URL('../../justfile', import.meta.url), 'utf8');
  const body = justfile.match(/^ui-shots:\n((?:[ \t].*\n|\n)+)/m)?.[1];
  assert.ok(body, 'ui-shots recipe exists');
  const script = body.replace(/^ {4}/gm, '');
  const dir = await mkdtemp(join(scratch, 'vecnet-ui-recipe-'));
  try {
    await mkdir(join(dir, 'scripts/ui'), { recursive: true });
    await mkdir(join(dir, 'bin'));
    await writeFile(join(dir, 'scripts/ui/reap-demos.sh'), 'echo reap >> "$CALL_LOG"\n');
    for (const name of ['ansi.mjs', 'lib.mjs', 'smoke.mjs', 'tmux.mjs', 'recipes.test.mjs', 'shots-index.mjs', 'shot-paths.mjs']) {
      await writeFile(join(dir, 'scripts/ui', name), '');
    }
    for (const name of ['shot-paths.mjs', 'capture-generation.mjs']) await copyFile(new URL(name, import.meta.url), join(dir, 'scripts/ui', name));
    // The recipe's leading `node --test …` unit step is logged as "unit" and passes; screens get CHECK_STATUS.
    await writeFile(join(dir, 'bin/build2'), '#!/usr/bin/env bash\nif [[ "$2" == --test ]]; then echo unit >> "$CALL_LOG"; exit 0; fi\nprintf "%s\\n" "$2" >> "$CALL_LOG"\nexit "$CHECK_STATUS"\n', { mode: 0o700 });
    for (const status of [23, 0]) {
      const log = join(dir, `calls-${status}`);
      const result = spawnSync('bash', ['-c', script], {
        cwd: dir, encoding: 'utf8',
        env: { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, CALL_LOG: log, CHECK_STATUS: String(status) },
      });
      assert.equal(result.status, status, `screen exit ${status}: ${result.stderr}`);
      assert.equal(await readFile(log, 'utf8'), 'reap\nunit\nscripts/ui/smoke.mjs\n' + (status === 0 ? 'scripts/ui/shots-index.mjs\n' : ''));
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

// A late screen writes after att-journey; the final gate must inspect SHOTS_DIR.
test('ui-shots checks the completed configured output for extra and missing PNGs', async () => {
  const justfile = await readFile(new URL('../../justfile', import.meta.url), 'utf8');
  const body = justfile.match(/^ui-shots:\n((?:[ \t].*\n|\n)+)/m)?.[1];
  assert.ok(body, 'ui-shots recipe exists');
  const script = body.replace(/^ {4}/gm, '');
  const dir = await mkdtemp(join(scratch, 'vecnet-ui-index-'));
  try {
    await mkdir(join(dir, 'scripts/ui'), { recursive: true });
    await mkdir(join(dir, 'docs/plans/shots/oui'), { recursive: true });
    await mkdir(join(dir, 'bin'));
    await writeFile(join(dir, 'scripts/ui/reap-demos.sh'), 'echo reap >> "$CALL_LOG"\n');
    for (const name of ['att-journey.mjs', 'shell.mjs', 'smoke.mjs']) await writeFile(join(dir, 'scripts/ui', name), '');
    // The canonical shots are complete; only the configured output is wrong.
    await writeFile(join(dir, 'docs/plans/shots/oui/README.md'), '[expected.png](expected.png)\n');
    await writeFile(join(dir, 'docs/plans/shots/oui/expected.png'), '');
    await copyFile(new URL('shots-index.mjs', import.meta.url), join(dir, 'scripts/ui/shots-index.mjs'));
    await copyFile(new URL('shot-paths.mjs', import.meta.url), join(dir, 'scripts/ui/shot-paths.mjs'));
    await copyFile(new URL('capture-generation.mjs', import.meta.url), join(dir, 'scripts/ui/capture-generation.mjs'));
    await writeFile(join(dir, 'bin/build2'), `#!/usr/bin/env bash
printf '%s\\n' "$2" >> "$CALL_LOG"
if [[ "$2" == scripts/ui/shots-index.mjs ]]; then exec "$NODE" "$2"; fi
if [[ "$2" == scripts/ui/shell.mjs ]]; then
  if [[ "$SCENARIO" != missing ]]; then touch "$SHOTS_DIR/expected.png"; fi
  if [[ "$SCENARIO" == extra ]]; then touch "$SHOTS_DIR/late.png"; fi
fi
`, { mode: 0o700 });
    for (const scenario of ['extra', 'missing', 'duplicate', 'complete']) {
      await writeFile(join(dir, 'docs/plans/shots/oui/README.md'), '[expected.png](expected.png)\n'.repeat(scenario === 'duplicate' ? 2 : 1));
      await rm(join(dir, 'output'), { recursive: true, force: true });
      await writeFile(join(dir, 'calls'), '');
      const result = spawnSync('bash', ['-c', script], {
        cwd: dir, encoding: 'utf8',
        env: { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, CALL_LOG: join(dir, 'calls'), NODE: process.execPath, SHOTS_DIR: join(dir, 'output'), SCENARIO: scenario },
      });
      assert.ok((await readFile(join(dir, 'calls'), 'utf8')).startsWith('reap\n'), 'reaper runs before screens');
      assert.ok((await readFile(join(dir, 'calls'), 'utf8')).includes('scripts/ui/smoke.mjs\n'), 'smoke runs after the reaper');
      if (scenario === 'complete') assert.equal(result.status, 0, result.stderr);
      else {
        assert.notEqual(result.status, 0, `${scenario} PNG in configured output must fail after all screen scripts`);
        assert.match(result.stderr, scenario === 'duplicate' ? /more than once/ : scenario === 'extra' ? /late\.png/ : /expected\.png/);
      }
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('ui-shots validates ignored default captures after all screens and skips the path helper', async () => {
  const justfile = await readFile(new URL('../../justfile', import.meta.url), 'utf8');
  const body = justfile.match(/^ui-shots:\n((?:[ \t].*\n|\n)+)/m)?.[1];
  assert.ok(body, 'ui-shots recipe exists');
  const dir = await mkdtemp(join(scratch, 'vecnet-ui-default-'));
  try {
    await mkdir(join(dir, 'scripts/ui'), { recursive: true });
    await mkdir(join(dir, 'docs/plans/shots/oui'), { recursive: true });
    await mkdir(join(dir, 'bin'));
    await writeFile(join(dir, 'scripts/ui/reap-demos.sh'), 'echo reap >> "$CALL_LOG"\n');
    for (const name of ['shell.mjs', 'smoke.mjs']) await writeFile(join(dir, 'scripts/ui', name), '');
    for (const name of ['lib.mjs', 'shots-index.mjs', 'shot-paths.mjs', 'capture-generation.mjs']) {
      await copyFile(new URL(name, import.meta.url), join(dir, 'scripts/ui', name));
    }
    await writeFile(join(dir, 'docs/plans/shots/oui/README.md'), '[expected.png](expected.png)\n');
    await writeFile(join(dir, 'docs/plans/shots/oui/expected.png'), 'historical image');
    await writeFile(join(dir, 'bin/build2'), `#!/usr/bin/env bash
if [[ "$2" == --test ]]; then echo unit >> "$CALL_LOG"; exit 0; fi
printf '%s\\n' "$2" >> "$CALL_LOG"
if [[ "$2" == scripts/ui/shots-index.mjs ]]; then exec "$NODE" "$2"; fi
if [[ "$2" == scripts/ui/shell.mjs ]]; then
  "$NODE" --input-type=module -e 'import { shotPath } from "./scripts/ui/lib.mjs"; import { writeFile } from "node:fs/promises"; await writeFile(await shotPath("expected"), "generated image");'
fi
if [[ "$2" == scripts/ui/smoke.mjs && "$SCENARIO" == extra ]]; then touch "$SHOTS_DIR/late.png"; fi
`, { mode: 0o700 });
    for (const scenario of ['complete', 'extra']) {
      await writeFile(join(dir, 'calls'), '');
      const env = { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, CALL_LOG: join(dir, 'calls'), NODE: process.execPath, SCENARIO: scenario };
      delete env.SHOTS_DIR;
      const result = spawnSync('bash', ['-c', body.replace(/^ {4}/gm, '')], { cwd: dir, encoding: 'utf8', env });
      assert.equal(await readFile(join(dir, 'calls'), 'utf8'), 'reap\nunit\nscripts/ui/shell.mjs\nscripts/ui/smoke.mjs\nscripts/ui/shots-index.mjs\n');
      if (scenario === 'complete') assert.equal(result.status, 0, result.stderr);
      else { assert.notEqual(result.status, 0); assert.match(result.stderr, /late\.png/); }
      assert.equal(await readFile(join(dir, 'docs/plans/shots/oui/expected.png'), 'utf8'), 'historical image');
    }
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test('evidence recipes forward paths and publisher arguments without shell expansion', async () => {
  const dir = await mkdtemp(join(scratch, 'vecnet-evidence-recipes-'));
  try {
    await copyFile(new URL('../../justfile', import.meta.url), join(dir, 'justfile'));
    await mkdir(join(dir, 'bin'));
    await writeFile(join(dir, 'bin/node'), `#!/usr/bin/env bash
exec "$NODE" -e 'process.stdout.write(JSON.stringify(process.argv.slice(1)))' -- "$@"
`, { mode: 0o700 });
    const env = { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, NODE: process.execPath };
    const args = ['captures with spaces', '--receipt', 'docs/plans/receipt $(literal).json', '--dry-run'];
    const publish = spawnSync('just', ['evidence-publish', ...args], { cwd: dir, env, encoding: 'utf8' });
    assert.equal(publish.status, 0, publish.stderr);
    assert.deepEqual(JSON.parse(publish.stdout), ['scripts/evidence/publish.mjs', ...args]);
    const check = spawnSync('just', ['check-evidence'], { cwd: dir, env, encoding: 'utf8' });
    assert.equal(check.status, 0, check.stderr);
    assert.deepEqual(JSON.parse(check.stdout), ['scripts/evidence/check.mjs']);
  } finally { await rm(dir, { recursive: true, force: true }); }
});
