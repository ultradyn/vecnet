# Enrolment, keys, scopes, audit

Type: grilling
Status: resolved
Blocked by: 01, 04

## Question

How the operator enrols a peer (CLI? dashboard? invite token?), per-peer secrets and rotation overlap, request signing per transport (Standard Webhooks HMAC on push, bearer on SSE/HTTP, MCP auth for dots), send scopes (who may message whom; defaults), what the audit log records, secret-safe logging.

Added from ticket 02: dots authenticate as an OAuth 2.1 (PKCE) ChatGPT **user** principal, never as "dot X". Decide how one OAuth principal maps to one or more enrolled dot peers (per-subscription `peer_id` argument? one peer per principal?), and whether the hub runs its own authorization server or delegates.

## Answer

Max approved by voice, 2026-11-01, with one correction on dots.

- **Enrolment:** operator runs e.g. `vecnet peer add --kind grokbot --name Mira`; it prints the peer id and a one-time enrolment token; the connector exchanges the token once for the peer's long-lived secret. The admin web UI gets the same flow later, and peer bindings and scopes are editable there.
- **Credentials per transport:** HTTP send and SSE use a per-peer bearer secret. Each push route has its own Standard Webhooks `whsec_` key. Rotation issues a new key and keeps the old valid for 24 h; pushes are dual-signed during the overlap.
- **Dots (corrected by Max):** a dot never holds a vecnet secret. The MCP install and OAuth login belong to a **ChatGPT account**, not to a bot; today each account has exactly one dot, and Max may run several accounts. So: the hub runs a minimal OAuth 2.1 (PKCE) authorization server; the operator pre-creates a dot peer; on first login the ChatGPT principal is bound to that peer via a one-time link code; **one principal → one dot peer for now**, with the binding model allowing several peers per principal if OpenAI later allows teams of dots. Event push to the dot is signed with the `whsec_` ChatGPT supplies at `events/subscribe`. Check webhook2mcp's OAuth setup for what ChatGPT requires of the authorization server (DCR/CIMD, metadata); it already works in production.
- **Send scopes:** default allow-all among enrolled peers and teams; per-peer allow/deny lists set by the operator; ad hoc group creation respects the same scopes.
- **Audit:** append-only audit table: actor peer, action (send, ack, enrol, rotate, group create/change, purge, scope change), target ids, timestamp. Message bodies and secrets never enter logs or audit rows; redaction lives in the logger.
