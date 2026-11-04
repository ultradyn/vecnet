import type { PermissionMode, SessionRef, SessionState, TurnEvent } from '../../backend.ts';

export interface T3Thread {
  threadId: string; projectId?: string; title: string; status: string;
  activeRunId?: string | null; latestRunId?: string | null; pendingRequestCount?: number;
  archived?: boolean; settled?: boolean; branch?: string | null; worktreePath?: string | null;
  updatedAt?: string; queuedInputCount?: number;
}
export interface T3Run { runId: string; ordinal: number; status: string; completedAt?: string | null }
export interface T3Item {
  itemId: string; position: number; runId?: string | null;
  createdBy?: string | null; type: string; text?: string | null;
  textTruncated?: boolean; nextTextOffset?: number | null;
}
export interface Snapshot {
  thread: T3Thread; recentRuns: T3Run[]; items: T3Item[];
  nextPosition?: number | null; hasMore?: boolean;
}
export interface Provider {
  providerInstanceId: string; enabled: boolean; source: string; stale: boolean;
  runtimeModes: string[]; models: Array<{id: string; hidden: boolean; options: OptionDescriptor[]}>;
}
interface OptionDescriptor {
  id: string; type?: string;
  options?: Array<string | {id: string}> | Record<string, unknown>;
}
export const offeredModes: PermissionMode[] = ['ask', 'auto', 'full-access'];
export function runtimeMode(mode: PermissionMode): string {
  if (mode === 'read-only') throw new Error('permission_mode');
  return mode === 'ask' ? 'approval-required' : mode;
}
export function optionAllowed(descriptors: OptionDescriptor[], id: string, value: string): boolean {
  const descriptor = descriptors.find(x => x?.id === id);
  if (!descriptor) return false;
  if (descriptor.type === 'boolean') return value === 'true' || value === 'false';
  if (descriptor.type !== undefined && descriptor.type !== 'select') return false;
  const choices = descriptor.options;
  if (Array.isArray(choices)) return choices.some(x => (typeof x === 'string' ? x : x.id) === value);
  return choices !== undefined && choices !== null && Object.hasOwn(choices, value);
}
export function outcome(status: string): Extract<TurnEvent, {type:'turn_finished'}>['outcome'] | undefined {
  switch (status) {
    case 'completed': return 'completed';
    case 'cancelled': case 'interrupted': return 'interrupted';
    case 'failed': case 'rolled_back': return 'failed';
    default: return undefined;
  }
}
export function stateOf(thread: T3Thread, now: string): SessionState {
  if (thread.archived || thread.settled) return {kind:'ended',reason:'settled',resumable:false};
  if ((thread.pendingRequestCount ?? 0) > 0 || thread.status === 'waiting') return {kind:'waiting_approval',requestIds:[]};
  if (thread.activeRunId || ['preparing','queued','starting','running'].includes(thread.status)) {
    return {kind:'turn_running',turnId:thread.activeRunId ?? thread.latestRunId ?? '',since:thread.updatedAt ?? now};
  }
  return {kind:'idle',since:thread.updatedAt ?? now};
}
export function refOf(thread: T3Thread, host: string, tag?: string): SessionRef {
  return {backend:'t3',host,id:thread.threadId,extra:{tag:tag ?? thread.title.split(/\s/,1)[0],branch:thread.branch ?? undefined,
    workspace:thread.worktreePath ?? undefined,link:`t3-thread://v1/${thread.threadId}`}};
}
export interface Cursor { p: number; r: string | null }
export function parseCursor(value?: string): Cursor {
  if (!value) return {p:0,r:null};
  let cursor: unknown;
  try { cursor = JSON.parse(value); } catch { throw new Error('invalid_t3_cursor'); }
  if (!cursor || typeof cursor !== 'object' || !('p' in cursor) || !Number.isSafeInteger(cursor.p) || (cursor.p as number) < 0 ||
      !('r' in cursor) || (cursor.r !== null && typeof cursor.r !== 'string')) throw new Error('invalid_t3_cursor');
  return cursor as Cursor;
}
