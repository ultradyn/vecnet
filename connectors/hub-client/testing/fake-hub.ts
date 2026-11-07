import { createServer } from 'node:http';
import type { IncomingHttpHeaders, ServerResponse } from 'node:http';
import { createHash } from 'node:crypto';
import { newUlid } from '../src/ids.ts';
import type { Clock } from '../src/ports.ts';
import type { DirectoryEntry, GroupInfo, EventType, VecnetEvent, SendRequest, SendResponse } from '../src/wire.ts';

export interface RecordedRequest { method: string; path: string; headers: IncomingHttpHeaders; body: unknown }
export interface SignedGrant { url: string; expires_at: string; object_expires_at: string | null; retention_class: string }
export interface FakeHub {
  url: string;
  bearer: string;
  requests: RecordedRequest[];
  acked: string[];
  s3Requests: RecordedRequest[];
  downloads: SignedGrant[];
  push(type: EventType, data: unknown): string;
  dropConnections(): void;
  failNext(path: string, status: number, body?: unknown, headers?: Record<string, string>): void;
  failNextS3(status: number): void;
  setPeers(entries: DirectoryEntry[]): void;
  setGroups(entries: GroupInfo[]): void;
  setRetention(value: string): void;
  omitUploadHeader(name: string): void;
  markExpired(id: string): void;
  markPurged(id: string): void;
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

const retentionSeconds: Record<string, number | null> = { '1h':3600, '6h':21600, '12h':43200, '1d':86400, '7d':604800, '30d':2592000, keep:null };

export async function startFakeHub(opts: { bearer?: string; heartbeatMs?: number; clock?: Clock; signedURLs?: boolean } = {}): Promise<FakeHub> {
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
  let retention = '1d';
  let minted = 0;
  const omitted = new Set<string>();
  const objects = new Map<string, {id:string; sha:string; size:number; media:string; className:string; expiresAt:number|null; bytes?:Buffer; state:'pending'|'complete'|'expired'|'purged'}>();
  const s3Requests: RecordedRequest[] = [];
  const downloads: SignedGrant[] = [];
  const s3Failures: number[] = [];
  let blobSeq = 0;
  let urlSeq = 0;
  let s3URL = '';
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
      if (req.method === 'GET' && path === '/v1/blob-capabilities') {
        json(res,200, opts.signedURLs
          ? {backend:'r2',upload_mode:'direct',mcp_transfer_mode:'signed_urls',mcp_remote_bytes:false,checksum_profile:'sha256',download_origins:[s3URL]}
          : {backend:'disk',upload_mode:'legacy',mcp_transfer_mode:'base64',mcp_remote_bytes:false,download_origins:[]});
        return;
      }
      if (opts.signedURLs && req.method === 'POST' && path === '/v1/blobs/uploads') {
        const input = body as {size:number; sha256:string; media_type:string};
        const id = `blo_${String(++blobSeq).padStart(26,'0')}`;
        const seconds = retentionSeconds[retention];
        const expiresAt = seconds == null ? null : clock.now()+seconds*1000;
        objects.set(id,{id,sha:input.sha256,size:input.size,media:input.media_type,className:retention,expiresAt,state:'pending'});
        const headers: Record<string,string> = {'Content-Length':String(input.size),'Content-Type':'application/octet-stream','Content-Disposition':'attachment','Cache-Control':'private, no-store','If-None-Match':'*','X-Amz-Checksum-Sha256':'required'};
        for (const name of omitted) delete headers[name];
        omitted.clear();
        json(res,201,{blob_id:id,sha256:input.sha256,sha256_verified:false,integrity:{algorithm:'sha256',value:input.sha256,verified:false},size:input.size,retention_class:retention,expires_at:expiresAt==null?null:new Date(expiresAt).toISOString(),upload:{method:'PUT',url:`${s3URL}/o/${id}?X-Amz-Signature=dummy&exp=${clock.now()+300_000}&n=${++urlSeq}`,headers,expires_at:new Date(clock.now()+300_000).toISOString()},complete_url:`/v1/blobs/uploads/${id}/complete`});
        return;
      }
      const complete = path.match(/^\/v1\/blobs\/uploads\/(blo_[0-9A-Z]+)\/complete$/);
      if (opts.signedURLs && req.method === 'POST' && complete) {
        const obj = objects.get(complete[1]);
        if (!obj?.bytes || obj.bytes.length !== obj.size) { error(res,409,'upload_incomplete','upload_incomplete'); return; }
        obj.state = 'complete';
        json(res,200,{blob_id:obj.id,sha256:obj.sha,sha256_verified:true,integrity:{algorithm:'sha256',value:obj.sha,verified:true},size:obj.size,retention_class:obj.className,expires_at:obj.expiresAt==null?null:new Date(obj.expiresAt).toISOString(),media_type:obj.media});
        return;
      }
      const download = path.match(/^\/v1\/blobs\/([0-9a-f]{64})\/download$/);
      if (opts.signedURLs && req.method === 'GET' && download) {
        const obj = objects.get(url.searchParams.get('blob_id') ?? '');
        if (!obj || obj.sha !== download[1]) { error(res,403,'forbidden','forbidden'); return; }
        if (obj.state === 'purged') { error(res,410,'blob_purged','blob_purged'); return; }
        if (obj.state === 'expired' || (obj.expiresAt != null && clock.now() >= obj.expiresAt)) { error(res,410,'blob_expired','blob_expired'); return; }
        if (obj.state !== 'complete') { error(res,409,'upload_incomplete','upload_incomplete'); return; }
        if (++minted > 30) { json(res,429,{error:{code:'blob_url_rate_limited',message:'blob_url_rate_limited',details:{retry_after_seconds:2}}},{'Retry-After':'2'}); return; }
        const ttl = obj.expiresAt == null ? 600 : Math.max(1, Math.min(600, Math.floor((obj.expiresAt-clock.now())/1000)));
        const signed = {url:`${s3URL}/o/${obj.id}?X-Amz-Signature=dummy&exp=${clock.now()+ttl*1000}&n=${++urlSeq}`,expires_at:new Date(clock.now()+ttl*1000).toISOString(),object_expires_at:obj.expiresAt==null?null:new Date(obj.expiresAt).toISOString(),retention_class:obj.className};
        downloads.push(signed);
        json(res,200,{method:'GET',...signed,blob_id:obj.id,sha256:obj.sha,sha256_verified:true,integrity:{algorithm:'sha256',value:obj.sha,verified:true},size:obj.size,media_type:obj.media});
        return;
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
  const s3 = createServer(async (req, res) => {
    const url = new URL(req.url ?? '/', 'http://localhost');
    const chunks: Buffer[] = [];
    for await (const chunk of req) chunks.push(Buffer.from(chunk));
    const body = Buffer.concat(chunks);
    s3Requests.push({method:req.method ?? 'GET',path:url.pathname+url.search,headers:{...req.headers},body});
    if (req.headers.authorization || req.headers.cookie) { res.writeHead(403); res.end(); return; }
    const exp = Number(url.searchParams.get('exp'));
    if (!Number.isFinite(exp) || clock.now() >= exp) { res.writeHead(403); res.end(); return; }
    const fail = s3Failures.shift();
    if (fail) { res.writeHead(fail); res.end(); return; }
    const obj = objects.get(url.pathname.split('/').at(-1) ?? '');
    if (req.method === 'PUT') {
      if (req.headers['if-none-match'] !== '*' || !req.headers['x-amz-checksum-sha256'] || !obj || obj.bytes) { res.writeHead(400); res.end(); return; }
      obj.bytes = body;
      res.writeHead(200); res.end(); return;
    }
    if (req.method === 'GET' && obj?.bytes) { res.writeHead(200,{'Content-Type':'application/octet-stream','Content-Length':String(obj.bytes.length)}); res.end(obj.bytes); return; }
    res.writeHead(404); res.end();
  });
  if (opts.signedURLs) {
    await new Promise<void>((resolve, reject) => { s3.once('error',reject); s3.listen(0,'127.0.0.1',() => { s3.removeListener('error',reject); resolve(); }); });
    const bound = s3.address();
    if (!bound || typeof bound === 'string') throw new Error('fake object store did not bind');
    s3URL = `http://127.0.0.1:${bound.port}`;
  }
  await new Promise<void>((resolve, reject) => {
    server.once('error',reject);
    server.listen(0,'127.0.0.1',() => { server.removeListener('error',reject); resolve(); });
  });
  const address = server.address();
  if (!address || typeof address === 'string') throw new Error('fake hub did not bind');
  return {
    url:`http://127.0.0.1:${address.port}`,bearer,requests,acked,s3Requests,downloads,
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
    failNextS3(status) { s3Failures.push(status); },
    setPeers(entries) { peers = structuredClone(entries); },
    setGroups(entries) { groups = structuredClone(entries); },
    setRetention(value) { retention = value; },
    omitUploadHeader(name) { omitted.add(name); },
    markExpired(id) { const obj = objects.get(id); if (obj) obj.state = 'expired'; },
    markPurged(id) { const obj = objects.get(id); if (obj) obj.state = 'purged'; },
    putBlobAt(path,bytes) { blobs.set(path,Buffer.from(bytes)); },
    async close() {
      if (closed) return; closed = true;
      for (const res of streams) res.end();
      for (const finish of [...polls]) finish();
      const done = new Promise<void>((resolve, reject) => server.close(err => err ? reject(err) : resolve()));
      server.closeAllConnections();
      if (opts.signedURLs) s3.closeAllConnections();
      await done;
      if (opts.signedURLs) await new Promise<void>((resolve, reject) => s3.close(err => err ? reject(err) : resolve()));
    },
  };
}
