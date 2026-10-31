# Message envelope schema

Type: prototype
Status: resolved
Blocked by: 01, 02, 04

## Question

Draft the canonical message envelope: ids (`message_id`, `delivery_id`, `idempotency_key`), `thread_id`, `in_reply_to`, `original_thread_id`, sender, recipients (peer ids / group ids), kind (message, presence, ack, read, approval_request, approval_response, job_request, job_status), priority bit, `hop_count`, `ttl`, `sender_chain`, thread budget hint, content (text <= 4 KiB), attachments (hash, size, media type, origin url, blob ref), metadata, provenance (transport, peer, tier), `created_at`. Produce a JSON Schema + examples as the prototype, send to `cc-pagchat-cos` for review, then walk Max through it by voice.

## Answer

Prototype: [`docs/protocol/envelope.schema.json`](../../../docs/protocol/envelope.schema.json), worked examples in [`docs/protocol/examples/`](../../../docs/protocol/examples/) (all validate via `just validate-protocol`), design notes and IGCs in [`docs/protocol/ENVELOPE.md`](../../../docs/protocol/ENVELOPE.md). Max approved all five contested choices by voice on 2026-11-01:

1. **Two layers.** `SendRequest` → one `Message` (identical across recipients) → one `Delivery` per recipient peer, carrying `recipient`, `via` (group), `attempt`, `ingress` and per-recipient scoped `blob_url`s.
2. **Receipts and presence are Event types**, never message kinds, so they cannot be replied to or start loops. Event types: `delivery`, `receipt`, `presence`, `thread_updated`, `directory_updated`, `approval_expired`.
3. **`priority` is required**, `wake` | `hold`, no default. `hold` = log only, delivered on the recipient's next pull (for Grok Bot: its MCP tool on the next run).
4. **Loop control** = `hops`/`max_hops` (default 16, inherited, never raised by a reply) + `expires_at` (default 24 h) + `sender_chain` (last 32). Thread `budget_remaining` rides in `Delivery.thread`.
5. **Every Delivery carries a `ThreadSummary`** (title, participants ≤ 64, count, budget, group).
Also: prefixed ULID ids; recipients are ids only (names are a schema error); `sender` set by the hub from auth; 4 KiB text bytes with larger text as a `text/markdown` attachment; `job_request`/`job_status`/`approval_request`/`approval_response` as kinds or sub-objects borrowed in shape from webhook2mcp.

pag-chat's review is pending (four questions at the end of ENVELOPE.md); its answers land as amendments to the schema under this ticket's context pointer, not as a reopened decision.
