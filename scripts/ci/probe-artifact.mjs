// Temporary diagnostic only; never included in release acceptance.
import {createHash} from 'node:crypto';
import {readFile,appendFile} from 'node:fs/promises';
import path from 'node:path';
const dir=path.resolve('dist','targets',`win32-${process.arch}`);
const receipt=JSON.parse(await readFile(path.join(dir,'target.json'),'utf8'));
if(receipt.platform!==`win32-${process.arch}`||receipt.version!=='0.2.4')throw new Error('unexpected diagnostic artifact');
for(const name of ['vecnet.exe','vecnet-hub.exe']){
 const hash=createHash('sha256').update(await readFile(path.join(dir,name))).digest('hex');
 if(hash!==receipt.binaries[name])throw new Error('artifact hash mismatch');
}
console.log(`PROBE_ARTIFACT run=44240225126 revision=${receipt.revision}`);
await appendFile(process.env.GITHUB_ENV,`VECNET_BIN=${path.join(dir,'vecnet.exe')}\n`);
