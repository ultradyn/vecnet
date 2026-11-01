# vecnet Connectors (Phase 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect the hosted and external peers from `SPEC.md` §16 phase 2: the `/mcp` front door with the vecnet tools and inbox resource, the MCP Events webhook profile and OAuth 2.1 so OpenAI dots can subscribe and reply, the Grok Bot push route kind, GitHub inbound webhook sources, and a public-IP-only egress guard for every outbound POST the hub makes.

**Architecture:** `/mcp` is a hand-rolled JSON-RPC 2.0 server in `internal/mcpserver` (dual-era: 2026-07-28 POST-only with `server/discover` and `subscriptions/listen`; 2025-11-25 with `initialize`, `Mcp-Session-Id` and the GET stream) that binds to the same `hub` methods the HTTP API uses. MCP Events is a per-subscription tailer of the recipient's existing event log: the subscription's cursor **is** the log cursor, so replay, outage and refresh fall out of Task 6 of the core plan. OAuth tokens are opaque, hashed at rest, and resolve to an `auth.Caller` like a bearer. Grok Bot is a second `routes.kind` driven by the existing push worker. GitHub ingress calls `Hub.Send` as a source actor. All four outbound paths share one egress dialer.

**Tech Stack:** Go 1.26 (`CGO_ENABLED=0`), stdlib `net/http`, `crypto`, `encoding/json`; `github.com/santhosh-tekuri/jsonschema/v6` for tool argument validation; `github.com/modelcontextprotocol/go-sdk/mcp` **types only** (`mcp.Tool`, `mcp.Resource`, `mcp.CallToolResult`, …) for wire shapes; existing `internal/{hub,eventlog,auth,webhook,push,store,envelope}`.

**Spec:** `docs/PROTOCOL.md` §2.11, §4 (all), §10, §12, §13.3, §13.4, §14; `docs/research/dots.md` (ChatGPT Events profile, OAuth rules), `docs/research/grokbot.md` (routine webhook facts), `docs/research/sse-mcp.md` (MCP revision facts); `docs/adr/0001-tech-stack.md`. Out of scope here: the openclaw channel plugin (TypeScript, its own plan against the openclaw repo), the operator UI surface for subscription health (phase 3), escalation (phase 4).

**ADR amendment (recorded by Task 2 in `docs/adr/0001-tech-stack.md`):** the official go-sdk cannot host MCP Events: its request dispatch (`handleReceive` → `checkRequest`) rejects unknown methods before any middleware runs, and `ServerCapabilities` has no top-level `events` field, which ChatGPT reads. `/mcp` is therefore hand-rolled on `net/http`; the SDK is kept as a types dependency and as the client in tests.

## Global Constraints

- Everything in the core plan's Global Constraints still applies (module path, `build2` with two threads, ids, timestamps, error envelope, caps, secrets never logged, worktree builds).
- `/mcp` serves protocol versions **2026-07-28** and **2025-11-25** only; an `initialize` naming any other version gets JSON-RPC error `-32602` with `data.supportedVersions: ["2026-07-28","2025-11-25"]` [R12].
- `/mcp` MUST return 403 when an `Origin` header is present and is not the hub's own public origin [§4.1]; MUST ignore `Last-Event-ID` [§4.1]; sends `: hb` every 15 s on idle streams.
- MCP Events: capability under both `capabilities.events` and `capabilities.extensions["io.modelcontextprotocol/events"]`; callback error code behind one constant `ErrCodeCallback = -32015`; one event per POST ≤ 256 KiB; headers `webhook-id` (= `eventId`), `webhook-timestamp`, `webhook-signature`, `X-MCP-Subscription-Id`; TTL clamp ≤ 24 h and ≤ access-token expiry; never retry 410 or 413 [§4.4].
- Egress (push routes, notification routes, Events callbacks, Grok Bot routines, CIMD fetches): resolve DNS once, connect only to public unicast IPs, pin the resolved IP while keeping SNI/Host, never follow redirects, 30 s total timeout. Dev override flag `--allow-private-egress` (env `VECNET_ALLOW_PRIVATE_EGRESS=1`) exists only so LAN receivers work on xsm [§14.7].
- OAuth: PKCE `S256` required; access tokens 1 h, refresh tokens 30 d rotated on use; redirect URIs limited to `https://chatgpt.com/connector_platform_oauth_redirect` and `https://chatgpt.com/connector/oauth/{callback_id}` plus `http://127.0.0.1:*` for tests; link codes 8 chars from `ABCDEFGHJKMNPQRSTUVWXYZ23456789`, valid 10 min, single use [§4.5, R15].
- Grok Bot routine URL MUST match `^https://api2\.cursor\.sh/automations/webhook/[0-9a-f-]{36}$`; key MUST start `crsr_`; stored encrypted (AES-256-GCM under `meta.hub_key`), never returned by any endpoint [§13.3].
- GitHub ingress: verify `X-Hub-Signature-256` over raw bytes with `hmac.Equal`; dedupe on `X-GitHub-Delivery`; body cap 256 KiB; summary ≤ 4096 bytes [§10, R09].
- New migrations are numbered per task to avoid merge collisions: Task 5 `0005_event_subscriptions.sql`, Task 6 `0006_oauth.sql`, Task 7 `0007_grokbot_routes.sql`, Task 8 `0008_sources.sql`.
- Worktree `.worktrees/connectors`, branch `connectors` off `master`; status files in `.scratch/vecnet/build-connectors/`.

## Review Focus

1. **ChatGPT opens with `initialize` for 2025-03-26** (its docs' example): the hub answers the -32602 error with `supportedVersions`, then ChatGPT retries with 2026-07-28 `server/discover`; a hub that 500s or hangs here loses every dot. Pinned in Task 2.
2. **Hostile callback URL** at `events/subscribe` (private IP, hostname resolving to 10.x, `http://`, port 8080, URL with userinfo): rejected with `-32015` `data.reason: "invalid_url"` before any connection is made. Pinned in Task 5.
3. **Access token expires while a subscription is live**: deliveries pause (subscription `paused_reason: token_expired`), nothing is dropped, and resume from the cursor after `events/subscribe` with a fresh token. Pinned in Task 5.
4. **GitHub redelivery and oversize payloads**: the same `X-GitHub-Delivery` twice yields one message and the same `message_id`; a 300 KiB body gets 413 before JSON parsing. Pinned in Task 8.
5. **Grok Bot's 400 rate-limit body** (`"Automation run rate limit exceeded"`) is retryable while `401` and `"is disabled"` 400s are terminal and alert; a hub that treats all 400s alike either spams a disabled routine or dead-letters on a transient. Pinned in Task 7.

---

## File structure

```
internal/egress/dialer.go                 public-IP-only pinned dialer, redirect-refusing http.Client factory
internal/mcpserver/jsonrpc.go             JSON-RPC 2.0 framing, error codes, batch rejection
internal/mcpserver/server.go              Server, method table, POST/GET/DELETE handling, version negotiation, sessions (2025)
internal/mcpserver/auth.go                Authenticator interface + bearer implementation
internal/mcpserver/tools.go               tools/list, tools/call + the nine vecnet tools
internal/mcpserver/resources.go           resources/list, resources/read, resources/subscribe|unsubscribe, subscriptions/listen, hints
internal/mcpserver/events.go              events/list|subscribe|unsubscribe (ChatGPT profile)
internal/mcpserver/sse.go                 shared SSE writer for 2025 GET streams and 2026 listen streams
internal/events/                          subscription store, challenge, outbox tailer worker
internal/oauth/                           authorization server + resource server + link codes
internal/hub/grokbot.go                   grokbot route kind helpers (key sealing, response classification)
internal/push/grokbot.go                  grokbot push transport
internal/ingress/github.go, ingress.go    source verification, summary, mapping to Send
internal/store/migrations/0005..0008      per task
internal/httpapi/{mcp,ingress,oauth,admin}.go  route mounting and admin additions
internal/cli/connectors.go                peer link, source add, route add --kind grokbot, subscriptions list
docs/connectors/{dots,grokbot,github}.md  operator guides with enrolment scripts
```

Dependency order: 1 (egress) and 2 (mcp core) are independent; 3, 4 need 2; 8 needs only `hub`; 5 needs 2 + 4 + 1; 6 needs 2; 7 needs 1; 9 needs 5–8; 10 last. Parallel groups for the workflow: `[1,2]`, `[3,4,8]`, `[5,6,7]`.

---

### Task 1: Egress guard

**Files:**
- Create: `internal/egress/dialer.go`, `internal/hub/seal.go`; Modify: `internal/push/worker.go` (client construction), `internal/hub/notify.go` (`defaultNotifyClient`), `cmd/vecnet-hub/main.go` (flag), `internal/hub/hub.go` (`Config.AllowPrivateEgress bool`)
- Test: `internal/egress/dialer_test.go`

**Interfaces:**
- Produces:
  ```go
  package egress
  var ErrPrivateAddress, ErrScheme, ErrRedirect, ErrUserinfo error
  type Policy struct{ AllowPrivate bool; Resolver func(ctx, host string) ([]net.IP, error) /* nil = net.DefaultResolver */ }
  func (p Policy) CheckURL(raw string) (*url.URL, error)   // https only (http allowed only when AllowPrivate), no userinfo, no fragment; host not .local/.internal/.onion/localhost; literal IPs must be public
  func (p Policy) IsPublic(ip net.IP) bool                 // rejects loopback, link-local, private (RFC1918, fc00::/7), multicast, unspecified, 100.64/10, 169.254/16, 0.0.0.0/8, 192.0.0.0/24, 198.18/15, broadcast
  func (p Policy) Client(timeout time.Duration) *http.Client // Transport.DialContext resolves host, filters IPs, dials the first public one (pinned), TLS ServerName = host; CheckRedirect returns ErrRedirect; MaxResponseHeaderBytes 64 KiB
  ```
  Also (shared by Tasks 5, 6 and 7, so it lives here to avoid parallel-merge conflicts):
  ```go
  // internal/hub/seal.go
  func (h *Hub) Seal(plaintext []byte) (string, error)   // AES-256-GCM under meta.hub_key, random 12-byte nonce, output "v1:" + base64(nonce|ciphertext)
  func (h *Hub) Unseal(sealed string) ([]byte, error)
  ```
  with tests `TestSealRoundTrip` and `TestUnsealTamperFails` in `internal/hub/seal_test.go`.
  `hub.Config.AllowPrivateEgress` feeds `egress.Policy{AllowPrivate}`; `push.NewClient` and `hub.Notify` use `Policy.Client(30*time.Second)`. `AddRoute`/`SetNotifyRoutes` call `CheckURL` and return 422 `invalid_url` on failure.

- [ ] **Step 1: Write the failing tests**

```go
func TestIsPublicTable(t *testing.T) { /* 8.8.8.8 true; 10.1.2.3, 127.0.0.1, 169.254.1.1, 100.64.0.1, ::1, fd00::1, fe80::1, 0.0.0.0 false */ }
func TestCheckURL(t *testing.T) { /* https://ok.example/x ok; http://… ErrScheme (ok with AllowPrivate); https://u:p@h/ ErrUserinfo; https://h/#f error; https://foo.local/ error; https://10.0.0.1/ ErrPrivateAddress */ }
func TestClientRefusesPrivateResolution(t *testing.T) { /* Resolver returns 10.0.0.5 for "evil.example" → Do() fails with ErrPrivateAddress and no TCP connection is attempted (count via a listener on 127.0.0.1 that must receive 0 conns) */ }
func TestClientRefusesRedirect(t *testing.T) { /* AllowPrivate=true httptest server 302 → Do returns ErrRedirect, body of the 302 is not followed */ }
func TestClientPinsIP(t *testing.T) { /* Resolver returns [10.0.0.9 (private), 127.0.0.1] with AllowPrivate=true → dials 127.0.0.1 httptest server; Host header still "evil.example" */ }
func TestPushAndNotifyUseGuard(t *testing.T) { /* in testhub with AllowPrivateEgress=false: AddRoute("http://127.0.0.1:…") → 422 invalid_url; with true → 201 (existing push tests keep passing because testhub sets AllowPrivateEgress=true) */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` (whole suite, since push/notify tests depend on the dev override) → commit `feat: public-IP-only egress guard for every outbound POST`

---

### Task 2: MCP JSON-RPC server core (dual-era)

**Files:**
- Create: `internal/mcpserver/jsonrpc.go`, `server.go`, `auth.go`, `sse.go`, `internal/httpapi/mcp.go`; Modify: `internal/httpapi/server.go` (mount `/mcp`, exempt from the bearer middleware since mcpserver authenticates itself; keep the 256 KiB body limit), `docs/adr/0001-tech-stack.md` (amendment above)
- Test: `internal/mcpserver/server_test.go` (uses `github.com/modelcontextprotocol/go-sdk/mcp` `Client` + `NewStreamableClientTransport` as the 2025-era client, and raw HTTP for 2026-era requests)

**Interfaces:**
- Produces:
  ```go
  package mcpserver
  const ProtoCurrent = "2026-07-28"; const ProtoLegacy = "2025-11-25"
  var SupportedVersions = []string{ProtoCurrent, ProtoLegacy}
  const ( ErrParse = -32700; ErrInvalidRequest = -32600; ErrMethodNotFound = -32601; ErrInvalidParams = -32602; ErrInternal = -32603; ErrCodeCallback = -32015 )
  type RPCError struct{ Code int; Message string; Data any }; func (e *RPCError) Error() string
  type Request struct{ Method string; ID json.RawMessage /* nil for notifications */; Params json.RawMessage; Meta map[string]any /* params._meta */ }
  type Principal struct{ Caller auth.Caller; OAuth *OAuthInfo /* nil for bearer */ }   // OAuthInfo{PrincipalID, ClientID string; Scopes []string; Expires time.Time} defined here, filled by Task 6
  type Authenticator interface{ Authenticate(ctx, r *http.Request) (Principal, error) } // error → 401 with WWW-Authenticate: Bearer resource_metadata="<public>/.well-known/oauth-protected-resource"
  type BearerAuth struct{ Resolver *auth.Resolver }  // vecnet secrets
  type Chain []Authenticator                           // first success wins; all fail → 401
  type Session struct{ ID string; Proto string; Principal Principal; /* 2025 only: */ stream *sseConn; created time.Time }
  type Handler func(ctx context.Context, s *Session, req *Request) (result any, err error) // err *RPCError or plain (→ -32603, logged)
  type Server struct{ Hub *hub.Hub; Auth Authenticator; PublicOrigin string; Heartbeat time.Duration /* 15 s */; SessionTTL time.Duration /* 1 h idle */; methods map[string]Handler; capabilities func(proto string) map[string]any; Now func() time.Time }
  func New(h *hub.Hub, a Authenticator, publicURL string) *Server
  func (s *Server) Register(method string, h Handler)       // panics on duplicate; Tasks 3–5 register their methods
  func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)
  // POST: Origin check (403 if present and != PublicOrigin); Content-Type application/json; Accept must list application/json (2026 also text/event-stream); body → single Request (JSON-RPC batches → -32600);
  //   method "initialize" → legacy session: validate params.protocolVersion ∈ Supported else -32602 {supportedVersions}; mint Mcp-Session-Id (ids.New-style 26 chars) and return InitializeResult{protocolVersion, capabilities, serverInfo{name:"vecnet", version}, instructions}
  //   method "server/discover" → 2026: result {protocolVersion, capabilities, serverInfo, instructions}; no session minted; MCP-Protocol-Version header must equal params._meta.protocolVersion else 400
  //   any other method: 2026 requests carry MCP-Protocol-Version: 2026-07-28 and need no session; 2025 requests carry Mcp-Session-Id (unknown → 404 {"error":"session not found"}); dispatch via methods[]; unknown → -32601
  //   "notifications/initialized" and other notifications → 202 empty body
  //   response: application/json for plain results; streaming handlers (Task 4) write text/event-stream themselves via the Streamer helper
  // GET: 2025 session stream (requires Mcp-Session-Id, Accept text/event-stream; one stream per session; a second GET closes the first); 2026 → 405
  // DELETE: 2025 ends the session → 204; 2026 → 405
  // Last-Event-ID is ignored everywhere.
  func (s *Server) Capabilities(proto string) map[string]any // {"tools":{"listChanged":false},"resources":{"subscribe":true,"listChanged":false},"events":{},"extensions":{"io.modelcontextprotocol/events":{}}}; Tasks 3–5 contribute fixed keys, so this is a static map
  type Streamer struct{ ... }; func (s *Server) Stream(w http.ResponseWriter, r *http.Request) (*Streamer, error) // text/event-stream headers (Cache-Control no-store, X-Accel-Buffering no), retry: 3000, hb every Heartbeat, Send(id string, event string, data any) error, Comment(), Close()
  ```
  `PrincipalFromContext(ctx) Principal` is set by `ServeHTTP` before dispatch; handlers use it for the `auth.Caller`.

- [ ] **Step 1: Write the failing tests**

```go
func TestDiscover2026(t *testing.T) { /* POST server/discover with MCP-Protocol-Version 2026-07-28 and bearer → result.protocolVersion 2026-07-28, capabilities has events and extensions["io.modelcontextprotocol/events"], no Mcp-Session-Id header */ }
func TestInitializeLegacyMintsSession(t *testing.T) { /* go-sdk Client Connect over NewStreamableClientTransport with bearer header → session works; ping succeeds; DELETE ends it; next POST with that id → 404 */ }
func TestInitializeOldVersionRejected(t *testing.T) { /* initialize protocolVersion 2025-03-26 → error -32602 data.supportedVersions == ["2026-07-28","2025-11-25"]; HTTP 200 (JSON-RPC error), then a server/discover on the same connection succeeds (Review Focus 1) */ }
func TestOriginAndAuth(t *testing.T) { /* Origin https://evil.example → 403; no bearer → 401 with WWW-Authenticate containing resource_metadata=; bad bearer → 401; principal credential ok */ }
func TestHeaderVersionMismatch400(t *testing.T) { /* MCP-Protocol-Version 2025-11-25 header with _meta 2026-07-28 → 400 */ }
func TestBatchRejected(t *testing.T) { /* [ {...}, {...} ] → -32600 */ }
func TestNotificationIs202(t *testing.T) { /* notifications/initialized → 202 empty */ }
func TestGetStreamLegacyHeartbeat(t *testing.T) { /* Heartbeat 50 ms; GET with session → text/event-stream, ": hb" within 500 ms; GET under 2026 → 405; Last-Event-ID ignored (no replay, no error) */ }
func TestUnknownMethod(t *testing.T) { /* -32601 */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: hand-rolled dual-era MCP server core on /mcp`

---

### Task 3: vecnet tools

**Files:**
- Create: `internal/mcpserver/tools.go`; Test: `internal/mcpserver/tools_test.go`

**Interfaces:**
- Produces: `func (s *Server) registerTools()` called from `New`; `tools/list` → `{tools:[mcp.Tool…]}` (static list, `nextCursor` absent); `tools/call` → `mcp.CallToolResult{Content:[{type:"text",text:<JSON>}], StructuredContent: <object>, IsError}`; argument validation against each tool's `inputSchema` (compiled once with jsonschema/v6) → `-32602` with `data.path`.
  | Tool | inputSchema | Handler maps to | annotations |
  |---|---|---|---|
  | `vecnet_send` | `$defs/sendRequest` from `envelope/schema.json` | `Hub.Send(ctx, caller, rawArgs, "mcp_tool")` → `SendResponse` | write |
  | `vecnet_reply` | `{message_id, text, priority, idempotency_key, attachments?, metadata?, max_depth?, expires_at?, as?}` required `message_id,text,priority,idempotency_key` | look up message → thread; build SendRequest with `thread_id`, `in_reply_to`; `Hub.Send` | write |
  | `vecnet_inbox` | `{after?: cursor, limit?: 1..200, peer?: peerId}` | `Hub.StreamOwners` + `Log.Read` → `{events, next_cursor}` | `readOnlyHint` |
  | `vecnet_ack` | `{cursor, peer?}` | `Hub.Ack` → `{}` | write |
  | `vecnet_receipt` | `{delivery_id, state: processed|read|failed, note?}` | `Hub.Receipt` → `{}` | write |
  | `vecnet_presence` | Presence body minus `peer_id` plus `as?` | `Hub.SetPresence` → `{}` | write |
  | `vecnet_threads` | `{participant?, group?, since?, limit?, cursor?}` | `Hub.ListThreads` → `{threads, next}` | `readOnlyHint` |
  | `vecnet_thread` | `{id, after?, limit?}` | `Hub.ThreadMessages` → `{messages, next_after}` | `readOnlyHint` |
  | `vecnet_peers` | `{}` | `Hub.Directory` → `{peers}` | `readOnlyHint` |
  Tool descriptions state plainly that write tools send messages on the operator's bus; no tool accepts a URL. `*hub.APIError` → `IsError: true` with text `code: message` and `StructuredContent {"error":{code,message,details}}` (a tool-level error, not a JSON-RPC error).

- [ ] **Step 1: Write the failing tests**

```go
func TestToolsListNamesAndHints(t *testing.T) { /* exactly the nine names; readOnlyHint true on inbox/threads/thread/peers only; no inputSchema property named url */ }
func TestSendReplyInboxAckRoundTrip(t *testing.T) { /* A vecnet_send hold to B (idempotency_key required: missing → -32602 path /idempotency_key); B vecnet_inbox → 1 delivery event; B vecnet_reply → A's inbox has reply with in_reply_to and same thread; B vecnet_ack → delivery accepted */ }
func TestToolErrorIsToolLevel(t *testing.T) { /* send to "Max" → CallToolResult.IsError true, structured error.code name_not_an_address, HTTP 200, no JSON-RPC error */ }
func TestPrincipalAs(t *testing.T) { /* principal bearer: vecnet_send without as → tool error as_required; with as=its peer → ok; vecnet_inbox peer= narrows */ }
func TestSchemaValidationPath(t *testing.T) { /* vecnet_receipt state "accepted" → -32602 data.path /state */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: vecnet MCP tools`

---

### Task 4: Inbox resources, `subscriptions/listen`, legacy subscribe, hints

**Files:**
- Create: `internal/mcpserver/resources.go`; Test: `internal/mcpserver/resources_test.go`

**Interfaces:**
- Produces: `func (s *Server) registerResources()`; resources: `vecnet://peer/<peer_id>/inbox` for each peer the principal may read (peer credential: its own; principal: all its peers plus `vecnet://principal/<prn_id>/inbox`); `resources/list` → those; `resources/read` → one `TextResourceContents` whose text is the JSON `{events, next_cursor}` of the last 50 events (same as `vecnet_inbox`); `resources/subscribe`/`unsubscribe` (2025 sessions; `-32602 not_subscribable` for other URIs) and `subscriptions/listen {resourceSubscriptions:[uris], toolsListChanged?, …}` (2026; streaming response: first frame `notifications/subscriptions/acknowledged` with `_meta.subscriptionId`, then `notifications/resources/updated {uri}` whenever the underlying log grows, `: hb` keep-alives, ends with the JSON-RPC result `{}` only when the client disconnects, which it will not, so effectively open until ctx done). Hint fan-out uses `eventlog.Log.Wait` on the owners behind the URI; coalesce to at most one notification per URI per 250 ms. 2025 sessions receive `notifications/resources/updated` on their GET stream (one stream per session, never broadcast).

- [ ] **Step 1: Write the failing tests**

```go
func TestResourcesListPerPrincipal(t *testing.T) { /* peer cred → 1 URI; principal with 2 peers → 3 URIs */ }
func TestResourcesRead(t *testing.T) { /* after a send: read inbox → text parses, events len 1, next_cursor matches event cursor; other peer's URI → -32602 forbidden */ }
func TestListenAckThenHints2026(t *testing.T) { /* POST subscriptions/listen (2026) → stream: first notifications/subscriptions/acknowledged; send to the peer → notifications/resources/updated with the URI within 1 s; no message content in the frame; two quick sends → ≤ 2 updated frames (coalescing not asserted strictly: at least 1, at most 2) */ }
func TestLegacySubscribeHintsOnGetStream(t *testing.T) { /* go-sdk Client with SubscribeHandler: resources/subscribe inbox URI, open GET; send → client receives ResourceUpdated for the URI */ }
func TestHintsStopOnDisconnect(t *testing.T) { /* cancel the listen request; goroutine count returns to baseline within 1 s (runtime.NumGoroutine with tolerance) */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: MCP inbox resources with listen/subscribe hints`

---

### Task 5: MCP Events profile (dots)

**Files:**
- Create: `internal/mcpserver/events.go`, `internal/events/store.go`, `internal/events/challenge.go`, `internal/events/worker.go`, `internal/store/migrations/0005_event_subscriptions.sql`, `internal/httpapi/admin_subscriptions.go`; Modify: `cmd/vecnet-hub/main.go` (start worker), `internal/testhub/testhub.go` (`StartWorkers` also starts it)
- Test: `internal/events/worker_test.go`, `internal/mcpserver/events_test.go`

**Interfaces:**
- Migration `0005`: `event_subscriptions(sub_id TEXT PK /* "sub_"+ULID */, principal_kind TEXT ('oauth'|'vecnet'), principal_id TEXT, peer_id TEXT /* recipient whose log is tailed */, name TEXT, arguments TEXT JSON, url TEXT, secret_enc TEXT /* AES-GCM under hub_key */, prev_secret_enc TEXT NULL, prev_secret_until TEXT NULL, cursor_seq INT, types TEXT JSON, expires_at TEXT NULL, verified_at TEXT NULL, paused_reason TEXT NULL, token_expires_at TEXT NULL, last_delivery_at TEXT NULL, last_status INT NULL, attempts INT, next_attempt_at TEXT NULL, created_at, updated_at)` + UNIQUE `(principal_kind, principal_id, url, name, arguments)`; `event_outbox(event_id TEXT PK /* dly_ for delivery events else "e<epoch>.<seq>" */, sub_id, body TEXT, attempts INT, next_attempt_at, created_at, done_at NULL, terminal TEXT NULL)`.
  ```go
  package events
  const Name = "vecnet.message"
  type Subscription struct{ ID, PrincipalKind, PrincipalID, PeerID, Name, URL string; Arguments map[string]any; Types []string; Cursor eventlog.Cursor; Expires *time.Time; Verified bool; PausedReason string }
  type Store struct{ DB *store.DB; Key []byte; Now func() time.Time }
  func (st *Store) Upsert(ctx, s Subscription, secret string, ttl time.Duration, tokenExp time.Time) (Subscription, created bool, err error) // key (principal, url, name, arguments); refresh keeps cursor, rotates secret (old kept 10 min in prev_secret_*), clamps expires ≤ now+24h and ≤ tokenExp, clears paused_reason
  func (st *Store) Delete(ctx, principalKind, principalID, url, name string, args map[string]any) error // idempotent
  func (st *Store) ListForPrincipal / Get / Active(ctx, now) ([]Subscription, error)
  func Challenge(ctx, client *http.Client, s Subscription, secret string) error  // POST {"type":"verification","challenge":<32 random bytes base64url>} signed (webhook.Sign, id "vrf_"+ULID) + X-MCP-Subscription-Id; expect 2xx and body {"challenge": same} compared with subtle.ConstantTimeCompare; ErrChallenge{Reason "challenge_failed"|"timeout"|"invalid_url"}
  type Worker struct{ Hub *hub.Hub; Store *Store; Log *eventlog.Log; Client *http.Client /* egress.Policy.Client */; Now func() time.Time; Interval time.Duration }
  func (w *Worker) Step(ctx) (sent int, err error)
  // for each Active subscription with verified_at set and paused_reason NULL and token_expires_at > now:
  //   read Log.Read([peer_id], cursor, 50); for each Event e: skip unless e.type ∈ types (default ["delivery"]); for delivery events skip when the delivery's priority is hold; a held wake_idle goes into the outbox parked and is pushed once on release (whole-branch review, PROTOCOL §5.2); build {eventId, name, timestamp, data: e, cursor: e.cursor} (≤ 256 KiB else data replaced by {delivery_id, message_id, thread_id, preview: first 1 KiB text, blob_url}); enqueue in event_outbox if absent; advance cursor_seq after enqueue
  //   drain outbox rows due: POST with webhook-id = eventId, webhook-timestamp, webhook-signature (dual-signed while prev_secret_until > now), X-MCP-Subscription-Id; 2xx → done; 410/413 → terminal, done_at set, terminal reason; other non-2xx/net error → attempts+1, next_attempt_at = now + hub.PushBackoff(attempts, retryAfter), after 8 → terminal "exhausted" and subscription paused_reason "delivery_failed"
  //   token_expires_at <= now → paused_reason "token_expired" (nothing dropped; cursor stays)
  func (w *Worker) Run(ctx) error
  ```
  ```go
  // mcpserver/events.go — func (s *Server) registerEvents()
  // events/list → {events:[{name:"vecnet.message", description, delivery:["webhook"], inputSchema:{peer_id?:peerId, thread_id?:threadId, types?: [enum]}, payloadSchema: $defs/event}], nextCursor: null}
  // events/subscribe {name, arguments, delivery:{mode:"webhook", url, secret}, cursor?, ttlMs?} →
  //   name must be vecnet.message (-32602); delivery.mode webhook; secret whsec_+base64 decoding to 24..64 bytes (-32602); egress.Policy.CheckURL(url) else -32015 {reason:"invalid_url"};
  //   peer_id: OAuth principal → its bound dot peer (Task 6; -32602 "not_linked" when unbound); vecnet bearer → caller's peer or arguments.peer_id (must belong to the principal); ttlMs clamp (null → 24 h);
  //   Upsert; if not verified (new, url changed, lapsed, or the pass is over 24 h old) run Challenge → on failure -32015 {reason}, keeping an existing row and its cursor; cursor param: ParseCursor else -32602; "head"/absent on create → Log.Current; on refresh → stored cursor unless a param is given and ≥ stored? No: a given cursor always wins (ChatGPT passes its last saved one); if the cursor is below retention → truncated:true and cursor = oldest
  //   result {id: sub_id, refreshBefore: expires_at - 10 min (ISO), cursor: <string>, truncated}
  // events/unsubscribe {name, arguments, delivery:{mode,url}} → Delete → {}
  ```
  Admin: `GET /v1/admin/subscriptions` → `[{sub_id, principal, peer_id, url (host only), name, cursor, expires_at, verified_at, paused_reason, last_delivery_at, last_status, attempts}]`; `POST /v1/admin/subscriptions/{id}/resume` clears `paused_reason` (operator nudge).

- [ ] **Step 1: Write the failing tests** (callback receiver = `httptest.Server`; testhub runs with `AllowPrivateEgress=true`)

```go
func TestEventsListShape(t *testing.T) { /* one event vecnet.message, delivery ["webhook"], inputSchema has peer_id/thread_id/types, payloadSchema has cursor/type/at/data */ }
func TestSubscribeChallengeAndDeliver(t *testing.T) { /* receiver echoes challenge; subscribe → id sub_…, refreshBefore ≈ now+23h50m, truncated false; send hold then wake to the peer → exactly 1 POST (wake only), headers webhook-id == delivery_id, X-MCP-Subscription-Id == id, signature verifies with the secret, body.name vecnet.message, body.cursor == event cursor, body.data validates as $defs/event */ }
func TestSubscribeRejectsBadUrlAndSecret(t *testing.T) { /* url http://, https://10.0.0.1/, https://u@h/, hostname→private resolver → -32015 reason invalid_url and receiver got 0 requests (Review Focus 2); secret too short → -32602 */ }
func TestChallengeFailure(t *testing.T) { /* receiver returns wrong challenge → -32015 reason challenge_failed; returns 500 slowly (> client timeout 2 s in test) → reason timeout */ }
func TestRefreshKeepsCursorRotatesSecret(t *testing.T) { /* subscribe; deliver 2; subscribe again same key new secret with cursor param = first event cursor → next POST carries two signatures (old+new) and starts after the given cursor */ }
func TestOutageQueuesAndDrains(t *testing.T) { /* receiver 503 ×3 then 200: same eventId each attempt, backoff via FakeClock, delivered once; 410 → terminal, never retried, subscription paused delivery_failed only after 8 exhausted (so 410 alone does not pause) */ }
func TestTokenExpiryPauses(t *testing.T) { /* token_expires_at in 1 h; advance 2 h; sends → 0 POSTs, paused_reason token_expired; re-subscribe with fresh token → drains from cursor (Review Focus 3) */ }
func TestTypesFilter(t *testing.T) { /* default: receipt events not pushed; types ["delivery","receipt"] → pushed */ }
func TestOversizePayloadPreview(t *testing.T) { /* delivery with 16 attachments and long metadata still ≤ 256 KiB (normal); synthetic event > 256 KiB → data replaced by preview object */ }
func TestUnsubscribeIdempotent(t *testing.T) { /* twice → {} both; no more POSTs */ }
func TestAdminSubscriptions(t *testing.T) { /* lists host only (no path/secret); resume clears paused */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: MCP Events webhook profile with log-tailing subscriptions`

---

### Task 6: OAuth 2.1 authorization + resource server, dot linking

**Files:**
- Create: `internal/oauth/{server.go,tokens.go,pkce.go,clients.go,consent.go,verify.go}`, `internal/store/migrations/0006_oauth.sql`, `internal/httpapi/oauth.go`; Modify: `internal/httpapi/server.go` (mount), `internal/mcpserver/auth.go` (`Chain{BearerAuth, oauth.TokenAuth}`), `internal/hub/admin.go` (`DotLink`), `cmd/vecnet-hub/main.go`
- Test: `internal/oauth/oauth_test.go`, `internal/httpapi/oauth_test.go`

**Interfaces:**
- Migration `0006`: `oauth_clients(client_id TEXT PK, kind ('dcr'|'cimd'|'static'), metadata TEXT JSON, redirect_uris TEXT JSON, created_at)`; `oauth_codes(code_hash PK, client_id, redirect_uri, scope, code_challenge, resource, principal_id, expires_at, used_at NULL)`; `oauth_tokens(token_hash PK, kind ('access'|'refresh'), client_id, principal_id, scope, resource, expires_at, revoked_at NULL, rotated_from NULL, created_at)`; `oauth_principals(prn_id TEXT PK /* a vecnet principal row is created for each ChatGPT account */, subject TEXT UNIQUE /* "oauth:"+client_id+":"+random stable id issued at first consent */, peer_id TEXT NULL /* bound dot peer */, created_at)`; `dot_links(code_hash PK, peer_id, expires_at, used_at NULL)`.
  ```go
  package oauth
  type Server struct{ DB *store.DB; Issuer string /* public URL */; Now func() time.Time; AccessTTL, RefreshTTL time.Duration /* 1h, 30d */; AllowedRedirects []string /* exact chatgpt.com URIs + prefix http://127.0.0.1: in tests */; Egress egress.Policy /* CIMD fetch */ }
  func (s *Server) Mount(mux *http.ServeMux)
  // GET  /.well-known/oauth-authorization-server → RFC 8414 {issuer, authorization_endpoint, token_endpoint, registration_endpoint, response_types_supported ["code"], grant_types_supported ["authorization_code","refresh_token"], code_challenge_methods_supported ["S256"], token_endpoint_auth_methods_supported ["none"], scopes_supported ["vecnet"], client_id_metadata_document_supported true}
  // GET  /.well-known/oauth-protected-resource → RFC 9728 {resource: Issuer+"/mcp", authorization_servers [Issuer], scopes_supported, bearer_methods_supported ["header"]}
  // POST /oauth/register → RFC 7591 DCR: stores metadata, returns {client_id, redirect_uris, ...}; redirect_uris must all be allowed
  // GET  /oauth/authorize?response_type=code&client_id&redirect_uri&code_challenge&code_challenge_method=S256&state&resource&scope → consent HTML page (embedded, CSP, no inline) asking for an 8-char link code (first login) or showing "Approve" (returning principal, identified by a signed cookie `vecnet_oauth_sub` set at first consent, HttpOnly, 30 d); client_id that is an https URL → CIMD: fetch metadata via Egress client, cache 1 h, validate redirect_uri ∈ its redirect_uris
  // POST /oauth/authorize (form: link_code?, approve=1) → validates link code (Hub.ConsumeDotLink) → creates/loads oauth_principals + vecnet principal (name "dot:"+peer name) and binds peer_id; mints code (10 min, single use) → 302 redirect_uri?code&state
  // POST /oauth/token (grant_type=authorization_code: code, redirect_uri, client_id, code_verifier; or refresh_token) → {access_token (opaque "vat_"+random), token_type "Bearer", expires_in 3600, refresh_token ("vrt_"+random), scope}; refresh rotates (old revoked, rotated_from set); PKCE verified with S256; resource must equal Issuer+"/mcp" else error invalid_target; errors per RFC 6749 JSON
  type TokenAuth struct{ S *Server }   // implements mcpserver.Authenticator: looks up access token hash, checks expiry/revocation/resource, returns Principal{Caller: auth.Caller{Kind:"principal", PrincipalID: prn_id, PeerID: bound peer or ""}, OAuth: &OAuthInfo{...}}
  ```
  Hub additions: `Hub.DotLink(ctx, peerID string) (code string, err error)` (peer must be kind `dot`; 8 chars from the alphabet above; 10 min; audit `dot_link`), `Hub.ConsumeDotLink(ctx, code string) (peerID string, err error)`; admin `POST /v1/admin/dot-links {"peer_id"}` → `{"code","expires_at"}`. Events (Task 5) resolve an OAuth principal's peer through `oauth_principals.peer_id`; tools called with an OAuth token act `as` that peer automatically (`Caller.PeerID` set), so no `as` is needed.

- [ ] **Step 1: Write the failing tests**

```go
func TestMetadataDocuments(t *testing.T) { /* both well-known docs; S256 advertised; resource == issuer+"/mcp" */ }
func TestDCRAndFullCodeFlow(t *testing.T) { /* register with chatgpt redirect; authorize GET → 200 HTML with link_code field; POST wrong code → 400 page; operator creates dot peer + dot-link; POST code → 302 with code&state; token exchange with correct verifier → access+refresh; wrong verifier → invalid_grant; code reuse → invalid_grant */ }
func TestTokenAuthOnMcp(t *testing.T) { /* access token on POST /mcp server/discover → 200; vecnet_send via tools → message sender == bound dot peer; expired (FakeClock +2h) → 401 WWW-Authenticate with resource_metadata */ }
func TestRefreshRotation(t *testing.T) { /* refresh → new pair; old refresh reused → invalid_grant and the new pair is revoked (reuse detection) */ }
func TestReturningPrincipalSkipsLinkCode(t *testing.T) { /* second authorize with the consent cookie → page shows Approve without link_code; approving binds to the same peer */ }
func TestCIMD(t *testing.T) { /* client_id = https URL served by httptest (AllowPrivate) returning client metadata with the redirect_uri → authorize proceeds; metadata without the redirect → 400 invalid_request */ }
func TestResourceMismatch(t *testing.T) { /* token request resource=https://other/mcp → invalid_target */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: OAuth 2.1 AS/RS with PKCE, DCR, CIMD and dot link codes`

---

### Task 7: Grok Bot route kind

**Files:**
- Create: `internal/hub/grokbot.go`, `internal/push/grokbot.go`, `internal/store/migrations/0007_grokbot_routes.sql`; Modify: `internal/hub/routes.go` (`AddRoute` kind param), `internal/push/worker.go` (dispatch by kind), `internal/httpapi/admin.go` (`routes` body gains `kind`, `key`)
- Test: `internal/push/grokbot_test.go`

**Interfaces:**
- Migration `0007`: recreate `routes` with `kind CHECK (kind IN ('http','grokbot'))`, add `key_enc TEXT NULL`, `runs_per_hour INT NOT NULL DEFAULT 200`; add `route_runs(route_id, at)` for budgeting.
  ```go
  // hub
  func (h *Hub) AddGrokbotRoute(ctx, ownerKind, ownerID, routineURL, key string) (routeID string, err error) // URL regex + key prefix check (422 invalid_url / invalid_key); key sealed with AES-GCM(hub_key); verified_at = now (no challenge exists for routines)
  func (h *Hub) GrokbotKey(ctx, routeID string) (string, error)         // unseal at send time; never logged
  func (h *Hub) GrokbotBudgetOK(ctx, routeID string, now) (bool, time.Duration) // < runs_per_hour in the trailing hour; else false + wait
  func ClassifyGrokbot(status int, body []byte) (outcome string) // "started" (200 + "success":true), "retry" (400 containing "rate limit", 408, 429, 5xx), "terminal_alert" (401, 400 containing "disabled"), "terminal" (other)
  // push
  func (w *Worker) pushGrokbot(ctx, d hub.DueDelivery) // POST d.Body (the Delivery JSON) to the routine URL with Authorization: Bearer <key> and X-Automation-Key: <key>; Content-Type application/json; via the egress client; no signature headers; budget check first → requeue with next_attempt_at = now + wait when exhausted (not an attempt)
  ```
  `PushResult` maps: `started` → accepted; `retry` → as 503 today; `terminal_alert` → failed + counter `push_auth_failed` + notification class `dead_letters`; `terminal` → dead_letter. `DueDelivery` gains `Kind string`. Hold deliveries are never pushed (existing rule); the Bot pulls them via `/mcp` with its peer bearer.

- [ ] **Step 1: Write the failing tests**

```go
func TestGrokbotRouteValidation(t *testing.T) { /* bad URL → 422 invalid_url; key without crsr_ → 422 invalid_key; good → route verified immediately; GET /v1/admin/routes never returns the key */ }
func TestGrokbotPushHeadersAndBody(t *testing.T) { /* fake routine (AllowPrivate, URL regex relaxed via a test hook hub.GrokbotURLPattern) returns 200 {"success":true,"runUuid":"x"} → Authorization Bearer crsr_…, X-Automation-Key, body validates as $defs/delivery, delivery accepted */ }
func TestGrokbotClassification(t *testing.T) { /* table: 200 success → started; 400 "Automation run rate limit exceeded" → retry (Review Focus 5); 400 "Automation x is disabled" → terminal_alert; 401 → terminal_alert; 404 → terminal; 503 → retry */ }
func TestGrokbotBudget(t *testing.T) { /* runs_per_hour=2: third delivery not posted until FakeClock +1h; attempt counter unchanged while waiting */ }
func TestGrokbotKeyNeverLogged(t *testing.T) { /* capture slog output during a push; assert the key string is absent */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: grokbot push route kind`

---

### Task 8: GitHub inbound webhook sources

**Files:**
- Create: `internal/ingress/ingress.go`, `internal/ingress/github.go`, `internal/store/migrations/0008_sources.sql`, `internal/httpapi/ingress.go`; Modify: `internal/httpapi/server.go` (mount, public path), `internal/hub/admin.go`, `internal/hub/directory.go` (sources listed as kind `source`)
- Test: `internal/ingress/github_test.go`, `internal/httpapi/ingress_test.go`

**Interfaces:**
- Migration `0008`: `sources(src_id TEXT PK, kind ('github'|'standard_webhooks'|'token'), name, target_id /* peer_|team_|thr_ */, secret_hash /* github: HMAC key kept sealed (secret_enc) since verification needs the plaintext */, secret_enc, event_allow TEXT JSON NULL, priority TEXT DEFAULT 'wake_idle', sends_per_min INT 60, created_at, disabled INT)`; `source_deliveries(src_id, provider_id, msg_id, at)` PK `(src_id, provider_id)`; `source_threads(src_id, origin_thread_id, thr_id)` PK `(src_id, origin_thread_id)`.
  ```go
  // hub
  func (h *Hub) AddSource(ctx, kind, name, targetID string, eventAllow []string, priority string) (srcID, ingressURL, secret string, err error) // target must exist (404 unknown_recipient); secret 32 random bytes hex
  type SourceMessage struct{ SrcID string; ProviderDeliveryID string; OriginThreadID string; OriginEventID string; EventName string; Title string; Summary string; Raw []byte; MediaType string }
  func (h *Hub) IngestSource(ctx, m SourceMessage) (msgID string, dropped bool, err error)
  // dedupe (src_id, provider_id) → existing msg_id; event allowlist → dropped "event_filtered"; per-source rate limit → 429 rate_limited; thread: source_threads lookup or create (title from m.Title, participants = resolved target peers (team → members; thread → its participants), creator = src_id); Raw → blob (uploader src_id, media_type application/json, name "github-event.json"); SendRequest{recipients:[target or thread], priority, content.text Summary, attachments [raw blob], origin {system: kind, thread_id: OriginThreadID, event_id}, metadata {"vecnet.source_kind","vecnet.event"}} sent with sender = src_id, ingress "inbound_webhook", loop depth 0 chain [src_id]; sources never receive deliveries
  ```
  ```go
  package ingress
  type GitHubEvent struct{ Delivery, Event, Action string; Repo string; Number int; Title, Actor, URL string }
  func VerifyGitHub(secret string, sigHeader string, body []byte) bool    // "sha256=" + hex HMAC, hmac.Equal
  func ParseGitHub(event string, body []byte) (GitHubEvent, error)        // issues, issue_comment, pull_request, pull_request_review, pull_request_review_comment, push (Number 0 → origin thread "<repo>@<ref>"), release, workflow_run; unknown events → Number 0, origin "<repo>#<event>"
  func SummaryGitHub(e GitHubEvent) string   // "[repo] event.action on #N by actor: \"title\" url", ≤ 4096 bytes (rune-safe cut)
  ```
  HTTP `POST /v1/ingress/{src_id}`: no bearer; 256 KiB cap; kind github → headers `X-Hub-Signature-256`, `X-GitHub-Event`, `X-GitHub-Delivery` required (400 `invalid_body`), signature → 401 `bad_signature`; `ping` event → 200 `{"ok":true}`; accepted → 202 `{"message_id"}`; dropped → 202 `{"dropped":true,"reason":"event_filtered"}`; redelivery → 202 original id. Admin `POST /v1/admin/sources`, `GET /v1/admin/sources`, `POST /v1/admin/sources/{id}/disable`.

- [ ] **Step 1: Write the failing tests** (fixtures: real-shaped GitHub `issue_comment.created` and `pull_request.opened` JSON in `testdata/`)

```go
func TestVerifyGitHub(t *testing.T) { /* correct sig true; wrong false; constant-time (no early return observable: both paths run hmac) */ }
func TestIngressHappyPath(t *testing.T) { /* source → team T; POST signed issue_comment → 202 message_id; team members' logs: delivery with sender src_…, origin {system github, thread_id "o/r#196", event_id = X-GitHub-Delivery}, metadata vecnet.source_kind/vecnet.event, attachment github-event.json whose blob equals the raw body, priority wake_idle, thread title "o/r#196: <issue title>", sender_chain [src_…] */ }
func TestIngressDedupeAndOversize(t *testing.T) { /* same X-GitHub-Delivery twice → same message_id, one message row; 300 KiB → 413 before parse (Review Focus 4) */ }
func TestIngressSameIssueSameThread(t *testing.T) { /* two comments on #196 → one thread, message_count 2; a comment on #197 → new thread */ }
func TestIngressFilterAndSignature(t *testing.T) { /* allowlist ["pull_request"] → issue_comment → 202 dropped event_filtered; bad sig → 401 bad_signature; missing headers → 400; ping → 200 */ }
func TestSourceInDirectoryNotAddressable(t *testing.T) { /* GET /v1/peers lists src_ kind source; send to src_… → 404 unknown_recipient */ }
func TestSourceRateLimit(t *testing.T) { /* sends_per_min 2 → third → 429 */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: GitHub inbound webhook sources`

---

### Task 9: CLI and admin additions

**Files:**
- Create: `internal/cli/connectors.go`; Modify: `internal/cli/commands.go`, `internal/httpapi/admin.go`
- Test: `internal/cli/connectors_test.go`

**Interfaces:**
- CLI: `vecnet peer link <peer>` → prints the 8-char code and expiry (`POST /v1/admin/dot-links`); `vecnet source add --kind github --name N --route <id> [--events a,b] [--priority wake_idle]` → prints ingress URL and secret once; `vecnet source list`; `vecnet route add --owner <id> --url U` (http, unchanged) and `vecnet route add --owner <id> --kind grokbot --url U --key-file PATH` (reads the `crsr_` key from a 0600 file, never from argv); `vecnet subscriptions list` (dots health table: peer, host, cursor, expires, paused, last status); `vecnet subscriptions resume <id>`. `--json` on all.

- [ ] **Step 1: Write the failing tests**

```go
func TestPeerLinkPrintsCode(t *testing.T) { /* code matches ^[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{8}$; --json has expires_at */ }
func TestSourceAddPrintsURLAndSecret(t *testing.T) { /* URL ends /v1/ingress/src_…; secret 64 hex; list omits secret */ }
func TestRouteAddGrokbotKeyFile(t *testing.T) { /* --key-file with crsr_ key → route created; key via argv flag does not exist (usage error); 0644 file → warning on stderr, still accepted */ }
func TestSubscriptionsList(t *testing.T) { /* after a Task 5 subscribe: one row with host only */ }
```

- [ ] **Step 2–5:** FAIL → implement → `ok` → commit `feat: CLI for dot links, sources, grokbot routes, subscriptions`

---

### Task 10: Docs and connector guides

**Files:**
- Create: `docs/connectors/dots.md`, `docs/connectors/grokbot.md`, `docs/connectors/github.md`; Modify: `docs/PROTOCOL.md` §4.1 (library line → hand-rolled; cite the ADR amendment), §15 (new reason codes: `invalid_url`, `invalid_key`, `not_linked`, `bad_signature` already present, `push_auth_failed` counter), `docs/DEPLOY.md` (public HTTPS is required for dots; `VECNET_ALLOW_PRIVATE_EGRESS` is dev-only), `README.md` (connectors section), `SPEC.md` §16 status.
- Test: `just validate-protocol` still passes; `go test ./...` unchanged.

**Interfaces:** each guide has: prerequisites, exact operator commands (`vecnet peer add --kind dot --name …`, `vecnet peer link`, ChatGPT plugin URL = `<public>/mcp`, the dot instruction script from §13.4; Grok Bot: create the `inbox-wake` routine in chat, copy URL + key to a 0600 file, `vecnet route add --kind grokbot …`, configure the Bot's MCP settings with the hub `/mcp` URL and its peer bearer; GitHub: `vecnet source add`, paste URL + secret into the repo webhook settings with content type `application/json`), what the bot receives (one example body), how it replies, and failure signals (`vecnet subscriptions list`, dead letters, counters).

- [ ] **Step 1: Write the guides and doc edits**
- [ ] **Step 2: Run `just validate-protocol` and `build2 go test -p 2 ./...`** — Expected: both pass
- [ ] **Step 3: Commit** — `docs: connector guides, PROTOCOL/ADR amendments for phase 2`

---

## Self-review notes

- **Spec coverage.** PROTOCOL §4.1–4.3 → Tasks 2–4; §4.4 → Task 5; §4.5 → Task 6; §13.3 (hub side) → Task 7; §10 + §2.11 → Task 8; §14.7 → Task 1; §13.7 additions → Task 9. §13.5 openclaw is a separate plan. SPEC §16 phase 2 "notification webhooks" already landed in phase 1.
- **Deliberate deviations from the research notes:** CIMD is implemented minimally (fetch + redirect check) because ChatGPT prefers it but DCR also works; the Secure MCP Tunnel is unsupported (R14).
- **Type consistency.** `mcpserver.Principal` wraps `auth.Caller`; `events.Subscription.Cursor` is `eventlog.Cursor`; Grok Bot and Events both reuse `hub.PushBackoff` and `webhook.Sign`.
- **Known risk.** Task 2's 2025-era session handling is tested with the official go-sdk client, which is the best available conformance check without ChatGPT itself; the first live dot test is an operator step after deploy (ticket 19 said a dot on webhook2mcp works, so the profile is right).

--- SUMMARY ---

- **What this builds:** the connector layer of vecnet: a hand-rolled dual-era `/mcp` (tools, inbox resources, `subscriptions/listen` hints), the MCP Events webhook profile as a tailer of the existing per-peer log, an OAuth 2.1 server with PKCE/DCR/CIMD and dot link codes, the Grok Bot routine push route, GitHub inbound sources, a public-IP-only egress guard for every outbound POST, CLI/admin commands and operator guides.
- **Key decision:** the go-sdk cannot host MCP Events (unknown methods rejected before middleware; no top-level `events` capability), so `/mcp` is hand-rolled and the SDK stays as a types dependency and test client. Recorded as an ADR 0001 amendment.
- **Design that keeps it small:** a dot subscription's cursor is the recipient's log cursor, so replay, outage queueing and refresh need no new storage beyond the subscription row and a small outbox; OAuth principals are vecnet principals bound to a dot peer, so tools and events need no special casing.
- **Parallelism:** tasks are numbered so `[1,2]`, `[3,4,8]` and `[5,6,7]` run side by side in worktrees (migrations pre-numbered 0005–0008 per task), then 9 and 10.
- **Deferred:** openclaw channel plugin (TypeScript, own plan), operator UI for subscription health (phase 3), escalation (phase 4).
