// A minimal test peer on hub-client: sends, a recorded event stream, waits,
// approval responses, replies and thread history. Real timers are fine here:
// this is test code, not the daemon.
import { randomUUID } from 'node:crypto';
import { createHubClient } from '../../../hub-client/src/client.ts';
import type { Clock, CursorStore, HubApi, Log } from '../../../hub-client/src/ports.ts';
import { createEventStream } from '../../../hub-client/src/stream.ts';
import type { Delivery, Message, SendRequest, SendResponse, VecnetEvent } from '../../../hub-client/src/wire.ts';

export interface Requester {
  peerId: string;
  secret: string;
  hub: HubApi;
  /** POST /v1/messages; a fresh idempotency key unless one is given. */
  send(req: Omit<SendRequest, 'idempotency_key'> & { idempotency_key?: string }): Promise<SendResponse>;
  /** Every event seen so far, then each new one as it arrives. */
  events(signal?: AbortSignal): AsyncIterableIterator<VecnetEvent>;
  seen(): VecnetEvent[];
  deliveries(): Delivery[];
  /** The first event (seen or future) for which `pick` returns a value; rejects after `timeoutMs`. */
  waitFor<T>(pick: (e: VecnetEvent) => T | undefined | null | false, timeoutMs?: number, what?: string): Promise<T>;
  respond(approval: Message, label: string): Promise<SendResponse>;
  reply(msg: Message, text: string, metadata?: Record<string, unknown>): Promise<SendResponse>;
  /** GET /v1/threads/{id}/messages, every page. */
  history(threadId: string): Promise<Message[]>;
  close(): Promise<void>;
}

const realClock: Clock = {
  now: () => Date.now(),
  timer(ms, fn) { const id = setTimeout(fn, ms); return { cancel: () => clearTimeout(id) }; },
  sleep(ms, signal) {
    return new Promise<void>((resolve, reject) => {
      if (signal?.aborted) { reject(signal.reason); return; }
      const id = setTimeout(() => { signal?.removeEventListener('abort', abort); resolve(); }, ms);
      function abort() { clearTimeout(id); reject(signal?.reason); }
      signal?.addEventListener('abort', abort, { once: true });
    });
  },
};
const quietLog: Log = { info() {}, warn() {}, error() {} };

export async function connectPeer(o: { url: string; peerId: string; secret: string }): Promise<Requester> {
  const hub = createHubClient({ baseUrl: o.url, credential: o.secret });
  const seen: VecnetEvent[] = [];
  const listeners = new Set<() => void>();
  let cursor: string | null = null;
  let acking = false;
  const cursors: CursorStore = {
    async load() { return cursor; },
    async save(next) {
      cursor = next;
      // Ack as a real client would, so held deliveries reach `accepted`; best effort.
      if (acking) return;
      acking = true;
      hub.ack(next).catch(() => {}).finally(() => { acking = false; });
    },
  };
  const controller = new AbortController();
  let connected!: () => void;
  const ready = new Promise<void>(resolve => { connected = resolve; });
  const stream = createEventStream({ baseUrl: o.url, credential: o.secret, cursors, clock: realClock, log: quietLog, silenceMs: 45_000,
    onState: s => { if (s === 'connected') connected(); } });
  const running = stream.run(async e => {
    seen.push(e);
    for (const wake of [...listeners]) wake();
  }, controller.signal).catch(() => {});
  const timeout = setTimeout(() => connected(), 10_000);
  await ready;
  clearTimeout(timeout);

  async function send(req: Omit<SendRequest, 'idempotency_key'> & { idempotency_key?: string }): Promise<SendResponse> {
    return hub.send({ ...req, idempotency_key: req.idempotency_key ?? `e2e.${randomUUID()}` });
  }
  return {
    peerId: o.peerId,
    secret: o.secret,
    hub,
    send,
    async *events(signal) {
      let index = 0;
      while (!signal?.aborted && !controller.signal.aborted) {
        while (index < seen.length) yield seen[index++];
        await new Promise<void>(resolve => {
          const wake = () => { listeners.delete(wake); signal?.removeEventListener('abort', wake); controller.signal.removeEventListener('abort', wake); resolve(); };
          listeners.add(wake);
          signal?.addEventListener('abort', wake, { once: true });
          controller.signal.addEventListener('abort', wake, { once: true });
        });
      }
    },
    seen: () => [...seen],
    deliveries: () => seen.filter(e => e.type === 'delivery').map(e => e.data as Delivery),
    waitFor(pick, timeoutMs = 15_000, what = 'event') {
      return new Promise((resolve, reject) => {
        let index = 0;
        const check = () => {
          for (; index < seen.length; index++) {
            const value = pick(seen[index]);
            if (value !== undefined && value !== null && value !== false) { done(); resolve(value); return; }
          }
        };
        const timer = setTimeout(() => { done(); reject(new Error(`timed out waiting for ${what}`)); }, timeoutMs);
        function done() { clearTimeout(timer); listeners.delete(check); }
        listeners.add(check);
        check();
      });
    },
    respond(approval, label) {
      if (!approval.approval_request) throw new Error('not an approval_request');
      return send({ thread_id: approval.thread_id, in_reply_to: approval.message_id, kind: 'approval_response', priority: 'wake',
        content: { text: label }, approval_response: { approval_id: approval.approval_request.approval_id, decision: label } });
    },
    reply(msg, text, metadata) {
      return send({ thread_id: msg.thread_id, in_reply_to: msg.message_id, kind: 'message', priority: 'wake',
        content: { text }, ...(metadata ? { metadata } : {}) });
    },
    async history(threadId) {
      const messages: Message[] = [];
      let after: string | null = null;
      do {
        const url = new URL(`/v1/threads/${encodeURIComponent(threadId)}/messages`, o.url);
        url.searchParams.set('limit', '200');
        if (after) url.searchParams.set('after', after);
        const res = await fetch(url, { headers: { Authorization: `Bearer ${o.secret}` }, redirect: 'error' });
        if (!res.ok) throw new Error(`history: HTTP ${res.status}`);
        const page = await res.json() as { messages: Message[]; next_after: string | null };
        messages.push(...page.messages);
        after = page.next_after;
      } while (after);
      return messages;
    },
    async close() { controller.abort(); await running; },
  };
}
