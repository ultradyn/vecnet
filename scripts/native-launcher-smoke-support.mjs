// Host-side smoke gates. No launcher/runtime policy or real-account activation.
import { spawn as spawnChild } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { readFile, readdir, lstat } from 'node:fs/promises';
import { writeFileSync, renameSync, openSync, closeSync, fsyncSync, chmodSync } from 'node:fs';
import { join, resolve, basename, isAbsolute } from 'node:path';

const check = (ok, message) => { if (!ok) throw new Error(message); };
const sha = data => createHash('sha256').update(data).digest('hex');
const digest = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);

export function runHost(command, args, { spawn = spawnChild, commandEnv = process.env, input = '', timeoutMs = 60000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { env: commandEnv, stdio: ['pipe', 'pipe', 'pipe'] });
    let out = '', timedOut = false;
    const timer = setTimeout(() => { timedOut = true; child.kill('SIGKILL'); }, timeoutMs);
    child.stdout.on('data', data => { out += data; if (out.length > 8 * 1024 * 1024) { timedOut = true; child.kill('SIGKILL'); } });
    child.stderr.on('data', () => {}); // Captured provider/config output never enters errors.
    child.stdin.on('error', () => {});
    child.once('error', () => { clearTimeout(timer); reject(new Error('host command spawn failed')); });
    child.once('close', (code, signal) => {
      clearTimeout(timer);
      if (timedOut || signal || code !== 0) reject(new Error('host command failed: ' + (timedOut ? 'timeout' : signal ? 'signal' : 'nonzero exit')));
      else resolve(out);
    });
    child.stdin.end(input);
  });
}

// Synchronous atomic/fsynced writes close the signal/dispatch journal gap.
// This directory belongs only to this run; never pass a real profile directory.
export function createSmokeJournal(dir, initial) {
  chmodSync(dir, 0o700);
  let value = { version: 1, ...initial };
  function privateFile(name, data) {
    const target = join(dir, name), temporary = target + '.' + randomBytes(8).toString('hex');
    writeFileSync(temporary, JSON.stringify(data, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
    const fd = openSync(temporary, 'r'); try { fsyncSync(fd); } finally { closeSync(fd); }
    renameSync(temporary, target);
    const parent = openSync(dir, 'r'); try { fsyncSync(parent); } finally { closeSync(parent); }
  }
  const save = () => privateFile('recovery.json', value);
  save();
  return {
    privateFile,
    update(patch) { value = { ...value, ...patch }; save(); },
    request(body) {
      check(body?.idempotency_key && body.job_request?.job_key === body.idempotency_key && Number.isFinite(Date.parse(body.expires_at)), 'invalid smoke request identity');
      check(!value.request || JSON.stringify(value.request) === JSON.stringify(body), 'refuse replacement recovery request');
      value = { ...value, request: structuredClone(body) }; save();
    },
    accepted(reply) {
      check(['job_id', 'thread_id', 'message_id'].every(k => typeof reply?.[k] === 'string' && reply[k]), 'unreadable accepted launch identifiers');
      const accepted = Object.fromEntries(['job_id', 'thread_id', 'message_id'].map(k => [k, reply[k]]));
      check(!value.accepted || JSON.stringify(value.accepted) === JSON.stringify(accepted), 'retry changed accepted launch identifiers');
      value = { ...value, accepted }; save(); return accepted;
    },
    snapshot: () => structuredClone(value),
  };
}

const assetRoots = ['connectors/launcher/src', 'connectors/launcher/schema', 'connectors/launcher/package.json',
  'connectors/hub-client/src', 'connectors/hub-client/package.json', 'connectors/launcher/systemd/vecnet-launcher-profile.service.in'];
// Same byte framing and complete runtime graph as launcher install's snapshot.
export async function smokeSnapshotDigest(root) {
  const files = new Map();
  async function walk(name) {
    const path = join(root, name), info = await lstat(path);
    if (info.isDirectory()) for (const entry of await readdir(path)) await walk(name + '/' + entry);
    else { check(info.isFile() && !info.isSymbolicLink(), 'runtime inventory must contain regular files'); files.set(name, await readFile(path)); }
  }
  for (const name of assetRoots) await walk(name);
  const hash = createHash('sha256');
  for (const name of [...files.keys()].sort()) { const bytes = files.get(name); hash.update(`${Buffer.byteLength(name)}:${name}:${bytes.length}:`); hash.update(bytes); }
  return hash.digest('hex');
}

export async function verifySmokeApproval({ path, source, bin, demoBin, backend, acpCommand, run }) {
  check(path && isAbsolute(path), 'absolute approved smoke manifest required');
  const bytes = await readFile(path), approved = JSON.parse(bytes);
  check(/^[a-f0-9]{40}$/.test(approved.source_revision) && ['snapshot_sha256', 'cli_sha256', 'demo_sha256'].every(k => digest(approved[k])), 'incomplete approved artifact identity');
  const sourceRevision = (await run('git', ['-C', source, 'rev-parse', 'HEAD'])).trim();
  check(sourceRevision === approved.source_revision, 'approved source revision mismatch');
  const snapshot = await smokeSnapshotDigest(source);
  check(snapshot === approved.snapshot_sha256, 'approved source runtime inventory mismatch');
  const cliDigest = sha(await readFile(bin)), demoDigest = sha(await readFile(demoBin));
  check(cliDigest === approved.cli_sha256 && demoDigest === approved.demo_sha256, 'approved CLI/demo digest mismatch');
  const runtime = approved[backend === 'acp' ? 'acp' : 'native'];
  check(runtime && isAbsolute(runtime.executable || '') && digest(runtime.executable_sha256), 'approved runtime identity missing');
  check(sha(await readFile(runtime.executable)) === runtime.executable_sha256, 'approved runtime executable digest mismatch');
  if (backend === 'acp') {
    check(resolve(acpCommand) === resolve(runtime.executable), 'ACP command differs from approved executable');
    check(isAbsolute(runtime.package_json || '') && runtime.package_version === '0.101.0', 'approved ACP package identity missing');
    const pkg = JSON.parse(await readFile(runtime.package_json));
    check(pkg.version === runtime.package_version && pkg.name === runtime.package_name, 'approved ACP package version/name mismatch');
    check(typeof runtime.agent_name === 'string' && runtime.agent_name && typeof runtime.agent_version === 'string' && runtime.agent_version && runtime.protocol_version === 1, 'approved ACP handshake identity missing');
    for (const [pathKey, hashKey] of [['entry', 'entry_sha256'], ['node_executable', 'node_sha256']]) {
      check(isAbsolute(runtime[pathKey] || '') && digest(runtime[hashKey]) && sha(await readFile(runtime[pathKey])) === runtime[hashKey], 'approved ACP entry/Node digest mismatch');
    }
  } else check(typeof runtime.version === 'string' && runtime.version, 'approved native version missing');
  return { approved, provenance: { approval_sha256: sha(bytes), source_revision: sourceRevision, snapshot_sha256: snapshot,
    cli_sha256: cliDigest, demo_sha256: demoDigest, runtime: { ...runtime }, provider_availability: 'not-yet-observed',
    effective_provider: null, effective_model: null, effective_effort: null,
    attestation_limit: backend === 'acp' ? 'ACP does not attest the effective provider, model or effort; approved package/bytes and actual handshake bind the adapter only.' : 'RPC reports the daemon version and selected route; the approved executable digest records the controller-inventoried daemon binary.' } };
}

export async function verifyInstalledSnapshot(installation, identity, source, bin) {
  const runtime = installation?.runtime;
  check(runtime && resolve(runtime.source) === resolve(source) && resolve(runtime.vecnet_bin) === resolve(bin), 'installed source/CLI differs from approval');
  check(basename(runtime.snapshot) === identity.provenance.snapshot_sha256 && resolve(runtime.entry) === join(resolve(runtime.snapshot), 'connectors/launcher/src/main.ts'), 'installed snapshot identity mismatch');
  check(await smokeSnapshotDigest(runtime.snapshot) === identity.provenance.snapshot_sha256, 'installed snapshot bytes mismatch');
  identity.provenance.snapshot_path = runtime.snapshot;
}

export async function probeSmokeRuntime(identity, { backend, run, openRpc }) {
  const expected = identity.approved[backend === 'acp' ? 'acp' : 'native'];
  if (backend === 'acp') {
    const result = await run(expected.executable, [], { input: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: 1, clientCapabilities: {}, clientInfo: { name: 'vecnet-smoke', version: '1' } } }) + '\n' });
    const init = result.trim().split('\n').map(line => JSON.parse(line)).find(x => x.id === 1)?.result;
    check(init?.protocolVersion === 1 && init.protocolVersion === expected.protocol_version && init.agentInfo?.name === expected.agent_name && init.agentInfo?.version === expected.agent_version && !/codex/i.test(init.agentInfo.name), 'approved ACP handshake/version mismatch');
    identity.provenance.runtime = { ...identity.provenance.runtime, agent_name: init.agentInfo.name, agent_version: init.agentInfo.version, protocol_version: init.protocolVersion };
  } else {
    const rpc = await openRpc();
    try { check(rpc.version === expected.version, 'approved native daemon version mismatch'); identity.provenance.runtime.version = rpc.version; }
    finally { rpc.close(); }
  }
}

export async function repositoryState(repo, run) {
  return {
    branch: (await run('git', ['-C', repo, 'symbolic-ref', '--short', 'HEAD'])).trim(),
    head: (await run('git', ['-C', repo, 'rev-parse', 'HEAD'])).trim(),
    status: await run('git', ['-C', repo, 'status', '--porcelain=v1', '-z', '--untracked-files=all']),
    tracked: sha(await readFile(join(repo, 'README.md'))), untracked: sha(await readFile(join(repo, 'sentinel.untracked'))),
  };
}

export async function verifySmokeWorkspace({ before, repo, run, session, backend, mode, expected, jobId }) {
  const after = await repositoryState(repo, run);
  check(JSON.stringify(before) === JSON.stringify(after), 'parent repository branch/HEAD/dirty/untracked state changed');
  check(session.backend === backend && session.extra.workspace === expected && session.extra.workspace_mode === mode, 'execution workspace differs from selection');
  if (mode === 'temp') check((await lstat(expected)).isDirectory(), 'temporary workspace directory missing');
  if (mode === 'temp' && backend === 'acp') {
    const git = await lstat(join(expected, '.git')).then(() => true, error => { if (error.code === 'ENOENT') return false; throw error; });
    check(!git && session.extra.branch == null, 'temporary workspace unexpectedly has Git state');
  } else {
    if (mode === 'temp') check((await lstat(join(expected, '.git'))).isDirectory(), 'native temporary Git repository missing');
    const wanted = mode === 'project-root' ? before.branch : 'launch/' + jobId;
    check(session.extra.branch === wanted && (await run('git', ['-C', expected, 'symbolic-ref', '--short', 'HEAD'])).trim() === wanted, 'workspace branch differs from selected contract');
    if (mode === 'worktree') check((await run('git', ['-C', expected, 'rev-parse', 'HEAD'])).trim() === before.head, 'worktree HEAD differs from base ref');
  }
  return { backend, mode, git_expected: mode !== 'temp' || backend !== 'acp', before, after };
}

export async function settleSmokeJob({ job, launcherId, history, cancel, until }) {
  const exact = m => m.sender === launcherId && m.thread_id === job.thread_id && m.job_status?.job_id === job.job_id;
  const terminal = messages => messages.find(m => exact(m) && ['failed', 'succeeded', 'cancelled'].includes(m.job_status.state));
  let ended = terminal(await history()), cancellationRequest = null;
  if (!ended) {
    cancellationRequest = await cancel();
    ended = await until(async () => terminal(await history()), 'exact owned job terminal acknowledgement', 30000);
  }
  check(ended?.message_id && exact(ended), 'owned terminal acknowledgement missing');
  return { state: ended.job_status.state, request_message_id: cancellationRequest?.message_id || null,
    acknowledgement: { message_id: ended.message_id, job_id: ended.job_status.job_id, sender: ended.sender, thread_id: ended.thread_id } };
}

export function finalizeSmokeReceipt(evidence, { safeToRemove, cancellation, provenance, workspaceProof, job }) {
  check(safeToRemove, 'owned teardown is not complete');
  check(cancellation?.state === 'cancelled' && cancellation.acknowledgement?.job_id === job.job_id, 'cancellation scenario not proven: observed ' + (cancellation?.state || 'unknown'));
  check(workspaceProof && provenance?.runtime, 'workspace/runtime evidence missing');
  return { ...evidence, pass: true, launch: job, cancellation, teardown: { complete: true }, provenance, workspace_proof: workspaceProof };
}
