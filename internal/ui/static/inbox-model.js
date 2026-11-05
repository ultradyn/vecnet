// Pure inbox logic: section order, dead-letter lines, why a delivery was held.
// No DOM; covered by node (jstest/inbox.test.mjs).
import { short, when } from './format.js';

// PROTOCOL §3.3: a Delivery gets 8 push attempts, and each operator retry
// grants a fresh 8 while the attempt counter continues (§5.5).
const PUSH_ATTEMPTS = 8;

const CLASS_LABEL = {
  retries_exhausted: 'retries exhausted',
  terminal: 'terminal 4xx',
  network: 'network error',
};

const list = v => (Array.isArray(v) ? v : []);
const plural = (n, one, many) => n + ' ' + (n === 1 ? one : many);

// sections returns the inbox's non-empty sections in page order: decisions,
// failed deliveries, the attention slot, then deliveries that woke the
// operator and those held for the next look. Items keep wire order.
export function sections(inbox) {
  if (!inbox) return [];
  const deliveries = list(inbox.deliveries);
  const woke = deliveries.filter(d => d.section === 'woke');
  const held = deliveries.filter(d => d.section !== 'woke');
  const all = [
    { key: 'approvals', title: 'Needs a decision', items: list(inbox.approvals), label: n => n + ' open' },
    { key: 'dead_letters', title: 'Failed deliveries', items: list(inbox.dead_letters), label: n => plural(n, 'dead letter', 'dead letters') },
    { key: 'attention', title: 'Subscriptions', items: list(inbox.attention), label: String },
    { key: 'woke', title: 'Woke you', items: woke, label: String },
    { key: 'held', title: 'Held', items: held, label: String },
  ];
  return all.filter(s => s.items.length).map(s => ({ key: s.key, title: s.title, count: s.items.length, label: s.label(s.items.length), items: s.items }));
}

// deadLetterParts is deadLetterLine split up, so the page can colour the
// status. A part with cls 'code' is the HTTP status.
export function deadLetterParts(dl, nowMs = Date.now()) {
  const attempt = Number.isInteger(dl.attempt) && dl.attempt > 0 ? dl.attempt : 0;
  const budget = PUSH_ATTEMPTS * Math.max(1, Math.ceil(attempt / PUSH_ATTEMPTS));
  const parts = [];
  if (dl.last_status !== null && dl.last_status !== undefined) parts.push({ text: String(dl.last_status), cls: 'code' });
  parts.push({ text: CLASS_LABEL[dl.class] || String(dl.class || 'failed'), cls: '' });
  parts.push({ text: 'attempts ' + attempt + '/' + budget, cls: '' });
  const at = when(dl.updated_at, nowMs);
  if (at) parts.push({ text: at, cls: '' });
  // id lets the page keep the full id in the text, so a copy is whole.
  parts.push({ text: short(dl.delivery_id), cls: 'mono', id: dl.delivery_id });
  return parts;
}

// "503 · retries exhausted · attempts 8/8 · 04:11 · dly_…8J0M2"
export function deadLetterLine(dl, nowMs = Date.now()) {
  return deadLetterParts(dl, nowMs).map(p => p.text).join(' · ');
}

// heldWhy says why a held delivery waited instead of waking the operator
// (PROTOCOL §5.2: for the operator peer, hold and wake_idle are inbox only).
export function heldWhy(delivery) {
  if (!delivery || delivery.section === 'woke') return '';
  if (delivery.priority === 'hold') return 'sent as hold: kept for your next look and never pushed';
  if (delivery.priority === 'wake_idle') return 'sent as wake_idle: it wakes idle peers but never notifies the operator';
  return '';
}

// "Load older" pages carry the cursor they were read with: { before,
// deliveries, next_before }. The hub's cursor is exclusive and names the last
// row of a page (PROTOCOL §2.13), so a page joins the one above it only while
// its before is that page's next_before. A new delivery shifts the head by a
// row, and the old head's last row then sits in neither the head nor the
// first older page.

// chainBreak returns the index of the first page that no longer follows the
// page above it (the head for index 0), or -1 when every page does.
export function chainBreak(inbox, pages) {
  let cursor = inbox ? inbox.next_before ?? null : null;
  for (let i = 0; i < pages.length; i++) {
    if (!cursor || pages[i].before !== cursor) return i;
    cursor = pages[i].next_before ?? null;
  }
  return -1;
}

// withOlder appends the older pages that still follow the head, so a gap can
// never show: past a break it pages on from the last page that joins. A row
// two pages hold keeps the first copy, which is the fresher of the two.
export function withOlder(inbox, pages) {
  if (!inbox || !pages.length) return inbox;
  const k = chainBreak(inbox, pages);
  const joined = k < 0 ? pages : pages.slice(0, k);
  if (!joined.length) return inbox;
  const seen = new Set();
  const deliveries = [];
  for (const d of [list(inbox.deliveries), ...joined.map(p => list(p.deliveries))].flat()) {
    if (seen.has(d.delivery_id)) continue;
    seen.add(d.delivery_id);
    deliveries.push(d);
  }
  return { ...inbox, deliveries, next_before: joined[joined.length - 1].next_before ?? null };
}

// follow re-chains pages under a reloaded head: the pages that still follow it
// stay, and from the first that does not, as many pages as were loaded are
// read again from the right cursor with fetchPage(before). A failed read
// keeps what joined so far and reports the error; nothing older than the
// head's tail is ever dropped silently, because withOlder pages on from there.
//
// With refresh, every loaded page is read again from the head's tail even when
// the chain still joins: a read in another client changes read_at, not the
// cursors, and the older pages must show it too.
//
// count is how many pages to end with (default: as many as are loaded); past
// the loaded ones, pages are read on from the end of the chain until it ends.
export async function follow(inbox, pages, fetchPage, { refresh = false, count = pages.length } = {}) {
  let k = refresh && pages.length ? 0 : chainBreak(inbox, pages);
  if (k < 0) {
    if (pages.length >= count) return { pages, error: null };
    k = pages.length;
  }
  const next = pages.slice(0, k);
  let before = k ? next[k - 1].next_before ?? null : inbox.next_before ?? null;
  try {
    while (next.length < count && before) {
      const page = await fetchPage(before);
      next.push({ ...page, before });
      before = page.next_before ?? null;
    }
  } catch (error) {
    return { pages: next, error };
  }
  return { pages: next, error: null };
}

// olderPager owns the "Load older" pages under a head that reloads. request()
// asks for one page more than is loaded; sync(head) brings the pages in line
// with head: on a new head it re-reads every loaded page (follow, refresh),
// and it reads on for any page asked for. A newer head supersedes a pass in
// flight, and the pass that replaces it reads every page asked for, so a click
// that lands while the head reloads is applied under the new head, never
// dropped. sync resolves to { error } once the pages are in line (a failed
// read is reported once and asks for nothing more), or null when the pass was
// superseded or another pass for the same head is already running.
export function olderPager(fetchPage) {
  const superseded = new Error('superseded');
  let pages = [], wanted = 0, synced = null, running = null;
  const pending = head => !!head && (wanted > pages.length || (pages.length > 0 && synced !== head));
  async function sync(head) {
    if (!pending(head) || running === head) return null;
    running = head;
    const count = Math.max(wanted, pages.length);
    const read = before => { if (running !== head) throw superseded; return fetchPage(before); };
    const { pages: got, error } = await follow(head, pages, read, { refresh: synced !== head, count });
    if (running !== head || error === superseded) return null;
    running = null;
    pages = got;
    synced = head;
    // Pages asked for during the pass are still owed; the rest are settled,
    // whether they were read, ran past the end of the chain, or failed.
    wanted = got.length + Math.max(0, wanted - count);
    return { error: error || null };
  }
  return {
    pages: () => pages,
    pending,
    // request is one click: false while a click is still owed.
    request() {
      if (wanted > pages.length) return false;
      wanted = pages.length + 1;
      return true;
    },
    sync,
  };
}
