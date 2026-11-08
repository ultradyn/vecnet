import type { TestContext } from 'node:test';
import { lstat, mkdir, mkdtemp, readlink, realpath, rm } from 'node:fs/promises';
import { basename, dirname, isAbsolute, join } from 'node:path';
import { homedir } from 'node:os';

type RootEnv = Readonly<Partial<Pick<NodeJS.ProcessEnv, 'ACP_TEST_ROOT' | 'XDG_CACHE_HOME' | 'HOME'>>>;

/** Resolve through existing ancestors, including dangling symlinks, without
 * creating anything. Preserve filesystem symlink/.. semantics until resolved. */
async function prospective(path: string, depth = 0): Promise<string> {
  if (depth > 128) throw new Error('ACP test root ancestry too deep');
  try { return await realpath(path); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
    try {
      if ((await lstat(path)).isSymbolicLink()) {
        const link = await readlink(path);
        return prospective(isAbsolute(link) ? link : `${dirname(path)}/${link}`, depth + 1);
      }
    } catch (lookup) { if ((lookup as NodeJS.ErrnoException).code !== 'ENOENT') throw lookup; }
    const parent = dirname(path);
    if (parent === path) throw error;
    const candidate = join(await prospective(parent, depth + 1), basename(path));
    // Resolving an ancestor can reveal an existing symlink after a missing
    // component followed by '..'. Recheck the reconstructed spelling.
    return candidate === path ? candidate : prospective(candidate, depth + 1);
  }
}
function persistent(path: string): void {
  if (!isAbsolute(path) || path === '/tmp' || path.startsWith('/tmp/')) throw new Error('ACP test root must be persistent and outside /tmp');
}
export async function acpTestParent(env: RootEnv = process.env): Promise<string> {
  const selected = env.ACP_TEST_ROOT ?? env.XDG_CACHE_HOME ?? env.HOME ?? homedir();
  if (!isAbsolute(selected)) throw new Error('ACP test root must be absolute');
  // Append the default suffix without collapsing the selected base's symlink/.. spelling.
  const requested = env.ACP_TEST_ROOT ?? `${selected}/${env.XDG_CACHE_HOME == null ? '.cache/' : ''}vecnet-launcher/acp-tests`;
  const canonical = await prospective(requested);
  persistent(canonical);
  return canonical;
}

/** Own exactly one unique child. Register its cleanup immediately after its
 * creation, before any caller initialization or further awaited work. */
export async function acpTestDir(t: Pick<TestContext, 'after'>, prefix: string, beforeRemove?: () => unknown | Promise<unknown>): Promise<string> {
  const parent = await acpTestParent();
  await mkdir(parent, { recursive: true });
  const canonicalParent = await realpath(parent);
  persistent(canonicalParent);
  const child = await mkdtemp(join(canonicalParent, prefix));
  t.after(async () => {
    try { await beforeRemove?.(); }
    finally { await rm(child, { recursive: true, force: true }); }
  });
  return child;
}
