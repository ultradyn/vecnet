// Check introduced changes, not grandfathered evidence or removals.
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
  // Git appends one LF; all preceding whitespace belongs to the root path.
  cwd = git(['rev-parse', '--show-toplevel']).slice(0, -1);
  const diffOptions = ['--no-ext-diff', '--no-renames', '--name-only', '-z', '--diff-filter=ACMRT'];
  let paths;
  if (base !== undefined) {
    // An initial push (zero SHA or the workflow's empty tree) introduces all
    // reachable commits, including media removed before the tip. Other trees
    // cannot define a history boundary: require a valid commit and fail closed.
    const emptyTree = git(['hash-object', '-t', 'tree', '--stdin'], '').trim();
    const initial = /^(?:0{40}|0{64})$/.test(base) || base === emptyTree;
    const boundary = initial ? [] : ['^' + git(['rev-parse', '--verify', '--end-of-options', `${base}^{commit}`]).trim()];
    const commits = git(['rev-list', 'HEAD', ...boundary, '--']).trim().split('\n').filter(Boolean);
    paths = commits.flatMap(commit => git(['diff-tree', '--root', '--cc', '--no-commit-id', '-r', ...diffOptions, commit, '--']).split('\0').filter(Boolean));
    // Combined merge diffs include only paths differing from every parent.
    // Parent commits are inspected above; inheriting an unchanged historical
    // file from one parent must not count as a new addition against another.
  } else {
    // Treat staged renames as deletion + addition; check the destination.
    paths = git(['diff', ...diffOptions, '--cached', '--']).split('\0').filter(Boolean);
  }
  return [...new Set(paths)].filter(path => /^(?:\.scratch\/|docs\/(?:plans|prototypes|design)\/)/.test(path)
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
