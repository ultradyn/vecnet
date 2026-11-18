import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { validateTargets } from './assemble.mjs';
import { nativeTargets } from './platforms.mjs';
test('assembly requires all8 hash-bound binaries from same exact revision/version/toolchain',async t=>{
 const dir=await mkdtemp(path.join(os.tmpdir(),'assembly-'));t.after(()=>rm(dir,{recursive:true,force:true}));
 const source={revision:'abc',version:'1.2.3'};const receipts=[];
 for(const target of nativeTargets){
  const dest=path.join(dir,target.platform);await mkdir(dest);const binaries={};
  for(const command of ['vecnet','vecnet-hub']){const name=command+target.suffix;await writeFile(path.join(dest,name),name);binaries[name]=createHash('sha256').update(name).digest('hex');}
  const info={...source,platform:target.platform,go:'go version go1.27.4 linux/amd64',binaries};receipts.push([dest,info]);await writeFile(path.join(dest,'target.json'),JSON.stringify(info));
 }
 assert.equal((await validateTargets(dir,source)).targets.length,4);
 for(const [field,value] of [['revision','wrong'],['version','9.9.9'],['platform','bad'],['go','go version go1.27.3 linux/amd64']]){
  const [dest,info]=receipts[0];await writeFile(path.join(dest,'target.json'),JSON.stringify({...info,[field]:value}));await assert.rejects(validateTargets(dir,source));await writeFile(path.join(dest,'target.json'),JSON.stringify(info));
 }
 await writeFile(path.join(receipts[0][0],'vecnet'),'corrupt');await assert.rejects(validateTargets(dir,source),/hash/);
});
