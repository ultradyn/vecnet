// Operator presence rules. Pure: no DOM, no clock (PROTOCOL §2.6, §8).

// The hub's TTL is 1 h; a beat every 20 min keeps a chosen state alive.
export const HEARTBEAT_MS = 20 * 60 * 1000;

const REASSERT = new Set(['idle', 'working', 'blocked']);

// heartbeatAction names the state to re-post, or null. It is whatever the hub
// reports, so two open clients re-post the same thing and never fight. Stale
// presence reads `unknown` and is left to lapse, as is `offline`.
export function heartbeatAction(me) {
  const state = me && me.presence;
  return REASSERT.has(state) ? state : null;
}

// One tap: working goes idle, and every other state goes working.
export function toggleTarget(state) {
  return state === 'working' ? 'idle' : 'working';
}

// `unknown` means no fresh report. It never reads as offline.
export function navLabel(name, state) {
  if (!state) return name;
  if (state === 'unknown') return name + ' · presence unknown';
  return name + ' is ' + state;
}

// The hub's wire timestamp: UTC with milliseconds.
export function wireTime(ms) {
  return new Date(ms).toISOString();
}
