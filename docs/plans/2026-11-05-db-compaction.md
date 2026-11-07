# DB Storage, Compaction and Retention Implementation Plan

> **For agentic workers:** This is a plan, not an implementation. Follow the house task format: each worker receives its task plus Global Constraints, uses test-first checkboxes, and commits only its assigned files. A `---` ends each task. Execute serially where the dependency map or shared-file order requires it. Build workflow arguments are a follow-up, not part of this planning assignment.

**Goal:** Store one immutable Message body per send rather than one additional copy per recipient; reclaim expired operational state and acknowledged event history without changing existing wire bytes, cursor ordering or retry guarantees. Make history retention explicit and measurable so the Fly hub's approximately 1 GB volume can be operated safely.

**Architecture:**
- Keep `messages.body` as the immutable canonical send-time Message. Store delivery events as versioned internal references to `msg_id`, with frozen per-recipient envelope fields and attachment overrides. Hydrate behind a hub-only storage module before any serializer sees an `envelope.Event` or delivery body. Small non-delivery events remain inline.
- `internal/eventpayload` owns exact-byte composition and batch loading. `eventlog.Log.Read`, push claiming and operator notifications use it; HTTP, MCP, TUI and web readers retain their existing representations. The module accepts the caller's query handle, so a push transaction never opens a second transaction or borrows another pool connection.
- An additive schema migration enables mixed inline/reference reads. A separately scheduled, bounded, restartable backfill rewrites existing rows in place after proving byte equality. Unusual legacy message representations use deduplicated immutable snapshots; uncertain rows stay inline. Event identity, sequence, time and logical payload are immutable even when their physical representation changes.
- Retention deletes **contiguous prefixes per peer**, maintaining explicit per-peer purge floors in the deletion transaction. Stream reads use floors for their actual owners, including principal-wide merged streams. Retry sources and immutable Message dependencies are pinned until settled.
- Housekeeping runs in bounded slices from the existing ticker. SQLite freelist reuse is the immediate win. Use bounded incremental vacuum only where already supported; offline NONE-to-INCREMENTAL conversion is deferred beyond this release.

**Tech Stack:** Existing Go/store/modernc SQLite, JSON and stdlib only initially. No new codec or CLI SQLite dependency. SQLite diagnostics and probes are hub/build-box tools.

**Spec and evidence:** `docs/PROTOCOL.md` §1.3, §2.2–2.4, §2.7, §3.3–3.4, §4.4, §5, §11, §12, §17; `docs/business/pricing-analysis.md` §1.1; `docs/BUILDBOX.md`; the original assignment in `.scratch/vecnet/integrations/plan-db-compaction.assignment.md` and decisions/preferences update in `.scratch/vecnet/integrations/plan-db-compaction-v2.assignment.md`.

**Starts from:** `plan-db-compaction`'s master-derived tree on 2026-11-05. Rebase at implementation time: this tree includes migrations 0019/0020 for subscription status/refresh. Hub-notices is expected to take 0021; the parallel R2 plan may add another. Every new migration below means **next free at build time**, after inspecting the merged migration directory, never a reserved number. Never edit a shipped migration or its checksum.

## Dependency map

```json
{"1":[],"2":[1],"3":[2],"4":[3],"5":[4],"6":[4,5],"7":[6],"8":[6,7],"9":[8]}
```

Shared-file order: `eventlog/log.go` 3 → 5; `hub/send.go` 3 → 6 → 7; `hub/tickers.go` 4 → 5 → 6 → 7; `events/store.go` 5 → 7; `store/store.go` 2 → 8; schema migrations 2 → 4 → 5 → 6 → 7, allocated on integration. Tasks 6 and 7 integrate serially; their independent investigations may overlap. Task 9 owns protocol, deployment and pricing updates after the code lands. Coordinate `internal/mcpserver/**` and protocol edits with mcp-first and hub-notices; coordinate `blob_refs` and storage backup/export behavior with R2. No R2 implementation belongs in this plan. Coordinate the typed hub preferences seam with the separate preferences API/web UI plan; this work consumes that seam without building its API or page.

## Decisions

### 1. Reference storage, frozen replay bytes

Today `hub.fanOut` serializes `deliveryWire`, including a **send-time** `thr.summary()`, fixed `attempt: 1`, ingress, recipient, via/also_via, and the full Message. It hashes those exact bytes into `deliveries.body_sha256`. `ClaimDue` reads `events.data` directly; `notifyOperatorDeliveries` does too. Merely changing `Log.Read` would send internal references to webhooks and notifications. `deliveryBlobURLs` also samples the clock and public URL at send, so generating URLs again at read would violate byte equality even if Message text stayed unchanged.

Add `events.payload_format TEXT NOT NULL DEFAULT 'inline'` with allowed values `inline` and `delivery_ref_v1`. `events.data` remains valid JSON; for the reference format it contains an internal record with:

- `msg_id`, optional `snapshot_id`, and SHA-256 of the exact base Message bytes;
- `wire_prefix` and `wire_suffix`, the exact Delivery bytes before and after its top-level `message` value;
- optional `attachments_json`, the exact send-time top-level Message attachment array when it differs from the canonical Message; it includes the original recipient-scoped blob URLs;
- an expected SHA-256 of the complete Delivery bytes, checked against `deliveries.body_sha256` when a delivery row exists.

The prefix/suffix encode schema_version, delivery_id, recipient, via, also_via, ingress, attempt and thread summary exactly, including optional-field behavior, key order and escaping. They are JSON strings in the storage record, so their escaping adds overhead; include it in the benchmark. Do not reconstruct them from mutable delivery/thread/peer rows. Store reference `msg_id` and `snapshot_id` additionally as nullable typed event columns with matching CHECK constraints and indexes for dependency queries; the storage JSON and columns must agree. A reference event must have a real immutable body before commit.

Use a JSON-aware top-level value scanner, never string replacement or regex: find the `message` slice, or the `attachments` slice within that Message, while respecting escapes and nested objects. Compose original bytes with that value replaced. Unmarshal/remarshal alone cannot preserve legacy key order or whitespace. New writes may continue using the existing wire renderer transiently, derive the reference from its output, and discard the full output after hashing; this saves durable storage without pretending to eliminate send-time JSON CPU. Do not write a second full durable copy as a cache.

Normal references resolve `messages.body`; legacy exceptions resolve `event_message_snapshots(snapshot_id PRIMARY KEY, msg_id, body_sha256, body TEXT CHECK(json_valid(body)), UNIQUE(msg_id,body_sha256)) STRICT`. Verify full bytes on a hash match. Use a snapshot only if the embedded legacy Message, after removing the frozen attachment override, is not byte-equal to the canonical body. Migration 0018 used `json_set` on both messages and events; historical rows and already-enqueued MCP callbacks need explicit coverage. Missing canonical messages can be preserved in a snapshot when a valid embedded Message exists. Unknown shapes, duplicate critical keys, malformed references or inability to prove reconstruction leave the original event inline and increment an ids-only diagnostic counter. Never discard unknown legacy fields to achieve conversion.

**Immutability:** After the old migrations run and references become enabled, Message bodies used for replay cannot be updated in place. Enforce this with an update guard/trigger and tests. Current public APIs do not implement Message edits or text redaction; reaction aggregates are overlays on history, not changes to `messages.body`. A future edit creates a new revision/current-history view and retains referenced originals. A history tombstone can hide a Message while its replay snapshot stays pinned. A future privacy erasure must explicitly invalidate affected replay prefixes and cancel pending sends before destroying bytes; it cannot silently change the payload of an existing delivery. Blob purge keeps the frozen URL bytes even when fetching that URL now returns `blob_purged`.

Hub-authored approval timeout/job/system messages and source/operator sends go through the same fan-out storage path; hydration must not require the sender to exist in `peers`. Reaction **Delivery** events use the same reference format because they contain a Message. Standalone reaction add/remove events, receipts, presence, directory/thread updates and future hub-notice events remain small inline snapshots. Never hydrate their data from current state. Do not compress or normalize them in this slice.

### 2. Hydration seam and every consumer

Proposed module interfaces (exact names may follow repo conventions; semantics are fixed):

```go
// internal/eventpayload; hub-only, accepts store/transaction query interfaces.
EncodeDelivery(ctx, tx, msgID string, wire []byte) (StoredPayload, error)
HydrateBatch(ctx, q, rows []StoredPayload) ([][]byte, error)
// eventlog exposes envelope.Event only; StoredPayload never crosses HTTP/MCP.
```

`HydrateBatch` loads distinct message/snapshot keys in bounded `IN` batches (at most 200 bound keys per query to stay below conservative variable limits), after closing the event rows. At most 1 event query plus ceil(unique bodies/200) body queries for one read, with snapshot queries charged separately only when needed; no query per recipient. Preserve input ordering. Use request/batch-local caching first, bounded by total body bytes (proposed 8 MiB); process a larger page in chunks rather than caching everything. Do not add an unbounded process cache or hide stale data behind it. Read event metadata, referenced bodies and purge floors in one consistent read transaction; use a deferred/read-only path distinct from `store.Tx`'s `BEGIN IMMEDIATE`, and test its actual driver behavior. A writer transaction passes itself to the hydrator. Close every row iterator before nested lookups: the pool is only four connections today.

Missing/corrupt body is an internal storage error: return no partial page and do not advance/ack a cursor. Bodies intentionally removed by retention must have had their referencing events pruned atomically first, causing the documented cursor error instead. Cache keys include immutable version/hash; do not refresh tokens or consult current directory, reactions, public URL or thread title during hydration.

| Surface | Current path and required integration |
|---|---|
| SSE and long-poll | `httpapi/events.go` calls `Log.Read`; keep the current `sseFrame` and JSON renderer. Same event id/type/at/data, frame delimiters and next_cursor. |
| HTTP/operator inbox and history | `opview/inbox.go`/`threads.go` and `hub/history.go` already read `messages.body`; preserve current excerpts, sender_info and reaction overlays. Do not introduce event hydration into these queries. |
| MCP inbox tool | `mcpserver/tools.go` reads Events then applies `dataWithoutBlobURLs`; hydrate **before** the existing token stripping. Its output intentionally differs from the raw HTTP Delivery. |
| MCP inbox resources/subscriptions | `mcpserver/resources.go` uses `inboxTail`/`tailStart`/`Log.Read`; use owner-aware floors, preserve notification and resources/read bytes and tail sizing. |
| MCP Events subscribe/push | `events/worker.go` reads through `Log.Read`, then filters and builds the callback/preview. Preserve `event_id`, `data`, ordering and oversized preview rules; `events/subscribe` replay/truncated behavior uses the same owner floors. |
| Direct push/webhooks/Grok Bot | `hub/deliveries.go:ClaimDue` currently joins `events.data`; batch hydrate in the existing claim transaction, return exact `DueDelivery.Body`. Attempt number in the frozen body stays 1. |
| Operator notifications | `hub/notify.go:notifyOperatorDeliveries` directly selects `e.data`; collect rows, close them, hydrate, then call the current renderer. Preserve dispatching gates. |
| TUI/web/CLI watch | `tui/data.go`, `tui/screen_inbox.go`, `ui/static/store.js` and CLI watch consume the public Events and opview projections. Keep tags, invalidation, excerpts and raw-log behavior; test their existing fixtures against both storage modes. |

MCP `event_outbox.body` is intentionally an exact durable callback copy, including its preview wrapper. Leave it unchanged for in-flight/retry rows: signatures may use a new timestamp, but callback body and webhook-id cannot change. Deduplicating this second queue is a separate measured follow-up. It must be included in bytes/send reports for subscribed recipients rather than hidden from the savings claim.

### 3. Online backfill and rollback

Schema migrations do only additive DDL/short metadata writes; no whole-table SQL JSON rewrite or `VACUUM` during startup. Deployment stages:

1. Ship a dual reader with new writes/backfill disabled; verify all surfaces and create a consistent external backup. Old binary can still run at this stage because rows remain inline.
2. Enable reference writes on the sole writer hub. Once the first reference exists, rollback requires the dual-reader binary or a bounded reverse conversion to inline with enough disk; an old binary is no longer safe.
3. Capture an initial events high-water mark. Resume scan by `seq > last_seq AND seq <= captured_high`, only inline delivery rows. New sends are already references. Default slice limits: 100 events, at most 1 MiB of source JSON, target 50 ms writer time, one transaction per slice, sleep/yield between slices; stop on cancellation. A single larger allowed event can occupy its own slice. These are initial operator settings, tuned by Task 8 measurements.
4. For each row, parse and verify `hydrate(candidate) == original` and verify existing body hash if available. A pre-existing hash mismatch is a diagnostic/manual-review exception, not permission to rewrite the hash. Insert any required snapshot and update the event plus checkpoint in the same transaction. Check the original format/data in the update predicate. Delete/prune races are harmless; conflicting mutations retry. Count converted/skipped/failed rows, bytes removed and last seq, never log payloads/tokens.
5. Save captured high and last processed seq in metadata. A restart reuses them, so work is bounded; an explicit rescan handles skipped rows only after a fix. No failed row may disappear behind an unrecorded checkpoint: durable ids-only exception table keyed by seq plus reason, separately resumable; transient errors roll back the slice.
6. Pause backfill before low disk or excessive WAL/lock pressure. Proposed gate: available bytes must exceed max(64 MiB, four times configured slice-byte limit) plus current WAL bytes; do not treat this operational threshold as a worst-case SQLite allocation bound. Catch SQLITE_FULL, roll back, report paused, and require capacity relief; do not run a full shrink to recover space automatically. On a nearly full 1 GB volume, expand it or use an external maintenance host first.
7. After reaching high-water, verify no unaccounted eligible inline rows remain, sample raw-byte equivalence, run integrity/foreign-key checks, checkpoint when readers permit, record schema/backfill state and backup again. Drop no compatibility decoder in this release.

Backups must use SQLite online backup or a quiesced consistent DB+WAL procedure, never copy a live main file alone. A backup during migration is valid mixed-format state including snapshot rows and checkpoint. Restore with the dual reader and resume; re-run checkpoint idempotently. Rollback/export must include version metadata and snapshots. Keep backup output outside the small Fly volume. Full logical reverse conversion can expand the DB back to its original size, so restoring the pre-feature external backup is the emergency downgrade path, with explicit acknowledged-data-loss consequences if later sends exist.

### 4. Retention contracts and defaults

The approved defaults below apply on upgrade as well as fresh installs. Retention settings are typed hub preferences editable by the operator at runtime, validated on every change and visible with their effective value/source in the operator storage-status view. Flags/env in `cmd/vecnet-hub/main.go` and `hub.Config` supply initial defaults only; a stored preference wins once set, including explicit zero/false. `0` disables expiry for a duration retention class. Maintenance limits, idempotency and the existing optional hard unacked-event limit remain ordinary validated config in this slice; the preferences contract below covers acknowledged events, history and archive mode.

| Setting | Default | Meaning |
|---|---:|---|
| `IdempotencyRetention` | 24 h | Preserve the existing `IdempotencyWindow` contract. Configurable extension to 48 h or longer; never accept a value below 24 h. `0` keeps keys forever. Lookup and cleanup share this policy. |
| `AckedEventRetention` / `retention.ack_events` | 7 days | Minimum age before an acknowledged event prefix is eligible, with retry pins overriding eligibility. `0` disables event pruning. |
| `UnackedEventRetention` | 0 | Unacknowledged history never expires automatically by default. An operator may choose a hard 30/90-day limit, accepting resync/truncation; active retry/held pins still win. |
| `MessageHistoryRetention` / `retention.history` | 0 | Keep history forever by default for existing/self-hosted hubs; hosted deployments may explicitly set 90 days after backup/export and user policy are established. No silent 30-day switch on upgrade. |
| Audit (fixed policy) | forever | Append-only audit retained indefinitely. No audit expiry or deletion setting in this work; privacy erasure is designed separately later. |
| `ArchiveMode` / `retention.archive_mode` | false | Explicit operator switch to retain all messages and their history; overrides finite history expiry as specified below. |
| `MaintenanceBatchRows/Bytes/Target` | 100 / 1 MiB / 50 ms | Bounded backfill and cleanup slices; target is measured, not a guaranteed statement timeout. |

The default shrinks duplication and acknowledged event history, **not total lifetime history to a fixed size**. A 1 GB volume still needs capacity alerts. Message TTL/delivery expiry remains a loop/delivery limit, never the history policy. No worker silently changes deployment defaults.

#### Typed hub preferences seam (API and web UI follow separately)

[Hub preferences](2026-11-05-hub-preferences.md), Tasks 1/2/4, exclusively owns registry, store migration, validation/audit and preview/confirm API. This plan consumes its exact seam:

```go
snapshot, err := preferences.LoadSnapshot(ctx, tx) // *hub.Preferences; current query/tx
// On error skip expiry, never choose a shorter fallback.
ack, err := snapshot.Get("retention.ack_events") // prefs.Effective, error
// Effective: Value prefs.Value, Source prefs.Source, Origin string, Revision uint64.
// Source = default|flag|stored; env uses flag with nonsecret env Origin.
wakeups := preferences.Subscribe(ctx) // <-chan uint64; reread latest snapshot
// Mutations belong only to ReadPreferences / PreviewPreferences /
// PatchPreferences(ctx, caller, expectedETag, prefs.Patch), as specified by owner.
```

Read all three retention keys from one `prefs.Snapshot`; a local `RetentionPolicy` projection retains its revision and activation bounds, never a second Get/Set API or store. `prefs.Validate` and complete-policy validation are owned by prefs. Private `Preferences.notify(revision)` publishes only after atomic values/revision/activation/audit commit; wakeups coalesce and cannot authorize deletion. Use owner methods/types verbatim; no `prefs.RetentionSnapshot`, standalone `prefs.Get/Set` or duplicate migration. Tasks 5/7 depend on prefs core/confirmation and must not activate cleanup until their consumer adapters pass. HTTP/CLI/web use operator checks initially and admin parity's `admin:prefs` hub capability once available; MCP tools belong to admin parity.

| Typed preference key | Type / built-in default | Validation and consumer |
|---|---|---|
| `retention.ack_events` | duration in integer seconds / 604800 | `0` or 3600..31536000; `0` retains events indefinitely; positive values are minimum acknowledged-event age. Retry pins and owner-prefix rules always apply. Task 5 reads this value. |
| `retention.history` | duration in integer seconds / `0` (forever) | `0` or 86400..315360000; positive values opt into history expiry only when archive mode is off and any required expiry confirmation is satisfied. Task 7 reads this value. |
| `retention.archive_mode` | boolean / false | Strict boolean, never truthiness of a string. Task 7 applies its preservation override before any marking, hiding or sweeping. |

Reject unknown keys, wrong types, negative/overflowing durations and malformed duration encodings on writes and initial default loading. Validate the complete prospective policy, including confirmation requirements, before commit. Audit every preference change with authorized actor identity, key, old/new typed values, old/new effective source, policy revision and confirmation outcome; audit and successful mutation commit atomically. Failed confirmation cannot alter policy. These values are non-secret; never include request bodies, auth tokens or message content in audit. A reset to defaults counts as a policy change and must obey the same confirmation rules; a flag/env change or restart cannot bypass them.

Every retention ticker resolves preferences at the start of each run, with a fresh consistent policy check inside each destructive writer slice. Serialize preference changes with that check/delete transaction so enabling archive mode stops subsequent expiry slices, including a sweep whose marks predate the change. No restart is needed and no long-lived cache may retain an old destructive policy. Subscribe wakeups refresh status/policy only; retrospective trim waits for persisted `not_before` at the next nightly sweep (default maintenance schedule 02:00 UTC, owned by prefs), including after restart. On preference read/validation failure, skip expiry and report the error; never fall back to a shorter retention. Status exposes configured history duration, effective history duration, archive mode, source, policy revision and any pending confirmation, even while the override masks finite history.

The prefs registry also defines `blobs.expiry`, consumed by R2 from this same store: enum `1h|6h|12h|1d|7d|30d|off`, default `1d`; only `off` disables blob expiry (numeric 0 is invalid). R2 storage quota is strictly positive bootstrap config, default 5 GiB, not another preference. Its blob lifecycle and any attachment-byte archival guarantee belong to R2; this archive mode preserves message/history records and frozen attachment metadata/URLs, and does not silently promise permanent hosted blob bytes.

#### Archive mode and confirmed exit

Archive mode retains all messages and their history (including revisions/snapshots and associated delivery/reaction history needed to read them). It overrides any opt-in finite `retention.history` without erasing that configured preference. While enabled, do not mark history expired, hide history by age, sweep previously marked history or remove its history dependencies. Disable any opt-in hard unacknowledged-event expiry too. Already erased data cannot be recovered by enabling archive mode; report that limitation explicitly.

**Acknowledged events may still be pruned after the effective `retention.ack_events` age (7 days by default), with the same pins and owner floors.** They are operational replay records; messages remain readable in history. Archive mode therefore does not promise indefinite cursor replay of old events. Operators needing that also set `retention.ack_events=0`; all unacked events remain retained in archive mode. Existing idempotency cleanup remains independent, and audit stays append-only/forever. This preserves useful operational cleanup while making the message archive guarantee explicit.

Turning archive mode off always requires the prefs owner's preview → explicit confirm → If-Match PATCH, even with all expiry disabled. The default `apply_existing:false` preserves pre-change rows under their former policy and authorizes no retrospective trim. New/shorter retention (including forever → finite, reset/default or bootstrap changes) needs its own preview/confirmation; a restart cannot bypass activation bounds. To trim existing data, the human makes a separate explicit choice and confirms the exact revision, cutoff and high-water with counts/pins and backup/export guidance. Keep archive effective until the confirmed change commits. With expiry disabled confirmation cannot pre-authorize later finite expiry. No PATCH/confirm/wakeup performs deletion: approved retrospective work first becomes eligible at the **next nightly sweep after confirmation**, persisted via the owner's activation `not_before` (separate from prospective `effective_from`) and fixed bounds. Recheck revision/authorization/pins inside every bounded slice; protective changes cancel incompatible unconsumed approvals. Bootstrap changes introducing destructive eligibility remain pending until approved. The prefs plan owns this mechanism, not a DB-specific token or write endpoint.

For peer P, derive its effective pull acknowledgement from `acks(P,'*')`, `acks(current_principal,'*')`, and `acks(current_principal,P)`, taking the maximum valid current-epoch sequence. A valid ack means accepted by the owner, not by every independent client; that matches the existing shared ack model. Do not mistake `read_at`, a receipt, a subscription scan cursor or `last_seen` for a pull ack. For a live verified MCP subscription, separately require its cursor to have passed the candidate or an exact durable outbox record to cover it. Paused/expired subscriptions retain their opportunity by default while unacked retention is infinite; an explicit hard retention policy can truncate their backlog using the existing `skipPurged`/`truncated` path. Do not call an MCP scan cursor a delivery acceptance.

Maintain `event_retention(owner PRIMARY KEY, purged_through_seq NOT NULL, updated_at)` with one monotonic floor per peer. Delete only an eligible prefix in that peer's ordered log; stop at its first too-young/unacknowledged/pinned event. Remove floor rows only as part of a separately specified peer deletion/reset, never just because no events remain. Initial floors are 0 (plus any known prior global purge floor at migration). Do not infer gaps from seq arithmetic: other peers naturally occupy intervening seq values.

**Cursor behavior:** Add owner-aware `OldestFor(ctx, owners)` and transaction helpers. `Read`, `Current`, default start-cursor resolution, MCP `tailStart`, subscribe validation/replay and outbox cleanup must stop using global `min(events.seq)` as proof of availability. For one owner, oldest_cursor is that owner's highest removed sequence; `after == floor` is valid. For merged principal streams it is `max(purged_through_seq)` over the current owners. `after < floor` is HTTP 409 `cursor_purged` with `details.oldest_cursor`; after that cursor yields all retained events **above the combined safe floor**. Older events still retained for another owner cannot be replayed through that merged cursor; explicitly re-sync that peer/thread history or use a narrowed stream. Document this conservative merged-stream limitation in §2.3: a scalar global cursor cannot encode different missing prefixes. Do not present this as recovery of every retained event below that floor. A fresh stream without a saved ack uses the same safe floor, while explicit history reads remain available. Preserve current epoch, AUTOINCREMENT and global seq ordering; never renumber rows. A pre-existing stale ack gets the same cursor_purged response, not an implicit silent rewind. Cross-epoch errors use the owner-aware safe floor too.

Read events, floor and bodies from a consistent snapshot; purge and floor advancement commit atomically. An in-progress read may complete from its older valid snapshot; the next read sees the advanced floor. A read after purge cannot silently skip a gap. Empty owner sets, empty logs, owner additions/removals, disabled peers, ack scopes, `head`, equal/future cursors and epoch mismatches need explicit tests. Preserve global `Oldest` only for diagnostics/legacy callers until every semantic caller has been audited; do not feed it into per-subscription tombstone deletion.

**Pins/deletion ordering:** Pending direct deliveries (`queued`, `pushed`, held wake_idle, or `dispatching > 0`) pin their event and Message. In-flight operator notifications pin via the existing dispatching mechanism. Outbox pending/parked/retryable-exhausted records retain their exact bodies; preserve terminal suppression tombstones until the **owning peer's** floor has passed the original event. A callback row's materialized bytes allow event pruning only when its delivery gate/state dependencies remain readable. Initially pin its Message/delivery as well; optimize this only with separate proof. Delivery history rows are kept with message history; terminal rows may be removed only by the history GC after all event/outbox/receipt dependencies are resolved. Message retention cannot delete a Message referenced by any retained delivery event, mutable approval/job/reaction operation, held delivery, active retry or retained blob-reference contract. Collect dependencies inside the same writer transaction; where no FK exists today, add explicit checks and tests rather than assume cascading deletes.

Message retention is a two-stage mark/sweep: age makes a row history-expired, then references decide physical deletion. Store per-thread `last_allocated_seq`, initialized from max existing Message seq and never decreased. Both ordinary and reaction insertions allocate from it; participant joins also use it. `message_count` is lifetime non-reaction count and cannot substitute for it. History GC must not reset budget/count/activity or reuse seq after deleting a thread's newest/all messages. Retain compact message locator tombstones (`msg_id`, `thr_id`, `seq`, `expired_at`) while the thread exists so `after=<purged msg_id>` resolves to its original seq; history pages return the remaining retained suffix with the current shape. Direct reply to an erased Message returns the existing not-found behavior. Keep thread/participant rows and high-water state; retiring whole threads is a separate policy. Test tombstone growth and report it; finite body retention does not mean zero metadata growth.

Sweep references in safe order: terminal/suppression outbox cleanup using owner floors; event prefix deletion and floors; removable terminal deliveries; history body/legacy snapshot removal; reactions/reaction_wakes and inactive job/approval references only when their semantics allow retirement; `blob_refs` removal last with Message removal, then the existing blob GC. Active reaction wakes must not reappear because their dedup row was pruned. Preserve any still-needed job_key/approval dedup state. For initially unsupported dependency classes, leave the Message pinned and report the reason. Recheck pins in each delete transaction. Export before enabling finite history, retain external backups under a separate expiry policy, and explain that backups/exports may still contain expired text and blobs.

### 5. Other per-send state and index decisions

`IdempotencyWindow` is already 24 h, but `idempotencyLookup` only removes an expired key when it is reused. Add bounded time-indexed cleanup for never-reused keys; response fields are ids, delivery refs and spilled/job status, **not the Message body**. Keep exact response bytes and request hash until expiry, and serialize lookup/pruning with the send transaction. No change to replay/conflict behavior within the configured window.

The current `audit.Record("send")` already contains message/thread ids and `{ingress, deliveries, new_thread}`; PROTOCOL forbids bodies/secrets there. Preserve this compact record and audit behavior, add a guard over send/ack/source/admin events to forbid Message content or secrets, and measure its actual bytes. Do not claim savings from deleting bodies that are not present. Audit is append-only and kept forever; no audit deletion or expiry is implemented here. Privacy erasure requires a separate later design.

`insertMessage` already updates thread message_count, budget_remaining and last_message_at in **one UPDATE**. Keep those synchronous writes in the send transaction. Do not defer budget/activity or replace lifetime counters with `count(messages)`. A new thread's initial INSERT may absorb this one update if all values/side effects are known at creation and a real benchmark shows a win; existing-thread sends keep one atomic update. Coalesce repeated touches to the same addressed group within a send only if duplicate target resolution currently permits them. Neither change is required to unlock reference storage, and neither earns assumed durable byte savings.

**Index verdict:** Drop **none** in the initial release. They are not redundant merely because column prefixes overlap. The following build-box EXPLAIN evidence was collected during this planning run with `BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 python3 -c '<in-memory migration + EQP probe>'`, Python SQLite **3.46.1**, all current SQL migrations applied to an empty disposable DB. These are access-path evidence, not populated modernc latency measurements:

| Index kept | Probe/result |
|---|---|
| `events_owner_seq(owner,seq)` | owner + seq range ordered read: `SEARCH events USING INDEX events_owner_seq (owner=? AND seq>?)` |
| `sqlite_autoindex_messages_2(thr_id,seq)` | thread-history range: `SEARCH messages USING INDEX sqlite_autoindex_messages_2 (thr_id=? AND seq>?)`; preserve message PK autoindex too |
| `deliveries_recipient_dly(recipient,dly_id)` | inbox pagination: `USING COVERING INDEX deliveries_recipient_dly (recipient=? AND dly_id<?)` |
| `deliveries_unread(recipient) WHERE read_at IS NULL` | unread count: `USING INDEX deliveries_unread (recipient=?)` |
| `deliveries_msg(msg_id)` | message delivery lookup: `USING INDEX deliveries_msg (msg_id=?)` |
| `deliveries_recipient_thr(recipient,thr_id,created_at)` | same-thread queue predecessor predicate: `USING INDEX deliveries_recipient_thr (recipient=? AND thr_id=?)`; event_seq still residual, so this is a future replacement candidate, not a proven duplicate |
| `deliveries_due(state,next_attempt_at)` | due range: `USING INDEX deliveries_due (state=? AND next_attempt_at<?)` |
| `deliveries_expiry(state,expires_at)` | expiry range: `USING INDEX deliveries_expiry (state=? AND expires_at<?)` |

Keep `event_outbox_pending`, `event_outbox_done`, `audit_action_at`, `blob_refs_msg`, and all existing PK/UNIQUE constraints. Also keep the remaining existing named indexes unchanged: `peers_prn`, `credentials_subject`, `team_members_peer`, `group_members_peer`, `thread_participants_peer`, `routes_owner`, `approvals_open`, `oauth_codes_expires`, `oauth_tokens_rotated_from`, `route_runs_route_at`, `reaction_ops_peer_at`, `peers_created_by`, `avatar_ops_peer_at`, `event_subscriptions_peer_expiry`. They are outside the demonstrated duplication issue; no evidence supports dropping them. Inspect the merged schema for parallel additions and record an explicit keep/drop verdict for those too.

Add only maintenance indexes with demonstrated queries: `idempotency(created_at)` for bounded expiry; typed event reference indexes on non-null msg/snapshot ids for GC; an appropriate message-age scan index if retention needs one. Plan benchmarks must charge their page bytes/write cost. Do not add an event-time index by assumption: per-owner scans already have `events_owner_seq`. A proposed queue index `(recipient,thr_id,event_seq)` or partial live-state variant must replace the existing one only after full ClaimDue predecessor/inbox/opview/history EXPLAIN and latency tests prove no regression for queries using created_at. Preserve the old one otherwise.

### 6. SQLite compaction and disk headroom

Retain WAL and synchronous FULL defaults. Set/observe `wal_autocheckpoint` explicitly (initial proposal 1000 pages per connection, approximately 4 MiB at 4096-byte pages), and expose page_size, page_count, freelist_count, DB/WAL sizes, checkpoint progress/busy readers and free filesystem bytes. Checkpoint is not vacuum: deleting rows makes reusable pages without shrinking the file. Run bounded PASSIVE checkpoints between maintenance slices; allow RESTART/TRUNCATE only in an operator window when readers permit. Never force a long read off merely to truncate WAL; pause the backfill and alert on growth.

New empty databases: choose `page_size=4096` and `auto_vacuum=INCREMENTAL` **before tables are created**, verify actual values after initialization, and keep per-connection checkpoint pragmas in the DSN. Existing databases: inspect values and retain their current page size. Changing an existing NONE database to INCREMENTAL needs a full `VACUUM`; setting the pragma alone does not retrofit pointer maps. It is not an online additive migration. Backfill works without that conversion and usually reuses freed pages for future traffic. With incremental mode already enabled, reclaim at most 128 pages per maintenance slice via `incremental_vacuum`, respecting disk/latency limits; report actual reclaimed pages and WAL writes. Page moves can generate WAL and still need checkpoints.

**Deferred reference runbook, not a task or rollout step in this release:** No offline NONE-to-INCREMENTAL conversion now; reuse freed pages first. If a separately approved future conversion/shrink is needed, drain and stop the hub, checkpoint, take a consistent external backup, ensure temporary output is on a sufficiently large external disk/build host, open with the desired auto_vacuum and page-size pragmas, rebuild/vacuum, verify data, schema checksums, identity/meta, retention floors, sqlite_sequence and pending callback bytes, then atomically install on the stopped volume and restart the dual reader. SQLite `VACUUM` can need up to roughly **twice the original DB size in additional free space**, plus WAL/journal/temp and backup needs; do not attempt it on a nearly full 1 GB volume. `VACUUM INTO` needs a second complete compact DB plus temp/WAL headroom and a coordinated final cutover; a live output without a write freeze is a point-in-time copy, not a replacement containing subsequent sends. Prefer moving the stopped consistent database to a larger maintenance host, rebuilding there, and copying the verified compact result back, or expand the Fly volume. Same-filesystem atomic rename requires enough room for old and new DB simultaneously; otherwise use a stopped copy with a separately verified external rollback backup. Never delete the only good DB to make room.

Keep compression disabled. Body references remove the fan-out multiplier first; then measure zstd/flate on realistic entropy, escaped JSON, already-compressed attachment metadata and spilled previews. A separate optional proposal must show net physical-page savings, p95 send/read/push CPU and allocation costs on x-boss and representative hub CPU, plus codec-version/backup/upgrade handling. Stdlib flate avoids a new module but can still cost too much CPU; zstd requires a reviewed dependency. Any codec stays behind hub storage and the `vecnet` CLI remains sqlite-free.

### 7. Expected bytes/send and measurement contract

**Historical planning model:** the arithmetic below predates implementation. Task 8 now supplies the [measured modernc receipt](../business/db-compaction-bench-2026-11-07.md); Task 9 updates [pricing §1.1](../business/pricing-analysis.md#11-message-and-sqlite-measurements) with selected comparable physical pairs. The logical forecasts below are preserved as assumptions, not relabeled as measurements.

Physical SQLite allocation is discontinuous at page/overflow thresholds. Do not extrapolate one price-analysis row across different body sizes or call these arithmetic estimates benchmark results. Let F be recipient count and M the canonical Message byte count. The legacy durable body term is `(F+1)*M`; new normal storage has `M`, saving exactly `F*M` **before** reference/index overhead and attachment overrides. The event envelope/thread snapshot still costs O(F); retained delivery rows still cost O(F).

Illustrative logical schema arithmetic for 2 KiB text: M=2479 bytes (pricing fixture), E=316 bytes for the old Delivery envelope excluding Message, R=650 bytes per new internal reference including JSON escaping and hashes (initial estimate to be measured), C=600 bytes per delivery row/index keys excluding the event envelope, B=500 bytes fixed audit/message-row/thread amortization, I=180+120F bytes for an idempotency row/response. These C/B/I figures are explicit assumptions from id/timestamp/hash widths and index-key copies, not dbstat results; they omit page fill, headers and additional receipts/subscriptions. Formula before = `(F+1)M + F(E+C) + B+I`; after immediate = `M + F(R+C) + B+I`; after 24 h idempotency cleanup = `M + F(R+C) + B`. This conservatively charges the reference instead of double-counting the old envelope. Rounded decimal KB (1000 bytes):

| Fan-out F | Before logical bytes/send | After reference, immediate | After reference + expired idempotency | Immediate reduction |
|---:|---:|---:|---:|---:|
| 1 | 6,674 (6.67 KB) | 4,529 (4.53 KB) | 4,229 (4.23 KB) | 2,145 (32%) |
| 2 | 10,189 (10.19 KB) | 5,899 (5.90 KB) | 5,479 (5.48 KB) | 4,290 (42%) |
| 5 | 20,734 (20.73 KB) | 10,009 (10.01 KB) | 9,229 (9.23 KB) | 10,725 (52%) |
| 20 | 73,459 (73.46 KB) | 30,559 (30.56 KB) | 27,979 (27.98 KB) | 42,900 (58%) |

For the 4 KiB-text fixture M=4527, add 2048*(F+1) to every before cell and 2048 to each after cell; immediate savings become F*(4527-334). These are forecasts only. The existing measured physical allocations remain **14,651 bytes/send at F=2, M=2479; 19,591 at F=3, M=2479; 35,176 at F=5, M=4527**. They exceed this logical model because pages, indexes and workload shape matter. There is no measured physical before/after figure yet for F=1 or 20, and no verified physical after figure for any F. Task 8 must replace forecasts with bench-hub's apples-to-apples physical numbers. Do not subtract logical savings directly from allocated-page results.

After 7-day event pruning, a fully acknowledged settled cohort can lose its remaining reference event/envelope storage too; retained Message/delivery history and optional outbox tombstones still remain. Report retention savings by lifecycle cohort, not as if every send instantly has zero event cost. Reference savings with many legacy snapshots or large recipient attachment overrides may be lower; report the exception rate and actual R distribution.

## Global Constraints

- This assignment changes only this plan and writes the requested result JSON; implementation below is future work. Commit the plan on `plan-db-compaction` only. Do not merge or deploy. No `attn`, secrets, live-DB reads/writes, config edits or background measurement on this PC.
- Every future build, test, query-plan/performance/storage measurement in this plan runs on the build box using `BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 <command>`. Go commands also pass `CGO_ENABLED=0` inside the remote command and `-p 2`. Race checks use `CGO_ENABLED=1 CC=/usr/bin/gcc` on that box. Never benchmark the live Fly DB or the dev hubs.
- Fixtures live in lane-private temp dirs with injected clock/config/env and fixed synthetic identities; never use real credentials, local user config, `.data/`, `.demo-data/`, ports 45752/49575 or a shared UI/browser session. Bodies/URLs/tokens may appear in synthetic golden files only, never real operational diagnostics.
- Preserve every existing surface's **own** bytes, not equality between intentionally different surfaces. Keep MCP token stripping, oversized previews, field omission/null/array semantics, blob authorization and recipient scoping. Keep outer public schemas and event types unchanged for storage-only changes. Protocol retention clarifications are explicit Task 9 work.
- Keep event seq/epoch/order stable; after-commit wakeups remain after commit. No dropped Events on hydration errors. Never recompute replay using mutable thread/peer/delivery state. Existing exact-byte outbox bodies remain exact-byte bodies.
- Always use passed transaction/query handles; close rows before further queries. Additive migration first; reference writes, backfill, retention and physical vacuum are independently enabled. Startup recovery resets dispatching as today before maintenance starts. No whole-table backfill in an embedded migration.
- Never hardcode migration numbers: next free at build time, reconcile hub-notices/R2/mcp-first on master. No edits to previous migrations. Add no store/eventpayload imports to CLI/client/TUI packages; check the actual command dependency graph.
- Defaults retain message and audit history forever, unacked events forever, idempotency at least 24 h and acknowledged event prefixes for 7 days. Archive mode overrides history expiry and hard unacked-event expiry; acknowledged-event pruning may continue with explicit cursor replay limits. Runtime retention preferences override initial flags/env and confirmed exits are mandatory. Operator expiry never overrides active retry/held/in-flight safety pins. Expose stuck/pinned growth rather than quietly breaking guarantees.
- Each task writes behavior tests first; **(guard)** tests pass on the inline baseline and stay passing. Other tests fail before implementation. Capture byte fixtures before changing production serializers; updating goldens to match new output is not proof of compatibility. A task ends only when its focused checks pass and its changes are committed.

## Review Focus

1. Every path selecting `events.data`, including `ClaimDue` and operator notifications, hydrates internally; no storage discriminator escapes. Guard raw-byte equality rather than only JSON semantic equality.
2. Blob URLs and thread summaries remain frozen; renamed peers, changed public URL/clock, current reactions, hub-authored senders and old migration output cannot alter retries.
3. Owner-scoped purging cannot silently skip holes. Principal-wide floors and historical resync limits are documented. Ack scope is not confused with delivery read state or MCP scan progress.
4. No pending webhook, parked wake_idle, retryable exhausted callback, dispatching notification, active approval/job or reaction dedup contract loses its Message/delivery/event dependencies.
5. Restart/SQLITE_FULL/interrupted backups resume at a committed checkpoint. Full rebuild and rollback need external disk and a stopped cutover; a pragma does not retrofit incremental vacuum.
6. History retention never reuses thread sequence, resets budget, grants from_join access or breaks pagination through a retained locator tombstone. Body retention and compact metadata retention are reported separately.
7. Shared `Effective` value/source/revision, `Subscribe` rereads and prefs-owned validation/audit/preview are used verbatim; no duplicate store. Every archive exit/new or shorter policy is confirmed; existing-data trim waits for the next nightly sweep. Archive preservation applies to old expiry marks and concurrent slices; stored preferences win over flags/env, updates take effect without restart, and exiting archive cannot bypass revision-bound confirmation. Audit is never expired.
8. Savings claims distinguish logical bytes, allocated pages, WAL write traffic, retained retry copies and steady-state expiry. Index drops require actual query-plan evidence on the merged schema and populated modernc benchmarks.

---

### Task 1: Freeze baseline contracts and inventory readers

**Depends:** none.

**Files:** Add `internal/eventlog/reference_guard_test.go`, `internal/hub/delivery_reference_guard_test.go`, HTTP/MCP/Events guard tests and synthetic `testdata` fixtures in their respective packages; add a storage-reader inventory to this plan or a companion implementation receipt only when implementation begins. Inspect `internal/opview`, `internal/tui`, `internal/ui` readers and existing goldens without rewriting expected output.

**Interfaces:** Existing `Log.Read`, `ClaimDue`, notifications, HTTP SSE/long-poll, `vecnet_inbox`, inbox resources, Events callbacks/subscribe, opview inbox/history and TUI/web readers. Fixtures use deterministic ids, times and synthetic blob keys.

- [ ] **Step 1 (guard):** Capture exact baseline bytes per surface for plain Unicode/escaped JSON, nil/empty optional values, source/operator/hub messages, reaction Deliveries, receipts/presence, hosted+reference-only attachments, oversized Events previews, held delivery release and webhook retry. Assert hashes and frozen URLs after clock/public URL/directory/thread changes. Preserve the existing `TestInboxCarriesNoBlobToken` distinction.
- [ ] **Step 2 (guard):** Capture principal/peer/narrowed cursor behavior, mixed event ordering, empty/head streams and outbox replay/exhausted suppression. Inventory every raw event-data SQL read with `rg`; explicitly record notifications and ClaimDue.
- [ ] **Step 3:** Run focused guards on the build box; all pass on the unchanged inline implementation. Store fixture provenance and do not regenerate these during later tasks.
- [ ] **Step 4: Commit** `test(storage): freeze delivery and event wire contracts`.

---

### Task 2: Additive schema and exact-byte hydration module

**Depends:** 1.

**Files:** New next-free SQL migration(s), `internal/store/migrate_sequence_test.go`, `internal/store/store_test.go`, new `internal/eventpayload/{payload.go,payload_test.go}`. Modify `internal/store/store.go` only for a proved read-snapshot/query-handle facility if needed; no writer cutover yet.

**Interfaces:** `StoredPayload`, `EncodeDelivery`, `HydrateBatch`; typed event format/ref columns; immutable snapshots and body update protection enabled at cutover. Inline remains default. Read snapshots must not acquire the writer lock through the existing immediate transaction path.

- [ ] **Step 1:** Write failing tests for additive migration on fresh/upgrade DB, legacy unchanged bytes, malformed reference rejection, exact composition with nested/escaped/duplicate keys, attachment patches, legacy snapshot dedup, hash mismatch and nonexistent bodies. Verify old migration checksums remain valid.
- [ ] **Step 2:** Write failing tests for <=200-key query chunks, bounded byte cache, input order, snapshot consistency during deletion, passed transaction usage and no four-connection-pool deadlock. Test actual driver read transaction locking with a concurrent writer.
- [ ] **Step 3:** Run and observe failures, implement the module and schema. Assert body/column identity constraints and no bytes are printed in error messages. Enable the immutable update guard only after legacy migrations finish and reference writes become enabled.
- [ ] **Step 4:** Run `BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 CGO_ENABLED=0 go test -p 2 ./internal/store/ ./internal/eventpayload/`; baseline guards still pass.
- [ ] **Step 5: Commit** `feat(storage): versioned delivery references and batch hydration`.

---

### Task 3: Route all reads and new writes through hydration

**Depends:** 2.

**Files:** `internal/eventlog/log.go`, `internal/hub/send.go`, `deliveries.go`, `notify.go`, hub config/construction; corresponding tests; `internal/mcpserver/{tools,resources}*_test.go`, `internal/events/worker_test.go`, `internal/httpapi/events_test.go`, opview integration tests and existing TUI/web reader tests as appropriate. Inspect hub-authored/reaction call sites; use the shared fanOut rather than fork them.

**Interfaces:** `Log.Read` still returns public `envelope.Event`; `DueDelivery.Body` remains the complete frozen Delivery. New-write switch defaults off for the first deployment stage; dual reader always active. Encode/hash/store a message and all of its reference events in one send transaction, notify only after commit.

- [ ] **Step 1:** Add failing mixed-format end-to-end tests with reference writes enabled. Repeat Task 1 fixtures through every surface and compare raw bytes to frozen inline results. Test transaction rollback leaves neither dangling refs nor partial fan-out, and source/system senders hydrate without a peer lookup.
- [ ] **Step 2:** Add failing direct push and notification tests proving internal references never escape, attempts resend the same body/hash, MCP token stripping remains in place and materialized callback bodies remain unchanged across retry/config changes.
- [ ] **Step 3:** Run failing checks; wire batch hydration into `Log.Read`, `ClaimDue`, and `notifyOperatorDeliveries`, closing SQL iterators before batch lookups. No new public fields and no event serializer changes to make tests pass.
- [ ] **Step 4:** Run build-box Go tests for eventpayload/eventlog/hub/httpapi/mcpserver/events/opview/tui and existing web model tests using `build2 node --test --test-concurrency=2 internal/ui/jstest/`. Run the full baseline guard set in both storage modes.
- [ ] **Step 5: Commit** `refactor(hub): store delivery references behind stable event bytes`.

---

### Task 4: Resumable online backfill with disk-pressure stops

**Depends:** 3.

**Files:** New `internal/hub/storage_backfill.go` and tests; modify `hub/tickers.go`, config and `cmd/vecnet-hub/main.go`; add status fields through an operator-only diagnostic path. Use existing `meta` for durable progress plus an exception table in a next-free additive migration if not introduced in Task 2.

**Interfaces:** Bounded `BackfillDeliverySlice(ctx, limits)` returns progress/counts/paused reason; cancellation safe. Dry-run reports counts/ids/bytes only. Backfill enable is separate from reference writes and retention. Use injected filesystem capacity/checkpoint probes and clock.

- [ ] **Step 1:** Write failing crash-before/after-commit, repeat/resume, captured-high-water and concurrent-send tests. Add legacy sender_info/key-order/attachment URL fixtures, missing messages with recoverable embedded snapshots, unknown fields and invalid hashes; prove unconvertible originals remain inline with durable exception ids.
- [ ] **Step 2:** Write failing row/byte/time budget tests, cancellation and forced SQLITE_FULL rollback, low-headroom pause, WAL-growth pause and reader-lock contention tests. A backup restored mid-backfill must hydrate all rows and resume without changing any public bytes.
- [ ] **Step 3:** Observe failures, implement selection/conversion/checkpoint atomicity and slow-ticker slices. Update original format/data conditionally; no long startup rewrite. Persist and rescan exception rows deliberately.
- [ ] **Step 4:** Run focused tests and a disposable build-box backfill under concurrent sends/reads. Verify high-water/seq/hash stability and integrity; record worst transaction duration and required headroom without using a live hub.
- [ ] **Step 5: Commit** `feat(storage): resumable delivery backfill with capacity limits`.

---

### Task 5: Runtime preferences seam, owner-aware event retention and cursor safety

**Depends:** 4 (and its prerequisite 3), plus prefs Tasks 1/2/4 and runtime core; adapter acceptance completes jointly with prefs Task 3. This is not a dependency on the whole prefs UI/release. Preserve ticker integration order.

**Files:** Next-free floor-table migration, `internal/eventlog/{log.go,retention.go,*test.go}`, `internal/hub/{ack.go,tickers.go,config files}`, `internal/httpapi/events.go`, `internal/mcpserver/{resources.go,events.go}`, `internal/events/{store.go,worker.go}` and focused tests; hub preferences adapter/wiring and contract tests, coordinating shared-store migration ownership with the separate preferences plan.

**Interfaces:** Consume `Preferences.LoadSnapshot(ctx, *sql.Tx)` → `prefs.Snapshot`, `Snapshot.Get` → `prefs.Effective` and `Subscribe(ctx)` exactly as above, plus owner preview/confirmation/activation bounds; no preference writer. `OldestFor(ctx, owners)`, shared floor computation/query helpers, bounded prefix prune; consistent read snapshot; owner-aware subscription replay and terminal tombstone cleanup. Typed `retention.ack_events` via the shared preferences seam, defaults per Decisions; no preferences API/web UI implementation. Resolve a policy snapshot each run/slice; stored values override flag/env defaults. Archive mode suppresses hard unacked expiry while allowing acknowledged-event pruning. The existing cursor string, 409 reason/details shape, event ordering and MCP truncated response shape stay unchanged.

- [ ] **Step 1:** Write failing tests for interleaved two-peer logs where one peer prunes while the other's oldest row remains. Prove read-after-gap returns 409, not a silent partial result; exact floor resumes; no-prune bytes unchanged. Cover merged/principal/narrowed/default-ack/head/empty/disabled/epoch cases and explicit history resync below a combined safe floor.
- [ ] **Step 2:** Write failing ack eligibility tests for all three applicable ack scopes and invalid substitutes (read_at, receipt and MCP scan). Cover live/paused/expired subscriptions, filtered events, lagging cursors and existing `skipPurged` behavior with optional hard retention enabled.
- [ ] **Step 3:** Write failing purge-vs-read/ack/backfill/push races, held and dispatching pins, active/pending/retryable-exhausted outbox pins, and terminal suppression surviving until its peer floor passes. Test empty logs preserve sequence/floors across restart.
- [ ] **Step 3a:** Write failing preferences contract tests for types/validation, explicit stored zero/false precedence, persistence/restart, sources, atomic audit/change, live duration changes, archive blocking hard unacked expiry, continued acknowledged pruning, and read errors skipping expiry. Verify snapshot/revision checks serialize policy updates with each deletion slice, the shared source vocabulary is exact, and no notification/confirm can start trim before its nightly not-before.
- [ ] **Step 4:** Observe failures, implement transactional prefix deletion and floor updates. Audit every `Oldest` caller including MCP tailStart/subscribe and pruneDone; remove inappropriate global-floor decisions. Add lag/pin/oldest-age metrics to explain delayed cleanup.
- [ ] **Step 5:** Run focused eventlog/hub/httpapi/mcpserver/events suites and race checks on x-boss, then baseline guards. Commit `feat(storage): acknowledged event retention with owner cursor floors`.

---

### Task 6: Bound idempotency state and guard per-send writes

**Depends:** 3; integrate after 4/5 for shared files.

**Files:** `internal/hub/{send.go,tickers.go,send_test.go}`, hub config, next-free idempotency-age-index migration; `internal/audit/*_test.go`; focused thread/group tests. Update index inventory evidence after rebasing.

**Interfaces:** One effective idempotency policy shared by lookup and expiry, default 24 h and configurable extension/disabled cleanup; bounded indexed cleanup returns count and cutoff. Existing response/hash semantics unchanged. Audit stays compact/append-only and thread accounting atomic.

- [ ] **Step 1:** Write failing expiry-without-key-reuse tests, exact boundary `< cutoff` semantics matching lookup, replay/conflict just inside the window, reuse after expiry, 48 h/forever configuration and invalid <24 h duration. Cover cleanup racing a send in BEGIN IMMEDIATE transactions.
- [ ] **Step 2 (guard):** Assert send audit carries ids/counts/ingress only and cannot contain the synthetic content/secret sentinel. Assert failed sends, reaction sends, hub-authored sends and loop cuts preserve current budget/count/activity effects.
- [ ] **Step 3:** Observe failures and implement indexed bounded cleanup. Retain the one thread UPDATE; any new-thread coalescing requires independent evidence and all guards. Record all kept indexes; drop none without the populated query-plan gate from Task 8.
- [ ] **Step 4:** Run focused hub/store/audit tests and baseline guards on the build box. Commit `feat(storage): expire idempotency rows without changing send semantics`.

---

### Task 7: Optional message-history retention with safe sequence allocation

**Depends:** 6 (and its prerequisites 4, 5), plus the shared prefs confirmation/activation adapter.

**Files:** Next-free thread high-water/history-tombstone migration; `internal/hub/{send.go,reactions.go,history.go,threads.go,tickers.go}`, new `history_gc.go`, relevant opview/history readers, `internal/events/store.go`, blob dependency/GC tests, config and command flags. Coordinate actual blob adapters with the parallel R2 plan.

**Interfaces:** Lifetime thread sequence allocator, persisted history-expiry markers/tombstone locators, bounded dependency-aware body GC. Default message policy forever. Read typed `retention.history` and `retention.archive_mode` each run/slice through the Task 5 preferences adapter; archive overrides marking, age-based hiding and sweeping, including existing marks. Consume only confirmed policy changes from the shared write seam; the separate plan owns the preferences API/web UI. Physical retention is delayed by pins; diagnostics state which references block it. Deleting bytes never changes an existing retry payload.

- [ ] **Step 1:** Write failing tests deleting newest/all Message bodies then ordinary/reaction sends: seq must increase and from_join access must remain unchanged. Budgets, message_count and last_message_at do not move backwards. History pagination after a purged msg_id uses its locator and preserves response shape.
- [ ] **Step 2:** Write failing dependency tests for retained reference events, pending direct/Events pushes, held/dispatching rows, jobs/approvals, reaction_wakes, blob refs shared with retained Messages, hub-authored bodies and legacy snapshots. Unsupported dependency classes remain pinned with a reason; no broad cascades.
- [ ] **Step 2a:** Write failing tests for archive enable/disable with a finite history preference, previously expired marks, preserved revisions/history, concurrent sweep cancellation at the next slice, and runtime changes without restart. Plain false writes, resets, changed flags/env and restarts cannot bypass confirmation; stale revision/cutoff confirmations fail. Prove a confirmed retrospective cutoff waits until the next nightly sweep (including restart/wakeups), still respects pins, safe exit with expiry disabled cannot authorize later finite expiry, and shorter/forever-to-finite policies require a fresh preview/confirmation. Test read/validation failures preserve history and status shows configured/effective policy and pending confirmation.
- [ ] **Step 3:** Observe failures; implement allocator for **all** insertion/join paths before enabling deletes. Add archive-aware mark/sweep with bounded transactions, fresh preference checks and last-moment pin checks; preserve audit and dedup semantics. Remove blob_refs only when the Message truly dies, then let existing blob GC choose hosted-byte deletion.
- [ ] **Step 4:** Run hub/opview/events/blob/store suites and all guards on x-boss; restore/export a finite-retention fixture and check remaining-history visibility, locator pagination and active retry equality. Commit `feat(storage): opt-in history retention with durable sequence high water`.

---

### Task 8: Compaction controls, bench-hub comparison and release gate

**Depends:** 6, 7.

**Files:** `internal/store/{store.go,store_test.go}`, new bounded maintenance helper/tests, hub diagnostics/config, integration with the **actual** bench-hub files once merged (discover names then), benchmark result receipt under `docs/business/` or bench-hub's canonical output location. Do not invent a benchmark command as if it already exists. A temporary build-box-only driver is acceptable until the harness lands, with its source/parameters captured in the receipt.

**Interfaces:** Explicit new-DB page/auto_vacuum setup, existing-DB detection, per-connection WAL settings, bounded checkpoint/incremental-vacuum controls and ids-only metrics. Never auto-run full VACUUM. Bench profiles identify schema/head/config and lifecycle stage.

- [ ] **Step 1:** Write failing tests for new-DB INCREMENTAL/4096 setup, existing NONE preserved, reopened settings correct on every connection, bounded reclaim, reader-pinned checkpoint and disk-pressure stop. Test physical size/reuse behavior on disposable on-disk databases on x-boss.
- [ ] **Step 2:** Observe failures; implement safe settings and maintenance. Retain FULL durability; no production performance claim based on NORMAL. Verify existing NONE databases reuse freed pages without conversion and backup/restore preserves their mode. Offline NONE-to-INCREMENTAL conversion and its execution tests are deferred; do not schedule a rebuild or touch a real Fly volume.
- [ ] **Step 3:** Once bench-hub exists, run identical seeded sends at F=1/2/5/20, 2048/4096 text bytes, repeated and realistic varied bodies, new and reused threads, 0/1/multiple subscriptions, hosted/reference-only attachments, hub/source/reaction cases. Compare inline vs reference immediate, post-backfill/pre-vacuum, idempotency-expired, acked-event-expired, finite-history and post-reclaim cohorts. Use the mandated x-boss command prefix for every run.
- [ ] **Step 4:** Report `sum(length(...))` payload bytes, per-table and per-index dbstat page/overflow bytes where supported, page_size/page_count/freelist_count, DB and WAL bytes, checkpoint state and elapsed/CPU/allocations/p50/p95/p99 send/read/push. Distinguish file size from live pages and WAL write amplification. Repeat on modernc, populated/analyzed DBs; baseline Python EQP is supporting evidence only. Include full ClaimDue predecessor/inbox/unread/history/purge/idempotency query plans and charge every new maintenance index. No proposed drop without evidence; default keep remains valid.
- [ ] **Step 5:** Replace the illustrative R/C/B/I assumptions with measured distributions, document snapshot/attachment/outbox costs and explain any profile that gets worse. Acceptance: no durable full Message copy in a normal reference delivery event; positive physical live-page savings at F=2/5/20 in controlled comparable fixtures; no raw-byte/cursor/retry regression; p95 read/send/push regression >10% requires investigation and an explicit recorded tradeoff before release. F=1 savings must be reported even if page quantization makes them zero; never fake a lower file size while free pages remain allocated.
- [ ] **Step 6:** Run `BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 CGO_ENABLED=0 go test -p 2 ./...`, focused races with `CGO_ENABLED=1 CC=/usr/bin/gcc`, existing web model checks, and `just validate-protocol` through the same prefix. On the box run `go list -deps ./cmd/vecnet` and assert absence of `modernc.org/sqlite`, `internal/store` and `internal/eventpayload` in its dependency list. Commit `perf(storage): validate reference savings and bounded SQLite maintenance`.

---

### Task 9: Protocol clarification, operator runbook and staged enablement

**Depends:** 8.

**Files:** `docs/PROTOCOL.md`, `docs/DEPLOY.md`, `docs/OPERATOR.md`, `docs/business/pricing-analysis.md`, `CHANGELOG.md`; update this plan with the implementation receipt and actual migration names. No deployment in the planning assignment; executing this task prepares the operator reviewable rollout.

**Interfaces:** Public storage/retention config and status field spellings match implemented code. Protocol amendments clarify existing cursor_purged/retention behavior and combined-owner floors, history expiry/tombstone pagination, message immutability and pin-delayed GC. Preserve R2/hub-notices/mcp-first section ownership and changes.

- [x] **Step 1:** Write the backup/export/restore and rollback runbooks from Decisions, including mixed-format backups, opaque cursor recovery, paused/exhausted subscription behavior, blob pinning, finite history vs external backup expiry, WAL growth and capacity alerts. Explain default indefinite history and optional hosted 90 days; 1 GB is not an unlimited plan.
- [x] **Step 2:** Document typed preference keys, validation/source precedence over initial flags/env, runtime refresh/error behavior and atomic audited changes; link the separate preferences API/web UI plan. Explain archive preservation, continuing acknowledged-event pruning and its cursor replay limit, plus revision-bound preview/confirmation before retrospective expiry on exit. Include the prefs-owned `blobs.expiry` enum consumed by R2 (default `1d`, disable with `off`, never numeric 0) and distinguish message archival from hosted blob-byte expiry. Document inspect/dry-run/pause/resume commands and status receipts. Explain NONE-to-INCREMENTAL needs an offline rebuild, free-page reuse vs shrink, and space requirements. Update pricing tables with measured evidence and conditions, never turn logical forecasts into measured numbers.
- [ ] **Step 3:** Validate protocol examples/docs and the full Task 8 gate through the build box. Cross-check every retention claim against tests, especially the merged-cursor conservative safe floor and pin-delayed expiry. Add one dated CHANGELOG line linking the behavior/docs.
- [x] **Step 4:** Prepare rollout checklist: external backup verified; dual reader deployed with switches off; enable references; observe byte guards/error/latency metrics; start/resume bounded backfill; verify completion/exceptions; dry-run retention; enable acknowledged pruning; optionally enable hosted history only after operator policy decision; defer offline NONE-to-INCREMENTAL conversion and use freed-page reuse first. Verify archive status and preferences sources; confirm any retrospective expiry through the separate preferences flow before enabling it. Each stage records DB/WAL/free-space/pin counts and can pause independently. Do not require or imply full VACUUM to get reference savings.
- [x] **Step 5: Commit** `docs(storage): retention contracts and safe compaction rollout`.

---

## Decisions (2026-11-05)

- Max approved the recommended defaults: idempotency 24 h, acknowledged events 7 days, unacked events/message history/audit forever. Hosted 90-day history is an explicit later opt-in, not an approved deployment change.
- Max approved the conservative principal-wide recovery floor; narrowed streams/history provide recovery below a merged stream's safe floor.
- Max approved reuse of freed pages first. No offline NONE-to-INCREMENTAL conversion now; existing NONE databases keep their mode. New empty databases may use INCREMENTAL as planned.
- Max approved append-only audit kept forever. Privacy erasure is designed separately later; this work adds no audit expiry.
- Archive mode is an explicit visible operator preference preserving all messages/history and disabling opt-in history expiry. Acknowledged events may still prune because history retains messages; indefinite cursor replay additionally requires `retention.ack_events=0`. Exiting archive cannot authorize retrospective deletion without the separate confirmation described above.
- Retention settings use the typed runtime hub preferences store; stored values win over initial flags/env. This plan defines its consumer/write-guard seam only. A separate plan builds the preferences API and web UI page; R2 adds `blobs.expiry` (24 h default, can be disabled) to the same store.

---

## Migration inventory and integration

Allocate every additive migration at the next free number after rebase; master ends at 0020 and hub-notices takes 0021. DB Task 2 adds event storage format/reference envelopes, immutable delivery snapshots and body guards; Task 4 adds backfill exceptions if not included in Task 2 (progress uses existing meta); Task 5 adds owner event-retention floors; Task 6 adds the idempotency age index; Task 7 adds thread sequence high-water and history-expiry/locator tombstones. Split/coalesce only before shipping; no prefs tables here. Task 8 sets up new-DB pragmas, not an existing-DB VACUUM migration. See [build order](2026-11-05-build-order.md) for the merge allocator and shared files.

--- SUMMARY ---

- Reference delivery payloads to immutable Message bodies; keep exact frozen recipient envelopes/attachment URLs and use rare deduplicated snapshots for legacy byte differences. Hydrate all raw event readers internally; preserve every API's own wire bytes with baseline guards.
- Backfill online in bounded resumable transactions, with byte proof, durable exception ids, WAL/disk-pressure pauses and mixed-format backup/restore. Allocate migration numbers only from the merged directory at build time.
- Keep all currently evidenced indexes, compact ids-only audit and the existing atomic thread update. Expire unused idempotency rows after the current 24 h window; measure any later index/thread-write optimization independently.
- Use the approved 24 h idempotency and 7-day acknowledged-event defaults; keep unacked/message/audit history forever. Typed runtime retention preferences override initial flags/env. Archive mode retains messages/history, blocks opt-in history/hard unacked expiry, and requires confirmation before retrospective expiry on exit; acknowledged pruning can continue with explicit replay limits. The prefs plan owns store/API/validation/audit and shared preview/explicit confirmation; retrospective trim runs at the next nightly sweep. R2 consumes `blobs.expiry` enum (`1d`, `off` to disable); admin parity owns prefs MCP/capability adapters. No second store or confirmation service. Owner floors and transactional pins protect cursors, retries, held deliveries, MCP replay and blobs. Finite history requires a durable thread sequence high-water and compact pagination locators.
- Use freelist reuse immediately; new DBs get incremental vacuum, existing NONE DBs stay unchanged this release, with offline conversion deferred. Compression stays off pending measured CPU/page benefit; CLI remains sqlite-free.
- Logical forecasts for F=1/2/5/20 are explicit assumptions; only bench-hub's comparable physical/lifecycle measurements can confirm the savings. This planning run collected disposable build-box EQP evidence, not a production capacity benchmark.


## Task 9 implementation receipt — 2026-11-07

Documentation-only implementation on `db-t09`, starting from merged Tasks 1–8 at `89d3133`. Updated only the assigned protocol retention/storage/cursor text, DEPLOY, OPERATOR, pricing evidence, CHANGELOG and this plan. R2 blob sections, hub notices and MCP-first/schema-diet protocol sections are preserved. No production code, schema, deployment, push or merge is part of this task.

- Steps 1/2: documented consistent external mixed-format backup/export/restore, downgrade boundaries, typed policy/confirmation, archive vs replay/blob expiry, supported inspection/pause/resume controls and their exposure limits. Existing NONE mode keeps free-page reuse; offline conversion and full VACUUM remain deferred.
- Step 3: gates and source/test evidence are recorded below before the final commit. Pricing uses Task 8's measured physical/live-page cohorts separately from the older logical forecasts. Accepted read p95 cost is explicit: roughly 0.5 ms inline to 3–13 ms in representative reference cohorts, with some 14–16 ms profiles/repeats. CoS will file the faster-hydration follow-up; this docs-only task does not publish an issue.
- Step 4: prepared independently pausable stages with DB/WAL/headroom/checkpoint/pin/policy/exception/latency receipts, verified backup first, dual reader, references, backfill, retention preview/activation and optional hosted history. Hosted 90 days remains unapproved for deployment.
- Step 5: commit subject `docs(storage): retention contracts and safe compaction rollout`, trailer `Plan-Task: 09`; actual final head is recorded in `.scratch/vecnet/build-db/task-09.result.json` outside the commit.

**Actual interface limits:** `GET /v1/admin/storage/backfill` and `/maintenance` expose status only. Backfill dry-run/rescan and retention status are internal methods, not public mutation endpoints. `hub.Config.EventRetentionEnabled` independently gates acknowledged-pruning ticks but is not exposed/enabled by the shipped CLI. The checklist explicitly holds that production activation stage pending an authorized integration; no fictitious enable flag is documented. `blobs.expiry` is the shared enum contract (`1d` default, `off` only to disable), unavailable until the R2 owner installs its verified adapter. Complete logical replay export, reverse conversion, backup automation and comprehensive pin telemetry are not shipped commands.

**Claim-to-test cross-check:**

| Documented contract | Existing executable evidence |
|---|---|
| Conservative combined floor, equality valid, lower narrowed replay, restart/epoch and consistent snapshots | `internal/eventlog/retention_test.go`: `TestOwnerRetentionInterleavedFloors`, `TestRetentionReadSnapshotSurvivesConcurrentPurge`; `internal/httpapi/event_retention_test.go`: `TestOwnerRetentionHTTPMergedNarrowedAndDefaultAck`, `TestOwnerRetentionDefaultAckFromOldEpochIsPurged` |
| Shared ack vs receipts/read, owner prefix and callback/held/dispatch pins | `internal/hub/event_retention_test.go`: `TestAcknowledgedRetentionScopesAndPrefix`, `TestRetentionSubscriptionEligibilityAndOutboxPins`, `TestRetentionDeliveryPins`; `event_retention_r2_test.go`: `TestRetentionReadAndReceiptAreNotPullAcks` |
| Archive keeps history/hard-unacked protection but acknowledged pruning continues; wakes do not trim | `TestRetentionArchiveSuppressesHardExpiryButNotAcknowledged`, `TestRetentionPreferenceWakeRefreshesStatusWithoutTrim` in `internal/hub/event_retention_test.go`; `TestHistoryRetentionWakeupsNeverTrim` in `history_retention_test.go` |
| Revision-bound confirmation, prospective vs retrospective, runtime protection, bootstrap and next-nightly bounds | `internal/hub/history_retention_test.go`: `TestHistoryRetentionDefaultForeverAndConfirmedNightlyLocator`, `TestHistoryRetentionFixedPreviewBoundsAndStaleConfirmation`, `TestHistoryRetentionArchiveExitCannotAuthorizeLaterFiniteExpiry`, `TestHistoryRetentionProtectionBetweenDestructiveSlices`, `TestHistoryRetentionBootstrapChangesRemainPending` |
| Pin-delayed GC, R2/shared blob refs, counters/high-water and pagination locators | `internal/hub/history_retention_test.go`: `TestHistoryRetentionDependenciesAndArchiveStopsNextSlice`, `TestHistoryRetentionR2BlobObjectRefPinsMessage`, `TestHistoryRetentionSharedBlobReferencesGoLast`, `TestHistoryRetentionMarkSweepPreservesThreadCounters`, `TestHistoryLifetimeSequenceAfterNewestAndAllBodiesDeleted`, `TestHistoryFromJoinAfterPurgedSuffix` |
| Exhausted bytes preserved, explicit resubscription/replay and owner-floor handling | `internal/events/retention_r2_test.go`: `TestRetentionExhaustedLegacyCallbackSurvivesReplay`; `retention_r3_test.go`: `TestRetentionExhaustedParkedResubscribeAtReportedCursor`; `retention_floor_test.go`: `TestOwnerRetentionFloorEchoPreservesCallback`; `replay_lookup_test.go`: `TestReplayAvailabilityLookupBounded` |
| Strict idempotency cutoff and shared lookup/cleanup policy | `internal/hub/idempotency_expiry_test.go`: `TestIdempotencyExactCutoff`, `TestIdempotencyPolicyConfiguration`, `TestIdempotencyBoundedIndexedExpiry` |
| Existing NONE reuse, mixed representation guards and bounded physical reclaim | Task 8 [benchmark/gate receipt](../business/db-compaction-bench-2026-11-07.md), `internal/store/compaction_test.go`, `internal/httpapi/delivery_hydration_test.go` and the frozen reader corpus linked in the Task 1 receipt |

**Actual merged migrations (filenames renumbered after master 0026; SQL bytes unchanged):**

| Owner / purpose | Shipped filenames |
|---|---|
| Hub notices / preferences (separate owners) | `0021_hub_identity_notices.sql`, `0022_hub_preferences.sql` |
| DB Tasks 2/4 | `0023_event_payload_refs.sql`, `0024_backfill_exceptions.sql` |
| R2 / peer archive (master; already deployed) | `0025_r2_blobs.sql`, `0026_peer_archive.sql` |
| DB Task 5 | `0027_event_retention_floors.sql`, `0028_event_outbox_retention_seq.sql`, `0029_callback_retention_lookups.sql`, `0030_event_retention_rule_intervals.sql`, `0031_callback_retention_identity.sql` |
| DB Task 6 | `0032_idempotency_created_at_index.sql` |
| DB Task 7 | `0033_thread_seq_history_tombstones.sql`, `0034_thread_history_loop_parent.sql` |
| DB Task 8 | No migration; new empty-file pragmas and opt-in maintenance preserve existing modes. |

**Task 9 validation — documentation committed; acceptance blocked by the full-suite gate:** All execution below used the x-tuf build box with `BB_JOBS=2`. No real database, code fix, push, merge, deployment or `attn` was used. The requested result JSON records every attempt and the final commit. Step 3 stays unchecked until the required full suite ends with `bb: exit=0`.

| Check | Actual result |
|---|---|
| `BB_HOST=x-tuf BB_JOBS=2 just bb just test` | **FAIL**, `bb: exit=1`, twice (594.7 s and 348.1 s). Both failed only `TestBackfillRealReaderCheckpointPressure`, `internal/hub/storage_backfill_test.go:260`: after reader rollback, expected conversion but got `paused_reason=lock_pressure`. |
| `GOMAXPROCS=2 BB_ENV=GOMAXPROCS BB_HOST=x-tuf BB_JOBS=2 just bb just test` | **FAIL**, `bb: exit=1`, 207.7 s. Same assertion with the remote runtime explicitly limited to two threads. No claim that the earlier failures were resolved by the runtime limit. |
| `BB_HOST=x-tuf BB_JOBS=2 just bb just validate-protocol` | **PASS**, `bb: exit=0`; 11 positive and two expected-negative example validations. Embedded schema matches. |
| Task 8 focused hub/HTTP races: `go test -p 2 -race -timeout 20m ./internal/hub ./internal/httpapi -run 'Test(StorageMaintenance\|Backfill\|HistoryRetention\|Idempotency\|DeliveryReference)' -count=1` through `just bb GOMAXPROCS=2 CGO_ENABLED=1 CC=/usr/bin/gcc` | **PASS**, `bb: exit=0`, 500.4 s; hub 496.034 s, HTTP 13.916 s. |
| Complete eventpayload race: same remote race settings, `go test -p 2 -race -timeout 20m ./internal/eventpayload -count=1` | **PASS**, `bb: exit=0`, 91.6 s. |
| Additional expanded race selection including `TestRetention*` across store/hub/HTTP/eventlog/eventpayload/Events/opview | **TIMEOUT / FAIL**, `bb: exit=1`, 1299.4 s. Hub reached its 20-minute package timeout in `TestRetentionOneRowSliceIndependentOfPolicyHistory` (9m49s in that test). Selected store/HTTP/eventlog/Events/opview races passed; eventpayload matched no tests in that selection and was subsequently run in full above. No complete hub race pass is claimed. |
| `just bb GOMAXPROCS=2 just test-ui` | **PASS**, `bb: exit=0`; 222/222 model tests. |
| CLI `go list -deps ./cmd/vecnet` with absence assertions through `just bb GOMAXPROCS=2 CGO_ENABLED=0 python3 -c ...` | **PASS**, `bb: exit=0`; 260 dependencies, no `modernc.org/sqlite`, `internal/store` or `internal/eventpayload`. No benchmark artifact was rewritten. |
| New operator examples/source assertions through `just bb python3 -c ...` | **PASS**, `bb: exit=0`; all three shell blocks pass `bash -n`, the prospective 90-day JSON patch parses, and routes/flags/error spellings match source. Local doc links pass after excluding pre-existing `.scratch` issue links omitted by build-box sync. Initial whole-file link-check scope failure is retained in the result JSON; it was not a behavior/example failure. Authenticated commands were not executed against a live hub. |

**Unblock:** fix or separately authorize investigation/repair of the existing checkpoint-pressure test/implementation on the integration branch, then rerun the exact required full-suite gate on x-tuf. Task 9's docs-only scope does not authorize modifying behavior or weakening that assertion. Keep reference/retention production enablement pending; the independently missing CLI acknowledged-pruning enable integration is also documented in the rollout checklist. No further unchanged retries are represented as a fix.
