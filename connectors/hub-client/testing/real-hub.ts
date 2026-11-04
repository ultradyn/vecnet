// A real bin/vecnet-hub child process for end-to-end tests: a fresh data
// directory, the operator from `init`, and peers enrolled over HTTP. Build the
// binary first (`just build`; `just ln-e2e` does). Node built-ins only.
import { spawn } from 'node:child_process';
import type { ChildProcess } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export interface RealHub {
  url: string;
  operator: { peerId: string; secret: string };
  addPeer(kind: string, name: string): Promise<{ peerId: string; secret: string }>;
  stop(): Promise<void>;
}

const START_TIMEOUT_MS = 30_000;
const STOP_TIMEOUT_MS = 15_000;
const DEFAULT_BIN = fileURLToPath(new URL('../../../bin/vecnet-hub', import.meta.url));

/** The child environment: every VECNET_* variable removed, so a developer shell cannot move the hub. */
function childEnv(): NodeJS.ProcessEnv {
  return Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^VECNET_/i.test(key)));
}

function tail(chunks: string[]): string { return chunks.join('').slice(-2000); }

function runInit(bin: string, dataDir: string, env: NodeJS.ProcessEnv): Promise<{ peerId: string; secret: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(bin, ['init', '--data-dir', dataDir, '--operator-name', 'Max'], { env, stdio: ['ignore', 'pipe', 'pipe'] });
    const out: string[] = [], err: string[] = [];
    child.stdout.setEncoding('utf8').on('data', (c: string) => out.push(c));
    child.stderr.setEncoding('utf8').on('data', (c: string) => err.push(c));
    child.on('error', () => reject(new Error(`cannot run ${bin}; build it with \`just build\``)));
    child.on('close', code => {
      const text = out.join('');
      const peer = /^operator peer:\s+(peer_\S+)\s*$/m.exec(text)?.[1];
      const secret = /^operator secret:\s+(\S+)\s*$/m.exec(text)?.[1];
      if (code !== 0 || !peer || !secret) { reject(new Error(`vecnet-hub init failed (exit ${code}): ${tail(err)}`)); return; }
      resolve({ peerId: peer, secret });
    });
  });
}

function serve(bin: string, dataDir: string, env: NodeJS.ProcessEnv): Promise<{ child: ChildProcess; url: string; exited: Promise<void> }> {
  return new Promise((resolve, reject) => {
    const child = spawn(bin, ['--listen', '127.0.0.1:0', '--data-dir', dataDir], { env, stdio: ['ignore', 'pipe', 'pipe'] });
    const err: string[] = [];
    let buffered = '', done = false;
    const exited = new Promise<void>(r => child.once('exit', () => r()));
    const timer = setTimeout(() => fail(new Error(`vecnet-hub did not start within ${START_TIMEOUT_MS} ms: ${tail(err)}`)), START_TIMEOUT_MS);
    function fail(e: Error) {
      if (done) return;
      done = true; clearTimeout(timer);
      child.kill('SIGKILL');
      reject(e);
    }
    // Keep draining both pipes for the child's whole life, so it never blocks on a full pipe.
    child.stderr.setEncoding('utf8').on('data', (c: string) => { err.push(c); if (err.length > 64) err.splice(0, err.length - 64); });
    child.stdout.setEncoding('utf8').on('data', (c: string) => {
      if (done) return;
      buffered += c;
      const addr = /listening on http:\/\/(\S+)/.exec(buffered)?.[1];
      if (addr) {
        done = true; clearTimeout(timer);
        resolve({ child, url: `http://${addr}`, exited });
      }
    });
    child.on('error', () => fail(new Error(`cannot run ${bin}; build it with \`just build\``)));
    child.on('exit', code => fail(new Error(`vecnet-hub exited (${code}) before listening: ${tail(err)}`)));
  });
}

async function call(url: string, path: string, body: unknown, bearer?: string): Promise<Record<string, unknown>> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (bearer) headers.Authorization = `Bearer ${bearer}`;
  const res = await fetch(new URL(path, url), { method: 'POST', headers, body: JSON.stringify(body), redirect: 'error' });
  const text = await res.text();
  // Error bodies carry a code and message, never a secret; success bodies are not echoed.
  if (!res.ok) throw new Error(`${path}: HTTP ${res.status} ${text.slice(0, 300)}`);
  return JSON.parse(text) as Record<string, unknown>;
}

export async function startRealHub(opts: { bin?: string } = {}): Promise<RealHub> {
  const bin = opts.bin ?? DEFAULT_BIN;
  const env = childEnv();
  const dataDir = mkdtempSync(join(tmpdir(), 'vecnet-real-hub-'));
  let server: Awaited<ReturnType<typeof serve>>;
  let operator: { peerId: string; secret: string };
  try {
    operator = await runInit(bin, dataDir, env);
    server = await serve(bin, dataDir, env);
  } catch (err) {
    rmSync(dataDir, { recursive: true, force: true });
    throw err;
  }
  const { child, url, exited } = server;
  let stopping: Promise<void> | undefined;
  return {
    url,
    operator,
    async addPeer(kind, name) {
      const added = await call(url, '/v1/admin/peers', { kind, name }, operator.secret);
      if (typeof added.enrol_token !== 'string' || !added.enrol_token) throw new Error('/v1/admin/peers: no enrol_token');
      const enrolled = await call(url, '/v1/enrol', { token: added.enrol_token });
      if (typeof enrolled.peer_id !== 'string' || typeof enrolled.secret !== 'string') throw new Error('/v1/enrol: unexpected body');
      return { peerId: enrolled.peer_id, secret: enrolled.secret };
    },
    stop() {
      stopping ??= (async () => {
        if (child.exitCode === null && child.signalCode === null) {
          child.kill('SIGTERM');
          const timer = setTimeout(() => child.kill('SIGKILL'), STOP_TIMEOUT_MS);
          await exited;
          clearTimeout(timer);
        }
        rmSync(dataDir, { recursive: true, force: true });
      })();
      return stopping;
    },
  };
}
