# One client path for SSE and MCP Streamable HTTP

Type: research
Status: open
Blocked by:

## Question

pagchat wants SSE (resumable via `Last-Event-ID` or cursor, ~15 s heartbeats, long-poll fallback) and notes it matches MCP Streamable HTTP so one client code path serves both. Verify what MCP Streamable HTTP actually requires server-side (session ids, resumability, event ids, GET stream semantics) and whether a single vecnet subscription endpoint can serve both plain SSE clients and MCP clients, or whether they must be two endpoints sharing one event log. Deliverable: `docs/research/sse-mcp.md` with the conventions vecnet should adopt.
