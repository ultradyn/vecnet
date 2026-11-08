import { basename, resolve, relative, isAbsolute } from 'node:path';
import type { BackendCapabilities, BackendHealth, LaunchPermission, ResolvedProject } from './backend.ts';
import { parseDuration } from './config.ts';
import type { ApprovalCategory, Ceiling, LauncherConfig } from './config.ts';
import type { LaunchRequest } from './contract/launch-input.ts';
import type { Directory } from './directory.ts';

// Task 9 is not a dependency of Task 10. This structural seam is exactly its
// planned Ledger.counts contract and accepts the real ledger without adaptation.
type Ledger = { counts(): { unparked: number; open: number; byKey: Record<string, { unparked: number; open: number }> } };
export interface PolicyKey { key: string; source: 'peer' | 'principal' | 'default' }
export function policyKeyFor(peerId: string, dir: Directory, cfg: LauncherConfig): PolicyKey {
  if (Object.hasOwn(cfg.policies, peerId)) return {key:peerId,source:'peer'};
  const principal = dir.createdBy(peerId);
  if (principal !== undefined && Object.hasOwn(cfg.policies, principal)) return {key:principal,source:'principal'};
  return {key:'default',source:'default'};
}
/**
 * The accounting key: whose sessions and launches a limit counts. A peer or
 * principal policy counts under its own key (a principal's peers share one
 * pool, as configured); policies.default is a per-requester template, so each
 * default requester counts under its own peer id.
 */
export function accountKeyFor(key: PolicyKey, peerId: string): string { return key.source === 'default' ? peerId : key.key; }
export function ceilingFor(key: PolicyKey, cfg: LauncherConfig): Ceiling {
  const defaults = cfg.policies.default as Ceiling;
  const entry = Object.hasOwn(cfg.policies, key.key) ? cfg.policies[key.key] : {};
  return {
    backends:entry.backends ?? defaults.backends,
    permission_modes:entry.permission_modes ?? defaults.permission_modes,
    projects:entry.projects ?? defaults.projects,
    max_sessions:entry.max_sessions ?? defaults.max_sessions,
    max_open_sessions:entry.max_open_sessions ?? defaults.max_open_sessions,
    launches_per_hour:entry.launches_per_hour ?? defaults.launches_per_hour,
  };
}
export type GateResult = { ok: true; key: PolicyKey; account: string; ceiling: Ceiling; project: ResolvedProject; deadlineAt: number; settleMs: number }
  | { ok: false; error: 'policy_denied'; reason: string; details?: Record<string, unknown> }
  | { ok: false; error: 'cap_reached'; details: { limit: string; running: number; open: number } }
  | { ok: false; error: 'backend_unhealthy'; reason: string };
export function gateLaunch(a: { sender: string; req: LaunchRequest; dir: Directory; cfg: LauncherConfig;
  counts: ReturnType<Ledger['counts']>; /** launches counted under accountKeyFor(policyKeyFor(sender), sender) */ launchesLastHour: number;
  backends: Map<string, { caps: BackendCapabilities; health: BackendHealth }>; now: number }): GateResult {
  const deny = (reason: string): GateResult => ({ok:false,error:'policy_denied',reason});
  const kind = a.dir.kindOf(a.sender);
  if (kind === undefined || kind === 'source' || kind === 'launched') return deny('sender_kind');
  const key = policyKeyFor(a.sender,a.dir,a.cfg), ceiling = ceilingFor(key,a.cfg);
  const input = a.req.input, backend = a.backends.get(input.backend);
  if (!a.cfg.backends.some(b => b.id === input.backend) || !ceiling.backends.includes(input.backend) || !backend) return deny('backend');
  if (!backend.health.ok) return {ok:false,error:'backend_unhealthy',reason:'backend unavailable'};
  if (!backend.caps.presets.some(p => p.id === input.preset)) return deny('preset');
  if (!ceiling.permission_modes.includes(input.permission_mode) || !backend.caps.permissionModes.includes(input.permission_mode)) return deny('permission_mode');
  const workspaceMode = input.workspace_mode ?? 'worktree';
  if (!(backend.caps.workspaceModes ?? ['worktree']).includes(workspaceMode)) return deny('workspace_mode');
  if ((workspaceMode === 'temp') !== (input.project === 'temporary')) return deny('workspace_project');
  const project = Object.hasOwn(a.cfg.projects,input.project) ? a.cfg.projects[input.project] : undefined;
  if (!project || (!ceiling.projects.includes('*') && !ceiling.projects.includes(input.project)) ||
      (project.backends !== undefined && !project.backends.includes(input.backend))) return deny('project');
  const wrapup = input.idle?.wrapup_after;
  const customSettle = typeof wrapup === 'string' ? parseDuration(wrapup) : wrapup;
  if (customSettle !== undefined && customSettle > a.cfg.limits.max_idle_wrapup_ms) return deny('idle');
  const settleMs = customSettle ?? (backend.caps.backgroundTasks === 'observable' ? a.cfg.idle.settle_observable_ms : a.cfg.idle.settle_opaque_ms);
  const deadlineAt = Math.min(Date.parse(a.req.expiresAt),a.now + a.cfg.limits.max_session_lifetime_ms);
  const neededMs = settleMs + 10 * 60 * 1000;
  if (!Number.isFinite(deadlineAt) || deadlineAt - a.now < neededMs) return {
    ok:false,error:'policy_denied',reason:'expires_too_soon',details:{expires_at:a.req.expiresAt,needed_ms:neededMs},
  };
  const account = accountKeyFor(key,a.sender);
  const local = Object.hasOwn(a.counts.byKey,account) ? a.counts.byKey[account] : {unparked:0,open:0};
  for (const [limit,used,max,counts] of [
    ['limits.max_sessions',a.counts.unparked,a.cfg.limits.max_sessions,a.counts],
    ['limits.max_open_sessions',a.counts.open,a.cfg.limits.max_open_sessions,a.counts],
    ['max_sessions',local.unparked,ceiling.max_sessions,local],
    ['max_open_sessions',local.open,ceiling.max_open_sessions,local],
    ['launches_per_hour',a.launchesLastHour,ceiling.launches_per_hour,local],
  ] as const) {
    if (used >= max) return {ok:false,error:'cap_reached',details:{limit,running:counts.unparked,open:counts.open}};
  }
  return {ok:true,key,account,ceiling,project:{alias:input.project,repoRoot:project.repo_root,baseRef:input.base_ref ?? project.base_ref},deadlineAt,settleMs};
}

export type ApprovalRoute = { to: 'requester' | 'operator' | 'relay'; escalateOption: boolean; escalateToOperator: boolean;
  parentExpiresMs: number; onParentExpiry: 'deny' | 'escalate'; categories: ApprovalCategory[] };

// Fail closed (coordinator decision, 2026-11-05): `line` is hostile text, and
// any hand-written shell parser can be steered around (comments, here-documents,
// `env`/`bash -c`/`eval` wrappers, quoting tricks). So a privileged command name
// anywhere in the line counts, whether it is a command word, an argument, quoted,
// commented or inside a substitution. Quotes and backslashes are removed first,
// so `s''udo` and `s\udo` still match. A false positive only sends the approval
// to the operator; a false negative would hide a privileged command from them.
// Obfuscation that never spells the name (variables, decoding) is out of reach
// for any classifier: route untrusted requesters with to_operator.requesters.
const PRIVILEGED_COMMANDS = new Set(['sudo','su','doas','pkexec']);
function mentionsPrivileged(line: string): boolean {
  // A shell deletes backslash-newline (line continuation) before it splits
  // words, so `s\<LF>udo` runs sudo: join those first, then drop quoting.
  const words = line.replace(/\\\r?\n/g,'').replace(/[\\'"`]/g,'').match(/[A-Za-z0-9_.\/~+]+/g) ?? [];
  return words.some(word => PRIVILEGED_COMMANDS.has(basename(word)));
}
function within(root: string, path: string): boolean {
  const rel = relative(root,path);
  return rel === '' || (!isAbsolute(rel) && rel !== '..' && !rel.startsWith('../'));
}
export function classifyPermission(p: LaunchPermission, home: string): ApprovalCategory[] {
  const categories: ApprovalCategory[] = [p.kind];
  if (p.outside_workspace === true && p.paths?.some(path => ['write','delete','move'].includes(path.access))) categories.push('outside_workspace_writes');
  const argv0 = p.command?.argv?.length ? p.command.argv[0] : undefined;
  const privilegedCommand = (argv0 !== undefined && PRIVILEGED_COMMANDS.has(basename(argv0))) || mentionsPrivileged(p.command?.line ?? '');
  const roots = ['/etc','/usr',resolve(home,'.ssh'),resolve(home,'.config')];
  const privilegedPath = p.paths?.some(p => {
    const expanded = p.path.startsWith('~/') ? resolve(home,p.path.slice(2)) : p.path;
    return isAbsolute(expanded) && roots.some(root => within(root,resolve(expanded)));
  });
  if (privilegedCommand || privilegedPath) categories.push('privileged_exec');
  if (p.fidelity.kind === 'reported' || (p.fidelity.command === 'none' && p.fidelity.paths === 'none')) categories.push('unverifiable');
  return categories;
}
export function routeApproval(p: LaunchPermission, job: { requester: string; policyKey: string }, dir: Directory, cfg: LauncherConfig, home: string): ApprovalRoute {
  const categories = classifyPermission(p,home), principal = dir.createdBy(job.requester);
  for (const relay of Object.values(cfg.relays)) {
    if (relay.peers?.includes(job.requester) || (principal !== undefined && relay.principals.includes(principal))) return {
      to:'relay',escalateOption:relay.escalate_option,escalateToOperator:relay.escalate_to_operator,
      parentExpiresMs:relay.parent_expires_ms,onParentExpiry:relay.escalate_to_operator ? cfg.approvals.on_parent_expiry : 'deny',categories,
    };
  }
  const operator = cfg.approvals.to_operator;
  if (operator.requesters.includes(job.requester) || (principal !== undefined && operator.requesters.includes(principal)) ||
      categories.some(category => operator.categories.includes(category) ||
        (category === 'outside_workspace_writes' && operator.presets.includes('outside-workspace')) ||
        (category === 'privileged_exec' && operator.presets.includes('privileged')))) return {
    to:'operator',escalateOption:false,escalateToOperator:false,parentExpiresMs:cfg.approvals.operator_expires_ms,onParentExpiry:'deny',categories,
  };
  return {to:'requester',escalateOption:true,escalateToOperator:true,parentExpiresMs:cfg.approvals.parent_expires_ms,onParentExpiry:cfg.approvals.on_parent_expiry,categories};
}
