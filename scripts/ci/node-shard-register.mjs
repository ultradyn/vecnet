// For a measured indivisible test file only. Wrap actual node:test registration,
// not a source parser/allowlist: loops, parameterized and future roots partition
// by registration ordinal. Nested t.test and all hooks stay with their parent.
import {registerHooks} from 'node:module';
const shard=Number(process.env.CI_NODE_SHARD),count=Number(process.env.CI_NODE_SHARDS);
if(!Number.isInteger(shard)||!Number.isInteger(count)||count<1||shard<0||shard>=count)throw new Error('invalid Node shard');
const source=`
import real, * as api from 'ci:real-node-test';
let ordinal=0,selected=0;
const wrap=fn=>(...args)=>{
 const index=ordinal++;
 if(index%${count}===${shard}){selected++;return fn(...args);}
 return Promise.resolve();
};
const decorate=fn=>{
 const result=wrap(fn);
 for(const key of ['skip','todo','only'])result[key]=wrap(fn[key]);
 return result;
};
const test=decorate(real);
// This measured file registers flat tests. Refuse a future suite registration
// rather than silently partitioning its children with a different ordinal set.
const describe=()=>{throw new Error('sharded file added a suite; review scheduling before execution');};
const it=decorate(api.it);
export default test;export {test,it,describe};
export const {before,after,beforeEach,afterEach,mock,run,snapshot}=api;
process.on('exit',()=>{console.log('ci-node-shard registered='+ordinal+' selected='+selected+' shard=${shard}/${count}');if(!ordinal)process.exitCode=1;});
`;
// The --import preload also runs in the test runner parent. Only wrap
// registration inside an isolated test-file worker, never the runner itself.
if(process.env.NODE_TEST_CONTEXT==='child-v8')registerHooks({resolve(specifier,context,next){
 if(specifier==='ci:real-node-test')return {url:'node:test',shortCircuit:true};
 if(specifier==='node:test')return {url:'data:text/javascript,'+encodeURIComponent(source),shortCircuit:true};
 return next(specifier,context);
}});
