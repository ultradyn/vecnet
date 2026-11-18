// Reuse exact-source target artifacts for disposable native CI tests. No Go
// compiler/setup is needed in Node, E2E or package consumer jobs.
import { createHash } from 'node:crypto';
import { chmod, copyFile, mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { readWindowsOwner, stageTargetOwner } from './windows-owner.mjs';
const args=process.argv.slice(2),ownerOnly=args.length===1&&args[0]==='--owner-only';
if(args.length&&!ownerOnly)throw new Error('usage: native.mjs [--owner-only]');
const windows=process.platform==='win32';
if(ownerOnly&&!windows)throw new Error('--owner-only requires Windows');
const platform=`${process.platform}-${process.arch}`,dir=path.join('dist','targets',platform);
const info=JSON.parse(await readFile(path.join(dir,'target.json'),'utf8'));
const manifest=JSON.parse(await readFile('npm/package.json','utf8'));
// PR GITHUB_SHA is the checkout/merge revision used by every target producer.
const sha=process.env.GITHUB_SHA;
if(!/^[a-f0-9]{40}$/.test(sha??'')||info.platform!==platform||info.version!==manifest.version||info.revision!==sha)throw new Error('native artifact source mismatch');
const owner=windows?await readWindowsOwner(dir,info,platform):null;
if(!ownerOnly){
 await mkdir('bin',{recursive:true});
 for(const command of ['vecnet','vecnet-hub']){
  const name=command+(windows?'.exe':'');const file=path.join(dir,name);
  if(createHash('sha256').update(await readFile(file)).digest('hex')!==info.binaries[name])throw new Error('native artifact checksum mismatch');
  await copyFile(file,path.join('bin',name));await chmod(path.join('bin',name),0o755);
 }
}
// Node/E2E may bootstrap before wrapping this install in the budget.
if(owner&&(ownerOnly||process.env.CI_WINDOWS_OWNER===undefined))await stageTargetOwner(owner);
