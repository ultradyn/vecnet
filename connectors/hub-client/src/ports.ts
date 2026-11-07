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
export interface BlobIntegrity { algorithm: string; value: string; verified: boolean }
// BlobRef is a stable object reference. It never carries a transfer URL.
export interface BlobRef {
  sha256: string;
  size: number;
  blob_id?: string;
  retention_class?: string;
  expires_at?: string | null;
  sha256_verified?: boolean;
  integrity?: BlobIntegrity;
  media_type?: string;
}
// A string is a legacy same-origin hub blob URL. An object is an R2 reference.
export type BlobLocator = string | { sha256: string; blob_id: string };
export interface HubApi {
  send(req: SendRequest, signal?: AbortSignal): Promise<SendResponse>;
  ack(cursor: string, signal?: AbortSignal): Promise<void>;
  receipt(body: ReceiptBody, signal?: AbortSignal): Promise<void>;
  presence(body: PresenceBody, signal?: AbortSignal): Promise<void>;
  // Disk returns {sha256, size}. R2 returns the completed reference and verified integrity.
  putBlob(bytes: Uint8Array, mediaType: string, signal?: AbortSignal): Promise<BlobRef>;
  peers(signal?: AbortSignal): Promise<DirectoryEntry[]>;
  groups(signal?: AbortSignal): Promise<GroupInfo[]>;
  // Legacy URLs stay on the hub origin, with no bearer and no redirects.
  // An R2 reference or a same-origin URL that names blob id is exchanged for one fresh grant, then fetched from an allowed credential-free origin.
  fetchBlob(locator: BlobLocator, opts: { maxBytes: number; signal?: AbortSignal }): Promise<Uint8Array>;
}
export type StreamStop = { reason: 'aborted' } | { reason: 'auth'; status: 401 | 403 } | { reason: 'disabled' };
export interface EventStream { run(handler: (e: VecnetEvent) => Promise<void>, signal: AbortSignal): Promise<StreamStop> }
export interface CursorStore { load(): Promise<string | null>; save(cursor: string): Promise<void> }
