// Disposable diagnostic producer: compile only the changed test package.
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {stageSource} from './source-archive.mjs';
const platform=process.env.CI_GO_PLATFORM;
if(!['win32-x64','win32-arm64'].includes(platform))throw new Error('Windows CLI target required');
const actual=execFileSync('go',['env','GOOS','GOARCH'],{encoding:'utf8'}).trim().split(/\r?\n/);
if(actual[0]!=='windows'||actual[1]!==({'win32-x64':'amd64','win32-arm64':'arm64'}[platform]))throw new Error('compiler target mismatch');
const revision=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim();
if(revision!==process.env.GITHUB_SHA)throw new Error('producer source mismatch');
const out=path.resolve('dist/cli-fixture',platform);await mkdir(out,{recursive:true});
const binary=path.join(out,'cli.test.exe');
execFileSync('go',['test','-mod=readonly','-p','4','-trimpath','-ldflags','-s -w','-c','-o',binary,'./internal/cli'],{stdio:'inherit'});
await stageSource(process.cwd(),out);
await writeFile(path.join(out,'candidate.json'),JSON.stringify({revision,platform,sha256:createHash('sha256').update(await readFile(binary)).digest('hex')}));
