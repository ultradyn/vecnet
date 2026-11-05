import { HubError } from './errors.ts';
import { SseParser } from './sse.ts';
import type { EventStream, CursorStore, Clock, Log } from './ports.ts';
import type { VecnetEvent } from './wire.ts';

export function createEventStream(opts: {
  baseUrl: string; credential: string; cursors: CursorStore; clock: Clock; log: Log;
  fetch?: typeof fetch;
  onState?: (s: 'connected' | 'recovering', detail?: string) => void;
  // PROTOCOL 2.3/13.1: explicitly reconcile thread history before resuming.
  // No hook or a rejected hook makes run reject with safe 409 cursor_purged.
  // Honor signal to stop reconciliation when the caller aborts.
  onCursorPurged?: (recovery: {oldestCursor: string; signal: AbortSignal}) => Promise<void>;
  silenceMs?: number; maxBackoffMs?: number;
}): EventStream {
  const fetcher = opts.fetch ?? fetch;
  return {
    async run(handler, signal) {
      if (signal.aborted) return {reason:'aborted'};
      // Only this private marker makes history-reconciliation failures terminal.
      const reconciliationFailed = Symbol();
      let cursor = await opts.cursors.load();
      let recoveryAfter: string | undefined;
      let retryMs = 3000, failures = 0, frameless = 0, pollUntil = 0;
      const maxBackoff = opts.maxBackoffMs ?? 60_000;
      async function advance(next: string): Promise<void> {
        await opts.cursors.save(next);
        cursor = next;
      }
      async function deliver(value: unknown, id?: string): Promise<void> {
        if (!isEvent(value)) {
          opts.log.warn('Skipping invalid event', {cursor:id});
          if (id !== undefined) await advance(id);
          return;
        }
        await handler(value);
        await advance(id ?? value.cursor);
      }
      while (!signal.aborted) {
        const polling = opts.clock.now() < pollUntil;
        const controller = new AbortController();
        const connectionSignal = AbortSignal.any([signal,controller.signal]);
        let watchdog: {cancel():void} | undefined;
        let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
        let receivedFrame = false, pollSucceeded = false, resync = false;
        function armWatchdog() {
          watchdog?.cancel();
          watchdog = opts.clock.timer(opts.silenceMs ?? 45_000, () => controller.abort());
        }
        try {
          armWatchdog();
          const url = new URL('/v1/events',opts.baseUrl);
          const headers = new Headers({Authorization:`Bearer ${opts.credential}`,Accept:polling ? 'application/json' : 'text/event-stream'});
          if (recoveryAfter !== undefined) url.searchParams.set('after',recoveryAfter);
          else if (cursor) {
            if (polling) url.searchParams.set('after',cursor);
            else headers.set('Last-Event-ID',cursor);
          }
          if (polling) url.searchParams.set('wait','25');
          const response = await abortable(fetcher(url,{headers,signal:connectionSignal,redirect:'error'}),connectionSignal);
          if (response.status === 401 || response.status === 403) {
            await response.body?.cancel();
            return {reason:'auth',status:response.status};
          }
          if (response.status === 204) return {reason:'disabled'};
          if (response.status === 409) {
            const body: unknown = await abortable(response.json(),connectionSignal);
            const oldest = oldestCursor(body);
            if (oldest !== undefined && recoveryAfter !== oldest) {
              opts.log.warn('cursor_purged; thread-history reconciliation required');
              opts.onState?.('recovering','cursor_purged');
              // The socket watchdog must not time out explicit history recovery.
              watchdog?.cancel();
              controller.abort();
              if (!opts.onCursorPurged) throw reconciliationFailed;
              try {await abortable(opts.onCursorPurged({oldestCursor:oldest,signal}),signal);}
              catch {throw reconciliationFailed;}
              if (signal.aborted) break;
              recoveryAfter = oldest;
              resync = true;
            }
          } else if (response.ok) {
            if (recoveryAfter !== undefined) {cursor = recoveryAfter;recoveryAfter = undefined;}
            opts.onState?.('connected');
            if (polling) {
              const body: unknown = await abortable(response.json(),connectionSignal);
              if (!body || typeof body !== 'object' || !('events' in body) || !Array.isArray(body.events)) throw new Error('bad poll response');
              watchdog?.cancel(); // The complete poll response has arrived.
              for (const e of body.events) {
                await deliver(e);
                if (signal.aborted) break;
              }
              if (!signal.aborted && 'next_cursor' in body && typeof body.next_cursor === 'string' && body.next_cursor !== cursor) await advance(body.next_cursor);
              pollSucceeded = true;
              failures = 0;
            } else {
              if (!response.headers.get('Content-Type')?.toLowerCase().startsWith('text/event-stream') || !response.body) {
                await response.body?.cancel();
                throw new Error('bad stream response');
              }
              const parser = new SseParser();
              reader = response.body.getReader();
              while (!connectionSignal.aborted) {
                const {done,value} = await abortable(reader.read(),connectionSignal);
                if (done) {parser.end();break;}
                for (const frame of parser.push(value)) {
                  receivedFrame = true;
                  failures = 0;
                  armWatchdog();
                  if (frame.kind === 'comment') continue;
                  if (frame.retryMs !== undefined) retryMs = frame.retryMs;
                  // Priming and retry-only frames carry no application Event.
                  if (frame.data === '') continue;
                  let parsed: unknown;
                  try {parsed = JSON.parse(frame.data);}
                  catch {parsed = undefined;}
                  // Silence still aborts the socket during a slow handler.
                  // Wait for processing and persistence before reconnecting,
                  // so a timed-out connection cannot overlap handlers.
                  await deliver(parsed,frame.id);
                  if (connectionSignal.aborted) break;
                }
              }
            }
          } else {
            await response.body?.cancel();
          }
        } catch (err) {
          if (!signal.aborted && err === reconciliationFailed) throw new HubError(409,'cursor_purged');
          if (!signal.aborted) opts.log.warn('Event stream interrupted');
        } finally {
          watchdog?.cancel();
          controller.abort();
          if (reader) {try {await reader.cancel();} catch { /* Already closed by abort. */ }reader.releaseLock();}
        }
        if (signal.aborted) break;
        if (resync) continue;
        if (!polling) {
          frameless = receivedFrame ? 0 : frameless + 1;
          if (frameless >= 3) {pollUntil = opts.clock.now()+300_000;frameless=0;}
        }
        if (pollSucceeded) continue;
        opts.onState?.('recovering');
        const cap = Math.min(maxBackoff,retryMs * 2 ** Math.min(failures++,52));
        try {await opts.clock.sleep(cap*(0.5+Math.random()*0.5),signal);}
        catch {if (!signal.aborted) throw new Error('stream clock failed');}
      }
      return {reason:'aborted'};
    },
  };
}

function isEvent(value: unknown): value is VecnetEvent {
  return !!value && typeof value === 'object' && 'cursor' in value && typeof value.cursor === 'string'
    && 'type' in value && typeof value.type === 'string' && 'at' in value && typeof value.at === 'string' && 'data' in value;
}
function oldestCursor(body: unknown): string | undefined {
  if (!body || typeof body !== 'object' || !('error' in body)) return;
  const error = body.error;
  if (!error || typeof error !== 'object' || !('code' in error) || error.code !== 'cursor_purged' || !('details' in error)) return;
  const details = error.details;
  if (details && typeof details === 'object' && 'oldest_cursor' in details && typeof details.oldest_cursor === 'string') return details.oldest_cursor;
}
// An injected fetch can ignore AbortSignal. Still release the connection
// promptly, and always observe the pending promise to avoid unhandled rejection.
function abortable<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise<T>((resolve,reject) => {
    const abort = () => reject(new Error('stream aborted'));
    if (signal.aborted) {promise.catch(()=>{});abort();return;}
    signal.addEventListener('abort',abort,{once:true});
    promise.then(resolve,reject).finally(()=>signal.removeEventListener('abort',abort));
  });
}
