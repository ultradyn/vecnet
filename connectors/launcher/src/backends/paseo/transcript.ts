import { createHash } from 'node:crypto';

export interface Cursor { line: number; last: string }
export function hash(text: string): string { return createHash('sha256').update(text).digest('hex'); }
export function parseCursor(value?: string): Cursor {
  if (!value) return { line: 0, last: '' };
  const cursor = JSON.parse(value) as Cursor;
  if (!Number.isSafeInteger(cursor.line) || cursor.line < 0 || typeof cursor.last !== 'string') throw new Error('invalid_paseo_cursor');
  return { line: cursor.line, last: cursor.last };
}

export interface WaitResult { agentId?: string; status: 'idle' | 'timeout' | 'permission' | 'error'; message?: string }
export function parseWait(stdout: string, stderr: string, code: number): WaitResult {
  let outer: Record<string, unknown>;
  try { outer = JSON.parse(stdout || stderr); }
  catch { if (code !== 0) return { status: 'error', message: 'Paseo wait failed' }; throw new Error('invalid_paseo_wait'); }
  // BridgeError is JSON on stderr, unlike a normal double-encoded wait.
  if (typeof outer.stdout !== 'string') {
    if (code !== 0) return { status: 'error', message: typeof outer.error === 'string' ? outer.error : 'Paseo wait failed' };
    throw new Error('invalid_paseo_wait');
  }
  let inner: WaitResult;
  try { inner = JSON.parse(outer.stdout) as WaitResult; }
  catch { throw new Error('invalid_paseo_wait'); }
  if (!inner || typeof inner !== 'object') throw new Error('invalid_paseo_wait');
  if (!['idle', 'timeout', 'permission', 'error'].includes(inner.status)) throw new Error('invalid_paseo_wait');
  if (inner.agentId !== undefined && typeof inner.agentId !== 'string') throw new Error('invalid_paseo_wait');
  if (inner.message !== undefined && typeof inner.message !== 'string') throw new Error('invalid_paseo_wait');
  return code !== 0 ? { ...inner, status: 'error' } : inner;
}

export function finalBlock(lines: string[], message?: string): string {
  let start = 0;
  for (let i = 0; i < lines.length; i++) if (/^\[(User|Tool|Thought)\]/.test(lines[i])) start = i + 1;
  const text = lines.slice(start).filter(line => line.trim() !== 'No activity to display.').join('\n').trim();
  return text || message?.trim() || '(no text captured)';
}
