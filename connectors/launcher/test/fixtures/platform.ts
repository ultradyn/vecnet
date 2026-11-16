import { spawnSync } from 'node:child_process';
import { MAX_PRIVATE_BYTES } from '../../src/private-files.ts';
import { cliEnvironment, commandInvocation, envValue } from '../../../hub-client/src/platform.ts';
import { symlink as nativeSymlink, lstat as lstatAsync } from 'node:fs/promises';
import { symlinkSync as nativeSymlinkSync, lstatSync, writeFileSync, mkdirSync, chmodSync } from 'node:fs';
import type { PathLike } from 'node:fs';
import { dirname, isAbsolute } from 'node:path';

/** Fixture failures expose subprocess metadata only, never helper output or
 * private bytes. The native CLI still performs every ownership/ACL check. */
function fixturePlatform(command: string, path: string, input?: string, directory = false): void {
  const inputBytes = Buffer.byteLength(input ?? '');
  if (!isAbsolute(path) || path.includes('\0') || inputBytes > MAX_PRIVATE_BYTES) throw new Error('invalid private fixture input');
  const binary = envValue(process.env, 'VECNET_BIN') || 'vecnet';
  const invocation = commandInvocation([binary, 'local-platform', command, path, ...(directory ? ['--directory'] : [])]);
  const result = spawnSync(invocation.file, invocation.args, { ...invocation, env: cliEnvironment(process.env), input,
    encoding: 'utf8', timeout: 5000, maxBuffer: MAX_PRIVATE_BYTES + 1, windowsHide: true, stdio: ['pipe', 'pipe', 'ignore'] });
  const outputBytes = Buffer.byteLength(result.stdout ?? '');
  if (result.error || result.status !== 0 || outputBytes > MAX_PRIVATE_BYTES) {
    throw new Error('native private fixture failed: ' + JSON.stringify({ command, status: result.status, signal: result.signal,
      errno: (result.error as NodeJS.ErrnoException | undefined)?.code ?? null, inputBytes, outputBytes,
      configuredBinary: !!envValue(process.env, 'VECNET_BIN'), absoluteBinary: isAbsolute(binary), batch: !!invocation.windowsVerbatimArguments }));
  }
}
function checkExistingParent(path: string): void {
  let parent = dirname(path);
  for (;;) {
    try { lstatSync(parent); break; }
    catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'ENOENT' || dirname(parent) === parent) throw error;
      parent = dirname(parent);
    }
  }
  fixturePlatform('check-safe', parent, undefined, true);
}

export async function symlink(target: PathLike, path: PathLike): Promise<void> {
  let directory = false;
  try { directory = (await lstatAsync(target)).isDirectory(); } catch { directory = true; }
  await nativeSymlink(target, path, process.platform === 'win32' ? directory ? 'junction' : 'file' : undefined);
}
export function symlinkSync(target: PathLike, path: PathLike): void {
  let directory = false;
  try { directory = lstatSync(target).isDirectory(); } catch { directory = true; }
  nativeSymlinkSync(target, path, process.platform === 'win32' ? directory ? 'junction' : 'file' : undefined);
}
/** A real executable test command on either platform; never a Unix-only shebang. */
export function nodeCommand(path: string, script: string): string {
  if (process.platform === 'win32') {
    const source = path + '.cjs'; writeFileSync(source, script);
    const command = path + '.cmd'; writeFileSync(command, `@echo off\r\n"${process.execPath}" "${source}" %*\r\n`);
    return command;
  }
  writeFileSync(path, `#!${process.execPath}\n${script}`, { mode: 0o700 }); return path;
}

export function writePrivate(path: string, data: string): void {
  if (process.platform === 'win32') { checkExistingParent(path); fixturePlatform('write-private', path, data); }
  else writeFileSync(path, data, { mode: 0o600 });
}
export function makePrivateDirectory(path: string): void {
  if (process.platform === 'win32') { checkExistingParent(path); fixturePlatform('mkdir-private', path); }
  else mkdirSync(path, { recursive: true, mode: 0o700 });
}
export function makePublicReadable(path: string): void {
  if (process.platform !== 'win32') { chmodSync(path, 0o644); return; }
  const result = spawnSync('icacls.exe', [path, '/grant', '*S-1-1-0:(R)'], { windowsHide: true, stdio: 'pipe' });
  if (result.status !== 0) throw new Error('test ACL setup failed');
}
export function makeOwnerPrivate(path: string): void {
  if (process.platform !== 'win32') { chmodSync(path, 0o600); return; }
  const result = spawnSync('icacls.exe', [path, '/remove:g', '*S-1-1-0'], { windowsHide: true, stdio: 'pipe' });
  if (result.status !== 0) throw new Error('test ACL reset failed');
}
