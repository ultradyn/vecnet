import type { Clock, CursorStore, HubApi, Log } from '../../hub-client/src/ports.ts';
import type { Delivery, VecnetEvent } from '../../hub-client/src/wire.ts';
import type { Ledger } from './ledger.ts';

const ACK_INTERVAL_MS = 1000;

function isDelivery(x: unknown): x is Delivery {
  if (!x || typeof x !== 'object') return false;
  const d = x as Partial<Delivery>;
  const m = d.message as Partial<Delivery['message']> | undefined;
  return typeof d.delivery_id === 'string' && typeof d.recipient === 'string' && !!m && typeof m === 'object' &&
    typeof m.message_id === 'string' && typeof m.thread_id === 'string' && typeof m.sender === 'string' &&
    typeof m.kind === 'string' && !!m.content && typeof m.content.text === 'string' &&
    !!m.metadata && typeof m.metadata === 'object' && Array.isArray(m.attachments) && !!m.loop && typeof m.loop === 'object';
}

/**
 * Admits Deliveries into the ledger, exactly once per delivery_id, and acks
 * the cursor only after the admission and the cursor have committed together.
 * Only `delivery` and `directory_updated` Events do anything (PROTOCOL 13.1).
 * `clock` batches acks to at most one a second; without it acks only coalesce.
 */
export function createIngress(deps: { ledger: Ledger; hub: Pick<HubApi, 'ack'>; log: Log; self: string; clock?: Clock;
  onDirectory(data: unknown): void }): { handler(e: VecnetEvent): Promise<void>; cursors: CursorStore } {
  const { ledger, hub, log, clock } = deps;
  let latest: string | null = null, acked: string | null = null;
  let inFlight = false, timer: { cancel(): void } | undefined, lastAckAt = -Infinity;

  function scheduleAck() {
    if (inFlight || timer || latest === null || latest === acked) return;
    const wait = clock ? lastAckAt + ACK_INTERVAL_MS - clock.now() : 0;
    if (clock && wait > 0) { timer = clock.timer(wait, () => { timer = undefined; scheduleAck(); }); return; }
    const cursor = latest;
    inFlight = true;
    lastAckAt = clock?.now() ?? 0;
    hub.ack(cursor).then(() => { acked = cursor; },
      () => log.warn('cursor ack failed; retrying with the next event')).finally(() => {
      inFlight = false;
      if (latest !== acked) scheduleAck();
    });
  }
  function saved(cursor: string) { latest = cursor; scheduleAck(); }

  async function handler(e: VecnetEvent): Promise<void> {
    if (e.type === 'delivery') {
      if (!isDelivery(e.data)) log.warn('dropping a malformed delivery', { cursor: e.cursor });
      else if (e.data.recipient !== deps.self) log.warn('dropping a delivery for another recipient', { cursor: e.cursor });
      else {
        const d = e.data;
        ledger.tx(() => {
          // A duplicate delivery_id (redelivery or replay) is dropped here.
          ledger.admit({ deliveryId: d.delivery_id, cursor: e.cursor, threadId: d.message.thread_id, messageId: d.message.message_id,
            sender: d.message.sender, delivery: d, receivedAt: clock?.now() ?? Date.parse(e.at) });
          ledger.setCursor(e.cursor);
        });
        saved(e.cursor);
        return;
      }
    } else if (e.type === 'directory_updated') {
      try { deps.onDirectory(e.data); } catch { log.warn('directory update handler failed'); }
    }
    // receipt, presence, thread_updated, approval_expired and unknown types
    // are dropped: the hub's approval_response message carries any expiry.
    ledger.setCursor(e.cursor);
    saved(e.cursor);
  }
  return {
    handler,
    cursors: {
      async load() { return ledger.cursor(); },
      async save(cursor) { ledger.setCursor(cursor); saved(cursor); },
    },
  };
}
