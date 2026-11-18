import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const suite = fileURLToPath(new URL('./suite.mjs', import.meta.url));
test('real Go discovery and every scheduled command execute all roots exactly once', { timeout: 50_000 }, async t => {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'ci conserved Go '));
  t.after(() => rm(cwd, { recursive: true, force: true }));
  await writeFile(path.join(cwd, 'go.mod'), 'module ci-fixture\n\ngo 1.26\n');
  const roots = Array.from({ length: 101 }, (_, n) => `TestCase${n}`);
  const declarations = roots.map(name => `func ${name}(t *testing.T) {}`).join('\n');
  await writeFile(path.join(cwd, 'cases_test.go'), `package fixture\nimport "testing"\n${declarations}\nfunc FuzzSeed(f *testing.F) {f.Add("seed"); f.Fuzz(func(t *testing.T, s string){})}\n`);
  // An executable example with stdout is included by Go's actual discovery.
  await writeFile(path.join(cwd, 'example_test.go'), 'package fixture\nimport "fmt"\nfunc Example() {fmt.Println("example")\n// Output: example\n}\n');
  await mkdir(path.join(cwd, 'small'));
  await writeFile(path.join(cwd, 'small', 'one_test.go'), 'package small\nimport "testing"\nfunc TestSmall(t *testing.T) {}\n');
  await mkdir(path.join(cwd, 'empty'));
  await writeFile(path.join(cwd, 'empty', 'empty.go'), 'package empty\n');
  const invoke = args => execFileSync(process.execPath, [suite, ...args], { cwd, encoding: 'utf8', timeout: 45_000, maxBuffer: 4 * 1024 * 1024 });
  invoke(['discover-go']);
  const inventory = JSON.parse(await readFile(path.join(cwd, '.ci', 'go.json')));
  assert.equal(inventory.length, 3);
  const passed = [];
  for (let lane = 0; lane < 2; lane++) for (let group = 0; group < 8; group++) {
    const output = invoke(['go', String(lane), String(group)]);
    for (const line of output.split('\n')) {
      if (!line.startsWith('{')) continue;
      const event = JSON.parse(line);
      if (event.Action === 'pass' && event.Test && !event.Test.includes('/')) passed.push(event.Test);
    }
  }
  assert.deepEqual(passed.sort(), [...roots, 'FuzzSeed', 'Example', 'TestSmall'].sort());
});
