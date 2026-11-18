// For a measured indivisible test file only. Wrap actual node:test registration,
// not a source parser/allowlist: loops, parameterized and future roots partition
// by registration ordinal. Nested t.test and all hooks stay with their parent.
import {registerHooks} from 'node:module';
const shard=Number(process.env.CI_NODE_SHARD),count=Number(process.env.CI_NODE_SHARDS);
if(!Number.isInteger(shard)||!Number.isInteger(count)||count<1||shard<0||shard>=count)throw new Error('invalid Node shard');
const source=`
import real, * as api from 'ci:real-node-test';
import {AsyncLocalStorage} from 'node:async_hooks';
const callbackContext=new AsyncLocalStorage();
let ordinal=0,selected=0;
const wrap=fn=>(...args)=>{
 // Imported registration inside a selected callback is not a new root. Keep
 // t.test native, but reject this unsupported shape even after an await.
 if(callbackContext.getStore()){
  process.exitCode=1;
  const message='sharded file has unsupported imported nested registration; use context.test or review scheduling';
  // Node can suppress a global hook error when the shard has no selected roots.
  console.error(message);
  throw new Error(message);
 }
 const index=ordinal++;
 if(index%${count}===${shard}){
  selected++;
  const callback=args.at(-1);
  if(typeof callback==='function')args[args.length-1]=function(...values){
   return callbackContext.run(true,()=>Reflect.apply(callback,this,values));
  };
  return fn(...args);
 }
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
// Hooks do not take shard ordinals. Their callback is first, before optional
// options; enter the same context only when Node invokes that callback.
const hook=fn=>function(...args){
 const callback=args[0];
 if(typeof callback==='function')args[0]=function(...values){
  return callbackContext.run(true,()=>Reflect.apply(callback,this,values));
 };
 return Reflect.apply(fn,this,args);
};
export const before=hook(api.before),after=hook(api.after),beforeEach=hook(api.beforeEach),afterEach=hook(api.afterEach);
export const {mock,run,snapshot}=api;
process.on('exit',()=>{console.log('ci-node-shard registered='+ordinal+' selected='+selected+' shard=${shard}/${count}');if(!ordinal)process.exitCode=1;});
`;
// The --import preload also runs in the test runner parent. Only wrap
// registration inside an isolated test-file worker, never the runner itself.
if(process.env.NODE_TEST_CONTEXT==='child-v8')registerHooks({resolve(specifier,context,next){
 if(specifier==='ci:real-node-test')return {url:'node:test',shortCircuit:true};
 if(specifier==='node:test')return {url:'data:text/javascript,'+encodeURIComponent(source),shortCircuit:true};
 return next(specifier,context);
}});
