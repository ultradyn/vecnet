import { createHash } from 'node:crypto';
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { nativeTargets } from './platforms.mjs';
export async function validateTargets(directory,{revision,version}) {
 const entries=(await readdir(directory)).sort();
 if(JSON.stringify(entries)!==JSON.stringify(nativeTargets.map(t=>t.platform).sort()))throw new Error('incomplete or unexpected target inventory');
 let go;const targets=[];
 for(const target of nativeTargets){
  const dir=path.join(directory,target.platform),info=JSON.parse(await readFile(path.join(dir,'target.json'),'utf8'));
  if(info.platform!==target.platform||info.revision!==revision||info.version!==version)throw new Error('target source/version mismatch');
  if(!/^go version go\S+ linux\/amd64$/.test(info.go))throw new Error('unexpected build toolchain');
  if(go&&go!==info.go)throw new Error('mixed build toolchains');go=info.go;
  const expected=['vecnet'+target.suffix,'vecnet-hub'+target.suffix].sort();
  if(JSON.stringify(Object.keys(info.binaries).sort())!==JSON.stringify(expected))throw new Error('incomplete binaries');
  if(JSON.stringify((await readdir(dir)).sort())!==JSON.stringify([...expected,'target.json'].sort()))throw new Error('unexpected target files');
  for(const name of expected){const hash=createHash('sha256').update(await readFile(path.join(dir,name))).digest('hex');if(info.binaries[name]!==hash)throw new Error('target hash mismatch');}
  targets.push({...target,dir,info});
 }
 return {go,targets};
}
