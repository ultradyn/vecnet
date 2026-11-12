import { closeSync, constants, fstatSync, openSync, readSync, realpathSync, statSync } from 'node:fs';
import { dirname, isAbsolute } from 'node:path';
import type { Binding } from './engine.ts';

export type FileState = { state: 'absent' | 'refused' | 'invalid' | 'unlocatable'; reason?: string }
  | { state: 'ok'; bindings: Binding[] };
const maxBytes = 1_048_576, maxBindings = 1024;
const threadPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const timestamp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;
const text = (x: unknown): x is string => typeof x === 'string' && x.length <= 4096 && x.trim().length > 0 && !/[\x00-\x1f\x7f]/.test(x);

// Date.parse normalizes impossible calendar days; authority must reject those.
function validTimestamp(value: string): boolean {
  if (!timestamp.test(value)) return false;
  const [year, month, day] = value.slice(0, 10).split('-').map(Number);
  const date = new Date(0);
  date.setUTCFullYear(year, month - 1, day);
  return date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day;
}

/** Match the CLI's hub identity: case/default-port normalization, no trailing slash. */
function hubIdentity(raw: string): string | undefined {
  try {
    const u = new URL(raw.trim());
    if (!['https:', 'http:'].includes(u.protocol) || !u.hostname || u.username || u.password) return;
    return u.protocol + '//' + u.host + decodeURIComponent(u.pathname).replace(/\/+$/, '');
  } catch { return; }
}

export function threadKey(b: Binding): string { return JSON.stringify([b.codexHome, b.threadId]); }
/** Expiry alone is a renewal. Everything else changes the engine/policy. */
export function bindingFingerprint(b: Binding): string {
  return JSON.stringify([b.configPath, b.generation, b.profile, b.as ?? '', b.hub, b.caller,
    b.codexHome, b.threadId, b.createdAt, b.idleDormantMs ?? null, b.attended]);
}

export function parseBinding(x: unknown): Binding | undefined {
  if (!x || typeof x !== 'object' || Array.isArray(x)) return;
  const r = x as Record<string, unknown>;
  const { profile, hub, caller, config_path: config, codex_home: home, thread_id: thread,
    generation, created_at: created, expires_at: expires, idle_dormant_hours: idle, attended } = r;
  const as = r.as === undefined ? '' : r.as;
  if (!text(profile) || !text(hub) || !text(caller) || typeof as !== 'string' || (as !== '' && !text(as)) ||
      !text(config) || !isAbsolute(config) || !text(home) || !isAbsolute(home) ||
      typeof generation !== 'string' || !/^[0-9a-f]{32}$/.test(generation) ||
      typeof thread !== 'string' || !threadPattern.test(thread) ||
      typeof created !== 'string' || !validTimestamp(created) || typeof expires !== 'string' || !validTimestamp(expires)) return;
  const createdAt = Date.parse(created), expiresAt = Date.parse(expires), normalizedHub = hubIdentity(hub);
  if (!normalizedHub || !Number.isFinite(createdAt) || !Number.isFinite(expiresAt) || expiresAt <= createdAt) return;
  if (idle !== undefined && (typeof idle !== 'number' || !Number.isFinite(idle) || idle < 0 || !Number.isSafeInteger(Math.round(idle * 3_600_000)))) return;
  if (attended !== undefined && typeof attended !== 'boolean') return;
  try {
    const configPath = realpathSync(config), codexHome = realpathSync(home);
    if (!statSync(configPath).isFile() || !statSync(codexHome).isDirectory()) return;
    return { configPath, generation, profile, ...(as ? { as } : {}), hub: normalizedHub, caller, codexHome,
      threadId: thread, createdAt, expiresAt, attended: attended === true, origin: 'explicit',
      ...(typeof idle === 'number' && idle > 0 ? { idleDormantMs: Math.round(idle * 3_600_000) } : {}) };
  } catch { return; }
}

/** JSON.parse validates syntax, but silently keeps the last duplicate property.
 * Scan its valid token stream as well, retaining decoded keys for each object.
 * Strings are consumed whole, so braces and escaped quotes in values cannot
 * affect nesting; separate objects may of course reuse the same property names. */
function parseAuthorityJSON(raw: string): unknown {
  const value: unknown = JSON.parse(raw);
  const tokens = /"(?:\\.|[^"\\])*"|[{}\[\]]/g;
  const stack: (Set<string> | undefined)[] = [];
  for (const token of raw.matchAll(tokens)) {
    const text = token[0];
    if (text === '{' || text === '[') {
      if (stack.length >= 128) throw new Error('authority_nesting');
      stack.push(text === '{' ? new Set<string>() : undefined);
    } else if (text === '}' || text === ']') stack.pop();
    else {
      let next = token.index + text.length;
      while (next < raw.length && /\s/.test(raw[next])) next++;
      if (raw[next] !== ':') continue;
      const keys = stack[stack.length - 1]!, key = JSON.parse(text) as string;
      if (keys.has(key)) throw new Error('duplicate_authority_property');
      keys.add(key);
    }
  }
  return value;
}

/** A single bad row invalidates the authority; never choose a winner by file order. */
export function readBindings(path: string | undefined, euid: number): FileState {
  if (path === undefined) return { state: 'unlocatable' };
  let raw: string;
  try {
    const fd = openSync(path, constants.O_RDONLY | constants.O_NONBLOCK);
    try {
      const st = fstatSync(fd);
      if (!st.isFile()) return { state: 'refused', reason: 'not_file' };
      if (st.uid !== euid) return { state: 'refused', reason: 'foreign_owner' };
      if ((st.mode & 0o077) !== 0) return { state: 'refused', reason: 'mode' };
      if (st.size > maxBytes) return { state: 'invalid', reason: 'size' };
      const bytes = Buffer.alloc(maxBytes + 1);
      let n = 0, count: number;
      while (n < bytes.length && (count = readSync(fd, bytes, n, bytes.length - n, null)) > 0) n += count;
      if (n > maxBytes) return { state: 'invalid', reason: 'size' };
      raw = bytes.subarray(0, n).toString('utf8');
    } finally { closeSync(fd); }
  } catch (e) {
    return (e as NodeJS.ErrnoException).code === 'ENOENT' ? { state: 'absent' } : { state: 'invalid', reason: 'unreadable' };
  }
  let file: unknown;
  try { file = parseAuthorityJSON(raw); } catch { return { state: 'invalid', reason: 'json' }; }
  const f = file as { version?: unknown; bindings?: unknown };
  if (!f || typeof f !== 'object' || f.version !== 1 || !Array.isArray(f.bindings) || f.bindings.length > maxBindings) return { state: 'invalid', reason: 'shape' };
  const bindings: Binding[] = [], recipients = new Set<string>(), threads = new Set<string>(), profiles = new Set<string>();
  for (const row of f.bindings) {
    const b = parseBinding(row);
    if (!b) return { state: 'invalid', reason: 'binding' };
    const recipient = JSON.stringify([b.hub, b.as || b.caller]), thread = threadKey(b);
    const profile = JSON.stringify([dirname(b.configPath!), b.profile, b.as ?? '']);
    if (recipients.has(recipient) || threads.has(thread) || profiles.has(profile)) return { state: 'invalid', reason: 'conflict' };
    recipients.add(recipient); threads.add(thread); profiles.add(profile); bindings.push(b);
  }
  return { state: 'ok', bindings };
}
