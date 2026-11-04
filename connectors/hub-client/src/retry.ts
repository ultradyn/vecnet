import { HubError } from './errors.ts';
import type { Clock } from './ports.ts';

export async function retryHub<T>(fn: () => Promise<T>, opts: { attempts: number; clock: Clock; signal?: AbortSignal; baseMs?: number; maxWaitMs?: number }): Promise<T> {
  if (!Number.isInteger(opts.attempts) || opts.attempts < 1) throw new RangeError('attempts must be positive');
  const maxWait = opts.maxWaitMs ?? 60_000;
  for (let n = 0; ; n++) {
    opts.signal?.throwIfAborted();
    try { return await fn(); }
    catch (err) {
      if (!(err instanceof HubError) || !err.retryable || n + 1 >= opts.attempts || (err.retryAfterMs ?? 0) > maxWait) throw err;
      // Jitter the exponential term, never the server's minimum Retry-After.
      const exponential = Math.min(maxWait, (opts.baseMs ?? 250) * 2 ** n);
      const wait = Math.max(err.retryAfterMs ?? 0, exponential * (0.5 + Math.random() * 0.5));
      await opts.clock.sleep(wait, opts.signal);
    }
  }
}
