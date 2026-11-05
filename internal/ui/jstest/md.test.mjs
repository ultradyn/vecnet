import test from 'node:test';
import assert from 'node:assert/strict';
import { MD_LIMIT, inline, needsHost, parse, safeHref } from '../static/md-model.js';

const only = text => { const b = parse(text); assert.equal(b.length, 1, JSON.stringify(b)); return b[0]; };
const kinds = kids => kids.map(k => k.t).join(',');

test('paragraph lines become hard breaks; blank lines split paragraphs', () => {
  const b = parse('one\ntwo\n\nthree');
  assert.deepEqual(b.map(x => x.t), ['p', 'p']);
  assert.equal(kinds(b[0].kids), 'text,br,text');
});

test('headings, rules, fences, lists and quotes', () => {
  assert.equal(only('## Title ##').level, 2);
  assert.equal(only('#### four').t, 'p', 'only # to ### are headings');
  assert.equal(only('---').t, 'hr');
  const code = only('```go\nfunc *x* [a](https://e.com)\n```');
  assert.deepEqual(code, { t: 'code', v: 'func *x* [a](https://e.com)' });
  assert.deepEqual(only('```\nno close').v, 'no close');
  assert.equal(only('~~~~\na\n~~~\nb\n~~~~').v, 'a\n~~~\nb', 'a shorter fence does not close');
  const ul = only('- a\n- **b**\n  more');
  assert.equal(ul.t, 'ul');
  assert.equal(ul.items.length, 2);
  assert.equal(kinds(ul.items[1]), 'b,br,text');
  const ol = only('3. x\n4. y');
  assert.deepEqual([ol.t, ol.start, ol.items.length], ['ol', 3, 2]);
  const q = only('> quoted\n> # not nested heading\n> > deeper');
  assert.equal(q.t, 'quote');
  assert.ok(q.kids.every(k => k.t !== 'quote'), 'quotes do not nest');
});

test('inline code, emphasis, escapes, snake_case', () => {
  assert.deepEqual(inline('a `x*y*` b'), [{ t: 'text', v: 'a ' }, { t: 'code', v: 'x*y*' }, { t: 'text', v: ' b' }]);
  assert.deepEqual(inline('``a`b``'), [{ t: 'code', v: 'a`b' }]);
  assert.equal(kinds(inline('**bold** and *it* and _it_')), 'b,text,i,text,i');
  assert.deepEqual(inline('\\*not\\*'), [{ t: 'text', v: '*not*' }]);
  assert.deepEqual(inline('snake_case_name'), [{ t: 'text', v: 'snake_case_name' }]);
  assert.deepEqual(inline('2 * 3 * 4'), [{ t: 'text', v: '2 * 3 * 4' }]);
  assert.deepEqual(inline('**unclosed'), [{ t: 'text', v: '**unclosed' }]);
});

test('links keep only http, https and mailto', () => {
  const [a] = inline('[docs](https://example.com/d)');
  assert.deepEqual([a.t, a.href, a.host], ['a', 'https://example.com/d', 'example.com']);
  for (const bad of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,<b>x', 'vbscript:x', '/relative', '//proto.rel', 'file:///etc/passwd']) {
    const out = inline('[x](' + bad + ')');
    assert.ok(out.every(k => k.t !== 'a'), bad);
    assert.equal(out.map(k => k.v).join(''), '[x](' + bad + ')', 'refused links read as typed: ' + bad);
  }
  assert.equal(inline('[m](mailto:max@example.com)')[0].host, 'max@example.com');
  assert.equal(safeHref('mailto:nobody'), null);
  const [img] = inline('![shot](https://e.com/a.png)');
  assert.deepEqual([img.t, img.image], ['a', true], 'images render as links, never fetched');
});

test('bare URLs autolink and drop trailing punctuation', () => {
  const out = inline('see https://xsm.hornbill-boa.ts.net:29044, then (https://e.com/a_(b)) ok.');
  const links = out.filter(k => k.t === 'a');
  assert.deepEqual(links.map(l => l.href), ['https://xsm.hornbill-boa.ts.net:29044/', 'https://e.com/a_(b)']);
  assert.equal(links[0].kids[0].v, 'https://xsm.hornbill-boa.ts.net:29044');
  assert.ok(out.some(k => k.t === 'text' && k.v.startsWith(', then (')));
  assert.equal(inline('nothttps://e.com').filter(k => k.t === 'a').length, 0);
});

test('raw HTML stays text', () => {
  const out = inline('<script>alert(1)</script><img src=x onerror=alert(1)>');
  assert.deepEqual(out, [{ t: 'text', v: '<script>alert(1)</script><img src=x onerror=alert(1)>' }]);
});

test('a label that does not name the real host gets the host shown', () => {
  const link = s => inline(s)[0];
  assert.equal(needsHost(link('[docs](https://evil.example/x)')), true);
  assert.equal(needsHost(link('[github.com](https://evil.example/)')), true);
  assert.equal(needsHost(link('[https://github.com/x](https://evil.example/)')), true);
  assert.equal(needsHost(link('[https://good.com/?//evil.example](https://evil.example/)')), true);
  assert.equal(needsHost(link('[example.com](https://example.com/)')), false);
  assert.equal(needsHost(link('[www.example.com/a](https://example.com/b)')), false);
  assert.equal(needsHost(link('[https://example.com/a](https://example.com/b)')), false);
  assert.equal(needsHost(link('[max@example.com](mailto:max@example.com)')), false);
  assert.equal(needsHost(inline('https://e.com')[0]), false, 'a bare URL is its own label');
  // A lookalike host is shown in punycode.
  assert.match(link('[x](https://gіthub.com/)').host, /^xn--/);
});

test('past MD_LIMIT the rest is plain text', () => {
  const big = ('**b** line\n').repeat(Math.ceil((MD_LIMIT + 5000) / 11));
  const b = parse(big);
  assert.equal(b[b.length - 1].t, 'rest');
  assert.ok(b[b.length - 1].v.startsWith('\n'));
});

test('pathological input stays fast', () => {
  for (const s of ['<'.repeat(1 << 20), '<a '.repeat(1 << 18) + '>', '*'.repeat(1 << 20), '[' .repeat(1 << 20), '*a '.repeat(1 << 18), '[a](' .repeat(1 << 18), '`'.repeat(1 << 20), 'http'.repeat(1 << 18), '_a'.repeat(1 << 19)]) {
    const t0 = performance.now();
    parse(s);
    const ms = performance.now() - t0;
    assert.ok(ms < 200, `${JSON.stringify(s.slice(0, 6))}… took ${ms.toFixed(0)} ms`);
  }
});

test('angle-bracket autolinks', () => {
  const out = inline('test: <https://xsm.hornbill-boa.ts.net:29044> and <mailto:a@b.co> but <b>bold</b> and <javascript:x>');
  const links = out.filter(k => k.t === 'a');
  assert.deepEqual(links.map(l => l.href), ['https://xsm.hornbill-boa.ts.net:29044/', 'mailto:a@b.co']);
  assert.equal(links[0].kids[0].v, 'https://xsm.hornbill-boa.ts.net:29044');
  assert.ok(out.some(k => k.t === 'text' && k.v.includes('<b>bold</b> and <javascript:x>')));
});

test('malformed percent escapes in mailto targets stay literal text, never throw', () => {
  // Invalid escapes and invalid UTF-8 make decodeURIComponent throw URIError.
  for (const target of ['mailto:%zz@example.com', 'mailto:%C3%28@example.com', 'mailto:%E0%A4%A@example.com', 'mailto:max@%ZZexample.com']) {
    assert.equal(safeHref(target), null, target);
    for (const src of ['[x](' + target + ')', '<' + target + '>', '[' + target + '](https://example.com/)', '![x](' + target + ')']) {
      let out;
      assert.doesNotThrow(() => { out = inline(src); }, src);
      if (!src.startsWith('[' + target + ']')) assert.ok(out.every(k => k.t !== 'a'), src);
      assert.doesNotThrow(() => parse('before\n\n' + src + '\n\nafter'), src);
    }
  }
  // A label that is a malformed mailto still links to its real target, and shows the host.
  const [a] = inline('[mailto:%zz@example.com](https://example.com/)');
  assert.deepEqual([a.t, a.href], ['a', 'https://example.com/']);
  assert.equal(needsHost(a), true);
  assert.equal(safeHref('mailto:%41x@example.com').host, 'Ax@example.com', 'valid escapes still decode');
});
