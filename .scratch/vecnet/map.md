# vecnet — wayfinder map

Label: wayfinder:map
Created: 2026-11-01
Tracker: local markdown (`.scratch/vecnet/`), per the mattpocock-skills `issue-tracker-local.md` conventions.

## Destination

A handoff-ready **spec** for vecnet v1: `SPEC.md` (product + behaviour), `docs/PROTOCOL.md` (envelope, transports, delivery, auth), `GLOSSARY.md`, and ADRs for the hard-to-reverse choices, such that implementation agents can build the hub, the connectors and the `vecnet` CLI without further decisions from Max.

## Notes

- vecnet = "a VPN for bots": one self-hosted hub through which all of Max's agents (grokbot bots, OpenAI dots, openclaw, pag-chat bots, codex/claude sessions, the human operator) message each other with threads, reply links, delivery guarantees and a peer directory.
- **Closed, friend-to-friend ecosystem.** The operator enrols every peer. Not an open any-agent-to-any-agent network (contrast c2c).
- **Simplicity of connection is the core value.** Each provider connects by the simplest mechanism it supports: webhooks for grokbot, MCP event subscriptions for dots, SSE for LAN peers and sessions.
- **Build primitives, not named features.** Max: dynamic bot behaviour, task distribution by capability/quota, etc. should emerge from bots composing bus primitives. The hub does no scheduling in v1.
- **webhook2mcp is inspiration, not a constraint** (Max, 2026-11-01). Borrow its proven ideas (id separation, signing, receipt vs job.status, MCP Events profile); do not treat code reuse as a goal.
- Decisions use IGC matrices (`use-igcs` skill). HITL tickets run over hark (voice). Max delegates technical choices (stack, framework, hosting mechanics) to the agent; product behaviour is his.
- pag-chat (`cc-pagchat-cos` over c2c `--cross-repo`) is the first external consumer; it reviews the envelope draft. Its requirements are in `grilling-log.md`.
- Subagents follow the shared `subagent-protocols` skill (file-backed coordinator inbox at `~/.cache/agent-comms/vecnet4/coord-inbox.md`).
- Workflows: author one, have Opus author one for the same task, synthesize into one good super workflow.
- Facts gathered during charting: `docs/research/transports-facts.md` (webhook2mcp, Grok Bot, dots, openclaw, c2c).
- Full charting transcript of decisions: [`grilling-log.md`](grilling-log.md).

## Decisions so far

<!-- one line per closed ticket; charting-session decisions are summarised in grilling-log.md -->

- (charting) Destination is a spec, not code; single public hub; closed membership; pagchat delivery baseline (at-least-once, dedupe, bounded retry, dead letter, per-thread ordering, accepted-vs-processed receipts); loop control (hop, TTL, sender chain, thread budget); priority bit (wake-now vs hold); 4 KiB text bodies + hosted content-addressed attachments; inbound webhook ingress (GitHub first); flat first-class threads; named teams + ad hoc groups; operator is a peer with web inbox first then chat UI; presence/ack/read/approval events; four transports (signed webhook push, MCP events, SSE, HTTP send); id-only addressing; indefinite history; self-reported capability/quota; w2m-style job envelope; escalation to operator. See [`grilling-log.md`](grilling-log.md).
- [webhook2mcp job envelope and signing, shown to Max](issues/04-w2m-envelope.md): borrow verbatim the Standard Webhooks signing, the separate message/delivery/thread/job ids with `delivery_id` as dedupe key, and the 2xx-receipt vs signed `job.status` split; adapt content to 4 KiB, fan groups out per member, add attachment hashes, priority, hop/ttl/sender_chain. Shapes in `docs/research/w2m-envelope.md`.
- [Where the hub runs: Fly vs VPS vs other](issues/05-hosting-igc.md): NAS + tunnel refuted (home outage goal); Fly auto-stop refuted (SSE streams); Fly always-on (~USD 4/mo) and Hetzner both pass. **Max confirmed Fly always-on (2026-11-01).** Requires nightly off-host DB+blob copy.
- [openclaw connector surface](issues/03-openclaw-surface.md): an openclaw **channel plugin** shipped as an extension; it dials out to the hub over SSE so openclaw needs no public ingress; replies and agent-initiated sends POST back via the plugin's outbound hooks. Gateway HTTP/WS surfaces rejected (operator token + inbound reachability). Details in `docs/research/openclaw.md`.
- [One client path for SSE and MCP Streamable HTTP](issues/06-sse-and-mcp-streaming.md): two endpoints over one durable per-peer event log. `/v1/events` serves SSE or long-poll by `Accept`, cursor `<epoch>.<seq>` as SSE id, `Last-Event-ID` replay, 15 s heartbeat comments, 409 on purged cursor. `/mcp` exposes the inbox as a resource; `notifications/resources/updated` is only a wake hint and `vecnet_inbox(after)` / `vecnet_ack(cursor)` tools read the same log, because MCP 2026-07-28 dropped sessions, GET streams and `Last-Event-ID`. Details in `docs/research/sse-mcp.md`.
- [OpenAI dots MCP event subscription facts](issues/02-dots-mcp-events.md): a dot is reached only via OpenAI's MCP Events profile in **webhook mode**: ChatGPT calls `events/subscribe` on our `/mcp` with a callback URL + `whsec_`, we verify by challenge echo and push Standard-Webhooks-signed events (≤ 256 KiB) with a cursor; ChatGPT re-subscribes before `refreshBefore`. Auth is OAuth 2.1 PKCE as the ChatGPT user (no API keys), so the hub sees a user principal, not a dot. Dots reply by calling our write tools. `/mcp` needs public HTTPS or OpenAI's outbound tunnel. Eight open questions, the go/no-go one became [Live test: a real dot receives an MCP event](issues/19-dots-live-test.md). Details in `docs/research/dots.md`.
- [Grok Bot connector facts](issues/01-grokbot-contract.md): Grok Bot (xAI + Cursor) has exactly one external ingress, a per-routine webhook `POST` with a static `Bearer crsr_` key and no HMAC; 200 means a run *started*; no server-side retries; 300 runs/h per account, 200 per routine, every run spends weekly quota; **no native outbound** (bots reply only via tools such as webhook2mcp); **no thread ids exposed**, so `original_thread_id` must ride in the body and be echoed back; every accepted POST wakes the bot, so the **hold** priority must be implemented hub-side (park in the event log, fetched on the bot's pull). Details in `docs/research/grokbot.md`.
- [Tech stack for hub, connectors and CLI](issues/07-tech-stack-igc.md): **Go** (revised from the agent's Node/TS pick after Max objected). Max dropped the "reuse w2m code verbatim" goal (vecnet is inspired by w2m, not bound to it) and added "single static binary, no runtime". Go and Rust survive; Go wins on Max's preference and because `CGO_ENABLED=0` builds carry no AVX-512 stamp. Stack: Go stdlib `net/http`, official MCP go-sdk + hand-rolled MCP Events, pure-Go SQLite (`modernc.org/sqlite`, WAL), embedded SQL migrations, blobs at `/data/blobs/sha256/aa/bb/<hex>`, CLI as a second `cmd/` binary, scratch Docker image to Fly, dev port 45752. ADR `docs/adr/0001-tech-stack.md`.
- [Live test: a real dot receives an MCP event](issues/19-dots-live-test.md): resolved by Max: a dot already talks to Grok Bots through webhook2mcp, so custom MCP plugins, Events push and unattended tool replies all work. UI landing spot and confirmation prompts remain unrecorded, non-blocking.

## Not yet specified

- **c2c connector.** Max parked c2c integration. "Connector" is agreed in principle; what it concretely means (does c2c relay get superseded? does a session register via the `vecnet` CLI instead?) is fog until [The vecnet CLI for coding sessions](issues/17-vecnet-cli.md) resolves.
- **Local-action transport.** "Fire a webhook back or run a command" on message arrival for non-bot targets (e.g. a machine running a script). Max: beside the point for now.
- **Peer id namespacing.** Flat ids in one namespace for v1; per-computer or other namespacing may come. Depends on enrolment design.
- **Managed-bot harness.** For bots Max runs himself: a compact "session" = list of tasks + summaries of what has happened, not a coding-agent-style log. Max wants to experiment; a client of the bus, not a bus feature. Likely its own effort.
- **Emergent dynamic behaviour.** Bots changing each other's behaviour via messages (prompts, config). Revisit once envelope + metadata are fixed to check the primitives suffice.
- **Attachment ingestion from provider side.** How a grokbot/dot attaches a file it cannot upload to the blob store itself (connector mirrors it?). Depends on connector research.

## Out of scope

- Hub-side scheduling / routing by capability, quota, location or latency (v1 ships the directory only; bots choose).
- Multi-operator tenancy, billing, open registration.
- Peer-to-peer / mesh topology; hub is the only topology.
- Nested sub-threads.
- All-peers broadcast (teams and ad hoc groups only).
- Replacing c2c's local same-machine broker.
