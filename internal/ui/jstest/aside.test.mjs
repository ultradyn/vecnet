import test from 'node:test';
import assert from 'node:assert/strict';
import { budgetBar, fileRows, participantEntry, presenceLabel, reachLabel } from '../static/aside-model.js';
import { blobView, downloadErrorText, nextExpiry } from '../static/attach-model.js';
import { APIError } from '../static/api.js';
import { mountAside } from '../static/thread-aside.js';

test('unknown presence with reachable reads unknown, never offline', () => {
  // A push-only peer (a dot, a Grok Bot) never reports presence: the hub
  // shows it as unknown while its pushes land (PROTOCOL §8).
  assert.equal(presenceLabel('unknown'), 'unknown');
  assert.equal(reachLabel('reachable'), 'reachable');
  assert.doesNotMatch(presenceLabel('unknown'), /offline/);
  assert.doesNotMatch(reachLabel('reachable'), /offline/);
  // Missing or unrecognised values are unknown too, never offline.
  for (const v of [undefined, null, '', 'stale', 'OFFLINE ']) {
    assert.equal(presenceLabel(v), 'unknown', `presence ${JSON.stringify(v)}`);
    assert.equal(reachLabel(v), 'unknown', `reachability ${JSON.stringify(v)}`);
  }
  // The self-reported states and hub-observed values pass through unchanged.
  for (const v of ['idle', 'working', 'blocked', 'offline']) assert.equal(presenceLabel(v), v);
  for (const v of ['connected', 'reachable', 'unreachable', 'unknown']) assert.equal(reachLabel(v), v);
  // Reachability has no offline state of its own.
  assert.equal(reachLabel('offline'), 'unknown');
});

test('budget null reads unlimited; 0 reads exhausted', () => {
  const row = budget => ({ thread: { thread_id: 'thr_x', budget_remaining: budget } });

  const unlimited = budgetBar(row(null), 500);
  assert.equal(unlimited.text, 'unlimited');
  assert.equal(unlimited.fraction, null);
  assert.equal(unlimited.tone, 'unlimited');

  const exhausted = budgetBar(row(0), 500);
  assert.equal(exhausted.text, 'exhausted');
  assert.equal(exhausted.fraction, 0);
  assert.equal(exhausted.tone, 'bad');

  const some = budgetBar(row(494), 500);
  assert.equal(some.text, '494 of 500 left');
  assert.equal(some.fraction, 494 / 500);
  assert.equal(some.tone, 'ok');

  // The last tenth warns.
  assert.equal(budgetBar(row(40), 500).tone, 'low');
  assert.equal(budgetBar(row(40), 500).text, '40 of 500 left');

  // A budget the operator raised past the default has no known total.
  const raised = budgetBar(row(700), 500);
  assert.equal(raised.text, '700 left');
  assert.equal(raised.fraction, 1);

  // The default total is the hub's 500 (PROTOCOL §6).
  assert.equal(budgetBar(row(10)).text, '10 of 500 left');
});

test('fileRows marks spilled and purged', () => {
  const base = { media_type: 'text/plain', message_id: 'msg_1', sender: 'peer_a', created_at: '2026-11-02T06:00:00.000Z' };
  const rows = fileRows([
    { ...base, sha256: 'a'.repeat(64), name: 'hark-listen.log', size: 18432, spilled: false, purged: false },
    { ...base, sha256: 'b'.repeat(64), name: 'message.md', media_type: 'text/markdown', size: 12288, spilled: true, purged: false },
    { ...base, sha256: 'c'.repeat(64), name: 'old.bin', size: 10, spilled: false, purged: true },
    { ...base, sha256: 'd'.repeat(64), name: '..\u001b[31m/‮.txt', size: 5, spilled: false, purged: false },
    { ...base, sha256: 'e'.repeat(64), name: '', size: 1, spilled: false, purged: false },
  ]);
  assert.equal(rows.length, 5);

  const [log, md, gone, hostile, unnamed] = rows;
  assert.equal(log.label, 'hark-listen.log');
  assert.equal(log.size, '18.0 KiB');
  assert.equal(log.spilled, false);
  assert.equal(log.purged, false);
  assert.equal(log.disabled, false);
  assert.equal(log.note, '');
  assert.equal(log.download, 'hark-listen.log');
  assert.equal(log.sender, 'peer_a');
  assert.equal(log.sha256, 'a'.repeat(64));

  assert.equal(md.spilled, true);
  assert.equal(md.purged, false);
  assert.equal(md.disabled, false);
  assert.equal(md.note, 'spilled');

  // A purged file stays listed, disabled and labelled.
  assert.equal(gone.purged, true);
  assert.equal(gone.disabled, true);
  assert.equal(gone.note, 'purged');
  assert.equal(gone.label, 'old.bin');

  // The shown label is the peer's text as is (rendered as text); the saved
  // name loses control characters, bidi overrides and path separators.
  assert.equal(hostile.label, '..\u001b[31m/‮.txt');
  assert.doesNotMatch(hostile.download, /[\u0000-\u001f\u007f-\u009f‪-‮⁦-⁩/\\]/);
  assert.ok(!hostile.download.startsWith('.'), hostile.download);
  assert.ok(hostile.download.length > 0);

  // No name falls back to the media type, and a saved name always exists.
  assert.equal(unnamed.label, 'text/plain');
  assert.match(unnamed.download, /^file-e{12}$/);

  assert.deepEqual(fileRows(undefined), []);
  assert.deepEqual(fileRows([]), []);
});

test('participantEntry: the refreshed directory decides presence; admin adds only disabled', () => {
  // The admin snapshot loaded while the session was fresh; the directory,
  // reloaded after its TTL, says unknown.
  const admin = [{ peer: { peer_id: 'peer_s', name: 's', presence: 'working', reachability: 'connected' }, disabled: false },
    { peer: { peer_id: 'peer_off', name: 'off', presence: 'idle', reachability: 'unknown' }, disabled: true }];
  const dir = [{ peer_id: 'peer_s', name: 's', presence: 'unknown', reachability: 'connected' }];
  assert.equal(participantEntry(admin, dir, 'peer_s').peer.presence, 'unknown');
  assert.equal(participantEntry(admin, dir, 'peer_s').disabled, false);
  // A peer only the admin view lists keeps its admin row.
  assert.deepEqual(participantEntry(admin, dir, 'peer_off'), { peer: admin[1].peer, disabled: true });
  assert.equal(participantEntry(admin, dir, 'peer_none'), null);
  assert.equal(participantEntry(null, dir, 'peer_s').peer.presence, 'unknown');
});

// ---------- R2 attachments in the Files panel (R2 blobs Task 7) ----------

const NOW = Date.parse('2026-11-06T10:00:00.000Z');
const r2file = (id, extra = {}) => ({ blob_id: id, sha256: 'd'.repeat(64), name: 'trace.log', media_type: 'text/plain', size: 2048, message_id: 'msg_9', sender: 'peer_a', created_at: '2026-11-05T10:00:00.000Z', spilled: false, purged: false, ...extra });
const refOf = (id, att, state) => [id, { att: { blob_id: id, sha256: 'd'.repeat(64), size: 2048, name: 'trace.log', ...att }, state }];
const verified = v => ({ sha256_verified: v, integrity: { algorithm: 'sha256', value: 'd'.repeat(64), verified: v } });

test('expired file remains visible without a download action', () => {
  const id = 'blo_00000000000000000000000001';
  const refs = new Map([refOf(id, { retention_class: '1d', expires_at: '2026-11-06T09:00:00.000Z', ...verified(true) }, { state: 'expired', retention_class: '1d', expires_at: '2026-11-06T09:00:00.000Z', ...verified(true) })]);
  const [f] = fileRows([r2file(id)], refs, NOW);
  assert.equal(f.note, 'Attachment expired (24-hour retention)');
  assert.equal(f.state, 'expired');
  assert.equal(f.label, 'trace.log');
  assert.equal(f.sha256, 'd'.repeat(64));
  assert.equal(f.blobId, id);
  assert.equal(f.disabled, true);
});

test('purged is not expired; kept has no deadline; recorded classes label old files, not the current preference', () => {
  const a = 'blo_00000000000000000000000002', b = 'blo_00000000000000000000000003', c = 'blo_00000000000000000000000004';
  const refs = new Map([
    refOf(a, { retention_class: '7d', expires_at: '2026-11-08T10:00:00.000Z', ...verified(true) }, { state: 'purged', retention_class: '7d', expires_at: '2026-11-08T10:00:00.000Z', ...verified(true) }),
    refOf(b, { retention_class: 'keep', expires_at: null, ...verified(true) }, { state: 'available', retention_class: 'keep', expires_at: null, ...verified(true) }),
    refOf(c, { retention_class: '30d', expires_at: '2026-11-01T10:00:00.000Z', ...verified(true) }, { state: 'expired', retention_class: '30d', expires_at: '2026-11-01T10:00:00.000Z', ...verified(true) }),
  ]);
  const [pa, kb, ec] = fileRows([r2file(a), r2file(b), r2file(c)], refs, NOW);
  assert.equal(pa.note, 'Attachment purged');
  assert.equal(pa.disabled, true);
  assert.equal(kb.note, 'Kept while referenced');
  assert.equal(kb.disabled, false);
  assert.equal(kb.expiresAt, null);
  assert.equal(ec.note, 'Attachment expired (30-day retention)');
  // The thread detail's own purged flag is purged, never expired.
  const [p2] = fileRows([r2file(a, { purged: true })], null, NOW);
  assert.equal(p2.note, 'Attachment purged');
});

test('the local clock expires a file at its recorded deadline before the hub says so', () => {
  const id = 'blo_00000000000000000000000005';
  const att = { retention_class: '1h', expires_at: '2026-11-06T10:30:00.000Z', ...verified(true) };
  const refs = new Map([refOf(id, att, { state: 'available', retention_class: '1h', expires_at: att.expires_at, ...verified(true) })]);
  let [f] = fileRows([r2file(id)], refs, NOW);
  assert.equal(f.state, 'available');
  assert.equal(f.note, 'Expires in 30 min');
  assert.equal(f.disabled, false);
  [f] = fileRows([r2file(id)], refs, Date.parse(att.expires_at));
  assert.equal(f.state, 'expired');
  assert.equal(f.note, 'Attachment expired (1-hour retention)');
  assert.equal(f.disabled, true);
  // The next timer: the earliest future deadline; keep and past ones never schedule.
  const views = [
    blobView({ blob_id: 'x', retention_class: 'keep', expires_at: null }, null, NOW),
    blobView({ blob_id: 'y', retention_class: '1h', expires_at: '2026-11-06T09:59:00.000Z' }, null, NOW),
    blobView({ blob_id: 'z', retention_class: '6h', expires_at: '2026-11-06T16:00:00.000Z' }, null, NOW),
    blobView({ blob_id: 'w', retention_class: '1h', expires_at: '2026-11-06T10:45:00.000Z' }, null, NOW),
  ];
  assert.equal(nextExpiry(views, NOW), Date.parse('2026-11-06T10:45:00.000Z'));
  assert.equal(nextExpiry([views[0]], NOW), null, 'keep has no expiry timer');
  // Unavailable still has a download until its deadline, so the timer has to
  // redraw it then; otherwise the row stays downloadable forever.
  const at = '2026-11-06T10:20:00.000Z';
  const unavailable = blobView(
    { blob_id: 'blo_u', sha256: 'd'.repeat(64), retention_class: '1h', expires_at: at, ...verified(true) },
    { state: 'unavailable', retention_class: '1h', expires_at: at, ...verified(true) },
    NOW,
  );
  assert.equal(unavailable.state, 'unavailable');
  assert.equal(unavailable.canDownload, true);
  assert.equal(nextExpiry([unavailable], NOW), Date.parse(at));
  assert.equal(nextExpiry([unavailable], Date.parse(at)), null, 'a deadline that has arrived is not scheduled again');
});

test('trust: a pending or unverified checksum is never shown as verified', () => {
  const pending = blobView({ blob_id: 'p', sha256: 'd'.repeat(64), retention_class: '1d', expires_at: '2026-11-07T10:00:00.000Z', ...verified(false) }, null, NOW);
  assert.equal(pending.verified, false);
  assert.match(pending.trust, /not verified/i);
  const mismatch = blobView({ blob_id: 'p', sha256: 'd'.repeat(64), retention_class: '1d', expires_at: '2026-11-07T10:00:00.000Z', sha256_verified: true, integrity: { algorithm: 'sha256', value: 'e'.repeat(64), verified: true } }, null, NOW);
  assert.equal(mismatch.verified, false, 'integrity must name this digest');
  const ok = blobView({ blob_id: 'p', sha256: 'd'.repeat(64), retention_class: '1d', expires_at: '2026-11-07T10:00:00.000Z', ...verified(true) }, null, NOW);
  assert.equal(ok.verified, true);
  assert.match(ok.trust, /SHA-256 verified/);
  // The projection's verification wins over a stale reference.
  const later = blobView({ blob_id: 'p', sha256: 'd'.repeat(64), retention_class: '1d', expires_at: '2026-11-07T10:00:00.000Z', ...verified(false) }, { state: 'available', ...verified(true) }, NOW);
  assert.equal(later.verified, true);
  // A legacy disk reference has no R2 view at all.
  assert.equal(blobView({ sha256: 'd'.repeat(64) }, null, NOW), null);
});

test('pending and unknown metadata are not downloadable; unavailable still is until its deadline', () => {
  const id = 'blo_0000000000000000000000000d';
  const file = r2file(id);
  const pend = fileRows([file], new Map([[id, { att: {}, state: { state: 'pending' } }]]), NOW)[0];
  assert.equal(pend.state, 'pending');
  assert.equal(pend.note, 'Status pending');
  assert.equal(pend.disabled, true);
  assert.equal(pend.expiresAt, null);
  const unk = fileRows([file], new Map([[id, { att: {}, state: { state: 'unknown' } }]]), NOW)[0];
  assert.equal(unk.state, 'unknown');
  assert.equal(unk.note, 'Status unknown');
  assert.equal(unk.disabled, true);
  // No reference at all is the same gap: not a download with an empty label.
  const bare = fileRows([file], new Map(), NOW)[0];
  assert.equal(bare.state, 'unknown');
  assert.equal(bare.disabled, true);
  assert.match(bare.note, /unknown/i);
  const at = '2026-11-06T10:20:00.000Z';
  const down = fileRows([file], new Map([refOf(id, { retention_class: '1h', expires_at: at, ...verified(true) }, { state: 'unavailable', retention_class: '1h', expires_at: at, ...verified(true) })]), NOW)[0];
  assert.equal(down.state, 'unavailable');
  assert.equal(down.disabled, false);
  assert.equal(down.note, 'Storage unavailable');
});

test('download errors: expired and purged differ; a rate limit shows its retry delay; nothing echoes a URL', () => {
  assert.equal(downloadErrorText(new APIError(410, 'blob_expired', ''), NOW, '1d'), 'Attachment expired (24-hour retention)');
  assert.equal(downloadErrorText(new APIError(410, 'blob_purged', ''), NOW, '1d'), 'Attachment purged from the hub');
  assert.match(downloadErrorText(new APIError(429, 'blob_url_rate_limited', '', { retry_after_seconds: 42 }), NOW), /try again in 42 s/);
  assert.match(downloadErrorText(new APIError(429, 'blob_url_rate_limited', '', {}), NOW), /try again shortly/);
  assert.match(downloadErrorText(new APIError(0, 'blob_checksum_mismatch', ''), NOW), /integrity|checksum/i);
  assert.match(downloadErrorText(new APIError(0, 'storage_unreachable', ''), NOW), /storage/i);
  assert.match(downloadErrorText(new APIError(503, 'blob_unavailable', ''), NOW), /unavailable/i);
});

// A document large enough for the Files panel. Installed only around a mount.
class AsideEl {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; this.on = {}; this.style = { setProperty() {} }; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(type, fn) { (this.on[type] ||= []).push(fn); }
  removeEventListener(type, fn) { this.on[type] = (this.on[type] || []).filter(f => f !== fn); }
  append(...xs) { for (const x of xs) this.kids.push(x); }
  replaceChildren(...xs) { this.kids = []; this.append(...xs); }
  remove() {}
  classList = { toggle() {}, add() {}, remove() {} };
}
const findEl = (el, pred, out = []) => {
  if (el instanceof AsideEl) { if (pred(el)) out.push(el); for (const k of el.kids) findEl(k, pred, out); }
  return out;
};
const tick = () => new Promise(r => setTimeout(r, 0));
function loaded(value) {
  const subs = new Set();
  const notify = () => { for (const fn of [...subs]) fn(); };
  return {
    get: () => value, error: () => null,
    subscribe(fn) { subs.add(fn); return () => subs.delete(fn); },
    notify,
    reload() {
      // Match the resource lifecycle: load start, a new snapshot, then the
      // completion-only notification after the running load is cleared.
      notify();
      if (value && typeof value === 'object') value = { ...value };
      notify();
      notify();
    },
  };
}
function asideWorld({ files, refs, hubNow, api }) {
  const detail = loaded({
    files,
    row: { thread: { thread_id: 'thr_x', participants: [], group: '', history_access: 'from_join', locked_participants: false, budget_remaining: null }, cut: null },
  });
  const conns = [];
  let n = 0;
  const admin = loaded({ peers: [] });
  const store = {
    resource() { return n++ ? admin : detail; },
    hubNow,
    onConn(fn) { conns.push(fn); return () => {}; },
  };
  const root = new AsideEl('aside');
  const off = mountAside(root, {
    api, store, threadId: 'thr_x', me: 'peer_op', nameOf: id => id, kindOf: () => 'peer', toast() {},
    peers: loaded({ peers: [] }),
    blobRefs: { get: () => refs, loaded: () => true, subscribe: () => () => {} },
  });
  return { root, detail, conns, off };
}

test('the mounted aside timer expires an unavailable file on the hub clock and disables download', async () => {
  // Five minutes ahead of this machine, so a browser-clock check would not
  // have reached the deadline when the timer fires.
  const skew = 5 * 60 * 1000;
  const hubNow = () => Date.now() + skew;
  const id = 'blo_0000000000000000000000000a';
  const exp = new Date(hubNow() + 80).toISOString();
  const att = { retention_class: '1h', expires_at: exp, ...verified(true) };
  const refs = new Map([refOf(id, att, { state: 'unavailable', retention_class: '1h', expires_at: exp, ...verified(true) })]);
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new AsideEl(tag) };
  try {
    const world = asideWorld({
      files: [r2file(id)], refs, hubNow,
      api: { blobStatus: () => Promise.reject(new Error('unused')), thread: () => Promise.reject(new Error('unused')), adminPeers: () => Promise.reject(new Error('unused')) },
    });
    const button = () => findEl(world.root, e => e.tag === 'button' && e.attrs['data-blob'] === id)[0];
    const first = button();
    assert.ok(first, 'the file is listed');
    assert.equal(first.attrs.disabled, undefined, 'unavailable is still downloadable before the deadline');
    assert.match(first.className, /s-unavailable/);
    await new Promise(r => setTimeout(r, 700));
    const later = button();
    assert.match(later.className, /s-expired/);
    assert.equal(later.attrs.disabled, '', 'the deadline disables download');
    assert.match(later.textContent, /Attachment expired \(1-hour retention\)/);
    world.off();
  } finally {
    globalThis.document = prev;
  }
});

test('file metadata that fails once is fetched again on refresh and then sticks', async () => {
  const id = 'blo_0000000000000000000000000b';
  const sha = 'd'.repeat(64);
  let calls = 0;
  const api = {
    blobStatus() {
      calls++;
      if (calls === 1) return Promise.reject(new APIError(0, 'network', 'hub unreachable'));
      return Promise.resolve({ state: 'available', retention_class: 'keep', expires_at: null, sha256_verified: true, integrity: { algorithm: 'sha256', value: sha, verified: true } });
    },
  };
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new AsideEl(tag) };
  try {
    const world = asideWorld({ files: [r2file(id, { sha256: sha })], refs: new Map(), hubNow: () => NOW, api });
    const pending = findEl(world.root, e => e.attrs['data-blob'] === id)[0];
    assert.match(pending.className, /s-pending/, pending.className);
    assert.match(pending.textContent, /Status pending/);
    assert.equal(pending.attrs.disabled, '', 'a pending read is not downloadable');
    await tick();
    assert.equal(calls, 1, 'the miss asks once');
    const before = findEl(world.root, e => e.attrs['data-blob'] === id)[0];
    assert.match(before.className, /s-unknown/, before.className);
    assert.match(before.textContent, /Status unknown/);
    assert.equal(before.attrs.disabled, '', 'a failed read is not downloadable');
    assert.doesNotMatch(before.textContent, /Kept while referenced/);
    world.detail.reload();
    await tick();
    assert.equal(calls, 2, 'refresh retries the failed read');
    const after = findEl(world.root, e => e.attrs['data-blob'] === id)[0];
    assert.match(after.textContent, /Kept while referenced/);
    world.detail.reload();
    await tick();
    assert.equal(calls, 2, 'a successful read is not asked again');
    world.off();
  } finally {
    globalThis.document = prev;
  }
});

test('file metadata retries on refresh and reconnect, once per signal, and can recover after three failures', async () => {
  const id = 'blo_0000000000000000000000000c';
  const sha = 'd'.repeat(64);
  let started = 0;
  const reads = [];
  const api = {
    blobStatus() {
      started++;
      // Keep every attempt deferred so completion-only notifications arrive
      // while it is in flight, before either rejection or recovery.
      return new Promise((resolve, reject) => reads.push({ resolve, reject }));
    },
  };
  const fail = async () => {
    reads.at(-1).reject(new APIError(503, 'blob_unavailable', 'storage unavailable'));
    await tick();
  };
  const prev = globalThis.document;
  globalThis.document = { createElement: tag => new AsideEl(tag) };
  try {
    const world = asideWorld({ files: [r2file(id, { sha256: sha })], refs: new Map(), hubNow: () => NOW, api });
    const row = () => findEl(world.root, e => e.attrs['data-blob'] === id)[0];
    assert.match(row().className, /s-pending/);
    assert.equal(row().attrs.disabled, '');
    world.detail.reload();
    assert.equal(started, 1, 'an in-flight read is not asked twice');
    await fail();
    assert.equal(started, 1, 'the failure is cached rather than retried on its own redraw');
    assert.match(row().textContent, /Status unknown/);
    assert.equal(row().attrs.disabled, '', 'a failed read is not an available download');
    for (const fn of world.conns) fn('reconnecting');
    await tick();
    assert.equal(started, 1, 'reconnecting does not retry');
    for (const fn of world.conns) fn('live');
    assert.equal(started, 2, 'a live connection retries the failed read');
    world.detail.notify();
    await fail();
    assert.equal(started, 2, 'completion during a reconnect attempt does not arm another read');
    world.detail.reload();
    assert.equal(started, 3);
    await fail();
    assert.equal(started, 3, 'the third failure does not ask again by itself');
    assert.match(row().className, /s-unknown/);
    // The old three-try ceiling must not stick for the rest of the mount.
    // Each later refresh or live reconnect is one attempt, and a rejection
    // does not chain into the next one.
    world.detail.reload();
    assert.equal(started, 4, 'a later refresh retries once after three failures');
    await fail();
    assert.equal(started, 4, 'that failure does not chain into another read');
    world.detail.notify();
    await tick();
    assert.equal(started, 4, 'completion alone is not a refresh opportunity');
    for (const fn of world.conns) fn('reconnecting');
    await tick();
    assert.equal(started, 4, 'reconnecting is not a retry');
    for (const fn of world.conns) fn('live');
    assert.equal(started, 5, 'a later live reconnect retries once');
    world.detail.notify();
    assert.match(row().className, /s-pending/);
    reads.at(-1).resolve({ state: 'available', retention_class: 'keep', expires_at: null, sha256_verified: true, integrity: { algorithm: 'sha256', value: sha, verified: true } });
    await tick();
    const after = row();
    assert.match(after.textContent, /Kept while referenced/);
    assert.equal(after.attrs.disabled, undefined, 'a recovered file is downloadable');
    world.detail.reload();
    for (const fn of world.conns) fn('live');
    await tick();
    assert.equal(started, 5, 'a successful read is not asked again');
    world.off();
  } finally {
    globalThis.document = prev;
  }
});
