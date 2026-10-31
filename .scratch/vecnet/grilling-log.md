# vecnet grilling log (wayfinder charting session, 2026-11-01)

Running record of settled decisions during charting. Will be folded into map.md.

## Round 1 (settled)
- Destination: handoff-ready spec (protocol doc, SPEC.md, GLOSSARY.md, ADRs). Not code.
- Topology: one self-hosted hub with public HTTPS; LAN peers connect outbound. No mesh in v1.
- Tenancy: single operator in v1; peer ids namespaced so a shared hub stays possible.
- Ecosystem is CLOSED, friend-to-friend: operator enrols every peer. Not open any-agent-to-any-agent (contrast c2c).
- c2c: integration parked for now ("leave off to the side"). Connector idea agreed in principle; exact meaning undefined -> fog.
- Delivery: at-least-once, dedupe on delivery id (same id+digest = same receipt; same id+different bytes = 409), bounded retries then operator-visible dead letter, per-thread ordering, receipts split accepted vs processed.
- Loop control: hop count, TTL, sender chain on every message; optional per-thread budget hint.
- Priority bit: wake-now vs hold-until-next-turn.
- Scope: v1 = messaging + peer directory (capability, quota state, location, presence). Hub does no scheduling; bots choose.
- Body: markdown, cap ~4 KiB (Max lowered from 64 KiB). Larger content goes as attachments; vecnet must provide a way to send larger things (hosted attachments), not references only.
- Hub supports INBOUND webhooks (generic ingress, e.g. GitHub) routed to agents, in addition to outbound push webhooks.
- Parked/fog: "run a command" on message (Max: beside the point); outbound "fire a webhook back" for non-bot targets.

## Process notes from Max
- Ask cc-pagchat-cos for its subagent protocol and adopt the same.
- Workflows: write one myself, have Opus write one for the same task, synthesize into one good (not big) super workflow.

## Round 2 (settled)
- Inbound webhook ingress per source with routing rule to peer/thread; GitHub first-class with signature verification. (yes)
- Attachments: hub hosts bytes (content-addressed blob store, ~25 MiB cap, TTL/retention, scoped read tokens so a reference does not leak); references (hash+size+media type+optional origin URL) mandatory on every attachment; 4 KiB cap is text-only. (yes; pagchat shape adopted)
- Membership: operator enrols every peer with its own secret; enrolled peers may message by default; send scopes can restrict; no open registration. (yes)
- Threads: first-class, flat, reply links, original_thread_id for provider mapping. (yes)
- Groups: TWO forms. (a) operator-managed named teams (collections). (b) ad hoc private group chats created by a bot with 2-4 members, used as needed. No all-peers broadcast.
- Human: operator is a peer kind. Interfaces: web inbox FIRST, and a chat UI too (both wanted, not v1-excluded).
- Events: presence (idle/working/blocked), ack + read receipts, minimal approval-request kind with expiry. (yes)
- Transports v1: signed webhook push; MCP server w/ event subscriptions (dots); SSE pull (resumable via Last-Event-ID/cursor, ~15 s heartbeats, long-poll/plain poll as documented fallback); HTTP send API. (yes; pagchat prefers SSE over long-poll)
- Operator observability: dashboard (peers, threads, dead letters, audit log) + CLI. (yes)

## From pagchat (2026-11-01)
- Subagent protocol = shared skill ~/.claude-shared/skills/subagent-protocols/ (file-backed inbox handshake; watch-file.sh). Adopt for vecnet subagents.
- Outbound from pag-chat: HTTP POST with per-peer key, Standard Webhooks HMAC (w2m grokbot-contract).

## Hark friction
- 'over' did not finalize round-2 listen; ended via `hark listen-end`. Filed bl bug in ~/src/grok/hark.

## Round 3 (settled)
- Hub on a public box (VPS or Fly): yes. Fly vs VPS cost question open -> research ticket (hosting IGC).
- Connectors v1: grokbot, dots, generic webhook/SSE peers (pagchat), coding sessions via `vecnet` CLI. Openclaw: via an openclaw add-on/extension ("it'll do it anyway") -> research its extension surface.
- History: retained indefinitely; participants fetch full thread history; only operator purges. (yes)
- Addressing: ids only; name lookup explicit. Namespacing (e.g. per computer) possible later; for now flat peer ids in one namespace.
- Ad hoc groups: yes as proposed.
- Capability/quota: self-reported via presence, stale on timeout. PRINCIPLE (Max): dynamic bot behaviour change should EMERGE from primitives the bots compose, not be a named hub feature. Build the parts; bots create the rest.
- Job request envelope: yes, borrow webhook2mcp. Max wants to SEE w2m's shape -> task.
- Operator notifications: yes via configurable webhooks. PLUS: escalation: messages needing user input (approval/needs-input) get automatically elevated to the operator.
- NEW (fog): managed-bot harness. For bots we run ourselves, a compact "session" = list of tasks + summaries of what has happened, not a coding-agent-style log. Max wants to experiment; it's a seam, not a v1 bus feature.
