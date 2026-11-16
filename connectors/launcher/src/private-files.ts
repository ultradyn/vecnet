import { spawnSync } from 'node:child_process';
import { closeSync, constants, fstatSync, lstatSync, openSync, readSync } from 'node:fs';
import { isAbsolute } from 'node:path';
import { open } from 'node:fs/promises';
import type { Stats } from 'node:fs';
import { cliEnvironment, commandInvocation, envValue } from '../../hub-client/src/platform.ts';
import type { Environment } from '../../hub-client/src/platform.ts';

export const MAX_PRIVATE_BYTES = 1_048_576;
export class PrivateFileError extends Error {
  readonly code: string;
  constructor(code: string) { super('secret file must be a regular file readable only by its owner'); this.code = code; }
}

/** Node's Windows mode/uid bits are not access control. These commands are
 * implemented by the native CLI, using handle-based owner/DACL validation.
 * No helper output or error is ever included in a diagnostic. */
export function localPlatform(command: string, path: string, options: { env?: Environment; input?: string; directory?: boolean } = {}): string {
  if (!isAbsolute(path) || path.includes('\0') || options.input !== undefined && Buffer.byteLength(options.input) > MAX_PRIVATE_BYTES) throw new PrivateFileError('size_or_path');
  const env = options.env ?? process.env;
  const invocation = commandInvocation([envValue(env, 'VECNET_BIN') || 'vecnet', 'local-platform', command, path, ...(options.directory ? ['--directory'] : [])], env);
  const result = spawnSync(invocation.file, invocation.args, { ...invocation, env: cliEnvironment(env), input: options.input,
    encoding: 'utf8', timeout: 5000, maxBuffer: MAX_PRIVATE_BYTES + 1, windowsHide: true, stdio: ['pipe', 'pipe', 'ignore'] });
  if (result.error || result.status !== 0 || Buffer.byteLength(result.stdout ?? '') > MAX_PRIVATE_BYTES) throw new PrivateFileError('security');
  return result.stdout;
}

interface ReadOptions { exactMode?: number; owner?: number; env?: Environment }
function validate(st: Stats, options: ReadOptions): void {
  if (!st.isFile()) throw new PrivateFileError('not_file');
  if (options.owner !== undefined && st.uid !== options.owner) throw new PrivateFileError('foreign_owner');
  if (options.exactMode === undefined ? (st.mode & 0o077) !== 0 : (st.mode & 0o7777) !== options.exactMode) throw new PrivateFileError('mode');
  if (st.size > MAX_PRIVATE_BYTES) throw new PrivateFileError('size');
}
export function readPrivateFileSync(path: string, options: ReadOptions = {}): string {
  if (process.platform === 'win32') {
    // Preserve absent/not-file diagnostics without opening an unsafe pipe or
    // device. Security AND the actual read remain on one native handle.
    const st = lstatSync(path);
    if (!st.isFile() || st.isSymbolicLink()) throw new PrivateFileError('not_file');
    if (st.size > MAX_PRIVATE_BYTES) throw new PrivateFileError('size');
    return localPlatform('read-private', path, { env: options.env });
  }
  const fd = openSync(path, constants.O_RDONLY | constants.O_NONBLOCK);
  try {
    validate(fstatSync(fd), options);
    const bytes = Buffer.alloc(MAX_PRIVATE_BYTES + 1);
    let n = 0, count: number;
    while (n < bytes.length && (count = readSync(fd, bytes, n, bytes.length - n, null)) > 0) n += count;
    if (n > MAX_PRIVATE_BYTES) throw new PrivateFileError('size');
    return bytes.subarray(0, n).toString('utf8');
  } finally { closeSync(fd); }
}
export async function readPrivateFile(path: string, options: ReadOptions = {}): Promise<string> {
  if (process.platform === 'win32') return readPrivateFileSync(path, options);
  const file = await open(path, constants.O_RDONLY | constants.O_NONBLOCK);
  try {
    validate(await file.stat(), options);
    const bytes = Buffer.alloc(MAX_PRIVATE_BYTES + 1);
    let n = 0;
    while (n < bytes.length) { const { bytesRead } = await file.read(bytes, n, bytes.length - n, null); if (!bytesRead) break; n += bytesRead; }
    if (n > MAX_PRIVATE_BYTES) throw new PrivateFileError('size');
    return bytes.subarray(0, n).toString('utf8');
  } finally { await file.close(); }
}
