import test from 'node:test';
import assert from 'node:assert/strict';
import { budgetBanner, budgetOut, canShowFull, clientLabel, CLIENT_LABEL_CAP, clientSuspect, FULL_TEXT_CAP, GROK_QUOTA_NOTE, GROK_QUOTA_PLAIN, items, pendingReplyMatches, replyKey, replyPriority, replyPriorityLocked, replyQuotaNote, replySig, sendError, spillOf } from '../static/thread-model.js';
import { SPILL_SLOT } from '../static/attach-model.js';

const OP = 'peer_op', CODEX = 'peer_codex', SLEUTH = 'peer_sleuth', GROK = 'peer_grok';

function msg(id, sender, text, extra = {}) {
  return {
    message_id: id, thread_id: 'thr_1', sender, kind: 'message', priority: 'wake',
    created_at: '2026-11-02T10:00:00.000Z', recipients: [], content: { text }, ...extra,
  };
}
const cell = (recipient, display, state = display) => ({
  recipient, delivery_id: 'dly_' + recipient, state, display,
  updated_at: '2026-11-02T10:00:01.000Z', attempt: 1, last_status: null, read_at: display === 'read' ? '2026-11-02T10:00:02.000Z' : null,
});

test('items links parents, including a parent outside the loaded page', () => {
  const messages = [
    msg('msg_2', CODEX, 'loaded parent'),
    msg('msg_3', SLEUTH, 'reply to a loaded message', { in_reply_to: 'msg_2' }),
    msg('msg_4', GROK, 'reply to an older page', { in_reply_to: 'msg_1' }),
    msg('msg_5', GROK, 'reply to a message nobody has', { in_reply_to: 'msg_0' }),
    msg('msg_6', CODEX, 'no parent'),
  ];
  // The matrix pages independently, so it may name a message the history page does not hold.
  const matrix = { recipients: [OP], rows: [{ message_id: 'msg_1', created_at: '2026-11-02T09:00:00.000Z', sender: OP, excerpt: 'the older question', cells: [] }] };
  const out = items(messages, matrix, []);

  assert.equal(out.length, 5);
  assert.deepEqual(out.map(i => i.message.message_id), ['msg_2', 'msg_3', 'msg_4', 'msg_5', 'msg_6']);
  assert.equal(out[0].parent, null);
  assert.deepEqual(out[1].parent, { message_id: 'msg_2', sender: CODEX, excerpt: 'loaded parent', loaded: true });
  // Outside the loaded page: the matrix row supplies who and what, but there is nothing to jump to.
  assert.deepEqual(out[2].parent, { message_id: 'msg_1', sender: OP, excerpt: 'the older question', loaded: false });
  // Known to nobody: the id alone, still a link target the view can name.
  assert.deepEqual(out[3].parent, { message_id: 'msg_0', sender: null, excerpt: '', loaded: false });
  assert.equal(out[4].parent, null);
  for (const i of out) assert.equal(i.system, false);
});

test('hub-authored approval_response is a system item', () => {
  const request = msg('msg_1', CODEX, 'Ship it?', {
    kind: 'approval_request',
    approval_request: { approval_id: 'apr_1', question: 'Ship it?', options: ['yes', 'no'], expires_at: '2026-11-02T10:01:00.000Z' },
  });
  const expiry = msg('msg_2', 'hub', 'Approval apr_1 expired without a decision.', {
    kind: 'approval_response', priority: 'hold', in_reply_to: 'msg_1',
    approval_response: { approval_id: 'apr_1', decision: 'expired' },
  });
  const human = msg('msg_3', OP, 'yes', { kind: 'approval_response', approval_response: { approval_id: 'apr_9', decision: 'yes' } });
  const out = items([request, expiry, human], null, []);

  assert.equal(out[1].system, true);
  assert.equal(out[1].text, 'approval expired');
  // A system line is quiet: no receipts, no approval card of its own.
  assert.deepEqual(out[1].receipts, []);
  assert.equal(out[1].approval, null);
  // An operator's answer is an ordinary message, whatever its kind.
  assert.equal(out[2].system, false);
  assert.equal(out[0].system, false);
  // The request it closed reads closed and expired, never open.
  assert.equal(out[0].approval.open, false);
  assert.equal(out[0].approval.decision, 'expired');
});

test('spill detected from metadata vecnet.spilled and the attachment', () => {
  const md = { sha256: 'a'.repeat(64), size: 12288, media_type: 'text/markdown', name: 'message.md' };
  const log = { sha256: 'b'.repeat(64), size: 40, media_type: 'text/plain', name: 'hark-listen.log' };
  const spilled = msg('msg_1', CODEX, 'preview…', { content: { text: 'preview…', truncated: true }, metadata: { 'vecnet.spilled': true }, attachments: [log, md] });
  const metadataOnly = msg('msg_2', CODEX, 'x', { metadata: { 'vecnet.spilled': true } });
  // A peer may name its own file message.md; without the hub's metadata it is an ordinary file.
  const nameOnly = msg('msg_3', CODEX, 'x', { attachments: [md] });
  const notTrue = msg('msg_4', CODEX, 'x', { metadata: { 'vecnet.spilled': 'true' }, attachments: [md] });
  const plain = msg('msg_5', CODEX, 'x');

  assert.deepEqual(spillOf(spilled), md);
  assert.equal(spillOf(metadataOnly), null);
  assert.equal(spillOf(nameOnly), null);
  assert.equal(spillOf(notTrue), null);
  assert.equal(spillOf(plain), null);
  // The hub appends message.md last; with no file of that name, the last attachment is the spill.
  const odd = { ...md, name: 'other.md' };
  assert.deepEqual(spillOf(msg('msg_6', CODEX, 'x', { metadata: { 'vecnet.spilled': true }, attachments: [log, odd] })), odd);

  const out = items([spilled, metadataOnly, nameOnly, plain], null, []);
  assert.deepEqual(out.map(i => i.spill), [md, null, null, null]);

  // Show full text fetches only text/* up to 1 MiB.
  assert.equal(FULL_TEXT_CAP, 1 << 20);
  assert.equal(canShowFull(md), true);
  assert.equal(canShowFull({ ...md, media_type: 'text/plain; charset=utf-8' }), true);
  assert.equal(canShowFull({ ...md, size: FULL_TEXT_CAP }), true);
  assert.equal(canShowFull({ ...md, size: FULL_TEXT_CAP + 1 }), false);
  assert.equal(canShowFull({ ...md, media_type: 'application/octet-stream' }), false);
  assert.equal(canShowFull({ ...md, media_type: 'image/svg+xml' }), false);
  assert.equal(canShowFull(null), false);
});

test('receipts map cell display per recipient; no cell for the sender', () => {
  const messages = [msg('msg_1', CODEX, 'from codex'), msg('msg_2', OP, 'from me'), msg('msg_3', SLEUTH, 'no matrix row')];
  const matrix = {
    recipients: [OP, CODEX, SLEUTH, GROK],
    rows: [
      { message_id: 'msg_1', created_at: '', sender: CODEX, excerpt: '', cells: [cell(CODEX, 'accepted'), cell(OP, 'read', 'accepted'), cell(SLEUTH, 'processed'), cell(GROK, 'dead_letter')] },
      { message_id: 'msg_2', created_at: '', sender: OP, excerpt: '', cells: [cell(CODEX, 'queued'), cell(OP, 'accepted'), cell(GROK, 'pushed')] },
    ],
  };
  const out = items(messages, matrix, []);

  // display, not state: a read delivery reads "read" even while its state is accepted.
  assert.deepEqual(out[0].receipts, [
    { recipient: OP, display: 'read', state: 'accepted', delivery_id: 'dly_' + OP },
    { recipient: SLEUTH, display: 'processed', state: 'processed', delivery_id: 'dly_' + SLEUTH },
    { recipient: GROK, display: 'dead_letter', state: 'dead_letter', delivery_id: 'dly_' + GROK },
  ]);
  assert.ok(!out[0].receipts.some(r => r.recipient === CODEX), 'the sender got a receipt marker');
  // Messages the operator sent and did not send are treated alike.
  assert.deepEqual(out[1].receipts.map(r => [r.recipient, r.display]), [[CODEX, 'queued'], [GROK, 'pushed']]);
  assert.deepEqual(out[2].receipts, []);
  assert.deepEqual(items(messages, null, []).map(i => i.receipts), [[], [], []]);
});

test('approval is actionable only when in openApprovals with addressed_to_me', () => {
  const ask = (id, apr) => msg(id, CODEX, 'q ' + apr, {
    kind: 'approval_request',
    approval_request: { approval_id: apr, question: 'Question ' + apr + '?', options: ['yes', 'no'], expires_at: '2026-11-02T10:30:00.000Z' },
  });
  const messages = [ask('msg_1', 'apr_mine'), ask('msg_2', 'apr_theirs'), ask('msg_3', 'apr_closed'), msg('msg_4', CODEX, 'plain')];
  const open = [
    { approval_id: 'apr_mine', message_id: 'msg_1', expires_at: '2026-11-02T10:20:00.000Z', options: ['yes', 'no', 'later'], addressed_to_me: true },
    { approval_id: 'apr_theirs', message_id: 'msg_2', expires_at: '2026-11-02T10:30:00.000Z', options: ['yes', 'no'], addressed_to_me: false },
  ];
  const out = items(messages, null, open);

  const mine = out[0].approval;
  assert.equal(mine.open, true);
  assert.equal(mine.actionable, true);
  assert.equal(mine.approval_id, 'apr_mine');
  assert.equal(mine.message_id, 'msg_1');
  assert.equal(mine.question, 'Question apr_mine?');
  // The hub's open record wins over the request body for expiry and options.
  assert.equal(mine.expires_at, '2026-11-02T10:20:00.000Z');
  assert.deepEqual(mine.options, ['yes', 'no', 'later']);

  assert.equal(out[1].approval.open, true);
  assert.equal(out[1].approval.actionable, false);
  // Not in openApprovals: closed, read-only, whatever its own expiry says.
  assert.equal(out[2].approval.open, false);
  assert.equal(out[2].approval.actionable, false);
  assert.equal(out[2].approval.decision, null);
  assert.equal(out[3].approval, null);
  // addressed_to_me must be exactly true.
  const loose = items([messages[0]], null, [{ ...open[0], addressed_to_me: 'yes' }]);
  assert.equal(loose[0].approval.actionable, false);
});

test('sendError names the budget, keeps code: message, and adds Retry-After', () => {
  const err = (status, code, message, details = {}) => ({ status, code, message, details });
  const budget = sendError(err(422, 'thread_budget_exhausted', 'thread budget is exhausted'));
  assert.equal(budget.kind, 'budget');
  assert.match(budget.text, /budget/i);
  assert.match(budget.text, /0 messages left/);
  // PROTOCOL §2.2: this rejection means the budget is 0, whatever a stale snapshot said.
  const stale = sendError(err(422, 'thread_budget_exhausted', 'thread budget is exhausted'), { budget_remaining: 3 });
  assert.match(stale.text, /0 messages left/);
  assert.doesNotMatch(stale.text, /3/);
  assert.equal(sendError(err(403, 'participants_locked', 'participants are locked')).text, 'participants_locked: participants are locked');
  assert.equal(sendError(err(403, 'scope_denied', 'scope forbids peer_x')).text, 'scope_denied: scope forbids peer_x');
  assert.equal(sendError(err(429, 'rate_limited', 'send rate limit exceeded', { retry_after_seconds: 17 })).text, 'rate_limited: send rate limit exceeded (retry after 17 s)');
  assert.equal(sendError(err(0, 'network', 'hub unreachable')).text, 'network: hub unreachable');
  // A definite rejection mints a new key next time; a lost or throttled send keeps it.
  assert.equal(sendError(err(0, 'network', 'hub unreachable')).keepKey, true);
  assert.equal(sendError(err(503, 'unavailable', 'x')).keepKey, true);
  assert.equal(sendError(err(429, 'rate_limited', 'x')).keepKey, true);
  assert.equal(sendError(err(422, 'thread_budget_exhausted', 'x')).keepKey, false);
  assert.equal(sendError(err(403, 'scope_denied', 'x')).keepKey, false);
});

test('the budget banner speaks to the operator and shows before a refused send', () => {
  const b = budgetBanner();
  assert.equal(b.kind, 'budget');
  assert.match(b.text, /0 messages left/);
  assert.match(b.text, /you raise the budget/);
  assert.doesNotMatch(b.text, /the operator/);
  assert.equal(budgetOut({ budget_remaining: 0 }), true);
  assert.equal(budgetOut({ budget_remaining: 3 }), false);
  assert.equal(budgetOut({ budget_remaining: null }), false);
  assert.equal(budgetOut(null), false);
});

test('replyKey keeps the key for unchanged text, prio, replyTo and sig; a changed sig mints a new one', () => {
  let n = 0;
  const mint = () => 'ui-key-' + ++n;
  const sigA = 'a'.repeat(64), sigAB = sigA + ',' + 'b'.repeat(64);
  const first = replyKey(null, { text: 'look at this', prio: 'wake', replyTo: 'msg_1', sig: sigA }, mint);
  assert.deepEqual(first, { key: 'ui-key-1', text: 'look at this', prio: 'wake', replyTo: 'msg_1', sig: sigA });
  // An unchanged retry (the reply was lost) keeps the very same pending send.
  assert.equal(replyKey(first, { text: 'look at this', prio: 'wake', replyTo: 'msg_1', sig: sigA }, mint), first);
  assert.equal(n, 1);
  // Each of the four parts mints a new key when it changes.
  const changed = [
    { text: 'look at this!', prio: 'wake', replyTo: 'msg_1', sig: sigA },
    { text: 'look at this', prio: 'hold', replyTo: 'msg_1', sig: sigA },
    { text: 'look at this', prio: 'wake', replyTo: '', sig: sigA },
    { text: 'look at this', prio: 'wake', replyTo: null, sig: sigA },
    { text: 'look at this', prio: 'wake', replyTo: 'msg_1', sig: sigAB },
    { text: 'look at this', prio: 'wake', replyTo: 'msg_1', sig: '' },
  ];
  for (const next of changed) {
    const got = replyKey(first, next, mint);
    assert.notEqual(got.key, first.key, JSON.stringify(next));
    assert.deepEqual(got, { key: 'ui-key-' + n, ...next });
  }
  assert.equal(n, 1 + changed.length);
  // Removing the chip (sig back to '') and sending again is a new send, not the old key.
  const textOnly = replyKey(null, { text: 'x', prio: 'wake', replyTo: null, sig: '' }, mint);
  assert.equal(replyKey(textOnly, { text: 'x', prio: 'wake', replyTo: null, sig: '' }, mint), textOnly);
  assert.notEqual(replyKey(textOnly, { text: 'x', prio: 'wake', replyTo: null, sig: sigA }, mint).key, textOnly.key);
});

test('sendError: the hub refusing a spill slot shows the attachment-slot text', () => {
  const e = Object.assign(new Error('too many attachments'), { status: 413, code: 'payload_too_large', details: { field: 'attachments', limit: 15 } });
  assert.deepEqual(sendError(e), { kind: 'error', keepKey: false, text: SPILL_SLOT });
  // Any other 413 keeps today's code: message.
  const other = Object.assign(new Error('the message is over 64 KiB'), { status: 413, code: 'payload_too_large', details: {} });
  assert.equal(sendError(other).text, 'payload_too_large: the message is over 64 KiB');
});

test('replySig covers every attachment wire field: same bytes under a new name or media type mint a new key; an unchanged retry keeps it', () => {
  const sha = 'a'.repeat(64), other = 'b'.repeat(64);
  const file = { sha256: sha, size: 3, media_type: 'image/png', name: 'pasted-20261103-040546.png' };
  let n = 0;
  const mint = () => 'k' + ++n;
  const send = files => ({ text: '', prio: 'wake', replyTo: null, sig: replySig(files) });
  const first = replyKey(null, send([file]), mint);
  assert.equal(replyKey(first, send([{ ...file }]), mint), first, 'an unchanged retry kept the key');
  for (const changed of [{ name: 'shot.png' }, { media_type: 'image/webp' }, { size: 4 }]) {
    assert.notEqual(replyKey(first, send([{ ...file, ...changed }]), mint).key, first.key, JSON.stringify(changed));
  }
  const two = [file, { ...file, sha256: other }];
  assert.notEqual(replySig(two), replySig([two[1], two[0]]), 'order is part of the signature');
  assert.notEqual(replySig([file]), replySig([]));
  assert.equal(replySig([]), replySig([]));
});

// A CLI send with the operator credential carries the hub-set
// metadata["vecnet.client"] (PROTOCOL §2.2 step 11); the header names the
// terminal beside the sender. Peers cannot write vecnet.* keys, so only the
// hub's own value shows, flattened to one capped line.
test('clientLabel shows only the hub operator-cli label, as one safe line', () => {
  const at = (client) => msg('m', OP, 'hi', { metadata: { 'vecnet.client': client } });
  assert.equal(clientLabel(at({ kind: 'operator-cli', host: 'xsm', ssh: false, label: 'Operator on xsm CLI' })), 'Operator on xsm CLI');
  assert.equal(clientLabel(at({ kind: 'operator-cli', host: 'x', ssh: true, agent_suspected: true, label: 'Operator on x CLI via ssh (agent suspected)' })),
    'Operator on x CLI via ssh (agent suspected)');
  assert.equal(clientLabel(at({ kind: 'operator-cli', label: 'Operator on \u001b[31mevil\n‮host   CLI' })), 'Operator on [31mevil host CLI');
  assert.equal(clientLabel(at({ kind: 'operator-cli', label: 'x'.repeat(500) })).length, CLIENT_LABEL_CAP);
  assert.equal(clientLabel(at({ kind: 'web', label: 'not a CLI' })), '');
  assert.equal(clientLabel(at({ kind: 'operator-cli', label: 7 })), '');
  assert.equal(clientLabel(at({ kind: 'operator-cli', label: '  ' })), '');
  assert.equal(clientLabel(at('Operator on xsm CLI')), '');
  assert.equal(clientLabel(msg('m', OP, 'hi')), '');
  assert.equal(clientLabel(msg('m', OP, 'hi', { metadata: { 'client': { kind: 'operator-cli', label: 'spoof' } } })), '');
  assert.equal(clientLabel(null), '');
  assert.equal(clientSuspect(at({ kind: 'operator-cli', label: 'L', agent_suspected: true })), true);
  assert.equal(clientSuspect(at({ kind: 'operator-cli', label: 'L', agent_suspected: 'yes' })), false);
  assert.equal(clientSuspect(at({ kind: 'operator-cli', label: 'L' })), false);
  assert.equal(clientSuspect(at({ kind: 'web', label: 'L', agent_suspected: true })), false);
  assert.equal(clientSuspect(msg('m', OP, 'hi')), false);
});

// Decisions §6, for a thread that already exists (including a setup thread
// whose participants are the operator and a grokbot). Kinds come from the
// directory; until it names a participant, the reply keeps the initial
// priority. PROTOCOL §§2.2, 5.2, 16.3.
test('Grok recipient defaults preserve user choice', () => {
  const kinds = { peer_op: 'operator', peer_g: 'grokbot', peer_c: 'session', peer_d: 'dot', peer_s: 'pagchat-bot' };
  const kindOf = id => kinds[id] || '';
  const setup = ['peer_op', 'peer_g'];
  const base = { manual: false, locked: false, current: 'wake', kindOf };
  assert.equal(replyPriority({ ...base, participants: setup }), 'wake_idle', 'setup thread');
  assert.equal(replyPriority({ ...base, participants: ['peer_op', 'peer_c', 'peer_g', 'peer_d'] }), 'wake_idle', 'mixed thread, one grokbot');
  assert.equal(replyPriority({ ...base, participants: ['peer_op', 'peer_c'] }), 'wake', 'no grokbot');
  assert.equal(replyPriority({ ...base, participants: ['peer_op', 'peer_s'] }), 'wake', 'pagchat-bot is not a grokbot');
  assert.equal(replyPriority({ ...base, participants: [] }), 'wake', 'thread not loaded');
  assert.equal(replyPriority({ ...base, participants: null }), 'wake');
  // Delayed directory: participant ids are known, kinds are not.
  assert.equal(replyPriority({ ...base, participants: setup, kindOf: () => '' }), 'wake');
  assert.equal(replyPriority({ ...base, participants: setup, kindOf: id => id === 'peer_g' ? 'grokbot' : '' }), 'wake_idle', 'directory arrives');
  assert.equal(replyPriority({ manual: true, locked: false, current: 'wake', participants: setup, kindOf }), 'wake', 'explicit wake');
  assert.equal(replyPriority({ manual: true, locked: false, current: 'hold', participants: setup, kindOf }), 'hold', 'explicit hold');
  assert.equal(replyPriority({ manual: false, locked: true, current: 'wake', participants: setup, kindOf }), 'wake', 'in-flight reply');

  let n = 0;
  const mint = () => 'ui-r' + (++n);
  const prio = replyPriority({ manual: false, locked: true, current: 'hold', participants: setup, kindOf });
  const first = replyKey(null, { text: 'same body', prio, replyTo: 'msg_1', sig: '' }, mint);
  const retry = replyKey(first, { text: 'same body', prio, replyTo: 'msg_1', sig: '' }, mint);
  assert.equal(retry, first, 'idempotent retry minted a new key');
  assert.equal(retry.prio, 'hold');
  assert.equal(retry.text, 'same body');

  const auto = replyPriority({ ...base, participants: setup });
  const note = replyQuotaNote(setup, kindOf, { manual: false, priority: auto });
  assert.equal(note, GROK_QUOTA_NOTE);
  assert.match(note, /^Preselected/);
  assert.doesNotMatch(note, /free/i);
  assert.doesNotMatch(note, /guarantee|no wake|never wake|won'?t wake|will not wake|without waking/i);
  assert.equal(replyQuotaNote(['peer_op', 'peer_c'], kindOf, { manual: false, priority: 'wake' }), '');
  assert.equal(replyQuotaNote(setup, () => '', { manual: false, priority: 'wake' }), '');

  // After an explicit wake or hold the note no longer says "preselected".
  for (const priority of ['wake', 'hold', 'wake_idle']) {
    const chosen = replyPriority({ manual: true, locked: false, current: priority, participants: setup, kindOf });
    const line = replyQuotaNote(setup, kindOf, { manual: true, priority: chosen });
    assert.equal(line, GROK_QUOTA_PLAIN, 'explicit ' + priority);
    assert.doesNotMatch(line, /preselected/i, 'explicit ' + priority);
  }
  // A failed wake reply kept for its retry after the grokbot became known.
  const kept = replyPriority({ manual: false, locked: true, current: 'wake', participants: setup, kindOf });
  assert.equal(replyQuotaNote(setup, kindOf, { manual: false, priority: kept }), GROK_QUOTA_PLAIN, 'kept wake retry');
});

// A lost reply keeps wake and its key while the text is unchanged, even
// after a grokbot participant becomes known. Editing that draft is a new
// send: wake_idle is chosen before the new key is minted. Manual hold stays.
test('failure then recipient edit recomputes automatic priority', () => {
  const kinds = { peer_op: 'operator', peer_g: 'grokbot', peer_c: 'session' };
  const kindOf = id => kinds[id] || '';
  const setup = ['peer_op', 'peer_g'];
  let n = 0;
  const mint = () => 'ui-e' + (++n);
  const failed = { text: 'hello', prio: 'wake', replyTo: 'msg_1', sig: '' };
  const pending = replyKey(null, failed, mint);
  const same = { ...failed };
  assert.equal(replyPriorityLocked({ sending: false, pending, draft: same }), true);
  assert.equal(pendingReplyMatches(pending, same), true);
  assert.equal(replyPriority({ manual: false, locked: replyPriorityLocked({ sending: false, pending, draft: same }), current: 'wake', participants: setup, kindOf }), 'wake');
  assert.equal(replyKey(pending, same, mint), pending, 'unchanged retry minted a new key');

  const edited = { ...failed, text: 'hello!' };
  assert.equal(pendingReplyMatches(pending, edited), false);
  assert.equal(replyPriorityLocked({ sending: false, pending, draft: edited }), false);
  const priority = replyPriority({ manual: false, locked: replyPriorityLocked({ sending: false, pending, draft: edited }), current: 'wake', participants: setup, kindOf });
  assert.equal(priority, 'wake_idle');
  const minted = replyKey(pending, { ...edited, prio: priority }, mint);
  assert.notEqual(minted.key, pending.key, 'an edited draft reused the failed reply\'s key');
  assert.equal(minted.prio, 'wake_idle');
  assert.equal(minted.text, 'hello!');

  // Undoing the edit restores the captured priority, even if the control moved.
  const restored = { ...failed, prio: 'wake_idle' };
  assert.equal(pendingReplyMatches(pending, restored), true);
  assert.equal(replyPriorityLocked({ sending: false, pending, draft: restored }), true);
  assert.equal(replyKey(pending, { ...restored, prio: pending.prio }, mint), pending, 'restoring the text minted a new key');

  const sessionOnly = replyPriority({ manual: false, locked: replyPriorityLocked({ sending: false, pending, draft: edited }), current: 'wake', participants: ['peer_op', 'peer_c'], kindOf });
  assert.equal(sessionOnly, 'wake', 'a non-grokbot thread adopted wake_idle');
  assert.equal(replyPriority({ manual: true, locked: replyPriorityLocked({ sending: false, pending, draft: edited }), current: 'hold', participants: setup, kindOf }), 'hold', 'manual hold was overwritten');
  assert.equal(replyPriority({ manual: false, locked: replyPriorityLocked({ sending: true, pending, draft: edited }), current: 'wake', participants: setup, kindOf }), 'wake', 'in-flight reply changed priority');
});
