import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, chmod, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { parseConfig, loadConfig, ConfigError, parseDuration } from '../src/config.ts';
const minimal = () => ({host:'xsm',hub:{url:'http://localhost:8080',credential_file:'/fake/credential'},state_dir:'/fake/state',backends:[{id:'t3',module:'./backends/t3/index.ts',config:{}}],projects:{vecnet4:{repo_root:'/fake/repo',base_ref:'master'}}});
test('parseConfig applies [D] §6 and D8/D10 defaults, puts per-requester caps only in policies.default, and reports a bad field by JSON pointer', async () => {
  const c=parseConfig(minimal());
  assert.deepEqual(c.limits,{max_sessions:6,max_open_sessions:24,max_idle_wrapup_ms:28800000,max_session_lifetime_ms:259200000,workspace_retention_ms:604800000});
  assert.deepEqual(c.policies.default,{backends:['t3','paseo'],permission_modes:['read-only','ask','auto','full-access'],projects:[],max_sessions:3,max_open_sessions:12,launches_per_hour:30});
  assert.deepEqual(c.approvals,{route:'requester',parent_expires_ms:900000,on_parent_expiry:'deny',operator_expires_ms:3600000,to_operator:{presets:['none'],categories:[],requesters:[]}});
  assert.deepEqual(c.questions,{blocking_timeout_ms:1800000});
  assert.deepEqual(c.idle,{park_after_ms:1800000,settle_opaque_ms:7200000,settle_observable_ms:86400000,check_every_ms:60000,deadline_margin_ms:300000});
  assert.deepEqual(c.presence,{heartbeat_ms:300000}); assert.equal(c.wake_interrupts,false); assert.deepEqual(c.relays,{});
  assert.throws(()=>parseConfig({...minimal(),limits:{max_sessions:'oops'}}),(e:unknown)=>e instanceof ConfigError && e.pointer==='/limits/max_sessions');
  assert.throws(()=>parseConfig({...minimal(),limits:{max_sessions_per_requester:7}}),ConfigError);
  assert.throws(()=>parseConfig({...minimal(),hub:{...minimal().hub,typo:true}}),ConfigError);
  assert.equal(parseDuration('35m'),2100000); assert.equal(parseDuration('7d'),604800000);
  for(const s of ['-2h','NaN','2','1h junk','1e99h']) assert.throws(()=>parseDuration(s));
  const example=JSON.parse(await readFile(new URL('../config.example.json',import.meta.url),'utf8'));
  assert.deepEqual(parseConfig(example).policies.default.projects,['vecnet4']);
  assert.equal(parseConfig({...minimal(),idle:{park_after:'35m'},presence:{heartbeat_ms:120000}}).idle.park_after_ms,2100000);
});
test('credential file looser than 0600 is refused', async () => {
  const dir=await mkdtemp(join(tmpdir(),'vl-config-'));
  try {
    const credential=join(dir,'credential'), config=join(dir,'config.json');
    await writeFile(credential,'vn_peer_do-not-print',{mode:0o600});
    await writeFile(config,JSON.stringify({...minimal(),hub:{url:'http://localhost',credential_file:credential}}));
    await loadConfig(config); await chmod(credential,0o644);
    await assert.rejects(loadConfig(config),(e:unknown)=>e instanceof ConfigError && e.pointer==='/hub/credential_file' && !e.message.includes('vn_peer'));
    await chmod(credential,0o400); await assert.rejects(loadConfig(config),ConfigError);
  } finally { await rm(dir,{recursive:true,force:true}); }
});
test('to_operator presets: none alone ok; none plus privileged is a ConfigError',()=>{
  assert.deepEqual(parseConfig({...minimal(),approvals:{to_operator:{presets:['none']}}}).approvals.to_operator.presets,['none']);
  assert.throws(()=>parseConfig({...minimal(),approvals:{to_operator:{presets:['none','privileged']}}}),ConfigError);
  assert.deepEqual(parseConfig({...minimal(),approvals:{to_operator:{presets:['outside-workspace','privileged']}}}).approvals.to_operator.presets,['outside-workspace','privileged']);
});
test('explicit null config values are rejected rather than selecting a permissive default',()=>{
  for(const field of ['limits','approvals','policies','relays','idle','presence','questions','wake_interrupts']) assert.throws(()=>parseConfig({...minimal(),[field]:null}),ConfigError,field);
  for(const value of [{limits:{max_sessions:null}},{approvals:{to_operator:{presets:null}}},{idle:{park_after_ms:null}}]) assert.throws(()=>parseConfig({...minimal(),...value}),ConfigError);
});
for (const location of ['policy key', 'unknown field', 'nested unknown field'] as const) {
  test(`ConfigError redacts a credential in ${location} while preserving its JSON pointer`,()=>{
    const secret = 'vn_peer_' + 'A'.repeat(43);
    const pointer = location === 'policy key' ? `/policies/${secret}` : location === 'unknown field' ? `/${secret}` : `/hub/${secret}`;
    const raw = location === 'policy key' ? {...minimal(),policies:{[secret]:{}}} :
      location === 'unknown field' ? {...minimal(),[secret]:true} : {...minimal(),hub:{...minimal().hub,[secret]:true}};
    assert.throws(()=>parseConfig(raw),(e:unknown)=>{
      assert.ok(e instanceof ConfigError);
      assert.equal(e.pointer,pointer);
      assert.equal(e.message.includes(secret),false,'message must not expose the credential');
      assert.ok(e.message.includes('[redacted]'));
      return true;
    });
  });
}
test('ConfigError keeps ordinary bad-field pointer diagnostics intact',()=>{
  for (const [raw,pointer,reason] of [
    [{...minimal(),limits:{max_sessions:'oops'}},'/limits/max_sessions','expected nonnegative integer'],
    [{...minimal(),hub:{...minimal().hub,typo:true}},'/hub/typo','unknown field'],
    [{...minimal(),policies:{typo:{}}},'/policies/typo','expected default, peer id or principal id'],
  ] as const) {
    assert.throws(()=>parseConfig(raw),(e:unknown)=>{
      assert.ok(e instanceof ConfigError);
      assert.equal(e.pointer,pointer);
      assert.equal(e.message,`${pointer}: ${reason}`);
      return true;
    });
  }
  assert.equal(new ConfigError('').message,'/: invalid field');
  assert.equal(new ConfigError('').pointer,'/');
});


test('scratch is explicit boolean metadata and legacy project aliases retain repository semantics', () => {
  const raw = {...minimal(), projects:{temporary:{repo_root:'/fake/repo',base_ref:'master'},scratch:{repo_root:'/fake/cache',base_ref:'HEAD',scratch:true}}};
  const parsed = parseConfig(raw);
  assert.equal(parsed.projects.temporary.scratch, undefined);
  assert.equal(parsed.projects.scratch.scratch, true);
  assert.equal(parseConfig({...raw,projects:{temporary:{...raw.projects.temporary,scratch:false}}}).projects.temporary.scratch,false);
  for (const scratch of [null,'true',0]) assert.throws(()=>parseConfig({...raw,projects:{scratch:{...raw.projects.scratch,scratch}}}),ConfigError);
});
