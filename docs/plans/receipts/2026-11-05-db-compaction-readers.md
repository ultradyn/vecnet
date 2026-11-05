# DB compaction Task 1: frozen baseline and storage-reader inventory

Baseline: `fcc71a10e9c2bf7c496983b7cb0dc363f6382d16`, branch `db-t01`.
Plan: `docs/plans/2026-11-05-db-compaction.md`, Task 1 only. No production
files, migrations, existing tests, existing goldens, or companion-plan work
change in this task.

## Raw event-data SQL readers

The required inventory command, run at the baseline:

```sh
rg -n -g '*.go' 'e\.data|events\.data|SELECT[^;]*\bdata\b[^;]*FROM events' internal cmd
```

After excluding tests and non-SQL matches, **three production call sites**
read raw event data. A second search for every `FROM events` / `JOIN events`
confirmed this list, including multiline queries.

| Location at baseline | Consumer and current bytes | Later hydration obligation |
| --- | --- | --- |
| `internal/eventlog/log.go:225`, `Log.Read` | Selects `seq,type,at,data` by owner and global sequence; returns `envelope.Event.Data` directly | Hydrate before exposing Events; preserve exact data bytes and ordered metadata |
| `internal/hub/deliveries.go:96`, `ClaimDue` | Joins `deliveries`, routes, and `events e`; reads `e.data` directly into `DueDelivery.Body` | Hydrate within the claim transaction; the body and stored `body_sha256` stay frozen while live attempt increases |
| `internal/hub/notify.go:302`, `notifyOperatorDeliveries` | Joins operator deliveries to `events e` and messages; reads `e.data`, then renders an Event for operator routes | Hydrate before rendering; keep wake/approval filters and reaction dispatch gates |

`internal/tools/r2spike/main.go:70` is a path containing `.data.json`, not
an event read. JavaScript DOM `.data` occurrences and `_test.go` queries are
not production SQL readers.

Metadata-only readers do not need payload hydration: `eventlog.oldestValid`
(minimum seq and sqlite sequence), `Log.Current` (owner max seq),
`Log.HighWater` (sqlite sequence), `mcpserver.tailStart` (owner seq with
OFFSET), and `internal/opseed/subs.go:27` (fixture seeding max seq).

## Consumers downstream of those readers

| Surface | Baseline call path and contract |
| --- | --- |
| HTTP SSE / long-poll | `httpapi/events.go:113,210,319` -> `Log.Read`; `sseFrame` at line 344 and `pollBody` retain field order, escaping, cursor and frame delimiters |
| MCP inbox tool | `mcpserver/tools.go:338` -> `Log.Read` -> `withoutBlobURLs` / `dataWithoutBlobURLs`; intentionally strips hosted URLs/tokens before both tool representations |
| MCP inbox resources | `mcpserver/resources.go:201,229,258` -> `inboxTail` / `tailStart` / `Log.Read` -> `withoutBlobURLs`; returns its own sorted-key wrapper; latest 50 events |
| MCP inbox notifications | Resource watchers signal log growth, not content; `subscriptions/listen` acknowledgement and `notifications/resources/updated` bytes are separately frozen |
| MCP subscribe validation | `mcpserver/events.go:383` calls `Log.Read`; owner cursor behavior must continue to agree with subscription replay |
| Events callbacks | `events/worker.go:172` -> `Log.Read` -> filter/gate -> `eventBody` -> durable `event_outbox.body`; normal callback includes an Event, oversized callback includes a preview |
| Events retries / replay | Exact outbox bytes are resent with the same webhook-id; timestamps/signatures may change. Exhausted callbacks remain suppressed and retain their replay pin |
| Inbox / history projections | `opview/inbox.go:84,144,173`, `opview/threads.go:170,190,241`, and `hub/history.go:237` read `messages.body`, not `events.data`; preserve excerpts, sender_info, attachment metadata and reaction overlays |
| TUI | `tui/data.go` loads opapi inbox/history and keeps public Events for invalidation/raw-log display; `screen_inbox.go` renders the projections. Existing `tui/testdata/inbox-80x24.golden`, `inbox-140x42.golden`, and chat/reaction fixtures were inspected and remain unchanged |
| Web | `ui/static/store.js` derives invalidation tags from public Event type/data, including message thread ids; `ui/jstest/store.test.mjs` and other existing fixtures remain unchanged |
| CLI watch | `cli/watch.go:eventFieldsOf` consumes Delivery / receipt / reaction / presence Event data; formatters and cursor handling consume the public contract, not storage records |

No CLI, client, or TUI storage import is introduced. Local offline
`go list -deps ./cmd/vecnet` verifies that command remains free of
`modernc.org/sqlite`, `internal/store`, and `internal/eventpayload`.

## Fixture provenance and freeze

All new fixture directories are `internal/{eventlog,hub,httpapi,mcpserver,events,opview}/testdata/reference_guard/`.
Each contains `SHA256SUMS` sealing its captured files. These fixtures are
**frozen**: no UPDATE_GOLDEN option or fixture-writing path exists in the
committed tests. Later tasks must repair production regressions, not change
these expected bytes or manifests.

The corpus is synthetic, constructed for this task from baseline wire
structs and protocol fields; it is not an export from a running hub. Each
corpus entry carries the SHA-256 of its exact data string. The oversized
entry intentionally exceeds normal ingress limits to exercise the existing
defensive Events-preview branch for stored data. All other Delivery entries
are checked against the baseline envelope schema.

Tests use `testhub.StartWith` with its `t.TempDir()` database, an injected
FakeClock advanced to a fixed millisecond instant, fixed synthetic owners,
a fixed principal, synthetic credentials, and synthetic payload URLs.
Actual transport listeners are ephemeral loopback `httptest` servers on
x-boss. No local user configuration, shared hub, real identity, or live DB
is used. Fixture diagnostics show only names, ids, counts and hashes.

| Generating / asserting test | Captured contract |
| --- | --- |
| `eventlog.TestReferenceGuardLogReadBytes` | Raw Log.Read bytes for Unicode/escaped JSON, nil/empty optionals, source/operator/hub messages, reaction Delivery, hosted/reference-only attachments, receipt and presence |
| `eventlog.TestReferenceGuardCursorOrdering` | Mixed-owner ordering, principal merge / peer / narrowed streams, empty / head / equal / future reads |
| `eventlog.TestReferenceGuardLegacy0018` | Real SQLite json_remove/json_set reproduces migration 0018's appended sender_info; before/after bytes are frozen independently |
| `hub.TestReferenceGuardWireRenderer` | Baseline private Delivery renderer, public Message renderer (including nil -> empty arrays/objects), sender snapshots |
| `hub.TestReferenceGuardBlobURLRenderer` | Baseline attachment URL renderer with a fixed synthetic signing key, clock, public URL, and two recipient scopes; reference-only attachment stays reference-only |
| `hub.TestReferenceGuardFanOutSendBytes` | Real production peer/source/operator/hub-expiry/reaction sends, nil/empty optionals, hosted/reference-only attachments, and actual held wake_idle release |
| `hub.TestReferenceGuardClaimDueFrozenRetry` | Exact bodies and body_sha256 across claims, held release, attempt 1 -> 2, clock / public URL / peer directory / thread summary changes |
| `hub.TestReferenceGuardWebhookAttempts` | Real worker HTTP retry, exact body, webhook-id and signatures, changing attempt header and signing timestamp |
| `hub.TestReferenceGuardOperatorNotifications` | Actual notifyOperatorDeliveries SQL reader and per-surface Event notification bytes, including the reaction dispatch gate |
| `httpapi.TestReferenceGuardHTTPPollAndSSE` | Literal long-poll bodies, empty-page next_cursor behavior, and complete SSE preamble/priming/Event frames |
| `httpapi.TestReferenceGuardAckScopes` | Separate principal-wide / principal-narrowed / peer acks and exact cursor strings |
| `mcpserver.TestReferenceGuardMCPInboxBytes` | Tool result (both representations) and resource text, peer/principal/empty scopes; MCP strips URLs while raw HTTP retains them |
| `mcpserver.TestReferenceGuardMCPInboxNotifications` | Live resource acknowledgement and growth notification bytes |
| `events.TestReferenceGuardEventsCallbackAndOutbox` | Actual durable outbox/callback bytes, receipt callback, oversized preview, retries after state changes, pending and settled replay cursors; presence remains filtered from pushes |
| `events.TestReferenceGuardExhaustedSuppression` | Retry-byte equality through exhaustion, no unsolicited retransmission, replay pin |
| `events.TestReferenceGuardLegacyOutbox0018` | Callback queued before migration-style json_set keeps original signed bytes, while Event replay includes the new sender_info |
| `opview.TestReferenceGuardOpviewInboxHistory` | Full inbox, thread-detail and history projections, reaction overlays and suppression of reaction Messages from ordinary history |

For production sends only, the baseline code mints random ULIDs and has no
injected id generator. `send-*.json` therefore uses deterministic named
slots for exactly the returned message/delivery/thread/source/parent ids.
Tests bind those ids into the **expected bytes** and compare the actual raw
body untouched. They never normalise, unmarshal/remarshal, or semantically
compare the actual body for the assertion. All seeded reader fixtures use
literal fixed ids.

A new test-only `hub/notify_reference_guard_test.go` exposes the private
notification reader to the external test package and pins private renderers;
it adds no production API. Opview gets its own new guard file because its
message-backed projections need independent expected bytes.

Baseline observations preserved explicitly: future cursors return an empty
200 poll / empty Log.Read page; ordinary history hides reaction Messages
and overlays reaction aggregates on targets; default Events subscriptions
only include deliveries, so the callback guard explicitly subscribes to
receipts too. No new cursor or retention behavior is implemented here.

## Validation receipt

Baseline captures ran on x-boss using:

```sh
BB_HOST=x-boss BB_JOBS=4 just bb build2 go test -p 2 -count=1 -run ReferenceGuard ./internal/eventlog/ ./internal/hub/ ./internal/httpapi/ ./internal/mcpserver/ ./internal/events/ ./internal/opview/
```

After capture writers were removed, the red proof flipped one byte in every
expected-byte fixture (118 files), preserving exact originals in the task's
scratch evidence. All 17 guard tests failed on byte/hash comparisons. All
124 corpus and expected-byte fixtures were then restored and verified against
SHA256SUMS. This is a guard mutation proof, not an intentional production
failure.

Required completion checks and their full output are recorded in
`/home/xertrov/src/vecnet4/.scratch/vecnet/build-db/task-01.impl.json` and its
referenced evidence logs. No tests or measurements run on xsm.

Completion results:

- Required complete six-package suite, `-count=1`: PASS (all six packages).
- `BB_HOST=x-boss BB_JOBS=4 just bb build2 go vet ./...`: PASS.
- Local `gofmt -l .`: empty output.
- All 124 fixture files match their frozen manifests; no capture writers ship.
- Offline CLI dependency check: no sqlite/store/eventpayload.
- All 138 additions are new guard tests, their fixtures/manifests, or this receipt.
  Existing production files, migrations, tests and goldens are unchanged.
