import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, readFileSync, readdirSync, realpathSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import type { PermissionDraft } from '../src/backend.ts';
import { symlinkSync } from './fixtures/platform.ts';
import { finishPermission, validateLaunchPermission, approvalOptions, hasShellOperators } from '../src/contract/permission.ts';
const ctx={machineId:'xsm',jobId:'job_00000000000000000000000000',permissionMode:'ask' as const,preset:'sol',backend:'t3',workspaceRoot:'/workspace',home:'/home/test'};
const draft=():PermissionDraft=>({kind:'edit',backend_method:'test',scope:'once',options:[{id:'yes',label:'Allow',grants:'once'},{id:'no',label:'Deny',grants:'deny'}],paths:[{path:'/workspace/file',access:'write'}],fidelity:{kind:'enforced',command:'none',paths:'enforced',host:'none'},backend_raw:{}});
test('validateLaunchPermission accepts every valid fixture and rejects every invalid one (unknown key, v 2, bad fidelity)',()=>{
  for(const kind of ['valid','invalid']) {
    const dir=new URL(`./fixtures/permission/${kind}/`,import.meta.url), files=readdirSync(dir); assert.ok(files.length>=3);
    for(const file of files) {const errors=validateLaunchPermission(JSON.parse(readFileSync(new URL(file,dir),'utf8'))); assert.equal(errors.length===0,kind==='valid',`${file}: ${errors}`);}
  }
});
test('finishPermission: in-workspace enforced edit → outside_workspace false; a command line → unknown; a path outside → true; symlink escape resolved by realpath → true',()=>{
  const temporary=mkdtempSync(join(tmpdir(),'vl-permission-')), root=realpathSync.native(temporary);mkdirSync(join(root,'ws'));mkdirSync(join(root,'outside'));writeFileSync(join(root,'outside','file'),'');symlinkSync(join(root,'outside'),join(root,'ws','escape'));
  try {
    const c={...ctx,workspaceRoot:join(root,'ws')};
    const p=finishPermission({...draft(),paths:[{path:'file',access:'write'}]},c);assert.equal(p.outside_workspace,false);assert.equal(p.paths![0].in_workspace,true);assert.equal(p.paths![0].path,join(root,'ws','file'));assert.deepEqual(validateLaunchPermission(p),[]);
    const alias=join(root,'ws-alias'), distinct=join(root,'ws-other');symlinkSync(join(root,'ws'),alias);mkdirSync(distinct);
    const aliased=finishPermission({...draft(),paths:[{path:join(alias,'file'),access:'write'}]},c);
    assert.equal(aliased.outside_workspace,false);assert.equal(aliased.paths![0].path,p.paths![0].path);
    assert.equal(finishPermission({...draft(),paths:[{path:join(distinct,'file'),access:'write'}]},c).outside_workspace,true);
    assert.equal(finishPermission({...draft(),command:{line:'cat file'},paths:[]},c).outside_workspace,'unknown');
    assert.equal(finishPermission({...draft(),paths:[{path:join(root,'outside','file'),access:'read'}]},c).outside_workspace,true);
    for(const path of ['escape/file','escape/new-file','escape/missing/child']) { const escaped=finishPermission({...draft(),paths:[{path,access:'write'}]},c);assert.equal(escaped.outside_workspace,true);assert.equal(escaped.paths![0].in_workspace,false);assert.ok(escaped.paths![0].path.startsWith(join(root,'outside'))); }
    assert.equal(finishPermission({...draft(),command:{cwd:join(root,'outside')},paths:[]},c).outside_workspace,true);
    assert.equal(finishPermission({...draft(),paths:[],fidelity:{kind:'reported',command:'none',paths:'none',host:'none'}},c).outside_workspace,'unknown');
    const redacted=finishPermission({...draft(),reason:'Bearer token123',command:{line:'echo crsr_123'},backend_raw:{token:'whsec_123'}},c);assert.equal(redacted.redacted,true);assert.ok(!JSON.stringify(redacted).includes('token123'));assert.ok(!JSON.stringify(redacted).includes('whsec_123'));
  } finally {rmSync(temporary,{recursive:true,force:true});}
});
test('finishPermission: backend_raw over 4 KiB is truncated with sha256; an object still over 6 KiB truncates command.line and paths and sets redacted',()=>{
  const raw={value:'α'.repeat(5000)}, p=finishPermission({...draft(),backend_raw:raw},ctx);
  assert.equal(p.backend_raw_truncated,true);assert.equal(p.backend_raw_sha256,createHash('sha256').update(JSON.stringify(raw)).digest('hex'));assert.ok(Buffer.byteLength(JSON.stringify(p.backend_raw))<=4096);
  const huge=finishPermission({...draft(),backend_raw:raw,command:{line:'λ'.repeat(10000)},paths:Array.from({length:100},(_,i)=>({path:`/workspace/${i}`,access:'write' as const}))},ctx);
  assert.ok(Buffer.byteLength(huge.command!.line!)<=1024);assert.ok(huge.paths!.length<=32);assert.equal(huge.redacted,true);assert.ok(Buffer.byteLength(JSON.stringify(huge))<=6144);assert.deepEqual(validateLaunchPermission(huge),[]);
});
test('approvalOptions caps at 7 + escalate, keeps deny, trims and dedupes labels',()=>{
  const opts=Array.from({length:10},(_,i)=>({id:String(i),label:' '+ 'x'.repeat(100)+' ',grants:'once' as const}));
  const selected=approvalOptions([...opts,{id:'no',label:'Deny',grants:'deny'}],true);
  assert.equal(selected.length,8);assert.ok(selected.some(x=>x.optionId==='no'));assert.equal(selected.at(-1)!.label,'escalate');assert.equal(new Set(selected.map(x=>x.label)).size,8);assert.ok(selected.every(x=>x.label.length<=64));assert.ok(selected.some(x=>x.label.endsWith('(2)')));
  const collisions=approvalOptions([{id:'a',label:'escalate',grants:'once'},{id:'b',label:'escalate',grants:'deny'}],true);assert.equal(new Set(collisions.map(x=>x.label)).size,3);
});
test('finishPermission refuses backend option IDs that redaction would rewrite',()=>{
  const options:PermissionDraft['options']=[
    {id:'https://a.local/allow',label:'Bearer a',grants:'once'},
    {id:'https://a.local/deny',label:'Bearer b',grants:'deny'},
  ];
  assert.throws(()=>finishPermission({...draft(),options},ctx),{message:'unsafe backend option id'});
  assert.throws(()=>finishPermission({...draft(),options:[{id:'crsr_optionSecret',label:'Allow',grants:'once'},draft().options[1]]},ctx),{message:'unsafe backend option id'});
});
test('finishPermission deduplicates final redacted labels and preserves exact option mapping',()=>{
  const options:PermissionDraft['options']=[
    {id:'allow:once',label:'Bearer a',grants:'once'},
    {id:'deny:once',label:'Bearer b',grants:'deny'},
  ];
  const permission=finishPermission({...draft(),options,reason:'Bearer reasonSecret',backend_raw:{token:'crsr_rawSecret'}},ctx);
  assert.deepEqual(permission.options,[
    {id:'allow:once',label:'Bearer [redacted]',grants:'once'},
    {id:'deny:once',label:'Bearer [redacted](2)',grants:'deny'},
  ]);
  assert.equal(new Set(permission.options.map(o=>o.label)).size,2);
  assert.equal(new Set(permission.options.map(o=>o.id)).size,2);
  assert.equal(permission.redacted,true);
  assert.equal(permission.reason,'Bearer [redacted]');
  assert.deepEqual(permission.backend_raw,{token:'[redacted]'});
  assert.deepEqual(validateLaunchPermission(permission),[]);
  assert.deepEqual(options.map(o=>o.label),['Bearer a','Bearer b']);
  assert.throws(()=>finishPermission({...draft(),options:options.map(o=>({...o,id:'same'}))},ctx),{message:'invalid backend option ids'});
});
test('hasShellOperators: ; | & && || $( ` > < detected; plain argv-like line not',()=>{
  for(const op of [';','|','&','&&','||','$(','`','>','<']) assert.equal(hasShellOperators(`echo a ${op} b`),true,op);
  assert.equal(hasShellOperators('git status --porcelain'),false);
});
test('permission redaction covers option-only secrets and raw keys; dangling symlinks cannot assert confinement',()=>{
  const opts=finishPermission({...draft(),options:[{id:'yes',label:'crsr_optionSecret',grants:'once'},{id:'no',label:'Deny',grants:'deny'}]},ctx);
  assert.equal(opts.redacted,true);assert.ok(!JSON.stringify(opts).includes('crsr_optionSecret'));
  const raw=finishPermission({...draft(),backend_raw:{'vn_peer_keySecret':'value'}},ctx);assert.equal(raw.redacted,true);assert.ok(!JSON.stringify(raw).includes('vn_peer_keySecret'));
  const root=mkdtempSync(join(tmpdir(),'vl-dangling-'));mkdirSync(join(root,'ws'));symlinkSync(join(root,'missing'),join(root,'ws','escape'));
  try { assert.equal(finishPermission({...draft(),paths:[{path:'escape/file',access:'write'}]},{...ctx,workspaceRoot:join(root,'ws')}).outside_workspace,true); }
  finally {rmSync(root,{recursive:true,force:true});}
});
test('symlink traversal before .. is resolved by filesystem semantics', { skip: process.platform === 'win32' && 'Windows normalizes junction/.. before lookup' }, ()=>{
  const root=mkdtempSync(join(tmpdir(),'vl-dotdot-'));mkdirSync(join(root,'ws'));mkdirSync(join(root,'outside'));mkdirSync(join(root,'outside','nested'));writeFileSync(join(root,'outside','file'),'');symlinkSync(join(root,'outside','nested'),join(root,'ws','escape'));
  try {
    for(const name of ['file','new-file']) {
      const permission=finishPermission({...draft(),paths:[{path:`escape/../${name}`,access:'write'}]},{...ctx,workspaceRoot:join(root,'ws')});
      assert.equal(permission.outside_workspace,true);assert.equal(permission.paths![0].path,join(root,'outside',name));
    }
  } finally {rmSync(root,{recursive:true,force:true});}
});
