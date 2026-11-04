import { createHash } from 'node:crypto';
import type { Clock, HubApi, Log } from '../../hub-client/src/ports.ts';
import type { Delivery, SendRequest } from '../../hub-client/src/wire.ts';
import { MAX_TURN_INPUT_BYTES } from './backend.ts';
import type { ActivityReport, BackendCapabilities, SessionHandle, SettleMode, SettleResult, SendMode, TurnEvent, TurnInput } from './backend.ts';
import type { Directory } from './directory.ts';
import type { Ledger, JobRow, InputRow } from './ledger.ts';
import type { Outbox } from './outbox.ts';
import type { JobReporter } from './jobs.ts';
import { planInput } from './input-plan.ts';
import { formatRelayed, relayDecision } from './relay.ts';
import { redactText } from './redact.ts';

export type EndReason = 'idle_wrapup' | 'settled' | 'deadline' | 'requester_retired' | 'expired';
export interface LaneDeps {
  ledger: Ledger; outbox: Outbox; jobs: JobReporter; dir: Pick<Directory, 'name' | 'isOperator' | 'operatorPeerId'>;
  hub: Pick<HubApi, 'fetchBlob' | 'putBlob'>; clock: Clock; log: Log; self: string; caps: BackendCapabilities;
  deliver(job: JobRow, r: SettleResult): Promise<{ pushed: boolean }>;
  onActivity(jobId: string): void; onEnded(jobId: string): void;
}
export interface SessionLane {
  readonly jobId: string;
  attach(handle: SessionHandle): void;
  handleMessage(d: Delivery): Promise<'relayed' | 'buffered' | 'not_relayed' | 'refused'>;
  control(c: 'interrupt' | 'settle' | 'extend', from: string): Promise<void>;
  cancel(by: string): Promise<void>;
  park(): Promise<void>; settle(mode: SettleMode, ended: EndReason, extra?: Record<string, unknown>): Promise<void>;
  activity(): Promise<ActivityReport & { launcherBuffered: number; lastRelayAt: number }>;
  stop(): Promise<void>;
}
const hash = (s: string) => createHash('sha256').update(s).digest('hex').slice(0, 24);
function prefix(s: string, bytes: number): string {
  const data = Buffer.from(s);
  let end = Math.max(0, Math.min(bytes, data.length));
  while (end > 0 && end < data.length && (data[end] & 0xc0) === 0x80) end--;
  return data.subarray(0, end).toString('utf8');
}
export function turnReply(job: JobRow, n: number, ev: Extract<TurnEvent, { type: 'turn_finished' }>, consumed: string[], redact: typeof redactText): { req: SendRequest; fullText?: string } {
  const full = redact(ev.finalText).text;
  const size = Buffer.byteLength(full), cap = 60 * 1024;
  const marker = `\n[truncated: ${size} bytes; full text attached]`;
  const key = `vl.tr.${job.jobId}.${hash(ev.turnId)}`;
  return {
    req: { idempotency_key: key, kind: 'message', thread_id: job.threadId, in_reply_to: job.launchMessageId,
      priority: job.input.reply_priority ?? 'wake_idle', content: { text: size > cap ? prefix(full, cap - Buffer.byteLength(marker)) + marker : full },
      metadata: { 'launch.turn': ev.outcome, 'launch.turn_id': ev.turnId, 'launch.job_id': job.jobId,
        'launch.consumed': consumed.slice(0, 100), ...(consumed.length > 100 ? { 'launch.consumed_truncated': consumed.length - 100 } : {}),
        ...(job.backend === 'paseo' ? { 'launch.final_text': 'best_effort' } : {}),
      },
    }, ...(size > cap ? { fullText: full } : {}),
  };
}

export function createLane(initial: JobRow, deps: LaneDeps): SessionLane {
  const { ledger, outbox, clock, log } = deps;
  const jobId = initial.jobId;
  const store = ledger.kv(`lane:${jobId}`);
  let handle: SessionHandle | undefined;
  let chain: Promise<unknown> = Promise.resolve();
  let stopped = false;
  let controller: AbortController | undefined;
  let pump: Promise<void> | undefined;
  const job = () => ledger.job(jobId)!;
  function serial<T>(fn: () => Promise<T> | T): Promise<T> {
    const work = chain.then(fn).catch(() => {
      log.warn('session lane operation failed', { jobId });
      throw new Error('session lane operation failed');
    });
    chain = work.catch(() => {});
    return work;
  }
  const authorized = (from: string) => from === deps.self || from === job().requester || deps.dir.isOperator(from);
  function receipt(id: string, state: 'processed' | 'failed', note?: string) {
    outbox.enqueue('receipt', `vl.rc.${id}.${state}`, { delivery_id: id, state, ...(note ? { note } : {}) }, jobId);
  }
  function activity() {
    ledger.updateJob(jobId, { lastActivityAt: clock.now() });
    deps.onActivity(jobId);
  }
  function pending() { return ledger.inputs(jobId, ['buffered', 'sending', 'sent']); }
  const cap = Math.min(MAX_TURN_INPUT_BYTES, deps.caps.maxInputBytes ?? Infinity);
  if (!Number.isSafeInteger(cap) || cap < 4) throw new Error('invalid backend input cap');
  type StoredBatch = { input: TurnInput & { batchId: string }; parts: Record<string, number> };
  function parts(row: InputRow): string[] | undefined {
    const saved = store.get(`parts:${row.deliveryId}`);
    return saved === undefined ? undefined : JSON.parse(saved) as string[];
  }
  function position(id: string) { return Number(store.get(`part:${id}`) ?? 0); }
  function inputItem(row: InputRow): TurnInput['items'][number] {
    const chunks = parts(row), part = position(row.deliveryId);
    return { deliveryId: chunks ? `${row.deliveryId}.part.${part + 1}` : row.deliveryId,
      messageId: row.messageId, from: row.sender, text: chunks ? chunks[part] : row.text, attachments: [] };
  }
  async function sendBatch(rows: InputRow[], mode: SendMode, replay = false) {
    if (!handle || rows.length === 0 || stopped) return;
    const ids = rows.map(r => r.deliveryId);
    const items = rows.map(inputItem);
    const batchId = replay ? rows[0].batchId! : hash(items.map(i => i.deliveryId).sort().join('\n'));
    // Persist the actual ordered payload: ledger insertion order differs from
    // send order when holds have been prepended. Fragment ids stay backend-local;
    // hub metadata and receipts always use the original opaque hub ids.
    const saved = replay ? store.get(`batch:${batchId}`) : undefined;
    const batch: StoredBatch = saved ? JSON.parse(saved) as StoredBatch : {
      input: { batchId, items }, parts: Object.fromEntries(rows.filter(r => parts(r)).map(r => [r.deliveryId, position(r.deliveryId) + 1])),
    };
    if (!replay) ledger.tx(() => {
      ledger.markInputs(ids, { state: 'sending', mode, batchId, turnSeq: job().turns });
      store.set(`batch:${batchId}`, JSON.stringify(batch));
    });
    // The optional structural field is consumed by t3's existing T3TurnInput seam.
    const input = batch.input;
    await handle.send(input, mode);
    ledger.tx(() => {
      ledger.markInputs(ids, { state: 'sent' });
      for (const [id, part] of Object.entries(batch.parts)) store.set(`part:${id}`, String(part));
      if (mode === 'start') ledger.updateJob(jobId, { sessionKind: 'turn_running', parkedAt: null });
    });
  }
  async function flush(mode: SendMode) {
    const buffered = ledger.inputs(jobId, ['buffered']);
    // Holds may join a send, but can never initiate one by themselves.
    if (!buffered.some(r => r.priority !== 'hold')) return;
    const ordered = [...buffered.filter(r => r.priority === 'hold'), ...buffered.filter(r => r.priority !== 'hold')];
    const selected: InputRow[] = [];
    let bytes = 0;
    for (const row of ordered) {
      const size = Buffer.byteLength(inputItem(row).text) + (selected.length ? 2 : 0);
      if (bytes + size > cap) break;
      bytes += size; selected.push(row);
    }
    if (selected.length) await sendBatch(selected, mode);
  }
  async function end(mode: SettleMode, ended: EndReason | 'cancel' | 'session_exited', extra: Record<string, unknown> = {}) {
    if (job().state === 'ended') return;
    ledger.updateJob(jobId, { state: 'settling' });
    const result = handle ? await handle.settle(mode) : { workspaceKept: true };
    let pushed = false;
    if (job().input.deliver?.push_branch && !['cancel', 'expired', 'requester_retired', 'session_exited'].includes(ended)) pushed = (await deps.deliver(job(), result)).pushed;
    ledger.tx(() => {
      const inputs = pending();
      for (const row of inputs) receipt(row.deliveryId, 'failed', 'session_ended');
      ledger.markInputs(inputs.map(r => r.deliveryId), { state: 'failed' });
      ledger.updateJob(jobId, { sessionKind: 'ended' });
      if (ended === 'expired' || ended === 'session_exited') deps.jobs.report(jobId, 'failed', { error: ended });
      else if (ended === 'requester_retired') deps.jobs.report(jobId, 'cancelled', { reason: ended });
      else if (ended === 'cancel') deps.jobs.report(jobId, 'cancelled', { branch: result.branch ?? job().branch, head_sha: result.headSha, workspace_kept: result.workspaceKept });
      else deps.jobs.report(jobId, 'succeeded', {
        ...extra, ended, turns: job().turns, branch: result.branch ?? job().branch, head_sha: result.headSha,
        commits: result.commits ?? [], pushed, summary: redactText(result.summary ?? '').text,
        ...(ended === 'idle_wrapup' ? { quiescence: extra.quiescence ?? (deps.caps.backgroundTasks === 'observable' ? 'proven' : 'assumed') } : {}),
      });
    });
    controller?.abort(); deps.onEnded(jobId);
  }
  async function control(c: 'interrupt' | 'settle' | 'extend', from: string) {
    if (!authorized(from) || stopped || job().state === 'ended') return;
    if (c === 'interrupt') await handle?.interrupt('requested');
    else if (c === 'settle') await end('requested', 'settled');
    else activity();
  }
  async function event(ev: TurnEvent, cursor: string) {
    if (stopped || job().state === 'ended') return;
    if (ev.type === 'turn_finished') {
      const current = job(), n = current.turns + 1;
      const inputs = ledger.inputs(jobId, ['sent']).filter(r => r.turnSeq !== null && (r.mode === 'queue' ? r.turnSeq < n - 1 : r.turnSeq <= n - 1));
      const continuing = inputs.filter(r => { const chunks = parts(r); return chunks && position(r.deliveryId) < chunks.length; });
      const consumed = inputs.filter(r => !continuing.includes(r));
      const ids = consumed.map(r => r.deliveryId);
      const reply = turnReply(current, n, ev, consumed.map(r => r.messageId), redactText);
      // Upload outside the synchronous transaction. A crash here may orphan a
      // content-addressed blob, but can never publish a reply without its turn row.
      if (!store.get(`turn:${hash(ev.turnId)}`) && reply.fullText !== undefined) {
        try {
          const blob = await deps.hub.putBlob(Buffer.from(reply.fullText), 'text/markdown', controller?.signal);
          reply.req.attachments = [{ ...blob, media_type: 'text/markdown', name: `turn-${n}.md` }];
        } catch { log.warn('turn text attachment upload failed', { jobId }); }
      }
      let fresh = false;
      ledger.tx(() => {
        fresh = ledger.recordTurn(jobId, ev.turnId, ev.outcome);
        if (fresh) {
          outbox.enqueue('send', reply.req.idempotency_key, reply.req, jobId);
          for (const id of ids) receipt(id, 'processed');
          ledger.markInputs(ids, { state: 'consumed' });
          ledger.markInputs(continuing.map(r => r.deliveryId), { state: 'buffered', mode: null, batchId: null, turnSeq: null });
          store.set(`turn:${hash(ev.turnId)}`, String(n));
          ledger.updateJob(jobId, { turns: n, lastActivityAt: clock.now(), sessionKind: ledger.inputs(jobId, ['sent']).some(r => r.mode === 'queue') ? 'turn_running' : 'idle' });
        }
        ledger.updateJob(jobId, { turnCursor: cursor });
      });
      if (fresh) {
        if (JSON.stringify([...ev.consumed].sort()) !== JSON.stringify([...ids].sort())) log.warn('turn consumed mismatch', { jobId, turn: n });
        deps.onActivity(jobId);
        if (job().sessionKind === 'idle') await flush('start');
      }
      return;
    }
    if (ev.type === 'process_exited') {
      if (ev.resumable) ledger.updateJob(jobId, { turnCursor: cursor, sessionKind: 'parked', parkedAt: clock.now() });
      else {
        await end('lost', 'session_exited');
        // A failed settle must leave this event replayable after a restart.
        ledger.updateJob(jobId, { turnCursor: cursor });
      }
      return;
    }
    ledger.tx(() => {
      ledger.updateJob(jobId, { turnCursor: cursor,
        ...(ev.type === 'state' ? { sessionKind: ev.state.kind } : ev.type === 'turn_started' ? { sessionKind: 'turn_running' as const } : {}),
      });
    });
  }
  const lane: SessionLane = {
    jobId,
    attach(next) {
      if (stopped || job().state === 'ended') return;
      if (handle) throw new Error('session lane already attached');
      handle = next;
      controller = new AbortController();
      const signal = controller.signal;
      void serial(async () => {
        const state = next.state();
        ledger.updateJob(jobId, { state: 'running', sessionKind: state.kind === 'ended' && state.resumable ? 'parked' : state.kind });
        const sending = ledger.inputs(jobId, ['sending']);
        const batches = new Map<string, InputRow[]>();
        for (const row of sending) { const key = row.batchId!; batches.set(key, [...(batches.get(key) ?? []), row]); }
        for (const rows of batches.values()) await sendBatch(rows, rows[0].mode!, true);
        if (['idle', 'parked'].includes(job().sessionKind!)) await flush('start');
        else if (ledger.inputs(jobId, ['buffered']).some(r => r.priority !== 'hold')) {
          const p = planInput('wake_idle', job().sessionKind!, deps.caps);
          if (p === 'queue-native') await flush('queue');
        }
        if (stopped) return;
        pump = (async () => {
          for await (const item of next.turns(job().turnCursor ? { value: job().turnCursor! } : undefined, signal)) {
            if (signal.aborted) break;
            await serial(() => event(item.event, item.cursor.value));
          }
        })().catch(() => { if (!signal.aborted) log.warn('session turn stream failed', { jobId }); });
      }).catch(() => {});
    },
    handleMessage(d) {
      return serial(async () => {
        const m = d.message;
        const decision = relayDecision(m.sender, job(), deps.self, id => deps.dir.isOperator(id));
        if (!decision.relay || m.thread_id !== job().threadId) { receipt(d.delivery_id, 'processed', 'not_relayed'); return 'not_relayed'; }
        if (stopped || job().state === 'ended' || job().sessionKind === 'ended') { receipt(d.delivery_id, 'failed', 'session_ended'); return 'refused'; }
        const command = m.metadata['launch.control'];
        if (command === 'interrupt' || command === 'settle' || command === 'extend') {
          const allowed = authorized(m.sender);
          await control(command, m.sender); receipt(d.delivery_id, 'processed', allowed ? undefined : 'not_relayed');
          return allowed ? 'relayed' : 'not_relayed';
        }
        // Structured parent-bridge traffic is consumed by Task 12 upstream.
        if (m.kind !== 'message' || m.job_request || m.metadata['launch.answers'] || m.metadata['launch.decision']) { receipt(d.delivery_id, 'processed', 'not_relayed'); return 'not_relayed'; }
        const existing = ledger.inputs(jobId, ['buffered', 'sending', 'sent', 'consumed', 'failed']).find(r => r.deliveryId === d.delivery_id);
        if (existing) return existing.state === 'buffered' ? 'buffered' : 'relayed';
        let text = m.content.text;
        let attachments = m.attachments;
        if (m.content.truncated) {
          // The hub appends its spill after user attachments; prefer the last
          // matching name so a requester cannot shadow it with a same-name file.
          const spill = attachments.findLast(a => a.name === 'message.md' && a.media_type === 'text/markdown');
          if (!spill?.blob_url) throw new Error('spilled input attachment missing');
          const bytes = await deps.hub.fetchBlob(spill.blob_url, { maxBytes: 256 * 1024, signal: controller?.signal });
          if (bytes.length > 256 * 1024) throw new Error('spilled input exceeds limit');
          text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
          attachments = attachments.filter(a => a !== spill);
        }
        const formatted = formatRelayed({ role: decision.role, name: m.sender_info?.name ?? deps.dir.name(m.sender), peerId: m.sender, messageId: m.message_id, text, attachments });
        const chunks: string[] = [];
        let remaining = formatted;
        while (Buffer.byteLength(remaining) > cap) {
          const chunk = prefix(remaining, cap);
          if (chunk.length === 0) throw new Error('backend input cap too small');
          chunks.push(chunk);
          // Keep a fragment that starts inside a line visibly quoted as data.
          remaining = `> ${remaining.slice(chunk.length)}`;
        }
        if (chunks.length) chunks.push(remaining);
        ledger.tx(() => {
          ledger.addInput({ deliveryId: d.delivery_id, jobId, messageId: m.message_id, sender: m.sender, text: formatted, priority: m.priority, createdAt: clock.now() });
          if (chunks.length) store.set(`parts:${d.delivery_id}`, JSON.stringify(chunks));
          store.set('lastRelayAt', String(clock.now()));
          ledger.updateJob(jobId, { lastActivityAt: clock.now() });
        });
        deps.onActivity(jobId);
        const plan = planInput(m.priority, handle ? job().sessionKind ?? 'starting' : 'launching', deps.caps);
        if (plan === 'start' || plan === 'steer' || plan === 'queue-native') await flush(plan === 'queue-native' ? 'queue' : plan);
        return plan === 'buffer' || plan === 'queue-launcher' ? 'buffered' : 'relayed';
      });
    },
    control: (c, from) => serial(() => control(c, from)),
    cancel: by => serial(async () => { if (authorized(by) && !stopped && job().state !== 'ended') { await handle?.interrupt('cancel'); await end('cancel', 'cancel'); } }),
    park: () => serial(async () => { if (!stopped && job().state !== 'ended' && handle?.park) { await handle.park(); ledger.updateJob(jobId, { sessionKind: 'parked', parkedAt: clock.now() }); } }),
    settle: (mode, ended, extra) => serial(() => end(mode, ended, extra)),
    activity: () => serial(async () => ({ ...(handle ? await handle.activity() : { turnRunning: false, openApprovals: 0, queuedInputs: 0, backgroundTasks: { known: 0, observable: false }, lastActivityAt: new Date(job().lastActivityAt).toISOString() }), launcherBuffered: ledger.inputs(jobId, ['buffered', 'sending']).length, lastRelayAt: Number(store.get('lastRelayAt') ?? initial.lastActivityAt) })),
    async stop() { stopped = true; controller?.abort(); await chain; await pump; },
  };
  return lane;
}
