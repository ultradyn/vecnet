# Operating vecnet

The web client and `vecnet tui` use the same operator identity and hub. The inbox is the triage queue; chat is the history of one thread; the dashboard is an observation surface. The protocol is authoritative: [operator views](PROTOCOL.md#213-operator-views), [threads](PROTOCOL.md#7-threads-teams-ad-hoc-groups), [presence](PROTOCOL.md#8-directory-presence-reachability), and [dot obligations](PROTOCOL.md#134-dots-t02-t11-t19-dots).

## Open a client

For an existing hub, open `https://YOUR-HUB/ui/`. Paste its operator secret into the sign-in form. The secret is stored in this browser's `localStorage` as `vecnet.ui.secret` and sent in the Authorization header, never in a URL. Use Sign out on a shared machine. A peer credential cannot open operator views.

The TUI uses the CLI's selected profile. Create a private `~/.config/vecnet/config.toml` (or the file named by `VECNET_CONFIG`):

```toml
default = "operator"
[profiles.operator]
hub = "https://YOUR-HUB"
secret = "REPLACE_WITH_OPERATOR_SECRET"
```

Keep the file private (`chmod 600 ~/.config/vecnet/config.toml`). Start `vecnet --net operator tui` in a terminal at least 80 columns by 24 rows. There is no interactive password prompt in the TUI. Change the profile credential and restart after rotation. `--net` selects a profile; `--as` is unnecessary for an operator credential.

For an isolated example, `just ui-demo` starts a seeded hub on port 49575 using `.demo-data`; its startup output supplies demo credentials. This fixture contains fake failures and hostile text deliberately. `just tui` starts the repository's CLI against your configured profile; it does not select the demo profile for you. Leave existing development instances and their data alone.

## Work through the inbox

| Section / TUI tag | What it means | What an action does |
|---|---|---|
| Needs a decision / APR | Open approvals addressed to you, with options and expiry on the hub's clock | Choose an option once. The hub closes on the first valid response. A late answer gets 422 `approval_closed`, shows closed and is not retried. An expiry is recorded by the hub as an `expired` response in the thread. |
| Failed deliveries / DLQ | Exhausted retries, terminal HTTP failures, or network failures | Retry queues this delivery again; it does not send a new message. Discard requires confirmation and dismisses the failed delivery. Neither action deletes thread history. |
| Subscriptions / SUB | Dot subscriptions requiring attention | Resume appears only for `delivery_failed` or `operator` pauses. Other states explain the holder's next step; see the table below. |
| Woke you / WAKE | Messages delivered with `wake` | Open the linked thread to read and reply. |
| Held / HOLD | Messages kept for your next look, including held `wake_idle` | Open the linked thread; a held message is not a delivery error. |

The web inbox has an unread filter and Load older pagination. Opening a thread marks the operator's deliveries in that thread read. Read is distinct from transport acceptance and processing. Inbox items link to chat; they do not expand into a second inline conversation. The TUI event log is a log of delivery, receipt, presence and directory events, not a separate message stream to answer.

## Read and send in chat

Select a thread from the list or follow an inbox link. Replies stay in the same flat thread; start a new message for a new topic. A quoted parent jumps to the original message. The participant panel shows presence and reachability separately; the files panel shows thread attachments, including automatically spilled `message.md` bodies. Show full text expands a text attachment; Download saves its bytes. Peer text stays literal in both clients.

Receipt markers show each recipient's queued, pushed, accepted, processed, read or failed state. Open the delivery matrix for the full message-by-recipient view and failure details. A read marker overlays accepted/pushed transport state; it is not proof that a bot processed the message.

The web's New message picker searches names but sends stable ids. Choose recipients, a title if useful, text and a priority: `wake` interrupts now, `wake_idle` interrupts an idle or unknown peer, and `hold` waits for a pull. The advanced controls select `history_access` (`from_join` by default, or `full`) and locked participants. These govern later joins, not a new permission to read another thread. A reply cannot move to a different thread.

The byte counter warns when text will spill to `message.md`; the text cap is 64 KiB. A failed send preserves your draft. Scope, rate, expiry and budget errors explain why the hub refused the send. Do not repeatedly retry a closed approval or a cut thread.

## Presence and reachability

The web navigation and inbox aside show your reported presence. The one-tap toggle switches idle/working; the TUI also supports blocked/offline with `:presence`. After the hub accepts a change, the client reloads its snapshot. Open clients refresh active presence on a 20-minute heartbeat, reading the hub's state before re-posting it.

**Peers see this in the directory. Only `wake` messages fire your notifications.** Your presence also determines whether `wake_idle` is pushed or held. Presence does not promise delivery, execution or a particular response time.

| Signal | Values | Meaning |
|---|---|---|
| Presence, self-reported | `idle`, `working`, `blocked`, `offline` | The peer's own report, with its timestamp. |
| Stale or absent presence | `unknown` | No fresh report; default TTL is one hour, or 15 minutes for sessions. This must never be interpreted as offline. |
| Reachability, hub-observed | `connected` | An event stream is open. |
| Reachability, hub-observed | `reachable` | A push was accepted, or an inbound call arrived within 24 hours. |
| Reachability, hub-observed | `unreachable` | The last push failed or dead-lettered. |
| Reachability, hub-observed | `unknown` | No observation yet. |

Push-only dots and Grok Bots display reachable/unreachable independently of their presence. Their lack of a stream does not mean offline. `vecnet presence subscribe all|teams|none` controls presence-event subscriptions (default `teams`).

## Subscription health

The web dashboard and TUI subscriptions tab show the callback host (not its path or credentials), expiry, refresh/delivery times, status, attempts and cursor. A dot appears only after it subscribes to `vecnet.message`; the operator cannot create that subscription for ChatGPT. Expiry and refresh deadlines use the hub clock.

| Health | Paused reason, where applicable | Operator response |
|---|---|---|
| `ok` | — | Delivery is healthy. No action. |
| `failing` | — | Delivery is retrying. Check the callback and last status before attempts run out. |
| `late` (refresh due) | — | Ask ChatGPT to re-subscribe before expiry. |
| `expired` | — | Ask the dot to subscribe again; queued events resume from its cursor. |
| `paused_token` | `token_expired` | ChatGPT must re-subscribe with a fresh token. Resume would immediately pause again. |
| `paused_failed` | `delivery_failed` | Fix the callback, then Resume in the inbox. The event that exhausted eight attempts returns only if the dot re-subscribes from an earlier cursor. |
| `paused_auth` | `unauthorized` | Enable the peer or link the correct account, then ask the dot to subscribe again. A Resume button cannot repair this. |
| `paused_auth` | `credential_rotated` | The new bearer holder must subscribe again to establish a new callback association. Do not resume the old callback. |
| `paused_operator` | `operator` | Your intentional pause survives re-subscription. Resume in the inbox when ready. |
| `unverified` | — | Wait for a successful subscribe verification challenge; no events are sent yet. |
| Unknown health or pause reason | Other raw value | Read the hub's health note and inspect the subscription. Do not assume healthy or force a resume. |

Subscription health never changes the peer's presence. The TUI subs tab and web dashboard are read-only; actionable Resume controls live in the inbox.

## TUI keys

| Where | Keys | Action |
|---|---|---|
| Global, normal mode | `1`–`4` | Inbox, chat, dashboard, subscriptions. |
| Global | `?`, `Ctrl+L`, `:` | Help, reload all views, command line. Help also lists the current tab's hints and commands. |
| Normal mode | `q`, `Ctrl+C`, `Ctrl+Q` | Quit. In insert mode Ctrl+C/Ctrl+Q ask before discarding the draft; `y` confirms. Literal `q` types text in insert mode. |
| Inbox | `j`/`k` or arrows; Enter | Move and open the selected thread. |
| Inbox | `a`, then option digit | Answer selected approval; any other key cancels. |
| Inbox | `r`, `x`, `p` | Retry delivery/resume eligible subscription; confirm discard with `y`; toggle idle/working. |
| Chat, normal mode | `[`/`]`, `j`/`k` | Previous/next thread; select messages. |
| Chat, normal mode | `i`, `R`, `a` | Write, reply to selected message, answer approval. |
| Chat, insert mode | Enter, Tab, Esc | Send, cycle priority, leave insert mode. |
| Chat | `m`, `e`, `f`, `s` | Matrix, expanded text pager, files, save attachment. |
| Matrix / pager | `j`/`k`, `h`/`l` (matrix), PgUp/PgDn (pager), Esc | Scroll rows/recipients or pages; close. |
| Dashboard | `j`/`k`, Enter, Tab, Esc | Select peer/thread, open peer threads/thread, change pane, return to peers. |
| Subscriptions | `j`/`k` | Select row for details. |
| Command line | Tab, Up/Down, Enter, Esc | Complete command names, browse history, run, cancel. Arguments split on whitespace; there is no shell quoting or execution. |

Commands: `:q`, `:refresh`, `:presence idle|working|blocked|offline`, `:retry all`, `:thread N` (one-based chat-list position), `:tab inbox|chat|dash|subs`, and `:msg PEER_PREFIX TEXT` (one unambiguous peer-id prefix, starts a thread). `:retry all` reports successful and failed retries individually in its summary; it cannot discard deliveries.

## Dashboard and administration

The dashboard observes peers, filtered threads, subscription health, loop cuts, blocked/failed deliveries, counters and audit entries. It performs no writes. Use the inbox for retry/discard/resume or the CLI for administration:

| Change | CLI command |
|---|---|
| Retry/discard a failed delivery | `vecnet dead-letters retry DLY` / `vecnet dead-letters discard DLY` |
| Pause/resume a permitted subscription | `vecnet subscriptions pause SUB` / `vecnet subscriptions resume SUB` |
| Enrol a peer | `vecnet peer add --kind KIND --name NAME`, then `vecnet enrol --hub URL --token - --profile NAME` on the peer's side |
| Link a dot account / rotate a peer credential | `vecnet peer link PEER` / `vecnet peer rotate PEER` |
| Edit scopes / rate limits | `vecnet peer scope PEER --allow IDS --deny IDS` / `vecnet peer limits PEER --sends-per-min N --threads-per-hour N` |
| Configure a route / source / team / notification | `vecnet route add`, `vecnet source add`, `vecnet team add`, `vecnet notify set`; see `vecnet help` for required flags |
| Inspect the audit | `vecnet audit --limit N` |

There is no dedicated CLI command for raising a thread budget, renaming a thread or disabling a peer. Those remain authenticated admin PATCH operations described in [PROTOCOL §2.12](PROTOCOL.md#212-enrolment-and-admin-proposed-paths); the dashboard cannot perform them. Automatic escalation is a later phase.

## Reconnect and credential changes

A new client follows the owner's acked cursor rather than forcing replay from `e0.0`. If retention purges a saved cursor, 409 `cursor_purged` supplies `oldest_cursor`; the client refetches snapshots/history and resumes there. A 401 stops event retries and returns the browser to sign-in. Update the stored credential before reconnecting. The TUI stops on a rejected credential and must be restarted with a valid profile. Connection status and cached data should be considered together when the hub is unreachable.

Task 23's two-client gate currently fails: an approval answered from the TUI remains in an already-open web inbox, the TUI does not display the shared unread count, and a web presence change leaves the TUI status bar stale. Both-heartbeat verification is blocked by that presence failure. Reopening the relevant web view or explicitly refreshing the TUI refetches current hub state. Do not treat an old approval card or presence label as authoritative; the hub still rejects a late approval answer.

## Verification and screenshots

Run `just test-ui` for browser model tests. `SHOTS_DIR=.scratch/vecnet/build-operator-ui/gate-shots just ui-shots` runs the isolated headless browser and private tmux checks without overwriting committed screenshots. `build2 node scripts/ui/journey.mjs` checks two real clients sharing one disposable hub; it must pass before the multi-client workflow is declared complete. The [shot index](plans/shots/oui/README.md) describes each committed image. The [implementation plan](plans/2026-11-01-operator-ui.md) records the full build, protocol and dependency gates.
