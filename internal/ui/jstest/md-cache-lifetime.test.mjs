import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { MD_LIMIT } from '../static/md-model.js';

const BUDGET = 256 * 1024;
const source = await readFile(new URL('../static/md.js', import.meta.url), 'utf8');
const domURL = new URL('../static/dom.js', import.meta.url).href;
const modelURL = new URL('../static/md-model.js', import.meta.url).href;
const dataURL = code => 'data:text/javascript;base64,' + Buffer.from(code).toString('base64');
let serial = 0;

// Same strict document surface as md-dom.test.mjs; h(), parse(), and markdown()
// are production code. Keep no rendered inventory during eviction workloads.
class El {
  constructor(tag) { this.tag = tag; this.attrs = {}; this.kids = []; this.className = ''; }
  set textContent(v) { this.kids = [String(v)]; }
  get textContent() { return this.kids.map(k => typeof k === 'string' ? k : k.textContent).join(''); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener() {}
  append(...xs) {
    for (const x of xs) {
      assert.ok(typeof x === 'string' || x instanceof El, 'append expects a node or string');
      this.kids.push(x);
    }
  }
}
const find = (el, pred) => el instanceof El
  ? [...(pred(el) ? [el] : []), ...el.kids.flatMap(k => find(k, pred))] : [];
const body = (label, size) => label + 'x'.repeat(size - label.length);

async function isolated(run) {
  const calls = new Map();
  const hook = '__webMem4Parse_' + serial++;
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
  assert.equal(Object.hasOwn(globalThis, hook), false);
  globalThis[hook] = key => calls.set(key, (calls.get(key) || 0) + 1);
  try {
    // Capture the counter at import time, then delegate every miss to REAL parse.
    // The unique wrapper URL also gives each case a fresh md.js module/cache.
    const wrapper = dataURL(`
      import { parse as realParse, needsHost } from ${JSON.stringify(modelURL)};
      const observe = globalThis[${JSON.stringify(hook)}];
      export { needsHost };
      export function parse(text) { observe(text); return realParse(text); }
    `);
    let domRewrites = 0, modelRewrites = 0;
    const instrumented = source
      .replace(/(?<=\bfrom\s*)(['"])\.\/dom\.js\1/g, () => {
        domRewrites++; return JSON.stringify(domURL);
      })
      .replace(/(?<=\bfrom\s*)(['"])\.\/md-model\.js\1/g, () => {
        modelRewrites++; return JSON.stringify(wrapper);
      });
    assert.equal(domRewrites, 1, 'rewrite exactly one DOM import specifier');
    assert.equal(modelRewrites, 1, 'rewrite exactly one model import specifier');
    const { markdown } = await import(dataURL(instrumented));
    globalThis.document = { createElement: tag => new El(tag) };
    await run({ markdown, count: key => calls.get(key) || 0 });
  } finally {
    delete globalThis[hook];
    if (previousDocument) Object.defineProperty(globalThis, 'document', previousDocument);
    else delete globalThis.document;
  }
}
const regression = (name, run) => test('WEB-MEM-4 ' + name, { concurrency: false }, () => isolated(run));

regression('exact budget is reusable; one extra unit evicts the oldest key', ({ markdown, count }) => {
  const exact = body('boundary:', BUDGET);
  assert.equal(exact.length, BUDGET);
  assert.equal(markdown(exact).textContent, exact);
  assert.equal(markdown(exact).textContent, exact);
  assert.equal(count(exact), 1, 'an exactly fitting key is admitted');
  markdown('!');
  markdown('!');
  assert.equal(count('!'), 1);
  assert.equal(markdown(exact).textContent, exact);
  assert.equal(count(exact), 2, 'one unit beyond the aggregate budget evicts the oldest');
});

regression('medium bodies evict by aggregate text below 1000 entries, without hit promotion', ({ markdown, count }) => {
  const keys = Array.from({ length: 5 }, (_, i) => body('medium' + i + ':', BUDGET / 4));
  for (const key of keys.slice(0, 4)) assert.equal(markdown(key).textContent, key);
  markdown(keys[0]);
  assert.equal(count(keys[0]), 1, 'a hit at the exact aggregate boundary is reusable');
  markdown(keys[4]);
  for (const key of keys.slice(1)) {
    assert.equal(markdown(key).textContent, key);
    assert.equal(count(key), 1, 'newer entries survive the FIFO eviction');
  }
  markdown(keys[0]);
  assert.equal(count(keys[0]), 2, 'hits must not promote an old entry to LRU');
});

regression('one admission can evict multiple old keys and reclaim their charges', ({ markdown, count }) => {
  const old = Array.from({ length: 4 }, (_, i) => body('old' + i + ':', BUDGET / 8));
  for (const key of old) markdown(key);
  const large = body('large:', 3 * BUDGET / 4);
  markdown(large);
  for (const key of [old[2], old[3], large]) {
    markdown(key);
    assert.equal(count(key), 1, 'exactly fitting survivors remain cached');
  }
  // Probe the second required victim first: re-admitting old[0] could itself
  // evict old[1], hiding a single eviction or twice-subtracted key length.
  for (const key of [old[1], old[0]]) {
    markdown(key);
    assert.equal(count(key), 2, 'both oldest entries must have been evicted');
  }
});

regression('oversized normalized input renders full markdown and plain rest but never enters the cache', ({ markdown, count }) => {
  const head = '# Heading\n\n**bold** [docs](https://evil.example/x) ![shot](https://e.com/a.png)\n\n';
  const fill = 'q'.repeat(MD_LIMIT - head.length);
  const tail = body('**literal** <script>alert(1)</script> END:', BUDGET + 1 - MD_LIMIT);
  const raw = head + fill + tail;
  const normalized = { toString: () => raw };
  assert.equal(raw.length, BUDGET + 1);
  markdown('sentinel');
  for (const input of [normalized, raw]) {
    const root = markdown(input);
    assert.equal(root.className, 'md');
    assert.equal(find(root, e => e.tag === 'h3')[0].textContent, 'Heading');
    assert.equal(find(root, e => e.tag === 'strong').length, 1);
    const links = find(root, e => e.tag === 'a');
    assert.deepEqual(links.map(e => e.attrs.href), ['https://evil.example/x', 'https://e.com/a.png']);
    for (const link of links) {
      assert.equal(link.attrs.target, '_blank');
      assert.equal(link.attrs.rel, 'noopener noreferrer nofollow');
    }
    assert.equal(find(root, e => e.tag === 'img' || e.tag === 'script').length, 0);
    const rest = find(root, e => e.className === 'md-rest');
    assert.equal(rest.length, 1);
    assert.equal(rest[0].textContent, tail);
    assert.equal(root.textContent, 'Headingbold docs (evil.example) shot (e.com)' + fill + tail);
  }
  assert.equal(count(raw), 2, 'oversized keys reparse even across String normalization');
  markdown('sentinel');
  assert.equal(count('sentinel'), 1, 'a bypassed oversized key must not evict existing entries');
});

regression('small repeated bodies reuse real parse data and create independent complete DOM trees', ({ markdown, count }) => {
  const key = '**bold** [docs](https://evil.example/x)\n- one';
  const first = markdown(key), second = markdown(key);
  const expected = 'bold docs (evil.example)one';
  assert.equal(first.textContent, expected);
  assert.equal(second.textContent, expected);
  const firstNodes = new Set(find(first, () => true));
  assert.ok(find(second, () => true).every(node => !firstNodes.has(node)), 'no DOM node is shared');
  find(first, e => e.tag === 'strong')[0].textContent = 'changed';
  find(first, e => e.tag === 'a')[0].setAttribute('href', 'https://changed.example/');
  assert.equal(second.textContent, expected);
  const third = markdown(key);
  assert.equal(third.textContent, expected);
  assert.equal(find(third, e => e.tag === 'a')[0].attrs.href, 'https://evil.example/x');
  assert.equal(count(key), 1, 'all three renders share only the parsed data');
});

regression('cache hits do not charge retained input twice', ({ markdown, count }) => {
  const a = body('hit-a:', BUDGET / 2), b = body('hit-b:', BUDGET / 2 - 1);
  markdown(a);
  for (let i = 0; i < 4; i++) markdown(a);
  markdown(b);
  markdown('c');
  for (const key of [a, b, 'c']) {
    markdown(key);
    assert.equal(count(key), 1, 'all entries at the exact budget survive repeated hits');
  }
  markdown('d');
  for (const key of [b, 'c', 'd']) {
    markdown(key);
    assert.equal(count(key), 1);
  }
  markdown(a);
  assert.equal(count(a), 2, 'the next unit still evicts the oldest');
});

regression('budget counts UTF-16 code units after String conversion', ({ markdown, count }) => {
  const astral = '😀'.repeat((BUDGET - 2) / 2);
  assert.equal(astral.length, BUDGET - 2);
  const input = { toString: () => astral };
  assert.equal(markdown(input).textContent, astral);
  markdown(input);
  markdown('xy');
  markdown(astral);
  assert.equal(count(astral), 1, 'UTF-8 bytes must not cause early bypass or eviction');
  markdown('z');
  markdown('xy');
  assert.equal(count('xy'), 1);
  markdown(input);
  assert.equal(count(astral), 2, 'code points must not undercharge surrogate pairs');
});

regression('null and primitive normalization share their actual string keys', ({ markdown, count }) => {
  for (const value of [null, undefined, '']) assert.equal(markdown(value).textContent, '');
  assert.equal(count(''), 1);
  for (const value of [0, '0', { toString: () => '0' }]) assert.equal(markdown(value).textContent, '0');
  assert.equal(count('0'), 1);
  assert.equal(markdown(false).textContent, 'false');
  markdown('false');
  assert.equal(count('false'), 1);
});

regression('raw line-ending keys stay distinct and pay their pre-parse length', ({ markdown, count }) => {
  const row = 'r'.repeat(510);
  const crlf = (row + '\r\n').repeat(128), lf = (row + '\n').repeat(128);
  const padding = body('line-padding:', BUDGET - crlf.length - lf.length);
  assert.equal(markdown(crlf).textContent, row.repeat(128));
  assert.equal(markdown(lf).textContent, row.repeat(128));
  markdown(padding);
  for (const key of [crlf, lf, padding]) {
    markdown(key);
    assert.equal(count(key), 1, 'distinct raw keys fit exactly before the extra unit');
  }
  markdown('!');
  markdown(lf);
  assert.equal(count(lf), 1, 'the second raw key remains cached');
  markdown(crlf);
  assert.equal(count(crlf), 2, 'charging normalized line endings would miss this eviction');
});

regression('the original 1000-entry FIFO ceiling still includes empty keys', ({ markdown, count }) => {
  markdown(null);
  for (let i = 0; i < 999; i++) markdown(String(i));
  markdown(undefined);
  markdown('0');
  assert.equal(count(''), 1, 'the empty entry fits at exactly 1000 entries');
  assert.equal(count('0'), 1);
  markdown('999');
  markdown('0');
  assert.equal(count('0'), 1, 'only the oldest empty entry was evicted');
  markdown('');
  assert.equal(count(''), 2, 'empty keys cost an entry even though they cost no text units');
  markdown('1');
  assert.equal(count('1'), 1);
  markdown('0');
  assert.equal(count('0'), 2, 'hits do not refresh FIFO order under the entry ceiling');
});
