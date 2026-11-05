// The operator activity feed connection (PROTOCOL §2.13), one per tab: app.js
// opens it and screens subscribe. Events are { type, data }: `ready` on every
// (re)connect and `resync` after a gap mean "refetch"; `message` and `thread`
// carry metadata to fold in (activity-model.js). The feed is lossy and
// unreplayed, so a broken or silent connection is simply reopened.
import { createSSEParser } from './activity-model.js';

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
  const subs = new Set();
  const emit = ev => {
    for (const fn of [...subs]) {
      try { fn(ev); } catch (e) { console.error(e); }
    }
  };
  async function run() {
    let backoff = BACKOFF_FIRST;
    while (!signal.aborted) {
      const conn = new AbortController();
      const cut = () => conn.abort();
      signal.addEventListener('abort', cut, { once: true });
      let watchdog, restartMs = null;
      const arm = () => { clearTimeout(watchdog); watchdog = setTimeout(cut, SILENT_MS); };
      try {
        const r = await api.activity({ signal: conn.signal });
        arm();
        const feed = createSSEParser(f => {
          let data;
          try { data = JSON.parse(f.data || '{}'); } catch { return; }
          if (f.event === 'ready') backoff = BACKOFF_FIRST;
          if (f.event === 'hub_restarting') {
            const ms = data && data.retry_after_ms;
            restartMs = Number.isFinite(ms) ? Math.min(Math.max(ms, 0), 60000) : 0;
          }
          emit({ type: f.event, data });
        });
        const reader = r.body.pipeThrough(new TextDecoderStream()).getReader();
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          arm();
          feed(value);
        }
      } catch (e) {
        // A refused credential will not change on retry: 401 signs out
        // (api.js), 403 is not the operator.
        if (e && (e.status === 401 || e.status === 403)) return;
      } finally {
        clearTimeout(watchdog);
        signal.removeEventListener('abort', cut);
      }
      if (signal.aborted) return;
      if (restartMs !== null) {
        // An announced restart (PROTOCOL §21.3): wait about the hint,
        // jittered ±20%, and start the backoff afresh rather than count it.
        await sleep(Math.round(restartMs * (0.8 + 0.4 * random())), signal);
        backoff = BACKOFF_FIRST;
        continue;
      }
      await sleep(backoff, signal);
      backoff = Math.min(backoff * 2, BACKOFF_MAX);
    }
  }
  return {
    start: run,
    subscribe(fn) { subs.add(fn); return () => subs.delete(fn); },
  };
}
