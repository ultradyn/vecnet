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
  parts.push({ text: short(dl.delivery_id), cls: 'mono' });
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

// withOlder appends the "Load older" pages to the head snapshot. A row both
// pages hold keeps the head's copy, which is the fresher of the two, and
// paging continues from the last older page.
export function withOlder(inbox, pages) {
  if (!inbox || !pages.length) return inbox;
  const seen = new Set();
  const deliveries = [];
  for (const d of [list(inbox.deliveries), ...pages.map(p => list(p.deliveries))].flat()) {
    if (seen.has(d.delivery_id)) continue;
    seen.add(d.delivery_id);
    deliveries.push(d);
  }
  return { ...inbox, deliveries, next_before: pages[pages.length - 1].next_before ?? null };
}
