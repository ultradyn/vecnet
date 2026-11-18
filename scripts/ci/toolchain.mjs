// Pinned official archives, verified before extraction. Download/extract are
// separate steps so slow ZIP expansion cannot hide cache work in setup-go.
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { appendFile, mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
const archives={
 'linux-x64':['linux-amd64.tar.gz','7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840'],
 'win32-x64':['windows-amd64.zip','178ab98950ac69ab25069307dc6ade79f3816e414cce95b4da166a115cde1e7e'],
 'win32-arm64':['windows-arm64.zip','9e7a72565792b8ab370b37ac9ac109799237abacb39ff2f541c438b963c0cc64'],
};
const archive=archives[`${process.platform}-${process.arch}`];if(!archive)throw new Error('unsupported host');
const directory=path.join(process.env.RUNNER_TEMP,'ci-go-1.27.4');
const file=path.join(directory,'archive'+(process.platform==='win32'?'.zip':'.tar.gz'));
await mkdir(directory,{recursive:true});
if(process.argv[2]==='download'){
 const response=await fetch(`https://go.dev/dl/go1.27.4.${archive[0]}`,{signal:AbortSignal.timeout(45000)});
 if(!response.ok)throw new Error(`Go download HTTP ${response.status}`);
 const bytes=Buffer.from(await response.arrayBuffer());
 if(createHash('sha256').update(bytes).digest('hex')!==archive[1])throw new Error('Go archive checksum mismatch');
 await writeFile(file,bytes);
}else if(process.argv[2]==='extract'){
 if(createHash('sha256').update(await readFile(file)).digest('hex')!==archive[1])throw new Error('Go archive checksum mismatch');
 // Windows tar.exe is libarchive, avoiding slow .NET ExtractToDirectory.
 execFileSync('tar',['-xf',file,'-C',directory],{stdio:'inherit'});
 const root=path.join(directory,'go');
 execFileSync(path.join(root,'bin',process.platform==='win32'?'go.exe':'go'),['version'],{stdio:'inherit'});
 await appendFile(process.env.GITHUB_PATH,path.join(root,'bin')+'\n');
 await appendFile(process.env.GITHUB_ENV,`GOROOT=${root}\nGOTOOLCHAIN=local\n`);
}else throw new Error('toolchain download|extract required');
