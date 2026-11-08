export type AllowedMethod = 'initialize' | 'configRequirements/read' | 'thread/read' | 'thread/turns/list' | 'thread/resume' |
  'thread/unsubscribe' | 'turn/start' | 'turn/steer';
export const CODEX_OPT_OUT_NOTIFICATIONS: readonly string[] = Object.freeze([
  'item/agentMessage/delta', 'item/plan/delta', 'item/reasoning/textDelta', 'item/reasoning/summaryTextDelta',
  'item/commandExecution/outputDelta', 'item/fileChange/outputDelta', 'hook/started', 'hook/completed',
  'thread/tokenUsage/updated', 'turn/moderationMetadata',
]);
export function buildThreadResume(threadId: string): { threadId: string; excludeTurns: true } {
  return { threadId, excludeTurns: true };
}
export function buildThreadUnsubscribe(threadId: string): { threadId: string } {
  return { threadId };
}
export type ResumePolicy = { approvalPolicy: string; approvalsReviewer: string; asks: boolean };
function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {};
}
export function parseResumePolicy(resp: unknown): ResumePolicy {
  const r = record(resp);
  const approvalPolicy = typeof r.approvalPolicy === 'string' ? r.approvalPolicy : 'unknown';
  const approvalsReviewer = typeof r.approvalsReviewer === 'string' ? r.approvalsReviewer : 'unknown';
  // Validate the 0.166.0 wire shape before applying the non-asking exception.
  // Otherwise an auto_review reviewer could admit a malformed policy.
  const policy = record(r.approvalPolicy), granular = record(policy.granular);
  const validGranular = Object.keys(policy).length === 1 && Object.hasOwn(policy, 'granular') &&
    ['mcp_elicitations', 'rules', 'sandbox_approval'].every(key => typeof granular[key] === 'boolean') &&
    ['request_permissions', 'skill_approval'].every(key => !Object.hasOwn(granular, key) || typeof granular[key] === 'boolean');
  const validPolicy = ['never', 'on-request', 'untrusted'].includes(approvalPolicy) || validGranular;
  const validReviewer = ['user', 'auto_review', 'guardian_subagent'].includes(approvalsReviewer);
  return { approvalPolicy, approvalsReviewer,
    asks: !validPolicy || !validReviewer || !(approvalPolicy === 'never' || approvalsReviewer === 'auto_review') };
}
export type UntrustedEntry = { value: string; kind: 'untrusted'; name: 'vecnet_inbox' };
type TurnInput = { threadId: string; stub: string; entries: { key: string; value: string }[]; key: string };
export function buildTurnStart(a: TurnInput): Record<string, unknown> {
  // Construct from allowlisted scalars only. Never spread a caller's object into
  // wire params: Codex persists policy overrides on the live thread.
  return {
    threadId: a.threadId,
    clientUserMessageId: a.key,
    input: [{ type: 'text', text: a.stub }],
    ...(a.entries.length ? { additionalContext: Object.fromEntries(a.entries.map(entry => [entry.key,
      { value: entry.value, kind: 'untrusted', name: 'vecnet_inbox' } satisfies UntrustedEntry])) } : {}),
  };
}
export function buildTurnSteer(a: TurnInput & { expectedTurnId: string }): Record<string, unknown> {
  return { ...buildTurnStart(a), expectedTurnId: a.expectedTurnId };
}
export function mapStatus(s: unknown): 'idle' | 'turn_running' | 'waiting_approval' | 'waiting_input' | 'not_loaded' | 'unknown' {
  const r = record(s);
  if (r.type === 'idle') return 'idle';
  if (r.type === 'notLoaded') return 'not_loaded';
  if (r.type !== 'active' || !Array.isArray(r.activeFlags)) return 'unknown';
  if (r.activeFlags.includes('waitingOnApproval')) return 'waiting_approval';
  if (r.activeFlags.includes('waitingOnUserInput')) return 'waiting_input';
  if (r.activeFlags.length) return 'unknown';
  return 'turn_running';
}
export function buildTurnsPage(threadId: string, cursor?: string): Record<string, unknown> {
  return { threadId, itemsView: 'full', limit: 5, ...(cursor !== undefined ? { cursor } : {}) };
}
