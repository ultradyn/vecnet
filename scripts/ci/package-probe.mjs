// Disposable diagnostic only. Never merge this script or its workflow.
import { readFile, writeFile } from 'node:fs/promises';
const file = 'scripts/npm/package.test.mjs';
let source = await readFile(file, 'utf8');
const oldImport = "import { execFileSync, spawn, spawnSync } from 'node:child_process';";
if (!source.includes(oldImport)) throw new Error('Unexpected original package smoke source');
source = source.replace(oldImport, "import { execFileSync as rawExecFileSync, spawn, spawnSync as rawSpawnSync } from 'node:child_process';");
source += `
function execFileSync(file, args, options) {
  const started = performance.now();
  const label = [String(file).split(/[\\\\/]/).at(-1), ...args.slice(0, 3)].join(' ');
  console.log('probe-start exec ' + label);
  try { return rawExecFileSync(file, args, options); }
  finally { console.log('probe-end exec ms=' + Math.round(performance.now() - started) + ' ' + label); }
}
function spawnSync(file, args, options) {
  const started = performance.now();
  const label = [String(file).split(/[\\\\/]/).at(-1), ...args.slice(0, 3)].join(' ');
  console.log('probe-start spawn ' + label);
  try { return rawSpawnSync(file, args, options); }
  finally { console.log('probe-end spawn ms=' + Math.round(performance.now() - started) + ' ' + label); }
}
`;
await writeFile(file, source);
const checksumFile = 'scripts/npm/verify-artifact.mjs';
let checksum = await readFile(checksumFile, 'utf8');
checksum = checksum.replace("const bytes = await readFile(path.join(root, 'dist', info.tarball));", "console.time('probe-read'); const bytes = await readFile(path.join(root, 'dist', info.tarball)); console.timeEnd('probe-read');");
checksum = checksum.replace("const digest = verifyChecksum(inventory, info.tarball, bytes);", "console.time('probe-hash'); const digest = verifyChecksum(inventory, info.tarball, bytes); console.timeEnd('probe-hash');");
await writeFile(checksumFile, checksum);
console.log('Instrumentation only; original test body/assertions/deadlines unchanged');
