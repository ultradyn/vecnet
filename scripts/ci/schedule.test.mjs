import assert from 'node:assert/strict';
import test from 'node:test';
import { planGo, bounded, releaseJobCount, goLanes, goGroups } from './schedule.mjs';
const inventory = [
 {pkg:'heavy', names:Array.from({length:48},(_,i)=>`Test${i}`), binary:'heavy'},
 {pkg:'small', names:['Example','FuzzSeed'],binary:'small'},
 {pkg:'empty', names:[], binary:null},
];
test('weighted whole-root schedule conserves discovered additions and indivisible small/no-test packages',()=>{
 const input=structuredClone(inventory);input[0].names.push('TestNew');
 const profile={packages:{heavy:1},roots:{heavy:{Test0:30,Test1:25}}};
 const plan=planGo(input,profile);
 assert.equal(plan.length,24);
 const tasks=plan.flat();
 assert.deepEqual(tasks.filter(t=>t.pkg==='heavy').flatMap(t=>t.selected).sort(),input[0].names.sort());
 assert.equal(tasks.filter(t=>t.pkg==='small').length,1);
 assert.deepEqual(tasks.find(t=>t.pkg==='small').selected,['Example','FuzzSeed']);
 assert.equal(tasks.filter(t=>t.pkg==='empty').length,1);
 const stragglers=plan.map(tasks=>tasks.flatMap(t=>t.selected));
 assert.notEqual(stragglers.findIndex(s=>s.includes('Test0')),stragglers.findIndex(s=>s.includes('Test1')));
 assert.deepEqual(planGo([...input].reverse(),profile),plan);
 for(const tasks of plan)assert.equal(new Set(tasks.map(t=>t.pkg)).size,tasks.length,'never concurrent executions of same package');
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
 assert.equal(goLanes,6);
 assert.equal(goGroups,4);
 assert.equal(releaseJobCount(),37);
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
test('four-worker failure stops every dequeue, drains all active workers and preserves first error',async()=>{
 const first=new Error('first observed failure'),later=new Error('later failure');
 const starts=[],finished=[],release=[];let settled=false;
 const run=bounded([0,1,2,3,4,5],async n=>{
  starts.push(n);await new Promise(resolve=>{release[n]=resolve;});finished.push(n);
  if(n===1)throw first;if(n===0)throw later;
 },4);
 const outcome=run.then(()=>{settled=true;return null;},error=>{settled=true;return error;});
 await turn();const initial=[...starts];
 release[1]();await turn();release[0]();await turn();
 const early=settled;release[2]?.();await turn();const beforeLast=settled;
 release[3]?.();const error=await outcome;
 assert.deepEqual(initial,[0,1,2,3]);assert.deepEqual(starts,[0,1,2,3]);
 assert.equal(early,false);assert.equal(beforeLast,false);
 assert.deepEqual(finished.sort(),[0,1,2,3]);assert.equal(error,first);
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
