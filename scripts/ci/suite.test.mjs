import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, realpath, stat, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const {nativeGoLanes}=await import('./schedule.mjs');
const lanes=nativeGoLanes(process.platform);
const suite = fileURLToPath(new URL('./suite.mjs', import.meta.url));
test('real Go discovery and every scheduled command execute all roots exactly once', { timeout: 50_000 }, async t => {
  const cwd = await mkdtemp(path.join(os.tmpdir(), 'ci conserved Go '));
  t.after(() => rm(cwd, { recursive: true, force: true }));
  await writeFile(path.join(cwd, 'go.mod'), 'module ci-fixture\n\ngo 1.26\n');
  const roots = Array.from({ length: 401 }, (_, n) => `TestCase${n}`);
  const declarations = roots.map(name => `func ${name}(t *testing.T) { checkPrivate(t) }`).join('\n');
  await writeFile(path.join(cwd, 'cases_test.go'), `package fixture\nimport ("testing";"os";"path/filepath";"fmt";"time";"sync")\nvar barrierOnce sync.Once\nfunc checkPrivate(t *testing.T) { t.Helper(); for _, key := range []string{"HOME","USERPROFILE","APPDATA","LOCALAPPDATA","TMPDIR","TMP","TEMP","XDG_CONFIG_HOME","XDG_CACHE_HOME","XDG_DATA_HOME","XDG_STATE_HOME","XDG_RUNTIME_DIR"} { p:=os.Getenv(key); if p=="" || !filepath.IsAbs(p) {t.Fatalf("missing private %s",key)}; info,err:=os.Stat(p); if err!=nil || !info.IsDir() {t.Fatalf("bad private %s",key)}; if filepath.Separator=='/' && info.Mode().Perm()!=0700 {t.Fatalf("nonprivate mode %s: %v",key,info.Mode())}; physical,err:=filepath.EvalSymlinks(p);if err!=nil || physical!=p {t.Fatalf("nonphysical private root %s",key)} }; barrierOnce.Do(func(){if shared:=os.Getenv("CI_PACKET_BARRIER"); shared!="" {if err:=os.WriteFile(filepath.Join(shared,fmt.Sprint(os.Getpid())),nil,0600);err!=nil{t.Fatal(err)}; until:=time.Now().Add(3*time.Second);for {files,err:=os.ReadDir(shared);if err!=nil{t.Fatal(err)};if len(files)>=2{break};if time.Now().After(until){t.Fatal("same-package packets did not overlap")};time.Sleep(time.Millisecond)}}}); home:=os.Getenv("HOME"); marker:=filepath.Join(home,"owner"); want:=fmt.Sprint(os.Getpid()); if old,err:=os.ReadFile(marker); err==nil && string(old)!=want {t.Fatal("shared HOME between packets")}; if err:=os.WriteFile(marker,[]byte(want),0600); err!=nil {t.Fatal(err)}; t.Cleanup(func(){if os.Getenv("CI_PACKET_FAIL")=="1" {t.Error("packet cleanup assertion")}}); fmt.Printf("PRIVATE_HOME=%s\\n",home) }\n${declarations}\nfunc FuzzSeed(f *testing.F) {f.Add("seed"); f.Fuzz(func(t *testing.T, s string){})}\n`);
  // An executable example with stdout is included by Go's actual discovery.
  await writeFile(path.join(cwd, 'example_test.go'), 'package fixture\nimport "fmt"\nfunc Example() {fmt.Println("example")\n// Output: example\n}\n');
  await mkdir(path.join(cwd, 'small'));
  await writeFile(path.join(cwd, 'small', 'one_test.go'), 'package small\nimport ("testing";"os")\nfunc TestSmall(t *testing.T) {if os.Getenv("CI_SCHEDULE_FAIL")=="1" {t.Fatal("scheduled assertion fails")}}\n');
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

  const passed = [], executions = [], homes = new Set();
  for (let lane = 0; lane < lanes; lane++) for (let group = 0; group < 4; group++) {
    const output = execFileSync(process.execPath,[suite,'go',String(lane),String(group)],{cwd,encoding:'utf8',timeout:45000,env:{...process.env,PATH:shim+path.delimiter+process.env.PATH},maxBuffer:4*1024*1024});
    for(const line of output.split('\n')) { if(line.startsWith('{')) { const e=JSON.parse(line); const m=e.Output?.match(/PRIVATE_HOME=(.+)\n/); if(m)homes.add(m[1]); } }
    executions.push(...output.split('\n').filter(line => line.startsWith('ci-package ')));
    for (const line of output.split('\n')) {
      if (!line.startsWith('{')) continue;
      const event = JSON.parse(line);
      if (event.Action === 'pass' && event.Test && !event.Test.includes('/')) passed.push(event.Test);
    }
  }
  assert.ok(homes.size>lanes*4,'multiple private packet processes per partition');
  for(const home of homes)await assert.rejects(stat(home),{code:'ENOENT'});
  assert.deepEqual(passed.sort(), [...roots, 'FuzzSeed', 'Example', 'TestSmall'].sort());
  assert.equal(executions.filter(line=>line.includes('pkg=ci-fixture/empty ')).length,1,'no-test package scheduled exactly once');
  assert.equal(executions.filter(line=>line.includes('pkg=ci-fixture/small ')).length,1,'small package scheduled whole exactly once');
  const barrier=path.join(cwd,'barrier');await mkdir(barrier,{mode:0o700});
  const overlap=execFileSync(process.execPath,[suite,'go','0','0'],{cwd,env:{...env,CI_PACKET_BARRIER:barrier},encoding:'utf8',timeout:45000});
  assert.match(overlap,/PRIVATE_HOME=/,'real same-package workers rendezvous before either can exit');
  assert.throws(()=>execFileSync(process.execPath,[suite,'go','0','0'],{
    cwd,env:{...env,CI_PACKET_FAIL:'1'},encoding:'utf8',stdio:'pipe',timeout:45000,
  }),error=>{assert.match(error.stdout,/packet cleanup assertion/);assert.match(error.stderr,/ci-fixture failed/);return true;});
  // A real assertion failure in the compiled Go binary must make the whole
  // command RED; no compile/relink needed to exercise failure propagation.
  const {planGo}=await import('./schedule.mjs');
  const scheduled=planGo(inventory,{}, {lanes}).findIndex(tasks=>tasks.some(p=>p.pkg==='ci-fixture/small'));
  assert.throws(()=>execFileSync(process.execPath,[suite,'go',String(scheduled%lanes),String(Math.floor(scheduled/lanes))],{
    cwd,env:{...env,CI_SCHEDULE_FAIL:'1'},encoding:'utf8',stdio:'pipe',timeout:45000,
  }),error=>{
    assert.match(error.stdout,/scheduled assertion fails/);
    assert.match(error.stderr,/ci-fixture\/small failed/);
    return true;
  });
});
