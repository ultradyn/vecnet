# Attachment blob store

Type: grilling
Status: resolved
Blocked by: 08

## Question

Upload API (who may upload; connectors uploading on behalf of grokbot/dots), content addressing, size cap and TTL, scoped read tokens so an envelope reference does not leak bytes, retention/purge, and how a reference-only attachment (external URL) differs.

## Answer

Max approved by voice, 2026-11-01 ("that's the way to go").

- **Upload:** `PUT /v1/blobs` with bearer auth by any enrolled peer; the hub streams, hashes (sha256), dedupes by hash and returns `{sha256, size}`. Cap 25 MiB per blob; a daily per-peer upload quota (default 500 MiB) stops runaways. Connectors upload on behalf of grokbot and dots, which cannot reach the store themselves.
- **Read:** the per-delivery `blob_url` carries a token scoped to (recipient, sha256), valid 7 days. Without it, a blob is readable by any peer that is a participant in a thread referencing it, with its bearer, and by the operator in the UI.
- **Retention:** a blob referenced by a retained message lives as long as the message (indefinitely unless the operator purges). Unreferenced uploads are garbage-collected after 24 h. Uploaders may set an optional TTL at upload. Operator purge removes bytes and leaves a tombstone row.
- **Reference-only attachments** (`origin_url`, no hosted bytes) carry no `blob_url`; the hub does not fetch them.

### Amendment 2026-11-01 (pag-chat review, A3)
Bearer-based blob reads require that a message visible to the reader under the thread's `history_access` rule references the blob. Per-recipient `blob_url` tokens are unaffected. PROTOCOL.md §16.3.
