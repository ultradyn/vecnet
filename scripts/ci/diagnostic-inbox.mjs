// Disposable native characterization only; this branch must never merge.
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {spawn} from 'node:child_process';
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
