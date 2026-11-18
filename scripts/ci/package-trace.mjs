// DISPOSABLE R27 probe. Mixed original package + instrumented ARM CLI; never publish.
import assert from 'node:assert/strict';
import {execFileSync, spawnSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {appendFile, chmod, copyFile, mkdir, mkdtemp, readFile, readdir, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {stageSource, restoreSource} from './source-archive.mjs';
import {verifyChecksum} from '../npm/verify-artifact.mjs';

export const original = Object.freeze({
  run: '44363484704', artifact: 14454388762,
  zip: 'e4dbc0545c08cca2361099a3a3e39f0494dd54da047356eff0a99496800a5167',
  head: '7f56ee17e466026e01844221a8bf45a0584a1443',
  revision: 'dadb65b6747a3152e323f5863f92f2ee9a40afcd',
  tree: '8696cc3ef2d3544b3a13e53522724db6617e6e9b',
  source: '56d34e52e6292ffa8746f7131d6039c4a826ca109dde6053c3f14b2f05d69f6c',
  tarball: '3d08fd25f7f058fbe38aa020a988b4a8a936c123fb3f316948abab1e1110df4f',
});
const archiveName = 'vecnet-0.2.5.tgz';
const nativeName = 'native/win32-arm64/vecnet.exe';
const root = fileURLToPath(new URL('../../', import.meta.url));
const work = path.resolve('dist/r27-work');
const output = path.resolve('dist/r27-trace');
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const json = async file => JSON.parse(await readFile(file, 'utf8'));
const exec = (file, args, extra = {}) => execFileSync(file, args, {encoding:'utf8', timeout:45_000, windowsHide:true, ...extra});
const git = args => exec('git', args, {cwd:root}).trim();

async function api(route, binary = false) {
  const repository = process.env.GITHUB_REPOSITORY;
  assert.match(repository ?? '', /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/);
  assert.ok(process.env.GH_TOKEN, 'GH_TOKEN is required');
  const response = await fetch(`https://api.github.com/repos/${repository}/${route}`, {
    headers: {Authorization:`Bearer ${process.env.GH_TOKEN}`, Accept:'application/vnd.github+json'},
    signal:AbortSignal.timeout(45_000),
  });
  assert.ok(response.ok, `GitHub API HTTP ${response.status}`);
  return binary ? Buffer.from(await response.arrayBuffer()) : response.json();
}

export function selectArtifact(list, run, revision, name) {
  const found = list.artifacts.filter(a => a.name === name && !a.expired);
  assert.equal(found.length, 1, 'one exact named artifact is required');
  const artifact = found[0];
  assert.equal(String(artifact.workflow_run.id), String(run), 'artifact run mismatch');
  assert.equal(artifact.workflow_run.head_sha, revision, 'artifact SHA mismatch');
  assert.match(artifact.digest, /^sha256:[a-f0-9]{64}$/);
  return artifact;
}

async function download(run, revision, name, destination, pinned) {
  const list = await api(`actions/runs/${run}/artifacts?per_page=100`);
  const artifact = selectArtifact(list, run, revision, name);
  if (pinned) {
    assert.equal(artifact.id, original.artifact);
    assert.equal(artifact.digest, `sha256:${original.zip}`);
  }
  const bytes = await api(`actions/artifacts/${artifact.id}/zip`, true);
  assert.equal(sha(bytes), artifact.digest.slice(7), 'downloaded ZIP digest mismatch');
  await mkdir(destination, {recursive:true, mode:0o700});
  const zip = path.join(destination, 'download.zip');
  await writeFile(zip, bytes);
  if (process.platform === 'win32') exec('tar', ['-xf', zip, '-C', destination]);
  else exec('unzip', ['-q', zip, '-d', destination]);
  await rm(zip);
  await writeFile(path.join(destination, 'download.json'), JSON.stringify({run, revision, id:artifact.id, sha256:sha(bytes)}));
}

export async function verifyOriginal(directory) {
  const receipt = await json(path.join(directory, 'npm-source/source.json'));
  assert.deepEqual(receipt, {revision:original.revision, scope:'package', sha256:original.source});
  assert.equal(sha(await readFile(path.join(directory, 'npm-source/source.tar.gz'))), original.source);
  const digest = verifyChecksum(await readFile(path.join(directory, 'SHA256SUMS'), 'utf8'), archiveName,
    await readFile(path.join(directory, archiveName)));
  assert.equal(digest, original.tarball);
  return receipt;
}

async function prepare() {
  const directory = path.join(work, 'original');
  await download(original.run, original.head, 'npm-release', directory, true);
  await verifyOriginal(directory);
  const commit = await api(`git/commits/${original.revision}`);
  assert.equal(commit.tree.sha, original.tree, 'original merge tree changed');
  assert.equal(git(['rev-parse', `${original.head}^{tree}`]), original.tree, 'probe base differs from original merge tree');
  await mkdir(path.join(work, 'unpacked'), {recursive:true, mode:0o700});
  exec('tar', ['-xzf', path.join(directory, archiveName), '-C', path.join(work, 'unpacked')]);
  const source = await mkdtemp(path.join(os.tmpdir(), 'r27-source-'));
  try {
    await restoreSource(path.join(directory, 'npm-source'), original.revision, source);
    for (const file of ['scripts/npm/package.test.mjs', 'npm/package.json']) {
      assert.deepEqual(await readFile(path.join(source, file)), await readFile(path.join(root, file)), `original ${file} changed`);
    }
  } finally { await rm(source, {recursive:true, force:true}); }
  const info = await json(path.join(work, 'unpacked/package/BUILD_INFO.json'));
  assert.equal(info.revision, original.revision);
  assert.equal(info.version, '0.2.5');
  for (const [file, digest] of Object.entries(info.binaries)) {
    assert.equal(sha(await readFile(path.join(work, 'unpacked/package', file))), digest, file);
  }
  console.error(`R27_PROBE verified original source=${original.revision} tree=${original.tree} tgz=${original.tarball}`);
}

async function build() {
  assert.equal(process.platform, 'linux');
  assert.equal(process.env.GOMAXPROCS, '2');
  assert.equal(git(['rev-parse', 'HEAD']), process.env.GITHUB_SHA, 'frozen build source required');
  assert.equal(git(['status', '--porcelain', '--untracked-files=no']), '', 'build source must be clean');
  await mkdir(path.join(work, 'cli'), {recursive:true, mode:0o700});
  exec('go', ['build', '-mod=readonly', '-p', '2', '-trimpath', '-buildvcs=false', '-ldflags',
    '-s -w -X github.com/XertroV/vecnet/internal/mcpserver.Version=0.2.5 -X github.com/XertroV/vecnet/internal/buildinfo.Version=0.2.5',
    '-o', path.join(work, 'cli/vecnet.exe'), './cmd/vecnet'], {
    cwd:root, stdio:'inherit', timeout:51_000,
    env:{...process.env, CGO_ENABLED:'0', GOOS:'windows', GOARCH:'arm64', GOARM64:'v8.0'},
  });
  await writeFile(path.join(work, 'cli/source.json'), JSON.stringify({revision:process.env.GITHUB_SHA,
    go:exec('go', ['version']).trim(), sha256:sha(await readFile(path.join(work, 'cli/vecnet.exe')))}));
}

async function inventory(directory, prefix = '') {
  const files = {};
  for (const entry of await readdir(directory, {withFileTypes:true})) {
    const file = path.posix.join(prefix, entry.name);
    if (entry.isDirectory()) Object.assign(files, await inventory(path.join(directory, entry.name), file));
    else { assert.ok(entry.isFile(), 'unexpected package link'); files[file] = sha(await readFile(path.join(directory, entry.name))); }
  }
  return files;
}

async function pack() {
  assert.equal(git(['rev-parse', 'HEAD']), process.env.GITHUB_SHA, 'probe must build frozen committed source');
  const directory = path.join(work, 'unpacked/package');
  const before = await inventory(directory);
  const native = await readFile(path.join(work, 'cli/vecnet.exe'));
  const buildSource = await json(path.join(work, 'cli/source.json'));
  assert.equal(buildSource.revision, process.env.GITHUB_SHA);
  assert.equal(buildSource.sha256, sha(native));
  assert.equal(native.subarray(0, 2).toString(), 'MZ');
  assert.equal(native.readUInt16LE(native.readUInt32LE(0x3c) + 4), 0xaa64);
  await copyFile(path.join(work, 'cli/vecnet.exe'), path.join(directory, nativeName));
  await chmod(path.join(directory, nativeName), 0o755);
  const infoFile = path.join(directory, 'BUILD_INFO.json');
  const info = await json(infoFile);
  info.revision = process.env.GITHUB_SHA;
  info.binaries[nativeName] = sha(native);
  await writeFile(infoFile, JSON.stringify(info, null, 2) + '\n');
  const after = await inventory(directory);
  assert.deepEqual(Object.keys(before).sort(), Object.keys(after).sort());
  assert.deepEqual(Object.keys(before).filter(file => before[file] !== after[file]).sort(), ['BUILD_INFO.json', nativeName].sort());
  await mkdir(output, {recursive:true, mode:0o700});
  // Preserve every original runtime/manifest/license byte; only these two files differ.
  const entries = path.join(work, 'package-files.txt');
  await writeFile(entries, Object.keys(after).sort().map(file => `package/${file}\n`).join(''));
  exec('tar', ['-czf', path.join(output, archiveName), '-C', path.join(work, 'unpacked'),
    '--no-recursion', '--verbatim-files-from', '-T', entries]);
  const mixed = sha(await readFile(path.join(output, archiveName)));
  await writeFile(path.join(output, 'SHA256SUMS'), `${mixed}  ${archiveName}\n`);
  await mkdir(path.join(output, 'npm-tool'), {recursive:true});
  await copyFile(path.join(work, 'original/npm-tool/npm.tgz'), path.join(output, 'npm-tool/npm.tgz'));
  await stageSource(root, path.join(output, 'npm-source'), 'package');
  const source = await json(path.join(output, 'npm-source/source.json'));
  const receipt = {kind:'trace-mixed-diagnostic-only-never-publish', original, revision:process.env.GITHUB_SHA,
    originalCli:before[nativeName], diagnosticCli:sha(native), diagnosticGo:buildSource.go, mixedTarball:mixed,
    sourceArchive:source.sha256, npmTool:sha(await readFile(path.join(output, 'npm-tool/npm.tgz'))),
    changedPackageFiles:['BUILD_INFO.json', nativeName], files:after};
  await writeFile(path.join(output, 'trace.json'), JSON.stringify(receipt, null, 2) + '\n');
  await copyFile(path.join(work, 'original/npm-source/source.json'), path.join(output, 'original-source.json'));
  console.error('R27_PROBE trace-mixed diagnostic receipt ' + JSON.stringify({...receipt, files:undefined}));
}

async function consume() {
  assert.equal(process.platform, 'win32');
  assert.equal(process.arch, 'arm64');
  assert.equal(process.versions.node.split('.')[0], '24');
  const receipt = await json('dist/trace.json');
  assert.equal(receipt.kind, 'trace-mixed-diagnostic-only-never-publish');
  assert.deepEqual(receipt.original, original);
  assert.equal(receipt.revision, process.env.GITHUB_SHA);
  assert.notEqual(receipt.mixedTarball, original.tarball, 'diagnostic archive must not claim original bytes');
  assert.notEqual(receipt.diagnosticCli, receipt.originalCli, 'diagnostic CLI must differ');
  assert.deepEqual(await json('dist/original-source.json'),
    {revision:original.revision, scope:'package', sha256:original.source});
  const source = await json('dist/npm-source/source.json');
  assert.equal(source.revision, receipt.revision);
  assert.equal(source.scope, 'package');
  assert.equal(source.sha256, receipt.sourceArchive);
  assert.equal(sha(await readFile('dist/npm-tool/npm.tgz')), receipt.npmTool);
  assert.equal(verifyChecksum(await readFile('dist/SHA256SUMS', 'utf8'), archiveName,
    await readFile(path.join('dist', archiveName))), receipt.mixedTarball);
  await restoreSource('dist/npm-source', receipt.revision, root);
  const temporary = await mkdtemp(path.join(os.tmpdir(), 'r27-package-check-'));
  try {
    exec('tar', ['-xzf', path.resolve('dist', archiveName), '-C', temporary]);
    assert.deepEqual(await inventory(path.join(temporary, 'package')), receipt.files);
    const info = await json(path.join(temporary, 'package/BUILD_INFO.json'));
    assert.equal(info.revision, receipt.revision);
    assert.equal(info.binaries[nativeName], receipt.diagnosticCli);
  } finally { await rm(temporary, {recursive:true, force:true}); }
  console.error(`R27_PROBE consuming MIXED DIAGNOSTIC ONLY source=${receipt.revision} tgz=${receipt.mixedTarball} cli=${receipt.diagnosticCli}`);
}

export function instrumentTest(source) {
  const line = '    return spawnSync(process.execPath, [binary, ...args], { cwd: temporary, env, encoding: \'utf8\', timeout: 20_000, windowsHide: true, ...extra });';
  assert.equal(source.split(line).length, 2, 'original package test run seam changed');
  return source.replace(line, `    const traceBegin = performance.now();
    const result = spawnSync(process.execPath, [binary, ...args], { cwd: temporary, env, encoding: 'utf8', timeout: 20_000, windowsHide: true, ...extra });
    process.stderr.write('R27_PROBE command=' + path.basename(binary) + ' elapsed_ms=' + Math.round(performance.now() - traceBegin) + ' status=' + result.status + '\\n');
    for (const line of (result.stderr || '').split(/\\r?\\n/)) if (line.startsWith('R27_TRACE ')) process.stderr.write(line + '\\n');
    return result;`);
}

async function testPackage() {
  const originalTest = path.join(root, 'scripts/npm/package.test.mjs');
  const source = await readFile(originalTest, 'utf8');
  const traced = path.join(root, 'scripts/npm/.r27-package-trace.test.mjs');
  await writeFile(traced, instrumentTest(source), {flag:'wx'});
  try {
    const result = spawnSync(process.execPath, ['--test', '--test-concurrency=2', traced], {
      cwd:root, env:process.env, stdio:'inherit', timeout:53_000, windowsHide:true,
    });
    assert.deepEqual(await readFile(originalTest), Buffer.from(source), 'original package test must remain unchanged');
    if (result.error) throw result.error;
    process.exitCode = result.status ?? 1;
  } finally { await rm(traced, {force:true}); }
}

async function deadline(audit = false) {
  const run = await api(`actions/runs/${process.env.GITHUB_RUN_ID}`);
  assert.equal(run.head_sha, process.env.GITHUB_SHA);
  const start = Date.parse(run.run_started_at);
  assert.ok(Number.isFinite(start));
  const end = start + 300_000;
  assert.ok(Date.now() < end - 3000, 'whole probe must finish in <300s');
  if (audit) {
    const jobs = await api(`actions/runs/${process.env.GITHUB_RUN_ID}/jobs?per_page=100`);
    assert.equal(jobs.jobs.length, 2, 'exactly two diagnostic jobs required');
    for (const job of jobs.jobs) for (const step of job.steps ?? []) {
      if (step.started_at && step.completed_at) assert.ok(Date.parse(step.completed_at) - Date.parse(step.started_at) < 60_000, `step must be <60s: ${step.name}`);
    }
    assert.ok(Date.now() < end - 3000, 'whole probe must finish in <300s');
    console.error(`R27_PROBE budget elapsed_ms=${Date.now() - start}; final Actions/API outcome still requires controller inspection`);
  } else await appendFile(process.env.GITHUB_ENV, `CI_RUN_DEADLINE=${end}\n`);
}

async function main(mode) {
  if (mode === 'prepare') await prepare();
  else if (mode === 'build') await build();
  else if (mode === 'pack') await pack();
  else if (mode === 'download') await download(process.env.GITHUB_RUN_ID, process.env.GITHUB_SHA, 'r27-trace-mixed', path.resolve('dist'));
  else if (mode === 'consume') await consume();
  else if (mode === 'test') await testPackage();
  else if (mode === 'deadline' || mode === 'audit') await deadline(mode === 'audit');
  else throw new Error('package-trace mode required');
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main(process.argv[2]).catch(error => { console.error(error.message); process.exitCode = 1; });
}
