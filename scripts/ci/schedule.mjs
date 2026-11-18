// Scheduling policy only. Discovery, not timing observations, determines coverage.
export const goLanes = 12;
export const goGroups = 2;
export const goPartitions = goLanes * goGroups;
// target4 + compile3 + build1 + Go36 + Node3 + E2E3 + artifact4 + accept1.
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
    if (row.names.length < 20) {
      work.push({row, selected:row.names, split:false, cost:Math.max(fallback,row.names.reduce((n,name)=>n+weight(name),0))});
    } else {
      for (const name of row.names) work.push({row, selected:[name], split:true, cost:weight(name)});
    }
  }
  work.sort((a,b)=>b.cost-a.cost || compare(a.row.pkg,b.row.pkg) || compare(a.selected[0]??'',b.selected[0]??''));
  const bins = Array.from({length:goPartitions},()=>({load:0, tasks:new Map()}));
  for (const item of work) {
    // Partition n belongs to lane n%12, group floor(n/12). Balance lane
    // totals as a tie breaker so early stragglers do not share a native runner.
    const laneLoad = lane => bins[lane].load + bins[lane+goLanes].load;
    let index = 0;
    for (let i=1;i<bins.length;i++) {
      if (bins[i].load < bins[index].load || (bins[i].load===bins[index].load && laneLoad(i%goLanes)<laneLoad(index%goLanes))) index=i;
    }
    const bin=bins[index], previous=bin.tasks.get(item.row.pkg);
    if(previous){previous.selected.push(...item.selected);previous.cost+=item.cost;}
    else bin.tasks.set(item.row.pkg,{...item.row,selected:[...item.selected],split:item.split,cost:item.cost});
    bin.load+=item.cost;
  }
  return bins.map(bin=>[...bin.tasks.values()].map(task=>({...task,selected:task.split?task.selected.sort(compare):task.selected})).sort((a,b)=>b.cost-a.cost || compare(a.pkg,b.pkg)));
}

// Do not return on the first error with live children still running. Stop
// dequeuing and drain the other worker; the enclosing budget owns cancellation.
export async function bounded(items, worker) {
  const results=new Array(items.length);let next=0, failure;
  async function run() {
    while(!failure && next<items.length){
      const index=next++;
      try{results[index]=await worker(items[index],index);}catch(error){failure??=error;}
    }
  }
  await Promise.all([run(),run()]);
  if(failure)throw failure;
  return results;
}
