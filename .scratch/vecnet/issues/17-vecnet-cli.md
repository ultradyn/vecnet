# The vecnet CLI for coding sessions

Type: grilling
Status: resolved
Blocked by: 06, 08

## Question

Commands for a codex/claude/pi session to enrol, subscribe (SSE), send, reply, list threads, and present inbound messages; how it coexists with c2c (fog: c2c connector); how it wakes an idle session (Monitor-style).

## Answer

Max approved by voice, 2026-11-01 ("everything sounds pretty ordinary"), including the answer to his VPN-analogy question about accounts and networks.

- **Networks and profiles:** one hub is one network ("a vecnet"). The CLI keeps named profiles in `~/.config/vecnet/config.toml`, each a hub URL plus a credential (a peer's, a principal's, or the operator's); `--net <name>` selects, with a default. Admin commands (`peer add`, `source add`, `route`, `scope`) need the operator credential; session commands use a peer or principal credential (`--as <peer>` when a principal owns several). Several networks are supported client-side via profiles; several networks on one hub is out of scope for v1.
- **Session identity:** one peer per long-lived agent identity (e.g. `cc-vecnet-cos`), minted once by the operator; each new session logs into that identity with the stored credential rather than enrolling a fresh peer.
- **Commands:** `send <peer|group> "text" [--thread] [--reply-to] [--wake|--wake-idle|--hold] [--attach FILE]`, `reply <message_id> "text"`, `inbox [--since CURSOR] [--ack]`, `ack <cursor>`, `watch [--for-monitor]`, `threads`, `thread <id> [--after CURSOR]`, `peers`, `groups create|add|list`, `presence set <state>`; admin: `peer add|rotate|scope`, `source add`, `route add`, `dead-letters list|retry|discard`.
- **Waking an idle session:** `vecnet watch --for-monitor` holds the SSE stream and prints one compact line per event, for use in a Monitor tool exactly as `c2c monitor` is used today. Hook-based delivery for specific harnesses is later. The c2c connector stays in the fog until after v1.
- **Distribution:** single static Go binary per OS/arch (ADR 0001).
