# Codex terminal-only recovery — current source, 2026-11-15

Implements the source-proven terminal-only completion defect in #61. A terminal
notice triggers an authoritative read of the already-loaded owned thread, rather
than being treated as proof of idle or permission to send input.

## Source pins

- Base: `4adae810180f02970ea46c4920a46668dc6d82a7`, branch `fix/issue-61-terminal-recovery`.
- Historical reference only: `8e69ff5bb3f7aff4d7a6a90bd35823cd3c4461aa`.
- Engine blob: `31a3f5ca103f9486343c1fb4b4cb379f0bca459e`.
- Engine-test blob: `6d6ab21012f9ded4d7817f3bc03c46a7af88c358`.
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
