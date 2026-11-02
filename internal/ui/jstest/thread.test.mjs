import test from 'node:test';
import assert from 'node:assert/strict';
import { canShowFull, FULL_TEXT_CAP, items, sendError, spillOf } from '../static/thread-model.js';

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
  const budget = sendError(err(422, 'thread_budget_exhausted', 'thread budget is exhausted'), { budget_remaining: 0 });
  assert.equal(budget.kind, 'budget');
  assert.match(budget.text, /budget/i);
  assert.match(budget.text, /0 messages left/);
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
