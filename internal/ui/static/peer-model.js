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

const RUNTIME_REASONS = {
  connecting: 'Waiting for runtime connection', unreachable: 'Runtime unreachable',
  refused_version: 'Unsupported runtime version', stopped: 'Runtime stopped',
  expired: 'Runtime lease expired', archived: 'Runtime archived', not_loaded: 'Waiting for runtime',
  idle_stale: 'Runtime inactive', approval_policy: 'Approval policy prevents delivery',
  waiting_approval: 'Waiting for approval', waiting_input: 'Waiting for input', over_limit: 'Runtime limit reached',
  identity_changed: 'Runtime identity changed', ownership_changed: 'Runtime ownership changed',
  unverified_thread: 'Runtime not verified', identity_unverified: 'Runtime identity not verified',
  watch_stopped: 'Runtime connection stopped', delivery_unresolved: 'Delivery outcome unresolved',
};
const RUNTIME_STATES = ['idle', 'working', 'blocked', 'offline', 'unknown'];
const RUNTIME_DELIVERIES = ['ready', 'unavailable', 'unsupported', 'unknown'];
const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const reportTime = v => typeof v === 'string' && ISO.test(v) && Number.isFinite(Date.parse(v)) ? v : null;

// Runtime reports are separate from manually authored presence. Only the
// public fields have a display projection; arbitrary reasons/private fields
// never become copy. nowMs must be the hub clock, supplied again on each tick.
function runtimeStatusOf(v, nowMs) {
  if (!v || typeof v !== 'object' || Array.isArray(v) || v.runtime !== 'codex') return null;
  const updatedAt = reportTime(v.updated_at), expiresAt = reportTime(v.expires_at);
  const valid = Number.isFinite(nowMs) && updatedAt && expiresAt
    && Date.parse(updatedAt) <= nowMs && Date.parse(expiresAt) > Date.parse(updatedAt)
    && RUNTIME_STATES.includes(v.state) && RUNTIME_DELIVERIES.includes(v.delivery);
  // Retain the hub's stale projection even while its clock offset catches up.
  const stale = v.reason === 'stale' || !!(valid && nowMs >= Date.parse(expiresAt));
  const reason = typeof v.reason === 'string' && Object.hasOwn(RUNTIME_REASONS, v.reason) ? RUNTIME_REASONS[v.reason] : null;
  return {
    state: valid && !stale ? v.state : 'unknown',
    delivery: valid && !stale ? v.delivery : 'unknown',
    reason: stale ? 'Stale report' : valid ? reason : 'Invalid report',
    version: str(v.version) || null,
    updatedAt,
    stale,
  };
}

// peerFacts flattens a PeerRow. Presence and reachability stay separate
// signals (PROTOCOL §8): a missing value reads unknown, and a source, which
// only pushes in, has no presence.
export function peerFacts(row, nowMs) {
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
    runtimeStatus: runtimeStatusOf(p.runtime_status, nowMs),
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

// ---- Push routes on the peer page (PATCH/POST/DELETE /v1/admin/routes) ----

// ROUTE_KINDS are what Add route offers: a signed webhook, or a Grok Bot
// routine (its URL and crsr_ key). GROK_URL is the routine URL the hub
// accepts (hub.GrokbotURLPattern); the hub checks again.
export const ROUTE_KINDS = ['http', 'grokbot'];
export const GROK_URL = /^https:\/\/api2\.cursor\.sh\/automations\/webhook\/[0-9a-f-]{36}$/;
// routeSetup says what the Routes section offers this peer. editable: the
// operator may add, edit and remove routes here (never for the operator, a
// source or an unknown row). missing: a Grok Bot that no grokbot route
// reaches, its own or its principal's (send falls back to the principal's),
// the one kind reached only by push (a routine cannot pull): the "added
// itself but gave no webhook" case. Every other kind can pull, so no route
// is not a fault. Unknown until routes load (routes null): never missing.
// canAdd: Add route is offered, which is only while the peer has no route
// of its own (the hub pushes through one per peer, so a second would
// quietly take over) or a missing Grok Bot routine needs one. suggest is
// the kind Add route starts on.
export function routeSetup(facts, routes) {
  if (!facts || !facts.id || facts.kind === 'operator' || facts.kind === 'source') return { editable: false, missing: false, suggest: 'http', canAdd: false };
  const known = Array.isArray(routes);
  const list = known ? routes : [];
  const grok = facts.kind === 'grokbot';
  const missing = known && grok && !facts.archivedAt && !list.some(r => r && r.kind === 'grokbot');
  const canAdd = known && (missing || !list.some(r => r && r.via === 'peer'));
  return { editable: true, missing, suggest: grok ? 'grokbot' : 'http', canAdd };
}

// normalizeKey takes a pasted routine key: trimmed, one leading "Bearer "
// dropped (as onboarding accepts). It never echoes the value in an error.
export function normalizeKey(raw) {
  let s = typeof raw === 'string' ? raw.trim() : '';
  if (/^bearer\s+/i.test(s)) s = s.replace(/^bearer\s+/i, '');
  return s;
}
const KEY_OK = /^crsr_[\x21-\x7e]+$/;

// routeForm checks an Add or Edit form before anything is sent. kind is
// the route's kind; editing says whether the route exists (then a blank
// field means unchanged, but something must change). Returns { body } to
// send, or { error, field }. Messages never quote the URL or the key.
export function routeForm({ kind, url, key, editing = false }) {
  const u = typeof url === 'string' ? url.trim() : '';
  const k = normalizeKey(key);
  const body = {};
  if (u) {
    let parsed = null;
    try { parsed = new URL(u); } catch { /* reported below */ }
    if (!parsed || !/^https?:$/.test(parsed.protocol)) return { error: 'Enter a full https:// URL.', field: 'url' };
    if (parsed.username || parsed.password) return { error: 'The URL must not hold a user name or password.', field: 'url' };
    if (parsed.hash || u.includes('#')) return { error: 'The URL must not have a #fragment.', field: 'url' };
    if (kind === 'grokbot' && !GROK_URL.test(u)) return { error: 'A Grok Bot routine URL is https://api2.cursor.sh/automations/webhook/ followed by its id.', field: 'url' };
    body.url = u;
  } else if (!editing) {
    return { error: 'Enter the URL to push to.', field: 'url' };
  }
  if (kind === 'grokbot') {
    if (k) {
      if (!KEY_OK.test(k)) return { error: 'A routine key starts crsr_ and has no spaces.', field: 'key' };
      body.key = k;
    } else if (!editing) {
      return { error: 'Enter the routine key (crsr_…).', field: 'key' };
    }
  }
  if (editing && !Object.keys(body).length) return { error: kind === 'grokbot' ? 'Enter a new URL, a new key, or both.' : 'Enter the new URL.', field: 'url' };
  return { body };
}

// ROUTE_CODE_ERR: fixed sentences for the hub's answers to a route change.
// The hub's own message and any unknown code stay off the page, so neither
// a URL nor a key quoted by a proxy body can render.
const ROUTE_CODE_ERR = {
  invalid_url: 'The hub refused that URL: it must be https, to a public host, with no user name or #fragment.',
  invalid_key: 'The hub refused that key: a routine key starts crsr_ and has no spaces.',
  not_found: 'This route no longer exists. Reload the page.',
  route_changed: 'The route\'s URL changed while it was being verified. Verify it again.',
  forbidden: 'Only the operator can change routes.',
  rate_limited: 'Too many changes. Wait and try again.',
};
const REASONS = {
  timeout: 'it did not answer in time', connection_refused: 'the connection was refused', dns_error: 'its host name did not resolve',
  tls_error: 'its TLS certificate was refused', private_address: 'it is a private address', redirect_refused: 'it answered with a redirect',
  scheme_refused: 'it is not https',
};

// routeError is the one sentence a failed route action shows. A failed
// verify names why when the hub gave a known reason code.
export function routeError(fallback, e, { verifying = false } = {}) {
  const code = e && typeof e === 'object' && typeof e.code === 'string' ? e.code : '';
  if (verifying && code === 'invalid_value') {
    const reason = e.details && typeof e.details.reason === 'string' ? e.details.reason : '';
    const why = Object.hasOwn(REASONS, reason) ? REASONS[reason]
      : /^challenge_failed: HTTP (\d{3})$/.test(reason) ? 'it answered HTTP ' + reason.slice(-3)
        : reason.startsWith('challenge_failed') ? 'it did not echo the challenge' : '';
    return 'The receiver did not answer its verification challenge' + (why ? ': ' + why + '.' : '.');
  }
  if (code === 'invalid_value' && e.details && e.details.field === 'key') return 'Only a Grok Bot route takes a key.';
  return Object.hasOwn(ROUTE_CODE_ERR, code) ? ROUTE_CODE_ERR[code] : fallback;
}
