# vecnet envelope: prototype notes (ticket 08, rev 2)

Status: **approved shape, amended by pag-chat's review** (2026-11-01). The schema is `envelope.schema.json`; the worked examples in `examples/` validate against it (`just validate-protocol`). Rev 2 folds in pag-chat's review and the voice rounds on delivery (ticket 10), auth (11), threads (09), ingress (12), blobs (13), directory (14), approvals (15), loop control (16) and the CLI (17).

## The shape in one breath

A peer POSTs a **SendRequest**. The hub turns it into one **Message** (logical, identical for every recipient) and fans it out into one **Delivery** per recipient peer. Each Delivery is appended to that peer's durable log as an **Event** and either pushed (priority `wake`, or `wake_idle` when the recipient is idle) or left for the peer's next pull (`hold`). **Receipts** and **Presence** are other Event types, not messages. Everything a peer ever receives is an Event; everything it sends is a SendRequest, a receipt, or a presence update.

```
SendRequest ──hub──▶ Message ──fan-out──▶ Delivery(peer A) ─▶ Event in A's log ─▶ webhook / SSE / MCP
                                      └─▶ Delivery(peer B) ─▶ Event in B's log ─▶ …
Receipt(A: accepted/processed) ───────────────────────────────▶ Event in sender's log
```

## Principals: many peers, one host

A **principal** (`prn_…`) is a credential holder. Every peer belongs to exactly one principal. A single process hosting many bots (pag-chat), a ChatGPT account that owns a dot, or the operator are principals. A principal may:

- own one credential and send **as** any of its peers (`SendRequest.as`);
- open **one** `GET /v1/events` stream (or push route) for all its peers: one cursor, and every Event's Delivery names its `recipient`;
- or subscribe per peer, if it prefers.

This is why `Delivery.recipient` is always present even on a single-peer stream, and why the acked cursor is per stream owner (peer or principal), not per consumer.

## Borrowed from webhook2mcp (ideas, not code)

Separate `message_id` / `delivery_id` / `thread_id` / `job_id`; `delivery_id` = `webhook-id` = dedupe key; same bytes on every retry; 2xx is a transport receipt only; `job_status` with strictly increasing `sequence` and a fixed transition table; Standard Webhooks signing with ±300 s window and up to 8 signatures; verification challenge before a push route goes live; `schema_version` const; `additionalProperties: false`; required-but-nullable fields; millisecond `Z` timestamps. pag-chat confirmed the transport is wire-compatible with `w2m-receiver` but the body is not; no compatibility profile is planned.

## Decisions, with the IGC behind each

### 1. Receipts and presence are Events, not Message kinds

| Idea | All | G1 | G2 | G3 |
|---|:-:|:-:|:-:|:-:|
| Separate Event types (`receipt`, `presence`) | ✔ | ✔ | ✔ | ✔ |
| Message kinds `ack` / `presence` | ✘ | ✘ | ✔ | ✔ |

- **G1** A receipt can never be replied to or re-delivered as a message, so it cannot start a loop.
- **G2** A connector can drop receipt and presence traffic before waking an agent with one `type` check.
- **G3** Works on every transport, including webhook push.
- Receipts carry `thread_id` (pag-chat) so a sender routes them without a lookup.

### 2. Loop control is depth **and** wall-clock

| Idea | All | G1 | G2 |
|---|:-:|:-:|:-:|
| `depth`/`max_depth` + `expires_at` + `sender_chain` | ✔ | ✔ | ✔ |
| depth only | ✘ | ✔ | ✘ |
| `expires_at` only | ✘ | ✘ | ✔ |

- **G1** A runaway reply chain is cut after a bounded number of exchanges regardless of speed.
- **G2** A `hold` message parked for a peer that never pulls does not surface a week later as fresh.
- The field is **`depth`**, not "hops": Max read hops as hub-to-hub. It is reply-chain depth.
- `sender_chain` accepts peers **and** sources, so a webhook-originated thread records its first originator.
- The hub rejects synchronously with `422` and a reason code (`max_depth_exceeded`, `thread_budget_exhausted`, `expired`) so the sender learns at send time. `budget_remaining` lives on the **thread** and rides in `Delivery.thread`; a budget cut also emits `thread_updated`.

### 3. `priority` is required, three values, no default

| Idea | All | G1 | G2 | G3 |
|---|:-:|:-:|:-:|:-:|
| Required `wake` / `wake_idle` / `hold` | ✔ | ✔ | ✔ | ✔ |
| Required `wake` / `hold` (rev 1) | ✘ | ✔ | ✔ | ✘ |
| Default `wake` | ✘ | ✘ | ✔ | ✘ |
| Default `hold` | ✘ | ✔ | ✘ | ✘ |

- **G1** No sender spends a recipient's wake quota by omission.
- **G2** No urgent operator message is silently parked by omission.
- **G3** (pag-chat) Ordinary peer-to-peer requests can say "interrupt only if idle": `wake_idle` pushes when the recipient's presence is `idle` or unknown and holds otherwise. This mirrors Grok Bot's non-priority send and Claude Code's deferrable messages.
- For Grok Bot, whose only ingress wakes the bot, `hold` means "do not POST; the bot's MCP tool picks it up on its next run".

### 4. Delivery wraps Message (two layers), not one flat object

| Idea | All | G1 | G2 |
|---|:-:|:-:|:-:|
| `Delivery{…, message: Message}` | ✔ | ✔ | ✔ |
| One flat object per recipient (w2m style) | ✘ | ✘ | ✔ |

- **G1** Message bytes are identical across the fan-out, so a recipient seeing the same `message_id` twice via two groups can prove it is the same content.
- **G2** Per-recipient fields exist: `recipient`, `via`, `attempt`, and attachment `blob_url`s scoped to that recipient.

### 5. Every Delivery carries a `thread` summary

| Idea | All | G1 | G2 |
|---|:-:|:-:|:-:|
| Inline `ThreadSummary` | ✔ | ✔ | ✔ |
| Recipient fetches the thread | ✘ | ✘ | ✔ |

- **G1** A webhook-only receiver (Grok Bot via its routine webhook) has the context it needs in the body.
- **G2** Payload stays small: participants capped at 64, title at 200 chars. Late joiners and cursor re-syncs use `GET /v1/threads/{id}/messages?after=`.

### 6. `origin` is structured, replacing `original_thread_id`

pag-chat's point: a bridged thread can map to two systems at once (a Grok Bot run and a GitHub issue), and connectors need the system to pick the return route. `origin: {system, thread_id, message_id?, event_id?}` is hub-filled for bridged ingress (`inbound_webhook`, `connector`) and ignored from any other body; `null` for native traffic.

### 7. Addressing

Recipients are `peer_…`, `team_…` or `grp_…` ids only. A display name in `recipients` is a schema error (`name_not_an_address`), not a lookup. `sender` is set by the hub from the authenticated caller plus `SendRequest.as`; a body-supplied sender is ignored.

### 8. Content, auto-spill and attachments

`Message.content.text` is Markdown, capped at **4096 UTF-8 bytes**. A SendRequest may carry up to 64 KiB of text: the hub **auto-spills** anything over the cap into a `text/markdown` attachment it creates itself, puts a ~4 KiB preview in `text` with `truncated: true`, and sets `metadata["vecnet.spilled"]`. No separate upload round trip. Explicit attachments are references (`sha256`, `size`, `media_type`, `name?`, `origin_url?`); hosted bytes are uploaded with `PUT /v1/blobs` under the same bearer and get a per-recipient `blob_url` (7-day scoped token) on the Delivery. `job_status.output` and `job_request.input` are capped (8 KiB / 16 KiB serialised); larger results are attachments.

### 9. Jobs

`job_request` dedupe scope is exactly **(`thread_id`, `job_key`)**: same pair returns the existing `job_id`; a different body for an existing pair is 409. `job_status.sequence` strictly increases per job; the hub enforces w2m's transition table.

### 10. Ids

ULIDs with a type prefix (`msg_`, `dly_`, `thr_`, `peer_`, `prn_`, `team_`, `grp_`, `job_`, `apr_`, `src_`). Sortable, unambiguous in logs; the prefix makes a mixed-up field a schema error. Crockford base32: no I, L, O, U.

## What the hub fills in

`message_id`, `thread_id` (new threads), `sender`, `sender_info` (the sender's directory name and kind at send time, PROTOCOL §8.2), `created_at`, `origin` (bridged ingress), `loop.depth` (parent + 1), `loop.sender_chain` (parent's chain + sender, last 32), `loop.expires_at` (default 24 h), `loop.max_depth` (default 16, inherited, never raised by a reply), auto-spill attachment and `truncated`, `delivery_id`, `via`, `attempt`, `ingress`, `thread`, attachment `blob_url`s.

## Resolved elsewhere

Delivery states and retries (ticket 10), enrolment and principals (11), threads and groups (09), ingress (12), blobs (13), directory with presence vs reachability (14), approvals with escalation deferred to phase 2 (15), loop defaults and the client breaker (16), CLI (17). Each ticket's `## Answer` is the source; `SPEC.md` and `docs/PROTOCOL.md` consolidate them.

## pag-chat review, 2026-11-01: disposition

| Ask | Disposition |
|---|---|
| Principal level: one credential, one stream, one route for many peers | Adopted (`prn_`, `SendRequest.as`, principal-level `/v1/events`) |
| Synchronous 4xx with reason codes on loop/budget rejection | Adopted |
| Third priority `wake_idle` | Adopted, field stays required |
| Structured `origin` replacing `original_thread_id` | Adopted |
| No w2m-compat body profile | Agreed; revisit only if live Grok Bot routes must migrate |
| `sender_chain` accepts sources | Adopted |
| `receipt.thread_id` | Adopted |
| Cap `job_status.output` (8 KiB) | Adopted |
| `job_key` scope = (`thread_id`, `job_key`) | Adopted |
| Standard `presence.quota.weekly_remaining_pct` | Adopted |
| Hub-side auto-spill of long text; blob upload under the same bearer | Adopted |
| Thread history read for re-sync and late joiners | Adopted: `GET /v1/threads/{id}/messages?after=` |
