// Build and stage the CI-only owner separately from release binaries.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFile, lstat, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const windowsOwnerFile = 'ci-windows-owner.exe';
const root = fileURLToPath(new URL('../../', import.meta.url));
const machine = {'win32-x64': 0x8664, 'win32-arm64': 0xaa64};
const hash = bytes => createHash('sha256').update(bytes).digest('hex');

function checkMachine(bytes, platform) {
  const pe = bytes.length >= 64 ? bytes.readUInt32LE(0x3c) : 0;
  if (!Object.hasOwn(machine, platform) || pe < 64 || pe > bytes.length - 6 ||
      bytes.readUInt16LE(0) !== 0x5a4d || bytes.readUInt32LE(pe) !== 0x4550 ||
      bytes.readUInt16LE(pe + 4) !== machine[platform]) throw new Error('Windows owner PE architecture mismatch');
}

export async function buildWindowsOwner(directory, platform, env) {
  if (!platform.startsWith('win32-')) return {};
  const file = path.join(directory, windowsOwnerFile);
  execFileSync('go', ['build', '-mod=readonly', '-p', '2', '-trimpath', '-buildvcs=false', '-ldflags', '-s -w',
    '-o', file, './scripts/ci/_owned-windows'], {cwd: root, env: {...env, GOMAXPROCS: '2'}, stdio: 'inherit'});
  const bytes = await readFile(file);
  checkMachine(bytes, platform);
  return {ciWindowsOwner: {file: windowsOwnerFile, sha256: hash(bytes)}};
}

export async function readWindowsOwner(directory, info, platform) {
  const metadata = info.ciWindowsOwner;
  if (info.platform !== platform || metadata?.file !== windowsOwnerFile ||
      typeof metadata.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(metadata.sha256)) {
    throw new Error('invalid Windows owner metadata');
  }
  const file = path.join(directory, windowsOwnerFile), stat = await lstat(file);
  if (!stat.isFile() || stat.nlink !== 1) throw new Error('unsafe Windows owner file');
  const bytes = await readFile(file);
  if (hash(bytes) !== metadata.sha256) throw new Error('Windows owner hash mismatch');
  checkMachine(bytes, platform);
  return bytes;
}

// Write exactly the validated bytes, never execute the artifact in place.
export async function stageWindowsOwner(bytes, temporary) {
  const file = path.join(temporary, windowsOwnerFile);
  await writeFile(file, bytes, {flag: 'wx', mode: 0o700});
  return file;
}

export async function exportWindowsOwner(file) {
  if (!process.env.GITHUB_ENV) throw new Error('GITHUB_ENV is required to export Windows owner');
  if (!path.isAbsolute(file) || /[\r\n]/.test(file)) throw new Error('invalid staged Windows owner path');
  await appendFile(process.env.GITHUB_ENV, `CI_WINDOWS_OWNER=${file}\n`);
}

export async function stageTargetOwner(bytes) {
  const temporary = await mkdtemp(path.join(await realpath(os.tmpdir()), 'vco-'));
  try {
    await exportWindowsOwner(await stageWindowsOwner(bytes, temporary));
  } catch (error) {
    await rm(temporary, {recursive: true, force: true});
    throw error;
  }
}

// Stage before the first budgeted Go command, so bundle discovery also avoids
// PowerShell startup. This step only validates and writes files; it spawns none.
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.platform !== 'win32' || process.argv.length !== 3 || process.argv[2] !== 'use-go') {
    throw new Error('usage: windows-owner.mjs use-go (Windows only)');
  }
  const directory = path.resolve('dist/go-tests');
  const info = JSON.parse(await readFile(path.join(directory, 'bundle.json'), 'utf8'));
  const sha = process.env.GITHUB_SHA;
  if (!/^[a-f0-9]{40}$/.test(sha || '') || info.revision !== sha) throw new Error('Windows Go owner source mismatch');
  await stageTargetOwner(await readWindowsOwner(directory, info, `win32-${process.arch}`));
  console.log(`Staged native Windows Go command owner for ${sha}`);
}
