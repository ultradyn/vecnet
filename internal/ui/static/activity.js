// The operator activity feed connection (PROTOCOL §2.13), one per tab: app.js
// opens it and screens subscribe. Events are { type, data }: `ready` on every
// (re)connect and `resync` after a gap mean "refetch"; `message` and `thread`
// carry metadata to fold in (activity-model.js). The feed is lossy and
// unreplayed, so a broken or silent connection is simply reopened.
import { createSSEParser } from './activity-model.js';
import { retryDelay } from './connection-model.js';

const BACKOFF_FIRST = 1000, BACKOFF_MAX = 30000;
// Three missed hub heartbeats (15 s, PROTOCOL §2.3) and the stream is dead.
const SILENT_MS = 45000;

function waitFor(ms, signal) {
  return new Promise(resolve => {
    if (signal.aborted) { resolve(); return; }
    const done = () => { clearTimeout(timer); signal.removeEventListener('abort', done); resolve(); };
    const timer = setTimeout(done, ms);
    signal.addEventListener('abort', done);
  });
}

export function createActivity({ api, signal, sleep = waitFor, random = Math.random }) {
  const subs = new Set(), connWatchers = new Set();
  let state = 'connecting';
  const setConn = next => {
    if (next === state) return;
    state = next;
    for (const fn of [...connWatchers]) fn(next);
  };
  const emit = ev => {
    for (const fn of [...subs]) {
      try { fn(ev); } catch (e) { console.error(e); }
    }
  };
  async function loop() {
    let backoff = BACKOFF_FIRST;
    while (!signal.aborted) {
      const conn = new AbortController();
      let reader, watchdog, restarting = false;
      const cut = () => {
        conn.abort();
        // Also release a pending read when the response ignores fetch's signal.
        if (reader) reader.cancel().catch(() => {});
      };
      signal.addEventListener('abort', cut, { once: true });
      const arm = () => { clearTimeout(watchdog); watchdog = setTimeout(cut, SILENT_MS); };
      try {
        const r = await api.activity({ signal: conn.signal });
        reader = r.body.getReader();
        if (signal.aborted) return;
        arm();
        const feed = createSSEParser(f => {
          if (restarting) return;
          if (f.event === 'hub_restarting') {
            restarting = true;
            setConn('reconnecting');
            cut();
            return; // Transport control never reaches thread/event lists.
          }
          let data;
          try { data = JSON.parse(f.data || '{}'); } catch { return; }
          if (f.event === 'ready') { backoff = BACKOFF_FIRST; setConn('live'); }
          emit({ type: f.event, data });
        });
        const decoder = new TextDecoder();
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          arm();
          feed(decoder.decode(value, { stream: true }));
          if (restarting) break;
        }
      } catch (e) {
        // A refused credential will not change on retry: 401 signs out
        // (api.js), 403 is not the operator.
        if (e && (e.status === 401 || e.status === 403 || e.status === 204)) return;
      } finally {
        clearTimeout(watchdog);
        signal.removeEventListener('abort', cut);
        conn.abort();
        if (reader) {
          try { await reader.cancel(); } catch { /* the transport already ended */ }
          reader.releaseLock();
        }
      }
      if (signal.aborted) return;
      setConn('reconnecting');
      await sleep(retryDelay(backoff, random), signal);
      backoff = Math.min(backoff * 2, BACKOFF_MAX);
    }
  }
  return {
    async start() { try { await loop(); } finally { setConn('stopped'); } },
    conn: () => state,
    onConn(fn) { connWatchers.add(fn); return () => connWatchers.delete(fn); },
    subscribe(fn) { subs.add(fn); return () => subs.delete(fn); },
  };
}
