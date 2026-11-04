// Hub notices and the restart banner, pure. A notice's message is shown like
// peer text; nothing acts on any notice field.
import { when as clock } from './format.js';

const RANK = { moving: 0, restart: 1, maintenance: 2 };
const LABEL = { moving: 'Moving', restart: 'Restart', maintenance: 'Maintenance' };
const GRACE = 30000;
const NONE = { phase: 'none', until: 0, since: 0 };

export const isNoticeEvent = ev => !!ev && ev.type === 'hub_notice';

// validNotice mirrors the hubNoticeEvent schema opapi.Tags checks.
const TS = /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/, URL_RE = /^https?:\/\/[^/?#@\s]+(?:\/[^?#\s]*)?$/;
const FIELD = {
  notice_id: /^ntc_[0-9A-HJKMNP-TV-Z]{26}$/, kind: /^(maintenance|restart|moving)$/, message: /(?:)/,
  hub: URL_RE, issued_at: TS, at: TS, expires_at: TS, new_url: URL_RE, sig: /^[A-Za-z0-9_-]{86}$/,
  cleared_at: TS, cleared_how: /^(operator|expired|replaced|boot)$/,
};
const isObj = v => v !== null && typeof v === 'object' && !Array.isArray(v);
export function validNotice(d) {
  const n = isObj(d) && d.notice;
  if (!isObj(n) || !/^(posted|cleared)$/.test(d.change) || typeof d.change !== 'string' || Object.keys(d).length !== 2) return false;
  if (['notice_id', 'kind', 'message', 'hub', 'issued_at', 'expires_at', 'sig'].some(k => !(k in n))) return false;
  for (const [k, v] of Object.entries(n)) if (!Object.hasOwn(FIELD, k) || typeof v !== 'string' || !FIELD[k].test(v)) return false;
  return (n.kind === 'moving') === ('new_url' in n);
}

function hostOf(url) {
  try { const u = new URL(url); return /^https?:$/.test(u.protocol) ? u.host : ''; } catch { return ''; }
}

function rel(t, now) {
  const s = Math.round((t - now) / 1000), a = Math.abs(s);
  if (a < 1) return 'now';
  const u = a < 60 ? a + 's' : a < 3600 ? Math.floor(a / 60) + 'm' : a < 86400 ? Math.round(a / 3600) + 'h' : Math.round(a / 86400) + 'd';
  return s > 0 ? 'in ' + u : u + ' ago';
}

// bannerItems: the notices still standing at now, most important first. A
// notice's time is its `at`, or when it was posted.
export function bannerItems(notices, now) {
  const rank = n => RANK[n.kind] ?? 3;
  return (Array.isArray(notices) ? notices : [])
    .filter(n => isObj(n) && typeof n.notice_id === 'string' && !n.cleared_at && !(Date.parse(n.expires_at) <= now))
    .sort((a, b) => rank(a) - rank(b))
    .map(n => {
      const iso = n.at || n.issued_at, t = Date.parse(iso);
      const host = n.kind === 'moving' ? hostOf(n.new_url) : '';
      return {
        id: n.notice_id, kind: String(n.kind), label: LABEL[n.kind] || 'Notice',
        when: Number.isNaN(t) ? '' : rel(t, now) + ' · ' + clock(iso, now),
        host, url: host ? n.new_url : '', text: String(n.message ?? ''),
      };
    });
}

export function restartState(state, input) {
  const s = state || NONE;
  if (!input) return s;
  if (input.frame === 'hub_restarting') {
    const ms = Number(input.retryAfterMs);
    return { phase: 'restarting', until: input.at + (Number.isFinite(ms) && ms > 0 ? ms : 0), since: input.at };
  }
  if (input.frame === 'ready') return NONE;
  if (input.tick && s.phase === 'restarting' && input.at > s.until + GRACE) return { ...s, phase: 'unreachable' };
  return s;
}

export function restartLine(s, now) {
  if (!s || s.phase === 'none') return null;
  if (s.phase === 'unreachable') return { tone: 'bad', text: 'Hub unreachable since ' + clock(new Date(s.since).toISOString(), now) };
  const n = Math.ceil((s.until - now) / 1000);
  return { tone: 'warn', text: n > 0 ? `Hub restarting: reconnecting in ${n} s` : 'Hub restarting: reconnecting…' };
}
