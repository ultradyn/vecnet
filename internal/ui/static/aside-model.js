// Pure helpers for the thread side panel. No DOM: covered by node.
import { blobView } from './attach-model.js';
import { bytes } from './format.js';

// PROTOCOL §6: the hub's default thread budget.
export const THREAD_BUDGET = 500;

const PRESENCE = new Set(['idle', 'working', 'blocked', 'offline']);
const REACH = new Set(['connected', 'reachable', 'unreachable']);

// Presence is self-reported (§8). Stale, missing or unrecognised presence is
// unknown, and unknown is never offline.
export function presenceLabel(presence) {
  return PRESENCE.has(presence) ? presence : 'unknown';
}

// Reachability is hub-observed per transport (§8) and is a separate signal:
// a push-only peer is reachable or unreachable, never offline.
export function reachLabel(reachability) {
  return REACH.has(reachability) ? reachability : 'unknown';
}

// row is a ThreadRow (or a bare ThreadSummary). budget_remaining null means
// unlimited (§6). fraction is 0..1 for the bar, or null for no bar.
export function budgetBar(row, threadBudget = THREAD_BUDGET) {
  const total = Number.isInteger(threadBudget) && threadBudget > 0 ? threadBudget : THREAD_BUDGET;
  const t = (row && row.thread) || row || {};
  const n = t.budget_remaining;
  if (n === null || n === undefined) return { text: 'unlimited', fraction: null, tone: 'unlimited' };
  if (n <= 0) return { text: 'exhausted', fraction: 0, tone: 'bad' };
  if (n > total) return { text: n + ' left', fraction: 1, tone: 'ok' };
  return { text: n + ' of ' + total + ' left', fraction: n / total, tone: n * 10 <= total ? 'low' : 'ok' };
}

// The name a download is saved under: the peer's name less anything that
// could steer the save path or hide the real extension.
// eslint-disable-next-line no-control-regex
const UNSAFE = /[\u0000-\u001f\u007f-\u009f‎‏‪-‮⁦-⁩/\\:*?"<>|]/g;
function downloadName(name, sha) {
  const clean = (name || '').replace(UNSAFE, '').replace(/^[.\s]+/, '').trim().slice(0, 200);
  return clean || 'file-' + (sha || '').slice(0, 12);
}

// files are a thread detail's files (§2.13). label is shown as text and never
// parsed; download is the safe saved name. refs (blob_id -> { att, state })
// carries what the thread's history says of each R2 file: its recorded
// class, deadline and checksum trust from the message, and its blob_states
// projection. An R2 row reads its state on nowMs, the hub clock.
export function fileRows(files, refs = null, nowMs = Date.now()) {
  return (files || []).map(f => {
    const row = {
      sha256: f.sha256,
      label: f.name || f.media_type || 'file',
      download: downloadName(f.name, f.sha256),
      size: bytes(f.size || 0),
      sender: f.sender,
      at: f.created_at,
      messageId: f.message_id,
      spilled: !!f.spilled,
      purged: !!f.purged,
      disabled: !!f.purged,
      note: f.purged ? 'purged' : f.spilled ? 'spilled' : '',
    };
    if (!f.blob_id) return row;
    const ref = (refs && refs.get(f.blob_id)) || null;
    // No reference yet: there is no recorded deadline, so the row is unknown
    // rather than an available download. A history reference may still carry
    // a null projection; its class and deadline stay on att.
    const att = { sha256: f.sha256, ...(ref ? ref.att : {}), blob_id: f.blob_id, purged: !!f.purged };
    const state = ref ? ref.state : { state: 'unknown' };
    const v = blobView(att, state, nowMs);
    return {
      ...row, blobId: f.blob_id, state: v.state, cls: v.cls, expiresAt: v.expiresAt, kept: v.keep,
      verified: v.verified, trust: ref ? v.trust : '', purged: v.state === 'purged', disabled: !v.canDownload, note: v.label,
    };
  });
}

// participantEntry is what the panel shows for one participant. Presence and
// reachability come from the public directory whenever it lists the peer: the
// shell reloads it every minute, so presence that crossed its TTL reads
// unknown there (PROTOCOL §8), while nothing reloads the admin snapshot for a
// quiet peer. The admin row adds only what is admin-only (disabled), and
// stands in for a peer the directory does not list.
export function participantEntry(adminRows, dirPeers, id) {
  const row = (adminRows || []).find(r => r && r.peer && r.peer.peer_id === id);
  const dir = (dirPeers || []).find(p => p && p.peer_id === id);
  if (!row && !dir) return null;
  const peer = dir ? { ...(row ? row.peer : {}), ...dir } : row.peer;
  return { peer, disabled: !!(row && row.disabled) };
}
