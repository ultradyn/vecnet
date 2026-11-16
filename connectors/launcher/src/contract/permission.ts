import { createHash } from 'node:crypto';
import { realpathSync, lstatSync, readlinkSync } from 'node:fs';
import { dirname, basename, resolve, relative, isAbsolute } from 'node:path';
import type { LaunchPermission, PermissionDraft, Grant, PermissionMode, PermissionRequest } from '../backend.ts';
import { redactText } from '../redact.ts';
import { readSchema, validateSchema } from './metadata.ts';

const schema = readSchema('launch-permission');
export function validateLaunchPermission(x: unknown): string[] { return validateSchema(schema, x); }
export function hasShellOperators(line: string): boolean { return /[;|&`<>]|\$\(/.test(line); }

// Keep .. until native realpath has followed symlinks. Resolve the nearest
// existing ancestor too: a not-yet-created file beneath a
// symlink must not look confined just because realpath(file) returns ENOENT.
function canonical(path: string, depth = 0): string {
  if (depth > 128) throw new Error('cannot resolve permission path');
  const absolute = isAbsolute(path) ? path : `${process.cwd()}/${path}`;
  try { return realpathSync.native(absolute); }
  catch (e) {
    const code = (e as NodeJS.ErrnoException).code;
    if (code !== 'ENOENT') throw new Error('cannot resolve permission path');
    const parent = dirname(absolute);
    if (parent === absolute) throw new Error('cannot resolve permission path');
    try {
      if (lstatSync(absolute).isSymbolicLink()) {
        const link = readlinkSync(absolute);
        return canonical(isAbsolute(link) ? link : `${parent}/${link}`,depth+1);
      }
    } catch (e) {
      if ((e as NodeJS.ErrnoException).code !== 'ENOENT') throw new Error('cannot resolve permission path');
    }
    return resolve(canonical(parent,depth+1),basename(absolute));
  }
}
function within(root: string, path: string): boolean {
  const rel = relative(root,path);
  return rel === '' || (!isAbsolute(rel) && rel !== '..' && !rel.startsWith('../') && !rel.startsWith('..\\'));
}
function jsonBytes(x: unknown): number { return Buffer.byteLength(JSON.stringify(x)); }
// JSON's escaping can make a truncated UTF-8 string larger than its byte slice.
function truncateJSONText(s: string, budget: number): string {
  let lo = 0, hi = s.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (jsonBytes(s.slice(0,mid)) <= budget) lo = mid; else hi = mid - 1;
  }
  return s.slice(0,lo).replace(/[\uD800-\uDBFF]$/,'');
}
export interface PermissionContext {
  machineId: string; jobId: string; permissionMode: PermissionMode; preset: string;
  backend: string; workspaceRoot: string; home: string;
}
/**
 * The complete canonical permission: paths resolved and classified against the
 * workspace, nothing redacted or shortened. Routing decides on this object, so
 * a long command line or path list cannot push a privileged operation past the
 * published copy's size cap (finishPermission).
 */
export function canonicalPermission(draft: PermissionDraft, ctx: PermissionContext): LaunchPermission {
  const root = canonical(ctx.workspaceRoot);
  function path(p: string): string {
    const expanded = p === '~' ? ctx.home : (/^~[\\/]/.test(p)) ? `${ctx.home}/${p.slice(2)}` : p;
    return canonical(isAbsolute(expanded) ? expanded : `${root}/${expanded}`);
  }
  const paths = draft.paths?.map(p => { const full = path(p.path); return { ...p, path: full, in_workspace: within(root,full) }; });
  const command = draft.command && { ...draft.command,
    ...(draft.command.cwd === undefined ? {} : { cwd: path(draft.command.cwd) }),
    shell_operators: hasShellOperators(draft.command.line ?? ''),
  };
  const outside = paths?.some(p=>!p.in_workspace) || (command?.cwd !== undefined && !within(root,command.cwd)) ? true :
    !command && draft.fidelity.paths === 'enforced' && paths !== undefined && paths.length > 0 && paths.every(p=>p.in_workspace) ? false : 'unknown';
  return {
    ...draft, v:1,machine_id:ctx.machineId,job_id:ctx.jobId,permission_mode:ctx.permissionMode,preset:ctx.preset,backend:ctx.backend,
    workspace_root:root,outside_workspace:outside,redacted:draft.options.some(o=>redactText(o.label).changed),backend_raw_truncated:false,
    command,paths,
  };
}
/** The published copy of a permission: redacted and bounded to 6 KiB. Never route on it. */
export function finishPermission(draft: PermissionDraft, ctx: PermissionContext): LaunchPermission {
  let raw: string;
  try { raw = JSON.stringify(draft.backend_raw); if (raw === undefined) throw new Error(); }
  catch { throw new Error('backend permission must be JSON serialisable'); }
  const sha256 = createHash('sha256').update(raw).digest('hex');
  const p: LaunchPermission = { ...canonicalPermission(draft,ctx), options:[] };
  // Redact values structurally, never the serialised JSON (a bearer regex could
  // eat quotes and produce invalid JSON). Cover reason, argv and raw keys;
  // option labels are redacted during final normalization below.
  function scrub(value: unknown): unknown {
    if (typeof value === 'string') {
      const r = redactText(value); if (r.changed) p.redacted = true; return r.text;
    }
    if (Array.isArray(value)) return value.map(scrub);
    if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key,v])=>[scrub(key) as string,scrub(v)]));
    return value;
  }
  const redacted = scrub(JSON.parse(JSON.stringify(p))) as LaunchPermission;
  redacted.redacted = p.redacted;
  // Finalize options after request redaction: scrubbing a deduplication suffix
  // can collapse labels again. IDs are opaque; refuse ones unsafe to publish
  // rather than rewrite the backend's decision mapping.
  redacted.options = approvalOptions(draft.options,false).map(o=>({id:o.optionId,label:o.label,grants:o.grants}));
  const rawText = JSON.stringify(redacted.backend_raw);
  if (Buffer.byteLength(rawText) > 4096) {
    redacted.backend_raw = truncateJSONText(rawText,4096);
    redacted.backend_raw_truncated = true; redacted.backend_raw_sha256 = sha256; redacted.redacted = true;
  }
  if (jsonBytes(redacted) > 6144) {
    if (redacted.command?.line !== undefined) redacted.command.line = truncateJSONText(redacted.command.line,1024);
    if (redacted.paths) redacted.paths = redacted.paths.slice(0,32);
    redacted.redacted = true;
    // The prescribed 1 KiB / 32-path fallback alone cannot bound an arbitrary
    // raw request or long filenames. Shed raw preview first, then optional data;
    // never shorten identities or change the meaning of an option id.
    if (jsonBytes(redacted) > 6144) {
      redacted.backend_raw = ''; redacted.backend_raw_truncated = true; redacted.backend_raw_sha256 = sha256;
    }
    while (jsonBytes(redacted) > 6144 && redacted.paths?.length) redacted.paths.pop();
    if (jsonBytes(redacted) > 6144) { delete redacted.reason; delete redacted.tool; delete redacted.host; }
    if (jsonBytes(redacted) > 6144 && redacted.command) { delete redacted.command.argv; delete redacted.command.cwd; }
  }
  if (jsonBytes(redacted) > 6144 || validateLaunchPermission(redacted).length) throw new Error('invalid or oversized backend permission');
  return redacted;
}

export function approvalOptions(opts: PermissionRequest['options'], escalate: boolean): Array<{ label: string; optionId: string; grants: Grant }> {
  const ordered = opts.length > 7 ? [...opts.filter(o=>['deny','deny_always','cancel'].includes(o.grants)),...opts.filter(o=>!['deny','deny_always','cancel'].includes(o.grants))].slice(0,7) : opts;
  const used = new Set<string>(escalate ? ['escalate'] : []);
  const ids = new Set<string>();
  const result = ordered.map(o=>{
    if (!o.id || ids.has(o.id)) throw new Error('invalid backend option ids');
    if (redactText(o.id).changed) throw new Error('unsafe backend option id');
    ids.add(o.id);
    const text = redactText(o.label).text.trim() || 'Option';
    const base = text.slice(0,64);
    let label = base, n = 1;
    while (used.has(label)) { const suffix = `(${++n})`; label = `${base.slice(0,64-suffix.length)}${suffix}`; }
    used.add(label); return {label,optionId:o.id,grants:o.grants};
  });
  if (escalate) {
    if (ids.has('escalate')) throw new Error('reserved backend option id');
    result.push({label:'escalate',optionId:'escalate',grants:'deny'});
  }
  if (result.length < 2) throw new Error('approval needs at least two options');
  return result;
}
