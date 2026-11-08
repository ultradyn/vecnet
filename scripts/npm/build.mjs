import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, cp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
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

// The integration coordinator can reserve one worker without a hidden second
// compiler. Builds remain sequential across both platform targets.
const workers = process.env.GOMAXPROCS || '2';
if (!/^[12]$/.test(workers)) throw new Error('npm build requires GOMAXPROCS=1 or 2');
const env = { ...process.env, CGO_ENABLED: '0', GOMAXPROCS: workers };
const go = args => execFileSync('go', args, { cwd: root, env, encoding: 'utf8' }).trim();
let revision = null;
try {
  revision = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
} catch { /* Build-box copies do not include .git. */ }
const info = { version: manifest.version, revision, go: go(['version']), binaries: {},
  delivery: { entry: 'connectors/launcher/src/codex/delivery-main.ts', assets: {} } };

// Preserve the delivery entry's existing static closure and digest inventory.
// Job-launcher assets are staged separately below, including dynamic plugins.
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

// The launcher loads builtins dynamically from config, so an import walk rooted
// at main.ts alone is incomplete. Include all implementation modules and only
// allowlisted builtin directories; ACP enters automatically when integrated.
const builtinAllowlist = new Set(['t3','paseo','codex-app-server','acp']);
const backendRoot = 'connectors/launcher/src/backends';
const builtins = (await readdir(path.join(root,backendRoot),{withFileTypes:true})).map(entry=>{
  if (!entry.isDirectory() || !builtinAllowlist.has(entry.name)) throw new Error('unrecognized packaged backend '+entry.name);
  return entry.name;
}).sort();
for (const required of ['t3','paseo','codex-app-server']) if (!builtins.includes(required)) throw new Error('missing packaged backend '+required);
info.launcher = {entry:'connectors/launcher/src/main.ts',backends:builtins,assets:{}};
async function stageLauncher(file) {
  const bytes = await readFile(path.join(root,file));
  await mkdir(path.dirname(path.join(destination,file)),{recursive:true});
  await writeFile(path.join(destination,file),bytes);
  info.launcher.assets[file] = createHash('sha256').update(bytes).digest('hex');
}
async function stageModules(directory) {
  for (const entry of await readdir(path.join(root,directory),{withFileTypes:true})) {
    const file=path.posix.join(directory,entry.name);
    if (entry.isDirectory()) await stageModules(file);
    else if (entry.isFile() && /^[A-Za-z0-9_.-]+\.ts$/.test(entry.name)) await stageLauncher(file);
    else throw new Error('non-runtime file in launcher module tree: '+file);
  }
}
for (const project of ['launcher','hub-client']) {
  await stageModules(`connectors/${project}/src`);
  const file=`connectors/${project}/package.json`;
  // Keep the exact minimal ESM boundary already used by delivery.
  info.launcher.assets[file]=createHash('sha256').update(await readFile(path.join(destination,file))).digest('hex');
}
for (const name of schemas) await stageLauncher(`connectors/launcher/schema/${name}.v1.schema.json`);
await stageLauncher('connectors/launcher/systemd/vecnet-launcher-profile.service.in');
console.log(`Staged ${Object.keys(info.launcher.assets).length} launcher runtime files (${builtins.join(', ')})`);

// Build sequentially within the caller's reserved worker budget.
for (const [platform, goos, goarch] of [
  ['linux-x64', 'linux', 'amd64'],
  ['linux-arm64', 'linux', 'arm64'],
]) {
  const directory = path.join(destination, 'native', platform);
  await mkdir(directory, { recursive: true });
  console.log(`Building vecnet ${manifest.version} for ${platform}`);
  execFileSync('go', [
    'build', '-mod=readonly', '-p', workers, '-trimpath', '-buildvcs=false',
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
await writeNotices({ root, destination: path.join(destination, 'THIRD_PARTY_NOTICES.txt'), workers });
await writeFile(path.join(destination, 'BUILD_INFO.json'), `${JSON.stringify(info, null, 2)}\n`);
console.log(`Staged ${manifest.name}@${manifest.version} in ${destination}`);
