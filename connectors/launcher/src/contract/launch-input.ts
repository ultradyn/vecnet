import type { Delivery, Priority } from '../../../hub-client/src/wire.ts';
import type { PermissionMode, WorkspaceMode } from '../backend.ts';
import { parseDuration } from '../config.ts';
import { readSchema, validateSchema } from './metadata.ts';
import type { Schema } from './metadata.ts';

export interface LaunchInput {
  backend: string; project: string; preset: string; permission_mode: PermissionMode;
  workspace_mode?: WorkspaceMode;
  base_ref?: string; title?: string; idle?: { wrapup_after?: string | number; wrapup_turn?: boolean };
  relay_from?: string[]; reply_priority?: Priority; deliver?: { push_branch?: boolean };
}
export interface LaunchRequest {
  jobId: string; threadId: string; messageId: string; requester: string;
  instructions: string; input: LaunchInput; expiresAt: string;
}
const schema = readSchema('launch-input');
export function validateLaunchInput(x: unknown): string[] {
  const errors = validateSchema(schema, x);
  if (!errors.length) {
    const d = (x as LaunchInput).idle?.wrapup_after;
    if (typeof d === 'string') {
      try { parseDuration(d); } catch { errors.push('/idle/wrapup_after: invalid duration'); }
    }
  }
  return errors;
}
/**
 * A validator error with every pointer segment the schema does not own replaced
 * by `*`. Unknown keys are caller text (and can be credential-shaped): the
 * reason names only schema-owned fields and the validator's fixed rule code.
 */
export function schemaReason(error: string): string {
  const at = error.lastIndexOf(': ');
  const pointer = at < 0 ? '' : error.slice(0, at), rule = at < 0 ? error : error.slice(at + 2);
  const segments: string[] = [];
  let node: Schema | undefined = schema;
  for (const raw of pointer.split('/').slice(1)) {
    const key = raw.replace(/~1/g, '/').replace(/~0/g, '~');
    if (node?.properties && Object.hasOwn(node.properties, key)) { segments.push(raw); node = node.properties[key]; }
    else if (node?.items && /^(?:0|[1-9][0-9]{0,5})$/.test(key)) { segments.push(raw); node = node.items; }
    else { segments.push('*'); node = typeof node?.additionalProperties === 'object' ? node.additionalProperties : undefined; }
  }
  const safeRule = /^[a-zA-Z][a-zA-Z ]{0,40}$/.test(rule) ? rule : 'invalid';
  return `${segments.length ? `/${segments.join('/')}` : '/'}: ${safeRule}`;
}
export function parseLaunchRequest(d: Delivery): { ok: true; req: LaunchRequest } | { ok: false; error: 'invalid_input' | 'unsupported'; reason: string } {
  const m = d.message, job = m.job_request;
  if (!job || job.version !== 1 || typeof job.instructions !== 'string') return { ok: false, error: 'invalid_input', reason: 'invalid job_request' };
  if (job.operation !== 'launch') return { ok: false, error: 'unsupported', reason: 'operation is not supported' };
  const input = job.input;
  if (input !== null && typeof input === 'object' && Object.hasOwn(input, 'continue_job')) return { ok: false, error: 'unsupported', reason: 'continue_job is not supported' };
  if (input !== null && typeof input === 'object' && 'backend' in input && input.backend === 'any') return { ok: false, error: 'invalid_input', reason: 'backend any is not supported yet; name a backend' };
  const errors = validateLaunchInput(input);
  if (errors.length) return { ok: false, error: 'invalid_input', reason: schemaReason(errors[0]) };
  const jobId = m.metadata['vecnet.job_id'];
  if (typeof jobId !== 'string' || !/^job_[0-9A-HJKMNP-TV-Z]{26}$/.test(jobId) || !Number.isFinite(Date.parse(m.loop.expires_at))) return { ok: false, error: 'invalid_input', reason: 'missing job id or expiry' };
  return { ok: true, req: { jobId, threadId: m.thread_id, messageId: m.message_id, requester: m.sender, instructions: job.instructions, input: input as LaunchInput, expiresAt: m.loop.expires_at } };
}
