import { open, readFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import type { PermissionMode } from './backend.ts';
import { redactText } from './redact.ts';

export type ApprovalCategory = 'outside_workspace_writes' | 'privileged_exec' | 'unverifiable' | 'exec' | 'edit' | 'network' | 'mcp_tool' | 'other';
export interface Ceiling { backends: string[]; permission_modes: PermissionMode[]; projects: string[]; max_sessions: number; max_open_sessions: number; launches_per_hour: number }
export interface LauncherConfig {
  host: string;
  hub: { url: string; credential_file: string };
  state_dir: string;
  backends: Array<{ id: string; module: string; config: unknown }>;
  projects: Record<string, { repo_root: string; base_ref: string; scratch?: boolean; backends?: string[] }>;
  limits: { max_sessions: number; max_open_sessions: number; max_idle_wrapup_ms: number; max_session_lifetime_ms: number; workspace_retention_ms: number };
  policies: Record<string, Partial<Ceiling>>;
  approvals: { route: 'requester'; parent_expires_ms: number; on_parent_expiry: 'deny' | 'escalate'; operator_expires_ms: number;
    to_operator: { presets: Array<'none' | 'outside-workspace' | 'privileged'>; categories: ApprovalCategory[]; requesters: string[] } };
  relays: Record<string, { principals: string[]; peers?: string[]; parent_expires_ms: number; escalate_option: boolean; escalate_to_operator: boolean }>;
  questions: { blocking_timeout_ms: number };
  idle: { park_after_ms: number; settle_opaque_ms: number; settle_observable_ms: number; check_every_ms: number; deadline_margin_ms: number };
  presence: { heartbeat_ms: number };
  wake_interrupts: boolean;
}
export class ConfigError extends Error {
  readonly pointer: string;
  constructor(pointer: string, reason = 'invalid field') {
    super(redactText(`${pointer || '/'}: ${reason}`).text); this.name = 'ConfigError'; this.pointer = pointer || '/';
  }
}
export function parseDuration(s: string): number {
  const m = /^([0-9]+(?:\.[0-9]+)?)(ms|s|m|h|d)$/.exec(s);
  const factors: Record<string, number> = { ms: 1, s: 1000, m: 60000, h: 3600000, d: 86400000 };
  const n = m ? Number(m[1]) * factors[m[2]] : NaN;
  if (!Number.isSafeInteger(n) || n <= 0) throw new ConfigError('/', 'invalid duration');
  return n;
}
type Obj = Record<string, unknown>;
function fallback(x: unknown, defaultValue: unknown): unknown { return x === undefined ? defaultValue : x; }
const modes: PermissionMode[] = ['read-only','ask','auto','full-access'];
const categories: ApprovalCategory[] = ['outside_workspace_writes','privileged_exec','unverifiable','exec','edit','network','mcp_tool','other'];
function ptr(p: string, k: string): string { return `${p}/${k.replace(/~/g,'~0').replace(/\//g,'~1')}`; }
function object(x: unknown, p: string, keys?: string[]): Obj {
  if (!x || typeof x !== 'object' || Array.isArray(x)) throw new ConfigError(p, 'expected object');
  if (keys) for (const k of Object.keys(x)) if (!keys.includes(k)) throw new ConfigError(ptr(p,k), 'unknown field');
  return x as Obj;
}
function str(x: unknown, p: string): string {
  if (typeof x !== 'string' || !x.trim()) throw new ConfigError(p, 'expected nonempty string');
  return x;
}
function path(x: unknown, p: string): string { const s = str(x,p); if (!isAbsolute(s)) throw new ConfigError(p,'expected absolute path'); return s; }
function bool(x: unknown, p: string): boolean { if (typeof x !== 'boolean') throw new ConfigError(p,'expected boolean'); return x; }
function integer(x: unknown, p: string): number { if (!Number.isSafeInteger(x) || (x as number) < 0) throw new ConfigError(p,'expected nonnegative integer'); return x as number; }
function strings(x: unknown, p: string, choices?: readonly string[]): string[] {
  if (!Array.isArray(x)) throw new ConfigError(p,'expected array');
  return x.map((v,i) => { const s = str(v,ptr(p,String(i))); if (choices && !choices.includes(s)) throw new ConfigError(ptr(p,String(i)),'invalid value'); return s; });
}
function choice<T extends string>(x: unknown, p: string, choices: readonly T[]): T {
  const s = str(x,p); if (!choices.includes(s as T)) throw new ConfigError(p,'invalid value'); return s as T;
}
// Accept the design's duration spelling and the public interface's _ms spelling,
// but reject both together to avoid silently choosing a conflicting value.
function durations(x: unknown, p: string, defaults: Record<string, number>, extra: string[] = []): Obj {
  const names = Object.keys(defaults), raw = object(fallback(x,{}),p,[...extra,...names,...names.map(k=>k.slice(0,-3))]);
  const out: Obj = {};
  for (const k of names) {
    const alias = k.slice(0,-3);
    if (Object.hasOwn(raw,k) && Object.hasOwn(raw,alias)) throw new ConfigError(ptr(p,k),'duplicate duration');
    const field = Object.hasOwn(raw,k) ? k : alias;
    if (!Object.hasOwn(raw,field)) { out[k] = defaults[k]; continue; }
    const v = raw[field], location = ptr(p,field);
    try { out[k] = typeof v === 'string' ? parseDuration(v) : integer(v,location); }
    catch { throw new ConfigError(location,'invalid duration'); }
    if ((out[k] as number) <= 0) throw new ConfigError(location,'duration must be positive');
  }
  return out;
}
export function parseConfig(raw: unknown): LauncherConfig {
  const r = object(raw,'',['host','hub','state_dir','backends','projects','limits','policies','approvals','relays','questions','idle','presence','wake_interrupts','_disabled_backends']);
  const h = object(r.hub,'/hub',['url','credential_file']);
  const url = str(h.url,'/hub/url');
  try { const u = new URL(url); if (!['http:','https:'].includes(u.protocol) || u.username || u.password) throw new Error(); }
  catch { throw new ConfigError('/hub/url','expected HTTP(S) URL without credentials'); }
  if (!Array.isArray(r.backends)) throw new ConfigError('/backends','expected array');
  const backends = r.backends.map((b,i) => {
    const p = `/backends/${i}`, v = object(b,p,['id','module','config']);
    return { id: str(v.id,`${p}/id`), module: str(v.module,`${p}/module`), config: fallback(v.config,{}) };
  });
  if (new Set(backends.map(b=>b.id)).size !== backends.length) throw new ConfigError('/backends','duplicate backend id');
  const projects = Object.fromEntries(Object.entries(object(r.projects,'/projects')).map(([k,v])=>{
    const p = ptr('/projects',k), x = object(v,p,['repo_root','base_ref','backends','scratch']);
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(k)) throw new ConfigError(p,'invalid project alias');
    return [k,{ repo_root:path(x.repo_root,`${p}/repo_root`), base_ref:str(x.base_ref,`${p}/base_ref`), ...(x.scratch === undefined ? {} : {scratch:bool(x.scratch,`${p}/scratch`)}), ...(x.backends === undefined ? {} : {backends:strings(x.backends,`${p}/backends`)}) }];
  }));
  const l = object(fallback(r.limits,{}),'/limits',['max_sessions','max_open_sessions','max_idle_wrapup','max_idle_wrapup_ms','max_session_lifetime','max_session_lifetime_ms','workspace_retention','workspace_retention_ms']);
  const limits = { ...durations(l,'/limits',{max_idle_wrapup_ms:28800000,max_session_lifetime_ms:259200000,workspace_retention_ms:604800000},['max_sessions','max_open_sessions']), max_sessions:integer(fallback(l.max_sessions,6),'/limits/max_sessions'), max_open_sessions:integer(fallback(l.max_open_sessions,24),'/limits/max_open_sessions') } as LauncherConfig['limits'];
  const policies: LauncherConfig['policies'] = { default: {backends:['t3','paseo'],permission_modes:[...modes],projects:[],max_sessions:3,max_open_sessions:12,launches_per_hour:30} };
  for (const [k,v] of Object.entries(object(fallback(r.policies,{}),'/policies'))) {
    const p = ptr('/policies',k);
    if (k !== 'default' && !/^(peer|prn)_[0-9A-HJKMNP-TV-Z]{26}$/.test(k)) throw new ConfigError(p,'expected default, peer id or principal id');
    const x = object(v,p,['backends','permission_modes','projects','max_sessions','max_open_sessions','launches_per_hour']);
    const c: Partial<Ceiling> = {};
    for (const key of ['backends','projects'] as const) if (Object.hasOwn(x,key)) c[key] = strings(x[key],`${p}/${key}`);
    if (Object.hasOwn(x,'permission_modes')) c.permission_modes = strings(x.permission_modes,`${p}/permission_modes`,modes) as PermissionMode[];
    for (const key of ['max_sessions','max_open_sessions','launches_per_hour'] as const) if (Object.hasOwn(x,key)) c[key] = integer(x[key],`${p}/${key}`);
    Object.defineProperty(policies,k,{value:{...(k === 'default' ? policies.default : {}),...c},enumerable:true,writable:true,configurable:true});
  }
  const a = object(fallback(r.approvals,{}),'/approvals');
  const times = durations(a,'/approvals',{parent_expires_ms:900000,operator_expires_ms:3600000},['route','on_parent_expiry','to_operator']);
  const op = object(fallback(a.to_operator,{}),'/approvals/to_operator',['presets','categories','requesters']);
  const presets = strings(fallback(op.presets,['none']),'/approvals/to_operator/presets',['none','outside-workspace','privileged']) as LauncherConfig['approvals']['to_operator']['presets'];
  if (presets.includes('none') && presets.length > 1) throw new ConfigError('/approvals/to_operator/presets','none cannot combine with another preset');
  const approvals = {route:choice(fallback(a.route,'requester'),'/approvals/route',['requester']),...times,on_parent_expiry:choice(fallback(a.on_parent_expiry,'deny'),'/approvals/on_parent_expiry',['deny','escalate']),to_operator:{presets,categories:strings(fallback(op.categories,[]),'/approvals/to_operator/categories',categories) as ApprovalCategory[],requesters:strings(fallback(op.requesters,[]),'/approvals/to_operator/requesters')}} as LauncherConfig['approvals'];
  const relays = Object.fromEntries(Object.entries(object(fallback(r.relays,{}),'/relays')).map(([k,v])=>{
    const p = ptr('/relays',k), x = object(v,p);
    const t = durations(x,p,{parent_expires_ms:2100000},['principals','peers','escalate_option','escalate_to_operator']);
    return [k,{principals:strings(x.principals,`${p}/principals`),...(x.peers === undefined ? {} : {peers:strings(x.peers,`${p}/peers`)}),...t,escalate_option:bool(fallback(x.escalate_option,false),`${p}/escalate_option`),escalate_to_operator:bool(fallback(x.escalate_to_operator,false),`${p}/escalate_to_operator`)}];
  })) as LauncherConfig['relays'];
  return {
    host:str(r.host,'/host'),hub:{url,credential_file:path(h.credential_file,'/hub/credential_file')},state_dir:path(r.state_dir,'/state_dir'),backends,projects,limits,policies,approvals,relays,
    questions:durations(r.questions,'/questions',{blocking_timeout_ms:1800000}) as LauncherConfig['questions'],
    idle:durations(r.idle,'/idle',{park_after_ms:1800000,settle_opaque_ms:7200000,settle_observable_ms:86400000,check_every_ms:60000,deadline_margin_ms:300000}) as LauncherConfig['idle'],
    presence:durations(r.presence,'/presence',{heartbeat_ms:300000}) as LauncherConfig['presence'],wake_interrupts:bool(fallback(r.wake_interrupts,false),'/wake_interrupts'),
  };
}
export async function loadConfig(path: string): Promise<LauncherConfig> {
  let raw: unknown;
  try { raw = JSON.parse(await readFile(path,'utf8')); } catch { throw new ConfigError('/','cannot read JSON config'); }
  const config = parseConfig(raw);
  try {
    const file = await open(config.hub.credential_file,'r');
    try { const stat = await file.stat(); if (!stat.isFile() || (stat.mode & 0o7777) !== 0o600) throw new Error(); }
    finally { await file.close(); }
  } catch { throw new ConfigError('/hub/credential_file','credential must be a readable regular file with mode 0600'); }
  return config;
}
