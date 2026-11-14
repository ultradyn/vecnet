import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { parseQuestion, parseAnswers, parseControl, parseTurn, parseDecision, parseDecidedVia, parseApprovalClosed, parseEscalatedFrom, parseFinalText, buildQuestion, buildAnswers, buildControl, buildTurn, buildDecision, buildDecidedVia, buildApprovalClosed, buildEscalatedFrom, buildFinalText } from '../src/contract/metadata.ts';
import { validateLaunchInput } from '../src/contract/launch-input.ts';
import { validateLaunchPermission } from '../src/contract/permission.ts';
const question={v:1 as const,question_id:'q1',blocking:true,questions:[{id:'a',header:'Pick',text:'Choose',options:['one','two'],allow_other:false}]};
const approval='apr_00000000000000000000000000', job='job_00000000000000000000000000', message='msg_00000000000000000000000000';
test('metadata parsers reject malformed launch.question, launch.answers, launch.control',()=>{
  assert.deepEqual(parseQuestion(buildQuestion(question)),question);
  for(const q of [{...question,v:2},{...question,extra:true},{...question,blocking:'true'},{...question,questions:[]},{...question,questions:[{...question.questions[0],extra:1}]}]) assert.equal(parseQuestion({'launch.question':q}),undefined);
  const answers={question_id:'q1',answers:{a:'one'}};assert.deepEqual(parseAnswers(buildAnswers(answers)),answers);
  for(const a of [{...answers,extra:1},{question_id:'q1',answers:{a:1}},{answers:{}}]) assert.equal(parseAnswers({'launch.answers':a}),undefined);
  for(const c of ['interrupt','settle','extend'] as const) assert.equal(parseControl(buildControl(c)),c);
  for(const c of ['stop',false,{},undefined]) assert.equal(parseControl({'launch.control':c}),undefined);
  const turn=buildTurn({outcome:'completed',turnId:'t1',jobId:job,consumed:Array(103).fill(message)});assert.equal((turn['launch.consumed'] as string[]).length,100);assert.equal(turn['launch.consumed_truncated'],3);assert.deepEqual(parseTurn(turn),{outcome:'completed',turnId:'t1',jobId:job,consumed:Array(100).fill(message),consumedTruncated:3});
  assert.equal(parseTurn({...turn,'launch.turn':'weird'}),undefined);assert.equal(parseTurn({...turn,'launch.consumed':['bad']}),undefined);
  assert.equal(parseDecision(buildDecision('Allow')),'Allow');assert.equal(parseDecision({'launch.decision':1}),undefined);
  assert.equal(parseDecidedVia(buildDecidedVia('pag_card')),'pag_card');assert.equal(parseDecidedVia({'launch.decided_via':{}}),undefined);
  const closed={approval_id:approval,superseded_by:'operator' as const,decision:'Deny'};assert.deepEqual(parseApprovalClosed(buildApprovalClosed(closed)),closed);assert.equal(parseApprovalClosed({'launch.approval_closed':{...closed,extra:1}}),undefined);
  assert.equal(parseEscalatedFrom(buildEscalatedFrom(approval)),approval);assert.equal(parseEscalatedFrom({'launch.escalated_from':'name'}),undefined);
  assert.equal(parseFinalText(buildFinalText()),'best_effort');assert.equal(parseFinalText({'launch.final_text':'anything'}),undefined);
});
test('schemas and validators agree on properties and required',()=>{
  const permission=JSON.parse(readFileSync(new URL('./fixtures/permission/valid/edit.json',import.meta.url),'utf8'));
  const cases:Array<[string,Record<string,unknown>,(x:unknown)=>boolean,string[],string[]]>=[
    ['launch-input',{backend:'t3',project:'vecnet4',preset:'sol',permission_mode:'ask',workspace_mode:'worktree',base_ref:'master',title:'title',idle:{wrapup_after:'2h',wrapup_turn:true},relay_from:[],reply_priority:'wake_idle',deliver:{push_branch:false}},x=>validateLaunchInput(x).length===0,['backend','project','preset','permission_mode','workspace_mode','base_ref','title','idle','relay_from','reply_priority','deliver'],['backend','project','preset','permission_mode']],
    ['launch-permission',{...permission,tool:'edit',command:{line:'echo hi',shell_operators:false},host:{name:'localhost'},reason:'test',backend_raw_sha256:'a'.repeat(64)},x=>validateLaunchPermission(x).length===0,['v','kind','machine_id','job_id','permission_mode','preset','backend','backend_method','tool','command','paths','host','scope','options','workspace_root','outside_workspace','reason','backend_raw','backend_raw_truncated','backend_raw_sha256','redacted','fidelity'],['v','kind','machine_id','job_id','permission_mode','preset','backend','backend_method','scope','options','workspace_root','outside_workspace','backend_raw','backend_raw_truncated','redacted','fidelity']],
    ['launch-question',question,x=>parseQuestion({'launch.question':x})!==undefined,['v','question_id','blocking','questions'],['v','question_id','blocking','questions']],
    ['launch-answers',{question_id:'q1',answers:{a:'one'}},x=>parseAnswers({'launch.answers':x})!==undefined,['question_id','answers'],['question_id','answers']],
    ['launch-approval-closed',{approval_id:approval,superseded_by:'operator',decision:'Deny'},x=>parseApprovalClosed({'launch.approval_closed':x})!==undefined,['approval_id','superseded_by','decision'],['approval_id','superseded_by','decision']],
  ];
  for(const [name,full,validate,keys,required] of cases){
    const schema=JSON.parse(readFileSync(new URL(`../schema/${name}.v1.schema.json`,import.meta.url),'utf8'));
    assert.equal(schema.$schema,'https://json-schema.org/draft/2020-12/schema');assert.equal(schema.additionalProperties,false);assert.deepEqual(Object.keys(schema.properties).sort(),keys.toSorted());assert.deepEqual(schema.required.toSorted(),required.toSorted());assert.equal(validate(full),true,name);
    assert.equal(validate({...full,unknown:true}),false,name);
    for(const key of keys){const partial={...full};delete partial[key];assert.equal(validate(partial),!required.includes(key),`${name}/${key}`);}
  }
});
