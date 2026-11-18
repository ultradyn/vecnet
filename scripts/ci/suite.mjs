// Only CI scheduling lives here; assertions and platform selection remain in
// the original suites. Run this whole command under budget.mjs, not each child
// separately: discovery/compilation and all packages share the step deadline.
import { execFileSync } from 'node:child_process';
import { mkdir, readFile, writeFile, access } from 'node:fs/promises';
import { globSync } from 'node:fs';
import path from 'node:path';
import { performance } from 'node:perf_hooks';
import { consumeBundle } from './go-bundle.mjs';
import { partition } from './partition.mjs';

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
} else if (kind === 'consume-go') {
  const platform=`${process.platform}-${process.arch}`,base=process.env.TMPDIR||process.env.TEMP||process.env.TMP;
  const bundle=await consumeBundle(path.resolve('dist/go-tests',platform),{sha:process.env.GITHUB_SHA,platform,temporary:path.join(base,'ci80-go-native')});
  await mkdir('.ci',{recursive:true});
  for(let i=0;i<6;i++)await writeFile(`.ci/go-${i}.json`,JSON.stringify(bundle.inventory.filter((_,n)=>n%6===i)));
  await writeFile('.ci/test2json',bundle.test2json);
  console.log(`Go native bundle: ${bundle.inventory.length} packages, ${bundle.inventory.reduce((n,p)=>n+p.names.length,0)} roots`);
} else if (kind === 'discover-go') {
  partition([], lane, 6);
  const metadata=go(['list','-mod=readonly','-f','{{.ImportPath}}\t{{.Dir}}\t{{len .TestGoFiles}}\t{{len .XTestGoFiles}}','./...']).trim().split(/\r?\n/).map(line=>{const [ImportPath,Dir,internal,external]=line.split('\t');return {ImportPath,Dir,hasTests:Number(internal)+Number(external)>0};});
  const packages=metadata.map(p=>p.ImportPath);
  const selected=partition(packages,lane,6);
  // The batch compile keeps Go's parallel package scheduler and avoids one
  // toolchain invocation/relink per package per root group.
  const base=process.env.TMPDIR||process.env.TEMP||process.env.TMP;
  if(!base)throw new Error('private test TMPDIR required');
  const output=path.join(base,'ci80-go-tests',String(lane));
  await mkdir(output,{recursive:true});
  const basenames=selected.map(pkg=>path.basename(pkg));
  const unique=basenames.filter((name,i)=>basenames.indexOf(name)===i);
  const batch=unique.length===selected.length;
  if(selected.length&&batch)go(['test','-mod=readonly','-p','2','-trimpath','-c','-o',output+path.sep,...selected]);
  const inventory=[];
  for(const pkg of selected){
    const meta=metadata.find(p=>p.ImportPath===pkg),directory=meta.Dir;
    const binary=path.join(output,(batch?path.basename(pkg):pkg.replace(/[^A-Za-z0-9_-]/g,'_'))+'.test'+(process.platform==='win32'?'.exe':''));
    if(!batch)go(['test','-mod=readonly','-p','2','-trimpath','-c','-o',binary,pkg]);
    const hasTests=meta.hasTests;
    if(!hasTests){inventory.push({pkg,names:[],directory,binary:null});continue;}
    // A missing binary for a package with test files must be RED, not a silent
    // empty-inventory success. Compilation has just returned successfully.
    await access(binary);
    const listing=execFileSync(binary,['-test.list','.'],{cwd:directory,encoding:'utf8'});
    const names=listing.trim().split(/\r?\n/).filter(Boolean);
    if(names.some(name=>!/^(?:Test\w+|Example\w*|Fuzz\w+)$/.test(name)))throw new Error('invalid binary discovery');
    inventory.push({pkg,names,directory,binary});
  }
  await mkdir('.ci', { recursive: true });
  await writeFile(`.ci/go-${lane}.json`, JSON.stringify(inventory));
  console.log(`Go inventory: ${inventory.length} packages, ${inventory.reduce((n, p) => n + p.names.length, 0)} root tests/examples/fuzz targets`);
} else if (kind === 'go') {
  partition([], lane, 3); partition([], group, 8);
  const inventory = (await Promise.all(Array.from({length:6},(_,i)=>readFile(`.ci/go-${i}.json`,'utf8')))).flatMap(JSON.parse).sort((a,b)=>a.pkg.localeCompare(b.pkg,'en'));
  if(new Set(inventory.map(p=>p.pkg)).size!==inventory.length)throw new Error('duplicate package inventory');
  let roots = 0, packages = 0;
  for (const [ordinal, { pkg, names, binary, directory }] of inventory.entries()) {
    // Large packages are split by complete root name, never by subtest. Small
    // packages run unfiltered once, including packages with no test files.
    const split = names.length >= 20;
    const selected = split ? partition(names, group * 3 + lane, 24) : names;
    if (split ? !selected.length : ordinal % 24 !== group * 3 + lane) continue;
    const filter = split ? ['-test.run', `^(${selected.join('|')})$`] : [];
    const started=performance.now();
    let tool;try{tool=(await readFile('.ci/test2json','utf8')).trim();}catch{}
    if(binary)execFileSync(tool||'go',[...(tool?[]:['tool','test2json']),'-t','-p',pkg,binary,'-test.v=test2json','-test.count=1','-test.timeout=50s',...filter],{cwd:directory,stdio:'inherit'});
    console.log(`ci-package pkg=${pkg} roots=${selected.length} elapsed_ms=${Math.round(performance.now()-started)}`);
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
