import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFile, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { npmInvocation } from './npm-command.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const registry = 'https://registry.npmjs.org/';

export function releaseInfo(manifest) {
  const version = manifest.version;
  const match = typeof version === 'string' && /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/.exec(version);
  if (manifest.name !== 'vecnet' || !match || match.slice(1, 4).some(part => !Number.isSafeInteger(Number(part)))
      || match[4]?.split('.').some(part => /^\d+$/.test(part) && part.length > 1 && part.startsWith('0'))) {
    throw new Error('Expected package vecnet with a canonical npm semver version');
  }
  return { version, tag: `v${version}`, npmTag: match[4] ? 'next' : 'latest', tarball: `vecnet-${version}.tgz` };
}

export function checkTag(tag, info) {
  if (tag !== info.tag) throw new Error(`Tag ${tag} does not match npm/package.json version ${info.version}`);
}

export function checkPublished(existing, info, integrity) {
  if (existing.name !== 'vecnet' || existing.version !== info.version || existing.dist?.integrity !== integrity) {
    throw new Error(`vecnet@${info.version} already exists with different contents; bump the package version`);
  }
}

export async function publish(info, {
  cwd = root, fetch = globalThis.fetch, exec = execFileSync, now = Date.now,
  sleep = ms => new Promise(resolve => setTimeout(resolve, ms)),
  source, output, summary,
} = {}) {
  if ((output || summary) && !/^[0-9a-f]{40}$/.test(source ?? '')) {
    throw new Error('Publication receipt requires the full source commit SHA');
  }
  // Leave startup/exit headroom inside the40s CI owner. That owner also
  // enforces the inherited absolute overall run deadline.
  const deadline = now() + 38_000;
  const remaining = () => {
    const ms = deadline - now();
    if (ms <= 0) throw new Error('npm publication budget exhausted; check registry before rerunning');
    return ms;
  };
  const archive = path.join(cwd, 'dist', info.tarball);
  const integrity = `sha512-${createHash('sha512').update(await readFile(archive)).digest('base64')}`;
  const record = async (state, message) => {
    const receipt = { state, source, version: info.version, tarball: info.tarball, integrity };
    console.log(message);
    if (summary) await appendFile(summary, `## npm publication: ${state}\n\n${message}\n\n\`\`\`json\n${JSON.stringify(receipt, null, 2)}\n\`\`\`\n`);
    return receipt;
  };
  const complete = async state => {
    const message = state === 'pending'
      ? 'npm accepted the upload, but it is not yet verified visible in the public registry. Keep the GitHub Release as a draft. Do not republish this version; verify the public tarball against the tested artifact before finalizing the draft or installing.'
      : 'Registry metadata matches the tested artifact integrity. An existing draft still requires controller verification of the public tarball before finalization or installation.';
    const receipt = await record(state, message);
    if (output) await appendFile(output, `state=${state}\n`);
    return receipt;
  };
  const response = await fetch(`${registry}vecnet/${info.version}`, {
    headers: { 'Cache-Control': 'no-cache' }, signal: AbortSignal.timeout(Math.min(5_000, remaining())),
  });
  if (response.ok) {
    checkPublished(await response.json(), info, integrity);
    console.log(`vecnet@${info.version} is already published with the same tarball; continuing release recovery`);
    return complete('verified');
  }
  if (response.status !== 404) throw new Error(`npm version lookup failed with HTTP ${response.status}`);
  const npm = npmInvocation([
    'publish', archive, '--ignore-scripts', '--access', 'public', '--tag', info.npmTag,
    '--registry', registry,
  ]);
  exec(npm.file, npm.args, { cwd, stdio: 'inherit', windowsHide: true, timeout: remaining() });
  await record('accepted', 'npm accepted the upload; registry visibility has not been verified. Do not republish this version, even if verification or a later release step fails.');
  // Only registry visibility is polled after a successful upload. Never retry
  // publication, even if the registry has not exposed the tested bytes yet.
  try {
    const visibilityDeadline = Math.min(deadline, now() + 5_000);
    while (now() < visibilityDeadline) {
      const published = await fetch(`${registry}vecnet/${info.version}?release-check=${now()}`, {
        headers: { 'Cache-Control': 'no-cache' }, signal: AbortSignal.timeout(Math.min(2_000, visibilityDeadline - now())),
      });
      if (published.ok) {
        checkPublished(await published.json(), info, integrity);
        console.log(`Verified vecnet@${info.version} is available from npm with the tested integrity`);
        return complete('verified');
      }
      if (published.status !== 404) throw new Error(`npm publication verification failed with HTTP ${published.status}`);
      console.log('Waiting briefly for npm registry visibility');
      const delay = Math.min(500, visibilityDeadline - now());
      if (delay > 0) await sleep(delay);
    }
    return complete('pending');
  } catch (error) {
    try {
      await record('verification-failed', `npm accepted the upload, but verification failed: ${error?.message ?? String(error)}. Do not republish this version; inspect the acceptance receipt and verify public bytes before recovery.`);
    } catch (receiptError) {
      console.error(`Could not record verification failure: ${receiptError.message}`);
    }
    throw error;
  }
}

async function main() {
  const info = releaseInfo(JSON.parse(await readFile(path.join(root, 'npm/package.json'), 'utf8')));
  const [command, argument, ...extra] = process.argv.slice(2);
  if (extra.length || (command !== 'check-tag' && argument)) throw new Error('Unexpected release command arguments');
  switch (command) {
    case 'info':
      if (process.env.GITHUB_OUTPUT) {
        await appendFile(process.env.GITHUB_OUTPUT, `version=${info.version}\ntag=${info.tag}\nnpm-tag=${info.npmTag}\ntarball=${info.tarball}\n`);
      }
      break;
    case 'check-tag':
      checkTag(argument, info);
      // Only release commits already incorporated in the default branch.
      execFileSync('git', ['merge-base', '--is-ancestor', 'HEAD', 'refs/remotes/origin/master'], { cwd: root, stdio: 'inherit' });
      break;
    case 'publish':
      checkTag(process.env.GITHUB_REF_NAME, info);
      if (process.env.GITHUB_EVENT_NAME !== 'push' || process.env.GITHUB_REF_TYPE !== 'tag') {
        throw new Error('Publishing requires a pushed version tag');
      }
      await publish(info, {
        source: process.env.GITHUB_SHA, output: process.env.GITHUB_OUTPUT, summary: process.env.GITHUB_STEP_SUMMARY,
      });
      break;
    default:
      throw new Error('Usage: node scripts/npm/release.mjs info|check-tag TAG|publish');
  }
  console.log(JSON.stringify(info));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
