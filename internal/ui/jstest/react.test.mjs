import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { APIError } from '../static/api.js';
import { REACTIONS, chips, defaultPriority, errorText, nextOp, reactBody } from '../static/react-model.js';

const OP = 'peer_op', CODEX = 'peer_codex', CLAW = 'peer_claw', HOSTILE = 'peer_hostile';
const NAMES = { [OP]: 'max', [CODEX]: 'codex-hark', [CLAW]: 'claw-ops', [HOSTILE]: 'evil\u202e\u0007name\u001b[2J\u2066x' };
const nameOf = id => NAMES[id] || id;
const CHECK = '\u2705', PLUS = '\ud83d\udc4d', EYES = '\ud83d\udc40', Q = '\u2753', HEART = '\u2764\ufe0f';

test('REACTIONS matches internal/envelope/testdata/reactions.json in order, names and default priority', () => {
  const fixture = JSON.parse(readFileSync(new URL('../../envelope/testdata/reactions.json', import.meta.url), 'utf8'));
  assert.equal(fixture.length, 8);
  assert.deepEqual(REACTIONS.map(r => [r.emoji, r.name, r.prio]), fixture.map(r => [r.emoji, r.name, r.priority]));
  for (const r of fixture) assert.equal(defaultPriority(r.emoji), r.priority, r.name);
  assert.ok(Object.isFrozen(REACTIONS), 'the table is frozen');
});

test('chips: counts, mine for the operator peer, titles cleaned (bidi override and controls stripped), own name shown as you', () => {
  // The hub's order is the fixed order, but chips sort by the table anyway.
  const reactions = [
    { emoji: CHECK, peers: [CODEX, OP, CLAW] },
    { emoji: PLUS, peers: [HOSTILE] },
    { emoji: EYES, peers: [] },
    { emoji: Q, peers: [OP] },
  ];
  const out = chips(reactions, OP, nameOf);
  assert.deepEqual(out.map(c => c.emoji), [PLUS, CHECK, Q], 'fixed order; an empty group shows no chip');
  assert.deepEqual(out.map(c => c.count), [1, 3, 1]);
  assert.deepEqual(out.map(c => c.mine), [false, true, true]);
  assert.equal(out[1].title, 'codex-hark, you, claw-ops');
  assert.equal(out[2].title, 'you');
  assert.equal(out[0].title, 'evilname[2Jx', 'bidi override, isolate and C0 controls stripped');
  assert.doesNotMatch(out[0].title, /[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/);
  assert.deepEqual(chips(undefined, OP, nameOf), []);
  assert.deepEqual(chips([], OP, nameOf), []);
  // A bare U+2764 is the heart; anything outside the set is not drawn.
  assert.deepEqual(chips([{ emoji: '\u2764', peers: [CODEX] }, { emoji: '\ud83d\ude80', peers: [CODEX] }], OP, nameOf).map(c => c.emoji), [HEART]);
});

test('nextOp: remove when mine, else add', () => {
  const reactions = [{ emoji: CHECK, peers: [CODEX, OP] }, { emoji: PLUS, peers: [CODEX] }];
  assert.equal(nextOp(reactions, OP, CHECK), 'remove');
  assert.equal(nextOp(reactions, OP, PLUS), 'add');
  assert.equal(nextOp(reactions, OP, Q), 'add');
  assert.equal(nextOp(undefined, OP, CHECK), 'add');
});

test('reactBody: check → wake_idle, +1 → hold, override wins, remove carries no priority', () => {
  assert.deepEqual(reactBody({ messageId: 'msg_1', emoji: CHECK, op: 'add' }), { message_id: 'msg_1', emoji: CHECK, op: 'add', priority: 'wake_idle' });
  assert.deepEqual(reactBody({ messageId: 'msg_1', emoji: PLUS, op: 'add' }), { message_id: 'msg_1', emoji: PLUS, op: 'add', priority: 'hold' });
  assert.deepEqual(reactBody({ messageId: 'msg_1', emoji: Q, op: 'add', override: 'hold' }), { message_id: 'msg_1', emoji: Q, op: 'add', priority: 'hold' });
  assert.deepEqual(reactBody({ messageId: 'msg_1', emoji: PLUS, op: 'add', override: 'wake' }), { message_id: 'msg_1', emoji: PLUS, op: 'add', priority: 'wake' });
  const rm = reactBody({ messageId: 'msg_1', emoji: CHECK, op: 'remove', override: 'wake' });
  assert.deepEqual(rm, { message_id: 'msg_1', emoji: CHECK, op: 'remove' });
  assert.ok(!('priority' in rm));
});

test('errorText: rate_limited shows en-AU time from retry_after_seconds; not_replyable and emoji_not_allowed have fixed text', () => {
  const now = Date.parse('2026-11-03T10:00:00.000Z');
  const at = new Date(now + 90_000).toLocaleTimeString('en-AU', { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
  assert.match(at, /^\d\d:\d\d:\d\d$/);
  const rate = errorText(new APIError(429, 'rate_limited', 'slow down', { retry_after_seconds: 90 }), now);
  assert.ok(rate.includes(at), `${rate} names ${at}`);
  assert.doesNotMatch(rate, /slow down/);
  assert.match(errorText(new APIError(429, 'rate_limited', 'slow down', {}), now), /^Too many/, 'no retry time still has its own text');
  assert.equal(errorText(new APIError(422, 'not_replyable', 'hub words'), now), "Reactions can't be reacted to.");
  const emoji = errorText(new APIError(422, 'emoji_not_allowed', 'hub words'), now);
  assert.ok(emoji && !emoji.includes('hub words'));
  const net = errorText(new APIError(0, 'network', 'hub unreachable'), now);
  assert.ok(net && net !== 'hub unreachable');
  assert.equal(errorText(new APIError(0, 'aborted', 'request aborted'), now), net);
  assert.equal(errorText(new APIError(404, 'not_found', 'message not found'), now), 'message not found');
});
