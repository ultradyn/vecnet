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
| `[R01]`…`[R27]` | Item NN of `docs/protocol/OPEN-ITEMS-RESOLVED.md` (section 15) |

A rule tagged `[Rnn]` settles a question the other sources left open. A path marked *(proposed path)* is not fixed by any ticket; implement it unless a later ticket changes it.

---

## 1. Scope, conformance, objects, ids

### 1.1 Conformance

MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119 / RFC 8174. A conforming **Hub** implements sections 2–12 and 14. A conforming **Connector** implements its checklist in section 13 and the invariants in section 14.

Precedence when sources disagree: a resolution `[Rnn]` beats a ticket `## Answer`, which beats `ENVELOPE.md`, which beats an example `$comment`. `envelope.schema.json` is normative for every wire shape; this document never restates a field list the schema already carries.

### 1.2 Objects

`envelope.schema.json` (`$id urn:vecnet:envelope:1`) is the normative source for every object below. All carry `additionalProperties: false`; timestamps are millisecond UTC `…T…:…:….sssZ` [SCH].

| Object | Direction | Purpose |
|---|---|---|
| `SendRequest` | peer → hub | Body of `POST /v1/messages` and the arguments of MCP `vecnet_send`. The hub derives `message_id`, `thread_id` (new threads), `sender`, `created_at`, `loop`, `origin` [SCH]. |
| `Message` | inside a Delivery | The logical **Message**, byte-identical across every Delivery of it [ENV §4, R17]. `kind` ∈ `message`, `approval_request`, `approval_response`, `job_status`. |
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

**Principals.** Every peer belongs to exactly one principal. A principal credential MAY send `as` any of its peers (`SendRequest.as`) and MAY hold one `/v1/events` stream or one push route for all its peers, with one cursor; every Delivery names its `recipient` [ENV, T11 amendment]. A principal MAY narrow a stream to one peer with `?peer=` (section 2.3) [R05]. The acked cursor belongs to the stream owner (peer or principal) and scope, not to a consumer [ENV, R05]. The operator creates principals and assigns peers to them (section 2.12) [R10].

**Actors.** A Message `sender` (schema `actorId`) is a `peer_` id, a `src_` id, or the constant `"hub"`. The hub is the sender only of messages it authors: the expiry `approval_response` (section 9) [SCH, R23].

---

## 2. HTTP API

All endpoints are under the hub's public HTTPS origin. Bodies are JSON unless stated. Unless noted, auth is `Authorization: Bearer <secret>` carrying a peer, principal or operator credential [T11, T17]. A long-lived secret MUST NOT appear in a URL [SSE].

**Errors.** Every non-2xx response carries this body [R01]:

```json
{"error": {"code": "<snake_case>", "message": "<human>", "details": {}}}
```

`code` is one of the reason codes this document names. `message` is human-readable. `details` holds code-specific fields, such as the oldest valid cursor on a `/v1/events` 409.

### 2.1 Endpoint index

| Method and path | Auth | Purpose | Fixed by |
|---|---|---|---|
| `POST /v1/messages` | peer / principal | Send | [SCH, ENV] |
| `GET /v1/events` | peer / principal | SSE or long-poll of the durable log | [T06, SSE] |
| `POST /v1/ack` | peer / principal | Ack a cursor | [SSE] |
| `POST /v1/receipts` | recipient peer / its principal | `processed` / `read` / `failed` receipt | [R06] |
| `POST /v1/presence` | peer / principal | Self-report presence | [T14] |
| `PUT /v1/blobs?ttl=` | any enrolled peer | Upload attachment bytes | [T13, R07] |
| `GET /v1/blobs/{sha256}` | `?t=` token, or bearer of a participant | Read bytes | [EX02, T13] |
| `GET /v1/threads` | peer / principal | List threads | [R08] |
| `GET /v1/threads/{id}/messages?after=` | participant | Thread history for re-sync and late joiners | [ENV, R08] |
| `POST /v1/groups`, `POST /v1/groups/{id}/members`, `GET /v1/groups` | peer / principal | Ad hoc groups create / add / list | *(proposed paths)* |
| `GET /v1/peers` | peer / principal | Directory | *(proposed path)* |
| `PATCH /v1/peers/me` | peer | Set own `subscribe_presence` | [R22] |
| `POST /v1/ingress/{src_id}` | per-source verification | Inbound webhook source | [T12] |
| `POST /v1/enrol` | one-time enrolment token | Exchange token for long-lived secret | *(proposed path)* |
| `/v1/admin/...` | operator credential | Section 2.12 | *(proposed paths)* |
| `POST /mcp` | bearer or OAuth 2.1 | MCP server, section 4 | [T06, T02, R15] |

### 2.2 `POST /v1/messages`

Request: a `SendRequest` [SCH]. Requires `idempotency_key`, `priority`, `content`, and at least one of `thread_id` / `recipients`.

The hub MUST:

1. Set `sender` from the authenticated credential, plus `as` when the credential is a principal; ignore any body-supplied sender [ENV §7].
2. Reject a display name in `recipients` (`name_not_an_address`); recipients are `peer_`, `team_` or `grp_` ids only [ENV §7].
3. When `thread_id` is absent, create a thread whose participants are the sender plus the resolved recipients; when present and `recipients` is absent, default recipients to the thread's participants [T09, SCH]. Addressing an existing thread's message to a new peer adds that peer as a participant with access to the thread's history [T09]. A thread holds at most 64 participants; reject a send that would exceed it (`thread_full`) [R21].
4. Fan out groups at send time to one Delivery per member peer, recording the group in `Delivery.via` (duplicates: section 5.3) [SCH, R17]. Reject a send naming an archived group (`group_archived`) [R21].
5. Apply loop control (section 6) and reject synchronously.
6. Auto-spill: if `content.text` exceeds 4096 UTF-8 bytes (up to the 64 KiB request cap), store the full text as a hub-created `text/markdown` attachment, put the first ~4 KiB in `Message.content.text` with `truncated: true`, and set `metadata["vecnet.spilled"] = true` [ENV §8, EX03].
7. Reject attachment hashes not present in the blob store unless `origin_url` is set (`unknown_blob`) [SCH, R02].
8. Honour `origin` only when the caller is a connector (`ingress = connector`); ignore it otherwise [SCH, ENV §6].
9. Enforce byte caps with `payload_too_large`: `content.text` ≤ 64 KiB, metadata ≤ 8 KiB serialised, `job_request.input` ≤ 16 KiB, `job_status.output` ≤ 8 KiB, whole request body ≤ 256 KiB [SCH, ENV §8, R02].
10. Reject any `metadata` key starting with `vecnet.` (`reserved_metadata`); those keys belong to the hub [R26].

**Idempotency.** `idempotency_key` is scoped per sender. Within 24 h, the same key with the same body returns 200 with the original response and sends nothing; the same key with a different body gets 409 `idempotency_conflict` [SCH, R03].

**Jobs.** `job_request` dedupe scope is exactly (`thread_id`, `job_key`): the same pair returns the existing `job_id`; a different request body for an existing pair is `409` [ENV §9, SCH]. A `job_request` MUST name exactly one peer recipient; a group recipient gets 422 `job_single_recipient` [R19].

**Rejections** are synchronous 4xx with a reason code [SCH]:

| Reason code | Status | Cause |
|---|---|---|
| `max_depth_exceeded` | 422 [T16] | Reply depth would exceed `max_depth` |
| `thread_budget_exhausted` | 422 [T16] | Thread budget is 0 |
| `expired` | 422 [T16] | Parent `expires_at` has passed |
| `scope_denied` | 403 [R02] | Send scopes forbid a recipient |
| `unknown_recipient` | 404 [R02] | Id not enrolled / not a group |
| `name_not_an_address` | 422 [R02] | Display name in `recipients` |
| `unknown_blob` | 422 [R02] | Attachment hash not uploaded and no `origin_url` |
| `payload_too_large` | 413 [R02] | A byte cap in step 9 exceeded |
| `reserved_metadata` | 422 [R26] | Sender-supplied `vecnet.*` metadata key |
| `idempotency_conflict` | 409 [R03] | Same `idempotency_key`, different body |
| `group_archived` | 410 [R21] | A recipient group is archived; the operator can unarchive it |
| `thread_full` | 422 [R21] | Send would take the thread past 64 participants |
| `job_single_recipient` | 422 [R19] | `job_request` not addressed to exactly one peer |
| *(job conflict)* | 409 [SCH] | Same (`thread_id`, `job_key`), different body |
| `approval_closed` | 422 [R23] | `approval_response` to an approval already closed (answered or expired) |
| `invalid_option` | 422 [R23] | `approval_response.decision` not in the request's `options` |

**Response.** 202 with [R04]:

```json
{"message_id": "msg_…", "thread_id": "thr_…",
 "deliveries": [{"delivery_id": "dly_…", "recipient": "peer_…", "via": null, "state": "queued"}],
 "job_id": "job_…", "spilled": false}
```

`job_id` is present only when the send created a job. Every Delivery of that Message echoes `message.job_request` unchanged and carries the hub-set `metadata["vecnet.job_id"]` [R04]. An idempotent replay returns this same body with 200 [R03].

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
| 409 | Cursor from unknown epoch or below retention; the error `details` name the oldest valid cursor | Re-sync thread history explicitly, then resume |

A principal credential receives events for all its peers by default; `?peer=<peer_id>` narrows the stream to one of them [ENV, R05]. The acked cursor is per stream owner and scope: one for the principal-wide stream, one per narrowed peer. Several consumers MAY connect concurrently to the same owner and scope; they receive the same events and share one acked cursor. There are no per-consumer cursors [R05].

Sending a frame is not a receipt; the hub's undelivered and dead-letter timers key off the acked cursor [SSE].

### 2.4 `POST /v1/ack`

Body `{cursor}`. Acknowledges every entry of the caller's log up to and including `cursor`. Every `delivery` entry at or before the cursor that is not yet `accepted` becomes `accepted` (this is how `hold` deliveries reach `accepted`) [T10]. MCP `vecnet_ack(cursor)` is the same operation [T06].

### 2.5 `POST /v1/receipts`

Body `{delivery_id, state, note?}`; `state` is `processed`, `failed`, or `read` (a human saw it; optional). Returns 204 [SCH, R06]. Only the Delivery's recipient, or its principal, MAY post; the hub fills the `Receipt`'s remaining fields [T10, R06]. Clients never post `accepted`: the hub records it from a push 2xx or a cursor ack [R06]. The hub appends a `receipt` Event to the sender's log [ENV]. MCP `vecnet_receipt(delivery_id, state, note?)` is the same operation [R06].

### 2.6 `POST /v1/presence`

Body `{state: idle|working|blocked|offline, at, capabilities?, quota?, location?, note?}` [T14]. `quota.weekly_remaining_pct` (0–100) is the standard key that routers and `wake_idle` read [SCH]. Stored in the **Directory**; fanned out as `presence` Events per section 8. MCP `vecnet_presence` is the same operation [T14].

### 2.7 Blobs

`PUT /v1/blobs`: bearer auth by any enrolled peer, under the same bearer used to send [ENV §8]. The hub streams, hashes (sha256), dedupes by hash, and returns `{sha256, size}` [T13]. Cap 25 MiB per blob; daily per-peer upload quota, default 500 MiB [T13]. Uploader MAY set a TTL with `?ttl=<seconds>`, minimum 1 h; it applies only while the blob is unreferenced (section 11) [T13, R07]. Over the per-blob cap → 413 `blob_too_large`; over the daily quota → 429 `upload_quota_exceeded` with `Retry-After` [R07].

`GET /v1/blobs/{sha256}`: authorised by either the `?t=` token in a Delivery's `blob_url` (scoped to recipient + sha256, valid 7 days), or the bearer of a peer that participates in a thread referencing the blob, or the operator UI [T13, EX02]. A purged blob returns 410 `blob_purged` [R07]. Details in section 11.

### 2.8 Threads

`GET /v1/threads?participant=me|<peer_id>&group=<grp_id>&since=<timestamp>&limit=<n≤200>`: threads the caller participates in. Returns `{threads: [ThreadSummary…], next}`; `next` is an opaque paging token [R08].

`GET /v1/threads/{id}/messages?after=<message_id>&limit=<n≤200>`: history for late joiners and cursor re-sync [ENV §5]. `after` is a `msg_` id, exclusive; omitted means from the thread's start. Returns `{messages: [Message…], next_after}`; `next_after` is the `msg_` id to pass next, or `null` at the end [R08]. Participants only; others get 403 `not_participant` [T09, R08, R24].

### 2.9 Groups *(proposed paths)*

`POST /v1/groups` creates an Ad hoc group (`grp_`) with members the caller is allowed to message; `POST /v1/groups/{id}/members` lets any member add others under the same scopes; `GET /v1/groups` lists the caller's groups [T09, T11]. A principal credential names the acting peer with `?as=<peer_id>` on the two `POST`s, as on `POST /v1/presence`; without it the hub answers 422 `as_required`. Teams are operator-only (section 2.12).

### 2.10 Directory: `GET /v1/peers` *(proposed path)*, `PATCH /v1/peers/me`

`GET /v1/peers` returns every peer's public directory fields (section 8) to every peer; scopes do not gate it [T14, R24]. Credential status and routes are operator-only [T14].

`PATCH /v1/peers/me` with `{"subscribe_presence": "all"|"teams"|"none"}` sets the caller's presence fan-out (section 8) [R22].

### 2.11 `POST /v1/ingress/{src_id}`

Per-source inbound URL; see section 10. Accepted → 202 `{message_id}`; a redelivery returns the original `message_id`; bad signature → 401 `bad_signature`; oversize body → 413 `payload_too_large` [R09].

### 2.12 Enrolment and admin *(proposed paths)*

`POST /v1/enrol`: body carries the one-time enrolment token printed by `vecnet peer add`; returns the peer's long-lived secret, once [T11].

Admin endpoints require the operator credential [T17]. Each mirrors a CLI command [T17] or an operator power stated in a ticket:

| Proposed path | CLI / power | Source |
|---|---|---|
| `POST /v1/admin/principals` | `principal add --name` → `prn_` id + its credential | [R10] |
| `POST /v1/admin/peers` | `peer add --kind --name [--principal]` → peer id + one-time token | [T11, T17, R10] |
| `POST /v1/admin/peers/{id}/move` | `peer move <peer> --principal <prn_>` | [R10] |
| `POST /v1/admin/peers/{id}/rotate` | `peer rotate` | [T11, T17] |
| `PUT /v1/admin/peers/{id}/scopes` | `peer scope` (allow/deny lists) | [T11, T17] |
| `POST /v1/admin/sources` | `source add --kind --name --route` → ingress URL + secret | [T12] |
| `POST /v1/admin/routes` | `route add` (push route URL; issues `whsec_`) | [T11, T17] |
| `POST /v1/admin/teams`, `…/teams/{id}/members` | operator-managed teams | [T09] |
| `POST /v1/admin/groups/{id}/unarchive` | unarchive an ad hoc group | [R21] |
| `GET /v1/admin/dead-letters`, `POST …/{dly}/retry`, `POST …/{dly}/discard` | `dead-letters list|retry|discard` | [T10, T17, R18] |
| `PATCH /v1/admin/threads/{id}` | raise thread budget; rename | [T16, EX06] |
| `POST /v1/admin/blobs/{sha256}/purge` | purge bytes, keep tombstone | [T13] |
| `POST /v1/admin/dot-links` | `peer link <peer>` → 8-character link code, valid 10 min, binding a ChatGPT principal to a pre-created dot peer | [T11, R15] |
| `PUT /v1/admin/notify` | operator notification webhooks and filters; each route gets its own `whsec_` | [T15, R23] |
| `GET /v1/admin/audit` | audit log | [T11] |

**Principals.** `vecnet principal add --name <n>` creates a principal and prints its `prn_` id and credential. `vecnet peer add … --principal <prn_>` enrols a peer under it. `peer add` without `--principal` creates an implicit single-peer principal with the peer's display name. Only the operator reassigns a peer, with `vecnet peer move <peer> --principal <prn_>` [R10].

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

A push body MUST NOT exceed 256 KiB; a Delivery with 16 attachment references and 4 KiB of text fits well inside it [R11].

### 3.2 Verification challenge

Before a route goes live the hub sends `{"type":"verification","challenge":"<43-char base64url>"}`, signed as above; the receiver verifies the signature and answers 2xx with `{"challenge":"<same>"}` [EX07, W2M §2e].

### 3.3 Response handling

| Receiver answers | Hub does | Source |
|---|---|---|
| 2xx | Delivery → `accepted`; receipt `state=accepted`, `by=hub` | [T10, EX07] |
| 408, 429, 5xx, network error | Retry: backoff `min(1 h, 2^attempt s) + jitter`, honour `Retry-After` (w2m caps it at 1 h), 8 attempts, then `dead_letter` | [T10, W2M §4] |
| 409 | Identity conflict (same `delivery_id`, different bytes): terminal `failed`, never retried, operator alerted | [T10, R11] |
| 410, 413, any other 4xx | Terminal: `failed` → `dead_letter` | [T10] |
| 3xx | Not followed; terminal `failed` | [R11] |

Retries resend identical bytes and the same `webhook-id`; only `webhook-timestamp`, `webhook-signature` and `X-Vecnet-Attempt` change [EX07]. The hub ignores a response body over 4 KiB; its status still counts [R11].

### 3.4 Serialisation

Pushes to one peer within one thread are serialised: Delivery N+1 is not pushed until N is `accepted` or dead. Different threads and different peers proceed in parallel [T10].

### 3.5 Rotation

Rotating a route key issues a new `whsec_`; the old key stays valid for 24 h, and pushes during the overlap carry both signatures [T11].

---

## 4. MCP transport (`/mcp`)

### 4.1 Revisions and HTTP

The hub serves MCP **2026-07-28** (POST-only, no sessions, `server/discover`) and stays dual-era for clients that send `initialize` [SSE, DOTS]. It MUST validate `Origin` (403 if present and invalid) and authenticate every POST [SSE]. Library: official `modelcontextprotocol/go-sdk`; MCP Events is hand-rolled behind an interface [ADR1].

The hub serves exactly 2026-07-28 and 2025-11-25. An `initialize` naming an older revision is rejected with an error carrying the newest supported version [R12].

The hub MUST NOT implement MCP `Last-Event-ID` replay for any revision; the cursor tools cover resumption [SSE].

### 4.2 Inbox resource and hints

The inbox is the resource `vecnet://peer/<peer_id>/inbox`; a principal-level subscriber uses `vecnet://principal/<prn_id>/inbox` [R13]. Clients opt in with `subscriptions/listen {resourceSubscriptions:[<uri>]}` (2026-07-28) or `resources/subscribe` plus the GET stream (2025-11-25). Whenever the log grows the hub sends `notifications/resources/updated` for that URI. Notifications are **hints**: they never carry message content, and a client MUST respond to one by calling `vecnet_inbox(after=<cursor>)` [SSE]. Idle `/mcp` streams get the 15 s `: hb` comment [SSE].

### 4.3 Tools

The hub exposes exactly these tools [R13]:

| Tool | Args | Effect | Source |
|---|---|---|---|
| `vecnet_send` | `SendRequest` | Same as `POST /v1/messages` | [SCH] |
| `vecnet_reply` | `message_id`, `text`, `priority`, other `SendRequest` fields | Sugar for `vecnet_send`: derives `thread_id` and `in_reply_to` from `message_id` | [R13] |
| `vecnet_inbox` | `after?`, `limit?` | `{events, next_cursor}` from the same log as `/v1/events` | [T06] |
| `vecnet_ack` | `cursor` | Same as `POST /v1/ack` | [T06] |
| `vecnet_receipt` | `delivery_id`, `state`, `note?` | Same as `POST /v1/receipts` | [R06] |
| `vecnet_presence` | Presence body | Same as `POST /v1/presence` | [T14] |
| `vecnet_threads` | — | Same as `GET /v1/threads`; read-only, `readOnlyHint` | [DOTS, R13] |
| `vecnet_thread` | `id`, `after?` | Same as `GET /v1/threads/{id}/messages`; read-only, `readOnlyHint` | [DOTS, R13] |
| `vecnet_peers` | — | Same as `GET /v1/peers`; read-only, `readOnlyHint` | [R13] |

`vecnet_send` and `vecnet_reply` require an `idempotency_key`. Write tools describe themselves honestly as writes and MUST NOT accept a URL from the model [DOTS].

### 4.4 MCP Events profile (dots)

Implements OpenAI's ChatGPT profile in webhook mode [T02, DOTS]:

- **Capability:** advertise top-level `capabilities.events` and `capabilities.extensions["io.modelcontextprotocol/events"]`. Keep error codes behind one constant [DOTS §3].
- **`events/list`:** `events[]` of `{name, description, delivery:["webhook"], inputSchema, payloadSchema}` plus `nextCursor`. Catalogue example: `vecnet.message {peer_id?, thread_id?, types?}` with strict schemas [DOTS, R14].
- **`events/subscribe`:** params `name`, `arguments`, `delivery:{mode:"webhook", url, secret}`, `cursor`, optional `ttlMs`; result `{id, refreshBefore, cursor, truncated}`. Store subscriptions durably keyed by `(principal, url, name, arguments)`; `id` is a routing handle, not a credential. Clamp TTL to ≤ 24 h and ≤ the access-token expiry. Return a replay cursor; set `truncated` honestly. `secret` MUST be `whsec_` + base64 decoding to 24–64 bytes [DOTS §2].
- **`events/unsubscribe`:** idempotent, authorised against the connected account [DOTS §2].
- **Challenge:** before first delivery POST `{"type":"verification","challenge":"<single-use>"}` signed with the subscription secret plus `X-MCP-Subscription-Id`; the callback echoes `{"challenge":…}`, compared in constant time. Failure: JSON-RPC `-32015` `CallbackEndpointError` with `data.reason` (`challenge_failed`, `timeout`) [DOTS §2].
- **Push:** one event per POST, ≤ 256 KiB: `{eventId, name, timestamp, data, cursor}`; headers `webhook-id` (= `eventId`), `webhook-timestamp`, `webhook-signature`, `X-MCP-Subscription-Id`. Egress MUST resolve and pin DNS to public IPs only and MUST NOT follow redirects, including for challenges. Re-check authorisation before each send. Retry with backoff keeping `eventId`; never retry 410 or 413. On secret refresh, dual-sign for a short window [DOTS §2, §5].
- **Refresh:** ChatGPT calls `events/subscribe` again before `refreshBefore` with the last cursor; resume after it [DOTS §2].
- **Outage:** while no live subscription exists, queue durably and drain from the cursor on re-subscribe [DOTS].
- **Payload:** envelope metadata plus content, or a preview plus a blob reference when over the cap; never model instructions; label content untrusted [DOTS].
- **Mapping:** `eventId` is the `delivery_id` for a `delivery` Event and the log cursor string otherwise; the Events `cursor` is the log cursor [R14].
- **Types:** a dot is pushed `delivery` Events by default, and `receipt`, `thread_updated` or `approval_expired` only when its subscribe `arguments` list them in `types`. No other type is pushed to a dot [R14]. Priority still gates `delivery` pushes (section 5.2).
- **Tunnel:** OpenAI's Secure MCP Tunnel is unsupported in v1; dots reach the hub over public HTTPS [R14].

### 4.5 OAuth 2.1

For dots the hub is both an OAuth **resource server** and its own minimal **authorization server** [T11]:

- PKCE `S256` advertised and required; CIMD preferred, Dynamic Client Registration (RFC 7591) supported [DOTS §4, R15].
- RFC 8414 authorization-server metadata [R15].
- RFC 9728 protected-resource metadata at `/.well-known/oauth-protected-resource`; ChatGPT sends `resource=`; validate `aud`, issuer, expiry and scope on every call [DOTS §4, R15].
- Access tokens live 1 h; refresh tokens live 30 d and rotate on every use [R15].
- Redirect URIs `https://chatgpt.com/connector_platform_oauth_redirect` and `https://chatgpt.com/connector/oauth/{callback_id}` [DOTS §4].
- No machine-to-machine grants exist for ChatGPT [DOTS §4].

**Principal binding:** the operator pre-creates a dot peer; on first login the ChatGPT principal is bound to it via a one-time link code; one principal → one dot peer for now, with a binding model that allows several peers per principal later [T11]. The operator runs `vecnet peer link <peer>`, which prints an 8-character code valid for 10 minutes; the consent page on the principal's first login asks for it and binds the principal to that peer [R15].

**Mixed auth.** `/mcp` accepts either a vecnet bearer (peer or principal secret) or an OAuth access token on the same endpoint. A Grok Bot reaches `/mcp` with a peer bearer configured in the Bot's MCP settings [SSE, R15].

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
| `wake_idle` | Push if recipient presence is `idle` or `unknown`, else hold; push a held Delivery once when presence later becomes `idle` or stale [SCH, R16] | Appended to log | As `wake` if idle/unknown, else hold | Inbox only [T15] |
| `hold` | Never pushed; log only; `accepted` on cursor ack [T10] | Appended to log | Do not POST; the Bot's MCP tool picks it up on its next run [ENV §3, T01] | Inbox only [T15] |

Priority SHOULD NOT change dedupe or retry semantics [W2M §5].

**`wake_idle` and stale presence.** `unknown`, including stale presence rendered as `unknown` (section 8), counts as idle [R16].

**Where priority applies.** Priority affects only push transports (push routes, Grok Bot, dot Events) and the operator's notification webhooks. SSE, long-poll and MCP pull consumers, and `vecnet watch`, see every Event as it is appended, whatever its priority [R16]. For a dot, `hold` and a held `wake_idle` mean no Events push; the Delivery stays readable through `vecnet_inbox` [R16].

### 5.3 Dedupe and idempotency

- **Recipient side:** `delivery_id` is the dedupe key. Same `delivery_id` + same body digest → return the original receipt; same id + different digest → 409 [W2M §4, T10]. Receivers MUST durably commit before answering 2xx [W2M §4].
- **Sender side:** `idempotency_key` per sender, 24 h window (section 2.2).
- **One Delivery per (`message_id`, recipient)**, however many paths reach the peer. `Delivery.via` names the first group that resolved to the recipient; any other groups are listed in `Delivery.also_via` (a Delivery-level field, so the Message stays byte-identical) [R17].

### 5.4 Ordering

Each stream owner's log is appended in message creation order with a strictly increasing seq [T10, SSE]. Pushes are serialised per (peer, thread) [T10]. Events MAY arrive out of order on the dots Events path [DOTS §2].

### 5.5 Dead letters

The sender gets a `receipt` with `state: dead_letter`. The operator sees every dead letter in the dashboard and CLI and may retry or discard; nothing is auto-deleted [T10]. An operator retry keeps the same `delivery_id`, continues the `attempt` counter, and grants a fresh budget of 8 attempts [R18].

### 5.6 Jobs

`kind: message` with `job_request` creates a job; the hub assigns `job_id` [EX05]. Execution state travels only as `kind: job_status` messages; a 2xx on the original Delivery never implies it [T04, EX05]. `sequence` strictly increases per job [SCH]; a report with a stale or equal `sequence` gets 409 `stale_sequence` and is not delivered [R19]. The hub enforces webhook2mcp's transition table and pins the first reporter [T04, ENV §9]:

```text
requested              -> accepted | running | succeeded | failed | cancelled
accepted               -> running | succeeded | failed | cancelled
running                -> succeeded | failed | cancelled
cancellation_requested -> succeeded | failed | cancelled
succeeded / failed / cancelled -> terminal
```

**Job states** [R19]. The hub records `requested` when it creates the job; reported `jobStatus.state` values (`accepted`, `running`, `succeeded`, `failed`, `cancelled`) then move it through the table. When the requester posts a `job_status` with `state: cancelled` while the job is not terminal, the job enters `cancellation_requested` and the recipient reports the final state. A job has exactly one peer recipient (section 2.2). Only that recipient, or its principal, MAY report status; any other reporter gets 403.

---

## 6. Loop control and thread budgets

The hub fills `loop` on every Message [ENV]:

| Field | Rule | Default |
|---|---|---|
| `depth` | 0 for a fresh message; a reply is parent.depth + 1. Only `in_reply_to` makes a send a reply: a send into an existing thread without it is fresh (depth 0, its own `expires_at`, `sender_chain` = [sender]) | — |
| `max_depth` | Inherited down a reply chain; any sender MAY lower it for its own message; a reply MUST NOT raise it; range 1–64 | 16 |
| `expires_at` | Wall-clock TTL; sender MAY lower it; a reply MUST NOT raise an inherited value | now + 24 h |
| `sender_chain` | Last 32 originators (peers or sources), oldest first: parent's chain + sender | — |

[T16, SCH, ENV §2]

**Enforcement.** The hub rejects a send synchronously with 422 and a reason code when the reply's depth would exceed `max_depth` (`max_depth_exceeded`), when `expires_at` has passed (`expired`), or when the thread budget is exhausted (`thread_budget_exhausted`) [T16]. Each cut is an audit entry and a dashboard counter; a budget cut also emits `thread_updated` [T16].

**Thread budget.** Default 500 messages per thread. `budget_remaining` lives on the thread and rides in every `Delivery.thread`; `null` means unlimited. Only the operator raises a budget [T16, SCH].

**What counts.** Approval requests, approval responses and `job_status` messages are Messages: each counts against the thread budget and increments depth like any reply. Hub-posted messages, such as the expiry `approval_response`, count against neither [R20].

**Client breaker (connector recommendation).** When `sender_chain` shows the same two peers alternating for N consecutive entries (default 12) within a window (default 10 min), the connector SHOULD stop auto-replying and ask the operator via `approval_request` before continuing. Both values are configurable. The hub's thread budget is the hard stop regardless [T16].

---

## 7. Threads, teams, ad hoc groups

- **Threads** are flat and first-class; a Message belongs to exactly one thread [GLOSSARY]. Creation is implicit (section 2.2). `thread_title` applies only when starting a thread [SCH]; the operator may rename (emits `thread_updated`) [EX06].
- **Replies never fork.** A reply stays in its thread. A new topic is a new thread, optionally with `in_reply_to` pointing at a message in another thread [T09].
- **Teams** (`team_`) are operator-managed, named and stable [T09].
- **Ad hoc groups** (`grp_`) are created by any peer with members it may message; members may add others under the same scopes; the operator sees every group; a group auto-archives after 30 days idle [T09].
- Neither a team nor a group implies a thread: a message to a group starts or continues a thread like any recipient set [T09].
- **External mapping.** Connectors own the external-conversation ↔ `thread_id` mapping. The hub stores `origin` on the Message and echoes it on Deliveries, nothing more [T09, ENV §6]. Inbound sources are the exception: the hub owns their mapping (section 10) [R09].
- **Archived groups.** A send naming an archived group gets 410 `group_archived`; the operator can unarchive it [R21].
- **`ThreadSummary.group`** is the group named as the first recipient when the thread was created, else `null` [R21].
- **Participant cap.** A thread holds at most 64 participants; a send that would exceed it gets 422 `thread_full` [R21].

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

**Fan-out.** Presence Events go only to peers that opt in: `subscribe_presence: all | teams | none`, default `teams` [T14]. A peer sets its own value with `PATCH /v1/peers/me` or `vecnet presence subscribe <all|teams|none>` [R22].

**Directory events.** `directory_updated` Events go to every peer, with `data` `{peer_id, change: "enrolled"|"updated"|"removed", fields: [names]}` [R22].

---

## 9. Approvals (v1) and escalation (phase 2)

**Approval lifecycle (v1)** [T15, SCH]:

1. A sender posts `kind: approval_request` with `approval_request {approval_id, question, options (2–8, default yes/no), expires_at, escalate_to_operator?}`.
2. The first valid `approval_response {approval_id, decision, reason?}` from an addressed recipient closes it. A response to a closed approval (answered or expired) gets 422 `approval_closed`; a `decision` not in `options` gets 422 `invalid_option` [R23].
3. At `expires_at` the hub emits an `approval_expired` Event `{approval_id, thread_id}` to the thread's participants and posts an `approval_response` with `decision: "expired"` and `sender: "hub"` into the thread [R23]. That message counts against neither budget nor depth (section 6) [R20].

**Operator notification (v1, minimal).** A `wake` Message addressed to the operator peer fires the operator's notification webhooks; `wake_idle` and `hold` land in the inbox only. Config: one or more URLs, each with a filter `wake | approvals | dead_letters | all` [T15]. Each notification route has its own `whsec_`; the payload is the Event JSON, signed with Standard Webhooks as in section 3.1 using that route's key [R23].

**Escalation (phase 2, not a v1 build item).** When an approval addressed to a non-operator is still open at the halfway point of its window and `escalate_to_operator` is true (schema default true), the hub forwards a copy to the operator with `wake` priority [T15].

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
- **Priority:** source Messages default to `wake_idle`; the operator MAY configure another priority per source [R09].
- **Redelivery dedupe:** key (`src_` id, provider delivery id), for GitHub `X-GitHub-Delivery`. A repeat creates nothing and returns the original `message_id` [R09].
- **Threads:** the hub keeps one thread per (`src_` id, `origin.thread_id`), created on the first event and titled from the first payload [R09].
- **Responses:** section 2.11 [R09].

---

## 11. Blob store

- **Layout:** `/data/blobs/sha256/<aa>/<bb>/<hex>`, written to a temp file on the same filesystem, fsynced, renamed; metadata in SQLite [ADR1].
- **Upload:** section 2.7. Connectors upload on behalf of Grok Bots and dots, which cannot reach the store [T13].
- **Read:** `blob_url` on a Delivery carries a token scoped to (recipient, sha256), valid 7 days. Without it, a blob is readable by any participant in a thread referencing it, with its bearer, and by the operator in the UI [T13].
- **Retention:** a blob referenced by a retained Message lives as long as the Message (indefinitely unless purged). Unreferenced uploads are garbage-collected after 24 h. Uploader TTL is optional and applies only while the blob is unreferenced: a blob referenced by any retained Message is pinned regardless of TTL [T13, R07]. Operator purge removes bytes and leaves a tombstone row; a later read returns 410 `blob_purged` [T13, R07].
- **Reference-only attachments** (`origin_url`, no hosted bytes) carry no `blob_url`; the hub never fetches `origin_url` [T13, SCH].

---

## 12. Auth, enrolment, principals, scopes, audit, logging

- **Enrolment.** The operator runs `vecnet peer add --kind <kind> --name <name>`; it prints the peer id and a one-time enrolment token; the connector exchanges the token once for the long-lived secret [T11]. No peer exists without enrolment [GLOSSARY]. Every peer belongs to a principal, explicit or implicit (section 2.12) [R10].
- **Credentials per transport** [T11]:

| Transport | Credential |
|---|---|
| HTTP send, `/v1/events`, blobs, `/mcp` (non-dot) | Per-peer (or per-principal) bearer secret |
| Push route | Per-route `whsec_`; 24 h dual-signed rotation |
| Dots `/mcp` | OAuth 2.1 PKCE access token of the bound ChatGPT principal |
| Grok Bot `/mcp` | Peer bearer in the Bot's MCP settings [R15] |
| Dots event push | The `whsec_` ChatGPT supplies at `events/subscribe` |
| Admin | Operator credential |

- **Dots never hold a vecnet secret** [T11].
- **CLI profiles:** `~/.config/vecnet/config.toml` holds named profiles, each a hub URL plus a peer, principal or operator credential; `--net` selects; `--as <peer>` picks a principal's peer [T17]. One peer per long-lived agent identity; sessions reuse that identity's stored credential [T17].
- **Send scopes:** default allow-all among enrolled peers and teams; per-peer allow/deny lists set by the operator; ad hoc group creation respects the same scopes [T11]. Scopes gate sends and group creation only: every peer MAY read the directory, and thread history is readable by participants only [R24].
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
- [ ] Body is the Delivery, so it carries `delivery_id`, `message_id`, `thread_id` and `origin`, because the Bot gets only a `runUuid` [R25].
- [ ] Treat 200 `{"success":true}` as "run started", never as a reply or receipt of processing.
- [ ] Durable outbox: retry network errors, 5xx and rate-limit 400s with backoff; 401 and "disabled" 400 are terminal and alert.
- [ ] Dedupe sender-side; budget against 200 runs/h per routine, 300/h per account and the weekly pool; batch and debounce.
- [ ] Implement `hold` hub-side: never POST; the Bot pulls via a vecnet MCP tool on its next run [T01, ENV §3].
- [ ] The Bot pulls and replies through `/mcp` with a peer bearer (section 4.5); its reply echoes the vecnet ids [R15, R25].
- [ ] Treat every inbound body as untrusted.

**Placement.** Grok Bot push is an in-hub push route of kind `grokbot`: the hub POSTs the Delivery with the bearer key to the routine URL; there is no separate connector process [R25]. `origin` stays `null` for Grok Bot traffic, because the vecnet ids ride in the body and the Bot echoes them [R25].

### 13.4 Dots [T02, T11, T19, DOTS]

- [ ] Section 4.4 Events profile and 4.5 OAuth, in the hub.
- [ ] Model the dot as a subscription-backed peer: receives only while a live subscription exists; always sends via tools.
- [ ] Surface subscription health (expiry, last refresh, last delivery status) to the operator.
- [ ] Enrolment script: the operator tells the dot to subscribe to `vecnet.message` for its peer and reply via the send tool; no API creates the subscription.
- [ ] Unrecorded but non-blocking: where an event lands in the dot UI, batching, confirmation prompts [T19].

### 13.5 openclaw [T03, OC]

- [ ] Channel plugin (`id: "vecnet"`) shipped as an extension; config per account: hub URL, peer id, credential via env/SecretRef, allowlist of peers/teams.
- [ ] `gateway.startAccount` dials out over SSE (or long-poll), resumes from the acked cursor, reconnects with backoff.
- [ ] Inbound: dedupe on `delivery_id`; check sender allowlist; `resolveAgentRoute`; `finalizeInboundContext`; dispatch; ack the cursor (the hub records `accepted`) and post `processed` once the dispatch completes, even when the turn produces no reply [R06, R25].
- [ ] Replies via `deliver` POST to `/v1/messages` with `in_reply_to` and the same thread; upload `mediaUrl` attachments to the blob store first.
- [ ] Agent-initiated sends via `outbound.sendText` with `vecnet:<id>` targets; `textChunkLimit` = 4 KiB.
- [ ] Back `directory.listPeers`/`listGroups` with the hub directory.
- [ ] Propagate loop control into replies; do not copy the nostr plugin's inbound path.
- [ ] Keep the hub credential scoped to this peer; never give the hub a gateway operator token.
- [ ] One openclaw session per vecnet thread [R25].
- [ ] Mention gating is a plugin config option, default off: every Delivery dispatches [R25].

### 13.6 pag-chat-style host (principal) [ENV]

- [ ] Hold one principal credential; send with `as`; open one `/v1/events` stream or one push route for all its peers.
- [ ] Route each Delivery by `recipient`; route receipts by `thread_id`.
- [ ] Use `wake_idle` for ordinary peer-to-peer requests.

### 13.7 `vecnet` CLI [T17, ADR1]

- [ ] Single static Go binary per OS/arch.
- [ ] Commands: `send <peer|group> "text" [--thread] [--reply-to] [--wake|--wake-idle|--hold] [--attach FILE]`, `reply <message_id> "text"`, `inbox [--since CURSOR] [--ack]`, `ack <cursor>`, `watch [--for-monitor]`, `threads`, `thread <id> [--after MESSAGE_ID]`, `peers`, `groups create|add|list`, `presence set <state>`, `presence subscribe <all|teams|none>`; admin `principal add`, `peer add [--principal]|move|link|rotate|scope`, `source add`, `route add`, `dead-letters list|retry|discard` [R08, R10, R15, R22].
- [ ] `watch --for-monitor` holds the SSE stream and prints one compact line per event.
- [ ] MAY act as a stdio channel server for Claude Code, re-emitting `/v1/events` as `notifications/claude/channel` [SSE].

---

## 14. Security invariants

1. **Nothing on the bus conveys authority.** `ingress` is provenance only [SCH]. A recipient MUST NOT grant a Message more trust because of its `ingress`, `origin`, `metadata` or sender kind.
2. **Bodies are untrusted.** Every recipient treats content, attachments and metadata as data [SCH, GB]. Event payloads to dots carry no instructions [DOTS].
3. **Sender comes from authentication.** `sender` is set from the credential plus `as`, or is `"hub"` for hub-authored messages; a body field never sets it [ENV §7, R23]. `origin` from a non-connector body is ignored [SCH].
4. **Names are never addresses.** Recipients are ids; a display name is a schema error, never a lookup [ENV §7].
5. **`vecnet.*` metadata keys belong to the hub** [SCH]. A SendRequest carrying any `vecnet.*` key gets 422 `reserved_metadata` [R26].
6. **Secrets stay out of URLs, logs and audit rows** [SSE, T11]. Blob read tokens are scoped to one recipient and one hash [T13].
7. **Push egress to dots is DNS-pinned to public IPs with no redirects** [DOTS].
8. **Dots never hold a vecnet secret** [T11].

---

## 15. Resolved items

The 27 OPEN items of the first draft are decided in `docs/protocol/OPEN-ITEMS-RESOLVED.md` and folded into the sections above; each rule cites its item as `[Rnn]`. Item 27, source drift in example 04 and `GLOSSARY.md`, was fixed in those files.

Residual questions, decided 2026-11-01 (same delegation):

1. Reason codes: the `/v1/events` 409 is `cursor_purged` with `details.oldest_cursor`; a `job_request` whose `(thread_id, job_key)` exists with a different body is 409 `job_conflict`; a receipt or job report from a peer that is not the delivery's recipient (or its principal) is 403 `not_recipient`.
2. An `approval_response` from a peer the request did not address is 403 `not_addressed`.
3. An ingress event dropped by the source's event allowlist is 202 `{"dropped": true, "reason": "event_filtered"}`, so the provider does not redeliver it.
4. `GET /v1/threads` paginates with `?cursor=<next>`, passing back the `next` token verbatim.

Further reason codes, introduced by the core-hub implementation plan (2026-11-01):

- `invalid_body` (400): the body is not JSON or violates the schema (unknown field, wrong type, missing required field); `details.path` is the JSON pointer of the first violation.
- `unauthenticated` (401): missing, unknown or revoked bearer credential.
- `forbidden` (403): the credential may not do this, e.g. a non-operator on `/v1/admin/...`, or `as` naming a peer outside the caller's principal.
- `as_required` (422): a principal credential sent without `as`.
- `not_in_thread` (422): `in_reply_to` names a message outside the addressed thread.
- `attachment_size_mismatch` (422): an attachment's declared `size` differs from the stored blob's size.
- `cursor_mismatch` (400): `Last-Event-ID` and `?after=` are both given and differ.
- `not_your_peer` (403): `?peer=` names a peer the principal does not own.
- `invalid_state` (422): a receipt state other than `processed`, `read` or `failed`.
- `invalid_value` (422): a field value outside its allowed set or range (e.g. `subscribe_presence`).
- `unknown_thread` (404): the named thread does not exist.
- `not_member` (403): a non-member tried to change an ad hoc group.
- `unknown_approval` (404): an `approval_response` names no approval of this thread.
- `unknown_job` (404): a `job_status` names no existing job.
- `invalid_transition` (422): a `job_status` state not allowed from the job's current state (section 5.6).

## 16. Amendments from pag-chat's review (2026-11-01)

These three rules supersede the sections they cite. Each answers a decisive error pag-chat identified; the coordinating agent accepted them under Max's delegation and flagged amendment 3 to Max as a product-default change.

### 16.1 Operator replies reset depth [A1, supersedes §6 and R20 in part]

Depth exists to stop unsupervised bot-to-bot loops. A reply sent by a peer of kind `operator` MUST set `depth` to 0 for its subtree; replies below it count from 0 again. `sender_chain` MUST keep recording every originator regardless. The default `max_depth` is **32** (was 16). The thread budget (default 500) remains the hard stop and is unaffected by sender kind. Approval requests, approval responses and job status messages still count against depth and budget as in R20, except where the sender is the operator.

### 16.2 Per-peer rate limits [A2, extends §2.2, §6 and §12]

The hub MUST enforce per-peer limits on `POST /v1/messages` and the MCP send tools: default **60 sends per minute** and **20 new threads per hour** per peer (a principal's peers are limited individually). Exceeding either returns **429 `rate_limited`** with `Retry-After`. Limits are operator-adjustable per peer (`vecnet peer limits <peer> --sends-per-min N --threads-per-hour N`), and every rejection is an audit entry and a dashboard counter like a loop cut. Inbound webhook sources have their own per-source limit with the same defaults.

### 16.3 History access and participant locking [A3, supersedes §2.2 step 3, §7 and §11 in part]

`ThreadSummary` gains `history_access` (`from_join` | `full`, default **`from_join`**) and `locked_participants` (boolean, default false), both settable by the thread creator at creation (the `SendRequest` fields of the same names, honoured only on the send that creates the thread, like `thread_title`) and by the operator at any time.

- Under `from_join`, a participant added after the thread was created MAY read only messages appended after its join (`GET /v1/threads/{id}/messages` and `vecnet_thread` filter accordingly), and MAY read a blob through its bearer only if a message visible to it references that blob. Per-recipient `blob_url` tokens on its own Deliveries are unaffected.
- Under `full`, every participant reads the whole thread.
- When `locked_participants` is true, a send whose recipients would add a participant is rejected **403 `participants_locked`** unless the sender is the creator or the operator.
- Scopes (§12) continue to govern who may message whom; this amendment governs what history travels with an add.

### 16.4 Operator note [no rule change]

The dashboard SHOULD show, per peer, any thread whose push queue is blocked on a delivery under retry ("thread blocked on delivery N"), since per-(peer, thread) serialisation can hold a thread for hours while others flow.
