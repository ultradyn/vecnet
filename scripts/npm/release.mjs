import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFile, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

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

async function publish(info) {
  const archive = path.join(root, 'dist', info.tarball);
  const integrity = `sha512-${createHash('sha512').update(await readFile(archive)).digest('base64')}`;
  const response = await fetch(`${registry}vecnet/${info.version}`, {
    headers: { 'Cache-Control': 'no-cache' }, signal: AbortSignal.timeout(30_000),
  });
  if (response.ok) {
    checkPublished(await response.json(), info, integrity);
    console.log(`vecnet@${info.version} is already published with the same tarball; continuing release recovery`);
    return;
  }
  if (response.status !== 404) throw new Error(`npm version lookup failed with HTTP ${response.status}`);
  execFileSync('npm', [
    'publish', archive, '--ignore-scripts', '--access', 'public', '--tag', info.npmTag,
    '--registry', registry,
  ], { cwd: root, stdio: 'inherit' });
  // npm can accept an upload before its asynchronous processing completes.
  // Do not report a successful release until the tested artifact is served.
  const deadline = Date.now() + 5 * 60_000;
  while (Date.now() < deadline) {
    const published = await fetch(`${registry}vecnet/${info.version}?release-check=${Date.now()}`, {
      headers: { 'Cache-Control': 'no-cache' }, signal: AbortSignal.timeout(15_000),
    });
    if (published.ok) {
      checkPublished(await published.json(), info, integrity);
      console.log(`Verified vecnet@${info.version} is available from npm with the tested integrity`);
      return;
    }
    if (published.status !== 404) throw new Error(`npm publication verification failed with HTTP ${published.status}`);
    console.log('Waiting for npm to finish processing the release');
    await new Promise(resolve => setTimeout(resolve, 10_000));
  }
  throw new Error('npm accepted the upload but did not serve the release within five minutes; rerun this workflow');
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
      await publish(info);
      break;
    default:
      throw new Error('Usage: node scripts/npm/release.mjs info|check-tag TAG|publish');
  }
  console.log(JSON.stringify(info));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
