# OpenAI dots: how an MCP server pushes events to a dot, and how a dot replies

_Researched 2026-11-01 by rs-02-dots for wayfinder ticket 02. OpenAI pages are undated and change often, so re-check anything marked time-sensitive before building._

**Source tags.** **[O]** official OpenAI docs. **[S]** official MCP spec, SEP or working-group repo. **[L]** Max's local code and ops docs (first-party for this project, not for OpenAI). **[3P]** third-party. Source ids such as S1 point to the table at the end.

## Summary

- **Mechanism.** A dot is pushed to only through **MCP Events webhook delivery**. ChatGPT, acting for the dot, calls `events/subscribe` on our MCP server and hands us a callback URL and a `whsec_` secret. We then POST Standard-Webhooks-signed events to that URL [O S1].
- **Not `resources/subscribe`.** Core MCP has no events. In spec `2026-07-28`, `resources/subscribe` was replaced by `subscriptions/listen`, a live stream, which ChatGPT does not document for push into a chat [S S5].
- **It is a vendor profile of a draft.** MCP Events is SEP-3415, an open Draft PR. ChatGPT implements an earlier design sketch: webhook only, a top-level `events` capability, and error code `-32015` [O S1][S S4].
- **A dot cannot be addressed.** It has no API, email or webhook inbox. The only programmatic way in is a subscription that the dot's user asked ChatGPT to create [O S1 S2 S3].
- **Replies are tool calls.** A dot "sends back" by calling a tool on our MCP server over the same OAuth-authenticated connection. Write tools need user confirmation by default [O S1 S8].
- **Auth.** OAuth 2.1 with PKCE S256, acting as the ChatGPT user. There are no API keys and no client-credentials grant [O S6].
- **Network.** Our `/mcp` endpoint needs public HTTPS or OpenAI's outbound-only Secure MCP Tunnel. Our event deliveries go outbound to ChatGPT's callback URL [O S1 S7 S9].
- **Payload limit.** Each event POST is one event, at most 256 KiB [O S1].
- **Prior art.** Max's webhook2mcp already implements this profile and has run live with ChatGPT since 2026-10-25 [L L1 L2].

## 1. What a dot is [O]

- **Definition.** "Your dot is an always-on agent that keeps work moving across your tools and projects. Powered by GPT-6 Astra, your dot lives in the cloud and has its own computer and browser." Dots are "rolling out gradually to eligible accounts" [S2].
- **Plans.** Pro tiers (18+, outside the EEA, UK and Switzerland), Business Premium, and Enterprise, where dots are off by default. This detail came from a summarised part of the page [S2].
- **Channels.** The ChatGPT apps, voice calls, Slack, Teams, and texting (a US Pro beta). "You reach the same dot through each connected contact method." No API, webhook or email inbound is documented [S3].
- **Wake triggers.** A dot wakes on messages and on its own timed wake-ups ("It can decide when to pause and wake up to continue"). It also wakes on saved schedules and on events: "When a connected service supports event monitoring, you can instead ask your dot to respond to a specific event… Connecting Slack or another source alone doesn't create a monitoring task" [S10].
- **Plugins.** "Your dot can use supported plugins installed and enabled for your account… Your existing ChatGPT app permissions apply to your dot." The dots pages never mention MCP by name [S11].
- **The link between dots and MCP Events.** It is a single statement on the MCP Events page: "MCP Events is available in Work chats on ChatGPT web, in Work chats in the desktop app with Cloud selected, and with dots… You can also test with a dot" [S1].

## 2. The push mechanism: MCP Events as ChatGPT implements it [O S1]

**Transport and capability.**

- ChatGPT supports "MCP Events over Streamable HTTP using `initialize`, or over MCP 2.0 HTTP (protocol version `2026-07-28`)".
- The server advertises `"capabilities": {"tools": {}, "events": {}}`. That goes in the `initialize` result or in `server/discover`.
- The three methods live on the same authenticated MCP endpoint as tools: `events/list`, `events/subscribe` and `events/unsubscribe`.
- The server must provide "persistent subscription storage and outbound HTTPS access to callback URLs."

**Methods.**

| Method | Params | Result |
|---|---|---|
| `events/list` | optional `cursor` | `events[]`, each `{name, description, delivery:["webhook"], inputSchema, payloadSchema}`, plus `nextCursor` |
| `events/subscribe` | `name`, `arguments` (validated by `inputSchema`), `delivery:{mode:"webhook", url, secret}`, `cursor`, optional `ttlMs` | `{id, refreshBefore, cursor, truncated}` |
| `events/unsubscribe` | `name`, `arguments`, `delivery:{mode, url}` | `{}`. It must be idempotent and authorised against the connected account. |

- **TTL.** If `ttlMs` is omitted, the server default applies. `ttlMs: null` asks for no expiry, and `refreshBefore: null` may be returned only then.
- **Secret.** It must be `whsec_` plus base64 that decodes to 24-64 bytes.
- **Subscription key.** It is `(principal, url, name, arguments)`. The `id` is derived from the key and is a routing handle, not a credential [S4 §Webhook].

**Callback verification.**

- Before the first delivery, the server POSTs `{"type":"verification","challenge":"<single-use>"}`. The request is signed with the subscription secret and carries `X-MCP-Subscription-Id`.
- The callback must return 2xx echoing `{"challenge": …}`, which the server compares in constant time.
- A pass may be cached per principal and URL for a bounded time.
- A failure returns JSON-RPC error `-32015` `CallbackEndpointError`, with `data.reason` such as `challenge_failed` or `timeout`.

**Delivery.**

- Each request is one event: `{eventId, name, timestamp (ISO 8601 with zone), data (matches payloadSchema), cursor}`.
- The headers are `webhook-id` (equal to `eventId`), `webhook-timestamp` (Unix seconds), `webhook-signature` (Standard Webhooks HMAC) and `X-MCP-Subscription-Id`.
- The signature is `v1,` + base64 HMAC-SHA256 over `id.timestamp.body`, keyed with the decoded secret bytes [S4 §Signing].
- 2xx means receipt. "ChatGPT processes the event asynchronously."
- Retries use exponential backoff with bounded attempts. Each retry keeps the same event id and gets a fresh timestamp and signature. **Never retry 410 or 413.**
- Events may arrive out of order, so write tools must be idempotent.
- When a refresh supplies a new secret, sign with both keys, space-separated, for a short rotation window.

**Refresh and replay.**

- "ChatGPT refreshes expiring subscriptions by calling `events/subscribe` before `refreshBefore`, using the same subscription identity and the last saved cursor."
- Replayable types resume after that cursor. `truncated: true` means history was lost.
- Non-replayable types return `cursor: null`, and missed events "cannot be recovered through the protocol."

**On the ChatGPT side.**

- "ChatGPT receives the event in the subscribed chat and follows the user's instructions for how to respond."
- It "can group separately delivered events into one task run according to the task's batching settings."
- The user creates a subscription in chat: "Ask ChatGPT to subscribe to one of your events and specify what it should do when an event arrives."

**Not supported:** "Polling, streaming, and the draft's `gap` and `terminated` control notifications."

**Payload hygiene:** "Treat comments and other user-authored text as data; do not add instructions telling the model how to behave inside the event payload."

## 3. Spec status, and how ChatGPT differs from the draft [S]

- **Latest released spec.** `2026-07-28` (released 2026-07-28; previous release `2025-11-25`). Events are not in core [S5 S12].
- **Core subscriptions.** `2026-07-28` "Replace[s] the HTTP GET endpoint and `resources/subscribe`/`resources/unsubscribe` with `subscriptions/listen`", which is a long-lived POST-response stream. It also removes the `initialize` handshake and adds `server/discover` [S5].
- **SEP-3415 "Events Extension"** [S4]:
  - It is PR #3415, still OPEN, Status Draft, Extensions Track, created 2026-10-05, last updated 2026-10-31.
  - Its id is `io.modelcontextprotocol/events`.
  - It has three optional modes: poll, push (`events/stream`, heartbeat at least every 30 s) and webhook.
  - "The reference implementation in an official SDK is outstanding."
- **Resources versus events.** The SEP says "`notifications/resources/updated` is unchanged. Events are a separate primitive." Whether to fold resource subscriptions into events is its Open Question 2 [S4].
- **ChatGPT follows the pre-SEP design sketch** in the working-group repo, which its page links as "the protocol reference" [S1 S13]. The SEP acknowledges this: "Prototype implementations of the design sketch… advertised a top-level `events` capability, and at least one deployed client reads it" [S4 §Backward Compatibility].

| Item | ChatGPT profile [S1] | SEP-3415 draft [S4] |
|---|---|---|
| Capability location | top-level `capabilities.events` | `capabilities.extensions["io.modelcontextprotocol/events"]` |
| Delivery modes | webhook only | poll, push and webhook, all optional |
| Callback error code | `-32015` | `-32027`, provisional |
| `gap` / `terminated` / `deliveryStatus` | not supported | defined |
| Body size | at most 256 KiB, hard | SHOULD be at most 256 KiB; 413 is non-retryable |

**Implication.** Advertise both capability keys, as mcpkit's ChatGPT-compat mode does [L L3]. Keep the error codes behind one constant, because the SEP may renumber them.

## 4. Auth [O S6]

- **Flow.** Authenticated servers "implement an OAuth 2.1 flow that conforms to the MCP authorization spec". ChatGPT requires PKCE `S256` ("MCP servers are unsupported when their authorization server metadata omits this field or does not advertise `S256`").
- **Client registration.** CIMD is preferred. DCR and predefined clients are also supported.
- **Not supported.** "ChatGPT does **not** support machine-to-machine OAuth grants such as client credentials, service accounts, or JWT bearer assertions, nor can it present custom API keys or customer-provided mTLS certificates."
- **Protected resource metadata.** RFC 9728 metadata is served at `/.well-known/oauth-protected-resource`. ChatGPT sends `resource=` on authorize and token requests. The server must check `aud`, issuer, expiry and scopes on every request.
- **Redirect URIs.** `https://chatgpt.com/connector_platform_oauth_redirect`, or `https://chatgpt.com/connector/oauth/{callback_id}`.
- **Identity.** An optional tool marked `_meta["openai/profile"]: true` returns a stable account `id`.
- **Server-side checks.** ChatGPT presents an OpenAI-managed mTLS client certificate (SAN `mtls.prod.connectors.openai.com`), and publishes egress IP ranges.
- **What the hub sees.** The hub sees **the ChatGPT user's OAuth principal, never "dot X"**. A user with several dots, or a dot and a Work chat, shares one principal. They are distinguishable only by callback URL and subscription arguments [O S1 S6][L L2].
- **Event authorisation.** The SEP says event subscriptions are authorised with "the same MCP principal as tools", and the server should re-check access before each delivery [S4 §Authorization].

## 5. Public-URL and network constraints

| Direction | Requirement | Source |
|---|---|---|
| ChatGPT to our `/mcp` (tools, `events/*`) | "reachable through a public HTTPS endpoint or Secure MCP Tunnel". A private server can sit behind a public HTTPS proxy. Custom servers support "SSE and streaming HTTP"; production should use Streamable HTTP. | [O S7 S9] |
| Secure MCP Tunnel option | `tunnel-client` makes an outbound-only connection to `api.openai.com:443` and long-polls for JSON-RPC work, so no inbound port is needed. Limits are 10 MiB per message, a 120 s deadline, and 600 requests/min per tunnel and app. It cannot be used for public plugin submission. The authorisation server is not tunnelled. Whether `events/*` work through a tunnel is **not documented**. | [O S9] |
| Our server to ChatGPT's callback (events) | Outbound HTTPS only. Resolve and validate DNS at connect time, connect to the validated IP while keeping SNI, block private and non-public addresses, and do not follow redirects. This applies to verification requests too. | [O S1] |
| Callback hosts we accept | Ours to choose. webhook2mcp allows only `https` on port 443, with no userinfo or fragment, and rejects `.local`, `.internal`, `.onion` and similar. | [L L4] |

**Consequence for vecnet.** The hub can receive dot traffic through either a public HTTPS endpoint or the Secure MCP Tunnel. The tunnel fits a NAS-hosted hub, but whether it carries events is unverified. Event push needs only outbound HTTPS from the hub.

## 6. Payload and other limits

- **Event POST.** One event per request, at most **256 KiB (262,144 bytes)** in total [O S1]. The sender example uses a 10 s timeout [O S1].
- **Tunnel.** 10 MiB per request or response, and a 120 s deadline [O S9].
- **Tool calls and the tool catalogue.** No documented size, timeout or tool-count limit outside the tunnel. The troubleshooting guide only notes that components "feel sluggish" past a few hundred ms [O S8].
- **webhook2mcp's own caps.** 32 KiB for MCP control and callbacks. Message content over 8 KiB becomes a truncated preview plus `content_ref`. 20 subscriptions per source. Subscription TTL is clamped to 60 s-24 h and to the OAuth access-token expiry, which is 3,600 s on Max's IdP [L L1 L5].

## 7. How a dot sends back

- **Mechanism.** The dot calls an **MCP tool on our server**. That is the only documented path. The SEP says "Events and tools compose at the application layer. An event arrives, the agent reasons about it, and it may call tools in response." [S4]. On ChatGPT's side, the event lands "in the subscribed chat", and the user's instructions decide the response [O S1].
- **Confirmation friction.** "Write actions by default require confirmation". Tools count as writes unless they carry `readOnlyHint` [O S7]. A `send_message` tool is honestly a write, so a dot replying unattended may stall on a confirmation prompt. This is **unverified for dots**; see the open questions.
- **Idempotency.** Events are at-least-once and may arrive out of order, so every write tool takes an `idempotency_key` [O S1].
- **Prior art.** webhook2mcp exposes `send_message {account_id, route_id, content, thread_id?, idempotency_key}` and `reply_to_message`. Neither accepts a callback URL [L L2]. In Max's deployment, `reply_to_message` failed on 2026-10-26 because source bindings had a NULL reply route [L L6]. That bug is in the bridge, not in the protocol.

## 8. Prior art on Max's LAN [L]

- **Live deployment.** webhook2mcp (`xpp-tm/xpp-server-gpt-webhook2mcp`, ops at `~/src/w2m-ops-admin`) is connected to ChatGPT as a custom MCP server at `https://gb2cxc.xpp.exchange/mcp` with OAuth, since 2026-10-25 [L1].
- **What it covers.** It implements the whole ChatGPT profile: `events/list`, subscribe and unsubscribe, the challenge, pinned-IP signed delivery, retries, cursors and backfill. It offers one event type, `webhook.received`, keyed by `source_id`, plus 15 messaging tools [L2].
- **Failure modes it has hit.**
  - On 2026-10-25, an outage of the auth container killed token refresh, and "Mira's subscription expired".
  - Subscriptions cannot outlive the 1 h access token unless ChatGPT refreshes them [L1 L5].
- **Identity of the subscriber.** The ChatGPT-side subscriber "Mira" is not documented as a dot; it may be a Work chat [L2].

## What the dots connector must do

1. **Be an MCP server on Streamable HTTP** at a stable `/mcp` endpoint.
   - Reach it through public HTTPS (a Cloudflare Tunnel, as on the NAS today) or the Secure MCP Tunnel.
   - Accept both legacy `initialize` (ChatGPT's example uses `2025-03-26`) and `2026-07-28` `server/discover`. webhook2mcp accepts only `2026-07-28`, which ChatGPT tolerates today [O S1][L L2].
2. **Be an OAuth 2.1 resource server.**
   - Publish RFC 9728 metadata. Use an authorisation server that advertises PKCE S256 and supports CIMD or DCR.
   - Validate `aud`, issuer, expiry and scope on every call.
   - Map each OAuth principal, plus the optional profile `id`, to a vecnet peer [O S6].
3. **Advertise events in both places:** top-level `capabilities.events` for ChatGPT and `extensions["io.modelcontextprotocol/events"]` for SEP clients [O S1][S S4].
4. **Implement `events/list`, `events/subscribe` and `events/unsubscribe`** per §2.
   - Offer a small catalogue, e.g. `vecnet.message {peer_id?, thread_id?}`, with a strict `inputSchema` and `payloadSchema`.
   - Store subscriptions durably, keyed by `(principal, url, name, arguments)`.
   - Clamp the TTL to at most 24 h and at most the token expiry.
   - Return a replay cursor, and set `truncated` honestly.
5. **Verify every callback** with the signed challenge before activating it, and fail with `-32015` plus a reason.
6. **Deliver from a durable outbox.**
   - One event per POST, at most 256 KiB.
   - Standard Webhooks headers plus `X-MCP-Subscription-Id`, with `webhook-id = eventId`.
   - DNS-pinned egress to public IPs only, no redirects.
   - Re-check authorisation before each send.
   - Retry with backoff, preserving `eventId`. Treat 410 and 413 as terminal.
   - Dual-sign during secret rotation.
7. **Keep payloads small and inert.**
   - Envelope metadata plus content, or a preview plus a blob-store reference when it would exceed the cap.
   - Never put model instructions in the payload, and label content as untrusted [O S1].
8. **Expose reply tools.**
   - `send_message`, `reply_to_message` and `ack`/`read`, each requiring an `idempotency_key`.
   - Tool descriptions are honest about being writes, and `readOnlyHint` goes only on genuinely read-only tools such as `list_threads` and `get_thread`.
   - Never accept a URL from the model [O S7][L L2].
9. **Model the dot as a subscription-backed peer.**
   - It can receive only while it has a live subscription, and it can always send via tools.
   - Surface subscription health (expiry, last refresh, last delivery status) in the operator UI.
   - Queue messages durably while no live subscription exists, and drain them from the cursor on re-subscribe [L L2 L5].
10. **Give enrolment a script.** The operator tells the dot something like: "subscribe to vecnet `vecnet.message` for peer X; when one arrives, treat it as a message from that peer and reply with `reply_to_message`". There is no API to create the subscription [O S1].
11. **Reuse webhook2mcp's design.** It is the reference: the same signing, the same envelope id separation, and the same outbox. Either embed its event server or keep it in front of the hub. That choice belongs to the tech-stack ticket [L L2].

## Open questions

1. **Can dots use a custom (unpublished) MCP server plugin, and does that hold on every eligible plan?** The docs say dots use "supported plugins installed and enabled for your account", and custom servers become personal plugins. No page states that the two combine. Needs a live test with a real dot.
2. **What happens to an event on the dot side?** Where does it land, how is batching configured for a dot, and do a dot's subscriptions survive a dot restart or context reset? Officially undocumented beyond "the subscribed chat" and "batching settings".
3. **Do write-tool confirmations block a dot replying unattended?** If so, does per-tool "always allow" exist for dots?
4. **Do `events/*` and callback verification work over the Secure MCP Tunnel?** The tunnel docs never mention events.
5. **Does ChatGPT refresh a subscription with a fresh access token,** so that the TTL survives a 1 h token? webhook2mcp's 2026-10-25 incident suggests refresh depends on the auth server being healthy.
6. **Will the SEP renumber `-32015` and move the capability under `extensions`, and when will ChatGPT follow?** Track PR #3415.
7. **Is "Mira" a dot or a Work chat?** If it is a Work chat, no one has yet verified a dot receiving an MCP event on Max's setup.
8. **Can one OAuth principal serve several dots, or a dot plus Work chats, as distinct vecnet peers?** Or must each be a separate subscription argument such as `peer_id`?

## Sources

| id | type | source | used for |
|---|---|---|---|
| S1 | O | https://developers.openai.com/plugins/build/mcp-events (read 2026-11-01, undated) | events profile, limits, egress rules, dots availability |
| S2 | O | https://learn.chatgpt.com/docs/dots | what a dot is, plans |
| S3 | O | https://learn.chatgpt.com/codex/dots/channels | dot channels; no API inbound |
| S4 | S | SEP-3415, https://github.com/modelcontextprotocol/modelcontextprotocol/pull/3415 (file `seps/0000-events-extension.md` @ `b08e4f9`) | draft spec, signing, error codes, backward-compat note |
| S5 | S | https://modelcontextprotocol.io/specification/2026-07-28/changelog and `server/resources` (repo `docs/specification/2026-07-28/`) | `resources/subscribe` replaced by `subscriptions/listen`; no core events |
| S6 | O | https://developers.openai.com/plugins/build/auth | OAuth 2.1, PKCE, no M2M, profile tool, mTLS |
| S7 | O | https://developers.openai.com/api/docs/guides/custom-mcp-server ; https://developers.openai.com/plugins/deploy/connect-chatgpt | adding a custom server, write confirmations, public HTTPS or tunnel |
| S8 | O | https://developers.openai.com/plugins/deploy/troubleshooting | no hard limits; latency guidance |
| S9 | O | https://developers.openai.com/api/docs/guides/secure-mcp-tunnels | tunnel mechanics and limits |
| S10 | O | https://learn.chatgpt.com/codex/dots/tasks-and-memory | wake triggers, event monitoring |
| S11 | O | https://learn.chatgpt.com/codex/dots/computers-and-apps | dots use installed plugins |
| S12 | S | `gh api repos/modelcontextprotocol/modelcontextprotocol/releases` | release dates |
| S13 | S | https://github.com/modelcontextprotocol/experimental-ext-triggers-events (`docs/design-sketch-proposal.md`) | design sketch that ChatGPT follows |
| L1 | L | `~/.llm-general/lan-swarm/operations/webhook2mcp-nas.md` (status 2026-10-25/26) | live ChatGPT connection, the expiry incident |
| L2 | L | `~/src/pag-chat/docs/research/prelim/dots-mcp-events.md` (2026-11-01; cites webhook2mcp `064fffc` source lines) | webhook2mcp implementation, tool list, dot-as-subscription model |
| L3 | 3P via L2 | `panyam/mcpkit` ChatGPTCompat, as reported in L2 (not re-read) | dual capability advertising |
| L4 | L via L2 | webhook2mcp `src/callback.ts:2-10`, as reported in L2 | callback URL rules |
| L5 | L via L2 | webhook2mcp `src/worker.ts:390-394`, `README.md:55-68`, as reported in L2 | TTL clamp, caps |
| L6 | L | `~/.llm-general/lan-swarm/operations/w2m-receiver-nas.md:48-52` | NULL reply-route bug |

**Not re-verified here:** the L3-L5 line references, taken from the pag-chat prelim; the dot plan details, which came from a summarised part of the page; and the 2026-09-29 dots launch date, which appears only in secondary sources and is not used above.
