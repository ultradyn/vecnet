// The side panel beside a thread. ctx.threadId names the thread (see app.js).
import { h } from './dom.js';

const RECEIPT_STATES = ['queued', 'accepted', 'read', 'processed', 'failed', 'dead_letter', 'expired'];

function dl(rows) {
  return h('dl', { class: 'kv' }, rows.flatMap(([k, v]) => [h('dt', { text: k }), h('dd', { text: v })]));
}

export function mountAside(root, ctx) {
  const render = () => {
    const t = ((ctx.threads.get() || {}).threads || []).find(x => x.thread_id === ctx.threadId);
    const peers = (ctx.peers.get() || {}).peers || [];
    const kids = [];
    if (t) {
      kids.push(h('h3', { text: 'Participants' }));
      for (const p of t.participants || []) {
        const e = peers.find(x => x.peer_id === p);
        kids.push(h('div', { class: 'pp' },
          h('span', { class: 'dot ' + (e ? e.presence : ''), title: e ? e.presence : 'unknown' }),
          h('span', { text: ctx.nameOf(p) + (p === ctx.me ? ' (you)' : '') }),
          h('span', { class: 'k', text: ctx.kindOf(p) })));
      }
      kids.push(h('h3', { text: 'Thread' }), dl([
        ['id', t.thread_id], ['group', t.group || '—'], ['messages', String(t.message_count)],
        ['budget', t.budget_remaining === null || t.budget_remaining === undefined ? 'unlimited' : t.budget_remaining + ' left'],
        ['history', t.history_access || 'from_join'], ['locked', t.locked_participants ? 'yes' : 'no'],
      ]));
    }
    kids.push(h('h3', { text: 'Receipts' }), ...RECEIPT_STATES.map(s => h('div', { class: 'lg st-' + s }, h('i'), s)));
    root.replaceChildren(...kids);
  };
  render();
  const off = [ctx.threads.subscribe(render), ctx.peers.subscribe(render)];
  return () => off.forEach(fn => fn());
}
