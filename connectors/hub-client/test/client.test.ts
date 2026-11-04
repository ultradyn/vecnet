import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createHubClient } from '../src/client.ts';
import { HubError } from '../src/errors.ts';
import { startFakeHub } from '../testing/fake-hub.ts';
import { setImmediate } from 'node:timers/promises';
import { directoryEntry, fakeClock } from '../testing/fixtures.ts';
import type { SendRequest, SendResponse } from '../src/wire.ts';
const request: SendRequest = {idempotency_key:'test.1',priority:'wake_idle',content:{text:'hello'},recipients:['peer_test']};
const response: SendResponse = {message_id:'msg_1',thread_id:'thr_1',deliveries:[],spilled:false};
const json = (body: unknown, status = 200, headers = {}) => new Response(JSON.stringify(body), {status,headers:{'Content-Type':'application/json',...headers}});
function fakeClient(fn: typeof fetch, timeoutMs?: number) { return createHubClient({baseUrl:'http://hub.invalid',credential:'secret-credential',fetch:fn,timeoutMs}); }

test('send posts the request and returns the 202 body', async t => {
  const hub = await startFakeHub(); t.after(() => hub.close());
  const result = await createHubClient({baseUrl:hub.url,credential:hub.bearer}).send(request);
  assert.match(result.message_id,/^msg_/); assert.match(result.thread_id,/^thr_/); assert.equal(result.deliveries.length,1);
  assert.deepEqual(hub.requests[0]?.body,request); assert.equal(hub.requests[0]?.method,'POST'); assert.equal(hub.requests[0]?.path,'/v1/messages');
});
test('idempotent replay 200 returns the same shape', async () => assert.deepEqual(await fakeClient(async () => json(response)).send(request),response));
test('error envelope becomes HubError with code and details', async () => {
  await assert.rejects(fakeClient(async () => json({error:{code:'max_depth_exceeded',message:'private',details:{depth:5}}},422)).send(request), e => e instanceof HubError && e.status === 422 && e.code === 'max_depth_exceeded' && !e.retryable && e.details.depth === 5);
});
test('429 Retry-After in seconds and as HTTP-date both set retryAfterMs', async () => {
  const date = new Date(Date.now()+60_000).toUTCString();
  for (const [header, min, max] of [['3',3000,3000],[date,58_000,60_000]] as const) {
    await assert.rejects(fakeClient(async () => json({error:{code:'rate_limited'}},429,{'Retry-After':header})).send(request), e => e instanceof HubError && e.retryable && (e.retryAfterMs ?? -1) >= min && (e.retryAfterMs ?? Infinity) <= max);
  }
});
test('network failure is HubError status 0, retryable', async () => {
  await assert.rejects(fakeClient(async () => {throw new Error('network');}).send(request), e => e instanceof HubError && e.status === 0 && e.retryable);
});
test('non-JSON 502 is bad_response, retryable', async () => {
  await assert.rejects(fakeClient(async () => new Response('upstream private text',{status:502})).send(request), e => e instanceof HubError && e.code === 'bad_response' && e.status === 502 && e.retryable);
});
test('3xx is refused and the bearer is not resent', async t => {
  const hub = await startFakeHub(), target = await startFakeHub(); t.after(() => hub.close()); t.after(() => target.close());
  hub.failNext('/v1/messages',302,{}, {Location:target.url+'/v1/messages'});
  await assert.rejects(createHubClient({baseUrl:hub.url,credential:hub.bearer}).send(request),HubError);
  assert.equal(target.requests.length,0);
});
test('the credential appears in no error message', async () => {
  for (const fn of [async () => {throw new Error('secret-credential');}, async () => json({error:{code:'secret-credential',message:'secret-credential'}},400)]) {
    await assert.rejects(fakeClient(fn).send(request), e => e instanceof HubError && !String(e).includes('secret-credential') && !e.stack?.includes('secret-credential'));
  }
});
test('putBlob sends raw bytes with the media type and returns sha256 and size', async t => {
  const hub = await startFakeHub(); t.after(() => hub.close()); const b = new Uint8Array([0,255,1,2]);
  const result = await createHubClient({baseUrl:hub.url,credential:hub.bearer}).putBlob(b,'application/test');
  assert.deepEqual(result,{sha256:createHash('sha256').update(b).digest('hex'),size:4});
  assert.equal(hub.requests[0]?.headers['content-type'],'application/test'); assert.deepEqual(hub.requests[0]?.body,Buffer.from(b));
});
test('peers and groups unwrap their envelopes', async t => {
  const hub = await startFakeHub(); t.after(() => hub.close()); const peers = [directoryEntry()], groups = [{group_id:'grp_1',members:['peer_1'],created_by:'peer_1',archived:false}];
  hub.setPeers(peers); hub.setGroups(groups); const c = createHubClient({baseUrl:hub.url,credential:hub.bearer});
  assert.deepEqual(await c.peers(),peers); assert.deepEqual(await c.groups(),groups);
});
test('fetchBlob refuses another origin, sends no bearer, and caps size', async t => {
  const hub = await startFakeHub(); t.after(() => hub.close()); hub.putBlobAt('/v1/blobs/test',new Uint8Array([1,2,3]));
  const c = createHubClient({baseUrl:hub.url,credential:hub.bearer});
  await assert.rejects(c.fetchBlob('http://foreign.invalid/blob',{maxBytes:3}), e => e instanceof HubError && e.code === 'foreign_origin');
  assert.equal(hub.requests.length,0);
  assert.deepEqual(await c.fetchBlob(hub.url+'/v1/blobs/test?t=token',{maxBytes:3}),new Uint8Array([1,2,3]));
  assert.equal(hub.requests[0]?.headers.authorization,undefined);
  for (const declared of [true,false]) {
    let cancelled = false;
    const capped = fakeClient(async (_url, init) => {
      assert.equal(new Headers(init?.headers).has('authorization'),false); assert.equal(init?.redirect,'error');
      return new Response(new ReadableStream({start(ctrl) {ctrl.enqueue(new Uint8Array([1,2]));ctrl.enqueue(new Uint8Array([3,4]));},cancel() {cancelled=true;}}),{headers:declared?{'Content-Length':'4'}:{}});
    });
    await assert.rejects(capped.fetchBlob('http://hub.invalid/blob?t=token',{maxBytes:3}),e => e instanceof HubError && e.code === 'too_large');
    assert.equal(cancelled,true);
  }
  const target = await startFakeHub(); t.after(() => target.close());
  hub.failNext('/v1/blobs/test',302,{}, {Location:target.url+'/v1/blobs/test?t=token'});
  await assert.rejects(c.fetchBlob(hub.url+'/v1/blobs/test?t=token',{maxBytes:3}),HubError); assert.equal(target.requests.length,0);
});
test('peers keeps created_by', async t => {
  const hub = await startFakeHub(); t.after(() => hub.close()); hub.setPeers([directoryEntry({created_by:'prn_owner'})]);
  assert.equal((await createHubClient({baseUrl:hub.url,credential:hub.bearer}).peers())[0]?.created_by,'prn_owner');
});
test('ack, receipt and presence post authenticated JSON and tolerate 204', async () => {
  const seen: string[] = [];
  const c = fakeClient(async (url, init) => {assert.equal(init?.method,'POST');assert.equal(new Headers(init?.headers).get('authorization'),'Bearer secret-credential');assert.equal(init?.redirect,'error'); seen.push(new URL(String(url)).pathname);assert.ok(JSON.parse(String(init?.body)));return new Response(null,{status:204});});
  await c.ack('e1.1'); await c.receipt({delivery_id:'dly_1',state:'processed'}); await c.presence({state:'idle',at:new Date(0).toISOString()});
  assert.deepEqual(seen,['/v1/ack','/v1/receipts','/v1/presence']);
});
test('request deadlines default to 30 s, are configurable, and uploads get 300 s', async t => {
  const scheduled: number[] = [];
  const realSetTimeout = globalThis.setTimeout;
  t.mock.method(globalThis,'setTimeout',(fn: () => void, ms: number) => {scheduled.push(ms);return realSetTimeout(fn,ms);});
  const c = fakeClient(async () => json({peers:[],sha256:'abc',size:0}));
  await c.peers();await c.putBlob(new Uint8Array(),'text/plain');
  await fakeClient(async () => json({peers:[]}),1234).peers();
  assert.deepEqual(scheduled,[30_000,300_000,1234]);
});
test('caller abort cancels an HTTP request without exposing its reason', async () => {
  const abort = new AbortController();
  const c = fakeClient(async (_url,init) => new Promise((_resolve,reject) => {
    init?.signal?.addEventListener('abort',()=>reject(new Error('secret-credential')),{once:true});
    abort.abort('secret-credential');
  }));
  await assert.rejects(c.send(request,abort.signal), e => e instanceof HubError && e.status===0 && !String(e).includes('secret-credential'));
});

test('malformed header credentials only escape as safe HubError', async () => {
  const sentinel = 'private-header-sentinel';
  const c = createHubClient({baseUrl:'http://hub.invalid',credential:sentinel+'\r\nsecret',fetch:async()=>assert.fail('invalid headers must not fetch')});
  await assert.rejects(c.ack('e1.1'), e => e instanceof HubError && e.status===0 && e.code==='network_error' && !String(e).includes(sentinel) && !e.stack?.includes(sentinel));
});
test('fetchBlob caps non-2xx bodies before parsing, declared and streamed', async () => {
  for (const declared of [true,false]) {
    let cancelled=false;
    const bytes=new TextEncoder().encode(JSON.stringify({error:{code:'oversize',details:{x:'x'.repeat(100)}}}));
    const c=fakeClient(async()=>new Response(new ReadableStream<Uint8Array>({
      start(ctrl){ctrl.enqueue(bytes);void setImmediate().then(()=>{if(!cancelled)ctrl.close();});}, cancel(){cancelled=true;},
    }),{status:500,headers:declared?{'Content-Length':String(bytes.length)}:{}}));
    await assert.rejects(c.fetchBlob('/blob',{maxBytes:1}),e=>e instanceof HubError && e.code==='too_large');
    assert.equal(cancelled,true);
  }
});
test('fetchBlob still parses an error envelope within the byte cap', async () => {
  const c=fakeClient(async()=>json({error:{code:'not_found',details:{id:'blob_1'}}},404));
  await assert.rejects(c.fetchBlob('/blob',{maxBytes:256}),e=>e instanceof HubError && e.status===404 && e.code==='not_found' && e.details.id==='blob_1');
});
test('a successful response body reset is a safe retryable network error', async () => {
  const c=fakeClient(async()=>new Response(new ReadableStream({start(ctrl){ctrl.error(new Error('secret-credential reset'));}}),{status:202}));
  await assert.rejects(c.send(request),e=>e instanceof HubError && e.status===0 && e.code==='network_error' && e.retryable && !e.stack?.includes('secret-credential'));
});
test('malformed successful JSON is bad_response at the HTTP status', async () => {
  await assert.rejects(fakeClient(async()=>new Response('secret-credential invalid JSON',{status:202})).send(request),e=>e instanceof HubError && e.status===202 && e.code==='bad_response' && !e.stack?.includes('secret-credential'));
});
test('body deadline and caller abort after headers use fakeClock and clean timers', async () => {
  for (const cause of ['deadline','caller'] as const) {
    const clock=fakeClock(), abort=new AbortController();
    let requestSignal: AbortSignal | undefined;
    const c=createHubClient({baseUrl:'http://hub.invalid',credential:'secret-credential',clock,timeoutMs:100,fetch:async(_url,init)=>{
      requestSignal=init?.signal ?? undefined;
      return new Response(new ReadableStream({start(ctrl){requestSignal?.addEventListener('abort',()=>ctrl.error(new Error('secret-credential abort')),{once:true});}}),{status:202});
    }});
    const pending=c.send(request,abort.signal);
    const checked=assert.rejects(pending,e=>e instanceof HubError && e.status===0 && e.code==='network_error' && e.retryable && !e.stack?.includes('secret-credential'));
    await setImmediate();
    try {
      clock.advance(99);assert.equal(requestSignal?.aborted,false);
      if (cause==='caller') abort.abort('secret-credential'); else clock.advance(1);
      assert.equal(requestSignal?.aborted,true);
    } finally {abort.abort();}
    await checked;
  }
  for (const status of [200,400]) {
    const clock=fakeClock();let requestSignal: AbortSignal | undefined;
    const c=createHubClient({baseUrl:'http://hub.invalid',credential:'secret-credential',clock,timeoutMs:100,fetch:async(_url,init)=>{requestSignal=init?.signal ?? undefined;return json(status===200?response:{error:{code:'invalid'}},status);}});
    if(status===200)await c.send(request);else await assert.rejects(c.send(request),HubError);
    clock.advance(1000);assert.equal(requestSignal?.aborted,false,'settled requests must cancel deadline timers');
  }
});
