import {createHash} from 'node:crypto';
import {readFileSync,rmSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {performance} from 'node:perf_hooks';
import path from 'node:path';
const [mode,pkg,policy,trial]=process.argv.slice(2);
if(mode==='cleanup'){
 const probeRoot=process.env.CI80_ABBA_ROOT;
 if(!probeRoot){console.log('ABBA_CLEANUP no initialized disposable root');process.exit(0);}
 const probeOuter=path.dirname(probeRoot);
 if(path.basename(probeRoot)!=='private'||!path.basename(probeOuter).startsWith('ci80-abba-'))throw new Error('unsafe cleanup root');
 rmSync(probeOuter,{recursive:true,force:true});process.exit(0);
}
const roots={hub:'TestPreferencesConfigDurationsValidatedBeforeConversion|TestRetentionIndexedIntervalsPreserveHistoricalRules',cmdhub:'TestHubURLFlagOverridesEnvironment|TestBlobExpiryBootstrapProductionPreferences',cli:'TestReceivedNoticeCLI|TestLauncherWindowsInstallPreviewAndExplicitIntent'};
const packages={hub:'internal/hub',cmdhub:'cmd/vecnet-hub',cli:'internal/cli'};
if(!roots[pkg])throw new Error('unknown package');
const dir=packages[pkg],binary=path.join(process.env.CI80_ABBA_ROOT,'bin',`${pkg}.test.exe`);
if(mode==='compile'){
 execFileSync('go',['test','-mod=readonly','-p','2','-trimpath','-c','-o',binary,`./${dir}`],{stdio:'inherit'});
 console.log('ABBA_BINARY '+JSON.stringify({package:pkg,sourceBase:'58b894aad81d4b5f77a804fea0fca7c22b7258be',sha256:createHash('sha256').update(readFileSync(binary)).digest('hex')}));
}else if(mode==='run'||mode==='overhead'){
 if(!['full','normal'].includes(policy))throw new Error('unknown policy');
 const started=performance.now(),env={...process.env,VECNET_SQLITE_SYNCHRONOUS:policy};
 let result=0;
 try{
  if(mode==='run')execFileSync(binary,['-test.v','-test.count=1','-test.timeout=50s','-test.run',`^(${roots[pkg]})$`],{cwd:dir,env,stdio:'inherit'});
  else execFileSync('go',['test','-mod=readonly','-p','2','-trimpath','-count=1','-timeout=50s','-v','-run',`^(${roots[pkg]})$`,'.'],{cwd:dir,env,stdio:'inherit'});
 }catch(error){result=error.status??1;}
 console.log('ABBA_RESULT '+JSON.stringify({mode,package:pkg,policy,trial,wallMs:Math.round(performance.now()-started),exit:result}));
 process.exitCode=result;
}else throw new Error('unknown mode');
