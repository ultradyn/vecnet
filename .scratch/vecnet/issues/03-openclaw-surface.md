# openclaw connector surface

Type: research
Status: resolved
Blocked by:

## Question

`~/src/openclaw` has an `extensions/` dir and a gateway. Which surface should a vecnet connector use: an openclaw extension (Max's preference: "an add-on, it'll do it anyway"), a channel plugin, or the gateway HTTP API? What does an inbound message to an openclaw agent look like and how does the agent send out? Deliverable: `docs/research/openclaw.md` with the recommended surface.

## Answer

Use an **openclaw channel plugin**, shipped as an extension. This matches Max's add-on preference: in openclaw a "channel" is one thing a plugin registers via `api.registerChannel`.

- **Inbound:** the plugin's `gateway.startAccount` dials out to the hub (SSE/long-poll, IRC-style, so no public ingress is needed). Each envelope becomes an inbound context via `runtime.channel.reply.finalizeInboundContext` (with `resolveAgentRoute` for per-peer/thread sessions). It is then dispatched with `dispatchReplyWithBufferedBlockDispatcher`.
- **Replies:** these come back through the dispatcher's `deliver` callback, which POSTs to the hub.
- **Agent-initiated sends:** the built-in `message` tool reaches the plugin's `outbound.sendText` with a `vecnet:<id>` target.
- **Rejected:** `/hooks/agent` returns no reply. `/v1/chat/completions`, `/v1/responses` and the WS/ACP paths all need an operator-level gateway token and inbound reachability.
- **Deployment:** the repo's `fly.toml` exposes a public HTTPS service. `fly.private.toml` has no public ingress. Which one Max runs is unknown.
- **Pitfall:** don't copy the nostr plugin. It calls a non-existent `handleInboundMessage`, so its inbound is silently dropped.

Details, file citations, the connector's job list and open questions: [docs/research/openclaw.md](../../../docs/research/openclaw.md).
