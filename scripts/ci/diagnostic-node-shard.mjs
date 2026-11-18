// Disposable measurement; never merge this probe branch.
import {spawnSync} from 'node:child_process';
const mode=process.argv[2];
if(mode!=='whole'&&!/^[0-3]$/.test(mode))throw new Error('whole or 0..3 required');
const started=performance.now(),env={...process.env};
const args=['--disable-warning=ExperimentalWarning','--test','--test-reporter=tap','connectors/launcher/test/windows-platform.test.ts'];
if(mode!=='whole'){
 env.CI_NODE_SHARD=mode;env.CI_NODE_SHARDS='4';
 args.unshift('--import',new URL('./node-shard-register.mjs',import.meta.url).href);
}else{delete env.CI_NODE_SHARD;delete env.CI_NODE_SHARDS;}
const result=spawnSync(process.execPath,args,{env,encoding:'utf8',maxBuffer:16*1024*1024,windowsHide:true});
process.stdout.write(result.stdout||'');process.stderr.write(result.stderr||'');
if(result.error)throw result.error;
const roots=[...(result.stdout||'').matchAll(/^(ok|not ok) \d+ - (.+)$/gm)].map(m=>({status:m[1],title:m[2],skipped:/ # SKIP(?: |$)/.test(m[2])}));
console.log('NODE_SHARD_RECEIPT '+JSON.stringify({source:process.env.GITHUB_SHA,arch:process.arch,mode,elapsed_ms:Math.round(performance.now()-started),code:result.status,signal:result.signal,roots}));
if(result.status!==0||result.signal)process.exitCode=1;
