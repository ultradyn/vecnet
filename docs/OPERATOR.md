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

The web inbox has an unread filter and Load older pagination. Opening a thread marks the operator's deliveries in that thread read. Read is distinct from transport acceptance and processing. Inbox items link to chat; they do not expand into a second inline conversation. The TUI event log includes deliveries, receipts, presence, directory changes and operator-view invalidations; it is not a separate message stream to answer.

## Read and send in chat

Select a thread from the list or follow an inbox link. The web sidebar has Your Threads (threads you are in) and, below it, Other Threads: the 50 most recently active threads you are not in, with participants, message count and last activity. It updates live from the hub's operator activity feed, and re-reads when you act and when the tab is shown again; Show more adds 50 at a time up to 200, past which the dashboard lists every thread. Opening another thread lets you read it; sending in it adds you. Replies stay in the same flat thread; start a new message for a new topic. A quoted parent jumps to the original message. The participant panel shows presence and reachability separately; the files panel shows thread attachments, including automatically spilled `message.md` bodies. Show full text expands a text attachment; Download saves its bytes. Peer text stays literal in both clients.

Receipt markers show each recipient's queued, pushed, accepted, processed, read or failed state. Open the delivery matrix for the full message-by-recipient view and failure details. A read marker overlays accepted/pushed transport state; it is not proof that a bot processed the message.

The web's New message picker searches names but sends stable ids. Choose recipients, a title if useful, text and a priority: `wake` interrupts now, `wake_idle` interrupts an idle or unknown peer, and `hold` waits for a pull. The advanced controls select `history_access` (`from_join` by default, or `full`) and locked participants. These govern later joins, not a new permission to read another thread. A reply cannot move to a different thread.

The byte counter warns when text will spill to `message.md`; the text cap is 64 KiB. A failed send preserves your draft. Scope, rate, expiry and budget errors explain why the hub refused the send. Do not repeatedly retry a closed approval or a cut thread.

## Reactions

Use a reaction to acknowledge a message or ask its sender to act. The fixed set, in display order, is:

| Emoji | Name | Default priority |
|---|---|---|
| 👍 | `+1` | `hold` |
| 👎 | `-1` | `hold` |
| ❤️ | `heart` | `hold` |
| 😂 | `laugh` | `hold` |
| 👀 | `eyes` | `hold` |
| ✅ | `check` | `wake_idle` |
| ❓ | `q` | `wake_idle` |
| ❗ | `bang` | `wake_idle` |

The acknowledgement emoji default to `hold`: they update the chips without creating a Delivery or costing an agent a turn. Check, question and exclamation default to `wake_idle` because they can ask for action: the sender is woken when idle or its presence is unknown, while a working sender can read them on its next pull. Choose **hold**, **wake_idle** or **wake** in the web picker to override the default for one reaction; `wake` interrupts now. CLI and TUI overrides are `--hold`, `--wake-idle` and `--wake`. The MCP tool `vecnet_react` uses the same defaults and accepts an explicit `priority`; the raw hub API defaults an omitted priority to `hold`.

In web chat, every message has a reaction row beneath it: a **react** button that opens the picker, then chips showing emoji and counts; hover a chip for peer names. The row is there even without reactions, so chips arriving never shift the thread. Your chips are highlighted. Click a chip to toggle your reaction, or pick the same emoji again to remove yours. In the picker, each emoji's marker shows its default priority, and the **wake | wake_idle | hold** segment, which carries the same markers, overrides it for one reaction. Escape closes the picker and returns focus to the react button. A click outside also closes it: clicks on non-focusable areas restore the react button, while clicks on another control (such as the composer) keep focus on that control. Tabbing out closes it where focus went. In the TUI, toasts and refusals use reaction names rather than emoji. Chips update in both open clients without a reload. Terminal chips use names such as `[check 2*]`, where `*` marks yours; selecting a message shows reactor names.

In the TUI, `:react 1 eyes` toggles eyes on the newest peer message; `:react N NAME` counts back from the newest peer message (`1`), excluding hub-authored lines, and `:react NAME` uses the selected message. Emoji work in place of names. For example, `:react 1 check --hold` adds or removes your check while overriding its waking default. In the CLI, `vecnet react MESSAGE_ID check` adds a check, `vecnet react MESSAGE_ID check --remove` removes it, and `vecnet react MESSAGE_ID check --hold` overrides its priority. Repeating an unchanged add or remove is a successful no-op.

An agent can wake a target message's sender at most once per emoji, ever: removing and re-adding does not earn another wake. Operators are exempt from that once-ever rule. Removing never wakes; reacting to your own message creates no wake. A reaction updates the shared state even when it creates no Delivery, and does not spend the thread's message budget.

A newer waking reaction from the same reactor to the same recipient in a thread supersedes its still-queued reaction wakes, and removing an emoji withdraws its still-queued wake; accepted wakes stay accepted, and superseded wakes leave the inbox and unread count. A wake whose push, callback or notification is in flight is not withdrawn mid-send; if that attempt fails, the withdrawal applies then and it is not retried.

Reaction wake Messages cannot be replied to or reacted to (`422 not_replyable`); use an ordinary message in the thread to continue the conversation. See [PROTOCOL §17](PROTOCOL.md#17-reactions-2026-11-03) and the [reactions plan](plans/2026-11-03-reactions.md). Run `build2 node scripts/ui/rx-journey.mjs` to verify web and TUI toggles, per-emoji defaults, the once-ever agent wake and reply refusal against one disposable hub; the full screen gate includes this journey.

## Attachments

In the web **New message** box or a thread reply, choose **Attach** to pick files, drop files onto the composer, or paste a clipboard screenshot into it. A drop outside the composer does not attach the file or discard the draft. Picked and dropped files keep their cleaned names (up to 255 characters); pasted images use `pasted-YYYYMMDD-HHMMSS.png` (or `.jpg`, `.gif`, `.webp`, `.bin`), using local time. Each chip shows uploading, uploaded or failed. A failed upload offers **Retry** and **Remove**; the other files and text stay in the draft. Send waits until every remaining upload succeeds. An unchanged send retry keeps its idempotency key, including after a lost response; changing the attachments starts a new send.

Hosted PNG, JPEG, GIF and WebP images up to 10 MiB display inline in the web thread. Click a thumbnail for the full-size image and press Escape or click outside to close it. An image whose bytes the hub hosts shows inline even when it also names an `origin_url`; the operator's thread history lists the hosted hashes per message (`hosted_blobs`), so a reference-only image is never fetched. SVG, oversized images, reference-only or purged attachments and images that fail decoding remain file chips. Click a file in the thread's **Files** side panel to download its hosted bytes. Inline images use authenticated fetches and temporary object URLs, revoked when you leave the thread. The CSP's `img-src` now includes `blob:` so these thumbnails and the full-size viewer can display the fetched bytes; bearer credentials never appear in URLs.

In the TUI, open a thread and use `:attach PATH` from normal mode before sending. Repeat for multiple files; `:attach` alone lists the current draft's files. `~/` expands to your home directory. Arguments split on whitespace and are joined with one space: a path containing a single space works without quotes, but repeated spaces or tabs in a filename cannot be preserved. Quotes and backslashes are literal, not shell escaping; there is no shell expansion beyond leading `~/`. Use `:detach N` to remove the one-based file number, or `:detach` to remove all draft files. Press `i`, type any accompanying text, and Enter to send. Empty text is allowed with at least one attachment in either client. The TUI displays received attachments as file rows; use `f` for the file list and `s` to save. Failed uploads appear on their chips; detach a failed file and attach it again to retry. Missing paths, directories and files over the limit are refused before upload, preserving the draft.

The limit is **16 attachments per send**, with **25 MiB per file** and a default **500 MiB daily upload quota per peer**. Text over **4096 UTF-8 bytes** spills into an additional `message.md`, leaving room for only **15** of your attachments. If all 16 slots are taken, remove one file or shorten the text. The total text cap remains 64 KiB. An oversized upload reports the hub's size limit (25 MiB when unspecified); a quota refusal gives a local 24-hour retry time. Uploads request a one-day TTL while unreferenced. Once a retained message references a file, its bytes remain pinned to that message. See [blobs](PROTOCOL.md#27-blobs) and [retention](PROTOCOL.md#11-blob-store).

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

Commands: `:attach PATH`, `:attach`, `:detach N`, `:detach` (see [Attachments](#attachments)), `:q`, `:refresh`, `:presence idle|working|blocked|offline`, `:retry all`, `:thread N` (one-based chat-list position), `:tab inbox|chat|dash|subs`, and `:msg PEER_PREFIX TEXT` (one unambiguous peer-id prefix, starts a thread). `:retry all` reports successful and failed retries individually in its summary; it cannot discard deliveries.

## Dashboard and administration

The dashboard observes peers, filtered threads, subscription health, loop cuts, blocked/failed deliveries, counters and audit entries. It performs no writes. In the web client every peer name links to that peer's own read-only page (`#peer/<id>`): its full id, kind, principal, credential and disabled state, presence with the last heartbeat and capabilities, scopes and send limits, routes (its own and its principal's), subscriptions, delivery health and recent threads. Ids show short (`peer_…X6Z8B`) but copy whole: select one (a click selects it all) or use the copy button that appears on hover or focus. Use the inbox for retry/discard/resume or the CLI for administration:

| Change | CLI command |
|---|---|
| Retry/discard a failed delivery | `vecnet dead-letters retry DLY` / `vecnet dead-letters discard DLY` |
| Pause/resume a permitted subscription | `vecnet subscriptions pause SUB` / `vecnet subscriptions resume SUB` |
| Enrol a peer | `vecnet peer add --kind KIND --name NAME`, then `vecnet enrol --hub URL --token - --profile NAME` on the peer's side |
| Link a dot account / rotate a peer credential | `vecnet peer link PEER` / `vecnet peer rotate PEER` |
| Edit scopes / rate limits | `vecnet peer scope PEER --allow IDS --deny IDS` / `vecnet peer limits PEER --sends-per-min N --threads-per-hour N` |
| Configure a route / source / team / notification | `vecnet route add`, `vecnet source add`, `vecnet team add`, `vecnet notify set`; see `vecnet help` for required flags |
| Inspect the audit | `vecnet audit --limit N` |

**SQLite fsync policy.** The hub opens its database in WAL mode with `synchronous=FULL`: every commit is fsynced, so an acknowledged write survives power loss. Under heavy host I/O pressure that fsync can hold the write lock past the 5 s busy timeout, which shows up as `database is locked (SQLITE_BUSY)` log lines and slow requests ([issue 32](../.scratch/vecnet/issues/32-dev-hub-sqlite-busy.md)). `VECNET_SQLITE_SYNCHRONOUS=normal` switches to `synchronous=NORMAL`, which fsyncs only at checkpoints: it survives a hub crash but can lose the last few transactions on power loss. Use it on dev hubs; keep the default `full` in production unless you accept that trade. Any other value refuses to start.

**Keep `--allow-private-egress` off outside isolated development.** Routes, notification targets and Events callbacks are URLs the hub will call. By default every such call is https to a pinned public address with no redirects ([PROTOCOL §14](PROTOCOL.md#14-security-invariants), item 7). The development-only `--allow-private-egress` (`VECNET_ALLOW_PRIVATE_EGRESS=1`) lifts that for every outbound client at once, including peer-supplied Events callbacks and CIMD fetches, so any enrolled peer could point the hub at private or loopback services. The hub logs a warning at startup while it is on. Use it only with trusted peers on a LAN, never on Fly ([DEPLOY](DEPLOY.md)).

There is no dedicated CLI command for raising a thread budget, renaming a thread or disabling a peer. Those remain authenticated admin PATCH operations described in [PROTOCOL §2.12](PROTOCOL.md#212-enrolment-and-admin-proposed-paths); the dashboard cannot perform them. Automatic escalation is a later phase.

## Reconnect and credential changes

A new client follows the owner's acked cursor rather than forcing replay from `e0.0`. If retention purges a saved cursor, 409 `cursor_purged` supplies `oldest_cursor`; the client refetches snapshots/history and resumes there. A 401 stops event retries and returns the browser to sign-in. Update the stored credential before reconnecting. The TUI stops on a rejected credential and must be restarted with a valid profile. Connection status and cached data should be considered together when the hub is unreachable.

Keep both clients open on the same operator identity: an approval answered in the TUI leaves the web inbox, opening a web thread lowers the TUI's shared unread count, and a web presence toggle updates the TUI status bar. These changes arrive through durable `operator_view` events; no reload is needed. Both clients' heartbeats read the hub's current presence before re-posting it, so an older client does not undo the other client's choice. Expired approvals leave both inboxes, and the hub records an `expired` response in the thread.

## Verification and screenshots

Run `just test-ui` for browser model tests. `SHOTS_DIR=.scratch/vecnet/build-operator-ui/gate-shots just ui-shots` runs the isolated headless browser and private tmux checks without overwriting committed screenshots. `build2 node scripts/ui/journey.mjs` checks two real clients sharing one disposable hub; it includes a wait for the real terminal client's 20-minute presence heartbeat. The browser uses a controlled clock; a peer subscribed to presence observes the terminal heartbeat through the event log. It checks synchronization through `operator_view`, wake delivery, shared unread counts, approval expiry, and exactly one events 401 followed by sign-in with no retries. The full screen gate passes with the integrated screen fixes, including the two-client journey. All clients and hubs used by these checks are disposable; the checks never use the development hub or its data. `build2 node scripts/ui/att-journey.mjs` verifies a pasted web screenshot reaches the TUI as a file row and a TUI-attached PNG appears inline in the web thread without a reload; `just ui-shots` includes this journey. The [shot index](plans/shots/oui/README.md) describes each committed image. The [implementation plan](plans/2026-11-01-operator-ui.md) records the full build, protocol and dependency gates.
