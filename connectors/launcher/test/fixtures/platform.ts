import { spawnSync } from 'node:child_process';
import { localPlatform } from '../../src/private-files.ts';
import { symlink as nativeSymlink, lstat as lstatAsync } from 'node:fs/promises';
import { symlinkSync as nativeSymlinkSync, lstatSync, writeFileSync, mkdirSync, chmodSync } from 'node:fs';
import type { PathLike } from 'node:fs';

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
  if (process.platform === 'win32') localPlatform('write-private', path, { input: data });
  else writeFileSync(path, data, { mode: 0o600 });
}
export function makePrivateDirectory(path: string): void {
  if (process.platform === 'win32') localPlatform('mkdir-private', path);
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
