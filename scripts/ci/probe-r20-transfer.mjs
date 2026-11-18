// Disposable probe plumbing; candidate transfer and extraction run inside budget.mjs.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createWriteStream } from 'node:fs';
import { appendFile, mkdir, unlink } from 'node:fs/promises';
import { pipeline } from 'node:stream/promises';

const env = process.env;
const api = `${env.GITHUB_API_URL}/repos/${env.GITHUB_REPOSITORY}/actions`;
const run = `${api}/runs/${env.GITHUB_RUN_ID}`;

async function request(url, options = {}) {
  assert(env.GH_TOKEN, 'GitHub Actions API token missing');
  return fetch(url, {
    headers: { Authorization: `Bearer ${env.GH_TOKEN}`, Accept: 'application/vnd.github+json' },
    redirect: 'error',
    ...options,
  });
}

async function deadline() {
  const response = await request(run, { signal: AbortSignal.timeout(5000) });
  assert(response.ok, `GitHub run lookup failed (${response.status})`);
  const current = await response.json();
  assert(current.head_sha === env.GITHUB_SHA && current.run_attempt === 1 && env.GITHUB_RUN_ATTEMPT === '1',
    'Expected original diagnostic attempt at this SHA');
  const cutoff = Date.parse(current.created_at) + 280_000;
  assert(Number.isSafeInteger(cutoff) && cutoff > Date.now(), 'Diagnostic overall budget exhausted or invalid');
  await appendFile(env.GITHUB_ENV, `CI_RUN_DEADLINE=${cutoff}\n`);
  console.log(`Original run-created cutoff: ${new Date(cutoff).toISOString()}`);
}

async function candidate() {
  assert(Number(env.CI_RUN_DEADLINE) > Date.now(), 'Diagnostic deadline missing or exhausted');
  assert(['win32-x64', 'win32-arm64'].includes(env.PLATFORM), 'Expected Windows CLI artifact platform');
  assert.match(env.CANDIDATE_ARTIFACT_ID ?? '', /^[0-9]+$/, 'Matching producer artifact ID missing');
  const name = `cli-fixture-${env.PLATFORM}`;
  const response = await request(`${run}/artifacts?name=${name}`);
  assert(response.ok, `Candidate artifact lookup failed (${response.status})`);
  const { artifacts } = await response.json();
  const artifact = artifacts.find(row => String(row.id) === env.CANDIDATE_ARTIFACT_ID && row.name === name);
  assert(artifact && artifact.expired === false, 'Matching producer artifact not available in this run');

  const redirect = await request(`${api}/artifacts/${artifact.id}/zip`, { redirect: 'manual' });
  const location = redirect.headers.get('location');
  assert(redirect.status === 302 && location?.startsWith('https://'),
    `Candidate artifact ZIP redirect missing (${redirect.status})`);
  // The signed blob URL is never logged or sent the GitHub token.
  const zip = '.r20-fixture/candidate.zip';
  const dir = 'dist/cli-fixture';
  const download = await fetch(location).catch(() => { throw new Error('Candidate ZIP request failed'); });
  assert(download.ok && download.body, `Candidate ZIP download failed (${download.status})`);
  await pipeline(download.body, createWriteStream(zip, { flags: 'wx' }));
  await mkdir(dir, { recursive: true });
  execFileSync('tar.exe', ['-xf', zip, '-C', dir], { stdio: 'inherit' });
  await unlink(zip);
  console.log(`Extracted ${name}, artifact ${artifact.id}, from run ${env.GITHUB_RUN_ID}`);
}

switch (process.argv[2]) {
  case 'deadline': await deadline(); break;
  case 'candidate': await candidate(); break;
  default: throw new Error('expected deadline or candidate');
}
