// Disposable native characterization only; this branch must never merge.
import assert from 'node:assert/strict';
import {readFile,mkdir,mkdtemp,realpath,chmod,writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {consumeBundle} from './go-bundle.mjs';
import {spawn} from 'node:child_process';
if(process.argv[2]==='prepare'){
 const platform=`${process.platform}-${process.arch}`;
 const temporary=await mkdtemp(path.join(await realpath(os.tmpdir()),'vecnet-inbox-probe-'));await chmod(temporary,0o700);
 const sha='528f8b508b2b4fe42aa1ecc44f858fb9b2ed6e1b';
 console.log(JSON.stringify({expectedBundleMerge:sha,platform,branchHead:'1c05da91ba7b1306b4def2bb48c38d7c9aa0b7d6'}));
 const bundle=await consumeBundle(path.resolve('dist/go-tests',platform),{sha,platform,temporary});
 await mkdir('.ci',{recursive:true});
 for(let i=0;i<6;i++)await writeFile(`.ci/go-${i}.json`,JSON.stringify(bundle.inventory.filter((_,n)=>n%6===i)));
 await writeFile('.ci/test2json',bundle.test2json);
 console.log(JSON.stringify({packages:bundle.inventory.length,roots:bundle.inventory.reduce((n,row)=>n+row.names.length,0)}));
 process.exit(0);
}
const n=Number(process.argv[2]);assert.ok(n===1||n===4);
const inventory=(await Promise.all(Array.from({length:6},(_,i)=>readFile(`.ci/go-${i}.json`,'utf8')))).flatMap(JSON.parse);
const row=inventory.find(r=>r.pkg.endsWith('/internal/cli'));
assert.ok(row?.binary && row.names.includes('TestInboxDisplayPreservesUnreadReplayMetadata'));
const tool=(await readFile('.ci/test2json','utf8')).trim();
const start=performance.now();
const results=await Promise.allSettled(Array.from({length:n},(_,i)=>new Promise((resolve,reject)=>{
 const child=spawn(tool,['-t','-p',`inbox-${i}`,row.binary,'-test.v=test2json','-test.count=1','-test.timeout=45s','-test.run','^TestInboxDisplayPreservesUnreadReplayMetadata$'],{cwd:row.directory,stdio:'inherit'});
 child.once('error',reject);child.once('close',(code,signal)=>code===0?resolve():reject(new Error(`inbox-${i} exit=${code} signal=${signal}`)));
})));
console.log(JSON.stringify({count:n,elapsed_ms:Math.round(performance.now()-start),results:results.map(r=>r.status)}));
for(const result of results)if(result.status==='rejected')throw result.reason;
