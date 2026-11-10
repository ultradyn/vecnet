import test from 'node:test';
import assert from 'node:assert/strict';
import { APIError, createApi } from '../static/api.js';
import { createSearchBox } from '../static/search.js';
import { DEBOUNCE_MS, PEERS_SHOWN, Q_MAX, SEARCH_PAGE, createSearch, matchPeers, normQuery, peerResults, quoted, threadResults, tooLong } from '../static/search-model.js';

const DIR = { peers: [
  { peer_id: 'peer_op', name: 'max', kind: 'operator' },
  { peer_id: 'peer_a', name: 'codex-hark', kind: 'session' },
  { peer_id: 'peer_b', name: 'Grok-Reviewer', kind: 'grokbot' },
  { peer_id: 'peer_c', name: 'hark', kind: 'session' },
  { peer_id: 'peer_d', name: 'pag-sleuth', kind: 'pagchat-bot' },
  { peer_id: 'src_gh', name: 'github-hark', kind: 'source' },
] };
const NAMES = Object.fromEntries(DIR.peers.map(p => [p.peer_id, p.name]));
const nameOf = id => NAMES[id] || id;
const ids = list => list.map(p => p.peer_id);

// Fake timers: run() fires whatever is scheduled, as the clock passing would.
function timers() {
  let next = 1;
  const live = new Map();
  return {
    setTimer: (fn, ms) => { const id = next++; live.set(id, { fn, ms }); return id; },
    clearTimer: id => { live.delete(id); },
    pending: () => [...live.values()],
    run() { const due = [...live.entries()]; live.clear(); for (const [, t] of due) t.fn(); },
  };
}

// A searchThreads stand-in: each call parks until the test answers it. It
// does not reject on abort, so a cancelled request can still answer late, as
// one already on the wire can, and the model must drop that answer itself.
function stubApi() {
  const calls = [];
  return {
    calls,
    api: {
      searchThreads: params => new Promise((resolve, reject) => { calls.push({ params, resolve, reject }); }),
    },
  };
}
const flush = () => new Promise(resolve => setImmediate(resolve));
const thread = (id, title, participants = ['peer_op', 'peer_a']) => ({ thread_id: id, title, participants, message_count: 2 });

function setup(extra = {}) {
  const t = timers(), { api, calls } = stubApi();
  const s = createSearch({ api, setTimer: t.setTimer, clearTimer: t.clearTimer, ...extra });
  let changes = 0;
  s.subscribe(() => { changes++; });
  return { t, calls, s, changes: () => changes };
}

test('matchPeers: a case-insensitive name substring over the loaded directory', () => {
  assert.deepEqual(ids(matchPeers(DIR, 'HARK')), ['peer_c', 'peer_a', 'src_gh'], 'exact name, then the rest by name');
  assert.deepEqual(ids(matchPeers(DIR, 'grok')), ['peer_b']);
  assert.deepEqual(ids(matchPeers(DIR, 'g')), ['src_gh', 'peer_b', 'peer_d'], 'names that start with g first, each by name');
  assert.deepEqual(ids(matchPeers(DIR, '  sleuth  ')), ['peer_d'], 'surrounding spaces are ignored');
  // Names only: an id or a kind does not match.
  assert.deepEqual(matchPeers(DIR, 'peer_a'), []);
  assert.deepEqual(matchPeers(DIR, 'grokbot'), []);
  assert.deepEqual(matchPeers(DIR, 'zzz'), []);
});

test('matchPeers: sigma variants match alone and in word-final names', () => {
  const directory = [
    { peer_id: 'peer_upper', name: 'ΟΣ' },
    { peer_id: 'peer_final', name: 'ος' },
    { peer_id: 'peer_medial', name: 'οσ' },
  ];
  for (const q of ['Σ', 'σ', 'ς', 'ΟΣ', 'οσ', 'ος']) {
    assert.deepEqual(ids(matchPeers(directory, q)).sort(), ['peer_final', 'peer_medial', 'peer_upper'], q);
  }
  const sigmas = ['Σ', 'σ', 'ς'].map((name, i) => ({ peer_id: 'peer_' + i, name }));
  for (const q of ['Σ', 'σ', 'ς']) assert.deepEqual(ids(matchPeers(sigmas, q)), ['peer_0', 'peer_1', 'peer_2'], q);
  assert.deepEqual(matchPeers([{ peer_id: 'peer_accent', name: 'café' }], 'cafe'), [], 'accents stay distinct');
});

for (const [name, query] of [
  ['ẞeta', 'ß'], ['ßeta', 'ẞ'],
  ['Kate', 'kate'], ['kate', 'Kate'],
  ['K', 'k'], ['k', 'K'],
]) {
  test(`matchPeers: name ${name} matches case-equivalent query ${query}`, () => {
    const peer = { peer_id: 'peer_match', name };
    assert.deepEqual(ids(matchPeers([peer], query)), ['peer_match']);
  });
}

for (const { label, names, queries } of [
  { label: 'sharp-S', names: ['aẞ', 'ßeta', 'ẞ', 'ß', 'ẞeta', 'ẞalpha', 'zß', 'aß'], queries: ['ß', 'ẞ'] },
  { label: 'Kelvin', names: ['aK', 'kate', 'K', 'k', 'Kate', 'Kalpha', 'zk', 'ak'], queries: ['k', 'K'] },
]) {
  test(`matchPeers: ${label} variants share exact, prefix and substring ranking`, () => {
    const peers = names.map((name, i) => ({ peer_id: 'peer_' + i, name }));
    for (const query of queries) {
      assert.deepEqual(ids(matchPeers(peers, query)), [
        'peer_2', 'peer_3', // Exact names, tied by peer id.
        'peer_5', 'peer_1', 'peer_4', // Prefix names, alphabetically then by id.
        'peer_0', 'peer_7', 'peer_6', // Substrings, alphabetically then by id.
      ], query);
    }
  });
}

test('matchPeers: an empty query is the full directory; bad input is no peers', () => {
  assert.equal(matchPeers(DIR, '').length, DIR.peers.length);
  assert.equal(matchPeers(DIR, '   ').length, DIR.peers.length);
  assert.equal(matchPeers(DIR.peers, '').length, DIR.peers.length, 'the bare array works too');
  assert.deepEqual(matchPeers(null, 'x'), []);
  assert.deepEqual(matchPeers({ peers: [null, { name: 'no id' }, { peer_id: 'peer_x', name: 7 }] }, ''), [{ peer_id: 'peer_x', name: 7 }]);
});

test('peerResults: hit, miss, loading, the operator and sources cannot be picked, and a cap', () => {
  const hit = peerResults({ directory: DIR, text: 'hark', me: 'peer_op' });
  assert.equal(hit.state, 'ok');
  assert.deepEqual(hit.rows.map(r => [r.id, r.name, r.pick]), [['peer_c', 'hark', true], ['peer_a', 'codex-hark', true], ['src_gh', 'github-hark', false]]);
  const me = peerResults({ directory: DIR, text: 'MAX', me: 'peer_op' });
  assert.deepEqual(me.rows.map(r => [r.id, r.me, r.pick]), [['peer_op', true, false]]);
  const miss = peerResults({ directory: DIR, text: 'nobody', me: 'peer_op' });
  assert.equal(miss.state, 'empty');
  assert.equal(miss.note, 'No peer name contains “nobody”');
  assert.equal(peerResults({ directory: null, text: 'x', me: 'peer_op' }).state, 'loading');
  const many = { peers: Array.from({ length: PEERS_SHOWN + 3 }, (_, i) => ({ peer_id: 'peer_' + i, name: 'bot-' + String(i).padStart(2, '0'), kind: 'session' })) };
  const capped = peerResults({ directory: many, text: 'bot', me: 'peer_op' });
  assert.equal(capped.rows.length, PEERS_SHOWN);
  assert.equal(capped.more, 3);
});

test('normQuery and tooLong count characters as the hub does (code points, max 128)', () => {
  assert.equal(normQuery('  release  '), 'release');
  assert.equal(normQuery(undefined), '');
  assert.equal(tooLong('x'.repeat(Q_MAX)), false);
  assert.equal(tooLong('x'.repeat(Q_MAX + 1)), true);
  // 128 emoji are 256 UTF-16 units but 128 characters: allowed.
  assert.equal(tooLong('🔎'.repeat(Q_MAX)), false);
  assert.equal(tooLong('🔎'.repeat(Q_MAX + 1)), true);
});

test('searchThreads calls GET /v1/threads with q, participant, limit and cursor; never the admin list', async () => {
  const calls = [];
  const api = createApi({ secret: 'vn_secret', fetch: async (url, init) => { calls.push({ url, init }); return new Response(JSON.stringify({ threads: [], next: null }), { status: 200 }); } });
  await api.searchThreads({ q: 'Release plan', limit: 50 });
  await api.searchThreads({ q: 'a&b', participant: 'peer_a', limit: 50, cursor: 'tok/1' });
  await api.searchThreads({ q: '', participant: 'peer_a', limit: 50 });
  assert.deepEqual(calls.map(c => c.url), [
    '/v1/threads?q=Release+plan&limit=50',
    '/v1/threads?q=a%26b&participant=peer_a&limit=50&cursor=tok%2F1',
    '/v1/threads?participant=peer_a&limit=50',
  ]);
  assert.ok(calls.every(c => c.init.method === 'GET' && c.init.headers.Authorization === 'Bearer vn_secret'));
});

test('typing is debounced: one request, for the last text, after the pause', async () => {
  const { t, calls, s } = setup();
  s.setText('r');
  s.setText('re');
  s.setText('rel');
  assert.equal(calls.length, 0, 'nothing is sent while typing');
  assert.equal(t.pending().length, 1, 'one timer, restarted by each keystroke');
  assert.equal(t.pending()[0].ms, DEBOUNCE_MS);
  assert.ok(DEBOUNCE_MS >= 200 && DEBOUNCE_MS <= 300);
  assert.deepEqual({ open: s.state().open, pending: s.state().pending }, { open: true, pending: true });
  t.run();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].params.q, 'rel');
  assert.equal(calls[0].params.participant, '');
  assert.equal(calls[0].params.limit, SEARCH_PAGE);
  assert.equal(calls[0].params.cursor, undefined);
  assert.equal(s.state().loading, true);
  calls[0].resolve({ threads: [thread('thr_1', 'Release plan')], next: null });
  await flush();
  assert.deepEqual(s.state().page, { threads: [thread('thr_1', 'Release plan')], next: null });
  assert.equal(s.state().loading, false);
  // Spaces around the same query, or typing back to it, ask nothing more.
  s.setText(' rel ');
  s.setText('rele');
  s.setText('rel');
  assert.equal(t.pending().length, 0);
  assert.equal(calls.length, 1);
});

test('the thread results are the hub page as-is: no client filter over titles', async () => {
  const { t, calls, s } = setup();
  s.setText('plan');
  t.run();
  // The hub matched these; one title does not visibly contain "plan" (say,
  // the hub folds case differently). The browser shows what the hub sent.
  calls[0].resolve({ threads: [thread('thr_1', 'Release PLAN'), thread('thr_2', 'Rollout'), thread('thr_3', null, ['peer_op', 'peer_b'])], next: 'tok1' });
  await flush();
  const v = threadResults(s.state(), { me: 'peer_op', nameOf, now: Date.now(), unread: { thr_1: 2 } });
  assert.equal(v.state, 'ok');
  assert.equal(v.heading, 'Your Threads');
  assert.deepEqual(v.rows.map(r => [r.id, r.title, r.unread]), [['thr_1', 'Release PLAN', 2], ['thr_2', 'Rollout', 0], ['thr_3', 'Grok-Reviewer', 0]]);
  assert.equal(v.rows[0].href, '#thread/thr_1');
  assert.equal(v.more, true, 'the hub has another page');
});

test('a title miss is an empty state, not an error', async () => {
  const { t, calls, s } = setup();
  s.setText('nothing like this');
  assert.equal(threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 }).note, 'Searching…');
  t.run();
  assert.equal(threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 }).state, 'loading');
  calls[0].resolve({ threads: [], next: null });
  await flush();
  const v = threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 });
  assert.equal(v.state, 'empty');
  assert.equal(v.note, 'No thread title contains “nothing like this”');
});

test('More results asks for the next page with the cursor and appends it', async () => {
  const { t, calls, s } = setup();
  s.setText('plan');
  t.run();
  calls[0].resolve({ threads: [thread('thr_1', 'plan 1')], next: 'tok1' });
  await flush();
  s.more();
  assert.equal(calls.length, 2);
  assert.deepEqual({ q: calls[1].params.q, cursor: calls[1].params.cursor }, { q: 'plan', cursor: 'tok1' });
  const busy = threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 });
  assert.deepEqual([busy.state, busy.rows.length, busy.more, busy.busy], ['loading', 1, true, true], 'rows and the button stay while the next page loads');
  s.more();
  assert.equal(calls.length, 2, 'a press while the next page loads asks nothing');
  calls[1].resolve({ threads: [thread('thr_2', 'plan 2')], next: null });
  await flush();
  assert.deepEqual(s.state().page.threads.map(x => x.thread_id), ['thr_1', 'thr_2']);
  assert.equal(s.state().page.next, null);
  s.more();
  assert.equal(calls.length, 2, 'no next page, no request');
});

test('clearing the text (Esc, the clear button, or backspacing to empty) restores at once and cancels', async () => {
  const { t, calls, s } = setup();
  s.setText('rel');
  t.run();
  assert.equal(calls.length, 1);
  s.clear();
  assert.equal(t.pending().length, 0);
  assert.deepEqual({ open: s.state().open, page: s.state().page, loading: s.state().loading, text: s.state().text }, { open: false, page: null, loading: false, text: '' });
  // The cancelled request's late answer is dropped.
  calls[0].resolve({ threads: [thread('thr_late', 'rel')], next: null });
  await flush();
  assert.equal(s.state().page, null);

  s.setText('plan');
  s.setText('');
  assert.equal(t.pending().length, 0, 'an empty box needs no debounce');
  assert.equal(calls.length, 1, 'and asks the hub nothing');
  assert.equal(s.state().open, false);
});

test('a newer query cancels the older request; its late answer never shows', async () => {
  const { t, calls, s } = setup();
  s.setText('rel');
  t.run();
  s.setText('relay');
  t.run();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].params.signal.aborted, true);
  calls[1].resolve({ threads: [thread('thr_new', 'relay')], next: null });
  calls[0].resolve({ threads: [thread('thr_old', 'rel')], next: null });
  await flush();
  assert.deepEqual(s.state().page.threads.map(x => x.thread_id), ['thr_new']);
  assert.equal(s.state().error, null);
});

test('a query over 128 characters is never sent; the hub refusing one shows its reason', async () => {
  const { t, calls, s } = setup();
  s.setText('x'.repeat(Q_MAX + 1));
  assert.equal(t.pending().length, 0);
  assert.equal(calls.length, 0);
  const v = threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 });
  assert.deepEqual([v.state, v.note], ['error', 'Title search takes at most 128 characters']);
  s.setText('x'.repeat(Q_MAX));
  t.run();
  assert.equal(calls.length, 1, 'exactly 128 is sent');
  calls[0].reject(new APIError(422, 'invalid_value', 'q must be at most 128 characters', { field: 'q' }));
  await flush();
  const r = threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 });
  assert.deepEqual([r.state, r.note, r.retry], ['error', 'The hub refused this search: q must be at most 128 characters', true]);
});

test('a failed search says so and Retry asks again', async () => {
  const { t, calls, s } = setup();
  s.setText('rel');
  t.run();
  calls[0].reject(new APIError(0, 'network', 'hub unreachable'));
  await flush();
  assert.equal(threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 }).note, 'Search failed: hub unreachable');
  s.retry();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].params.q, 'rel');
  calls[1].resolve({ threads: [], next: null });
  await flush();
  assert.equal(s.state().error, null);
});

test('picking a peer combines participant= with q, at once; unpicking returns to your threads', async () => {
  const { t, calls, s } = setup();
  s.setText('plan');
  t.run();
  calls[0].resolve({ threads: [], next: null });
  await flush();
  s.pick('peer_b');
  assert.equal(t.pending().length, 0, 'a pick is not debounced');
  assert.equal(calls.length, 2);
  assert.deepEqual({ q: calls[1].params.q, participant: calls[1].params.participant }, { q: 'plan', participant: 'peer_b' });
  calls[1].resolve({ threads: [thread('thr_g', 'Plan review', ['peer_b', 'peer_d'])], next: null });
  await flush();
  const v = threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 });
  assert.equal(v.heading, 'Threads with Grok-Reviewer');
  assert.deepEqual(v.rows.map(r => r.id), ['thr_g']);
  // With a peer picked, an empty text lists that peer's threads (no q).
  s.setText('');
  assert.equal(calls.length, 3);
  assert.deepEqual({ q: calls[2].params.q, participant: calls[2].params.participant }, { q: '', participant: 'peer_b' });
  assert.equal(s.state().open, true, 'the pick keeps the results open');
  calls[2].resolve({ threads: [], next: null });
  await flush();
  assert.equal(threadResults(s.state(), { me: 'peer_op', nameOf, now: 0 }).note, 'No threads');
  s.setText('plan');
  t.run();
  s.pick('');
  assert.deepEqual({ q: calls.at(-1).params.q, participant: calls.at(-1).params.participant }, { q: 'plan', participant: '' });
  // Clear drops the pick too.
  s.pick('peer_b');
  s.clear();
  assert.deepEqual({ open: s.state().open, participant: s.state().participant }, { open: false, participant: '' });
});

test('close stops timers and requests and silences subscribers', async () => {
  const { t, calls, s, changes } = setup();
  s.setText('rel');
  t.run();
  const seen = changes();
  s.close();
  assert.equal(calls[0].params.signal.aborted, true);
  calls[0].resolve({ threads: [], next: null });
  await flush();
  assert.equal(changes(), seen);
});

test('More results is ignored while a typed query waits out the debounce', async () => {
  const { t, calls, s } = setup();
  s.setText('plan');
  t.run();
  calls[0].resolve({ threads: [thread('thr_1', 'plan 1')], next: 'tok1' });
  await flush();
  s.setText('plans');
  s.more();
  assert.equal(calls.length, 1, 'the next page of the old query is not asked for');
  t.run();
  assert.deepEqual({ q: calls[1].params.q, cursor: calls[1].params.cursor }, { q: 'plans', cursor: undefined });
});

test('typing back to the query in flight keeps that request and asks nothing new', async () => {
  const { t, calls, s } = setup();
  s.setText('rel');
  t.run();
  s.setText('rele');
  s.setText('rel');
  assert.equal(t.pending().length, 0);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].params.signal.aborted, false);
  calls[0].resolve({ threads: [thread('thr_1', 'rel')], next: null });
  await flush();
  assert.deepEqual(s.state().page.threads.map(x => x.thread_id), ['thr_1']);
});

test('result rows take last activity from the admin pages the shell holds', async () => {
  const { t, calls, s } = setup();
  s.setText('plan');
  t.run();
  calls[0].resolve({ threads: [thread('thr_1', 'plan 1'), thread('thr_2', 'plan 2')], next: null });
  await flush();
  const now = Date.parse('2026-11-06T10:00:00.000Z');
  const v = threadResults(s.state(), { me: 'peer_op', nameOf, now, lastAt: id => (id === 'thr_1' ? '2026-11-06T09:55:00.000Z' : '') });
  assert.deepEqual(v.rows.map(r => r.active), ['5m ago', '']);
});

test('notes quote a long query cut to 40 characters', () => {
  assert.equal(quoted('plan'), '“plan”');
  assert.equal(quoted('x'.repeat(41)), '“' + 'x'.repeat(40) + '…”');
  assert.equal(quoted('🔎'.repeat(40)), '“' + '🔎'.repeat(40) + '”');
  const v = peerResults({ directory: DIR, text: 'y'.repeat(129), me: 'peer_op' });
  assert.equal(v.note, 'No peer name contains “' + 'y'.repeat(40) + '…”');
});

// Just enough DOM for the search field, scoped to this regression check.
test('a directory refresh renames the selected peer without searching or notifying again', () => {
  class El {
    constructor() { this.children = []; this.attrs = {}; }
    setAttribute(k, v) { this.attrs[k] = v; }
    addEventListener() {}
    removeEventListener() {}
    append(child) { this.children.push(child); }
    replaceChildren(...children) { this.children = children; }
    set textContent(text) { this.children = [text]; }
    get textContent() { return this.children.map(c => typeof c === 'string' ? c : c.textContent).join(''); }
  }
  const previous = globalThis.document;
  globalThis.document = { createElement: () => new El(), addEventListener() {}, removeEventListener() {} };
  const { api, calls } = stubApi();
  const field = new El();
  let name = 'Grok-Reviewer', changes = 0, box;
  try {
    box = createSearchBox(field, { api, nameOf: () => name, onChange: () => { changes++; } });
    box.pick('peer_b');
    assert.ok(field.textContent.includes('Grok-Reviewer'));
    const chip = field.children[1].children[1], [text, button] = chip.children;
    const before = changes;
    name = 'Renamed reviewer';
    box.refresh();
    assert.ok(field.textContent.includes(name));
    assert.equal(field.children[1].children[1], chip, 'chip stays mounted');
    assert.equal(chip.children[1], button, 'remove button stays mounted');
    assert.equal(text.attrs.title, name + ' · peer_b');
    assert.equal(button.attrs['aria-label'], 'Stop narrowing to ' + name);
    assert.ok(!field.textContent.includes('Grok-Reviewer'));
    assert.equal(changes, before, 'repainting must not recurse through renderNav');
    assert.equal(calls.length, 1, 'renaming must not issue another title search');
    assert.equal(box.state().participant, 'peer_b', 'scope stays bound to the peer id');
    name = 'Renamed\u202ereviewer';
    box.refresh();
    assert.equal(text.textContent, 'Renamedreviewer');
    assert.equal(text.attrs.title, 'Renamedreviewer · peer_b');
    assert.equal(button.attrs['aria-label'], 'Stop narrowing to Renamedreviewer');
  } finally {
    if (box) box.close();
    globalThis.document = previous;
  }
});
