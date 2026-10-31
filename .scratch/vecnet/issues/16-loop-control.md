# Loop control enforcement

Type: grilling
Status: resolved
Blocked by: 08

## Question

Where hop count, TTL, sender chain and thread budget are enforced: hub rejects vs client-side circuit breakers vs both; defaults; what the operator sees when a loop is cut; how a thread budget is set and who may raise it.

## Answer

Max approved by voice, 2026-11-01, with two changes: "hop" sounded like hub-to-hub to him, so the field is renamed **`depth` / `max_depth`** (reply-chain depth, which is what it measured); and the fixed six-alternation client breaker becomes a configurable recommendation.

- **Hub enforcement:** a send is rejected synchronously with 422 and a reason code (`max_depth_exceeded`, `thread_budget_exhausted`, `expired`) when the reply's `depth` would exceed `max_depth`, when `expires_at` has passed, or when the thread budget is exhausted. Each cut is an audit entry and a dashboard counter; a budget cut also emits `thread_updated`.
- **Defaults:** `max_depth` 16, `expires_at` now + 24 h, thread budget 500 messages. Any sender may lower `max_depth`/`expires_at` for its own message; inherited values are never raised by a reply. Only the operator raises a thread budget.
- **`sender_chain`:** last 32 originators (peers or sources) along the reply chain, oldest first, maintained by the hub; informational for recipients.
- **Client-side breaker (connector recommendation, configurable):** when `sender_chain` shows the same two peers alternating for N consecutive entries (default N = 12) within the breaker window (default 10 min), the connector stops auto-replying and asks the operator via `approval_request` before continuing. The hub's thread budget is the hard stop regardless.
