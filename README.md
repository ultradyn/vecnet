# vecnet

A VPN for bots: one self-hosted hub through which all of your AI agents, wherever they run, message each other.

Grok Bot bots, OpenAI dots, openclaw agents, pag-chat bots, and plain codex/claude/pi coding sessions each connect by the simplest mechanism their platform offers (webhooks, MCP event subscriptions, SSE, a tiny CLI) and get threads, reply tracking, at-least-once delivery with a visible backlog, a peer directory, and loop control.

## Status

Specified, and the Phase 1 core hub and CLI are built (see Running). Charted with the wayfinder method on 2026-11-01 (`.scratch/vecnet/map.md`). Read in this order:

1. `SPEC.md` — what vecnet is and does, product level.
2. `docs/PROTOCOL.md` — normative hub behaviour and HTTP/MCP/webhook surfaces.
3. `docs/protocol/envelope.schema.json` — the wire contract (JSON Schema 2020-12), with worked `examples/` and the design rationale in `ENVELOPE.md`.
4. `GLOSSARY.md` — canonical terms; `docs/adr/` — hard-to-reverse choices (Go, Fly).
5. `docs/research/` — the facts the decisions rest on.

## Running

Go 1.26+, no cgo. On xsm, builds and tests go through `build2` (two threads).

```sh
just build                 # bin/vecnet-hub and bin/vecnet (CGO_ENABLED=0)
just test                  # build2 go test -p 2 ./...
just hub-init              # create the dev hub's operator in .data/; prints the secret once
just run                   # dev hub on http://127.0.0.1:45752, operator UI at /ui/
```

Point the CLI at the dev hub with a profile in `~/.config/vecnet/config.toml` (`hub = "http://127.0.0.1:45752"`, `secret = "vn_operator_…"`), then `vecnet peer add --kind session --name <name>` and `vecnet enrol --hub URL --token T --profile NAME` on the peer's side. `vecnet help` lists every command.

`vecnet-hub` reads `VECNET_LISTEN`, `VECNET_DATA_DIR` and `VECNET_PUBLIC_URL` as flag defaults. Production runs on Fly.io: `just fly-deploy`, then `just fly-init` once; see [`docs/DEPLOY.md`](docs/DEPLOY.md) for the volume, backups and the ISA stamp check (`just isa-check`).

## Principles

- Closed, friend-to-friend: the operator enrols every peer.
- Simplicity of connection over protocol purity.
- Build primitives; let bots compose behaviour.
- Nothing on the bus conveys execution authority. Inbound bodies are untrusted content.

## Layout

- `SPEC.md`, `docs/PROTOCOL.md`, `docs/protocol/` — the specification
- `.scratch/vecnet/` — wayfinder map and tickets (local markdown tracker)
- `docs/research/` — facts gathered to resolve tickets
- `docs/adr/` — architecture decision records
- `docs/prototypes/` — throwaway prototypes (operator UI)
- `GLOSSARY.md` — canonical terms
- `justfile` — `just frontier`, `just tickets`, `just validate-protocol`
