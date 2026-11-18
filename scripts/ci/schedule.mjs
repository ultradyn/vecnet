import {availableParallelism} from 'node:os';
// Scheduling policy only. Discovery, not timing observations, determines coverage.
export const goLanes = 6;
export const goGroups = 4;
export const nativeGoLanes = platform => platform==='linux'?2:goLanes;
// Only the hosted native Go execution step opts into the wider pool.
export const nativeGoWorkers = platform => platform==='win32'?6:platform==='linux'?4:2;
export const goPartitions = goLanes * goGroups;
// target4 + compile3 + build1 + Go(2+6+6) + Node3 + E2E3 + artifact4 + accept1.
export const releaseJobCount = () => 4 + 3 + 1 + nativeGoLanes('linux') + 2 * goLanes + 3 + 3 + 4 + 1;
// Explicit producer opt-in only; unrelated callers retain the two-job default.
export function goCompileJobs(value, available=availableParallelism()) {
  if(value===undefined)return '2';
  if(typeof value!=='string'||!/^[1-6]$/.test(value))throw new Error('CI_GO_COMPILE_JOBS must be an integer from 1 to 6');
  if(!Number.isInteger(available)||available<1)throw new Error('invalid available parallelism');
  return String(Math.min(Number(value),available));
}
export function goCompileEnvironment(env, available=availableParallelism()) {
  const jobs=goCompileJobs(env.CI_GO_COMPILE_JOBS,available);
  return env.CI_GO_COMPILE_JOBS===undefined?env:{...env,GOMAXPROCS:jobs};
}
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const packetOrder = (a,b) => b.cost-a.cost || compare(a.pkg,b.pkg) || compare(a.selected[0]??'',b.selected[0]??'');
function windowsGroupCost(tasks) {
  // Simulate the same packet order and the configured ordinary width as runGoGroup.
  const loads=Array(nativeGoWorkers('win32')).fill(0);
  let prefix=0, ordinary=0;
  for(const task of tasks.toSorted(packetOrder)){
    if(task.exclusive){prefix+=task.cost;continue;}
    loads[loads.indexOf(Math.min(...loads))]+=task.cost;
    ordinary+=task.cost;
  }
  return [prefix+Math.max(...loads),prefix+ordinary/loads.length];
}
const cost = n => {
  if (!Number.isFinite(n) || n <= 0) throw new Error('invalid scheduling cost');
  return n;
};
// Resource admission for discovered Windows roots, not coverage selection.
const repairRoot = 'TestLauncherWindowsManagedRepairPreservesServiceIntent';
const exclusiveGoRoots = new Map([
  ['github.com/XertroV/vecnet/internal/httpapi',['TestSlowSSEConsumerDoesNotBlockSend']],
  ['github.com/XertroV/vecnet/internal/cli',[
    'TestCodexHookWindowsInstalledCommandPreservesArgv',
    // These three real Node admissions overlapped and timed out on ARM64.
    'TestLauncherWindowsInstallPreviewAndExplicitIntent',
    'TestLauncherWindowsInstallRejectsUnsafeDACLAndHardlinks',
    'TestLauncherWindowsRepairPendingDriftUnit',
    repairRoot,
  ]],
]);
export function planGo(inventory, profile = {}, {lanes=goLanes,platform=process.platform} = {}) {
  if(!Number.isInteger(lanes)||lanes<1)throw new Error('invalid lane count');
  const seen = new Set(), work = [];
  for (const row of inventory) {
    if (!row.pkg || seen.has(row.pkg)) throw new Error('duplicate/invalid package inventory');
    seen.add(row.pkg);
    if (!Array.isArray(row.names) || row.names.some(n => !/^(?:Test\w+|Example\w*|Fuzz\w+)$/.test(n))) throw new Error('invalid root inventory');
    if (new Set(row.names).size !== row.names.length) throw new Error('duplicate root inventory');
    const fallback = cost(profile.packages?.[row.pkg] ?? 0.1);
    const weight = name => cost(profile.roots?.[row.pkg]?.[name] ?? fallback);
    const measured = profile.packageElapsed?.[row.pkg];
    if (measured !== undefined) cost(measured);
    // ~70s remaining across four groups gives ~17.5s/group. An 8s threshold
    // leaves placement headroom; it is policy, not a measured runtime forecast.
    const exclusive = name => platform==='win32' && exclusiveGoRoots.get(row.pkg)?.includes(name);
    const rootSum = row.names.reduce((n,name)=>n+weight(name),0);
    const split = row.names.some(exclusive) || row.names.length >= 20 || (row.names.length >= 2 && (measured ?? (platform==='win32'?rootSum:0)) > 8);
    if (!split) {
      // Whole packages use process elapsed; summed roots overcount parallel tests.
      work.push({row, selected:row.names, split:false, cost:measured === undefined ? Math.max(fallback,rootSum) : cost(measured), measured:measured!==undefined});
    } else {
      for (const name of row.names) work.push({row, selected:[name], split:true, exclusive:exclusive(name), cost:weight(name)});
    }
  }
  const windows=platform==='win32';
  work.sort((a,b)=>(windows?Number(!!b.exclusive)-Number(!!a.exclusive):0) ||
    b.cost-a.cost || compare(a.row.pkg,b.row.pkg) || compare(a.selected[0]??'',b.selected[0]??''));
  const bins = Array.from({length:lanes*goGroups},()=>({load:0, tasks:[]}));
  const laneLoad = lane => Array.from({length:goGroups},(_,group)=>bins[group*lanes+lane].load).reduce((a,b)=>a+b,0);
  for (const item of work) {
    const startup=item.measured?0:(profile.startup===undefined?0:cost(profile.startup));
    const task={...item.row,selected:[...item.selected],split:item.split,...(item.exclusive?{exclusive:true}:{}),rootCost:item.cost,cost:item.cost+startup};
    const add = tasks => {
      // Keep the packet caps: an indivisible root can exceed the cost cap.
      const previous=item.split && !item.exclusive ? tasks.findLastIndex(t=>t.pkg===task.pkg&&t.split&&!t.exclusive&&t.selected.length+task.selected.length<=8&&t.rootCost+item.cost<=4) : -1;
      if(previous===-1)return {tasks:[...tasks,task],newPacket:true};
      const old=tasks[previous], next=tasks.slice();
      next[previous]={...old,selected:[...old.selected,...task.selected].sort(compare),rootCost:old.rootCost+item.cost,cost:old.cost+item.cost};
      return {tasks:next,newPacket:false};
    };
    if(windows){
      let best;
      for(const [i,bin] of bins.entries()){
        const candidate=add(bin.tasks), [elapsed,pressure]=windowsGroupCost(candidate.tasks);
        const sharesRepair=candidate.tasks.length>1&&candidate.tasks.some(t=>t.exclusive&&t.selected[0]===repairRoot);
        // Keep one existing group for the whole repair root. It passed at 28s
        // while its remaining pool hit the owner; even 50s must leave no tail.
        // The 37s soft target leaves residual/noise headroom, never omits work.
        const score=[Number(sharesRepair),Math.max(0,elapsed-37),laneLoad(i%lanes)+elapsed-bin.load,elapsed,pressure];
        // Compare lexicographically; the first equal slot wins deterministically.
        const diff=best?.score.findIndex((v,j)=>v!==score[j]);
        if(!best||(diff>=0&&score[diff]<best.score[diff]))best={bin,tasks:candidate.tasks,elapsed,score};
      }
      best.bin.tasks=best.tasks;best.bin.load=best.elapsed;
    } else {
      // Keep non-Windows placement unchanged: scalar load, then lane totals.
      let index=0;
      for(let i=1;i<bins.length;i++){
        if(bins[i].load<bins[index].load||(bins[i].load===bins[index].load&&laneLoad(i%lanes)<laneLoad(index%lanes)))index=i;
      }
      const bin=bins[index], candidate=add(bin.tasks);
      bin.tasks=candidate.tasks;
      // Retain the historical addition order for exact Linux ties.
      if(candidate.newPacket)bin.load+=startup;
      bin.load+=item.cost;
    }
  }
  return bins.map(bin=>bin.tasks.map(({rootCost,...task})=>({...task,selected:task.split?task.selected.sort(compare):task.selected})).sort(packetOrder));
}

// Do not return on the first error with live children still running. Stop
// dequeuing and drain ALL active workers; the enclosing budget owns cancellation.
export async function bounded(items, worker, concurrency = 2) {
  if (!Number.isInteger(concurrency) || concurrency <= 0) throw new Error('concurrency must be a positive integer');
  const results=new Array(items.length);let next=0, failed=false, failure;
  async function run() {
    while(!failed && next<items.length){
      const index=next++;
      try{results[index]=await worker(items[index],index);}catch(error){if(!failed){failed=true;failure=error;}}
    }
  }
  await Promise.all(Array.from({length:Math.min(concurrency,items.length)},()=>run()));
  if(failed)throw failure;
  return results;
}

// A serial prefix owns the runner before any ordinary packet is admitted.
// Both phases remain inside the same step owner and native test timeouts.
export async function runGoGroup(tasks, worker, concurrency) {
  for (const task of tasks) if (task.exclusive) await worker(task);
  await bounded(tasks.filter(task=>!task.exclusive),worker,concurrency);
}
