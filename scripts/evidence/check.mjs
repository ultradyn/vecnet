// Check changes, not the historical tree: existing evidence and removals are allowed.
import { spawnSync } from 'node:child_process';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export function checkEvidence({ cwd = process.cwd(), base } = {}) {
  function git(args, input) {
    const result = spawnSync('git', args, { cwd, input, encoding: 'utf8' });
    if (result.error || result.status !== 0) throw new Error('git evidence check failed; verify the repository and base history');
    return result.stdout;
  }
  // Anchor names at the repository root even when invoked from a subdirectory.
  cwd = git(['rev-parse', '--show-toplevel']).trimEnd();
  let range = ['--cached'];
  if (base !== undefined) {
    // GitHub sends an all-zero before SHA when there is no previous branch tip.
    const tree = /^(?:0{40}|0{64})$/.test(base)
      ? git(['hash-object', '-t', 'tree', '--stdin'], '').trim()
      : git(['rev-parse', '--verify', '--end-of-options', `${base}^{tree}`]).trim();
    range = [tree, 'HEAD'];
  }
  // Treat renames as deletion + addition: only the destination can be blocked.
  const paths = git(['diff', '--no-ext-diff', '--no-renames', '--name-only', '-z', '--diff-filter=ACMRT', ...range, '--'])
    .split('\0').filter(Boolean);
  return paths.filter(path => /^(?:\.scratch\/|docs\/(?:plans|prototypes|design)\/)/.test(path)
    && /\.(?:png|jpe?g|gif|webp|mp4|webm|mov|avi|mkv)$/i.test(path));
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  try {
    const args = process.argv.slice(2);
    if (args.length && (args.length !== 2 || args[0] !== '--base' || !args[1] || args[1].startsWith('-'))) {
      throw new Error('usage: node scripts/evidence/check.mjs [--base REF]');
    }
    const paths = checkEvidence({ base: args[1] });
    if (paths.length) {
      console.error('Evidence media must be published outside Git; commit a public receipt instead:');
      for (const path of paths) console.error(`  ${JSON.stringify(path)}`);
      process.exitCode = 1;
    } else console.log('ok - no new or modified evidence media');
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
