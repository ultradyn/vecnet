// Inline image thumbnails: which attachments may be fetched and shown, and the
// object URLs made for them. Pure: the api and URL are handed in, no DOM.
// Attachments are untrusted (PROTOCOL §14.2): a thumbnail is fetched only for
// a declared raster type of at most 10 MiB with hosted bytes, and shown only
// when the hub serves the same kind of type and size. Never SVG.

export const THUMB_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];
export const THUMB_MAX = 10 << 20;

const SHA = /^[0-9a-f]{64}$/;
const essence = t => String(t || '').split(';')[0].trim().toLowerCase();
const rasterType = t => THUMB_TYPES.includes(essence(t));
const fits = n => Number.isFinite(n) && n >= 0 && n <= THUMB_MAX;
const NONE = Object.freeze({ state: 'none', url: null });

// eligible applies the declared checks. hosted is what the operator's
// history page says (its hosted_blobs, PROTOCOL §2.8): true when the hub
// hosts this message's bytes, false when it does not (reference-only or
// purged), undefined when the page did not say. A stored Message never holds
// a blob_url, so without that word an origin_url may mean reference-only
// bytes (§11), which the hub does not host: no fetch.
export function eligible(att, hosted) {
  if (!att || typeof att.sha256 !== 'string' || !SHA.test(att.sha256)) return false;
  if (!rasterType(att.media_type) || !fits(att.size)) return false;
  if (att.blob_url) return true;
  if (typeof hosted === 'boolean') return hosted;
  return !att.origin_url;
}

// createThumbCache fetches each sha256 at most once while it lives, and calls
// onReady once per settled fetch so the view can draw again. broken(sha256)
// marks an image that failed to decode; release() revokes every URL it made.
export function createThumbCache(options) {
  if (options != null && 'budget' in Object(options)) return createLeaseThumbCache(options);
  return createLegacyThumbCache(options);
}

function createLegacyThumbCache({ api, URL, onReady = () => {} }) {
  const entries = new Map(); // sha256 -> { state: loading|ok|chip, url }
  const stats = { fetches: 0, created: 0, revoked: 0 };
  let dead = false;

  const revoke = e => {
    if (!e || !e.url) return;
    URL.revokeObjectURL(e.url);
    e.url = null;
    stats.revoked++;
  };

  function fetchOne(sha) {
    const e = { state: 'loading', url: null };
    entries.set(sha, e);
    stats.fetches++;
    let p;
    try { p = Promise.resolve(api.blob(sha)); } catch (err) { p = Promise.reject(err); }
    p.then(blob => {
      if (dead) return;
      if (blob && rasterType(blob.type) && fits(blob.size)) {
        e.url = URL.createObjectURL(blob);
        stats.created++;
        e.state = 'ok';
      } else {
        e.state = 'chip';
      }
    }, () => {
      if (!dead) e.state = 'chip';
    }).then(() => { if (!dead) onReady(); });
  }

  function state(att, hosted) {
    if (dead || !eligible(att, hosted)) return NONE;
    if (!entries.has(att.sha256)) fetchOne(att.sha256);
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
    for (const e of entries.values()) revoke(e);
    entries.clear();
  }

  return { eligible, state, broken, release, stats };
}

let nextCacheIdentity = 0n;
function leaseView(cell) {
  return Object.freeze({
    get state() { return cell.state; },
    get url() { return cell.url; },
    get generation() { return cell.generation; },
  });
}
function abortThumbTask(task) {
  const controller = task.controller;
  task.controller = null;
  try { controller?.abort(); } catch { /* Abort failure is not transport settlement. */ }
}
function unlinkThumbTask(task) {
  task.entry = task.start = task.finish = task.cancel = null;
}
function retireThumbTask(task) {
  if (!task) return;
  unlinkThumbTask(task);
  task.ticket?.cancel();
  abortThumbTask(task);
}
// These continuations capture only their task, not factory arguments or a cache.
function finishThumbTask(task, blob) {
  if (task.finish) task.finish(task, blob);
  else {
    const ticket = task.ticket;
    task.ticket = null;
    ticket?.settle({ residentBytes: null });
  }
}
function observeThumbTask(task, promise) {
  Promise.resolve(promise).then(blob => finishThumbTask(task, blob), () => finishThumbTask(task, null));
  promise = undefined;
}
function thumbTaskCallbacks(task) {
  return {
    onStart(ticket) {
      task.ticket = ticket;
      const start = task.start;
      task.start = null;
      if (start) start(task);
      else { ticket.cancel(); finishThumbTask(task, null); }
    },
    onCancel(reason) {
      abortThumbTask(task);
      const cancel = task.cancel;
      task.cancel = null;
      if (cancel) cancel(task, reason);
    },
  };
}
function capacityNotification(link) {
  return () => {
    if (!link.run || link.pending) return;
    link.pending = true;
    queueMicrotask(() => { link.pending = false; link.run?.(); });
  };
}
function ownerKey(key) {
  if (typeof key !== 'string' || !key.length) throw new TypeError('Expected a nonempty thumbnail owner key');
}
function priorityOption(options) {
  if (!options || typeof options !== 'object' || Array.isArray(options)) throw new TypeError('Expected thumbnail priority options');
  const priority = options.priority;
  if (priority !== 'automatic' && priority !== 'explicit') throw new TypeError('Invalid thumbnail priority');
  return priority;
}

function createLeaseThumbCache(options) {
  let { api, URL, budget, beforeRevoke, onChange = () => {} } = options;
  options = undefined;
  let initialStats = budget?.stats;
  if (typeof api?.thumbnailBlob !== 'function' || typeof URL?.createObjectURL !== 'function' ||
      typeof URL?.revokeObjectURL !== 'function' || typeof budget?.enqueue !== 'function' ||
      typeof budget?.claimSource !== 'function' || !initialStats ||
      !['running', 'residentEntries', 'residentBytes', 'reservedBytes', 'queued', 'sources'].every(key => Number.isSafeInteger(initialStats[key]) && initialStats[key] >= 0) ||
      typeof beforeRevoke !== 'function' ||
      typeof onChange !== 'function') throw new TypeError('Invalid lease thumbnail dependencies');
  initialStats = null;
  const identity = `thumb-cache-${++nextCacheIdentity}`, entries = new Map(), owners = new Map();
  // Each cell owns one still-running ticket, never a retired entry or cache.
  const obligations = new Set();
  const counters = { fetches: 0, created: 0, revoked: 0 };
  const capacityLink = { run: capacityPass, pending: false }, onCapacity = capacityNotification(capacityLink);
  let sequence = 0n, dead = false, detaching = false, releasing = false;

  function guard() {
    if (detaching) throw new Error('Thumbnail detach/revoke must not reenter cache mutation');
  }
  function current(entry, task) {
    return !dead && entries.get(entry.sha) === entry && (!task || entry.task === task);
  }
  function publish(entry) {
    for (const owner of entry.owners) {
      owner.cell.state = entry.state;
      owner.cell.url = entry.state === 'ready' ? entry.url : null;
      owner.cell.generation = entry.generation;
    }
  }
  function notify(entry, reason = 'state', terminal = false) {
    if (dead || (!terminal && !current(entry))) return;
    try { onChange({ sha: entry.sha, generation: entry.generation, reason }); } catch { /* Observers do not own cleanup. */ }
  }
  function aggregatePriority(entry) {
    const priority = [...entry.owners].some(owner => owner.priority === 'explicit') ? 'explicit' : 'automatic';
    if (entry.priority === priority) return;
    entry.priority = priority;
    entry.ticket?.setPriority(priority);
  }
  // Mutate no ownership until both synchronous cleanup operations succeed.
  function discardURL(entry) {
    if (entry.url === null) return null;
    detaching = true;
    try {
      beforeRevoke(entry.sha, entry.generation);
      const revoke = URL.revokeObjectURL;
      revoke(entry.url);
    }
    finally { detaching = false; }
    counters.revoked++;
    const ticket = entry.ticket;
    entry.url = null; entry.bytes = 0; entry.ticket = null;
    return ticket;
  }
  function clearObligation(cell) {
    const ticket = cell.ticket;
    cell.phase = 'cleared';
    cell.ticket = cell.url = cell.revoke = null;
    obligations.delete(cell);
    return ticket;
  }
  function discardUnpublished(cell) {
    cell.phase = 'disposing';
    detaching = true;
    try {
      const revoke = cell.revoke;
      revoke(cell.url);
    } catch (error) {
      cell.phase = 'failed';
      throw error;
    } finally { detaching = false; }
    counters.revoked++;
    // Remove all resource ownership before settlement can dispatch reentry.
    return clearObligation(cell);
  }
  function removeEntry(entry) {
    if (entries.get(entry.sha) !== entry) return;
    const resident = discardURL(entry), task = entry.task;
    entry.task = null;
    entries.delete(entry.sha);
    for (const owner of entry.owners) {
      owners.delete(owner.key);
      owner.cell.state = 'dormant'; owner.cell.url = null;
      owner.att = owner.entry = null;
      owner.permit.drop();
    }
    entry.owners.clear();
    // All local views and source ownership retire before abort listeners or
    // released payload capacity can synchronously admit a successor.
    retireThumbTask(task);
    resident?.dropPayload();
  }
  function displaced(task, reason) {
    const entry = task.entry;
    if (reason !== 'displaced' || !entry || !current(entry, task)) return;
    removeEntry(entry);
    notify(entry, 'displaced', true);
  }
  function complete(task, blob) {
    const entry = task.entry, ticket = task.ticket;
    if (!entry || !current(entry, task)) { unlinkThumbTask(task); finishThumbTask(task, null); return; }
    let cell = null, bytes = 0;
    try {
      if (blob && rasterType(blob.type)) {
        const size = blob.size;
        if (Number.isSafeInteger(size) && size >= 0 && size <= task.maxBytes && current(entry, task)) {
          const urlAPI = URL, create = urlAPI.createObjectURL, revoke = urlAPI.revokeObjectURL;
          if (typeof create !== 'function' || typeof revoke !== 'function') throw new TypeError('Invalid thumbnail URL operations');
          // Dependency access may retire the generation too. The provider stays
          // on this synchronous stack only; the cell keeps a standalone revoke.
          if (current(entry, task)) {
            cell = { phase: 'allocating', ticket, url: null, revoke };
            obligations.add(cell);
            cell.url = Reflect.apply(create, urlAPI, [blob]);
            counters.created++;
            bytes = size;
          }
        }
      }
    } catch { /* No URL was returned; release its reservation below. */ }
    const url = cell?.url ?? null;
    // Allocation can synchronously release/drop the original owners. A cell
    // installed before that call survives independently of their hash entry.
    if (!current(entry, task)) {
      unlinkThumbTask(task);
      task.ticket = task.controller = null;
      if (url !== null) {
        let disposed;
        try { disposed = discardUnpublished(cell); }
        catch { return; } // Keep the failed cell, not an ignored rejected promise.
        disposed.settle({ residentBytes: null });
      } else {
        if (cell) clearObligation(cell);
        ticket.settle({ residentBytes: null });
      }
      return;
    }
    entry.task = null; entry.url = url; entry.bytes = url === null ? 0 : bytes;
    entry.state = url === null ? 'error' : 'ready';
    if (url === null) entry.ticket = null;
    publish(entry);
    unlinkThumbTask(task); task.controller = null; task.ticket = null;
    // Ready ownership is committed before this reservation becomes resident.
    if (cell) clearObligation(cell);
    ticket.settle({ residentBytes: url === null ? null : entry.bytes });
    if (current(entry)) notify(entry);
  }
  function start(task) {
    const entry = task.entry;
    if (!entry || !current(entry, task)) { retireThumbTask(task); finishThumbTask(task, null); return; }
    entry.ticket = task.ticket; entry.state = 'loading'; publish(entry);
    let promise;
    try {
      const stats = budget.stats;
      task.maxBytes = stats.reservedBytes / stats.running;
      if (!Number.isSafeInteger(task.maxBytes) || task.maxBytes <= 0 || task.maxBytes > THUMB_MAX) throw new RangeError('Invalid reserved thumbnail limit');
      task.controller = new AbortController();
      counters.fetches++;
      promise = api.thumbnailBlob(entry.sha, { signal: task.controller.signal, maxBytes: task.maxBytes });
    } catch { finishThumbTask(task, null); return; }
    observeThumbTask(task, promise);
    if (current(entry, task)) notify(entry);
  }
  function admit(entry) {
    if (!current(entry) || entry.state !== 'waiting' || entry.task) return;
    const previous = entry.generation;
    const task = { entry, start, finish: complete, cancel: displaced, ticket: null, controller: null, maxBytes: 0 };
    entry.generation = `${identity}/${++sequence}`;
    entry.task = task; entry.state = 'queued'; publish(entry);
    const ticket = budget.enqueue({ key: `${identity}/${entry.sha}/${entry.generation}`, priority: entry.priority, ...thumbTaskCallbacks(task) });
    if (!current(entry, task)) { ticket?.cancel(); return; }
    if (!ticket) {
      unlinkThumbTask(task);
      entry.task = null; entry.state = 'waiting'; entry.generation = previous;
      // Waiting-to-waiting is not a state change or an automatic error retry.
      publish(entry);
    } else {
      task.ticket = entry.ticket = ticket;
      if (entry.state === 'queued') notify(entry);
    }
  }
  function capacityPass() {
    if (dead || !owners.size) return;
    for (const entry of [...entries.values()]) admit(entry);
    if (!dead && owners.size) {
      try { onChange({ sha: null, generation: null, reason: 'capacity' }); } catch { /* A hint is not an ownership transfer. */ }
    }
  }
  function acquire(key, att, hosted, options) {
    guard(); ownerKey(key);
    const priority = priorityOption(options);
    if (dead) return null;
    const old = owners.get(key);
    if (!eligible(att, hosted)) { if (old) drop(key); return null; }
    if (old && old.entry.sha !== att.sha256) throw new TypeError('Drop an owner before changing its hash');
    const metadata = { sha256: att.sha256, size: att.size, media_type: essence(att.media_type), blob_url: !!att.blob_url, origin_url: !!att.origin_url };
    if (old) {
      const entry = old.entry;
      old.att = metadata; old.hosted = typeof hosted === 'boolean' ? hosted : undefined; old.priority = priority;
      aggregatePriority(entry); admit(entry);
      return old.lease;
    }
    const permit = budget.claimSource(`${identity}/owner/${key}`, { onCapacity });
    if (!permit) return null;
    let entry = entries.get(att.sha256);
    if (!entry) {
      entry = { sha: att.sha256, generation: null, state: 'waiting', url: null, bytes: 0, owners: new Set(), priority, ticket: null, task: null };
      entries.set(entry.sha, entry);
    }
    const cell = { state: entry.state, url: entry.url, generation: entry.generation };
    const owner = { key, entry, permit, cell, lease: leaseView(cell), att: metadata, hosted: typeof hosted === 'boolean' ? hosted : undefined, priority };
    owners.set(key, owner); entry.owners.add(owner);
    aggregatePriority(entry); admit(entry);
    return owner.lease;
  }
  function moveOwner(fromKey, toKey, options) {
    guard(); ownerKey(fromKey); ownerKey(toKey);
    const priority = priorityOption(options);
    if (dead) return null;
    const owner = owners.get(fromKey);
    if (!owner || (fromKey !== toKey && owners.has(toKey))) return null;
    if (fromKey !== toKey) {
      // Permit transfer has no callback boundary or zero-owner interval. Keep
      // its existing capacity link rather than dropping/reclaiming a source.
      owner.permit.move(`${identity}/owner/${toKey}`, { onCapacity });
      owners.delete(fromKey);
      owner.key = toKey;
      owners.set(toKey, owner);
    }
    owner.priority = priority;
    aggregatePriority(owner.entry);
    // Priority dispatch may retire/move this owner; never look up a replacement.
    return owner.lease;
  }
  function retry(key) {
    guard(); ownerKey(key);
    if (dead) return null;
    const owner = owners.get(key);
    if (!owner) return null;
    if (!eligible(owner.att, owner.hosted)) { drop(key); return null; }
    const entry = owner.entry;
    if (entry.state !== 'error' && entry.state !== 'waiting') return owner.lease;
    const wasError = entry.state === 'error', previous = entry.generation;
    if (wasError) entry.state = 'waiting';
    admit(entry);
    // A denied retry still commits error -> waiting. Admission/observer reentry
    // may instead have retired this owner or started a different generation.
    if (wasError && current(entry) && owner.entry === entry && owners.get(owner.key) === owner &&
        entry.state === 'waiting' && entry.generation === previous) notify(entry);
    return owner.lease;
  }
  function drop(key) {
    guard(); ownerKey(key);
    const owner = owners.get(key);
    if (!owner) return;
    const entry = owner.entry;
    if (entry.owners.size === 1) removeEntry(entry);
    else {
      owners.delete(key); entry.owners.delete(owner);
      owner.cell.state = 'dormant'; owner.cell.url = null;
      owner.att = owner.entry = null;
      owner.permit.drop(); aggregatePriority(entry);
    }
  }
  function broken(sha, generation) {
    guard();
    const entry = entries.get(sha);
    if (dead || !entry || entry.generation !== generation || entry.state !== 'ready') return;
    const ticket = discardURL(entry);
    entry.state = 'error'; publish(entry);
    ticket?.dropPayload();
    if (current(entry)) notify(entry);
  }
  function release() {
    guard();
    if (releasing) return;
    releasing = true;
    dead = true; onChange = api = null; capacityLink.run = null;
    const pendingEntries = [...entries.values()], pendingObligations = [...obligations];
    for (const owner of owners.values()) { owner.cell.state = 'dormant'; owner.cell.url = null; }
    let didThrow = false, failure;
    // A failed ready-payload cleanup cannot keep unrelated network callbacks
    // alive: cancel every other entry, retaining only failed cleanup ownership.
    try {
      for (const entry of pendingEntries) {
        try { removeEntry(entry); }
        catch (error) { if (!didThrow) { didThrow = true; failure = error; } }
      }
      for (const cell of pendingObligations) {
        // Allocating cells have no returned URL; their synchronous completion
        // will dispose or transfer it. Never settle them from release().
        if (cell.phase !== 'failed') continue;
        try { discardUnpublished(cell).settle({ residentBytes: null }); }
        catch (error) { if (!didThrow) { didThrow = true; failure = error; } }
      }
    } finally {
      if (!entries.size) URL = beforeRevoke = null;
      releasing = false;
    }
    if (didThrow) throw failure;
  }
  return Object.freeze({
    acquire, moveOwner, retry, drop, broken, release,
    get stats() {
      let queued = 0, loading = 0, residentEntries = 0, residentBytes = 0;
      for (const entry of entries.values()) {
        if (entry.state === 'queued') queued++;
        if (entry.state === 'loading') loading++;
        if (entry.url !== null) { residentEntries++; residentBytes += entry.bytes; }
      }
      return Object.freeze({ owners: owners.size, entries: entries.size, queued, loading, residentEntries, residentBytes, ...counters });
    },
  });
}
