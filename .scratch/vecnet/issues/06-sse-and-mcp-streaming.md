# One client path for SSE and MCP Streamable HTTP

Type: research
Status: resolved
Blocked by:

## Question

pagchat wants SSE (resumable via `Last-Event-ID` or cursor, ~15 s heartbeats, long-poll fallback) and notes it matches MCP Streamable HTTP so one client code path serves both. Verify what MCP Streamable HTTP actually requires server-side (session ids, resumability, event ids, GET stream semantics) and whether a single vecnet subscription endpoint can serve both plain SSE clients and MCP clients, or whether they must be two endpoints sharing one event log. Deliverable: `docs/research/sse-mcp.md` with the conventions vecnet should adopt.

## Answer

Use two endpoints over one durable per-peer event log. MCP's current revision, 2026-07-28, removed sessions, the GET stream, SSE event ids and `Last-Event-ID` resumption, and it is POST-only via `subscriptions/listen`. Sharing one endpoint would mean sniffing protocols. pag-chat's "one client code path" therefore holds only for the SSE parser and the cursor.

- **`/v1/events`.** GET serves SSE or long-poll, chosen by `Accept`. Each event's SSE `id` is an opaque cursor `<epoch>.<seq>` with a strictly increasing per-peer seq. `Last-Event-ID` beats `?after=`. The hub replays every entry with seq > cursor, then tails. With no cursor, it starts from the peer's acked cursor. Heartbeats are `: hb` comments every 15 s. Each stream opens with `retry: 3000` and a priming id. Unknown or purged cursors get `409`.
- **`/mcp`.** The inbox is a resource. `notifications/resources/updated` is only a wake-up hint. The tools `vecnet_inbox(after)` and `vecnet_ack(cursor)` read and acknowledge the same log, so delivery stays lossless without MCP transport features.
- **Clients.** Persist the cursor, parse SSE with fetch (auth headers), dedupe on `delivery_id`, ack explicitly, and fall back to long-poll.

Details, citations and open questions are in [docs/research/sse-mcp.md](../../../docs/research/sse-mcp.md).
