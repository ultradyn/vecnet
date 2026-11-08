# Launcher live harness fix1 — synthetic verification receipt

Plan-Task: launcher-live-harness-fix1

Source/plan commit: `6cc526bac143648a1c09c390922ca717f3f0ea45`.
Parent/frozen native3 source: `e694d76184e0ab4caacc2c4eca61143e1c859c0d`.
Branch: `feat/launcher-codex-live`. Same former UI context was the sole owner
of this harness batch. This report is the separate LAST commit; its immutable
HEAD is supplied in the handoff.

## Scope and six corrections

1. **Outer recovery lifecycle.** The local smoke opts into retained demo data
   via `recovery.onReady` / `recovery.canDispose`. Before any launcher activity,
   its own 0700 directory receives a 0600 atomic authority file containing the
   disposable demo origin, sender peer, sender secret, data directory and exact
   binary path. This is newly generated synthetic/disposable authority, never
   copied real-account auth. Browser/hub process closure, child signals and
   exit/signal backstops cannot authorize data disposal. Unresolved dispatch,
   partial install and all six existing teardown failures preserve hub history
   and private recovery material. Default demo disposal is unchanged. Proven
   cancellation, complete teardown and acceptance authorize disposal; successful
   smoke completion then retires the disposable authority file.
2. **Strict host commands.** `runHost` consumes both close arguments, tracks
   timeout independently, and accepts only normal code exactly 0 without a
   signal or timeout. Captured stdout/stderr and arguments do not enter command
   errors. A killed reload retains recovery and prevents receipt/PASS.
3. **Durable dispatch and acceptance.** The exact body/key/deadline is atomically
   written and fsynced before route continuation permits dispatch. An independent
   response handler validates and synchronously journals job/thread/message IDs
   before later waits/restarts, including a click that subsequently rejects.
   Installation intent/unit bytes precede install. Replays use the journal body
   and require all three accepted IDs to match. Recovery never generates a new
   request key. The synthetic durable-ledger replay tests resolve the original
   IDs after dispatch interruption, lost response and post-acceptance interruption.
4. **Cancellation proof after teardown.** Settlement binds terminal messages to
   exact job ID, launcher sender and thread. Succeeded/failed can authorize safe
   teardown but cannot pass the cancellation scenario. A foreign-job cancelled
   message cannot satisfy it. The final receipt requires the exact cancelled
   acknowledgement, launch message ID and complete teardown, and is published
   only after teardown. No early `pass: true` receipt is assembled.
5. **Approved artifacts and honest runtime identity.** An explicit approved
   manifest is mandatory. Before launch, verify source revision, complete source
   runtime inventory, CLI/demo digests, actual installed snapshot bytes/path and
   approved runtime executable bytes. Native records the actual RPC version and
   requires the approved version. ACP verifies the approved wrapper, actual
   adapter entry and fixed Node digests, nested scoped package version/name and
   actual initialize protocol/agent version. The bootstrap workspace package is
   not adapter identity. ACP effective provider/model/effort remain explicit
   nulls with an attestation limit; observed model turns establish availability
   in a future live run, not an effective-model attestation. Marker text cannot
   override an artifact/version failure. The native executable digest is the
   controller-inventoried binary; RPC version alone does not attest its path.
6. **Workspace preservation.** Capture and compare parent branch, HEAD, full
   porcelain status and tracked/untracked sentinel digests. Project-root retains
   its original branch/content; worktree requires the owned branch and base
   commit without changing the parent. Native temp is an independent Git repo
   on its owned branch; ACP temp is a directory without Git or branch. Correct
   cwd/session/marker/consumed metadata cannot override a workspace failure.

The existing cleanup helper and all nine original cleanup cases are unchanged.
Production changes are only the local smoke, new shared smoke-support module,
the opt-in demo lifecycle, their synthetic tests and the exact acceptance plan.
No backend, daemon, Go, SQL, UI static, configuration or credential file was
edited. Main's daemon test and Fly drafts were preserved and never staged.

## Tests-first chronology and actual command exits

All executable gates used the controller-granted sole CX slot, sequentially.
The test driver evaluates the real production smoke control flow with rebound
filesystem/host/browser/RPC ports. Host processes are EventEmitters, browser/hub
responses are synthetic, and filesystem access is limited to disposable test
directories. Lifecycle exit/signal hooks use a synthetic process, never signals
to the test runner or real service. No missing feature-module import supplied
the six-finding RED.

Before any production edit, an initial draft driver run exposed an option-name
shadow in its own injected cancellation/provider port. The driver was corrected
without changing production or assertions; its earlier raw log is retained as
`red-draft-driver.log`. The corrected behavioral RED and source hashes preceded
production. All nine old cleanup cases passed in RED.

| Phase / command | Direct command and Node exit | Actual tests | Raw log under `/tmp/` |
| --- | --- | --- | --- |
| RED: `node --test --test-concurrency=1 scripts/native-launcher-smoke-support.test.mjs scripts/native-launcher-cleanup.test.mjs` | 1 / 1 | 47 total, 12 pass, 35 fail | `vecnet-live-harness-fix1-red.log` |
| RED: `node --test --test-concurrency=1 --test-name-pattern='H1' scripts/ui/lib.test.mjs` | 1 / 1 | 5 total, 1 pass, 4 fail | `vecnet-live-harness-fix1-red-lifecycle.log` |
| Additional native-temp RED: `node --test --test-concurrency=1 --test-name-pattern='H6 native temp' scripts/native-launcher-smoke-support.test.mjs` | 1 / 1 | 1 total, 0 pass, 1 fail | `vecnet-live-harness-fix1-native-temp-red.log` |
| Final GREEN: `node --test --test-concurrency=1 scripts/native-launcher-smoke-support.test.mjs scripts/native-launcher-cleanup.test.mjs` | 0 / 0 | **52/52 pass** | `vecnet-live-harness-fix1-green.log` |
| Final GREEN: `node --test --test-concurrency=1 --test-name-pattern='H1' scripts/ui/lib.test.mjs` | 0 / 0 | **5/5 pass** | `vecnet-live-harness-fix1-green-lifecycle.log` |

These were direct `exec_command` invocations, with no build2/bb wrapper; there
is no bb exit to infer. Separate `.exit` files record each actual status.
Final suites have zero failures, skips or cancellations. The main suite has 47
top-level cases plus five nested host-command cases (52 tests); the original
nine cleanup tests are included. The five lifecycle cases cover default/proven
disposal and retained close, signalled child, process exit and SIGTERM paths.
The two unrelated original browser-based lib tests were deliberately not run;
this is a focused synthetic lifecycle gate, not a full browser-suite claim.

An initial GREEN attempt passed 44/47 and failed three: two interruption cases
required more conservative outer disposal, and the project-root fixture had
incorrectly treated returned session metadata as its initial parent branch.
The fixture was corrected without weakening its assertions and production now
retains outer recovery on failed acceptance. Raw `green-initial.log` is retained.
The next run passed 47/47; four additional artifact mismatch cases raised it to
51/51. Before committing, source inspection caught the native/ACP temp distinction
and the worker explicitly released the slot while drafting its regression.
After main's borrowed slot drained, a fresh grant produced the additional RED
above, then the minimal helper correction and final **52/52 + 5/5** GREEN.

RED examples observed: outer hub database deleted after failed cleanup;
signalled/timeout command falsely resolving; absent pre-dispatch journal;
pre-existing succeeded/failed or foreign cancellation incorrectly passing;
unapproved artifact/version still reaching dispatch; and wrong branch/HEAD or
mutated sentinel still passing markers. This is behavioral evidence from the
original source, not a syntax-only exit or assertion relaxation.

## Frozen sources and safe evidence copy

`/tmp/vecnet-live-harness-fix1-red-source.sha256` precedes production.
`/tmp/vecnet-live-harness-fix1-native-temp-red-source.sha256` precedes the temp
correction. `/tmp/vecnet-live-harness-fix1-green-source.sha256` was captured before
final GREEN and verified afterward and before the source commit:

```text
36fc7de9053f942912bb454034acac0a1d6abfb599bd8fbe7b707e2b8c26f214  scripts/native-launcher-live-smoke.mjs
e664759b0806419a301e1b967189da970f501fa37f56b3fe69678f890286361f  scripts/native-launcher-cleanup.mjs
dcf3091e055e95f3f8eeb60a232848bd17f056f201419c2b7d34e7dc3dc28255  scripts/native-launcher-cleanup.test.mjs
c399f3787ffd3019fa989274f57774e470da43af147e3a2af61bc5a8e822c9b9  scripts/native-launcher-smoke-support.mjs
d50ae2eed535483f1ac9bd300243d470e79cc9dfa4fe9a079a7f910115cab430  scripts/native-launcher-smoke-support.test.mjs
ee1cede6f25ee9d34619751c1782ff1e47a10d03de0255148ec0bd1793359500  scripts/ui/lib.mjs
dc2b88861b19a9b0fa3659c932786f355c358f3f3ccecedaf60955ccdacf2b04  scripts/ui/lib.test.mjs
c43fa1fcca407cc1007e0769f93f444343da1a60b3e3eeaa7ac1a3a53373108e  docs/plans/launcher-final-acceptance-plan.md
```

Raw log digests:

```text
091dc25940901325612a5f6fe440928402714b78322d5ecfe0079410f3f54f49  vecnet-live-harness-fix1-red.log
e0efe5e3d91b1e4bef4baad7a07cc1917f4fa7ab5442ef1086face05306339d2  vecnet-live-harness-fix1-red-lifecycle.log
79962daec1567c071739a6c11dc128fbb02999805df3e60ff6188dbf19afbbc5  vecnet-live-harness-fix1-native-temp-red.log
f18bf987efb14963929eee436434edca1612f36d975e0471425e294cdf307369  vecnet-live-harness-fix1-green.log
09f8bbdc932acca8c41735da2773d5b10cbb827aa9b613203ee69547ca719747  vecnet-live-harness-fix1-green-lifecycle.log
```

Safe raw copy, command/exit receipts, intermediate chronology and source hashes:
`.scratch/vecnet/integrations/launcher-live-harness-fix1-evidence/` in this
worktree. Directory mode 0700, artifact mode 0600. Its `SHA256SUMS` digest is
`d8855e99abbf822943f763217ea206533ec6fac9ded54af20e9c0f24070184a8`.
Only synthetic logs/public source inventory were copied; no real credentials,
config, hub database, unit, session state or live recovery authority were read
or copied. Main owns remote mirroring/copying after handoff.

## Shared ports and operational limits

Stable support exports: `runHost`, `createSmokeJournal`,
`verifySmokeApproval`, `verifyInstalledSnapshot`, `probeSmokeRuntime`,
`repositoryState`, `verifySmokeWorkspace`, `settleSmokeJob`, and
`finalizeSmokeReceipt`. Journal methods are synchronous `request`, `accepted`,
`update`, `snapshot`, and `privateFile`. `cleanupNativeSmoke(state, ops)` remains
the single injected cleanup sequence. Main owns the disjoint Fly producer and
may take over Fly plan details after this handoff; this worker did not write it.

There was **no live/provider/browser/service/auth probe**, Go/TS/package build,
real unit operation, model session or intake activation. Fixture versions/model
names/digests are synthetic and establish no provider availability. The actual
ACP adapter's EOF/initialize behavior and the approved daemon inventory still
require the controller's authorized live gate after independent source review.

The plan preserves R-L11: Fly acceptance requires a fresh disposable launcher
and separate sender through fly-operator, exact owned job/unit scope and verified
peer disablement afterward. Loopback demo disposal is not Fly peer disablement.
Frozen xsm/a9 core harness and its separate direct approval remain untouched.
Same independent Lens fifth scoped re-review remains controller-owned; no review
acceptance or final operational PASS is claimed by this receipt.

## STOP / drain / source handoff

Final Node children exited and were reaped. The owned-process scan found no
smoke/support/cleanup/lifecycle test or live-smoke child. The sole CX executable
slot was explicitly released immediately after final GREEN, before plan/report
writing, evidence copying or commits; no later executable gate was started.

The staged source list was inspected and contained exactly the six authorized
source/test/plan files in `6cc526ba`; unrelated daemon/Fly work was excluded.
Scoped `git diff --check` and frozen hashes passed. This report is committed
separately and LAST with `Plan-Task: launcher-live-harness-fix1`, then the feature
branch alone is pushed to GitHub. No merge, tag, deploy or runtime activation.

DONE
