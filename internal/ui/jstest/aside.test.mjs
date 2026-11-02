import test from 'node:test';
import assert from 'node:assert/strict';
import { budgetBar, fileRows, participantEntry, presenceLabel, reachLabel } from '../static/aside-model.js';

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
