// Only CI scheduling lives here; assertions and platform selection remain in
// the original suites. Run this whole command under budget.mjs, not each child
// separately: discovery/compilation and all packages share the step deadline.
import { execFileSync } from 'node:child_process';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { globSync } from 'node:fs';
import { partition, goInventory } from './partition.mjs';

const [kind, laneText = '0', groupText = '0'] = process.argv.slice(2);
const lane = Number(laneText), group = Number(groupText);
const go = args => execFileSync('go', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] });
if (kind === 'prepare-go') {
  const stages = [
    ['mod', 'download'],
    ['build','-mod=readonly','-p','2','-trimpath','std'],
    ['build','-mod=readonly','-p','2','-trimpath','modernc.org/sqlite'],
    ['build','-mod=readonly','-p','2','-trimpath','charm.land/bubbletea/v2','github.com/santhosh-tekuri/jsonschema/v6','github.com/modelcontextprotocol/go-sdk/mcp'],
  ];
  if (!stages[lane]) throw new Error('invalid prepare stage');
  process.stdout.write(go(stages[lane]));
} else if (kind === 'discover-go') {
  partition([], lane, 6);
  const packages=go(['list','-mod=readonly','./...']).trim().split(/\r?\n/).filter(Boolean);
  const selected=partition(packages,lane,6);
  const inventory = selected.length ? goInventory(go(['test', '-mod=readonly', '-p', '2', '-trimpath', '-list', '.', ...selected])) : [];
  await mkdir('.ci', { recursive: true });
  await writeFile(`.ci/go-${lane}.json`, JSON.stringify(inventory));
  console.log(`Go inventory: ${inventory.length} packages, ${inventory.reduce((n, p) => n + p.names.length, 0)} root tests/examples/fuzz targets`);
} else if (kind === 'go') {
  partition([], lane, 3); partition([], group, 8);
  const inventory = (await Promise.all(Array.from({length:6},(_,i)=>readFile(`.ci/go-${i}.json`,'utf8')))).flatMap(JSON.parse).sort((a,b)=>a.pkg.localeCompare(b.pkg,'en'));
  if(new Set(inventory.map(p=>p.pkg)).size!==inventory.length)throw new Error('duplicate package inventory');
  let roots = 0, packages = 0;
  for (const [ordinal, { pkg, names }] of inventory.entries()) {
    // Large packages are split by complete root name, never by subtest. Small
    // packages run unfiltered once, including packages with no test files.
    const split = names.length >= 100;
    const selected = split ? partition(names, group * 3 + lane, 24) : names;
    if (split ? !selected.length : ordinal % 24 !== group * 3 + lane) continue;
    const filter = split ? ['-run', `^(${selected.join('|')})$`] : [];
    execFileSync('go', ['test', '-mod=readonly', '-p', '2', '-trimpath', '-count=1', '-timeout=50s', '-json', ...filter, pkg], { stdio: 'inherit' });
    packages++; roots += selected.length;
  }
  console.log(`Go lane=${lane}/3 group=${group}/8 packages=${packages} roots=${roots}`);
} else if (kind === 'node' || kind === 'e2e') {
  const patterns = kind === 'node' ? ['connectors/hub-client/test/*.test.ts', 'connectors/launcher/test/*.test.ts', 'connectors/launcher/test/backends/*.test.ts'] : ['connectors/launcher/test/e2e/*.test.ts'];
  const all = patterns.flatMap(pattern => globSync(pattern));
  if (!all.length) throw new Error('empty Node inventory');
  const files = partition(all, group, 4);
  if (!files.length) throw new Error('empty Node group');
  console.log(`${kind} group=${group}/4 files=${files.length}/${all.length}: ${files.join(', ')}`);
  execFileSync(process.execPath, ['--disable-warning=ExperimentalWarning', '--test', '--test-reporter=tap', '--test-concurrency=2', ...files], { stdio: 'inherit' });
} else throw new Error(`unknown suite: ${kind}`);
