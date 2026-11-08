# Disposable Fly native harness — synthetic evidence

Source/plan commit: `f4d0721a21e48e598c15f574b2a88baa47701e29`.
This report is LAST for the new Fly variant. Main was its sole producer; the
shared harness owner independently supplied source `6cc526ba` / report
`c8b74d3b`. No shared helper or cleanup source was changed by this variant.

`scripts/native-launcher-fly-smoke.mjs` consumes the private disposable-peer
handoff described in the final acceptance plan. It refuses the persistent
launcher/coordinator IDs, requires distinct peers, owned mode-0600 invitation
authority in a mode-0700 parent, and enrols into a fresh private configuration
using tokens on stdin. Returned peer IDs must match. It verifies the approved
source/runtime inventory, CLI/demo bytes, installed snapshot and actual native
RPC version before dispatch, then compares supported session model/provider/
effort fields with the approved values. Unknown or mismatched identity fails.

The same shared journal writes/fsyncs exact request bytes/key/deadline before
wire dispatch and job/thread/launch-message IDs before retry. A disagreeing or
lost retry response restores the ambiguity hold. Reads, native inspection,
restart, follow-up and cancellation use only saved owned identifiers. The
shared workspace proof checks the owned worktree branch/base and preservation
of the parent branch/HEAD/dirty/untracked sentinels. The shared cancellation
helper and cleanup sequence require exact terminal sender/job/thread proof,
unit bytes and inactive/MainPID 0 before unit removal and daemon reload.

The runtime receipt deliberately has `runtime_pass:true`, **no** final
`pass:true`, and both exact peers in `operator_cleanup_pending`. Private
credentials/journal remain after service teardown. Only CoS's subsequent
operator disable-and-verify evidence completes production acceptance and
authorizes retiring the run's private configuration. No real invitation,
credential, Fly request, model, service or provider probe was used here.

## Actual chronology and limitation

The initial 14-test draft preceded behavior source, but its first execution
failed because the new module was absent: 3 pass / 11 fail, actual exit 1.
**That was a setup failure, not behavioral RED; the three rejection passes
were not valid controls.** Main then authored an unexecuted behavior draft
before reading R-L17's clarification. This was a workflow lapse. The draft
was preserved at SHA-256
`c0e7076fa40ca5243643fc26ca9342c53b519ab78259fb61bd2cffbdd1986927`;
there is no genuine first-implementation test-first claim for that draft.

Main strengthened rejection checks to exclude missing-module failures, saved
the draft, and replaced the entry with a minimal loadable unsupported scaffold
that returns an empty result and performs no actions. Its SHA was
`847619db6f9f30378cc438bb2e285f4b171f1db1b62eddf35719d07893aa468b`;
the test SHA was
`06237f25276841edfff7173ede16a392761dab23d8454d2c663ca852f3b28cc7`.
Execution then reached intended missing behavior/assertions: **0 pass / 14
fail**, actual exit 1. This is a retrospective sensitivity baseline, not an
erasure of the earlier chronology. Restoring the saved implementation produced
14/14 pass, exit 0.

Before changing invitation-parent handling, a new focused test used a 0755
authority parent and failed with “Missing expected rejection”, 0/1 pass,
actual exit 1. Only then was the explicit owned-0700-parent check added.
The final combined Fly/shared smoke/cleanup run passed **67/67**, actual exit
0, with no skipped/cancelled tests: 15 Fly cases plus the existing 52 cases.
This last parent correction has its own observed RED-before-fix chronology.

Commands were sequential in CX's explicitly returned sole slot. After the
last child exited at 05:20:53 UTC, main STOPPED/drained and granted that slot
exclusively to the fresh ACP fixture owner. CoS's earlier loan was returned
unused. No extra execution slot was created.

## Evidence and scope

Raw logs and exact command/actual-exit receipts:
`/work/package-cache/xertrov/vecnet-fly-harness-fix1/`.
They preserve initial setup failure, scaffold baseline, initial GREEN, focused
authority-parent RED and final combined GREEN. Tests execute real orchestration
and shared inventory/journal/workspace/settlement/cleanup helpers with synthetic
host commands, HTTP and RPC, and private disposable fixture files.

The 15 cases cover successful runtime evidence with operator cleanup pending,
exact pre-send body and accepted-ID journaling, lost response, partial install,
signalled restart, stop/inactive refusal, disagreeing retry thread, foreign job
and terminal thread, wrong terminal state, unapproved artifact/runtime/provider,
persistent identity and nonprivate authority parent. Matching marker/session/cwd
data cannot override these failures.

The runner's whole tracked-source inventories were stable for each command.
The new scripts were then untracked, so those inventories alone do **not**
freeze their bytes. Separate explicit before/after final-source manifests match:

```text
894a83cf2695de1cc0989e9f4d5489716b193142be00a500744985f82f12dee9  scripts/native-launcher-fly-smoke.mjs
903b8d73264496a087288e489f52894338805f5d0edb5abc3786b539ec47bd95  scripts/native-launcher-fly-smoke.test.mjs
```

The final source commit contains those exact bytes. The approval plan changed
after synthetic execution to document the same behavior and final 44-case E2E
gate; no behavior/test bytes changed. Evidence includes the saved early draft
and scaffold/source hashes, with the chronology limitation above explicit.

The default real sender TOML/HTTP adapter and real process-level signal exits
were inspected in source, not executed by these injected tests. The shared
host-wrapper signal/timeout tests are included; no actual daemon/provider
availability, effective route, credential enrolment, restart, cancellation,
peer disablement or production acceptance is established. The whole combined
Node/TS/npm/E2E gates and independent harness review remain required before any
live execution. CoS owns invitation issuance, both peer disablements and final
integration/release; the separate xsm direct-approval hold is unchanged.
