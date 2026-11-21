// CI job ownership only. The test binary creates/validates genuine private Node;
// packet processes borrow it and still perform real production admission.
import {execFileSync} from 'node:child_process';
import {readFile, writeFile, rm, realpath} from 'node:fs/promises';
import path from 'node:path';

const receipt = '.ci/launcher-node-fixture';
export async function launcherNodeLayout(inventory) {
  const rows=inventory.filter(row=>row.pkg==='github.com/XertroV/vecnet/internal/cli');
  if(rows.length!==1)throw new Error('expected one native CLI test binary');
  const cli=rows[0], root=cli.temporary;
  if(typeof root!=='string'||!path.isAbsolute(root)||/[\r\n]/.test(root)||
      !path.basename(root).startsWith('vecnet-ci-go-native-')||
      typeof cli.binary!=='string'||path.dirname(cli.binary)!==root||await realpath(root)!==root)
    throw new Error('invalid native launcher Node fixture owner');
  const directory=path.join(root,'launcher-node-fixture');
  return {cli,directory,node:path.join(directory,process.platform==='win32'?'node.exe':'node')};
}
function utility({cli,directory},mode) {
  return execFileSync(cli.binary,['--ci-launcher-node-fixture',mode,directory],
    {cwd:cli.directory,encoding:'utf8',stdio:['ignore','pipe','inherit']});
}
export async function prepareLauncherNode(inventory) {
  const layout=await launcherNodeLayout(inventory);
  try {
    const output=utility(layout,'prepare');
    if(output!==`${layout.node}\n`)throw new Error('invalid prepared launcher Node path');
    await writeFile(receipt,output,{flag:'wx',mode:0o600});
  } catch(error) {
    try {utility(layout,'cleanup');} catch(cleanupError) {throw new AggregateError([error,cleanupError],'launcher Node preparation/cleanup failed');}
    throw error;
  }
}
export async function borrowedLauncherNode(inventory) {
  const {node}=await launcherNodeLayout(inventory);
  if(await readFile(receipt,'utf8')!==`${node}\n`)throw new Error('invalid shared launcher Node fixture receipt');
  return node;
}
export async function cleanupLauncherNode(inventory) {
  // Never derive a recursive-delete target from the published path file.
  utility(await launcherNodeLayout(inventory),'cleanup');
  await rm(receipt,{force:true});
}
