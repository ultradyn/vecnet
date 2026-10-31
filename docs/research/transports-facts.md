# Transport facts gathered during charting (2026-11-01)

Partial: a fact-finding subagent reported on webhook2mcp before the session restarted; items on Grok Bot, dots, openclaw and c2c were lost and are re-researched by tickets 01–03. Nothing below was verified by the coordinator beyond reading the agent's report.

## webhook2mcp ("w2m")

**Where it lives.**
- Repo: `/home/xertrov/src/w2m-ops-admin` (remote `git.lan` `max/w2m-ops-admin`). Production is the private `xpp-tm/xpp-server-gpt-webhook2mcp`, running on the NAS at `https://gb2cxc.xpp.exchange`.
- Ops notes: `~/.llm-general/lan-swarm/operations/webhook2mcp-nas.md` and `w2m-receiver-nas.md`.
- TypeScript: one Node process with SQLite behind a Cloudflare Tunnel. A Workers/D1 variant also exists. See the repo `README.md`.

**Inbound to the bridge from any sender.**
- Senders `POST /webhooks/<source_id>`, signed with Standard Webhooks, body `{"timestamp": ISO, "data": {...}}`. Success is 202.
- A reply into a thread sends `data: {schema_version:1, content, thread_id, in_reply_to_message_id, message_id}`.
- The authenticated source binding decides member, channel and return route; payload fields never override it.
- Sender helper: `~/.local/share/w2m/w2m_send.py`. See `docs/grokbot-contract.md` §Receipt and `~/.config/w2m/xpp-cos-USAGE.md`.

**Outbound "grokbot-contract" v1 (bridge → bot receiver).** Source: `docs/grokbot-contract.md`, `examples/grokbot-envelope.schema.json`. Note: this is what the bridge sends to a receiver, not what Grok Bot itself emits.
- Admins configure a route: HTTPS URL + per-route `whsec_` key via the admin UI. Send tools never accept a URL.
- One JSON object per POST, at most 96 KiB, `content` up to 64 KiB.
- Signing headers: `webhook-id` (= `delivery_id`), `webhook-timestamp` (Unix seconds), `webhook-signature: v1,<b64 HMAC-SHA256>`. HMAC over `id.timestamp.body` with base64-decoded key bytes. Receivers allow ±300 s skew; up to 8 signatures for key rotation.
- Route activation: bridge sends `{"type":"verification","challenge":...}`; receiver echoes `{"challenge":...}`.
- Twelve required envelope fields: `schema_version, message_id, delivery_id, thread_id, original_thread_id, in_reply_to_message_id, original_event_id, sender_member_id, recipient_member_id, job_refs[], content, created_at`.
- Optional `job_request` with seven keys: `version, repository_id, route_id, recipient_member_id, operation, instructions, input`.
- Response semantics: 2xx = durable receipt only; 408/429/5xx/network errors retried up to 8 attempts honouring `Retry-After`; 410 and 413 terminal; 409 = same id, different bytes. At-least-once; receivers dedupe on `delivery_id`.

**Job envelope and the `job.status` split.**
- A job key is immutable within account and repository; same key + same intent returns the existing job. Message, thread, job and delivery ids are all separate.
- A transport 2xx or a text reply is never an execution report. Execution state comes only from a signed webhook on a source with job-report permission: `{"kind":"job.status","job_id","sequence":n,"state":"accepted|running|succeeded|failed|cancelled"}`. `sequence` strictly increasing so stale reports cannot move state backwards.

**w2m-receiver.**
- Separate repo `git.lan xpp-tm/w2m-receiver` v0.1.1, live at `https://w2m-rx.xpp.exchange/messages[/<sender>]`. No local clone found.
- Verifies the bridge's signed delivery, dedupes in SQLite, forwards to a Grok Bot's inbound routine webhook using `WEBHOOK_URL` + `Authorization: Bearer <WEBHOOK_AUTH>`. One process per path/member/key, so each sender gets its own container.

## Implications noted so far

- A Grok Bot receives messages via an inbound "routine webhook" URL with a bearer token (as w2m-receiver forwards). Ticket 01 must confirm shape and limits.
- Standard Webhooks HMAC with `webhook-id`/`webhook-timestamp`/`webhook-signature` is already deployed on Max's LAN; vecnet's push transport should reuse it verbatim so `w2m-receiver` and pag-chat's receiver work unchanged.
- The id separation (message / delivery / thread / job) and the 2xx-vs-`job.status` split are proven in production and match pag-chat's asks.
