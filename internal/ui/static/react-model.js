// Reactions (PROTOCOL §17): the fixed set and its client defaults (§17.6),
// chips and request bodies. Pure; checked against
// internal/envelope/testdata/reactions.json.
import { clean } from './format.js';

const row = (emoji, name, prio) => Object.freeze({ emoji, name, prio });
export const REACTIONS = Object.freeze([
  row('👍', '+1', 'hold'), row('👎', '-1', 'hold'), row('❤️', 'heart', 'hold'),
  row('😂', 'laugh', 'hold'), row('👀', 'eyes', 'hold'),
  row('✅', 'check', 'wake_idle'), row('❓', 'q', 'wake_idle'), row('❗', 'bang', 'wake_idle'),
]);

// A bare U+2764 is the heart (§17.1).
const norm = e => (e === '❤' ? '❤️' : e);
const peersOf = (reactions, emoji) => ((reactions || []).find(g => norm(g.emoji) === emoji) || {}).peers || [];

export function defaultPriority(emoji) {
  const r = REACTIONS.find(x => x.emoji === norm(emoji));
  return r ? r.prio : 'hold';
}

// chips are the message's reactions in the fixed order; emoji outside the set
// are not drawn. title names the reactors, the operator's own peer as you.
export function chips(reactions, me, nameOf) {
  const out = [];
  for (const { emoji } of REACTIONS) {
    const peers = peersOf(reactions, emoji);
    if (!peers.length) continue;
    out.push({ emoji, count: peers.length, mine: peers.includes(me), title: peers.map(p => (p === me ? 'you' : clean(nameOf(p)))).join(', ') });
  }
  return out;
}

export function nextOp(reactions, me, emoji) {
  return peersOf(reactions, norm(emoji)).includes(me) ? 'remove' : 'add';
}

// reactBody: an add carries the override, else the emoji's default; a remove
// never wakes, so it carries no priority.
export function reactBody({ messageId, emoji, op, override }) {
  const body = { message_id: messageId, emoji, op };
  if (op === 'add') body.priority = override || defaultPriority(emoji);
  return body;
}

const AT = { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' };
export function errorText(err, nowMs = Date.now()) {
  const code = err && err.code, d = (err && err.details) || {};
  if (code === 'not_replyable') return "Reactions can't be reacted to.";
  if (code === 'rate_limited') {
    const s = d.retry_after_seconds;
    return 'Too many reactions and messages. ' + (typeof s === 'number' && s >= 0
      ? 'Try again at ' + new Date(nowMs + s * 1000).toLocaleTimeString('en-AU', AT) + '.'
      : 'Try again shortly.');
  }
  if (code === 'emoji_not_allowed') return 'That emoji is not one of the eight reactions.';
  if (!err || !err.status) return 'Could not reach the hub. Try again.';
  return err.message || code || 'error';
}
