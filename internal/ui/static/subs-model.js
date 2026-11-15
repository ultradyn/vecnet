// Subscription health, pure: no DOM and no clock of its own (PROTOCOL §2.13,
// §13.4). The tones match the TUI's subscription screen.

export const TONES = ['ok', 'warn', 'bad', 'neutral'];

// Every paused state reads as paused (Review Focus 5); its reason follows.
const CHIPS = {
  ok: { label: 'ok', tone: 'ok' },
  failing: { label: 'failing', tone: 'bad' },
  late: { label: 'refresh due', tone: 'warn' },
  expired: { label: 'expired', tone: 'bad' },
  paused_token: { label: 'paused · token', tone: 'bad' },
  paused_failed: { label: 'paused · failed', tone: 'bad' },
  paused_auth: { label: 'paused · auth', tone: 'bad' },
  paused_operator: { label: 'paused · operator', tone: 'warn' },
  unverified: { label: 'unverified', tone: 'warn' },
};

// A value this client does not know stays neutral: it must read neither as
// healthy nor as broken. The raw value is shown, as text.
export function chip(health) {
  if (typeof health === 'string' && Object.hasOwn(CHIPS, health)) return { ...CHIPS[health] };
  return { label: health ? String(health) : 'unknown', tone: 'neutral' };
}

// Keep known problems first without mutating the shared subscription snapshot.
// Future health states stay neutral, as they do in chip().
export function subscriptionItems(list, peerId) {
  return (Array.isArray(list) ? list : [])
    .filter(s => s && s.peer_id === peerId && typeof s.sub_id === 'string' && s.sub_id)
    .map(s => ({ ...s, attention: Object.hasOwn(CHIPS, s.health) && s.health !== 'ok' }))
    .sort((a, b) => Number(b.attention) - Number(a.attention));
}

// The admin view normally supplies a host. Redact paths and credentials even
// if an older or unexpected response supplies a whole callback URL instead.
export function subscriptionHost(value) {
  if (typeof value !== 'string' || !value.trim()) return 'Unknown host';
  // Go's URL.Hostname() projection returns IPv6 without brackets or a port.
  // Validate that representation as an IPv6 authority before displaying it.
  const host = value.trim();
  if (!host.includes('://') && host.includes(':') && !/[/@?#\[\]]/.test(host)) {
    try { new URL('https://[' + host + ']'); return host; } catch {}
  }
  try {
    const u = new URL(value.includes('://') ? value : 'https://' + value);
    return ['https:', 'http:'].includes(u.protocol) && u.host ? u.host : 'Unknown host';
  } catch { return 'Unknown host'; }
}

export function subscriptionRemoveError(e) {
  if (e && e.status === 403) return 'Only the operator can remove a subscription. Sign in as the operator and try again.';
  if (e && e.status === 401) return 'Your sign-in has expired. Sign in again to remove this subscription.';
  if (e && (e.status === 0 || e.code === 'network')) return 'Could not confirm removal. Check your connection and try again; an already removed subscription stays removed.';
  return 'Could not remove this subscription. Try again.';
}

function span(s, big, bigUnit, small, smallUnit) {
  return Math.floor(s / big) + bigUnit + ' ' + Math.floor((s % big) / small) + smallUnit;
}

// hubNowMs is the hub's clock (store.hubNow()), never the client's alone. The
// hub calls a subscription expired from its expiry instant on.
export function expiresIn(iso, hubNowMs) {
  if (!iso || !Number.isFinite(hubNowMs)) return '';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const left = Math.floor((t - hubNowMs) / 1000);
  if (left <= 0) {
    const s = -left;
    if (s < 1) return 'expired just now';
    if (s < 60) return 'expired ' + s + 's ago';
    if (s < 3600) return 'expired ' + Math.floor(s / 60) + 'm ago';
    if (s < 86400) return 'expired ' + Math.floor(s / 3600) + 'h ago';
    return 'expired ' + Math.floor(s / 86400) + 'd ago';
  }
  if (left < 60) return 'in ' + left + 's';
  if (left < 3600) return 'in ' + Math.floor(left / 60) + 'm';
  if (left < 86400) return 'in ' + span(left, 3600, 'h', 60, 'm');
  return 'in ' + span(left, 86400, 'd', 3600, 'h');
}
