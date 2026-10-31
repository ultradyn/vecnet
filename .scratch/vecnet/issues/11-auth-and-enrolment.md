# Enrolment, keys, scopes, audit

Type: grilling
Status: claimed
Blocked by: 01, 04

## Question

How the operator enrols a peer (CLI? dashboard? invite token?), per-peer secrets and rotation overlap, request signing per transport (Standard Webhooks HMAC on push, bearer on SSE/HTTP, MCP auth for dots), send scopes (who may message whom; defaults), what the audit log records, secret-safe logging.

Added from ticket 02: dots authenticate as an OAuth 2.1 (PKCE) ChatGPT **user** principal, never as "dot X". Decide how one OAuth principal maps to one or more enrolled dot peers (per-subscription `peer_id` argument? one peer per principal?), and whether the hub runs its own authorization server or delegates.
