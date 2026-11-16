// ui-shots owns one fresh output directory; individual screens keep SHOTS_DIR.
import { mkdir, mkdtemp } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { shotsDirectory } from './shot-paths.mjs';

export async function createCaptureGeneration(env = process.env) {
  const requested = shotsDirectory(env);
  await mkdir(dirname(requested), { recursive: true });
  if (!env.SHOTS_DIR) return mkdtemp(`${requested}-`);
  // Exclusive creation refuses old files, directories and symlinks. Never clear
  // arbitrary caller paths; an unsuccessful run remains available to inspect.
  await mkdir(requested);
  return requested;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  try { console.log(await createCaptureGeneration()); }
  catch { console.error('Capture destination must be a new writable directory; choose a fresh SHOTS_DIR.'); process.exitCode = 1; }
}
