# openclaw: how a vecnet connector talks to an openclaw agent

Researched 2026-11-01 by rs-03-openclaw for ticket [03](../../.scratch/vecnet/issues/03-openclaw-surface.md).
Source: `~/src/openclaw` at `0c8ea8d98` (2026-03-13, origin `github.com/openclaw/openclaw`, clean checkout, plugin packages at version `2026.3.12`). All paths below are relative to that repo unless noted. `~/.llm-general` has no openclaw integration notes (only an unrelated mention in `research/stt/gray-market/claude-sub-outside-apps-prior-art.md`).

## Recommendation

**Build vecnet as an openclaw channel plugin** (a plugin package whose `register(api)` calls `api.registerChannel(...)`), with the plugin **dialling out** to the vecnet hub: SSE (or long-poll) for inbound, hub HTTP send for outbound.

This is exactly Max's "add-on/extension" preference. In openclaw "plugin" and "extension" are the same thing, and a *channel* is one capability a plugin registers. So the choice is not extension vs channel plugin: it is a channel plugin, shipped as an extension.

Why this surface:

- **It is the native inbound path.** Every chat surface (IRC, Synology Chat, Nextcloud Talk, Matrix, …) delivers messages to agents this way. Inbound gets per-peer sessions, routing to agents via `bindings`, allowlists/pairing, typing/streaming and reply chunking for free.
- **Replies come back on the same rail.** The reply dispatcher hands every reply block to a `deliver` callback the plugin supplies. The plugin posts it to the hub. No polling for results, no scraping transcripts.
- **The agent can start conversations.** Registering a channel means the agent's built-in `message` tool can send to `vecnet:<peerId>`. That call lands in the plugin's `outbound.sendText`. Nothing else gives openclaw agents a first-class "send to another bot" verb.
- **No public ingress needed.** A dial-out channel (IRC model) works on the hardened `fly.private.toml` deployment. A webhook-receiving channel would force the gateway onto the public internet.
- **The gateway HTTP APIs are the wrong trust shape.** `/v1/chat/completions` and `/v1/responses` require the gateway operator token and grant full operator access, so the hub would hold an owner credential. `/hooks/agent` is fire-and-forget and returns no reply to the caller. Both need inbound reachability.

Rejected alternatives:

| Surface | Inbound to agent | Reply back | Why not |
|---|---|---|---|
| Channel plugin, dial-out (**recommended**) | `finalizeInboundContext` + reply dispatcher | `deliver` callback; `outbound.sendText` for agent-initiated | (chosen) |
| Channel plugin, webhook route | Same, triggered by `registerPluginHttpRoute` | Same | Needs public ingress on the gateway; keep as fallback only if the hub must push |
| `POST /hooks/agent` | Isolated agent turn | None to caller; summary to main session, optional delivery to a *chat channel* | No synchronous reply; needs ingress; session-key overrides off by default |
| `POST /v1/chat/completions`, `/v1/responses` | Agent run | HTTP response | Operator-level credential held by the hub; needs ingress; agent cannot initiate |
| Gateway WS protocol (`chat.send`) / `openclaw acp` | Operator client | WS events | Operator role again; external process must hold a gateway token; agent cannot initiate to vecnet |
| Plain tool plugin (`registerTool`) | — | — | Gives outbound only; no inbound path into a session |

## Facts

### Plugins (= extensions)

- Plugins are TypeScript modules loaded in-process by jiti. They are **not sandboxed**; a plugin is equivalent to core code. `docs/tools/plugin.md` §Execution model (line 73).
- A plugin can register tools, hooks, channels, providers, gateway RPC methods, HTTP routes, CLI commands, background services and context engines. `docs/tools/plugin.md` §Plugin API (overview); registry list under §Registry model.
- Each plugin needs an `openclaw.plugin.json` manifest (`id`, `channels`, `configSchema`). Config validation reads only the manifest, never runs plugin code. `docs/tools/plugin.md` §Manifest-first behavior; example `extensions/synology-chat/openclaw.plugin.json`.
- `package.json` declares `openclaw.extensions` (entry files) and optional `openclaw.channel` catalog metadata plus `openclaw.install` (`npmSpec`, `localPath`). `extensions/synology-chat/package.json`; `docs/tools/plugin.md` §Channel catalog metadata (line 412).
- Discovery order: `plugins.load.paths`, then `<workspace>/.openclaw/extensions/`, then `~/.openclaw/extensions/`, then bundled `extensions/*`. Workspace plugins are disabled by default. Config changes need a gateway restart. `docs/tools/plugin.md` §Discovery & precedence (line 312), §Config.
- Install: `openclaw plugins install <path|tgz|zip|npm-spec>` (`-l` links for dev). npm installs run with `--ignore-scripts`, so deps must be pure JS/TS. `docs/tools/plugin.md` §CLI.
- Channel config lives under `channels.<id>` (multi-account: `channels.<id>.accounts.<accountId>`), not `plugins.entries`. Unknown `channels.<id>` keys are errors unless a manifest declares the channel. `docs/tools/plugin.md` §Register a messaging channel (line 1025), §Write a new messaging channel (line 1092), §Config.
- Plugins import from `openclaw/plugin-sdk/core` for generic APIs; per-channel subpaths exist for bundled plugins. `docs/tools/plugin.md` §Plugin SDK import paths.

### The channel plugin contract

- `ChannelPlugin` adapters: `config` (required), `outbound`, `gateway`, `security`, `pairing`, `threading`, `messaging`, `directory`, `status`, `streaming`, `agentPrompt`, `agentTools`, and more. `src/channels/plugins/types.plugin.ts` lines 60–85.
- Minimum: `config.listAccountIds`, `config.resolveAccount`, `capabilities`, `outbound.deliveryMode` + `outbound.sendText`. `docs/tools/plugin.md` line 1092 onward.
- `gateway.startAccount(ctx)` starts the listener for one account. It **must return a promise that stays pending until `ctx.abortSignal` fires**; resolving early triggers a restart loop. `extensions/synology-chat/src/channel.ts` lines 230–346 (comment at 338–340).

### Inbound: external message to agent

The pattern every working channel uses (`extensions/synology-chat/src/channel.ts` 257–311, `extensions/irc/src/inbound.ts` 278–360):

1. Receive the event (webhook route, socket, relay subscription).
2. Optionally `api.runtime.channel.routing.resolveAgentRoute({cfg, channel, accountId, peer: {kind: "direct"|"group", id}})`. It returns `{agentId, sessionKey, accountId}` and honours config `bindings`.
3. Build the message context with `runtime.channel.reply.finalizeInboundContext({...})`. Key fields: `Body`, `RawBody`, `CommandBody`, `From`/`To` (`"<channel>:<id>"`), `SessionKey`, `AccountId`, `ChatType`, `SenderId`, `SenderName`, `ConversationLabel`, `MessageSid`, `Timestamp`, `Provider`/`Surface`/`OriginatingChannel` (= channel id), `OriginatingTo`, `CommandAuthorized`, and for groups `GroupSubject` / `GroupSystemPrompt`.
4. Dispatch with `runtime.channel.reply.dispatchReplyWithBufferedBlockDispatcher({ctx, cfg, dispatcherOptions: {deliver, onReplyStart}})`. Alternatively use the SDK helper `dispatchInboundReplyWithBase` (`src/plugin-sdk/inbound-reply-dispatch.ts` line 83), which also records session metadata.
5. The agent runs. Each reply block arrives at `deliver(payload)` (`payload.text`, plus media). The plugin sends it back out.

The available runtime surface is typed in `src/plugins/runtime/types-channel.ts` (`reply`, `routing`, `pairing`, `session`, `media`, `debounce`, …). Plugins receive it via `api.runtime` and store it in a module-level setter (`extensions/synology-chat/src/runtime.ts`).

**Pitfall: do not copy the nostr plugin.** `extensions/nostr/src/channel.ts` (~line 216) calls `runtime.channel.reply.handleInboundMessage?.(...)`. That method does not exist on the plugin runtime (`src/plugins/runtime/types-channel.ts`; no definition anywhere in `src/`). The optional chaining means nostr inbound DMs are silently dropped in this checkout. Use IRC or Synology Chat as templates.

### Outbound: agent to external system

- **Replies** to an inbound message go through the dispatcher's `deliver` callback (above).
- **Agent-initiated sends** use the built-in `message` tool. Its send path is `src/agents/tools/message-tool.ts`, then `src/infra/outbound/message-action-runner.ts`, then `outbound-send-service.ts`, then `message.ts`, then `deliver.ts`. `deliver.ts` calls the channel plugin's `outbound.sendText` (lines 153–184). Targets are normalised by the plugin's `messaging.normalizeTarget` and `messaging.targetResolver`. Example: `extensions/synology-chat/src/channel.ts` 170–228.
- `outbound.deliveryMode: "gateway"` plus `textChunkLimit` makes core chunk long replies before they reach `sendText`. Synology uses 2000. vecnet's 4 KiB body cap maps onto this.
- `agentPrompt.messageToolHints()` injects channel-specific guidance into the system prompt (Synology lines 353–377). vecnet can use it to explain peer ids, threads and reply links to the agent.
- Plugins may also add custom tools (`registerTool`, `agentTools`) if the `message` tool is too narrow. `docs/plugins/agent-tools.md`.

### Optional HTTP ingress from a plugin

- `api.registerHttpRoute({path, auth: "gateway"|"plugin", match, handler})` mounts a route on the gateway's HTTP server, which shares the gateway port (default 18789; 3000 on Fly). `auth: "plugin"` means the plugin verifies signatures itself. `docs/tools/plugin.md` §Gateway HTTP routes (line 198).
- Channels register routes per account inside `startAccount` via `registerPluginHttpRoute(...)` and unregister on abort. `extensions/synology-chat/src/channel.ts` 315–345. Request-hardening helpers exist in `src/plugin-sdk/webhook-request-guards.ts`, `webhook-memory-guards.ts` and `webhook-targets.ts`.

### Gateway-level APIs (not recommended for vecnet)

- `POST /hooks/agent` (token in `Authorization: Bearer` or `x-openclaw-token`) runs an isolated agent turn and returns 200 "accepted" with no reply body. Delivery of the answer goes to a chat channel (`channel`, `to`); a summary is posted into the main session. Request `sessionKey` is rejected unless `hooks.allowRequestSessionKey=true`. `docs/automation/webhook.md`.
- `POST /v1/chat/completions` and `POST /v1/responses` are disabled by default and authenticated with the gateway token. The docs say to treat them as **full operator access** and keep them off the public internet. Agent selection is via `model: "openclaw:<agentId>"` or `x-openclaw-agent-id`; session via `x-openclaw-session-key`. `docs/gateway/openai-http-api.md`, `docs/gateway/openresponses-http-api.md`.
- The gateway WS protocol (`docs/gateway/protocol.md`) is the control plane for CLI/UI/nodes: `connect` handshake with role and scopes, then `req` frames such as `chat.send`. `openclaw acp` (`docs.acp.md`) bridges ACP over stdio to this WS. Both make the external party an operator client.
- `openclaw agent --message … [--deliver --reply-channel …]` runs one turn from the CLI. `docs/tools/agent-send.md`.

### Deployment and ingress

- `fly.toml`: app `openclaw`, process `node dist/index.js gateway --allow-unconfigured --port 3000 --bind lan`, `[http_service]` on internal port 3000 with `force_https`, `auto_stop_machines = false`, `min_machines_running = 1`, volume `openclaw_data` at `/data` (`OPENCLAW_STATE_DIR`). **This template has public ingress** at `https://<app>.fly.dev`.
- `fly.private.toml`: identical but with no `[http_service]`, so **no public IP**. Access is by `fly proxy`, WireGuard or `fly ssh console`. The docs recommend it when the deployment only makes outbound calls, and suggest ngrok/Tailscale for any webhook callbacks. `docs/install/fly.md` lines 361–464.
- Non-loopback binds (`--bind lan`) require `OPENCLAW_GATEWAY_TOKEN`. `docs/install/fly.md` lines 96–113.
- `render.yaml`, `docker-compose.yml` and podman scripts are other supported deploy shapes.
- Which template Max's actual openclaw instance uses is not recorded in the repo (see Open questions).

## What the openclaw connector must do

A plugin package, e.g. `@vecnet/openclaw` (id `vecnet`), installed with `openclaw plugins install` and configured under `channels.vecnet`.

1. **Manifest and package.** Write `openclaw.plugin.json` with `id: "vecnet"`, `channels: ["vecnet"]` and a config schema. Add a `package.json` with `openclaw.extensions` and `openclaw.channel` metadata. Use pure JS/TS deps only.
2. **Config per account.** Store the hub URL, this peer's vecnet id and enrolment credential (via env or SecretRef, not plaintext), an allowlist of vecnet peers or teams that may talk to this agent, and an optional agent binding. One account per vecnet peer identity lets one gateway host several vecnet peers mapped to different openclaw agents.
3. **`gateway.startAccount`.** Open an outbound SSE (or long-poll) subscription to the hub. Reconnect with backoff and resume from the last acknowledged cursor. Stay pending until `abortSignal`. Report health through `status`/`setStatus`.
4. **Inbound mapping.** For each envelope:
   - Dedupe on the delivery id, since the hub is at-least-once.
   - Check the sender against the allowlist.
   - Call `resolveAgentRoute` with peer `{kind: "direct", id: senderPeerId}`, or `{kind: "group", id: threadOrGroupId}` for team/group threads.
   - Build the context with `finalizeInboundContext`. Put the envelope `content` in `Body`/`RawBody`, the message id in `MessageSid`, `From: "vecnet:<sender>"`, and `To`/`OriginatingTo` set to the thread.
   - Dispatch. Send the hub an *accepted* receipt on receipt and a *processed* receipt after dispatch completes.
5. **Session mapping.** Decide whether one openclaw session maps to one vecnet thread or to one peer (see Open questions). Carry `thread_id` and `in_reply_to_message_id` in the session key or a side map, so `deliver` and `sendText` can thread replies correctly.
6. **Reply path.** In `deliver`, POST each reply block to the hub send API with `in_reply_to` set to the triggering message and the same thread. Attachments (`payload.mediaUrl`) need uploading to the hub blob store first.
7. **Agent-initiated path.** Implement `outbound.sendText` (and `sendMedia`) to send to a vecnet peer or thread id. Add `messaging.normalizeTarget` / `targetResolver` for the `vecnet:<id>` form. Set `textChunkLimit` to vecnet's 4 KiB cap.
8. **Directory.** Back `directory.listPeers` / `listGroups` with the hub's peer directory so the agent and CLI can resolve names to ids.
9. **Prompt hints.** Use `agentPrompt.messageToolHints` to explain vecnet addressing, threads and etiquette, including loop-control and hop budget rules.
10. **Loop control.** Propagate hop count, TTL and sender chain from inbound to the replies the agent produces. Never auto-reply to receipts, presence or ack events; drop those before dispatch.
11. **Presence and capability.** Optionally run a background `registerService` heartbeat that publishes presence and self-reported capability/quota to the hub.
12. **Security.** The plugin runs in-process with full trust, so keep the hub credential scoped to this peer only. Never expose a gateway operator token to the hub.

## Open questions

- **Which deployment is live?** Is Max's openclaw instance on public `fly.toml`, `fly.private.toml`, or somewhere else? The recommended dial-out design works either way. A hub-push webhook design only works with public ingress or a tunnel.
- **Session granularity.** Should one openclaw session be per vecnet thread or per vecnet peer? Per-thread matches vecnet's first-class threads but multiplies sessions. Per-peer mirrors how DMs work in other channels. This depends on [09 threads and groups](../../.scratch/vecnet/issues/09-threads-and-groups.md).
- **Hub inbound transport for a plugin.** SSE fits the dial-out model, and Node in-process can hold it. Confirm against [06 SSE and MCP streaming](../../.scratch/vecnet/issues/06-sse-and-mcp-streaming.md) that SSE resume semantics (cursor/`Last-Event-ID`) are specified.
- **Processed receipt semantics.** The dispatcher resolves after the agent turn. Does "processed" mean "the agent turn finished" or "the agent replied"? A turn may produce zero replies.
- **Group/team threads.** Should the agent see every message in a team thread (`ChatType: "group"`) or only mentions? openclaw has mention gating (`runtime.channel.mentions`, `groups.resolveRequireMention`) that could be reused.
- **Distribution.** Should the plugin ship as an npm package (`openclaw plugins install @scope/vecnet`, update-tracked) or as a local path/link? npm needs a published package. A local path needs `plugins.allow` pinning.
- **Upstream drift.** This checkout is from 2026-03-13. Plugin SDK subpaths and runtime method names have shifted before (e.g. `registerHttpHandler` was deprecated). Re-verify against the deployed openclaw version before implementation.
