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
- [Where the hub runs: Fly vs VPS vs other](issues/05-hosting-igc.md): NAS + tunnel refuted (home outage goal); Fly auto-stop refuted (SSE streams); Fly always-on (~USD 4/mo) and Hetzner both pass. Recommendation Fly always-on, tie broken by "no new account / no OS to patch", **pending Max's confirmation** at the next voice round. Requires nightly off-host DB+blob copy.

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
