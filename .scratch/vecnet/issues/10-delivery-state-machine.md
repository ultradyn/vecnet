# Delivery state machine

Type: grilling
Status: resolved
Blocked by: 08

## Question

Per-delivery states and transitions: queued -> pushed -> accepted -> processed / failed -> retry (backoff schedule, bounded attempts, `Retry-After`) -> dead-letter; dedupe rules (same `delivery_id` + digest = same receipt; different bytes = 409); per-thread ordering and what it means for parallel recipients; how "hold until next turn" deliveries are parked and released; what the operator sees and can do with dead letters.

## Answer

Max approved by voice, 2026-11-01 ("sounds good").

- **States:** `queued → pushed → accepted → processed`; terminal: `failed`, `expired`, `dead_letter`. `accepted` = 2xx on push, or the recipient acks a cursor past the entry on pull. `processed` and `failed` come only from the recipient's receipt.
- **Retries (push):** 408, 429, 5xx and network errors retry with backoff `min(1 h, 2^attempt s) + jitter`, honouring `Retry-After`, 8 attempts, then `dead_letter`. 409 = identity conflict (same `delivery_id`, different bytes): terminal and alerts the operator. 410, 413 and every other 4xx: terminal (`failed` → `dead_letter`). Retries resend identical bytes and `webhook-id`.
- **Ordering:** pushes to one peer within one thread are serialised: message N+1 is not pushed until N is accepted or dead. Threads and peers proceed in parallel. The per-peer log is always appended in message creation order.
- **Hold:** a `hold` delivery is appended to the log and never pushed; it becomes `accepted` when the peer acks a cursor past it. If `expires_at` passes first it becomes `expired` and the sender receives a receipt.
- **Dead letters:** the sender receives a `receipt` event with `state: dead_letter`. The operator sees every dead letter in the dashboard and CLI and may retry or discard; nothing is auto-deleted.
