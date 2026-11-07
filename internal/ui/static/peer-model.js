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
    archivedAt: str(r.archived_at) || null,
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

// recentThread is the newest-activity thread on that page that both this peer
// and the operator (me) are in, or null.
export function recentThread(page, peerId, me) {
  if (!me) return null;
  return peerThreads(page, peerId).rows.find(r => (r.thread.participants || []).includes(me) && str(r.thread.thread_id)) || null;
}

// messageDraft is what New message opens with for this peer: the peer as the
// one recipient, at wake_idle for a Grok Bot (a wake would start a run while
// it is busy; wake_idle waits for idle) and at the composer's wake otherwise.
export function messageDraft(facts) {
  return { recipients: [facts.id], priority: facts.kind === 'grokbot' ? 'wake_idle' : 'wake' };
}

// peerActions is the peer header's two message actions, or null when the
// operator cannot message this id: a source, the operator itself, or a peer
// row that has not loaded. open is the quick link: the operator's newest
// thread with the peer (state 'thread'), else New message with the draft
// (state 'compose'); before the threads page answers it is 'loading', or
// 'error' when that read failed. compose is New message, always present.
// more says the page was not the whole list, so "no thread" means none among
// the newest.
export function peerActions(facts, page, me, error) {
  if (!facts || !facts.id || facts.kind === 'source' || facts.kind === 'operator' || facts.id === me) return null;
  const draft = messageDraft(facts);
  const compose = { href: '#new', draft };
  if (!page) return { open: { state: error ? 'error' : 'loading', error: error || null }, compose };
  const r = recentThread(page, facts.id, me);
  if (r) return { open: { state: 'thread', href: '#thread/' + r.thread.thread_id, thread: r.thread, at: str(r.last_message_at) || null }, compose };
  return { open: { state: 'compose', href: '#new', draft, more: !!page.next }, compose };
}

// INVITE_STATES are the invite states the card shows (GET
// /v1/admin/peers/{id}/enrol-token); claimed and linked mean no card.
const INVITE_STATES = ['pending', 'expired', 'revoked', 'none'];
// NO_TOKEN_KINDS never hold an enrolment token: a dot links with a code, the
// operator and a source are not invited.
const NO_TOKEN_KINDS = ['dot', 'operator', 'source'];

// inviteCard is the peer page's Invite card, or null for none. It shows only
// while a peer that enrols with a token holds no live credential (its own or
// its principal's: facts.credential), and never for a kind that holds no
// token, nor for a Grok Bot reached by its route (it links instead; routes
// is peerRoutes, null until loaded). status is the invite-status answer, or
// null before it lands ({state: 'loading'}). canRevoke: only a pending
// invite has a live token to withdraw.
export function inviteCard(facts, status, routes) {
  if (!facts || !facts.id || NO_TOKEN_KINDS.includes(facts.kind) || facts.credential === 'active') return null;
  if (facts.kind === 'grokbot' && (!Array.isArray(routes) || routes.some(r => r && r.kind === 'grokbot'))) return null;
  const card = { state: 'loading', canRevoke: false, expiresAt: null, revokedAt: null, disabled: !!facts.disabled };
  if (!status || status.peer_id !== facts.id) return card;
  if (!INVITE_STATES.includes(status.state)) return null;
  card.state = status.state;
  card.canRevoke = status.state === 'pending';
  if (status.state === 'pending' || status.state === 'expired') card.expiresAt = str(status.expires_at) || null;
  if (status.state === 'revoked') card.revokedAt = str(status.revoked_at) || null;
  return card;
}
