import { readFileSync } from 'node:fs';
import type { Clock, Log } from '../src/ports.ts';
import type { Delivery, VecnetEvent, EventType, DirectoryEntry } from '../src/wire.ts';

type Timer = { at: number; order: number; fn: () => void };
export interface FakeClock extends Clock { advance(ms: number): void }
export function fakeClock(startMs = 0): FakeClock {
  let now = startMs;
  let order = 0;
  const pending = new Set<Timer>();
  function timer(ms: number, fn: () => void) {
    if (!Number.isFinite(ms)) throw new RangeError('invalid timer delay');
    const entry = { at: now + Math.max(0, ms), order: order++, fn };
    pending.add(entry);
    return { cancel() { pending.delete(entry); } };
  }
  return {
    now: () => now, timer,
    sleep(ms, signal) {
      return new Promise<void>((resolve, reject) => {
        if (signal?.aborted) { reject(signal.reason); return; }
        const alarm = timer(ms, () => { signal?.removeEventListener('abort', abort); resolve(); });
        function abort() { alarm.cancel(); signal?.removeEventListener('abort', abort); reject(signal?.reason); }
        signal?.addEventListener('abort', abort, { once: true });
      });
    },
    advance(ms) {
      if (!Number.isFinite(ms) || ms < 0) throw new RangeError('invalid clock advance');
      const target = now + ms;
      while (true) {
        const due = [...pending].filter(t => t.at <= target).sort((a,b) => a.at - b.at || a.order - b.order)[0];
        if (!due) break;
        pending.delete(due); now = due.at; due.fn();
      }
      now = target;
    },
  };
}

export interface LogEntry { level: 'info' | 'warn' | 'error'; msg: string; meta?: Record<string, unknown> }
export function memoryLog(): Log & { entries: LogEntry[]; text(): string } {
  const entries: LogEntry[] = [];
  return {
    entries,
    info(msg, meta) { entries.push({level:'info',msg,meta}); },
    warn(msg, meta) { entries.push({level:'warn',msg,meta}); },
    error(msg, meta) { entries.push({level:'error',msg,meta}); },
    text: () => entries.map(e => `${e.level}: ${e.msg}${e.meta ? ` ${JSON.stringify(e.meta)}` : ''}`).join('\n'),
  };
}
export function delivery(overrides: Partial<Delivery> = {}): Delivery {
  const fixture: Delivery = JSON.parse(readFileSync(new URL('../../../docs/protocol/examples/02-delivery-direct.json', import.meta.url), 'utf8'));
  return { ...fixture, ...overrides };
}
export function event(type: EventType, data: unknown, cursor = 'e1.1'): VecnetEvent {
  return { cursor, type, data, at: new Date(0).toISOString() };
}
export function directoryEntry(overrides: Partial<DirectoryEntry> = {}): DirectoryEntry {
  return {peer_id:'peer_01JD3Q2K8M4N6P8R0T2V4X6Z8A',name:'test peer',kind:'session',capabilities:[],quota:{},location:null,presence:'idle',reachability:'online',last_seen:null,enrolled_at:null,teams:[],...overrides};
}
