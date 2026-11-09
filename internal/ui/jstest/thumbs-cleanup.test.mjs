import test from 'node:test';
import assert from 'node:assert/strict';
import { createThumbCache } from '../static/thumbs-model.js';
import { createThumbBudget } from '../static/thumbs-budget.js';

const BASE = { B: 16, C: 2, E: 4, N: 8, Q: 4, M: 4 };
const sha = n => n.toString(16).padStart(64, '0');
const raster = size => new Blob([new Uint8Array(size)], { type: 'image/png' });
const view = lease => ({ state: lease.state, url: lease.url, generation: lease.generation });
// node:test owns unhandled-rejection detection; install no process-level swallow.
const hostTurn = () => new Promise(resolve => setImmediate(resolve));
function deferred() {
  let resolve;
  const promise = new Promise(yes => { resolve = yes; });
  return { promise, resolve };
}
function fixture(t, patch = {}) {
  const limits = { ...BASE, ...patch }, budget = createThumbBudget({ limits });
  const calls = [], allocated = [], revoked = [], attempts = [], trace = [], errors = [], caches = [];
  let closing = false, legacyCalls = 0;
  function equations() {
    const s = budget.stats;
    for (const n of Object.values(s)) assert.ok(Number.isSafeInteger(n) && n >= 0);
    assert.equal(s.reservedBytes, limits.M * s.running);
    assert.ok(s.residentBytes + s.reservedBytes <= limits.B);
    assert.ok(s.running <= limits.C);
    assert.ok(s.running + s.residentEntries <= limits.E);
    assert.ok(s.queued <= limits.Q && s.sources <= limits.N);
  }
  const tap = fn => { try { equations(); fn(); } catch (error) { errors.push(error); } };
  const record = (op, fields = {}) => trace.push({ op, ...fields, quota: budget.stats });
  function check() { equations(); assert.deepEqual(errors, [], 'callback assertions must survive containment'); }
  function make() {
    const id = caches.length, h = {}, events = [];
    const api = {
      blob() { legacyCalls++; throw new Error('lease mode must use thumbnailBlob'); },
      thumbnailBlob(hash, { signal, maxBytes }) {
        tap(() => { assert.ok(signal instanceof AbortSignal); assert.equal(maxBytes, limits.M); });
        const body = deferred(), cleanup = deferred();
        const terminal = body.promise.then(async value => { await cleanup.promise; return value; });
        const call = { id, hash, signal, maxBytes, body, cleanup, terminal };
        calls.push(call); record('fetch', { id, hash }); h.start?.(call);
        if (closing) { body.resolve(null); cleanup.resolve(); }
        return terminal;
      },
    };
    // Arrow capability is standalone: neither URL operation needs a receiver.
    const revoke = url => {
      tap(() => assert.ok(allocated.includes(url), 'revoke must identify a returned URL'));
      attempts.push(url); record('revoke-attempt', { id, url }); h.revoke?.(url);
      revoked.push(url); record('revoke-success', { id, url });
    };
    const URL = {
      createObjectURL(blob) {
        tap(() => { assert.ok(blob instanceof Blob); assert.ok(budget.stats.running >= 1); });
        h.create?.(blob);
        const url = `blob:cleanup-test/${allocated.length + 1}`;
        allocated.push(url); record('allocate', { id, url, size: blob.size }); h.allocate?.(url);
        return url;
      },
      revokeObjectURL: revoke,
    };
    const cache = createThumbCache({ api, URL, budget,
      beforeRevoke(hash, generation) { record('detach', { id, hash, generation }); h.before?.(hash, generation); },
      onChange(event) {
        tap(() => {
          assert.deepEqual(Object.keys(event).sort(), ['generation', 'reason', 'sha']);
          events.push({ ...event }); record('change', { id, ...event }); h.change?.(event);
        });
      },
    });
    const item = { id, h, cache, URL, revoke, events }; caches.push(item); return item;
  }
  async function finish(call, value = raster(1)) {
    assert.ok(call, 'expected a real thumbnailBlob call');
    call.body.resolve(value); call.cleanup.resolve(); await call.terminal;
    await hostTurn(); check();
  }
  t.after(async () => {
    closing = true;
    for (const item of caches) {
      for (const key of Object.keys(item.h)) delete item.h[key];
      item.URL.revokeObjectURL = item.revoke;
    }
    const releaseAll = () => {
      for (const { cache } of caches) { try { cache.release(); } catch (error) { errors.push(error); } }
    };
    releaseAll();
    for (const call of calls) { call.body.resolve(null); call.cleanup.resolve(); }
    await Promise.all(calls.map(call => call.terminal)); await hostTurn();
    releaseAll(); await hostTurn(); check();
    for (const n of Object.values(budget.stats)) assert.equal(n, 0, 'teardown must drain real accounting');
    assert.deepEqual([...revoked].sort(), [...allocated].sort(), 'one successful revoke per returned URL');
    assert.equal(legacyCalls, 0);
  });
  return { limits, budget, calls, allocated, revoked, attempts, trace, tap, check, make, finish };
}
function acquire(item, owner, n = 1) {
  return item.cache.acquire(owner, { sha256: sha(n), size: 1, media_type: 'image/png' }, undefined, { priority: 'automatic' });
}
function quota(r, expected) {
  r.check(); for (const [key, n] of Object.entries(expected)) assert.equal(r.budget.stats[key], n, key);
}
function stats(item, expected) {
  const s = item.cache.stats;
  assert.ok(Object.isFrozen(s));
  assert.deepEqual(Object.keys(s).sort(), ['created', 'entries', 'fetches', 'loading', 'owners', 'queued', 'residentBytes', 'residentEntries', 'revoked']);
  for (const n of Object.values(s)) assert.ok(Number.isSafeInteger(n) && n >= 0);
  for (const [key, n] of Object.entries(expected)) assert.equal(s[key], n, key);
}
function dormant(lease, generation) { assert.deepEqual(view(lease), { state: 'dormant', url: null, generation }); }
function throwsExact(fn, sentinel) {
  let didThrow = false, caught;
  try { fn(); } catch (error) { didThrow = true; caught = error; }
  assert.equal(didThrow, true, 'explicit cleanup retry must throw even a falsy sentinel');
  assert.equal(caught, sentinel, 'throw this retry error, not the previous async error');
}
const generationEvents = (item, generation) => item.events.filter(event => event.generation === generation).length;

test('WEB-MEM-3 T2b1 cleanup_allocation_release_throw', async t => {
  const r = fixture(t), old = r.make(), lease = acquire(old, 'old'), generation = lease.generation;
  const failure = new Error('late unpublished revoke'); let eventCount;
  old.h.allocate = () => r.tap(() => {
    assert.equal(old.cache.release(), undefined); eventCount = old.events.length;
    dormant(lease, generation); assert.ok(r.calls[0].signal.aborted);
    quota(r, { sources: 0, running: 1, reservedBytes: 4, residentEntries: 0 });
    assert.equal(r.attempts.length, 0); stats(old, { owners: 0, entries: 0, created: 0, revoked: 0 });
  });
  old.h.revoke = () => { throw failure; };
  await r.finish(r.calls[0], raster(0)); // A full host turn exposes any ignored rejecting completion.
  dormant(lease, generation); assert.equal(old.events.length, eventCount);
  quota(r, { sources: 0, running: 1, reservedBytes: 4, residentEntries: 0, residentBytes: 0 });
  stats(old, { owners: 0, entries: 0, loading: 0, created: 1, revoked: 0 });
  assert.equal(r.attempts.length, 1); assert.equal(r.trace.filter(e => e.op === 'detach').length, 0);
  assert.ok(Object.isFrozen(lease)); assert.deepEqual(Object.keys(lease).sort(), ['generation', 'state', 'url']);
  for (const key of Object.keys(lease)) {
    assert.ok(lease[key] === null || typeof lease[key] === 'string');
    assert.throws(() => { lease[key] = 'forged'; }, TypeError);
  }
  old.h.revoke = undefined; assert.equal(old.cache.release(), undefined); old.cache.release();
  quota(r, { running: 0, reservedBytes: 0, residentEntries: 0, sources: 0 });
  stats(old, { created: 1, revoked: 1 }); assert.deepEqual(r.revoked, r.allocated);
  assert.equal(r.attempts.length, 2); assert.equal(old.events.length, eventCount);
});

test('WEB-MEM-3 T2b1 cleanup_drop_live_same_hash_successor', async t => {
  const r = fixture(t, { N: 1 }), item = r.make(), old = acquire(item, 'same');
  const generation = old.generation, failure = new Error('old URL only'); let next, oldURL, oldEvents;
  item.h.allocate = url => r.tap(() => {
    item.h.allocate = undefined; oldURL = url;
    r.trace.push({ op: 'caller-detach' }); item.cache.drop('same'); oldEvents = generationEvents(item, generation);
    dormant(old, generation); quota(r, { sources: 0, running: 1, reservedBytes: 4 });
    next = acquire(item, 'same'); assert.notEqual(next, old); assert.notEqual(next.generation, generation);
    assert.equal(next.state, 'loading'); quota(r, { sources: 1, running: 2, reservedBytes: 8 });
  });
  item.h.revoke = url => { if (url === oldURL) throw failure; };
  await r.finish(r.calls[0]);
  assert.equal(next.state, 'loading'); assert.equal(next.url, null); assert.equal(r.calls.length, 2);
  quota(r, { running: 2, reservedBytes: 8, sources: 1 });
  await r.finish(r.calls[1], raster(2)); const current = view(next);
  quota(r, { running: 1, reservedBytes: 4, residentEntries: 1, residentBytes: 2, sources: 1 });
  item.cache.broken(sha(1), generation); assert.deepEqual(view(next), current);
  assert.equal(generationEvents(item, generation), oldEvents); dormant(old, generation);
  stats(item, { owners: 1, entries: 1, created: 2, revoked: 0 });
  throwsExact(() => item.cache.release(), failure); // Release legitimately retires the local successor too.
  dormant(next, current.generation); quota(r, { running: 1, residentEntries: 0, sources: 0 });
  stats(item, { owners: 0, entries: 0, created: 2, revoked: 1 });
  item.h.revoke = undefined; item.cache.release(); item.cache.release();
  quota(r, { running: 0, reservedBytes: 0, sources: 0 }); stats(item, { created: 2, revoked: 2 });
  assert.equal(r.revoked.filter(url => url === oldURL).length, 1);
});

test('WEB-MEM-3 T2b1 cleanup_explicit_falsy_retries', async t => {
  const r = fixture(t, { C: 1 }), old = r.make(), waiting = r.make();
  const retired = acquire(old, 'old'), queued = acquire(waiting, 'queued', 2), generation = retired.generation;
  let eventCount;
  old.h.allocate = () => r.tap(() => { old.cache.release(); eventCount = old.events.length; });
  old.h.revoke = () => { throw new Error('initial async failure'); };
  await r.finish(r.calls[0]);
  for (const sentinel of [undefined, null, false, 0, '', new Error('new explicit failure')]) {
    old.h.revoke = () => { throw sentinel; }; const count = r.attempts.length;
    throwsExact(() => old.cache.release(), sentinel);
    assert.equal(r.attempts.length, count + 1); dormant(retired, generation);
    assert.equal(queued.state, 'queued'); assert.equal(r.calls.length, 1);
    quota(r, { running: 1, reservedBytes: 4, queued: 1, sources: 1, residentEntries: 0 });
    stats(old, { owners: 0, entries: 0, created: 1, revoked: 0 }); assert.equal(old.events.length, eventCount);
  }
  old.h.revoke = undefined; assert.equal(old.cache.release(), undefined);
  assert.equal(queued.state, 'loading'); assert.equal(r.calls.length, 2); assert.equal(r.attempts.length, 8);
  quota(r, { running: 1, reservedBytes: 4, queued: 0, residentEntries: 0, sources: 1 });
  const revokeIndex = r.trace.findIndex(e => e.op === 'revoke-success');
  assert.ok(revokeIndex >= 0 && revokeIndex < r.trace.findIndex(e => e.op === 'fetch' && e.id === waiting.id));
  stats(old, { created: 1, revoked: 1 }); old.cache.release(); assert.equal(r.attempts.length, 8);
  await r.finish(r.calls[1]); const current = view(queued); old.cache.release();
  assert.deepEqual(view(queued), current); quota(r, { running: 0, residentEntries: 1, residentBytes: 1, sources: 1 });
});

test('WEB-MEM-3 T2b1 cleanup_cross_cache_same_hash_original_ticket', async t => {
  const r = fixture(t), old = r.make(), successor = r.make();
  const retired = acquire(old, 'same'), next = acquire(successor, 'same'); let eventCount;
  old.h.allocate = () => r.tap(() => { old.cache.release(); eventCount = old.events.length; });
  old.h.revoke = () => { throw new Error('initial'); };
  await r.finish(r.calls[0]); await r.finish(r.calls[1], raster(2));
  const current = view(next), snapshot = successor.cache.stats, count = generationEvents(successor, next.generation);
  assert.notEqual(retired.generation, next.generation);
  // Recovery must use the captured standalone capability, never reread this provider.
  old.URL.revokeObjectURL = () => { assert.fail('reread retired URL provider'); };
  const failure = new Error('explicit current failure'); old.h.revoke = () => { throw failure; };
  throwsExact(() => old.cache.release(), failure);
  quota(r, { running: 1, reservedBytes: 4, residentEntries: 1, residentBytes: 2, sources: 1 });
  assert.deepEqual(view(next), current); assert.deepEqual(successor.cache.stats, snapshot);
  stats(old, { created: 1, revoked: 0, owners: 0, entries: 0 });
  old.h.revoke = undefined; old.cache.release(); old.cache.release(); await hostTurn();
  quota(r, { running: 0, reservedBytes: 0, residentEntries: 1, residentBytes: 2, sources: 1 });
  assert.deepEqual(view(next), current); assert.deepEqual(successor.cache.stats, snapshot);
  assert.equal(generationEvents(successor, next.generation), count); assert.equal(old.events.length, eventCount);
  assert.deepEqual(r.revoked, [r.allocated[0]]); stats(old, { created: 1, revoked: 1 });
  const cleanup = r.trace.find(e => e.op === 'revoke-success' && e.id === old.id);
  assert.equal(cleanup.quota.running, 1); assert.equal(cleanup.quota.residentEntries, 1);
  successor.cache.release(); quota(r, { residentEntries: 0, residentBytes: 0, sources: 0 });
});

test('WEB-MEM-3 T2b1 cleanup_creation_throw_no_url', async t => {
  for (const mode of ['current', 'release', 'drop', 'null']) {
    const r = fixture(t), item = r.make(), lease = acquire(item, 'owner'), generation = lease.generation;
    let eventCount;
    item.h.create = () => {
      if (mode === 'release' || mode === 'drop') r.tap(() => {
        if (mode === 'release') item.cache.release(); else item.cache.drop('owner');
        eventCount = item.events.length; quota(r, { running: 1, reservedBytes: 4, sources: 0 });
      });
      throw new Error('allocation failed without returning a URL');
    };
    r.calls[0].body.resolve(mode === 'null' ? null : raster(1)); await hostTurn();
    quota(r, { running: 1, reservedBytes: 4, sources: 1 }); assert.equal(r.allocated.length, 0);
    await r.finish(r.calls[0]);
    quota(r, { running: 0, reservedBytes: 0, residentEntries: 0, residentBytes: 0 });
    stats(item, { fetches: 1, created: 0, revoked: 0 });
    if (mode === 'release' || mode === 'drop') {
      dormant(lease, generation); assert.equal(item.events.length, eventCount); quota(r, { sources: 0 });
    } else { assert.equal(lease.state, 'error'); assert.equal(lease.url, null); quota(r, { sources: 1 }); }
    assert.equal(item.cache.release(), undefined); item.cache.release();
    quota(r, { sources: 0, running: 0 }); stats(item, { owners: 0, entries: 0, created: 0, revoked: 0 });
    assert.deepEqual(r.attempts, []); assert.deepEqual(r.allocated, []);
  }
});

test('WEB-MEM-3 T2b1 cleanup_double_release_dispatch_reentry', async t => {
  const r = fixture(t, { C: 1 }), old = r.make(), waiting = r.make();
  const retired = acquire(old, 'old'), next = acquire(waiting, 'waiting', 2); let eventCount, dispatched = 0;
  old.h.allocate = () => r.tap(() => {
    assert.equal(old.cache.release(), undefined); assert.equal(old.cache.release(), undefined);
    eventCount = old.events.length; assert.equal(r.attempts.length, 0);
    quota(r, { running: 1, reservedBytes: 4, queued: 1, sources: 1 }); assert.equal(next.state, 'queued');
  });
  old.h.revoke = () => { throw new Error('late cleanup must remain reachable'); };
  await r.finish(r.calls[0], raster(0));
  waiting.h.start = () => r.tap(() => {
    dispatched++; assert.equal(next.state, 'loading'); stats(old, { created: 1, revoked: 1 });
    assert.equal(old.cache.release(), undefined); assert.equal(old.cache.release(), undefined);
    quota(r, { running: 1, queued: 0, residentEntries: 0, sources: 1 });
  });
  old.h.revoke = undefined; assert.equal(old.cache.release(), undefined); r.check();
  assert.equal(dispatched, 1); assert.equal(r.attempts.length, 2); assert.equal(r.calls.length, 2);
  assert.equal(retired.state, 'dormant'); assert.equal(retired.url, null); assert.equal(old.events.length, eventCount);
  await r.finish(r.calls[1], raster(0)); const current = view(next); old.cache.release();
  assert.deepEqual(view(next), current); quota(r, { running: 0, residentEntries: 1, residentBytes: 0, sources: 1 });
  assert.equal(old.events.length, eventCount); stats(old, { created: 1, revoked: 1 });
});

test('WEB-MEM-3 T2b1 cleanup_independent_B_C_E_pressure', async t => {
  for (const limits of [{ B: 40, C: 1, E: 8 }, { B: 4, C: 4, E: 8 }, { B: 40, C: 4, E: 1 }]) {
    const r = fixture(t, limits), old = r.make(), waiting = r.make();
    const retired = acquire(old, 'old'), next = acquire(waiting, 'next', 2); let eventCount;
    old.h.allocate = () => r.tap(() => { old.cache.release(); eventCount = old.events.length; });
    const failure = new Error('hold the limiting reservation'); old.h.revoke = () => { throw failure; };
    await r.finish(r.calls[0], raster(0));
    for (let attempt = 0; attempt < 2; attempt++) {
      throwsExact(() => old.cache.release(), failure);
      quota(r, { running: 1, reservedBytes: 4, residentEntries: 0, residentBytes: 0, queued: 1, sources: 1 });
      assert.equal(next.state, 'queued'); assert.equal(r.calls.length, 1); assert.equal(retired.url, null);
      stats(old, { owners: 0, entries: 0, created: 1, revoked: 0 }); assert.equal(old.events.length, eventCount);
    }
    old.h.revoke = undefined; old.cache.release();
    quota(r, { running: 1, reservedBytes: 4, residentEntries: 0, queued: 0, sources: 1 });
    assert.equal(next.state, 'loading'); assert.equal(r.calls.length, 2); stats(old, { created: 1, revoked: 1 });
    const success = r.trace.find(e => e.op === 'revoke-success');
    assert.equal(success.quota.reservedBytes, 4); assert.equal(success.quota.residentEntries, 0);
    await r.finish(r.calls[1], raster(0)); old.cache.release();
    quota(r, { running: 0, reservedBytes: 0, residentEntries: 1, residentBytes: 0, sources: 1 });
    assert.equal(next.state, 'ready'); waiting.cache.release();
    quota(r, { running: 0, residentEntries: 0, sources: 0 });
  }
});

test('WEB-MEM-3 T2b1 cleanup_multiple_obligations_one_release_pass', async t => {
  const r = fixture(t), item = r.make(), a = acquire(item, 'a', 1), b = acquire(item, 'b', 2);
  let allocation = 0, eventCount;
  item.h.allocate = () => r.tap(() => {
    item.cache.drop(allocation++ === 0 ? 'a' : 'b'); eventCount = item.events.length;
  });
  item.h.revoke = () => { throw new Error('async obligation'); };
  await r.finish(r.calls[0]); await r.finish(r.calls[1]);
  assert.equal(a.state, 'dormant'); assert.equal(b.state, 'dormant');
  quota(r, { running: 2, reservedBytes: 8, sources: 0, residentEntries: 0 });
  const [first, second] = r.allocated, later = new Error('second explicit failure');
  item.h.revoke = url => { throw url === first ? false : later; };
  throwsExact(() => item.cache.release(), false); assert.deepEqual(r.attempts, [first, second, first, second]);
  quota(r, { running: 2, reservedBytes: 8, sources: 0 }); stats(item, { created: 2, revoked: 0, entries: 0 });
  item.h.revoke = url => { if (url === second) throw undefined; };
  throwsExact(() => item.cache.release(), undefined);
  quota(r, { running: 1, reservedBytes: 4, sources: 0, residentEntries: 0 });
  stats(item, { created: 2, revoked: 1, owners: 0, entries: 0 }); assert.deepEqual(r.revoked, [first]);
  item.h.revoke = undefined; assert.equal(item.cache.release(), undefined); item.cache.release();
  quota(r, { running: 0, reservedBytes: 0, sources: 0, residentEntries: 0 });
  stats(item, { created: 2, revoked: 2 }); assert.deepEqual(r.revoked, [first, second]);
  assert.equal(r.attempts.filter(url => url === first).length, 3);
  assert.equal(r.attempts.filter(url => url === second).length, 4); assert.equal(item.events.length, eventCount);
  assert.equal(r.trace.filter(e => e.op === 'detach').length, 0);
});
