# vecnet — wayfinder map

Label: wayfinder:map
Created: 2026-11-01
Tracker: local markdown (`.scratch/vecnet/`), per the mattpocock-skills `issue-tracker-local.md` conventions.

## Destination

**Reached 2026-11-01.** All 19 tickets resolved; frontier empty. Deliverables: `SPEC.md`, `docs/PROTOCOL.md` (+ `docs/protocol/OPEN-ITEMS-RESOLVED.md`), `docs/protocol/envelope.schema.json` + examples + `ENVELOPE.md`, `GLOSSARY.md`, `docs/adr/0001-tech-stack.md`. Next effort: implementation (plan in `docs/plans/`).

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
- **Pace (Max, 2026-11-01: "built asap").** With Max live on voice, grilling tickets are batched two or three per voice round in one session rather than one ticket per session; each still gets its own resolution and map line.
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
- [Message envelope schema](issues/08-envelope-schema.md): `SendRequest` → `Message` → one `Delivery` per recipient, each an `Event` in the recipient's log; receipts and presence are Event types not messages; `priority` required (`wake`|`hold`); loop = hops + `expires_at` + `sender_chain`, thread budget in `Delivery.thread`; prefixed ULID ids; id-only addressing; 4 KiB text + sha256 attachments with scoped `blob_url`. Schema, examples and IGCs in `docs/protocol/`. pag-chat review pending as amendments.
- [Thread and group lifecycle](issues/09-threads-and-groups.md): threads implicit on first send, participants grow by addressing, replies never fork, cross-thread reply links allowed; teams operator-managed, ad hoc groups peer-created within scopes and auto-archived after 30 idle days; connectors own external-id mapping.
- [Delivery state machine](issues/10-delivery-state-machine.md): queued → pushed → accepted → processed; terminal failed/expired/dead_letter; w2m retry classes (8 attempts, 1 h cap, Retry-After); 409 alerts; per-(peer, thread) push serialisation; hold accepted on cursor ack; dead letters visible, retry/discard by operator.
- [Enrolment, keys, scopes, audit](issues/11-auth-and-enrolment.md): CLI enrolment with one-time token → long-lived bearer; per-route `whsec_` with 24 h dual-signed rotation; dots authenticate per ChatGPT account via the hub's own OAuth 2.1 PKCE server, one account → one dot peer today; default allow-all scopes with per-peer allow/deny; append-only audit without bodies or secrets.
- [Inbound webhook sources and routing rules](issues/12-inbound-webhook-ingress.md): `vecnet source add` prints an ingress URL + secret; GitHub HMAC verified; payload → summary text + raw JSON attachment with issue/PR number as `original_thread_id`; one static target per source plus event filter; sources are not peers and the hub never posts back.
- [Attachment blob store](issues/13-blob-store.md): `PUT /v1/blobs` by any peer, sha256 dedupe, 25 MiB cap, daily per-peer quota; per-delivery `blob_url` token scoped to recipient+hash for 7 days; referenced blobs live with their messages, unreferenced GC after 24 h.
- [Peer directory and presence](issues/14-directory-and-presence.md): directory fields fixed; **presence is self-reported** (stale → `unknown` after per-kind TTL) and **reachability is hub-observed per transport** (`connected`/`reachable`/`unreachable`/`unknown`), so push-only dots are never inferred offline; presence fan-out opt-in, default teammates.
- [Escalation and approval requests](issues/15-escalation-and-approval.md): approval lifecycle in v1 (first valid response closes, 409 after, hub posts `expired` at expiry); operator notification webhooks with filters in v1; **escalation forwarding deferred to phase 2** at Max's request.
- [Loop control enforcement](issues/16-loop-control.md): field renamed `depth`/`max_depth` (Max read "hops" as hub-to-hub); hub rejects synchronously with 422 reason codes; defaults 16 / 24 h / 500 per thread; only operator raises budgets; client breaker is a configurable connector recommendation (default 12 alternations in 10 min).
- [The vecnet CLI for coding sessions](issues/17-vecnet-cli.md): one hub = one network, CLI profiles of hub + credential, operator credential for admin commands; one peer per long-lived agent identity; `vecnet watch --for-monitor` wakes sessions like `c2c monitor`; single static binary.
- (amendment to 08 and 11, from pag-chat's review 2026-11-01) **Principals**: every peer belongs to a `prn_` credential holder that may send `as` any of its peers and hold one stream or push route for all of them; `priority` gains `wake_idle`; structured `origin` replaces `original_thread_id`; receipts carry `thread_id`; hub auto-spills long text into an attachment; `job_key` scope is (`thread_id`, `job_key`). Disposition table in `docs/protocol/ENVELOPE.md`.
- (amendments from pag-chat's PROTOCOL.md review, 2026-11-01) operator replies reset `depth`, default `max_depth` 32; per-peer rate limits (60 sends/min, 20 threads/h, 429); `history_access` default `from_join` + `locked_participants` + visibility-gated blob reads (**changes Max's round-3 "late joiners get full history" default; flagged for his veto**). PROTOCOL.md §16.
- [Operator web inbox, chat UI and dashboard](issues/18-operator-ui.md): three-variant prototype in `docs/prototypes/operator-ui/` (shots in `shots/`). Max: **variant A is the base** (inbox + chat), **a TUI is wanted as its own thing**, dashboard to be redone cleaner later (B's tiles too busy), chat borrows B's side-panel use of horizontal space with the delivery matrix as an extra view and thread files in the side panel. Inbox and chat separate but linked; one-tap operator presence toggle; dashboard read-only.
- (Max, final voice round 2026-11-01) accepted the pag-chat amendments including the `history_access` default reversal, `wake_idle` re-push on idle, loop accounting with operator depth reset, per-peer rate limits, and **go to build planning**.
- [Live test: a real dot receives an MCP event](issues/19-dots-live-test.md): resolved by Max: a dot already talks to Grok Bots through webhook2mcp, so custom MCP plugins, Events push and unattended tool replies all work. UI landing spot and confirmation prompts remain unrecorded, non-blocking.

## Not yet specified

- **c2c connector.** Max parked c2c integration (confirmed again at ticket 17: stays in the fog until after v1). "Connector" is agreed in principle; what it concretely means (does c2c relay get superseded? does a session register via the `vecnet` CLI instead?) is fog until [The vecnet CLI for coding sessions](issues/17-vecnet-cli.md) resolves.
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

## Build (Phase 1 core hub), started 2026-11-01

- Plan: `docs/plans/2026-11-01-core-hub.md` (20 TDD tasks). Workflow: `docs/plans/vecnet-core-build.workflow.mjs`, synthesised from a coordinator draft and an Opus draft (Max's workflow-synthesis process); the Opus draft's hazard list was folded into the plan text.
- Execution: worktree `.worktrees/core-hub`, branch `core-hub`; status files in `.scratch/vecnet/build/` (gitignored); run in chunks (tasks 1–5, 6–11, 12–17, 18–20 + branch review) so the coordinator stays in the loop between chunks.
- Later plans: connectors (MCP/Events/OAuth, Grok Bot route, openclaw, GitHub ingress), operator UI (variant A + TUI), phase-2 behaviours.
- **Done 2026-11-01:** all 20 tasks merged to master (044fabd). 61 workflow agents over four chunks (~4 h wall clock, ~7.4M subagent tokens); fix rounds on tasks 11, 12, 16, 18; branch review found 7 serious + 21 minor, 36 fixed; the gate caught and reverted a loop-control relaxation in the fix pass (sends without `in_reply_to` now continue the loop; operator sends reset the TTL, PROTOCOL §6/§16.1). Deferred to the connectors plan: SSRF-guarded egress dialer for push/notify URLs (operator-only today). Dev hub: `just run --dev-ui-dir internal/ui/static`, UI at http://127.0.0.1:45752/ui/, operator secret in `.data/operator-secret.txt`.
- Leftover merged branches `core-hub`, `core-hub-t18`, `core-hub-t19`: deleting branch refs is reserved for Max.

## Build (Phase 2 connectors), done 2026-11-01

- Plan `docs/plans/2026-11-01-connectors.md`; merged to master at eb5d197 (10 tasks, 53 workflow agents, about 9.2M subagent tokens, 11:35 to 16:20).
- Branch review found 14 major and 24 minor; all fixed except tests-14 (rejected: no flake reproduced). Theme: access outliving its credential (Events subscriptions, listen streams, /v1/events SSE), dots able to get a bearer, held wake_idle never pushed to a dot.
- The fixer amended PROTOCOL §2.3, §2.12, §4.2, §4.4, §4.5, §5.2, §10, §12, §14 inv. 7 and 8, §15. Reviewed: these tighten behaviour, with one relaxation: the "untrusted" label on dot payloads lives in the events/list description and payloadSchema, not on each POST, to keep the profile's five fields.
- Not done: no live ChatGPT dot test yet (issue 19), which is an operator step. The first real Grok Bot routine and GitHub webhook have not been tried either. Link-code guessing has no failure limiter (39.6 bits, 10 min life).
- `vecnet-build.mjs` now schedules by `deps` with a per-plan `taskPrefix`. First real use was tasks 9 and 10.
- Next plans written and committed: `docs/plans/2026-11-01-openclaw-plugin.md` (21 tasks), `docs/plans/2026-11-01-operator-ui.md` (23 tasks). Neither build has started.
- Leftover branches for Max to delete: connectors, connectors-t09, connectors-t10, core-hub, core-hub-t01..08, core-hub-t18, core-hub-t19.
- Dev hub data backup from before migrations 0005-0009: `.scratch/vecnet/build-connectors/data-backup-phase1`.

## Build (Phase 3 operator UI), done 2026-11-02

- Plan: `docs/plans/2026-11-01-operator-ui.md` (25 tasks, including cross-client synchronization and integrated screen gate fixes); operator guide: `docs/OPERATOR.md`; screenshot index: `docs/plans/shots/oui/README.md`.
- Implemented: operator read models, seeded demo and screen harness, web inbox/chat/aside/matrix/dashboard/subscriptions/presence/compose, typed client and terminal inbox/chat/dashboard/subscriptions/command line.
- Task 24 supplies durable `operator_view` invalidations and the shared unread header. Task 23 verifies TUI approval → web removal, web read → terminal unread drop, peer wake → both clients, web presence → terminal status, both clients' presence heartbeats, one browser events 401 → sign-in without retries, and hub-authored expiry → both clients' histories. Task 25 supplies the integrated screen fixes. The full Go/JS suites, vet, formatting, static binaries, ISA stamps, protocol examples, dependency boundaries, isolated screenshot gate and two-client journey pass. The journey waits for the real TUI heartbeat (20 minutes). Verification details: `.scratch/vecnet/build-operator-ui/task-23.impl.json`.
- Dashboard remains read-only; subscription recovery and failed-delivery actions live in the inbox. Openclaw and later-phase behaviours are separate plans.
