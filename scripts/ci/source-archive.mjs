// Share the producer's committed source with native consumers. The trusted
// same-run artifact carries this bootstrap, the archive and its source receipt.
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {copyFile, mkdir, readFile, writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const scopes={
  go:['bench','cmd','internal','connectors','docs/protocol','scripts/ci','scripts/npm'],
  package:['connectors','scripts/ci','scripts/npm','npm'],
};
const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
const git=(root,args)=>execFileSync('git',args,{cwd:root,encoding:'utf8',timeout:45000,maxBuffer:4*1024*1024});
export async function stageSource(root,bundle,scope='go'){
  const folders=scopes[scope];
  if(!Array.isArray(folders))throw new Error('unknown source archive scope');
  root=path.resolve(root); bundle=path.resolve(bundle);
  const revision=git(root,['rev-parse','HEAD']).trim();
  const files=git(root,['ls-tree','-r','--name-only','-z',revision]).split('\0').filter(Boolean);
  // Cone-mode sparse checkout also retains files directly inside ancestors.
  const selected=files.filter(file=>!file.includes('/')||folders.some(folder=>
    file.startsWith(folder+'/')||(folder.startsWith(path.posix.dirname(file)+'/')&&path.posix.dirname(file)!=='.')));
  if(!selected.length)throw new Error('empty native source inventory');
  await mkdir(bundle,{recursive:true});
  const archive=path.join(bundle,'source.tar.gz');
  git(root,['archive','--format=tar.gz','--output='+archive,revision,'--',...selected]);
  await writeFile(path.join(bundle,'source.json'),JSON.stringify({revision,scope,sha256:hash(await readFile(archive))}));
  await copyFile(fileURLToPath(import.meta.url),path.join(bundle,'restore-source.mjs'));
}
export async function restoreSource(bundle,sha,root){
  const receipt=JSON.parse(await readFile(path.join(bundle,'source.json'),'utf8'));
  if(!/^[a-f0-9]{40}$/.test(sha??'')||receipt.revision!==sha)throw new Error('native archive source mismatch');
  const archive=path.resolve(bundle,'source.tar.gz');
  if(hash(await readFile(archive))!==receipt.sha256)throw new Error('native source archive hash mismatch');
  execFileSync('tar',['-xzf',archive,'-C',path.resolve(root)],{stdio:'inherit',timeout:45000});
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
  const [command,bundle,argument]=process.argv.slice(2);
  if(command==='restore')await restoreSource(bundle,argument,process.cwd());
  else if(command==='stage')await stageSource(process.cwd(),bundle,argument);
  else throw new Error('source archive stage/restore command required');
}
