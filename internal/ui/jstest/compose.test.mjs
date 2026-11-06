import test from 'node:test';
import assert from 'node:assert/strict';
import { SPILL, TEXT_CAP, anyGrokbot, buildSend, canSend, composePriority, counter, eligible, errorText, GROK_QUOTA_DETAIL, GROK_QUOTA_NOTE, GROK_QUOTA_PLAIN, GROK_QUOTA_TITLE, grokQuotaNote, keepKey, keyFor, matches, pendingMatches, priorityLocked, sendRefusal } from '../static/compose-model.js';
import { SPILL_SLOT } from '../static/attach-model.js';

const peers = [
  { peer_id: 'peer_op', name: 'max', kind: 'operator' },
  { peer_id: 'peer_gh', name: 'gh-ultradyn', kind: 'source' },
  { peer_id: 'peer_g', name: 'grok-reviewer', kind: 'grokbot' },
  { peer_id: 'peer_c', name: 'Codex-hark', kind: 'session' },
  { peer_id: 'peer_d', name: 'dot-research', kind: 'dot' },
];

test('eligible drops operator and source', () => {
  const got = eligible(peers);
  assert.deepEqual(got.map(p => p.peer_id), ['peer_c', 'peer_d', 'peer_g'], 'sorted by name, case-insensitive');
  assert.ok(got.every(p => p.kind !== 'operator' && p.kind !== 'source'));
  // The signed-in operator is excluded by id too, whatever kind the directory says.
  assert.deepEqual(eligible([{ peer_id: 'peer_me', name: 'me', kind: 'session' }, ...peers], 'peer_me').map(p => p.peer_id), ['peer_c', 'peer_d', 'peer_g']);
  // The directory response object is accepted as well as its array.
  assert.deepEqual(eligible({ peers }).map(p => p.peer_id), ['peer_c', 'peer_d', 'peer_g']);
  assert.deepEqual(eligible(undefined), []);
  assert.deepEqual(eligible(null), []);
  // The input is not reordered in place.
  assert.equal(peers[0].peer_id, 'peer_op');
});

test('matches searches by name, case-insensitive, and an empty query matches all', () => {
  assert.ok(matches(peers[3], 'codex'));
  assert.ok(matches(peers[3], '  HARK '));
  assert.ok(!matches(peers[3], 'grok'));
  assert.ok(matches(peers[3], ''));
  // Searching is by name only: an id is not a name.
  assert.ok(!matches(peers[3], 'peer_c'));
});

test('buildSend uses ids, omits empty title and default advanced fields, requires priority', () => {
  const form = { recipients: ['peer_c', 'peer_g'], title: '   ', priority: 'wake', text: 'Ship it.', historyAccess: 'from_join', lockedParticipants: false };
  const body = buildSend(form, 'ui-k1');
  assert.deepEqual(body, { idempotency_key: 'ui-k1', recipients: ['peer_c', 'peer_g'], priority: 'wake', content: { text: 'Ship it.' } });
  assert.ok(!('thread_title' in body), 'empty title omitted');
  assert.ok(!('history_access' in body), 'default history_access omitted');
  assert.ok(!('locked_participants' in body), 'default locked_participants omitted');
  assert.ok(!('thread_id' in body), 'a new message never names a thread');

  // Missing advanced fields read as their defaults.
  assert.deepEqual(buildSend({ recipients: ['peer_c'], priority: 'hold', text: 'x' }, 'ui-k2'),
    { idempotency_key: 'ui-k2', recipients: ['peer_c'], priority: 'hold', content: { text: 'x' } });

  // Non-default values travel, and the title is trimmed.
  const full = buildSend({ ...form, title: '  Release  ', historyAccess: 'full', lockedParticipants: true, priority: 'wake_idle' }, 'ui-k3');
  assert.equal(full.thread_title, 'Release');
  assert.equal(full.history_access, 'full');
  assert.equal(full.locked_participants, true);
  assert.equal(full.priority, 'wake_idle');

  // Duplicate picks collapse; order is kept. The form array is copied, not shared.
  const dup = buildSend({ ...form, recipients: ['peer_g', 'peer_c', 'peer_g'] }, 'ui-k4');
  assert.deepEqual(dup.recipients, ['peer_g', 'peer_c']);
  assert.notEqual(body.recipients, form.recipients);

  // Text is sent as typed, not trimmed.
  assert.equal(buildSend({ ...form, text: '  indented\n' }, 'k').content.text, '  indented\n');

  // Priority is required and must be one of the three.
  assert.throws(() => buildSend({ ...form, priority: '' }, 'k'), /priority/);
  assert.throws(() => buildSend({ ...form, priority: undefined }, 'k'), /priority/);
  assert.throws(() => buildSend({ ...form, priority: 'urgent' }, 'k'), /priority/);

  // Recipients are ids, never names (PROTOCOL §14.4), and at least one is required.
  assert.throws(() => buildSend({ ...form, recipients: ['grok-reviewer'] }, 'k'), /id/);
  assert.throws(() => buildSend({ ...form, recipients: [] }, 'k'), /recipient/);
  assert.ok(buildSend({ ...form, recipients: ['team_x', 'grp_y'] }, 'k'));
  // History access accepts only the protocol's two values; the key is required.
  assert.throws(() => buildSend({ ...form, historyAccess: 'everything' }, 'k'), /history_access/);
  assert.throws(() => buildSend(form, ''), /key/);
  // Empty text cannot be sent.
  assert.throws(() => buildSend({ ...form, text: '  ' }, 'k'), /text/);
});

test('canSend needs a recipient and non-blank text under the 64 KiB cap', () => {
  const ok = { recipients: ['peer_c'], priority: 'wake', text: 'hi' };
  assert.equal(canSend(ok), true);
  assert.equal(canSend({ ...ok, recipients: [] }), false);
  assert.equal(canSend({ ...ok, text: '' }), false);
  assert.equal(canSend({ ...ok, text: ' \n\t' }), false);
  assert.equal(canSend({ ...ok, text: 'x'.repeat(TEXT_CAP) }), true);
  assert.equal(canSend({ ...ok, text: 'x'.repeat(TEXT_CAP + 1) }), false);
  // The cap is in UTF-8 bytes, not characters.
  assert.equal(canSend({ ...ok, text: 'é'.repeat(TEXT_CAP / 2 + 1) }), false);
});

test('counter is the composer byte counter', () => {
  assert.deepEqual(counter(''), { label: '0 / 4096 B', tone: '' });
  assert.deepEqual(counter('é'), { label: '2 / 4096 B', tone: '' });
  assert.deepEqual(counter('x'.repeat(SPILL)), { label: '4096 / 4096 B', tone: '' });
  assert.deepEqual(counter('x'.repeat(SPILL + 1)), { label: '4.0 KiB · spills to message.md', tone: 'spill' });
  assert.deepEqual(counter('x'.repeat(TEXT_CAP + 1)), { label: '64.0 KiB · spills to message.md', tone: 'over' });
});

test('keyFor keeps the idempotency key while the body is unchanged and mints one when it changes', () => {
  let n = 0;
  const mint = () => 'ui-' + (++n);
  const form = { recipients: ['peer_c'], title: '', priority: 'wake', text: 'hi', historyAccess: 'from_join', lockedParticipants: false };
  const a = keyFor(null, form, mint);
  assert.equal(a.key, 'ui-1');
  assert.equal(keyFor(a, { ...form }, mint), a, 'same body, same pending key');
  assert.equal(keyFor(a, { ...form, text: 'hi!' }, mint).key, 'ui-2');
  assert.equal(keyFor(a, { ...form, priority: 'hold' }, mint).key, 'ui-3');
  assert.equal(keyFor(a, { ...form, title: 'T' }, mint).key, 'ui-4');
  assert.equal(keyFor(a, { ...form, recipients: ['peer_c', 'peer_g'] }, mint).key, 'ui-5');
  assert.equal(keyFor(a, { ...form, historyAccess: 'full' }, mint).key, 'ui-6');
  assert.equal(keyFor(a, { ...form, lockedParticipants: true }, mint).key, 'ui-7');
  // A whitespace-only title change does not change the body, so the key stays.
  assert.equal(keyFor(a, { ...form, title: '  ' }, mint), a);
});

test('keepKey keeps the key across a retryable failure and drops it after a definite rejection', () => {
  assert.equal(keepKey({ status: 0 }), true, 'hub never reached');
  assert.equal(keepKey({ status: 502 }), true);
  assert.equal(keepKey({ status: 429 }), true, 'rate limited: the same send is retried');
  assert.equal(keepKey({ status: 409 }), true);
  assert.equal(keepKey({ status: 403 }), false, 'scope_denied will not change on retry');
  assert.equal(keepKey({ status: 422 }), false);
  assert.equal(keepKey({ status: 404 }), false);
});

test('errorText names the denied recipient and shows the retry time', () => {
  const nameOf = id => ({ peer_g: 'grok-reviewer', team_t: 'reviewers' })[id] || id;
  assert.equal(errorText({ status: 403, code: 'scope_denied', message: 'send scopes forbid this recipient', details: { recipient: 'peer_g' } }, nameOf),
    'Your send scopes do not allow messages to grok-reviewer.');
  assert.equal(errorText({ status: 403, code: 'scope_denied', message: 'm', details: { recipient: 'peer_g', via: 'team_t' } }, nameOf),
    'Your send scopes do not allow messages to grok-reviewer (via reviewers).');
  assert.equal(errorText({ status: 403, code: 'scope_denied', message: 'send scopes forbid this recipient', details: {} }, nameOf),
    'scope_denied: send scopes forbid this recipient');

  const now = new Date(2026, 10, 2, 14, 5, 0).getTime();
  assert.equal(errorText({ status: 429, code: 'rate_limited', message: 'threads rate limit exceeded', details: { retry_after_seconds: 90 } }, nameOf, now),
    'Rate limited. Try again at 14:06:30 (in 90 s).');
  assert.equal(errorText({ status: 429, code: 'rate_limited', message: 'threads rate limit exceeded', details: {} }, nameOf, now),
    'Rate limited. Try again shortly.');

  assert.equal(errorText({ status: 0, code: 'network', message: 'hub unreachable', details: {} }, nameOf), 'network: hub unreachable');
  assert.equal(errorText({ status: 422, code: 'name_not_an_address', message: 'display name', details: {} }, nameOf), 'name_not_an_address: display name');
  // The hub's 413 for a 17th attachment (the spilled message.md) reads as the form's own refusal.
  assert.equal(errorText({ status: 413, code: 'payload_too_large', message: 'too many attachments', details: { field: 'attachments', limit: 15 } }, nameOf), SPILL_SLOT);
  assert.equal(errorText({ status: 413, code: 'payload_too_large', message: 'body too large', details: { field: 'content.text' } }, nameOf), 'payload_too_large: body too large');
});

const att = n => ({ sha256: String(n).padStart(64, '0'), size: 100 + n, media_type: 'image/png', name: 'shot-' + n + '.png' });
const atts = n => Array.from({ length: n }, (_, i) => att(i + 1));

test('buildSend with only attachments → content.text "" and attachments', () => {
  const two = atts(2);
  const body = buildSend({ recipients: ['peer_c'], priority: 'wake', text: '', attachments: two }, 'ui-a1');
  assert.deepEqual(body, { idempotency_key: 'ui-a1', recipients: ['peer_c'], priority: 'wake', content: { text: '' }, attachments: two });
  assert.notEqual(body.attachments, two, 'the form array is copied, not shared');
  // Missing text reads as empty when there is an attachment.
  assert.equal(buildSend({ recipients: ['peer_c'], priority: 'wake', attachments: two }, 'k').content.text, '');
  // Text and attachments travel together; order is kept.
  const both = buildSend({ recipients: ['peer_c'], priority: 'wake', text: 'see attached', attachments: [two[1], two[0]] }, 'k');
  assert.equal(both.content.text, 'see attached');
  assert.deepEqual(both.attachments.map(a => a.sha256), [two[1].sha256, two[0].sha256]);
  // No attachments: the key is omitted entirely.
  assert.ok(!('attachments' in buildSend({ recipients: ['peer_c'], priority: 'wake', text: 'x', attachments: [] }, 'k')));
  // Neither text nor attachments: refused with the new message.
  assert.throws(() => buildSend({ recipients: ['peer_c'], priority: 'wake', text: '  ', attachments: [] }, 'k'), /text or at least one attachment is required/);
  assert.throws(() => buildSend({ recipients: ['peer_c'], priority: 'wake', text: '' }, 'k'), /text or at least one attachment is required/);
  // More than the hub's 16 is refused before it is sent.
  assert.throws(() => buildSend({ recipients: ['peer_c'], priority: 'wake', text: '', attachments: atts(17) }, 'k'), /16/);
});

test('canSend false while uploads are not ready', () => {
  const ok = { recipients: ['peer_c'], priority: 'wake', text: '', attachments: atts(1), uploadsReady: true };
  assert.equal(canSend(ok), true, 'attachments alone may be sent');
  assert.equal(canSend({ ...ok, uploadsReady: false }), false, 'a chip still uploading or failed');
  assert.equal(canSend({ ...ok, text: 'hi', uploadsReady: false }), false, 'text does not override unready uploads');
  assert.equal(canSend({ ...ok, attachments: [], uploadsReady: false }), false, 'a failed chip with nothing done');
  assert.equal(canSend({ ...ok, attachments: [] }), false, 'neither text nor attachments');
  assert.equal(canSend({ ...ok, recipients: [] }), false, 'no recipient');
  // A form from before attachments (no uploadsReady field) keeps today's rule.
  assert.equal(canSend({ recipients: ['peer_c'], priority: 'wake', text: 'hi' }), true);
  // A refusal set by the caller blocks the send.
  assert.equal(canSend({ ...ok, sendRefusal: 'no' }), false);
});

test('canSend false with 16 attachments and 5000-byte text', () => {
  const form = { recipients: ['peer_c'], priority: 'wake', text: 'x'.repeat(5000), attachments: atts(16), uploadsReady: true };
  assert.equal(canSend(form), false);
  assert.equal(sendRefusal(form), SPILL_SLOT, 'the spill-slot message explains why');
  // One fewer, or text at the spill threshold, fits.
  assert.equal(canSend({ ...form, attachments: atts(15) }), true);
  assert.equal(sendRefusal({ ...form, attachments: atts(15) }), '');
  assert.equal(canSend({ ...form, text: 'x'.repeat(SPILL) }), true);
  assert.equal(sendRefusal({ ...form, text: 'x'.repeat(SPILL) }), '');
  // The threshold is in UTF-8 bytes: 2049 × é is 4098 bytes.
  assert.equal(canSend({ ...form, text: 'é'.repeat(2049) }), false);
});

test('keyFor: same text, different attachments → new key', () => {
  let n = 0;
  const mint = () => 'ui-' + (++n);
  const form = { recipients: ['peer_c'], title: '', priority: 'wake', text: 'see attached', attachments: atts(2), uploadsReady: true };
  const a = keyFor(null, form, mint);
  assert.equal(a.key, 'ui-1');
  assert.equal(keyFor(a, { ...form, attachments: atts(2) }, mint), a, 'unchanged attachments keep the key');
  assert.equal(keyFor(a, { ...form, attachments: atts(1) }, mint).key, 'ui-2', 'a removed chip mints a new key');
  assert.equal(keyFor(a, { ...form, attachments: [att(2), att(1)] }, mint).key, 'ui-3', 'reordered chips mint a new key');
  assert.equal(keyFor(a, { ...form, attachments: [att(1), att(3)] }, mint).key, 'ui-4', 'a different file mints a new key');
  assert.equal(keyFor(a, { ...form, attachments: [] }, mint).key, 'ui-5', 'dropping every attachment mints a new key');
  // uploadsReady is not part of the body.
  assert.equal(keyFor(a, { ...form, uploadsReady: false }, mint), a);
});

// Decisions §6. priority stays on the wire (PROTOCOL §§2.2, 5.2). A grokbot
// among the recipients preselects wake_idle; everyone else keeps the
// existing initial priority. A manual choice and an in-flight or idempotent
// retry are left alone, including when the directory arrives late.
test('Grok recipient defaults preserve user choice', () => {
  const open = { manual: false, locked: false, current: 'wake', peers };
  assert.equal(composePriority({ ...open, recipients: [] }), 'wake', 'empty composer');
  assert.equal(composePriority({ ...open, recipients: ['peer_c'] }), 'wake', 'session recipient');
  assert.equal(composePriority({ ...open, recipients: ['peer_d'] }), 'wake', 'dot recipient');
  assert.equal(composePriority({ ...open, recipients: ['peer_g'] }), 'wake_idle', 'selecting a grokbot');
  assert.equal(composePriority({ ...open, current: 'wake_idle', recipients: ['peer_c', 'peer_g'] }), 'wake_idle', 'mixed destination, one grokbot');
  assert.equal(composePriority({ ...open, current: 'wake_idle', recipients: ['peer_c'] }), 'wake', 'removing the grokbot restores the initial priority');

  // The directory is not in yet: a recipient id is not treated as grokbot.
  assert.equal(composePriority({ ...open, recipients: ['peer_g'], peers: null }), 'wake', 'delayed directory');
  assert.equal(composePriority({ ...open, recipients: ['peer_g'], peers: undefined }), 'wake');
  assert.equal(composePriority({ ...open, recipients: ['peer_g'], peers: { peers } }), 'wake_idle', 'directory object arrives');
  // A later directory or presence reload must not overwrite wake or hold.
  assert.equal(composePriority({ manual: true, locked: false, current: 'wake', recipients: ['peer_g'], peers }), 'wake', 'explicit wake');
  assert.equal(composePriority({ manual: true, locked: false, current: 'hold', recipients: ['peer_g'], peers }), 'hold', 'explicit hold');
  assert.equal(composePriority({ manual: true, locked: false, current: 'wake', recipients: ['peer_c'], peers }), 'wake', 'explicit wake with no grokbot');
  assert.equal(composePriority({ manual: true, locked: false, current: 'wake_idle', recipients: ['peer_c'], peers }), 'wake_idle', 'manual wake_idle stays after the grokbot is gone');

  // In flight, and the retry of that same body, keep the priority already captured.
  const captured = composePriority({ manual: false, locked: true, current: 'wake', recipients: ['peer_g'], peers });
  assert.equal(captured, 'wake', 'in-flight send changed priority');
  let n = 0;
  const mint = () => 'ui-g' + (++n);
  const form = { recipients: ['peer_g', 'peer_c'], title: '', priority: captured, text: 'same body', historyAccess: 'from_join', lockedParticipants: false };
  const pending = keyFor(null, form, mint);
  const retry = keyFor(pending, { ...form }, mint);
  assert.equal(retry, pending, 'idempotent retry minted a new key');
  const body = buildSend(form, retry.key);
  assert.equal(body.priority, 'wake');
  assert.equal(body.content.text, 'same body');
  assert.deepEqual(body.recipients, ['peer_g', 'peer_c']);
  // The preselect is still an explicit field. Nothing omits it.
  const chosen = composePriority({ ...open, recipients: ['peer_g'] });
  const sent = buildSend({ recipients: ['peer_g'], priority: chosen, text: 'hello' }, 'ui-g-send');
  assert.equal(sent.priority, 'wake_idle');
  assert.equal(typeof sent.priority, 'string');

  const note = grokQuotaNote({ addressed: true, manual: false, priority: chosen });
  assert.equal(note, GROK_QUOTA_NOTE);
  assert.equal(grokQuotaNote({ addressed: false, manual: false, priority: 'wake' }), '');
});

// The visible note says why the control shows what it shows, in one short
// line; the timing and the second-run caveat live in the detail (title and
// aria-describedby). "Preselected" only while wake_idle is the automatic
// choice: after an explicit wake or hold it would describe the opposite of
// what that send does.
test('Grok quota note follows why the priority is selected', () => {
  assert.equal(GROK_QUOTA_NOTE, 'Preselected for Grok Bot · each run spends Grok quota');
  assert.equal(GROK_QUOTA_PLAIN, 'Grok Bot: each run spends Grok quota');
  assert.equal(GROK_QUOTA_TITLE, 'A run that also needs you in chat can start a second one.');
  assert.equal(GROK_QUOTA_DETAIL, 'wake_idle pushes now if the Grok Bot is idle or its presence is unknown, otherwise once it goes idle; hold never starts a run. A run that also needs you in chat can start a second one.');
  // One line at 11.5px in a phone-width composer.
  for (const line of [GROK_QUOTA_NOTE, GROK_QUOTA_PLAIN]) assert.ok(line.length <= 55, `${line.length} chars: ${line}`);
  for (const text of [GROK_QUOTA_NOTE, GROK_QUOTA_PLAIN, GROK_QUOTA_DETAIL]) {
    assert.doesNotMatch(text, /free/i);
    assert.doesNotMatch(text, /guarantee|no wake|never wake|won'?t wake|will not wake|without waking/i);
    assert.match(text, /Grok/);
  }
  assert.match(GROK_QUOTA_PLAIN, /quota/);
  assert.match(GROK_QUOTA_NOTE, /quota/);

  const grok = { addressed: true };
  assert.equal(grokQuotaNote({ ...grok, manual: false, priority: 'wake_idle' }), GROK_QUOTA_NOTE, 'automatic wake_idle');
  // Explicit choices, including choosing wake_idle by hand.
  for (const priority of ['wake', 'wake_idle', 'hold']) {
    const line = grokQuotaNote({ ...grok, manual: true, priority });
    assert.equal(line, GROK_QUOTA_PLAIN, 'explicit ' + priority);
    assert.doesNotMatch(line, /preselected/i, 'explicit ' + priority);
  }
  // A kept retry of wake (sent before the directory named the grokbot) is
  // not the preselect either.
  assert.equal(grokQuotaNote({ ...grok, manual: false, priority: 'wake' }), GROK_QUOTA_PLAIN, 'kept wake retry');
  assert.equal(grokQuotaNote({ ...grok, manual: false, priority: 'hold' }), GROK_QUOTA_PLAIN);
  for (const priority of ['wake', 'wake_idle', 'hold']) {
    assert.equal(grokQuotaNote({ addressed: false, manual: false, priority }), '', 'no grokbot, ' + priority);
    assert.equal(grokQuotaNote({ addressed: false, manual: true, priority }), '');
  }
  assert.equal(grokQuotaNote(), '');

  // The composer's flow: preselect, explicit wake, add the grokbot again.
  const recipients = ['peer_c', 'peer_g'];
  let current = composePriority({ manual: false, locked: false, current: 'wake', recipients, peers });
  assert.equal(grokQuotaNote({ addressed: anyGrokbot(recipients, peers), manual: false, priority: current }), GROK_QUOTA_NOTE);
  current = 'wake';
  current = composePriority({ manual: true, locked: false, current, recipients, peers });
  const after = grokQuotaNote({ addressed: anyGrokbot(recipients, peers), manual: true, priority: current });
  assert.equal(current, 'wake');
  assert.doesNotMatch(after, /preselected/i, 'the note still says preselected after an explicit wake');
});

// A 503 keeps the key. Swapping the session peer for a Grok Bot is a new
// draft, so automatic priority is chosen again before that key is minted.
// The unchanged retry, a manual choice, and an in-flight send stay put.
test('failure then recipient edit recomputes automatic priority', () => {
  let n = 0;
  const mint = () => 'ui-e' + (++n);
  const session = { recipients: ['peer_c'], title: '', priority: 'wake', text: 'hello', historyAccess: 'from_join', lockedParticipants: false };
  const pending = keyFor(null, session, mint);
  const again = { ...session };
  assert.equal(priorityLocked({ sending: false, pending, form: again }), true);
  assert.equal(composePriority({ manual: false, locked: priorityLocked({ sending: false, pending, form: again }), current: 'wake', recipients: again.recipients, peers }), 'wake');
  assert.equal(keyFor(pending, again, mint), pending, 'unchanged retry minted a new key');

  const swapped = { ...session, recipients: ['peer_g'] };
  assert.equal(pendingMatches(pending, swapped), false);
  assert.equal(priorityLocked({ sending: false, pending, form: swapped }), false);
  const priority = composePriority({ manual: false, locked: priorityLocked({ sending: false, pending, form: swapped }), current: 'wake', recipients: swapped.recipients, peers });
  assert.equal(priority, 'wake_idle');
  const next = { ...swapped, priority };
  const minted = keyFor(pending, next, mint);
  assert.notEqual(minted.key, pending.key, 'a changed draft reused the failed send\'s key');
  const body = buildSend(next, minted.key);
  assert.equal(body.priority, 'wake_idle');
  assert.equal(body.content.text, 'hello');
  assert.deepEqual(body.recipients, ['peer_g']);

  assert.equal(composePriority({ manual: true, locked: priorityLocked({ sending: false, pending, form: swapped }), current: 'hold', recipients: ['peer_g'], peers }), 'hold', 'manual hold was overwritten');
  assert.equal(composePriority({ manual: false, locked: priorityLocked({ sending: true, pending, form: swapped }), current: 'wake', recipients: ['peer_g'], peers }), 'wake', 'in-flight send changed priority');
});
