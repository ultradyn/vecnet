import { spawn } from 'node:child_process';
import type { Clock, Log } from '../../../hub-client/src/ports.ts';
import type { Binding } from './engine.ts';
import { ownerArgs, ownerOf, validOwner } from './ownership.ts';
import { cliEnvironment, commandInvocation, terminateProcess } from '../../../hub-client/src/platform.ts';

/** Public, bounded metadata only. Never serialize a binding or an RPC error. */
export interface RuntimeSnapshot {
  state: 'idle' | 'working' | 'blocked' | 'offline';
  delivery: 'ready' | 'unavailable' | 'unsupported';
  reason?: string;
  version?: string;
}
export type RuntimeStatusPost = (binding: Binding, snapshot: RuntimeSnapshot, signal: AbortSignal) => Promise<boolean>;
type Entry = { binding: Binding; engine: { runtimeStatus(): RuntimeSnapshot } };
type Slot = { entry: Entry; pending: boolean; active?: AbortController; sentSignature?: string; signature?: string; reportedAt: number; failed: boolean };
const offline: RuntimeSnapshot = { state: 'offline', delivery: 'unavailable', reason: 'stopped' };

/** The credential-holding CLI validates exact ownership under the handoff lock.
 * Stdin is a closed shape; neither subprocess output nor errors are logged. */
export function runtimeStatusPost(bin: string, sourceEnv: Record<string, string>): RuntimeStatusPost {
  const env = cliEnvironment(sourceEnv);
  return (binding, snapshot, signal) => new Promise(resolve => {
    if (signal.aborted || !validOwner(ownerOf(binding))) { resolve(false); return; }
    const body = JSON.stringify({ state: snapshot.state, delivery: snapshot.delivery, reason: snapshot.reason, version: snapshot.version });
    const command = commandInvocation([bin, '--net', binding.profile, ...(binding.as ? ['--as', binding.as] : []),
      'codex', 'report-status', ...ownerArgs(binding)], env);
    const child = spawn(command.file, command.args,
    { ...command, windowsHide: true, env: { ...env, VECNET_CONFIG: binding.configPath! }, stdio: ['pipe', 'ignore', 'ignore'] });
    let settled = false;
    const kill = () => { terminateProcess(child); };
    const timeout = setTimeout(kill, 4000);
    const finish = (ok: boolean) => {
      if (settled) return;
      settled = true; clearTimeout(timeout); signal.removeEventListener('abort', kill); resolve(ok && !signal.aborted);
    };
    child.once('error', () => finish(false));
    child.once('close', code => finish(code === 0));
    child.stdin.on('error', () => {});
    signal.addEventListener('abort', kill, { once: true });
    // Close the spawn/listener race if cancellation happened synchronously.
    if (signal.aborted) kill();
    child.stdin.end(body);
  });
}

/** One coalesced slot per owner, at most two children, no timer-driven retry
 * loop. The connector's ordinary reload supplies changes and 30s heartbeats. */
export function createRuntimeStatusReporter(o: { clock: Clock; log: Log; post: RuntimeStatusPost }) {
  const slots = new Map<string, Slot>();
  let active = 0, stopping = false, stopped = false, fenced = false, stopPromise: Promise<void> | undefined;
  let drained: (() => void) | undefined;
  function pump() {
    if (stopped) return;
    for (const [key, slot] of slots) {
      if (active >= 2) break;
      if (!slot.pending || slot.active) continue;
      if (slot.entry.binding.expiresAt <= o.clock.now()) { slots.delete(key); continue; }
      slot.pending = false;
      const snapshot = stopping ? { ...offline, version: slot.entry.engine.runtimeStatus().version } : slot.entry.engine.runtimeStatus();
      const signature = JSON.stringify(snapshot), controller = new AbortController();
      slot.active = controller; slot.sentSignature = signature; active++;
      // Defer invocation so even a throwing adapter cannot break local reloads.
      void Promise.resolve().then(() => controller.signal.aborted ? false : o.post(slot.entry.binding, snapshot, controller.signal))
        .catch(() => false).then(ok => {
          if (slots.get(key) !== slot || controller.signal.aborted) return;
          if (ok) { slot.signature = signature; slot.reportedAt = o.clock.now(); slot.failed = false; }
          else if (!slot.failed) { slot.failed = true; o.log.warn('codex_runtime_status_failed'); }
        }).finally(() => { slot.active = undefined; active--; pump(); });
    }
    if (stopping && active === 0 && ![...slots.values()].some(s => s.pending)) drained?.();
  }
  return {
    sync(entries: Iterable<Entry>) {
      if (stopping || fenced) return;
      const wanted = new Set<string>(), now = o.clock.now();
      for (const entry of entries) {
        if (entry.binding.expiresAt <= now || !validOwner(ownerOf(entry.binding))) continue;
        const key = JSON.stringify(ownerOf(entry.binding)); wanted.add(key);
        let slot = slots.get(key);
        if (!slot) { slot = { entry, pending: false, reportedAt: -Infinity, failed: false }; slots.set(key, slot); }
        slot.entry = entry;
        const signature = JSON.stringify(entry.engine.runtimeStatus());
        slot.pending = slot.active ? slot.sentSignature !== signature : slot.signature !== signature || now - slot.reportedAt >= 30_000;
      }
      for (const [key, slot] of slots) if (!wanted.has(key)) { slots.delete(key); slot.active?.abort(); }
      pump();
    },
    /** Singleton loss revokes ALL writes, including an already-started offline
     * shutdown. Abort adapters synchronously and drain them in stop(). */
    fence(): void {
      fenced = true;
      for (const slot of slots.values()) slot.active?.abort();
      slots.clear(); pump();
    },
    stop(): Promise<void> {
      if (stopPromise) return stopPromise;
      stopping = true;
      stopPromise = new Promise(resolve => {
        let finished = false;
        const finish = () => {
          if (finished) return;
          finished = true; stopped = true; deadline.cancel();
          for (const slot of slots.values()) slot.active?.abort();
          slots.clear(); resolve();
        };
        const deadline = o.clock.timer(5000, finish);
        drained = finish;
        for (const slot of slots.values()) { slot.active?.abort(); slot.pending = true; }
        pump();
      });
      return stopPromise;
    },
  };
}
