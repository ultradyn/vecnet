import test from 'node:test';
import assert from 'node:assert/strict';
import { createThumbCache, THUMB_MAX } from '../static/thumbs-model.js';
import { createThumbBudget } from '../static/thumbs-budget.js';

const BASE = { B: 8, C: 2, E: 4, N: 8, Q: 2, M: 4 };
const sha = n => n.toString(16).padStart(64, '0');
const att = (n, patch = {}) => ({ sha256: sha(n), size: 1, media_type: 'image/png', ...patch });
const raster = (size = 1, type = 'image/png') => new Blob([new Uint8Array(size)], { type });
const flush = async () => {
  await Promise.resolve(); await Promise.resolve();
  await Promise.resolve(); await Promise.resolve();
};
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const view = lease => ({ state: lease.state, url: lease.url, generation: lease.generation });
function fixture(t, overrides = {}) {
  const limits = { ...BASE, ...overrides }, budget = createThumbBudget({ limits });
  const calls = [], allocated = [], revoked = [], log = [], errors = [], caches = [], hooks = {};
  let closing = false, legacyCalls = 0;
  function check() {
    const s = budget.stats;
    for (const value of Object.values(s)) assert.ok(Number.isSafeInteger(value) && value >= 0);
    assert.equal(s.reservedBytes, limits.M * s.running);
    assert.ok(s.residentBytes + s.reservedBytes <= limits.B);
    assert.ok(s.running <= limits.C && s.running + s.residentEntries <= limits.E);
    assert.ok(s.queued <= limits.Q && s.sources <= limits.N);
  }
  // Persist assertions outside containment by cache/budget notification code.
  const tap = fn => { try { check(); fn(); } catch (error) { errors.push(error); } };
  const api = {
    blob() { legacyCalls++; throw new Error('Forbidden api.blob in lease mode'); },
    thumbnailBlob(hash, { signal, maxBytes }) {
      tap(() => { assert.ok(signal instanceof AbortSignal); assert.equal(maxBytes, limits.M); assert.ok(budget.stats.running >= 1); });
      const body = deferred(), cleanup = deferred();
      const terminal = body.promise.then(async value => { await cleanup.promise; return value; },
        async error => { await cleanup.promise; throw error; });
      terminal.catch(() => {}); // Fixture owns rejection even if a RED implementation never subscribes.
      const call = { hash, signal, maxBytes, body, cleanup, terminal, aborts: 0, stats: budget.stats };
      calls.push(call);
      signal.addEventListener('abort', () => { call.aborts++; }); // Abort does NOT settle body or cleanup.
      hooks.start?.(call);
      if (closing) { body.resolve(null); cleanup.resolve(); }
      return terminal;
    },
  };
  const URL = {
    createObjectURL(blob) {
      tap(() => {
        assert.ok(blob instanceof Blob);
        assert.ok(budget.stats.running >= 1, 'URL creation remains under the running reservation');
      });
      if (hooks.createError) throw hooks.createError;
      const url = `blob:cache-test/${allocated.length + 1}`;
      allocated.push({ url, blob, stats: budget.stats });
      hooks.allocate?.(url);
      return url;
    },
    revokeObjectURL(url) {
      tap(() => assert.ok(allocated.some(item => item.url === url)));
      log.push({ op: 'revoke', url, stats: budget.stats });
      hooks.revoke?.(url);
      revoked.push(url);
    },
  };
  function create(h = {}) {
    const events = []; let cache;
    const options = { api, URL, budget,
      beforeRevoke(hash, generation) {
        tap(() => assert.equal(typeof generation, 'string'));
        log.push({ op: 'detach', hash, generation, stats: budget.stats });
        h.before?.(hash, generation);
      },
      onChange(event) {
        tap(() => {
          assert.deepEqual(Object.keys(event).sort(), ['generation', 'reason', 'sha']);
          assert.ok(['state', 'displaced', 'capacity'].includes(event.reason));
          if (event.reason === 'capacity') { assert.equal(event.sha, null); assert.equal(event.generation, null); }
          else { assert.match(event.sha, /^[0-9a-f]{64}$/); assert.ok(event.generation === null || typeof event.generation === 'string'); }
          events.push({ ...event });
          h.change?.(event, cache);
        });
        if (h.throwChange) throw h.throwChange;
      } };
    cache = createThumbCache(options);
    caches.push({ cache, h });
    return { cache, events, options, h };
  }
  async function complete(call, value = raster(), failed = false) {
    assert.ok(call, 'expected a thumbnailBlob invocation');
    if (failed) call.body.reject(value); else call.body.resolve(value);
    call.cleanup.resolve();
    await Promise.allSettled([call.terminal]);
    await flush(); check();
  }
  const drop = (cache, owner) => { log.push({ op: 'caller-detach', owner }); return cache.drop(owner); };
  t.after(async () => {
    closing = true;
    for (const key of Object.keys(hooks)) delete hooks[key];
    for (const { h } of caches) { h.before = h.change = h.throwChange = undefined; }
    try { for (const { cache } of caches) cache.release(); }
    finally {
      for (const call of calls) { call.body.resolve(null); call.cleanup.resolve(); }
      await Promise.allSettled(calls.map(call => call.terminal)); await flush();
    }
    check();
    for (const value of Object.values(budget.stats)) assert.equal(value, 0, 'cleanup left budget ownership');
    assert.deepEqual([...revoked].sort(), allocated.map(item => item.url).sort(), 'each allocated URL revoked exactly once');
    assert.equal(legacyCalls, 0);
    assert.deepEqual(errors, []);
  });
  return { budget, calls, allocated, revoked, log, hooks, api, URL, tap, check, create, complete, drop };
}
function stats(cache, expected) {
  const s = cache.stats;
  assert.ok(Object.isFrozen(s));
  assert.deepEqual(Object.keys(s).sort(), ['created', 'entries', 'fetches', 'loading', 'owners', 'queued', 'residentBytes', 'residentEntries', 'revoked']);
  for (const value of Object.values(s)) assert.ok(Number.isSafeInteger(value) && value >= 0);
  for (const [key, value] of Object.entries(expected)) assert.equal(s[key], value, key);
}
function quota(r, expected) {
  r.check(); for (const [key, value] of Object.entries(expected)) assert.equal(r.budget.stats[key], value, key);
}
function acquire(cache, owner, n, priority = 'automatic', patch = {}, hosted) {
  const lease = cache.acquire(owner, att(n, patch), hosted, { priority });
  if (lease) {
    assert.ok(['dormant', 'waiting', 'queued', 'loading', 'ready', 'error'].includes(lease.state));
    if (lease.state !== 'ready') assert.equal(lease.url, null);
    assert.ok(lease.generation === null || typeof lease.generation === 'string');
  }
  return lease;
}

test('WEB-MEM-3 T2b1 constructor_legacy_isolation', async t => {
  const r = fixture(t), { cache, options } = r.create();
  for (const name of ['acquire', 'drop', 'broken', 'release']) assert.equal(typeof cache[name], 'function', name);
  assert.equal(cache.state, undefined); assert.equal(cache.onReady, undefined);
  stats(cache, { owners: 0, entries: 0, fetches: 0, created: 0, revoked: 0 }); assert.equal(r.calls.length, 0);
  for (const patch of [{ api: {} }, { URL: {} }, { budget: null }, { budget: {} }, { beforeRevoke: null }, { onChange: 1 }])
    assert.throws(() => createThumbCache({ ...options, ...patch }), TypeError);
  assert.equal(r.calls.length, 0); quota(r, { sources: 0, running: 0 });
  for (const args of [['', att(1), undefined, { priority: 'automatic' }], ['x', att(1), undefined, { priority: 'viewer' }], ['x', att(1), undefined, null]])
    assert.throws(() => cache.acquire(...args), TypeError);
  const old = cache.stats;
  assert.throws(() => { old.owners = 9; }, TypeError);
  assert.throws(() => { cache.stats = {}; }, TypeError);
  acquire(cache, 'owner', 1); await flush();
  assert.equal(old.owners, 0); stats(cache, { owners: 1, loading: 1, fetches: 1 });
  const legacy = createThumbCache({ api: r.api, URL: r.URL, onReady() {} });
  assert.equal(typeof legacy.state, 'function'); assert.equal(typeof legacy.broken, 'function');
  assert.deepEqual(legacy.state(att(2, { media_type: 'image/svg+xml' })), { state: 'none', url: null }); legacy.release();
  assert.equal(cache.release(), undefined); assert.equal(acquire(cache, 'late', 2), null); assert.equal(cache.drop('missing'), undefined);
});

test('WEB-MEM-3 T2b1 eligibility_before_sharing', async t => {
  const r = fixture(t), { cache } = r.create(), live = acquire(cache, 'eligible', 1);
  for (const [index, patch, hosted] of [[0, { media_type: 'image/svg+xml' }], [1, { size: THUMB_MAX + 1 }],
    [2, { size: -1 }], [3, { sha256: '../bad' }], [4, { origin_url: 'https://invalid.test/image' }], [5, {}, false], [6, { size: undefined }]])
    assert.equal(acquire(cache, `bad${index}`, 1, 'automatic', patch, hosted), null);
  assert.equal(r.calls.length, 1); quota(r, { sources: 1 });
  const hosted = acquire(cache, 'hosted', 2, 'automatic', { origin_url: 'https://invalid.test/image' }, true);
  assert.ok(hosted); assert.ok(acquire(cache, 'blob-url', 3, 'automatic', { blob_url: 'never-fetch-this', origin_url: 'also-never-fetch', size: THUMB_MAX }, false));
  assert.deepEqual(r.calls.map(call => call.hash), [sha(1), sha(2)]); // URLs are metadata, never request targets.
  r.log.push({ op: 'caller-detach', owner: 'eligible' });
  assert.equal(acquire(cache, 'eligible', 1, 'automatic', { media_type: 'text/html' }), null);
  assert.equal(live.state, 'dormant'); assert.equal(live.url, null); assert.ok(r.calls[0].signal.aborted);
  assert.throws(() => acquire(cache, 'hosted', 9), TypeError); assert.equal(hosted.state, 'loading');
  stats(cache, { owners: 2 }); assert.equal(r.allocated.length, 0);
});

test('WEB-MEM-3 T2b1 bounded_transport_and_reservation', async t => {
  for (const limits of [{ B: 40, C: 1, E: 8 }, { B: 4, C: 4, E: 8 }, { B: 40, C: 4, E: 1 }, { B: 5, C: 4, E: 8 }]) {
    const r = fixture(t, limits), { cache } = r.create(), a = acquire(cache, 'a', 1), b = acquire(cache, 'b', 2);
    await flush(); assert.equal(a.state, 'loading'); assert.equal(b.state, 'queued'); assert.equal(r.calls.length, 1);
    assert.equal(r.calls[0].maxBytes, 4); quota(r, { running: 1, reservedBytes: 4, queued: 1 });
    await r.complete(r.calls[0], raster(limits.B === 5 ? 2 : 0)); assert.equal(a.state, 'ready');
    if (limits.E === 1 || limits.B === 5) {
      assert.equal(b.state, 'queued'); quota(r, { residentEntries: 1, running: 0 });
    }
    r.drop(cache, 'a'); await flush(); assert.equal(b.state, 'loading'); assert.equal(r.calls.length, 2);
    await r.complete(r.calls[1]); quota(r, { running: 0, residentEntries: 1, residentBytes: 1 });
  }
});

test('WEB-MEM-3 T2b1 duplicate_hash_one_url_many_permits', async t => {
  const r = fixture(t, { N: 2 }), { cache } = r.create(), a = acquire(cache, 'a', 1), b = acquire(cache, 'b', 1);
  assert.equal(acquire(cache, 'a', 1), a); assert.notEqual(a, b); assert.equal(r.calls.length, 1); quota(r, { sources: 2 });
  assert.equal(acquire(cache, 'third', 2), null); assert.throws(() => { a.state = 'error'; }, TypeError);
  assert.throws(() => { a.url = 'blob:forged'; }, TypeError);
  assert.throws(() => { a.generation = 'forged'; }, TypeError);
  assert.equal(a.blob, undefined);
  await r.complete(r.calls[0], raster(0)); assert.equal(a.url, b.url); assert.ok(a.url); assert.equal(typeof a.generation, 'string');
  assert.equal(a.generation, b.generation); quota(r, { residentEntries: 1, residentBytes: 0 }); stats(cache, { created: 1, owners: 2 });
  const old = view(a); assert.equal(r.drop(cache, 'a'), undefined); assert.equal(a.state, 'dormant'); assert.equal(a.url, null);
  assert.equal(b.state, 'ready'); assert.equal(r.revoked.length, 0); quota(r, { sources: 1, residentEntries: 1 });
  r.drop(cache, 'b'); r.drop(cache, 'b');
  assert.deepEqual(r.revoked, [old.url]); quota(r, { sources: 0, residentEntries: 0 });
  assert.equal(r.log.find(e => e.op === 'detach').hash, sha(1));
  assert.equal(a.state, 'dormant'); stats(cache, { owners: 0, entries: 0, created: 1, revoked: 1 });
});

test('WEB-MEM-3 T2b1 source_denial_has_no_waitlist', async t => {
  const r = fixture(t, { C: 1, N: 1 }), first = r.create(), denied = r.create();
  acquire(first.cache, 'first', 1); assert.equal(acquire(denied.cache, 'denied', 2), null);
  const count = denied.events.length; first.cache.release(); await flush();
  quota(r, { sources: 0, running: 1 }); await r.complete(r.calls[0]);
  assert.equal(denied.events.length, count); assert.equal(r.calls.length, 1); stats(denied.cache, { owners: 0, entries: 0, fetches: 0 });
  const admitted = acquire(denied.cache, 'denied', 2); await flush(); assert.equal(admitted.state, 'loading'); assert.equal(r.calls.length, 2);
});

test('WEB-MEM-3 T2b1 cancelled_loading_retains_until_cleanup', async t => {
  for (const [index, limits] of [{ B: 40, C: 1, E: 8 }, { B: 4, C: 4, E: 8 }, { B: 40, C: 4, E: 1 }].entries()) {
    const r = fixture(t, limits), old = r.create(), next = r.create(), lease = acquire(old.cache, 'old', 1);
    const waiting = acquire(next.cache, 'waiting', 2), call = r.calls[0];
    if (index === 1) old.cache.release(); else r.drop(old.cache, 'old');
    const count = old.events.length;
    assert.ok(call.signal.aborted); assert.equal(call.aborts, 1); assert.equal(lease.state, 'dormant');
    if (index === 1) call.body.reject(new Error('delayed cleanup failure')); else call.body.resolve(raster());
    await flush(); quota(r, { running: 1, reservedBytes: 4, queued: 1 });
    assert.equal(waiting.state, 'queued'); assert.equal(r.calls.length, 1);
    call.cleanup.resolve(); await Promise.allSettled([call.terminal]); await flush();
    assert.equal(waiting.state, 'loading'); assert.equal(r.calls.length, 2);
    assert.equal(old.events.length, count); assert.equal(r.allocated.length, 0);
    quota(r, { running: 1, residentEntries: 0 }); await r.complete(r.calls[1]);
  }
});

test('WEB-MEM-3 T2b1 two_many_caches_share_budget', async t => {
  for (const reverse of [false, true]) {
    const r = fixture(t), old = [r.create(), r.create()];
    old.forEach((item, i) => { acquire(item.cache, 'old', i + 1); item.cache.release(); });
    const counts = old.map(item => item.events.length);
    for (let i = 0; i < 20; i++) {
      const { cache } = r.create(), lease = acquire(cache, 'temporary', 3); cache.release();
      assert.equal(lease.state, 'dormant'); stats(cache, { owners: 0, entries: 0, loading: 0 }); quota(r, { running: 2, reservedBytes: 8, queued: 0, sources: 0 });
    }
    assert.equal(r.calls.length, 2); const current = r.create(), lease = acquire(current.cache, 'current', 1);
    const order = reverse ? [r.calls[1], r.calls[0]] : [r.calls[0], r.calls[1]];
    await r.complete(order[0]); assert.equal(lease.state, 'loading'); assert.equal(r.calls.length, 3);
    await r.complete(order[1], new Error('old rejection'), true); quota(r, { running: 1, residentEntries: 0 });
    assert.deepEqual(old.map(item => item.events.length), counts); assert.equal(r.allocated.length, 0);
  }
});

test('WEB-MEM-3 T2b1 queued_cancel_and_displacement', async t => {
  const r = fixture(t, { C: 1 }), { cache, events } = r.create();
  acquire(cache, 'hold', 1); const older = acquire(cache, 'older', 2), victim = acquire(cache, 'victim', 3);
  const explicit = acquire(cache, 'explicit', 4, 'explicit'); await flush();
  assert.equal(victim.state, 'dormant'); quota(r, { queued: 2, sources: 3 });
  const displaced = events.filter(e => e.reason === 'displaced');
  assert.equal(displaced.length, 1); assert.equal(displaced[0].sha, sha(3)); assert.equal(displaced[0].generation, victim.generation);
  r.drop(cache, 'older'); assert.equal(older.state, 'dormant'); const second = acquire(cache, 'second', 5, 'explicit');
  const denied = acquire(cache, 'denied', 6, 'explicit'); assert.equal(denied.state, 'waiting'); assert.equal(second.state, 'queued');
  r.drop(cache, 'denied'); r.drop(cache, 'second'); await r.complete(r.calls[0]); assert.equal(explicit.state, 'loading');
  assert.deepEqual(r.calls.map(call => call.hash), [sha(1), sha(4)]); assert.equal(second.state, 'dormant');
  const ordered = fixture(t, { B: 40, C: 1, E: 10, Q: 6 }), c = ordered.create().cache;
  acquire(c, 'hold', 10); acquire(c, 'auto', 11); acquire(c, 'earlier', 12, 'explicit');
  const shared = acquire(c, 'shared-explicit', 11, 'explicit'); acquire(c, 'later', 13, 'explicit');
  ordered.drop(c, 'shared-explicit'); assert.equal(shared.state, 'dormant');
  const replacement = acquire(c, 'shared-explicit', 11, 'explicit'); acquire(c, 'last', 14, 'explicit');
  assert.equal(acquire(c, 'shared-explicit', 11, 'explicit'), replacement);
  for (const hash of [10, 12, 13, 11, 14]) {
    assert.equal(ordered.calls.at(-1).hash, sha(hash)); await ordered.complete(ordered.calls.at(-1), raster(0));
  }
  quota(ordered, { running: 0, queued: 0 });
  // Repeated acquire changes an existing owner's priority; no move/retry API.
  const updated = fixture(t, { B: 40, C: 1, E: 10, Q: 6 }), p = updated.create().cache;
  acquire(p, 'hold', 20); const x = acquire(p, 'x', 21); acquire(p, 'earlier', 22, 'explicit');
  assert.equal(acquire(p, 'x', 21, 'explicit'), x);
  assert.equal(acquire(p, 'x', 21, 'automatic'), x);
  acquire(p, 'middle', 23, 'explicit'); acquire(p, 'x', 21, 'explicit'); acquire(p, 'last', 24, 'explicit');
  acquire(p, 'x', 21, 'explicit');
  for (const hash of [20, 22, 23, 21, 24]) {
    assert.equal(updated.calls.at(-1).hash, sha(hash)); await updated.complete(updated.calls.at(-1), raster(0));
  }
  // A permitted waiting explicit job must enter only after real Q capacity.
  const capacity = fixture(t, { C: 1, Q: 1 }), c2 = capacity.create().cache;
  acquire(c2, 'hold', 30); acquire(c2, 'accepted', 31, 'explicit');
  const waiting = acquire(c2, 'waiting', 32, 'explicit'); assert.equal(waiting.state, 'waiting');
  await capacity.complete(capacity.calls[0], raster(0));
  assert.equal(waiting.state, 'queued'); quota(capacity, { queued: 1 });
  await capacity.complete(capacity.calls[1], raster(0));
  assert.equal(waiting.state, 'loading'); assert.equal(capacity.calls[2].hash, sha(32));
});

test('WEB-MEM-3 T2b1 current_failure_and_zero_success', async t => {
  const cases = [{ value: null }, { value: raster(1, 'image/svg+xml') }, { value: raster(1, 'text/html') },
    { value: raster(5) }, { value: raster(1, '') }, { reject: true }, { sync: true }, { url: true }, { value: raster(0), ready: true }];
  for (const item of cases) {
    const r = fixture(t), { cache } = r.create(), failure = new Error('synthetic failure');
    if (item.sync) r.hooks.start = () => { throw failure; };
    if (item.url) r.hooks.createError = failure;
    const lease = acquire(cache, 'owner', 1);
    if (item.sync) await flush(); else await r.complete(r.calls[0], item.reject ? failure : item.url ? raster() : item.value, item.reject);
    assert.equal(lease.state, item.ready ? 'ready' : 'error'); assert.equal(r.calls.length, 1);
    quota(r, { running: 0, residentEntries: item.ready ? 1 : 0, residentBytes: 0, sources: 1 });
    if (!item.ready) { assert.equal(lease.url, null); assert.equal(r.allocated.length, 0); }
    await flush(); assert.equal(r.calls.length, 1); stats(cache, { fetches: 1, owners: 1 });
  }
});

test('WEB-MEM-3 T2b1 same_hash_successor_and_stale_completion', async t => {
  for (const reject of [false, true]) {
    const r = fixture(t), { cache, events } = r.create(), old = acquire(cache, 'same-owner', 1), generation = old.generation;
    r.drop(cache, 'same-owner'); const next = acquire(cache, 'same-owner', 1);
    assert.notEqual(next, old); assert.notEqual(next.generation, generation); assert.equal(r.calls.length, 2);
    await r.complete(r.calls[1]); const current = view(next), count = events.filter(e => e.sha === sha(1) && e.generation === current.generation).length;
    assert.ok(count > 0, 'ready notification identifies the current hash and generation');
    const hashCount = events.filter(e => e.sha === sha(1)).length;
    await r.complete(r.calls[0], reject ? new Error('stale failure') : raster(), reject);
    assert.deepEqual(view(next), current); quota(r, { running: 0, residentEntries: 1, residentBytes: 1, sources: 1 });
    assert.equal(r.allocated.length, 1); assert.equal(r.revoked.length, 0);
    assert.equal(old.state, 'dormant'); assert.equal(old.url, null);
    assert.equal(events.filter(e => e.sha === sha(1) && e.generation === current.generation).length, count);
    assert.equal(events.filter(e => e.sha === sha(1)).length, hashCount, 'no hash notification from retired completion');
    assert.equal(cache.broken(sha(1), generation), undefined); assert.deepEqual(view(next), current);
    cache.release(); cache.release(); assert.equal(next.state, 'dormant'); assert.equal(old.state, 'dormant'); assert.deepEqual(r.revoked, [current.url]);
  }
});

test('WEB-MEM-3 T2b1 detach_revoke_drop_release_order', async t => {
  for (const operation of ['drop', 'broken', 'release']) for (const failAt of ['before', 'revoke']) {
    const r = fixture(t, { N: 1 }), h = {}, { cache } = r.create(h), lease = acquire(cache, 'owner', 1);
    await r.complete(r.calls[0]); const generation = lease.generation, failure = new Error('cleanup must retry');
    if (failAt === 'before') h.before = () => { throw failure; }; else r.hooks.revoke = () => { throw failure; };
    const dispose = () => operation === 'drop' ? r.drop(cache, 'owner') : operation === 'broken' ? cache.broken(sha(1), generation) : cache.release();
    assert.throws(dispose, error => error === failure); await flush();
    quota(r, { residentEntries: 1, residentBytes: 1, sources: 1 });
    assert.equal(r.revoked.length, 0); stats(cache, { created: 1, revoked: 0 });
    h.before = undefined; r.hooks.revoke = undefined;
    cache.release(); cache.release(); quota(r, { residentEntries: 0, residentBytes: 0, sources: 0 });
    stats(cache, { created: 1, revoked: 1, owners: 0, entries: 0 });
    assert.equal(r.revoked.length, 1); const successful = r.log.filter(e => e.op === 'revoke').at(-1);
    assert.equal(successful.stats.residentEntries, 1); assert.ok(r.log.findIndex(e => e.op === 'detach') < r.log.findIndex(e => e.op === 'revoke'));
  }
  const r = fixture(t), h = {}, { cache } = r.create(h), a = acquire(cache, 'a', 1), b = acquire(cache, 'b', 1);
  await r.complete(r.calls[0]); h.before = () => r.tap(() => assert.throws(() => acquire(cache, 'forbidden-reentry', 2), Error));
  cache.broken(sha(1), a.generation); cache.broken(sha(1), a.generation);
  assert.equal(a.state, 'error'); assert.equal(b.state, 'error'); assert.equal(a.url, null); assert.equal(r.revoked.length, 1);
  quota(r, { residentEntries: 0, sources: 2 }); cache.release(); quota(r, { sources: 0 });
});

test('WEB-MEM-3 T2b1 callback_throw_reentry_retirement', async t => {
  const r = fixture(t), h = { throwChange: new Error('observer throws') }, { cache } = r.create(h);
  h.change = (event, current) => {
    if (event.reason === 'state' && current.stats.residentEntries === 1) r.drop(current, 'owner');
  };
  const lease = acquire(cache, 'owner', 1); await r.complete(r.calls[0]);
  assert.equal(lease.state, 'dormant'); assert.equal(r.allocated.length, 1); assert.equal(r.revoked.length, 1);
  quota(r, { running: 0, residentEntries: 0, sources: 0 });
  const apiReentry = fixture(t), a = apiReentry.create(); apiReentry.hooks.start = () => a.cache.release();
  const retired = acquire(a.cache, 'owner', 1), count = a.events.length;
  assert.ok(apiReentry.calls[0].signal.aborted); quota(apiReentry, { running: 1 });
  await apiReentry.complete(apiReentry.calls[0]);
  assert.equal(retired.state, 'dormant'); assert.equal(a.events.length, count); assert.equal(apiReentry.allocated.length, 0);
  const urlReentry = fixture(t), u = urlReentry.create(); urlReentry.hooks.allocate = () => u.cache.release();
  const unpublished = acquire(u.cache, 'owner', 1); await urlReentry.complete(urlReentry.calls[0]);
  assert.equal(unpublished.state, 'dormant'); assert.equal(unpublished.url, null);
  assert.equal(urlReentry.allocated.length, 1); assert.equal(urlReentry.revoked.length, 1);
  quota(urlReentry, { running: 0, residentEntries: 0, sources: 0 });
  const dispatch = fixture(t, { C: 1 }), first = dispatch.create(), second = dispatch.create();
  const ready = acquire(first.cache, 'first', 1), queued = acquire(second.cache, 'second', 2, 'explicit'); let afterRelease;
  dispatch.hooks.start = call => {
    if (call.hash === sha(2)) {
      dispatch.tap(() => { assert.equal(ready.state, 'ready'); assert.ok(ready.url); stats(first.cache, { residentEntries: 1 }); quota(dispatch, { residentEntries: 1 }); });
      first.cache.release(); afterRelease = first.events.length;
    }
  };
  await dispatch.complete(dispatch.calls[0]); assert.equal(ready.state, 'dormant'); assert.equal(queued.state, 'loading');
  assert.equal(first.events.length, afterRelease); assert.equal(dispatch.allocated.length, 1); assert.equal(dispatch.revoked.length, 1);
  quota(dispatch, { running: 1, residentEntries: 0, sources: 1 }); await dispatch.complete(dispatch.calls[1]); assert.equal(queued.state, 'ready');
});

test('WEB-MEM-3 T2b2 move_owner_N1_identity', async t => {
  const r = fixture(t, { N: 1 }), { cache } = r.create(), denied = r.create();
  const lease = acquire(cache, 'from', 1), loading = view(lease);
  assert.equal(acquire(denied.cache, 'denied', 2), null);
  r.log.push({ op: 'caller-detach', owner: 'from' });
  assert.equal(cache.moveOwner('from', 'loading-target', { priority: 'explicit' }), lease);
  assert.deepEqual(view(lease), loading); assert.equal(r.calls.length, 1); assert.equal(r.calls[0].signal.aborted, false);
  quota(r, { sources: 1, running: 1, reservedBytes: 4 }); stats(cache, { owners: 1, entries: 1, fetches: 1 });
  assert.equal(cache.retry('from'), null); r.drop(cache, 'from'); assert.deepEqual(view(lease), loading);
  await r.complete(r.calls[0], raster(2)); const ready = view(lease), snapshot = cache.stats;
  r.log.push({ op: 'caller-detach', owner: 'loading-target' });
  assert.equal(cache.moveOwner('loading-target', 'ready-target', { priority: 'automatic' }), lease);
  assert.equal(cache.moveOwner('ready-target', 'ready-target', { priority: 'explicit' }), lease);
  assert.equal(cache.moveOwner('missing', 'unused', { priority: 'automatic' }), null);
  assert.deepEqual(view(lease), ready); assert.deepEqual(cache.stats, snapshot);
  assert.equal(cache.retry('loading-target'), null); r.drop(cache, 'loading-target');
  assert.equal(r.calls.length, 1); assert.equal(r.allocated.length, 1); assert.equal(r.revoked.length, 0);
  quota(r, { sources: 1, running: 0, residentEntries: 1, residentBytes: 2 });
  r.drop(cache, 'ready-target'); assert.equal(lease.state, 'dormant'); assert.equal(lease.url, null);
  assert.deepEqual(r.revoked, [ready.url]); quota(r, { sources: 0, residentEntries: 0, residentBytes: 0 });
});

test('WEB-MEM-3 T2b2 retry_revalidates_owner_metadata', async t => {
  const r = fixture(t, { N: 1 }), { cache } = r.create();
  const original = att(1, { origin_url: 'https://invalid.test/source' });
  const lease = cache.acquire('owner', original, true, { priority: 'automatic' }), firstGeneration = lease.generation;
  await r.complete(r.calls[0], new Error('transport failure'), true); await flush();
  assert.equal(lease.state, 'error'); assert.equal(r.calls.length, 1); quota(r, { sources: 1, running: 0 });
  // Caller mutation cannot change the scalar metadata snapshot admitted earlier.
  original.sha256 = '../changed'; original.media_type = 'image/svg+xml'; original.size = THUMB_MAX + 1;
  assert.equal(cache.retry('owner'), lease); const secondGeneration = lease.generation;
  assert.notEqual(secondGeneration, firstGeneration); assert.equal(typeof secondGeneration, 'string');
  assert.equal(lease.state, 'loading');
  const loadingRetry = view(lease);
  assert.equal(cache.retry('owner'), lease); assert.equal(r.calls.length, 2); assert.equal(r.calls[1].hash, sha(1));
  assert.deepEqual(view(lease), loadingRetry);
  await r.complete(r.calls[1], raster(1, 'text/html')); assert.equal(lease.state, 'error');
  assert.equal(acquire(cache, 'owner', 1, 'explicit', { origin_url: 'origin', blob_url: 'hosted-metadata' }, false), lease);
  await flush(); assert.equal(r.calls.length, 2); assert.equal(lease.state, 'error');
  assert.equal(cache.retry('owner'), lease); const thirdGeneration = lease.generation;
  assert.notEqual(thirdGeneration, secondGeneration); assert.equal(typeof thirdGeneration, 'string');
  quota(r, { sources: 1, running: 1, reservedBytes: 4 });
  await r.complete(r.calls[2], raster(2)); const ready = view(lease);
  assert.equal(cache.retry('owner'), lease); assert.deepEqual(view(lease), ready); assert.equal(r.calls.length, 3);
  cache.broken(sha(1), firstGeneration); cache.broken(sha(1), secondGeneration);
  assert.deepEqual(view(lease), ready); assert.equal(r.revoked.length, 0);
  quota(r, { sources: 1, running: 0, residentEntries: 1, residentBytes: 2 });
  r.log.push({ op: 'caller-detach', owner: 'owner' });
  assert.equal(acquire(cache, 'owner', 1, 'explicit', { origin_url: 'not-hosted' }, false), null);
  assert.equal(cache.retry('owner'), null); assert.equal(lease.state, 'dormant'); assert.equal(lease.url, null);
  stats(cache, { owners: 0, entries: 0, fetches: 3, created: 1, revoked: 1 });
  quota(r, { sources: 0, residentEntries: 0, residentBytes: 0 });
});

test('WEB-MEM-3 T2b2 aggregate_explicit_priority_FIFO', async t => {
  const r = fixture(t, { B: 40, C: 1, E: 16, N: 16, Q: 8 }), { cache } = r.create();
  acquire(cache, 'hold', 10); const automatic = acquire(cache, 'auto', 11);
  acquire(cache, 'earlier', 12, 'explicit'); const shared = acquire(cache, 'shared', 11, 'explicit');
  acquire(cache, 'later', 13, 'explicit'); const second = acquire(cache, 'second', 11, 'explicit');
  const generation = shared.generation;
  assert.equal(cache.moveOwner('shared', 'moved', { priority: 'automatic' }), shared);
  assert.equal(cache.moveOwner('second', 'second', { priority: 'explicit' }), second);
  assert.equal(acquire(cache, 'auto', 11), automatic); // Another explicit owner still promotes this hash.
  assert.equal(shared.generation, generation); quota(r, { running: 1, queued: 3, sources: 6 });
  for (const hash of [10, 12, 11, 13]) {
    assert.equal(r.calls.at(-1).hash, sha(hash)); await r.complete(r.calls.at(-1), raster(0));
  }
  assert.deepEqual(r.calls.map(call => call.hash), [10, 12, 11, 13].map(sha));
  const demoted = fixture(t, { B: 40, C: 1, E: 16, N: 16, Q: 8 }), c = demoted.create().cache;
  acquire(c, 'hold', 20); const base = acquire(c, 'base', 21);
  acquire(c, 'earlier', 22, 'explicit'); acquire(c, 'last-explicit', 21, 'explicit');
  acquire(c, 'later', 23, 'explicit'); acquire(c, 'automatic', 24);
  const queuedGeneration = base.generation; demoted.drop(c, 'last-explicit');
  acquire(c, 'before-upgrade', 25, 'explicit');
  assert.equal(c.moveOwner('base', 'renamed', { priority: 'explicit' }), base);
  acquire(c, 'after-upgrade', 26, 'explicit');
  assert.equal(c.moveOwner('renamed', 'renamed', { priority: 'explicit' }), base);
  assert.equal(acquire(c, 'renamed', 21, 'explicit'), base); assert.equal(base.generation, queuedGeneration);
  quota(demoted, { running: 1, queued: 6, sources: 7 });
  for (const hash of [20, 22, 23, 25, 21, 26, 24]) {
    assert.equal(demoted.calls.at(-1).hash, sha(hash)); await demoted.complete(demoted.calls.at(-1), raster(0));
  }
  quota(demoted, { running: 0, queued: 0, residentEntries: 7 });
  const moved = fixture(t, { B: 40, C: 1, E: 10, Q: 4 }), m = moved.create().cache;
  acquire(m, 'hold', 30); const only = acquire(m, 'only-explicit', 31, 'explicit');
  acquire(m, 'automatic', 34); acquire(m, 'other-explicit', 32, 'explicit');
  const movedGeneration = only.generation;
  assert.equal(m.moveOwner('only-explicit', 'now-automatic', { priority: 'automatic' }), only);
  assert.equal(only.generation, movedGeneration); quota(moved, { running: 1, queued: 3, sources: 4 });
  for (const hash of [30, 32, 31, 34]) {
    assert.equal(moved.calls.at(-1).hash, sha(hash)); await moved.complete(moved.calls.at(-1), raster(0));
  }
  quota(moved, { running: 0, queued: 0, residentEntries: 4 });
});

test('WEB-MEM-3 T2b2 waiting_retry_on_actual_capacity', async t => {
  const r = fixture(t, { B: 40, C: 1, E: 10, N: 3, Q: 1 }), hold = r.create(), active = r.create(), denied = r.create();
  acquire(hold.cache, 'hold', 1); const accepted = acquire(active.cache, 'accepted', 2, 'explicit');
  const waiting = acquire(active.cache, 'waiting', 3, 'explicit'); assert.equal(waiting.state, 'waiting');
  assert.equal(acquire(denied.cache, 'denied', 5), null);
  assert.equal(active.cache.retry('waiting'), waiting); assert.equal(waiting.generation, null);
  quota(r, { sources: 3, running: 1, queued: 1 }); assert.equal(r.calls.length, 1);
  r.drop(active.cache, 'accepted'); assert.equal(accepted.state, 'dormant');
  assert.equal(active.cache.retry('waiting'), waiting); const generation = waiting.generation;
  assert.equal(waiting.state, 'queued'); assert.equal(typeof generation, 'string');
  const follower = acquire(active.cache, 'follower', 4, 'explicit'); assert.equal(follower.state, 'waiting');
  assert.equal(active.cache.retry('follower'), follower); assert.equal(active.cache.retry('waiting'), waiting);
  assert.equal(waiting.generation, generation); hold.cache.release();
  r.calls[0].body.resolve(raster(0)); await flush();
  quota(r, { sources: 2, running: 1, reservedBytes: 4, queued: 1 });
  assert.equal(waiting.state, 'queued'); assert.equal(follower.state, 'waiting'); assert.equal(r.calls.length, 1);
  r.calls[0].cleanup.resolve(); await Promise.allSettled([r.calls[0].terminal]); await flush();
  assert.equal(waiting.state, 'loading'); assert.equal(waiting.generation, generation);
  assert.equal(follower.state, 'queued'); assert.equal(typeof follower.generation, 'string');
  const followerGeneration = follower.generation, loadingView = view(waiting), queuedView = view(follower);
  assert.equal(active.cache.retry('waiting'), waiting); assert.equal(active.cache.retry('follower'), follower);
  assert.deepEqual(view(waiting), loadingView); assert.deepEqual(view(follower), queuedView);
  assert.equal(r.calls.length, 2); quota(r, { sources: 2, running: 1, queued: 1 });
  await r.complete(r.calls[1], raster(0)); assert.equal(follower.state, 'loading');
  assert.equal(follower.generation, followerGeneration); assert.equal(active.cache.retry('follower'), follower);
  await r.complete(r.calls[2]); assert.equal(follower.state, 'ready');
  assert.deepEqual(r.calls.map(call => call.hash), [1, 3, 4].map(sha)); assert.equal(denied.events.length, 0);
  quota(r, { sources: 2, running: 0, queued: 0, residentEntries: 2 });
});

test('WEB-MEM-3 T2b2 move_drop_retry_terminal_reentry', async t => {
  const r = fixture(t), h = {}, { cache, events } = r.create(h), lease = acquire(cache, 'old', 1);
  assert.equal(typeof cache.moveOwner, 'function'); assert.equal(typeof cache.retry, 'function');
  const before = cache.stats, budgetBefore = r.budget.stats, loading = view(lease);
  for (const args of [['', 'new', { priority: 'explicit' }], ['old', '', { priority: 'automatic' }],
    ['old', 'new', { priority: 'viewer' }], ['old', 'new', null], ['old', 'new', undefined],
    [null, 'new', { priority: 'automatic' }], ['old', 1, { priority: 'explicit' }]])
    assert.throws(() => cache.moveOwner(...args), TypeError);
  for (const key of ['', null, 1, {}]) assert.throws(() => cache.retry(key), TypeError);
  assert.deepEqual(cache.stats, before); assert.deepEqual(r.budget.stats, budgetBefore); assert.deepEqual(view(lease), loading);
  let phase = 0, replacement, retiredCount;
  h.change = () => {
    if (phase === 0 && lease.state === 'error') {
      phase = 1; const failedGeneration = lease.generation;
      assert.equal(cache.moveOwner('old', 'moved', { priority: 'explicit' }), lease);
      assert.equal(cache.retry('moved'), lease); assert.notEqual(lease.generation, failedGeneration);
      assert.equal(cache.retry('moved'), lease); r.drop(cache, 'old');
    } else if (phase === 1 && lease.state === 'ready') {
      phase = 2; assert.equal(cache.retry('moved'), lease);
      r.log.push({ op: 'caller-detach', owner: 'moved' });
      assert.equal(cache.moveOwner('moved', 'ready', { priority: 'automatic' }), lease);
      r.drop(cache, 'moved'); assert.equal(lease.state, 'ready'); r.drop(cache, 'ready');
      replacement = acquire(cache, 'ready', 1); assert.notEqual(replacement, lease);
      assert.equal(cache.retry('ready'), replacement); cache.release(); retiredCount = events.length;
    }
  };
  await r.complete(r.calls[0], null); assert.equal(phase, 1); assert.equal(lease.state, 'loading');
  assert.equal(r.calls.length, 2); quota(r, { sources: 1, running: 1 });
  await r.complete(r.calls[1]); assert.equal(phase, 2); assert.equal(r.calls.length, 3);
  assert.equal(lease.state, 'dormant'); assert.equal(replacement.state, 'dormant'); assert.ok(r.calls[2].signal.aborted);
  assert.equal(cache.retry('ready'), null); assert.equal(cache.moveOwner('ready', 'late', { priority: 'explicit' }), null);
  assert.equal(cache.retry('missing'), null); assert.equal(r.drop(cache, 'ready'), undefined);
  quota(r, { sources: 0, running: 1, reservedBytes: 4, residentEntries: 0 });
  await r.complete(r.calls[2]); assert.equal(events.length, retiredCount); assert.equal(r.allocated.length, 1);
  assert.equal(r.revoked.length, 1); quota(r, { sources: 0, running: 0, reservedBytes: 0 });
  const collision = fixture(t), c = collision.create().cache, a = acquire(c, 'a', 2), b = acquire(c, 'b', 3);
  const aView = view(a), bView = view(b), snapshot = c.stats, quotaBefore = collision.budget.stats;
  assert.equal(c.moveOwner('a', 'b', { priority: 'explicit' }), null);
  assert.equal(c.moveOwner('missing', 'free', { priority: 'automatic' }), null); assert.equal(c.retry('missing'), null);
  assert.deepEqual(view(a), aView); assert.deepEqual(view(b), bView); assert.deepEqual(c.stats, snapshot);
  assert.deepEqual(collision.budget.stats, quotaBefore); assert.equal(collision.calls.length, 2);
});

test('WEB-MEM-3 T2b2 cross_cache_explicit_close_switch_progress', async t => {
  const r = fixture(t, { B: 40, C: 1, E: 10, Q: 2 });
  const hold = r.create(), earlier = r.create(), closed = r.create(), switched = r.create();
  acquire(hold.cache, 'hold', 1); const first = acquire(earlier.cache, 'first', 2, 'explicit');
  const cancelled = acquire(closed.cache, 'pending', 3, 'explicit'), next = acquire(switched.cache, 'switch', 4, 'explicit');
  assert.equal(first.state, 'queued'); assert.equal(cancelled.state, 'queued'); assert.equal(next.state, 'waiting');
  assert.equal(switched.cache.retry('switch'), next); quota(r, { running: 1, queued: 2, sources: 4 });
  closed.cache.release(); const closedCount = closed.events.length;
  assert.equal(cancelled.state, 'dormant'); assert.equal(switched.cache.retry('switch'), next);
  assert.equal(next.state, 'queued'); assert.equal(first.state, 'queued');
  assert.equal(closed.cache.retry('pending'), null); hold.cache.release();
  r.calls[0].body.reject(new Error('closed transport still cleaning')); await flush();
  quota(r, { running: 1, reservedBytes: 4, queued: 2, sources: 2 }); assert.equal(r.calls.length, 1);
  r.calls[0].cleanup.resolve(); await Promise.allSettled([r.calls[0].terminal]); await flush();
  assert.equal(first.state, 'loading'); assert.equal(next.state, 'queued'); assert.equal(r.calls[1].hash, sha(2));
  earlier.cache.release(); const earlierCount = earlier.events.length;
  assert.ok(r.calls[1].signal.aborted); assert.equal(first.state, 'dormant');
  r.calls[1].body.resolve(raster()); await flush(); assert.equal(next.state, 'queued');
  quota(r, { running: 1, reservedBytes: 4, queued: 1, sources: 1 }); assert.equal(r.calls.length, 2);
  r.calls[1].cleanup.resolve(); await Promise.allSettled([r.calls[1].terminal]); await flush();
  assert.equal(next.state, 'loading'); assert.equal(r.calls[2].hash, sha(4));
  assert.equal(switched.cache.retry('switch'), next); await r.complete(r.calls[2], raster(2));
  const ready = view(next); closed.cache.release(); earlier.cache.release(); hold.cache.release();
  assert.deepEqual(view(next), ready); assert.deepEqual(r.calls.map(call => call.hash), [1, 2, 4].map(sha));
  assert.equal(closed.events.length, closedCount); assert.equal(earlier.events.length, earlierCount);
  assert.equal(r.allocated.length, 1); assert.equal(r.revoked.length, 0);
  quota(r, { running: 0, queued: 0, residentEntries: 1, residentBytes: 2, sources: 1 });
});
