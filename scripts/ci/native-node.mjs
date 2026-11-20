// Go consumers need native Node 24, not package-manager discovery or installation.
import { execFileSync } from 'node:child_process';
import { appendFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { fileURLToPath } from 'node:url';

const identityProbe = 'JSON.stringify({version:process.version,arch:process.arch,platform:process.platform})';

export async function selectNativeNode({ cache, arch, platform = process.platform, githubPath } = {}) {
  // Share the existing strict step budget across cache lookup and all probes.
  const deadline = performance.now() + 53_000;
  if (!cache || !githubPath || !['x64', 'arm64'].includes(arch) || !['linux', 'win32'].includes(platform)) {
    throw new Error('native Node cache context required: RUNNER_TOOL_CACHE, RUNNER_ARCH (X64/ARM64), GITHUB_PATH, and Linux or Windows');
  }
  const root = path.resolve(cache, 'node');
  if (/[\r\n]/.test(root)) throw new Error('native Node cache path cannot be published to GITHUB_PATH');
  const missing = `native Node 24 missing from hosted cache for ${platform}-${arch}: ${root}`;
  let entries;
  try {
    entries = await readdir(root);
  } catch (error) {
    if (error.code === 'ENOENT') throw new Error(missing, { cause: error });
    throw error;
  }

  const versions = entries.filter(name => /^24\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(name))
    .map(name => {
      const [, minor, patch] = name.split('.');
      return { name, minor: BigInt(minor), patch: BigInt(patch) };
    }).sort((a, b) => Number(b.minor - a.minor) || Number(b.patch - a.patch));

  for (const { name } of versions) {
    const slot = path.join(root, name, arch);
    const bin = platform === 'win32' ? slot : path.join(slot, 'bin');
    const executable = path.join(bin, platform === 'win32' ? 'node.exe' : 'node');
    const remaining = Math.floor(deadline - performance.now());
    if (remaining <= 0) throw new Error('native Node selection budget exhausted');
    let output;
    try {
      output = execFileSync(executable, ['-p', identityProbe], {
        encoding: 'utf8', timeout: remaining, killSignal: 'SIGKILL', windowsHide: true,
      });
    } catch (error) {
      // Hosted images can have a newer version for a different architecture only.
      if (error.code === 'ENOENT') continue;
      throw error;
    }
    const actual = JSON.parse(output);
    if (actual?.version !== `v${name}` || actual.arch !== arch || actual.platform !== platform) {
      throw new Error(`cached Node identity mismatch at ${executable}: expected v${name} ${platform}-${arch}, got ${JSON.stringify(actual)}`);
    }
    await appendFile(githubPath, `${bin}\n`);
    console.log(`native Node ${actual.version} ${platform}-${arch}: ${executable}`);
    return executable;
  }
  throw new Error(missing);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const { RUNNER_TOOL_CACHE: cache, RUNNER_ARCH, GITHUB_PATH: githubPath } = process.env;
  const arch = { X64: 'x64', ARM64: 'arm64' }[RUNNER_ARCH];
  await selectNativeNode({ cache, arch, githubPath });
}
