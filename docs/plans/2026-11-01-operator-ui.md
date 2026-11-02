# vecnet Operator UI (Phase 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the operator surface of `SPEC.md` §16 phase 3: variant A's web inbox and chat with a wider thread side panel and an openable delivery matrix, a provisional read-only dashboard, subscription health for dots, and a keyboard-first `vecnet tui`, both clients running on one operator read API.

**Architecture:** The hub owns the read models. New operator-only `/v1/admin/*` reads return finished snapshots (inbox sections, thread detail, delivery matrix, peer health), so web and TUI hold almost no domain logic. Both clients use **snapshot + invalidate**: an event from `/v1/events` never mutates client state, it only names tags (`inbox`, `threads`, `thread:<id>`, `peers`) whose snapshots are refetched; the client acks once the refetch has landed, which is what "accepted once shown" means for held deliveries. Wire types live in the leaf package `internal/opapi` so the CLI binary never links the hub or SQLite; queries live in `internal/opview`. The web UI stays plain ES modules served by `internal/ui`, split into one module per screen. The TUI is `vecnet tui`, built on Bubble Tea v2.

**Tech Stack:** Go 1.26 (`CGO_ENABLED=0`), stdlib `net/http`, `database/sql`; `charm.land/bubbletea/v2` v2.1.1 and `charm.land/lipgloss/v2` v2.0.6 (TUI only, both checked pure Go and `go 1.26`-compatible); vanilla ES modules with no bundler and no npm dependency; `node --test` (Node 22) for pure JS; Playwright and Chromium from the existing `headless-browser-screenshots` skill install for screen scripts; `tmux` for TUI screenshots.

**Spec:** `.scratch/vecnet/issues/18-operator-ui.md` (Max's verdict there is binding), `docs/prototypes/operator-ui/` (variant A for web, variant C for TUI; shots in `shots/`), `SPEC.md` §13, §16; `docs/PROTOCOL.md` §2.3, §2.8, §2.12, §5.2, §5.5, §8, §9, §13.4, §14, §15, §16; `docs/plans/2026-11-01-connectors.md` Task 5 (subscription table and admin endpoint). Out of scope: enrolment, scope editing and pausing a subscription in the UI (the CLI covers them), budget raising, escalation, attachment upload from the UI, team or group addressing from the UI, a light theme, the web keyboard layer (decided below).

**Precondition (met 2026-11-02):** `connectors` is merged to `master` at `eb5d197`, and this plan was reconciled against that merge. `GET /v1/admin/subscriptions` stays a bare array, `POST …/{id}/pause` and `POST …/{id}/resume` exist, `event_subscriptions.last_refresh_at` exists (migration 0009), and `paused_reason` has five values, not two (Task 6).

## Dependency map

Every task starts the moment its dependencies are merged. There are no levels or groups.

```json
{"1":[],"2":[],"3":[1],"4":[1],"5":[1],"6":[1],"7":[1,2],"8":[3,7],"9":[4,7],"10":[4,7],"11":[4,7],"12":[4,5,7],"13":[3,6,7],"14":[3,7],"15":[7],"16":[1],"17":[2,16],"18":[3,17],"19":[4,17],"20":[4,5,17],"21":[6,17],"22":[17],"23":[8,9,10,11,12,13,14,15,18,19,20,21,22,24,25],"24":[7,12,18],"25":[8,9,10,11,12,13,14,15,18,19,20,21,22,24]}
```

Build settings: `taskPrefix: "oui"`, worktree `.worktrees/operator-ui`, branch `operator-ui` off `master`, status files in `.scratch/vecnet/build-operator-ui/` (gitignored by Task 2's first commit; the coordinator adds the line first if it starts before Task 2).

## Global Constraints

- Everything in the core and connectors plans' Global Constraints still applies (module path, ids, timestamps, error envelope, secrets never logged, `git -c commit.gpgsign=false commit`).
- Build and test only through `build2` with two threads: `build2 go test -p 2 ./...`, `CGO_ENABLED=0 build2 go build -p 2 ...`, `build2 node --test --test-concurrency=2 internal/ui/jstest/`, `build2 node scripts/ui/<name>.mjs`.
- Implementers never touch the dev hub on 45752 or its `.data/`. Screen scripts boot their own demo hub on port 0 in a temp dir. Implementers do not use `attn` or `xdg-open`.
- **Live preview (coordinator only, Max's standing rule):** once Task 7 merges, run `just ui-demo` in `.worktrees/operator-ui` on generated port **49575** with `--dev-ui-dir internal/ui/static`, `xdg-open http://127.0.0.1:49575/ui/` once, and leave it running so merged work shows on refresh.
- Web: plain HTML, CSS and ES modules, no bundler, flat files in `internal/ui/static/` (`ui.Handler` refuses subdirectories), everything in that directory ≤ **256 KiB** total (test-enforced, raised from 60 KiB by Task 7).
- The `ui.CSP` constant does not change. No inline script, no inline style, no `style=` attribute. A dynamic value goes through `el.style.setProperty('--x', v)` and CSS.
- **Bodies are untrusted (§14.2).** Web renders every peer-supplied string (text, names, titles, options, file names, error messages) with `textContent` or `h()`. `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `eval` and `new Function` are banned by test. No Markdown rendering and no linkifying. TUI passes every peer-supplied string through `tui.Clean`/`tui.CleanBody`.
- The operator secret stays in `localStorage` under `vecnet.ui.secret` (web) or the CLI profile (TUI) and travels only in `Authorization` (§2). All UI sends carry `X-Vecnet-Ingress: operator_ui`, TUI included.
- **The dashboard performs no writes** (Max, ticket 18). Dead-letter retry and discard and subscription resume live in the inbox.
- **Presence and reachability are two separate signals.** `unknown` presence is never rendered as offline; push-only peers show `reachable` or `unreachable` (§8).
- Countdowns and "expires in" use the hub's clock: the `now` field of a snapshot, never the client's clock alone.
- Dark only. Palette: background `#0b0f19`, text `#f3f4f6`, accent `linear-gradient(135deg,#6366f1 0%,#a855f7 100%)`; receipt-state colours as in `shots/a-chat.png`; `color-scheme: dark`. Fonts are `Inter, system-ui, sans-serif` and `'JetBrains Mono', ui-monospace, monospace` by name only; no font files ship. Times are en-AU 24-hour local, full id in `title`.
- Viewport for web shots 1440×900; TUI shots 140×42.
- **Every UI task commits four PNGs** named `docs/plans/shots/oui/<NN>-<screen>-<state>.png`, `<state>` being `initial`, `progress`, `success`, `error`, as defined in that task. All shots are dark. Inspect each PNG with the Read tool before committing; a shot nobody looked at does not count.
- Only Task 17 touches `go.mod`/`go.sum`. No other task adds a dependency. `go list -deps ./cmd/vecnet | grep -c 'modernc.org/sqlite'` stays 0.

## Shared resources (pre-assigned)

| Resource | Owner |
|---|---|
| Migration `0010_audit_action_index.sql` | Task 5 |
| Migration `0011_inbox_indexes.sql` | Task 3 |
| `GET /v1/admin/inbox`, `POST /v1/admin/threads/{id}/read` | Task 1 (handler), Task 3 (query) |
| `GET /v1/admin/threads`, `GET /v1/admin/threads/{id}`, `GET /v1/admin/threads/{id}/deliveries` | Task 1, Task 4 |
| `GET /v1/admin/peers`, `GET /v1/admin/loop-cuts`, `GET /v1/admin/delivery-health` | Task 1, Task 5 |
| `GET /v1/admin/subscriptions` (additive fields) | Task 6 |
| `internal/opapi/*`, `internal/httpapi/op.go`, `docs/PROTOCOL.md` | Task 1 only |
| `internal/opview/{inbox,threads,dash,subs}.go` | stubbed by Task 1; then Tasks 3, 4, 5, 6 respectively |
| `justfile`, `.gitignore`, `scripts/ui/lib.mjs`, `tmux.mjs`, `ansi.mjs` | Task 2 only |
| `index.html`, `app.js`, `api.js`, `store.js`, `format.js`, `dom.js`, `approvals.js`, `base.css` | Task 7 only |
| `inbox.js`, `inbox-model.js`, `inbox.css`, `jstest/inbox.test.mjs`, `scripts/ui/inbox.mjs` | Task 8 |
| `thread.js`, `thread-model.js`, `thread.css`, `jstest/thread.test.mjs`, `scripts/ui/thread.mjs` | Task 9 |
| `thread-aside.js`, `aside-model.js`, `aside.css`, `jstest/aside.test.mjs`, `scripts/ui/aside.mjs` | Task 10 |
| `matrix.js`, `matrix-model.js`, `matrix.css`, `jstest/matrix.test.mjs`, `scripts/ui/matrix.mjs` | Task 11 |
| `dash.js`, `dash-model.js`, `dash.css`, `jstest/dash.test.mjs`, `scripts/ui/dash.mjs` | Task 12 |
| `subs.js`, `subs-model.js`, `subs.css`, `jstest/subs.test.mjs`, `scripts/ui/subs.mjs` | Task 13 |
| `presence.js`, `presence-model.js`, `presence.css`, `jstest/presence.test.mjs`, `scripts/ui/presence.mjs` | Task 14 |
| `compose.js`, `compose-model.js`, `compose.css`, `jstest/compose.test.mjs`, `scripts/ui/compose.mjs` | Task 15 |
| `internal/opclient/*` | Task 16 |
| `go.mod`, `go.sum`, `internal/cli/commands.go`, `internal/tui/{tui,model,keys,theme,layout,clean,data}.go` | Task 17 only |
| `internal/tui/screen_inbox.go` | stub by 17, then Task 18 |
| `internal/tui/screen_chat.go` | stub by 17, then Task 19 |
| `internal/tui/screen_dash.go` | stub by 17, then Task 20 |
| `internal/tui/screen_subs.go` | stub by 17, then Task 21 |
| `internal/tui/cmdline.go` | stub by 17, then Task 22 |
| Task 24 (after all its owners merged): `operator_view` across `internal/hub/`, `internal/opapi/tags.go`, `store.js` `tagsFor`, `docs/PROTOCOL.md` §2.13 row, both schema files, the TUI inbox header, `scripts/ui/dash.mjs:32` | Task 24 |

Stub files exist from Tasks 1, 7 and 17 so that later tasks edit only files they own and worktrees merge without conflicts. A task that needs a neighbour calls its stub's agreed export; it does not edit the neighbour's file.

## Review Focus

1. **Escape sequences in bot text reaching the operator's terminal** (a message body, peer name, thread title or file name holding OSC 52 clipboard writes, OSC 8 links, CSI cursor moves, C1 controls or U+202E bidi overrides): the TUI shows inert text and the terminal's state does not change. The web has no equivalent, but `<img onerror>` in a body must render as literal text. Pinned in Tasks 7, 8, 17, 18, 19.
2. **An approval that closes under the operator's finger** (answered from the other client, expired mid-click, or double-clicked): exactly one response is sent; a late click gets 422 `approval_closed`, the card shows closed and never retries, and the operator is not told the answer landed. Pinned in Tasks 8, 18.
3. **Client clock wrong by minutes** (xsm has a recorded NTP skew): countdowns still match the hub, because a client that trusts its own clock shows an open approval as expired, or the reverse. Pinned in Tasks 3, 7, 18.
4. **A long-lived log and a rotated secret**: a browser opening after months must not replay from `e0.0`, a 409 `cursor_purged` resumes at `oldest_cursor` and refetches everything, 401 stops the loop and returns to sign-in with no retry storm, and two clients open at once share read state and do not fight over presence. Pinned in Tasks 7, 14, 16, 23.
5. **A paused subscription read as healthy, or as a broken button**: every `paused_reason` must show as paused, and Resume is offered only where it works. `token_expired` needs ChatGPT to re-subscribe (the worker re-pauses it), `unauthorized` needs the peer enabled or relinked (the worker re-pauses it), and `credential_rotated` must not be resumed from a button because the callback may no longer be the holder's. Only `delivery_failed` and `operator` offer Resume. A dot with `unknown` presence never reads as offline. Pinned in Tasks 6, 10, 13, 18, 20.

---

## File structure

```
internal/opapi/types.go, tags.go            wire types and the event→tag table; imports only envelope
internal/opapi/testdata/tags.json           tag fixture shared by the Go and JS tests
internal/opview/reader.go                   Reader; inbox.go threads.go dash.go subs.go hold the queries
internal/httpapi/op.go                      routes, parameter parsing, JSON for the reads above
internal/opseed/seed.go, subs.go            demo scenario used by every screen check
internal/opseed/demo/main.go                demo hub (outside cmd/, so never built into the image)
internal/opclient/client.go, tail.go        typed Go client and the event-tail loop
internal/tui/                               Bubble Tea app, one screen_*.go per tab
internal/cli/tui.go                         `vecnet tui`
internal/ui/static/                         app shell, one module + one model + one stylesheet per screen
internal/ui/jstest/*.test.mjs               node --test over the pure *-model.js and core modules
scripts/ui/*.mjs                            Playwright and tmux screen scripts, screenshots
docs/OPERATOR.md                            operator guide
```

## Recipes

Tasks name a recipe rather than repeating its steps.

**Web recipe** (after the task's Step 1 test):
1. `build2 node --test --test-concurrency=2 internal/ui/jstest/<x>.test.mjs` → FAIL.
2. Implement the task's files.
3. Same command → `ok`; `build2 go test -p 2 ./internal/httpapi/ ./internal/ui/...` → `ok`.
4. `build2 node scripts/ui/<x>.mjs` exits 0: it runs the task's named assertions and writes the four PNGs.
5. Read each PNG, compare with the prototype shot the task names, fix what is off, re-run.
6. Commit code and PNGs with the task's message.

**TUI recipe** (after the task's Step 1 test):
1. `build2 go test -p 2 ./internal/tui/ -run '<pattern>'` → FAIL.
2. Implement the task's file.
3. Same command → `ok`; `UPDATE_GOLDEN=1` only for a change you have read in the diff.
4. `build2 node scripts/ui/tui-<x>.mjs` exits 0 and writes the four PNGs.
5. Read each PNG against `shots/c-*.png`, fix, re-run.
6. Commit code, goldens and PNGs with the task's message.

**Backend recipe:** FAIL → implement → `build2 go test -p 2 ./internal/opview/ ./internal/httpapi/` `ok` → commit.

---

### Task 1: Operator API contract

**Depends:** none

**Files:**
- Create: `internal/opapi/types.go`, `internal/opapi/tags.go`, `internal/opapi/testdata/tags.json`, `internal/opview/reader.go`, `internal/opview/inbox.go`, `internal/opview/threads.go`, `internal/opview/dash.go`, `internal/opview/subs.go`, `internal/httpapi/op.go`
- Modify: `internal/httpapi/server.go` (`server.op *opview.Reader`, built in `New`), `internal/httpapi/admin.go` (one line in `adminRoutes`: `s.opRoutes(mux)`), `docs/PROTOCOL.md` (§2.1 row, new §2.13 listing the routes below and the additive `health`, `health_note` and `refresh_before` fields of `GET /v1/admin/subscriptions`, whose values Task 6 fills; the temporary `not_implemented` stub error stays undocumented, since Task 23 removes it)
- Test: `internal/httpapi/op_test.go`, `internal/opapi/tags_test.go`

**Interfaces:**
- Produces wire types. `opapi` imports only `envelope`. JSON tags are the snake_case of each field, nullable fields are pointers, slices are never `null`. Do not embed `envelope.ThreadSummary` anonymously: it has its own `MarshalJSON`, which would swallow every sibling field; hence the named `Thread` field.
  ```go
  package opapi
  type Me struct{ PeerID, Name, Presence string; PresenceAt *string }
  type InboxQuery struct{ Limit int; Before string; UnreadOnly bool }
  type OpenApproval struct{ ApprovalID, ThreadID, ThreadTitle, MessageID, Requester, Question, Text, ExpiresAt string; Options []string }
  type DeadLetter struct{ DeliveryID, MessageID, ThreadID, Recipient, Sender, Excerpt, Class, UpdatedAt string; Attempt int; LastStatus *int; LastError *string } // Class: "retries_exhausted"|"terminal"|"network"
  type Attention struct{ Kind, ID, PeerID, Health, Summary, Action, At string }                 // Kind "subscription"; Action "resume" or ""
  type InboxDelivery struct{ DeliveryID, MessageID, ThreadID, ThreadTitle, Sender, Kind, Priority, Section, State, CreatedAt, Excerpt string; ReadAt *string; Attachments int } // Section: "woke"|"held"
  type Inbox struct{ Now string; Me Me; Approvals []OpenApproval; DeadLetters []DeadLetter; Attention []Attention; Deliveries []InboxDelivery; Unread int; NextBefore *string }
  type ReadResult struct{ Marked int }

  type ThreadQuery struct{ Participant, Cursor string; Limit int }
  type CutInfo struct{ Reason, At string }
  type ThreadRow struct{ Thread envelope.ThreadSummary; CreatedBy, CreatedAt, LastMessageAt string; Cut *CutInfo }
  type ThreadPage struct{ Threads []ThreadRow; Next *string }
  type ThreadFile struct{ SHA256, Name, MediaType, MessageID, Sender, CreatedAt string; Size int64; Spilled, Purged bool }
  type ThreadApproval struct{ ApprovalID, MessageID, ExpiresAt string; Options []string; AddressedToMe bool }
  type ThreadDetail struct{ Now string; Row ThreadRow; Files []ThreadFile; OpenApprovals []ThreadApproval }
  type MatrixCell struct{ Recipient, DeliveryID, State, Display, UpdatedAt string; Attempt int; LastStatus *int; ReadAt *string }
  type MatrixRow struct{ MessageID, CreatedAt, Sender, Excerpt string; Cells []MatrixCell }
  type Matrix struct{ Recipients []string; Rows []MatrixRow; NextAfter *string }

  type Peer struct{ /* same fields and JSON as hub.DirectoryEntry */ }
  type RouteInfo struct{ RouteID, Kind, Host string; VerifiedAt *string }                       // Host only: never a path, key or secret
  type PeerRow struct{ Peer Peer; PrincipalID, Credential string; Disabled bool; PresenceAt, ReachVia *string; Routes []RouteInfo } // Credential: "active"|"revoked"|"none"
  type LoopCut struct{ At, ThreadID, Actor, Reason string }
  type LoopCuts struct{ Since string; Hours, Total int; ByReason map[string]int; Recent []LoopCut }
  type BlockedThread struct{ Recipient, ThreadID, DeliveryID string; Attempt, Waiting int; NextAttemptAt *string; LastStatus *int; LastError *string }
  type FailedDelivery struct{ DeliveryID, Recipient, ThreadID, UpdatedAt string; LastStatus *int; LastError *string }
  type DeliveryHealth struct{ Blocked []BlockedThread; Failed []FailedDelivery }

  const ( HealthOK = "ok"; HealthFailing = "failing"; HealthLate = "late"; HealthExpired = "expired"; HealthPausedToken = "paused_token"; HealthPausedFailed = "paused_failed"; HealthPausedAuth = "paused_auth"; HealthPausedOperator = "paused_operator"; HealthUnverified = "unverified" )
  type Subscription struct{ SubID, Principal, PeerID, URL, Name, Cursor, Health, HealthNote string; ExpiresAt, RefreshBefore, LastRefreshAt, VerifiedAt, PausedReason, LastDeliveryAt *string; LastStatus *int; Attempts int }

  type AuditEntry struct{ /* same JSON as hub.AuditEntry */ }; type AuditPage struct{ Entries []AuditEntry; Next *string }
  type History struct{ Messages []envelope.Message; NextAfter *string }
  type SendResult struct{ /* same JSON as hub.SendResponse */ }

  func DisplayState(state string, readAt *string) string // "read" when state is accepted or pushed and readAt is set, else state
  func Excerpt(m envelope.Message) string                // approval → "Approval: <question>", response → "Decision: <decision>", job status → "Job <state> · <text>", else the text; ≤ 200 bytes cut on a rune boundary, newlines collapsed to spaces
  func Tags(ev envelope.Event) []string                  // invalidation tags, table below
  ```
- `Tags` table, fixed by `testdata/tags.json` (`[{"event":{…},"tags":[…]}]`), which the Go test and Task 7's JS test both load: `delivery` → `inbox`, `threads`, `thread:<message.thread_id>`; `receipt` → `inbox`, `thread:<thread_id>`; `thread_updated` → `threads`, `thread:<thread_id>`; `presence` → `peers`; `directory_updated` → `peers`; `approval_expired` → `inbox`, `thread:<thread_id>`; unknown type or undecodable data → none.
- Produces the reader, whose stubs return `hub.E(501, "not_implemented", …)` except `Attention`, which returns `nil, nil` so Task 3 works before Task 6:
  ```go
  package opview
  type Reader struct{ Hub *hub.Hub }
  func New(h *hub.Hub) *Reader
  func (r *Reader) Inbox(ctx, c auth.Caller, q opapi.InboxQuery) (opapi.Inbox, error)           // Task 3
  func (r *Reader) MarkThreadRead(ctx, c auth.Caller, thrID string) (opapi.ReadResult, error)   // Task 3
  func (r *Reader) Threads(ctx, q opapi.ThreadQuery) (opapi.ThreadPage, error)                  // Task 4
  func (r *Reader) Thread(ctx, c auth.Caller, thrID string) (opapi.ThreadDetail, error)         // Task 4
  func (r *Reader) Matrix(ctx, thrID, after string, limit int) (opapi.Matrix, error)            // Task 4
  func (r *Reader) Peers(ctx) ([]opapi.PeerRow, error)                                          // Task 5
  func (r *Reader) LoopCuts(ctx, hours int) (opapi.LoopCuts, error)                             // Task 5
  func (r *Reader) DeliveryHealth(ctx) (opapi.DeliveryHealth, error)                            // Task 5
  func (r *Reader) Subscriptions(ctx) ([]opapi.Subscription, error)                             // Task 6
  func (r *Reader) Attention(ctx) ([]opapi.Attention, error)                                    // Task 6
  ```
- Produces `opRoutes` and handlers: `GET /v1/admin/inbox?limit=&before=<dly_id>&unread=1` → `opapi.Inbox`; `POST /v1/admin/threads/{id}/read` → `ReadResult`; `GET /v1/admin/threads?participant=&limit=&cursor=` → `ThreadPage`; `GET /v1/admin/threads/{id}` → `ThreadDetail`; `GET /v1/admin/threads/{id}/deliveries?after=<msg_id>&limit=` → `Matrix`; `GET /v1/admin/peers` → `{"peers":[…]}`; `GET /v1/admin/loop-cuts?hours=` (1–168, default 24) → `LoopCuts`; `GET /v1/admin/delivery-health` → `DeliveryHealth`. Reuse `queryLimit`; `before` must be a `dly_` id and `participant` a `peer_` id, else 422 `invalid_value` with `details.field`. Everything is under `/v1/admin/`, so the auth middleware already gates it.

- [ ] **Step 1: Write the failing tests**

```go
func TestOpRoutesOperatorOnly(t *testing.T) { /* for each of the eight routes: no bearer → 401 unauthenticated; peer bearer → 403 forbidden; PATCH /v1/admin/threads/{id} still works */ }
func TestOpParamValidation(t *testing.T) { /* limit=0, hours=0, hours=169, before=xyz, participant=xyz → 422 invalid_value with details.field, checked before the reader runs */ }
func TestTagsFixture(t *testing.T) { /* every row of testdata/tags.json; unknown type → empty; malformed data → empty, no panic */ }
func TestDisplayState(t *testing.T) { /* accepted+readAt → read; processed+readAt → processed; dead_letter+readAt → dead_letter; accepted → accepted */ }
func TestExcerpt(t *testing.T) { /* 300 bytes of 4-byte runes → ≤ 200 bytes, valid UTF-8; approval and job forms; newlines collapsed */ }
func TestThreadRowJSONKeepsSiblings(t *testing.T) { /* marshal ThreadRow: has thread.thread_id and last_message_at at top level */ }
```

- [ ] **Step 2–5:** Backend recipe → commit `feat: operator API contract (opapi types, opview stubs, routes)`

---

### Task 2: Demo hub, seed scenario, visual harness

**Depends:** none

**Files:**
- Create: `internal/opseed/seed.go`, `internal/opseed/subs.go`, `internal/opseed/demo/main.go`, `scripts/ui/lib.mjs`, `scripts/ui/tmux.mjs`, `scripts/ui/ansi.mjs`, `scripts/ui/smoke.mjs`
- Modify: `justfile`, `.gitignore` (`.demo-data/`, `.scratch/vecnet/build-operator-ui/`), `docs/adr/0001-tech-stack.md` (one line: the UI stays hub-served; the demo preview uses generated port 49575)
- Test: `internal/opseed/seed_test.go`

**Interfaces:**
- Produces:
  ```go
  package opseed
  type Options struct{ Scale bool }                 // Scale adds one 64-participant thread and one 500-message thread
  type Scenario struct{ OperatorPeer string; Peers map[string]string /* name→peer_id */; Secrets map[string]string /* name→peer secret */; Threads map[string]string /* key→thr_id */; Approvals, DeadLetters, Subscriptions []string }
  func Seed(ctx context.Context, h *hub.Hub, opCaller auth.Caller, o Options) (Scenario, error)
  ```
  Peers, sends, approvals and presence go through real `hub` methods; dead letters, one `failed` delivery, one blocked thread, loop-cut audit rows and `event_subscriptions` rows are direct SQL, since this is a fixture. Contents, mirroring `docs/protocol/examples/` and the prototype's mock: ten peers of every kind, including a `dot` with `reachable` + `unknown` and an `unreachable` grokbot; thread keys `hark` (spill, job status, attachment, an open three-option approval, a closed approval, a hub `expired` line), `hosting` (open two-option approval), `ticket18`, `digest` (hold), `quota` (budget exhausted), `hostile` (message body `<img src=x onerror=alert(1)>` plus `\x1b]52;c;aGk=\x07\x1b[2Jpwn‮`, a peer named with the same escapes, an attachment named `..\x1b[31m.txt`); three dead letters (503 8/8, 410, network); one `failed` 409; one thread blocked on a retrying delivery; loop cuts of three reasons; six subscriptions, one healthy, one past `refreshBefore`, one `token_expired`, one `delivery_failed`, one `unauthorized`, one `operator`; operator presence `working`.
- Demo hub: `go run ./internal/opseed/demo --listen 127.0.0.1:0 [--data-dir D] [--dev-ui-dir D] [--scale] [--reset]`. Wired through `httpapi.NewStack(h)` and `Stack.Run`, as `cmd/vecnet-hub/main.go` and `testhub` are, never by hand, with `hub.Config.AllowPrivateEgress` true and the hub on an offset clock (real time plus an adjustable offset). Prints one JSON line `{url, operator_secret, operator_peer, peers:{name:{id,secret}}, threads:{key:id}, approvals:[…], dead_letters:[…]}`. Reusing a data dir reprints the saved line from `<dir>/demo.json` so the secret is stable. Only this binary serves the test controls: `POST /__demo/advance {"by":"15m"}`, `POST /__demo/stall {"for":"3s"}` (delays `/v1/*` responses) and `POST /__demo/fail {"prefix":"/v1/admin/delivery-health","status":500,"for":"10s"}`, which give the TUI scripts the in-progress and error states Playwright gets from route interception.
- `justfile` adds: `ui-demo` (`--listen 127.0.0.1:49575 --data-dir .demo-data --dev-ui-dir internal/ui/static`), `test-ui` (`build2 node --test --test-concurrency=2 internal/ui/jstest/`), `ui-shots` (runs every `scripts/ui/*.mjs` except libs, serially), `tui` (`build2 go run ./cmd/vecnet tui`).
- `scripts/ui/lib.mjs` (loads Playwright by dynamic import from `$HOME/.llm-general/skills/headless-browser-screenshots/node_modules/playwright/index.mjs`, overridable by `PLAYWRIGHT_MODULE`; `PLAYWRIGHT_BROWSERS_PATH` defaults to `$HOME/.cache/ms-playwright`; no `package.json` in the repo):
  ```js
  export async function withDemo({ scale = false, signedIn = true }, fn) // builds the demo once per worktree into a temp dir, boots on port 0, opens Chromium 1440×900, seeds localStorage, sets `page.setDefaultTimeout(60000)` because every script shares the two-core `build2` slice, calls fn(ctx), always kills hub and browser, throws on any console error or CSP violation not listed in ctx.allowConsole
  // ctx: { page, demo, shot(name), hold(glob) → release(), fail(glob, status, code, message), advance(by), asPeer(name, method, path, body) /* name "operator" uses the operator secret */, countRequests(filter), allowConsole(regex) }
  export function assert(cond, msg)
  ```
  `shot('08-inbox-initial')` writes `08-inbox-initial.png` to `$SHOTS_DIR`, default `docs/plans/shots/oui/`. Reviewers and the final gate set `SHOTS_DIR` so a check run never rewrites a committed PNG; `withTui`'s `shot` honours it too.
- `scripts/ui/tmux.mjs`: `withTui({ scale }, fn)` boots the demo, writes a temp `VECNET_CONFIG` profile holding the operator secret, starts `bin` built from `./cmd/vecnet` in a private tmux server (`tmux -L oui-<pid>`, size 140×42, so Max's tmux is never touched), and gives fn `{ keys(...k), waitFor(text), shot(name), demo, advance, stall(for), fail(prefix, status, for), asPeer }`. `scripts/ui/ansi.mjs` turns `capture-pane -e -p` output (SGR 0,1,2,4,7,22,39,49, 38/48;5 and 38/48;2) into an HTML page rendered to PNG with a fixed monospace font.

- [ ] **Step 1: Write the failing tests**

```go
func TestSeedScenario(t *testing.T) { /* on a testhub: 10 peers covering every kind; 2 open approvals addressed to the operator; 3 dead_letter + 1 failed deliveries; hostile thread body stored byte-exact; budget_remaining 0 on quota; 6 subscription rows, one per seeded health */ }
func TestSeedIsRepeatableOnFreshDB(t *testing.T) { /* two fresh hubs → same counts */ }
```

- [ ] **Step 2–5:** Backend recipe, plus `build2 node scripts/ui/smoke.mjs` exits 0 (boots the demo, shoots today's `/ui/` login to `docs/plans/shots/oui/02-smoke-login.png`, then runs `printf` colours through `withTui`'s tmux into `02-smoke-tmux.png`), and `git status` shows no stray demo data → commit `feat: demo hub, seed scenario and UI harness`

---

### Task 3: Inbox read model

**Depends:** 1

**Files:**
- Modify: `internal/opview/inbox.go`
- Create: `internal/opview/inbox_test.go`, `internal/store/migrations/0011_inbox_indexes.sql` (`CREATE INDEX deliveries_recipient_dly ON deliveries(recipient, dly_id); CREATE INDEX deliveries_unread ON deliveries(recipient) WHERE read_at IS NULL;`)

**Interfaces:**
- Consumes: Task 1 reader signatures; `hub.Receipt`, `hub.EffectivePresence`, `hub.DeadLetters`, tables `deliveries`, `messages`, `threads`, `approvals`, `peers`.
- Produces: the implemented `Inbox` and `MarkThreadRead`; excerpts come from `opapi.Excerpt`.
- `Inbox` rules: `Now` is `hub.Now()`; `Deliveries` are the caller peer's, newest `dly_id` first, default limit 50, max 200, excluding any delivery whose message is in `Approvals`; `Section` is `woke` for priority `wake` and `held` for `wake_idle` and `hold` (§5.2 gives both "inbox only"); `Approvals` are open, unexpired and addressed to the operator, soonest expiry first; `Class` is `retries_exhausted` for a 5xx/408/429 last status, `network` for a null status, otherwise `terminal`; `Attention` comes from `r.Attention`. `MarkThreadRead` calls `hub.Receipt(…, "read", "")` once per unread delivery, so senders get the usual receipts and repeats do nothing; this replaces the browser's `vecnet.ui.read` set, and read state becomes shared between web and TUI.

- [ ] **Step 1: Write the failing tests**

```go
func TestInboxSections(t *testing.T) { /* wake → woke; wake_idle, hold → held; newest first; unread count; another peer's deliveries absent */ }
func TestInboxApprovalLifecycle(t *testing.T) { /* open → in Approvals, not Deliveries; soonest first; answered or expired on FakeClock → gone from Approvals, present in Deliveries */ }
func TestInboxApprovalsOnlyMine(t *testing.T) { /* an approval addressed to another peer is absent */ }
func TestInboxDeadLetterEnriched(t *testing.T) { /* sender, excerpt, class table: 503 retries_exhausted, 410 terminal, null network */ }
func TestInboxPaging(t *testing.T) { /* limit=2 → next_before; following page disjoint; unread=1 filters; last page next_before null */ }
func TestInboxNowIsHubClock(t *testing.T) { /* now == FakeClock formatted, after Advance too (Review Focus 3) */ }
func TestInboxMe(t *testing.T) { /* after POST /v1/presence: me.presence, me.presence_at; stale after TTL → unknown */ }
func TestMarkThreadRead(t *testing.T) { /* two unread → marked 2, sender log has 2 read receipts; repeat → 0, no new receipts; unknown thread → 404 unknown_thread */ }
```

- [ ] **Step 2–5:** Backend recipe → commit `feat: operator inbox snapshot and bulk read`

---

### Task 4: Thread read models

**Depends:** 1

**Files:**
- Modify: `internal/opview/threads.go`
- Create: `internal/opview/threads_test.go`

**Interfaces:**
- Consumes: Task 1 reader signatures, `opapi.Excerpt`, `opapi.DisplayState`; tables `threads`, `thread_participants`, `deliveries`, `messages`, `blob_refs`, `blobs`, `approvals`, `audit`.
- `Threads`: every thread on the hub, newest activity first; `Participant` narrows to one peer; same opaque paging style as `hub.ListThreads`. `Cut` is the newest `loop_cut` audit row for the thread within 24 h, or `{thread_budget_exhausted, last_message_at}` when `budget_remaining` is 0.
- `Thread`: `Files` from every attachment of the thread's messages, newest first, `Spilled` from `metadata["vecnet.spilled"]`, `Purged` from the blob tombstone; `OpenApprovals` lists open unexpired approvals, `AddressedToMe` from `deliveries` for the caller. Unknown id → 404 `unknown_thread`.
- `Matrix`: one row per message from `after` (exclusive), default limit 50, max 200 (as PROTOCOL §2.13); `Cells` one per delivery (a sender has no cell for its own message); `Recipients` is the union in thread join order; `Display` from `opapi.DisplayState`. Hub-authored messages have zero cells and stay in `Rows`.

- [ ] **Step 1: Write the failing tests**

```go
func TestThreadsAllPeers(t *testing.T) { /* a thread between two peers without the operator is listed; participant= narrows; limit=999 clamps to 200; paging disjoint */ }
func TestThreadCut(t *testing.T) { /* budget 0 → cut.reason thread_budget_exhausted; depth cut → reason max_depth_exceeded within 24 h, null after FakeClock +25 h */ }
func TestThreadDetailFiles(t *testing.T) { /* attachment and spilled message.md both listed with sizes; spilled flag; purge → purged:true, row retained */ }
func TestThreadDetailApprovals(t *testing.T) { /* open addressed → addressed_to_me true; addressed elsewhere → false; closed → absent */ }
func TestMatrixCells(t *testing.T) { /* 2 messages × 3 recipients: states, display read after a read receipt, dead_letter shown, sender has no cell, recipients in join order */ }
func TestMatrixPagingAndUnknown(t *testing.T) { /* limit+after pages; unknown thread 404 unknown_thread */ }
func TestMatrix64Recipients(t *testing.T) { /* a 64-recipient message yields 64 cells */ }
```

- [ ] **Step 2–5:** Backend recipe → commit `feat: operator thread list, detail and delivery matrix`

---

### Task 5: Dashboard reads

**Depends:** 1

**Files:**
- Modify: `internal/opview/dash.go`
- Create: `internal/opview/dash_test.go`, `internal/store/migrations/0010_audit_action_index.sql` (`CREATE INDEX audit_action_at ON audit(action, at);`)

**Interfaces:**
- Consumes: Task 1 reader signatures; `hub.Directory`, `hub.EffectiveReachability`; tables `peers`, `credentials`, `routes`, `audit`, `deliveries`.
- `Peers`: `hub.Directory` rows plus disabled peers (directory fields read directly, presence through `EffectivePresence`); `Credential` is `active` when an unrevoked credential row exists for the peer or its principal, `revoked` when only revoked rows exist, `none` otherwise; `Routes` from `routes` owned by the peer or its principal, `Host` from `url.Parse`, kind as stored; `PresenceAt`, `ReachVia` as stored.
- `LoopCuts`: audit rows with action `loop_cut` in the window; `rate_limited` rows are not cuts; `Recent` newest 50.
- `DeliveryHealth.Blocked` implements §16.4: per (recipient, thread) the head delivery that `ClaimDue` would serialise on (state `queued` or `pushed`, not `hold`, not held, route set, no earlier such delivery) with `attempt ≥ 1`; `Waiting` counts later such deliveries. `Failed`: state `failed`, updated within 7 days, newest first, at most 100.

- [ ] **Step 1: Write the failing tests**

```go
func TestPeersOperatorView(t *testing.T) { /* route host only, no path or whsec anywhere in the JSON; credential active; rotated-then-revoked → revoked; disabled peer present with disabled:true; push-only peer keeps presence unknown */ }
func TestLoopCutsWindow(t *testing.T) { /* three reasons counted by_reason; row older than hours excluded; rate_limited excluded */ }
func TestBlockedThread(t *testing.T) { /* receiver 503: first delivery retrying plus 2 queued → one entry waiting 2, attempt ≥ 1; a second thread to that peer is not blocked by it; on 200 the entry clears */ }
func TestFailedWindow(t *testing.T) { /* 409 → listed; FakeClock +8 d → absent */ }
```

- [ ] **Step 2–5:** Backend recipe → commit `feat: operator peer, loop-cut and delivery-health reads`

---

### Task 6: Subscription health

**Depends:** 1

**Files:**
- Modify: `internal/opview/subs.go`, `internal/httpapi/admin_subscriptions.go` (handler uses `Reader.Subscriptions`), `internal/events/store.go` and `internal/mcpserver/events.go` (export the margin below)
- Create: `internal/opview/subs_test.go`

**Interfaces:**
- Consumes: `events.NewStore(h).ListHealth` and `GET /v1/admin/subscriptions`. No migration: `last_refresh_at` already exists. Its existing JSON fields stay byte-compatible (`vecnet subscriptions list` and `docs/connectors/dots.md` depend on them) and this task only adds `health`, `health_note`, `refresh_before`. `last_refresh_at` stays as stored: null until the first re-subscribe, which the clients show as `never`.
- `refresh_before` is `expires_at − 10 min`. Move `mcpserver.refreshMargin` to exported `events.RefreshMargin` and use it in both places, so the snapshot and `events/subscribe` cannot disagree.
- `Health`, first match wins at `hub.Now()`: `paused_reason` `token_expired` → `paused_token`; `delivery_failed` → `paused_failed`; `operator` → `paused_operator`; any other non-null reason, including `unauthorized` and `credential_rotated` → `paused_auth`, so a reason added later can never read as healthy; `verified_at` null → `unverified`; `expires_at ≤ now` → `expired`; `now ≥ refresh_before` → `late`; `attempts > 0` or last status not 2xx → `failing`; else `ok`. Null `expires_at` skips the two time rules.
- `HealthNote` is fixed copy per `paused_reason` or health, matching the operator actions in `docs/connectors/dots.md`:
  - `token_expired`: `"Waiting for ChatGPT to re-subscribe with a fresh token. Nothing is lost; delivery resumes from the cursor."`
  - `delivery_failed`: `"One event used all 8 attempts. Fix the callback, then resume. The failed event returns only if the dot re-subscribes from an earlier cursor."`
  - `unauthorized`: `"The peer was disabled or moved, or its login was replaced or revoked. Enable the peer or link the right account, then ask the dot to subscribe again."`
  - `credential_rotated`: `"The bearer that subscribed was rotated. The holder of the new bearer must subscribe again."`
  - `operator`: `"You paused this subscription. A re-subscribe does not lift it."`
  - any other reason: `"Paused: <reason>."`
  - `unverified`, `expired`, `late`, `failing`: one line each, naming what the operator should expect next.
- `Attention`: one entry per subscription in `paused_token`, `paused_failed`, `paused_auth`, `paused_operator`, `expired`, `late` or `failing`; `Action` is `"resume"` for `paused_failed` and `paused_operator` only; `Summary` is `<peer name> · <note>`; worst health first, with `paused_operator` last because it is the operator's own choice.

- [ ] **Step 1: Write the failing tests**

```go
func TestHealthTable(t *testing.T) { /* one row per value, FakeClock boundaries: refresh_before exactly → late; expires_at exactly → expired; paused wins over expired; all five stored reasons plus an invented one map as stated, and none reads ok */ }
func TestRefreshBeforeMatchesSubscribe(t *testing.T) { /* refresh_before equals expires_at minus events.RefreshMargin, the value events/subscribe returns */ }
func TestLastRefreshOnResubscribe(t *testing.T) { /* first Upsert → last_refresh_at null; Advance 1 h, Upsert → set; a delivery does not move it */ }
func TestAdminSubscriptionsAdditive(t *testing.T) { /* connectors fields unchanged, host only, no secret; new fields present; body still a bare array */ }
func TestAttentionActions(t *testing.T) { /* paused_failed, paused_operator → action resume; paused_token, paused_auth → action ""; ok absent; paused_operator sorts last (Review Focus 5) */ }
func TestInboxCarriesAttention(t *testing.T) { /* GET /v1/admin/inbox lists it */ }
```

- [ ] **Step 2–5:** Backend recipe, adding `./internal/events/ ./internal/mcpserver/` to the test command → commit `feat: subscription health for the operator`

---

### Task 7: Web shell, API client, store

**Depends:** 1, 2

**Files:**
- Create in `internal/ui/static/`: `api.js`, `store.js`, `format.js`, `dom.js`, `approvals.js`, `base.css`, and the stub pairs `inbox.js`/`inbox.css`, `thread.js`/`thread.css`, `thread-aside.js`/`aside.css`, `matrix.js`/`matrix.css`, `dash.js`/`dash.css`, `subs.js`/`subs.css`, `presence.js`/`presence.css`, `compose.js`/`compose.css`
- Modify: `app.js` (shell only), `index.html` (`<script type="module" src="app.js">`, one `<link>` per stylesheet); Delete: `app.css`
- Modify: `internal/httpapi/ui_test.go` (`uiMaxBytes = 256 << 10`, summed over every file; checks below)
- Test: `internal/ui/jstest/api.test.mjs`, `store.test.mjs`, `format.test.mjs`, `approvals.test.mjs`; `scripts/ui/shell.mjs`

**Interfaces:**
- Consumes: `internal/opapi/testdata/tags.json`, Task 2 harness.
- Produces (all later web tasks rely on these):
  ```js
  // api.js
  export class APIError extends Error { status; code; details }          // status 0 = network
  export function createApi({ secret, fetch, onUnauthorized }) // returns:
  //  peers() adminPeers() inbox({limit,before,unread}) myThreads({limit}) threads({participant,limit,cursor}) thread(id) messages(id,{after,limit})
  //  matrix(id,{after,limit}) readThread(id) events({after,wait}) ack(cursor) send(body) presence(state,atIso) deadLetters() retryDeadLetter(dly)
  //  discardDeadLetter(dly) subscriptions() resumeSubscription(id) loopCuts(hours) deliveryHealth() audit({limit,before}) counters() blob(sha) → Blob
  // store.js
  export function tagsFor(ev)                                            // mirrors opapi.Tags
  export function createStore({ api, now, sleep })                       // returns:
  //  resource(tag, loader) → { get(), error(), loading(), subscribe(fn) → off, reload() }
  //  invalidate(...tags), invalidateAll(), settled() → Promise, hubNow() → ms, setHubNow(iso), pump(signal), conn() → 'connecting'|'live'|'offline'|'stopped'
  // approvals.js (pure)
  export function countdown(expiresAtIso, hubNowMs) // → { text: 'mm:ss' or 'h:mm:ss', urgent: < 60 s, expired }
  export function answerBody(approval, optionIndex) // → SendRequest: idempotency_key `ui-apr-<approval_id>-<optionIndex>`, thread_id, in_reply_to message_id, kind approval_response, priority wake, content.text the option, approval_response {approval_id, decision}
  // dom.js: h(tag, attrs, ...kids), plus avatar, chip, toast, confirmButton(label, confirmLabel, onConfirm, ms = 3000)
  // format.js (pure): when, ago, bytes, short, title(thread, me, nameOf), quotaLabel(quota)
  // every screen module:
  export function mount(root, ctx) { /* returns unmount() */ }           // ctx = { api, store, me, nameOf, kindOf, nav(hash), toast(msg, bad) }
  export function mountAside(root, ctx) { /* returns unmount() */ }      // inbox.js and thread-aside.js
  // stub exports later tasks fill in, already imported where needed:
  //  matrix.js openMatrix(threadId, ctx); subs.js renderSection(el, ctx), renderAttention(el, items, ctx);
  //  presence.js renderNav(el, ctx), renderAside(el, me, ctx), startHeartbeat(ctx) → stop
  ```
- Routes: `#inbox`, `#thread/<id>`, `#matrix/<id>` (the thread with its matrix open, so Tasks 10 and 11 stay independent), `#dash`, `#new`. `body[data-route]` carries the route for CSS. Nav matches `shots/a-inbox.png`: brand, Inbox with unread count, New message, Dashboard, Threads list (the operator's own threads from `GET /v1/threads`), You (from `presence.renderNav`), connection state, Sign out. The three-column grid and every existing rule of `app.css` move into `base.css` or the matching screen stylesheet.
- `thread.js` receives the existing `renderThread`, `message`, `structured`, `composer` and `send` code, with reply still working. `inbox.js` is a placeholder that loads `api.inbox()`, renders its error state on 501, and already calls `subs.renderAttention` and `presence.renderAside`; `dash.js` is a placeholder that already calls `subs.renderSection`. Tasks 13 and 14 can therefore finish first and test through these slots. The bare inbox is an accepted temporary regression on the build branch only.
- **Pump rule (Review Focus 4):** the first `GET /v1/events` has `wait=0` and **no `after`**, so the hub starts at the acked cursor; `e0.0` is never sent. After that `after=<next_cursor>&wait=25`. A non-empty batch runs `invalidate(tagsFor…)`, awaits `settled()`, then acks `next_cursor`. 409 `cursor_purged`: `after = details.oldest_cursor`, `invalidateAll()`. 401: stop, sign out. 403 or 204: stop, conn `stopped`. Network or 5xx: conn `offline`, backoff 1 s doubling to 30 s. Two invalidates in one tick load a resource once. `hubNow()` is client time plus the offset from the last `setHubNow`.
- The `vecnet.ui.read` localStorage key is dropped and removed if found.

- [ ] **Step 1: Write the failing tests**

```js
// api.test.mjs
test('sends bearer and accepts JSON; 204 → null')
test('error envelope → APIError with status, code, details; non-JSON body → code http_<status>')
test('401 calls onUnauthorized exactly once; network failure → status 0')
// store.test.mjs
test('tagsFor matches internal/opapi/testdata/tags.json row for row')
test('first events call has no after and wait=0; never sends e0.0')
test('batch: invalidates, awaits reload, then acks; ack never precedes settled')
test('409 resumes at oldest_cursor and invalidates all')
test('401 stops the loop; 403 and 204 stop with conn stopped; no retry')
test('5xx backs off 1,2,4…30 s with fake sleep; success resets')
test('two invalidates in one tick call the loader once; failed load keeps last value and sets error')
test('hubNow applies the setHubNow offset with a client clock 5 min off')
// approvals.test.mjs
test('countdown mm:ss, hours form, urgent under 60 s, expired at 0, independent of client clock')
test('answerBody is deterministic per approval and option and matches the idempotency key pattern')
```
```go
func TestUIStaticNoHTMLSinks(t *testing.T) { /* no static .js contains innerHTML, outerHTML, insertAdjacentHTML, document.write, eval(, new Function */ }
func TestUIStaticNoInline(t *testing.T) { /* index.html has no <style>, no inline <script>, no style= attribute; script is type=module with src; every <link>/<script> target is served with the right type */ }
func TestUIUnderSizeCap(t *testing.T) { /* sum of files ≤ 256 KiB */ }
```

**Shots** (`scripts/ui/shell.mjs`; base `shots/a-inbox.png` frame): `07-shell-initial` login card; `07-shell-progress` signed in with `/v1/peers` held, "connecting"; `07-shell-success` shell with nav and thread list, reply delivered in `#thread/<hark>`; `07-shell-error` bad secret rejected on the login card. Also asserts the page's module graph loads with zero CSP violations.

- [ ] **Step 2–6:** Web recipe → commit `feat: operator web shell, API client and store`

---

### Task 8: Web inbox

**Depends:** 3, 7

**Files:**
- Modify: `internal/ui/static/inbox.js`, `inbox.css`
- Create: `internal/ui/static/inbox-model.js`, `internal/ui/jstest/inbox.test.mjs`, `scripts/ui/inbox.mjs`

**Interfaces:**
- Consumes: `api.inbox`, `api.send`, `api.retryDeadLetter`, `api.discardDeadLetter`, `countdown`, `answerBody`, `confirmButton`, `presence.renderAside`, `subs.renderAttention` (stubs may render nothing).
- Page per `shots/a-inbox.png`: heading, lede, then **Needs a decision**, **Failed deliveries**, the attention slot, **Woke you**, **Held**; right aside "Your presence" and "On this page". Inbox and chat stay separate: every item links to `#thread/<id>`.
- `inbox-model.js` (pure): `sections(inbox)`, `deadLetterLine(dl)` (`503 · retries exhausted · attempts 8/8 · <time> · dly_…`), `heldWhy(delivery)`.
- Rules: a card's countdown ticks each second from `store.hubNow()`; at zero its buttons disable, it reads "expired" and `invalidate('inbox')` runs. Clicking an option disables every option in that card until the POST settles, so a double click sends once. 422 `approval_closed`: toast "Already closed", no retry, reload. Other errors re-enable and toast `code: message`. Discard uses `confirmButton` (two-step, 3 s); retry is immediate. Deliveries page with "Load older" through `next_before`. A failed snapshot keeps the last data under an error banner with a Retry button.

- [ ] **Step 1: Write the failing tests**

```js
test('sections keeps order and hides empty ones; approvals first')
test('deadLetterLine covers 503, 410 and null status')
test('heldWhy explains hold vs wake_idle')
```
Named assertions in `scripts/ui/inbox.mjs`:
`double-click sends one POST` (via `countRequests`); `answered elsewhere, then click → closed toast, one POST, no retry` (`hold` the events call so the card stays stale, answer with `asPeer('operator', …)`, then click; Review Focus 2); `advance past expiry → buttons disabled, card leaves after reload`; `retry moves a dead letter out; discard needs a second click`; `hostile body renders as literal text and no dialog fires` (Review Focus 1).

**Shots:** `08-inbox-initial` seeded page, compare `a-inbox.png`; `08-inbox-progress` option clicked with the POST held; `08-inbox-success` card gone, toast, count dropped; `08-inbox-error` the `approval_closed` toast over the reloaded page.

- [ ] **Step 2–6:** Web recipe → commit `feat: operator inbox with approvals and dead-letter actions`

---

### Task 9: Web thread view

**Depends:** 4, 7

**Files:**
- Modify: `internal/ui/static/thread.js`, `thread.css`
- Create: `internal/ui/static/thread-model.js`, `internal/ui/jstest/thread.test.mjs`, `scripts/ui/thread.mjs`

**Interfaces:**
- Consumes: `api.thread`, `api.messages`, `api.matrix`, `api.readThread`, `api.send`, `api.blob`, `countdown`, `answerBody`.
- Vertical chat per `shots/a-chat.png`, but the reading column fills the space between nav and side panel up to a 980 px measure rather than A's 740 px column (Max: better use of horizontal space).
- `thread-model.js` (pure): `items(messages, matrix, openApprovals)` → render items with `{ message, parent, receipts, spill, approval, system }`; `system` true for hub-authored messages, which render as one quiet line (`approval expired`).
- Rules: reply quote jumps to and flashes its parent; a spilled message shows the preview, a dashed `message.md` chip and "Show full text", which fetches the blob (only `text/*`, at most 1 MiB) into a `<pre>` by `textContent`; job and approval boxes as today; an open approval addressed to the operator gets inline answer buttons with the Task 8 rules, otherwise a read-only card; receipts are quiet per-recipient markers from `matrix.cells[].display`, including messages the operator did not send. The composer keeps the priority segment, byte counter, spill hint, Enter to send and the stable `pending` idempotency key; if the operator is not a participant it states that sending joins them to the thread. `api.readThread` runs on open and on new deliveries while the tab is visible. The view stays pinned to the bottom unless scrolled up.
- Send errors: `thread_budget_exhausted` shows a composer banner naming the budget; `participants_locked`, `scope_denied`, `rate_limited` (with `Retry-After`) show `code: message`; text is retained on every failure.

- [ ] **Step 1: Write the failing tests**

```js
test('items links parents, including a parent outside the loaded page')
test('hub-authored approval_response is a system item')
test('spill detected from metadata vecnet.spilled and the attachment')
test('receipts map cell display per recipient; no cell for the sender')
test('approval is actionable only when in openApprovals with addressed_to_me')
```
Named assertions: `reply link jumps to parent`; `Show full text renders the spilled body literally`; `send keeps its idempotency key across a held retry`; `opening a thread clears its unread count via one POST`; `live delivery from asPeer appears without reload`.

**Shots:** `09-thread-initial` `hark` thread, compare `a-chat.png`; `09-thread-progress` send held, button disabled; `09-thread-success` sent, receipt markers moved by `asPeer`; `09-thread-error` `quota` thread budget-exhausted banner with text retained.

- [ ] **Step 2–6:** Web recipe → commit `feat: operator thread view with receipts, spill and inline approvals`

---

### Task 10: Web thread side panel

**Depends:** 4, 7

**Files:**
- Modify: `internal/ui/static/thread-aside.js`, `aside.css`
- Create: `internal/ui/static/aside-model.js`, `internal/ui/jstest/aside.test.mjs`, `scripts/ui/aside.mjs`

**Interfaces:**
- Consumes: `api.thread`, `api.adminPeers`, `api.blob`, `ctx.nav('#matrix/<id>')`.
- Layout borrows B's horizontal use (`shots/b-chat.png`) inside A's frame: on `body[data-route=thread]` the aside widens to 380 px at ≥ 1280 px wide; below 1100 px it collapses behind a toggle. Sections in order: **Participants** (name, kind, presence dot and reachability ring as two signals), **Files** (the thread's files, spilled ones dashed, size and sender, click to download), **Thread** (id, group, budget bar `n of 500 left`, history access, locked, cut), and a **Delivery matrix** button.
- Download fetches `api.blob`, then a temporary `a[download]` with an object URL revoked afterwards; the bearer never enters a URL. A purged file is listed, disabled, labelled "purged". 410 `blob_purged` from a stale list toasts and reloads.
- `aside-model.js` (pure): `presenceLabel(presence)`, `reachLabel(reachability)`, `budgetBar(row, threadBudget)`, `fileRows(files)`.

- [ ] **Step 1: Write the failing tests**

```js
test('unknown presence with reachable reads unknown, never offline')
test('budget null reads unlimited; 0 reads exhausted')
test('fileRows marks spilled and purged')
```
Named assertions: `file download fires a browser download and no request carries the secret in a URL`; `side panel is ≥ 360 px at 1440 wide and the chat column widens`; `at 1000 wide the panel is behind its toggle`; `matrix button navigates to #matrix/<id>`.

**Shots:** `10-aside-initial` panel beside `hark`, compare `b-chat.png` right column; `10-aside-progress` file download held; `10-aside-success` download finished, toast; `10-aside-error` 410 purged state.

- [ ] **Step 2–6:** Web recipe → commit `feat: thread side panel with participants, files and budget`

---

### Task 11: Web delivery matrix

**Depends:** 4, 7

**Files:**
- Modify: `internal/ui/static/matrix.js`, `matrix.css`
- Create: `internal/ui/static/matrix-model.js`, `internal/ui/jstest/matrix.test.mjs`, `scripts/ui/matrix.mjs`

**Interfaces:**
- Consumes: `api.matrix`, `store.resource('thread:<id>', …)`.
- Produces: `openMatrix(threadId, ctx)`: a native `<dialog>` (Esc closes, focus returns), an extra view Max opens, never always-on. Grid of messages × recipients, sticky header and first column, cells coloured by `display` per `shots/b-chat.png`, the sender's own cell hatched, legend below. Column count through `el.style.setProperty('--cols', n)`. Clicking a cell shows delivery id, attempt, last status and updated time. "Load more" pages with `next_after`. It refreshes live on the thread's tag while open.
- `matrix-model.js` (pure): `grid(matrix)` → rows of cells aligned to `recipients`, gaps filled with `{ state: 'none' }` or `{ state: 'self' }`.

- [ ] **Step 1: Write the failing tests**

```js
test('grid aligns cells to recipients; missing → none; sender → self')
test('grid keeps zero-cell hub messages as a row')
```
Named assertions: `Esc closes and focus returns to the button`; `64 recipients scroll inside the dialog with no page overflow` (`--scale`); `a receipt from asPeer recolours its cell while open`.

**Shots:** `11-matrix-initial` open on `hark`, compare `b-chat.png`; `11-matrix-progress` load held, skeleton; `11-matrix-success` a cell recoloured live; `11-matrix-error` load failed with Retry.

- [ ] **Step 2–6:** Web recipe → commit `feat: delivery matrix as an openable view`

---

### Task 12: Web dashboard (provisional)

**Depends:** 4, 5, 7

**Files:**
- Modify: `internal/ui/static/dash.js`, `dash.css`
- Create: `internal/ui/static/dash-model.js`, `internal/ui/jstest/dash.test.mjs`, `scripts/ui/dash.mjs`

**Interfaces:**
- Consumes: `api.adminPeers`, `api.threads`, `api.deliveryHealth`, `api.deadLetters` (the existing `GET /v1/admin/dead-letters`, so the dashboard does not need the inbox snapshot), `api.loopCuts(24)`, `api.counters`, `api.audit`, `subs.renderSection`.
- Max will redo this later, so it stays plain: A's stacked tables per `shots/a-dash.png`, no tiles and no charts, one counter line at the top. Sections each load and fail independently: **Peers** (kind, presence + age, reachability + via + age, credential, route hosts, quota, location, disabled dimmed), **Threads** (all threads, links, participants, messages, budget, last activity, cut), **Delivery health** (`Blocked on delivery N`, §16.4; failed deliveries, which have no retry by protocol), **Dead letters** (read-only, "Act on these in Inbox"), **Loop control** (24 h total and by reason), **Subscriptions** (slot), **Audit tail** (50, Load older). Reloads on the `peers` tag and every 30 s while mounted.
- `dash-model.js` (pure): `peerRows(rows)`, `blockedLine(b)`, `cutSummary(cuts)`; `format.quotaLabel` renders a `*_pct` key as `61%`, an empty quota as `none`, anything else as compact `k=v`.

- [ ] **Step 1: Write the failing tests**

```js
test('peerRows: dot reachable + unknown presence; disabled flagged; source shows n/a presence')
test('blockedLine names recipient, delivery and waiting count')
test('cutSummary orders reasons by count')
```
Named assertions: `no POST, PUT, PATCH or DELETE leaves the page in a full dashboard session` (`countRequests`, excluding `/v1/ack` and the pump, which the shell owns); `one failing section shows its own error while the others render`; `thread rows link to #thread/<id>`.

**Shots:** `12-dash-initial` first paint with section headers; `12-dash-progress` reads held; `12-dash-success` loaded, compare `a-dash.png`; `12-dash-error` Delivery health failed, the rest fine.

- [ ] **Step 2–6:** Web recipe → commit `feat: provisional read-only operator dashboard`

---

### Task 13: Web subscription health

**Depends:** 3, 6, 7

**Files:**
- Modify: `internal/ui/static/subs.js`, `subs.css`
- Create: `internal/ui/static/subs-model.js`, `internal/ui/jstest/subs.test.mjs`, `scripts/ui/subs.mjs`

**Interfaces:**
- Consumes: `api.subscriptions`, `api.resumeSubscription`, `opapi.Subscription` and `opapi.Attention` JSON; Task 3 puts `attention` into the inbox snapshot, Task 7's slots call this module.
- Produces: `renderSection(el, ctx)` for the dashboard: table of peer, host, health chip, expires in (hub clock), last refresh, last delivery status and time, attempts, cursor, with `health_note` as the row's tooltip and visible under any non-ok row; no actions. `renderAttention(el, items, ctx)` for the inbox: section "Subscriptions" listing each item's summary, and a Resume button only when `action === "resume"`. After resume, `invalidate('inbox')`.
- `subs-model.js` (pure): `chip(health)` → `{ label, tone }` for all nine health values, `expiresIn(iso, hubNowMs)`.

- [ ] **Step 1: Write the failing tests**

```js
test('chip covers every opapi health value; unknown value falls back to neutral')
test('expiresIn uses hub clock; past → "expired 3m ago"')
```
Named assertions: `paused_token and paused_auth rows have no Resume button anywhere` (Review Focus 5); `paused_failed Resume clears it from the inbox`; `paused_operator Resume clears it from the inbox`; `dashboard section has no buttons`.

**Shots:** `13-subs-initial` dashboard section with all six seeded health states; `13-subs-progress` Resume held; `13-subs-success` item gone; `13-subs-error` resume failed, button re-enabled.

- [ ] **Step 2–6:** Web recipe → commit `feat: dots subscription health in dashboard and inbox`

---

### Task 14: Web presence toggle

**Depends:** 3, 7

**Files:**
- Modify: `internal/ui/static/presence.js`, `presence.css`
- Create: `internal/ui/static/presence-model.js`, `internal/ui/jstest/presence.test.mjs`, `scripts/ui/presence.mjs`

**Interfaces:**
- Consumes: `api.presence`, `inbox.me`.
- Produces: `renderNav(el, ctx)` (dot, `max is working`, one-tap idle/working toggle), `renderAside(el, me, ctx)`, `startHeartbeat(ctx)`.
- **Truthful copy:** §5.2 says `wake_idle` and `hold` land in the operator's inbox and only `wake` fires notifications, so presence does not gate the operator's own interruptions today. The aside reads: "Peers see this in the directory. Only `wake` messages fire your notifications." Do not reuse the prototype's "wake_idle messages wait while you work". See Decisions for Max.
- Heartbeat: every 20 min while a tab is open, fetch `me`; if its state is `idle`, `working` or `blocked`, re-post that same state, so presence survives the 1 h TTL. It re-posts what the hub says rather than a local choice, so two clients cannot fight. `unknown` or `offline` is never re-asserted. `at` comes from `store.hubNow()`. The displayed state changes only after the 204 and a reload.

- [ ] **Step 1: Write the failing tests**

```js
test('heartbeatAction: working → repost working; unknown → nothing; offline → nothing')
test('toggleTarget: idle ↔ working; blocked or unknown → working')
```
Named assertions: `toggle posts once and the nav updates after 204`; `a second tab's change wins: the first tab's heartbeat re-posts the hub's state` (Review Focus 4); `failure leaves the old state with a toast`.

**Shots:** `14-presence-initial` working; `14-presence-progress` POST held; `14-presence-success` idle; `14-presence-error` 500, unchanged.

- [ ] **Step 2–6:** Web recipe → commit `feat: one-tap operator presence with heartbeat`

---

### Task 15: Web new message

**Depends:** 7

**Files:**
- Modify: `internal/ui/static/compose.js`, `compose.css`
- Create: `internal/ui/static/compose-model.js`, `internal/ui/jstest/compose.test.mjs`, `scripts/ui/compose.mjs`

**Interfaces:**
- Consumes: `api.peers`, `api.send`.
- Route `#new`: pick one or more peers (searchable by name, excludes the operator and kind `source`, addressed by id, never by name, §14.4), optional title, priority segment, text with the same byte counter and stable idempotency key as the composer, and an "Advanced" fold for `history_access` and `locked_participants`. Success navigates to `#thread/<thread_id>`. Errors keep the form: `scope_denied` names the recipient from `details.recipient`, `rate_limited` shows the retry time.
- `compose-model.js` (pure): `eligible(peers)`, `buildSend(form, key)`.

- [ ] **Step 1: Write the failing tests**

```js
test('eligible drops operator and source')
test('buildSend uses ids, omits empty title and default advanced fields, requires priority')
```
Named assertions: `send opens the new thread`; `empty text or no recipient keeps Send disabled`.

**Shots:** `15-compose-initial` empty form; `15-compose-progress` send held; `15-compose-success` new thread open; `15-compose-error` `scope_denied` with the form intact.

- [ ] **Step 2–6:** Web recipe → commit `feat: start a new thread from the web UI`

---

### Task 16: Go operator client

**Depends:** 1

**Files:**
- Create: `internal/opclient/client.go`, `internal/opclient/tail.go`
- Test: `internal/opclient/client_test.go`, `internal/opclient/tail_test.go`

**Interfaces:**
- Consumes: `opapi` types; imports `opapi`, `envelope`, stdlib only (`testhub` in tests only).
- Produces:
  ```go
  package opclient
  type Error struct{ Status int; Code, Message string; Details map[string]any }
  type Client struct{ /* base, secret, hc */ }
  func New(base, secret string) *Client
  func (c *Client) Inbox(ctx, q opapi.InboxQuery) (opapi.Inbox, error)
  func (c *Client) ReadThread(ctx, id string) (opapi.ReadResult, error)
  func (c *Client) Threads(ctx, q opapi.ThreadQuery) (opapi.ThreadPage, error)
  func (c *Client) Thread(ctx, id string) (opapi.ThreadDetail, error)
  func (c *Client) History(ctx, id, after string, limit int) (opapi.History, error)
  func (c *Client) Matrix(ctx, id, after string, limit int) (opapi.Matrix, error)
  func (c *Client) Peers(ctx) ([]opapi.PeerRow, error)
  func (c *Client) LoopCuts(ctx, hours int) (opapi.LoopCuts, error)
  func (c *Client) DeliveryHealth(ctx) (opapi.DeliveryHealth, error)
  func (c *Client) Subscriptions(ctx) ([]opapi.Subscription, error)
  func (c *Client) ResumeSubscription(ctx, id string) error
  func (c *Client) Audit(ctx, limit int, before string) (opapi.AuditPage, error)
  func (c *Client) Counters(ctx) (map[string]int, error)
  func (c *Client) RetryDeadLetter(ctx, dly string) error
  func (c *Client) DiscardDeadLetter(ctx, dly string) error
  func (c *Client) Send(ctx, r envelope.SendRequest) (opapi.SendResult, error) // sets X-Vecnet-Ingress: operator_ui
  func (c *Client) Answer(ctx, a opapi.OpenApproval, option int) error          // key `tui-apr-<approval_id>-<option>`, same body shape as approvals.js
  func (c *Client) SetPresence(ctx, state string, at time.Time) error
  func (c *Client) Ack(ctx, cursor string) error
  func (c *Client) Blob(ctx, sha string, max int64) ([]byte, string, error)     // bytes and media type; more than max → error
  // Tail follows the Task 7 pump rule exactly. handle returns after its reload has landed; Tail then acks.
  type Batch struct{ Events []envelope.Event; Cursor string; Reset bool /* set after a 409 */ }
  var ErrUnauthorized, ErrForbidden, ErrDisabled error                          // terminal
  func (c *Client) Tail(ctx, handle func(Batch) error, status func(string)) error // status: "connecting"|"live"|"offline"
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestReadsDecode(t *testing.T) { /* stub server serves canned JSON for each new read (Tasks 3–6 need not be merged): every field of each opapi type round-trips */ }
func TestExistingEndpointsAgainstTestHub(t *testing.T) { /* Send, History, Ack, SetPresence, Audit, Counters, Blob, RetryDeadLetter, DiscardDeadLetter on a real testhub */ }
func TestErrorEnvelope(t *testing.T) { /* 422 → *Error with details; non-JSON 502 → *Error{Status:502} */ }
func TestAnswerIdempotent(t *testing.T) { /* same call twice → one message; closed approval → *Error{Code:"approval_closed"} */ }
func TestTailFirstCallHasNoAfter(t *testing.T) { /* stub server records queries: wait=0 no after; then after=<next>&wait=25; never e0.0 (Review Focus 4) */ }
func TestTailAcksAfterHandle(t *testing.T) { /* ack request arrives only after handle returned */ }
func TestTailPurgedCursor(t *testing.T) { /* 409 oldest_cursor → resumes there, Batch.Reset true */ }
func TestTailTerminals(t *testing.T) { /* 401 → ErrUnauthorized, 403 → ErrForbidden, 204 → ErrDisabled, no further request */ }
func TestTailBackoff(t *testing.T) { /* injected Sleep: 1,2,4…30 s, status offline; success resets; ctx cancel returns promptly */ }
```

- [ ] **Step 2–5:** FAIL → implement → `build2 go test -p 2 ./internal/opclient/` `ok`; `build2 go list -deps ./internal/opclient | grep -c 'internal/hub\|modernc.org/sqlite'` prints 0 → commit `feat: typed operator client and event tail`

---

### Task 17: TUI shell

**Depends:** 2, 16

**Files:**
- Create: `internal/tui/tui.go`, `model.go`, `keys.go`, `theme.go`, `layout.go`, `clean.go`, `data.go`, `screen_inbox.go`, `screen_chat.go`, `screen_dash.go`, `screen_subs.go`, `cmdline.go` (the last five are stubs), `internal/cli/tui.go`, `scripts/ui/tui-shell.mjs`
- Modify: `internal/cli/commands.go` (`case "tui"`, usage line), `go.mod`, `go.sum`, `docs/adr/0001-tech-stack.md` (one amendment line: Bubble Tea v2 and lipgloss v2 for the TUI; pure Go, `vecnet` only, not linked into the hub)
- Test: `internal/tui/shell_test.go`, `clean_test.go`, `fake_test.go`, `golden_test.go`

**Interfaces:**
- Consumes: `opclient.Client` through an interface.
- Produces (all later TUI tasks rely on these):
  ```go
  package tui
  // API is the subset of *opclient.Client the TUI calls; tests use a fake.
  type API interface { Inbox(…); ReadThread(…); Threads(…); Thread(…); History(…); Matrix(…); Peers(…); LoopCuts(…); DeliveryHealth(…); Subscriptions(…); ResumeSubscription(…); Audit(…); Counters(…); RetryDeadLetter(…); DiscardDeadLetter(…); Send(…); Answer(…); SetPresence(…); Blob(…); Tail(…) }
  type Options struct{ API API; Profile, Hub string; Now func() time.Time; In io.Reader; Out io.Writer }
  func Run(ctx context.Context, o Options) error                 // opclient.ErrForbidden on first load → "profile <x> is not the operator credential"
  type Screen interface { Title() string; Update(msg tea.Msg, m *Model) tea.Cmd; View(m *Model, w, h int) string; Hints() string }
  type Data struct{ Inbox opapi.Inbox; Threads opapi.ThreadPage; Peers []opapi.PeerRow; Subs []opapi.Subscription; Cuts opapi.LoopCuts; Health opapi.DeliveryHealth; Audit opapi.AuditPage; Counters map[string]int; Thread map[string]*ThreadState; Events []envelope.Event /* newest 200, for the raw log */ ; Errs map[string]error }
  type ThreadState struct{ Detail opapi.ThreadDetail; Messages []envelope.Message; Matrix opapi.Matrix; Loading bool }
  type Model struct{ API API; Data Data; Mode Mode; Tab int; W, H int; Conn string; CurrentThread string; Now func() time.Time; Commands map[string]Command /* … */ }
  type Mode int // ModeNormal, ModeInsert, ModeCommand
  type Command struct{ Help string; Run func(m *Model, args []string) tea.Cmd }
  func (m *Model) HubNow() time.Time                             // m.Now() plus the offset from the last snapshot's now
  func (m *Model) Reload(tags ...string) tea.Cmd                 // loads resources for tags, coalesced, updates Data
  func (m *Model) Toast(msg string, bad bool)
  func (m *Model) OpenThread(id string) tea.Cmd                  // switches to the chat tab, sets m.CurrentThread, loads it
  func Clean(s string) string                                    // single line
  func CleanBody(s string) string                                // keeps \n and \t
  func Pane(title string, w, h int, focused bool, body string) string
  // constructors in the stub files: newInboxScreen(), newChatScreen(), newDashScreen(), newSubsScreen(); cmdline.go: updateCommand(m, msg) tea.Cmd, viewHelp(m) string
  ```
- `Model` implements `tea.Model` of Bubble Tea v2: `Init() tea.Cmd`, `Update(tea.Msg) (tea.Model, tea.Cmd)`, `View() tea.View` with `AltScreen = true`. Keys arrive as `tea.KeyPressMsg`.
- Layout per `shots/c-inbox.png`: top bar `vecnet | 1:inbox 2:chat 3:dash 4:subs`, right side hub host, connection state, `apr n`, `dlq n`, `cuts/24h n`, clock; screen body; status line with mode chip, the screen's hints, `max@vecnet · working · prio wake`; command line. Keys: `1`–`4` tabs, `q` quit (with confirm when in Insert), `:` command mode, `?` help, `ctrl+l` reload all. Below 80×24 the body is `terminal too small: need 80x24`.
- `Tail` runs in a command; a batch feeds `opapi.Tags` to `Reload`, then the handler returns so the tail acks. 401 stops the tail, the status shows `credential rejected`, and last data stays visible; offline shows `offline, retrying`.
- `Clean` removes ESC sequences (CSI, OSC, DCS, APC), other C0 and C1 controls, DEL, invalid UTF-8 and bidi controls U+202A–U+202E and U+2066–U+2069. Every peer-supplied string passes through `Clean`/`CleanBody` before reaching lipgloss.
- `vecnet tui` reads the `--net` profile as other commands do. A non-terminal stdout is a usage error (exit 1).
- Goldens: `renderAt(t, m, w, h)` strips ANSI and compares with `internal/tui/testdata/<name>.golden`; `UPDATE_GOLDEN=1` rewrites.

- [ ] **Step 1: Write the failing tests**

```go
func TestClean(t *testing.T) { /* table: OSC 52, OSC 8, CSI 2J, DCS, APC, 0x9b, BEL, BS, CR, U+202E, U+2066, invalid UTF-8 → inert; CleanBody keeps \n,\t; plain and CJK untouched (Review Focus 1) */ }
func TestTabsResizeTooSmall(t *testing.T) { /* 1–4 switch, golden at 140x42 and 80x24; 79x23 → too-small line */ }
func TestStatusConn(t *testing.T) { /* connecting, live, offline retrying, credential rejected render */ }
func TestTagReloadCoalesced(t *testing.T) { /* batch of 3 delivery events → Inbox loaded once; presence event → Peers only */ }
func TestHubNowOffset(t *testing.T) { /* client clock 5 min off; HubNow tracks the snapshot */ }
func TestForbiddenFirstLoad(t *testing.T) { /* fake 403 → Run returns the profile error */ }
func TestProgramQuits(t *testing.T) { /* tea.NewProgram with pipes, WithoutSignals, WithWindowSize(140,42): q exits within 2 s */ }
func TestCmdTUINotATerminal(t *testing.T) { /* in internal/cli: non-tty stdout → exit 1 usage error */ }
```

**Shots** (`scripts/ui/tui-shell.mjs`, real binary in tmux): `17-tui-initial` connecting; `17-tui-progress` first load under `stall('3s')`; `17-tui-success` live with all tab labels; `17-tui-error` demo hub killed, `offline, retrying`.

- [ ] **Step 2–6:** TUI recipe; also `CGO_ENABLED=0 build2 go build -p 2 -o bin/ ./cmd/...`, `just isa-check` prints 0 for both, and `build2 go list -deps ./cmd/vecnet | grep -c 'modernc.org/sqlite'` prints 0 → commit `feat: vecnet tui shell`

---

### Task 18: TUI inbox and event log

**Depends:** 3, 17

**Files:**
- Modify: `internal/tui/screen_inbox.go`
- Create: `internal/tui/screen_inbox_test.go`, `internal/tui/testdata/inbox-*.golden`, `scripts/ui/tui-inbox.mjs`

**Interfaces:**
- Consumes: `Screen`, `Data.Inbox`, `Data.Events`, `Model.HubNow`, `API.Answer`, `RetryDeadLetter`, `DiscardDeadLetter`, `ResumeSubscription`, `SetPresence`.
- Layout per `shots/c-inbox.png`: typed list (`APR`, `DLQ`, `SUB`, `WAKE`, `HOLD`) above the raw event log (`your event log`, receipts and presence as events), detail pane on the right. `SUB` rows come straight from `Inbox.Attention`.
- Keys: `j`/`k` move; `enter` opens the item's thread on the chat tab; `a` then a digit answers the selected approval, and any other key cancels; `r` retries a dead letter or resumes a `SUB` row whose `Action` is `resume`; `x` then `y` discards a dead letter; `p` toggles idle/working by the same `toggleTarget` rule as Task 14; opening a thread marks it read through `ReadThread`.
- Countdown from `HubNow`; expired approvals show `expired` and refuse the key locally. 422 `approval_closed` toasts and reloads, never retries. Heartbeat: same rule as Task 14, every 20 min.

- [ ] **Step 1: Write the failing tests**

```go
func TestInboxGolden(t *testing.T) { /* seeded fake at 140x42 */ }
func TestAnswerFlow(t *testing.T) { /* a,1 → one Answer(option 0); a,9 on a two-option card → nothing; a,esc → nothing */ }
func TestAnswerClosed(t *testing.T) { /* fake approval_closed → toast, reload, no second Answer (Review Focus 2) */ }
func TestCountdownHubClock(t *testing.T) { /* client clock 5 min off, same text; past expiry → key refused */ }
func TestDiscardNeedsConfirm(t *testing.T) { /* x alone → no call; x,y → one call */ }
func TestResumeOnlyWithAction(t *testing.T) { /* r on paused_token or paused_auth SUB → nothing and a hint; on paused_failed or paused_operator → Resume */ }
func TestInboxHostile(t *testing.T) { /* hostile sender, body and title: rendered output has no ESC except lipgloss SGR, no OSC, no U+202E (Review Focus 1) */ }
func TestPresenceToggle(t *testing.T) { /* p → SetPresence; status bar updates after reload */ }
```

**Shots:** `18-tui-inbox-initial` compare `c-inbox.png`; `18-tui-inbox-progress` `a` pressed, prompt showing options; `18-tui-inbox-success` approval answered, item gone; `18-tui-inbox-error` closed-approval toast. The script also asserts that after the `hostile` thread is on screen, `tmux display -p '#{pane_title}'` is unchanged and the pane's contents are intact.

- [ ] **Step 2–6:** TUI recipe → commit `feat: TUI inbox, actions and raw event log`

---

### Task 19: TUI chat

**Depends:** 4, 17

**Files:**
- Modify: `internal/tui/screen_chat.go`
- Create: `internal/tui/screen_chat_test.go`, `internal/tui/testdata/chat-*.golden`, `scripts/ui/tui-chat.mjs`

**Interfaces:**
- Consumes: `Screen`, `Data.Threads`, `Data.Thread`, `API.History`, `Thread`, `Matrix`, `Send`, `ReadThread`, `Answer`, `Blob`, `Model.Commands`.
- Layout per `shots/c-chat.png`: thread list, IRC-style log with priority glyphs (`!` wake, `~` wake_idle), compact per-recipient receipt glyphs, inline spill and job lines, and a who-and-files pane with presence and reachability separate.
- Keys: `[`/`]` previous and next thread, `j`/`k` select a message, `i` Insert mode, `enter` sends in Insert, `esc` leaves, `tab` cycles priority, `R` replies to the selected message, `a`+digit answers an inline approval, `e` expands a spilled message (`text/*`, ≤ 1 MiB, through `CleanBody`), `m` opens the delivery matrix as an overlay (an extra view, as on the web), `s` saves the selected file into the working directory using `filepath.Base` of the cleaned name, never overwriting (`name (1).ext`).
- Registers `:msg <peer-prefix> <text>` (new thread, unique prefix required, addressed by id) in `Model.Commands`.
- Errors as in Task 9; typed text is retained on failure.

- [ ] **Step 1: Write the failing tests**

```go
func TestChatGolden(t *testing.T) { /* hark thread at 140x42 and 80x24 */ }
func TestSendFlow(t *testing.T) { /* i, type, enter → one Send, ingress set, stable key on retry after a network error; budget error keeps the text */ }
func TestMatrixOverlay(t *testing.T) { /* m shows cells by display; esc closes; 64 recipients scroll */ }
func TestExpandSpill(t *testing.T) { /* binary media type refused; >1 MiB refused; body with escapes rendered clean */ }
func TestSaveFileNameSafe(t *testing.T) { /* name "../../x\x1b[31m" saves as a base name inside cwd; existing file not overwritten */ }
func TestMsgCommand(t *testing.T) { /* ambiguous prefix → error; unique → Send with peer id */ }
func TestChatHostile(t *testing.T) { /* hostile thread renders inert */ }
```

**Shots:** `19-tui-chat-initial` compare `c-chat.png`; `19-tui-chat-progress` Insert mode with text; `19-tui-chat-success` sent, receipt glyphs moved; `19-tui-chat-error` budget-exhausted toast with text retained.

- [ ] **Step 2–6:** TUI recipe → commit `feat: TUI chat, composer and delivery matrix overlay`

---

### Task 20: TUI dashboard

**Depends:** 4, 5, 17

**Files:**
- Modify: `internal/tui/screen_dash.go`
- Create: `internal/tui/screen_dash_test.go`, `internal/tui/testdata/dash-*.golden`, `scripts/ui/tui-dash.mjs`

**Interfaces:**
- Consumes: `Screen`, `Data.Peers`, `Threads`, `Health`, `Cuts`, `Audit`, `Counters`.
- Layout per `shots/c-dash.png`: htop-like peer table on top; threads, delivery health with loop cuts, and audit tail panes below. Read-only, same rule as the web. `j`/`k` move, `enter` on a peer narrows the thread pane to that peer via `ThreadQuery.Participant`, `enter` on a thread opens it on the chat tab. Quota uses the same rule as `quotaLabel`; a `*_pct` value draws a bar.

- [ ] **Step 1: Write the failing tests**

```go
func TestDashGolden(t *testing.T) { /* 140x42 and 80x24: narrow drops columns in a fixed order, never wraps */ }
func TestDashReadOnly(t *testing.T) { /* every key on this screen: the fake records zero write calls */ }
func TestDashPartialError(t *testing.T) { /* Data.Errs["health"] set: that pane shows the error, others render */ }
func TestUnknownPresenceNotOffline(t *testing.T) { /* dot row reads unknown + reachable */ }
```

**Shots:** `20-tui-dash-initial` loading panes; `20-tui-dash-progress` peer selected, threads narrowing; `20-tui-dash-success` compare `c-dash.png`; `20-tui-dash-error` Delivery health pane failed through `fail(...)`.

- [ ] **Step 2–6:** TUI recipe → commit `feat: TUI read-only dashboard`

---

### Task 21: TUI subscription health

**Depends:** 6, 17

**Files:**
- Modify: `internal/tui/screen_subs.go`
- Create: `internal/tui/screen_subs_test.go`, `internal/tui/testdata/subs-*.golden`, `scripts/ui/tui-subs.mjs`

**Interfaces:**
- Consumes: `Screen`, `Data.Subs`, `Model.HubNow`.
- Tab `4:subs`: table of peer, host, health, expires in, last refresh, last status, attempts, cursor; detail pane shows `health_note`. Read-only: resume lives on inbox `SUB` rows, matching the web's split. The top-bar gains `subs n` when any row is not `ok`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSubsGolden(t *testing.T) { /* all nine health values */ }
func TestSubsExpiresHubClock(t *testing.T) { /* client clock off; past → expired */ }
func TestSubsNoWrites(t *testing.T) { /* every key: zero write calls */ }
func TestTopBarSubsCount(t *testing.T) { /* two non-ok → subs 2; all ok → absent */ }
```

**Shots:** `21-tui-subs-initial` table; `21-tui-subs-progress` row selected with the reload running; `21-tui-subs-success` all states with note; `21-tui-subs-error` load failed through `fail(...)`.

- [ ] **Step 2–6:** TUI recipe → commit `feat: TUI subscription health`

---

### Task 22: TUI command line and help

**Depends:** 17

**Files:**
- Modify: `internal/tui/cmdline.go`
- Create: `internal/tui/cmdline_test.go`, `internal/tui/testdata/help.golden`, `scripts/ui/tui-cmd.mjs`

**Interfaces:**
- Consumes: `Model.Commands`, `Data`, `API.RetryDeadLetter`, `SetPresence`.
- `:` opens the command line with history (`up`/`down`) and tab completion over `Model.Commands`. Registers `:q`, `:refresh`, `:presence <idle|working|blocked|offline>`, `:retry all` (every listed dead letter, sequentially, reporting `n ok, m failed`), `:thread <n>` (nth in the chat list), `:tab <name>`. An unknown command shows `unknown command: x` and changes nothing. `?` shows a help overlay listing global keys, the current screen's `Hints()` and every registered command.

- [ ] **Step 1: Write the failing tests**

```go
func TestParse(t *testing.T) { /* quoting-free split; empty line; leading spaces */ }
func TestRetryAll(t *testing.T) { /* 3 dead letters, 1 fails → all 3 attempted, "2 ok, 1 failed" */ }
func TestCompletionAndHistory(t *testing.T) { /* tab, up, down */ }
func TestUnknownCommand(t *testing.T) { /* message only; no API call */ }
func TestHelpGolden(t *testing.T) { /* overlay at 140x42 */ }
```

**Shots:** `22-tui-cmd-initial` help overlay; `22-tui-cmd-progress` `:retry all` running; `22-tui-cmd-success` summary line; `22-tui-cmd-error` unknown command.

- [ ] **Step 2–6:** TUI recipe → commit `feat: TUI command line and help overlay`

---

### Task 23: Two-client journey, docs, final gate

**Depends:** 8, 9, 10, 11, 12, 13, 14, 15, 18, 19, 20, 21, 22, 24, 25

**Files:**
- Create: `docs/OPERATOR.md`, `docs/plans/shots/oui/README.md`, `scripts/ui/journey.mjs`
- Modify: `README.md` (operator UI section), `SPEC.md` §13 (states what shipped), `.scratch/vecnet/map.md` (Build Phase 3 section), `.scratch/vecnet/issues/18-operator-ui.md` (link the plan), `internal/opview/reader.go` (remove the `not_implemented` stub helper), `justfile` only if a recipe drifted

**Interfaces:**
- `docs/OPERATOR.md`: opening each client, sign-in, inbox sections and what each action does, presence meaning (Task 14 copy), keys for the TUI, subscription health table with the operator's response per value, what the dashboard cannot do and which CLI command does it.
- `shots/oui/README.md`: one line per PNG. No task may leave a state unshot.

- [ ] **Step 1: Write the journey**

`scripts/ui/journey.mjs` runs the web page and the real TUI against one demo hub (`withDemo` plus the tmux helper on the same data dir):
- approve in the TUI → the card leaves the web inbox without a reload;
- read a thread on the web → its unread count drops in the TUI;
- a peer sends `wake` → both clients show it;
- toggle presence on the web → the TUI status bar follows, and neither client's heartbeat flips it back;
- `fail('**/v1/events*', 401, 'unauthenticated')` on the web → back to sign-in after exactly one failed events call, and no retries (Review Focus 4);
- `advance('15m')` closes an approval on both clients with the hub's `expired` line in the thread.

- [ ] **Step 2: Run the full gate**

```bash
build2 go test -p 2 ./...
build2 node --test --test-concurrency=2 internal/ui/jstest/
build2 go vet ./...
gofmt -l .
CGO_ENABLED=0 build2 go build -p 2 -o bin/ ./cmd/...
just isa-check
just validate-protocol
build2 go list -deps ./cmd/vecnet | grep -c 'modernc.org/sqlite'
build2 go list -deps ./cmd/vecnet-hub | grep -c 'charm.land'
grep -rn 'not_implemented' internal/ docs/PROTOCOL.md
SHOTS_DIR=.scratch/vecnet/build-operator-ui/gate-shots just ui-shots
build2 node scripts/ui/journey.mjs
```
Expected: suites `ok`, `gofmt` silent, `isa-check` prints 0 for both, both `grep -c` print 0, the `not_implemented` grep prints nothing, every PNG listed in the README exists.

- [ ] **Step 3:** Commit `docs: operator guide, journey check, shot index`

---

### Task 24: Operator self-invalidation across clients

**Depends:** 7, 12, 18

Added 2026-11-02 after Task 23's journey failed. Hub fan-out reaches other peers, never the operator's own event log. So when one operator client answers an approval, reads a thread, sends, or changes presence, a second client signed in as the same operator never learns of it: approval answers go to the requester (`hub/send.go`), read receipts go to the sender (`hub/receipts.go`), and presence goes to subscribers other than the actor (`hub/presence.go` `presenceSubscribers`). The TUI also shows no unread total. This task adds one operator-only event type and the TUI unread total, so Task 23's journey can pass without polling, reloads or weakened assertions.

**Files:**
- Create: `internal/hub/operator_view.go`, `internal/hub/operator_view_test.go`
- Modify: `internal/hub/send.go`, `internal/hub/receipts.go`, `internal/hub/presence.go`, the hub functions behind `POST /v1/admin/dead-letters/{dly}/retry|discard` and `POST /v1/admin/subscriptions/{id}/pause|resume`, `docs/PROTOCOL.md` (§2.3 event list if it enumerates types, §2.13 table), `docs/protocol/envelope.schema.json` and `internal/envelope/schema.json` (identical edit: add `operator_view` to the event `type` enum and to the `data` description), `internal/opapi/tags.go`, `internal/opapi/tags_test.go`, `internal/ui/static/store.js`, `internal/ui/jstest/store.test.mjs`, `internal/tui/screen_inbox.go` and its test (unread total, raw-log line), `scripts/ui/dash.mjs:32`
- This task, like Task 1, edits `docs/PROTOCOL.md`; it touches only the lines named here.

**Interfaces:**
- Event `operator_view`, data `{reason, thread_id}`. `reason` is one of `sent`, `read`, `presence`, `dead_letter`, `subscription`. `thread_id` is a `thr_` id or `null`.
- `func (h *Hub) noteOperatorView(ctx context.Context, tx *sql.Tx, reason string, threadID string) ([]string, error)` appends one `operator_view` Event to the log of every enabled peer of kind `operator` inside the caller's transaction (empty `threadID` → `null`). It returns those peer ids, so the caller calls `h.Log.Notify` for each one after commit, the way the existing fan-out does.
- When it fires, in the same transaction as the change:
  - `sent`: `Hub.Send` accepted a new message whose sender is an operator-kind peer. That includes `approval_response`. An idempotent replay does not fire.
  - `read`: a `read` receipt changed `read_at` on a delivery whose recipient is an operator-kind peer. That covers both `POST /v1/receipts` and `POST /v1/admin/threads/{id}/read`, which uses the receipt path. An already-read delivery does not fire.
  - `presence`: an operator-kind peer's effective presence changed. A heartbeat that re-posts the same state does not fire.
  - `dead_letter`: retry or discard changed a delivery. `thread_id` is its thread.
  - `subscription`: pause or resume changed a subscription. `thread_id` is `null`.
- Invalidation, §2.13 table row and both clients: `operator_view` → `inbox`, `threads`, `peers`, plus `thread:<thread_id>` when `thread_id` is a non-empty string. As with the other types, if `data` is not an object, or `reason` is not a string, or `thread_id` is neither a string nor null, the event is inert. `opapi.Tags` (which the TUI uses through `internal/tui/data.go`) and `store.js` `tagsFor` stay mirror images.
- Not pushable: `events/worker.go` `pushable` and the MCP events enum stay as they are. No agent peer's log ever holds an `operator_view`.
- TUI: the inbox shows the snapshot's `unread` total in its header as `N unread` (`0 unread` when none), and the raw event log renders `operator_view` as `<reason>` plus the short thread id when there is one.
- `scripts/ui/dash.mjs:32`: the `Subscriptions` locator matches more than one element once the journey's extra state exists. Scope it to the dashboard section's heading (`getByRole('heading', { name: 'Subscriptions', exact: true })` or within the section), with no other change to Task 12's script.

- [ ] **Step 1: Write the failing tests**

```go
func TestOperatorViewOnOperatorSend(t *testing.T)            // operator sends approval_response → operator log gains {reason:"sent", thread_id}; replay of same idempotency key adds none
func TestOperatorViewOnRead(t *testing.T)                    // MarkThreadRead with 2 unread → 2 "read" events on the operator log; repeat call → none
func TestOperatorViewOnPresenceChangeOnly(t *testing.T)      // idle→working fires once; working→working (heartbeat) fires none
func TestOperatorViewDeadLetterAndSubscription(t *testing.T) // retry and resume each fire once with the stated thread_id
func TestOperatorViewNeverReachesAgents(t *testing.T)        // after all of the above, no agent peer's log holds operator_view
func TestTagsOperatorView(t *testing.T)                      // {reason:"read",thread_id:"thr_x"} → inbox, threads, peers, thread:thr_x; thread_id null → first three; reason 7 → none; data "x" → none
```

```js
test('tagsFor operator_view mirrors opapi.Tags')  // the same four cases as TestTagsOperatorView
```

TUI test: an inbox snapshot with `unread: 3` renders `3 unread` in the header; `unread: 0` renders `0 unread`.

- [ ] **Step 2: Run them and see them fail**

Run: `build2 go test -p 2 ./internal/hub/ ./internal/opapi/ ./internal/tui/ -run 'OperatorView|Tags|Unread'` and `build2 node --test --test-concurrency=2 internal/ui/jstest/store.test.mjs`
Expected: FAIL (no `operator_view`, no unread header).

- [ ] **Step 3: Implement** the helper, the five call sites, the table row in both clients, the schema enum in both files, the TUI header and log line, and the `dash.mjs` locator.

- [ ] **Step 4: Verify**

Run: `build2 go test -p 2 ./...`, `build2 node --test --test-concurrency=2 internal/ui/jstest/`, `just validate-protocol`, `build2 node scripts/ui/dash.mjs`, `build2 node scripts/ui/tui-inbox.mjs` (with `SHOTS_DIR` set to a scratch dir, so committed PNGs stay unchanged unless the header change is meant to show; if `18-*` shots change because of the unread header, re-shoot and inspect them).
Expected: all pass, `validate-protocol` prints no drift.

- [ ] **Step 5: Commit** `feat: operator_view events keep a second operator client in step`

---

### Task 25: Integrated screen gate

**Depends:** 8, 9, 10, 11, 12, 13, 14, 15, 18, 19, 20, 21, 22, 24

Added 2026-11-02 after Task 23's second attempt. Each web task passed its screen script in its own worktree against its neighbours' stubs. On the merged branch, `SHOTS_DIR=<dir> just ui-shots` fails in five scripts, reproduced on `operator-ui` without Task 23's changes:

| Script | Failure | First suspect |
|---|---|---|
| `scripts/ui/inbox.mjs:47` | `section order Needs a decision\|Failed deliveries\|5\|Woke you\|Held` | Task 13's `subs.renderAttention` fills Task 8's attention slot with an `h2` whose first span is a count, not a title |
| `scripts/ui/matrix.mjs:105` (and `:129`) | `focus did not return to the opener` | Task 11's matrix close path versus Task 9/10's thread mount |
| `scripts/ui/shell.mjs:74` | `moving between #matrix and #thread remounted the view` (a draft is lost) | Task 7's router versus Task 11's `#matrix/<id>` route |
| `scripts/ui/subs.mjs:31` | waitFor timeout: `section.subs` sixth row absent | seeded subscriptions versus Task 24's or Task 13's filtering |
| `scripts/ui/thread.mjs:76` | `reading column is only 756px` (needs > 760 at 1440×900) | Task 10's aside width versus Task 9's 980 px column |

**Files:** any of `internal/ui/static/*`, `internal/ui/jstest/*`, `scripts/ui/*.mjs` and `docs/plans/shots/oui/*.png` needed for the fixes above, and nothing else. This task runs alone after every owner has merged, so the Shared resources ownership no longer applies to these files.

**Rules:**
- For each failure, first decide against this plan which side is wrong. A behaviour the plan requires (focus returns to the opener, a draft survives `#matrix` ↔ `#thread`, the reading column is wider than 760 px at 1440×900, six seeded subscriptions) is fixed in the code, never by relaxing the script. A script expectation that predates a neighbour's real content (the attention slot was empty when Task 8 wrote its order check) is updated to the integrated truth. Record each decision in one line in the commit body.
- The inbox attention block follows the inbox's own heading convention: a title span first, then the `.n` count. So the order check becomes `Needs a decision|Failed deliveries|Subscriptions|Woke you|Held`, or the attention block is not an `h2` section at all. Choose whichever matches `docs/prototypes/operator-ui/shots/a-inbox.png` more closely.
- Do not widen a timeout or add a sleep. Wait on page state.
- Re-shoot every PNG whose pixels change, and inspect each one with the Read tool.

- [ ] **Step 1:** Run `SHOTS_DIR=.scratch/vecnet/build-operator-ui/t25-before just ui-shots` and record every failing script, including any not in the table.
- [ ] **Step 2:** Fix each failure under the rules above, one at a time, re-running that script after each fix.
- [ ] **Step 3: Verify**

Run: `build2 node --test --test-concurrency=2 internal/ui/jstest/`, then `SHOTS_DIR=.scratch/vecnet/build-operator-ui/t25-after just ui-shots`, twice in a row.
Expected: both pass every script, twice.

- [ ] **Step 4: Commit** `fix: integrated screen gate passes on the merged branch`

---

## Decisions for Max

Plan assumes the first entry of each and proceeds; none blocks the build.

1. **Presence toggle's meaning.** Ticket 18 said presence "decides whether `wake_idle` interrupts" the operator, but PROTOCOL §5.2 makes `wake_idle` inbox-only for the operator, so the toggle only informs peers. The plan ships truthful copy and leaves the protocol alone. To make it matter, fire notification webhooks on `wake_idle` while the operator is `idle`: a one-paragraph §5.2 change plus one hub task, which can follow this plan.
2. **Web keyboard layer deferred.** The ticket recommended C's keys over A. The TUI now exists as the keyboard surface, so the web skips them. Reversal is one more module, `keys.js`, with no other change.
3. **TUI is `vecnet tui` inside the CLI binary, on Bubble Tea v2.** This adds about 15 transitive Go modules to `vecnet`, none to the hub, recorded as an ADR amendment. A separate `vecnet-tui` binary would keep the CLI lean at the cost of a second artifact.
4. **Read state moves to the hub.** It was browser-local; it becomes `deliveries.read_at`, so web and TUI agree.

## Self-review notes

- **Ticket coverage.** Variant A inbox: Task 8. A's chat with B's side-panel width: Tasks 9, 10. Thread files in the panel: Task 10. Matrix as an extra view: Tasks 11, 19. Dashboard provisional and read-only: Tasks 12, 20. TUI as its own thing: Tasks 17–22. One-tap presence: Tasks 14, 18. Reasons a message is held beside presence: Tasks 8, 14. Approval countdown: Tasks 7, 8, 18. Reply links and spill chip: Task 9. Loop cuts and cut state: Tasks 4, 5, 12, 20. §16.4 blocked threads: Tasks 5, 12, 20. §13.4 subscription health including last refresh: Tasks 6, 13, 21.
- **Boundary choices.** Tasks 3–6 fill stubs so `opclient` and the web shell can merge first. `subs.js`, `presence.js` and the `#matrix/<id>` route sit behind slots and stubs from Task 7, so inbox, side panel and dashboard do not wait for them, and those modules do not wait for the screens. TUI screens read one shared `Data`, so they do not depend on each other.
- **Type consistency.** `opapi` is the only definition of wire shapes; JS relies on the same JSON. `approvals.js` and `opclient.Answer` build the same body with different key prefixes, so one client's retry replays while the other client's attempt reaches `approval_closed`. `Tags` is pinned by one fixture read from both languages.
- **Reconciled against master (2026-11-02).** Task 6 no longer adds a column, Task 2 uses `httpapi.NewStack`, and the health table covers all five `paused_reason` values.

--- SUMMARY ---

- **What this builds:** the phase 3 operator surface: variant A web inbox and chat, a wider thread side panel with participants and files, an openable delivery matrix, a provisional read-only dashboard, dots subscription health, one-tap presence, starting a new thread, and `vecnet tui` as a full keyboard client with inbox, chat, dashboard, subscriptions and a command line.
- **Key design:** hub-owned snapshots at `/v1/admin/*` and **snapshot + invalidate** on both clients; events only name what to refetch, and the ack follows the refetch. No `e0.0` replay.
- **Shared layer:** leaf `internal/opapi` (wire types, event→tag table with a fixture read by Go and JS), `internal/opview` (queries), `internal/opclient` (Go client), `api.js`/`store.js` (web). `opapi` being a leaf keeps SQLite and the hub out of the CLI binary.
- **Parallelism:** 23 tasks; each starts when its named dependencies merge. Four backend reads, then eight web screens and five TUI screens fan out, one file set per task, with stubs from Tasks 1, 7 and 17 to prevent conflicts. Migrations are pre-numbered 0010 and 0011.
- **Visual proof:** a demo hub with a seeded scenario (Task 2) lets every UI task produce four dark PNGs (initial, in-progress, success, error) from real runs: Playwright for web, the real binary in a private tmux for the TUI. Task 23 adds a two-client journey over one hub.
- **Safety:** web bans HTML sinks by test; TUI strips terminal escapes from all peer text; dashboard writes are tested to be zero; countdowns use the hub's clock; Resume is offered only where it can work.
- **Precondition:** met; `connectors` merged at `eb5d197`.
- **Open for Max:** whether presence should gate operator notifications (plan ships truthful copy, protocol unchanged). Web keyboard layer deferred, TUI in the CLI binary on Bubble Tea v2, and read state moving to the hub are stated decisions, reversible.
