import { execFileSync } from 'node:child_process';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { nativeTargets } from './platforms.mjs';

// Include the union of modules linked on every supported OS and architecture,
// including Windows-only dependencies.
export async function writeNotices({ root, destination, workers = '2' }) {
  const env = { ...process.env, CGO_ENABLED: '0', GOMAXPROCS: workers, GOFLAGS: `${process.env.GOFLAGS || ''} -p=${workers}`.trim() };
  const go = (args, targetEnv = {}) => execFileSync('go', args, {
    cwd: root, env: { ...env, ...targetEnv }, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024,
  }).trim();
  const modules = new Map();
  for (const { goos: GOOS, goarch: GOARCH } of nativeTargets) {
    const rows = go([
      'list', '-mod=readonly', '-deps', '-f',
      '{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}\t{{.Module.Version}}\t{{.Module.Dir}}{{end}}{{end}}',
      './cmd/vecnet', './cmd/vecnet-hub',
    ], { GOOS, GOARCH });
    for (const row of rows.split(/\r?\n/).filter(Boolean)) {
      const [name, version, directory] = row.split('\t');
      if (!name || !version || !directory) throw new Error(`Incomplete module license inventory: ${name}`);
      modules.set(`${name}@${version}`, { name, version, directory });
    }
  }

  const goroot = go(['env', 'GOROOT']);
  let goLicense;
  for (const candidate of [path.join(goroot, 'LICENSE'), '/usr/share/licenses/go/LICENSE']) {
    try {
      goLicense = await readFile(candidate, 'utf8');
      break;
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
  }
  if (!goLicense) throw new Error(`Go standard library LICENSE missing from ${goroot}`);
  const sections = [
    'Third-party notices for the native binaries distributed with vecnet.\nThese licenses apply to the named dependencies; they do not license vecnet itself.',
    `Go standard library (${go(['env', 'GOVERSION'])})\n\n${goLicense.trim()}`,
  ];
  for (const [identifier, { directory }] of [...modules].sort(([a], [b]) => a.localeCompare(b, 'en'))) {
    const files = (await readdir(directory, { withFileTypes: true }))
      .filter(entry => entry.isFile() && /^(?:licen[cs]e|copying|notice)(?:$|[._-])/i.test(entry.name))
      .map(entry => entry.name).sort();
    if (!files.some(file => /^(?:licen[cs]e|copying)(?:$|[._-])/i.test(file))) {
      throw new Error(`Missing dependency license: ${identifier}`);
    }
    const notices = await Promise.all(files.map(async file => `${file}\n\n${(await readFile(path.join(directory, file), 'utf8')).trim()}`));
    sections.push(`${identifier}\n\n${notices.join('\n\n')}`);
  }
  await writeFile(destination, `${sections.join(`\n\n${'='.repeat(78)}\n\n`)}\n`);
  console.log(`Included Go and ${modules.size} dependency modules in third-party notices`);
}
