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
test('redactText masks every hub secret format: standard-Base64 whsec_ keys (+ / =), OAuth vat_/vrt_/vac_ and blob rt_ tokens',()=>{
  const b64=(s:string)=>s; // deterministic synthetic keys in each real alphabet
  const secrets=[
    'whsec_+'+'A'.repeat(42)+'=', 'whsec_/'+'B'.repeat(20)+'+/'+'C'.repeat(20)+'=', 'whsec_'+'D'.repeat(43)+'=',
    'vat_'+'E'.repeat(43), 'vrt_'+'F_-'.repeat(14)+'G', 'vac_'+'H'.repeat(43), 'rt_'+b64('MTc2Mjg2fHBlZXJfMDF8YWJjZGVm'+'I'.repeat(40)),
  ];
  for (const s of secrets) {
    for (const text of [s,`key=${s} next`,`"${s}"`]) {
      const r=redactText(text); assert.equal(r.changed,true,text);
      for (const part of [s,s.slice(6,30)]) assert.ok(!r.text.includes(part),`${text} -> ${r.text}`);
    }
  }
  // Ordinary identifiers are untouched.
  for (const plain of ['start_time','support_rt_id','private_vat_','the rt_ prefix']) assert.equal(redactText(plain).changed,false,plain);
  const inner=memoryLog(), log=redactingLog(inner);
  log.warn('leak '+secrets[0],{detail:secrets[3]}); assert.ok(!inner.text().includes(secrets[0]) && !inner.text().includes(secrets[3]));
});
