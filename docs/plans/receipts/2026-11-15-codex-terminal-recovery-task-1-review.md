# Independent Task 1 review — terminal-only completion recovery

Spec compliance: **FAIL**

Code quality: **FAIL**

Reviewed BASE `4adae810180f02970ea46c4920a46668dc6d82a7` to HEAD
`ae754d3ee29ef5476d778d799b5c0292f2307e78`. Findings: Critical 0,
Important 2, Minor 0. Both findings require correction and independent
re-review; neither is accepted as a deferred follow-up. The original incident
and F1 remain outside this source deliverable and do not cause these verdicts.

## Scope and method

Read the binding task brief, implementation report, progress rulings, frozen
issue, and supplied review diff. Inspected the complete current engine and the
added test functions, with focused historical-source reads to compare the
settlement adaptation's behavioral oracle. No tests or builds were run.
No child agents, services, messaging, runtime/private state, GitHub writes,
index/branch changes, or production edits were used. This report is the only
write. The read-only dispatch supersedes the review skill's agent fan-out.

Focused outside-seam reads had these concrete purposes:

- `ownership.ts` and `internal/cli/codexowner.go:299`: determine whether Go's
  guard supplies the missing pending-receipt barrier identified in TR61-01.
- `internal/cli/codexreceipt.go:60`: distinguish accepted reservation from
  durable display/receipt settlement; they are different operations.
- Existing engine settlement tests and `fake-vecnet.ts`: check whether a failed
  receipt leaves a pending row and whether a different claim can be guarded.
- Historical engine/test excerpts: compare the omitted settlement scheduler
  and the automatic post-retirement confirmation oracle, rather than importing
  the old schema or implementation.
- `justfile` and the existing gate/build-box scripts: interpret strict-check
  execution, serialization and environment isolation in the retained evidence.

## Findings

### TR61-01 — Important — An already-armed delivery timer bypasses completion settlement

**Location:** `connectors/launcher/src/codex/engine.ts:373`,
`connectors/launcher/src/codex/engine.ts:396`; interacting existing entry points
`connectors/launcher/src/codex/engine.ts:635` and `:663`.

**Scenario:** A wake was accepted while active, its item receipt arrives, and
durable `codex settle --receipt-key` fails. The existing receipt handler retains
the original row in `pending` state and retains its receipt key. A retained
`wake_idle` has already armed a debounce or minimum-gap timer. Terminal-only
completion then observes idle. The history/receipt scan fails settlement again,
so the completion lane correctly withholds its own `arm(0)`. However, it has
already published idle into the shared cached status. When the pre-existing
timer executes `push()`, liveness is true and the only row gate rejects
non-pending rows. The old row is still pending. Nothing in `push()` consults
`completionRows` or the failed receipt obligation, so it can claim and start the
retained wake before the original receipt settlement succeeds.

This does not require another notice after completion: the normal production
five-second minimum gap can leave precisely this timer outstanding. It can
also occur with a subsequent delivery trigger. Go does not close the gap:
`guard.finish('accepted')` removes the pending-send marker at
`codexowner.go:329`, while receipt settlement is a later operation. A new claim
can therefore acquire a guard despite the earlier accepted claim's unconfirmed
receipt. The existing fake models that distinction as well.

**Effect:** The completion path's advertised proof-before-reconsideration fence
is local to its own scheduling decision, rather than an invariant of the serial
delivery lane. It violates brief lines 24/41 and ET3. This finding does not claim
the same delivery is immediately duplicated: its accepted claim remains
reserved. It identifies fresh input admitted before the required original
proof has durably settled.

**Fix/verification:** Carry the completion-related failed settlement obligation
into admission for every delivery entry point, including after awaited
checkpoint/claim operations as appropriate. Preserve ordinary pending-row
behavior, the current owner/admission/guard gates, and known definite-rejection
retries. Do not merely suppress the completion lane's own timer. Through the
controller's serialized x-boss gate, add a regression with the real minimum-gap
shape: retain the original accepted row, fail its settlement, leave a gap timer
armed for queued wake_idle, complete without a status notice, and advance past
the gap while settlement still fails. Assert no new claim/guard/turn RPC; then
restore settlement and assert original proof/attempt/owner/key confirmation
precedes exactly one queued wake delivery. Also cover an independent watch
trigger while the same proof is pending.

**Evidence boundary:** This is a source-derived interleaving, not a newly
executed failing probe. The current tests at `codex-engine.test.ts:520` and
`:560` flush `h.wake()` timers before completing the turn and never exercise
this outstanding-timer interleaving. Their passing results do not refute it.

### TR61-02 — Important — Idle retirement abandons automatic receipt confirmation

**Location:** `connectors/launcher/src/codex/engine.ts:401`, with cancellation
at `:444`/`:246`; weakened oracle at
`connectors/launcher/test/codex-engine.test.ts:553`.

**Scenario:** Use the added unchanged-idle/failed-settlement case. A real
accepted attempt has an item receipt and retained original row. Completion
observes idle; repeated original receipt confirmations fail. Once idle age
expires, `reason(false)` becomes `idle_stale`, reconciliation unsubscribes and
cancels completion recovery. The finalizer cannot schedule another completion
retry. Now let settlement become available without another native event or
external engine recovery call. No remaining timer retries accepted receipt
confirmation: claim recovery handles `releasing:`/`claiming:` records, not
accepted `inflight:` rows; heartbeat does not scan these rows.

**Effect:** The original receipt/claim remains unsettled indefinitely until a
separate recovery trigger. The new test explicitly calls `h.engine.start()`
to hide that missing automatic half. The historical F2-I2 oracle instead
cleared the receipt failure and advanced timers, requiring the original proof
to confirm after idle retirement without reviving stale wake_idle. Brief lines
31/39/63 require preserving historical behavioral oracles/concepts. Switching
to current Go settlement commands is permitted; replacing automatic
confirmation with an explicit engine recovery call is a behavioral omission.
There is no progress ruling waiving that requirement. This is an incomplete
port of a specified behavior, not a demand to merge the historical settlement
subsystem or to solve F1.

**Fix/verification:** Retain a bounded original-proof settlement retry distinct
from loaded-thread observation/delivery eligibility. It may reuse current
inflight rows and Go settle operations; no historical schema is required.
Idle retirement must still cancel loaded-thread observation and new input,
while proof-only confirmation can finish under the existing exact-owner
cleanup contract. Stop must cancel the additional scheduling correctly.
Replace the explicit `engine.start()` in this added test with finite timer
advances after restoring settlement. Assert original settlement arguments,
eventual row cleanup, no release of an accepted claim, no renewed subscription,
no cold load, and no stale wake delivery. Run only through the controller gate.

**Evidence boundary:** The final passing test proves idle age and retention,
and explicitly proves cleanup after an external recovery call. It does not
prove autonomous post-retirement confirmation. The implementation report and
receipt acknowledge this adaptation, so the omission is not concealed, but
acknowledgement does not satisfy the binding oracle.

## RF-by-RF behavioral pins

All test locations below are in `connectors/launcher/test/codex-engine.test.ts`;
the final retained engine/RPC log names all 26 F2 cases as passing.

| RF | Concrete tests and oracle | Assessment / missing half |
| --- | --- | --- |
| RF1 | `:201` “terminal-only completion drains queued wake_idle exactly once after an authoritative idle read”: only terminal notice, engine-owned read before input, exact one guard/send/receipt, owner/claim/attempt/key agreement, duplicates do not redisplay. `:492` “long-turn terminal-only idle starts retained wake_idle before idle dormancy”: two-minute active interval does not count as idle age. `:560` “terminal-only failed settlement retries original proof before draining retained wake_idle”: original settlement arguments repeat before the new delivery. | Basic and long-turn recovery pass. The settlement gate is incomplete for an existing timer/watch entry point (TR61-01). The post-retirement proof continuation is weakened (TR61-02). |
| RF2 | `:332` both F2-I1 cases independently recover initial/trailing read failures without another event; engine-owned response failures exclude harness barriers. `:427` finite burst counts one in-flight and at most one trailing read. `:599` seven errors check 1/2/4/8/15/15/15 deadlines and duplicates cannot bypass them. `:520` repeated unchanged-idle successful observations preserve original row/proof, become idle_stale and unsubscribe. | Status-read backoff/coalescing and unchanged idle age pass the stated oracles. The unchanged-idle test's automatic confirmation half after retirement is missing (TR61-02); no need to claim the backoff test itself failed. |
| RF3 | `:316` stale terminal event observes the newer active turn. `:455` four captured-idle variants fence started/waiting/unload/archive. `:581` captured replies are fenced by owner/generation/expiry/over_limit. `:648` failed-read timer rechecks replaced on-disk ownership before issuing another read. `:301` queued completion cannot restart after stop. `:394` blocked-read stop closes RPC and finishes before response release; late reply/notice arms zero timers. | PASS for reviewed status-observation/cancellation behavior. Generation, status reference, connection, subscription, closed epoch, admission and running checks are present. Not every invalidator has a separate failed-retry-timer permutation; source cancellation covers those paths, and no concrete extra defect was found. A future independent settlement timer must retain these shutdown/owner constraints. |
| RF4 | `:480` launcher-origin notLoaded discovery waits past the load throttle, supplies only completion, then asserts ensureLoaded count zero and no new resume/send. Retained `task1-mutation-load.log` reports the expected failure at ensureLoaded (actual 1, expected 0) when the completion reconcile call is mutated. | PASS. This is the requested launcher-origin mutation witness, not the older explicit-origin control. Completion uses `reconcile(false)` and cannot attach through that path. |

The non-idle discovery family at `:275` covers approval, user input, systemError
and notLoaded without a claim, reply, cold load or new subscription. Existing
attendance, policy, hold, identity/root, ambiguity and rejection tests remain
in the full suite. The source keeps `verifyAdmission()` before and after the
observation, owner identity comparisons, root verification, expiry and liveness
gates; it does not replace them with completion authority.

## Claimed adaptations and deviations

1. **Current fake and proof API — accepted.** Empty reservations are filtered
   when asserting nonempty claims; current guard/settle/accepted-reservation
   evidence replaces obsolete fake display files. This preserves the claim and
   attempt oracle. `completeTurnOnly` and read gates are opt-in methods; existing
   `notify()` still emits its original idle status and ungated read behavior is
   unchanged. No original assertion was removed (controller's +472/-0 fact).
2. **Initial mixed RED discarded — accepted.** Corrected RED establishes the
   production defect; non-idle and shutdown cases are correctly called controls.
3. **Engine-owned failure injection — accepted.** It throws after an actual
   engine wire read and does not let the harness barrier consume the injected
   failure. No production retry semantics are excused by the fixture change.
4. **Recover claims before scans — accepted.** This avoids an immediate second
   release attempt in the same completion pass, retains the release retry lane
   and preserves the existing release-once assertion.
5. **Known mismatch waitingRetry exclusion — accepted.** It removes a
   definitely rejected retained batch from receipt obligations and routes it
   through unchanged transmit/claim/key logic. The mismatch-race test and RED
   evidence support this; it is not generic refusal replay or an F1 fix.
6. **Supported config reauthentication — accepted.** A retry may enqueue fresh
   admission for an already authenticated config, but the actual observation
   still awaits successful verification; replaced ownership is checked before
   reading. These are necessary current-source fences, not an authority bypass.
7. **Omitting the historical independent settlement subsystem — accepted only
   as an implementation choice.** Unchanged schemas and current Go receipt
   operations are appropriate. Omitting automatic post-retirement confirmation
   is rejected (TR61-02), and the resulting fresh-delivery proof barrier is
   incomplete (TR61-01). A smaller current-source implementation can retain the
   required behavior without importing the old subsystem.
8. **Scope and controller gate substitution — accepted.** Five owned paths,
   no public API/schema/dependency change, bounded observation work, and the
   source-only incident/F1 limitations match the brief. Progress R1–R4 permits
   the runner and proportional suite reuse; it does not waive proof behavior.

## Retained raw gate evidence

Read raw `.log`, `.exit`, `.head` and relevant `.dirty` files under
`.superpowers/sdd/issue-61-terminal-recovery/logs/`. Independently extracted all
reported gate totals/exits; they agree with the report. Notable gates:

| Gate | Pass / fail | Skipped / cancelled | Wrapper / build-box |
| --- | --- | --- | --- |
| baseline | 140 / 0 | 0 / 0 | 0 / 0 |
| task1-red-a | 1 / 14 | 0 / 0 | 1 / 1 |
| task1-red-b | 1 / 7 | 0 / 0 | 1 / 1 |
| task1-red-c | 5 / 1 | 0 / 0 | 1 / 1 |
| task1-mutation-load | 0 / 1 | 0 / 0 | 1 / 1 |
| task1-red-mismatch-race | 0 / 1 | 0 / 0 | 1 / 1 |
| task1-green-d | 26 / 0 | 0 / 0 | 0 / 0 |
| task1-engine-rpc-final | 177 / 0 | 0 / 0 | 0 / 0 |
| task1-ln-check-final | 2168 / 0 | 0 / 0 | 0 / 0 |

Corrected RED fails specifically at “existing wake_idle must start without
another delivery or idle notice,” not compilation. Its dirty stat lists only
the test/fake changes, with the base HEAD. The additional red-b failures pin
missing engine observation and long-turn/backoff behavior; the launcher-origin
case already passes there. The mutation log fails exactly the cold-load
counter, and the mismatch-race RED lacks the expected third turn/start.

The `just ln-check` dependency chain includes strict TypeScript before the full
launcher/hub-client suite; the retained actual build-box exit is zero. The full
suite has 942 top-level cases and 2,168 tests. The gate and remote runner source
confirm serialization, two jobs, umask 0022, physical temporary directory and
lane-private clean HOME/XDG setup. No reported suite was rerun for this review.

Current source Git blob hashes independently match the report/receipt and HEAD:

- Engine: `a05397fd3254c11af481fa292c1e598e74324710`.
- Engine tests: `d250987ed6d7be27bc08321a8ad26504cc9dc397`.
- Fake server: `d521e3a1f394c4082ddd6ef813f16e70928646eb`.

The three current SHA-256 values also match the retained controller
`integration-source.sha256`. Final task gates ran against dirty base HEAD;
per-run head/stat files alone cannot establish byte-for-byte historical source
equivalence. The controller supplies that equivalence as a dispatch fact, and
the source pins checked here agree. Independently reconstructing each gate's
input would require its preserved content manifest/snapshot; this is an
evidence limit, not an assertion that those suites were absent or failed.

## Cannot-verify items and required handoff

- TR61-01's outstanding-timer/watch interleaving has not been executed in this
  review. Controller-owned focused RED/GREEN through the serialized gate is
  needed, followed by proportionate affected checks after source changes.
- No current test proves automatic receipt confirmation after retirement;
  restore that oracle and retain raw results for TR61-02. Passing with an
  explicit recovery call is insufficient evidence for the missing behavior.
- Controller E2E completion, any later whole-branch review, merge/push, external
  artifact preservation and cleanup are not certified by this task review.
  Supply their final raw exit/source-pinned artifacts at their own gates.
- No incident-time attribution, installed-runtime behavior, real-message result
  or generic refusal retry classification was examined or is claimed.

Disposition: return TR61-01 and TR61-02 to the implementation/controller loop.
Both remain open; no source PASS or merge approval is given. No additional
concrete minor finding was identified.

=== DONE ===
