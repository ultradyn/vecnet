// Disposable native fixture diagnostic; never merge this branch.
import {execFileSync} from 'node:child_process';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import path from 'node:path';
import {goCompileJobs,goCompileEnvironment} from './schedule.mjs';
const env=goCompileEnvironment(process.env),jobs=goCompileJobs(env.CI_GO_COMPILE_JOBS);
const root=path.resolve('dist/fixture-probe');await mkdir(root,{recursive:true});
const run=args=>execFileSync('go',args,{env,stdio:'inherit'});
run(['test','-mod=readonly','-p',jobs,'-trimpath','-ldflags','-s -w','-c','-o',path.join(root,'cli.test.exe'),'./internal/cli']);
run(['test','-mod=readonly','-p',jobs,'-trimpath','-ldflags','-s -w','-c','-o',path.join(root,'store.test.exe'),'./internal/store']);
run(['build','-p',jobs,'-trimpath','-ldflags','-s -w','-o',path.join(root,'test2json.exe'),'cmd/test2json']);
const files={};for(const name of ['cli.test.exe','store.test.exe','test2json.exe'])files[name]=createHash('sha256').update(await readFile(path.join(root,name))).digest('hex');
await writeFile(path.join(root,'receipt.json'),JSON.stringify({revision:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),arch:process.env.GOARCH,files}));
