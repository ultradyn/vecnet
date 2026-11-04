import { DatabaseSync } from 'node:sqlite';
import type { SQLInputValue } from 'node:sqlite';
import type { Clock } from '../../hub-client/src/ports.ts';
import type { Delivery, Priority, SendRequest, ReceiptBody } from '../../hub-client/src/wire.ts';
import type { PermissionMode, SessionRef, SessionState, SendMode, Grant, LaunchPermission, AgentQuestion, KeyValue } from './backend.ts';
import type { LaunchInput } from './contract/launch-input.ts';

export type JobState = "accepted" | "launching" | "running" | "settling" | "ended";
export type Terminal = "succeeded" | "failed" | "cancelled";
export interface JobRow {
  jobId: string; threadId: string; launchMessageId: string; requester: string; policyKey: string;
  backend: string; preset: string; permissionMode: PermissionMode; project: string; title: string;
  input: LaunchInput; tag: string /* vl-<job_id> */; state: JobState;
  sessionRef: SessionRef | null; sessionKind: SessionState["kind"] | null; turnCursor: string | null;
  branch: string | null; workspace: string | null; statusSeq: number; terminal: Terminal | null;
  turns: number; lastActivityAt: number; parkedAt: number | null; deadlineAt: number;
  reattachPending: boolean; endedAt: number | null; createdAt: number; updatedAt: number;
}
export interface IngressRow { deliveryId: string; cursor: string; threadId: string; messageId: string; sender: string;
  delivery: Delivery; state: "queued" | "done" | "failed"; note: string | null; receivedAt: number }
export interface InputRow { deliveryId: string; jobId: string; messageId: string; sender: string; text: string;
  priority: Priority; state: "buffered" | "sending" | "sent" | "consumed" | "failed"; mode: SendMode | null;
  batchId: string | null /* first 24 hex of sha256(sorted delivery ids of the batch) */; turnSeq: number | null /* lane turn counter at send */; createdAt: number }
export interface ApprovalRow { approvalId: string; jobId: string; route: "requester" | "operator" | "relay";
  requestMessageId: string | null; threadId: string | null /* null until an escalation opener is sent */; addressee: string; backendRequestId: string;
  options: Array<{ label: string; optionId: string; grants: Grant }>; defaultOnExpiry: string; permission: LaunchPermission;
  state: "open" | "decided" | "expired" | "escalated" | "superseded" | "abandoned";
  decidedBy: string | null; decidedVia: string | null; decision: string | null; escalatedTo: string | null;
  createdAt: number; expiresAt: number; decidedAt: number | null }
export interface QuestionRow { questionId: string; jobId: string; messageId: string | null; blocking: boolean;
  questions: AgentQuestion["questions"]; state: "open" | "answered" | "timed_out" | "abandoned";
  answeredBy: string | null; createdAt: number; expiresAt: number }
export type OutboxKind = "send" | "receipt";
export interface OutboxRow { id: number; kind: OutboxKind; key: string; payload: SendRequest | ReceiptBody; tag: string;
  state: "pending" | "sent" | "failed"; attempts: number; notBefore: number; result: unknown; createdAt: number }

export interface Ledger {
  tx<T>(fn: () => T): T; close(): void;
  cursor(): string | null; setCursor(c: string): void;
  admit(row: Omit<IngressRow, "state" | "note">): boolean;                 // false: duplicate delivery_id
  queued(limit: number): IngressRow[]; finishIngress(deliveryId: string, state: "done" | "failed", note?: string): void;
  createJob(row: Omit<JobRow, "statusSeq" | "terminal" | "turns" | "updatedAt" | "reattachPending" | "endedAt">): boolean; // false: job_id exists
  job(jobId: string): JobRow | undefined; jobByThread(threadId: string): JobRow | undefined;
  jobByEscalationThread(threadId: string): JobRow | undefined;
  updateJob(jobId: string, patch: Partial<Omit<JobRow, "jobId" | "threadId" | "createdAt">>): void;
  openJobs(): JobRow[];                                                     // state != "ended"
  allJobRefs(): Array<{ jobId: string; backend: string; ref: SessionRef | null; tag: string; state: JobState; endedAt: number | null; workspace: string | null }>;
  jobsByRequester(peerId: string): JobRow[];
  nextStatusSeq(jobId: string): number;                                     // increments and returns, inside the caller's tx
  setStatusSeq(jobId: string, seq: number): void;                           // re-sequencing after 409 stale_sequence
  nextCounter(jobId: string, purpose: string): number;                      // for vl.nt.<job>.<purpose>.<n>
  addInput(row: Omit<InputRow, "state" | "mode" | "batchId" | "turnSeq">): boolean; inputs(jobId: string, states: InputRow["state"][]): InputRow[];
  markInputs(deliveryIds: string[], patch: Partial<Pick<InputRow, "state" | "mode" | "batchId" | "turnSeq">>): void;
  recordTurn(jobId: string, turnId: string, outcome: string): boolean;      // false: already posted
  putApproval(r: ApprovalRow): void; approval(id: string): ApprovalRow | undefined;
  updateApproval(id: string, patch: Partial<ApprovalRow>): void; openApprovals(jobId?: string): ApprovalRow[];
  approvalByRequestMessage(messageId: string): ApprovalRow | undefined;
  putQuestion(r: QuestionRow): void; question(id: string): QuestionRow | undefined;
  questionByMessage(messageId: string): QuestionRow | undefined; updateQuestion(id: string, patch: Partial<QuestionRow>): void;
  openQuestions(jobId?: string): QuestionRow[];
  escalationThread(jobId: string): string | null; setEscalationThread(jobId: string, threadId: string): void;
  counts(): { unparked: number; open: number; byKey: Record<string, { unparked: number; open: number }> }; // over openJobs; parked = sessionKind "parked"
  noteLaunch(policyKey: string, at: number): void; launchesSince(policyKey: string, since: number): number;
  enqueue(kind: OutboxKind, key: string, payload: SendRequest | ReceiptBody, tag: string): number; // unique key: a second enqueue of a key is a no-op returning the first id
  pendingOutbox(limit: number, now: number): OutboxRow[];                   // state pending and notBefore <= now, in id order
  markOutbox(id: number, state: "sent" | "failed", result?: unknown): void; deferOutbox(id: number, notBefore: number): void;
  replaceOutbox(id: number, key: string, payload: SendRequest): void;       // re-sequencing
  kv(ns: string): KeyValue;
}
export function openLedger(path: string, clock: Clock): Ledger {
  const db = new DatabaseSync(path);
  try {
    db.exec('PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;');
    const version = Number(db.prepare('PRAGMA user_version').get()!.user_version);
    if (version !== 0 && version !== 1) throw new Error('unsupported ledger schema version');
    if (version === 0) {
      db.exec(`BEGIN IMMEDIATE;
        CREATE TABLE jobs (id TEXT PRIMARY KEY, data TEXT NOT NULL);
        CREATE UNIQUE INDEX jobs_thread ON jobs(json_extract(data, '$.threadId'));
        CREATE TABLE ingress (id TEXT PRIMARY KEY, data TEXT NOT NULL);
        CREATE TABLE inputs (id TEXT PRIMARY KEY, data TEXT NOT NULL);
        CREATE TABLE approvals (id TEXT PRIMARY KEY, data TEXT NOT NULL);
        CREATE TABLE questions (id TEXT PRIMARY KEY, data TEXT NOT NULL);
        CREATE TABLE turns (job TEXT NOT NULL, turn TEXT NOT NULL, outcome TEXT NOT NULL, PRIMARY KEY(job, turn));
        CREATE TABLE counters (job TEXT NOT NULL, purpose TEXT NOT NULL, n INTEGER NOT NULL, PRIMARY KEY(job, purpose));
        CREATE TABLE escalation (job TEXT PRIMARY KEY, thread TEXT NOT NULL UNIQUE);
        CREATE TABLE launches (policy_key TEXT NOT NULL, at INTEGER NOT NULL);
        CREATE INDEX launches_key_at ON launches(policy_key, at);
        CREATE TABLE kv (ns TEXT NOT NULL, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(ns, key));
        CREATE TABLE outbox (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, key TEXT NOT NULL UNIQUE,
          payload TEXT NOT NULL, tag TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
          not_before INTEGER NOT NULL, result TEXT, created_at INTEGER NOT NULL);
        PRAGMA user_version = 1;
        COMMIT;`);
    }
  } catch (err) { db.close(); throw err; }

  // Table names here are constants owned by this module; caller data is always bound.
  type Table = 'jobs' | 'ingress' | 'inputs' | 'approvals' | 'questions';
  function rows<T>(table: Table, where = '1', args: SQLInputValue[] = [], limit?: number): T[] {
    return db.prepare(`SELECT data FROM ${table} WHERE ${where} ORDER BY rowid${limit === undefined ? '' : ' LIMIT ?'}`).all(...args, ...(limit === undefined ? [] : [limit]))
      .map(row => JSON.parse(row.data as string) as T);
  }
  function get<T>(table: Table, id: string): T | undefined { return rows<T>(table, 'id = ?', [id])[0]; }
  function insert(table: Table, id: string, data: unknown): boolean {
    return db.prepare(`INSERT INTO ${table}(id, data) VALUES (?, ?) ON CONFLICT(id) DO NOTHING`).run(id, JSON.stringify(data)).changes === 1;
  }
  function put(table: Table, id: string, data: unknown): void {
    db.prepare(`INSERT INTO ${table}(id, data) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`).run(id, JSON.stringify(data));
  }
  function patch<T extends object>(table: Table, id: string, changes: Partial<T>): void {
    const row = get<T>(table, id);
    if (!row) throw new Error('ledger row not found');
    put(table, id, { ...row, ...changes });
  }
  function requiredJob(id: string): JobRow {
    const row = get<JobRow>('jobs', id);
    if (!row) throw new Error('ledger job not found');
    return row;
  }
  function outboxRows(where: string, args: SQLInputValue[], limit: number): OutboxRow[] {
    return db.prepare(`SELECT * FROM outbox WHERE ${where} ORDER BY id LIMIT ?`).all(...args, limit).map(r => ({
      id: Number(r.id), kind: r.kind as OutboxKind, key: r.key as string, payload: JSON.parse(r.payload as string), tag: r.tag as string,
      state: r.state as OutboxRow['state'], attempts: Number(r.attempts), notBefore: Number(r.not_before),
      result: r.result === null ? undefined : JSON.parse(r.result as string), createdAt: Number(r.created_at),
    }));
  }
  let depth = 0;
  const ledger: Ledger = {
    tx(fn) {
      const level = depth;
      db.exec(level === 0 ? 'BEGIN IMMEDIATE' : `SAVEPOINT ledger_${level}`);
      depth++;
      try {
        const result = fn();
        if (result && typeof (result as { then?: unknown }).then === 'function') throw new Error('ledger transactions must be synchronous');
        db.exec(level === 0 ? 'COMMIT' : `RELEASE SAVEPOINT ledger_${level}`);
        return result;
      } catch (err) {
        db.exec(level === 0 ? 'ROLLBACK' : `ROLLBACK TO SAVEPOINT ledger_${level}; RELEASE SAVEPOINT ledger_${level}`);
        throw err;
      } finally { depth--; }
    },
    close() { db.close(); },
    cursor: () => ledger.kv('ledger').get('cursor') ?? null,
    setCursor(c) { ledger.kv('ledger').set('cursor', c); },
    admit: row => insert('ingress', row.deliveryId, { ...row, state: 'queued', note: null }),
    queued: limit => rows<IngressRow>('ingress', "json_extract(data, '$.state') = 'queued'", [], limit),
    finishIngress(id, state, note) { patch<IngressRow>('ingress', id, { state, note: note ?? null }); },
    createJob: row => insert('jobs', row.jobId, { ...row, statusSeq: 0, terminal: null, turns: 0, updatedAt: clock.now(), reattachPending: false, endedAt: null }),
    job: id => get<JobRow>('jobs', id),
    jobByThread: id => rows<JobRow>('jobs', "json_extract(data, '$.threadId') = ?", [id])[0],
    jobByEscalationThread(id) {
      const row = db.prepare('SELECT job FROM escalation WHERE thread = ?').get(id);
      return row ? ledger.job(row.job as string) : undefined;
    },
    updateJob(id, changes) { patch<JobRow>('jobs', id, { ...changes, updatedAt: clock.now() }); },
    openJobs: () => rows<JobRow>('jobs', "json_extract(data, '$.state') != 'ended'"),
    allJobRefs: () => rows<JobRow>('jobs').map(r => ({ jobId: r.jobId, backend: r.backend, ref: r.sessionRef, tag: r.tag, state: r.state, endedAt: r.endedAt, workspace: r.workspace })),
    jobsByRequester: id => rows<JobRow>('jobs', "json_extract(data, '$.requester') = ?", [id]),
    nextStatusSeq(id) { const seq = requiredJob(id).statusSeq + 1; ledger.setStatusSeq(id, seq); return seq; },
    setStatusSeq(id, seq) { ledger.updateJob(id, { statusSeq: seq }); },
    nextCounter(job, purpose) {
      const r = db.prepare(`INSERT INTO counters(job, purpose, n) VALUES (?, ?, 1)
        ON CONFLICT(job, purpose) DO UPDATE SET n=n+1 RETURNING n`).get(job, purpose)!;
      return Number(r.n);
    },
    addInput: row => insert('inputs', row.deliveryId, { ...row, state: 'buffered', mode: null, batchId: null, turnSeq: null }),
    inputs(job, states) {
      if (states.length === 0) return [];
      return rows<InputRow>('inputs', `json_extract(data, '$.jobId') = ? AND json_extract(data, '$.state') IN (${states.map(() => '?').join(',')})`, [job, ...states]);
    },
    markInputs(ids, changes) { ledger.tx(() => { for (const id of ids) patch<InputRow>('inputs', id, changes); }); },
    recordTurn: (job, turn, outcome) => db.prepare('INSERT INTO turns VALUES (?, ?, ?) ON CONFLICT(job, turn) DO NOTHING').run(job, turn, outcome).changes === 1,
    putApproval(r) { put('approvals', r.approvalId, r); },
    approval: id => get<ApprovalRow>('approvals', id),
    updateApproval(id, changes) { patch<ApprovalRow>('approvals', id, changes); },
    openApprovals: job => rows<ApprovalRow>('approvals', "json_extract(data, '$.state') = 'open'" + (job === undefined ? '' : " AND json_extract(data, '$.jobId') = ?"), job === undefined ? [] : [job]),
    approvalByRequestMessage: id => rows<ApprovalRow>('approvals', "json_extract(data, '$.requestMessageId') = ?", [id])[0],
    putQuestion(r) { put('questions', r.questionId, r); },
    question: id => get<QuestionRow>('questions', id),
    questionByMessage: id => rows<QuestionRow>('questions', "json_extract(data, '$.messageId') = ?", [id])[0],
    updateQuestion(id, changes) { patch<QuestionRow>('questions', id, changes); },
    openQuestions: job => rows<QuestionRow>('questions', "json_extract(data, '$.state') = 'open'" + (job === undefined ? '' : " AND json_extract(data, '$.jobId') = ?"), job === undefined ? [] : [job]),
    escalationThread: job => db.prepare('SELECT thread FROM escalation WHERE job = ?').get(job)?.thread as string ?? null,
    setEscalationThread(job, thread) { db.prepare('INSERT INTO escalation VALUES (?, ?) ON CONFLICT(job) DO UPDATE SET thread=excluded.thread').run(job, thread); },
    counts() {
      const byKey: Record<string, { unparked: number; open: number }> = {};
      let open = 0, unparked = 0;
      for (const r of ledger.openJobs()) {
        if (!Object.hasOwn(byKey, r.policyKey)) Object.defineProperty(byKey, r.policyKey, { value: { unparked: 0, open: 0 }, enumerable: true });
        open++; byKey[r.policyKey].open++;
        if (r.sessionKind !== 'parked') { unparked++; byKey[r.policyKey].unparked++; }
      }
      return { open, unparked, byKey };
    },
    noteLaunch(key, at) { db.prepare('INSERT INTO launches VALUES (?, ?)').run(key, at); },
    launchesSince: (key, since) => Number(db.prepare('SELECT count(*) AS n FROM launches WHERE policy_key = ? AND at >= ?').get(key, since)!.n),
    enqueue(kind, key, payload, tag) {
      db.prepare('INSERT INTO outbox(kind, key, payload, tag, not_before, created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(key) DO NOTHING')
        .run(kind, key, JSON.stringify(payload), tag, clock.now(), clock.now());
      return Number(db.prepare('SELECT id FROM outbox WHERE key = ?').get(key)!.id);
    },
    pendingOutbox: (limit, now) => outboxRows('state = ? AND not_before <= ?', ['pending', now], limit),
    // attempts counts completed dispatch cycles (retryHub's internal retries share a cycle).
    markOutbox(id, state, result) { db.prepare('UPDATE outbox SET state = ?, result = ?, attempts = attempts + 1 WHERE id = ?').run(state, JSON.stringify(result) ?? null, id); },
    deferOutbox(id, at) { db.prepare('UPDATE outbox SET not_before = ?, attempts = attempts + 1 WHERE id = ?').run(at, id); },
    replaceOutbox(id, key, payload) { db.prepare('UPDATE outbox SET key = ?, payload = ? WHERE id = ?').run(key, JSON.stringify(payload), id); },
    kv(ns) {
      return {
        get: key => db.prepare('SELECT value FROM kv WHERE ns = ? AND key = ?').get(ns, key)?.value as string | undefined,
        set(key, value) { db.prepare('INSERT INTO kv VALUES (?, ?, ?) ON CONFLICT(ns, key) DO UPDATE SET value=excluded.value').run(ns, key, value); },
        delete(key) { db.prepare('DELETE FROM kv WHERE ns = ? AND key = ?').run(ns, key); },
        keys: (prefix = '') => db.prepare('SELECT key FROM kv WHERE ns = ? ORDER BY key').all(ns).map(r => r.key as string).filter(key => key.startsWith(prefix)),
      };
    },
  };
  return ledger;
}
