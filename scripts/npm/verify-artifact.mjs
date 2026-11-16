import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { releaseInfo } from './release.mjs';

export function verifyChecksum(inventory, tarball, bytes) {
  const match = /^([a-f0-9]{64}) [ *]([^\r\n]+)$/.exec(inventory.trim());
  if (!match || match[2] !== tarball) throw new Error('Unexpected release checksum inventory');
  const digest = createHash('sha256').update(bytes).digest('hex');
  if (digest !== match[1]) throw new Error('Release tarball checksum mismatch');
  return digest;
}

async function main() {
  // Read the same sha256sum file on Windows and Unix. Do not glob for a tarball:
  // only the manifest's exact artifact may be tested and later published.
  const root = fileURLToPath(new URL('../../', import.meta.url));
  const info = releaseInfo(JSON.parse(await readFile(path.join(root, 'npm', 'package.json'), 'utf8')));
  const inventory = await readFile(path.join(root, 'dist', 'SHA256SUMS'), 'utf8');
  const bytes = await readFile(path.join(root, 'dist', info.tarball));
  const digest = verifyChecksum(inventory, info.tarball, bytes);
  console.log(`${info.tarball}: OK (${digest})`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
