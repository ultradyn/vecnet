import test from 'node:test';
import assert from 'node:assert/strict';
import { setImmediate } from 'node:timers/promises';
import { createServer } from 'node:http';
import { HubError } from '../src/errors.ts';
import { createEventStream } from '../src/stream.ts';
import { fakeClock, memoryLog, event } from '../testing/fixtures.ts';
import { startFakeHub } from '../testing/fake-hub.ts';
import type { VecnetEvent } from '../src/wire.ts';
const encode = (s: string) => new TextEncoder().encode(s);
const frame = (cursor: string) => `id: ${cursor}\nevent: delivery\ndata: ${JSON.stringify(event('delivery',{},cursor))}\n\n`;
async function until(fn: () => boolean) { for (let n=0;n<300;n++) {if (fn()) return; await setImmediate();} assert.fail('condition did not become true'); }
function harness(steps: Array<string | Response | Error>, initial: string | null = null, recovery: Pick<Parameters<typeof createEventStream>[0], 'onCursorPurged'> = {}) {
  const clock = fakeClock(), log = memoryLog(), abort = new AbortController();
  const requests: Array<{url: URL; headers: Headers; signal: AbortSignal}> = [], saved: string[] = [], waits: number[] = [];
  let stored = initial, open = 0;
  const sleep = clock.sleep.bind(clock);
  clock.sleep = (ms,signal) => {waits.push(ms);return sleep(ms,signal);};
  const fetcher: typeof fetch = async (input, init) => {
    assert.equal(init?.redirect,'error');
    const signal = init?.signal; assert.ok(signal);
    requests.push({url:new URL(String(input)),headers:new Headers(init?.headers),signal});
    const next = steps.shift();
    if (next instanceof Error) throw next;
    if (next instanceof Response) return next;
    let cleanup = () => {};
    const body = new ReadableStream<Uint8Array>({start(c) {
      open++;
      const stop = () => {open--; signal.removeEventListener('abort',stop);c.error(new Error('aborted'));};
      signal.addEventListener('abort',stop,{once:true});
      cleanup = () => {open--;signal.removeEventListener('abort',stop);};
      if (next !== undefined) {c.enqueue(encode(next));cleanup();c.close();}
    },cancel() {cleanup();}});
    return new Response(body,{headers:{'Content-Type':'text/event-stream'}});
  };
  const stream = createEventStream({...recovery,baseUrl:'http://hub.invalid',credential:'bearer',clock,log,fetch:fetcher,cursors:{async load(){return stored;},async save(c){saved.push(c);stored=c;}}});
  return {clock,log,abort,requests,saved,waits,stream,open:()=>open,async next(n: number) {await until(()=>requests.length>=n);},async retry(n: number) {await until(()=>waits.length>=n);clock.advance(60_000);await until(()=>requests.length>=n+1);}};
}

test('delivers events in order and resumes with Last-Event-ID after a drop', async t => {
  const clock = fakeClock(), hub = await startFakeHub({clock}); t.after(()=>hub.close()); hub.push('delivery',{one:1});hub.push('delivery',{two:2});
  const abort = new AbortController(), seen: string[] = [], saved: string[] = [];
  const stream = createEventStream({baseUrl:hub.url,credential:hub.bearer,clock,log:memoryLog(),cursors:{async load(){return null;},async save(c){saved.push(c);}}});
  const running = stream.run(async e => {seen.push(e.cursor);},abort.signal); t.after(()=>abort.abort());
  await until(()=>saved.length===2); hub.dropConnections(); await until(()=>hub.requests.length===1); await setImmediate(); clock.advance(3000);
  await until(()=>hub.requests.length===2);
  assert.deepEqual(seen,['e1.1','e1.2']); assert.deepEqual(saved,['e1.1','e1.2']); assert.equal(hub.requests[1]?.headers['last-event-id'],'e1.2');
  abort.abort();assert.deepEqual(await running,{reason:'aborted'});
});
test('first connect with no stored cursor sends neither header nor after', async () => {
  const h=harness([]), run=h.stream.run(async()=>{},h.abort.signal); await h.next(1);
  assert.equal(h.requests[0]?.headers.get('last-event-id'),null);assert.equal(h.requests[0]?.url.searchParams.has('after'),false);
  h.abort.abort();await run;
});
test('handler failure reconnects from the last good cursor and redelivers', async () => {
  const h=harness([frame('e1.1')+frame('e1.2'),frame('e1.2')],'e1.0'); const seen: string[]=[];
  let release!:()=>void; const gate=new Promise<void>(r=>{release=r;});
  const run=h.stream.run(async e=>{seen.push(e.cursor);if(seen.length===1)await gate;if(seen.length===2)throw new Error('private body');},h.abort.signal);
  await until(()=>seen.length===1);assert.deepEqual(h.saved,[]);release();await h.retry(1);await until(()=>h.saved.length===2);
  assert.equal(h.requests[0]?.headers.get('last-event-id'),'e1.0');assert.equal(h.requests[1]?.headers.get('last-event-id'),'e1.1');assert.deepEqual(seen,['e1.1','e1.2','e1.2']);assert.deepEqual(h.saved,['e1.1','e1.2']);assert.ok(!h.log.text().includes('private body'));
  h.abort.abort();await run;
});
test('priming frame does not move the resume cursor', async () => {
  const h=harness(['id: e1.99\ndata:\n\n'],'e1.1'), seen: VecnetEvent[]=[]; const run=h.stream.run(async e=>{seen.push(e);},h.abort.signal);
  await h.retry(1);assert.deepEqual(h.saved,[]);assert.deepEqual(seen,[]);assert.equal(h.requests[1]?.headers.get('last-event-id'),'e1.1');h.abort.abort();await run;
});
test('poison data frame is skipped and later frames still arrive', async () => {
  const h=harness(['id: e1.1\ndata: private-not-json\n\n'+frame('e1.2')]);const seen: string[]=[];
  const run=h.stream.run(async e=>{seen.push(e.cursor);},h.abort.signal);await until(()=>h.saved.length===2);
  assert.deepEqual(h.saved,['e1.1','e1.2']);assert.deepEqual(seen,['e1.2']);assert.ok(h.log.text().includes('e1.1'));assert.ok(!h.log.text().includes('private-not-json'));h.abort.abort();await run;
});
test('45 s of silence reconnects; a heartbeat comment prevents it', async t => {
  const h=harness([]), run=h.stream.run(async()=>{},h.abort.signal);await h.next(1);h.clock.advance(44_999);await setImmediate();assert.equal(h.requests.length,1);h.clock.advance(1);await h.retry(1);assert.equal(h.requests.length,2);h.abort.abort();await run;
  const clock=fakeClock(), hub=await startFakeHub({clock,heartbeatMs:15_000});t.after(()=>hub.close());const abort=new AbortController();t.after(()=>abort.abort());
  const running=createEventStream({baseUrl:hub.url,credential:hub.bearer,clock,log:memoryLog(),cursors:{async load(){return null;},async save(){assert.fail('priming must not save');}}}).run(async()=>assert.fail('no events'),abort.signal);
  await until(()=>hub.requests.length===1);await setImmediate();
  for(let n=0;n<5;n++){clock.advance(15_000);await setImmediate();await setImmediate();}
  assert.equal(hub.requests.length,1);abort.abort();await running;
});
test('honours the retry field and backs off with a 60 s cap', async () => {
  const h=harness(['retry: 10000\n\n',...Array.from({length:6},()=>new Error('network'))]);const run=h.stream.run(async()=>{},h.abort.signal);
  for(let n=1;n<=5;n++){await until(()=>h.waits.length>=n);const cap=Math.min(60_000,10_000*2**(n-1));assert.ok(h.waits[n-1]!>=cap/2 && h.waits[n-1]!<=cap);h.clock.advance(60_000);await h.next(n+1);}
  h.abort.abort();await run;
});
test('401 stops without retrying', async () => {
  for(const status of [401,403] as const){const h=harness([new Response(null,{status})]);assert.deepEqual(await h.stream.run(async()=>{},h.abort.signal),{reason:'auth',status});assert.equal(h.requests.length,1);assert.deepEqual(h.waits,[]);}
});
test('204 stops', async () => {const h=harness([new Response(null,{status:204})]);assert.deepEqual(await h.stream.run(async()=>{},h.abort.signal),{reason:'disabled'});assert.equal(h.requests.length,1);});
const purged = () => new Response(JSON.stringify({error:{code:'cursor_purged',details:{oldest_cursor:'e2.0'}}}),{status:409});
test('409 waits for explicit thread-history reconciliation before oldest_cursor resume', async () => {
  let release!:()=>void, reconciling=false;
  const gate=new Promise<void>(resolve=>{release=resolve;});
  const h=harness([purged()],'e1.9',{async onCursorPurged({oldestCursor,signal}){assert.equal(oldestCursor,'e2.0');assert.equal(signal,h.abort.signal);reconciling=true;await gate;}});
  const run=h.stream.run(async()=>{},h.abort.signal);
  try {
    await h.next(1);await setImmediate();assert.equal(reconciling,true);assert.equal(h.requests.length,1);
    h.clock.advance(100_000);await setImmediate();assert.equal(h.requests.length,1);assert.deepEqual(h.saved,[]);
    release();await h.next(2);
    assert.equal(h.requests[1]?.url.searchParams.get('after'),'e2.0');assert.equal(h.requests[1]?.headers.get('last-event-id'),null);assert.ok(h.log.text().includes('cursor_purged'));
  } finally {release();h.abort.abort();await run;}
});
test('409 without a successful reconciliation path rejects safely without resuming', async () => {
  for (const recovery of [{},{onCursorPurged:async()=>{throw new Error('private recovery details');}}]) {
    const h=harness([purged()],'e1.9',recovery);
    const run=h.stream.run(async()=>{},h.abort.signal);
    const checked=assert.rejects(run,e=>e instanceof HubError && e.status===409 && e.code==='cursor_purged' && !e.stack?.includes('private recovery details'));
    try {await h.next(1);await setImmediate();assert.equal(h.requests.length,1);await checked;assert.deepEqual(h.saved,[]);assert.deepEqual(h.waits,[]);}
    finally {h.abort.abort();await checked;}
  }
});
test('abort during reconciliation stops without another fetch or cursor save', async () => {
  let reconciling=false;
  const h=harness([purged()],'e1.9',{async onCursorPurged(){reconciling=true;await new Promise<void>(()=>{});}});
  const run=h.stream.run(async()=>{},h.abort.signal);
  try {await h.next(1);await setImmediate();assert.equal(reconciling,true);}
  finally {h.abort.abort();assert.deepEqual(await run,{reason:'aborted'});}
  assert.equal(h.requests.length,1);assert.deepEqual(h.saved,[]);
});
test('three frameless connects fall back to long-poll and return to SSE', async () => {
  const h=harness(['','','',new Response(JSON.stringify({events:[event('delivery',{},'e1.2')],next_cursor:'e1.2'}),{headers:{'Content-Type':'application/json'}})],'e1.1');const seen:string[]=[];
  const run=h.stream.run(async e=>{seen.push(e.cursor);h.clock.advance(300_000);},h.abort.signal);
  await h.retry(1);await h.retry(2);await h.retry(3);await h.next(5);
  assert.equal(h.requests[3]?.headers.get('accept'),'application/json');assert.equal(h.requests[3]?.headers.get('last-event-id'),null);assert.equal(h.requests[3]?.url.searchParams.get('after'),'e1.1');assert.equal(h.requests[3]?.url.searchParams.get('wait'),'25');
  assert.equal(h.requests[4]?.headers.get('accept'),'text/event-stream');assert.equal(h.requests[4]?.headers.get('last-event-id'),'e1.2');assert.deepEqual(seen,['e1.2']);h.abort.abort();await run;
});
test("abort resolves { reason: 'aborted' } and leaves no open socket", async t => {
  const h=harness([]),run=h.stream.run(async()=>{},h.abort.signal);await h.next(1);assert.equal(h.open(),1);h.abort.abort();assert.deepEqual(await run,{reason:'aborted'});assert.equal(h.open(),0);assert.equal(h.requests[0]?.signal.aborted,true);
  let connected=false, closed=false;
  const server=createServer((_req,res)=>{
    connected=true;
    res.on('close',()=>{closed=true;});
    res.writeHead(200,{'Content-Type':'text/event-stream'});
    res.write(': hb\n\n');
  });
  await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve));
  t.after(()=>{server.closeAllConnections();return new Promise<void>((resolve,reject)=>server.close(err=>err?reject(err):resolve()));});
  const address=server.address();assert.ok(address && typeof address!=='string');
  const abort=new AbortController();t.after(()=>abort.abort());
  const running=createEventStream({baseUrl:`http://127.0.0.1:${address.port}`,credential:'fake',clock:fakeClock(),log:memoryLog(),cursors:{async load(){return null;},async save(){}}}).run(async()=>{},abort.signal);
  await until(()=>connected);abort.abort();assert.deepEqual(await running,{reason:'aborted'});await until(()=>closed);assert.equal(closed,true);
});
test('a slow handler stays serial and persists before a reconnect', async () => {
  const h=harness([frame('e1.1')+frame('e1.2'),frame('e1.2')]);let release!:()=>void;
  const gate=new Promise<void>(r=>{release=r;});const seen:string[]=[];
  const run=h.stream.run(async e=>{seen.push(e.cursor);if(e.cursor==='e1.1')await gate;},h.abort.signal);
  await until(()=>seen.length===1);h.clock.advance(100_000);await setImmediate();
  assert.deepEqual(seen,['e1.1']);assert.deepEqual(h.saved,[]);assert.equal(h.requests.length,1);assert.equal(h.requests[0]?.signal.aborted,true);
  release();await h.retry(1);await until(()=>h.saved.length===2);assert.equal(h.requests[1]?.headers.get('last-event-id'),'e1.1');assert.deepEqual(seen,['e1.1','e1.2']);assert.deepEqual(h.saved,['e1.1','e1.2']);h.abort.abort();await run;
});
test('a repeated cursor_purged refusal backs off rather than spinning', async () => {
  const refusal=()=>new Response(JSON.stringify({error:{code:'cursor_purged',details:{oldest_cursor:'e2.0'}}}),{status:409});
  const h=harness([refusal(),refusal()],'e1.9',{async onCursorPurged(){}});const run=h.stream.run(async()=>{},h.abort.signal);
  await until(()=>h.waits.length===1);assert.equal(h.requests.length,2);assert.ok(h.waits[0]!>=1500);h.abort.abort();await run;
});
test('aborting a poll batch never saves a cursor beyond the last handled event', async () => {
  const h=harness(['','','',new Response(JSON.stringify({events:[event('delivery',{},'e1.1'),event('delivery',{},'e1.2')],next_cursor:'e1.2'}),{headers:{'Content-Type':'application/json'}})]);
  const seen:string[]=[];
  const run=h.stream.run(async e=>{seen.push(e.cursor);h.abort.abort();},h.abort.signal);
  await h.retry(1);await h.retry(2);await h.retry(3);await run;
  assert.deepEqual(seen,['e1.1']);assert.deepEqual(h.saved,['e1.1']);
});
