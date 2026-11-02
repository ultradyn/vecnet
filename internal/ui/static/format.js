// Pure display helpers. No DOM, and no clock unless a caller leaves one out.

// Times are en-AU 24-hour local. h23, because hour12:false can print 24:05.
const TIME = { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' };
const DATE = { day: 'numeric', month: 'short' };

export function when(iso, nowMs = Date.now()) {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const t = d.toLocaleTimeString('en-AU', TIME);
  return d.toDateString() === new Date(nowMs).toDateString() ? t : d.toLocaleDateString('en-AU', DATE) + ' ' + t;
}

// nowMs is the hub's clock wherever the caller has one.
export function ago(iso, nowMs = Date.now()) {
  if (!iso) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const s = Math.floor((nowMs - t) / 1000);
  if (s < 1) return 'now';
  if (s < 60) return s + 's ago';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

export function bytes(n) {
  if (n < 1024) return n + ' B';
  return n < 1 << 20 ? (n / 1024).toFixed(1) + ' KiB' : (n / 1048576).toFixed(1) + ' MiB';
}

export function short(id) {
  return id && id.length > 14 ? id.slice(0, id.indexOf('_') + 1) + '…' + id.slice(-5) : (id || '?');
}

export function title(thread, me, nameOf) {
  if (!thread) return '';
  if (thread.title) return thread.title;
  const others = (thread.participants || []).filter(p => p !== me).map(nameOf);
  return others.length ? others.join(', ') : short(thread.thread_id);
}

// weekly_remaining_pct is the one standard quota key (PROTOCOL §2.6).
export function quotaLabel(quota) {
  const pct = quota && quota.weekly_remaining_pct;
  return typeof pct === 'number' ? pct + '% weekly left' : '';
}
