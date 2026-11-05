import test from 'node:test';
import assert from 'node:assert/strict';
import { parseLaunchRequest, validateLaunchInput } from '../src/contract/launch-input.ts';
import { launchDelivery } from './fixtures.ts';
const input={backend:'t3',project:'vecnet4',preset:'sol',permission_mode:'auto'};
test('parseLaunchRequest: required fields, unknown key → invalid_input, backend any → invalid_input, continue_job → unsupported, job id from vecnet.job_id, expiresAt from loop',()=>{
  const d=launchDelivery(input), parsed=parseLaunchRequest(d); assert.equal(parsed.ok,true);
  if(!parsed.ok) return;
  assert.deepEqual(parsed.req,{jobId:d.message.metadata['vecnet.job_id'],threadId:d.message.thread_id,messageId:d.message.message_id,requester:d.message.sender,instructions:d.message.job_request!.instructions,input,expiresAt:d.message.loop.expires_at});
  for(const key of Object.keys(input)) { const x:Record<string,unknown>={...input}; delete x[key]; assert.equal(parseLaunchRequest(launchDelivery(x)).ok,false,key); }
  for(const x of [{...input,unknown:true},{...input,idle:{unknown:true}},{...input,deliver:{unknown:true}},{...input,title:'x'.repeat(201)},{...input,permission_mode:'root'},{...input,relay_from:['name']},{...input,idle:{wrapup_after:'nope'}},{...input,project:'/etc'}]) assert.equal(parseLaunchRequest(launchDelivery(x)).ok,false);
  assert.deepEqual(parseLaunchRequest(launchDelivery({...input,backend:'any'})),{ok:false,error:'invalid_input',reason:'backend any is not supported yet; name a backend'});
  const continued=parseLaunchRequest(launchDelivery({...input,continue_job:'job_old'})); assert.equal(continued.ok,false); if(!continued.ok) assert.equal(continued.error,'unsupported');
  const other=launchDelivery(input); other.message.job_request!.operation='other'; const r=parseLaunchRequest(other); assert.equal(r.ok,false); if(!r.ok) assert.equal(r.error,'unsupported');
  const missing=launchDelivery(input); delete missing.message.metadata['vecnet.job_id']; assert.equal(parseLaunchRequest(missing).ok,false);
  const valid={...input,idle:{wrapup_after:'45m',wrapup_turn:true},deliver:{push_branch:true},reply_priority:'hold'};
  assert.deepEqual(validateLaunchInput(valid),[]); assert.equal(parseLaunchRequest(launchDelivery(valid)).ok,true);
});
test('an invalid_input reason names only schema-owned fields: an unknown, credential-shaped key is never echoed',()=>{
  const secret='vn_peer_'+'A'.repeat(43);
  for (const [x,reason] of [[{...input,[secret]:true},'/*: unknown key'],[{...input,idle:{[secret]:1}},'/idle/*: unknown key'],[{...input,relay_from:[secret]},'/relay_from/0: pattern']] as const) {
    const r=parseLaunchRequest(launchDelivery(x));
    assert.equal(r.ok,false); if(r.ok) return;
    assert.equal(r.reason,reason); assert.ok(!JSON.stringify(r).includes(secret));
  }
});
