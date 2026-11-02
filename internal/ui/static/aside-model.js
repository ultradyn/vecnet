// Pure helpers for the thread side panel. No DOM: covered by node.
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
// parsed; download is the safe saved name.
export function fileRows(files) {
  return (files || []).map(f => ({
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
  }));
}
