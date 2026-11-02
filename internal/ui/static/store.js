// Cached views, the event pump and the hub clock. No DOM: covered by node.

const WAIT = 25, BACKOFF_FIRST = 1000, BACKOFF_MAX = 30000;

const THREAD_TAGS = {
  delivery: ['inbox', 'threads'],
  receipt: ['inbox'],
  thread_updated: ['threads'],
  approval_expired: ['inbox'],
};

function isObject(v) { return v !== null && typeof v === 'object' && !Array.isArray(v); }
function stringOrAbsent(v) { return v === undefined || v === null || typeof v === 'string'; }

// tagsFor mirrors opapi.Tags (PROTOCOL §2.13). As there, an event whose data
// would not decode invalidates nothing, whichever field is the wrong type.
export function tagsFor(ev) {
  const type = ev && ev.type, data = ev && ev.data;
  const known = type === 'presence' || type === 'directory_updated' || Object.hasOwn(THREAD_TAGS, type);
  if (!known || !isObject(data)) return [];
  const message = data.message;
  if (!stringOrAbsent(data.thread_id) || !stringOrAbsent(data.peer_id)) return [];
  if (message !== undefined && message !== null && !(isObject(message) && stringOrAbsent(message.thread_id))) return [];
  if (type === 'presence' || type === 'directory_updated') return data.peer_id ? ['peers'] : [];
  const thread = type === 'delivery' ? message && message.thread_id : data.thread_id;
  return thread ? [...THREAD_TAGS[type], 'thread:' + thread] : [];
}

function waitFor(ms, signal) {
  return new Promise(resolve => {
    if (signal && signal.aborted) { resolve(); return; }
    const done = () => { clearTimeout(timer); if (signal) signal.removeEventListener('abort', done); resolve(); };
    const timer = setTimeout(done, ms);
    if (signal) signal.addEventListener('abort', done);
  });
}

export function createStore({ api, now = Date.now, sleep = waitFor }) {
  const live = new Set();       // resources invalidation reaches
  const inflight = new Set();   // running load cycles
  const connWatchers = new Set();
  let flushQueued = false, offset = 0, conn = 'connecting';

  function setConn(next) {
    if (next === conn) return;
    conn = next;
    for (const fn of [...connWatchers]) fn(next);
  }

  function setHubNow(iso) {
    const t = Date.parse(iso);
    if (Number.isFinite(t)) offset = t - now();
  }

  // Invalidations made in one tick collapse into one load per resource.
  function flush() {
    flushQueued = false;
    for (const r of [...live]) r.start();
  }
  function queueFlush() {
    if (flushQueued) return;
    flushQueued = true;
    queueMicrotask(flush);
  }

  // A resource loads at most one snapshot at a time and goes round again if it
  // was invalidated meanwhile. It stops being reachable when its last
  // subscriber leaves, and catches up when one returns, so a screen that
  // unmounts stops costing requests.
  function resource(tag, loader) {
    const subs = new Set();
    let value, err = null, dirty = false, running = null, stale = true;

    const notify = () => {
      for (const fn of [...subs]) {
        try { fn(); } catch (e) { console.error(e); }
      }
    };
    async function cycle() {
      await null; // so a loader that throws at once still runs after start()
      do {
        dirty = false;
        try {
          value = await loader();
          err = null;
          // Invalidations do not reach a resource nobody is listening to, so a
          // load that ends then may already be behind.
          if (live.has(self)) stale = false;
          if (isObject(value) && typeof value.now === 'string') setHubNow(value.now);
        } catch (e) { err = e; }
        notify();
      } while (dirty);
      // Same tick as the check above, so no invalidate can fall between them.
      inflight.delete(running);
      running = null;
      notify();
    }
    function start() {
      if (running || !dirty) return running;
      running = cycle();
      inflight.add(running);
      notify();
      return running;
    }
    const self = {
      tag,
      start,
      mark() { dirty = true; queueFlush(); },
      get: () => value,
      error: () => err,
      loading: () => dirty || running !== null,
      subscribe(fn) {
        const first = subs.size === 0;
        subs.add(fn);
        if (first) {
          live.add(self);
          if (stale) self.mark();
        }
        return () => {
          if (!subs.delete(fn) || subs.size) return;
          live.delete(self);
          dirty = false;
          stale = true;
        };
      },
      async reload() {
        dirty = true;
        await null;
        await (running || start());
      },
    };
    live.add(self);
    return {
      get: self.get, error: self.error, loading: self.loading,
      subscribe: self.subscribe, reload: self.reload,
    };
  }

  function invalidate(...tags) {
    for (const r of live) if (tags.includes(r.tag)) r.mark();
  }
  function invalidateAll() {
    for (const r of live) r.mark();
  }
  // A hub that answers can serve what it could not before. Nothing else would
  // ask again: the batch that prompted a failed load has been acked, and a
  // first load that failed had no event behind it at all.
  function retryFailed() {
    for (const r of live) if (r.error()) r.mark();
  }
  // Resolves once nothing is queued or loading. A failed load counts: its
  // error sits on the resource, and the pump's next poll retries it.
  async function settled() {
    await null;
    while (flushQueued || inflight.size) await Promise.allSettled(inflight);
  }

  // The loop behind the PROTOCOL §2.3 status table. An ack stays owed across
  // failures and is paid before the next poll, or a held delivery in the last
  // batch would never reach accepted.
  async function pump(signal) {
    const aborted = () => !!signal && signal.aborted;
    const interrupted = new Promise(resolve => {
      if (aborted()) resolve();
      else if (signal) signal.addEventListener('abort', resolve, { once: true });
    });
    const resync = () => Promise.race([settled(), interrupted]);
    let after, first = true, owed, backoff = BACKOFF_FIRST;
    while (!aborted()) {
      try {
        if (owed) { await api.ack(owed); owed = undefined; }
        const page = await api.events(first ? { wait: 0, signal } : { after, wait: WAIT, signal });
        if (aborted()) break;
        if (page === null) break; // 204: peer disabled
        first = false;
        backoff = BACKOFF_FIRST;
        setConn('live');
        retryFailed();
        after = page.next_cursor || after;
        const events = page.events || [];
        if (!events.length) continue;
        invalidate(...events.flatMap(tagsFor));
        await resync();
        if (!aborted()) owed = after;
      } catch (e) {
        if (aborted() || e.status === 401 || e.status === 403) break;
        const oldest = e.status === 409 && e.details && e.details.oldest_cursor;
        if (oldest) {
          first = false;
          after = oldest;
          owed = undefined;
          invalidateAll();
          await resync();
          continue;
        }
        // A plain refusal of the ack will not change on retry.
        if (e.status >= 400 && e.status < 500 && e.status !== 429) owed = undefined;
        setConn('offline');
        await sleep(backoff, signal);
        backoff = Math.min(backoff * 2, BACKOFF_MAX);
      }
    }
    setConn('stopped');
  }

  return {
    resource, invalidate, invalidateAll, settled, setHubNow, pump,
    hubNow: () => now() + offset,
    conn: () => conn,
    // The nav redraws its connection state from this.
    onConn(fn) { connWatchers.add(fn); return () => connWatchers.delete(fn); },
  };
}
