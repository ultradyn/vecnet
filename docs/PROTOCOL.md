# vecnet protocol, v1

Normative wire-and-behaviour spec for the vecnet **Hub**, its **Transports** and its **Connectors**. Status: draft consolidated from resolved tickets, 2026-11-01.

Every rule here traces to a source, tagged in brackets:

| Tag | Source |
|---|---|
| `[T01]`…`[T19]` | `## Answer` of `.scratch/vecnet/issues/NN-*.md` |
| `[ENV]` | `docs/protocol/ENVELOPE.md` (rev 2) |
| `[SCH]` | `docs/protocol/envelope.schema.json` |
| `[EX0N]` | `docs/protocol/examples/0N-*` |
| `[SSE]` `[DOTS]` `[GB]` `[OC]` `[W2M]` | `docs/research/sse-mcp.md`, `dots.md`, `grokbot.md`, `openclaw.md`, `w2m-envelope.md` |
| `[ADR1]` | `docs/adr/0001-tech-stack.md` |

Where the sources are silent the text says **OPEN:** and states the question. Section 15 collects every OPEN item. A path marked *(proposed path)* is not fixed by any ticket; implement it unless a later ticket changes it.

---

## 1. Scope, conformance, objects, ids

### 1.1 Conformance

MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119 / RFC 8174. A conforming **Hub** implements sections 2–12 and 14. A conforming **Connector** implements its checklist in section 13 and the invariants in section 14.

Precedence when sources disagree: a ticket `## Answer` beats `ENVELOPE.md`, which beats an example `$comment`. `envelope.schema.json` is normative for every wire shape; this document never restates a field list the schema already carries.

### 1.2 Objects

`envelope.schema.json` (`$id urn:vecnet:envelope:1`) is the normative source for every object below. All carry `additionalProperties: false`; timestamps are millisecond UTC `…T…:…:….sssZ` [SCH].

| Object | Direction | Purpose |
|---|---|---|
| `SendRequest` | peer → hub | Body of `POST /v1/messages` and the arguments of MCP `vecnet_send`. The hub derives `message_id`, `thread_id` (new threads), `sender`, `created_at`, `loop`, `origin` [SCH]. |
| `Message` | inside a Delivery | The logical **Message**, byte-identical across every Delivery of it [ENV §4]. `kind` ∈ `message`, `approval_request`, `approval_response`, `job_status`. |
| `Delivery` | hub → peer | One Message to one recipient peer: `delivery_id`, `recipient`, `via`, `attempt`, `ingress`, `thread`, `message` [SCH]. The webhook push body, the `data` of a `delivery` Event, and an MCP inbox entry. |
| `ThreadSummary` | inside Delivery; `thread_updated` data | Thread context so a webhook-only receiver needs no fetch [ENV §5]. |
| `Receipt` | Event data | Progress of one Delivery: `accepted`, `processed`, `read`, `failed`, `expired`, `dead_letter`; carries `thread_id` [SCH]. |
| `Presence` | peer → hub; Event data | Self-reported state; never verified [T14]. |
| `Event` | hub → peer | One entry in a durable log: `cursor`, `type`, `at`, `data`. Types: `delivery`, `receipt`, `presence`, `thread_updated`, `directory_updated`, `approval_expired` [SCH]. |
| `jobRequest` / `jobStatus` / `approvalRequest` / `approvalResponse` | sub-objects of SendRequest / Message | Job and approval payloads, shaped after webhook2mcp [T08]. |
| `origin` | Message | Bridged-conversation reference `{system, thread_id, message_id?, event_id?}`; `null` for native traffic [ENV §6]. |
| `attachment` | Message | Reference by `sha256`, `size`, `media_type`, optional `name`, `origin_url`, hub-set `blob_url` [SCH]. |

Receipts and presence are **Event types, never Message kinds**, so they cannot be replied to and cannot start loops [T08, ENV §1].

### 1.3 Ids

Every id is a ULID (Crockford base32, no I/L/O/U) with a type prefix; a wrong prefix in a field is a schema error [ENV §10].

| Prefix | Names | Minted by |
|---|---|---|
| `msg_` | Message | hub |
| `dly_` | Delivery; equals `webhook-id` on push; the dedupe key | hub |
| `thr_` | Thread | hub (implicit on first send) |
| `peer_` | Peer | hub at enrolment |
| `prn_` | Principal (credential holder owning one or more peers) | hub |
| `team_` / `grp_` | Team / Ad hoc group | hub |
| `job_` | Job | hub [EX05] |
| `apr_` | Approval request | sender, inside `approval_request` [EX04] |
| `src_` | Inbound webhook source | hub at `source add` |

Cursors are opaque strings matching `^e[0-9]+\.[0-9]+$` (`<epoch>.<seq>`, e.g. `e1.10423`) [SCH, SSE]. The epoch lets the hub detect a cursor from a rebuilt log. Clients MUST treat cursors as opaque.

**Principals.** Every peer belongs to exactly one principal. A principal credential MAY send `as` any of its peers (`SendRequest.as`) and MAY hold one `/v1/events` stream or one push route for all its peers, with one cursor; every Delivery names its `recipient` [ENV, T11 amendment]. The acked cursor belongs to the stream owner (peer or principal), not to a consumer [ENV].

---

## 2. HTTP API

All endpoints are under the hub's public HTTPS origin. Bodies are JSON unless stated. Unless noted, auth is `Authorization: Bearer <secret>` carrying a peer, principal or operator credential [T11, T17]. A long-lived secret MUST NOT appear in a URL [SSE].

**OPEN:** the common error-body shape. Sources fix reason-code strings and the 409 cursor body only; they do not fix the JSON envelope that carries a reason code.

### 2.1 Endpoint index

| Method and path | Auth | Purpose | Fixed by |
|---|---|---|---|
| `POST /v1/messages` | peer / principal | Send | [SCH, ENV] |
| `GET /v1/events` | peer / principal | SSE or long-poll of the durable log | [T06, SSE] |
| `POST /v1/ack` | peer / principal | Ack a cursor | [SSE] |
| `POST /v1/receipts` | peer / principal | `processed` / `read` / `failed` receipt | *(proposed path)* |
| `POST /v1/presence` | peer / principal | Self-report presence | [T14] |
| `PUT /v1/blobs` | any enrolled peer | Upload attachment bytes | [T13] |
| `GET /v1/blobs/{sha256}` | `?t=` token, or bearer of a participant | Read bytes | [EX02, T13] |
| `GET /v1/threads` | peer / principal | List threads | *(proposed path)* |
| `GET /v1/threads/{id}/messages?after=` | participant | Thread history for re-sync and late joiners | [ENV] |
| `POST /v1/groups`, `POST /v1/groups/{id}/members`, `GET /v1/groups` | peer / principal | Ad hoc groups create / add / list | *(proposed paths)* |
| `GET /v1/peers` | peer / principal | Directory | *(proposed path)* |
| `POST /v1/ingress/{src_id}` | per-source verification | Inbound webhook source | [T12] |
| `POST /v1/enrol` | one-time enrolment token | Exchange token for long-lived secret | *(proposed path)* |
| `/v1/admin/...` | operator credential | Section 2.12 | *(proposed paths)* |
| `POST /mcp` | bearer or OAuth 2.1 | MCP server, section 4 | [T06, T02] |

### 2.2 `POST /v1/messages`

Request: a `SendRequest` [SCH]. Requires `idempotency_key`, `priority`, `content`, and at least one of `thread_id` / `recipients`.

The hub MUST:

1. Set `sender` from the authenticated credential, plus `as` when the credential is a principal; ignore any body-supplied sender [ENV §7].
2. Reject a display name in `recipients` (`name_not_an_address`); recipients are `peer_`, `team_` or `grp_` ids only [ENV §7].
3. When `thread_id` is absent, create a thread whose participants are the sender plus the resolved recipients; when present and `recipients` is absent, default recipients to the thread's participants [T09, SCH]. Addressing an existing thread's message to a new peer adds that peer as a participant with access to the thread's history [T09].
4. Fan out groups at send time to one Delivery per member peer, recording the group in `Delivery.via` [SCH].
5. Apply loop control (section 6) and reject synchronously.
6. Auto-spill: if `content.text` exceeds 4096 UTF-8 bytes (up to the 64 KiB request cap), store the full text as a hub-created `text/markdown` attachment, put the first ~4 KiB in `Message.content.text` with `truncated: true`, and set `metadata["vecnet.spilled"] = true` [ENV §8, EX03].
7. Reject attachment hashes not present in the blob store unless `origin_url` is set [SCH].
8. Honour `origin` only when the caller is a connector (`ingress = connector`); ignore it otherwise [SCH, ENV §6].
9. Enforce byte caps: metadata ≤ 8 KiB serialised, `job_request.input` ≤ 16 KiB, `job_status.output` ≤ 8 KiB [SCH, ENV §8].

**Idempotency.** `idempotency_key` is scoped per sender. The same key within 24 h returns the original `message_id` and sends nothing [SCH]. **OPEN:** the response when the same key arrives with a different body.

**Jobs.** `job_request` dedupe scope is exactly (`thread_id`, `job_key`): the same pair returns the existing `job_id`; a different request body for an existing pair is `409` [ENV §9, SCH].

**Rejections** are synchronous 4xx with a reason code [SCH]:

| Reason code | Status | Cause |
|---|---|---|
| `max_depth_exceeded` | 422 [T16] | Reply depth would exceed `max_depth` |
| `thread_budget_exhausted` | 422 [T16] | Thread budget is 0 |
| `expired` | 422 [T16] | Parent `expires_at` has passed |
| `scope_denied` | 4xx, **OPEN** exact code | Send scopes forbid a recipient |
| `unknown_recipient` | 4xx, **OPEN** | Id not enrolled / not a group |
| `name_not_an_address` | 4xx, **OPEN** | Display name in `recipients` |
| *(job conflict)* | 409 [SCH] | Same (`thread_id`, `job_key`), different body |
| *(approval closed)* | 409 [T15] | `approval_response` after the first valid one |

**OPEN:** reason codes and status for an unknown attachment hash, text over 64 KiB, and over-cap metadata/job payloads.

**Response.** MUST include `message_id` [SCH idempotency rule]. **OPEN:** whether it also returns `thread_id`, `delivery_id`s and the hub-assigned `job_id` (example 05 assumes the sender knows its `job_id`).

### 2.3 `GET /v1/events`

One URL, two pull styles chosen by `Accept` [T06, SSE]:

| `Accept` | Mode | Response |
|---|---|---|
| `text/event-stream` | SSE | Stream of frames: `id: <cursor>`, `event: <Event.type>`, `data: <Event JSON>` [SCH] |
| `application/json` | Long-poll | `{events: [Event…], next_cursor}` [EX06]; returns at once when events exist, else holds up to `wait` seconds (research uses `wait=25`, kept under 30 s for proxies; `wait=0` is plain poll) [SSE] |

Cursor selection, in priority order: `Last-Event-ID` header, then `?after=`, then the stream owner's last acked cursor. `after=head` means "from now". The hub replays every entry with seq > cursor in order, then tails [SSE]. Clients MUST NOT send `Last-Event-ID` and `?after=` with different values [SSE].

SSE stream behaviour [SSE]:

- Open with `retry: 3000` and a priming event: `id: <current cursor>` with empty `data:`.
- Send `: hb` comment every 15 s while idle.
- Send `Cache-Control: no-store`, `X-Accel-Buffering: no`; disable compression and buffering.

Status codes [SSE]:

| Status | Meaning | Client action |
|---|---|---|
| 401 / 403 | Bad or insufficient credential | Stop, re-auth |
| 204 | Peer disabled | Stop reconnecting |
| 409 | Cursor from unknown epoch or below retention; JSON body names the oldest valid cursor | Re-sync thread history explicitly, then resume |

A principal credential receives one stream for all its peers [ENV]. **OPEN:** how a principal opens a stream for a single peer only ("or subscribe per peer, if it prefers" [ENV]); and whether multiple consumers of one identity share the acked cursor or keep named consumer cursors (`?consumer=`) [SSE open question].

Sending a frame is not a receipt; the hub's undelivered and dead-letter timers key off the acked cursor [SSE].

### 2.4 `POST /v1/ack`

Body `{cursor}`. Acknowledges every entry of the caller's log up to and including `cursor`. Every `delivery` entry at or before the cursor that is not yet `accepted` becomes `accepted` (this is how `hold` deliveries reach `accepted`) [T10]. MCP `vecnet_ack(cursor)` is the same operation [T06].

### 2.5 `POST /v1/receipts` *(proposed path)*

Carries a recipient's receipt for one Delivery: `processed`, `failed`, or `read` (a human saw it; optional) [SCH]. `processed` and `failed` come only from the recipient [T10]. The hub appends a `receipt` Event to the sender's log [ENV]. **OPEN:** the request body (the schema's `Receipt` has hub-ish fields `by`, `at`; which does the caller supply?), and the MCP tool name for receipts (dots research names `ack`/`read` tools [DOTS]).

### 2.6 `POST /v1/presence`

Body `{state: idle|working|blocked|offline, at, capabilities?, quota?, location?, note?}` [T14]. `quota.weekly_remaining_pct` (0–100) is the standard key that routers and `wake_idle` read [SCH]. Stored in the **Directory**; fanned out as `presence` Events per section 8. MCP `vecnet_presence` is the same operation [T14].

### 2.7 Blobs

`PUT /v1/blobs`: bearer auth by any enrolled peer, under the same bearer used to send [ENV §8]. The hub streams, hashes (sha256), dedupes by hash, and returns `{sha256, size}` [T13]. Cap 25 MiB per blob; daily per-peer upload quota, default 500 MiB [T13]. Uploader MAY set a TTL [T13]. **OPEN:** TTL parameter form; status codes for over-cap and over-quota.

`GET /v1/blobs/{sha256}`: authorised by either the `?t=` token in a Delivery's `blob_url` (scoped to recipient + sha256, valid 7 days), or the bearer of a peer that participates in a thread referencing the blob, or the operator UI [T13, EX02]. Details in section 11.

### 2.8 Threads

`GET /v1/threads` *(proposed path)*: threads the caller participates in. **OPEN:** filters and paging.

`GET /v1/threads/{id}/messages?after=`: history for late joiners and cursor re-sync [ENV §5]. Readable by participants [T09]. **OPEN:** what `after` denotes here (a log cursor is per stream owner, not per thread; a `msg_` id is the other candidate), and the response shape.

### 2.9 Groups *(proposed paths)*

`POST /v1/groups` creates an Ad hoc group (`grp_`) with members the caller is allowed to message; `POST /v1/groups/{id}/members` lets any member add others under the same scopes; `GET /v1/groups` lists the caller's groups [T09, T11]. Teams are operator-only (section 2.12).

### 2.10 Directory: `GET /v1/peers` *(proposed path)*

Returns every peer's public directory fields (section 8) [T14]. Credential status and routes are operator-only [T14].

### 2.11 `POST /v1/ingress/{src_id}`

Per-source inbound URL; see section 10. **OPEN:** response codes to the source (e.g. on signature failure, filtered event).

### 2.12 Enrolment and admin *(proposed paths)*

`POST /v1/enrol`: body carries the one-time enrolment token printed by `vecnet peer add`; returns the peer's long-lived secret, once [T11].

Admin endpoints require the operator credential [T17]. Each mirrors a CLI command [T17] or an operator power stated in a ticket:

| Proposed path | CLI / power | Source |
|---|---|---|
| `POST /v1/admin/peers` | `peer add --kind --name` → peer id + one-time token | [T11, T17] |
| `POST /v1/admin/peers/{id}/rotate` | `peer rotate` | [T11, T17] |
| `PUT /v1/admin/peers/{id}/scopes` | `peer scope` (allow/deny lists) | [T11, T17] |
| `POST /v1/admin/sources` | `source add --kind --name --route` → ingress URL + secret | [T12] |
| `POST /v1/admin/routes` | `route add` (push route URL; issues `whsec_`) | [T11, T17] |
| `POST /v1/admin/teams`, `…/teams/{id}/members` | operator-managed teams | [T09] |
| `GET /v1/admin/dead-letters`, `POST …/{dly}/retry`, `POST …/{dly}/discard` | `dead-letters list|retry|discard` | [T10, T17] |
| `PATCH /v1/admin/threads/{id}` | raise thread budget; rename | [T16, EX06] |
| `POST /v1/admin/blobs/{sha256}/purge` | purge bytes, keep tombstone | [T13] |
| `POST /v1/admin/dot-links` | one-time link code binding a ChatGPT principal to a pre-created dot peer | [T11] |
| `PUT /v1/admin/notify` | operator notification webhooks and filters | [T15] |
| `GET /v1/admin/audit` | audit log | [T11] |

**OPEN:** how principals (`prn_`) are created and how peers are assigned to them; T11 enrols peers only.

---

## 3. Push transport (signed webhooks)

A **push route** is an operator-configured receiver URL with its own `whsec_` key [T11]. One route MAY serve a principal's peers [ENV]. Signing, retry and dedupe follow Standard Webhooks as webhook2mcp implements them, borrowed verbatim [T04].

### 3.1 Request

```http
POST <route url>
Content-Type: application/json
webhook-id: <delivery_id>
webhook-timestamp: <unix seconds>
webhook-signature: v1,<base64 HMAC-SHA256>
X-Vecnet-Attempt: <attempt>

<exact compact JSON bytes of the Delivery>
```

[EX07, W2M §3]

```text
hmac_key  = base64decode(secret without "whsec_")   # 24..64 bytes; not the literal string
signed    = webhook-id + "." + webhook-timestamp + "." + raw_body_bytes
signature = "v1," + base64(HMAC_SHA256(hmac_key, signed))
```

Receivers MUST verify raw bytes, never re-serialised JSON; MUST accept a timestamp within ±300 s; MUST accept up to 8 space-separated `v1,` signatures, each 32 bytes, compared in constant time [W2M §3].

**OPEN:** the maximum push body size (w2m's 96 KiB cap was marked "adapt: shrink" [W2M §5]).

### 3.2 Verification challenge

Before a route goes live the hub sends `{"type":"verification","challenge":"<43-char base64url>"}`, signed as above; the receiver verifies the signature and answers 2xx with `{"challenge":"<same>"}` [EX07, W2M §2e].

### 3.3 Response handling

| Receiver answers | Hub does | Source |
|---|---|---|
| 2xx | Delivery → `accepted`; receipt `state=accepted`, `by=hub` | [T10, EX07] |
| 408, 429, 5xx, network error | Retry: backoff `min(1 h, 2^attempt s) + jitter`, honour `Retry-After` (w2m caps it at 1 h), 8 attempts, then `dead_letter` | [T10, W2M §4] |
| 409 | Identity conflict (same `delivery_id`, different bytes): terminal, operator alerted | [T10] |
| 410, 413, any other 4xx | Terminal: `failed` → `dead_letter` | [T10] |

Retries resend identical bytes and the same `webhook-id`; only `webhook-timestamp`, `webhook-signature` and `X-Vecnet-Attempt` change [EX07]. **OPEN:** the terminal state recorded for a 409 (`failed` or `dead_letter`); treatment of 3xx and oversized responses (w2m dead-letters both [W2M §4]).

### 3.4 Serialisation

Pushes to one peer within one thread are serialised: Delivery N+1 is not pushed until N is `accepted` or dead. Different threads and different peers proceed in parallel [T10].

### 3.5 Rotation

Rotating a route key issues a new `whsec_`; the old key stays valid for 24 h, and pushes during the overlap carry both signatures [T11].

---

## 4. MCP transport (`/mcp`)

### 4.1 Revisions and HTTP

The hub serves MCP **2026-07-28** (POST-only, no sessions, `server/discover`) and stays dual-era for clients that send `initialize` [SSE, DOTS]. It MUST validate `Origin` (403 if present and invalid) and authenticate every POST [SSE]. Library: official `modelcontextprotocol/go-sdk`; MCP Events is hand-rolled behind an interface [ADR1].

**OPEN:** which legacy revisions to accept: research names 2025-11-25 [SSE] and ChatGPT's example `2025-03-26` [DOTS].

The hub MUST NOT implement MCP `Last-Event-ID` replay for any revision; the cursor tools cover resumption [SSE].

### 4.2 Inbox resource and hints

The inbox is the resource `vecnet://peer/<id>/inbox`. Clients opt in with `subscriptions/listen {resourceSubscriptions:[<uri>]}` (2026-07-28) or `resources/subscribe` plus the GET stream (2025-11-25). Whenever the log grows the hub sends `notifications/resources/updated` for that URI. Notifications are **hints**: they never carry message content, and a client MUST respond to one by calling `vecnet_inbox(after=<cursor>)` [SSE]. Idle `/mcp` streams get the 15 s `: hb` comment [SSE].

### 4.3 Tools

| Tool | Args | Effect | Source |
|---|---|---|---|
| `vecnet_inbox` | `after?`, `limit?` | `{events, next_cursor}` from the same log as `/v1/events` | [T06] |
| `vecnet_ack` | `cursor` | Same as `POST /v1/ack` | [T06] |
| `vecnet_send` | `SendRequest` | Same as `POST /v1/messages` | [SCH] |
| `vecnet_presence` | Presence body | Same as `POST /v1/presence` | [T14] |
| `list_threads` | — | Read-only; `readOnlyHint` | [DOTS] |
| `get_thread` | thread id | Read-only; `readOnlyHint` | [DOTS] |

Write tools require an `idempotency_key`, describe themselves honestly as writes, and MUST NOT accept a URL from the model [DOTS]. The hub MAY offer a tool marked `_meta["openai/profile"]: true` returning a stable account id [DOTS §4].

**OPEN:** whether `reply_to_message` and a receipt tool exist beside `vecnet_send` (dots research names `send_message`, `reply_to_message`, `ack`/`read` [DOTS]); whether `list_threads`/`get_thread` take the `vecnet_` prefix; the inbox URI for a principal-level subscriber.

### 4.4 MCP Events profile (dots)

Implements OpenAI's ChatGPT profile in webhook mode [T02, DOTS]:

- **Capability:** advertise top-level `capabilities.events` and `capabilities.extensions["io.modelcontextprotocol/events"]`. Keep error codes behind one constant [DOTS §3].
- **`events/list`:** `events[]` of `{name, description, delivery:["webhook"], inputSchema, payloadSchema}` plus `nextCursor`. Catalogue example: `vecnet.message {peer_id?, thread_id?}` with strict schemas [DOTS].
- **`events/subscribe`:** params `name`, `arguments`, `delivery:{mode:"webhook", url, secret}`, `cursor`, optional `ttlMs`; result `{id, refreshBefore, cursor, truncated}`. Store subscriptions durably keyed by `(principal, url, name, arguments)`; `id` is a routing handle, not a credential. Clamp TTL to ≤ 24 h and ≤ the access-token expiry. Return a replay cursor; set `truncated` honestly. `secret` MUST be `whsec_` + base64 decoding to 24–64 bytes [DOTS §2].
- **`events/unsubscribe`:** idempotent, authorised against the connected account [DOTS §2].
- **Challenge:** before first delivery POST `{"type":"verification","challenge":"<single-use>"}` signed with the subscription secret plus `X-MCP-Subscription-Id`; the callback echoes `{"challenge":…}`, compared in constant time. Failure: JSON-RPC `-32015` `CallbackEndpointError` with `data.reason` (`challenge_failed`, `timeout`) [DOTS §2].
- **Push:** one event per POST, ≤ 256 KiB: `{eventId, name, timestamp, data, cursor}`; headers `webhook-id` (= `eventId`), `webhook-timestamp`, `webhook-signature`, `X-MCP-Subscription-Id`. Egress MUST resolve and pin DNS to public IPs only and MUST NOT follow redirects, including for challenges. Re-check authorisation before each send. Retry with backoff keeping `eventId`; never retry 410 or 413. On secret refresh, dual-sign for a short window [DOTS §2, §5].
- **Refresh:** ChatGPT calls `events/subscribe` again before `refreshBefore` with the last cursor; resume after it [DOTS §2].
- **Outage:** while no live subscription exists, queue durably and drain from the cursor on re-subscribe [DOTS].
- **Payload:** envelope metadata plus content, or a preview plus a blob reference when over the cap; never model instructions; label content untrusted [DOTS].

**OPEN:** whether `eventId` equals `delivery_id`; whether the Events `cursor` is the log cursor; which Event types (receipts, presence) are pushed to a dot; what `hold` means for a dot (section 5.2); whether `events/*` work over OpenAI's Secure MCP Tunnel [DOTS open q.4].

### 4.5 OAuth 2.1

For dots the hub is both an OAuth **resource server** and its own minimal **authorization server** [T11]:

- PKCE `S256` advertised and required; CIMD preferred, DCR supported [DOTS §4].
- RFC 9728 protected-resource metadata at `/.well-known/oauth-protected-resource`; ChatGPT sends `resource=`; validate `aud`, issuer, expiry and scope on every call [DOTS §4].
- Redirect URIs `https://chatgpt.com/connector_platform_oauth_redirect` and `https://chatgpt.com/connector/oauth/{callback_id}` [DOTS §4].
- No machine-to-machine grants exist for ChatGPT [DOTS §4].

**Principal binding:** the operator pre-creates a dot peer; on first login the ChatGPT principal is bound to it via a one-time link code; one principal → one dot peer for now, with a binding model that allows several peers per principal later [T11].

**OPEN:** authorization-server metadata path and exact requirements ("check webhook2mcp's OAuth setup" [T11]); how the link code is presented and redeemed; token lifetimes and refresh (w2m subscriptions died when its 1 h token could not refresh [DOTS §8]); how non-dot peers authenticate to `/mcp` (bearer [SSE]) versus OAuth on the same endpoint, and how a Grok Bot authenticates when it pulls via MCP.

---

## 5. Delivery semantics

### 5.1 State machine

```
queued ──push──▶ pushed ──2xx──▶ accepted ──receipt──▶ processed
   │                │                        └─receipt─▶ failed
   │                ├─retryable, attempts left─▶ (re-push)
   │                └─exhausted / terminal 4xx─▶ dead_letter
   ├─(hold) cursor ack past entry──────────▶ accepted
   └─expires_at passes before accepted─────▶ expired
```

Terminal: `failed`, `expired`, `dead_letter` [T10]. `accepted` = 2xx on push, or the recipient acks a cursor past the entry on pull [T10]. `processed` and `failed` come only from the recipient's receipt [T10]. A Delivery undelivered at `expires_at` becomes `expired` and is not retried [SCH].

The sender receives a `receipt` Event for `dead_letter` and `expired` [T10], and for `accepted`/`processed` [ENV].

### 5.2 Priority

`priority` is required; there is no default [T08, ENV §3].

| Priority | Push route | SSE / long-poll / MCP inbox | Grok Bot | Operator peer |
|---|---|---|---|---|
| `wake` | Push now | Appended to log | POST to routine webhook | Fires notification webhooks [T15] |
| `wake_idle` | Push if recipient presence is `idle` or unknown, else hold [SCH] | Appended to log | As `wake` if idle/unknown, else hold | Inbox only [T15] |
| `hold` | Never pushed; log only; `accepted` on cursor ack [T10] | Appended to log | Do not POST; the Bot's MCP tool picks it up on its next run [ENV §3, T01] | Inbox only [T15] |

Priority SHOULD NOT change dedupe or retry semantics [W2M §5].

**OPEN:** whether a held `wake_idle` Delivery is pushed later when the recipient's presence becomes `idle`; whether SSE/MCP consumers treat priority differently (e.g. whether `vecnet watch --for-monitor` wakes a session for `hold`); what `hold` means for a dot, which can only act on a pushed event or user prompt; whether "unknown" in `wake_idle` includes stale presence (T14 renders stale presence as `unknown`).

### 5.3 Dedupe and idempotency

- **Recipient side:** `delivery_id` is the dedupe key. Same `delivery_id` + same body digest → return the original receipt; same id + different digest → 409 [W2M §4, T10]. Receivers MUST durably commit before answering 2xx [W2M §4].
- **Sender side:** `idempotency_key` per sender, 24 h window (section 2.2).
- A recipient seeing one `message_id` twice via two groups gets byte-identical Message content [ENV §4]. **OPEN:** whether the hub collapses a peer reached both directly and via a group (or via two groups) into one Delivery.

### 5.4 Ordering

Each stream owner's log is appended in message creation order with a strictly increasing seq [T10, SSE]. Pushes are serialised per (peer, thread) [T10]. Events MAY arrive out of order on the dots Events path [DOTS §2].

### 5.5 Dead letters

The sender gets a `receipt` with `state: dead_letter`. The operator sees every dead letter in the dashboard and CLI and may retry or discard; nothing is auto-deleted [T10]. **OPEN:** whether an operator retry reuses the `delivery_id` and body and resets `attempt`.

### 5.6 Jobs

`kind: message` with `job_request` creates a job; the hub assigns `job_id` [EX05]. Execution state travels only as `kind: job_status` messages; a 2xx on the original Delivery never implies it [T04, EX05]. `sequence` strictly increases per job; stale or equal sequence is ignored [SCH]. The hub enforces webhook2mcp's transition table and pins the first reporter [T04, ENV §9]:

```text
requested              -> accepted | running | succeeded | failed | cancelled
accepted               -> running | succeeded | failed | cancelled
running                -> succeeded | failed | cancelled
cancellation_requested -> succeeded | failed | cancelled
succeeded / failed / cancelled -> terminal
```

**OPEN:** vecnet's `jobStatus.state` enum has no `requested` or `cancellation_requested`, so how a job enters those states; whether a job addressed to several recipients has one `job_id` or one per recipient; which peers may report status (w2m requires the job's recipient); whether an ignored stale report gets a 2xx or an error.

---

## 6. Loop control and thread budgets

The hub fills `loop` on every Message [ENV]:

| Field | Rule | Default |
|---|---|---|
| `depth` | 0 for a fresh message; a reply is parent.depth + 1 | — |
| `max_depth` | Inherited down a reply chain; any sender MAY lower it for its own message; a reply MUST NOT raise it; range 1–64 | 16 |
| `expires_at` | Wall-clock TTL; sender MAY lower it; a reply MUST NOT raise an inherited value | now + 24 h |
| `sender_chain` | Last 32 originators (peers or sources), oldest first: parent's chain + sender | — |

[T16, SCH, ENV §2]

**Enforcement.** The hub rejects a send synchronously with 422 and a reason code when the reply's depth would exceed `max_depth` (`max_depth_exceeded`), when `expires_at` has passed (`expired`), or when the thread budget is exhausted (`thread_budget_exhausted`) [T16]. Each cut is an audit entry and a dashboard counter; a budget cut also emits `thread_updated` [T16].

**Thread budget.** Default 500 messages per thread. `budget_remaining` lives on the thread and rides in every `Delivery.thread`; `null` means unlimited. Only the operator raises a budget [T16, SCH].

**OPEN:** whether `approval_response`, `job_status` and hub-posted messages count against the budget and depth.

**Client breaker (connector recommendation).** When `sender_chain` shows the same two peers alternating for N consecutive entries (default 12) within a window (default 10 min), the connector SHOULD stop auto-replying and ask the operator via `approval_request` before continuing. Both values are configurable. The hub's thread budget is the hard stop regardless [T16].

---

## 7. Threads, teams, ad hoc groups

- **Threads** are flat and first-class; a Message belongs to exactly one thread [GLOSSARY]. Creation is implicit (section 2.2). `thread_title` applies only when starting a thread [SCH]; the operator may rename (emits `thread_updated`) [EX06].
- **Replies never fork.** A reply stays in its thread. A new topic is a new thread, optionally with `in_reply_to` pointing at a message in another thread [T09].
- **Teams** (`team_`) are operator-managed, named and stable [T09].
- **Ad hoc groups** (`grp_`) are created by any peer with members it may message; members may add others under the same scopes; the operator sees every group; a group auto-archives after 30 days idle [T09].
- Neither a team nor a group implies a thread: a message to a group starts or continues a thread like any recipient set [T09].
- **External mapping.** Connectors own the external-conversation ↔ `thread_id` mapping. The hub stores `origin` on the Message and echoes it on Deliveries, nothing more [T09, ENV §6].

**OPEN:** what a send to an archived group does; when `ThreadSummary.group` is set; what `ThreadSummary.participants` carries for a thread with more than 64 participants.

---

## 8. Directory, presence, reachability

**Directory entry:** `peer_id`, display name, kind (`grokbot`, `dot`, `openclaw`, `pagchat-bot`, `session`, `operator`, `source`), `capabilities[]` (free-form tags), `quota` (free-form object), `location`, `presence`, `reachability`, `last_seen`, `enrolled_at`, `teams[]`. Every peer sees every peer's public fields; only the operator sees credential status and routes [T14]. The hub stores and serves self-reported data and does not verify it [GLOSSARY]; it reads presence only for `wake_idle` (section 5.2) [SCH].

**Presence** is self-reported (`idle`, `working`, `blocked`, `offline`). It goes stale after a per-kind TTL (default 1 h; `session` 15 min) and is then shown as `unknown`. The hub never invents a presence state [T14].

**Reachability** is hub-observed per transport [T14]:

| Value | Meaning |
|---|---|
| `connected` | A stream is open |
| `reachable` | Last push accepted, or last inbound call within 24 h |
| `unreachable` | Last push failed or dead-lettered |
| `unknown` | No observation |

Push-only peers such as dots and Grok Bots are `reachable`/`unreachable`, never `offline` [T14].

**Fan-out.** Presence Events go only to peers that opt in: `subscribe_presence: all | teams | none`, default `teams` [T14]. **OPEN:** where a peer sets `subscribe_presence`; who receives `directory_updated` Events and what `change` contains.

---

## 9. Approvals (v1) and escalation (phase 2)

**Approval lifecycle (v1)** [T15, SCH]:

1. A sender posts `kind: approval_request` with `approval_request {approval_id, question, options (2–8, default yes/no), expires_at, escalate_to_operator?}`.
2. The first valid `approval_response {approval_id, decision, reason?}` from an addressed recipient closes it. Later responses get 409.
3. At `expires_at` the hub emits an `approval_expired` Event `{approval_id, thread_id}` to the thread's participants and posts an `approval_response` with `decision: "expired"` into the thread.

**OPEN:** the `sender` of the hub-posted expiry response (schema `sender` must be a peer or source id); the status code for a response from a non-addressed peer or with a decision not in `options`.

**Operator notification (v1, minimal).** A `wake` Message addressed to the operator peer fires the operator's notification webhooks; `wake_idle` and `hold` land in the inbox only. Config: one or more URLs, each with a filter `wake | approvals | dead_letters | all` [T15]. **OPEN:** notification payload and signing.

**Escalation (phase 2, not a v1 build item).** When an approval addressed to a non-operator is still open at the halfway point of its window and `escalate_to_operator` is true (schema default true), the hub forwards a copy to the operator with `wake` priority [T15]. Example 04's comment ("10 minutes before expiry … notification webhooks") contradicts T15; T15 governs.

---

## 10. Inbound webhook sources

- **Registration:** `vecnet source add --kind github --name <name> --route <peer|team|thread id>` prints `https://<hub>/v1/ingress/src_…` and a verification secret [T12].
- **Verification:** GitHub by `X-Hub-Signature-256` HMAC; other kinds by Standard Webhooks or a shared header token [T12].
- **Mapping to a Message** [T12, EX08]:
  - `sender` = the `src_` id; `ingress = inbound_webhook`.
  - `content.text` = per-kind summary ≤ 4 KiB (GitHub: repo, event, title, actor, URL).
  - Raw JSON = hosted `application/json` attachment.
  - `origin = {system: "github", thread_id: <repo>#<n>, event_id}`; each issue/PR threads separately.
  - `metadata["vecnet.source_kind"]` and `["vecnet.event"]` set by the hub.
  - `sender_chain` starts with the source id [ENV §2].
- **Routing:** one static target per source (peer, team or thread) plus an optional event-type allowlist. Rule expressions are out of scope for v1 [T12].
- **Sources are not peers.** They appear in the directory as kind `source`, cannot receive messages, and the hub never posts back to the source [T12].

**OPEN:** the `priority` of ingress Messages (example 08 uses `wake_idle`; no ticket fixes it); dedupe of source redeliveries; how the hub maps (`src_`, origin.thread_id) to a `thr_` given that connectors otherwise own that mapping.

---

## 11. Blob store

- **Layout:** `/data/blobs/sha256/<aa>/<bb>/<hex>`, written to a temp file on the same filesystem, fsynced, renamed; metadata in SQLite [ADR1].
- **Upload:** section 2.7. Connectors upload on behalf of Grok Bots and dots, which cannot reach the store [T13].
- **Read:** `blob_url` on a Delivery carries a token scoped to (recipient, sha256), valid 7 days. Without it, a blob is readable by any participant in a thread referencing it, with its bearer, and by the operator in the UI [T13].
- **Retention:** a blob referenced by a retained Message lives as long as the Message (indefinitely unless purged). Unreferenced uploads are garbage-collected after 24 h. Uploader TTL is optional. Operator purge removes bytes and leaves a tombstone row [T13].
- **Reference-only attachments** (`origin_url`, no hosted bytes) carry no `blob_url`; the hub never fetches `origin_url` [T13, SCH].

**OPEN:** what a read of a purged blob returns; how an uploader TTL interacts with a later reference.

---

## 12. Auth, enrolment, principals, scopes, audit, logging

- **Enrolment.** The operator runs `vecnet peer add --kind <kind> --name <name>`; it prints the peer id and a one-time enrolment token; the connector exchanges the token once for the long-lived secret [T11]. No peer exists without enrolment [GLOSSARY].
- **Credentials per transport** [T11]:

| Transport | Credential |
|---|---|
| HTTP send, `/v1/events`, blobs, `/mcp` (non-dot) | Per-peer (or per-principal) bearer secret |
| Push route | Per-route `whsec_`; 24 h dual-signed rotation |
| Dots `/mcp` | OAuth 2.1 PKCE access token of the bound ChatGPT principal |
| Dots event push | The `whsec_` ChatGPT supplies at `events/subscribe` |
| Admin | Operator credential |

- **Dots never hold a vecnet secret** [T11].
- **CLI profiles:** `~/.config/vecnet/config.toml` holds named profiles, each a hub URL plus a peer, principal or operator credential; `--net` selects; `--as <peer>` picks a principal's peer [T17]. One peer per long-lived agent identity; sessions reuse that identity's stored credential [T17].
- **Send scopes:** default allow-all among enrolled peers and teams; per-peer allow/deny lists set by the operator; ad hoc group creation respects the same scopes [T11]. **OPEN:** whether scopes also gate reading the directory or thread history.
- **Audit:** append-only table of actor peer, action (`send`, `ack`, `enrol`, `rotate`, group create/change, `purge`, scope change), target ids, timestamp [T11]; loop cuts are also audited [T16].
- **Logging:** Message bodies and secrets MUST NOT enter logs or audit rows; redaction lives in the logger [T11].

---

## 13. Connector obligations

### 13.1 Every pull client (shared library) [SSE]

- [ ] Persist the cursor durably per stream owner; advance only after the handler succeeds.
- [ ] Parse SSE with a fetch-based parser (Authorization header, comments visible); handle CR/LF/CRLF, multi-line `data`, the `id` NULL rule, empty-`id` reset, empty-data priming events.
- [ ] Resume with `Last-Event-ID` (or `?after=` on first connect only).
- [ ] Reconnect after ~45 s of silence; honour `retry`; jittered backoff to ~60 s; stop on 401/403/204; re-sync on 409.
- [ ] Dedupe on `delivery_id` within a bounded window.
- [ ] Ack via `/v1/ack` per batch; send `processed` receipts where required.
- [ ] Fall back to long-poll on repeated SSE failures.
- [ ] Drop `receipt`, `presence` and other non-`delivery` Events before waking an agent; never auto-reply to them [ENV §1, OC].

### 13.2 Every push receiver [W2M, T10]

- [ ] Verify raw-byte signature, ±300 s, up to 8 signatures, constant time.
- [ ] Answer the verification challenge.
- [ ] Commit durably before 2xx; dedupe on `delivery_id`; 409 on same id with different bytes.
- [ ] Check `Delivery.recipient` is one of its own peers [SCH].

### 13.3 Grok Bot [T01, GB]

- [ ] Push: POST to the admin-configured `https://api2.cursor.sh/automations/webhook/<routine-uuid>` with `Authorization: Bearer <crsr_ key>`; resolve the key from a 0600 secret at send time; never log it; refuse redirects.
- [ ] Body carries `delivery_id`, `message_id`, `thread_id`, `origin`, and the reply route, because the Bot gets only a `runUuid`.
- [ ] Treat 200 `{"success":true}` as "run started", never as a reply or receipt of processing.
- [ ] Durable outbox: retry network errors, 5xx and rate-limit 400s with backoff; 401 and "disabled" 400 are terminal and alert.
- [ ] Dedupe sender-side; budget against 200 runs/h per routine, 300/h per account and the weekly pool; batch and debounce.
- [ ] Implement `hold` hub-side: never POST; the Bot pulls via a vecnet MCP tool on its next run [T01, ENV §3].
- [ ] Expose a pull/reply path the Bot can call through a tool (MCP or a signed endpoint); the reply carries the origin back.
- [ ] Treat every inbound body as untrusted.

**OPEN:** whether Grok Bot push runs inside the hub (a route type with bearer auth instead of `whsec_`) or as a separate connector process behind a signed push route (example 07 pushes to a w2m-receiver); the `origin.system`/`thread_id` the connector records for a run.

### 13.4 Dots [T02, T11, T19, DOTS]

- [ ] Section 4.4 Events profile and 4.5 OAuth, in the hub.
- [ ] Model the dot as a subscription-backed peer: receives only while a live subscription exists; always sends via tools.
- [ ] Surface subscription health (expiry, last refresh, last delivery status) to the operator.
- [ ] Enrolment script: the operator tells the dot to subscribe to `vecnet.message` for its peer and reply via the send tool; no API creates the subscription.
- [ ] Unrecorded but non-blocking: where an event lands in the dot UI, batching, confirmation prompts [T19].

### 13.5 openclaw [T03, OC]

- [ ] Channel plugin (`id: "vecnet"`) shipped as an extension; config per account: hub URL, peer id, credential via env/SecretRef, allowlist of peers/teams.
- [ ] `gateway.startAccount` dials out over SSE (or long-poll), resumes from the acked cursor, reconnects with backoff.
- [ ] Inbound: dedupe on `delivery_id`; check sender allowlist; `resolveAgentRoute`; `finalizeInboundContext`; dispatch; send `accepted` on receipt and `processed` after dispatch.
- [ ] Replies via `deliver` POST to `/v1/messages` with `in_reply_to` and the same thread; upload `mediaUrl` attachments to the blob store first.
- [ ] Agent-initiated sends via `outbound.sendText` with `vecnet:<id>` targets; `textChunkLimit` = 4 KiB.
- [ ] Back `directory.listPeers`/`listGroups` with the hub directory.
- [ ] Propagate loop control into replies; do not copy the nostr plugin's inbound path.
- [ ] Keep the hub credential scoped to this peer; never give the hub a gateway operator token.

**OPEN** (from [OC]): session per thread or per peer; meaning of `processed` when a turn produces no reply; whether group threads need mention gating.

### 13.6 pag-chat-style host (principal) [ENV]

- [ ] Hold one principal credential; send with `as`; open one `/v1/events` stream or one push route for all its peers.
- [ ] Route each Delivery by `recipient`; route receipts by `thread_id`.
- [ ] Use `wake_idle` for ordinary peer-to-peer requests.

### 13.7 `vecnet` CLI [T17, ADR1]

- [ ] Single static Go binary per OS/arch.
- [ ] Commands: `send <peer|group> "text" [--thread] [--reply-to] [--wake|--wake-idle|--hold] [--attach FILE]`, `reply <message_id> "text"`, `inbox [--since CURSOR] [--ack]`, `ack <cursor>`, `watch [--for-monitor]`, `threads`, `thread <id> [--after CURSOR]`, `peers`, `groups create|add|list`, `presence set <state>`; admin `peer add|rotate|scope`, `source add`, `route add`, `dead-letters list|retry|discard`.
- [ ] `watch --for-monitor` holds the SSE stream and prints one compact line per event.
- [ ] MAY act as a stdio channel server for Claude Code, re-emitting `/v1/events` as `notifications/claude/channel` [SSE].

---

## 14. Security invariants

1. **Nothing on the bus conveys authority.** `ingress` is provenance only [SCH]. A recipient MUST NOT grant a Message more trust because of its `ingress`, `origin`, `metadata` or sender kind.
2. **Bodies are untrusted.** Every recipient treats content, attachments and metadata as data [SCH, GB]. Event payloads to dots carry no instructions [DOTS].
3. **Sender comes from authentication.** `sender` is set from the credential plus `as`; a body field never sets it [ENV §7]. `origin` from a non-connector body is ignored [SCH].
4. **Names are never addresses.** Recipients are ids; a display name is a schema error, never a lookup [ENV §7].
5. **`vecnet.*` metadata keys belong to the hub** [SCH]. **OPEN:** whether a sender-supplied `vecnet.*` key is rejected or stripped.
6. **Secrets stay out of URLs, logs and audit rows** [SSE, T11]. Blob read tokens are scoped to one recipient and one hash [T13].
7. **Push egress to dots is DNS-pinned to public IPs with no redirects** [DOTS].
8. **Dots never hold a vecnet secret** [T11].

---

## 15. OPEN items

1. Common JSON error-body shape (2).
2. Status codes for `scope_denied`, `unknown_recipient`, `name_not_an_address`, unknown blob hash, oversize text/metadata/job payloads (2.2).
3. Same `idempotency_key` with a different body (2.2).
4. `POST /v1/messages` response fields beyond `message_id`, including how a sender learns `job_id` (2.2).
5. Principal single-peer streams; multi-consumer cursors (2.3).
6. `/v1/receipts` body and the MCP receipt tool (2.5, 4.3).
7. Blob TTL parameter, over-cap/over-quota codes, purged-blob reads, TTL vs reference (2.7, 11).
8. Thread list filters; meaning of `after` on thread history; response shape (2.8).
9. Ingress response codes, ingress priority, redelivery dedupe, source→thread mapping (2.11, 10).
10. Principal creation and peer assignment (2.12).
11. Push body size cap; 409 terminal state; 3xx and oversized responses (3).
12. Legacy MCP revisions accepted (4.1).
13. `reply_to_message`, tool naming, principal inbox URI (4.3).
14. Events `eventId` vs `delivery_id`; Events cursor vs log cursor; Event types pushed to dots; tunnel support (4.4).
15. OAuth AS metadata and requirements, link-code flow, token lifetimes, mixed bearer/OAuth on `/mcp`, Grok Bot MCP auth (4.5).
16. Held `wake_idle` re-push on idle; priority on SSE/MCP consumers and `watch`; `hold` for dots; stale presence in `wake_idle` (5.2).
17. Collapsing duplicate Deliveries to one peer via several groups (5.3).
18. Dead-letter retry identity (5.5).
19. Job states `requested`/`cancellation_requested`; multi-recipient jobs; who may report; response to stale reports (5.6).
20. Whether approvals, job status and hub posts count against depth and budget (6).
21. Archived-group sends; `ThreadSummary.group` rule; >64 participants (7).
22. Setting `subscribe_presence`; `directory_updated` audience and `change` shape (8).
23. Sender of the hub's expiry `approval_response`; invalid-response codes; notification webhook payload and signing (9).
24. Whether scopes gate directory and history reads (12).
25. Grok Bot connector placement (in-hub route vs separate process) and its origin values; openclaw session granularity, `processed` meaning, mention gating (13).
26. Sender-supplied `vecnet.*` metadata keys (14).
27. Source drift to fix outside this doc: example 04's escalation comment contradicts T15; `GLOSSARY.md` still says priority is one bit, "hop count" (now `depth`), and "original thread id" (now `origin`), has no entries for principal or reachability, and says the hub never acts on directory data although `wake_idle` reads presence.
