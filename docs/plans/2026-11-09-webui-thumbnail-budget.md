# Task2a — standalone shared thumbnail admission plan

Status: Task2a pure-budget component complete. Parent x-boss focused26/fullUI522/Go10 gates pass; independent GPT-6.1-Sol SpecPASS/QualityPASS C0/I0/M2. R-M38 split applies; cache/DOM activation awaits Tasks2b/3. See [component receipt](receipts/2026-11-09-thumbnail-budget.md).
Authority: reviewed design Decision/logical limits, I1/R-M31 and Cache/demand/viewer; parent plan Task2.

## Files
- Future production: NEW `internal/ui/static/thumbs-budget.js`, target <=300 lines; pure, no dependencies, DOM, transport or payload operations.
- Future tests: NEW `internal/ui/jstest/thumbs-budget.test.mjs`, authorized R-M38 exception; <=18 root tests and <=450 lines including helpers.
- Task2a changes no existing source/tests/callers. Parent owns RED/GREEN and all source/test/build/browser execution on x-boss, aggregate TWO.

## Interfaces
1. Export only `createThumbBudget({limits} = {}) -> budget`; budget exposes only `enqueue`, `claimSource`, read-only `stats`; no reset/release/subscription API.
2. `limits` is optional partial overrides of `{B:33554432,C:2,E:16,N:16,Q:16,M:10485760}`; copy values, never retain caller object.
   Each recognized value must be a positive safe integer; require `M <= 10485760` and `B >= M`. Missing fields default; no artificial C/E coupling.
   Tiny valid fixture: `{B:8,C:2,E:3,N:2,Q:2,M:4}`. Invalid numeric limits throw RangeError before ownership/callbacks; wrong object types throw TypeError.
3. `enqueue({key,priority,onStart,onCancel}) -> ticket|null`; nonempty string key, priority exactly `'automatic'|'explicit'`, both callbacks required functions.
   Keys encode cache identity/hash/generation; budget treats them as opaque. Bad shape/key/priority/callback throws TypeError, without mutation.
   Duplicate live queued/running/resident key returns null without notifications or callback retention; released keys may be reused by distinct tickets.
   Admission refusal returns null and retains nothing. Accepted work may start synchronously before enqueue returns; onStart receives its ticket.
4. Ticket exposes only `setPriority(priority)`, `cancel()`, `settle({residentBytes})`, `dropPayload()`; all return undefined.
   setPriority validates the same two literals; changes only queued scheduling, never preempts running/resident work. Same-priority calls preserve rank.
   Automatic FIFO uses original enqueue order. Explicit FIFO uses acceptance as explicit, or most recent automatic-to-explicit upgrade order.
   Explicit-to-automatic voluntarily loses explicit rank; a later upgrade joins the explicit tail. Automatic FIFO remains original enqueue order.
5. Dispatch always selects earliest explicit before automatic and services accepted queued work before a newly arriving equal-priority job.
   Start only after reserving one C slot, one E entry and M bytes; `onStart(ticket)` fires once, then its reference is discarded.
   A running ticket occupies all three until actual task/cleanup settlement, including after cancel, owner release, remount or callback failure.
6. Q counts waiting jobs only. At full Q, incoming explicit may displace the newest queued automatic; never running/resident or earlier explicit.
   Automatic at full Q, or explicit with no queued automatic victim, returns null. No capacity denial invokes onCancel or retains its callbacks.
   Install accepted replacement/remove victim before calling victim `onCancel('displaced')`; replacement cannot bypass earlier explicit work.
7. Queued cancel removes ownership immediately, calls `onCancel('cancelled')` once, clears callbacks and permits another admission.
   Running cancel marks cancelled and invokes the same once-only callback to request owner abort, then clears callbacks; C/M/E stay charged.
   Repeated cancel and cancel after settlement are no-ops. Resident disposal requires dropPayload; budget cannot detach/revoke it.
8. `settle({residentBytes:null})` means actual completed failure/no payload; no resident entry remains. Numeric 0 means successful zero-byte resident, consuming E.
   Successful bytes must be a safe integer in [0,M]; invalid bytes throw RangeError, bad settlement shape throws TypeError, with no mutation.
   Settling a still-queued ticket throws Error without mutation; valid repeat settlement on a terminal/resident ticket is a no-op, first result wins.
   First running settlement atomically releases C/M and either retains one E entry with the supplied bytes or releases E for null/cancelled work.
   Cancelled success is discarded, even at zero bytes; it cannot become resident. Settlement clears remaining callbacks before dispatch/notifications.
9. dropPayload releases a resident ticket's E/bytes exactly once; no-op on queued/running/already-released tickets, never an early-settlement escape.
   Terminal handles cannot change a successor using the same key. Budget retains no released-key history, mounts, diagnostics or failure registry.
10. `claimSource(ownerKey,{onCapacity} = {}) -> permit|null`; ownerKey is a nonempty string; optional onCapacity must be a function.
    Invalid input throws TypeError before mutation. Duplicate live source key or exhausted N returns null; denied callback is never retained/called.
    Accepted claim charges one source immediately, even without src/payload. Ticket keys and source keys have separate namespaces.
11. Permit exposes only `drop() -> undefined`, `move(nextOwnerKey,{onCapacity} = {}) -> undefined`.
    drop releases N once, removing key and callback before notification; repeated drop is inert. Caller must detach any physical src first.
    move validates as claimSource, atomically replaces key/callback without changing N; omitted callback clears old callback, including same-key moves.
    Move to another live permit's key throws RangeError without mutation; repeat same-key move is accounting-idempotent. Valid move after drop is inert.
    No detached-state proof, element registry, URL or payload is added; caller must detach before transferring a permit.
12. `budget.stats` returns a frozen current scalar snapshot `{running,residentEntries,residentBytes,reservedBytes,queued,sources}`; no setters.
    reservedBytes=M*running; each read reflects committed state, old snapshots remain unchanged; attempted mutation cannot affect accounting.
13. All ownership mutations commit before callbacks; onStart/onCancel may reenter any method and see valid stats. Dispatch rechecks after each callback.
    If reentry cancels queued work it must never later start; running reentrant cancel still requires settlement. Callback exceptions never escape public calls.
    An onStart throw is not proof of transport settlement: keep reservation until owner settles; onCancel throws likewise cannot free running ownership.
14. Capacity means a strict decrease in charged bytes, running slots, occupied E, queued jobs or sources; reserve-to-resident conversion qualifies when capacity improves.
    Coalesce onCapacity delivery to one pending microtask, at most once per currently live permit in that delivery; callbacks take no arguments.
    No initial claim/enqueue/priority/move notification without capacity gain. Deliver after dispatch/accounting; notification is a retry hint, not reserved capacity.
    Delivery uses current callbacks: dropped/replaced callbacks are suppressed, newly claimed/moved owners cannot receive an old callback. Exceptions are contained.
    Capacity gained during delivery may schedule another coalesced delivery; no per-denial waitlist, polling, timers or retained delivery-history registry.

## Global constraints
- After every transition: residentBytes + reservedBytes <= B; running <= C; running + residentEntries <= E; queued <= Q; sources <= N.
- Retained controller state is bounded by <=Q queued, <=C running, <=E resident and <=N permits, plus scalars/callbacks; no Blob/Response/URL/DOM/net.
- No budget reset on route/sign-out; mount cancellation never frees unsettled work. Notification/callback containment does not invent cleanup completion.
- Pure tests use ticket callbacks and synthetic owner keys; no cache/DOM activation, new payload API, real transport, dependency or executor on this seat.

## Named RED tests (18 roots maximum; assertions target public counters, return values and callback traces)
1. `defaults_and_tiny_limits`: exact defaults via boundary admissions; tiny overrides; limit validation/defensive copy and frozen current stats.
2. `inputs_and_live_duplicate_keys`: malformed arguments fail atomically; duplicate tickets/permits deny; distinct generations and released keys admit.
3. `request_reserves_M_before_start`: C and B independently block work; onStart observes full reservation regardless of eventual supplied size.
4. `entry_limit_counts_zero_byte_residents`: E independently blocks; zero success retains E; null failure releases it; drop restores admission.
5. `settlement_validation_and_first_result`: null/zero/M valid; invalid size/shape and queued settlement preserve counters; duplicate completion inert.
6. `cancelled_running_counts_until_actual_settle`: cancel/abort request retains C/M/E through delayed cleanup; successful late bytes discarded.
7. `many_mounts_share_stalled_budget`: synthetic successive owners cancel under ONE budget; no replacements past C; drain both orders, no successor corruption.
8. `queue_limit_and_queued_cancel`: Q independent of running/E; null denial retains no callback; queued cancel removes/calls once and never starts.
9. `explicit_displaces_newest_automatic_only`: full Q replacement atomic; once-only displaced reason; reentry sees replacement and valid bounds.
10. `accepted_explicit_cannot_be_displaced`: full explicit queue rejects every later arrival; no running displacement or early reservation release.
11. `explicit_FIFO_upgrade_order`: initial explicit/upgrade interleave FIFO, same-priority preserves position, demote/reupgrade joins tail.
12. `explicit_precedes_later_automatic`: after actual capacity returns, accepted explicit starts before queued/later automatic; finite explicit cohort all progresses.
13. `payload_drop_and_terminal_handles_are_idempotent`: repeated drop/cancel/settle cannot underflow, retain history or affect reused-key successor.
14. `source_limit_counts_occurrences`: duplicate hashes with distinct owners each charge N; waiting owners count; denied claim has no eventual callback.
15. `permit_move_drop_idempotence`: N=1 transfer, same-key update, duplicate destination atomic error, old callback cleared, drop/move after drop inert.
16. `capacity_notifications_are_coalesced_live_only`: batch releases yield one hint/live permit; no hint on denial/no-op; move/drop suppress stale recipients.
17. `callback_reentry_observes_committed_accounting`: start cancels/settles/enqueues; cancel drops/moves; capacity retries; all equations hold before callbacks.
18. `throwing_callbacks_do_not_corrupt_ownership`: start/cancel/capacity throws contained; other work progresses only when accounted capacity truly returns.

## Review focus (3)
1. Every C/M/E/Q/N boundary, zero-vs-null distinction and cancelled late settlement holds across reentry and same-key successors.
2. Explicit FIFO/upgrade/displacement has no premature abort credit, hidden denied waitlist or earlier-explicit loss; callbacks are bounded and retired promptly.
3. Task2b consumes tickets/permits with complete transport cleanup and detach/revoke/drop ordering; Task3 alone activates existing callers atomically.

## Handoffs
- Existing cache constructor is `{api,URL,onReady}` with eager state()/broken()/release(); preserve it and existing thumbs.test.mjs until Task2b/3 switch together.
- Task2b must validate real-cache sharing/eligibility/generations, pass null on failure and Blob.size on success, settle even after cancellation, discard stale payloads.
- Task2b/3 must detach before permit drop/move and local revoke/dropPayload; Task3 owns document-lifetime default, demand passes, viewer/focus/physical-src checks.
- Logical admission cannot prove physical src counts, native RAM or incident closure. No irreducible contract conflict identified; parent review precedes test drafting.
