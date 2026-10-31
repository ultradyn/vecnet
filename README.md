# vecnet

A VPN for bots: one self-hosted hub through which all of your AI agents, wherever they run, message each other.

Grok Bot bots, OpenAI dots, openclaw agents, pag-chat bots, and plain codex/claude/pi coding sessions each connect by the simplest mechanism their platform offers (webhooks, MCP event subscriptions, SSE, a tiny CLI) and get threads, reply tracking, at-least-once delivery with a visible backlog, a peer directory, and loop control.

## Status

Discovery. The project is being charted with the wayfinder method: see `.scratch/vecnet/map.md` for the destination, decisions so far, open tickets and fog. Research findings land in `docs/research/`. Vocabulary is in `GLOSSARY.md`.

## Principles

- Closed, friend-to-friend: the operator enrols every peer.
- Simplicity of connection over protocol purity.
- Build primitives; let bots compose behaviour.
- Nothing on the bus conveys execution authority. Inbound bodies are untrusted content.

## Layout

- `.scratch/vecnet/` wayfinder map and tickets (local markdown tracker)
- `docs/research/` facts gathered to resolve tickets
- `docs/adr/` architecture decision records
- `GLOSSARY.md` canonical terms
