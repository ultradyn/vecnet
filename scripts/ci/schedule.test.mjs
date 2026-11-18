import assert from 'node:assert/strict';
import test from 'node:test';
import { planGo, bounded, runGoGroup, nativeGoWorkers, releaseJobCount, goLanes, nativeGoLanes, goGroups } from './schedule.mjs';
const inventory = [
 {pkg:'heavy', names:Array.from({length:48},(_,i)=>`Test${i}`), binary:'heavy'},
 {pkg:'small', names:['Example','FuzzSeed'],binary:'small'},
 {pkg:'empty', names:[], binary:null},
];
test('weighted whole-root schedule conserves discovered additions and indivisible small/no-test packages',()=>{
 const input=structuredClone(inventory);input[0].names.push('TestNew');
 const profile={packages:{heavy:1},roots:{heavy:{Test0:30,Test1:25}}};
 const plan=planGo(input,profile);
 assert.equal(plan.length,32);
 const tasks=plan.flat();
 assert.deepEqual(tasks.filter(t=>t.pkg==='heavy').flatMap(t=>t.selected).sort(),input[0].names.sort());
 assert.equal(tasks.filter(t=>t.pkg==='small').length,1);
 assert.deepEqual(tasks.find(t=>t.pkg==='small').selected,['Example','FuzzSeed']);
 assert.equal(tasks.filter(t=>t.pkg==='empty').length,1);
 const stragglers=plan.map(tasks=>tasks.flatMap(t=>t.selected));
 assert.notEqual(stragglers.findIndex(s=>s.includes('Test0')),stragglers.findIndex(s=>s.includes('Test1')));
 assert.deepEqual(planGo([...input].reverse(),profile),plan);
 // Split packages may now occupy independent execution packets in one group.
});
test('rejects duplicate packages/roots and malformed inventories or measured costs',()=>{
 assert.throws(()=>planGo([...inventory,inventory[0]]),/duplicate/);
 assert.throws(()=>planGo([{pkg:'x',names:['TestA','TestA']}]),/duplicate/);
 assert.throws(()=>planGo([{pkg:'x',names:['TestA/sub']}]),/root/);
 assert.throws(()=>planGo(inventory,{packages:{heavy:-1}}),/cost/);
});
test('measured expensive roots are balanced rather than colliding by lexical ordinal',()=>{
 const names=Array.from({length:48},(_,i)=>`Test${String(i).padStart(2,'0')}`);
 const plan=planGo([{pkg:'p',names,binary:'p'}],{roots:{p:{Test00:30,Test24:30}},packages:{p:1}});
 assert.ok(plan.every(tasks=>tasks.reduce((n,t)=>n+t.cost,0)<=31));
 const containing=name=>plan.findIndex(tasks=>tasks.some(t=>t.selected.includes(name)));
 assert.notEqual(containing('Test00'),containing('Test24'),'measured roots that collide under ordinal modulo must separate');
});
test('bounded workers overlap, never exceed two and retain result order',async()=>{
 let active=0,peak=0;const starts=[];
 const result=await bounded([0,1,2,3],async n=>{
  starts.push(n);active++;peak=Math.max(peak,active);
  await new Promise(resolve=>setTimeout(resolve,n===0?30:10));active--;return n;
 });
 assert.equal(peak,2);assert.equal(active,0);assert.deepEqual(result,[0,1,2,3]);
 assert.deepEqual(starts.slice(0,2),[0,1]);
});
test('bounded failures stop queued work, drain active work and propagate failure',async()=>{
 let drained=false;const starts=[];
 await assert.rejects(bounded([0,1,2,3],async n=>{
  starts.push(n);await new Promise(resolve=>setTimeout(resolve,n===0?5:20));
  if(n===0)throw new Error('actual failure');drained=true;
 }),/actual failure/);
 assert.deepEqual(starts,[0,1]);assert.equal(drained,true);
});
test('acceptance cardinality includes every native lane and existing release gate',()=>{
 assert.equal(goLanes,8);
 assert.equal(goGroups,4);
 assert.equal(releaseJobCount(),37);
 assert.equal(nativeGoLanes('linux'),2);assert.equal(nativeGoLanes('win32'),8);
 const input=[{pkg:'p',names:Array.from({length:401},(_,i)=>`Test${i}`)}];
 for(const platform of ['linux','win32']){
  const plan=planGo(input,{}, {lanes:nativeGoLanes(platform)});
  assert.equal(plan.length,nativeGoLanes(platform)*4);
  assert.deepEqual(plan.flat().flatMap(t=>t.selected).sort(),input[0].names.toSorted());
 }
});
test('workflow execution lanes and cold-cache generation match the scheduler',async()=>{
 const {readFile}=await import('node:fs/promises');
 const workflow=await readFile(new URL('../../.github/workflows/release.yml',import.meta.url),'utf8');
 const windows=workflow.match(/strategy: &windows-go-strategy[\s\S]*?lane: (\[[^\n]+\])/);
 assert.deepEqual(JSON.parse(windows[1]),Array.from({length:nativeGoLanes('win32')},(_,i)=>i));
 assert.match(workflow,/test-go-windows-arm:[\s\S]*?strategy: \*windows-go-strategy/);
 const linux=workflow.match(/test-go-linux:[\s\S]*?lane: (\[[^\n]+\])/);
 assert.deepEqual(JSON.parse(linux[1]),Array.from({length:nativeGoLanes('linux')},(_,i)=>i));
 // Two producer definitions, each with primary and both fallback prefixes.
 const keys=workflow.split('\n').filter(line=>line.includes('go-compile-v'));
 assert.equal(keys.length,6);
 assert.ok(keys.every(line=>line.includes('go-compile-v2-')));
});
test('scheduled partitions distribute all no-test packages, not just packages with roots',()=>{
 const empty=Array.from({length:46},(_,i)=>({pkg:`empty${i}`,names:[],binary:null}));
 const tasks=planGo(empty).flat();
 assert.equal(tasks.length,46);assert.equal(new Set(tasks.map(t=>t.pkg)).size,46);
});
test('package launch overhead is counted once after batching, including no-test scheduling',()=>{
 const plan=planGo([{pkg:'p',names:Array.from({length:48},(_,i)=>`Test${i}`)}],{packages:{p:1},startup:0.25});
 for(const task of plan.flat())assert.equal(task.cost,task.selected.length+0.25);
});
test('indivisible small packages use complete measured process cost, not serial sum of parallel roots',()=>{
 const tasks=planGo([{pkg:'parallel',names:['TestA','TestB']}],{
  packages:{parallel:5.1},roots:{parallel:{TestA:10,TestB:10}},packageElapsed:{parallel:8},startup:0.1,
 }).flat();
 assert.equal(tasks.length,1);
 assert.equal(tasks[0].cost,8);
});

test('measured 42s small package splits complete discovered roots exactly once, including new roots',()=>{
 const names=['TestSlow','TestParallel','Example','FuzzSeed','TestNew'];
 const tasks=planGo([{pkg:'heavy-small',names}],{
  packageElapsed:{'heavy-small':42},packages:{'heavy-small':5},
  roots:{'heavy-small':{TestSlow:20,TestParallel:10,Example:2,FuzzSeed:1}},
 }).flat();
 assert.equal(tasks.length,5);
 assert.ok(tasks.every(t=>t.split));
 assert.deepEqual(tasks.flatMap(t=>t.selected).sort(),[...names].sort());
 assert.equal(tasks.find(t=>t.selected.includes('TestNew')).cost,5);
});
test('cheap, unknown small, single-root and no-test packages remain whole with actual elapsed cost',()=>{
 const tasks=planGo([
  {pkg:'cheap',names:['TestA','TestB']},{pkg:'unknown',names:['TestA','Example']},
  {pkg:'single',names:['TestOnly']},{pkg:'empty',names:[]},
 ],{packageElapsed:{cheap:8,single:42,empty:42},roots:{cheap:{TestA:10,TestB:10}},startup:0.25}).flat();
 assert.equal(tasks.length,4);assert.ok(tasks.every(t=>!t.split));
 assert.equal(tasks.find(t=>t.pkg==='cheap').cost,8);
 assert.equal(tasks.find(t=>t.pkg==='single').cost,42);
 assert.equal(tasks.find(t=>t.pkg==='empty').cost,42);
 assert.deepEqual(tasks.find(t=>t.pkg==='unknown').selected,['TestA','Example']);
});
const turn=()=>new Promise(resolve=>setImmediate(resolve));
test('explicit four workers reach four, never exceed four and preserve index results',async()=>{
 let active=0,peak=0;const starts=[],release=[];
 const run=bounded([0,1,2,3,4,5],async(n,index)=>{
  starts.push(n);active++;peak=Math.max(peak,active);
  await new Promise(resolve=>{release[n]=resolve;});active--;return `${index}:${n}`;
 },4);
 await turn();const initial=[...starts];
 // Also release queued work on the old default-two implementation so RED terminates.
 for(let i=0;i<6;i++){for(const resolve of [...release].reverse())resolve?.();await turn();}
 const results=await run;
 assert.deepEqual(initial,[0,1,2,3]);assert.equal(peak,4);assert.equal(active,0);
 assert.deepEqual(results,['0:0','1:1','2:2','3:3','4:4','5:5']);
});
for(const width of [4,6]) test(`${width}-worker failure stops dequeue, drains active workers and preserves the first error`,async()=>{
 const first=new Error('first observed failure'),later=new Error('later failure');
 const items=Array.from({length:width+2},(_,id)=>id),admitted=items.slice(0,width);
 const starts=[],finished=[],release=[];let settled=false;
 const run=bounded(items,async n=>{
  starts.push(n);await new Promise(resolve=>{release[n]=resolve;});finished.push(n);
  if(n===1)throw first;if(n===0)throw later;
 },width);
 const outcome=run.then(()=>{settled=true;return null;},error=>{settled=true;return error;});
 await turn();const initial=[...starts];
 release[1]();await turn();release[0]();await turn();
 const early=settled;
 for(let i=2;i<width-1;i++)release[i]?.();
 await turn();const beforeLast=settled;
 release[width-1]?.();const error=await outcome;
 assert.deepEqual(initial,admitted);assert.deepEqual(starts,admitted);
 assert.equal(early,false);assert.equal(beforeLast,false);
 assert.deepEqual(finished.sort(),admitted);assert.equal(error,first);
});
test('invalid explicit pool capacity rejects before starting any work',async()=>{
 for(const capacity of [0,-1,1.5,NaN,Infinity,'4',null]){
  let calls=0;await assert.rejects(bounded([1],async()=>{calls++;},capacity),/positive integer/);
  assert.equal(calls,0);
 }
 assert.deepEqual(await bounded([1,2],async n=>n,1),[1,2]);
});
test('falsey first rejection still stops dequeue and survives later errors',async()=>{
 const starts=[];let drained=false;
 const outcome=await bounded([0,1,2],async n=>{
  starts.push(n);await turn();if(n===0)throw null;drained=true;throw new Error('later');
 }).then(()=>({ok:true}),error=>({error}));
 assert.deepEqual(starts,[0,1]);assert.equal(drained,true);assert.deepEqual(outcome,{error:null});
});

test('large split packages form bounded packets, preserving every root including unknown additions',()=>{
 const names=Array.from({length:1200},(_,n)=>`TestRoot${n}`);
 const input=[{pkg:'large',names:[...names,'Example','FuzzSeed','TestNew'],binary:'native',directory:'original'}];
 const profile={packages:{large:0.3},roots:{large:{TestRoot0:9}},startup:0.2};
 const plan=planGo(input,profile),tasks=plan.flat();
 assert.deepEqual(tasks.flatMap(t=>t.selected).sort(),input[0].names.toSorted());
 assert.ok(plan.some(group=>group.filter(t=>t.pkg==='large').length>1),'pool can run multiple packets of same package');
 for(const task of tasks){
  assert.ok(task.selected.length<=8);
  assert.ok(task.cost<=4.2+1e-9 || task.selected.length===1,'only indivisible expensive roots exceed packet cost');
  assert.equal(task.directory,'original');assert.equal(task.binary,'native');
 }
 const placements=p=>p.map(group=>group.map(({selected,cost})=>({selected,cost})));
 assert.deepEqual(placements(planGo([{...input[0],names:[...input[0].names].reverse()}],profile)),placements(plan));
});

test('compile capacity defaults to two, permits explicit bounded jobs and rejects malformed overrides',async()=>{
 const {goCompileJobs}=await import('./schedule.mjs');
 assert.equal(typeof goCompileJobs,'function','shared compile capacity policy exists');
 assert.equal(goCompileJobs(undefined),'2');
 for(const value of ['1','2','3','4','5','6'])assert.equal(goCompileJobs(value,6),value);
 for(const value of ['', '0','7','-1','2.5','04',' 4','4 ','NaN','Infinity',null,4]){
  assert.throws(()=>goCompileJobs(value),/CI_GO_COMPILE_JOBS/);
 }
});
test('invalid compile override fails closed at each producer entry point before Go work',async()=>{
 const {spawnSync}=await import('node:child_process');
 const {fileURLToPath}=await import('node:url');
 for(const [file,args] of [
  ['./suite.mjs',['prepare-go','0']],
  ['./go-bundle.mjs',['compile','linux-x64','0']],
  ['./go-bundle.mjs',['finalize','linux-x64']],
 ]){
  const child=spawnSync(process.execPath,[fileURLToPath(new URL(file,import.meta.url)),...args],{
   env:{...process.env,CI_GO_COMPILE_JOBS:'7',PATH:''},encoding:'utf8',timeout:5000,
  });
  assert.equal(child.status,1);
  assert.match(child.stderr,/CI_GO_COMPILE_JOBS must be an integer from 1 to 6/);
  assert.doesNotMatch(child.stderr,/spawnSync go|ENOENT/);
 }
});

test('explicit compile jobs cap to available CPUs and use the same Go runtime environment',async()=>{
 const {goCompileJobs,goCompileEnvironment}=await import('./schedule.mjs');
 assert.equal(goCompileJobs('4',2),'2');assert.equal(goCompileJobs('4',4),'4');
 assert.equal(goCompileJobs(undefined,1),'2','unset compatibility default remains two');
 assert.equal(typeof goCompileEnvironment,'function');
 const env={GOMAXPROCS:'4',CI_GO_COMPILE_JOBS:'4',KEEP:'original'};
 assert.deepEqual(goCompileEnvironment(env,2),{...env,GOMAXPROCS:'2'});
 assert.deepEqual(goCompileEnvironment(env,4),env);
 assert.deepEqual(goCompileEnvironment({GOMAXPROCS:'2'},4),{GOMAXPROCS:'2'});
 for(const available of [0,-1,1.5,NaN,Infinity,'2'])assert.throws(()=>goCompileJobs('4',available),/available parallelism/);
});

const ssePackage='github.com/XertroV/vecnet/internal/httpapi';
const sseRoot='TestSlowSSEConsumerDoesNotBlockSend';

test('Windows makes the discovered SSE root a singleton even in a small, cheap package',()=>{
 const inv=[{pkg:ssePackage,names:[sseRoot,'TestNew','Example','FuzzSeed'],binary:'api',directory:'original'},
  {pkg:'other',names:[sseRoot],binary:'other'}];
 const windows=planGo(inv,{}, {platform:'win32'}).flat();
 assert.equal(windows.filter(t=>t.exclusive).length,1);
 const isolated=windows.find(t=>t.exclusive);
 assert.equal(isolated.pkg,ssePackage);
 assert.deepEqual(isolated.selected,[sseRoot]);
 assert.equal(isolated.split,true,'a singleton must retain its native root filter');
 assert.equal(isolated.binary,'api');assert.equal(isolated.directory,'original');
 assert.deepEqual(windows.filter(t=>t.pkg===ssePackage).flatMap(t=>t.selected).sort(),inv[0].names.toSorted());
 assert.ok(windows.filter(t=>t.pkg===ssePackage).every(t=>t.split),'no unfiltered repeat of the SSE root');
 const absent=planGo([{...inv[0],names:['TestNew']}],{}, {platform:'win32'}).flat();
 assert.equal(absent.flatMap(t=>t.selected).join(','),'TestNew');
 assert.ok(absent.every(t=>!t.exclusive));
 const linux=planGo(inv,{}, {platform:'linux',lanes:2}).flat();
 assert.ok(linux.every(t=>!t.exclusive));
 assert.equal(linux.filter(t=>t.pkg===ssePackage).length,1);
});

test('Windows Go group finishes its exclusive packet before using all six ordinary workers',async()=>{
 const packets=Array.from({length:8},(_,id)=>({id,...(id===1?{exclusive:true}:{})}));
 const starts=[],release=[];
 const run=runGoGroup(packets,async ({id})=>{
  starts.push(id);await new Promise(resolve=>{release[id]=resolve;});
 },nativeGoWorkers('win32'));
 const initial=[...starts];
 release[1]?.();await turn();
 const afterExclusive=[...starts];
 for(let i=0;i<8;i++){for(const finish of release)finish?.();await turn();}
 await run;
 assert.deepEqual(initial,[1]);
 assert.deepEqual(afterExclusive,[1,0,2,3,4,5,6]);
 assert.deepEqual(starts,[1,0,2,3,4,5,6,7]);
});

test('an exclusive Go packet failure prevents every ordinary launch and propagates unchanged',async()=>{
 const failure=new Error('native SSE assertion');
 const starts=[];
 await assert.rejects(runGoGroup([{id:0},{id:1,exclusive:true},{id:2}],async ({id})=>{
  starts.push(id);
  if(id===1)throw failure;
 },4),e=>e===failure);
 assert.deepEqual(starts,[1]);
});

test('Windows admits the exact installed PowerShell hook root alone without matching other hook tests',()=>{
 const pkg='github.com/XertroV/vecnet/internal/cli';
 const root='TestCodexHookWindowsInstalledCommandPreservesArgv';
 const sibling=root+'Extended';
 const inv=[{pkg,names:[sibling,root],binary:'cli'}];
 const packets=planGo(inv,{roots:{[pkg]:{[root]:25}}},{platform:'win32'}).flat();
 assert.equal(packets.filter(t=>t.exclusive).map(t=>t.selected.join()).join(),root);
 assert.equal(packets.filter(t=>!t.exclusive).map(t=>t.selected.join()).join(),sibling);
 assert.ok(packets.every(t=>t.split&&t.binary==='cli'));
});

test('Windows admits the whole repair root and PowerShell root as separate serial packets',async()=>{
 const pkg='github.com/XertroV/vecnet/internal/cli';
 const repair='TestLauncherWindowsManagedRepairPreservesServiceIntent';
 const hook='TestCodexHookWindowsInstalledCommandPreservesArgv';
 const ordinary=repair+'Extended';
 const inv=[{pkg,names:[hook,repair,ordinary],binary:'cli',directory:'original'},
  ...['a','b','c'].map(pkg=>({pkg,names:[repair],binary:pkg}))];
 const profile={packages:{a:4,b:4,c:4},roots:{[pkg]:{[repair]:2,[hook]:1}}};
 const plan=planGo(inv,profile,{platform:'win32',lanes:1});
 const roots=groups=>groups.flat().flatMap(t=>t.selected.map(name=>`${t.pkg}/${name}`)).sort();
 const discovered=inv.flatMap(p=>p.names.map(name=>`${p.pkg}/${name}`)).sort();
 assert.deepEqual(roots(plan),discovered);
 const exclusive=plan.flat().filter(t=>t.exclusive).sort((a,b)=>b.cost-a.cost);
 assert.deepEqual(exclusive.map(t=>t.selected),[[repair],[hook]]);
 assert.ok(exclusive.every(t=>t.split&&t.pkg===pkg&&t.binary==='cli'&&t.directory==='original'));
 const group=[plan.flat().find(t=>t.pkg===pkg&&!t.exclusive),...exclusive];

 const starts=[],release=new Map();
 const run=runGoGroup(group,async task=>{
  const name=task.selected.join(',');
  starts.push(name);
  await new Promise(finish=>release.set(name,finish));
 },4);
 const initial=[...starts];
 release.get(repair)?.();await turn();
 const afterRepair=[...starts];
 for(let i=0;i<group.length;i++){for(const finish of release.values())finish();await turn();}
 await run;
 assert.deepEqual(initial,[repair]);
 assert.deepEqual(afterRepair,[repair,hook]);
 assert.deepEqual(starts,[repair,hook,ordinary]);

 const linux=planGo(inv,profile,{platform:'linux',lanes:2});
 assert.deepEqual(roots(linux),discovered);
 assert.ok(linux.flat().every(t=>!t.exclusive));
 assert.equal(linux.flat().filter(t=>t.pkg===pkg).length,1);
});

test('native Go worker policy admits six Windows and four Linux packets, with a two-worker fallback',async()=>{
 const starts=[];
 for(const platform of ['win32','linux','darwin']){
  const items=Array.from({length:8},(_,id)=>({id}));
  let admitted=0;const releases=[];
  const running=runGoGroup(items,async ({id})=>{
   admitted++;
   await new Promise(resolve=>{releases[id]=resolve;});
  },nativeGoWorkers(platform));
  starts.push(admitted);
  for(let i=0;i<items.length;i++){for(const release of releases)release?.();await turn();}
  await running;
 }
 assert.deepEqual(starts,[6,4,2]);
});

test('Windows isolates the three observed cold Node admission failures, scoped to exact CLI roots',async()=>{
 const pkg='github.com/XertroV/vecnet/internal/cli';
 const names=['TestLauncherWindowsInstallRejectsUnsafeDACLAndHardlinks','TestLauncherWindowsRepairPendingDriftUnit','TestLauncherWindowsInstallPreviewAndExplicitIntent'];
 const ordinary=['TestLauncherWindowsRepairPendingDriftTask',names[0]+'Extended','TestNew','Example','FuzzSeed'];
 const input=[{pkg,names:[...names,...ordinary],binary:'real-cli',directory:'original'},
  {pkg:'other',names:[names[0]]},{pkg:'empty',names:[],binary:null}];
 const plan=planGo(input,{}, {platform:'win32',lanes:2});
 const packets=plan.flat();
 assert.equal(packets.filter(t=>t.exclusive).length,names.length);
 assert.deepEqual(packets.filter(t=>t.exclusive).flatMap(t=>t.selected).sort(),names.toSorted());
 assert.ok(packets.filter(t=>t.exclusive).every(t=>t.pkg===pkg&&t.split&&t.selected.length===1&&t.binary==='real-cli'&&t.directory==='original'));
 const discovered=input.flatMap(t=>(t.names.length?t.names:['']).map(n=>`${t.pkg}/${n}`)).sort();
 const roots=tasks=>tasks.flatMap(t=>(t.selected.length?t.selected:['']).map(n=>`${t.pkg}/${n}`)).sort();
 assert.deepEqual(roots(packets),discovered);
 const linux=planGo(input,{}, {platform:'linux',lanes:2}).flat();
 assert.deepEqual(roots(linux),discovered);
 assert.ok(linux.every(t=>!t.exclusive));
 for(const group of plan){
  const starts=[];
  await runGoGroup(group,async t=>{starts.push(!!t.exclusive);},4);
  assert.deepEqual(starts,group.map(t=>!!t.exclusive).sort((a,b)=>Number(b)-Number(a)));
 }
});

test('Windows keeps an existing group free of any repair tail even with cheap ordinary work',()=>{
 const pkg='github.com/XertroV/vecnet/internal/cli';
 const repair='TestLauncherWindowsManagedRepairPreservesServiceIntent';
 const input=[{pkg,names:[repair]},...Array.from({length:12},(_,i)=>({pkg:`other${i}`,names:['TestOrdinary']}))];
 const profile={roots:{[pkg]:{[repair]:2}}};
 const bins=planGo(input,profile,{platform:'win32',lanes:1});
 const repairBin=bins.find(g=>g.some(t=>t.selected.includes(repair)));
 assert.equal(repairBin.length,1);
 assert.equal(repairBin[0].exclusive,true);
});

test('Windows placement prices the serial prefix separately and leaves whole repair no queued remainder',()=>{
 const pkg='github.com/XertroV/vecnet/internal/cli';
 const repair='TestLauncherWindowsManagedRepairPreservesServiceIntent';
 const dacl='TestLauncherWindowsInstallRejectsUnsafeDACLAndHardlinks';
 const input=[{pkg,names:[repair,dacl],binary:'real-cli'},
  ...Array.from({length:12},(_,j)=>({pkg:`p${j}`,names:['TestHeavy']}))];
 const profile={roots:{[pkg]:{[repair]:28,[dacl]:28}},packages:Object.fromEntries(input.slice(1).map(t=>[t.pkg,14]))};
 const bins=planGo(input,profile,{platform:'win32',lanes:1});
 const repairBin=bins.find(g=>g.some(t=>t.selected.includes(repair)));
 assert.equal(repairBin.length,1);
 assert.equal(repairBin[0].exclusive,true);
 const daclBin=bins.find(g=>g.some(t=>t.selected.includes(dacl)));
 assert.equal(daclBin.length,1,'ordinary workers should use the low-makespan bins, not load up an expensive serial prefix');
 assert.equal(bins.flat().filter(t=>t.selected[0]==='TestHeavy').length,12);
 assert.ok(bins.filter(g=>g!==repairBin&&g!==daclBin).every(g=>g.length<=8));
 assert.equal(JSON.stringify(bins),JSON.stringify(planGo([...input].reverse(),profile,{platform:'win32',lanes:1})));
});

test('Windows serial prefix adds only the exact unsafe-executable CLI root and keeps ordinary admission',async()=>{
 const pkg='github.com/XertroV/vecnet/internal/cli';
 const unsafe='TestLauncherWindowsInstallRejectsUnsafeExecutableBeforeExecution';
 const previous=[sseRoot,'TestCodexHookWindowsInstalledCommandPreservesArgv',
  'TestLauncherWindowsInstallPreviewAndExplicitIntent','TestLauncherWindowsInstallRejectsUnsafeDACLAndHardlinks',
  'TestLauncherWindowsRepairPendingDriftUnit','TestLauncherWindowsManagedRepairPreservesServiceIntent'];
 const ordinary=[unsafe+'Extended','TestLauncherWindowsInstallRejectsUnsafeExecutable','Example','FuzzSeed'];
 const inv=[{pkg,names:[unsafe,...previous.slice(1),...ordinary],binary:'cli',directory:'original'},
  {pkg:ssePackage,names:[sseRoot]}, {pkg:'other',names:[unsafe]}, {pkg:'empty',names:[],binary:null}];
 const keys=(pkg,names)=>(names.length?names:['']).map(name=>`${pkg}/${name}`);
 const discovered=inv.flatMap(t=>keys(t.pkg,t.names)).sort();
 const packets=planGo(inv,{}, {platform:'win32'}).flat();
 assert.deepEqual(packets.flatMap(t=>keys(t.pkg,t.selected)).sort(),discovered);
 assert.deepEqual(packets.filter(t=>t.exclusive).flatMap(t=>keys(t.pkg,t.selected)).sort(),
  [...keys(ssePackage,[sseRoot]),...keys(pkg,[unsafe,...previous.slice(1)])].sort());
 const isolated=packets.find(t=>t.exclusive&&t.selected.includes(unsafe));
 assert.deepEqual(isolated.selected,[unsafe]);
 assert.ok(isolated.split&&isolated.binary==='cli'&&isolated.directory==='original');
 for(const platform of ['linux','darwin']){
  const other=planGo(inv,{}, {platform}).flat();
  assert.ok(other.every(t=>!t.exclusive));
  assert.deepEqual(other.flatMap(t=>keys(t.pkg,t.selected)).sort(),discovered);
 }
 const neighbors=Array.from({length:7},(_,id)=>({id}));
 const starts=[];let finishExclusive;
 const run=runGoGroup([...neighbors,isolated],async task=>{
  if(task===isolated){
   starts.push(unsafe);
   await new Promise(resolve=>{finishExclusive=resolve;});
  }else starts.push(task.id);
 },nativeGoWorkers('win32'));
 const beforeRelease=[...starts];
 finishExclusive();await run;
 assert.deepEqual(beforeRelease,[unsafe]);
 assert.deepEqual(starts,[unsafe,0,1,2,3,4,5,6]);
});

test('Windows artifact eligibility keeps producer readiness and acceptance keeps every proof',async()=>{
 const {readFile}=await import('node:fs/promises');
 const workflow=await readFile(new URL('../../.github/workflows/release.yml',import.meta.url),'utf8');
 const needs=name=>workflow.split(`  ${name}:\n`)[1].match(/^    needs: \[([^\n]+)\]$/m)[1].split(', ');
 assert.deepEqual(needs('test-artifact-windows'),['build','test-compile-windows-arm']);
 assert.deepEqual(needs('accept'),[
  'target','test-compile-linux','test-compile-windows-x64','test-compile-windows-arm','build',
  'test-go-linux','test-go-windows-x64','test-go-windows-arm',
  'test-node-linux','test-node-windows-x64','test-node-windows-arm',
  'test-e2e','test-artifact-windows','test-artifact-linux',
 ]);
});
