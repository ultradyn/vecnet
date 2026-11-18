// Build one exact-source native artifact; default build.mjs remains unchanged.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmod, mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildWindowsOwner } from '../ci/windows-owner.mjs';
import { nativeTargets } from './platforms.mjs';
const root=fileURLToPath(new URL('../../',import.meta.url));
export async function buildTarget(platform,{destination=path.join(root,'dist','targets',platform)}={}) {
 const target=nativeTargets.find(t=>t.platform===platform);if(!target)throw new Error('unknown target');
 const {version}=JSON.parse(await readFile(path.join(root,'npm/package.json'),'utf8'));
 const revision=execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim();
 await mkdir(destination,{recursive:true});
 const env={...process.env,CGO_ENABLED:'0',GOMAXPROCS:'2',GOOS:target.goos,GOARCH:target.goarch,GOAMD64:'v1',GOARM64:'v8.0'};
 execFileSync('go',['build','-mod=readonly','-p','2','-trimpath','-buildvcs=false','-ldflags',`-s -w -X github.com/XertroV/vecnet/internal/mcpserver.Version=${version} -X github.com/XertroV/vecnet/internal/buildinfo.Version=${version}`,'-o',destination+path.sep,'./cmd/vecnet','./cmd/vecnet-hub'],{cwd:root,env,stdio:'inherit'});
 const binaries={};
 for(const name of ['vecnet','vecnet-hub']){const file=name+target.suffix;await chmod(path.join(destination,file),0o755);binaries[file]=createHash('sha256').update(await readFile(path.join(destination,file))).digest('hex');}
 const helper=await buildWindowsOwner(path.resolve(destination),platform,env);
 const go=execFileSync('go',['version'],{encoding:'utf8'}).trim();
 await writeFile(path.join(destination,'target.json'),JSON.stringify({platform,version,revision,go,binaries,...helper},null,2)+'\n');
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url))await buildTarget(process.argv[2]);
