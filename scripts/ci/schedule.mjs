// Scheduling policy only. Discovery, not timing observations, determines coverage.
export const goLanes = 6;
export const goGroups = 4;
export const goPartitions = goLanes * goGroups;
// target4 + compile3 + build1 + Go18 + Node3 + E2E3 + artifact4 + accept1.
export const releaseJobCount = () => 4 + 3 + 1 + 3 * goLanes + 3 + 3 + 4 + 1;
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const cost = n => {
  if (!Number.isFinite(n) || n <= 0) throw new Error('invalid scheduling cost');
  return n;
};
export function planGo(inventory, profile = {}) {
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
    const split = row.names.length >= 20 || (row.names.length >= 2 && measured > 8);
    if (!split) {
      // Whole packages use process elapsed; summed roots overcount parallel tests.
      work.push({row, selected:row.names, split:false, cost:measured === undefined ? Math.max(fallback,row.names.reduce((n,name)=>n+weight(name),0)) : cost(measured), measured:measured!==undefined});
    } else {
      for (const name of row.names) work.push({row, selected:[name], split:true, cost:weight(name)});
    }
  }
  work.sort((a,b)=>b.cost-a.cost || compare(a.row.pkg,b.row.pkg) || compare(a.selected[0]??'',b.selected[0]??''));
  const bins = Array.from({length:goPartitions},()=>({load:0, tasks:new Map()}));
  for (const item of work) {
    // Partition n belongs to lane n%goLanes, group floor(n/goLanes). Balance lane
    // totals as a tie breaker so early stragglers do not share a native runner.
    const laneLoad = lane => Array.from({length:goGroups},(_,group)=>bins[group*goLanes+lane].load).reduce((a,b)=>a+b,0);
    let index = 0;
    for (let i=1;i<bins.length;i++) {
      if (bins[i].load < bins[index].load || (bins[i].load===bins[index].load && laneLoad(i%goLanes)<laneLoad(index%goLanes))) index=i;
    }
    const bin=bins[index], previous=bin.tasks.get(item.row.pkg);
    if(previous){previous.selected.push(...item.selected);previous.cost+=item.cost;}
    else bin.tasks.set(item.row.pkg,{...item.row,selected:[...item.selected],split:item.split,cost:item.cost,measured:item.measured});
    bin.load+=item.cost;
  }
  return bins.map(bin=>[...bin.tasks.values()].map(task=>({...task,cost:task.cost+(task.measured?0:(profile.startup??0)),selected:task.split?task.selected.sort(compare):task.selected})).sort((a,b)=>b.cost-a.cost || compare(a.pkg,b.pkg)));
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
