import { readFileSync } from 'node:fs';
import { ID } from '../../../hub-client/src/ids.ts';

// Small draft-2020-12 subset used by our five checked-in schemas. Loading the
// schemas here keeps the wire validators and the published contracts in sync.
export interface Schema {
  type?: string | string[]; const?: unknown; enum?: unknown[]; not?: Schema;
  properties?: Record<string, Schema>; required?: string[];
  additionalProperties?: boolean | Schema; items?: Schema;
  minLength?: number; maxLength?: number; pattern?: string;
  minimum?: number; maximum?: number; minItems?: number; maxItems?: number;
}
export function readSchema(name: string): Schema {
  return JSON.parse(readFileSync(new URL(`../../schema/${name}.v1.schema.json`, import.meta.url), 'utf8')) as Schema;
}
export function validateSchema(schema: Schema, x: unknown, pointer = ''): string[] {
  const errors: string[] = [];
  const fail = (rule: string) => { errors.push(`${pointer || '/'}: ${rule}`); };
  if (Object.hasOwn(schema, 'const') && !Object.is(x, schema.const)) fail('const');
  if (schema.enum && !schema.enum.some(v => Object.is(v, x))) fail('enum');
  if (schema.not && validateSchema(schema.not, x).length === 0) fail('reserved value');
  if (schema.type) {
    const types = typeof schema.type === 'string' ? [schema.type] : schema.type;
    const matches = types.some(t => t === 'object' ? x !== null && typeof x === 'object' && !Array.isArray(x) :
      t === 'array' ? Array.isArray(x) : t === 'integer' ? Number.isSafeInteger(x) :
      t === 'number' ? typeof x === 'number' && Number.isFinite(x) : typeof x === t);
    if (!matches) { fail('type'); return errors; }
  }
  if (typeof x === 'string') {
    if (schema.minLength !== undefined && [...x].length < schema.minLength) fail('minLength');
    if (schema.maxLength !== undefined && [...x].length > schema.maxLength) fail('maxLength');
    if (schema.pattern && !new RegExp(schema.pattern).test(x)) fail('pattern');
  }
  if (typeof x === 'number') {
    if (schema.minimum !== undefined && x < schema.minimum) fail('minimum');
    if (schema.maximum !== undefined && x > schema.maximum) fail('maximum');
  }
  if (Array.isArray(x)) {
    if (schema.minItems !== undefined && x.length < schema.minItems) fail('minItems');
    if (schema.maxItems !== undefined && x.length > schema.maxItems) fail('maxItems');
    if (schema.items) for (const [i, v] of x.entries()) errors.push(...validateSchema(schema.items, v, `${pointer}/${i}`));
  } else if (x !== null && typeof x === 'object') {
    const record = x as Record<string, unknown>;
    for (const k of schema.required ?? []) if (!Object.hasOwn(record, k)) errors.push(`${pointer}/${k}: required`);
    for (const [k, v] of Object.entries(record)) {
      const p = `${pointer}/${k.replace(/~/g, '~0').replace(/\//g, '~1')}`;
      const field = schema.properties && Object.hasOwn(schema.properties, k) ? schema.properties[k] : undefined;
      if (field) errors.push(...validateSchema(field, v, p));
      else if (schema.additionalProperties === false) errors.push(`${p}: unknown key`);
      else if (typeof schema.additionalProperties === 'object') errors.push(...validateSchema(schema.additionalProperties, v, p));
    }
  }
  return errors;
}

export interface LaunchQuestion {
  v: 1; question_id: string; blocking: boolean;
  questions: Array<{ id: string; header?: string; text: string; options: string[]; allow_other: boolean }>;
}
export interface LaunchAnswers { question_id: string; answers: Record<string, string> }
export interface LaunchApprovalClosed { approval_id: string; superseded_by: 'operator'; decision: string }
export type LaunchControl = 'interrupt' | 'settle' | 'extend';
export type TurnOutcome = 'completed' | 'interrupted' | 'failed' | 'refused' | 'max_tokens';
export interface TurnMetadata { outcome: TurnOutcome; turnId: string; jobId: string; consumed: string[]; consumedTruncated?: number }
export type Metadata = Record<string, unknown>;
const questionSchema = readSchema('launch-question');
const answersSchema = readSchema('launch-answers');
const closedSchema = readSchema('launch-approval-closed');
function value(meta: unknown, key: string): unknown {
  return meta !== null && typeof meta === 'object' && !Array.isArray(meta) && Object.hasOwn(meta, key) ? (meta as Metadata)[key] : undefined;
}
export function parseQuestion(meta: unknown): LaunchQuestion | undefined {
  const x = value(meta, 'launch.question');
  if (validateSchema(questionSchema, x).length) return undefined;
  const q = x as LaunchQuestion;
  if (new Set(q.questions.map(v => v.id)).size !== q.questions.length) return undefined;
  return q;
}
export function parseAnswers(meta: unknown): LaunchAnswers | undefined {
  const x = value(meta, 'launch.answers');
  return validateSchema(answersSchema, x).length ? undefined : x as LaunchAnswers;
}
export function parseApprovalClosed(meta: unknown): LaunchApprovalClosed | undefined {
  const x = value(meta, 'launch.approval_closed');
  return validateSchema(closedSchema, x).length ? undefined : x as LaunchApprovalClosed;
}
function stringField(meta: unknown, key: string, max = Infinity): string | undefined {
  const x = value(meta, key);
  return typeof x === 'string' && x.length > 0 && [...x].length <= max ? x : undefined;
}
export function parseControl(meta: unknown): LaunchControl | undefined {
  const x = value(meta, 'launch.control');
  return x === 'interrupt' || x === 'settle' || x === 'extend' ? x : undefined;
}
export function parseDecision(meta: unknown): string | undefined { return stringField(meta, 'launch.decision', 64); }
export function parseDecidedVia(meta: unknown): string | undefined { return stringField(meta, 'launch.decided_via'); }
export function parseEscalatedFrom(meta: unknown): string | undefined {
  const x = stringField(meta, 'launch.escalated_from');
  return x && ID.approval.test(x) ? x : undefined;
}
export function parseFinalText(meta: unknown): 'best_effort' | undefined {
  return value(meta, 'launch.final_text') === 'best_effort' ? 'best_effort' : undefined;
}
export function parseTurn(meta: unknown): TurnMetadata | undefined {
  const outcome = value(meta, 'launch.turn'), turnId = stringField(meta, 'launch.turn_id'), jobId = stringField(meta, 'launch.job_id');
  const consumed = value(meta, 'launch.consumed'), n = value(meta, 'launch.consumed_truncated');
  if (!['completed','interrupted','failed','refused','max_tokens'].includes(outcome as string) || !turnId || !jobId || !/^job_[0-9A-HJKMNP-TV-Z]{26}$/.test(jobId) ||
      !Array.isArray(consumed) || consumed.length > 100 || !consumed.every(v => typeof v === 'string' && ID.message.test(v)) ||
      (n !== undefined && (!Number.isSafeInteger(n) || (n as number) <= 0))) return undefined;
  return { outcome: outcome as TurnOutcome, turnId, jobId, consumed, ...(n === undefined ? {} : { consumedTruncated: n as number }) };
}
function checked<T>(key: string, x: T, parse: (m: unknown) => T | undefined): Metadata {
  const m = { [key]: x };
  if (parse(m) === undefined) throw new Error(`invalid ${key}`);
  return m;
}
export function buildQuestion(q: LaunchQuestion): Metadata { return checked('launch.question', q, parseQuestion); }
export function buildAnswers(a: LaunchAnswers): Metadata { return checked('launch.answers', a, parseAnswers); }
export function buildControl(c: LaunchControl): Metadata { return checked('launch.control', c, parseControl); }
export function buildDecision(d: string): Metadata { return checked('launch.decision', d, parseDecision); }
export function buildDecidedVia(v: string): Metadata { return checked('launch.decided_via', v, parseDecidedVia); }
export function buildApprovalClosed(c: LaunchApprovalClosed): Metadata { return checked('launch.approval_closed', c, parseApprovalClosed); }
export function buildEscalatedFrom(id: string): Metadata { return checked('launch.escalated_from', id, parseEscalatedFrom); }
export function buildFinalText(): Metadata { return { 'launch.final_text': 'best_effort' }; }
export function buildTurn(t: TurnMetadata): Metadata {
  const omitted = Math.max(0, t.consumed.length - 100) + (t.consumedTruncated ?? 0);
  const m: Metadata = { 'launch.turn': t.outcome, 'launch.turn_id': t.turnId, 'launch.job_id': t.jobId, 'launch.consumed': t.consumed.slice(0, 100), ...(omitted ? { 'launch.consumed_truncated': omitted } : {}) };
  if (!parseTurn(m)) throw new Error('invalid launch.turn');
  return m;
}
