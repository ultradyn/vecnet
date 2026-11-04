import test from 'node:test';
import assert from 'node:assert/strict';
import { redactText, scrubEnv, redactingLog } from '../src/redact.ts';
import { memoryLog } from '../../hub-client/testing/fixtures.ts';
test('redactText masks bearer tokens, whsec_, crsr_ and vn_ secrets; scrubEnv drops VECNET_* and *_API_KEY unless allowlisted, keeps XDG_RUNTIME_DIR',()=>{
  const secrets=['Bearer abcDEF123','whsec_abc123','crsr_abc123','vn_peer_abcDEF-_123','vn_enrol_a123'];
  for(const s of secrets){const r=redactText(s); assert.equal(r.changed,true); assert.ok(!r.text.includes(s));}
  assert.deepEqual(redactText('hello world'),{text:'hello world',changed:false});
  assert.equal(redactText('https://example.com/path?token=private').text,'https://example.com/[redacted]');
  assert.deepEqual(scrubEnv({VECNET_AUTH:'bad',VECNET_URL:'bad',OPENAI_API_KEY:'bad',X_AUTH_TOKEN:'bad',X_BASE_URL:'bad',X_SECRET:'bad',X_TOKEN:'bad',X_PASSWORD:'bad',HOME:'/home/test',PATH:'/bin',XDG_RUNTIME_DIR:'/run/user/test',DBUS_SESSION_BUS_ADDRESS:'unix:path=/fake',ALLOWED_API_KEY:'keep',UNSET:undefined},['ALLOWED_API_KEY']),{HOME:'/home/test',PATH:'/bin',XDG_RUNTIME_DIR:'/run/user/test',DBUS_SESSION_BUS_ADDRESS:'unix:path=/fake',ALLOWED_API_KEY:'keep'});
});
test('redactingLog drops body-like meta keys',()=>{
  const inner=memoryLog(), log=redactingLog(inner);
  for(const level of ['info','warn','error'] as const) log[level]('Bearer private',{text:'body1',body:'body2',content:'body3',prompt:'body4',credential:'body5',secret:'body6',token:'body7',nested:{body:'nested-secret',detail:'whsec_123'},detail:'crsr_123'});
  assert.equal(inner.entries.length,3); const text=inner.text();
  for(const secret of ['private','body1','body2','body3','body4','body5','body6','body7','nested-secret','whsec_123','crsr_123']) assert.ok(!text.includes(secret));
  assert.deepEqual(inner.entries[0].meta,{nested:{detail:'[redacted]'},detail:'[redacted]'});
});
