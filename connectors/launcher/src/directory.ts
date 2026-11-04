import type { HubApi, Log } from '../../hub-client/src/ports.ts';
import type { DirectoryEntry } from '../../hub-client/src/wire.ts';

export interface Directory {
  get(peerId: string): DirectoryEntry | undefined;
  name(peerId: string): string;
  kindOf(peerId: string): string | undefined;
  createdBy(peerId: string): string | undefined;
  operatorPeerId(): string | undefined;
  isOperator(peerId: string): boolean;
  refresh(signal?: AbortSignal): Promise<void>;
  ensureKnown(peerId: string, signal?: AbortSignal): Promise<boolean>;
  onDirectoryUpdated(data: unknown): Promise<void>;
  onRemoved(fn: (peerId: string) => void): void;
}

export function createDirectory(deps: { hub: Pick<HubApi, 'peers'>; log: Log }): Directory {
  let entries = new Map<string, DirectoryEntry>();
  const listeners = new Set<(peerId: string) => void>();
  // Serialize snapshots so a slow, older response cannot undo a retirement.
  let pending: Promise<void> = Promise.resolve();
  function refresh(signal?: AbortSignal): Promise<void> {
    const update = pending.then(async () => {
      let peers: DirectoryEntry[];
      try { peers = await deps.hub.peers(signal); }
      catch {
        deps.log.warn('directory refresh failed');
        throw new Error('directory refresh failed');
      }
      const next = new Map(peers
        .filter(p => (p as DirectoryEntry & { disabled?: boolean }).disabled !== true)
        .map(p => [p.peer_id, structuredClone(p)]));
      const removed = [...entries.keys()].filter(id => !next.has(id));
      entries = next;
      for (const id of removed) for (const fn of listeners) {
        try { fn(id); }
        catch { deps.log.warn('directory removal listener failed'); }
      }
    });
    pending = update.catch(() => {});
    return update;
  }
  return {
    get: id => entries.get(id),
    name: id => entries.get(id)?.name ?? id,
    kindOf: id => entries.get(id)?.kind,
    createdBy: id => entries.get(id)?.created_by,
    operatorPeerId: () => [...entries.values()].find(p => p.kind === 'operator')?.peer_id,
    isOperator: id => entries.get(id)?.kind === 'operator',
    refresh,
    async ensureKnown(id, signal) {
      if (!entries.has(id)) await refresh(signal);
      return entries.has(id);
    },
    async onDirectoryUpdated(_data) { await refresh(); },
    onRemoved(fn) { listeners.add(fn); },
  };
}
