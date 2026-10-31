# webhook2mcp job envelope and signing, shown to Max

Type: research
Status: resolved
Blocked by:

## Question

Extract webhook2mcp's envelope (`docs/grokbot-contract.md`, `examples/grokbot-envelope.schema.json` in `~/src/w2m-ops-admin`): the twelve required fields, the optional `job_request`, the `job.status` report with strictly increasing `sequence`, the 2xx-vs-job.status receipt split, retry/terminal status codes, and the Standard Webhooks signing headers. Produce a short readable summary with the actual JSON shapes so Max can see it (he asked), and list what vecnet should borrow verbatim vs adapt. Deliverable: `docs/research/w2m-envelope.md`.

## Answer

Full shapes are in [docs/research/w2m-envelope.md](../../../docs/research/w2m-envelope.md). That file has the schema excerpt, an ordinary message, a job-creating delivery, a `job.status` report and real signed headers.

Gist:
- **Envelope.** It has twelve required fields, and unused ones are `null`, not omitted. `schema_version` is the constant 1. Message, delivery, thread and job ids are all separate. `delivery_id` equals `webhook-id` and is the dedupe key.
- **Job request.** The optional `job_request` has exactly seven keys. When it is present, `job_refs` has exactly one entry and `content` becomes a preview of at most 1 KiB.
- **Receipt.** A 2xx means durable receipt only. Execution state comes only from a signed `job.status` webhook with a strictly increasing `sequence` and a fixed state-transition table. The first source to report is pinned.
- **Retries.** 408, 429, 5xx and network errors are retried up to 8 attempts, honouring `Retry-After`. Every other 4xx is terminal in code, including 409, 410 and 413.
- **Signing.** Standard Webhooks with an HMAC-SHA256 over `id.timestamp.rawbody`. The key is the base64-decoded `whsec_` value. The timestamp window is ±300 s, and up to 8 signatures are allowed.

vecnet should borrow the signing, id separation, dedupe and receipt-versus-status split verbatim. It should shrink content to 4 KiB, fan group recipients out to one delivery per member, and add attachment hashes, priority, and hop/ttl/sender_chain.
