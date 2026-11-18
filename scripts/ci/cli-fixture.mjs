// Windows-only synthetic CLI subprocess image, built by the Linux producer.
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {lstat,readFile,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {checkWindowsMachine} from './windows-owner.mjs';

const file='ci-cli-fixture.exe';
const root=fileURLToPath(new URL('../../',import.meta.url));
const hash=bytes=>createHash('sha256').update(bytes).digest('hex');
export async function buildCLIFixture(directory,platform,env){
 if(!platform.startsWith('win32-'))return {};
 execFileSync('go',['build','-mod=readonly','-p','2','-trimpath','-buildvcs=false','-ldflags','-s -w','-o',path.join(directory,file),'./scripts/ci/_cli-fixture'],{cwd:root,env:{...env,GOMAXPROCS:'2'},stdio:'inherit'});
 const bytes=await readFile(path.join(directory,file));checkWindowsMachine(bytes,platform,'CLI fixture');
 return {ciCLIFixture:{file,sha256:hash(bytes)}};
}
export async function readCLIFixture(directory,receipt,platform){
 const meta=receipt.ciCLIFixture;
 if(receipt.platform!==platform||meta?.file!==file||typeof meta.sha256!=='string'||!/^[a-f0-9]{64}$/.test(meta.sha256))throw new Error('invalid CLI fixture metadata');
 const source=path.join(directory,file),info=await lstat(source);
 if(!info.isFile()||info.nlink!==1)throw new Error('unsafe CLI fixture file');
 const bytes=await readFile(source);
 if(hash(bytes)!==meta.sha256)throw new Error('CLI fixture hash mismatch');
 checkWindowsMachine(bytes,platform,'CLI fixture');return bytes;
}
export async function stageCLIFixture(bytes,temporary){
 const dest=path.join(temporary,file);await writeFile(dest,bytes,{flag:'wx',mode:0o700});return dest;
}
