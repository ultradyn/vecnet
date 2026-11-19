import { spawn } from 'node:child_process';
import { terminateProcess } from '../../../hub-client/src/platform.ts';
import type { Log } from '../../../hub-client/src/ports.ts';

/** Shared notification watches. A pooled child is a trigger, never turn
 * input: it carries only the wake priority, never delivery ids or message
 * text, and it never changes a consumer's cursor. */
export type WakeEvent = { priority: 'wake' | 'wake_idle' };
/** A child's own classification of its exit: `terminal` carries the hub's
 * refused status (401/403/204-style) and must not be reconnected by the
 * pool; `reconnect` leaves resubscription to the subscriber. */
export type WatchEnd = { kind: 'reconnect' } | { kind: 'terminal'; status: number };
/** The verified stream a child observes. Every component participates in the
 * sharing key: hub identity, endpoint, stream owner, credential slot, and an
 * explicit discriminator including whether the CLI opened the default stream
 * or a narrowed one. Scopes whose stream identity is unproved must carry an
 * exclusive discriminator so they never pool. */
export type WatchScope = { hubIdentity: string; endpoint: string; streamOwner: string; credentialSlot: string; scope: string };
export type WatchChild = { close(): void; ended: Promise<WatchEnd>; onEvent(f: (event: WakeEvent) => void): () => void };
export type WatchRequest = { scope: WatchScope; revision: string; start: () => WatchChild };
export interface WatchPoolDeps { log: Log }
export interface WatchPool {
  subscribe(request: WatchRequest, notify: (event: WakeEvent) => void, stopped: (end: WatchEnd) => void): () => void;
  close(): Promise<void>;
}

type RecordValue = Record<string, any>;
const obj = (x: unknown): RecordValue => x !== null && typeof x === 'object' && !Array.isArray(x) ? x as RecordValue : {};
const MAX_LINE_BYTES = 1_048_576;

/** Spawn one `watch --json` child with bounded line parsing. Lines that are
 * malformed or oversized yield no wake and no message body; a no-newline
 * stream never grows an unbounded buffer. Terminal classification follows the
 * watch's own last line (cursorless `watch_stopped` with a 2xx-4xx status). */
export function spawnWatchChild(spec: {
  command: { file: string; args: string[]; windowsVerbatimArguments?: boolean };
  env: Record<string, string>;
  log: Log;
  context?: Record<string, unknown>;
}): WatchChild {
  const child = spawn(spec.command.file, spec.command.args, { ...spec.command, windowsHide: true, env: spec.env, stdio: ['ignore', 'pipe', 'ignore'] });
  const listeners = new Set<(event: WakeEvent) => void>();
  let terminalStatus: number | undefined, settled = false, fenced = false;
  let resolveEnded!: (end: WatchEnd) => void;
  const ended = new Promise<WatchEnd>(resolve => { resolveEnded = resolve; });
  const settle = (end: WatchEnd) => { if (!settled) { settled = true; resolveEnded(end); } };
  const line = (text: string) => {
    try {
      const event = obj(JSON.parse(text));
      const priority = obj(obj(event.data).message).priority;
      if (event.type === 'delivery' && (priority === 'wake' || priority === 'wake_idle')) {
        // Notification-only: the priority, never ids or message text.
        if (!fenced) for (const f of [...listeners]) f({ priority });
      }
      const status = obj(event.data).status;
      if (event.type === 'watch_stopped' && event.cursor === '' && Number.isInteger(status) && status >= 200 && status < 500) terminalStatus = status;
    } catch { /* The watch stream is a trigger, never turn input. */ }
  };
  let buffer = '', skipping = false;
  child.stdout!.setEncoding('utf8');
  child.stdout!.on('data', (chunk: string) => {
    if (fenced) return;
    buffer += chunk;
    for (let nl = buffer.indexOf('\n'); nl !== -1; nl = buffer.indexOf('\n')) {
      const text = buffer.slice(0, nl);
      buffer = buffer.slice(nl + 1);
      if (skipping) { skipping = false; continue; } // Discard the oversized line's remainder.
      if (Buffer.byteLength(text) <= MAX_LINE_BYTES) line(text);
    }
    // An unterminated line must never accumulate without bound.
    if (Buffer.byteLength(buffer) > MAX_LINE_BYTES) { skipping = true; buffer = ''; }
  });
  child.once('error', () => {
    spec.log.warn('codex_watch_unreachable', spec.context);
    fenced = true; listeners.clear(); settle({ kind: 'reconnect' });
  });
  // 'close' follows the end of stdout, so the stop line has been read.
  child.once('close', () => {
    fenced = true; listeners.clear();
    settle(terminalStatus !== undefined ? { kind: 'terminal', status: terminalStatus } : { kind: 'reconnect' });
  });
  return {
    // Synchronously fence the event callback, then request termination.
    close() { fenced = true; listeners.clear(); terminateProcess(child); },
    ended,
    onEvent(f) { listeners.add(f); return () => { listeners.delete(f); }; },
  };
}

type Subscriber = { notify: (event: WakeEvent) => void; stopped: (end: WatchEnd) => void };
type Entry = { key: string; revision: string; child: WatchChild; subscribers: Set<Subscriber> };

/** One child per verified (scope, credential revision). Subscribers with an
 * identical verified scope and revision share a child; any scope component or
 * revision difference isolates. Rotation within a scope replaces the child and
 * detaches old-revision subscribers (they re-verify and may rejoin); late
 * events from a retired source reach nobody. The pool never respawns on its
 * own, and close() waits for every owned child to exit. */
export function createWatchPool(deps: WatchPoolDeps): WatchPool {
  const log = deps.log;
  const entries = new Map<string, Entry>();
  let closed = false, closing: Promise<void> | undefined;
  const keyOf = (scope: WatchScope) => JSON.stringify([scope.hubIdentity, scope.endpoint, scope.streamOwner, scope.credentialSlot, scope.scope]);
  const detach = (entry: Entry, end: WatchEnd) => {
    const subscribers = [...entry.subscribers];
    entry.subscribers.clear();
    for (const s of subscribers) s.stopped(end);
  };
  /** Retire an entry: no later event or end from its child reaches anyone. */
  const retire = (entry: Entry) => {
    entries.delete(entry.key);
    entry.subscribers.clear();
    entry.child.close();
  };
  return {
    subscribe(request, notify, stopped) {
      if (closed) return () => { };
      const key = keyOf(request.scope);
      const existing = entries.get(key);
      if (existing && existing.revision !== request.revision) {
        // A verified credential revision change replaces the scope's child.
        // Subscribers pinned to the old revision detach rather than migrate
        // onto a child opened under credentials they have not verified.
        const detached = [...existing.subscribers];
        retire(existing);
        for (const s of detached) s.stopped({ kind: 'reconnect' });
      }
      let entry = entries.get(key);
      if (!entry) {
        let child: WatchChild;
        try { child = request.start(); }
        catch {
          log.warn('delivery_watch_start_failed');
          return () => { };
        }
        entry = { key, revision: request.revision, child, subscribers: new Set() };
        entries.set(key, entry);
        // Fence by source: only the scope's current child delivers events.
        child.onEvent(event => { if (entries.get(key) === entry) for (const s of [...entry!.subscribers]) s.notify(event); });
        void child.ended.then(end => {
          if (entries.get(key) !== entry) return; // Replaced or already retired.
          entries.delete(key);
          detach(entry!, end);
        });
      }
      const subscriber: Subscriber = { notify, stopped };
      entry.subscribers.add(subscriber);
      let active = true;
      return () => {
        if (!active) return;
        active = false;
        const current = entries.get(key);
        if (current?.subscribers.delete(subscriber) && current.subscribers.size === 0) retire(current);
      };
    },
    close(): Promise<void> {
      if (closing) return closing;
      closed = true;
      closing = (async () => {
        const drained: { subscribers: Subscriber[]; ended: Promise<WatchEnd> }[] = [];
        for (const entry of [...entries.values()]) {
          const subscribers = [...entry.subscribers];
          entries.delete(entry.key);
          entry.subscribers.clear();
          entry.child.close();
          drained.push({ subscribers, ended: entry.child.ended });
        }
        for (const { subscribers, ended } of drained) {
          const end = await ended;
          for (const s of subscribers) s.stopped(end);
        }
      })();
      return closing;
    },
  };
}
