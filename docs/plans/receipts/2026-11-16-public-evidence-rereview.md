### Finding verdicts

Scope: final fix package `f8cedeac6f096cd8cc5a65645cde3c9d8692fe33..50717f47a8b6daf0b3b59af9f5e74a700dfb40eb`, the three original Important findings, and rulings R8–R10. Read the re-review brief, original findings, complete fix report, fix diff, relevant numbered code/tests, ledger rulings and supplied gate/RED logs. No subagents, source/index/branch changes, credential access or live writes. This report is the only file written.

**I1 — Fresh capture generations and fail-closed handoff replace reused output** — **ADDRESSED**.

`scripts/ui/capture-generation.mjs:7` creates a unique default directory or exclusively creates the requested directory; it does not clear caller files. `justfile:103` exports that generation to every screen and the final index check, skips the new helper as an executable screen, and prints completion only after the index succeeds. `scripts/bb/run.sh:118` refuses an existing local destination and chooses an absent unique remote generation. Its handoff at `:138` runs only after remote success and propagates transfer errors. `scripts/bb/shots-handoff.sh:8` refuses reuse, transfers into fresh sibling staging, promotes only after rsync succeeds, and detects a destination appearing during transfer without overwriting it.

Covering tests:

- `scripts/ui/capture-generation.test.mjs:45`: **old default captures cannot satisfy an omitted current screenshot (Plan 4 final I1)**.
- `scripts/ui/capture-generation.test.mjs:54`: **explicit capture destination refuses reuse without deleting caller files (Plan 4 final I1)**.
- `scripts/ui/capture-generation.test.mjs:61`: **successful generations publish exactly the completed fresh directory (Plan 4 final I1)**.
- `scripts/bb/shots-handoff.test.mjs:55`: **build-box refuses an old destination rather than merging captures (Plan 4 final I1)**.
- `scripts/bb/shots-handoff.test.mjs:65`: **build-box transfer failure propagates and offers no completed directory (Plan 4 final I1)**.
- `scripts/bb/shots-handoff.test.mjs:74`: **two build-box generations hand exactly fresh bytes to publication (Plan 4 final I1)**.
- `scripts/bb/shots-handoff.test.mjs:91`: **failed remote captures are not handed off as complete (Plan 4 final I1)**.

Checked the actual recipe/wrapper fixtures and assertions, not only their names. They preserve old bytes, exercise failed remote commands and failed transfers, and feed the exact completed directories into the real publisher dry-run. The retained BASE replay in `scratch/final-baseline.log` reports **0 passed / 7 failed**, including stale-output acceptance and swallowed transfer status. Final controller log records all seven GREEN. The report correctly distinguishes the positive two-fresh-destination bytes scenario, which BASE could already satisfy, from its missing completion marker; independent reuse/freshness tests pin the original defect. R10 is implemented without deleting arbitrary SHOTS_DIR data.

The exact new guide command at `docs/EVIDENCE.md:42` assigns a new local path, passes the quoted same value as SHOTS_DIR to the build box, and uses `&&` before dry-run publication of that value. The actual publish command at `:80` reuses that completed local path. README, Operator and Buildbox instructions consistently require fresh paths, successful capture/transfer, and publication of the completed local generation. Individual helpers/standalone index defaults remain explicitly documented separately from the recipe's generation default.

**I2 — Introduced commit history catches transient evidence while preserving grandfathered merges** — **ADDRESSED**.

`scripts/evidence/check.mjs:21` recognizes zero/empty-tree initial boundaries, otherwise requires a valid commit. At `:24` it enumerates commits reachable from HEAD but excluded by the base; `:25` inspects root/ordinary commits and combined merge changes. Introduced side commits are inspected even when their media disappears before the merge tip. Combined merge diffs avoid treating unchanged evidence inherited from one grandfathered parent as a new addition against another. Staged checking remains a separate index-only path at `:31`. `.github/workflows/evidence.yml:37` retains the event-base invocation and read-only checkout configuration.

Covering tests:

- `scripts/evidence/check.test.mjs:156`: **history rejects add/delete while staged mode stays local (Plan 4 final I2)**.
- `:168`: **history rejects modified then restored grandfathered media (Plan 4 final I2)**.
- `:180`: **history examines merged side commits and merge-created evidence (Plan 4 final I2)**.
- `:145`: **initial zero and empty-tree bases check all introduced history (Plan 4 final I2)**.
- `:199`: **merge preserves unchanged grandfathered media present on only one parent (Plan 4 final I2)**.

Checked real-Git fixture ancestry, commit trees and expected rejected paths. The merge fixture asserts both transient side-history evidence and a merge-created path, rather than accepting any generic failure. Existing rename history coverage now correctly remains blocked after a later move out; staged move-out remains allowed. Existing grandfathered/deletion-only, base-versus-index, unusual-name and workflow-base fixtures remain.

`scratch/final-baseline-i2.log` records **11 passed / 6 failed**, including intended add/delete, modify/restore, side-history and initial-history failures. Final controller log records the corresponding GREEN cases. Positive grandfathered merge compatibility was GREEN on BASE by design; merge-created tip evidence likewise is preservation coverage rather than a falsely claimed new RED. The commit-only boundary assertion and the existing endpoint-to-history expectation changes match R9 rather than relaxing the guard.

**I3 — Retained deterministic media/index/in-flight failure regressions pin the already-correct publisher** — **ADDRESSED**.

`scripts/evidence/publish.test.mjs:177`: **completed media followed by media failure never verifies or receipts (Plan 4 final I3)** ensures an actual completed upload before failure and no indexes, verification or receipt.

`:204`: **index failure after completed media never verifies or receipts (Plan 4 final I3)** covers both manifest and gallery failure after media completion, with the other in-flight index allowed to settle, sanitized failure and no receipt.

`:230`: **partial failure waits for its in-flight sibling and starts no queued uploads (Plan 4 final I3)** uses deferred promises to establish the pending sibling, observes the publication promise still unsettled after failure, then releases the sibling and checks final failure, no queued uploads, no verification and no receipt. The explicit timeout bounds a broken regression.

The existing corruption tests remain intact. There is no publisher production change: a read-only Git comparison confirms publisher/config identity through the live-tested `9b9fbd30`. R8 explicitly accepts honest first-run GREEN for this coverage-only finding. First-run **5/5** is implementer-reported; retained controller output independently supplied for this review records all three tests and both index subcases GREEN on the unchanged runtime. No RED is claimed or required for I3.

### New breakage in the fix diff

None. No new Critical, Important or Minor defect found in the scoped changes.

### Out-of-scope observations

None newly raised. The five original declined boundaries remain ledgered and were not reopened: provider administrative controls, future elapsed retention, public-gallery browser/offline-CSP/codec behaviour, same-user hostile ancestor mutation, and current alternate NAS/LFS/pricing state.

### Regressions

- Read `gate-final-controller.log`: the committed target's eight-file combined gate reports **94 passed / 0 failed / 0 skipped**, `bb: exit=0`, versus **78/78** at the original whole-branch review. The increase is seven capture/handoff tests, four history tests and five partial-failure tests/subcases. Existing recipe failure propagation, helper skipping, final extra/missing/duplicate index checks and caller-argument forwarding remain asserted. No original test was removed; changed expectations implement the approved fresh-output/history contracts.
- Read `gate-final-remote.log`: literal **`BB_JOBS=2 just bb bash scripts/bb/remote.test.sh`** reports **PASS: physical lane paths and framed argv/environment (19 assertions)** and **`bb: exit=0`**. This resolves the fix report's earlier limitation; the shell test and remote implementation remain unchanged.
- Checked the touched generation-helper/recipe/export seam, absolute/relative SHOTS_DIR handling, remote absent-generation setup, failure/status propagation, sibling staging and no-clobber promotion, and exact documented local-directory-to-publisher handoff. The transfer fixtures use the real wrapper with isolated SSH/rsync boundaries; their success paths use actual local rsync and publisher dry-run.
- Checked ordinary/root/merge history enumeration, exclusion of base ancestry, zero/empty-tree event handling, staged-versus-history isolation, and grandfathered/deletion compatibility. These changes preserve the existing read-only workflow privilege boundary.
- `git diff --check f8cedeac 50717f47` returned exit 0. Git comparisons confirmed no publisher/config change from the live canary and no remote.sh/remote.test.sh change from FIX_BASE. Worktree status was clean before this report.
- No suite, focused runtime probe, cloud upload or public GET was repeated here: the supplied target gates are complete, static inspection establishes the requested fixes, and no new concrete runtime doubt justified another run. Reported gate/RED results are supplied logs inspected here, not tests executed by this reviewer.

**None found** after checking those scoped regression seams and evidence. R8's coverage-only exception, R9's merge/history policy and R10's fresh-directory policy are implemented; no residual original Critical/Important finding needs a controller waiver.

### Box state

**Not deployed** for the changed capture/guard workflow: the brief states these changes are not yet on master. The build box exercised target `50717f47` in the supplied 94-test and literal 19-assertion gates; that is validation of the branch, not a claim of persistent production installation.

The public R2 deployment uses the publisher/config already live-tested at `9b9fbd30`; their branch identity was confirmed by read-only Git comparison. No service unit exists for this CLI, so active-since/restart checks do not apply. No installed-file hashes were read from xsm in this re-review, and no provider/credential inspection or fresh public probe was performed: the scoped fixes do not alter that transport/config, deployment of capture/guard changes remains pending, and all five prior boundaries remain accepted by the controller. No running-hub restart is required by this fix diff.

### Verdict

**Final fix pass:** All findings addressed, no new Critical/Important breakage.

=== DONE ===
