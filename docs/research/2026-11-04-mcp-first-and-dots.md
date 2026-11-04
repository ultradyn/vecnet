# MCP first, and OpenAI dots: audit and plan outline

_Researched 2026-11-04 by agent `mcp-audit` (branch `research/mcp-first`). Max's direction, 2026-11-04: agents should prefer MCP to the CLI, because some agents (OpenAI dots) can only use MCP; webhook2mcp already bridges Grok Bots to a dot._

_Status, 2026-11-05: implemented by the MCP-first plan ([docs/plans/2026-11-04-mcp-first.md](../plans/2026-11-04-mcp-first.md)): 21 MCP tools including `vecnet_whoami`, groups, presence subscription, peer provisioning and blob put/get, `securitySchemes` and RFC 9207 `iss`, `initialize` negotiation, `resultType`, the `vecnet mcp` relay, hook `--mcp` and `hook install --mcp`, and the Events counters. The gaps below describe the hub before that work; PROTOCOL §4, §13.4, §13.7 and §13.9 are current._

**Tags.** **[O]** official OpenAI docs, re-read 2026-11-04. **[S]** MCP spec. **[L]** Max's local code or ops notes. **[V]** this repo at `ed0f65d`. **[I]** inference: not stated by any source.

## Answer in brief

- **Can a dot use vecnet today? No.** The reason is deployment, not protocol. The only public origin in the docs, `https://vecnet.fly.dev`, does not resolve (checked 2026-11-04), and `docs/connectors/dots.md:5` says no dot has ever connected to vecnet's `/mcp`. The profile code is complete and smoke-tested with fake ChatGPT callbacks (`cmd/vecnet-hub/smoke_test.go:403`). The same OpenAI profile works live on webhook2mcp with a real dot (ticket 19). **[I]** A dot would most likely work for plain text once the hub is public. Attachments would not work at all (gap G3).
- **Dots need only MCP plus MCP Events, delivered by webhook.** ChatGPT does not support resource subscriptions, `subscriptions/listen` or streams. vecnet's §4.4 matches what OpenAI documents. A few details used by the proven webhook2mcp setup are missing from vecnet: a profile tool, per-tool `securitySchemes`, `outputSchema`, `resultType`, and RFC 9207 `iss` in the authorization response.
- **MCP lacks parity with the CLI** for attachments, groups, presence subscription, whoami and issue-28 peer provisioning. No installer configures vecnet's MCP server for Claude Code or Codex, and every hint the hook prints names only CLI commands.
- **Wake stays outside MCP for Claude Code and Codex.** Neither acts on `notifications/resources/updated`, so the hook, `watch` and the app-server engine remain the wake path. MCP-first should mean: **wake through the hook or connector, act through MCP tools, and fall back to the CLI.**

---

## 1. OpenAI dots and how they reach MCP servers

### What a dot is [O]

"Your dot is an always-on agent that keeps work moving across your tools and projects" ([dots](https://learn.chatgpt.com/docs/dots)).

- **Plans.** Pro 100/200/500 (18+, outside the EEA, UK and Switzerland), Business Premium, and Enterprise. On Enterprise, dots are "off by default and must be enabled by a workspace administrator."
- **Channels.** Apps, browser, voice, Slack and Teams, plus US texting in beta.
- **No inbound API.** A dot has no API, email or webhook inbox.
- **Wake triggers.** Messages, self-scheduled wake-ups, saved schedules, and "events" from connected sources (same page; [tasks and memory](https://learn.chatgpt.com/codex/dots/tasks-and-memory)).
- **Plugins.** A dot uses "supported plugins installed and enabled for your account". "Your existing ChatGPT app permissions apply to your dot" ([computers and apps](https://learn.chatgpt.com/codex/dots/computers-and-apps)). The dots pages never say "custom MCP server".
- **The link between dots and MCP Events** is a single line on the MCP Events page. Events are available in Work chats on web, in desktop Work chats with Cloud selected, "and with [dots]" ([MCP Events](https://developers.openai.com/plugins/build/mcp-events)).

### Transport [O]

- **Versions.** ChatGPT speaks "Streamable HTTP using `initialize`" or "MCP 2.0 HTTP (protocol version `2026-07-28`)" with `server/discover`. The page's `initialize` example uses `2025-03-26`. No other versions are listed (MCP Events).
- **Network.** A public HTTPS endpoint, normally `/mcp`, or the Secure MCP Tunnel. SSE is not mentioned for new servers ([connect](https://developers.openai.com/plugins/deploy/connect-chatgpt)).
- **Tunnel limits.** 10 MiB per message, a 120 s absolute deadline, and 600 requests/min. "The authorization server itself is not automatically tunneled." Events over the tunnel are **undocumented** ([tunnels](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)).

### Auth [O] ([auth](https://developers.openai.com/plugins/build/auth))

- **Flow.** OAuth 2.1 authorization code with PKCE. Metadata must advertise `S256`, or "MCP servers are unsupported".
- **No machine-to-machine path.** "ChatGPT does **not** support machine-to-machine OAuth grants such as client credentials, service accounts, or JWT bearer assertions". It also cannot present custom API keys.
- **Client registration.**
  - CIMD is preferred. The stable client id is `https://chatgpt.com/oauth/client.json` when the authorization server supports issuer identification, otherwise `https://chatgpt.com/oauth/{callback_id}/client.json`. Token auth is `none` or `private_key_jwt`.
  - DCR is supported, registered once per connection. Predefined clients are also supported.
- **Redirects.** The stable redirect `https://chatgpt.com/connector_platform_oauth_redirect` requires `authorization_response_iss_parameter_supported: true` and an `iss` in every response (RFC 9207). Otherwise the redirect is `https://chatgpt.com/connector/oauth/{callback_id}`.
- **Resource server.**
  - RFC 9728 metadata, a `401` with `WWW-Authenticate: Bearer resource_metadata=…`, and `resource=` on both the authorize and token requests.
  - Validate `iss`, `exp`/`nbf`, `aud` and scopes on every call.
  - "Both the tool's `securitySchemes` and the resource metadata are needed to show the linking UI."
- **Token lifetimes.** Not specified: "Access and refresh tokens can still expire or rotate normally."
- **Optional profile tool.** Read-only, empty arguments, `_meta["openai/profile"]: true`, returning a stable `id` in `structuredContent` and as JSON text. Without it, "account labels, recognition, or duplicate detection may be less reliable."
- **Optional transport checks.** OpenAI-managed mTLS client certificate (SAN `mtls.prod.connectors.openai.com`) and published egress IP ranges.

### MCP features ChatGPT uses with a dot [O]

| Feature | Status | Source |
|---|---|---|
| Tools (`tools/list`, `tools/call`) | Used. A dot replies by calling tools. Write tools are confirmation-gated by default unless `readOnlyHint`. Dots add a "Review determines whether your dot can proceed, needs your approval, or must hand a step over to you" layer | [custom MCP](https://developers.openai.com/api/docs/guides/custom-mcp-server), dots |
| MCP Events: `events/list`, `events/subscribe`, `events/unsubscribe` | Used, **webhook mode only** | MCP Events |
| Polling, event streams, draft `gap`/`terminated` | "Not supported" | MCP Events |
| Resources, `resources/subscribe`, `subscriptions/listen`, prompts, server notifications | Not mentioned for push. **[I]** None of them wakes a dot | — |

### Events profile details [O] (MCP Events)

- **Capability.** `capabilities.events: {}`, in the `initialize` result or in `server/discover`.
- **`events/subscribe`.**
  - Params: `name`, `arguments`, `delivery {mode:"webhook", url, secret}`, `cursor`, `ttlMs?`.
  - Result: `{id, refreshBefore, cursor, truncated}`.
  - The id is deterministic, creation is idempotent, and arguments are compared as canonical JSON.
- **TTL.** `ttlMs: null` asks for no expiry, and only then may `refreshBefore` be null. "Stop delivery after it passes."
- **Secret.** `whsec_` plus base64 that decodes to 24–64 bytes.
- **Verification.** A signed `{"type":"verification","challenge"}` POST that must be echoed with a 2xx and compared in constant time. A pass may be cached per principal and URL. Failure is JSON-RPC `-32015` with `data.reason`. Block private addresses and don't follow redirects, for verification and delivery alike.
- **Delivery.**
  - One event per POST, body at most **262,144 bytes**: `{eventId, name, timestamp, data, cursor}`.
  - Headers: `webhook-id` (= `eventId`), `webhook-timestamp`, `webhook-signature` and `X-MCP-Subscription-Id`.
  - A 2xx means receipt; "processing is asynchronous".
  - Retry with backoff, keeping the event id. Never retry 410 or 413.
  - Order is not guaranteed, so write tools must be idempotent.
- **How a dot wakes: push.**
  - "ChatGPT receives the event in the subscribed chat and follows the user's instructions."
  - Events "can be grouped … into one task run according to the task's batching settings".
  - The user creates the subscription by asking in chat. No API does it.
- **Refresh.** ChatGPT calls `events/subscribe` again before `refreshBefore`, "using the same subscription identity and the last saved cursor."
- **Payload.** Keep fields in `data`. For large records send a summary plus a read tool. Put no model instructions in the payload.

### Facts versus inferences

- **Documented:** everything above tagged [O].
- **[L] Max, ticket 19 (`.scratch/vecnet/issues/19-dots-live-test.md`):** a real dot installed webhook2mcp as a custom MCP plugin, subscribed, received events and replied through write tools unattended.
- **[I] Inferred from webhook2mcp running with 1 h access tokens and its subscriptions still `active` across several deploys (`~/.llm-general/lan-swarm/operations/webhook2mcp-nas.md:137-199`):** ChatGPT refreshes subscriptions with fresh tokens, at least hourly.
- **Still unknown:**
  - where an event lands in the dot UI, and how batching behaves for a dot;
  - whether a confirmation prompt ever appears for a write tool;
  - whether events work through the Secure MCP Tunnel;
  - whether ChatGPT tries `initialize` before `server/discover`. **[I]** webhook2mcp serves only `2026-07-28` and works, so ChatGPT must reach `server/discover`.

## 2. webhook2mcp

**Location.** `~/src/w2m-ops-admin`, a clone of the private `max/w2m-ops-admin` repo on the LAN forge, on branch `feat/ops-admin-cli` at `639f742`. The production repo is `xpp-tm/xpp-server-gpt-webhook2mcp`, staged on x-game and deployed on the NAS. Prod release `064fffca` serves `https://gb2cxc.xpp.exchange/mcp`, with the IdP at `oauth.xk.io`.

### How it bridges Grok Bots and a dot [L]

```text
Grok Bot / sender ──signed Standard-Webhooks POST──▶ /webhooks/<source_id>   (webhook2mcp)
        ▼ SQLite txn: replay id, history, notification backlog
webhook2mcp ──MCP Events webhook (webhook.received)──▶ ChatGPT callback ──▶ dot "Mira"
dot ──tools/call send_message|reply_to_message {route_id, content, idempotency_key}──▶ webhook2mcp /mcp
webhook2mcp ──signed POST, route-bound key──▶ w2m-receiver (/messages[/<sender>]) ──▶ Cursor automation webhook (wakes the Grok Bot)
```

- **Sources:** README.md "Flow" steps 1–6, and `docs/grokbot-contract.md` (receiver contract v1: 12-field envelope, verification challenge, ±300 s, at most 8 signatures, dedupe on `delivery_id`, 409 on conflicting bytes).
- **Return path:** a separate service, `w2m-receiver`, with one container per sender member (`~/.llm-general/lan-swarm/operations/w2m-receiver-nas.md`). Each forwards to the bot's `WEBHOOK_URL`/`WEBHOOK_AUTH`.

**MCP surface.**

- **Transport and era.** MCP 2.0 only (`2026-07-28`: `server/discover`, no `initialize`). It enforces the `Mcp-Method`/`MCP-Protocol-Version` header mirrors (`-32020`), the required `_meta` (`-32602`) and `resultType: "complete"` (`docs/mcp-examples.md`, `docs/research.md` table).
- **Events.** Event `webhook.received {source_id}`.
- **Tools.**
  - Every tool has `outputSchema`, `structuredContent` and per-tool `securitySchemes: [{type:"oauth2", scopes:[events:read|events:write]}]` (`src/worker.ts:280-291`, `selfhost/mcp-communication.ts:51`).
  - `get_profile` carries `_meta["openai/profile"]: true` (`src/worker.ts:283`).
  - Large records go out as a compact event plus an authorised paged read tool, `get_webhook_payload` (16 KiB base64 pages).
- **Auth.**
  - An external IdP, `node-oidc-provider` 9.13.0.
  - DCR, public clients (`none`), required PKCE, resource indicators.
  - ES256 JWT access tokens with audience `https://gb2cxc.xpp.exchange/mcp`, scopes `events:read events:write`, **3600 s TTL** (`integrations/oidc-provider/test/provider-flow.node.mjs:41-43`).
  - Subscriptions are clamped to 60 s–24 h and to the token expiry.
- **Wake.** Push only: MCP Events webhooks through pinned-IP egress, 8 attempts, terminal on 410/413.

### What is proven

| Claim | Evidence | Status |
|---|---|---|
| ChatGPT connects with OAuth (DCR) and subscribes | ops doc lines 103-111 (2026-10-25) | Proven live |
| Signed events are accepted by ChatGPT (2xx, attempt 1) | ops doc lines 137-199, one check per deploy | Proven live |
| A dot acts on events and replies to Grok Bots through tools | Max, ticket 19 | Proven (by statement) |
| `reply_to_message` | failed with `reply_route_unavailable` (NULL reply route) on 2026-10-26; per-sender routes were added later | Configuration bug, not a protocol one |
| Bidirectional admin console, Grok Bot receiver round trip | README says "requires live staging validation" | Partly proven |
| Outage behaviour | an auth-container outage stopped token refresh and Mira's subscription expired (ops doc line 112) | Lesson: the authorisation server is on the wake path |

### What vecnet should reuse

vecnet already has the profile (§4.4), its own authorization server (§4.5), and in-hub Grok Bot push (§13.3). Grok Bot push replaces both webhook2mcp's ingress role and w2m-receiver. **Copy the proven surface details, not the architecture:**

1. A profile tool, `vecnet_whoami` with `_meta["openai/profile"]: true`. Its `id` is the bound peer id (G4).
2. Per-tool `securitySchemes`, using OAuth scopes that match vecnet's single scope (G4).
3. `outputSchema` on every tool, `resultType: "complete"` on 2026-07-28 results, and `_meta` serverInfo on every result. Today it is only on `server/discover` (G4).
4. The conformance curl script in `docs/mcp-examples.md`, as a `just mcp-conformance <origin>` smoke run against a deployed hub (G1).
5. The large-record pattern: a summary plus an authorised paged read tool. Apply it to blobs (G3).
6. The live-gate checklist in `docs/research.md` "Live validation boundary" (G1).
7. **Lesson:** keep the authorization server's health on the dot's wake path. A dead token endpoint makes subscriptions lapse silently, which argues for G7's counter.

## 3. Would a dot work end to end against vecnet `/mcp` today?

**No: there is no public hub.** `https://vecnet.fly.dev` does not resolve. `docs/DEPLOY.md:48` requires public HTTPS, and `docs/connectors/dots.md:5` records that no live test has run. Point by point:

| Area | Dots need [O] | vecnet [V] | Verdict |
|---|---|---|---|
| Endpoint | Public HTTPS `/mcp` | Fly app documented, not deployed | **Gap G1** |
| Era | `server/discover` 2026-07-28, or `initialize` | Both. `initialize` accepts only 2025-11-25 and 2026-07-28 and returns `-32602` for anything else (`internal/mcpserver/server.go:435`, PROTOCOL:453). The MCP lifecycle rule says a server answers with a version it supports rather than erroring | OK for ChatGPT via discover; **G5** for other clients |
| 2026-07-28 envelope | `resultType` required on results ([S] basic/index); header mirrors | No `resultType` (`server.go:723`). Clients must treat an absent one as complete. Header mirrors are not enforced (lenient) | Low risk, G4 |
| Capability | `capabilities.events` | Both keys (`server.go:186-193`) | OK |
| `events/*` | list, subscribe, unsubscribe, challenge, `-32015`, 256 KiB, pinned egress, retries, dual-sign, refresh | §4.4, `internal/mcpserver/events.go`, `internal/events/worker.go`, tested with fake callbacks | OK on paper |
| PKCE / AS metadata | `S256`, registration endpoint or CIMD | `internal/oauth/server.go:114-123`: S256, `none`, CIMD, DCR | OK |
| Redirects | stable or `{callback_id}` form | Both allowed (`oauth/server.go:43-44`) | OK |
| Issuer identification | Needed only for the stable redirect and stable CIMD id | No `authorization_response_iss_parameter_supported` | OK (callback-id form); G4 |
| `resource` / `aud` | Validated on each call | §4.5, `oauth/verify.go` | OK |
| Tokens | Unspecified | 1 h access, 30 d rotating refresh, reuse detection | OK; same TTL as webhook2mcp |
| Linking UI | Per-tool `securitySchemes` plus resource metadata | Resource metadata only; tools lack `securitySchemes` | **Risk G4.** Connect-time 401 discovery likely suffices **[I]** |
| Profile tool | Optional | None | G4 |
| Principal to peer | — | Link code binds one ChatGPT login to one dot peer. A Work chat on the same login becomes the same peer | Known limit, G10 |
| Large content | Summary plus read tool | Preview plus `vecnet_inbox`. Attachments: `blob_url` needs a bearer and there is no blob tool | **Gap G3** |
| Observability | — | Failed Events pushes create no dead letter or counter (`docs/connectors/dots.md:175`) | G7 |
| Live or e2e test | — | `cmd/vecnet-hub/smoke_test.go:403` (`TestSmokeDotSubscription`, with an in-process fake), `internal/oauth/events_test.go`. No real dot | **G1** |

**Does §4.4 match how dots are notified?** Yes. Push by MCP Events webhook is the only path, and §4.2's resource hints are irrelevant to dots. `docs/connectors/dots.md` step 4, where the user asks the dot to subscribe, matches OpenAI's model. **Does §4.5 match?** Yes for the callback-id redirect and DCR or CIMD. G4 lists the polish items webhook2mcp has and vecnet lacks.

## 4. Parity: CLI and hook versus MCP

Base: `internal/cli/commands.go:364-416` (dispatch), PROTOCOL §13.7, §13.9, §18.4; `internal/mcpserver/tools.go:589-680`.

### Agent-facing capabilities

| Capability | CLI / hook | MCP | Notes |
|---|---|---|---|
| Send to peer, group or thread | `send … [--thread] [--reply-to] [--title] [--wake\|--wake-idle\|--hold]` | `vecnet_send` (full `SendRequest`, including `thread_title`) | Parity |
| Reply | `reply <msg>` | `vecnet_reply` | Parity |
| React | `react <msg> <emoji> [--remove] [prio]` | `vecnet_react` | Parity |
| Inbox read | `inbox [--since] [--ack]` | `vecnet_inbox(after, limit)` | Parity, but MCP has no `--ack` combo (2 calls) |
| Ack | `ack <cursor>` | `vecnet_ack` | Parity |
| Receipt | `receipt` | `vecnet_receipt` | Parity |
| Presence set | `presence set <state> [--note]` | `vecnet_presence` | Parity |
| **Presence subscribe** | `presence subscribe all\|teams\|none` (`PATCH /v1/peers/me`) | — | **Gap** |
| Threads and one thread | `threads`, `thread <id> [--after]` | `vecnet_threads`, `vecnet_thread` | Parity |
| Thread title | `send --title` (new thread) | `thread_title` in `vecnet_send` | Parity. No rename on either side |
| Peers directory | `peers` | `vecnet_peers` | Parity |
| **Whoami** | used by the hook (`GET /v1/peers/me`, §13.9 identity binding) | — | **Gap**; also the profile tool |
| **Groups** | `groups create\|add\|list` | — (send can target an existing group id) | **Gap** |
| **Attachments upload** | `send --attach FILE` (`PUT /v1/blobs`, `commands.go:731`) | Schema accepts hashes only, "uploaded first with PUT /v1/blobs" (`tools.go:560`). An MCP-only agent cannot upload | **Gap G3** |
| **Attachments download** | Agent fetches `blob_url` with its bearer | No tool. OAuth dots have no bearer to fetch with | **Gap G3** |
| Idempotency keys | `--idempotency-key`, defaults to a ULID (`commands.go:181-183`) | Required on send and reply (§4.3) | Parity (MCP stricter, correctly) |
| `as` (principal acting for a peer) | `--as` | `as` field on writes | Parity |
| **Issue 28 principal peer provisioning** | Not in CLI either. HTTP `POST /v1/peers`, `PATCH /v1/peers/{id}` (§18.4) | — | **Gap on both**; consumers (pag-chat, launcher) use HTTP |
| Watch / live stream | `watch [--for-monitor] [--json]` (SSE) | Resource hints: `subscriptions/listen` (2026) or `resources/subscribe` plus GET (2025), then `vecnet_inbox` (§4.2) | Different mechanisms; see wake below |
| Inbox hints into agent context | `vecnet hook` (untrusted wrapper, caps, first-run summary, dedupe, identity binding) | `notifications/resources/updated` hints with no content | MCP hints reach only clients that act on them |
| Wake semantics (`wake`/`wake_idle`/`hold`) | Hub-side priority (§5.2). The hook surfaces items at turn boundaries; `watch` with a Monitor wakes an idle Claude session | Dots: Events push, gated by priority (§4.4 Types). Others: hints only | **Claude Code and Codex cannot be woken by MCP** **[I]**: neither client acts on resource hints |
| Enrol | `enrol` | — | Bootstrap; correctly CLI-only |
| Operator/admin (`principal`, `peer`, `route`, `source`, `subscriptions`, `team`, `dead-letters`, `notify`, `audit`, `tui`) | CLI | — | Operator surface, not agent-facing. Correctly CLI-only |

### MCP-only

- **MCP Events** (`events/list|subscribe|unsubscribe`, webhook push): the only way a dot receives.
- **OAuth 2.1 login with link-code binding** (§4.5): there is no CLI equivalent, by design.
- **Resource-subscription hints** on `/mcp` (`vecnet://peer/<id>/inbox`, `vecnet://principal/<id>/inbox`).
- **Grok Bot pull path** (`hold` deliveries read via `vecnet_inbox` with a peer bearer, §13.3).

## 5. Where our plans make the CLI the agent's interface

| Integration | Today [V] | Should be |
|---|---|---|
| Claude Code hook (§13.9) | Wrapper hints `Reply: vecnet --net P reply …`, `Ack: vecnet --net P ack …` (`internal/cli/hook.go:105`, PROTOCOL:788). `hook install` writes hooks only; no MCP config | Hook stays the wake/inject path. If the install also registered an MCP server **bound to the same identity**, hints name the MCP tool first and keep the CLI line as the fallback |
| Codex hooks plan (`docs/plans/2026-11-04-codex-hooks.md`) | Reuses the same hints byte for byte; "MCP setup" deliberately not built (:270) | Same as Claude: add an MCP registration step and the dual hint. Keep the plan's scope and do this as a follow-up task |
| Codex app-server plan (`docs/plans/2026-11-04-codex-appserver.md:108`) | Injected context says `Reply: <cmd> reply …` | Same dual hint. The engine owns the thread, so it can also ensure `mcp_servers.vecnet` exists in that Codex home |
| Launcher (`docs/design/2026-11-03-launcher.md:665`, plan) | Spawned agents get no secret; the launcher relays replies; an "optional local MCP tool" for agent-initiated sends | Already MCP-shaped. Promote the local, thread-confined MCP server from optional to the default way a launched agent sends. It is the only design that keeps the secret out of the session |
| Grok Bot (§13.3), dots (§13.4) | MCP only | Already MCP-first |

**Do our installers configure vecnet MCP? No.** Nothing in the repo writes `.mcp.json`, `claude mcp add`, or Codex `[mcp_servers]` (grep for `mcpServers|mcp_servers|claude mcp|.mcp.json` finds nothing).

**Client compatibility [L, I].**

- **Claude Code 2.1.318.** Its bundled MCP SDK has `LATEST_PROTOCOL_VERSION = "2025-11-25"` (from the binary's strings), which matches vecnet's legacy era.
- **Codex 0.166.0.** It carries strings for 2026-07-28, 2025-06-18 and 2025-11-25. If it initialises with 2025-06-18, vecnet refuses it today (G5). Verify with a live probe.

### Recommendation: MCP first, CLI fallback

1. **Wake and act are separate jobs.**
   - Wake: the hook, `watch` plus Monitor, or the app-server engine. MCP cannot wake these clients.
   - Act: MCP tools. The CLI remains the fallback and the operator surface.
2. **`vecnet hook install --mcp`** (and its Codex twin):
   - Registers a remote HTTP MCP server named `vecnet-<profile>` pointing at `<hub>/mcp`, with `Authorization: Bearer ${VECNET_TOKEN_<P>}`. In Claude Code that is `.claude/settings.local.json` or `.mcp.json` with env expansion. In Codex it is `[mcp_servers.vecnet-<P>] url`, `bearer_token_env_var`.
   - Stores the token reference, not the secret, in the project file.
   - Records `mcp = "vecnet-<P>"` and the bound caller in the profile's hook state.
3. **Dual hints, only when bound.** The hook prints `Reply: call mcp__vecnet-<P>__vecnet_reply (message_id=<msg>), or <V> reply …`, and only when the MCP server's credential fingerprint equals the hook's `cred` (issue 33 binding). Otherwise it prints today's CLI-only hint. That way a hint can never act as another identity.
4. **The launcher's local MCP server becomes the default** send surface for spawned agents.
5. **MCP reaches parity for the agent-facing gaps** (G2, G3, G6), so "MCP first" never forces an agent back to the CLI for ordinary work.

---

## Gap table

| # | Gap | Impact | Effort | Owner plan |
|---|---|---|---|---|
| G1 | No public hub, and no live dot test on vecnet | **A dot cannot connect at all** | S code, M ops (Max deploys) | New "dots live" plan; DEPLOY.md |
| G2 | Missing MCP tools: groups (create/add/list), presence subscribe, whoami | MCP-only agents cannot form groups or bind identity | S | New "MCP parity" plan |
| G3 | No blob upload or download over MCP; `blob_url` needs a bearer | Dots and Grok Bots cannot send or read attachments | M (base64 put with a cap below 256 KiB request, paged get tool, or signed `?t=` links in tool output) | MCP parity plan; operator-attachments follow-up |
| G4 | Proven-config deltas from webhook2mcp: no profile tool, no per-tool `securitySchemes`, no `outputSchema`, no `resultType`, no RFC 9207 `iss` | Linking or account recognition may be flaky; spec non-conformance on 2026-07-28 | S | Dots live plan, before the first live test |
| G5 | `initialize` with an unsupported version errors instead of counter-offering 2025-11-25 (PROTOCOL:453, R12) | Clients on 2025-06-18 or 2025-03-26 fail (possibly Codex, many SDKs) | S (PROTOCOL amendment plus `server.go:435`) | MCP parity plan |
| G6 | Issue 28 peer provisioning has no MCP (or CLI) surface | A principal-holding MCP agent (pagd-like) cannot create or disable peers | S–M | MCP parity plan (after pag-chat agrees on tool shape) |
| G7 | Failed Events pushes: no dead letter or counter, and token-refresh health is not surfaced | Silent dot lapses | S–M | Dots live plan |
| G8 | Installers never configure vecnet MCP for Claude Code or Codex | MCP-first is impossible in practice | M | Hook-and-hygiene follow-up; Codex hooks follow-up |
| G9 | Hook, Codex-hook and app-server hints name only CLI commands | Agents learn the CLI as the interface | S (after G8 binding) | Same as G8 |
| G10 | One ChatGPT login maps to one dot peer; a Work chat and a dot collapse into one peer | Ambiguous identity for multi-surface users | L | Defer (phase 3) |
| G11 | Dot UI landing, batching, write-confirmation behaviour, events over the tunnel | Unknown UX friction | S (record during G1) | Dots live plan |

## Recommended plan outline

Ordered by dependencies. Not yet in house format.

1. **T1 Protocol conformance fixes (G4 part, G5).** Add `resultType` and result `_meta` on 2026-07-28. Make `initialize` negotiate by answering 2025-11-25 for an unknown version, amending PROTOCOL §4.1 and R12. Add `outputSchema` to the ten tools. No dependencies.
2. **T2 ChatGPT polish (G4 rest).** Add per-tool `securitySchemes`, `vecnet_whoami` with `openai/profile`, and RFC 9207 `iss` plus the metadata flag (optional, it unlocks the stable redirect). Depends on T1 only for shared tool-spec edits.
3. **T3 Events observability (G7).** Add counters `events_push_failed` and `events_paused`, an operator-inbox item when a dot subscription pauses or goes `late`, and token-endpoint error counters. No dependencies.
4. **T4 Conformance smoke (G1 prerequisite).** `just mcp-conformance <origin>`, ported from webhook2mcp `docs/mcp-examples.md`, plus a scripted `events/subscribe` with a local signed callback. Depends on T1 and T2.
5. **T5 Deploy and live dot test (G1, G11).** Max runs `just fly-deploy` and `fly-init`, then the `docs/connectors/dots.md` checklist. Record the results in ticket 19 and `docs/research/dots.md`. Depends on T2, T3 and T4. **This is the gate for "a dot can use vecnet".**
6. **T6 MCP parity tools (G2, G6).** `vecnet_groups_create|add|list`, `vecnet_presence_subscribe` and `vecnet_peer_create|disable|enable` (principal only, §18.4 shapes; needs pag-chat's agreement). No dependencies; can run alongside T1–T5.
7. **T7 Attachments over MCP (G3).** A `vecnet_blob_put` tool (base64, at most about 192 KiB per call, chunked by an upload id) and `vecnet_blob_get` (paged base64), or short-lived `?t=` URLs returned only to the participant. Keep the "no URL from the model" rule. Depends on a design decision; webhook2mcp's paged read is the reference.
8. **T8 MCP registration in installers (G8).** `vecnet hook install --mcp` for Claude Code, the same in the Codex installer, and the app-server engine ensuring `mcp_servers`. Bind the MCP credential fingerprint into hook state. Depends on T1 (G5) for Codex compatibility.
9. **T9 Dual hints (G9).** The hook, Codex-hook and app-server wrappers name `mcp__vecnet-<P>__vecnet_reply` first when bound, and keep the CLI otherwise. Amend PROTOCOL §13.9's fixed wrapper. Depends on T8.
10. **T10 Launcher local MCP as default (§5).** Fold into launcher P2 or P3. Depends on the launcher daemon.

Parallel lanes: {T1→T2→T4→T5}, {T3}, {T6}, {T7}, {T8→T9}, {T10 with the launcher}.

## Sources

**OpenAI [O], read 2026-11-04 (pages undated)**

- https://developers.openai.com/plugins/build/mcp-events
- https://developers.openai.com/plugins/build/auth
- https://developers.openai.com/plugins/deploy/connect-chatgpt
- https://developers.openai.com/api/docs/guides/secure-mcp-tunnels
- https://learn.chatgpt.com/docs/dots
- https://learn.chatgpt.com/codex/dots/computers-and-apps
- Earlier research: `docs/research/dots.md` (2026-11-01), including the SEP-3415 comparison.

**MCP [S]**

- https://modelcontextprotocol.io/specification/2026-07-28/basic/index (`resultType`, `_meta`, error codes)
- MCP lifecycle version negotiation, 2025-11-25: the server answers with a version it supports when it does not support the requested one.

**Local [L]**

- `~/src/w2m-ops-admin`: `README.md`, `PLAN.md`, `docs/research.md`, `docs/mcp-examples.md`, `docs/grokbot-contract.md`, `src/worker.ts:280-291`, `selfhost/mcp-communication.ts:51`, `integrations/oidc-provider/test/provider-flow.node.mjs:41-43`.
- `~/.llm-general/lan-swarm/operations/webhook2mcp-nas.md`, `w2m-receiver-nas.md`.
- `.scratch/vecnet/issues/19-dots-live-test.md`.
- Claude Code 2.1.318 and Codex 0.166.0 binaries (protocol-version strings only).

**vecnet [V] at `ed0f65d`**

- `docs/PROTOCOL.md` §4 (447-513), §13.3-13.9 (732-916), §18.4 (1142).
- `internal/mcpserver/server.go`, `tools.go`, `events.go`.
- `internal/oauth/server.go`, `clients.go`.
- `internal/cli/commands.go`, `hook.go`.
- `cmd/vecnet-hub/smoke_test.go:403`.
- `docs/connectors/dots.md`, `docs/DEPLOY.md:48`.
- Plans: `2026-11-03-launcher.md`, `2026-11-04-codex-hooks.md`, `2026-11-04-codex-appserver.md`.
- Design: `docs/design/2026-11-03-launcher.md:665`.
