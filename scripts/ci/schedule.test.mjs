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
