import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { releaseInfo } from './release.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const source = path.join(root, 'npm');
const destination = path.join(root, 'dist', 'npm');
const manifest = JSON.parse(await readFile(path.join(source, 'package.json'), 'utf8'));
releaseInfo(manifest);

// This directory contains only generated npm release files.
await rm(destination, { recursive: true, force: true });
await mkdir(destination, { recursive: true });
for (const file of ['package.json', 'README.md', 'bin']) {
  await cp(path.join(source, file), path.join(destination, file), { recursive: true });
}
for (const command of ['vecnet', 'vecnet-hub']) {
  await chmod(path.join(destination, 'bin', `${command}.cjs`), 0o755);
}

const env = { ...process.env, CGO_ENABLED: '0', GOMAXPROCS: '2' };
const go = args => execFileSync('go', args, { cwd: root, env, encoding: 'utf8' }).trim();
let revision = null;
try {
  revision = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
} catch { /* Build-box copies do not include .git. */ }
const info = { version: manifest.version, revision, go: go(['version']), binaries: {} };

// Build sequentially so release preparation uses at most two compiler threads.
for (const [platform, goos, goarch] of [
  ['linux-x64', 'linux', 'amd64'],
  ['linux-arm64', 'linux', 'arm64'],
]) {
  const directory = path.join(destination, 'native', platform);
  await mkdir(directory, { recursive: true });
  console.log(`Building vecnet ${manifest.version} for ${platform}`);
  execFileSync('go', [
    'build', '-mod=readonly', '-p', '2', '-trimpath', '-buildvcs=false',
    '-ldflags', `-s -w -X github.com/XertroV/vecnet/internal/mcpserver.Version=${manifest.version}`,
    '-o', `${directory}${path.sep}`, './cmd/vecnet', './cmd/vecnet-hub',
  ], {
    cwd: root,
    env: { ...env, GOOS: goos, GOARCH: goarch, GOAMD64: 'v1', GOARM64: 'v8.0' },
    stdio: 'inherit',
  });
  for (const command of ['vecnet', 'vecnet-hub']) {
    const file = path.join(directory, command);
    await chmod(file, 0o755);
    info.binaries[`native/${platform}/${command}`] = createHash('sha256').update(await readFile(file)).digest('hex');
  }
}

const { writeNotices } = await import('./notices.mjs');
await writeNotices({ root, destination: path.join(destination, 'THIRD_PARTY_NOTICES.txt') });
await writeFile(path.join(destination, 'BUILD_INFO.json'), `${JSON.stringify(info, null, 2)}\n`);
console.log(`Staged ${manifest.name}@${manifest.version} in ${destination}`);
