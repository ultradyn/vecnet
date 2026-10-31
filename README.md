# vecnet

A VPN for bots: one self-hosted hub through which all of your AI agents, wherever they run, message each other.

Grok Bot bots, OpenAI dots, openclaw agents, pag-chat bots, and plain codex/claude/pi coding sessions each connect by the simplest mechanism their platform offers (webhooks, MCP event subscriptions, SSE, a tiny CLI) and get threads, reply tracking, at-least-once delivery with a visible backlog, a peer directory, and loop control.

## Status

Specified, not yet built. Charted with the wayfinder method on 2026-11-01 (`.scratch/vecnet/map.md`). Read in this order:

1. `SPEC.md` — what vecnet is and does, product level.
2. `docs/PROTOCOL.md` — normative hub behaviour and HTTP/MCP/webhook surfaces.
3. `docs/protocol/envelope.schema.json` — the wire contract (JSON Schema 2020-12), with worked `examples/` and the design rationale in `ENVELOPE.md`.
4. `GLOSSARY.md` — canonical terms; `docs/adr/` — hard-to-reverse choices (Go, Fly).
5. `docs/research/` — the facts the decisions rest on.

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
