import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {mkdtemp, mkdir, readFile, writeFile, access, rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import {stageSource, restoreSource} from './source-archive.mjs';

test('native source archive restores committed fixture bytes and sparse ancestor files without a checkout', async t => {
  const temp=await mkdtemp(path.join(os.tmpdir(),'ci source archive '));
  t.after(()=>rm(temp,{recursive:true,force:true}));
  const root=path.join(temp,'producer'), bundle=path.join(temp,'bundle'), destination=path.join(temp,'native');
  await mkdir(root); await mkdir(destination);
  const files={
    'go.mod':'module fixture\n', 'internal/cli/testdata/input.txt':'first\nsecond\n',
    'docs/PROTOCOL.md':'ancestor file\n', 'docs/protocol/schema.json':'{}\n',
    'scripts/root-helper.py':'print(1)\n', 'scripts/ci/guard.mjs':'export default 1;\n',
    'connectors/launcher/test/fixture.ts':'export {};\n',
    'docs/design/large.png':'not in the native sparse checkout',
  };
  for(const [name,bytes] of Object.entries(files)){
    await mkdir(path.dirname(path.join(root,name)),{recursive:true});
    await writeFile(path.join(root,name),bytes);
  }
  const git=args=>execFileSync('git',args,{cwd:root,encoding:'utf8'}).trim();
  git(['init','-q']); git(['add','.']);
  git(['-c','core.hooksPath=/dev/null','-c','commit.gpgsign=false','-c','user.name=CI','-c','user.email=ci@example.invalid','commit','-qm','fixture']);
  const sha=git(['rev-parse','HEAD']);
  await writeFile(path.join(root,'internal/cli/testdata/input.txt'),'dirty working copy');
  await writeFile(path.join(root,'internal/cli/testdata/untracked.txt'),'untracked');
  await stageSource(root,bundle);
  await assert.rejects(restoreSource(bundle,'0'.repeat(40),destination),/source/);
  await assert.rejects(access(path.join(destination,'go.mod')));
  await restoreSource(bundle,sha,destination);
  for(const [name,bytes] of Object.entries(files).filter(([name])=>!name.startsWith('docs/design/'))){
    assert.equal(await readFile(path.join(destination,name),'utf8'),bytes,name);
  }
  for(const name of ['.git','internal/cli/testdata/untracked.txt','docs/design/large.png']){
    await assert.rejects(access(path.join(destination,name)));
  }
  const bootstrap=path.join(temp,'bootstrap native'); await mkdir(bootstrap);
  execFileSync(process.execPath,[path.join(bundle,'restore-source.mjs'),'restore',bundle,sha],{cwd:bootstrap});
  assert.equal(await readFile(path.join(bootstrap,'internal/cli/testdata/input.txt'),'utf8'),files['internal/cli/testdata/input.txt']);
  const archive=path.join(bundle,'source.tar.gz'), bytes=await readFile(archive);
  await writeFile(archive,Buffer.concat([bytes,Buffer.from('corruption')]));
  const fresh=path.join(temp,'corrupt-native'); await mkdir(fresh);
  await assert.rejects(restoreSource(bundle,sha,fresh),/hash/);
  await assert.rejects(access(path.join(fresh,'go.mod')));
});
