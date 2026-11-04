import { createServer } from 'node:http';
import type { IncomingHttpHeaders, ServerResponse } from 'node:http';
import { createHash } from 'node:crypto';
import { newUlid } from '../src/ids.ts';
import type { Clock } from '../src/ports.ts';
import type { DirectoryEntry, GroupInfo, EventType, VecnetEvent, SendRequest, SendResponse } from '../src/wire.ts';

export interface RecordedRequest { method: string; path: string; headers: IncomingHttpHeaders; body: unknown }
export interface FakeHub {
  url: string;
  bearer: string;
  requests: RecordedRequest[];
  acked: string[];
  push(type: EventType, data: unknown): string;
  dropConnections(): void;
  failNext(path: string, status: number, body?: unknown, headers?: Record<string, string>): void;
  setPeers(entries: DirectoryEntry[]): void;
  setGroups(entries: GroupInfo[]): void;
  putBlobAt(path: string, bytes: Uint8Array): void;
  close(): Promise<void>;
}

// Real timers are confined to the Clock port; callers can inject fakeClock.
const realClock: Clock = {
  now: () => Date.now(),
  timer(ms, fn) { const id = setTimeout(fn, ms); return { cancel: () => clearTimeout(id) }; },
  sleep(ms, signal) {
    return new Promise<void>((resolve, reject) => {
      if (signal?.aborted) { reject(signal.reason); return; }
      const timer = realClock.timer(ms, () => { signal?.removeEventListener('abort', abort); resolve(); });
      function abort() { timer.cancel(); signal?.removeEventListener('abort', abort); reject(signal?.reason); }
      signal?.addEventListener('abort', abort, {once:true});
    });
  },
};

export async function startFakeHub(opts: { bearer?: string; heartbeatMs?: number; clock?: Clock } = {}): Promise<FakeHub> {
  const clock = opts.clock ?? realClock;
  const bearer = opts.bearer ?? 'fake-hub-bearer';
  const requests: RecordedRequest[] = [];
  const acked: string[] = [];
  const events: VecnetEvent[] = [];
  const streams = new Set<ServerResponse>();
  const polls = new Set<() => void>();
  const blobs = new Map<string, Buffer>();
  const failures = new Map<string, Array<{status:number; body:unknown; headers:Record<string,string>}>>();
  let peers: DirectoryEntry[] = [];
  let groups: GroupInfo[] = [];
  let sequence = 0;
  let closed = false;
  const cursor = () => `e1.${sequence}`;
  function json(res: ServerResponse, status: number, body: unknown, headers: Record<string,string> = {}) {
    res.writeHead(status, {'Content-Type':'application/json', ...headers});
    res.end(status === 204 ? undefined : JSON.stringify(body));
  }
  function error(res: ServerResponse, status: number, code: string, message: string, details: Record<string,unknown> = {}) {
    json(res,status,{error:{code,message,details}});
  }
  const frame = (ev: VecnetEvent) => `id: ${ev.cursor}\nevent: ${ev.type}\ndata: ${JSON.stringify(ev)}\n\n`;
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url ?? '/', 'http://localhost');
      const path = url.pathname;
      const chunks: Buffer[] = [];
      for await (const chunk of req) chunks.push(Buffer.from(chunk));
      const bytes = Buffer.concat(chunks);
      const body: unknown = bytes.length ? (path === '/v1/blobs' ? bytes : JSON.parse(bytes.toString('utf8'))) : undefined;
      requests.push({method:req.method ?? 'GET',path,headers:{...req.headers},body});
      const blob = blobs.get(url.pathname + url.search) ?? blobs.get(path);
      if (!(req.method === 'GET' && blob && url.searchParams.has('t')) && req.headers.authorization !== `Bearer ${bearer}`) {
        error(res,401,'unauthenticated','Missing or invalid bearer credential'); return;
      }
      const fail = failures.get(path)?.shift();
      if (fail) { json(res,fail.status,fail.body,fail.headers); return; }
      if (req.method === 'GET' && blob) { res.writeHead(200,{'Content-Type':'application/octet-stream'}); res.end(blob); return; }
      if (req.method === 'GET' && path === '/v1/events') {
        const after = req.headers['last-event-id'] ?? url.searchParams.get('after') ?? acked.at(-1) ?? 'e1.0';
        const head = after === 'head';
        if (!head && (typeof after !== 'string' || !/^e1\.\d+$/.test(after))) {
          error(res,409,'cursor_purged','Cursor is no longer available',{oldest_cursor:'e1.0'}); return;
        }
        const n = head ? sequence : Number(String(after).split('.')[1]);
        const pending = () => events.filter(e => Number(e.cursor.split('.')[1]) > n);
        if (req.headers.accept?.includes('application/json')) {
          const respond = () => json(res,200,{events:pending(),next_cursor:cursor()});
          const wait = Math.min(25, Math.max(0, Number(url.searchParams.get('wait') ?? 25)));
          if (pending().length || !wait) { respond(); return; }
          let timer: {cancel():void};
          const finish = () => { timer.cancel(); polls.delete(finish); if (!res.destroyed) respond(); };
          timer = clock.timer(wait * 1000, finish);
          polls.add(finish);
          res.on('close', () => { timer.cancel(); polls.delete(finish); });
          return;
        }
        res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-store','X-Accel-Buffering':'no'});
        res.write(`retry: 3000\n\nid: ${cursor()}\ndata:\n\n`);
        for (const ev of pending()) res.write(frame(ev));
        streams.add(res);
        const heartbeat = () => { if (!res.destroyed) { res.write(': hb\n\n'); timer = clock.timer(opts.heartbeatMs ?? 15_000, heartbeat); } };
        let timer = clock.timer(opts.heartbeatMs ?? 15_000, heartbeat);
        res.on('close', () => { timer.cancel(); streams.delete(res); });
        return;
      }
      if (req.method === 'GET' && path === '/v1/peers') { json(res,200,{peers}); return; }
      if (req.method === 'GET' && path === '/v1/groups') { json(res,200,{groups}); return; }
      if (req.method === 'POST' && path === '/v1/messages') {
        const input = body as SendRequest;
        const result: SendResponse = {
          message_id:`msg_${newUlid(clock.now())}`, thread_id:input.thread_id ?? `thr_${newUlid(clock.now())}`,
          deliveries:(input.recipients ?? []).map(recipient => ({delivery_id:`dly_${newUlid(clock.now())}`,recipient,via:null,state:'queued'})),spilled:false,
          ...(input.job_request ? {job_id:`job_${newUlid(clock.now())}`} : {}),
        };
        json(res,202,result); return;
      }
      if (req.method === 'PUT' && path === '/v1/blobs') {
        const sha256 = createHash('sha256').update(bytes).digest('hex');
        blobs.set(`/v1/blobs/${sha256}`,bytes);
        json(res,201,{sha256,size:bytes.length}); return;
      }
      if (req.method === 'POST' && ['/v1/ack','/v1/receipts','/v1/presence'].includes(path)) {
        if (path === '/v1/ack') acked.push((body as {cursor:string}).cursor);
        json(res,204,undefined); return;
      }
      error(res,404,'not_found','Route not found');
    } catch {
      if (!res.headersSent) error(res,400,'invalid_body','Body is not valid JSON',{path:''});
      else res.destroy();
    }
  });
  await new Promise<void>((resolve, reject) => {
    server.once('error',reject);
    server.listen(0,'127.0.0.1',() => { server.removeListener('error',reject); resolve(); });
  });
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('fake hub did not bind');
  return {
    url:`http://127.0.0.1:${address.port}`,bearer,requests,acked,
    push(type,data) {
      const ev: VecnetEvent = {cursor:`e1.${++sequence}`,type,data,at:new Date(clock.now()).toISOString()};
      events.push(ev);
      for (const res of streams) res.write(frame(ev));
      for (const finish of [...polls]) finish();
      return ev.cursor;
    },
    dropConnections() { for (const res of streams) res.end(); streams.clear(); for (const finish of [...polls]) finish(); },
    failNext(path,status,body = {error:{code:'injected'}},headers = {}) {
      const queue = failures.get(path) ?? []; queue.push({status,body,headers}); failures.set(path,queue);
    },
    setPeers(entries) { peers = structuredClone(entries); },
    setGroups(entries) { groups = structuredClone(entries); },
    putBlobAt(path,bytes) { blobs.set(path,Buffer.from(bytes)); },
    async close() {
      if (closed) return; closed = true;
      for (const res of streams) res.end();
      for (const finish of [...polls]) finish();
      const done = new Promise<void>((resolve, reject) => server.close(err => err ? reject(err) : resolve()));
      server.closeAllConnections();
      await done;
    },
  };
}
