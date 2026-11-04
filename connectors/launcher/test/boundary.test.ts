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
function imports(file:string, source=readFileSync(file,'utf8')):string[] {
  const specs:string[]=[];
  const trivia=String.raw`(?:\s|/\*[\s\S]*?\*/|//[^\n]*(?:\n|$))*`;
  // Fail closed on escaped specifiers instead of resolving their raw spelling.
  const add=(spec:string) => {
    assert.ok(!spec.includes('\\'),`${file}: import target must be statically checkable (no escapes)`);
    specs.push(spec);
  };
  const declarations=/\b(?:import|export)\s+(?:type\s+)?(?:[^;"'`()]*?\s+from\s*)?(["'])([^"']+)\1/g;
  for (const match of source.matchAll(declarations)) add(match[2]);
  // Check every call, including ones that do not start with a quoted string.
  // Only a complete literal first argument is safe to pass to the path guard.
  const calls=new RegExp(String.raw`\b(?:import|require)${trivia}\(${trivia}`,'g');
  for (const match of source.matchAll(calls)) {
    const rest=source.slice(match.index!+match[0].length);
    const literal=/^(["'`])((?:\\[\s\S]|(?!\1)[^\\])*)\1/.exec(rest);
    assert.ok(literal,`${file}: import target must be statically checkable`);
    assert.ok(literal[1]!=='`' || !literal[2].includes('${'),`${file}: import target must be statically checkable`);
    const after=rest.slice(literal[0].length).replace(new RegExp(`^${trivia}`),'');
    assert.ok(after.startsWith(')') || after.startsWith(','),`${file}: import target must be statically checkable`);
    add(literal[2]);
  }
  return specs;
}
test('no runtime dependencies in either package.json', () => {
  for (const path of [resolve(root,'package.json'),resolve(root,'../hub-client/package.json')]) {
    const pkg=JSON.parse(readFileSync(path,'utf8'));
    for (const field of ['dependencies','optionalDependencies','peerDependencies','bundledDependencies','bundleDependencies']) assert.equal(Object.keys(pkg[field]??{}).length,0,`${path}: ${field}`);
  }
  const hub=JSON.parse(readFileSync(resolve(root,'../hub-client/package.json'),'utf8'));
  assert.equal('devDependencies' in hub,false);
});
function checkPluginImports(file:string, source?:string):void {
  const backends=resolve(root,'src/backends');
  for (const spec of imports(file,source)) {
    if (spec.startsWith('node:')) continue;
    assert.ok(spec.startsWith('.'),`${file}: ${spec}`);
    const target=resolve(dirname(file),spec); const plugin=relative(backends,file).split('/')[0];
    const own=relative(resolve(backends,plugin),target);
    assert.ok(target===resolve(root,'src/backend.ts') || (own!== '..' && !own.startsWith('../') && !own.startsWith('/')),`${file}: ${spec}`);
  }
}
test('plugins import only backend.ts, their own files and node:*', () => {
  for (const file of files(resolve(root,'src/backends'))) checkPluginImports(file);
});

const pluginFile=resolve(root,'src/backends/fake/index.ts');
const importForms=[
  (spec:string) => `import value from '${spec}';`,
  (spec:string) => `import type { Value } from "${spec}";`,
  (spec:string) => `import '${spec}';`,
  (spec:string) => `export { value } from '${spec}';`,
  (spec:string) => `export * from "${spec}";`,
  (spec:string) => `import('${spec}');`,
  (spec:string) => `import("${spec}");`,
  (spec:string) => 'import(`'+spec+'`);',
  (spec:string) => `require('${spec}');`,
  (spec:string) => 'require(`'+spec+'`);',
  (spec:string) => 'import /* module */ (`'+spec+'`);',
  (spec:string) => 'import // module\n (`'+spec+'` /* target */);',
  (spec:string) => 'import(`'+spec+'`, {with:{type: "json"}});',
];
for (const [index,form] of importForms.entries()) test(`plugin guard checks literal import syntax ${index+1}`, () => {
  for (const spec of ['../../backend.ts','./helper.ts','./nested/helper.ts','node:fs']) {
    assert.deepEqual(imports(pluginFile,form(spec)),[spec]);
    assert.doesNotThrow(() => checkPluginImports(pluginFile,form(spec)));
  }
  for (const spec of ['../../hub.ts','../other/index.ts','../../../hub-client/src/ports.ts','package','/tmp/plugin.ts']) {
    assert.throws(() => checkPluginImports(pluginFile,form(spec)));
  }
});
for (const source of [
  'import(target);',
  'import(`./${target}.ts`);',
  'import("./"+target);',
  'import("./helper.ts"+suffix);',
  'import(("../../hub.ts"));',
  'require(target);',
  'require(`./${target}.ts`);',
  'import /* module */ (target);',
  'import(`./helper.ts`, options); import(target);',
  String.raw`import('./h\u0065lper.ts');`,
  String.raw`import value from './h\u0065lper.ts';`,
]) test(`plugin guard rejects unchecked dynamic target: ${source}`, () => {
  assert.throws(() => checkPluginImports(pluginFile,source),/statically checkable/);
});
test('hub-client never imports from launcher', () => {
  const hub=resolve(root,'../hub-client');
  for (const file of [...files(resolve(hub,'src')),...files(resolve(hub,'testing')),...files(resolve(hub,'test'))]) for (const spec of imports(file)) {
    assert.ok(!spec.includes('launcher'),`${file}: ${spec}`);
    if (spec.startsWith('.')) { const path=relative(hub,resolve(dirname(file),spec)); assert.ok(path!=='..' && !path.startsWith('../'),`${file}: ${spec}`); }
  }
});
