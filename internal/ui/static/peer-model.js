// Pure helpers for the peer detail view (#peer/<id>). No DOM and no clock.
// Every value comes from an operator view the dashboard already reads
// (PROTOCOL §2.13); peer-supplied strings stay strings, and the view sets them
// as text.
import { quotaLabel } from './format.js';

const str = v => (typeof v === 'string' ? v : '');
const strs = v => (Array.isArray(v) ? v.filter(x => typeof x === 'string') : []);
const num = v => (Number.isFinite(v) ? v : null);
const ID = /^(peer|src)_[A-Za-z0-9]+$/;

// peerHref is the route for a peer or a directory source, or null for
// anything else (the hub's own "hub" sender, a malformed id).
export function peerHref(id) {
  return typeof id === 'string' && ID.test(id) ? '#peer/' + id : null;
}

// findPeer is the GET /v1/admin/peers row for id, or null.
export function findPeer(page, id) {
  const rows = page && Array.isArray(page.peers) ? page.peers : [];
  return rows.find(r => r && r.peer && r.peer.peer_id === id) || null;
}

// scopes is null when the hub did not send them (an older hub): unknown,
// which must not read as "allow all". open is both lists empty: allow all.
function scopesOf(v) {
  if (!v || typeof v !== 'object') return null;
  const allow = strs(v.allow), deny = strs(v.deny);
  return { allow, deny, open: !allow.length && !deny.length };
}

function limitsOf(v) {
  if (!v || typeof v !== 'object') return null;
  return { sendsPerMin: num(v.sends_per_min), threadsPerHour: num(v.threads_per_hour) };
}

// peerFacts flattens a PeerRow. Presence and reachability stay separate
// signals (PROTOCOL §8): a missing value reads unknown, and a source, which
// only pushes in, has no presence.
export function peerFacts(row) {
  const r = row || {}, p = r.peer || {};
  const kind = str(p.kind);
  return {
    id: str(p.peer_id),
    name: str(p.name),
    kind,
    disabled: !!r.disabled,
    credential: str(r.credential) || 'none',
    principalId: str(r.principal_id),
    createdBy: str(p.created_by),
    presence: kind === 'source' ? 'n/a' : str(p.presence) || 'unknown',
    presenceAt: str(r.presence_at) || null,
    reach: str(p.reachability) || 'unknown',
    via: str(r.reach_via) || null,
    seen: str(p.last_seen) || null,
    enrolled: str(p.enrolled_at) || null,
    capabilities: strs(p.capabilities),
    quota: quotaLabel(p.quota),
    location: str(p.location),
    teams: strs(p.teams),
    scopes: kind === 'source' ? { allow: [], deny: [], open: true } : scopesOf(r.scopes),
    limits: limitsOf(r.limits),
  };
}

// principalFacts is the GET /v1/admin/principals row for id, or null.
export function principalFacts(page, id) {
  if (!id) return null;
  const rows = page && Array.isArray(page.principals) ? page.principals : [];
  const p = rows.find(x => x && x.principal_id === id);
  if (!p) return null;
  return {
    id: p.principal_id, name: str(p.name),
    maxPeers: num(p.max_peers), createsPerHour: num(p.creates_per_hour), kinds: strs(p.kinds),
    active: num(p.active), disabled: num(p.disabled), createsLastHour: num(p.creates_last_hour),
    defaultAllow: strs(p.default_allow), defaultDeny: strs(p.default_deny),
  };
}

// scopeTarget is what a scope id names, from its prefix.
export function scopeTarget(id) {
  const head = str(id).split('_')[0];
  return { peer: 'peer', team: 'team', grp: 'group' }[head] || 'id';
}

// peerRoutes is the push routes that deliver to this peer: its own, and its
// principal's, which every peer under that principal inherits. via says which.
export function peerRoutes(page, peerId, principalId) {
  const rows = page && Array.isArray(page.routes) ? page.routes : [];
  return rows
    .filter(r => r && (r.owner_id === peerId || (principalId && r.owner_id === principalId)))
    .map(r => ({
      id: str(r.route_id), via: r.owner_id === peerId ? 'peer' : 'principal', kind: str(r.kind), host: str(r.host),
      verified: str(r.verified_at) || null, created: str(r.created_at) || null, runsPerHour: num(r.runs_per_hour),
    }));
}

// peerSubs is this peer's subscriptions from the bare GET /v1/admin/subscriptions array.
export function peerSubs(list, peerId) {
  return (Array.isArray(list) ? list : []).filter(s => s && s.peer_id === peerId);
}

// peerHealth is the delivery-health entries addressed to this peer.
export function peerHealth(health, peerId) {
  const h = health || {};
  const mine = xs => (Array.isArray(xs) ? xs : []).filter(x => x && x.recipient === peerId);
  return { blocked: mine(h.blocked), failed: mine(h.failed) };
}

// peerThreads is a GET /v1/admin/threads?participant= page, newest activity
// first. more is true when the hub has another page.
export function peerThreads(page, peerId) {
  if (!page) return { rows: [], more: false };
  const rows = (Array.isArray(page.threads) ? page.threads : [])
    .filter(r => r && r.thread && (r.thread.participants || []).includes(peerId))
    .sort((a, b) => {
      const at = str(a.last_message_at), bt = str(b.last_message_at);
      return at === bt ? 0 : at < bt ? 1 : -1;
    });
  return { rows, more: !!page.next };
}
