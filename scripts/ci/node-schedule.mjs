// Runtime discovery supplies every file. Measurements only choose placement;
// unknown files are whole tasks with a positive conservative cost.
export function planNode(files, profile = {}) {
 if(new Set(files).size!==files.length)throw new Error('duplicate Node inventory');
 const work=files.flatMap(file=>{
  const row=profile[file.replaceAll('\\','/')]??{seconds:5};
  const shards=row.shards??1;
  if(!Number.isFinite(row.seconds)||row.seconds<=0||!Number.isInteger(shards)||shards<1||shards>4)throw new Error('invalid Node cost/shards');
  return Array.from({length:shards},(_,shard)=>({file,shard,shards,seconds:row.seconds/shards}));
 }).sort((a,b)=>b.seconds-a.seconds||a.file.localeCompare(b.file)||a.shard-b.shard);
 const groups=Array.from({length:4},()=>({load:0,tasks:[]}));
 for(const task of work){
  const available=groups.filter(g=>!g.tasks.some(t=>t.file===task.file));
  available.sort((a,b)=>a.load-b.load||groups.indexOf(a)-groups.indexOf(b));
  available[0].tasks.push(task);available[0].load+=task.seconds;
 }
 return groups.map(g=>g.tasks);
}
