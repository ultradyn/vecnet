import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, writeFile, readFile, symlink } from 'node:fs/promises';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import type { PluginContext } from '../src/backend.ts';
import { allocate, workspaceRoot } from '../src/backends/acp/workspace.ts';
import type { AcpLaunchSpec } from '../src/backends/acp/workspace.ts';
import { acpTestDir } from './fixtures/acp-test-root.ts';

const base = 'a'.repeat(40);
test('workspace allocation owns worktree creation; root mode never changes branch/files; temp needs no alias repo', async t => {
  const dir = await acpTestDir(t, 'vecnet-acp-workspace-');
  const repo = join(dir, 'repo'), root = join(dir, 'allocated'); await mkdir(repo);
  const marker = join(repo, 'existing.txt'); await writeFile(marker, 'keep existing checkout');
  const calls: string[][] = [];
  const ctx = { exec: async (argv: string[]) => { calls.push(argv); return { code: 0, stdout: argv.includes('rev-parse') ? base : argv.includes('branch') ? 'existing-branch' : '', stderr: '' }; } } as unknown as PluginContext;
  const spec: AcpLaunchSpec = { jobId: 'job_one', tag: 'vl-job_one', title: 'test', prompt: 'hello', preset: 'fake', permissionMode: 'ask',
    project: { alias: 'repo', repoRoot: repo, baseRef: 'master' }, branch: 'launch/job_one', env: {}, attachments: [] };
  const worktree = await allocate(spec, root, ctx);
  assert.equal(worktree.path, join(root, 'job_one')); assert.equal(worktree.branch, 'launch/job_one');
  assert.deepEqual(calls, [
    ['git', '-C', repo, 'rev-parse', '--verify', '--end-of-options', 'master^{commit}'],
    ['git', '-C', repo, 'worktree', 'add', '-b', 'launch/job_one', '--', join(root, 'job_one'), base],
  ]);
  calls.length = 0;
  const projectRoot = await allocate({ ...spec, workspaceMode: 'project-root' }, root, ctx);
  assert.equal(projectRoot.path, repo); assert.equal(projectRoot.branch, 'existing-branch');
  assert.deepEqual(calls, [['git', '-C', repo, 'branch', '--show-current']]);
  assert.equal(await readFile(marker, 'utf8'), 'keep existing checkout');
  calls.length = 0;
  const temporary = await allocate({ ...spec, workspaceMode: 'temp', project: { alias: 'any-scratch-name', repoRoot: '/does-not-exist', baseRef: '', scratch: true } }, root, ctx);
  assert.equal(temporary.path, join(root, 'job_one')); assert.equal(temporary.branch, undefined); assert.equal(calls.length, 0);
  await assert.rejects(allocate({ ...spec, workspaceMode: 'temp', project: { ...spec.project, scratch: true } }, root, ctx), /EEXIST/);
  await assert.rejects(allocate({ ...spec, jobId: '../escape', workspaceMode: 'temp' }, root, ctx), /invalid_job/);
  assert.throws(() => workspaceRoot('relative'), /absolute/);
});


test('real disposable Git: worktree branch/base and project-root preservation', async t => {
  const dir = await acpTestDir(t, 'vecnet-acp-git-');
  const repo = join(dir, 'repo'), root = join(dir, 'jobs'); await mkdir(repo);
  const exec = (argv: string[]): Promise<string> => new Promise((resolve, reject) => {
    execFile(argv[0], argv.slice(1), { timeout: 5000, env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' } }, (error, stdout) => {
      if (error) reject(new Error('disposable git failed')); else resolve(stdout.trim());
    });
  });
  await exec(['git', '-C', repo, 'init', '-b', 'existing-branch']);
  await writeFile(join(repo, 'tracked.txt'), 'keep me');
  await exec(['git', '-C', repo, 'add', 'tracked.txt']);
  await exec(['git', '-C', repo, '-c', 'user.name=ACP fixture', '-c', 'user.email=acp@example.invalid', '-c', 'commit.gpgsign=false', 'commit', '-m', 'fixture']);
  const sha = await exec(['git', '-C', repo, 'rev-parse', 'HEAD']);
  const ctx = { exec: async (argv: string[]) => ({ code: 0, stdout: await exec(argv), stderr: '' }) } as unknown as PluginContext;
  const spec: AcpLaunchSpec = { jobId: 'job_real', tag: 'vl-job_real', title: 'test', prompt: 'hello', preset: 'fake', permissionMode: 'ask',
    project: { alias: 'repo', repoRoot: repo, baseRef: 'existing-branch' }, branch: 'launch/job_real', env: {}, attachments: [] };
  const before = await exec(['git', '-C', repo, 'status', '--porcelain']);
  const ws = await allocate(spec, root, ctx);
  assert.equal(await exec(['git', '-C', ws.path, 'branch', '--show-current']), 'launch/job_real');
  assert.equal(await exec(['git', '-C', ws.path, 'rev-parse', 'HEAD']), sha);
  assert.equal(await exec(['git', '-C', repo, 'branch', '--show-current']), 'existing-branch');
  const rootWs = await allocate({ ...spec, workspaceMode: 'project-root' }, root, ctx);
  assert.equal(rootWs.path, repo); assert.equal(rootWs.branch, 'existing-branch');
  assert.equal(await exec(['git', '-C', repo, 'status', '--porcelain']), before);
  assert.equal(await readFile(join(repo, 'tracked.txt'), 'utf8'), 'keep me');
});


test('scratch flag, not alias spelling, gates temp and ordinary modes; canonical /tmp roots are refused before creation', async t => {
  const dir = await acpTestDir(t, 'vecnet-acp-scratch-');
  const repo = join(dir, 'repo'), root = join(dir, 'jobs'); await mkdir(repo);
  const calls: string[][] = [];
  const ctx = { exec: async (argv: string[]) => { calls.push(argv); return { code: 0, stdout: argv.includes('rev-parse') ? base : 'existing', stderr: '' }; } } as unknown as PluginContext;
  const spec = { jobId: 'job_scratch', tag: 'vl-job_scratch', title: 'test', prompt: 'hello', preset: 'fake', permissionMode: 'ask',
    project: { alias: 'temporary', repoRoot: repo, baseRef: 'master' }, branch: 'launch/job_scratch', env: {}, attachments: [] } as AcpLaunchSpec;
  await assert.rejects(allocate({ ...spec, workspaceMode: 'temp' }, root, ctx), /scratch/);
  await assert.rejects(allocate({ ...spec, workspaceMode: 'temp', project: { ...spec.project, scratch: false } } as AcpLaunchSpec, root, ctx), /scratch/);
  for (const workspaceMode of ['worktree', 'project-root'] as const) {
    await assert.rejects(allocate({ ...spec, workspaceMode, project: { ...spec.project, scratch: true } } as AcpLaunchSpec, root, ctx), /scratch/);
  }
  assert.equal(calls.length, 0);
  // A legacy ordinary repo literally called temporary remains ordinary.
  const ordinary = await allocate({ ...spec, workspaceMode: 'project-root' }, root, ctx);
  assert.equal(ordinary.path, repo);
  const scratch = { ...spec, workspaceMode: 'temp', project: { alias: 'any-scratch-name', repoRoot: '', baseRef: '', scratch: true } } as AcpLaunchSpec;
  const temp = await allocate(scratch, root, ctx); assert.equal(temp.path, join(root, 'job_scratch'));
  await assert.rejects(allocate({ ...scratch, jobId: 'job_forbidden' }, '/tmp/acp-must-not-create-this-root', ctx), /tmp/);
  const link = join(dir, 'to-system-temp'); await symlink('/tmp', link);
  await assert.rejects(allocate({ ...scratch, jobId: 'job_forbidden' }, join(link, 'acp-must-not-create-this-root'), ctx), /tmp/);
});
