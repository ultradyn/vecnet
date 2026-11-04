import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { EventEmitter, once } from 'node:events';
import { startFakeHub } from '../testing/fake-hub.ts';
import { delivery, directoryEntry, fakeClock } from '../testing/fixtures.ts';

test('fake hub built-in errors use protocol statuses, codes and complete envelopes', async (t) => {
  const hub = await startFakeHub({clock:fakeClock()}); t.after(() => hub.close());
  const headers = {Authorization:`Bearer ${hub.bearer}`};
  const cases = [
    {name:'missing bearer',path:'/v1/events',init:{},status:401,code:'unauthenticated',details:{}},
    {name:'incorrect bearer',path:'/v1/events',init:{headers:{Authorization:'Bearer incorrect'}},status:401,code:'unauthenticated',details:{}},
    {name:'invalid cursor',path:'/v1/events?after=unknown',init:{headers},status:409,code:'cursor_purged',details:{oldest_cursor:'e1.0'}},
    {name:'unknown route',path:'/v1/unknown',init:{headers},status:404,code:'not_found',details:{}},
    {name:'malformed JSON',path:'/v1/messages',init:{method:'POST',headers,body:'{'},status:400,code:'invalid_body',details:{path:''}},
  ];
  for (const c of cases) await t.test(c.name, async () => {
    const response = await fetch(`${hub.url}${c.path}`,c.init);
    assert.equal(response.status,c.status);
    const body = await response.json();
    assert.equal(body.error.code,c.code);
    assert.equal(typeof body.error.message,'string'); assert.ok(body.error.message.length > 0);
    assert.deepEqual(body.error.details,c.details);
    assert.deepEqual(Object.keys(body.error).sort(),['code','details','message']);
  });
  await t.test('failNext preserves arbitrary malformed responses', async () => {
    const malformed = {error:{code:'intentionally_incomplete'}};
    hub.failNext('/v1/messages',503,malformed);
    const response = await fetch(`${hub.url}/v1/messages`,{method:'POST',headers,body:'{}'});
    assert.equal(response.status,503); assert.deepEqual(await response.json(),malformed);
  });
});

test('fake hub serves §2.3 frames, replays after Last-Event-ID, records sends and blob puts', async (t) => {
  const clock = fakeClock();
  const scheduled = new EventEmitter();
  const timer = clock.timer;
  clock.timer = (ms,fn) => { const alarm = timer(ms,fn); scheduled.emit('timer',ms); return alarm; };
  const hub = await startFakeHub({heartbeatMs:10,clock}); t.after(() => hub.close());
  const headers = {Authorization:`Bearer ${hub.bearer}`};
  assert.equal((await fetch(`${hub.url}/v1/events`)).status, 401);
  const response = await fetch(`${hub.url}/v1/events`, {headers:{...headers, Accept:'text/event-stream'}});
  assert.equal(response.headers.get('cache-control'), 'no-store');
  assert.equal(response.headers.get('x-accel-buffering'), 'no');
  const reader = response.body!.getReader();
  const decoder = new TextDecoder();
  const first = decoder.decode((await reader.read()).value);
  assert.match(first, /retry: 3000/); assert.match(first, /id: e1\.0\ndata:\s*\n/);
  const c1 = hub.push('delivery', delivery());
  let frame = ''; while (!frame.includes(`id: ${c1}`)) frame += decoder.decode((await reader.read()).value);
  assert.match(frame, /event: delivery/); assert.match(frame, /data: .*"type":"delivery"/);
  assert.ok(!first.includes(': hb')); assert.ok(!frame.includes(': hb'));
  clock.advance(10);
  let heartbeat = ''; while (!heartbeat.includes(': hb')) heartbeat += decoder.decode((await reader.read()).value);
  await reader.cancel();
  const c2 = hub.push('presence', {state:'idle'});
  const replay = await fetch(`${hub.url}/v1/events`, {headers:{...headers, Accept:'text/event-stream','Last-Event-ID':c1}});
  const replayReader = replay.body!.getReader();
  let replayText = ''; while (!replayText.includes('event: presence')) replayText += decoder.decode((await replayReader.read()).value);
  assert.ok(replayText.includes(`id: ${c2}`)); assert.ok(!replayText.includes('event: delivery'));
  await replayReader.cancel();
  const poll = await fetch(`${hub.url}/v1/events?after=${c1}&wait=0`, {headers:{...headers,Accept:'application/json'}});
  const polled = await poll.json(); assert.equal(polled.next_cursor, c2); assert.deepEqual(polled.events.map((e:any) => e.cursor), [c2]);
  const parked = once(scheduled,'timer');
  const waiting = fetch(`${hub.url}/v1/events?after=${c2}&wait=1`, {headers:{...headers,Accept:'application/json'}});
  assert.deepEqual(await parked,[1000]);
  const c3 = hub.push('directory_updated', {});
  assert.deepEqual((await (await waiting).json()).events.map((e:any) => e.cursor), [c3]);
  const timeoutParked = once(scheduled,'timer');
  const timedOut = fetch(`${hub.url}/v1/events?after=${c3}&wait=1`, {headers:{...headers,Accept:'application/json'}});
  assert.deepEqual(await timeoutParked,[1000]);
  clock.advance(1000);
  const timeoutResponse = await timedOut; assert.equal(timeoutResponse.status,200);
  assert.deepEqual(await timeoutResponse.json(),{events:[],next_cursor:c3});
  const post = (path:string, body:unknown) => fetch(`${hub.url}${path}`, {method:'POST',headers:{...headers,'Content-Type':'application/json'},body:JSON.stringify(body)});
  const body = {idempotency_key:'test-send', priority:'wake', content:{text:'hello'}};
  const sent = await post('/v1/messages', body); assert.equal(sent.status,202);
  const ids = await sent.json(); assert.match(ids.message_id,/^msg_/); assert.match(ids.thread_id,/^thr_/);
  const second = await (await post('/v1/messages', {...body,idempotency_key:'test-send-2'})).json(); assert.notEqual(ids.message_id,second.message_id);
  for (const path of ['/v1/ack','/v1/receipts','/v1/presence']) assert.equal((await post(path,{cursor:c1})).status,204);
  assert.deepEqual(hub.acked,[c1]); assert.deepEqual(hub.requests.find(r => r.path === '/v1/messages')!.body,body);
  const peers = [directoryEntry()]; hub.setPeers(peers);
  assert.deepEqual(await (await fetch(`${hub.url}/v1/peers`,{headers})).json(),{peers});
  const groups = [{group_id:'grp_test',members:[],created_by:'peer_test',archived:false}]; hub.setGroups(groups);
  assert.deepEqual(await (await fetch(`${hub.url}/v1/groups`,{headers})).json(),{groups});
  const bytes = Buffer.from('blob bytes');
  const upload = await fetch(`${hub.url}/v1/blobs`,{method:'PUT',headers,body:bytes}); assert.equal(upload.status,201);
  assert.deepEqual(await upload.json(),{sha256:createHash('sha256').update(bytes).digest('hex'),size:bytes.length});
  assert.deepEqual(hub.requests.find(r => r.method === 'PUT')!.body,bytes);
  hub.putBlobAt('/v1/blobs/test?t=token',bytes);
  assert.equal(await (await fetch(`${hub.url}/v1/blobs/test?t=token`)).text(),'blob bytes');
  hub.failNext('/v1/messages',503,{error:{code:'unavailable'}},{'Retry-After':'5'});
  const failed = await post('/v1/messages',body); assert.equal(failed.status,503); assert.equal(failed.headers.get('retry-after'),'5');
  assert.equal((await post('/v1/messages',body)).status,202);
  const drop = await fetch(`${hub.url}/v1/events`,{headers}); const dropReader = drop.body!.getReader(); await dropReader.read(); hub.dropConnections();
  assert.equal((await dropReader.read()).done,true);
});
