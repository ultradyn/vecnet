// Peer avatars (PROTOCOL §8.1). Pure: api and URL are handed in. The bytes
// need the bearer, so they come as a blob shown through an object URL, one
// fetch per hash; prune() revokes hashes the directory no longer names.
export const AVATAR_MAX = 256 << 10;
const SHA = /^[0-9a-f]{64}$/;
const NONE = Object.freeze({ state: 'none', url: null, ready: null });

export const avatarHash = p => (p && typeof p.avatar === 'string' && SHA.test(p.avatar) ? p.avatar : null);

// state(peerId, hash) -> {state: none|loading|ok|failed, url, ready}; ready
// settles with a loading entry.
export function createAvatarCache({ api, URL, onReady = () => {} }) {
  const entries = new Map(), stats = { fetches: 0, created: 0, revoked: 0 };
  let dead = false;
  const revoke = e => { if (e.url) { URL.revokeObjectURL(e.url); e.url = null; stats.revoked++; } };
  function state(peerId, hash) {
    if (dead || !peerId || !SHA.test(hash || '')) return NONE;
    let e = entries.get(hash);
    if (!e) {
      e = { state: 'loading', url: null };
      entries.set(hash, e);
      stats.fetches++;
      const live = () => !dead && entries.get(hash) === e;
      e.ready = new Promise(r => r(api.avatar(peerId))).then(b => {
        if (!live()) return;
        const ok = b && b.type === 'image/png' && b.size > 0 && b.size <= AVATAR_MAX;
        if (ok) { e.url = URL.createObjectURL(b); stats.created++; }
        e.state = ok ? 'ok' : 'failed';
      }, () => { if (live()) e.state = 'failed'; }).then(() => { if (!dead) onReady(); });
    }
    return { state: e.state, url: e.url, ready: e.ready };
  }
  // broken: the browser could not decode it; back to the letter.
  function broken(hash) { const e = entries.get(hash); if (e && !dead) { revoke(e); e.state = 'failed'; } }
  function prune(live) {
    const keep = new Set(live);
    for (const [h, e] of entries) if (!keep.has(h)) { revoke(e); entries.delete(h); }
  }
  function release() { if (!dead) { dead = true; entries.forEach(revoke); entries.clear(); } }
  return { state, broken, prune, release, stats };
}
