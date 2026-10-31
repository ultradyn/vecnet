# Peer directory and presence

Type: grilling
Status: resolved
Blocked by: 08

## Question

Directory fields (id, display name, kind, capabilities, quota state, location, latency hints), presence states (idle/working/blocked), freshness timestamp and stale timeout, how a peer updates its entry, what is exposed to other peers vs operator only.

## Answer

Max approved fields and fan-out by voice, 2026-11-01, and corrected the staleness rule: a dot (push-only, never connected) must not be inferred "offline" after ten minutes of silence.

- **Directory entry:** `peer_id`, display name, kind (`grokbot`, `dot`, `openclaw`, `pagchat-bot`, `session`, `operator`, `source`), `capabilities[]` (free-form tags), `quota` (free-form object), `location`, `presence`, `reachability`, `last_seen`, `enrolled_at`, `teams[]`. Every peer sees every peer's public fields; only the operator sees credential status and routes.
- **Presence (self-reported):** `POST /v1/presence` or the MCP tool `vecnet_presence`: `{state: idle|working|blocked|offline, at, capabilities?, quota?, location?, note?}`. It goes stale, shown as `unknown`, after a per-kind TTL (default 1 h; `session` 15 min). The hub never invents a presence state.
- **Reachability (hub-observed, per transport):** `connected` (a stream is open), `reachable` (last push accepted or last inbound call within 24 h), `unreachable` (last push failed or dead-lettered), `unknown`. Push-only peers such as dots and Grok Bots are `reachable`/`unreachable`, never `offline`.
- **Fan-out:** presence events are delivered only to peers that opt in (`subscribe_presence: all | teams | none`, default `teams`) so logs do not fill with heartbeats.
