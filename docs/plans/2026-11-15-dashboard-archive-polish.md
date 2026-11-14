# Dashboard polish, archiving and browsing at scale

**Goal:** the operator web UI stays usable with hundreds of threads (Max had 534 on the dashboard) and a long agent list. The dashboard becomes an overview instead of a "list everything" page. Agents and threads can be archived, automatically as well as by hand. Every thread and agent can still be found through dedicated browse and search pages.

**Requested by Max (2026-11-15):** a "show disabled" checkbox next to "show archived"; auto-archive for agents unseen for more than 2 days (configurable); archivable threads; auto-archive of threads once most of their agents are archived (configurable); a dashboard that does not list everything; ways to find any agent or thread; collapsible sidebar "Your Threads" and "Other Threads" sections; and polished dashboard graphs whose hover readout neither looks ugly nor reflows the page. SVG attachments ship separately (branch `feat/svg-thumbnails`).

**Defaults chosen without Max's answer (state them in the final report):** auto-archive defaults to `session` peers only, and it uses the existing archive, which revokes credentials. A bool preference widens it to every agent kind except operator and source.

## Lanes and ownership

| Lane | Branch / worktree | Owns |
|---|---|---|
| B: backend | `feat/archive-backend` in `.worktrees/archive-backend` | `internal/store/migrations/0036_*`, `internal/hub`, `internal/opview`, `internal/opapi`, `internal/httpapi`, `internal/prefs`, `cmd/vecnet-hub`, `internal/cli`, `internal/mcpserver` (only if a parity test forces it), `docs/PROTOCOL.md`, Go tests, one CHANGELOG line |
| U: web UI | `feat/dashboard-archive-polish` in `.worktrees/dashboard-archive-polish` | `internal/ui/static/**`, `internal/ui/jstest/**`, `scripts/ui/**`, `internal/opseed/**` (demo data for many threads/peers), `internal/httpapi/ui_test.go` size cap only, `docs/OPERATOR.md`, `docs/plans/shots/oui/README.md` if shots are added, one CHANGELOG line |

U merges B's branch (`git merge feat/archive-backend`) once B reports green. It builds the thread-archive UI against the contract below and runs the screen gates against the merged tree. Neither lane edits the other's files. A lane that needs a change in the other's area reports it instead.

## API contract (B implements, U consumes)

### Storage: migration `0036_thread_archive.sql`
```sql
ALTER TABLE threads ADD COLUMN archived_at TEXT;
ALTER TABLE threads ADD COLUMN archived_by TEXT CHECK (archived_by IN ('operator', 'hub'));
ALTER TABLE threads ADD COLUMN unarchived_at TEXT;
CREATE INDEX threads_archived ON threads(archived_at);
ALTER TABLE peers ADD COLUMN unarchived_at TEXT;
```
(Adjust only if migration tests or sequence rules require it; keep the column names.)

### Thread archive is an operator-view state
- Archiving changes no peer-facing behaviour. Peers can still read and post. `envelope.ThreadSummary` and the peer endpoints are unchanged.
- A new non-reaction message in an archived thread unarchives it in the same transaction (`archived_at=NULL, archived_by=NULL, unarchived_at=now`).
- `POST /v1/admin/threads/{id}/archive` and `POST /v1/admin/threads/{id}/unarchive` are operator only, with no body, and return 204. Repeating either is a no-op 204. An unknown thread is 404 `unknown_thread`. They audit `thread_archive` / `thread_unarchive` with actor `operator`. Manual archive sets `archived_by='operator'`; unarchive sets `unarchived_at=now`.
- Each change appends `operator_view` with `reason: "archive"` and the thread id (document the new reason in PROTOCOL §2.13 and update any schema/enum that lists reasons). A batch sweep sends one `operator_view` with `thread_id: null` per operator.
- `GET /v1/admin/threads` gets two new optional query parameters, applied in the SQL before the page is cut, with cursor semantics unchanged:
  - `archived=true|false`. Absent means all, as today. Any other value is 422 `invalid_value`.
  - `q`: a case-insensitive title substring matched like `Hub.ListThreads` (`instr(lower(title), lower(q))`), at most 128 runes (422 beyond). Untitled threads never match a non-empty q.
- `ThreadRow` gets `archived_at` (string|null) and `archived_by` (`"operator"|"hub"|null`).
- `GET /v1/admin/thread-stats` returns `{"now", "total", "open", "archived", "active_24h", "active_7d"}`. `open` = not archived; `active_*` = open threads whose `last_message_at` is at or after the hub's now − 24 h / 7 d.
- CLI: `vecnet thread archive <id>` and `vecnet thread unarchive <id>`, next to `thread budget`.

### Peer auto-archive
- Preferences, all live and editable, group `agents_defaults`:
  - `agents.auto_archive_after`: duration, default 172800 (2 d), min 3600, max 31536000, 0 turns it off (`DisabledValue` 0). Label "Auto-archive idle agents after".
  - `agents.auto_archive_all_kinds`: bool, default false. Off: only `session` peers. On: every peer except `operator` and `source`. Label "Auto-archive every agent kind".
  - `threads.auto_archive_share`: int percent, default 51, min 1, max 100, 0 turns it off. Label "Auto-archive threads when this % of agents are archived".
  - Update `internal/prefs` tests, the PROTOCOL preference table, and anything else that enumerates the registry (grep `presence.launcher_ttl`). Bump `RegistryVersion` if the preferences plan/protocol says a key-set change requires it; otherwise explain why not. Startup flags in `cmd/vecnet-hub` are optional; add them if the existing pattern makes that natural.
- `Hub.ArchiveIdlePeers(ctx, now) (int, error)`:
  - A peer is idle when its kind is in scope, `archived_at IS NULL`, `disabled = 0` (a disabled peer is the operator's deliberate state and is left alone), it has no open stream (in-memory stream count), and `max(last_seen, presence_at, enrolled_at, unarchived_at)` is older than now − `agents.auto_archive_after`. Missing values are ignored. Check that the timestamp formats compare correctly as strings, or compare parsed times.
  - Archive with exactly the existing `SetPeerArchived(true)` effects (credentials, enrol tokens, dot links and OAuth tokens revoked; `directory_updated` removed; notify). Refactor that into a shared tx helper; do not duplicate the revocation SQL.
  - Audit `peer_archive` with actor `hub` and detail `{"auto": true, "idle_seconds": N}`. Bound each run (for example 200 peers).
  - Unarchive now also sets `peers.unarchived_at = now`, so a just-unarchived peer is not re-archived on the next sweep.
- `Hub.ArchiveAbandonedThreads(ctx, now) (int, error)`:
  - For each open thread, let agents = participants whose peer kind ≠ `operator`, and archived = those agents with `archived_at` set. Archive the thread when agents > 0, `archived*100 >= share*agents`, and the thread was not unarchived after its most recent participant archival (`unarchived_at IS NULL OR unarchived_at < max(archived participant archived_at)`). This keeps an operator's unarchive while nothing changes, and re-evaluates once another participant is archived.
  - Set `archived_by='hub'`. Audit one `thread_archive` entry per batch, actor `hub`, targets = thread ids, detail `{"auto": true, "reason": "participants_archived"}`. Bound each run (for example 500 threads).
- Run both sweeps in the slow tick (hourly), peers first and then threads, logging errors the way `ArchiveIdleGroups` does. A successful manual `SetPeerArchived(true)` also runs the thread rule for that peer's threads right after commit, so the UI updates without waiting an hour. Neither sweep runs on the preference-wake path.

## UI design (U)

Use the dataviz and frontend-design skills for the graphs and layout; the existing visual language (dark/light themes, dash.css tokens) stays.

1. **Navigation:** add **Threads** (`#threads`) and **Peers** (`#peers`) to the sidebar nav. Routes accept query strings (`#threads?q=&peer=&state=open|archived|all&mine=1`, `#peers?q=`) so links can prefilter. Keep `#dash`.
2. **Dashboard (`#dash`) as an overview:**
   - Keep the activity strip, polished (item 6).
   - A KPI row of small stat tiles, each linking to its detail: Peers (active/total, presence breakdown), Threads (active in the last 24 h · open · archived, from `thread-stats`), Needs attention (blocked + failed + dead letters, with loop cuts in 24 h). Use the dataviz stat-tile rules.
   - **Peers panel:** at most about 10 peers, ordered by most recent activity or seen time. A toolbar holds a filter box and the **Show disabled** and **Show archived** checkboxes side by side, each with its hidden count, e.g. "Show disabled (3)". Both are unchecked by default, so disabled and archived peers are hidden. The state persists in localStorage and is shared with `#peers`. A footer reads "All N peers →".
   - **Recent threads panel:** about 10 open threads from `GET /v1/admin/threads?archived=false&limit=10`. Do not walk every page (delete or stop calling `allThreads` here). A footer reads "Browse all threads →".
   - Delivery health, dead letters and loop control stay. When empty they collapse to one compact "all clear" line instead of three headed sections. Subscriptions and principals stay. The audit tail goes inside a `<details>`, closed by default, with its open state remembered.
3. **Peers page (`#peers`):** the full peer table (the current one with activity sparklines), plus search (name and id substring, client-side), a kind filter, a presence filter, the same two checkboxes, and sort (recent activity, name). Render 50 rows, then "Show more". Only rendered rows own sparklines; keep the dash-memory guarantees that retired rows release their chart owners.
4. **Threads page (`#threads`):**
   - Search by title (server `q`, debounced, with stale responses dropped), a participant filter (type-ahead over the directory, mapped to `participant=peer_…`), a state switch (Open / Archived / All), and "only mine". Results load 50 at a time through the server cursor with "Load more".
   - Rows show the title, participants, message count, last activity and an archived chip (marked "auto" when `archived_by='hub'`). Each row has an Archive / Unarchive action. A row checkbox plus **Archive selected** / **Unarchive selected** handle bulk changes, one sequential request per thread, with failures reported per thread.
   - The thread view (`#thread/<id>`) also gets an Archive / Unarchive control and shows an archived note.
5. **Sidebar:**
   - "Your Threads" and "Other Threads" headers become disclosure buttons (`aria-expanded`, a chevron and a count) that collapse their lists. State persists in localStorage and works in both desktop and phone layouts, alongside the existing phone `#side-tog`.
   - Both lists exclude archived threads (`archived=false`).
   - Your Threads shows about 15 rows, then "Show more".
   - Other Threads starts at about 15 rows instead of 50. Its "past the cap" link points to `#threads` instead of the dashboard.
   - The search results behaviour is unchanged.
6. **Graphs:**
   - Hovering or keyboard-inspecting must not change layout. Use a fixed-height readout slot that is always reserved, or a floating tooltip (absolute, `pointer-events: none`, clamped inside the chart, never pushing siblings). Verify by measuring the bounding boxes of the panel and the peer rows before and during hover in the screen script: zero height change.
   - Readout text is human: for example "3 messages · 14:32–14:33 (12 min ago)" in local time, computed from the hub clock. Do not use the ISO interval. Keep the full UTC interval in `aria-label`/`title`.
   - Polish the marks: a hover point marker, light axis ticks (−60 m, −30 m, now) and peak labelling. Bars instead of a line are fine if the dataviz guidance favours them for sparse minute counts. Keep keyboard inspection (arrows, Home/End, Esc), forced-colors and print rules, and the shared peer scale.
7. **Settings:** the three new preferences render correctly. `threads.auto_archive_share` shows a % unit, and the bool row gets On/Off. Check `scripts/ui/prefs.mjs` and the Settings model tests for hard-coded key counts.

## Gates

On x-boss `just bb`/`build2` cannot ssh to itself. Use the toolchain env below with a private XDG and SHOTS dir per lane:
```
export TC=/home/xertrov/bb-src/vecnet4/.toolchain
export PATH="$PWD/scripts/bb/bin:$TC/go/bin:$TC/node/bin:$PATH" GOTOOLCHAIN=local CGO_ENABLED=0
export PLAYWRIGHT_BROWSERS_PATH=$TC/ms-playwright PLAYWRIGHT_MODULE=$TC/playwright-1.61.1/node_modules/playwright/index.mjs
mkdir -p /tmp/<lane>-xdg && ln -sfn $TC/fonts /tmp/<lane>-xdg/fonts && export XDG_DATA_HOME=/tmp/<lane>-xdg
export SHOTS_DIR=/tmp/<lane>-shots   # always, or screen scripts overwrite committed PNGs
```
Limit Go to `-p 2`. Run targeted packages while iterating and `go test -p 2 ./...` once at the end.

- **B:** red then green tests for every rule above, covering:
  - migration (fresh and upgrade);
  - archive/unarchive routes (204, 404, idempotent, operator only, audit, `operator_view`);
  - the archived and `q` filters with paging;
  - thread-stats;
  - unarchive on a new message (but not on a reaction);
  - the idle-peer sweep: kinds, disabled skipped, stream skipped, unarchived_at respected, the off switch, revocation shared with the manual path;
  - the thread-share sweep: the majority boundary, the operator excluded, unarchive respected until another participant is archived, the off switch;
  - preference registry and validation;
  - CLI commands.

  Run `just validate-protocol` if python jsonschema is present.
- **U:** `node --test --test-concurrency=2 internal/ui/jstest/` with new model tests (peer filters, sidebar collapse state, the threads query/model, readout text, no layout shift where it can be modelled). Also run `go test ./internal/ui/ ./internal/httpapi/ -run 'TestUI|TestCSP|TestPolicy|TestHandler'`, the updated `scripts/ui/dash.mjs` and `scripts/ui/sidebar.mjs`, and new `scripts/ui/threads.mjs` / `scripts/ui/peers.mjs` against a demo seeded with at least 300 threads and 60 peers (some disabled and archived). The screens must assert the bounded dashboard row counts, zero hover reflow, collapse persistence, search results and bulk archive. Raise `uiMaxBytes` to the next 16 KiB boundary with a comment if needed. Inspect the screenshots in both themes and at phone width.
