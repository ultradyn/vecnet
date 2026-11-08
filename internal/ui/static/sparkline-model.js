// Pure rolling-hour data and geometry. No client clock, presence, or DOM.
const MINUTE = 60000;
const count = value => Number.isInteger(value) && value >= 0;
const record = value => value !== null && typeof value === 'object' && !Array.isArray(value);

function series(value) {
  if (!record(value) || !Array.isArray(value.buckets) || value.buckets.length !== 60) return null;
  const buckets = [];
  let total = 0;
  for (let i = 0; i < 60; i++) {
    const n = value.buckets[i];
    if (!count(n)) return null;
    buckets.push(n);
    total += n;
  }
  const rate = value.messages_per_minute, expected = total / 60;
  if (!Number.isFinite(total) || !count(value.messages) || value.messages !== total
      || !Number.isFinite(rate) || rate < 0
      || Math.abs(rate - expected) > Number.EPSILON * 8 * Math.max(1, expected)) return null;
  return { messages: total, messages_per_minute: expected, buckets };
}

function timestamp(value) {
  if (typeof value !== 'string' || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,3})?Z$/.test(value)) return NaN;
  const at = Date.parse(value);
  // Date.parse silently rolls February 30 into March; require a real date.
  const canonical = value.replace(/(?:\.(\d{1,3}))?Z$/, (_, fraction = '') => `.${fraction.padEnd(3, '0')}Z`);
  return Number.isFinite(at) && new Date(at).toISOString() === canonical ? at : NaN;
}

// null means unavailable/invalid, not zero. Copy the accepted fields only;
// caller-owned arrays and object prototypes never become snapshot data.
export function activitySnapshot(value) {
  try {
    if (!record(value)) return null;
    const now = timestamp(value.now), start = timestamp(value.window_start), end = timestamp(value.window_end);
    if (![now, start, end].every(Number.isFinite) || end !== now || end - start !== 60 * MINUTE
        || value.bucket_seconds !== 60 || !count(value.active_peers) || !record(value.peers)) return null;
    const total = series(value);
    if (!total) return null;
    const peers = Object.create(null), sums = Array(60).fill(0);
    let active = 0;
    for (const id of Object.keys(value.peers)) {
      const peer = series(value.peers[id]);
      if (!peer) return null;
      peers[id] = peer;
      if (id.startsWith('peer_') && peer.messages > 0) active++;
      for (let i = 0; i < 60; i++) sums[i] += peer.buckets[i];
    }
    if (active !== value.active_peers || !sums.every((n, i) => n === total.buckets[i])) return null;
    return {
      now: new Date(now).toISOString(), window_start: new Date(start).toISOString(),
      window_end: new Date(end).toISOString(), bucket_seconds: 60,
      ...total, active_peers: active, peers,
    };
  } catch {
    // Unknown input can include revoked proxies or throwing getters.
    return null;
  }
}

// Accepts an activitySnapshot result; absence is zero only after success.
export function peerActivity(snapshot, peerID) {
  if (!snapshot) return null;
  return typeof peerID === 'string' && Object.hasOwn(snapshot.peers, peerID)
    ? snapshot.peers[peerID]
    : { messages: 0, messages_per_minute: 0, buckets: Array(60).fill(0) };
}

// SVG points: "x,y x,y"; area: "M x,baseline L x,y ... L x,baseline Z".
// Mathematical inset 4, linear scale anchored at zero; a supplied finite max
// is shared verbatim (including 0) and values exceeding it are clipped.
export function sparklineGeometry(values, { width = 240, height = 48, max } = {}) {
  width = Number.isFinite(width) && width > 0 ? Math.max(8, width) : 240;
  height = Number.isFinite(height) && height > 0 ? Math.max(8, height) : 48;
  const data = Array.isArray(values)
    ? Array.from(values, n => Number.isFinite(n) && n > 0 ? n : 0) : [];
  let peak = 0;
  for (const n of data) peak = Math.max(peak, n);
  const usedMax = Number.isFinite(max) && max >= 0 ? max : peak;
  const baseline = height - 4;
  const points = data.map((n, i) => {
    const x = 4 + (data.length > 1 ? i / (data.length - 1) : 0) * (width - 8);
    const y = baseline - (usedMax > 0 ? Math.min(n / usedMax, 1) : 0) * (height - 8);
    return `${x},${y}`;
  });
  const lastX = data.length > 1 ? width - 4 : 4;
  return {
    points: points.join(' '),
    area: points.length ? `M 4,${baseline} L ${points.join(' L ')} L ${lastX},${baseline} Z` : '',
    baseline, max: usedMax,
  };
}

// {start,end,label}, ISO UTC and half-open, or null for an invalid selection.
// A bucket is a one-minute count, never an instantaneous rate.
export function bucketLabel(snapshot, index) {
  if (!snapshot || !Number.isInteger(index) || index < 0 || index >= 60) return null;
  const at = timestamp(snapshot.window_start) + index * MINUTE;
  if (!Number.isFinite(at)) return null;
  const start = new Date(at).toISOString(), end = new Date(at + MINUTE).toISOString();
  return { start, end, label: `[${start}, ${end}) UTC — messages in this minute` };
}
