// Renders md-model blocks as DOM nodes through h(), which writes text with
// textContent only (PROTOCOL §14.2). Links open in a new tab with no opener
// or referrer, carry their full target as a title, and name the real host
// when the label does not (needsHost).
import { h } from './dom.js';
import { needsHost, parse } from './md-model.js';

// The thread rebuilds every message on each update, so parsed blocks (plain
// data, never mutated here) are kept per body text, oldest dropped first.
const memo = new Map(), MEMO = 1000;
function blocksOf(text) {
  const key = String(text == null ? '' : text);
  let b = memo.get(key);
  if (!b) {
    b = parse(key);
    memo.set(key, b);
    if (memo.size > MEMO) memo.delete(memo.keys().next().value);
  }
  return b;
}

// markdown returns a div.md holding the rendered body.
export function markdown(text) {
  return h('div', { class: 'md' }, blocksOf(text).map(block));
}

function block(b) {
  switch (b.t) {
    case 'p': return h('p', {}, b.kids.flatMap(span));
    case 'h': return h('h' + (b.level + 2), { class: 'md-h' }, b.kids.flatMap(span));
    case 'code': return h('pre', { class: 'md-pre' }, h('code', { text: b.v }));
    case 'hr': return h('hr', {});
    case 'quote': return h('blockquote', {}, b.kids.map(block));
    case 'ul': return h('ul', {}, b.items.map(kids => h('li', {}, kids.flatMap(span))));
    case 'ol': return h('ol', { start: b.start !== 1 ? String(b.start) : null }, b.items.map(kids => h('li', {}, kids.flatMap(span))));
    case 'rest': return h('div', { class: 'md-rest', text: b.v });
    default: return null;
  }
}

function span(k) {
  switch (k.t) {
    case 'text': return k.v;
    case 'br': return h('br', {});
    case 'code': return h('code', { text: k.v });
    case 'b': return h('strong', {}, k.kids.flatMap(span));
    case 'i': return h('em', {}, k.kids.flatMap(span));
    case 'a': {
      const a = h('a', { href: k.href, target: '_blank', rel: 'noopener noreferrer nofollow', title: k.href, class: k.image ? 'md-img' : null }, k.kids.flatMap(span));
      return needsHost(k) ? [a, h('span', { class: 'md-host', text: ' (' + k.host + ')' })] : a;
    }
    default: return null;
  }
}
