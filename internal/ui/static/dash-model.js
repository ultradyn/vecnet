// Pure helpers for the read-only dashboard. No DOM and no clock.
import { quotaLabel } from './format.js';

const str = v => (typeof v === 'string' ? v : '');

// peerRows flattens GET /v1/admin/peers rows for display. Presence and
// reachability stay two separate signals (PROTOCOL §8): a missing value reads
// unknown, never offline, and a source, which only pushes in, reads n/a.
export function peerRows(rows) {
  return (Array.isArray(rows) ? rows : []).map(r => {
    const p = (r && r.peer) || {};
    const hosts = [];
    for (const route of (r && r.routes) || []) {
      const host = str(route && route.host);
      if (host && !hosts.includes(host)) hosts.push(host);
    }
    return {
      id: str(p.peer_id),
      name: str(p.name),
      kind: str(p.kind),
      presence: p.kind === 'source' ? 'n/a' : str(p.presence) || 'unknown',
      presenceAt: r.presence_at || null,
      reach: str(p.reachability) || 'unknown',
      via: r.reach_via || null,
      seen: p.last_seen || null,
      credential: str(r.credential) || 'none',
      hosts,
      quota: quotaLabel(p.quota),
      location: str(p.location),
      disabled: !!r.disabled,
    };
  });
}

// blockedLine is one PROTOCOL §16.4 note: a peer's push queue for a thread is
// held behind a delivery under retry. names.peer and names.thread turn ids into
// display names; without them the ids are shown.
export function blockedLine(b, names = {}) {
  const peer = names.peer || (id => id), thread = names.thread || (id => id);
  const parts = [
    peer(b.recipient) + ': thread ' + thread(b.thread_id) + ' blocked on delivery ' + b.delivery_id,
    'attempt ' + (b.attempt || 0),
    (b.waiting || 0) + ' waiting',
  ];
  if (b.last_status !== null && b.last_status !== undefined) parts.push('last ' + b.last_status);
  return parts.join(' · ');
}

// cutSummary is the loop-cut window with its reasons, highest count first and
// then by name.
export function cutSummary(cuts) {
  const c = cuts || {};
  const by = c.by_reason && typeof c.by_reason === 'object' ? c.by_reason : {};
  const reasons = Object.keys(by)
    .map(reason => ({ reason, count: Number(by[reason]) || 0 }))
    .sort((a, b) => b.count - a.count || (a.reason < b.reason ? -1 : a.reason > b.reason ? 1 : 0));
  return { total: Number(c.total) || 0, hours: Number(c.hours) || 24, reasons };
}

// allThreads reads every thread page by page (PROTOCOL §2.13: `next` until it
// is null), each request bounded by limit. The Threads section lists them all;
// a cursor the hub hands back twice would loop, so it ends the walk.
export async function allThreads(api, limit = 200) {
  let page = await api.threads({ limit });
  const threads = [...(page.threads || [])];
  const seen = new Set();
  while (page.next && !seen.has(page.next)) {
    seen.add(page.next);
    page = await api.threads({ limit, cursor: page.next });
    threads.push(...(page.threads || []));
  }
  return { threads };
}
