import test from 'node:test';
import assert from 'node:assert/strict';
import { checkRoute } from '../src/route-check.ts';
test('checkRoute: loopback ok with models; non-loopback refused without a request; key never in detail',async()=>{
  let calls=0, reads=0;
  const deps={readFile:async(_path:string)=>{reads++;return 'super-private-key\n';},fetch:async(url:string|URL|Request,init?:RequestInit)=>{calls++; assert.equal(new Headers(init?.headers).get('authorization'),'Bearer super-private-key');assert.equal(init?.redirect,'error'); assert.ok(String(url).endsWith('/v1/models'));return new Response(JSON.stringify({data:[{id:'sol'},{id:'terra'}]}));}};
  for(const host of ['127.0.0.1','127.5.6.7','[::1]','localhost']) assert.deepEqual(await checkRoute({base_url:`http://${host}:1234/v1/`,key_file:'/fake'},deps),{ok:true,models:['sol','terra']});
  for(const host of ['example.com','127.0.0.1.example.com','[::ffff:192.0.2.1]','192.168.1.2']) assert.deepEqual(await checkRoute({base_url:`http://${host}/v1`,key_file:'/fake'},deps),{ok:false,models:[],detail:'not_loopback'});
  assert.equal(calls,4);assert.equal(reads,4);
  const fail=await checkRoute({base_url:'http://localhost/v1',key_file:'/fake'},{...deps,fetch:async()=>{throw new Error('super-private-key');}});assert.equal(fail.ok,false);assert.ok(!JSON.stringify(fail).includes('super-private-key'));
  const redirect=await checkRoute({base_url:'http://localhost/v1'}, {readFile:deps.readFile,fetch:async()=>new Response(null,{status:302,headers:{location:'https://evil.test'}})});assert.equal(redirect.ok,false);
  for(const data of [{data:[]},{data:[{}]},{models:['sol']}]) assert.equal((await checkRoute({base_url:'http://localhost/v1'},{readFile:deps.readFile,fetch:async()=>new Response(JSON.stringify(data))})).ok,false);
});
