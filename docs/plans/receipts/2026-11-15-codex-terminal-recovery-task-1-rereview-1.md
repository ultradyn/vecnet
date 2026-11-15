# Task 1 scoped re-review — fix round 1

Spec compliance: **FAIL**

Code quality: **FAIL**

Range: `ae754d3ee29ef5476d778d799b5c0292f2307e78` to
`8398a1fcc66434f0c363baf3a88c18db81b607f1`.

TR61-01: **ADDRESSED in source**; its current exact outstanding-timer coverage
has the Minor gap recorded separately as TR61-04 below.

TR61-02: **ADDRESSED**.

New findings in the fix range: Critical 0, Important 1, Minor 1. Neither new
finding is deferred. The passing mechanical gates are real; these verdicts
identify a new source interleaving and a regression-oracle gap that those gates
do not cover.

## Review scope

Read the binding brief, R5/R6 progress rulings, appended Fix round 1 report,
supplied fix diff and original review. Inspected the changed engine functions,
their immediate delivery/receipt callers, restored oracle and eight added
cases. Focused Go cleanup reads checked the claimed expiry/owner contract;
they were necessary to assess the proof retry rather than unrelated work.
The fake server's receipt emission was checked specifically for coverage of
the duplicate receipt path described below.

No builds/tests were run, no child agents were spawned, and no runtime, service,
private state, message, GitHub, index or branch operation was used. This report
is the only write. Old task gates are treated as historical throughout.

## Original finding dispositions

### TR61-01 — ADDRESSED in source

`connectors/launcher/src/codex/engine.ts:128` defines the shared proof barrier.
It includes both a snapshotted failed confirmation and a received item whose
queued confirmation has not yet run. Ordinary pending rows without proof still
have their prior behavior; the current sending row does not block itself on
its own early receipt.

The checks cover the actual delivery paths:

- `:684`: `push()` entry, covering debounce, minimum-gap, history retry, watch
  and completion-triggered delivery.
- `:704` and `:725`: both retained-batch and fresh-batch awaited checkpoints.
- `:620`: `transmit()` entry, including a receipt arriving while the claim hook
  was awaited; that already-issued claim is retained without sending input.
- `:634`: the awaited guard boundary, rejecting/releasing the known-unsent
  new attempt if proof appeared during guard acquisition.
- The mismatch/fallback loop returns through the same transmit checks; neither
  turn/start nor turn/steer has a bypass around this boundary.

Concrete oracles: `codex-engine.test.ts:669` has both gap/watch variants. They
assert unchanged claim/guard/turn counts while proof fails, original row
retention and settlement argv, row removal before queued input, one eventual
batch, and no accepted-claim release/duplicate send. `:726` injects a real item
receipt during `latest()` and asserts no claim is created afterward. The
baseline RED fails both gap variants with three claim hooks rather than two;
the current final GREEN passes both and the awaited-checkpoint case.

The shared barrier fixes the original defect by effect. TR61-04 below is a
separate limitation in the claim that the current GREEN actually leaves a gap
timer armed; it does not identify a surviving bypass in the inspected source.

### TR61-02 — ADDRESSED

`engine.ts:164` snapshots the original proof before fallible confirmation;
`:138` schedules one independent proof-only retry with capped
1/2/4/8/15-second delays. It does not depend on thread status, subscription,
idle age, root observation or delivery expiry. Confirmation uses the original
owner/attempt/claim/key/full delivery set at `:172`, with current-row provenance
checks at `:147`. It performs no thread read, load, resume or input operation.

`canRetrySettlement()` at `:133` rejects stop and locally invalidated owner or
identity/generation; Go settle revalidates the exact original owner and caller
under its lock. `stop()` at `:858` cancels the timer; queued callbacks and loop
continuations recheck running/owner state. A failed in-flight cleanup after stop
cannot rearm the timer. After successful proof cleanup, `:157` reauthenticates
admission and arms delivery only if current liveness permits it.

The restored oracle at `codex-engine.test.ts:551` clears the failure and advances
finite timers, with no `engine.start()`. It asserts eventual original-row
cleanup, unchanged retired read/resume counts, no subscription, idle_stale,
no new delivery and identical proof argv. The boundary family at `:703`
adds expiry/unloaded cleanup and owner/generation/stop retention; launcher
origin asserts zero ensureLoaded calls and no new resume/turn/release. The
baseline RED leaves the row after the finite retirement budget and fails
expiry/unloaded cleanup; current GREEN passes these cases.

The expiry adaptation matches `internal/cli/codexowner.go:76`: cleanup permits
expiry while retaining all original owner fields. The command also checks the
authenticated hub/caller before settling. This is reuse of the current cleanup
contract, not authorization for expired delivery or generic refusal replay.

## New findings

### TR61-03 — Important — A stale duplicate receipt cleanup creates an orphan proof and disables the engine

**Location:** `connectors/launcher/src/codex/engine.ts:169`, `:147`;
caller `:483`.

**Scenario:** A pending accepted row receives item/started and item/completed
for the same user message before its first asynchronous settlement finishes.
Both notices pass `parseRow()` and enqueue `clear(r)` with the same row. The
first clear successfully confirms the exact receipt and removes the row and
proof. The already-queued second clear still runs: it unconditionally creates
a new settlement proof from the stale row, although that row was already
successfully retired. If this redundant confirmation now fails transiently,
the new retry fires, sees no `inflight:` row, and calls `quarantine()`.

**Effect:** A normal duplicate receipt sequence plus a transient redundant
confirmation failure permanently marks unchanged ownership as changed, kills
the watch and blocks future delivery until engine replacement. The original
proof was already durably confirmed. This regression is introduced by the new
proof map and the new interpretation of missing rows as corruption; it is not
the original missing terminal observation or F1.

The serial lane does not prevent this: notifications run outside the lane and
can enqueue both operations while the first child is awaited. `clear()` has
no stale-row check before creating the proof. Existing fake receipt emission
at `fake-codex-appserver.ts:117` sends only item/started, so the new passing cases
do not exercise this two-notice interleaving. Repeating an exact Go receipt is
normally idempotent, but it remains fallible because each invocation performs
authority/authentication/state I/O.

**Fix/verification:** Coalesce or discard stale clear requests once their exact
row has already been successfully retired; do not manufacture a fresh proof
obligation for an absent, confirmed row. Retain fail-closed handling for a
genuinely altered live row and immutable outstanding proof. Add a controller-
gated regression that queues both receipt notices while the row still exists,
lets the first confirmation succeed, then makes any redundant confirmation
fail. A synthetic KV deletion hook can deterministically switch the failure
after the first successful row removal. Advance the retry budget and assert
no quarantine/ownership_changed, no recreated proof barrier, no accepted-claim
release, and exactly one subsequent eligible delivery. Confirm recovery still
works when the *first* confirmation fails.

**Evidence limit:** This is a source-derived interleaving, not a newly executed
failing test. Only the controller may run the proposed focused probe, through
the serialized gate.

### TR61-04 — Minor — The current gap regression no longer arms the timer it claims to test

**Location:** `connectors/launcher/test/codex-engine.test.ts:679`–`:685`.

**Scenario:** The test records the original receipt and completes its failed
settlement before calling `h.wake()` for queued wake_idle. On current source,
`proofBlocksDelivery()` is already true when that wake reaches `push()`, so
the new entry check returns before `push()` can create a minimum-gap timer.
The later six-second advance therefore does not exercise an already-armed
gap timer in the fixed engine. On baseline source the entry check is absent,
so the same setup does create a timer and produces the retained RED. That RED
is genuine, but does not establish the claimed fixed-source timer precondition.

**Effect:** The test covers rejecting a trigger after proof exists, and the
watch-true branch covers a later watch trigger. The specifically requested
proof-arrives-after-a-timer-is-already-armed regression is missing. This leaves
the original timer seam dependent on source inspection rather than its named
behavioral oracle. The implementation report's claim that the production
five-second gap remains armed across completion is inaccurate for current
GREEN execution.

**Fix/verification:** Queue wake_idle and establish/observe the gap timer while
the prior pending row still has no item proof. Then deliver its real item,
fail confirmation and supply terminal-only idle before that existing timer
fires. Keep the no-claim/guard/turn assertions past the gap deadline and the
post-confirmation exactly-once assertions. Pin timer creation explicitly so a
future early return cannot silently remove the precondition. Run the revised
oracle only through the controller gate and correct the report wording.

## Current retained evidence

Read raw fix-round `.log`, `.exit`, `.head` and `.dirty` artifacts. Extracted
totals and actual build-box exits agree with the report:

| Gate | Pass / fail | Skip / cancel | Wrapper / build-box |
| --- | --- | --- | --- |
| fix1-red-both | 0 / 3 | 0 / 0 | 1 / 1 |
| fix1-red-boundaries | 3 / 2 | 0 / 0 | 1 / 1 |
| fix1-green-focused | 34 / 1 | 0 / 0 | 1 / 1 |
| fix1-green-focused-final | 36 / 0 | 0 / 0 | 0 / 0 |
| fix1-engine-rpc | 185 / 0 | 0 / 0 | 0 / 0 |
| fix1-ln-check | 2176 / 0 | 0 / 0 | 0 / 0 |
| fix1-ln-e2e | 16 / 0 | 0 / 0 | 0 / 0 |

RED artifacts identify only test changes against FIX_BASE, so these are
behavioral failures on unchanged production source. The first GREEN fails
because restored retirement expects idle_stale but sees identity_unverified;
the final source reauthenticates admission after successful cleanup and the
final focused pass includes all 34 F2 cases plus two existing receipt/release
controls. The current full suite has **950 top-level cases / 2,176 tests**;
the engine/RPC total is **185**, and canonical launcher E2E has **16** cases.
The strict check is included by `just ln-check`'s existing dependency chain.
Original totals 2,168/177 and the earlier mutation witness remain historical.

E2E raw output shows the Go build command, three `.git`-absent metadata
diagnostics, then all 16 synthetic launcher cases and actual exit zero. This
supports the current synthetic build/integration result. It does not certify
release version/commit-date metadata or an installed Codex runtime, and the
generic launcher E2E family is not a replacement for the focused engine proof
oracles. R6's proportional reuse is acceptable; the diagnostics do not turn a
zero-exit successful synthetic suite into an absent or failed execution.

Working-file blob hashes independently match `git ls-tree HEAD` and the
appended report/receipt:

- Engine: `31a3f5ca103f9486343c1fb4b4cb379f0bca459e`.
- Engine tests: `6d6ab21012f9ded4d7817f3bc03c46a7af88c358`.
- Fake server: `d521e3a1f394c4082ddd6ef813f16e70928646eb` (unchanged).
- Changelog: `c94f1a9d1d604b225dcf080807ae9ee0957659ad`.
- Receipt: `a0deedf7306ca240339f69fd41b7bd12efe48c31`.

The controller provides tested-content equivalence as a dispatch fact. Raw
per-gate HEADs are the dirty FIX_BASE and their stats are consistent with the
reported test-first sequence; they alone are not byte-content manifests of
every historical gate input. No suite was rerun to manufacture that evidence.

## Contract/adaptation judgment and remaining limits

The original fake defaults are unchanged in this round. Replacing only the
new explicit-start oracle with timer-driven confirmation is authorized and
correct. Current Go settle reuse, immutable proof snapshots, expiry cleanup,
and post-cleanup admission verification are appropriate adaptations. Public
interfaces, durable schemas, completion observation fences, policy/input gates,
hold/wake_idle semantics and known rejection paths were not replaced.

Stop and local owner/generation invalidation checks are present. The owner
boundary test proves retained proof/no input under on-disk replacement, but
does not assert zero redundant cleanup calls after that replacement: Go is the
authority for each such cleanup, while local cancellation follows detected
invalidation. No stronger immediate on-disk timer-cancellation result is claimed.
The capped retry delay is clear from source; the new suite does not independently
measure every proof-only backoff deadline as it does for status observations.

RF1's original delivery bypass and RF2's post-retirement confirmation omission
are repaired. RF3/RF4 controls still pass in current focused/full evidence;
their observation and no-cold-load logic is unchanged in this fix range.
TR61-03 is a new proof-lifecycle liveness defect, and TR61-04 is the remaining
exact timer-oracle gap. Neither is silently assigned to incident/F1 follow-up.

Return both new findings to the controller/implementation loop. A focused
gate probe and fixes/re-review are required before source PASS. No claim is
made about incident attribution, generic refusal replay, deployment, release
provenance, later whole-branch review, merge or cleanup.

=== DONE ===
