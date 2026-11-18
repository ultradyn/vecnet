// Reuse exact-source target artifacts for test-only native bin/ paths. No Go
// compiler/setup is needed in Node/E2E jobs.
import { createHash } from 'node:crypto';
import { chmod, copyFile, mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';
const platform=`${process.platform}-${process.arch}`,dir=path.join('dist','targets',platform);
const info=JSON.parse(await readFile(path.join(dir,'target.json'),'utf8'));
const manifest=JSON.parse(await readFile('npm/package.json','utf8'));
// PR GITHUB_SHA is the checkout/merge revision used by every target producer.
if(info.platform!==platform||info.version!==manifest.version||info.revision!==process.env.GITHUB_SHA)throw new Error('native artifact source mismatch');
await mkdir('bin',{recursive:true});
for(const command of ['vecnet','vecnet-hub']){
 const name=command+(process.platform==='win32'?'.exe':'');const file=path.join(dir,name);
 if(createHash('sha256').update(await readFile(file)).digest('hex')!==info.binaries[name])throw new Error('native artifact checksum mismatch');
 await copyFile(file,path.join('bin',name));await chmod(path.join('bin',name),0o755);
}
