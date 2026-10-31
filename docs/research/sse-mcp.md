# SSE and MCP streaming: one event log, two front doors

Research for ticket 06 (`.scratch/vecnet/issues/06-sse-and-mcp-streaming.md`). Gathered 2026-11-01 by rs-06-sse.

## Bottom line

- **The premise has aged.** pag-chat assumed SSE "matches MCP Streamable HTTP so one client code path serves both". That was roughly true for MCP revisions 2025-03-26 to 2025-11-25, which had a GET SSE stream with `Last-Event-ID` resumption. The current revision, **2026-07-28**, removed sessions, the GET stream, SSE event ids and `Last-Event-ID` resumption [M1, M2].
- **Use two endpoints over one durable per-peer event log.** Plain SSE (plus long-poll on the same URL) at `/v1/events`, and an MCP server at `/mcp`. They share only the log, the cursor and the SSE line parser.
- **Losslessness comes from vecnet's cursor, never from the transport.** Every front door reads "events after cursor C" from the same log. MCP notifications are treated as wake-up hints. The MCP client then fetches with a cursor through a tool.

## Facts

### MCP, current revision 2026-07-28

`https://modelcontextprotocol.io/specification/latest/...` resolves to 2026-07-28. Its changelog compares against 2025-11-25 [M2].

- **POST only.** "The server MUST provide a single HTTP endpoint path ... that supports POST." The client "MUST include an `Accept` header listing both `application/json` and `text/event-stream`". A request's response is either `application/json` or a `text/event-stream` response stream, and "The client MUST support both" [M1 §Sending Messages].
- **No GET stream.** The changelog says: "Replace the HTTP GET endpoint and `resources/subscribe`/`resources/unsubscribe` with `subscriptions/listen`: a single long-lived POST-response stream for opted-in server-to-client change notifications" [M2 major 4]. Toward older clients, a modern-only server SHOULD answer "HTTP GET or DELETE to the MCP endpoint" with `405 Method Not Allowed` [M1 §Earlier Streamable HTTP Revisions].
- **No resumability.** "Resumable SSE streams via `Last-Event-ID` are not supported" [M1]. The changelog says: "Remove SSE stream resumability and message redelivery (the `Last-Event-ID` header and SSE event IDs) ... A broken response stream loses the in-flight request; clients MUST re-issue it as a new request with a new request ID" [M2 major 9]. Older clients' `Last-Event-ID` should be ignored [M1].
- **No sessions.** "Remove protocol-level sessions and the `Mcp-Session-Id` header" [M2 major 1]. Old session headers should be ignored, "and do not mint or echo session IDs" [M1]. The `initialize` handshake is gone. Every request carries its version and capabilities in `_meta`, and servers MUST implement `server/discover` [M2 majors 2–3, M4].
- **Headers.** "Every POST request to the MCP endpoint MUST include an `MCP-Protocol-Version` header", and it must match the `_meta` version or the request gets 400 [M1 §Protocol Version Header]. `Mcp-Method` and `Mcp-Name` are required standard headers [M1, M2 minor 4].
- **Security.** "Servers MUST validate the `Origin` header on all incoming connections ... If the `Origin` header is present and invalid, servers MUST respond with HTTP 403 Forbidden." Servers "SHOULD implement proper authentication for all connections" [M1 §Security].
- **Keep-alive.** "For long-lived streams — in particular the `subscriptions/listen` response stream — servers are encouraged to periodically emit an SSE comment line ... as a keep-alive." Clients "must ignore such lines" [M1].
- **`subscriptions/listen` filter is closed.** The filter has four fields: `toolsListChanged`, `promptsListChanged`, `resourcesListChanged`, and `resourceSubscriptions` (URIs, which yields `notifications/resources/updated`). "The server MUST NOT send notification types the client has not explicitly requested" [M3].
- **Acknowledgement first.** The server "MUST send `notifications/subscriptions/acknowledged` as the first message", carrying the subscription id in `_meta` [M3]. A client "MAY have multiple active subscriptions concurrently". A close without a final `complete` result "indicates an unexpected disconnect, which the client MAY treat as a trigger to reconnect". On stdio the client "MUST re-send `subscriptions/listen`", because "the server holds no subscription state across reconnections" [M3].
- **Notifications are change hints.** `resources/updated` carries only `uri`, and the resources page shows the client calling `resources/read` afterwards [M3, M5]. Nothing in the spec covers notifications missed during a gap. Treat them as lossy.
- **Dual-era serving is allowed.** "A dual-era server MAY serve both eras concurrently on the same endpoint or process." An `initialize` request selects the legacy semantics [M4].
- **Extensions.** Extension ids use reverse-DNS prefixes, and `io.modelcontextprotocol` / `mcp` second labels are reserved [M4, M6]. The tasks extension can push `notifications/tasks` via `subscriptions/listen`, but "Polling is the default" [M7].

### MCP 2025-11-25 (previous revision, still what many deployed clients speak)

- **GET stream.** The client "MAY issue an HTTP GET to the MCP endpoint". The server returns `text/event-stream` or 405. It "MAY send JSON-RPC requests and notifications on the stream" but no responses except when resuming [M8 §Listening].
- **No broadcast.** The server "MUST send each of its JSON-RPC messages on only one of the connected streams; that is, it MUST NOT broadcast" [M8 §Multiple Connections].
- **Event ids.** If present, the event `id` "MUST be globally unique across all streams within that session". Ids "SHOULD encode sufficient information to identify the originating stream". The client resumes with GET plus `Last-Event-ID`, and the server "MAY ... replay messages ... on the stream that was disconnected" but "MUST NOT replay messages that would have been delivered on a different stream". Event ids act "as a cursor within that particular stream" [M8 §Resumability].
- **SSE polling (SEP-1699).** The server "SHOULD immediately send an SSE event consisting of an event ID and an empty `data` field" to prime reconnection. It "MAY close the connection (without terminating the SSE stream)". It "SHOULD send an SSE event with a standard `retry` field before closing", and "The client MUST respect the `retry` field" [M8, M9 minors 6–7].
- **Sessions.** `MCP-Session-Id` is assigned with `InitializeResult` and must be visible ASCII 0x21–0x7E. A 404 means "start a new session". DELETE terminates [M8 §Session Management].
- **Resumability was a MAY.** Even in this revision replay is optional, so no MCP revision gives a lossless guarantee by itself.

### WHATWG Server-Sent Events [W]

- **Fields.** Only `event`, `data`, `id` and `retry` are recognised. A line starting with `:` is a comment [W §9.2.6].
- **`id` persists.** An `id` containing U+0000 is ignored. An empty `id:` resets the last event ID, so no `Last-Event-ID` is sent on the next reconnect. The id "remains set to this value until the next time it is set by the server" [W §9.2.6].
- **Empty-data events update the id.** An event with an empty data buffer is not dispatched, but the last event ID is still updated, because step 1 runs before the empty check. That is the basis of MCP's priming event [W §9.2.6].
- **Header value space.** `Last-Event-ID` is sent on reconnect only when the stored id is non-empty. Its value is "any UTF-8 encoded string, that does not contain U+0000 NULL, U+000A LF, or U+000D CR" [W §9.2.4, #the-last-event-id-header].
- **`retry`.** It accepts ASCII digits only and sets the reconnection time in ms. The default is "implementation-defined ... probably in the region of a few seconds", and user agents "might introduce an exponential backoff delay" [W §9.2.2, §9.2.3].
- **Status codes.** A non-200 status or wrong Content-Type fails the connection, and "it does not attempt to reconnect". Network errors reconnect. The non-normative intro says 204 stops reconnection [W §9.2.2]. A normal end-of-body also triggers a reconnect.
- **Heartbeats.** Authoring note: "authors can include a comment line (one starting with a ':' character) every 15 seconds or so." The note also warns about intermediary chunking and per-server connection limits [W §9.2.7].
- **No custom headers.** `EventSource` takes only `url` and `withCredentials`, so a browser `EventSource` cannot send `Authorization` [W §9.2.2 IDL]. The stream must be UTF-8 [W §9.2.5].

### pag-chat requirements (local)

- `.scratch/vecnet/grilling-log.md` round 2 says: "SSE pull (resumable via Last-Event-ID/cursor, ~15 s heartbeats, long-poll/plain poll as documented fallback) ... pagchat prefers SSE over long-poll". Delivery is at-least-once with dedupe on delivery id, per-thread ordering, and receipts split into accepted and processed.
- The 15 s figure matches the WHATWG authoring note.

### Local MCP notes are stale

`~/.llm-general/ai-coding/mcp/protocol-fundamentals.md` and `tips-and-gotchas.md` (2026-04-23) call 2025-06-18 "current" and recommend `Last-Event-ID` for MCP reconnection. Both are superseded by 2026-07-28. Claude Code channels (`claude-channels.md`) push via a non-spec `notifications/claude/channel` over **stdio**. That is relevant to the `vecnet` CLI bridge, not to `/mcp`.

## Answers

### One endpoint or two?

Use **two endpoints over one log.**

1. **Methods collide.** `/v1/events` needs GET so a browser `EventSource` and `curl` work. 2026-07-28 MCP is POST-only, and 2025-era MCP clients also GET the MCP endpoint, expecting JSON-RPC frames and session headers. A shared path would have to sniff `MCP-Protocol-Version` or `Mcp-Session-Id` to choose framing. That is fragile and buys nothing.
2. **Framing differs.** MCP frames every event as JSON-RPC with `_meta` subscription ids, an acknowledgement first and a `complete` result last. Plain SSE subscribers want `event: <type>` / `id: <cursor>` / `data: <envelope>`.
3. **Delivery rules differ.** MCP 2025-11-25 forbids broadcasting a message across a client's streams, and 2026-07-28 forbids unrequested notification kinds. A vecnet peer with two plain SSE connections should get every event on both, because the cursor makes duplicates harmless.

Long-poll can share `/v1/events`. Choose by `Accept`: `text/event-stream` gets SSE and `application/json` gets a long-poll batch. That gives one URL and one cursor for both pull styles.

### Event id and cursor scheme

- **Per-peer log.** Each peer has an append-only, durable delivery log (its mailbox). Each entry gets a strictly increasing per-peer sequence number `seq` at append time. The log gives a total order per peer, which preserves per-thread order.
- **Cursor format.** The cursor is an opaque string, `<log-epoch>.<seq>`, for example `e1.10423`. The epoch lets the hub detect a cursor from a rebuilt or restored log instead of silently skipping. It is visible ASCII, so it is safe in `Last-Event-ID`, query strings and MCP params.
- **Same cursor everywhere.** The SSE `id:`, the long-poll `next_cursor`, the MCP tool's `after`/`next_cursor` and the ack endpoint all use it.
- **Replay rule.** The hub replays every entry with `seq > cursor`, in order, then tails. That is lossless as long as the entry is retained, and retention is indefinite (grilling round 3).
- **Starting position.** With no cursor, the stream starts at the peer's last acked cursor, not at "now". Undelivered mail survives a fresh client. `after=head` explicitly means "from now".
- **Header beats query.** `Last-Event-ID` wins over `?after=`. A browser `EventSource` reconnects to the same URL, so the original `?after=` is stale while the header holds the newer id. Clients that cannot set headers pass `?after=` on first connect only.
- **Priming.** After connecting, the hub emits `retry: 3000` and a priming event `id: <current cursor>` with empty `data:`. The client then holds a valid resume point before the first real event, per W §9.2.6 and the M8 priming pattern.
- **Dedupe still applies.** Replay plus at-least-once delivery means duplicates happen. The envelope's `delivery_id` stays the dedupe key, and `seq` is not a substitute.
- **Delivery is not processing.** Sending an SSE frame is not a receipt. Clients POST `/v1/ack {cursor}` for "accepted", plus per-message processed receipts as the envelope spec defines. The hub's "undelivered" state and dead-letter timers key off the acked cursor, not the sent one.

### Heartbeats and reconnect

- **Heartbeat comment.** Send `: hb\n\n` every 15 s on idle `/v1/events` and `/mcp` streams. The 15 s value comes from the WHATWG note and pag-chat, and the MCP spec encourages comment keep-alives.
- **Liveness timeout.** Clients treat about 45 s of silence (3 missed heartbeats) as a dead connection. They then close and reconnect with the last cursor.
- **Retry.** Send `retry: 3000` at stream start. Clients add jittered exponential backoff up to about 60 s, which W §9.2.3 permits.
- **Status codes.** `401`/`403` mean stop and re-auth, and `EventSource` already treats any non-200 as fatal. Use `204` only for "peer disabled, stop reconnecting". A cursor from an unknown epoch, or below retention, gets `409` with a JSON body naming the oldest valid cursor. The client then re-syncs thread history explicitly.
- **Long-poll.** `GET /v1/events?after=C&wait=25` with `Accept: application/json` returns `{events:[...], next_cursor}`. It returns immediately when events exist and holds up to 25 s otherwise. A `wait` under the 30 s mark avoids common proxy idle cutoffs. Plain poll is the same call with `wait=0`.
- **Proxy hygiene.** Send `Cache-Control: no-store` and `X-Accel-Buffering: no`, and disable response compression and buffering on the stream. Prefer HTTP/2 so browsers avoid the per-server connection limit (W §9.2.7).

### MCP front door (`/mcp`)

- **Revisions.** Serve 2026-07-28 and stay dual-era for 2025-11-25 clients (M4 allows this). Which revision dots, Claude Code and Codex speak today belongs to ticket 02.
- **Inbox as a resource.** Expose `vecnet://peer/<id>/inbox` as a resource. On 2026-07-28 the client opts in with `subscriptions/listen {resourceSubscriptions:["vecnet://peer/<id>/inbox"]}`. On 2025-11-25 it uses `resources/subscribe` plus the GET stream.
- **Updates are hints.** The hub sends `notifications/resources/updated` for the inbox URI whenever the log grows. It never puts message content in notifications, because the 2026 filter cannot carry custom kinds and notifications are lossy.
- **Tools carry the data.** `vecnet_inbox(after?, limit?)` returns `{events, next_cursor}` from the same log, `vecnet_ack(cursor)` acknowledges, and `vecnet_send(...)` sends. A missed notification costs latency, not data. On reconnect the client calls `vecnet_inbox(after=<its cursor>)`.
- **No transport resumability.** Do not implement MCP `Last-Event-ID` replay, even for 2025-era clients. It is optional there and removed in 2026, and the cursor tool already covers it.
- **Auth.** Validate `Origin` and require per-peer bearer auth on every POST.
- **Coding sessions.** The `vecnet` CLI can act as a stdio channel server for Claude Code. It consumes `/v1/events` and re-emits `notifications/claude/channel`. That is a client of the SSE door, not a third hub endpoint.

### What a client library must do

1. **Persist the cursor.** Store the last processed cursor durably, per peer and per consumer, and advance it only after the handler succeeds.
2. **Use a fetch-based SSE parser, not `EventSource`.** It needs `Authorization` headers and comment visibility for liveness. It must handle CR, LF and CRLF line endings, multi-line `data`, the `id` NULL rule, empty-`id` reset and empty-data priming events.
3. **Resume with `Last-Event-ID`.** Send the stored cursor as `Last-Event-ID`, or `?after=` when headers are impossible. Never send both with different values.
4. **Apply the liveness timeout.** Reconnect after about 45 s of silence. Honour `retry` and use jittered backoff. Stop on 401, 403 or 204, and re-sync on 409.
5. **Dedupe.** Drop any `delivery_id` already seen, within a bounded window.
6. **Ack.** POST `/v1/ack` periodically, or after each batch, and send processed receipts where required.
7. **Fall back.** Switch to long-poll after repeated SSE failures, for example when a proxy strips streaming, using the same cursor.
8. **MCP clients.** Treat any `resources/updated` for the inbox URI as "call `vecnet_inbox(after=cursor)`". After any stream loss, re-listen and then fetch immediately.

## Open questions

- **Multiple consumers per peer.** One identity may have several consumers, such as two sessions of the same bot. Should they share one acked cursor or keep named consumer cursors (`?consumer=`)? This affects whether ack is per peer or per consumer.
- **Cursor below retention.** History is indefinite now, but if the operator purges, is `409` plus explicit re-sync acceptable to pag-chat?
- **Client revisions.** Which MCP revision do dots, Claude Code and Codex speak today? That decides whether dual-era support on `/mcp` is needed in v1 (ticket 02).
- **Tasks push.** Should vecnet use the MCP tasks extension's `notifications/tasks` for job status? Its opt-in field was not visible in the core filter [M3, M7]. Read https://github.com/modelcontextprotocol/ext-tasks before deciding.
- **Browser auth.** For the operator web inbox, choose between a cookie with `withCredentials` and a short-lived query token. Never put a peer's long-lived secret in a URL.
- **pag-chat sign-off.** Confirm with pag-chat that "two endpoints, one cursor" meets its "one client code path" goal. The shared part is the cursor and the SSE parser, not MCP framing.

## Sources

- [M1] MCP 2026-07-28 Streamable HTTP: https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http (overview: https://modelcontextprotocol.io/specification/latest/basic/transports)
- [M2] MCP 2026-07-28 changelog: https://modelcontextprotocol.io/specification/latest/changelog
- [M3] MCP 2026-07-28 subscriptions: https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/subscriptions
- [M4] MCP 2026-07-28 versioning: https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning
- [M5] MCP 2026-07-28 resources: https://modelcontextprotocol.io/specification/2026-07-28/server/resources
- [M6] MCP extensions overview: https://modelcontextprotocol.io/docs/extensions/overview
- [M7] MCP tasks extension: https://modelcontextprotocol.io/extensions/tasks/overview
- [M8] MCP 2025-11-25 transports: https://modelcontextprotocol.io/specification/2025-11-25/basic/transports
- [M9] MCP 2025-11-25 changelog: https://modelcontextprotocol.io/specification/2025-11-25/changelog
- [W] WHATWG HTML, Server-sent events (Last Updated 30 October 2026): https://html.spec.whatwg.org/multipage/server-sent-events.html
- Local: `.scratch/vecnet/grilling-log.md`; `~/.llm-general/ai-coding/mcp/claude-channels.md`, `protocol-fundamentals.md`, `tips-and-gotchas.md`.

Method note: the spec pages were read through fetch subagents that quoted the normative text. The rs-06-sse author did not read them directly. The quotes above are as reported, and the claims marked as removals in 2026-07-28 were cross-checked across the transport page and the changelog.
