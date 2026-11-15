# Task 1 scoped re-review — fix round 2

Spec compliance: **PASS**

Code quality: **PASS**

Reviewed FIX_BASE `8398a1fcc66434f0c363baf3a88c18db81b607f1` to HEAD
`27b931dd9cac3dd4d6848d1c3f5506ca86f230cf`.

TR61-03 Important: **ADDRESSED**. TR61-04 Minor: **ADDRESSED**.
TR61-01/TR61-02 remain addressed. New concrete findings in this scoped range:
Critical 0, Important 0, Minor 0. No accepted finding is deferred.

## Scope and evidence discipline

Read the binding task brief, R7 ruling, Fix round 2 report appendix, supplied
fix diff and previous re-review. Inspected the changed clear/coalescer/retry
functions, their immediate history/receipt callers, shutdown interaction and
corrected/new behavioral oracles. The existing history/receipt-ordering test
was inspected for the concrete risk that a scan could retire a row before its
queued item cleanup executes. No unrelated source crawl was performed.

Read the retained fix2 raw logs/exits/heads/dirty stats and verified current
working-file blobs against the commit and reported pins. No test/build was
rerun. This report is the only write; no source/index/branch mutation, child
agent, external message, service, private runtime/data or GitHub operation
was used.

## TR61-03 — ADDRESSED

**Source:** `connectors/launcher/src/codex/engine.ts:493` coalesces receipt
cleanup by key while the request is queued or running. Repeated item/started
and item/completed notices still record receipt evidence, but cannot enqueue
a second fallible cleanup for the same pending request.

The token retains the captured row and an explicit confirmation flag.
`engine.ts:187` sets that flag only after an accepted, exact-attempt cleanup
has successfully confirmed and retired the row. This also covers a history
scan already ahead of the item task in the serial lane. At `:499`, a queued
request skips redundant cleanup only if its own token was confirmed and the
row is still absent. A live row at that point fails closed.

`engine.ts:172` validates the current row and any outstanding proof before
accepted cleanup. `sameProofAttempt()` at `:164` checks key, attempt, claim,
full owner and delivery set; the retry path at `:148` uses the same comparison.
An absent unconfirmed row or changed provenance is not treated as successful
receipt confirmation. Failed first confirmation retains the immutable original
proof and existing bounded retry. Rejected/released cleanup cannot mark an
item token as accepted confirmation.

**Coalescer lifetime:** the token is removed in the queued task's `finally`
at `:503`, including failure. A stopped lane can skip queued task bodies, so
the explicit stop drain also clears the map at `:888`. This is request-scoped
state, not an accumulating history of confirmed client keys. Tokens neither
replace durable inflight evidence nor authorize delivery themselves.

**Concrete oracle:** `connectors/launcher/test/codex-engine.test.ts:765`, both
“duplicate receipts with first confirmation failure false/true” cases:

- Both actual wire notices must arrive while the original row still exists.
- The deletion hook makes redundant confirmation fallible immediately after
  the first successful removal.
- The first-success variant requires exactly one confirmation and one removal,
  no quarantine, live unchanged ownership and no recreated delivery barrier.
- The first-failure variant requires original-row retention, later bounded
  recovery and identical original settlement arguments.
- Both advance the retry budget and require exactly one subsequent eligible
  delivery, no duplicate send and no accepted-claim release.

The three controls at `codex-engine.test.ts:803` retain fail-closed behavior
for altered attempt, altered deliveries and missing unconfirmed row. The
existing test at `:1856`, “a receipt arriving during recovery cannot be released
by a stale scan,” exercises receipt arrival while the history call is awaited;
the scan can confirm the matching attempt ahead of its queued item cleanup.
It passes in the current complete engine/RPC gate.

**RED/GREEN:** `fix2-red-duplicate.log` fails the first-success case exactly
at “a stale duplicate cannot quarantine unchanged ownership,” reporting
ownership_changed instead of live. The failed-first variant is correctly
recorded as a passing control: 1 pass / 1 fail, wrapper/build-box 1/1. Both
duplicate variants and all three provenance controls pass in current focused
and complete gates. This fixes the accepted source interleaving without a
general missing-row success shortcut.

## TR61-04 — ADDRESSED

**Oracle:** `connectors/launcher/test/codex-engine.test.ts:679` now observes
timer creation while the original pending accepted row has no item proof.
The wake is queued first. Assertions at `:691` require a future deadline,
not-fired/uncancelled state and zero receipt confirmations. Only afterward,
at `:694`, does the test deliver the real item, fail confirmation and supply
terminal-only idle. Assertions at `:699` prove the original callback actually
fired uncancelled while proof remained unconfirmed.

Both watch variants retain the no-new-claim/guard/turn assertions past the
gap deadline, original row/proof arguments, one eventual queued batch after
confirmation and no accepted-claim release or duplicate delivery. The
watch-true case also checks a later independent watch trigger.

**Coverage control and mutation:** `fix2-gap-control.log` passes both corrected
tests on unchanged FIX_BASE production source. It is properly labeled a
coverage control, not invented RED. `fix2-gap-mutation.log` fails both cases
at the later “gap cannot acquire a new claim before proof confirmation”
assertion, actual 3 versus expected 2. Therefore the earlier timer-creation
and callback-fired assertions were reached successfully before failure.
The current `proofBlocksDelivery()` at `engine.ts:132` has its real expression,
and the final engine/full gates follow the mutation in the retained sequence.

The appended report explicitly withdraws the earlier inaccurate fixed-source
armed-timer claim and describes the corrected order. The accepted Minor
finding is fixed in both executable coverage and reporting.

## Interaction checks and regression judgment

The proof barrier remains at push entry (`engine.ts:706`), both awaited
checkpoint boundaries (`:726`, `:747`), transmit entry (`:642`) and after
guard acquisition (`:656`). Receipt coalescing does not bypass or remove
received-item evidence while confirmation is pending. Ordinary pending rows
without receipt/history proof retain their prior admission behavior.

Proof-only retry remains independent of loaded observation, idle age and
delivery eligibility. Existing automatic retirement cleanup, expiry/unloaded
cleanup, owner/generation/stop controls and original-proof settlement cases
all pass in the current focused evidence. Stop still cancels the settlement
timer; running/owner checks prevent queued retry work from restarting it.
Cleanup continues to use the current exact-owner Go contract rather than
renewing input authority. No history/receipt ordering defect was found in
the changed paths.

RF1–RF4 controls are present and passing in the current focused log: exact-once
terminal/long-turn recovery, read-failure/backoff/burst/idle-age controls,
stale reply and blocked-read stop controls, and launcher-origin notLoaded
no-cold-load coverage. Their observation machinery was not broadened by fix2.

No new public API, dependency, durable schema, fixture default, policy/input
behavior or generic rejected-input replay is introduced. The coalescer and
provenance checks are proportionate to the accepted race. No additional
concrete defect, including Minor, was identified in this scoped review.

## Current raw gates and source pins

All stems below are under
`.superpowers/sdd/issue-61-terminal-recovery/logs/`; totals are read from raw
logs and actual `bb: exit=` values, with matching wrapper `.exit` files.

| Gate | Pass / fail | Skip / cancel | Wrapper / build-box |
| --- | --- | --- | --- |
| fix2-red-duplicate | 1 / 1 | 0 / 0 | 1 / 1 |
| fix2-gap-control | 2 / 0 | 0 / 0 | 0 / 0 |
| fix2-green-focused | 41 / 0 | 0 / 0 | 0 / 0 |
| fix2-gap-mutation | 0 / 2 | 0 / 0 | 1 / 1 |
| fix2-engine-rpc | 190 / 0 | 0 / 0 | 0 / 0 |
| fix2-ln-check | 2181 / 0 | 0 / 0 | 0 / 0 |

Current canonical strict/full evidence is **2,181 tests across 955 top-level
cases**, with strict TypeScript included by the `just ln-check` dependency
chain. Current focused coverage contains 39 F2 cases and two existing
receipt/release controls. No success gate has failures, skips or cancellations.

Raw RED and coverage-control dirty stats contain only test changes against
FIX_BASE. The mutation dirty record differs from restored final engine/full
records as expected. Per-gate HEAD is dirty FIX_BASE, not a byte-content
manifest. The controller supplies final tested-content equivalence as an
independently verified dispatch fact; current working-file blob calculations
match `git ls-tree HEAD`, report and receipt:

- Engine: `6ca887a8b7a15f4f0ecdd36c4f4eba812433c7f2`.
- Engine tests: `455edb91421773b692d953db216df003be063295`.
- Fake server: `d521e3a1f394c4082ddd6ef813f16e70928646eb` (unchanged).
- Changelog: `86368cb284d7c4541a1a11de480a6ed9c8be306f`.
- Receipt: `10bd88f4a758761bb2ea62acadf7d606e3e0efd0`.

The prior fix1 E2E 16/0 result belongs to source `8398a1fc`; it is previous-source
generic launcher coverage, not a new gate on HEAD. No concrete new public
integration risk was identified in this private cleanup/idempotence change
and test setup repair, so no additional E2E gate is requested under R4/R6's
proportional verification rule. The prior metadata diagnostics and release
provenance limitations are not erased by this judgment.

## Limits and disposition

No suite was executed by this reviewer. Per-gate historical content cannot be
reconstructed from head/stat artifacts alone; current source pins and the
controller's independent content-equivalence verification define that evidence
boundary. Current focused, engine/RPC and strict/full executions are established
by retained raw outputs/exits, not inferred absent or present from a diff.

This PASS resolves the scoped task-review findings TR61-03/TR61-04, with
TR61-01/TR61-02 still addressed and no accepted finding left open. It is not
the later independent whole-branch review, merge/push authorization report,
deployment proof or artifact-cleanup certification. Incident attribution and
F1 generic refusal classification remain outside this source deliverable.

=== DONE ===
