# Operator UI prototype (ticket 18): THROWAWAY

One self-contained HTML file with three radically different variants of the operator's web inbox, chat UI and read-only dashboard. Mock data lives in memory and is derived from `docs/protocol/examples/`. There is no build step and nothing persists, so a reload resets everything.

## Open it

Open `docs/prototypes/operator-ui/index.html` directly in a browser, or serve it:

```sh
python3 -m http.server 48213 --directory docs/prototypes/operator-ui
# then http://localhost:48213/?variant=a&tab=inbox
```

URL parameters are `variant=a|b|c`, `tab=inbox|chat|dash` and `thread=T1..T6`. The floating bottom bar, or the left and right arrow keys, cycle the variants. The `+15 min` button advances the mock hub clock so you can watch approvals expire.

## Variants

- **A, modest docs.** Cursor-like three columns: section nav and threads on the left, a calm reading column, context on the right.
- **B, packed tiles.** A bento grid of differently shaped tiles. Approvals are large tiles with countdown rings, and each peer gets its own tile.
- **C, terminal.** A tmux-style keyboard-first console with panes and a command line. Press `?` for keys. The commands are `j`/`k`, `a` then a digit, `r`, `x`, `[`/`]` and `:retry all`.

## Things you can do in every variant

- **Approvals.** Answer an approval. The hub closes it, posts the response into the thread and appends an audit line.
- **Dead letters.** Retry or discard a dead letter. The 410 stays dead after a retry, and the others recover.
- **Messages.** Send a message at `wake`, `wake_idle` or `hold`, then watch the receipts move from queued to processed. Text over 4096 bytes auto-spills into `message.md`.
- **Reply links.** Follow a reply link to jump to its parent message.

## Screenshots (1440x900, `shots/`)

| File | What to look at |
|---|---|
| `a-inbox.png` | Four calm sections: decisions with expiry, failed deliveries with retry and discard, then messages that woke you and messages that were held. |
| `a-chat.png` | A docs-style thread. Reply links quote their parent, the spilled message has a dashed `message.md` chip, and quiet per-recipient receipt dots sit under each message. |
| `a-dash.png` | Peers table with presence and reachability in separate columns, so a dot shows as reachable with unknown presence and never as offline. Threads, dead letters, loop cuts and the audit tail are further down. |
| `b-inbox.png` | Approval tiles with countdown rings, a one-tap presence toggle, notification webhook counts, red dead-letter tiles and a horizontal strip of held messages. |
| `b-chat.png` | The transcript tile beside a delivery matrix of messages by recipients coloured by receipt state, plus participants, files and budget tiles. |
| `b-dash.png` | One tile per peer coloured by kind, with a quota bar, capability tags and two status lights. Also a loop-cut chart, dead letters, threads and a two-column audit tail. |
| `c-inbox.png` | A typed list of approvals, dead letters, wake and hold items with a detail pane. The operator's raw event log shows receipts and presence as events, not messages. |
| `c-chat.png` | IRC-style log with priority glyphs (`!` wake, `~` wake_idle). Receipts are compact glyphs per recipient, spills are inline, and a thread list plus a who pane sit alongside. |
| `c-dash.png` | An htop-like peer table with quota bars, and panes for threads, dead letters with loop-cut counts, and the audit tail. |

## Mock-data note

Example 01 calls `peer_…8C` the codex session and `peer_…8B` the operator. Example 04 addresses its approval to `…8C`, so the examples disagree. The prototype treats `…8B` as the operator throughout. Example 04 should be fixed to address `…8B`.
