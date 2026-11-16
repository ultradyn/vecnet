import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync, statSync, writeFileSync } from 'node:fs';
import { chmod, copyFile, mkdir, mkdtemp, readFile, realpath, rm, stat, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { npmInvocation } from './npm-command.mjs';
import { nativeTargets } from './platforms.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const manifest = JSON.parse(await readFile(path.join(root, 'npm', 'package.json'), 'utf8'));
const tarball = path.join(root, 'dist', `vecnet-${manifest.version}.tgz`);
const windows = process.platform === 'win32';
const commands = ['vecnet', 'vecnet-hub'];

function deadline(promise, ms, label) {
  let timer;
  return Promise.race([
    promise,
    new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(label)), ms); }),
  ]).finally(() => clearTimeout(timer));
}

test('release contains only runtime files and complete native binaries', async t => {
  const packed = execFileSync('tar', ['-tzf', tarball], { encoding: 'utf8' }).trim().split(/\r?\n/).sort();
  const binaries = nativeTargets.flatMap(({ platform, suffix }) => commands.map(command => `native/${platform}/${command}${suffix}`));
  const expected = [
    'package.json', 'README.md', 'BUILD_INFO.json', 'THIRD_PARTY_NOTICES.txt',
    'bin/run.cjs', 'bin/vecnet.cjs', 'bin/vecnet-hub.cjs', ...binaries,
  ].map(file => `package/${file}`).sort();
  const runtime = packed.filter(file => file.startsWith('package/connectors/'));
  for (const file of runtime) assert.match(file, /^package\/connectors\/(launcher|hub-client)\/(package\.json|src\/[A-Za-z0-9_./-]+\.ts|schema\/launch-(input|permission|question|answers|approval-closed)\.v1\.schema\.json|systemd\/vecnet-launcher-profile\.service\.in)$/);
  assert.deepEqual(packed.filter(file => !file.startsWith('package/connectors/')), expected);
  const extracted = await mkdtemp(path.join(os.tmpdir(), 'vecnet-npm-inspect-'));
  t.after(() => rm(extracted, { recursive: true, force: true }));
  execFileSync('tar', ['-xzf', tarball, '-C', extracted]);
  const contents = path.join(extracted, 'package');
  const packedManifest = JSON.parse(await readFile(path.join(contents, 'package.json'), 'utf8'));
  assert.equal(packedManifest.name, 'vecnet');
  assert.equal(packedManifest.version, manifest.version);
  assert.deepEqual(packedManifest.os, ['linux', 'win32']);
  assert.deepEqual(packedManifest.cpu, ['x64', 'arm64']);
  const metadata = JSON.parse(await readFile(path.join(contents, 'BUILD_INFO.json'), 'utf8'));
  assert.equal(metadata.version, manifest.version);
  assert.deepEqual(Object.keys(metadata.binaries).sort(), binaries.sort());
  for (const file of binaries) {
    const bytes = await readFile(path.join(contents, file));
    assert.equal(createHash('sha256').update(bytes).digest('hex'), metadata.binaries[file], file);
    if (!windows) assert.ok((await stat(path.join(contents, file))).mode & 0o111, file + ' is executable');
    if (file.includes('/linux-')) {
      assert.equal(bytes.subarray(0, 4).toString('hex'), '7f454c46', file + ' ELF signature');
      assert.equal(bytes.readUInt16LE(18), file.includes('-x64/') ? 62 : 183, file + ' ELF machine');
    } else {
      assert.equal(bytes.subarray(0, 2).toString(), 'MZ', file + ' DOS signature');
      const pe = bytes.readUInt32LE(0x3c);
      assert.equal(bytes.subarray(pe, pe + 4).toString('hex'), '50450000', file + ' PE signature');
      assert.equal(bytes.readUInt16LE(pe + 4), file.includes('-x64/') ? 0x8664 : 0xaa64, file + ' PE machine');
    }
  }
  assert.equal(packedManifest.scripts, undefined, 'installation needs no lifecycle scripts');
  assert.equal(packedManifest.dependencies, undefined, 'runtime has no npm dependencies');
  const notices = await readFile(path.join(contents, 'THIRD_PARTY_NOTICES.txt'), 'utf8');
  assert.match(notices, /modernc\.org\/sqlite/);
  assert.match(notices, /github\.com\/charmbracelet\/x\/windows/, 'Windows-only linked dependencies are licensed');
  assert.match(notices, /copyright/i);
});

test('packed npm installation runs both commands and exchanges messages through its hub', { timeout: 180_000 }, async t => {
  // The installer intentionally rejects group-writable build-lane ancestors.
  const temporary = await mkdtemp(path.join(await realpath(windows ? os.tmpdir() : '/tmp'), 'vecnet npm smoke '));
  let stopHub, cleanupTask, completed = false;
  t.after(async () => {
    try { if (stopHub) await stopHub(); }
    finally {
      try { if (cleanupTask) cleanupTask(); }
      finally {
        if (completed) await rm(temporary, { recursive: true, force: true });
        else t.diagnostic('Preserved failed npm fixture (disposable credentials): ' + temporary);
      }
    }
  });
  const prefix = path.join(temporary, 'prefix with spaces');
  const env = { ...process.env };
  const searchPath = Object.entries(env).find(([key]) => key.toUpperCase() === 'PATH')?.[1] || '';
  for (const key of Object.keys(env)) if (key.toUpperCase() === 'PATH') delete env[key];
  env.PATH = searchPath;
  for (const key of Object.keys(env)) {
    if (/^(VECNET_|CODEX_|XDG_|HOME$|USERPROFILE$|HOMEDRIVE$|HOMEPATH$|APPDATA$|LOCALAPPDATA$)/i.test(key)) delete env[key];
  }
  env.HOME = path.join(temporary, 'home with spaces');
  env.USERPROFILE = env.HOME;
  env.APPDATA = path.join(env.HOME, 'AppData', 'Roaming');
  env.LOCALAPPDATA = path.join(env.HOME, 'AppData', 'Local');
  await mkdir(env.HOME, { mode: 0o700 });
  await mkdir(env.APPDATA, { recursive: true });
  await mkdir(env.LOCALAPPDATA, { recursive: true });
  await chmod(env.HOME, 0o700);
  env.CODEX_HOME = path.join(env.HOME, '.codex');
  await mkdir(env.CODEX_HOME, { mode: 0o700 });
  env.XDG_CONFIG_HOME = path.join(env.HOME, '.config');
  env.XDG_DATA_HOME = path.join(env.HOME, '.local', 'share');
  env.XDG_RUNTIME_DIR = path.join(temporary, 'runtime');
  await mkdir(env.XDG_RUNTIME_DIR, { mode: 0o700 });
  env.XDG_CACHE_HOME = path.join(temporary, 'cache');
  env.VECNET_CONFIG = path.join(temporary, 'unused-config.toml');
  const npm = npmInvocation([
    'install', '--global', '--prefix', prefix, '--offline', '--ignore-scripts',
    '--no-audit', '--no-fund', tarball,
  ], { env });
  execFileSync(npm.file, npm.args, { cwd: temporary, env, encoding: 'utf8', timeout: 60_000, windowsHide: true });

  const installedRoot = windows ? path.join(prefix, 'node_modules', 'vecnet') : path.join(prefix, 'lib', 'node_modules', 'vecnet');
  env.PATH = (windows ? prefix : path.join(prefix, 'bin')) + path.delimiter + env.PATH;
  // Node can exec the installed wrapper without routing its user arguments
  // through npm.cmd/cmd.exe. Also check npm generated the public command shims.
  for (const command of commands) {
    const shim = windows ? path.join(prefix, command + '.cmd') : path.join(prefix, 'bin', command);
    assert.ok((await stat(shim)).isFile(), command + ' npm command shim exists');
    if (windows) {
      // This command line is constant, with no caller-controlled shell text.
      // npm's generated .cmd shim requires cmd.exe; all actual CLI arguments
      // below go straight to Node/the native executable instead.
      const output = execFileSync(path.join(env.SystemRoot || env.SYSTEMROOT || 'C:\\Windows', 'System32', 'cmd.exe'),
        ['/d', '/s', '/c', command + ' --version'], { cwd: temporary, env, encoding: 'utf8', timeout: 15_000, windowsHide: true });
      assert.equal(output.trim(), command + ' ' + manifest.version);
    } else assert.equal(execFileSync(shim, ['--version'], { cwd: temporary, env, encoding: 'utf8', timeout: 15_000 }).trim(), command + ' ' + manifest.version);
  }
  const cli = path.join(installedRoot, 'bin', 'vecnet.cjs');
  const hubBin = path.join(installedRoot, 'bin', 'vecnet-hub.cjs');
  function run(binary, args, extra = {}) {
    return spawnSync(process.execPath, [binary, ...args], { cwd: temporary, env, encoding: 'utf8', timeout: 20_000, windowsHide: true, ...extra });
  }
  function diagnose(binary, result, extra = {}) {
    const command = path.basename(binary, '.cjs');
    const native = path.join(installedRoot, 'native', process.platform + '-' + process.arch, command + (windows ? '.exe' : ''));
    const errorInfo = error => error && ({ code: error.code, errno: error.errno, syscall: error.syscall, message: error.message });
    const summarize = result => ({ status: result.status, signal: result.signal, error: errorInfo(result.error), stderr: result.stderr?.slice(0, 2000) });
    const evidence = { native, pathLength: native.length, wrapper: summarize(result) };
    try { evidence.stat = { size: statSync(native).size }; } catch (error) { evidence.stat = errorInfo(error); }
    try {
      const bytes = readFileSync(native);
      const pe = bytes.readUInt32LE(0x3c);
      evidence.file = { sha256: createHash('sha256').update(bytes).digest('hex'), peMachine: bytes.readUInt16LE(pe + 4).toString(16) };
    } catch (error) { evidence.file = errorInfo(error); }
    // One diagnostic-only probe with constant safe arguments and identical
    // cwd/env, never a retry of a failed enrol/send/install operation.
    evidence.direct = summarize(spawnSync(native, command === 'vecnet' ? ['help'] : ['--help'], {
      cwd: temporary, env: extra.env || env, encoding: 'utf8', timeout: 10_000, windowsHide: true,
    }));
    const childEnv = extra.env || env;
    evidence.environment = Object.fromEntries(['HOME', 'USERPROFILE', 'APPDATA', 'LOCALAPPDATA', 'SystemRoot', 'WINDIR', 'TEMP', 'TMP', 'PATH', 'VECNET_BIN']
      .map(key => [key, Object.entries(childEnv).find(([name]) => name.toUpperCase() === key.toUpperCase())?.[1]]));
    writeFileSync(path.join(temporary, 'npm-failure.json'), JSON.stringify(evidence, null, 2) + '\n');
    t.diagnostic('Native failure evidence: ' + JSON.stringify(evidence));
  }
  function mustRun(binary, args, extra) {
    const result = run(binary, args, extra);
    if (result.status !== 0) diagnose(binary, result, extra);
    assert.equal(result.status, 0, path.basename(binary) + ' failed: ' + (result.stderr || result.error?.message || 'unknown failure'));
    return result.stdout;
  }
  for (const [binary, command] of [[cli, 'vecnet'], [hubBin, 'vecnet-hub']]) {
    assert.equal(mustRun(binary, ['--version']).trim(), `${command} ${manifest.version}`);
  }
  assert.match(mustRun(cli, ['help']), /usage: vecnet/);
  assert.equal(run(cli, ['--definitely-invalid-flag']).status, 1, 'native usage exit code passes through');
  assert.match(run(hubBin, ['--help']).stderr, /data-dir/, 'hub arguments reach the native binary');

  const dataDir = path.join(temporary, 'hub data with spaces & symbols');
  const initialised = mustRun(hubBin, ['init', '--data-dir', dataDir, '--operator-name', 'npm-smoke']);
  const operatorSecret = initialised.match(/operator secret:\s+(\S+)/)?.[1];
  assert.ok(operatorSecret, 'hub init returned an operator credential');
  const hub = spawn(process.execPath, [hubBin, '--listen', '127.0.0.1:0', '--data-dir', dataDir], {
    cwd: temporary, env, detached: !windows, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const exited = new Promise(resolve => hub.once('close', (code, signal) => resolve({ code, signal })));
  let stderr = '';
  hub.stderr.on('data', chunk => { stderr += chunk; });
  let stopping;
  stopHub = () => stopping ??= (async () => {
    try {
      if (hub.exitCode === null && hub.signalCode === null) {
        if (windows) {
          // Windows kill() terminates just the wrapper, not its native child.
          // Terminate the whole tree; POSIX graceful signal forwarding is not
          // available through Node's Windows process API.
          const killed = spawnSync('taskkill.exe', ['/PID', String(hub.pid), '/T', '/F'], { encoding: 'utf8', windowsHide: true });
          assert.equal(killed.status, 0, killed.stderr || killed.error?.message);
        } else hub.kill('SIGTERM');
      }
      const result = await deadline(exited, 15_000, 'hub process tree did not stop');
      if (windows) assert.ok(result.code !== 0 || result.signal, 'Windows reports forced process termination');
      else assert.equal(result.code, 0, 'hub shutdown exits cleanly through the npm wrapper');
    } finally {
      if (!windows && hub.pid) {
        try { process.kill(-hub.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
      }
    }
  })();
  const base = await deadline(new Promise((resolve, reject) => {
    let output = '';
    hub.once('error', reject);
    hub.once('exit', () => reject(new Error(`hub stopped before listening: ${stderr}`)));
    hub.stdout.on('data', chunk => {
      output += chunk;
      const match = output.match(/listening on (http:\/\/127\.0\.0\.1:\d+)/);
      if (match) resolve(match[1]);
    });
  }), 10_000, 'hub did not start');
  const health = await fetch(`${base}/healthz`, { signal: AbortSignal.timeout(5000) });
  assert.equal(health.status, 200);
  await health.text();
  const ui = await fetch(`${base}/ui/`, { signal: AbortSignal.timeout(5000) });
  assert.equal(ui.status, 200);
  assert.match(await ui.text(), /<html/);

  const operatorConfig = path.join(temporary, 'operator.toml');
  const operatorContents = `[profiles.op]\nhub = ${JSON.stringify(base)}\nsecret = ${JSON.stringify(operatorSecret)}\n`;
  if (windows) mustRun(cli, ['local-platform', 'write-private', operatorConfig], { input: operatorContents });
  else await writeFile(operatorConfig, operatorContents, { mode: 0o600 });
  const users = [];
  for (const name of ['sender', 'receiver']) {
    const added = JSON.parse(mustRun(cli, ['--net', 'op', '--json', 'peer', 'add', '--kind', 'session', '--name', name], {
      env: { ...env, VECNET_CONFIG: operatorConfig },
    }));
    const userEnv = { ...env, VECNET_CONFIG: path.join(temporary, `${name}.toml`) };
    mustRun(cli, ['enrol', '--hub', base, '--token', '-', '--profile', name], {
      env: userEnv, input: `${added.enrol_token}\n`,
    });
    users.push({ name, id: added.peer_id, env: userEnv });
  }
  const [sender, receiver] = users;
  const message = 'hello from npm with spaces & | ^ %PATH% "quotes" $(echo not-a-shell)';
  const sent = JSON.parse(mustRun(cli, ['--net', sender.name, '--json', 'send', receiver.id, message, '--hold'], { env: sender.env }));
  const inbox = JSON.parse(mustRun(cli, ['--net', receiver.name, '--json', 'inbox'], { env: receiver.env }));
  assert.ok(inbox.events.some(event => event.data?.message?.message_id === sent.message_id && event.data.message.content.text === message));
  const projects = JSON.parse(mustRun(cli, ['projects', 'list', '--json']));
  const projectRoot = await realpath(temporary);
  const project = projects.find(entry => windows ? entry.root.toLowerCase() === projectRoot.toLowerCase() : entry.root === projectRoot);
  assert.ok(project, 'packed CLI ordinary use registers the disposable project');
  assert.equal(project.observed_version, manifest.version, 'installed native CLI records the npm release version');

  // Bootstrap using only the actually npm-installed CLI and assets, outside
  // the checkout. Linux fakes systemctl; Windows registers a real, disabled
  // per-user task, so no background agent survives this test.
  const added = JSON.parse(mustRun(cli, ['--net', 'op', '--json', 'peer', 'add', '--kind', 'launcher', '--name', 'packed-launcher'], {
    env: { ...env, VECNET_CONFIG: operatorConfig },
  }));
  const launcherEnv = { ...env, VECNET_CONFIG: path.join(temporary, 'launcher.toml') };
  const managerDir = path.join(temporary, 'fake-manager'), managerLog = path.join(temporary, 'manager.log');
  await mkdir(managerDir, { mode: 0o700 });
  if (!windows) await writeFile(path.join(managerDir, 'systemctl'), '#!/bin/sh\nprintf \'%s\\n\' "$*" >> "$LAUNCHER_MANAGER_LOG"\n', { mode: 0o700 });
  launcherEnv.PATH = managerDir + path.delimiter + path.dirname(process.execPath) + path.delimiter + env.PATH;
  launcherEnv.LAUNCHER_MANAGER_LOG = managerLog;
  launcherEnv.VECNET_BIN = path.join(installedRoot, 'native', process.platform + '-' + process.arch, 'vecnet' + (windows ? '.exe' : ''));
  const repo = path.join(temporary, 'launcher repo with spaces');
  execFileSync('git', ['init', '-q', repo], { cwd: temporary, env: launcherEnv });
  execFileSync('git', ['-C', repo, '-c', 'user.name=Package test', '-c', 'user.email=package@example.invalid', 'commit', '--allow-empty', '-qm', 'initial'], { cwd: temporary, env: launcherEnv });
  mustRun(cli, ['enrol', '--hub', base, '--token', '-', '--profile', 'packed-launcher'], { env: launcherEnv, input: added.enrol_token + '\n' });
  mustRun(cli, ['--net', 'packed-launcher', 'launcher', 'setup', '--project', 'packed', '--repo', repo, '--base-ref', 'HEAD', '--write'], { env: launcherEnv });
  const installArgs = ['--net', 'packed-launcher', 'launcher', 'install', '--node', process.execPath];
  const plan = JSON.parse(mustRun(cli, installArgs, { env: launcherEnv }));
  assert.equal(plan.runtime.source, installedRoot, 'installed executable discovers its own package outside the checkout');
  await assert.rejects(readFile(managerLog), { code: 'ENOENT' }, 'preview must not touch the manager');
  let taskName;
  if (windows) {
    taskName = JSON.parse(plan.unit).name;
    assert.match(taskName, /^vecnet-launcher-p-[a-f0-9]{16}$/);
    // Set cleanup before publication to cover partial installations as well.
    cleanupTask = () => {
      const removed = spawnSync('schtasks.exe', ['/Delete', '/TN', taskName, '/F'], { encoding: 'utf8', windowsHide: true });
      assert.equal(removed.status, 0, 'remove disposable launcher task: ' + removed.stderr);
    };
  }
  const writeArgs = [...installArgs, '--write', ...(windows ? [] : ['--enable', '--start'])];
  const installed = JSON.parse(mustRun(cli, writeArgs, { env: launcherEnv }));
  assert.ok(installed.runtime.entry.startsWith(launcherEnv.XDG_DATA_HOME + path.sep));
  if (windows) {
    const state = JSON.parse(mustRun(cli, ['--net', 'packed-launcher', 'launcher', 'status'], { env: launcherEnv }));
    assert.equal(state.manager, 'windows-task-scheduler');
    assert.equal(state.installed, true);
    assert.equal(state.active, 'inactive');
    assert.equal(state.enabled, 'disabled', 'package smoke must not enable a logon task');
  } else assert.match(await readFile(managerLog, 'utf8'), /--user daemon-reload\n--user enable vecnet-launcher-.*\.service\n--user restart vecnet-launcher-.*\.service\n/);
  const unit = await readFile(installed.unit_path, 'utf8');
  if (windows) {
    const task = JSON.parse(unit);
    assert.equal(task.executable, process.execPath);
    assert.ok(task.args.includes(installed.runtime.entry), 'task executes the private installed snapshot');
    assert.equal(task.environment.VECNET_BIN, launcherEnv.VECNET_BIN);
  } else assert.ok(unit.includes(installed.runtime.entry), 'service executes the private installed snapshot');
  // A developer may keep the Node toolchain in the checkout. Only that
  // explicitly selected executable and PATH may refer there, never runtime code.
  const escaped = value => windows ? JSON.stringify(value).slice(1, -1) : value;
  const runtimeUnit = [process.execPath, launcherEnv.PATH].reduce((text, tool) => text.replaceAll(escaped(tool), '<trusted-tool>'), unit);
  assert.ok(!runtimeUnit.includes(escaped(root)), 'manager runtime must not reference the source checkout');
  const builtins = JSON.parse(await readFile(path.join(installedRoot, 'BUILD_INFO.json'), 'utf8')).launcher.backends;
  for (const id of ['t3', 'paseo', 'codex-app-server', 'acp']) assert.ok(builtins.includes(id));
  const graph = execFileSync(process.execPath, ['--disable-warning=ExperimentalWarning', '--input-type=module', '-e',
    'import {pathToFileURL} from "node:url"; import path from "node:path"; const ids=JSON.parse(process.argv[2]); for (const id of ids) { const m=await import(pathToFileURL(path.join(process.argv[1],"connectors/launcher/src/backends",id,"index.ts"))); const plugin=m.default ?? Object.values(m).find(p=>p?.id===id); if(plugin?.id!==id) throw new Error("wrong backend"); } console.log(JSON.stringify(ids));',
    installed.runtime.snapshot, JSON.stringify(builtins)], { cwd: temporary, env: launcherEnv, encoding: 'utf8', timeout: 10000, windowsHide: true });
  assert.deepEqual(JSON.parse(graph), builtins);
  const checked = run(cli, ['--net', 'packed-launcher', 'launcher', 'check'], { env: launcherEnv });
  assert.equal(checked.status, 1, 'missing private Codex endpoint is an unhealthy backend, not a module/config failure');
  if (!/^\s*\{/.test(checked.stdout)) diagnose(cli, checked, { env: launcherEnv });
  assert.match(checked.stdout, /^\s*\{/, 'launcher check must return its backend report: ' + checked.stderr);
  const report = JSON.parse(checked.stdout);
  assert.equal(report.backends.length, 1);
  assert.equal(report.backends[0].id, 'codex-app-server');
  assert.equal(report.backends[0].ok, false);
  assert.deepEqual(report.backends[0].presets, ['default']);
  assert.deepEqual(report.backends[0].permission_modes, ['read-only', 'ask', 'auto', 'full-access']);
  const managerState = () => windows
    ? execFileSync('schtasks.exe', ['/Query', '/TN', taskName, '/XML'], { encoding: 'utf8', windowsHide: true })
    : readFile(managerLog, 'utf8');
  const managerBefore = await managerState(), unitBefore = await readFile(installed.unit_path, 'utf8');
  const changedModule = path.join(installedRoot, 'connectors/launcher/src/backends/codex-app-server/index.ts');
  await writeFile(changedModule, (await readFile(changedModule, 'utf8')) + '\n// changed after packaging\n');
  const refused = run(cli, writeArgs, { env: launcherEnv });
  assert.notEqual(refused.status, 0, 'changed packaged source must not become an installed runtime');
  assert.match(refused.stderr, /packaged launcher asset digest mismatch/);
  assert.equal(await managerState(), managerBefore, 'digest refusal must not change the manager');
  assert.equal(await readFile(installed.unit_path, 'utf8'), unitBefore, 'digest refusal must not publish a unit');
  await stopHub();
  await assert.rejects(fetch(base + '/healthz', { signal: AbortSignal.timeout(2000) }), 'shutdown leaves no native hub listening');
  assert.match(mustRun(hubBin, ['init', '--data-dir', dataDir, '--operator-name', 'npm-smoke']), /already initialised/, 'database is reusable after shutdown');
  completed = true;
});

test('real native wrapper preserves arguments and arbitrary nonzero exit codes', async t => {
  const fixture = await mkdtemp(path.join(os.tmpdir(), 'vecnet npm native child & '));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  execFileSync('tar', ['-xzf', tarball, '-C', fixture]);
  const binary = path.join(fixture, 'package', 'native', process.platform + '-' + process.arch, 'vecnet' + (windows ? '.exe' : ''));
  await copyFile(process.execPath, binary);
  await chmod(binary, 0o755);
  const args = ['a b', 'c&d', 'e|f', '%PATH%', 'a"b', '$(echo no-shell)', 'trailing\\'];
  const result = spawnSync(process.execPath, [path.join(fixture, 'package', 'bin', 'vecnet.cjs'), '-e',
    'console.log(JSON.stringify(process.argv.slice(1))); process.exit(37);', '--', ...args], {
    encoding: 'utf8', timeout: 10000, windowsHide: true,
  });
  assert.equal(result.status, 37, result.stderr || result.error?.message);
  assert.deepEqual(JSON.parse(result.stdout), args);
});

test('wrapper preserves failure when a native process is killed by SIGPIPE', { skip: windows ? 'POSIX signals; Windows process-tree shutdown is tested in the live installation test' : false }, async t => {
  const fixture = await mkdtemp(path.join(os.tmpdir(), 'vecnet-npm-signal-'));
  t.after(() => rm(fixture, { recursive: true, force: true }));
  execFileSync('tar', ['-xzf', tarball, '-C', fixture]);
  const binary = path.join(fixture, 'package', 'native', `${process.platform}-${process.arch}`, 'vecnet');
  await writeFile(binary, '#!/bin/sh\nkill -PIPE "$$"\n', { mode: 0o755 });
  const result = spawnSync(process.execPath, [path.join(fixture, 'package', 'bin', 'vecnet.cjs')], {
    encoding: 'utf8', timeout: 5000,
  });
  assert.ok(result.signal === 'SIGPIPE' || result.status === 141, `unexpected wrapper exit ${result.status}/${result.signal}: ${result.stderr}`);
});


test('npm package includes complete launcher runtime and dynamic builtins', async t => {
  const extracted = await mkdtemp(path.join(os.tmpdir(),'vecnet-npm-launcher-assets-'));
  t.after(()=>rm(extracted,{recursive:true,force:true}));
  execFileSync('tar',['-xzf',tarball,'-C',extracted]);
  const contents = path.join(extracted,'package');
  const metadata = JSON.parse(await readFile(path.join(contents,'BUILD_INFO.json'),'utf8'));
  assert.equal(metadata.launcher?.entry,'connectors/launcher/src/main.ts');
  for (const file of ['src/main.ts','src/daemon.ts','src/plugins.ts','src/backends/t3/index.ts','src/backends/paseo/index.ts','src/backends/codex-app-server/index.ts','src/backends/acp/index.ts','systemd/vecnet-launcher-profile.service.in']) {
    const key='connectors/launcher/'+file;
    assert.ok(metadata.launcher.assets[key],'missing launcher asset '+key);
    assert.deepEqual(await readFile(path.join(contents,key)),await readFile(path.join(root,key)),'exact shipped source '+key);
  }
  for (const [file,digest] of Object.entries(metadata.launcher.assets)) {
    assert.equal(createHash('sha256').update(await readFile(path.join(contents,file))).digest('hex'),digest,file);
  }
  const packed = execFileSync('tar',['-tzf',tarball],{encoding:'utf8'}).trim().split(/\r?\n/);
  const runtime = packed.filter(file=>file.startsWith('package/connectors/')).map(file=>file.slice('package/'.length)).sort();
  assert.deepEqual(runtime,[...new Set([...Object.keys(metadata.launcher.assets)])].sort(),'every runtime artifact has recorded provenance');
});
