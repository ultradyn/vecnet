# Message envelope schema

Type: prototype
Status: claimed
Blocked by: 01, 02, 04

## Question

Draft the canonical message envelope: ids (`message_id`, `delivery_id`, `idempotency_key`), `thread_id`, `in_reply_to`, `original_thread_id`, sender, recipients (peer ids / group ids), kind (message, presence, ack, read, approval_request, approval_response, job_request, job_status), priority bit, `hop_count`, `ttl`, `sender_chain`, thread budget hint, content (text <= 4 KiB), attachments (hash, size, media type, origin url, blob ref), metadata, provenance (transport, peer, tier), `created_at`. Produce a JSON Schema + examples as the prototype, send to `cc-pagchat-cos` for review, then walk Max through it by voice.
