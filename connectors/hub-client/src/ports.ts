import type { SendRequest, SendResponse, ReceiptBody, PresenceBody, DirectoryEntry, GroupInfo, VecnetEvent } from './wire.ts';

export interface Clock {
  now(): number;
  sleep(ms: number, signal?: AbortSignal): Promise<void>;
  timer(ms: number, fn: () => void): { cancel(): void };
}
export interface Log {
  info(msg: string, meta?: Record<string, unknown>): void;
  warn(msg: string, meta?: Record<string, unknown>): void;
  error(msg: string, meta?: Record<string, unknown>): void;
}
export interface HubApi {
  send(req: SendRequest, signal?: AbortSignal): Promise<SendResponse>;
  ack(cursor: string, signal?: AbortSignal): Promise<void>;
  receipt(body: ReceiptBody, signal?: AbortSignal): Promise<void>;
  presence(body: PresenceBody, signal?: AbortSignal): Promise<void>;
  putBlob(bytes: Uint8Array, mediaType: string, signal?: AbortSignal): Promise<{ sha256: string; size: number }>;
  peers(signal?: AbortSignal): Promise<DirectoryEntry[]>;
  groups(signal?: AbortSignal): Promise<GroupInfo[]>;
  // Same origin as baseUrl only, redirect: "error", no bearer (the URL carries ?t=).
  fetchBlob(blobUrl: string, opts: { maxBytes: number; signal?: AbortSignal }): Promise<Uint8Array>;
}
export type StreamStop = { reason: 'aborted' } | { reason: 'auth'; status: 401 | 403 } | { reason: 'disabled' };
export interface EventStream { run(handler: (e: VecnetEvent) => Promise<void>, signal: AbortSignal): Promise<StreamStop> }
export interface CursorStore { load(): Promise<string | null>; save(cursor: string): Promise<void> }
