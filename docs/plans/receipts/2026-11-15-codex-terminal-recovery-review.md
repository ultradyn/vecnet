# Independent whole-branch review — issue #61 terminal completion recovery

Spec compliance: **PASS**

Code quality: **PASS**

Findings: **Critical 0 / Important 0 / Minor 0**. No accepted source finding
remains open. TR61-01, TR61-02, TR61-03 and TR61-04 are addressed in the
reviewed source and behavioral coverage.

Reviewed exact BASE `85c7bc9c85eb0416d66846997659963e8a175c4d` to
HEAD `27b931dd9cac3dd4d6848d1c3f5506ca86f230cf`, tree
`8f297785e8d896395bdffaa5a7ef8d0ce2dd8b9c`, in
`/home/xertrov/.codex/worktrees/e2f6/vecnet`.

**Method and scope.** Read the plan, operational brief, complete task report
including both fix appendices, all three task reviews, progress rulings and
current receipt. Verified that `final-review.diff` exactly equals the pinned
Git diff with ten context lines. Independently inspected the complete current
engine, added tests, changed fake functions, and concrete ownership, Go
settlement, history-recovery and gate seams. Previous reviewer conclusions
were treated as claims to check against source and evidence.

The code-review skill's spec and quality axes were applied directly; the
explicit read-only/no-subagents dispatch supersedes its fan-out workflow.
No tracked standards document was found by the repository filename inventory;
the supplied operator instructions and binding brief govern this review.
The test diff is +625/-0, preserving pre-existing assertions. Public Engine
and Binding interfaces, production owner/guard/receipt implementations,
dependencies and durable schemas are unchanged. `git diff --check BASE HEAD`
completed successfully.

**RF acceptance.** Test references below are all in
`connectors/launcher/test/codex-engine.test.ts`; each referenced family is
present in the current retained engine/full gates.

| Focus | Current behavioral pins and checked effect | Verdict |
| --- | --- | --- |
| RF1 | `:201`, “terminal-only completion drains queued wake_idle exactly once after an authoritative idle read,” requires only the terminal notice, an engine-owned authoritative read before input, matching owner/claim/attempt/key and one eventual guard/send/receipt despite duplicates. `:492`, “long-turn terminal-only idle starts retained wake_idle before idle dormancy,” advances two active minutes and then requires one retained delivery. `:563` requires repeated original settlement arguments before the queued wake drains; `:669` and `:741` cover independent delivery entry points while proof is pending. | PASS; idle discovery and proof-before-delivery are both covered. |
| RF2 | `:332`, both F2-I1 variants, independently recover an initial or trailing failed read without another notice. `:427` gates the first read, delivers the entire finite burst while blocked, and requires maximum concurrency one and at most two reads. `:602` checks all seven 1/2/4/8/15/15/15-second deadlines and duplicate suppression. `:520` observes repeated unchanged idle, retirement without renewed age, and automatic exact-proof cleanup after restoring settlement using finite timer advances. | PASS; failed-read liveness, coalescing, capped backoff and unchanged-idle age have behavioral oracles. |
| RF3 | `:316` retains a newer active turn. `:455` covers captured idle invalidated by started/waiting/unload/archive; `:584` covers on-disk owner, local generation, expiry and over-limit. `:651` requires no additional status read after a failed-read timer meets replaced ownership. `:301` covers shutdown with pending delivery scheduling; `:394` requires stop to finish while the response gate remains closed and zero timers/RPCs/input from a late response or notice. Proof-only lifecycle controls are at `:718`. | PASS; stale-response rejection and timer/shutdown cancellation are both covered. |
| RF4 | `:480`, “launcher-origin terminal-only notLoaded discovery never calls ensureLoaded or resumes,” waits beyond the load throttle and requires zero loads, extra resumes and input. `task1-mutation-load.log` shows the requested reconcile(false)-to-reconcile() mutation failing specifically with ensureLoaded count 1 versus 0. | PASS; launcher origin and a mutation-sensitive no-load oracle are present. |

**Observation versus delivery authority.** At
`connectors/launcher/src/codex/engine.ts:385`, observation deliberately uses
`reason(false)`, allowing unresolved delivery rows to obtain history recovery
without granting delivery eligibility. It still requires the current RPC,
running state, attachment, warm state and valid admission/liveness fences.
`verifyAdmission()` runs before and after the read (`:418`, `:425`);
the response also requires matching unload epoch, completion revision and
cached-status reference (`:426`). Status/started notices cancel the obligation;
archive and other dormant states cancel through their current reason, and
over-limit explicitly cancels at `:896`. Ownership is checked against the
authority file by admission, not inferred from completion.

Only the first running-to-idle observation records activity (`:431`). An
unchanged idle response cannot extend dormancy. Completion uses
`reconcile(false)` (`:441`), whose load and attach branches are explicitly
disabled (`:518`, `:534`). Discovery of waiting, systemError or notLoaded
does not authorize input; the four non-idle controls at test `:275` exercise
this distinction. No policy answer or generic refused-input replay is added.

**Prior findings and shared delivery/proof seams.**

- **TR61-01 — ADDRESSED.** `proofBlocksDelivery()` at `engine.ts:129`
  accounts for both retained failed proof and a receipt received before its
  queued cleanup. Checks at `:706`, `:726`, `:747`, `:642` and `:656` cover
  push, awaited checkpoints, transmit and awaited guard acquisition. An
  ordinary accepted row without proof retains its former behavior, and an
  early receipt for the current sending row does not block its own acceptance.
  Gap/watch tests `:669` and awaited-checkpoint test `:741` verify the relevant
  interleavings, original settlement arguments and eventual single delivery.
- **TR61-02 — ADDRESSED.** `engine.ts:168` captures exact proof before the
  fallible command. The independent retry at `:139` has one timer with capped
  1/2/4/8/15-second delays, immutable owner/attempt/claim/key/deliveries and
  provenance checks against the current row. It performs proof cleanup without
  loading, resuming or observing the thread. The restored test `:520` no longer
  calls engine.start(); expiry/unload and owner/generation/stop controls at
  `:718` exercise cleanup separately from delivery authority. Go explicitly
  allows exact-owner cleanup after expiry (`internal/cli/codexowner.go:77`),
  while its locked command rechecks authority and authenticated identity
  (`:260`, `:263`). Delivery is armed after cleanup only following current
  admission and live eligibility (`engine.ts:157`).
- **TR61-03 — ADDRESSED.** Receipt requests coalesce while queued/running
  (`engine.ts:493`). Exact successful cleanup marks the matching request
  confirmed (`:187`); a scan ahead of its queued item task can therefore
  retire the same attempt without a redundant fallible confirmation. The skip
  at `:499` requires that confirmation and continued row absence. Missing
  unconfirmed or altered provenance remains fail-closed (`:172`, `:148`).
  Request tokens are removed in finally and the stop drain. Both duplicate
  receipt cases at test `:765` require actual notices before first removal,
  switch redundant confirmation to failure after deletion, and verify live
  unchanged ownership, original-proof recovery and one subsequent delivery.
  Three provenance controls at `:803` and the existing receipt-during-scan
  case at `:1856` cover the interacting fail-closed/history paths.
- **TR61-04 — ADDRESSED.** Test `:679` now records a real gap timer before
  item proof exists; `:691` requires its future, uncancelled deadline and no
  confirmation. Proof arrives at `:694`, and `:699` requires the same callback
  to fire uncancelled while settlement still fails. The report and receipt
  explicitly withdraw the earlier inaccurate GREEN precondition claim.
  Corrected coverage passes unchanged fix1 source; disabling the barrier
  makes both variants fail at `:701` with three claim hooks versus two.

The proof-only timer is independent of observation retirement, not a claim
that every settlement invocation globally follows that cadence: while loaded
observation remains eligible, a history scan can also retry the same proof.
Those calls preserve exact provenance and serialized cleanup. No new defect
was identified in their interaction with the timer or receipt coalescer.
Accepted proof does not become a release: current Go receipt recording commits
display and claim retirement together (`internal/cli/codexreceipt.go:60`).
Known definite-mismatch retries keep their original batch/key and are excluded
from completion proof obligations once in waitingRetry (`engine.ts:450`);
test `:626` verifies terminal completion racing that transition. Recovering
unsent claims before scans avoids the previously observed immediate duplicate
release without changing accepted/ambiguous claim treatment.

**Adaptation judgment.** The opt-in fake terminal helper updates native state
and history while emitting only completion; its read gate captures a cloned
wire response. Existing notify behavior and ungated reads retain their
defaults. Engine-owned read-error injection excludes independent harness
barriers, so the failure tests exercise the production retry lane. Current
guard/settle evidence and filtered nonempty claim reservations preserve the
old behavioral intent under current contracts. Supported config edits may
queue reauthentication, but actual reads still await successful admission.
The omitted historical settlement schema is appropriately replaced by current
exact-owner cleanup; automatic retirement cleanup is now retained. These
adaptations preserve the required effects, including both halves rejected in
the original task review.

**Independently checked retained evidence.** Read raw logs, wrapper exits,
per-gate heads and dirty stats under the operational `logs/` directory;
counts and actual `bb: exit=` values agree with the reports.

| Gate | Pass / fail | Skip / cancel | Wrapper / build-box |
| --- | --- | --- | --- |
| task1-red-c | 5 / 1 | 0 / 0 | 1 / 1 |
| task1-mutation-load | 0 / 1 | 0 / 0 | 1 / 1 |
| task1-red-mismatch-race | 0 / 1 | 0 / 0 | 1 / 1 |
| fix1-red-both | 0 / 3 | 0 / 0 | 1 / 1 |
| fix1-red-boundaries | 3 / 2 | 0 / 0 | 1 / 1 |
| fix2-red-duplicate | 1 / 1 | 0 / 0 | 1 / 1 |
| fix2-gap-control | 2 / 0 | 0 / 0 | 0 / 0 |
| fix2-gap-mutation | 0 / 2 | 0 / 0 | 1 / 1 |
| fix2-green-focused | 41 / 0 | 0 / 0 | 0 / 0 |
| fix2-engine-rpc | 190 / 0 | 0 / 0 | 0 / 0 |
| fix2-ln-check | 2181 / 0 | 0 / 0 | 0 / 0 |

Corrected terminal RED fails the missing automatic wake, rather than fixture
or compilation setup. The duplicate RED fails unchanged-owner liveness;
its first-failure variant is correctly called a passing control. The gap
control is not misrepresented as RED; the deliberate barrier mutation fails
both tests after reaching their timer preconditions. Earlier mixed fixture
failures are explicitly discarded, and initial/full gates remain historical.
Current focused evidence includes all 39 F2 cases and two existing controls;
the full gate has 955 top-level cases. `just ln-check` includes strict
TypeScript via its ln-typecheck dependency (`justfile:168`).

Current working-file SHA-256 values exactly match
`logs/reviewed-fix2-source.sha256`; HEAD/tree match
`logs/reviewed-fix2-head-tree.txt`. Committed executable blobs independently
match the report and receipt:

- Engine: `6ca887a8b7a15f4f0ecdd36c4f4eba812433c7f2`.
- Engine tests: `455edb91421773b692d953db216df003be063295`.
- Fake server: `d521e3a1f394c4082ddd6ef813f16e70928646eb`.

`fix1-ln-e2e` really executed 16 passing synthetic launcher cases, with no
skips/cancellations and wrapper/build-box 0/0. It belongs to source
`8398a1fc`, not the reviewed HEAD. The raw log includes three .git-absent
build-metadata diagnostics before successful build/tests; that is not release
provenance. No concrete new public integration concern was found in fix2's
private cleanup coalescing and oracle repair, so another E2E run is not
requested under R4/R6's proportionate validation ruling.

**Limits and controller handoff.** No test/build was run during this review.
Per-gate dirty-base HEAD/stat artifacts do not reconstruct every historical
input byte. Tested-content equivalence is a supplied independently checked
controller fact, consistent with the current hashes and executable Git blobs;
it is not inferred from those per-gate HEADs alone. No unresolved source doubt
requiring another gate was identified.

This verdict covers the current source deliverable and its honest synthetic
acceptance evidence. Original incident attribution, F1 generic refusal
classification and live installed behavior remain unverified and explicitly
unclaimed. Closing plan checkboxes/receipt, copying committed ledger/review
artifacts, merge/push and external artifact preservation/cleanup remain
controller-owned work after this verdict.

This report is the only write. No source/index/HEAD/branch edit, commit,
subagent, test/build, private runtime access, service, GitHub write or real
message was used.

=== DONE ===
