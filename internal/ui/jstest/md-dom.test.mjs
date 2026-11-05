import test from 'node:test';
import assert from 'node:assert/strict';

// A minimal document: enough for h() to build nodes, strict enough to catch a
// child that is not a node or a string (a real append() stringifies an array,
// which is how a link once rendered as its bare href).
class El {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener() {}
  append(...xs) {
    for (const x of xs) {
      if (typeof x !== 'string' && !(x instanceof El)) throw new Error('append got ' + Object.prototype.toString.call(x));
      this.kids.push(x);
    }
  }
}
globalThis.document = { createElement: tag => new El(tag) };
const { markdown } = await import('../static/md.js');

const find = (el, pred, out = []) => {
  if (el instanceof El) { if (pred(el)) out.push(el); el.kids.forEach(k => find(k, pred, out)); }
  return out;
};

test('a link whose label hides its host is followed by the real host, in every context', () => {
  for (const src of ['see [github.com](https://evil.example/x) now', '# [docs](https://evil.example/x)', '- **[docs](https://evil.example/x)**', '1. *[docs](https://evil.example/x)*']) {
    const root = markdown(src);
    const [a] = find(root, e => e.tag === 'a');
    assert.ok(a, src);
    assert.equal(a.attrs.href, 'https://evil.example/x');
    assert.equal(a.attrs.rel, 'noopener noreferrer nofollow');
    assert.equal(a.attrs.target, '_blank');
    const parent = find(root, e => e.kids.includes(a))[0];
    const next = parent.kids[parent.kids.indexOf(a) + 1];
    assert.ok(next instanceof El && next.className === 'md-host' && next.textContent === ' (evil.example)', src);
  }
});

test('hostile markup stays text and the root is div.md', () => {
  const root = markdown('<script>alert(1)</script> <img src=x onerror=alert(2)>');
  assert.equal(root.tag, 'div');
  assert.equal(root.className, 'md');
  assert.equal(find(root, e => ['script', 'img', 'a'].includes(e.tag)).length, 0);
  assert.equal(root.textContent, '<script>alert(1)</script> <img src=x onerror=alert(2)>');
});

test('a repeated body renders the same nodes from the memoised parse', () => {
  const src = '**a** [docs](https://evil.example/x)\n- one';
  const one = markdown(src), two = markdown(src);
  assert.notEqual(one, two);
  assert.equal(two.textContent, one.textContent);
  assert.equal(find(two, e => e.tag === 'a').length, 1);
});

test('a malformed mailto in one message does not stop its neighbours rendering', () => {
  // thread.js renders every message body with markdown() before replacing the
  // list, so one throwing body would blank the whole thread.
  const bodies = ['first [ok](mailto:max@example.com)', 'bad [x](mailto:%zz@example.com) and <mailto:%C3%28@example.com>', 'last **one**'];
  let roots;
  assert.doesNotThrow(() => { roots = bodies.map(b => markdown(b)); });
  assert.equal(roots.length, 3);
  assert.ok(roots[1].textContent.includes('[x](mailto:%zz@example.com)'), 'malformed link reads as typed');
  assert.equal(find(roots[1], e => e.tag === 'a').length, 0);
  assert.ok(roots[2].textContent.includes('last one'));
  assert.equal(find(roots[0], e => e.tag === 'a')[0].attrs.href, 'mailto:max@example.com');
});
