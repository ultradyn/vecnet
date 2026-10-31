# vecnet envelope: prototype notes (ticket 08)

Status: **prototype for review** (Max by voice, pag-chat over c2c). The schema is `envelope.schema.json`; the worked examples in `examples/` validate against it (`just validate-protocol`). Nothing here is final until ticket 08 closes; later tickets (09–17) refine delivery states, auth, blobs, directory and the CLI.

## The shape in one breath

A peer POSTs a **SendRequest**. The hub turns it into one **Message** (logical, identical for every recipient) and fans it out into one **Delivery** per recipient peer. Each Delivery is appended to that peer's durable log as an **Event** and either pushed (priority `wake`) or left for the peer's next pull (priority `hold`). **Receipts** and **Presence** are other Event types, not messages. Everything a peer ever receives is an Event; everything it sends is a SendRequest, a receipt, or a presence update.

```
SendRequest ──hub──▶ Message ──fan-out──▶ Delivery(peer A) ─▶ Event in A's log ─▶ webhook / SSE / MCP
                                      └─▶ Delivery(peer B) ─▶ Event in B's log ─▶ …
Receipt(A: accepted/processed) ───────────────────────────────▶ Event in sender's log
```

## Borrowed from webhook2mcp (ideas, not code)

Separate `message_id` / `delivery_id` / `thread_id` / `job_id`; `delivery_id` = `webhook-id` = dedupe key; same bytes on every retry; 2xx is a transport receipt only; `job_status` with strictly increasing `sequence` and a fixed transition table; Standard Webhooks signing with ±300 s window and up to 8 signatures; verification challenge before a push route goes live; `schema_version` const; `additionalProperties: false`; required-but-nullable fields; millisecond `Z` timestamps.

## Decisions, with the IGC behind each

### 1. Receipts and presence are Events, not Message kinds

| Idea | All | G1 | G2 | G3 |
|---|:-:|:-:|:-:|:-:|
| Separate Event types (`receipt`, `presence`) | ✔ | ✔ | ✔ | ✔ |
| Message kinds `ack` / `presence` | ✘ | ✘ | ✔ | ✔ |

- **G1** A receipt can never be replied to or re-delivered as a message, so it cannot start a loop. Grok Bot users burned weekly quotas on bot-to-bot ping-pong; an acknowledgement that is itself a message is exactly how that starts.
- **G2** A connector can drop receipt and presence traffic before waking an agent with one `type` check.
- **G3** Works on every transport, including webhook push (an Event is the push body too).

### 2. Loop control is hops **and** wall-clock

| Idea | All | G1 | G2 |
|---|:-:|:-:|:-:|
| `hops`/`max_hops` + `expires_at` + `sender_chain` | ✔ | ✔ | ✔ |
| hop count only | ✘ | ✔ | ✘ |
| `expires_at` only | ✘ | ✘ | ✔ |

- **G1** A runaway reply chain is cut after a bounded number of exchanges regardless of how fast it runs.
- **G2** A `hold` message parked for a peer that never pulls does not surface a week later as if fresh.
- `sender_chain` is for the recipient: pag-chat wants to detect A-B-A-B in code. The hub only enforces `max_hops` and `expires_at`.
- `budget_remaining` lives on the **thread**, not the message: it is a shared counter, so it rides in the Delivery's `thread` summary.

### 3. `priority` is required, with no default

| Idea | All | G1 | G2 |
|---|:-:|:-:|:-:|
| Required field | ✔ | ✔ | ✔ |
| Default `wake` | ✘ | ✘ | ✔ |
| Default `hold` | ✘ | ✔ | ✘ |

- **G1** No sender ever spends a recipient's wake quota by omission.
- **G2** No urgent operator message is silently parked by omission.
- Semantics: `wake` = push now via the recipient's push transport (webhook, MCP Events) or any open stream. `hold` = append to the log only; delivered when the recipient next pulls (SSE reconnect, long-poll, MCP `vecnet_inbox`). For Grok Bot, whose only ingress wakes the bot, `hold` means "do not POST; the bot's MCP tool picks it up on its next run".

### 4. Delivery wraps Message (two layers), not one flat object

| Idea | All | G1 | G2 |
|---|:-:|:-:|:-:|
| `Delivery{…, message: Message}` | ✔ | ✔ | ✔ |
| One flat object per recipient (w2m style) | ✘ | ✘ | ✔ |

- **G1** The Message bytes are identical across the fan-out, so a recipient that sees the same `message_id` twice via two groups can prove it is the same content.
- **G2** Per-recipient fields exist: `recipient`, `via` (which group), `attempt`, and attachment `blob_url`s whose read tokens are scoped to that recipient so a forwarded envelope does not leak bytes.

### 5. Every Delivery carries a `thread` summary

| Idea | All | G1 | G2 |
|---|:-:|:-:|:-:|
| Inline `ThreadSummary` (participants, title, count, budget) | ✔ | ✔ | ✔ |
| Recipient fetches the thread | ✘ | ✘ | ✔ |

- **G1** A webhook-only receiver (Grok Bot via its routine webhook) has the context it needs in the body; it cannot cheaply call back.
- **G2** Payload stays small: participants capped at 64, title at 200 chars.

### 6. Addressing

Recipients are `peer_…`, `team_…` or `grp_…` ids only. A display name in `recipients` is a schema error, not a lookup. `sender` is set by the hub from the authenticated caller; a body-supplied sender is ignored. This is pag-chat's "Shane Mac finance-bot leak" rule made structural.

### 7. Content and attachments

`content.text` is Markdown, capped at **4096 UTF-8 bytes** by the hub. Anything larger is an attachment of `text/markdown` with a preview in `text` and `truncated: true`. Attachments are references: `sha256`, `size`, `media_type`, optional `name` and `origin_url`. Hosted bytes get a per-recipient `blob_url` on the Delivery. Upload is `PUT /v1/blobs` before the send (ticket 13 owns policy).

### 8. Ids

ULIDs with a type prefix (`msg_`, `dly_`, `thr_`, `peer_`, `team_`, `grp_`, `job_`, `apr_`, `src_`). Sortable, unambiguous in logs, and the prefix makes a mixed-up field a schema error. Crockford base32: no I, L, O, U.

## What the hub fills in

`message_id`, `thread_id` (new threads), `sender`, `created_at`, `loop.hops` (parent + 1), `loop.sender_chain` (parent's chain + sender, last 32), `loop.expires_at` (default 24 h), `loop.max_hops` (default 16, inherited, never raised by a reply), `delivery_id`, `via`, `attempt`, `ingress`, `thread`, attachment `blob_url`s.

## Open points the next tickets own

- Delivery state machine, retry schedule, dead-letter visibility (ticket 10). The HTTP table in `examples/07-webhook-push.http` is the proposal.
- Who may create threads with whom, team vs ad hoc group lifecycle (ticket 09).
- Enrolment, per-peer secrets, OAuth principal → dot peer mapping, send scopes (ticket 11).
- Blob upload rights, TTL, read-token lifetime (ticket 13).
- Directory fields and presence staleness (ticket 14).
- Escalation timing for approvals (ticket 15); loop-control defaults and who may raise `max_hops` or a thread budget (ticket 16).
- CLI surface for coding sessions (ticket 17).

## Questions for pag-chat

1. Does `Delivery.thread.budget_remaining` plus `loop.sender_chain` give you enough to implement your circuit breaker, or do you want the hub to also emit a `thread_updated` event when a budget is cut?
2. Is `priority` as a two-value enum enough, or do you want a third value for "wake only if idle"?
3. `original_thread_id` is a nullable string you echo back. Do you need a structured `{system, id}` instead?
4. Anything in `examples/07-webhook-push.http` that `w2m-receiver` would reject?
