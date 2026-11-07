# Operator-flow audit

_Researched 2026-11-06. Docs only. No hub was contacted, no token was minted, and no secret was read._

**Question.** For each object an operator manages — peer, invite, credential, subscription, push route, group or team, blob, thread — which states does this checkout store, what do web, CLI, TUI, and MCP show, which actions exist, and which alarms have no way to clear them?

**Verdict.** The surfaces an operator actually sits in (web and TUI) can see most alarms and can clear only a few: resume a `delivery_failed` or operator pause, retry or discard a dead letter, answer an approval, send a message. Enrolment, disable, route verify, group unarchive, blob purge, and thread-budget raise exist on HTTP, and some of those exist on the CLI, and almost none exist on the web, the TUI, or MCP. The dead end Max hit is real: a paused or expired Events subscription stays in the inbox and on the subscriptions badge even when another row for the same event and arguments is healthy, and there is no end or delete. CoS has approved a fix (`msg_01M6R14ES8A9HS2PZW7TZRPAGW`); it is not in this tree. Admin parity ([ADM], GitHub issue #48) already inventories the missing client commands. This note checks that inventory against the code and ranks the gaps an operator hits.

Labels: **SOURCE** = read in this checkout at `cd345021a0c53702b3e5ed4f84a790c5786a92bc`. The subscription fix in section 4 is the CoS ruling recorded on this assignment, not a behaviour of this HEAD.

---

## Source key

| Tag | Source | Trust |
|---|---|---|
| [PROTO] | `docs/PROTOCOL.md` | Normative behaviour |
| [ADM] | `docs/plans/2026-11-05-admin-parity.md` | Plan only; the grant table is not in Go |
| [SQL] | `internal/store/migrations/` | Stored columns and checks |
| [HUB] | `internal/hub/` | Enrolment, routes, groups, threads, purge |
| [EV] | `internal/events/` | Subscription rows, pause, resume, delete-by-key |
| [OP] | `internal/opview/subs.go`, `internal/opview/dash.go`, `internal/opapi/types.go` | Derived health and admin directory |
| [HTTP] | `internal/httpapi/admin.go`, `admin_subscriptions.go`, `server.go`, `enrol.go` | Routes an operator can call |
| [CLI] | `internal/cli/commands.go`, `connectors.go`, `invite.go` | Commands that exist |
| [UI] | `internal/ui/static/` | Web screens and the one subscription write |
| [TUI] | `internal/tui/` | Terminal screens and the API they may call |
| [MCP] | `internal/mcpserver/tools.go`, `subscriptions.go` | Tools a model can call |
| [AUTH] | `internal/auth/auth.go` | Credentials and enrol tokens |
| [BLOB] | `internal/blob/store.go`, `usage.go`, `transfers.go`, `internal/blobwire/types.go` | Disk purge and R2 projection |

---

## Verified facts

| Fact | Status | Source |
|---|---|---|
| Admin HTTP is operator-only. The handlers do not check the caller again | SOURCE | [HTTP] `admin.go` lines 17–19; routes lines 21–66 |
| There is no `DELETE /v1/admin/subscriptions`, no `subscription_end` audit action, and no `vecnet admin subs end` | SOURCE | [HTTP] `admin.go` lines 63–65 register list, pause, and resume only; repo search at this HEAD |
| `event_subscriptions` is unique on `(principal_kind, principal_id, url, name, arguments)`. A second callback URL is a second row. A refresh of the same key clears `paused_reason` except `operator` | SOURCE | [SQL] `0005_event_subscriptions.sql` line 34; [EV] `store.go` lines 160–163, 227–230, 272–280 |
| Inbox attention lists every subscription whose health is paused, expired, late, or failing. It does not look for a healthy sibling. `unverified` is omitted. Resume is attached only for `paused_failed` and `paused_operator` | SOURCE | [OP] `subs.go` lines 96–123 |
| `events_paused` only increases. Operator pauses and `credential_rotated` are not counted. Nothing subtracts | SOURCE | [PROTO] §13.4 line 895; [EV] `store.go` lines 710, 734–736; [HUB] `deliveries.go` lines 499–507 |
| Web subscriptions are read-only on the dashboard. Resume exists only on an inbox attention row whose `action` is `resume`. The web client has no pause and no delete | SOURCE | [UI] `subs.js` lines 74–75, 124–130, 149–156; `api.js` line 154 |
| The TUI subscriptions tab is read-only. Inbox `r` resumes only when `action` is `resume`. The TUI API has resume, dead-letter retry and discard, send, and presence. It has no pause, disable, purge, unarchive, or thread patch | SOURCE | [TUI] `screen_subs.go` lines 61–62, 82–84; `screen_inbox.go` lines 299–335; `tui.go` lines 25–48 |
| `vecnet_subscriptions` reads the caller's own peer, drops rows whose `expires_at` has passed, and cannot subscribe, pause, resume, or delete | SOURCE | [MCP] `subscriptions.go` lines 38–64; `tools.go` lines 852–853; [PROTO] §4.4 lines 576, 584 |
| CLI `subscriptions resume` posts resume for any id. Hub `Resume` clears `paused_reason` with no reason check. The worker then re-pauses a still-expired token or an unauthorised peer | SOURCE | [CLI] `connectors.go` lines 207–210; [EV] `store.go` lines 564–585; `worker.go` lines 449–469 |
| Peer disable and enable are `POST /v1/admin/peers/{id}/disable` and `/enable`. The CLI `peer` usage has no disable. The web peer page shows the disabled note and has no enable button. The TUI paints `disabled` or `revoked` and has no key for it | SOURCE | [HTTP] `admin.go` lines 41–42; [HUB] `admin.go` lines 528–531; [CLI] `commands.go` lines 100–106, 1548–1549; [UI] `peer.js` lines 183, 211; `peer-model.js` lines 137–146; [TUI] `screen_dash.go` lines 411–427 |
| An archived group refuses sends with 410 `group_archived`. Unarchive is `POST /v1/admin/groups/{id}/unarchive`. CLI `groups` is create, add, and list. MCP has create, add, and list. Web and TUI have no group admin | SOURCE | [HUB] `groups.go` lines 61–63, 316–319; [HUB] `admin.go` lines 582–599; [HTTP] `admin.go` line 54; [CLI] `commands.go` line 1281; [MCP] `tools.go` lines 930–944 |
| Thread budget, title, history access, and participant lock change only through `PATCH /v1/admin/threads/{id}`. CLI `thread` and `threads` are the ordinary read. Web and TUI display budget and lock and do not patch them | SOURCE | [HTTP] `admin.go` lines 58, 581–584; [CLI] `commands.go` lines 476–479, 1096–1127; [UI] `thread-aside.js` lines 128–134; [TUI] `screen_chat.go` lines 695–700, 1523–1533 |
| Blob purge is `POST /v1/admin/blobs/{sha}/purge`. It tombstones the hash, marks disk rows, and queues R2 deletes for that hash. No CLI, web, TUI, or MCP command calls it | SOURCE | [HTTP] `admin.go` lines 59, 642–651; [HUB] `admin.go` lines 803–816; [BLOB] `store.go` lines 473–488; `usage.go` lines 54–85 |
| Admin-parity's capability names (`admin:peers`, `admin:subscriptions`, and the rest) are plan text. No Go file in this checkout grants them | SOURCE | [ADM] security contract 2; this checkout (same finding as `docs/research/2026-11-06-accounts-scope.md`) |

---

## 1. Peer

**Stored and derived states.** `peers.disabled` is 0 or 1 ([SQL] `0001_init.sql` line 31). Presence stored is `idle`, `working`, `blocked`, or `offline`, or null (line 33). The directory shows that value, or `unknown` once the kind TTL has passed ([PROTO] §8 lines 741–741; [HUB] `presence.go` lines 50–52). Reachability stored is observed by the hub: `connected`, `reachable`, `unreachable`, or `unknown` ([PROTO] §8 lines 743–751). A call-based `reachable` older than 24 h is shown as `unknown` ([HUB] `directory.go` lines 46–61). `reach_via` is `call` or `push` ([SQL] `0002_reach_via.sql` line 5). Kind is one of the directory kinds in [PROTO] §8 line 739. The operator peer cannot be disabled ([HUB] `admin.go` lines 556–557).

The admin directory adds three credential words and the disabled flag ([PROTO] §2.13 lines 357–358; [OP] `dash.go` lines 103–107):

| `credential` | When |
|---|---|
| `active` | Some credential row for the peer or its principal has `revoked_at` null |
| `revoked` | Credential rows exist and every one of them is revoked |
| `none` | No credential row. A dot is in this state on purpose: it has no bearer ([PROTO] §2.12 line 261) |

**What each surface shows.**

| Surface | What a state looks like | Actions |
|---|---|---|
| Web | Peer page: kind, credential word, enabled or disabled, presence, reachability, teams, scopes, limits, routes, this peer's subscriptions ([UI] `peer.js` lines 201–224, 269–296). A disabled peer gets a red chip and the sentence "Disabled: this peer cannot use the hub until an operator enables it." (line 183). Header actions are Open thread and New message, and those are hidden for a source, the operator, and the operator's own id ([UI] `peer-model.js` lines 137–146) | Add agent (creates a peer; section 2). No disable, enable, move, rotate, scope edit, or limit edit |
| CLI | `vecnet peers` is the public directory: id, kind, name, presence, reachability. It omits disabled peers and credential status ([CLI] `commands.go` lines 1194–1221; [HUB] `directory.go` lines 68–72) | `peer add`, `peer invite`, `peer move`, `peer link`, `peer avatar`, `peer rotate`, `peer scope`, `peer limits` ([CLI] `commands.go` lines 100–106, 1477–1549). No disable or enable |
| TUI | Dashboard peer column appends `disabled` or `revoked` ([TUI] `screen_dash.go` lines 411–427) | No peer-admin keys. The TUI API cannot disable or rotate ([TUI] `tui.go` lines 25–48) |
| MCP | `vecnet_peers` is `GET /v1/peers`, the public directory ([MCP] `tools.go` lines 902–907; [PROTO] §4.3 line 544) | `vecnet_peer_create` and `vecnet_peer_set_disabled` are the principal's own peers, not the operator directory ([MCP] `tools.go` lines 907–920; [PROTO] §18.4). No operator disable, move, scope, or limits tool |

**Dead end.** A disabled peer, or a subscription paused because that peer was disabled, tells the operator to enable it ([OP] `subs.go` lines 70–71). Enable exists only as the admin POST. Web, CLI, and TUI cannot do it.

**Admin parity.** [ADM] inventory rows "Operator peer disable/enable", "Peer scope replacement", "Peer limit replacement", "Peer ownership move", and "Peer credential rotate" match this tree: HTTP (and, except disable, CLI) exist; web and TUI are read-only; MCP operator tools are absent. Task 5 is the planned CLI and MCP slice. Task 8 is the web forms. The TUI forms are the follow-up after that plan merges.

---

## 2. Invite

Two one-time secrets, stored only as hashes.

**Enrol token** (`enrol_tokens`: `token_hash`, `peer_id`, `expires_at`, `used_at`; [SQL] `0001_init.sql` lines 57–62). There is no status column. The states an operator can infer:

| State | Columns | What the hub does |
|---|---|---|
| Pending | `used_at` null and `expires_at` still ahead | `POST /v1/enrol` mints `vn_peer_…` once ([AUTH] `auth.go` lines 172–186, 199–204) |
| Expired | `used_at` null and `expires_at` passed | 410 `enrol_token_expired` ([HTTP] `enrol.go` lines 47–48; [PROTO] §2.12 line 258) |
| Spent | `used_at` set | 401 `unauthenticated`, the same code as an unknown token ([AUTH] `auth.go` lines 220–228; [PROTO] §2.12 line 258) |

Reissue spends every unused token for that peer and inserts a new one, audited `enrol_reissue`. A peer that already holds a live peer credential is 409 `already_enrolled`. A dot is 422 ([HUB] `invite.go` lines 29–61; [PROTO] §2.12 line 271). Lifetime is 60 s through 7 d, default 24 h ([HUB] `invite.go` lines 15–26; [PROTO] §2.12 line 270). Spent and "operator revoked" are the same column: both set `used_at`. A later reader cannot tell them apart. There is no invite id distinct from the hash.

**Link code** (`dot_links`: `code_hash`, `peer_id`, `expires_at`, `used_at`; [SQL] `0006_oauth.sql` lines 58–64). Twelve characters, 10 minutes, single use, only for an enabled `dot` or `grokbot` ([HUB] `admin.go` lines 137–138, 149–151, 180–209; [PROTO] §20.1, §4.5 lines 599–599). Unknown, used, expired, and ineligible all return one error ([HUB] `admin.go` lines 157–159).

**What each surface shows.**

| Surface | Enrol token | Link code | Actions |
|---|---|---|---|
| Web | Add agent shows the paste block once, with a TTL of 1 h, 24 h, or 7 d ([UI] `invite-model.js` lines 8–12, 27–40). The page does not list pending, expired, or spent invites | Dot panel shows the MCP URL and the steps, and mints a code through `POST /v1/admin/dot-links` ([UI] `invite-model.js` lines 59–74; `api.js` line 133) | Create. No status, no revoke, no reissue button |
| CLI | `peer add` prints `peer_id` and `enrol_token` once. `peer invite` prints the paste block. `peer invite <peer>` reissues ([CLI] `commands.go` lines 1492–1513; `invite.go` line 192; [PROTO] §12 line 831) | `peer link <peer>` prints the grouped code ([CLI] `commands.go` line 101) | Reissue while the peer has no live credential. No list, no status, no revoke |
| TUI | N/A | N/A | The TUI API has no invite call ([TUI] `tui.go` lines 25–48) |
| MCP | N/A. No tool returns an enrol token | N/A. No tool returns a link code | [PROTO] §4.3 has no invite tool. [ADM] contract 8 keeps it that way: a model requests, a human receives the paste block |

**Dead end.** An invite that was claimed by the wrong party has no status row and no one-step recovery. The operator can rotate the peer credential from the CLI (`peer rotate`), which also spends unused enrol tokens ([PROTO] §2.12 line 273). Disabling that peer, which is the other half of "stop the unexpected enrolment", is the HTTP-only action in section 1. An unused token that should be withdrawn is cleared only by reissue or rotate, and reissue is refused once a live credential exists.

**Admin parity.** [ADM] decision 4 and contract 11 specify `pending|claimed|expired|revoked`, a non-secret invite id, token-free reads, `peer invite revoke`, and one-step recovery of an unexpected enrolment. None of that is stored or shown at this HEAD. Task 4 is the planned HTTP and metadata. Task 5 is the CLI and MCP status tools, which return no token.

---

## 3. Credential

**States.** `credentials.kind` is `peer`, `principal`, or `operator`. `revoked_at` null means the secret still resolves; a timestamp means it does not ([SQL] `0001_init.sql` lines 47–54; [AUTH] `auth.go` line 119). Rotation revokes the live row for that kind and subject and inserts a new secret, returned once ([AUTH] `auth.go` lines 337–359). The operator directory collapses every row for the peer and its principal into one word: `active`, `revoked`, or `none` (section 1). A live principal credential keeps the word `active` after the peer credential is revoked ([OP] `dash.go` lines 103–106).

Rotating a peer or principal bearer pauses that subject's `principal_kind = 'vecnet'` subscriptions with `paused_reason = credential_rotated`, unless the row is already `operator` or already `credential_rotated` ([HUB] `admin.go` lines 339–359, 389–407). That pause is not counted in `events_paused` ([PROTO] §13.4 line 895).

**What each surface shows.**

| Surface | Display | Actions |
|---|---|---|
| Web | The credential word on the peer page ([UI] `peer.js` line 210). No secret, no credential id, no `revoked_at` | None. Sign-in stores the operator secret in `localStorage` under `vecnet.ui.secret` ([UI] `app.js` lines 6–7, 29, 266–273). No rotate button |
| CLI | `principal list` does not print credential ids. `peer add` and `principal add` print a secret once. `peer rotate` prints the new peer secret ([CLI] `commands.go` lines 1403–1406, 1524–1529) | `peer rotate`. `principal add`. No `principal rotate` command, though `POST /v1/admin/principals/{id}/rotate` exists ([HTTP] `admin.go` line 34; [ADM] inventory "Principal credential rotate" marks CLI as missing) |
| TUI | The word `revoked` on the dashboard when the admin row says so ([TUI] `screen_dash.go` lines 418–421) | None |
| MCP | `vecnet_whoami` returns credential kind `peer`, `principal`, or `operator` and no secret ([MCP] `tools.go` lines 953–963) | No rotate tool. [ADM] contract 8: MCP must not receive the new secret |

**Dead end.** After a rotate, the old Events subscription sits at `paused_auth` until something ends it (section 4). The web and TUI show the peer as `active` once the new secret exists, so the credential looks fine while the subscription alarm remains. There is no credential list that shows the revoked row beside the live one.

**Admin parity.** [ADM] puts rotate behind a human channel for MCP (`vecnet_peer_rotate_request`, `vecnet_principal_rotate_request`, Task 5 and Task 6). CLI `peer rotate` already returns the secret to the human. `principal rotate` is still HTTP-only. `admin:credentials` is not implemented.

---

## 4. Subscription

**Stored fields.** `paused_reason` is null or one of `token_expired`, `delivery_failed`, `unauthorized`, `credential_rotated`, `operator` ([EV] `store.go` lines 60–68; [HUB] `admin.go` lines 339–347). `verified_at` null means the callback challenge has not passed. `expires_at` is the subscription lease. `token_expires_at` is the access token's expiry. `attempts` and `last_status` describe the oldest unsettled push ([SQL] `0005_event_subscriptions.sql` lines 9–35).

**Derived health**, first match wins ([OP] `subs.go` lines 61–93; the same strings are [PROTO] §2.13 lines 375–382 and [OP] `internal/opapi/types.go` lines 537–547):

| Health | Condition | Inbox action |
|---|---|---|
| `paused_token` | `paused_reason = token_expired` | none. Note: ChatGPT must re-subscribe |
| `paused_failed` | `delivery_failed` | `resume` |
| `paused_operator` | `operator` | `resume`. A re-subscribe does not clear this reason ([EV] `store.go` lines 272–275) |
| `paused_auth` | `unauthorized`, `credential_rotated`, or any other reason string | none. Notes differ: enable or relink; or the new bearer must subscribe again |
| `unverified` | not paused, `verified_at` null | not an inbox item |
| `expired` | lease time has passed | none. Note: ask the dot to subscribe again |
| `late` | inside the lease, refresh time has passed | none |
| `failing` | attempts in flight, or last HTTP status outside 2xx | none |
| `ok` | none of the above | none |

`ListHealth` returns every row, including expired ones ([EV] `store.go` lines 620–625). The worker re-pauses an unpaused row whose token has expired, or whose peer is disabled, whose OAuth token is revoked or missing, or whose OAuth principal is no longer bound to that peer ([EV] `worker.go` lines 449–490; `store.go` lines 911–944). `events/unsubscribe` deletes by the full key, not by id, and only for the connected account ([EV] `store.go` lines 304–333; [PROTO] §4.4 line 572).

**The sibling dead end.** The unique key includes the callback URL and the principal. ChatGPT subscribing again with a new URL, or a link that moves the peer onto a new `dot:` principal ([PROTO] §4.5 line 599), inserts a new row and leaves the old one. Refresh of the *same* URL and arguments would have cleared `token_expired`, `delivery_failed`, `unauthorized`, and `credential_rotated`. A different URL does not. Attention ranks each non-ok row on its own and never asks whether another row with the same `name` and `arguments` is `ok` ([OP] `subs.go` lines 107–123). The TUI subs badge counts every non-`ok` row, including `unverified` ([TUI] `screen_subs.go` lines 90–94). `events_paused` keeps the historical total after the row is resumed or superseded ([HUB] `deliveries.go` lines 499–507).

What an operator can do today:

| Health | Web | CLI | TUI | MCP |
|---|---|---|---|---|
| `paused_failed`, `paused_operator` | Inbox Resume ([UI] `subs.js` lines 124–130) | `subscriptions resume <id>` | Inbox `r` ([TUI] `screen_inbox.go` lines 311–317) | Read-only on the caller's own unexpired rows. An operator credential sees the operator peer, not the dot ([MCP] `subscriptions.go` lines 38–64) |
| `paused_token`, `paused_auth`, `expired` | Chip and note. No button | `subscriptions list` prints `paused=<reason>` and not the health word ([CLI] `connectors.go` lines 172–199). `resume` is accepted by the hub and then undone by the worker if the token is still expired or the peer is still unauthorised | Toast: no resume ([TUI] `screen_inbox.go` lines 326–335). Subs tab is read-only | Own unexpired paused rows stay visible with `paused: true`. Expired rows disappear from this tool and remain on the admin list |
| `late`, `failing` | Chip and note | `paused=-` plus last status | Badge counts them. No key | Visible while unexpired |
| `unverified` | Chip on the table | `paused=-` if the reason is null | Badge counts it. Not in the inbox | `verified: false` while unexpired |
| any | No pause button | `subscriptions pause <id>` sets `operator` ([CLI] `connectors.go` lines 202–205; [EV] `store.go` lines 517–533) | No pause | No pause |

A healthy sibling does not clear any of those rows. That is the dead end: the inbox keeps an alarm whose prescribed action ("re-subscribe", "enable the peer") has already happened on another row, or cannot be performed from the screen that shows the alarm.

**Planned fix, not implemented.** CoS approved this on `msg_01M6R14ES8A9HS2PZW7TZRPAGW`. Recorded here so the gap has an owner. This note does not add it.

- Auto-supersede a paused row when a healthy sibling covers the same event and arguments.
- Auto-end a `credential_rotated` pause whose token expired more than 7 days ago.
- Manual `DELETE /v1/admin/subscriptions/{id}` soft-ends the row, audits `subscription_end`, and is reached by CLI `vecnet admin subs end`, a web Remove control, and MCP admin parity.
- Alarm counts include only paused or failed rows that have no healthy sibling.
- The hub change waits for DB compaction Tasks 5–9 (`docs/plans/2026-11-05-db-compaction.md`). UI follows the hub.

[ADM] Task 6 stops at list, get, pause, and resume (`vecnet_admin_subscription_pause` / `_resume`). It does not name an end. The CoS ruling is the additional recovery for this dead end. Pause and resume parity with MCP and a targeted GET are still the admin-parity gap for the rows that *should* be resumed.

---

## 5. Push route

**States.** `routes.kind` is `http` or `grokbot` ([SQL] `0007_grokbot_routes.sql` lines 7–16). `verified_at` null means an `http` route has not passed its challenge; `ClaimDue` skips it ([HUB] `routes.go` lines 32–40; `deliveries.go` line 102). A grokbot route is inserted with `verified` true ([HUB] `grokbot.go` lines 52–78). `route_keys.retired_at` marks a rotated-out `whsec_` during the 24 h overlap ([SQL] `0001_init.sql` lines 207–212; [HUB] `routes.go` lines 24–26; [PROTO] §3.5). List responses show host only, never the path, query, or key ([PROTO] §2.12 line 277).

| Surface | Display | Actions |
|---|---|---|
| Web | Peer page table: id, owner (this peer or its principal), kind, host, verified time or the word `unverified` in a bad tone, created time ([UI] `peer.js` lines 269–280) | None. No verify, rotate, or remove |
| CLI | `route list` prints id, owner, kind, host, `verified_at` or `-`, `runs_per_hour` ([CLI] `commands.go` lines 1568–1594) | `route add` (http, returns `whsec_` once; grokbot key from a 0600 file), `route verify`, `route rotate`, `route remove` (lines 1558–1618) |
| TUI | N/A. Peers are loaded; routes are not on the TUI API | None |
| MCP | N/A | None |

**Dead end.** An unverified `http` route is a red "unverified" on the peer page, and deliveries do not push, and the page has no Verify. The CLI can clear it. A web-only operator cannot. Grokbot routes do not sit in that state. A removed route leaves pending deliveries to pull ([PROTO] §2.12 line 277); the web cannot remove one either.

**Admin parity.** [ADM] inventory matches: create, list, verify, rotate, and remove are HTTP and CLI; web is metadata read; TUI and MCP are absent. Task 6 adds redacted get, verify, remove, and human-channel create and rotate. Task 8 adds the web forms.

---

## 6. Group or team

**Team.** `teams` plus `team_members`. No archive column ([SQL] `0001_init.sql` lines 72–82). Teams do not auto-archive ([HUB] `groups.go` lines 316–319). Create is operator-only ([HUB] `groups.go` lines 346–360). Member replace is `POST /v1/admin/teams/{id}/members` ([HTTP] `admin.go` line 52).

**Group.** `groups.archived_at` null means live; a timestamp means archived ([SQL] `0001_init.sql` lines 85–91). The hub archives a group whose `last_active_at` is more than 30 days ago, actor `hub`, action `group_archive` ([HUB] `groups.go` lines 17–19, 316–338). Send and member-add then return 410 `group_archived`, and the error says the operator can unarchive it (lines 61–63, 220). Unarchive clears `archived_at`, stamps `last_active_at` to now, and is a no-op if the group is already live ([HUB] `admin.go` lines 582–599).

| Surface | Team | Group | Actions |
|---|---|---|---|
| Web | Peer identity lists team ids as copyable ids ([UI] `peer.js` line 212). No team page | Thread aside shows `thread.group` or an em dash ([UI] `thread-aside.js` line 130). No archived flag, no unarchive | None |
| CLI | `team add --name N [--members IDS]` only ([CLI] `commands.go` lines 1621–1634). No list, no member replace | `groups create`, `groups add`, `groups list`. List prints ` archived` when the flag is set (lines 1225–1281) | No `groups unarchive`. No `team list` |
| TUI | N/A | N/A | None |
| MCP | N/A | `vecnet_groups`, `vecnet_group_create`, `vecnet_group_add` ([MCP] `tools.go` lines 930–944; [PROTO] §4.3 lines 549–551) | No unarchive tool |

**Dead end.** Thirty days after the last group activity, a send fails with 410 and the recovery the error names is not on the CLI, the web, the TUI, or MCP. `GET /v1/groups` still lists the archived group, so `groups list` shows the word and cannot clear it.

**Admin parity.** [ADM] marks team create as CLI-partial (`team add` only), member replace and list as HTTP-only, and group unarchive as HTTP-only. Task 6 adds `vecnet_teams_list`, `vecnet_team_get`, `vecnet_team_create`, `vecnet_team_members_set`, `vecnet_admin_group_get`, and `vecnet_group_unarchive`, plus the matching CLI commands. Ordinary group create and add stay member powers, not admin powers.

---

## 7. Blob

Two stores, one operator purge by hash.

**Disk row** (`blobs`): present with `purged_at` null, or tombstoned with `purged_at` set. `ttl_until` applies only while nothing references the blob ([SQL] `0001_init.sql` lines 223–230; [PROTO] §11 lines 818–823). A later read is 410 `blob_purged`.

**R2 row** (`blob_objects.state`): `pending`, `complete`, `rejected`, `abandoned`, `expired`, `purged` ([SQL] `0025_r2_blobs.sql` line 21). The wire projection a caller sees is coarser ([BLOB] `transfers.go` lines 521–545; `blobwire/types.go` lines 80–86):

| Projected `state` | When |
|---|---|
| `purged` | SHA-256 was verified and a tombstone exists, or the row state is `purged` |
| `expired` | row state `expired`, or `expires_at` has passed |
| `available` | row state `complete` |
| `unavailable` | `pending`, `rejected`, `abandoned`, or not yet verified |

`POST /v1/admin/blobs/{sha}/purge` calls the disk store's `Purge`, which writes `blob_purge_tombstones`, sets `blobs.purged_at`, erases durable bytes, and enqueues R2 deletes for every object with that hash ([HTTP] `admin.go` lines 642–651; [BLOB] `store.go` lines 473–488; `usage.go` lines 54–85). Audited as `purge` ([HUB] `admin.go` lines 803–816). Unknown hash is 404 `unknown_blob`.

| Surface | Display | Actions |
|---|---|---|
| Web | Thread files: a purged file is a disabled button labelled purged. Opening a file the hub has since purged toasts and stops ([UI] `aside-model.js` lines 55–57; `thread-aside.js` lines 71–88) | Download a live file. No purge |
| CLI | Attachments upload with `PUT /v1/blobs` and are fetched by `blob_url` ([CLI] `commands.go` lines 896–914). No blob admin command | No purge |
| TUI | Chat lists a purged file as `purged` and refuses to save it ([TUI] `tui.go` line 46; `screen_chat.go` lines 1368–1369, 2132–2133) | No purge |
| MCP | `vecnet_blob_put` and `vecnet_blob_get` move bytes by hash ([MCP] `tools.go` lines 966–982; [PROTO] §4.3 lines 554–555) | No purge tool. Results do not include a `blob_url` |

**Dead end.** Purge is terminal, which is what [PROTO] §11 specifies: there is no un-purge, and the tombstone is the record. The gap is reaching purge. A file the operator wants removed stays readable from the thread aside until someone posts the admin route. `pending` and `abandoned` R2 rows have no operator screen at all.

**Admin parity.** [ADM] lists blob purge as HTTP-only and plans `vecnet_admin_blob_get` plus `vecnet_admin_blob_purge` (Task 6) and a web control (Task 8). The R2 plan owns object lifecycle; this plan's purge is the operator permission and the safe metadata read. That split is still accurate: the HTTP purge already tombstones an R2 hash, and no client exposes it.

---

## 8. Thread

A thread has no single status column. The operator-visible fields are the ones `PATCH` can change, plus delivery state and loop cuts.

| Field | Values | Where set |
|---|---|---|
| `history_access` | `from_join` (default) or `full` | [SQL] `0001_init.sql` line 108; patch [HTTP] `admin.go` lines 581–625 |
| `locked_participants` | 0 or 1 | [SQL] line 109 |
| `budget_remaining` | integer, or null for unlimited. Default 500. Only the operator raises it | [PROTO] §6 lines 714–714; [HTTP] `admin.go` lines 596–606 |
| `title` | string or null | [HTTP] `admin.go` lines 608–616 |
| Delivery `state` | `queued`, `pushed`, `accepted`, `processed`, `failed`, `expired`, `dead_letter`, and internal `superseded` | [PROTO] §5.1 lines 637–647 |
| `held` | 1 while a `wake_idle` delivery waits | [SQL] `0001_init.sql` line 161 |
| `dispatching` | in-flight attempt count, not a state | [SQL] `0013_delivery_dispatching.sql` lines 1–6 |
| Loop cut | `cut.reason` and `cut.at` on the operator thread row | [PROTO] §2.13 lines 349–350 |

Dead letters are the operator's queue for `dead_letter`: list, retry (fresh budget of 8, same `delivery_id`), discard ([PROTO] §5.5 lines 677–679; [CLI] `commands.go` lines 1637–1680). Those three exist on HTTP, CLI, web inbox, and TUI inbox. They do not exist on MCP.

| Surface | Display | Actions that change a thread |
|---|---|---|
| Web | Dashboard and thread aside: budget (`unlimited` or the number; 0 is bad), history, locked yes or no, cut ([UI] `dash.js` line 184; `thread-aside.js` lines 128–134). Matrix shows per-recipient delivery state | Send, reply, react, answer an approval, retry or discard a dead letter, mark read. No budget, title, history, or lock control. Composer can set `locked_participants` on a *new* send ([UI] `compose-model.js` line 86), which is the sender's field, not the admin patch |
| CLI | `threads` prints id, message count, participants, title ([CLI] `commands.go` lines 1118–1127). `thread` prints messages. `dead-letters list` prints delivery id, recipient, thread, attempt, status | `dead-letters retry` and `discard`. No `thread` patch |
| TUI | Chat title keeps the budget on screen. A send refused with `thread_budget_exhausted` toasts "thread budget exhausted (budget N), your text is kept" ([TUI] `screen_chat.go` lines 695–700, 1523–1533). Matrix and inbox show delivery and dead-letter rows | Send, answer, react, mark read, retry, discard, resume a resumable subscription. No patch |
| MCP | `vecnet_threads` and `vecnet_thread` are the participant read ([MCP] `tools.go` lines 882–893; [PROTO] §4.3 lines 542–543) | Send and reply through the ordinary tools. No admin patch. A budget refusal comes back as the send error |

**Dead end.** `thread_budget_exhausted` is a 422 on the send the operator just attempted ([PROTO] §6 line 712). Web and TUI show the zero and keep the draft. Nothing on those screens, or on the CLI, or on MCP, raises `budget_remaining`. The only clear is `PATCH /v1/admin/threads/{id}`. The same is true of a locked thread and of `history_access`: they are visible and not editable outside that route. Dead letters are the exception: retry and discard exist on web, CLI, and TUI.

**Admin parity.** [ADM] lists thread patch as HTTP-only and read-only on web and TUI, and plans `vecnet_admin_thread_patch` plus CLI `thread admin-get` / `patch` (Task 6) and web controls (Task 8). Dead-letter MCP tools are in the same task. Ordinary `vecnet_thread` does not become an admin view.

---

## Gap table

Ranked by how likely an operator is to hit the gap while using the web, the TUI, or the CLI they already run. Proposed fixes name the existing plan. None of them are implemented here.

| Rank | Gap | Why it comes up | Proposed fix |
|---|---|---|---|
| 1 | Paused or expired subscription with a healthy sibling, and no end | Every new callback URL or new dot principal leaves the old row. Inbox attention, the TUI subs badge, and (for `token_expired`, `unauthorized`, and `delivery_failed`) the lifetime `events_paused` counter keep alarming. Web and TUI offer no button. CLI resume does not stick when the token or the peer is still bad | CoS `msg_01M6R14ES8A9HS2PZW7TZRPAGW`: auto-supersede when a healthy sibling covers the same event and arguments; auto-end `credential_rotated` whose token expired more than 7 days ago; `DELETE /v1/admin/subscriptions/{id}` soft-`ended` with audit `subscription_end`; CLI `vecnet admin subs end`; web Remove; MCP admin parity; alarms count only paused or failed rows with no healthy sibling. Hub after DB compaction Tasks 5–9; UI after the hub. [ADM] Task 6 still adds pause, resume, and get for the rows that should stay |
| 2 | Thread budget exhausted, and no raise | Default budget is 500 ([PROTO] §6). The TUI keeps the draft and names the error. Web shows budget 0. CLI `thread` cannot patch | [ADM] Task 6 `vecnet_admin_thread_patch` and CLI thread patch; Task 8 web control; TUI forms in the follow-up. Until then the only clear is `PATCH /v1/admin/threads/{id}` |
| 3 | "Enable the peer" with no enable | Disable, move, or a dropped OAuth binding pauses subscriptions as `paused_auth`. The note says to enable or relink. Web, CLI, and TUI cannot disable or enable. Relink is CLI `peer link` and the web dot panel; enable is HTTP only | [ADM] Task 5 `vecnet_admin_peer_set_disabled` and CLI `peer disable` / `peer enable`; Task 8 web enable on the peer page, next to the sentence that already promises it |
| 4 | Archived group, 410, no unarchive | Idle groups archive after 30 days ([HUB] `groups.go` lines 17–19). The error tells the operator to unarchive. `groups list` prints the word. No command or screen clears it | [ADM] Task 6 `vecnet_group_unarchive` and CLI `groups unarchive`; Task 8 a groups screen. HTTP `POST /v1/admin/groups/{id}/unarchive` already does the write |
| 5 | Invite has no status and no recovery | Every Add agent and every `peer invite` leaves a hash with no id. Expired, spent, and withdrawn look the same once `used_at` is set. A wrong claim has no one-step disable | [ADM] contract 11 and Task 4: persist `pending\|claimed\|expired\|revoked` without the token; Task 5 `peer invite list`, `status`, `revoke`, and claimed-enrolment recovery. Human paste blocks stay on CLI and web |
| 6 | Unverified push route painted red, Verify only on the CLI | `route add` of kind `http` is dark until `route verify`. The peer page shows `unverified` and does not post verify. TUI and MCP omit routes | [ADM] Task 6 `vecnet_route_verify` and `vecnet_route_remove`, with create and rotate on the human channel; Task 8 verify and remove on the peer route table. CLI verify already exists |
| 7 | `credential_rotated` looks like a healthy peer | Rotate (CLI or HTTP) pauses the old vecnet subscription and the new credential reads `active`. The subscription stays on the inbox as `paused_auth` with no resume | Same end path as rank 1. [ADM] Task 5 keeps rotate on the human channel so MCP never sees the new secret. Add CLI `principal rotate` for the HTTP route that already exists ([HTTP] `admin.go` line 34) |
| 8 | Blob purge is a bare POST | Thread files show `purged` only after the fact. Nothing in web, CLI, TUI, or MCP sends the purge. `pending` and `abandoned` R2 rows have no operator view | [ADM] Task 6 `vecnet_admin_blob_get` and `vecnet_admin_blob_purge`; Task 8 a purge control on the file row. The hash tombstone in [BLOB] `usage.go` is the write to call |
| 9 | Team is create-only | `team add` returns an id. There is no CLI list, no member edit, no web team page, no MCP team tool. Membership still fans out sends ([PROTO] §7) | [ADM] Task 6 team list, get, create, and members set, hub boundary. CLI `team list` and `team members` in the same task |
| 10 | MCP cannot administer any of these objects | The model can read its own subscriptions, put and get blobs, and create groups. It cannot pause, end, disable, verify, unarchive, purge, or raise a budget. `vecnet_subscriptions` on an operator credential does not list dot rows | [ADM] Tasks 5–6 static admin tools that return `forbidden` with the capability name until granted; contract 7. Self-only tools do not count as the operator tools |

Ranks 1, 2, 3, and 4 are dead ends in the sense this audit uses: a state with an error or an alarm, and no action on the surface that shows it. Ranks 5–10 are the same holes seen before the alarm, or the MCP half of a hole the CLI already covers.
