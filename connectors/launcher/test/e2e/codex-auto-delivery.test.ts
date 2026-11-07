// Real native CLI/owner, real Node, real temporary hub and stable SQLite.
// Only Codex is simulated. Requires prebuilt bin/vecnet{,-5c-b} and vecnet-hub.
// Run only through the serialized, explicitly permitted x-boss build lane.
import test, { describe } from 'node:test';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync,
  realpathSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { DatabaseSync } from 'node:sqlite';
import { startRealHub } from '../../../hub-client/testing/real-hub.ts';
import type { RealHub } from '../../../hub-client/testing/real-hub.ts';
import type { SendResponse, VecnetEvent } from '../../../hub-client/src/wire.ts';
import { startFakeCodexAppServer } from '../fake-codex-appserver.ts';
import type { FakeCodex } from '../fake-codex-appserver.ts';
import type { InflightRow } from '../../src/codex/engine.ts';

const BIN = fileURLToPath(new URL('../../../../bin/vecnet', import.meta.url));
const NATIVE_B = fileURLToPath(new URL('../../../../bin/vecnet-5c-b', import.meta.url));
const HUB_BIN = fileURLToPath(new URL('../../../../bin/vecnet-hub', import.meta.url));
const ENTRY = fileURLToPath(new URL('../../src/codex/delivery-main.ts', import.meta.url));
const CONNECTORS = fileURLToPath(new URL('../../../', import.meta.url));
const WAIT = 30_000;
const A = '00000000-0000-4000-8000-000000000001';
const B = '00000000-0000-4000-8000-000000000002';
const pause = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms));
const hash = (data: string | Buffer) => createHash('sha256').update(data).digest('hex');
const sh = (value: string) => "'" + value.replaceAll("'", "'\\''") + "'";
type Peer = { peerId: string; secret: string };
type Profile = Peer & { name: string; hub: RealHub; config: string };
type Owner = { pid: number; birth: string; hub: string; state_root: string; registry: string;
  binary_digest: string; launcher: string; managed: boolean };
type Scope = { profile: string; as?: string; hub: string; caller: string; env: Record<string, string> };
type Attempt = Scope & { row: InflightRow };
type Settlement = Attempt & { origin: 'receipt' | 'history'; attempt: Attempt };
type HookState = { hub?: string; caller?: string; as?: string; displayed?: string[];
  claims?: { ID: string; Claim: string }[]; replay?: { ID: string }[]; ack_after?: string; app_cursor?: string };
type Trace = { kind: string; pid: number; birth: string; argv?: string[]; config?: string;
  child?: number; childBirth?: string; lease?: string; key?: string; claim?: string;
  ids?: string[]; released?: number; proof?: boolean; point?: string; fd3?: boolean };
type Control = { importBarrier?: boolean; loseReceipt?: boolean; holdOrdinary?: string;
  pauseAttemptDelete?: boolean; pauseReleasingDelete?: boolean };
type Process = { child: ChildProcess; done: Promise<{ code: number | null; signal: NodeJS.Signals | null }>;
  stdout(): string; stop(): Promise<void> };

function privateDir(path: string): string { mkdirSync(path, { recursive: true, mode: 0o700 }); chmodSync(path, 0o700); return path; }
function privateFile(path: string, data: string, mode = 0o600) {
  writeFileSync(path, data, { mode }); chmodSync(path, mode);
}
function json<T>(path: string): T { return JSON.parse(readFileSync(path, 'utf8')) as T; }
function atomic(path: string, value: unknown) {
  const temporary = path + '.' + randomUUID(); privateFile(temporary, JSON.stringify(value)); renameSync(temporary, path);
}
function inode(path: string): string { const s = statSync(path, { bigint: true }); return `${s.dev}:${s.ino}`; }
function birth(pid: number): string | undefined {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, 'utf8');
    const fields = stat.slice(stat.lastIndexOf(')') + 2).split(' ');
    return fields[0] === 'Z' || fields[0] === 'X' ? undefined : fields[19];
  } catch { return undefined; }
}
async function until<T>(read: () => T | undefined | false, description: string, timeout = WAIT): Promise<T> {
  const deadline = performance.now() + timeout;
  while (performance.now() < deadline) {
    const value = read(); if (value !== undefined && value !== false) return value;
    await pause(20);
  }
  throw new Error(`fixture deadline: ${description}`);
}
function track(binary: string, argv: string[], env: NodeJS.ProcessEnv, cwd: string, timeout = 0): Process {
  const child = spawn(binary, argv, { cwd, env, stdio: ['ignore', 'pipe', 'pipe'],
    ...(timeout ? { timeout, killSignal: 'SIGKILL' as const } : {}) });
  let stdout = '', length = 0, tooLarge = false;
  child.stdout!.setEncoding('utf8').on('data', (part: string) => {
    length += Buffer.byteLength(part);
    if (length > 1_048_576) { tooLarge = true; child.kill('SIGKILL'); } else stdout += part;
  });
  // No raw stderr or stdout is used in failure messages: profiles and peer text
  // are private, including when a child unexpectedly emits its input.
  child.stderr!.on('data', () => {});
  const done = new Promise<{ code: number | null; signal: NodeJS.Signals | null }>((resolve, reject) => {
    child.once('error', () => reject(new Error('fixture child spawn failed')));
    child.once('close', (code, signal) => tooLarge ? reject(new Error('fixture output limit')) : resolve({ code, signal }));
  });
  return { child, done, stdout: () => stdout, async stop() {
    if (child.exitCode === null && child.signalCode === null) { child.kill('SIGCONT'); child.kill('SIGTERM'); }
    const timer = setTimeout(() => child.kill('SIGKILL'), 3000);
    try { await done; } finally { clearTimeout(timer); }
  } };
}

// pidfd is opened BEFORE checking birth/ownership. No PID-only signaling of a
// detached owner or orphan is permitted. This Linux fixture uses system Python
// stdlib only; no package or product runtime dependency is added.
const PIDFD_SIGNAL = `import os, signal, sys
pid, expected, sig, root = int(sys.argv[1]), sys.argv[2], int(sys.argv[3]), sys.argv[4]
try: fd = os.pidfd_open(pid)
except ProcessLookupError: sys.exit(0)
try:
    raw = open('/proc/%d/stat' % pid).read()
    fields = raw[raw.rfind(')')+2:].split()
    if fields[0] in ('Z','X'): sys.exit(0)
    if fields[19] != expected or os.stat('/proc/%d' % pid).st_uid != os.geteuid(): sys.exit(3)
    cwd = os.readlink('/proc/%d/cwd' % pid)
    if cwd != root and not cwd.startswith(root + '/'): sys.exit(4)
    signal.pidfd_send_signal(fd, sig)
finally: os.close(fd)
`;

// Generated per test, loaded by real Node before the unchanged delivery entry.
// Real Go is ALWAYS spawned with original argv/env/stdio (including FD3).
// Faults affect only receipt/release confirmation, or pause after actual KV I/O.
function preload(root: string): string {
  return `import cp from 'node:child_process';
import fs from 'node:fs';
import { dirname, join } from 'node:path';
import { PassThrough } from 'node:stream';
import { syncBuiltinESMExports } from 'node:module';
import { DatabaseSync } from 'node:sqlite';
const root = ${JSON.stringify(root)}, control = join(root, 'control.json'), log = join(root, 'trace.jsonl');
const read = p => { try { return JSON.parse(fs.readFileSync(p, 'utf8')); } catch { return {}; } };
const birth = p => { try { const s=fs.readFileSync('/proc/'+p+'/stat','utf8'); return s.slice(s.lastIndexOf(')')+2).split(' ')[19]; } catch {} };
const emit = value => fs.appendFileSync(log, JSON.stringify({ pid: process.pid, birth: birth(process.pid), ...value })+'\\n', { mode: 0o600 });
const arg = (a, k) => a[a.indexOf(k)+1];
const flag = (name, value) => fs.writeFileSync(join(root, name), JSON.stringify(value), { mode: 0o600 });
function gate(point) {
  emit({kind:'barrier',point}); const path=join(root,'resume-'+point);
  const deadline=Date.now()+20000, cell=new Int32Array(new SharedArrayBuffer(4));
  while (!fs.existsSync(path)) {
    if (fs.existsSync(join(root,'kill-'+process.pid))) process.kill(process.pid,'SIGKILL');
    if (Date.now()>deadline) process.exit(81);
    Atomics.wait(cell,0,0,10);
  }
}
let lease=''; try { const s=fs.fstatSync(3); lease=s.dev+':'+s.ino; } catch {}
emit({kind:'runner',lease});
if(read(control).importBarrier) gate('import');
const prepare=DatabaseSync.prototype.prepare;
DatabaseSync.prototype.prepare=function(sql,...rest) {
  const stmt=prepare.call(this,sql,...rest);
  if(sql==='DELETE FROM kv WHERE ns = ? AND key = ?') {
    const run=stmt.run.bind(stmt);
    stmt.run=(...args)=>{const result=run(...args), c=read(control), key=String(args[1]);
      if(c.pauseAttemptDelete && key.includes(':attempt:') && !fs.existsSync(join(root,'attempt-paused'))) {
        flag('attempt-paused',{key}); gate('attempt-delete');
      }
      if(c.pauseReleasingDelete && key.includes(':releasing:') && !fs.existsSync(join(root,'release-paused'))) {
        flag('release-paused',{key}); gate('releasing-delete');
      }
      return result;
    };
  }
  return stmt;
};
const spawn=cp.spawn;
cp.spawn=function(file,args,options) {
  const child=spawn.call(this,file,args,options), c=read(control), isHook=args.includes('hook');
  let fd3=false;
  try { const a=fs.statSync('/proc/'+child.pid+'/fd/3'), b=fs.fstatSync(3); fd3=a.dev===b.dev&&a.ino===b.ino; } catch {}
  const scope={argv:args,config:options?.env?.VECNET_CONFIG,child:child.pid,childBirth:birth(child.pid),fd3};
  emit({kind:isHook?'hook':'watch',...scope});
  if(!isHook) return child;
  if(!args.includes('--release')) {
    const chunks=[]; child.stdout.on('data',b=>chunks.push(Buffer.from(b)));
    child.once('close',()=>{let out;try{out=JSON.parse(Buffer.concat(chunks).toString());}catch{}
      emit({kind:'claim-confirmation',...scope,claim:arg(args,'--claim'),ids:out?.deliveries});});
    return child;
  }
  const source=child.stdout, replacement=new PassThrough(), chunks=[];
  Object.defineProperty(child,'stdout',{value:replacement});
  source.on('data',b=>chunks.push(Buffer.from(b)));
  const receipt=args.includes('--receipt-key'), nonce=arg(args,'--claim');
  child.once('close',code=>{
    let out; try {out=JSON.parse(Buffer.concat(chunks).toString());}catch{}
    const config=scope.config, profile=arg(args,'--net'), as=args.includes('--as')?arg(args,'--as'):'';
    const state=read(join(dirname(config),'state',profile+(as?'@'+as:'')+'.json'));
    const proof=receipt&&out?.lifecycle==='receipt-display-v1'&&out?.receipt_key===arg(args,'--receipt-key')&&
      Array.isArray(out.deliveries)&&out.deliveries.every(id=>state.displayed?.includes(id));
    emit({kind:receipt?'receipt-confirmation':'ordinary-confirmation',...scope,claim:nonce,
      ...(receipt?{key:arg(args,'--receipt-key'),ids:out?.deliveries,proof}:{}),released:out?.released});
    const fresh=read(control), lose=receipt&&fresh.loseReceipt&&!fs.existsSync(join(root,'receipt-lost'));
    if(lose) {
      if(code!==0||!proof) process.exit(82);
      flag('receipt-lost',{key:out.receipt_key,claim:nonce,ids:out.deliveries,proof});
      gate('receipt-loss');
    }
    if(!lose && !(fresh.holdOrdinary===nonce && !receipt)) replacement.write(Buffer.concat(chunks));
    replacement.end();
  });
  return child;
};
syncBuiltinESMExports();
`;
}

async function fixture(t: TestContext, options: { hubs?: number; cap?: number } = {}) {
  // Fail on missing prerequisites; never skip an acceptance scenario as PASS.
  for (const path of [BIN, HUB_BIN, ENTRY]) assert.ok(existsSync(path), 'prebuilt acceptance asset is required');
  // The physical build-box TMPDIR is long; keep the Unix rendezvous below
  // sockaddr_un's pathname limit without using a shared /tmp fallback.
  const root = mkdtempSync(join(tmpdir(), 'a-')); chmodSync(root, 0o700);
  const dirs = Object.fromEntries(['home', 'cfg', 'cache', 'data', 'run', 'tmp', 'shims'].map(n => [n, privateDir(join(root, n))]));
  const env: NodeJS.ProcessEnv = { HOME: dirs.home, XDG_CONFIG_HOME: dirs.cfg, XDG_CACHE_HOME: dirs.cache,
    XDG_DATA_HOME: dirs.data, XDG_RUNTIME_DIR: dirs.run, TMPDIR: dirs.tmp,
    PATH: dirs.shims + ':/usr/bin:/bin', LANG: 'C.UTF-8', VECNET_BIN: BIN,
    VECNET_CODEX_DELIVERY_ENTRY: ENTRY, VECNET_CODEX_AUTO_DELIVERY: '0',
    ...(options.cap ? { VECNET_CODEX_MAX_BINDINGS: String(options.cap) } : {}) };
  const hubs: (RealHub & { database: string })[] = [], children: Process[] = [];
  let fake: FakeCodex | undefined;
  const seenOwners = new Map<number, Owner>();
  const tracePath = join(root, 'trace.jsonl'), controlPath = join(root, 'control.json');
  privateFile(tracePath, ''); atomic(controlPath, {});
  const trace = (): Trace[] => readFileSync(tracePath, 'utf8').split('\n').filter(Boolean).map(x => JSON.parse(x) as Trace);
  const process = (binary: string, argv: string[], childEnv = env, timeout = 0) => {
    const p = track(binary, argv, childEnv, root, timeout); children.push(p); return p;
  };
  const signal = async (pid: number, savedBirth: string, sig: 9 | 15 | 18 | 19) => {
    if (birth(pid) !== savedBirth) return;
    const p = process('/usr/bin/python3', ['-c', PIDFD_SIGNAL, String(pid), savedBirth, String(sig), root], env, 5000);
    assert.equal((await p.done).code, 0, 'pidfd refused unsafe fixture signal');
  };
  const paths = (hub: RealHub) => {
    const slug = hash(hub.url.replace(/\/$/, '')), state = join(dirs.data, 'vecnet', 'codex-delivery', slug);
    return { state, ledger: join(state, 'ledger.sqlite'), registry: join(dirs.data, 'vecnet', 'codex-delivery', 'registry.json'),
      lease: join(dirs.run, 'vecnet', 'delivery-' + slug + '.lock'),
      owner: join(dirs.run, 'vecnet', 'delivery-' + slug + '.json'),
      start: join(dirs.run, 'vecnet', 'start-' + slug + '.lock') };
  };
  const owner = (hub: RealHub): Owner | undefined => {
    const p = paths(hub);
    if (!existsSync(p.owner)) return;
    const value = json<Owner>(p.owner);
    if (birth(value.pid) !== value.birth) return;
    if (value.hub !== hub.url || value.state_root !== dirname(p.state) || value.registry !== p.registry || value.managed) return;
    try {
      if (inode(`/proc/${value.pid}/fd/3`) !== inode(p.lease) ||
        !/^lock:\s+\d+: FLOCK\s+ADVISORY\s+WRITE\s/m.test(readFileSync(`/proc/${value.pid}/fdinfo/3`, 'utf8'))) return;
    } catch { return; }
    seenOwners.set(value.pid, value); return value;
  };
  const stopOwner = async (hub: RealHub, sig: 9 | 15 = 15) => {
    const held = owner(hub); if (!held) return;
    await signal(held.pid, held.birth, sig);
    await until(() => birth(held.pid) !== held.birth, 'verified owner exit');
  };
  t.after(async () => {
    const failures: string[] = [];
    // Never discard a root while a sender or inherited lease child remains.
    for (const hub of hubs) { try { await stopOwner(hub, 9); } catch { failures.push('owner stop'); } }
    for (const saved of seenOwners.values()) {
      try { await signal(saved.pid, saved.birth, 9); } catch { failures.push('owner reap'); }
    }
    for (const row of trace().filter(x => x.child && x.childBirth)) {
      try { await signal(row.child!, row.childBirth!, 9); } catch { failures.push('bounded/watch child reap'); }
    }
    for (const p of children) { try { await p.stop(); } catch { failures.push('direct child reap'); } }
    try { await fake?.close(); } catch { failures.push('fake close'); }
    for (const hub of hubs) { try { await hub.stop(); } catch { failures.push('hub stop'); } }
    if (!failures.length) rmSync(root, { recursive: true, force: true });
    assert.equal(failures.length, 0, 'owned fixture cleanup failed; private root retained');
  });
  // No real manager executable is ever delegated to, including unsupported calls.
  privateFile(join(dirs.shims, 'systemctl'), '#!/bin/sh\ncase "$*" in *show*--value*) echo not-found; exit 0;; *) exit 1;; esac\n', 0o700);
  const preloadPath = join(root, 'preload.mjs'); privateFile(preloadPath, preload(root));
  privateFile(join(dirs.shims, 'node'), `#!/bin/sh\nexport XDG_CACHE_HOME=${sh(dirs.cache)} TMPDIR=${sh(dirs.tmp)}\nfor arg in "$@"; do\n if [ "$arg" = run ]; then exec ${sh(realpathSync(globalThis.process.execPath))} --import ${sh(preloadPath)} "$@"; fi\ndone\nexec ${sh(realpathSync(globalThis.process.execPath))} "$@"\n`, 0o700);
  fake = await startFakeCodexAppServer({ dir: join(root, 'f') });
  env.CODEX_HOME = join(root, 'f', 'home');
  assert.ok(Buffer.byteLength(fake.socketPath) < 108, 'private Unix socket pathname exceeds the Linux limit');
  for (const id of [A, B]) { fake.setThread({ id, status: { type: 'idle' }, approvalPolicy: 'never' }); fake.attachOther(id); }
  for (let i = 0; i < (options.hubs ?? 1); i++) {
    // real-hub snapshots process.env, stripping only VECNET_*. Install an
    // entirely private environment during its serialized startup, then restore.
    const saved = { ...globalThis.process.env };
    const before = new Set(readdirSync(dirs.tmp));
    try {
      for (const key of Object.keys(globalThis.process.env)) delete globalThis.process.env[key];
      Object.assign(globalThis.process.env, env);
      const real = await startRealHub({ bin: HUB_BIN }).catch(() => { throw new Error('private real hub startup failed'); });
      const created = readdirSync(dirs.tmp).filter(n => !before.has(n) && n.startsWith('vecnet-real-hub-'));
      assert.equal(created.length, 1, 'real hub must use private TMPDIR');
      hubs.push(Object.assign(real, { database: join(dirs.tmp, created[0], 'hub.db') }));
    } finally {
      for (const key of Object.keys(globalThis.process.env)) delete globalThis.process.env[key];
      Object.assign(globalThis.process.env, saved);
    }
  }
  const profiles: Profile[] = [];
  const config = join(dirs.cfg, 'config.toml');
  const add = async (name: string, hub = hubs[0], configPath = config) => {
    const peer = await hub.addPeer('session', 'auto-process-' + name).catch(() => { throw new Error('private peer enrollment failed'); });
    const p = { name, hub, config: configPath, ...peer }; profiles.push(p); return p;
  };
  const writeConfigs = () => {
    for (const path of new Set(profiles.map(p => p.config))) {
      privateDir(dirname(path));
      // Keep native-persisted hub pins and all other fields. Rotation changes
      // only the existing secret line; new fixture profiles append a section.
      let text = existsSync(path) ? readFileSync(path, 'utf8') : '';
      for (const p of profiles.filter(p => p.config === path)) {
        const section = new RegExp('(^\\[profiles\\.' + p.name + '\\]\\r?\\n)([\\s\\S]*?)(?=^\\[|$(?![\\s\\S]))', 'm');
        if (section.test(text)) text = text.replace(section, (_all, header: string, fields: string) =>
          header + fields.replace(/^secret\s*=.*$/m, () => 'secret = ' + JSON.stringify(p.secret)));
        else text += `\n[profiles.${p.name}]\nhub = ${JSON.stringify(p.hub.url)}\npeer_id = ${JSON.stringify(p.peerId)}\nsecret = ${JSON.stringify(p.secret)}\n`;
      }
      const temporary = path + '.' + randomUUID(); privateFile(temporary, text); renameSync(temporary, path);
    }
    // The explicitly older-overlap fixture aliases the SAME actual receiver
    // through a new source/profile. Give that alias the exact real-Go replay
    // checkpoint already produced by N1's ordinary release, so its N2 is a
    // fresh real claim of E1 rather than an unrelated initial backlog summary.
    // No claim, receipt, key, nonce or accepted row is invented here.
    for (const p of profiles) {
      const target = join(dirname(p.config), 'state', p.name + '.json');
      if (existsSync(target)) continue;
      const original = profiles.find(other => other !== p && other.peerId === p.peerId && other.hub === p.hub &&
        existsSync(join(dirname(other.config), 'state', other.name + '.json')));
      if (!original) continue;
      const source = join(dirname(original.config), 'state', original.name + '.json'), saved = json<HookState>(source);
      assert.equal(saved.hub, p.hub.url); assert.equal(saved.caller, p.peerId);
      privateDir(dirname(target)); privateFile(target, readFileSync(source, 'utf8'));
    }
  };
  const a = await add('a'), b = await add('b'), sender = await add('sender'); writeConfigs();
  const call = async (p: Profile, argv: string[], extra: NodeJS.ProcessEnv = {}, binary = BIN) => {
    const child = process(binary, ['--net', p.name, ...argv], { ...env, VECNET_CONFIG: p.config, ...extra }, 15000);
    const result = await child.done; return { ...result, stdout: child.stdout() };
  };
  // threads is an ordinary eligible action with stable empty output in a fresh
  // fixture. peers JSON includes last_seen/reachability changed by real HTTP
  // authentication and SSE; comparing those independent responses would test
  // wall-clock observations instead of the CLI's output preservation contract.
  const action = (p: Profile, thread = A, binary = BIN, entry = ENTRY) => call(p, ['--json', 'threads'], {
    CODEX_THREAD_ID: thread, VECNET_CODEX_AUTO_DELIVERY: '1', VECNET_BIN: binary, VECNET_CODEX_DELIVERY_ENTRY: entry }, binary);
  const register = async (p = a, thread = A) => {
    assert.equal((await action(p, thread)).code, 0, 'first eligible action failed');
    await until(() => owner(p.hub), 'automatic lifetime owner');
    await until(() => fake!.subscribers(thread).length > 0, 'automatic thread subscription');
    // Subscription precedes the first bounded claim. Wait for its real empty
    // backlog bootstrap before a test sends the fresh eligible delivery.
    await until(() => state(p).app_cursor, 'real initial app-server cursor');
  };
  const control = (value: Control) => atomic(controlPath, value);
  const barrier = (point: string) => until(() => trace().find(x => x.kind === 'barrier' && x.point === point), point);
  const resume = (point: string) => privateFile(join(root, 'resume-' + point), '1');
  const inspect = <T>(path: string, fn: (db: DatabaseSync) => T): T => {
    const db = new DatabaseSync(path, { readOnly: true }); try { db.exec('PRAGMA busy_timeout=1000'); return fn(db); } finally { db.close(); }
  };
  const rows = (hub: RealHub = hubs[0]): Map<string, string> => {
    const path = paths(hub).ledger; if (!existsSync(path)) return new Map();
    const prefix = 'hub:' + JSON.stringify(hub.url) + ':';
    return inspect(path, db => new Map(db.prepare('SELECT key,value FROM kv WHERE ns = ?').all('codex')
      .filter(r => String(r.key).startsWith(prefix)).map(r => [String(r.key).slice(prefix.length), String(r.value)])));
  };
  const inflight = (hub: RealHub = hubs[0]): InflightRow[] => [...rows(hub)].filter(([key]) => key.startsWith('inflight:')).map(([, value]) => JSON.parse(value) as InflightRow);
  const state = (p: Profile = a): HookState => {
    const path = join(dirname(p.config), 'state', p.name + '.json'); return existsSync(path) ? json<HookState>(path) : {};
  };
  const http = async <T>(p: Peer, hub: RealHub, path: string, body?: unknown, method = body === undefined ? 'GET' : 'POST'): Promise<T> => {
    const response = await fetch(new URL(path, hub.url), { method,
      headers: { Authorization: 'Bearer ' + p.secret, 'Content-Type': 'application/json', Accept: 'application/json' },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: AbortSignal.timeout(5000), redirect: 'error' })
      .catch(() => { throw new Error('private hub transport failed'); });
    if (!response.ok) throw new Error('private hub HTTP status ' + response.status);
    return response.status === 204 ? undefined as T : await response.json() as T;
  };
  const wake = async (p = a, priority: 'wake' | 'hold' = 'wake', text = 'fixture-' + randomUUID()) => {
    // Monitor-only fixtures have no runner to do the ordinary initial backlog
    // bootstrap. Establish an empty head through actual Go, never by editing
    // shared state. This neither binds a session nor publishes display proof.
    if (!state(p).app_cursor) {
      const initial = await call(p, ['hook', '--event', 'app-server', '--claim', 'bootstrap.' + randomUUID(),
        '--priorities', 'wake,wake_idle', '--expect-hub', p.hub.url, '--expect-caller', p.peerId]);
      assert.equal(initial.code, 0, 'real empty cursor bootstrap failed');
      assert.ok(state(p).app_cursor, 'real Go did not persist its initial app-server cursor');
    }
    const from = profiles.find(x => x.name === 'sender' && x.hub === p.hub)!;
    const response = await http<SendResponse>(from, p.hub, '/v1/messages', {
      idempotency_key: randomUUID(), recipients: [p.peerId], kind: 'message', priority,
      content: { text }, expires_at: new Date(Date.now() + 3_600_000).toISOString() });
    assert.equal(response.deliveries.length, 1); return { id: response.deliveries[0].delivery_id, text };
  };
  const turns = () => fake!.requests.filter(r => r.method === 'turn/start' || r.method === 'turn/steer');
  const keyFor = (hub: RealHub, thread: string, ids: string[]) => 'vn.' + hash(JSON.stringify([hub.url, thread, [...ids].sort()])).slice(0, 24);
  const refresh = async (hub = hubs[0], binary = BIN, entry = ENTRY) => {
    const child = process(binary, ['codex', 'delivery-refresh', '--hub', hub.url], {
      ...env, VECNET_BIN: binary, VECNET_CODEX_DELIVERY_ENTRY: entry }, 15000);
    assert.equal((await child.done).code, 0, 'real native refresh failed');
    return await until(() => { const held = owner(hub); return held?.binary_digest === hash(readFileSync(binary)) && held.launcher === entry ? held : undefined; }, 'replacement generation');
  };
  const waitReceipt = async (id: string, p: Profile = a, timeout = WAIT) => {
    await until(() => state(p).displayed?.includes(id), 'real Go Displayed commit', timeout);
    await until(() => !inflight(p.hub).some(r => r.deliveries.includes(id)) && ![...rows(p.hub)].some(([key, value]) =>
      key.startsWith('settling:') && (JSON.parse(value) as Settlement).row.deliveries.includes(id)), 'exact settlement cleanup');
  };
  const armDrop = () => {
    const control = (fake as FakeCodex & { acceptThenDropNextTurn?: () => void }).acceptThenDropNextTurn;
    assert.equal(typeof control, 'function', 'R17 accepted-then-response-loss control is required');
    control!.call(fake);
  };
  const bindings = (p = a) => json<{ version: number; bindings: { thread_id: string; profile: string; caller: string;
    hub: string; codex_home: string; as: string }[]; disabled?: { profile: string; as: string }[] }>(join(dirname(p.config), 'codex', 'bindings.json'));
  return { root, dirs, env, hubs, fake, a, b, sender, profiles, add, writeConfigs, call, action, register, process, signal,
    paths, owner, stopOwner, rows, inflight, state, http, wake, turns, keyFor, refresh, waitReceipt, armDrop, bindings,
    trace, control, barrier, resume, inspect };
}

type Fixture = Awaited<ReturnType<typeof fixture>>;
function assertScope(actual: Attempt | Settlement, original: Attempt) {
  assert.equal(actual.profile, original.profile); assert.equal(actual.as, original.as);
  assert.equal(actual.hub, original.hub); assert.equal(actual.caller, original.caller);
  assert.deepEqual(actual.env, original.env);
  assert.equal(actual.row.threadId, original.row.threadId); assert.equal(actual.row.key, original.row.key);
  assert.equal(actual.row.claim, original.row.claim); assert.deepEqual(actual.row.deliveries, original.row.deliveries);
}
function receiptTrace(f: Fixture, key: string) { return f.trace().filter(x => x.kind === 'receipt-confirmation' && x.key === key); }
function assertOriginalChild(row: Trace, attempt: Attempt) {
  assert.equal(row.config, attempt.env.VECNET_CONFIG);
  assert.equal(row.argv?.[row.argv.indexOf('--net') + 1], attempt.profile);
  const as = row.argv?.includes('--as') ? row.argv[row.argv.indexOf('--as') + 1] : undefined;
  assert.equal(as, attempt.as); assert.equal(row.claim, attempt.row.claim);
}

// R38 explicitly requires recovery of overlap written by an older owner.
// Current admission correctly prevents creating it. Lift/restore only the
// exact retained N1 row while NO owner exists and a real helper exclusively
// holds the SAME lifetime lock. N2 itself is created by real normal admission.
// This is supporting persisted-state construction, not a production migration,
// assertion bypass, invented key, or fabricated Go/receipt result.
async function recoveryFixtureRows(f: Fixture, changes: { key: string; value: string | null }[]) {
  const hub = f.hubs[0], paths = f.paths(hub); assert.equal(f.owner(hub), undefined);
  assert.ok(changes.every(x => x.key.startsWith('releasing:')), 'only known old release fixtures may be lifted/restored');
  const payload = join(f.root, 'persisted-overlap.json'); atomic(payload, changes);
  const source = `import { DatabaseSync } from 'node:sqlite'; import fs from 'node:fs';
const [path,payload,hub]=process.argv.slice(1);let db;
try{db=new DatabaseSync(path);db.exec('BEGIN IMMEDIATE');for(const {key,value} of JSON.parse(fs.readFileSync(payload,'utf8'))){
 const full='hub:'+JSON.stringify(hub)+':'+key;
 if(value===null)db.prepare('DELETE FROM kv WHERE ns = ? AND key = ?').run('codex',full);
 else db.prepare('INSERT INTO kv VALUES (?, ?, ?) ON CONFLICT(ns,key) DO UPDATE SET value=excluded.value').run('codex',full,value);
}db.exec('COMMIT');}catch(e){process.stdout.write(JSON.stringify({fixtureError:e.code??e.name}));process.exitCode=72;}finally{db?.close();}`;
  const child = f.process('/usr/bin/flock', ['-w', '3', '-x', '-E', '73', paths.lease, realpathSync(globalThis.process.execPath),
    '--input-type=module', '-e', source, paths.ledger, payload, hub.url], f.env, 5000);
  assert.equal((await child.done).code, 0, 'persisted recovery fixture requires exclusive original lease; typed helper result: ' + child.stdout());
}

async function interruptedOrdinaryCleanup(t: TestContext) {
  const f = await fixture(t); await f.register(); f.control({ pauseAttemptDelete: true });
  f.fake!.failNext('turn/start', { code: -32602, message: 'fixture invalid input' });
  const sent = await f.wake(); await f.barrier('attempt-delete');
  const found = [...f.rows()].find(([key]) => key.startsWith('releasing:')); assert.ok(found);
  const releasing = found[1], attempt = (JSON.parse(releasing) as InflightRow & { attempt: Attempt }).attempt;
  const original = attempt.row;
  assert.deepEqual(original.deliveries, [sent.id]); assert.equal(original.key, f.keyFor(f.hubs[0], A, [sent.id]));
  assert.equal(f.rows().get('inflight:' + original.key), undefined);
  assert.equal(f.rows().get('attempt:' + original.key), undefined);
  assert.equal(f.state().claims?.some(x => x.Claim === original.claim) ?? false, false, 'real Go ordinary release did not commit');
  assert.equal(f.state().displayed?.includes(sent.id) ?? false, false, 'ordinary release fabricated display proof');
  const confirmations = f.trace().filter(x => x.kind === 'ordinary-confirmation' && x.claim === original.claim);
  assert.ok(confirmations.some(x => x.released === 1));
  for (const row of confirmations) assertOriginalChild(row, attempt);
  await f.stopOwner(f.hubs[0], 9);
  assert.equal(f.rows().get('releasing:' + original.claim), releasing, 'hard kill lost final ordinary authority');
  return { f, sent, original, attempt, releasing };
}

describe('actual native automatic Codex delivery', { concurrency: false }, () => {
  test('automatic delivery starts on first action without bind', { timeout: 120000 }, async t => {
    const f = await fixture(t);
    const off = await f.call(f.a, ['--json', 'threads']);
    assert.equal(off.code, 0); assert.equal(existsSync(join(f.dirs.cfg, 'codex', 'bindings.json')), false);
    const enabled = await f.action(f.a);
    assert.equal(enabled.code, off.code); assert.equal(enabled.signal, off.signal);
    assert.equal(hash(enabled.stdout), hash(off.stdout), 'ordinary stdout changed');
    const held = await until(() => f.owner(f.hubs[0]), 'one automatic owner');
    await until(() => f.fake!.subscribers(A).length === 1, 'one automatic subscription');
    const binding = f.bindings().bindings[0];
    assert.equal(binding.thread_id, A); assert.equal(binding.caller, f.a.peerId);
    assert.equal(binding.codex_home, realpathSync(f.env.CODEX_HOME!)); assert.equal(binding.hub, f.hubs[0].url);
    assert.equal(held.binary_digest, hash(readFileSync(BIN))); assert.equal(held.launcher, ENTRY);
    assert.equal(f.trace().filter(x => x.kind === 'runner').length, 1);
  });

  test('concurrent first actions share one lifetime owner and preserve both results', { timeout: 120000 }, async t => {
    const f = await fixture(t), off = await f.call(f.a, ['--json', 'threads']);
    const results = await Promise.all([f.action(f.a), f.action(f.a)]);
    for (const result of results) { assert.equal(result.code, off.code); assert.equal(hash(result.stdout), hash(off.stdout)); }
    await until(() => f.owner(f.hubs[0]) && f.fake!.subscribers(A).length === 1, 'concurrent registration');
    await pause(1000);
    assert.equal(f.trace().filter(x => x.kind === 'runner').length, 1, 'first actions started competing lifetime owners');
    assert.equal(f.bindings().bindings.length, 1);
    assert.equal(json<{ sources: unknown[] }>(f.paths(f.hubs[0]).registry).sources.length, 1);
  });

  test('automatic delivery shares shown claims with hooks', { timeout: 120000 }, async t => {
    const f = await fixture(t); await f.register();
    const sent = await f.wake(); await f.waitReceipt(sent.id);
    const key = f.keyFor(f.hubs[0], A, [sent.id]);
    assert.equal(f.turns().filter(r => (r.params as { clientUserMessageId: string }).clientUserMessageId === key).length, 1);
    const confirmations = receiptTrace(f, key); assert.ok(confirmations.length); assert.ok(confirmations.every(r => r.proof));
    const hook = await f.call(f.a, ['hook', '--event', 'user-prompt']);
    assert.equal(hook.code, 0); assert.equal(hook.stdout.length, 0, 'duplicate hook rendered a delivery');
    const state = f.inspect(f.hubs[0].database, db => db.prepare('SELECT state FROM deliveries WHERE dly_id = ?').get(sent.id)?.state);
    assert.notEqual(state, 'accepted', 'receipt publication unexpectedly acknowledged the inbox');
    assert.equal(f.turns().length, 1);
  });

  for (const generation of ['native', 'launcher'] as const) {
    test(`automatic delivery recovers an ambiguous send across replacement (${generation})`, { timeout: 150000 }, async t => {
      const f = await fixture(t); await f.register();
      f.fake!.withholdNextItem(); f.armDrop();
      const sent = await f.wake();
      const original = await until(() => f.inflight().find(r => r.deliveries.includes(sent.id) && r.state === 'unresolved'), 'accepted lost-response inflight');
      const companion = f.rows().get('attempt:' + original.key); assert.ok(companion);
      const attempt = JSON.parse(companion) as Attempt;
      assert.equal(attempt.row.key, f.keyFor(f.hubs[0], A, [sent.id]));
      assert.equal(f.state().displayed?.includes(sent.id) ?? false, false);
      const path = f.paths(f.hubs[0]), originalLease = inode(path.lease), originalLedger = inode(path.ledger), originalStart = inode(path.start);
      const prior = f.owner(f.hubs[0])!;
      let binary = BIN, entry = ENTRY;
      if (generation === 'native') {
        assert.ok(existsSync(NATIVE_B), 'prebuilt native B is required');
        assert.notEqual(hash(readFileSync(NATIVE_B)), hash(readFileSync(BIN)), 'native B must have a distinct digest'); binary = NATIVE_B;
      } else {
        const copy = privateDir(join(f.root, 'release-b'));
        for (const name of ['launcher', 'hub-client']) cpSync(join(CONNECTORS, name), join(copy, name), {
          recursive: true, filter: source => !source.split('/').some(part =>
            part === 'node_modules' || part === 'auth.json' || /^\.env(?:\.|$)|^credentials|\.(?:pem|key)$/.test(part)) });
        entry = join(copy, 'launcher', 'src', 'codex', 'delivery-main.ts');
        assert.equal(hash(readFileSync(entry)), hash(readFileSync(ENTRY)));
      }
      f.control({ importBarrier: true });
      const replacement = await f.refresh(f.hubs[0], binary, entry); await f.barrier('import');
      assert.notEqual(replacement.pid, prior.pid); assert.notEqual(birth(prior.pid), prior.birth);
      await until(() => f.fake!.subscribers(A).length === 0, 'no subscriber during history publication');
      assert.equal(f.rows().get('attempt:' + original.key), companion);
      f.fake!.recordLater(original.key, Date.now()); await pause(30);
      f.resume('import'); await f.waitReceipt(sent.id);
      assert.equal(inode(path.lease), originalLease); assert.equal(inode(path.start), originalStart); assert.equal(inode(path.ledger), originalLedger);
      assert.equal(f.turns().length, 1, 'replacement reinjected an accepted batch');
      const confirmations = receiptTrace(f, original.key); assert.ok(confirmations.length);
      for (const receipt of confirmations) { assertOriginalChild(receipt, attempt); assert.deepEqual(receipt.ids, original.deliveries); assert.equal(receipt.proof, true); }
      assert.equal(f.rows().get('attempt:' + original.key), undefined);
    });
  }

  test('real-Go receipt publication survives committed-response loss across a real owner', { timeout: 150000 }, async t => {
    const f = await fixture(t); f.control({ loseReceipt: true }); await f.register();
    const sent = await f.wake(); await f.barrier('receipt-loss');
    const lost = json<{ key: string; claim: string; ids: string[]; proof: boolean }>(join(f.root, 'receipt-lost'));
    assert.equal(lost.proof, true); assert.deepEqual(lost.ids, [sent.id]);
    const saved = f.rows().get('settling:' + lost.key); assert.ok(saved);
    const settlement = JSON.parse(saved) as Settlement; assertScope(settlement, settlement.attempt);
    assert.equal(settlement.row.claim, lost.claim);
    assert.equal(f.state().displayed?.includes(sent.id), true);
    assert.equal(f.state().claims?.some(c => c.Claim === lost.claim) ?? false, false);
    const path = f.paths(f.hubs[0]), lease = inode(path.lease), ledger = inode(path.ledger);
    await f.stopOwner(f.hubs[0], 9);
    // Unavailable Codex must not block original-scope Go settlement recovery.
    const socket = f.fake!.socketPath, unavailable = socket + '.unavailable'; renameSync(socket, unavailable);
    f.control({}); await f.refresh(); await f.waitReceipt(sent.id);
    assert.equal(f.turns().length, 1); assert.equal(inode(path.lease), lease); assert.equal(inode(path.ledger), ledger);
    const confirmations = receiptTrace(f, lost.key); assert.ok(confirmations.length >= 2);
    assert.ok(confirmations.some(r => r.released === 0), 'recovery did not reassert real committed receipt idempotently');
    for (const row of confirmations) { assertOriginalChild(row, settlement.attempt); assert.equal(row.proof, true); }
    assert.equal(f.rows().get('settling:' + lost.key), undefined); renameSync(unavailable, socket);
  });

  test('automatic delivery isolates two sessions and two hubs', { timeout: 150000 }, async t => {
    const f = await fixture(t, { hubs: 2 });
    const a2 = await f.add('a2', f.hubs[1]), sender2 = await f.add('sender', f.hubs[1]);
    assert.ok(sender2); f.writeConfigs();
    await f.register(f.a, A); await f.register(f.b, B); await f.register(a2, A);
    const routes = [{ p: f.a, thread: A }, { p: f.b, thread: B }, { p: a2, thread: A }];
    const keys: string[] = [];
    for (const { p, thread } of routes) {
      const sent = await f.wake(p); await f.waitReceipt(sent.id, p);
      const key = f.keyFor(p.hub, thread, [sent.id]); keys.push(key);
      const sends = f.turns().filter(x => (x.params as { clientUserMessageId: string }).clientUserMessageId === key);
      assert.equal(sends.length, 1); assert.equal((sends[0].params as { threadId: string }).threadId, thread);
      assert.equal(receiptTrace(f, key).every(x => x.argv?.[1] === p.name), true);
      for (const other of routes.filter(x => x.p !== p)) assert.equal(f.state(other.p).displayed?.includes(sent.id) ?? false, false);
    }
    assert.equal(new Set(keys).size, 3); assert.equal(f.turns().length, 3);
    assert.equal(f.trace().filter(x => x.kind === 'runner').length, 2);
    assert.notEqual(f.owner(f.hubs[0])!.pid, f.owner(f.hubs[1])!.pid);
    assert.equal(f.bindings().bindings.length, 3);
  });

  test('automatic delivery resumes after credential rotation without changing pinned caller', { timeout: 180000 }, async t => {
    const f = await fixture(t); await f.register(); const pinned = f.bindings().bindings[0];
    const first = await f.wake(); await f.waitReceipt(first.id);
    const oldWatch = f.trace().find(x => x.kind === 'watch' && x.argv?.[1] === f.a.name); assert.ok(oldWatch?.childBirth);
    const rotated = await f.http<{ secret: string }>(f.hubs[0].operator, f.hubs[0], '/v1/admin/peers/' + f.a.peerId + '/rotate', undefined, 'POST');
    assert.equal(typeof rotated.secret, 'string');
    await until(() => birth(oldWatch.child!) !== oldWatch.childBirth, 'old credential watch stops', 30000);
    f.a.secret = rotated.secret; f.writeConfigs();
    const sent = await f.wake(); await f.waitReceipt(sent.id, f.a, 90000);
    const after = f.bindings().bindings[0];
    assert.equal(after.caller, pinned.caller); assert.equal(after.hub, pinned.hub); assert.equal(after.codex_home, pinned.codex_home);
    assert.equal(after.thread_id, pinned.thread_id); assert.equal(f.trace().filter(x => x.kind === 'runner').length, 1);
  });

  for (const transport of ['replay-poll', 'live-SSE'] as const) for (const resolution of ['receipt', 'release'] as const) {
    test(`pending reservation defers monitor output and cumulative ack (${transport}, ${resolution})`, { timeout: 120000 }, async t => {
      const f = await fixture(t), claim = 'monitor.' + randomUUID();
      const head = await f.http<{ next_cursor: string }>(f.a, f.a.hub, '/v1/events?after=head&wait=0');
      let monitor: Process | undefined;
      const start = () => f.process(BIN, ['--net', f.a.name, 'watch', '--for-monitor', '--auto-ack', '--deliveries-only', '--since', head.next_cursor],
        { ...f.env, VECNET_CONFIG: f.a.config });
      // A real hub reports connected only after opening the SSE stream. Stop
      // this owned monitor with pidfd before creating E1 so its actual claim
      // commits before the monitor may read/print queued transport bytes.
      let monitorBirth: string | undefined;
      if (transport === 'live-SSE') {
        monitor = start();
        const deadline = performance.now() + WAIT;
        let connected = false;
        while (performance.now() < deadline) {
          const directory = await f.http<{ peers: { peer_id: string; reachability: string }[] }>(f.sender, f.a.hub, '/v1/peers');
          if (directory.peers.some(x => x.peer_id === f.a.peerId && x.reachability === 'connected')) { connected = true; break; }
          await pause(20);
        }
        assert.equal(connected, true, 'actual monitor SSE was not established');
        monitorBirth = birth(monitor.child.pid!); assert.ok(monitorBirth);
        await f.signal(monitor.child.pid!, monitorBirth, 19);
      }
      const sent = await f.wake();
      const reserved = await f.call(f.a, ['hook', '--event', 'app-server', '--claim', claim, '--priorities', 'wake',
        '--expect-hub', f.a.hub.url, '--expect-caller', f.a.peerId]);
      assert.equal(reserved.code, 0);
      const response = JSON.parse(reserved.stdout) as { deliveries: string[]; lifecycle: string };
      assert.equal(response.lifecycle, 'receipt-display-v1'); assert.deepEqual(response.deliveries, [sent.id]);
      const later = await f.wake(f.a, 'hold');
      monitor ??= start();
      if (monitorBirth) await f.signal(monitor.child.pid!, monitorBirth, 18);
      await pause(1200);
      assert.equal(monitor.stdout().includes(sent.text), false, 'reserved event printed');
      assert.equal(monitor.stdout().includes(later.text), false, 'later event crossed reservation barrier');
      const eventSeq = (id: string) => Number(f.inspect(f.hubs[0].database, db => db.prepare('SELECT event_seq FROM deliveries WHERE dly_id = ?').get(id)?.event_seq));
      const ackSeq = () => Number(f.inspect(f.hubs[0].database, db => db.prepare('SELECT seq FROM acks WHERE owner = ? AND scope = ?').get(f.a.peerId, '*')?.seq ?? 0));
      assert.ok(ackSeq() < eventSeq(sent.id), 'cumulative ACK crossed the blocked event');
      const key = f.keyFor(f.a.hub, A, [sent.id]);
      const args = ['hook', '--release', '--claim', claim, ...(resolution === 'receipt' ? [
        '--receipt-key', key, '--delivery-ids', sent.id, '--expect-hub', f.a.hub.url, '--expect-caller', f.a.peerId] : [])];
      const released = await f.call(f.a, args); assert.equal(released.code, 0);
      if (resolution === 'receipt') {
        const proof = JSON.parse(released.stdout) as { receipt_key: string; lifecycle: string; deliveries: string[] };
        assert.equal(proof.receipt_key, key); assert.equal(proof.lifecycle, 'receipt-display-v1'); assert.deepEqual(proof.deliveries, [sent.id]);
      }
      await until(() => monitor!.stdout().includes(later.text), 'later event after local barrier retry');
      assert.equal(monitor.stdout().split(sent.text).length - 1, resolution === 'receipt' ? 0 : 1);
      assert.equal(monitor.stdout().split(later.text).length - 1, 1);
      await until(() => ackSeq() >= eventSeq(later.id), 'cumulative ACK after barrier resolution');
      // Neither resolution posts a new delivery; progress came from local retry.
      assert.equal(Number(f.inspect(f.hubs[0].database, db => db.prepare('SELECT count(*) AS n FROM deliveries WHERE recipient = ?').get(f.a.peerId)?.n)), 2);
      await monitor.stop();
    });
  }

  test('explicit unbind stays durable after the next ordinary action', { timeout: 120000 }, async t => {
    const f = await fixture(t); await f.register();
    assert.equal((await f.call(f.a, ['codex', 'unbind'])).code, 0);
    const after = await f.action(f.a); assert.equal(after.code, 0);
    await until(() => f.fake!.subscribers(A).length === 0, 'opted-out engine retires');
    assert.equal(f.bindings().bindings.length, 0); assert.ok(f.bindings().disabled?.some(x => x.profile === 'a'));
    const sent = await f.wake(); await pause(1200);
    assert.equal(f.turns().length, 0); assert.equal(f.state().displayed?.includes(sent.id) ?? false, false);
  });

  for (const outcome of ['accepted', 'ambiguous'] as const) for (const evidence of ['receipt', 'history'] as const) {
    test(`R35 first ${evidence} after source replacement preserves original ${outcome} attempt scope`, { timeout: 150000 }, async t => {
      const f = await fixture(t); await f.register(); f.fake!.withholdNextItem();
      if (outcome === 'ambiguous') f.armDrop();
      const sent = await f.wake();
      const original = await until(() => f.inflight().find(x => x.deliveries.includes(sent.id) &&
        x.state === (outcome === 'ambiguous' ? 'unresolved' : 'pending')), 'pre-receipt original generation');
      const bytes = f.rows().get('attempt:' + original.key); assert.ok(bytes);
      const attempt = JSON.parse(bytes) as Attempt;
      assert.equal(attempt.profile, f.a.name); assert.equal(attempt.caller, f.a.peerId); assert.equal(attempt.env.VECNET_CONFIG, f.a.config);
      assert.equal(attempt.env.VECNET_CODEX_AUTO_DELIVERY, '0');
      assert.equal(Object.keys(attempt.env).some(x => /SECRET|TOKEN|CODEX_HOME/i.test(x)), false);
      const paths = f.paths(f.hubs[0]), lease = inode(paths.lease), ledger = inode(paths.ledger);
      await f.stopOwner(f.hubs[0], 9);
      await until(() => f.fake!.subscribers(A).length === 0, 'old source disconnected');
      assert.equal((await f.call(f.a, ['codex', 'unbind'])).code, 0);
      const replacement = await f.add('replacement', f.hubs[0], join(f.root, 'source-b', 'config.toml'));
      f.writeConfigs(); f.control({ loseReceipt: true, ...(evidence === 'history' ? { importBarrier: true } : {}) });
      assert.equal((await f.action(replacement)).code, 0, 'replacement next action failed');
      await until(() => f.owner(f.hubs[0]), 'replacement source real owner');
      if (evidence === 'history') {
        await f.barrier('import'); assert.equal(f.fake!.subscribers(A).length, 0);
        f.fake!.recordLater(original.key, Date.now()); await pause(30); f.resume('import');
      } else {
        await until(() => f.fake!.subscribers(A).length === 1, 'replacement warm subscription');
        assert.equal(f.rows().get('attempt:' + original.key), bytes, 'first receipt provenance was rewritten');
        f.fake!.recordLater(original.key, Date.now());
      }
      await f.barrier('receipt-loss');
      const saved = f.rows().get('settling:' + original.key); assert.ok(saved);
      const settlement = JSON.parse(saved) as Settlement;
      assertScope(settlement, attempt); assertScope(settlement.attempt, attempt); assert.equal(settlement.origin, evidence);
      assert.equal(f.state(f.a).displayed?.includes(sent.id), true);
      assert.equal(f.state(replacement).displayed?.includes(sent.id) ?? false, false, 'receipt used replacement caller/source');
      for (const row of receiptTrace(f, original.key)) assertOriginalChild(row, attempt);
      await f.stopOwner(f.hubs[0], 9); f.control({}); await f.refresh(); await f.waitReceipt(sent.id);
      assert.equal(f.turns().length, 1); assert.equal(inode(paths.lease), lease); assert.equal(inode(paths.ledger), ledger);
    });
  }

  test('R38 hard kill after companion deletion retains key reservation through lost ordinary confirmation and same-key wake', { timeout: 150000 }, async t => {
    const { f, original, releasing, attempt } = await interruptedOrdinaryCleanup(t);
    f.fake!.withholdNextItem(); f.control({ holdOrdinary: original.claim }); await f.refresh();
    await until(() => f.trace().some(x => x.kind === 'ordinary-confirmation' && x.claim === original.claim && x.released === 0), 'real released:0 whose response is lost');
    const before = f.trace().filter(x => x.kind === 'claim-confirmation').length;
    f.fake!.setThread({ id: A, status: { type: 'active', activeFlags: [] }, activeTurnId: 'r38-warm-turn' });
    f.fake!.notify('thread/status/changed', { threadId: A, status: { type: 'active', activeFlags: [] } });
    await until(() => f.trace().filter(x => x.kind === 'claim-confirmation').slice(before).some(x =>
      x.ids?.includes(original.deliveries[0])), 'eligible same-batch real Go claim');
    assert.equal(f.turns().length, 1, 'retained N1 releasing authority failed to reserve original deterministic key');
    assert.equal(f.rows().get('releasing:' + original.claim), releasing);
    assert.equal(f.rows().get('inflight:' + original.key), undefined); assert.equal(f.rows().get('attempt:' + original.key), undefined);
    f.control({});
    const newer = await until(() => f.inflight().find(x => x.key === original.key && x.claim !== original.claim && x.state === 'pending'), 'same original key becomes eligible after N1 cleanup');
    assert.equal(f.turns().length, 2); assert.notEqual(newer.claim, original.claim);
    assert.equal(f.rows().get('releasing:' + original.claim), undefined); assert.equal(receiptTrace(f, original.key).length, 0);
    const confirmations = f.trace().filter(x => x.kind === 'ordinary-confirmation' && x.claim === original.claim);
    assert.ok(confirmations.some(x => x.released === 0)); for (const row of confirmations) assertOriginalChild(row, attempt);
  });

  for (const outcome of ['accepted', 'ambiguous'] as const) {
    test(`R38 old genuine released:0 preserves a newer persisted ${outcome} nonce and provenance`, { timeout: 180000 }, async t => {
      const { f, sent, original, attempt, releasing } = await interruptedOrdinaryCleanup(t);
      await recoveryFixtureRows(f, [{ key: 'releasing:' + original.claim, value: null }]);
      assert.equal((await f.call(f.a, ['codex', 'unbind'])).code, 0);
      // Same real receiver through another source/profile makes provenance
      // distinguishable while the delivery batch and deterministic key stay K.
      const newerProfile: Profile = { ...f.a, name: 'newer', config: join(f.root, 'newer-source', 'config.toml') };
      f.profiles.push(newerProfile); f.writeConfigs(); f.control({});
      f.fake!.withholdNextItem(); if (outcome === 'ambiguous') f.armDrop();
      assert.equal((await f.action(newerProfile)).code, 0);
      const newer = await until(() => f.inflight().find(x => x.key === original.key && x.claim !== original.claim &&
        x.state === (outcome === 'accepted' ? 'pending' : 'unresolved')), 'real accepted newer nonce');
      const inflight = f.rows().get('inflight:' + newer.key), provenance = f.rows().get('attempt:' + newer.key);
      assert.ok(inflight); assert.ok(provenance); assert.equal((JSON.parse(provenance) as Attempt).profile, newerProfile.name);
      await f.stopOwner(f.hubs[0], 9);
      await recoveryFixtureRows(f, [{ key: 'releasing:' + original.claim, value: releasing }]);
      f.control({ pauseReleasingDelete: true }); await f.refresh(); await f.barrier('releasing-delete');
      // Pause immediately after genuine old cleanup, before any independent
      // history scan may legitimately update N2's pending/unresolved state.
      assert.equal(f.rows().get('inflight:' + newer.key), inflight);
      assert.equal(f.rows().get('attempt:' + newer.key), provenance);
      assert.ok(f.state(newerProfile).claims?.some(x => x.Claim === newer.claim && x.ID === sent.id));
      assert.equal(f.rows().get('releasing:' + original.claim), undefined);
      const last = f.trace().filter(x => x.kind === 'ordinary-confirmation' && x.claim === original.claim).at(-1);
      assert.ok(last); assert.equal(last.released, 0); assertOriginalChild(last, attempt);
      assert.equal(f.turns().length, 2); assert.equal(receiptTrace(f, original.key).length, 0);
      await f.stopOwner(f.hubs[0], 9); f.control({}); await f.refresh();
      await until(() => f.fake!.subscribers(A).length === 1, 'newer generation reconnects');
      f.fake!.recordLater(newer.key, Date.now()); await f.waitReceipt(sent.id, newerProfile);
      assert.equal(f.state(f.a).displayed?.includes(sent.id) ?? false, false);
      assert.ok(receiptTrace(f, newer.key).every(x => x.config === newerProfile.config)); assert.equal(f.turns().length, 2);
    });
  }

  test('a pending claim child retains ownership after runner death', { timeout: 150000 }, async t => {
    const f = await fixture(t); await f.register();
    const held = f.owner(f.hubs[0])!, path = f.paths(f.hubs[0]), originalLease = inode(path.lease);
    const ready = join(f.root, 'state-lock-held'), stateLock = join(dirname(f.a.config), 'state', 'a.json.lock');
    privateDir(dirname(stateLock)); privateFile(stateLock, '');
    const source = `import fs from 'node:fs'; fs.writeFileSync(process.argv[1], '1', {mode:0o600}); setTimeout(()=>{},8000);`;
    const holder = f.process('/usr/bin/flock', ['-x', stateLock, realpathSync(globalThis.process.execPath),
      '--input-type=module', '-e', source, ready], f.env, 10000);
    await until(() => existsSync(ready), 'independent bounded state lock holder');
    const start = f.trace().length; const sent = await f.wake();
    const claim = await until(() => f.trace().slice(start).find(x => x.kind === 'hook' && !x.argv?.includes('--release') && x.childBirth), 'blocked real Go claim child');
    assert.equal(inode(`/proc/${claim.child}/fd/3`), originalLease);
    assert.match(readFileSync(`/proc/${claim.child}/fdinfo/3`, 'utf8'), /^lock:\s+\d+: FLOCK\s+ADVISORY\s+WRITE\s/m);
    const runners = f.trace().filter(x => x.kind === 'runner').length;
    await f.stopOwner(f.hubs[0], 9); assert.equal(birth(claim.child!), claim.childBirth);
    const contender = f.process(BIN, ['codex', 'delivery-refresh', '--hub', f.hubs[0].url], f.env, 15000);
    await pause(250);
    assert.equal(f.trace().filter(x => x.kind === 'runner').length, runners, 'replacement overlapped surviving bounded claimant');
    assert.equal(f.turns().length, 0);
    assert.equal((await holder.done).code, 0); await until(() => birth(claim.child!) !== claim.childBirth, 'orphan claim drains after external holder deadline');
    assert.equal((await contender.done).code, 0);
    const replacement = await until(() => f.owner(f.hubs[0]), 'replacement after bounded child drains');
    assert.notEqual(replacement.pid, held.pid); assert.equal(inode(path.lease), originalLease);
    await f.waitReceipt(sent.id); assert.equal(f.turns().length, 1);
  });

  test('a lingering watch child does not retain ownership', { timeout: 120000 }, async t => {
    const f = await fixture(t); await f.register();
    const watch = await until(() => f.trace().find(x => x.kind === 'watch' && x.childBirth), 'actual watch child');
    const lease = inode(f.paths(f.hubs[0]).lease);
    for (const fd of readdirSync(`/proc/${watch.child}/fd`)) {
      try { assert.notEqual(inode(`/proc/${watch.child}/fd/${fd}`), lease, 'watch inherited lifetime lease'); }
      catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error; }
    }
    await f.stopOwner(f.hubs[0], 9);
    assert.equal(birth(watch.child!), watch.childBirth, 'watch did not remain alive for the orphan control');
    await f.refresh(); assert.equal(inode(f.paths(f.hubs[0]).lease), lease);
    assert.equal(birth(watch.child!), watch.childBirth, 'replacement depended on watch exit');
  });

  for (const failure of ['missing-node', 'old-node', 'missing-assets', 'missing-socket', 'unsafe-socket'] as const) {
    test(`automatic delivery ${failure} fallback preserves ordinary stdout and exit`, { timeout: 120000 }, async t => {
      const f = await fixture(t), before = await f.call(f.a, ['--json', 'threads']);
      f.env.PATH = f.dirs.shims; // prevent fallback to a real system node/manager
      if (failure === 'missing-node') renameSync(join(f.dirs.shims, 'node'), join(f.root, 'unused-node'));
      if (failure === 'old-node') privateFile(join(f.dirs.shims, 'node'), '#!/bin/sh\necho v0.1.0\n', 0o700);
      const entry = failure === 'missing-assets' ? join(f.root, 'missing-entry.ts') : ENTRY;
      if (failure === 'missing-socket') renameSync(f.fake!.socketPath, f.fake!.socketPath + '.unavailable');
      if (failure === 'unsafe-socket') chmodSync(dirname(realpathSync(f.fake!.socketPath)), 0o755);
      const result = await f.action(f.a, A, BIN, entry);
      assert.equal(result.code, before.code); assert.equal(result.signal, before.signal); assert.equal(hash(result.stdout), hash(before.stdout));
      await pause(2000); assert.equal(f.turns().length, 0); assert.equal(f.owner(f.hubs[0]), undefined);
      // Fallback leaves explicit inbox usable through the same real CLI.
      assert.equal((await f.call(f.a, ['--json', 'inbox'])).code, 0);
    });
  }

  test('combined canonical-home cap is enforced across hubs before filtering', { timeout: 150000 }, async t => {
    const f = await fixture(t, { hubs: 2, cap: 2 }), third = await f.add('third', f.hubs[1]);
    await f.add('sender', f.hubs[1]); f.writeConfigs();
    await f.register(f.a, A); await f.register(f.b, B);
    const result = await f.action(third, A); assert.equal(result.code, 0);
    await pause(2500);
    assert.equal(f.bindings().bindings.length, 2); assert.equal(f.bindings().bindings.some(x => x.profile === third.name), false);
    const sent = await f.wake(third); await pause(500);
    assert.equal(f.state(third).displayed?.includes(sent.id) ?? false, false);
  });
});
