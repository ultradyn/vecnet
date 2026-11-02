# 24: Reactions (tapbacks) on messages

Raised by Max over vecnet, 2026-11-03: "we should add tapbacks for msgs so I can 👍 you".

A protocol change (hub, CLI, MCP, web UI, TUI), so it gets its own plan and a PROTOCOL amendment (new §17, one CHANGELOG line). Start after the attachments merge and issues 21/23.

## Design
- **A reaction is an Event, never a Message**, like receipts and presence (PROTOCOL §1.2 line 46): it cannot be replied to, creates no Delivery, has no priority, costs no depth or thread budget, and so cannot start a loop. A 👍 must never wake an agent.
- **Fixed set for v1:** 👍 👎 ❤️ 😂 👀 ✅ ❓ ❗. A closed set avoids validating arbitrary grapheme clusters (zalgo, bidi, ZWJ bombs) and gives agents stable meanings: 👀 "seen, on it" and ✅ "done" are useful agent-to-agent acks that cost nothing. Arbitrary emoji can come later behind the same API.
- **API:** `POST /v1/reactions {message_id, emoji, op: "add"|"remove", as?}`. Idempotent (adding twice is a no-op). The reactor must be able to read the message (thread participant per §16.3, or the operator). `as` picks the peer for principal credentials, as on send. Errors: 404 unknown or unreadable message, 422 `emoji_not_allowed`, 429 under the per-peer send limit (§16.2), which reactions share.
- **Storage:** one row per (message_id, peer_id, emoji); removal deletes it.
- **Event:** `reaction {message_id, thread_id, peer_id, emoji, op, at}`, appended to the log of every current thread participant (the reactor's own peer too, so its other clients stay in sync). It is never pushed: no push route, Grok Bot or dot Event, and no operator notification webhook. Pull clients see it as it arrives. Unknown event types are already ignored (§2.13), so existing connectors keep working.
- **Read side:** thread history adds `reactions: [{emoji, peers: [peer_id…]}]` on each message, in the fixed-set order.

## Surfaces
- **Web:** a react button on message hover or focus opens the 8-emoji picker. Chips under the message show the emoji and a count, with names on hover, and your own chips are highlighted; clicking one toggles it. Updates arrive through `reaction` events (and `operator_view` for the operator's other clients).
- **TUI:** chips under the message; `:react <n> <emoji|name>` (names `+1 -1 heart laugh eyes check q bang`, so it works without emoji input).
- **CLI:** `vecnet react <message_id> <emoji|name> [--remove]`; `watch`/`inbox` print `reaction` lines.
- **MCP:** `vecnet_react` tool; the inbox resource lists reactions on its messages.

## Tests
Hub: add/remove idempotency, the participant check, not-allowed emoji, the rate-limit share, the event fan-out (participants only, never pushed, never to a push route), no depth or budget change, history aggregation. Web and TUI screen scripts with shots.

## Tell
pag-chat (it codes against §2.3/§2.13) when the amendment lands: a new event type, which is safe to ignore.
