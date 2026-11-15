// Inline image thumbnails: which attachments may be fetched and shown, and the
// object URLs made for them. Pure: the api and URL are handed in, no DOM.
// Attachments are untrusted (PROTOCOL §14.2): a thumbnail is fetched only for
// a declared raster type of at most 10 MiB or SVG of at most 2 MiB with hosted
// bytes. Served types/sizes are checked too; SVG is rasterized off-DOM first.
// Only raster object URLs may be handed to the view.

export const THUMB_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];
export const THUMB_MAX = 10 << 20;
export const SVG_MAX = 2 << 20;
const SVG_TYPE = 'image/svg+xml';

const SHA = /^[0-9a-f]{64}$/;
const essence = t => String(t || '').split(';')[0].trim().toLowerCase();
const rasterType = t => THUMB_TYPES.includes(essence(t));
const fits = (n, max = THUMB_MAX) => Number.isFinite(n) && n >= 0 && n <= max;
const NONE = Object.freeze({ state: 'none', url: null });

// eligible applies the declared checks. hosted is what the operator's
// history page says (its hosted_blobs, PROTOCOL §2.8): true when the hub
// hosts this message's bytes, false when it does not (reference-only or
// purged), undefined when the page did not say. A stored Message never holds
// a blob_url, so without that word an origin_url may mean reference-only
// bytes (§11), which the hub does not host: no fetch.
export function eligible(att, hosted) {
  if (!att || typeof att.sha256 !== 'string' || !SHA.test(att.sha256)) return false;
  const svg = essence(att.media_type) === SVG_TYPE;
  if ((!svg && !rasterType(att.media_type)) || !fits(att.size, svg ? SVG_MAX : THUMB_MAX)) return false;
  if (att.blob_url) return true;
  if (typeof hosted === 'boolean') return hosted;
  return !att.origin_url;
}

// createThumbCache fetches each sha256 at most once while it lives, and calls
// onReady once per settled fetch so the view can draw again. broken(sha256)
// marks an image that failed to decode; release() revokes every URL it made.
export function createThumbCache({ api, URL, rasterize, onReady = () => {} }) {
  const entries = new Map(); // sha256 -> { state: loading|ok|chip, url }
  const stats = { fetches: 0, created: 0, revoked: 0 };
  const controller = new AbortController();
  let dead = false;

  const revoke = e => {
    if (!e || !e.url) return;
    URL.revokeObjectURL(e.url);
    e.url = null;
    stats.revoked++;
  };

  function fetchOne(sha, svg) {
    const e = { state: 'loading', url: null };
    entries.set(sha, e);
    stats.fetches++;
    let p;
    try { p = Promise.resolve(api.blob(sha, undefined, { signal: controller.signal })); } catch (err) { p = Promise.reject(err); }
    p.then(async blob => {
      if (dead) return;
      if (svg) {
        if (!blob || essence(blob.type) !== SVG_TYPE || !fits(blob.size, SVG_MAX) || !rasterize) throw new Error('invalid SVG');
        blob = await rasterize(blob);
        if (dead) return;
        // The rasterizer returns bytes, not a URL: untrusted SVG URLs can
        // never escape through this cache, even on a failed conversion.
        if (!blob || essence(blob.type) !== 'image/png') throw new Error('not PNG');
      }
      if (blob && rasterType(blob.type) && fits(blob.size)) {
        e.url = URL.createObjectURL(blob);
        stats.created++;
        e.state = 'ok';
      } else {
        e.state = 'chip';
      }
    }).catch(() => {
      if (!dead) e.state = 'chip';
    }).then(() => { if (!dead) onReady(); });
  }

  function state(att, hosted) {
    if (dead || !eligible(att, hosted)) return NONE;
    if (!entries.has(att.sha256)) fetchOne(att.sha256, essence(att.media_type) === SVG_TYPE);
    const e = entries.get(att.sha256);
    return { state: e.state, url: e.state === 'ok' ? e.url : null };
  }

  function broken(sha) {
    const e = entries.get(sha);
    if (dead || !e) return;
    revoke(e);
    e.state = 'chip';
  }

  function release() {
    if (dead) return;
    dead = true;
    controller.abort();
    for (const e of entries.values()) revoke(e);
    entries.clear();
  }

  return { eligible, state, broken, release, stats };
}
