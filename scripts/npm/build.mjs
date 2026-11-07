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
const info = { version: manifest.version, revision, go: go(['version']), binaries: {},
  delivery: { entry: 'connectors/launcher/src/codex/delivery-main.ts', assets: {} } };

// Follow static runtime imports only. Type-only imports disappear under Node's
// native stripping; tests, backend plugins, dependencies and local config never
// enter this release. Preserve ESM boundaries inside the CommonJS npm wrapper.
const schemas = ['launch-input','launch-permission','launch-question','launch-answers','launch-approval-closed'];
const pending = [info.delivery.entry, ...schemas.map(name => `connectors/launcher/schema/${name}.v1.schema.json`)], visited = new Set();
while (pending.length) {
  const file = pending.pop();
  if (visited.has(file)) continue;
  if (!/^connectors\/(launcher|hub-client)\/src\/[A-Za-z0-9_./-]+\.ts$/.test(file) && !schemas.some(name => file === `connectors/launcher/schema/${name}.v1.schema.json`)) throw new Error('delivery import outside runtime sources');
  visited.add(file);
  const bytes = await readFile(path.join(root, file));
  const target = path.join(destination, file);
  await mkdir(path.dirname(target), { recursive: true });
  await writeFile(target, bytes);
  info.delivery.assets[file] = createHash('sha256').update(bytes).digest('hex');
  if (file.endsWith('.json')) continue;
  for (const [statement] of bytes.toString('utf8').matchAll(/^import(?!\s+type\b)[^;]+;/gm)) {
    const specifier = statement.match(/['"]([^'"]+)['"]\s*;$/)?.[1];
    if (!specifier) throw new Error('unrecognized delivery import');
    if (specifier.startsWith('node:')) continue;
    if (!specifier.startsWith('.')) throw new Error('delivery must not need npm dependencies');
    pending.push(path.posix.normalize(path.posix.join(path.posix.dirname(file), specifier)));
  }
}
for (const project of ['launcher', 'hub-client']) {
  const file = `connectors/${project}/package.json`, bytes = '{"type":"module"}\n';
  await writeFile(path.join(destination, file), bytes);
  info.delivery.assets[file] = createHash('sha256').update(bytes).digest('hex');
}
console.log(`Staged ${visited.size} delivery runtime files`);

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
