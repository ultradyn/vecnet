import { mkdir, realpath } from 'node:fs/promises';
import { homedir } from 'node:os';
import { isAbsolute, join, resolve } from 'node:path';
import type { LaunchSpec, PluginContext } from '../../backend.ts';

export const workspaceModes = ['worktree', 'project-root', 'temp'] as const;
export type WorkspaceMode = typeof workspaceModes[number];
// Local extension until cx lands the shared allocation contract. Never interpret
// project.repoRoot as an already allocated job directory.
export type AcpLaunchSpec = LaunchSpec & { workspaceMode?: WorkspaceMode };
export interface Workspace { mode: WorkspaceMode; path: string; repo?: string; branch?: string }
export function workspaceRoot(configured?: string): string {
  const root = configured ?? join(homedir(), '.cache', 'vecnet-launcher', 'workspaces');
  if (!isAbsolute(root)) throw new Error('acp_workspace_root_must_be_absolute');
  return resolve(root);
}
export async function git(ctx: PluginContext, args: string[], signal?: AbortSignal): Promise<string> {
  const r = await ctx.exec(['git', ...args], { timeoutMs: 60000, signal });
  if (r.code !== 0) throw new Error('acp_workspace_git_failed');
  return r.stdout.trim();
}
export async function allocate(spec: AcpLaunchSpec, root: string, ctx: PluginContext, signal?: AbortSignal): Promise<Workspace> {
  const mode = spec.workspaceMode ?? 'worktree';
  if (!workspaceModes.includes(mode)) throw new Error('acp_workspace_mode_unsupported');
  if (!/^job_[A-Za-z0-9_]+$/.test(spec.jobId)) throw new Error('acp_invalid_job');
  if (mode === 'project-root') {
    const repo = await realpath(spec.project.repoRoot);
    if (!isAbsolute(spec.project.repoRoot) || resolve(spec.project.repoRoot) !== repo) throw new Error('acp_project_root_must_be_canonical');
    // Read only: retain its current branch, including a detached HEAD.
    const branch = await git(ctx, ['-C', repo, 'branch', '--show-current'], signal);
    return { mode, path: repo, repo, ...(branch ? { branch } : {}) };
  }
  await mkdir(root, { recursive: true, mode: 0o700 });
  if (await realpath(root) !== root) throw new Error('acp_workspace_root_must_be_canonical');
  const path = join(root, spec.jobId);
  if (mode === 'temp') {
    // Exclusive mkdir prevents adopting an old job's directory. No alias repo
    // lookup, Git init, branch creation, or shared repo mutation in this mode.
    await mkdir(path, { mode: 0o700 });
    return { mode, path };
  }
  const repo = await realpath(spec.project.repoRoot), branch = `launch/${spec.jobId}`;
  const base = await git(ctx, ['-C', repo, 'rev-parse', '--verify', '--end-of-options', `${spec.project.baseRef}^{commit}`], signal);
  await git(ctx, ['-C', repo, 'worktree', 'add', '-b', branch, '--', path, base], signal);
  return { mode, path, repo, branch };
}
