import type { JobStatusBody } from '../../hub-client/src/wire.ts';
import type { Ledger, Terminal } from './ledger.ts';
import type { Outbox } from './outbox.ts';
import { redactSecrets, redactSecretsIn } from './redact.ts';

export interface JobReporter {
  report(jobId: string, state: JobStatusBody['state'], output?: Record<string, unknown>, text?: string): void;
}
export function createJobReporter(deps: { ledger: Ledger; outbox: Outbox }): JobReporter {
  const { ledger, outbox } = deps;
  return {
    report(jobId, state, output, text) {
      ledger.tx(() => {
        const job = ledger.job(jobId);
        if (!job) throw new Error('ledger job not found');
        if (job.terminal !== null) return;
        // A final safeguard: no report carries a credential, whatever built it.
        let bounded: Record<string, unknown> | undefined = output === undefined ? undefined : redactSecretsIn(output);
        if (output !== undefined) {
          const json = JSON.stringify(bounded);
          if (Buffer.byteLength(json) > 8192) {
            let summary = '';
            for (const char of json) {
              if (Buffer.byteLength(summary) + Buffer.byteLength(char) > 1024) break;
              summary += char;
            }
            bounded = { output_truncated: true, summary };
          }
        }
        const sequence = ledger.nextStatusSeq(jobId), key = `vl.js.${jobId}.${sequence}`;
        const terminal = state === 'succeeded' || state === 'failed' || state === 'cancelled';
        outbox.enqueue('send', key, {
          idempotency_key: key, kind: 'job_status', thread_id: job.threadId, in_reply_to: job.launchMessageId,
          priority: terminal ? 'wake_idle' : 'hold', content: { text: redactSecrets(text ?? state) },
          job_status: { job_id: jobId, sequence, state, ...(bounded === undefined ? {} : { output: bounded }) },
        }, jobId);
        if (terminal) {
          // One terminal finalization for every path that ends a job (lane
          // settle, restart loss, failed launch): inputs still buffered or in
          // flight are failed with session_ended in the same commit.
          const pending = ledger.inputs(jobId, ['buffered', 'sending', 'sent']);
          for (const row of pending) {
            outbox.enqueue('receipt', `vl.rc.${row.deliveryId}.failed`, { delivery_id: row.deliveryId, state: 'failed', note: 'session_ended' }, jobId);
          }
          ledger.markInputs(pending.map(r => r.deliveryId), { state: 'failed' });
          // updatedAt is supplied by the ledger's Clock port, keeping this interface clock-free.
          ledger.updateJob(jobId, { terminal: state as Terminal, state: 'ended' });
          ledger.updateJob(jobId, { endedAt: ledger.job(jobId)!.updatedAt });
        }
      });
    },
  };
}
