# Codex terminal-only recovery — current source, 2026-11-15

Implements the source-proven terminal-only completion defect in #61. A terminal
notice triggers an authoritative read of the already-loaded owned thread, rather
than being treated as proof of idle or permission to send input.

## Source pins

- Base: `4adae810180f02970ea46c4920a46668dc6d82a7`, branch `fix/issue-61-terminal-recovery`.
- Historical reference only: `8e69ff5bb3f7aff4d7a6a90bd35823cd3c4461aa`.
- Engine blob: `6ca887a8b7a15f4f0ecdd36c4f4eba812433c7f2`.
- Engine-test blob: `455edb91421773b692d953db216df003be063295`.
- Fake-app-server blob: `d521e3a1f394c4082ddd6ef813f16e70928646eb`.

The historical branch was not merged. Public Engine/Binding interfaces, durable
row schemas and current Go owner/guard/settle contracts are unchanged.

## Behavior and safeguards

- Coalesce terminal notices into one observation and at most one trailing read.
  Failed reads retain one retry obligation with 1/2/4/8/15-second capped backoff;
  duplicates cannot bypass it.
- Fence replies by connection, cancellation/status generation, unload epoch,
  subscription, admission and owner. Reverify supported config changes before a
  retry can read status. Stop closes a blocked observation RPC before draining.
- Record the first running-to-idle transition as activity, including long turns;
  unchanged idle polls do not extend idle age.
- Use `reconcile(false)` for completion observations: no cold load or new resume.
  Existing history/item proof and durable Go settlement must succeed before that
  completion reconsiders delivery. Known rejected retries retain their existing
  claim and key; completed attempts do not supply receipt proof themselves.
- Fake terminal-only and captured-read controls are opt-in; the original fake
  notification behavior and all pre-existing test assertions remain unchanged.

## Evidence

All commands used the serialized operational gate on x-boss with two jobs,
private physical TMPDIR, umask 0022, lane-private HOME/XDG and synthetic fixtures.
The logs below live under `.superpowers/sdd/issue-61-terminal-recovery/logs/`;
matching `.exit` files contain wrapper exits, and logs include build-box exits.

```bash
bash .superpowers/sdd/issue-61-terminal-recovery/bin/gate.sh task1-red-c node --disable-warning=ExperimentalWarning --test --test-concurrency=2 --test-name-pattern='F2 terminal-only completion (drains|does not assume|queued)' connectors/launcher/test/codex-engine.test.ts
bash .superpowers/sdd/issue-61-terminal-recovery/bin/gate.sh task1-green-b node --disable-warning=ExperimentalWarning --test --test-concurrency=2 --test-name-pattern='F2|terminal-only' connectors/launcher/test/codex-engine.test.ts
bash .superpowers/sdd/issue-61-terminal-recovery/bin/gate.sh task1-engine-rpc-final node --disable-warning=ExperimentalWarning --test --test-concurrency=2 connectors/launcher/test/codex-engine.test.ts connectors/launcher/test/codex-rpc.test.ts
bash .superpowers/sdd/issue-61-terminal-recovery/bin/gate.sh task1-ln-check-final just ln-check
```

- Corrected RED `task1-red-c.log`: 5 controls pass, terminal-only drain fails
  “existing wake_idle must start without another delivery or idle notice”;
  wrapper/build-box exits 1/1. `task1-red-b.log` separately pins missing long-turn,
  idle-age, captured-response and backoff observation behavior.
- Initial RED fixture mismatches in `task1-red-a.log` are discarded evidence:
  modern fake settlement reserves accepted claims and retains empty claim rows.
  Assertions now distinguish nonempty reservations and inspect current guard and
  settlement operations. Error injection targets engine-owned responses after
  real fake-wire reads; `task1-probe-backoff.log` proved independent harness
  barriers could otherwise consume a server-wide injected error.
- `task1-green-b.log`: original 23 recovery cases pass, exits 0/0.
- `task1-mutation-load.log`: replacing completion `reconcile(false)` with
  `reconcile()` fails the launcher-origin notLoaded oracle (cold-load count 1,
  expected 0), exits 1/1. Mutation restored before all final gates.
- Self-review added definite-rejection/terminal racing and settlement retry
  controls. `task1-red-mismatch-race.log` reproduces the retained retry stall;
  `task1-green-d.log` passes 26 recovery/release cases, exits 0/0.
- Final engine/RPC: `task1-engine-rpc-final.log`, 177 pass, 0 fail, 0 skipped,
  0 cancelled; wrapper/build-box exits 0/0.
- Final canonical strict TypeScript and full launcher/hub-client:
  `task1-ln-check-final.log`, 2,168 pass across 942 top-level cases, 0 fail,
  0 skipped, 0 cancelled; wrapper/build-box exits 0/0.

## Limits and handoff

The tests cover synthetic ET1–ET3 and RF1–RF4, including 26 named F2 cases in the
initial engine suite. Fix round 1 below supersedes the initial settlement
adaptation: original proof is now retried automatically after idle retirement,
without reviving stale input or importing historical schemas.

Independent task/final source review, fresh controller integration/E2E gates,
merge/push and artifact-preserving cleanup remain controller-owned. No deployment,
real service, session, private state or real message was used. The roughly
eleven-message incident and F1 generic refusal classification remain open; this
receipt does not close all of #61 or claim an installed-release fix.

## Fix round 1 — controller R5 / TR61-01 and TR61-02

Base `ae754d3ee29ef5476d778d799b5c0292f2307e78`. The independent review
accepted both findings as Important; initial passing gates above are historical
for this revision. Current executable blob pins above include these fixes.

A central proof barrier now protects push, awaited checkpoints, transmit and the
post-guard boundary. Receipt arrival during awaited I/O blocks fresh input even
before its queued settlement operation runs; ordinary pending attempts without
proof retain their prior admission behavior. Failed proof settlement retains
original owner, attempt, claim, client key and full delivery set.

One coalesced 1/2/4/8/15-second capped proof-only retry confirms that exact proof
through current Go cleanup operations. It is independent of status observation,
subscription and expiry; the Go cleanup contract allows exact-owner confirmation
after expiry. Replacement owner/generation, altered attempts/deliveries, and stop
retain proof rather than authorize new input. Retirement still cancels loaded
observations and new delivery. Cleanup never reads, loads or resumes a thread;
only live, successfully verified admission may be armed after confirmation.

RED `fix1-red-both.log`: all three selected cases fail against unchanged source;
the gap takes a new claim (3 vs expected 2), and retirement leaves the original
row after finite timer advances. RED `fix1-red-boundaries.log`: expiry and
unloaded cleanup fail; owner/generation/stop are passing controls. Wrapper and
build-box exits are 1/1 for both gates. `fix1-green-focused-final.log` passes all
36 selected recovery/current-contract cases, exits 0/0. This includes the restored
timer-driven retirement oracle and eight new interleaving/lifecycle cases.

Fresh `fix1-engine-rpc.log` passes 185/185, and `fix1-ln-check.log` passes strict
TypeScript plus 2,176/2,176 launcher/hub-client tests across 950 top-level cases.
Both wrapper/build-box exits are 0/0; neither has skips or cancellations.
Full commands, raw log/exit paths and source commit are in the appended task report.

Fresh `fix1-ln-e2e.log`: 16/16 synthetic launcher E2E cases pass, no skips or
cancellations, wrapper/build-box exits 0/0. The remote checkout emitted Git
build-metadata diagnostics because .git is not copied; Go build and E2E succeeded.
Commands: gate `fix1-engine-rpc` with both complete engine/RPC test files, gate
`fix1-ln-check just ln-check`, and gate `fix1-ln-e2e just ln-e2e`. Independent
controller re-review remains required; no merge, deployment or incident/F1
acceptance is claimed.

## Fix round 2 — controller R7 / TR61-03 and TR61-04

Base `8398a1fcc66434f0c363baf3a88c18db81b607f1`. TR61-01/02 remain
addressed; prior full gates and E2E results above are previous-source evidence.

Coalesce receipt clears per key while their asynchronous operation is queued or
running. An exact successful history/receipt cleanup can mark that queued request
confirmed; it skips another fallible confirmation only if its row is still
absent. A live altered row, changed outstanding proof, or missing unconfirmed
row remains fail-closed. The coalescer is removed on completion/failure and stop;
failed first confirmation retains the independent proof retry. No durable schema,
public interface or fake-server default changes.

`fix2-red-duplicate.log` reproduces unchanged-owner quarantine on current source
when both item notices arrive before first cleanup, then a synthetic KV deletion
hook makes redundant confirmation fail. Wrapper/build-box exits 1/1 (one fail,
one failed-first-recovery control passes). `fix2-green-focused.log` passes 41/41,
including both duplicate variants and three altered/missing unconfirmed-proof
controls; exits 0/0.

Timer-claim correction: the fix-round-1 GREEN did not actually arm a gap timer
before proof existed. That earlier report claim is withdrawn. The corrected test
queues wake_idle while the original row has no proof, explicitly records the
roughly three-second remainder of the production five-second gap, asserts it is
armed and uncancelled before proof arrives, and observes its callback firing
while confirmation still fails. All no-claim/guard/turn and post-proof exactly-once
assertions remain. `fix2-gap-control.log` passes 2/2 on unchanged production
source, honestly recorded as a coverage control. Temporarily disabling only
`proofBlocksDelivery()` produces `fix2-gap-mutation.log` RED (both cases take an
extra claim, 3 vs 2; exits 1/1). The mutation was restored before final gates.

Fresh final affected/strict/full results and exact commands are in the appended
task report. No E2E rerun is required for private clear idempotence and an oracle
repair under R7; prior 16-case E2E remains previous-source evidence. Independent
same-reviewer re-review and final source PASS remain controller-owned.

Fresh final gates: `fix2-engine-rpc.log` passes 190/190, and `fix2-ln-check.log`
passes strict TypeScript and 2,181/2,181 launcher/hub-client tests across 955
top-level cases. All skips/cancellations are zero and wrapper/build-box exits
are 0/0. The barrier mutation was restored before these gates. Committed source
pins and all exact commands are in the Fix round 2 report appendix.

## Final source acceptance

Reviewed source `27b931dd9cac3dd4d6848d1c3f5506ca86f230cf`: independent
task re-review and fresh whole-branch review both PASS for spec and quality.
All four accepted findings, including the minor timer-precondition correction,
are addressed. See the adjacent review reports and ledger. No accepted finding
is deferred. Closing metadata preserves executable source hashes.

Final exact-source evidence: 41 focused, 190 engine/RPC, and strict TypeScript
plus 2,181 launcher/hub-client tests, all passing with zero skips/cancellations
and wrapper/build-box exits zero. The 16-case generic launcher E2E passed at
prior source `8398a1fc`; it is not an exact-current-HEAD integration run. R4/R6
and the independent reviews found no concrete new public integration risk
requiring replay for the private clear-coalescing/test-setup change. Metadata
diagnostics and historical per-gate content-manifest limits remain recorded.

This source acceptance supersedes the earlier pending-review handoff, and
supports merge to master. It does not claim another installed release, live
Codex session acceptance, original incident attribution or F1 generic refusal
classification; #61 remains open for those follow-ups. Raw logs/scripts/reports
must be copied and hash-verified outside the worktree before cleanup.

## Integration lifecycle

Merged and pushed to master at `4a9f8f83`; executable blobs match reviewed
source `27b931dd`. GitHub #61 progress was posted, and the issue remains open.
All seven worker processes exited zero. The operational workspace was copied
to `.scratch/vecnet/issue-loop/2026-11-15/issue-61-terminal-evidence` and all
171 files were hash-verified before removing the owned worktree and merged
branch. `ARCHIVE-MANIFEST.json` retains the complete copy hashes.
