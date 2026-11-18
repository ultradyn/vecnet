import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, realpath, stat, rm, writeFile } from 'node:fs/promises';
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
  // Hosted Linux runners may omit all three temporary-directory variables.
  // Discovery must allocate its own private physical directory in that case.
  const env = { ...process.env }; delete env.TMPDIR; delete env.TEMP; delete env.TMP;
  const invoke = args => execFileSync(process.execPath, [suite, ...args], { cwd, env, encoding: 'utf8', timeout: 45_000, maxBuffer: 4 * 1024 * 1024 });
  for(let i=0;i<6;i++)invoke(['discover-go',String(i)]);
  const inventory=(await Promise.all(Array.from({length:6},(_,i)=>readFile(path.join(cwd,'.ci',`go-${i}.json`),'utf8')))).flatMap(JSON.parse);
  assert.equal(inventory.length, 3);
  const compiled=inventory.filter(p=>p.names.length);
  t.after(() => Promise.all([...new Set(inventory.map(p=>p.temporary).filter(Boolean))].map(dir=>rm(dir,{recursive:true,force:true}))));
  for(const directory of new Set(inventory.map(p=>p.temporary))) {
    assert.equal(await realpath(directory),directory);
    if(process.platform!=='win32')assert.equal((await stat(directory)).mode & 0o777,0o700);
  }
  assert.equal(compiled.length,2);
  for(const p of compiled)assert.ok(p.binary && p.directory,'discovery retains original cwd and reusable test binary');
  // A PATH shim rejects go test during execution: compilation belongs solely
  // to discovery. Real binaries must still execute every case exactly once.
  const shim=path.join(cwd,'shim');await mkdir(shim);
  if(process.platform!=='win32')await writeFile(path.join(shim,'go'),'#!/bin/sh\nif [ "$1" = test ]; then echo repeated-compilation >&2; exit 99; fi\nexec '+JSON.stringify(execFileSync('which',['go'],{encoding:'utf8'}).trim())+' "$@"\n',{mode:0o755});

  const passed = [];
  for (let lane = 0; lane < 3; lane++) for (let group = 0; group < 8; group++) {
    const output = execFileSync(process.execPath,[suite,'go',String(lane),String(group)],{cwd,encoding:'utf8',timeout:45000,env:{...process.env,PATH:shim+path.delimiter+process.env.PATH},maxBuffer:4*1024*1024});
    for (const line of output.split('\n')) {
      if (!line.startsWith('{')) continue;
      const event = JSON.parse(line);
      if (event.Action === 'pass' && event.Test && !event.Test.includes('/')) passed.push(event.Test);
    }
  }
  assert.deepEqual(passed.sort(), [...roots, 'FuzzSeed', 'Example', 'TestSmall'].sort());
});
