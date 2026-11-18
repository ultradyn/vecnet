import { execFileSync } from 'node:child_process';
import type { TestContext } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, readFileSync, rmSync, mkdirSync, renameSync, existsSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { ownerOf } from '../src/codex/ownership.ts';
import type { Owner } from '../src/codex/ownership.ts';
import { createEngine } from '../src/codex/engine.ts';
import type { Binding, EngineDeps, InflightRow } from '../src/codex/engine.ts';
import { openCodexRpc, CodexRpcError } from '../src/codex/rpc.ts';
import type { AllowedMethod } from '../src/codex/protocol.ts';
import type { CodexRpc } from '../src/codex/rpc.ts';
import { startFakeCodexAppServer } from './fake-codex-appserver.ts';
import { fakeClock, memoryLog } from '../../hub-client/testing/fixtures.ts';
import type { KeyValue } from '../src/backend.ts';

const threadId = '11111111-1111-4111-8111-111111111111';
const minute = 60_000, hour = 60 * minute;
const stub = 'vecnet: new messages attached as untrusted context.';
const allRequests: { method: string; params: unknown }[] = [];
async function eventually(fn: () => boolean, description = 'condition') {
  for (let i = 0; i < 500; i++) { if (fn()) return; await delay(10); }
  assert.ok(fn(), description);
}
function checkRequests(requests: typeof allRequests) {
  const allowed: Record<string, string[]> = {
    initialize: ['clientInfo', 'capabilities'], initialized: [], 'thread/read': ['threadId', 'includeTurns'],
    'thread/turns/list': ['threadId', 'limit', 'itemsView', 'cursor'],
    'thread/resume': ['threadId', 'excludeTurns'], 'thread/unsubscribe': ['threadId'],
    'turn/start': ['threadId', 'input', 'clientUserMessageId', 'additionalContext'],
    'turn/steer': ['threadId', 'input', 'clientUserMessageId', 'additionalContext', 'expectedTurnId'],
  };
  for (const r of requests) {
    assert.ok(Object.hasOwn(allowed, r.method), r.method);
    for (const key of Object.keys((r.params ?? {}) as object)) assert.ok(allowed[r.method].includes(key), `${r.method}: ${key}`);
    if (r.method === 'thread/resume') assert.deepEqual(r.params, { threadId: threadId, excludeTurns: true });
  }
}
type TraceRecord = { argv: string[]; env: Record<string, string>; ack?: boolean; watchRevision?: number; hookClaimed?: string; guardReady?: string; guardFinished?: string };
function parseTraceRecords(text: string): TraceRecord[] {
  // A concurrent append may expose an incomplete suffix. Retry that suffix on
  // the next full-file read; malformed newline-terminated records must throw.
  return text.slice(0, text.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(s => JSON.parse(s) as TraceRecord);
}

type Item = { id: string; priority: string; value?: string };
async function harness(t: TestContext, options: { binding?: Partial<Binding>; status?: unknown; wrap?: (rpc: CodexRpc) => CodexRpc; seed?: InflightRow; claiming?: string; unreachable?: boolean; refusedVersion?: boolean; releaseFailure?: boolean; history?: number; coldResumeIdleDelayMs?: number; source?: unknown; authority?: Partial<Owner>; legacySeed?: boolean; verifyFailure?: boolean; parentThreadId?: string | null; homeAlias?: boolean } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'vn-e-')), clock = fakeClock(), log = memoryLog();
  let activity = 0, pendingRpcs = 0, pendingHooks = 0, watchArms = 0;
  const timer = clock.timer;
  clock.timer = (ms, fn) => {
    activity++;
    if (ms === 1) watchArms++;
    if ((ms === 5000 || ms === 32000)) pendingHooks++;
    let live = true;
    const done = () => { if (live) { live = false; if ((ms === 5000 || ms === 32000)) pendingHooks--; activity++; } };
    const alarm = timer(ms, () => { done(); fn(); });
    return { cancel() { done(); alarm.cancel(); } };
  };
  const fake = await startFakeCodexAppServer({ dir, clock, coldResumeIdleDelayMs: options.coldResumeIdleDelayMs });
  if (options.refusedVersion) fake.setVersion('0.0.0');
  fake.setThread({ id: threadId, status: options.status ?? { type: 'idle' },
    ...((options.status as { type?: string })?.type === 'active' ? { activeTurnId: 'active' } : {}) });
  fake.attachOther(threadId);
  if (options.history) fake.addTurns(threadId, options.history);
  const map = new Map<string, string>();
  const kv: KeyValue = { get: k => map.get(k), set: (k, v) => { map.set(k, v); }, delete: k => { map.delete(k); }, keys: (p = '') => [...map.keys()].filter(k => k.startsWith(p)) };
  const config = join(dir, 'plan.json'), trace = join(dir, 'trace.jsonl'), claims = join(dir, 'claims.json');
  const plan = { trace, claims, stub, items: (options.claiming ? [{ id: 'w1', priority: 'wake' }] : []) as Item[], events: [] as unknown[], refused: '', revision: 0, hookGate: '', hookFailure: '', releaseFailure: options.releaseFailure ?? false, watchStop: 0, watchCrash: '', owner: undefined as Owner | undefined, handoffAfterClaim: undefined as Partial<Owner> | undefined, settleFailure: false, verifyFailure: options.verifyFailure ?? false };
  writeFileSync(trace, ''); writeFileSync(claims, '{}');
  const authorityPath = join(dir, '.local', 'state', 'vecnet', 'codex', 'bindings.json');
  mkdirSync(dirname(authorityPath), { recursive: true });
  const save = () => {
    if (plan.owner) writeFileSync(authorityPath, JSON.stringify({ version: 1, bindings: [{
      profile: plan.owner.profile, as: plan.owner.as, hub: plan.owner.hub, caller: plan.owner.caller,
      config_path: plan.owner.configPath, codex_home: plan.owner.codexHome, thread_id: plan.owner.threadId, generation: plan.owner.generation,
      created_at: new Date(binding.createdAt).toISOString(), expires_at: new Date(binding.expiresAt).toISOString(),
    }] }), { mode: 0o600 });
    plan.revision++; writeFileSync(config + '.next', JSON.stringify(plan)); renameSync(config + '.next', config); }; save();
  const bin = join(dir, 'vecnet');
  writeFileSync(bin, `#!/bin/sh\nexport FAKE_VECNET_PLAN=\"$VECNET_CONFIG\"\nexec '${process.execPath}' '${fileURLToPath(new URL('./fake-vecnet.ts', import.meta.url))}' "$@"\n`, { mode: 0o700 });
  const codexTrap = join(dir, 'codex-spawned');
  writeFileSync(join(dir, 'codex'), `#!/bin/sh\necho forbidden > '${codexTrap}'\nexit 99\n`, { mode: 0o700 });
  for (const p of ['config', 'cache', 'codex', 'runtime']) mkdirSync(join(dir, p + '-home'));
  if (options.homeAlias) symlinkSync(dir, join(dir, 'home-alias'));
  const env: Record<string, string> = { HOME: options.homeAlias ? join(dir, 'home-alias') : dir, PATH: dir, VECNET_CONFIG: config, XDG_CONFIG_HOME: join(dir, 'config-home'), XDG_CACHE_HOME: join(dir, 'cache-home'),
    CODEX_HOME: join(dir, 'codex-home'), XDG_RUNTIME_DIR: join(dir, 'runtime-home'), SECRET_TOKEN: 'never-forward' };
  if (options.homeAlias) env.VECNET_CODEX_BINDINGS = authorityPath;
  const binding: Binding = { configPath: config, generation: 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', profile: 'test', as: 'peer', hub: 'https://hub.invalid', caller: 'peer_pinned', codexHome: dirname(dirname(fake.socketPath)),
    threadId: threadId, createdAt: 0, expiresAt: 100 * hour, attended: false, origin: 'explicit', ...options.binding };
  plan.owner = { ...ownerOf(binding), ...options.authority }; save();
  if (options.seed) kv.set('inflight:' + options.seed.key, JSON.stringify({ ...options.seed, ...(!options.legacySeed ? { owner: options.seed.owner ?? ownerOf(binding) } : {}) }));
  if (options.claiming) { kv.set('claiming:' + options.claiming, JSON.stringify({ owner: ownerOf(binding), threadId: threadId, at: 0 })); writeFileSync(claims, JSON.stringify({ [options.claiming]: ['w1'] })); }
  let opens = 0, current: CodexRpc | undefined;
  const deps: EngineDeps = { clock, log, kv, vecnetBin: bin, env, euid: (process.geteuid?.() ?? -1), debounceMs: 1, minGapMs: 5,
    openRpc: async o => { opens++; if (options.unreachable) throw new CodexRpcError(-1, 'unreachable');
      const rpc = await openCodexRpc(o); current = rpc;
      const observed: CodexRpc = { ...rpc,
        async call<T>(m: AllowedMethod, p: unknown, ms: number): Promise<T> {
          pendingRpcs++; activity++;
          try {
            const result = await rpc.call<any>(m, p, ms);
            if (m === 'thread/read') { result.thread.source = Object.hasOwn(options, 'source') ? options.source : 'cli'; result.thread.parentThreadId = options.parentThreadId ?? null; }
            return result;
          } finally { pendingRpcs--; activity++; }
        },
        onNotification(fn) { rpc.onNotification((m, p) => { activity++; fn(m, p); }); },
      };
      return options.wrap ? options.wrap(observed) : observed; } };
  let watching = true;
  const makeEngine = () => {
    const e = createEngine(binding, deps), stop = e.stop;
    e.stop = async () => { watching = false; await stop(); };
    return e;
  };
  let engine = makeEngine();
  const records = () => parseTraceRecords(readFileSync(trace, 'utf8'));
  const hooks = () => records().filter(r => !r.ack && r.argv.includes('hook'));
  const releases = () => hooks().filter(r => r.argv.includes('--release'));
  const sends = () => fake.requests.filter(r => r.method.startsWith('turn/'));
  const rows = () => kv.keys('inflight:').map(k => JSON.parse(kv.get(k)!) as InflightRow);
  // A socket round trip delivers prior notifications. Hook timer cancellation
  // acknowledges child close/output consumption; counters include queued RPCs.
  const sync = async () => {
    for (;;) {
      await eventually(() => pendingRpcs === 0 && pendingHooks === 0, 'RPC and hook completion');
      const before = activity;
      if (current && engine.state() === 'ready') {
        try { await current.call('thread/read', { threadId: threadId }, 1000); } catch {}
      }
      await Promise.resolve();
      if (before === activity && pendingRpcs === 0 && pendingHooks === 0) return;
    }
  };
  const tick = async (ms = 10) => { await sync(); clock.advance(ms); await sync(); clock.advance(10); await sync(); };
  const wake = async (items: Item[] = [{ id: 'w1', priority: 'wake' }]) => {
    const arms = watchArms;
    plan.items.push(...items); plan.events.push(...items.map(i => ({ type: 'delivery', data: { delivery_id: i.id, message: { priority: i.priority } } }))); save();
    if (watching && !plan.verifyFailure && records().some(r => !r.ack && r.argv.includes('watch')) && !['ownership_changed', 'identity_changed'].includes((engine.liveness() as any).reason)) {
      await eventually(() => records().some(r => r.ack && r.watchRevision === plan.revision), 'watch publication acknowledgement');
      const eligible = items.filter(i => i.priority === 'wake' || i.priority === 'wake_idle').length;
      if (eligible) await eventually(() => watchArms >= arms + eligible, 'watch consumed and debounce armed');
    }
    await tick(2000);
  };
  const loaded = async () => {
    await eventually(() => engine.state() === 'ready', 'connection and subscription ready');
    if (!['ownership_changed', 'identity_changed', 'unverified_thread', 'identity_unverified', 'expired'].includes((engine.liveness() as any).reason) && !rows().some(r => r.state !== 'pending')) {
      await eventually(() => records().some(r => r.ack && r.watchRevision === plan.revision), 'watch ready');
    }
    await tick(2000);
  };
  t.after(async () => {
    await engine.stop(); await fake.close();
    allRequests.push(...fake.requests); checkRequests(fake.requests);
    assert.equal(fake.overrides.length, 0); assert.deepEqual(fake.replies, []);
    assert.throws(() => readFileSync(codexTrap));
    for (const r of records().filter(r => !r.ack)) { assert.equal(r.env.SECRET_TOKEN, undefined); assert.equal(r.env.CODEX_HOME, undefined); }
    rmSync(dir, { recursive: true, force: true });
  });
  engine.start();
  if (options.refusedVersion) await eventually(() => engine.state() === 'refused_version');
  else if (options.unreachable) await eventually(() => engine.state() === 'unreachable');
  else await loaded();
  return { fake, clock, log, kv, plan, save, binding, deps, records, hooks, releases, sends, rows, tick, wake, loaded, sync,
    get engine() { return engine; }, get opens() { return opens; }, disconnect() { current!.close(); },
    async restart() { await engine.stop(); save(); engine = makeEngine(); watching = true; engine.start(); await loaded(); } };
}
const active = { type: 'active', activeFlags: [] };
const row = (patch: Partial<InflightRow> = {}): InflightRow => ({ attempt: 'c'.repeat(32), threadId: threadId, key: 'vn.seed', claim: '123456789012345678901234', deliveries: ['w1'],
  method: 'turn/steer', attemptTurnId: 'active', priorTurnId: null, acceptedAt: 0, state: 'pending', ...patch });

// Current settlement is a Go codex/settle operation, rather than a hook receipt.
type RecoveryHarness = Awaited<ReturnType<typeof harness>>;
const receiptSettles = (h: RecoveryHarness) => h.records().filter(r => !r.ack && r.argv.includes('settle') && r.argv.includes('--receipt-key'));
const claimReservations = (h: RecoveryHarness): Record<string, string[]> => Object.fromEntries(Object.entries(JSON.parse(readFileSync(h.plan.claims, 'utf8')) as Record<string, string[]>).filter(([, ids]) => ids.length > 0));
const deliveryGuards = (h: RecoveryHarness) => h.records().filter(r => !r.ack && r.argv.includes('guard'));

// F2 synthetic native state transition; this is not evidence about the xpp incident.

// Observation retries never retry a rejected input request.

// Completion reconciliation must never opt a launcher binding into loading.
// Mutating reconcile(false) to reconcile() must invoke this otherwise idle hook.

// Seven consecutive observation errors exercise the capped 1/2/4/8/15/15/15 cadence.

// Round-three probes, exercised through the fake wire and real hook children.

function ambiguousAfterAccept(after?: (rpc: CodexRpc, key: string) => Promise<void>) {
  let once = true;
  return (rpc: CodexRpc): CodexRpc => ({ ...rpc, async call<T>(method: AllowedMethod, params: unknown, timeout: number): Promise<T> {
    const result = await rpc.call<T>(method, params, timeout);
    if (method === 'turn/start' && once) { once = false; await after?.(rpc, (params as any).clientUserMessageId); rpc.close(); throw new CodexRpcError(-1, 'connection_closed', true); }
    return result;
  } });
}

// The release can commit in Go before the engine commits its KV cleanup.


export { threadId, minute, hour, stub, allRequests, eventually, checkRequests, parseTraceRecords, harness, active, row, receiptSettles, claimReservations, deliveryGuards, ambiguousAfterAccept };
export type { TraceRecord, Item, RecoveryHarness };
