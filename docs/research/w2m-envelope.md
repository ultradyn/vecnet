# webhook2mcp (w2m) envelope, job report and signing

Researched 2026-11-01 for ticket 04. All facts come from `~/src/w2m-ops-admin` at commit `639f742` (2026-10-26). Paths below are relative to that repo.

## In one paragraph

The bridge POSTs **one signed JSON object** to a receiver URL that an admin configured. The object has **twelve required fields**. Fields that don't apply are JSON `null`, never omitted. An optional `job_request` rides along only when the delivery creates a job. A **2xx means "durably received", nothing more**. Execution state travels the other way, as a separate signed `job.status` webhook whose `sequence` must strictly increase. Signing is plain **Standard Webhooks**: `webhook-id`, `webhook-timestamp` and `webhook-signature: v1,<b64 HMAC-SHA256>`.

## 1. The envelope schema (verbatim excerpt)

Source: `examples/grokbot-envelope.schema.json` (JSON Schema 2020-12, `$id` `urn:webhook2mcp:grokbot-envelope:1`).

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["schema_version", "message_id", "delivery_id", "thread_id", "original_thread_id",
               "in_reply_to_message_id", "original_event_id", "sender_member_id", "recipient_member_id",
               "job_refs", "content", "created_at"],
  "properties": {
    "schema_version":         { "const": 1 },
    "message_id":             { "$ref": "#/$defs/objectId" },
    "delivery_id":            { "$ref": "#/$defs/eventId" },
    "thread_id":              { "$ref": "#/$defs/objectId" },
    "original_thread_id":     { "type": ["string", "null"], "maxLength": 192 },
    "in_reply_to_message_id": { "anyOf": [{ "$ref": "#/$defs/objectId" }, { "type": "null" }] },
    "original_event_id":      { "anyOf": [{ "$ref": "#/$defs/eventId" }, { "type": "null" }] },
    "sender_member_id":       { "$ref": "#/$defs/objectId" },
    "recipient_member_id":    { "$ref": "#/$defs/objectId" },
    "job_refs": { "type": "array", "maxItems": 32, "items": {
        "type": "object", "additionalProperties": false, "required": ["id", "key", "repository_id"],
        "properties": {
          "id":            { "$ref": "#/$defs/objectId" },
          "key":           { "type": "string", "pattern": "^[a-z0-9][a-z0-9._-]{0,63}$" },
          "repository_id": { "$ref": "#/$defs/objectId" } } } },
    "content":    { "type": "string", "minLength": 1, "maxLength": 65536 },
    "created_at": { "type": "string", "format": "date-time",
                    "pattern": "^\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{3}Z$" },
    "job_request": { "type": "object", "additionalProperties": false,
      "required": ["version", "repository_id", "route_id", "recipient_member_id", "operation", "instructions", "input"],
      "properties": {
        "version":             { "const": 1 },
        "repository_id":       { "$ref": "#/$defs/objectId" },
        "route_id":            { "$ref": "#/$defs/objectId" },
        "recipient_member_id": { "$ref": "#/$defs/objectId" },
        "operation":           { "type": "string", "pattern": "^[a-z][a-z0-9_.-]{0,63}$" },
        "instructions":        { "type": "string", "minLength": 1, "maxLength": 65536 },
        "input": {} } }
  },
  "allOf": [{ "if": { "required": ["job_request"] },
              "then": { "properties": { "job_refs": { "minItems": 1, "maxItems": 1 },
                                        "content":  { "maxLength": 1024 } } } }],
  "$defs": {
    "objectId": { "type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_-]{0,191}$" },
    "eventId":  { "type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$" }
  }
}
```

Rules that JSON Schema cannot express. Source: `docs/grokbot-contract.md:11,46,89`.
- The whole POST body is at most 96 KiB of exact bytes. `content` is at most 64 KiB of UTF-8, which is bytes, not the characters that `maxLength` counts.
- `job_request` is at most 64 KiB, depth 32 and 4,096 nodes.
- `job_refs[0].repository_id` must equal `job_request.repository_id`. `job_request.recipient_member_id` must equal the envelope's `recipient_member_id`.
- Duplicate JSON keys are rejected, and the recipient is checked against the receiver's own config.

What each id means. Source: `docs/grokbot-contract.md:71-73`.

| Field | Meaning |
|---|---|
| `message_id` | One logical message. It stays the same across retries and across recipients. |
| `delivery_id` | This message to this route. It equals `webhook-id` and is the dedupe key. |
| `thread_id` | The bridge's internal conversation. One thread can hold several jobs. |
| `original_thread_id` | The external thread id scoped to the source, or `null`. |
| `in_reply_to_message_id` | The direct parent message, or `null`. |
| `original_event_id` | The authenticated inbound event that caused this message, or `null`. |
| `job_refs[].id` / `.key` | The job id is separate from the message id. The key is immutable per account and repository, and the same key with the same intent returns the existing job. |

## 2. Example payloads

**(a) Ordinary message.** It has no `job_request` and `job_refs` is empty. This is adapted from `docs/grokbot-contract.md:48-69`, with the job ref removed because the contract says ordinary messages carry none.

```json
{
  "schema_version": 1,
  "message_id": "msg_example_0001",
  "delivery_id": "dly_example_0001",
  "thread_id": "thr_example_0001",
  "original_thread_id": null,
  "in_reply_to_message_id": "msg_original_0001",
  "original_event_id": "evt_original_0001",
  "sender_member_id": "mem_mira_example",
  "recipient_member_id": "mem_grokbot_example",
  "job_refs": [],
  "content": "Please review the route configuration.",
  "created_at": "2026-10-25T10:50:00.000Z"
}
```

**(b) Job-creating delivery.** This is the exact object from `scripts/sign-grokbot-example.mjs:7-16`, pretty-printed. `content` shrinks to a preview of at most 1,024 bytes. The real intent lives in `job_request`.

```json
{
  "schema_version": 1,
  "message_id": "msg_example_0001",
  "delivery_id": "dly_example_0001",
  "thread_id": "thr_example_0001",
  "original_thread_id": "grokbot-thread-example",
  "in_reply_to_message_id": "msg_original_0001",
  "original_event_id": "evt_original_0001",
  "sender_member_id": "mem_mira_example",
  "recipient_member_id": "mem_grokbot_example",
  "job_refs": [{ "id": "job_example_0001", "key": "review-bridge-ui", "repository_id": "repo_example_0001" }],
  "content": "Please review the route configuration. This is inert example data.",
  "created_at": "2026-10-25T10:50:00.000Z",
  "job_request": {
    "version": 1,
    "repository_id": "repo_example_0001",
    "route_id": "rte_example_0001",
    "recipient_member_id": "mem_grokbot_example",
    "operation": "review",
    "instructions": "Please review the route configuration. This is inert example data.",
    "input": { "scope": "configured routes", "requested_checks": ["isolation", "retries"] }
  }
}
```

**(c) `job.status` report from the receiver or executor back to the bridge.** It is POSTed to `/webhooks/<source_id>` and signed with that source's own key. The bridge's generic inbound wrapper is `{timestamp, data}`, and the bridge answers `202 {"status": "accepted" | ...}`. Sources: `docs/grokbot-contract.md:104-127` and `src/worker.ts:205-228`.

```json
{
  "timestamp": "2026-10-25T11:05:00.000Z",
  "data": {
    "schema_version": 1,
    "kind": "job.status",
    "job_id": "job_example_0001",
    "sequence": 2,
    "state": "running",
    "content": "Executor started the review.",
    "thread_id": "thr_example_0001",
    "in_reply_to_message_id": "msg_example_0001"
  }
}
```

The last three keys in `data` are optional. Server-side enforcement is in `selfhost/communication.ts:482-489`. If any check below fails, the report is silently ignored.
- The source must have `allow_job_updates` set. The zod parse is strict, so unknown keys are rejected.
- `sequence` must be greater than the job's stored `report_sequence`.
- The first source to report on a job is pinned. Later reports from any other source are ignored.
- The source's member must equal `job_request.recipient_member_id`.
- Only these state transitions are allowed:

```text
requested              -> accepted | running | succeeded | failed | cancelled
accepted               -> running | succeeded | failed | cancelled
running                -> succeeded | failed | cancelled
cancellation_requested -> succeeded | failed | cancelled
succeeded / failed / cancelled -> (terminal; nothing)
```

**(d) Text reply, which is not a status report.** Source: `docs/grokbot-contract.md:106-117`.

```json
{ "timestamp": "2026-10-25T11:10:00.000Z",
  "data": { "schema_version": 1, "content": "Review complete; please inspect the linked job.",
            "thread_id": "thr_example_0001", "in_reply_to_message_id": "msg_example_0001",
            "message_id": "grokbot_reply_0001" } }
```

**(e) Route verification before activation.** The bridge sends `{"type":"verification","challenge":"<43-char base64url>"}`. The receiver checks the signature, then echoes `{"challenge":"<same>"}` with a 2xx. Source: `docs/grokbot-contract.md:26-40`.

## 3. Signing

These headers are real output for payload (b). They came from `signedGrokbotExample(Date.parse('2026-10-25T10:50:00.000Z'))` in `scripts/sign-grokbot-example.mjs`, which uses the **public dummy key**: 32 bytes of `0x42`.

```http
POST /messages HTTP/1.1
Content-Type: application/json
webhook-id: dly_example_0001
webhook-timestamp: 1792925400
webhook-signature: v1,2jEkRqRVRqXvKCGDrT7kTDonSwDvBP5ATKgokh8nuEI=

<exact compact JSON.stringify bytes of (b)>
```

```text
key        = whsec_QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI=   (dummy, public)
hmac_key   = base64decode(key without "whsec_")      # 24..64 bytes; NOT the literal string
signed     = webhook-id + "." + webhook-timestamp + "." + raw_body_bytes
signature  = "v1," + base64(HMAC_SHA256(hmac_key, signed))
```

Verifier rules. Sources: `src/crypto.ts:58-108` and `docs/grokbot-contract.md:20-24`.
- `webhook-timestamp` must be within **±300 s** of the receiver's clock.
- The signature header may hold up to **8** space-separated `v1,` candidates, to allow key rotation. Each decodes to exactly 32 bytes, and the comparison is constant-time.
- Verify the **raw bytes**. Never re-serialise the JSON before verifying.
- A retry sends the **same body and the same `webhook-id`** with a fresh timestamp and signature.

## 4. Receipt, retry and terminal codes

Sources: `docs/grokbot-contract.md:95-102` and `selfhost/communication-delivery.ts:39,97,138,148-159`.

| Receiver answers | Bridge does |
|---|---|
| 2xx with a body of at most 4 KiB | Marks the delivery delivered. This is **transport receipt only**, and the job state does not change. |
| 408, 429, 5xx or a network error | Retries with backoff of `min(1h, 2^attempt s)` plus jitter. It honours `Retry-After` up to 1 h. It stops after **8 attempts**. |
| 410 or 413 | Terminal. The delivery goes dead. |
| 409 | Same `delivery_id` with different bytes. The receiver must reject it. The bridge treats it as terminal. |
| Any other 4xx, a redirect, or an oversized response | In code, every 4xx except 408 and 429 is permanent and dead-letters as `recipient_rejected`. |

Delivery is **at-least-once**. The receiver must durably commit the receipt before returning 2xx, and it must dedupe on `delivery_id`. The same id with the same body digest gets the original receipt. The same id with a different digest gets a 409.

## 5. For vecnet: borrow verbatim, adapt or drop

vecnet differs from w2m in five ways. Text bodies are capped at 4 KiB. Attachments are references by hash. A recipient can be a group. Messages carry a priority bit. Messages also carry hop, ttl and sender-chain fields.

| Item | Verdict | Note |
|---|---|---|
| Standard Webhooks signing: headers, `whsec_` decoded key, `id.ts.body` input, ±300 s, up to 8 signatures, `v1,` | **Borrow verbatim** | `w2m-receiver` and other existing receivers keep working unchanged. |
| Separate ids for message, delivery, thread and job, with `delivery_id` equal to `webhook-id` | **Borrow verbatim** | This is the main idea that makes retries and fan-out safe. |
| Dedupe on `delivery_id`, returning 409 for the same id with different bytes | **Borrow verbatim** | |
| 2xx means receipt only, and execution state travels only through `job.status` | **Borrow verbatim** | |
| `job.status` shape, strictly increasing `sequence`, transition table, first reporter pinned | **Borrow verbatim** | |
| Retry classes: 408, 429 and 5xx retry, 8 attempts max, `Retry-After` honoured, other 4xx terminal | **Borrow verbatim** | |
| `schema_version` const, `additionalProperties:false`, required-but-nullable fields, `objectId`/`eventId` patterns, millisecond `Z` `created_at`, job-key pattern | **Borrow verbatim** | |
| Verification challenge echo before a route goes live | **Borrow verbatim** | This applies to push transports only. |
| `content` limit of 64 KiB, and 1 KiB when a job is attached | **Adapt** | Cap at 4,096 UTF-8 **bytes**. The 1 KiB job preview rule may become unnecessary, but keep "intent lives in `job_request`". |
| 96 KiB envelope cap | **Adapt** | Shrink it to fit the 4 KiB body plus metadata and attachment references. |
| `recipient_member_id`, a single recipient | **Adapt** | Allow a group id, and keep one `delivery_id` per resolved member route. `message_id` is shared across the fan-out. The group id should be recorded in a separate field so that recipient equality checks still apply per member. |
| *(none)* attachments | **Adapt: add** | Add something like `attachments: [{sha256, size, mime, name}]`, with bodies fetched by hash. w2m's closest analogue is its inbound `payload_ref`/`payload_sha256` retention, at `src/worker.ts:211-213`. |
| *(none)* priority | **Adapt: add** | Add a boolean or small enum. It should not change dedupe or retry semantics. |
| *(none)* hop, ttl and `sender_chain` | **Adapt: add** | Add these for relay loops. w2m's `original_event_id` and `in_reply_to_message_id` are causal links, not a hop record. |
| `job_request` with its seven keys | **Adapt** | Keep `version`, `operation`, `instructions`, `input` and `recipient_member_id`. Rework `repository_id` and `route_id` to vecnet's own scopes. |
| `{timestamp, data}` inbound wrapper on `/webhooks/<source_id>` | **Adapt** | Use it only at the boundary when vecnet talks to w2m. vecnet's native envelope does not need it. |
| `original_thread_id` | **Adapt** | Keep it only for bridged external transports, and leave it `null` for native traffic. |
| Bridge SSRF and DNS guards, the MCP compatibility wrapper, and the GitHub native secret convention | **Drop** | These are concerns of the w2m bridge, not of the envelope. |

Note: the contract lists 409 separately from 410 and 413, but the code dead-letters all three alike at `selfhost/communication-delivery.ts:148`. vecnet should define 409 explicitly as "identity conflict, terminal, alert".
