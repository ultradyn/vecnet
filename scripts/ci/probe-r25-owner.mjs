// Disposable diagnostic only. Never merge this script or its workflow.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { buildWindowsOwner, readWindowsOwner, stageTargetOwner } from './windows-owner.mjs';
import { run } from './budget.mjs';
import path from 'node:path';
const directory = path.resolve('dist/owner');
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const platform = 'win32-' + (process.env.GOARCH === 'amd64' ? 'x64' : process.env.GOARCH || process.arch);
const unit = path.join(directory, 'owner.test.exe');
const mode = process.argv[2];
if (mode === 'build') {
  await mkdir(directory, {recursive: true});
  const metadata = await buildWindowsOwner(directory, platform, process.env);
  execFileSync('go', ['test', '-mod=readonly', '-p', '2', '-trimpath', '-ldflags', '-s -w', '-c', '-o', unit, './scripts/ci/_owned-windows'], {stdio: 'inherit'});
  await writeFile(path.join(directory, 'probe.json'), JSON.stringify({revision: process.env.GITHUB_SHA, platform, ...metadata, unitHash: sha(await readFile(unit))}));
} else if (mode === 'stage') {
  const metadata = JSON.parse(await readFile(path.join(directory, 'probe.json'), 'utf8'));
  assert.equal(metadata.revision, process.env.GITHUB_SHA);
  assert.equal(metadata.platform, platform);
  assert.equal(metadata.unitHash, sha(await readFile(unit)));
  await stageTargetOwner(await readWindowsOwner(directory, metadata, platform));
  console.log(JSON.stringify({revision: metadata.revision, platform, metadata}));
} else if (mode === 'benchmark') {
  assert.ok(path.isAbsolute(process.env.CI_WINDOWS_OWNER));
  const native = process.env.CI_WINDOWS_OWNER;
  for (const kind of ['native', 'fallback', 'native']) {
    const env = {...process.env};
    if (kind === 'fallback') delete env.CI_WINDOWS_OWNER;
    else env.CI_WINDOWS_OWNER = native;
    const started = performance.now();
    for (let i = 0; i < 3; i++) assert.equal(await run(process.execPath, ['-e', 'process.exit(0)'], {ms: 2000, env}), 0);
    console.log('probe-owner ' + JSON.stringify({kind, launches: 3, elapsed_ms: performance.now() - started, platform}));
  }
} else throw new Error('build, stage or benchmark required');
