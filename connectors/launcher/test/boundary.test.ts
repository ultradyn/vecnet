import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
const root=fileURLToPath(new URL('../',import.meta.url));
function files(dir:string):string[] {
  if (!existsSync(dir)) return [];
  return readdirSync(dir,{withFileTypes:true}).flatMap(entry => entry.isDirectory() ? files(resolve(dir,entry.name)) : /\.[cm]?ts$/.test(entry.name) ? [resolve(dir,entry.name)] : []);
}
function imports(file:string):string[] {
  const source=readFileSync(file,'utf8');
  return Array.from(source.matchAll(/(?:\b(?:import|export)\s+(?:type\s+)?(?:[^;"']*?\s+from\s*)?|\b(?:import|require)\s*\(\s*)["']([^"']+)["']/g), match => match[1]);
}
test('no runtime dependencies in either package.json', () => {
  for (const path of [resolve(root,'package.json'),resolve(root,'../hub-client/package.json')]) {
    const pkg=JSON.parse(readFileSync(path,'utf8'));
    for (const field of ['dependencies','optionalDependencies','peerDependencies','bundledDependencies','bundleDependencies']) assert.equal(Object.keys(pkg[field]??{}).length,0,`${path}: ${field}`);
  }
  const hub=JSON.parse(readFileSync(resolve(root,'../hub-client/package.json'),'utf8'));
  assert.equal('devDependencies' in hub,false);
});
test('plugins import only backend.ts, their own files and node:*', () => {
  const backends=resolve(root,'src/backends');
  for (const file of files(backends)) for (const spec of imports(file)) {
    if (spec.startsWith('node:')) continue;
    assert.ok(spec.startsWith('.'),`${file}: ${spec}`);
    const target=resolve(dirname(file),spec); const plugin=relative(backends,file).split('/')[0];
    const own=relative(resolve(backends,plugin),target);
    assert.ok(target===resolve(root,'src/backend.ts') || (own!== '..' && !own.startsWith('../') && !own.startsWith('/')),`${file}: ${spec}`);
  }
});
test('hub-client never imports from launcher', () => {
  const hub=resolve(root,'../hub-client');
  for (const file of [...files(resolve(hub,'src')),...files(resolve(hub,'testing')),...files(resolve(hub,'test'))]) for (const spec of imports(file)) {
    assert.ok(!spec.includes('launcher'),`${file}: ${spec}`);
    if (spec.startsWith('.')) { const path=relative(hub,resolve(dirname(file),spec)); assert.ok(path!=='..' && !path.startsWith('../'),`${file}: ${spec}`); }
  }
});
