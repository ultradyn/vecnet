import test from 'node:test';
import assert from 'node:assert/strict';
import { budgetBar, fileRows, participantEntry, presenceLabel, reachLabel } from '../static/aside-model.js';
import { blobView, downloadErrorText, nextExpiry } from '../static/attach-model.js';
import { APIError } from '../static/api.js';

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

test('download errors: expired and purged differ; a rate limit shows its retry delay; nothing echoes a URL', () => {
  assert.equal(downloadErrorText(new APIError(410, 'blob_expired', ''), NOW, '1d'), 'Attachment expired (24-hour retention)');
  assert.equal(downloadErrorText(new APIError(410, 'blob_purged', ''), NOW, '1d'), 'Attachment purged from the hub');
  assert.match(downloadErrorText(new APIError(429, 'blob_url_rate_limited', '', { retry_after_seconds: 42 }), NOW), /try again in 42 s/);
  assert.match(downloadErrorText(new APIError(429, 'blob_url_rate_limited', '', {}), NOW), /try again shortly/);
  assert.match(downloadErrorText(new APIError(0, 'blob_checksum_mismatch', ''), NOW), /integrity|checksum/i);
  assert.match(downloadErrorText(new APIError(0, 'storage_unreachable', ''), NOW), /storage/i);
  assert.match(downloadErrorText(new APIError(503, 'blob_unavailable', ''), NOW), /unavailable/i);
});
