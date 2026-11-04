import { HubError } from '../../hub-client/src/errors.ts';
import { retryHub } from '../../hub-client/src/retry.ts';
import type { Clock, HubApi, Log } from '../../hub-client/src/ports.ts';
import type { ReceiptBody, SendRequest } from '../../hub-client/src/wire.ts';
import type { Ledger, OutboxKind, OutboxRow } from './ledger.ts';

export interface Outbox {
  enqueue(kind: OutboxKind, key: string, payload: SendRequest | ReceiptBody, tag: string): void;
  kick(): void; run(signal: AbortSignal): Promise<void>;
  onSent(fn: (row: OutboxRow, res: unknown) => void): void;
  onRejected(fn: (row: OutboxRow, err: HubError) => void): void;
}

export function createOutbox(deps: { ledger: Ledger; hub: Pick<HubApi, 'send' | 'receipt'>; clock: Clock; log: Log }): Outbox {
  const { ledger, hub, clock, log } = deps;
  const sent: Array<(row: OutboxRow, res: unknown) => void> = [];
  const rejected: Array<(row: OutboxRow, err: HubError) => void> = [];
  let running: Promise<void> | undefined;
  let wake: (() => void) | undefined;
  function kick() { wake?.(); }
  function wait(ms: number | undefined, signal: AbortSignal): Promise<void> {
    return new Promise(resolve => {
      let alarm: { cancel(): void } | undefined;
      function done() {
        alarm?.cancel(); signal.removeEventListener('abort', done);
        if (wake === done) wake = undefined;
        resolve();
      }
      wake = done;
      signal.addEventListener('abort', done, { once: true });
      if (signal.aborted) done();
      else if (ms !== undefined) alarm = clock.timer(ms, done);
    });
  }
  function resequence(row: OutboxRow, err: HubError): boolean {
    const payload = row.payload as SendRequest, status = payload.job_status;
    const last = err.details.last_sequence;
    if (row.kind !== 'send' || payload.kind !== 'job_status' || !status || !Number.isSafeInteger(last) || (last as number) < status.sequence || (last as number) >= Number.MAX_SAFE_INTEGER) return false;
    const sequence = (last as number) + 1;
    // Re-number queued reports as one transaction: the new head key may already
    // belong to the next report. Replace from the tail to free those keys first.
    ledger.tx(() => {
      const job = ledger.job(status.job_id);
      if (!job) throw new Error('outbox job not found');
      let reserved = sequence;
      const replacements = [{ row, payload, sequence }];
      for (const later of ledger.pendingOutbox(Number.MAX_SAFE_INTEGER, Number.MAX_SAFE_INTEGER)) {
        const p = later.payload as SendRequest;
        if (later.id <= row.id || later.kind !== 'send' || p.kind !== 'job_status' || p.job_status?.job_id !== status.job_id) continue;
        reserved = Math.max(reserved + 1, p.job_status.sequence);
        replacements.push({ row: later, payload: p, sequence: reserved });
      }
      for (const replacement of replacements.reverse()) {
        const key = `vl.js.${status.job_id}.${replacement.sequence}`;
        ledger.replaceOutbox(replacement.row.id, key, {
          ...replacement.payload, idempotency_key: key,
          job_status: { ...replacement.payload.job_status!, sequence: replacement.sequence },
        });
      }
      ledger.setStatusSeq(status.job_id, Math.max(job.statusSeq, reserved));
    });
    return true;
  }
  async function drain(signal: AbortSignal): Promise<void> {
    while (!signal.aborted) {
      // Read the oldest pending row even if deferred, so a newer ready row cannot overtake it.
      const row = ledger.pendingOutbox(1, Number.MAX_SAFE_INTEGER)[0];
      if (!row) { await wait(undefined, signal); continue; }
      if (row.notBefore > clock.now()) { await wait(row.notBefore - clock.now(), signal); continue; }
      let result: unknown;
      try {
        result = await retryHub<unknown>(() => row.kind === 'send'
          ? hub.send(row.payload as SendRequest, signal)
          : hub.receipt(row.payload as ReceiptBody, signal), { attempts: 8, clock, signal, maxWaitMs: 60_000 });
      } catch (err) {
        if (signal.aborted) return;
        if (err instanceof HubError) {
          if (err.code === 'stale_sequence' && err.status === 409 && resequence(row, err)) continue;
          if (err.retryable) {
            ledger.deferOutbox(row.id, clock.now() + Math.max(1000, err.retryAfterMs ?? 1000));
            continue;
          }
          if (err.status >= 400 && err.status < 500) {
            ledger.tx(() => {
              ledger.markOutbox(row.id, 'failed');
              if (err.status === 409 && err.code === 'idempotency_conflict') log.error('outbox key collision', { key: row.key, tag: row.tag });
              else log.warn('outbox rejected', { code: err.code, tag: row.tag });
              for (const fn of rejected) fn({ ...row, state: 'failed', attempts: row.attempts + 1 }, err);
            });
            continue;
          }
        }
        // Unknown transport failures remain durable; never expose exception messages or request bodies.
        log.warn('outbox retry deferred', { tag: row.tag });
        ledger.deferOutbox(row.id, clock.now() + 60_000);
        continue;
      }
      ledger.tx(() => {
        ledger.markOutbox(row.id, 'sent', result);
        for (const fn of sent) fn({ ...row, state: 'sent', result, attempts: row.attempts + 1 }, result);
      });
    }
  }
  return {
    enqueue(kind, key, payload, tag) { ledger.enqueue(kind, key, payload, tag); kick(); },
    kick,
    run(signal) {
      if (!running) running = drain(signal).finally(() => { running = undefined; });
      return running;
    },
    onSent(fn) { sent.push(fn); },
    onRejected(fn) { rejected.push(fn); },
  };
}
