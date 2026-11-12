import test from 'node:test';
import assert from 'node:assert/strict';
import { createThumbCache, retryThumbCleanup } from '../static/thumbs-model.js';
import { createThumbBudget } from '../static/thumbs-budget.js';

const BASE = { B: 16, C: 2, E: 8, N: 8, Q: 4, M: 4 };
const sha = n => n.toString(16).padStart(64, '0');
const raster = n => new Blob([new Uint8Array(n)], { type: 'image/png' });
const view = lease => ({ state: lease.state, url: lease.url, generation: lease.generation });
// Let node:test observe ignored rejecting completion chains; no process-level swallow.
const hostTurn = () => new Promise(resolve => setImmediate(resolve));
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function equations(s) {
  const q = s.budget.stats, l = s.limits;
  for (const n of Object.values(q)) assert.ok(Number.isSafeInteger(n) && n >= 0);
  assert.equal(q.reservedBytes, l.M * q.running);
  assert.ok(q.residentBytes + q.reservedBytes <= l.B);
  assert.ok(q.running <= l.C && q.running + q.residentEntries <= l.E);
  assert.ok(q.queued <= l.Q && q.sources <= l.N);
  assert.equal(s.created, s.revoked + s.urls.size);
}
function tap(s, fn) {
  try { equations(s); return fn(); } catch (error) { s.errors.push(error); }
}
// These capability factories receive no cache handle or live-cache teardown registry.
function apiFor(s, id, h) {
  return {
    blob() { s.legacy++; throw new Error('lease mode must not call api.blob'); },
    thumbnailBlob(hash, { signal, maxBytes }) {
      tap(s, () => { assert.ok(signal instanceof AbortSignal); assert.equal(maxBytes, s.limits.M); });
      const body = deferred(), cleanup = deferred();
      const terminal = body.promise.then(async value => { await cleanup.promise; return value; },
        async error => { await cleanup.promise; throw error; });
      const call = { id, hash, signal, body, cleanup, terminal, aborts: 0 };
      s.calls.push(call); signal.addEventListener('abort', () => { call.aborts++; });
      h.start?.(call);
      if (s.closing) { body.resolve(null); cleanup.resolve(); }
      return terminal;
    },
  };
}
function urlFor(s, id, h, meter) {
  const revoke = url => {
    tap(s, () => assert.ok(s.urls.has(url), 'only a returned, unreclaimed URL can be revoked'));
    const record = s.urls.get(url); s.attempts++;
    if (record) { record.attempts++; record.attemptSeq = ++s.seq; }
    h.revoke?.(url);
    s.urls.delete(url); s.revoked++; meter.revoked++;
  };
  return { revoke, URL: {
    createObjectURL(blob) {
      tap(s, () => { assert.ok(blob instanceof Blob); assert.ok(s.budget.stats.running > 0); });
      h.create?.();
      const url = `blob:cleanup-owner/${id}/${++s.created}`;
      s.urls.set(url, { id, size: blob.size, attempts: 0, attemptSeq: 0 });
      h.allocate?.(url); return url;
    },
    revokeObjectURL: revoke,
  } };
}
function observers(s, h, meter) {
  return {
    beforeRevoke() { meter.detaches++; meter.detachSeq = ++s.seq; h.before?.(); },
    onChange() { tap(s, () => { meter.events++; h.change?.(); }); },
  };
}
function fixture(t, patch = {}) {
  const limits = { ...BASE, ...patch }, budget = createThumbBudget({ limits });
  const s = { limits, budget, calls: [], urls: new Map(), errors: [], created: 0, revoked: 0, attempts: 0, seq: 0, legacy: 0, closing: false };
  const live = new Map(), controls = []; let serial = 0;
  const check = () => { equations(s); assert.deepEqual(s.errors, [], 'assertions must survive callback containment'); };
  function make() {
    const id = ++serial, h = {}, meter = { events: 0, detaches: 0, detachSeq: 0, revoked: 0 };
    const { URL, revoke } = urlFor(s, id, h, meter);
    const cache = createThumbCache({ api: apiFor(s, id, h), URL, budget, ...observers(s, h, meter) });
    const item = { id, h, meter, URL, cache }; live.set(id, item); controls.push({ h, URL, revoke }); return item;
  }
  function forget(item) {
    for (const key of ['allocate', 'create', 'start', 'before', 'change']) delete item.h[key];
    live.delete(item.id); item.cache = null; // No retained release callback in fixture teardown.
  }
  async function finish(call, value = raster(1), reject = false) {
    assert.ok(call, 'expected actual thumbnailBlob dispatch');
    if (reject) call.body.reject(value); else call.body.resolve(value);
    call.cleanup.resolve(); const [result] = await Promise.allSettled([call.terminal]);
    assert.equal(result.status, reject ? 'rejected' : 'fulfilled'); await hostTurn(); check();
  }
  t.after(async () => {
    s.closing = true;
    for (const { h, URL, revoke } of controls) {
      for (const key of Object.keys(h)) delete h[key]; URL.revokeObjectURL = revoke;
    }
    const release = () => {
      for (const item of live.values()) { try { item.cache.release(); } catch (error) { s.errors.push(error); } }
    };
    release();
    for (const call of s.calls) { call.body.resolve(null); call.cleanup.resolve(); }
    await Promise.allSettled(s.calls.map(call => call.terminal)); await hostTurn(); release();
    try { retryThumbCleanup(budget); } catch (error) { s.errors.push(error); }
    await hostTurn(); check();
    for (const n of Object.values(budget.stats)) assert.equal(n, 0, 'teardown drains real quota');
    assert.equal(s.urls.size, 0); assert.equal(s.created, s.revoked); assert.equal(s.legacy, 0);
  });
  return { s, budget, make, forget, finish, check, tap: fn => tap(s, fn) };
}
function acquire(item, key, n = 1, priority = 'automatic') {
  return item.cache.acquire(key, { sha256: sha(n), size: 1, media_type: 'image/png' }, undefined, { priority });
}
function quota(r, expected) {
  r.check(); for (const [key, n] of Object.entries(expected)) assert.equal(r.budget.stats[key], n, key);
}
function stats(item, expected) {
  const snapshot = item.cache.stats; assert.ok(Object.isFrozen(snapshot));
  for (const [key, n] of Object.entries(expected)) assert.equal(snapshot[key], n, key);
}
function throwsExact(fn, sentinel) {
  let threw = false, value;
  try { fn(); } catch (error) { threw = true; value = error; }
  assert.ok(threw, 'this explicit pass must throw its current error, even if falsy'); assert.equal(value, sentinel);
}
function retireOnAllocation(r, item) {
  item.h.allocate = () => r.tap(() => item.cache.release());
  item.h.revoke = () => { throw new Error('initial unpublished failure'); };
}

test('WEB-MEM-3 T2b3 late_retirement_discard_then_document_retry', async t => {
  const r = fixture(t, { C: 1 }), unused = createThumbBudget({ limits: BASE });
  const idle = unused.stats; assert.equal(retryThumbCleanup(unused), undefined); assert.deepEqual(unused.stats, idle);
  let old = r.make(); const h = old.h, meter = old.meter, lease = acquire(old, 'old'), generation = lease.generation;
  const next = r.make(), waiting = acquire(next, 'waiting', 2, 'explicit'); let retiredEvents;
  h.allocate = () => r.tap(() => {
    assert.equal(old.cache.release(), undefined); retiredEvents = meter.events;
    assert.ok(r.s.calls[0].signal.aborted); assert.equal(r.s.calls[0].aborts, 1);
    r.forget(old); old = null;
    quota(r, { running: 1, reservedBytes: 4, sources: 1, queued: 1 }); assert.equal(r.s.attempts, 0);
  });
  h.revoke = () => { throw new Error('late failure after retirement returned'); };
  await r.finish(r.s.calls[0], raster(0));
  assert.equal(old, null); assert.deepEqual(view(lease), { state: 'dormant', url: null, generation });
  assert.equal(meter.events, retiredEvents); assert.equal(meter.detaches, 0); assert.equal(r.s.urls.size, 1);
  const held = r.budget.stats, attempts = r.s.attempts;
  for (const bad of [undefined, null, false, 0, 'budget', {}, { stats: held }, { enqueue() {}, claimSource() {} }]) {
    assert.throws(() => retryThumbCleanup(bad), TypeError); assert.deepEqual(r.budget.stats, held);
    assert.equal(r.s.attempts, attempts);
  }
  retryThumbCleanup(unused); assert.deepEqual(r.budget.stats, held); assert.equal(r.s.attempts, attempts);
  const current = new Error('current document retry'); h.revoke = () => { throw current; };
  throwsExact(() => retryThumbCleanup(r.budget), current);
  assert.equal(waiting.state, 'queued'); assert.equal(r.s.calls.length, 1); assert.equal(meter.events, retiredEvents);
  h.revoke = undefined; assert.equal(retryThumbCleanup(r.budget), undefined);
  await hostTurn(); r.check();
  quota(r, { running: 1, reservedBytes: 4, queued: 0, residentEntries: 0, sources: 1 });
  assert.equal(waiting.state, 'loading'); assert.equal(r.s.calls.length, 2); assert.equal(r.s.calls[1].hash, sha(2));
  assert.equal(meter.revoked, 1); assert.equal(meter.events, retiredEvents); assert.equal(r.s.urls.size, 0);
  const done = r.s.attempts; retryThumbCleanup(r.budget); assert.equal(r.s.attempts, done);
  await r.finish(r.s.calls[1]); assert.equal(waiting.state, 'ready'); assert.equal(meter.events, retiredEvents);
});

test('WEB-MEM-3 T2b3 scoped_release_and_cross_cache_same_hash', async t => {
  const r = fixture(t, { B: 40, C: 4 }), a = r.make(), b = r.make(), next = r.make();
  const first = acquire(a, 'same'), second = acquire(b, 'same'), successor = acquire(next, 'same');
  retireOnAllocation(r, a); retireOnAllocation(r, b);
  await r.finish(r.s.calls[0]); await r.finish(r.s.calls[2], raster(2)); await r.finish(r.s.calls[1]);
  const stable = view(successor), nextStats = next.cache.stats, aEvents = a.meter.events, bEvents = b.meter.events;
  assert.notEqual(first.generation, successor.generation); assert.notEqual(second.generation, successor.generation);
  const aURL = [...r.s.urls].find(([, record]) => record.id === a.id)[0];
  const bURL = [...r.s.urls].find(([, record]) => record.id === b.id)[0];
  const current = new Error('only a scoped failure'); a.h.revoke = () => { throw current; };
  let attempts = r.s.attempts, bAttempts = r.s.urls.get(bURL).attempts;
  throwsExact(() => a.cache.release(), current); assert.equal(r.s.attempts, attempts + 1);
  assert.equal(r.s.urls.get(bURL).attempts, bAttempts); quota(r, { running: 2, reservedBytes: 8, residentEntries: 1, residentBytes: 2 });
  a.h.revoke = undefined; a.cache.release(); assert.equal(r.s.urls.has(aURL), false);
  stats(a, { created: 1, revoked: 1, owners: 0, entries: 0 }); stats(b, { created: 1, revoked: 0 });
  assert.deepEqual(view(successor), stable); assert.deepEqual(next.cache.stats, nextStats);
  b.h.revoke = () => { throw false; }; throwsExact(() => retryThumbCleanup(r.budget), false);
  b.h.revoke = undefined; retryThumbCleanup(r.budget); stats(b, { created: 1, revoked: 1, entries: 0 });
  quota(r, { running: 0, reservedBytes: 0, residentEntries: 1, residentBytes: 2, sources: 1 });
  attempts = r.s.attempts; a.cache.release(); b.cache.release(); retryThumbCleanup(r.budget);
  await hostTurn(); r.check();
  assert.equal(r.s.attempts, attempts); assert.deepEqual(view(successor), stable); assert.deepEqual(next.cache.stats, nextStats);
  assert.equal(a.meter.events, aEvents); assert.equal(b.meter.events, bEvents); assert.equal(r.s.urls.has(stable.url), true);
});

test('WEB-MEM-3 T2b3 allocating_ready_and_no_url_transfer', async t => {
  const r = fixture(t, { C: 1 }), first = r.make(), next = r.make();
  const ready = acquire(first, 'first'), waiting = acquire(next, 'next', 2);
  first.h.allocate = () => r.tap(() => {
    retryThumbCleanup(r.budget); quota(r, { running: 1, reservedBytes: 4, residentEntries: 0, queued: 1 });
    assert.equal(r.s.attempts, 0); assert.equal(ready.state, 'loading');
  });
  next.h.start = () => r.tap(() => {
    assert.equal(ready.state, 'ready'); assert.ok(ready.url); stats(first, { created: 1, residentEntries: 1, residentBytes: 0 });
    retryThumbCleanup(r.budget); assert.equal(r.s.attempts, 0); assert.equal(r.s.urls.has(ready.url), true);
  });
  await r.finish(r.s.calls[0], raster(0)); assert.equal(waiting.state, 'loading'); assert.equal(r.s.calls.length, 2);
  retryThumbCleanup(r.budget); quota(r, { running: 1, residentEntries: 1, residentBytes: 0 });
  await r.finish(r.s.calls[1], null); first.cache.release(); assert.equal(r.s.revoked, 1);
  for (const mode of ['current', 'release', 'drop', 'reject']) {
    const f = fixture(t, { C: 1 }), item = f.make(), queued = f.make();
    const lease = acquire(item, 'old'), generation = lease.generation, later = acquire(queued, 'later', 2);
    let retiredEvents;
    item.h.create = () => {
      if (mode === 'release' || mode === 'drop') f.tap(() => {
        if (mode === 'release') item.cache.release(); else item.cache.drop('old'); retiredEvents = item.meter.events;
      });
      throw new Error('no returned URL');
    };
    const call = f.s.calls[0], value = mode === 'reject' ? new Error('body failure') : raster(0);
    if (mode === 'reject') call.body.reject(value); else call.body.resolve(value);
    await hostTurn(); quota(f, { running: 1, reservedBytes: 4, queued: 1 }); assert.equal(f.s.created, 0);
    await f.finish(call, value, mode === 'reject');
    assert.equal(later.state, 'loading'); assert.equal(f.s.calls.length, 2); assert.equal(f.s.created, 0);
    retryThumbCleanup(f.budget); retryThumbCleanup(f.budget);
    quota(f, { running: 1, reservedBytes: 4, residentEntries: 0 }); assert.equal(f.s.attempts, 0);
    if (mode === 'release' || mode === 'drop') {
      assert.deepEqual(view(lease), { state: 'dormant', url: null, generation }); assert.equal(item.meter.events, retiredEvents);
    } else { assert.equal(lease.state, 'error'); assert.equal(lease.url, null); }
    await f.finish(f.s.calls[1], raster(0)); assert.equal(later.state, 'ready');
  }
});

test('WEB-MEM-3 T2b3 failures_falsy_partial_and_reentry', async t => {
  const r = fixture(t), item = r.make(), next = r.make();
  const a = acquire(item, 'a', 1), b = acquire(item, 'b', 2), waiting = acquire(next, 'next', 3, 'explicit');
  let allocation = 0;
  item.h.allocate = () => r.tap(() => item.cache.drop(allocation++ === 0 ? 'a' : 'b'));
  item.h.revoke = () => { throw new Error('initial async error'); };
  await r.finish(r.s.calls[0]); await r.finish(r.s.calls[1]);
  const [firstURL, secondURL] = r.s.urls.keys(), eventCount = item.meter.events;
  for (const sentinel of [undefined, null, false, 0, '', new Error('new current error')]) {
    const later = new Error('second current error');
    item.h.revoke = url => {
      r.tap(() => {
        const attempts = r.s.attempts; retryThumbCleanup(r.budget); assert.equal(r.s.attempts, attempts);
        for (const mutate of [() => acquire(item, 'forbidden', 9), () => item.cache.drop('a'),
          () => item.cache.moveOwner('a', 'x', { priority: 'explicit' }), () => item.cache.retry('a'),
          () => item.cache.broken(sha(1), a.generation), () => item.cache.release()]) assert.throws(mutate, Error);
      });
      throw url === firstURL ? sentinel : later;
    };
    const attempts = r.s.attempts, x = r.s.urls.get(firstURL).attempts, y = r.s.urls.get(secondURL).attempts;
    throwsExact(() => retryThumbCleanup(r.budget), sentinel);
    assert.equal(r.s.attempts, attempts + 2); assert.equal(r.s.urls.get(firstURL).attempts, x + 1);
    assert.equal(r.s.urls.get(secondURL).attempts, y + 1);
    quota(r, { running: 2, reservedBytes: 8, queued: 1, sources: 1 }); assert.equal(r.s.calls.length, 2);
  }
  let dispatched = 0;
  next.h.start = () => r.tap(() => {
    dispatched++; const attempts = r.s.attempts;
    stats(item, { created: 2, revoked: 1 }); assert.equal(item.cache.release(), undefined);
    retryThumbCleanup(r.budget); assert.equal(r.s.attempts, attempts); assert.equal(waiting.state, 'loading');
  });
  item.h.revoke = url => { if (url === firstURL) throw false; };
  const attempts = r.s.attempts; throwsExact(() => retryThumbCleanup(r.budget), false);
  assert.equal(r.s.attempts, attempts + 2); assert.equal(dispatched, 1); assert.equal(r.s.calls.length, 3);
  assert.equal(r.s.urls.has(firstURL), true); assert.equal(r.s.urls.has(secondURL), false);
  quota(r, { running: 2, reservedBytes: 8, queued: 0, sources: 1 }); stats(item, { created: 2, revoked: 1 });
  item.h.revoke = undefined; retryThumbCleanup(r.budget); stats(item, { created: 2, revoked: 2 });
  quota(r, { running: 1, reservedBytes: 4 }); assert.equal(item.meter.events, eventCount);
  assert.equal(a.state, 'dormant'); assert.equal(b.state, 'dormant');
  await r.finish(r.s.calls[2], raster(0)); assert.equal(waiting.state, 'ready');
});

test('WEB-MEM-3 T2b3 independent_B_C_E_pressure_and_alias_bound', async t => {
  const axes = [{ B: 64, C: 2, E: 16 }, { B: 8, C: 8, E: 16 }, { B: 64, C: 8, E: 2 }];
  for (const limits of axes) for (const reverse of [false, true]) for (const size of [0, 1]) {
    const r = fixture(t, limits), old = [r.make(), r.make()], next = r.make();
    old.forEach((item, i) => { acquire(item, 'old', i + 1); retireOnAllocation(r, item); });
    const waiting = acquire(next, 'next', 3, 'explicit');
    for (let i = 0; i < 8; i++) {
      const transient = r.make(), lease = acquire(transient, 'transient', 4); transient.cache.release(); r.forget(transient);
      assert.equal(lease.state, 'dormant'); assert.equal(r.s.calls.length, 2);
    }
    quota(r, { running: 2, reservedBytes: 8, queued: 1, sources: 3 });
    const order = reverse ? [1, 0] : [0, 1];
    for (const i of order) {
      const call = r.s.calls[i]; call.body.resolve(raster(size)); await hostTurn();
      quota(r, { running: 2, reservedBytes: 8, queued: 1 }); assert.equal(r.s.calls.length, 2);
      await r.finish(call, raster(size)); assert.equal(call.signal.aborted, true); assert.equal(call.aborts, 1);
      assert.equal(waiting.state, 'queued'); assert.equal(r.s.calls.length, 2);
    }
    const events = old.map(item => item.meter.events), failure = new Error('no capacity until real disposal');
    old.forEach(item => { item.h.revoke = () => { throw failure; }; });
    throwsExact(() => retryThumbCleanup(r.budget), failure);
    quota(r, { running: 2, reservedBytes: 8, residentEntries: 0, residentBytes: 0, queued: 1, sources: 1 });
    assert.equal(waiting.state, 'queued'); assert.equal(r.s.calls.length, 2);
    const releasedURL = [...r.s.urls].find(([, record]) => record.id === old[order[0]].id)[0];
    old[order[0]].h.revoke = () => r.tap(() => {
      assert.equal(r.s.calls.length, 2); quota(r, { running: 2, reservedBytes: 8 });
    });
    next.h.start = () => r.tap(() => {
      assert.equal(r.s.urls.has(releasedURL), false); stats(old[order[0]], { created: 1, revoked: 1 });
      quota(r, { running: 2, reservedBytes: 8, residentEntries: 0 });
    });
    throwsExact(() => retryThumbCleanup(r.budget), failure);
    assert.equal(waiting.state, 'loading'); assert.equal(r.s.calls.length, 3); assert.equal(r.s.calls[2].hash, sha(3));
    stats(old[order[0]], { created: 1, revoked: 1 }); stats(old[order[1]], { created: 1, revoked: 0 });
    quota(r, { running: 2, reservedBytes: 8, residentEntries: 0, queued: 0, sources: 1 });
    old[order[1]].h.revoke = undefined; retryThumbCleanup(r.budget); quota(r, { running: 1, reservedBytes: 4 });
    await r.finish(r.s.calls[2], raster(size));
    quota(r, { running: 0, reservedBytes: 0, residentEntries: 1, residentBytes: size, sources: 1 });
    assert.deepEqual(old.map(item => item.meter.events), events); assert.equal(waiting.state, 'ready');
  }
  // Reusing one live cache exposes stale alias effects; Set/capture bounds still need source audit.
  const r = fixture(t, { B: 4, C: 1 }), item = r.make();
  item.h.allocate = () => r.tap(() => item.cache.drop('slot'));
  for (let i = 0; i < 16; i++) {
    item.h.revoke = () => { throw new Error('current round only'); };
    const lease = acquire(item, 'slot'), call = r.s.calls[i]; await r.finish(call, raster(i % 2));
    quota(r, { running: 1, reservedBytes: 4, sources: 0 }); assert.equal(lease.state, 'dormant');
    item.h.revoke = undefined; retryThumbCleanup(r.budget); stats(item, { created: i + 1, revoked: i + 1 });
    quota(r, { running: 0, reservedBytes: 0, sources: 0 }); assert.equal(r.s.urls.size, 0);
    const attempts = r.s.attempts; retryThumbCleanup(r.budget); assert.equal(r.s.attempts, attempts);
  }
  item.cache.release(); assert.equal(r.s.created, 16); assert.equal(r.s.revoked, 16);
});

test('WEB-MEM-3 T2b3 capability_and_retention_contract', async t => {
  const r = fixture(t); let old = r.make();
  const h = old.h, meter = old.meter, lease = acquire(old, 'old'), generation = lease.generation;
  retireOnAllocation(r, old); await r.finish(r.s.calls[0]); const events = meter.events;
  let providerReads = 0;
  old.URL.revokeObjectURL = () => { providerReads++; throw new Error('retired provider must not be reread'); };
  r.forget(old); old = null;
  assert.ok(Object.isFrozen(lease)); assert.deepEqual(Object.keys(lease).sort(), ['generation', 'state', 'url']);
  for (const key of Object.keys(lease)) {
    assert.ok(lease[key] === null || typeof lease[key] === 'string'); assert.throws(() => { lease[key] = 'forged'; }, TypeError);
  }
  h.revoke = undefined; retryThumbCleanup(r.budget); await hostTurn(); r.check(); assert.equal(providerReads, 0);
  quota(r, { running: 0, reservedBytes: 0, sources: 0, residentEntries: 0 });
  assert.equal(meter.revoked, 1); assert.equal(meter.detaches, 0); assert.equal(meter.events, events);
  assert.deepEqual(view(lease), { state: 'dormant', url: null, generation });
  const attempts = r.s.attempts; retryThumbCleanup(r.budget); assert.equal(r.s.attempts, attempts);
  // Scalar lease/capability effects do not prove graph collection or installed viewer Retry wiring.
});

test('WEB-MEM-3 T2b3 published_failure_contract_unchanged', async t => {
  for (const failAt of ['before', 'revoke']) {
    const r = fixture(t), item = r.make(), lease = acquire(item, 'published'); await r.finish(r.s.calls[0], raster(2));
    const url = lease.url, failure = failAt === 'before' ? false : undefined;
    if (failAt === 'before') item.h.before = () => { throw failure; }; else item.h.revoke = () => { throw failure; };
    throwsExact(() => item.cache.release(), failure);
    quota(r, { running: 0, reservedBytes: 0, residentEntries: 1, residentBytes: 2, sources: 1 });
    stats(item, { created: 1, revoked: 0, owners: 1, entries: 1 }); assert.equal(lease.url, null);
    const attempts = r.s.attempts, detaches = item.meter.detaches;
    retryThumbCleanup(r.budget); retryThumbCleanup(r.budget);
    assert.equal(r.s.attempts, attempts); assert.equal(item.meter.detaches, detaches); assert.equal(r.s.urls.has(url), true);
    quota(r, { residentEntries: 1, residentBytes: 2, sources: 1 });
    item.h.before = undefined; item.h.revoke = () => r.tap(() => {
      assert.ok(item.meter.detaches > detaches); assert.ok(item.meter.detachSeq < r.s.urls.get(url).attemptSeq);
      assert.throws(() => item.cache.broken(sha(1), lease.generation), Error);
      quota(r, { residentEntries: 1, residentBytes: 2, sources: 1 });
    });
    item.cache.release(); stats(item, { created: 1, revoked: 1, owners: 0, entries: 0 });
    quota(r, { running: 0, residentEntries: 0, residentBytes: 0, sources: 0 });
    const done = r.s.attempts; item.cache.release(); retryThumbCleanup(r.budget); assert.equal(r.s.attempts, done);
  }
});
