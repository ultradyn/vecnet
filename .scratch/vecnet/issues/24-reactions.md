# 24: Reactions (tapbacks) on messages

Status: resolved

Done: implemented across hub, CLI, MCP, web and TUI under the [reactions plan](../../../docs/plans/2026-11-03-reactions.md), with the shared-client journey and full verification gate.

Raised by Max over vecnet, 2026-11-03: "we should add tapbacks for msgs so I can 👍 you".

A protocol change (hub, CLI, MCP, web UI, TUI), so it gets its own plan and a PROTOCOL amendment (new §17, one CHANGELOG line). Start after the attachments merge and issues 21/23.

## Design
- **A reaction is always an Event** (history, chips, sync), and **it can also wake its target.** Max, 2026-11-03: "It should be able to wake an agent. because i sometimes use ✅ to say something is done". The two layers:
  - **Event layer (every reaction):** a `reaction` event, like receipts and presence (PROTOCOL §1.2 line 46). No Delivery and no budget cost.
  - **Wake layer (opt-in):** `priority` is optional on a reaction, defaulting to `hold`, which means the event only. With `wake` or `wake_idle`, an `add` also creates **one Delivery to the reacted message's sender** (not to other participants, never to the reactor). It carries a Message of the new kind `reaction` `{target: message_id, emoji}`, with `in_reply_to` set to the target. That Delivery follows §5.2 exactly, so push routes, Grok Bot and dots all wake the way a message would.
  - **Loop safety for the wake layer:** a reaction Message takes depth target + 1 and obeys `max_depth`, `expires_at` and `sender_chain` (§6). It is exempt from the thread budget. It cannot be replied to or reacted to (422 `not_replyable`). `remove` never wakes. Reactions share the per-peer send limit (§16.2). A connector's breaker (§6) treats a reaction like any message.
- **Fixed set for v1:** 👍 👎 ❤️ 😂 👀 ✅ ❓ ❗. A closed set avoids validating arbitrary grapheme clusters (zalgo, bidi, ZWJ bombs) and gives agents stable meanings: 👀 "seen, on it" and ✅ "done" are useful agent-to-agent acks that cost nothing. Arbitrary emoji can come later behind the same API.
- **API:** `POST /v1/reactions {message_id, emoji, op: "add"|"remove", as?}`. Idempotent (adding twice is a no-op). The reactor must be able to read the message (thread participant per §16.3, or the operator). `as` picks the peer for principal credentials, as on send. Errors: 404 unknown or unreadable message, 422 `emoji_not_allowed`, 429 under the per-peer send limit (§16.2), which reactions share.
- **Storage:** one row per (message_id, peer_id, emoji); removal deletes it.
- **Event:** `reaction {message_id, thread_id, peer_id, emoji, op, at}`, appended to the log of every current thread participant (the reactor's own peer too, so its other clients stay in sync). The event itself is never pushed; only the wake layer's Delivery is. Unknown event types are already ignored (§2.13), so existing connectors keep working.
- **API:** `POST /v1/reactions` also takes an optional `priority`.
- **Read side:** thread history adds `reactions: [{emoji, peers: [peer_id…]}]` on each message, in the fixed-set order.

## Surfaces
- **Web:** a react button on message hover or focus opens the 8-emoji picker. **Per-emoji default priority**, to avoid wasted wake-ups (Max: "idk if there's like a good way to handle it so it's not super wasteful"):
  - ✅ ❓ ❗ default to `wake_idle`. They ask for action, but interrupt only an idle agent; a working agent reads them on its next pass.
  - 👍 👎 ❤️ 😂 👀 default to `hold`. They are information only and never cost a turn.
  - A small wake / wake_idle / hold toggle in the picker overrides the default for one reaction.
  - CLI, MCP and the TUI take the same defaults, with `--wake`/`--hold` overrides.
  - **Coalescing:** while a reactor's reaction Delivery to an agent is still held (not yet accepted), a newer waking reaction from the same reactor in the same thread supersedes it rather than queuing a second wake. Chips under the message show the emoji and a count, with names on hover, and your own chips are highlighted; clicking one toggles it. Updates arrive through `reaction` events (and `operator_view` for the operator's other clients).
- **TUI:** chips under the message; `:react <n> <emoji|name>` (names `+1 -1 heart laugh eyes check q bang`, so it works without emoji input).
- **CLI:** `vecnet react <message_id> <emoji|name> [--remove]`; `watch`/`inbox` print `reaction` lines.
- **MCP:** `vecnet_react` tool; the inbox resource lists reactions on its messages.

## Tests
Hub:
- add/remove idempotency, the participant check, not-allowed emoji, the rate-limit share, history aggregation;
- event fan-out to participants only, and the event itself is never pushed;
- `hold` creates no Delivery;
- `wake`/`wake_idle` create exactly one Delivery to the target's sender, pushed per §5.2, with depth target + 1, refused past `max_depth` or expiry, no budget cost, and replies or reactions to it refused;
- a repeated add does not wake twice, and `remove` never wakes. Web and TUI screen scripts with shots.

## Tell
pag-chat (it codes against §2.3/§2.13) when the amendment lands: a new event type, which is safe to ignore.
