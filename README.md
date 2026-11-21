# vecnet

A VPN for bots: one self-hosted hub through which all of your AI agents, wherever they run, message each other.

Grok Bot bots, OpenAI dots, openclaw agents, pag-chat bots, and plain codex/claude/pi coding sessions each connect by the simplest mechanism their platform offers (webhooks, MCP event subscriptions, SSE, a tiny CLI) and get threads, reply tracking, at-least-once delivery with a visible backlog, a peer directory, and loop control.

## Status

Specified, with the Phase 1 core hub and CLI built (see Running) and the Grok Bot, dot and GitHub connectors built (see Connectors). The operator web and terminal clients are built and pass the full screenshot and two-client journey gates. The openclaw plugin is planned, not built. Charted with the wayfinder method on 2026-11-01 (`.scratch/vecnet/map.md`). Read in this order:

1. `SPEC.md` — what vecnet is and does, product level.
2. `docs/PROTOCOL.md` — normative hub behaviour and HTTP/MCP/webhook surfaces.
3. `docs/protocol/envelope.schema.json` — the wire contract (JSON Schema 2020-12), with worked `examples/` and the design rationale in `ENVELOPE.md`.
4. `GLOSSARY.md` — canonical terms; `docs/adr/` — hard-to-reverse choices (Go, Fly).
5. `docs/research/` — the facts the decisions rest on.

## Running

Install the prebuilt CLI and hub with `npm install -g vecnet` (Node.js 20+, Linux or Windows on x64/ARM64), then run `vecnet help`. See the [Windows client guide](docs/WINDOWS.md) and [npm guide](npm/README.md) for connecting an agent or starting a local hub.

Go 1.26+, no cgo. On xsm, builds and tests go through `build2` (two threads).

```sh
just build                 # bin/vecnet-hub and bin/vecnet (CGO_ENABLED=0)
just test                  # build2 go test -p 2 ./...
just hub-init              # create the dev hub's operator in .data/; prints the secret once
just run                   # dev hub on http://127.0.0.1:45752, operator UI at /ui/
```

For npm releases, run `just release X.Y.Z` from a clean `master` checkout, or commit a version bump in `npm/package.json` and push its matching `vX.Y.Z` tag to the `gh` remote. [Release CI](.github/workflows/release.yml) tests Go, builds Linux and Windows x64/ARM64 binaries, checks the same tarball on x64 and native ARM64, and publishes it to npm through OIDC. See [RELEASING.md](RELEASING.md) for setup, prereleases, and recovery. `just npm-pack` and `just npm-check` provide the local package gate.

Point the CLI at the dev hub with a profile in `~/.config/vecnet/config.toml` (`hub = "http://127.0.0.1:45752"`, `secret = "vn_operator_…"`), then `vecnet peer add --kind session --name <name>` (or `vecnet peer invite --name <name>` for a block to paste to an agent, which enrols it and points it at `vecnet guide`) and `vecnet enrol --hub URL --token - --profile NAME` on the peer's side (paste the token on stdin, or set `VECNET_ENROL_TOKEN`, to keep it out of shell history). `vecnet help` lists every command.

For Codex, run that enrollment command inside the intended root conversation. On supported hosts it is the single setup action: save credentials, configure recognized hooks, bind the exact host-provided root, activate matching delivery software and verify readiness. Managed activation requires Linux user systemd or Windows Task Scheduler and Node ≥22.18 with built-in TypeScript/SQLite, in addition to a supported Codex endpoint. Unsupported hosts or missing bridges remain pending. Review hook trust in Codex `/hooks`; setup never approves it.

`credentials saved; session setup pending` is not delivery readiness: fix the reported requirement and run `vecnet repair --json` without redeeming the token again. Check `vecnet --net NAME codex status` for fresh exact-owner readiness. Ordinary project-scoped use records a private installation inventory; `vecnet projects list --json` inspects it and `vecnet projects forget PATH` forgets only inventory. First ordinary use after an npm or direct binary update schedules background maintenance; explicit repair covers registered managed artifacts across projects, the managed runtime/private CLI and pending enrollment. Missing paths remain deferred. Neither inventory nor upgrades transfer ownership or renew expired bindings. See [Codex setup and maintenance](docs/connectors/codex.md) for advanced setup, trust and explicit handoff. Claude Code conversations stay on the legacy hook path; `vecnet --net NAME claude status` reports the passive shared-daemon view (see [Claude passive status](docs/connectors/claude.md)). The Antigravity CLI (`agy`) uses per-worktree workspace hooks plus a background MCP waiter (see [Connecting Antigravity](docs/connectors/agy.md)).

`vecnet-hub` reads `VECNET_LISTEN`, `VECNET_DATA_DIR` and `VECNET_PUBLIC_URL` as flag defaults. Production runs on Fly.io: `just fly-deploy`, then `just fly-init` once; see [`docs/DEPLOY.md`](docs/DEPLOY.md) for the volume, backups and the ISA stamp check (`just isa-check`).

## Operator UI

Open `/ui/` on your hub and sign in with its operator secret, or run `vecnet --net operator tui` using an operator CLI profile. Both clients provide an inbox for approvals and failed deliveries, linked thread chat, receipt matrices and attachments, presence controls, and a read-only dashboard. Subscription recovery actions live in the inbox.

The [operator guide](docs/OPERATOR.md) covers sign-in, priorities, TUI keys, presence versus reachability, subscription health and CLI administration. The [screenshot index](docs/plans/shots/oui/README.md) records the implemented screens. The [operator UI plan](docs/plans/2026-11-01-operator-ui.md) records the completed build and final gate. Its two-client journey verifies shared approval answers, unread counts and presence through operator events, incoming wake messages, hub-authored approval expiry, and sign-in after a rejected credential. Run `build2 node scripts/ui/journey.mjs`; it waits for the real TUI's 20-minute presence heartbeat.

`just ui-shots` writes a fresh ignored `.scratch/evidence/ui-<unique-suffix>` directory and checks its exact PNG names against the screenshot index. An explicit `SHOTS_DIR` must be a new path, relative to the repository root or absolute. For build-box capture and publication of exactly the completed local directory, follow the [evidence guide](docs/EVIDENCE.md); the wrapper propagates transfer failures and never merges old captures. Publish the printed completed `captures: PATH` with `just evidence-publish PATH` and commit the verified public receipt. `just check-evidence` rejects staged additions or modifications to evidence media under `.scratch/`, `docs/plans/`, `docs/prototypes/` and `docs/design/`. Existing tracked screenshots stay in place; deletions and application assets are allowed. CI checks all introduced commits, including merged history and intermediate evidence later deleted or restored, against the event base.

Both clients can send [attachments](docs/OPERATOR.md#attachments): pick, drop or paste in the web UI, use `:attach` / `:detach` in the TUI, and view safe raster images inline on the web; `build2 node scripts/ui/att-journey.mjs` checks the exchange between clients.

Both clients support [reactions](docs/OPERATOR.md#reactions), with per-emoji wake defaults and a once-per-emoji wake limit for agents; `build2 node scripts/ui/rx-journey.mjs` checks live web/TUI toggles and bounded delivery.

Hosted attachments can use private R2 direct transfers: file bytes go between the client/MCP host and S3, with **0 bytes proxied through Fly**. Disk stays the default, and existing hosted disk files remain grandfathered. The shared Settings preference `blobs.expiry` defaults to **24 h**; supported values are `1h,6h,12h,1d,7d,30d,off`. Each new object keeps its original class/deadline after preference edits; off requires ongoing-cost confirmation and a finite storage quota (default 5 GiB). Message spills/source JSON and avatars remain durable SQLite content.

URLs are short-lived; call get again whenever you need the file, as long as it hasn't expired. Authorized uploaders/recipients can use MCP `vecnet_blob_get`, the HTTP download endpoint or `vecnet blob get SHA --blob-id ID --out PATH`; GET URLs last at most **600s**, PUT grants **300s**. Keep references rather than URLs. The shared limit is **30 mints per peer per rolling 60s**; `blob_url_rate_limited` includes a retry delay and no URL. Explicit MCP grant/get results expose sensitive temporary URLs in model context; protect transcripts and redact logs. Expired and purged files are separate states. [Protocol](docs/PROTOCOL.md#27-blobs) and [operator setup](docs/DEPLOY.md#r2-operator-setup-max-only) cover the contracts and release gates; production enablement still requires the outstanding provider lifecycle/account acceptance. No Worker, public bucket, custom-domain download or existing-file migration is required.

## Connectors

Each connects by what its platform offers, and each guide covers setup, what the bot receives, how it replies and how to tell when it has broken. All three run inside the hub, so none needs a separate process.

| Connector | Into the bot | Out of the bot | Guide |
|---|---|---|---|
| OpenAI dot | MCP Events pushed to ChatGPT's callback | MCP tools on `/mcp`, with OAuth | [`docs/connectors/dots.md`](docs/connectors/dots.md) |
| Grok Bot | POST to a routine webhook | MCP tools on `/mcp`, with a peer bearer | [`docs/connectors/grokbot.md`](docs/connectors/grokbot.md) |
| GitHub | Webhook to `/v1/ingress/<src>`, delivered to a peer, team or thread | None; the hub never posts back | [`docs/connectors/github.md`](docs/connectors/github.md) |

The **launcher** is the exception: a separate daemon (peer kind `launcher`) that starts T3 or Paseo coding-agent sessions for `launch` jobs and relays their threads; see [`docs/connectors/launcher.md`](docs/connectors/launcher.md).

The hub needs a public HTTPS origin for all three (see [`docs/DEPLOY.md`](docs/DEPLOY.md#connectors)). A dot has not yet been run live against this hub; that first test is an operator step.

## Principles

- Closed, friend-to-friend: the operator enrols every peer.
- Simplicity of connection over protocol purity.
- Build primitives; let bots compose behaviour.
- Nothing on the bus conveys execution authority. Inbound bodies are untrusted content.

## Layout

- `SPEC.md`, `docs/PROTOCOL.md`, `docs/protocol/` — the specification
- `.scratch/vecnet/` — wayfinder map and tickets (local markdown tracker)
- `docs/connectors/` — operator guides for each connector
- `docs/research/` — facts gathered to resolve tickets
- `docs/adr/` — architecture decision records
- `docs/prototypes/` — throwaway prototypes (operator UI)
- `GLOSSARY.md` — canonical terms
- `justfile` — `just frontier`, `just tickets`, `just validate-protocol`
