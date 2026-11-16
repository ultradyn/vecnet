# Final fix report

Status: FIXED. I1 and I2 corrected; I3 retained as regression coverage for an already-correct implementation. No unresolved source finding. Controller build-box gate at committed `50717f47` passed **94 tests, 0 failures, 0 skips**. Local combined gate also passed **94/94**, and the existing remote framing/physical-path harness passed **19 assertions** locally with the cleanup adaptation disclosed below. The literal build-box invocation of that shell harness was not run by this implementer.

Worktree: `/home/xertrov/src/vecnet/.worktrees/evidence-r2`; branch: `feat/public-evidence`; review BASE: `f8cedeac6f096cd8cc5a65645cde3c9d8692fe33`. Read the complete final review, ledger R8–R10 and all five final-declined rulings before editing. Critical: none. No folded minors or carried concerns were supplied. No subagents, notifications, credentials, operator data or shared-host operations were used by this implementer. Temporary fixtures, browser downloads, logs and verification scripts stayed inside the specified task scratch directory. Tracked source status was clean after the three commits and before this final ignored report.

## Commits

| Finding | Commit | Message |
|---|---|---|
| I2 | `31cde6fed08c5a28bcd98b9a63be6afb79465720` | `fix(evidence): inspect introduced commit history (final I2)` |
| I3 | `01ec933786f451ca7b2ea9ad145dfefad31bd716` | `test(evidence): retain partial upload failure regressions (final I3)` |
| I1 | `50717f47a8b6daf0b3b59af9f5e74a700dfb40eb` | `fix(evidence): capture and hand off fresh generations (final I1)` |

## I1 — fresh capture generations and fail-closed handoff

`just ui-shots` exclusively creates its explicit SHOTS_DIR, or creates a unique ignored default directory. Existing caller paths are refused without deletion. All screens retain failure propagation and the final real index check still runs last. Only successful completion prints the exact capture directory. Individual screen helpers retain their existing SHOTS_DIR/default interface.

The build-box wrapper uses a unique absent remote generation, refuses existing local destinations, transfers only a successful remote result into fresh local staging, and promotes it only after rsync succeeds. A failed transfer propagates its status and leaves the completed destination absent. A failed capture is never handed off. Documentation captures into a new path and publishes exactly that completed local directory; the generic publisher remains unchanged.

Tests were written and run before production edits. Final fixture versions were also replayed against copies of the original BASE recipe/wrapper without modifying the worktree. The corrected baseline replay produced **0 passed / 7 failed**, with intended assertion failures rather than missing imports. Retained RED log: `scratch/final-baseline.log`. GREEN lines below are from the final local combined run, `scratch/final-combined-green.log`; the controller gate independently passed these same seven tests. All rows belong to commit `50717f47a8b6daf0b3b59af9f5e74a700dfb40eb`.

| Test name | Witnessed RED output line | Witnessed GREEN output line |
|---|---|---|
| old default captures cannot satisfy an omitted current screenshot (Plan 4 final I1) | `not ok 5 - old default captures cannot satisfy an omitted current screenshot (Plan 4 final I1)`; `old expected.png must not make an empty generation pass` | `ok 44 - old default captures cannot satisfy an omitted current screenshot (Plan 4 final I1)` |
| explicit capture destination refuses reuse without deleting caller files (Plan 4 final I1) | `not ok 6 - explicit capture destination refuses reuse without deleting caller files (Plan 4 final I1)`; `existing output must not be reused` | `ok 45 - explicit capture destination refuses reuse without deleting caller files (Plan 4 final I1)` |
| successful generations publish exactly the completed fresh directory (Plan 4 final I1) | `not ok 7 - successful generations publish exactly the completed fresh directory (Plan 4 final I1)`; `recipe offers its exact completed directory after the index gate` | `ok 46 - successful generations publish exactly the completed fresh directory (Plan 4 final I1)` |
| build-box refuses an old destination rather than merging captures (Plan 4 final I1) | `not ok 1 - build-box refuses an old destination rather than merging captures (Plan 4 final I1)`; `existing destination must not merge old media into a completed capture` | `ok 1 - build-box refuses an old destination rather than merging captures (Plan 4 final I1)` |
| build-box transfer failure propagates and offers no completed directory (Plan 4 final I1) | `not ok 2 - build-box transfer failure propagates and offers no completed directory (Plan 4 final I1)`; `0 !== 23`, with baseline `bb: shots pull failed` followed by `bb: exit=0` | `ok 2 - build-box transfer failure propagates and offers no completed directory (Plan 4 final I1)` |
| two build-box generations hand exactly fresh bytes to publication (Plan 4 final I1) | `not ok 3 - two build-box generations hand exactly fresh bytes to publication (Plan 4 final I1)`; missing completed `captures: DIRECTORY` handoff line | `ok 3 - two build-box generations hand exactly fresh bytes to publication (Plan 4 final I1)` |
| failed remote captures are not handed off as complete (Plan 4 final I1) | `not ok 4 - failed remote captures are not handed off as complete (Plan 4 final I1)`; `Missing expected rejection.` for destination absence | `ok 4 - failed remote captures are not handed off as complete (Plan 4 final I1)` |

The tests execute the actual shebang recipe and the actual run.sh with isolated SSH/rsync boundary fixtures, then use the real publisher's dry-run API on completed directories. They preserve old generated captures and historical fixture media. The two-generation test checks exact directory contents and published manifest entries for both runs. On an accurately simulated original wrapper, two distinct fresh destination paths already contain only their respective generations; its RED is the missing explicit completed-path interface. The reused-destination test independently reproduces the unsafe local merge. Existing recipe regressions still exercise screen exit 23, extra/missing/duplicate PNGs, final index placement and helper skipping.

## I2 — inspect introduced history, preserve staged mode

Staged checking still compares only the index. `--base REF` now enumerates commits reachable from HEAD but not from the event base and checks their additions/modifications. Root commits are included. Combined merge diffs inspect changes differing from every parent; all newly introduced parent commits are also checked. This detects intermediate evidence while allowing a merge to inherit unchanged grandfathered media present on only one parent. Zero SHA and the workflow's literal empty-tree sentinel inspect all reachable history. Other bases must resolve to commits; invalid or missing history fails closed. No history rewrite or historical-media deletion was used.

Before implementation, add/delete, modify/restore, transient merged history and initial-push regressions failed with `0 !== 1`. Final tests were replayed against the original check.mjs for retained evidence: **11 passed / 6 failed**, log `scratch/final-baseline-i2.log`. GREEN below comes from `scratch/final-combined-green.log`; focused current guard suite passed **17/17**. All rows belong to commit `31cde6fed08c5a28bcd98b9a63be6afb79465720`.

| Test name | Witnessed RED output line | Witnessed GREEN output line |
|---|---|---|
| history rejects add/delete while staged mode stays local (Plan 4 final I2) | `not ok 12 - history rejects add/delete while staged mode stays local (Plan 4 final I2)`; `0 !== 1` | `ok 16 - history rejects add/delete while staged mode stays local (Plan 4 final I2)` |
| history rejects modified then restored grandfathered media (Plan 4 final I2) | `not ok 13 - history rejects modified then restored grandfathered media (Plan 4 final I2)`; `0 !== 1` | `ok 17 - history rejects modified then restored grandfathered media (Plan 4 final I2)` |
| history examines merged side commits and merge-created evidence (Plan 4 final I2) | `not ok 14 - history examines merged side commits and merge-created evidence (Plan 4 final I2)`; `0 !== 1` on transient merged side history | `ok 18 - history examines merged side commits and merge-created evidence (Plan 4 final I2)` |
| initial zero and empty-tree bases check all introduced history (Plan 4 final I2) | `not ok 11 - initial zero and empty-tree bases check all introduced history (Plan 4 final I2)`; `0 !== 1` | `ok 15 - initial zero and empty-tree bases check all introduced history (Plan 4 final I2)` |
| merge preserves unchanged grandfathered media present on only one parent (Plan 4 final I2) | No RED: positive compatibility regression was GREEN on the original implementation | `ok 19 - merge preserves unchanged grandfathered media present on only one parent (Plan 4 final I2)` |
| renames are checked at their destination, including changed and unusual names | `not ok 5 - renames are checked at their destination, including changed and unusual names`; `0 !== 1` when evidence was moved back out after an intermediate commit | `ok 9 - renames are checked at their destination, including changed and unusual names` |
| invalid arguments and missing base history fail closed | `not ok 16 - invalid arguments and missing base history fail closed`; `nonempty trees cannot delimit commit history` | `ok 20 - invalid arguments and missing base history fail closed` |

Real Git fixtures cover unchanged grandfathered files, deletion-only commits, staged versus unstaged contents, merge ancestry, newly created evidence in a merge, every evidence root/extension, unusual filenames and whitespace-suffixed repository roots. Workflow tests execute the actual event-base selection script for pull requests, ordinary pushes and initial pushes. The nonempty-tree assertion was added after the main implementation and verified RED on the original snapshot; the main I2 reproductions had already failed before production edits.

## I3 — retained partial-upload regressions, existing runtime correct

Commit: `01ec933786f451ca7b2ea9ad145dfefad31bd716`. **No RED was manufactured.** All three new tests, including both index subcases, were GREEN on their first run against the unchanged publisher. First focused run: **5 passed / 0 failed / 0 skipped**. Full publisher suite: **43 passed / 0 failed / 0 skipped**. This follows R8 exactly.

| Test name | RED output | First-run GREEN output line | Final combined GREEN output line |
|---|---|---|---|
| completed media followed by media failure never verifies or receipts (Plan 4 final I3) | None; already-correct runtime | `ok 1 - completed media followed by media failure never verifies or receipts (Plan 4 final I3)` | `ok 26 - completed media followed by media failure never verifies or receipts (Plan 4 final I3)` |
| index failure after completed media never verifies or receipts (Plan 4 final I3) | None; already-correct runtime | `ok 2 - index failure after completed media never verifies or receipts (Plan 4 final I3)`; both `manifest.json` and `index.html` subtests passed | `ok 27 - index failure after completed media never verifies or receipts (Plan 4 final I3)` |
| partial failure waits for its in-flight sibling and starts no queued uploads (Plan 4 final I3) | None; already-correct runtime | `ok 3 - partial failure waits for its in-flight sibling and starts no queued uploads (Plan 4 final I3)` | `ok 28 - partial failure waits for its in-flight sibling and starts no queued uploads (Plan 4 final I3)` |

Substitute evidence is behavioral: the tests establish completed media before failure, use deferred promises to hold a sibling in flight, verify the publishing promise remains unsettled until release, and check settlement afterward. They assert no receipt, no public verification, no inappropriate later media/index uploads and sanitized errors. Both index failures occur after all media succeeded. Existing public corruption cases remain intact. `publish.mjs` and config are byte-identical to review BASE in my git diff; the controller separately confirmed identity with live-tested `9b9fbd30`.

## Found on the way and fixed

No additional product-code finding was introduced or fixed outside I1/I2.

1. **Local browser prerequisite:** the initial combined run passed 92/94; the two existing browser tests failed because Chromium headless shell 1228 was absent. RED lines: `not ok 58 - message scroll reaches the viewport while live updates replace message nodes` and `not ok 59 - quiet waits for a slow snapshot and the request its render starts, not for streams or held requests`, with `Executable doesn't exist ... chromium_headless_shell-1228`. Downloaded the installed Playwright version's headless shell and FFmpeg into task scratch and set PLAYWRIGHT_BROWSERS_PATH for the rerun. GREEN: `ok 58 - message scroll reaches the viewport while live updates replace message nodes` and `ok 59 - quiet waits for a slow snapshot and the request its render starts, not for streams or held requests`; combined **94/94**. No repository commit or system installation was needed. Logs: `scratch/final-combined.log` and `scratch/final-combined-green.log`.
2. **Fixture corrections before accepting RED evidence:** the initial success-capture fixture attempted to use empty SHOTS_DIR, producing `mkdir: cannot create directory '': No such file or directory`; corrected the fixture to mirror the original helpers' default directory. Re-running the original recipe then gave the intended RED, `recipe offers its exact completed directory after the index gate`, and final GREEN is the I1 success-generation row above. An early two-generation stub also omitted the original wrapper's remote cleanup; its stale-remote-file failure was discarded. The corrected stub models that cleanup only inside its owned fixture, and the retained original-wrapper replay gives the honest completed-interface RED described under I1. These fixture corrections are included in `50717f47`; no production RED claim relies on the discarded fixture failures.
3. **Existing recipe fixtures:** updated them to copy the new generation helper, leave explicit output paths absent, and write late extra PNGs into the generation's exported SHOTS_DIR. Preserved their established assertions, including screen failure and final index checks. All four recipe tests and all four shot-path tests pass. Their scratch placement and publisher/guard scratch placement implement the operator's fixture-isolation instruction; no real store was used.

## Gate table

All Node test commands used `--test-concurrency=2`; isolated upload tests retain a two-upload maximum. Commands were run sequentially, without additional workers or agents. Local test processes used TMPDIR under the specified scratch directory and UV_THREADPOOL_SIZE=2.

| Command | Result counts |
|---|---|
| `node --test --test-concurrency=2 scripts/evidence/check.test.mjs` | Implementer, current code: **17 passed, 0 failed, 0 skipped** |
| `node --test --test-concurrency=2 --test-name-pattern='Plan 4 final I3' scripts/evidence/publish.test.mjs` | Implementer, first run: **5 passed, 0 failed, 0 skipped**; no publisher production edits |
| `node --test --test-concurrency=2 scripts/evidence/publish.test.mjs` | Implementer: **43 passed, 0 failed, 0 skipped** |
| `node --test --test-concurrency=2 scripts/ui/shot-paths.test.mjs scripts/ui/recipes.test.mjs scripts/ui/capture-generation.test.mjs scripts/bb/shots-handoff.test.mjs` | Implementer: **15 passed, 0 failed, 0 skipped**, including all seven new capture/handoff regressions |
| `node --test --test-concurrency=2 scripts/evidence/publish.test.mjs scripts/evidence/check.test.mjs scripts/ui/shot-paths.test.mjs scripts/ui/recipes.test.mjs scripts/ui/lib.test.mjs scripts/ui/closer.test.mjs scripts/ui/capture-generation.test.mjs scripts/bb/shots-handoff.test.mjs` | Implementer, final local run: **94 passed, 0 failed, 0 skipped**; `scratch/final-combined-green.log` |
| `BB_JOBS=2 just bb node --test --test-concurrency=2 scripts/evidence/publish.test.mjs scripts/evidence/check.test.mjs scripts/ui/shot-paths.test.mjs scripts/ui/recipes.test.mjs scripts/ui/lib.test.mjs scripts/ui/closer.test.mjs scripts/ui/capture-generation.test.mjs scripts/bb/shots-handoff.test.mjs` | **Controller-run**, committed `50717f47`: **94 passed, 0 failed, 0 skipped**, `bb: exit=0`; read `gate-final-controller.log`. This includes the required six-file live gate plus both new focused files. |
| `python3 /home/xertrov/src/vecnet/.worktrees/evidence-r2/.superpowers/sdd/2026-11-16-public-evidence/scratch/verify-final-fixes.py remote` | Implementer, isolated local copy of existing remote.test.sh: **PASS, 19 assertions**, real remote.sh; `scratch/final-remote.log` |
| `BB_JOBS=2 just bb bash scripts/bb/remote.test.sh` | Literal shared-host invocation **not run by implementer**, per shared-machine rule. All established 19 assertions passed in the local adaptation; no build-box result claimed for this command. Controller's final instruction requested the existing result and no further suite reruns. |
| `just test` / additional product and Go suites | Not run: controller explicitly stated **“No additional product/Go tests needed.”** |
| `bash -n scripts/bb/run.sh scripts/bb/shots-handoff.sh` | Exit **0** |
| `git diff --check f8cedeac6f096cd8cc5a65645cde3c9d8692fe33 HEAD` | Exit **0** |
| `git diff --exit-code f8cedeac6f096cd8cc5a65645cde3c9d8692fe33 HEAD -- scripts/evidence/publish.mjs scripts/evidence/config.json scripts/bb/remote.test.sh scripts/bb/remote.sh` | Exit **0**; publisher/config and established remote implementation/assertions unchanged |

The local remote.test.sh adaptation preserves all 19 assertions and invokes the real remote.sh. Its scratch copy changes only the wrapper locator to that real file and disables the original `rm -rf` cleanup trap; the Python driver removes its own scratch fixture afterward. The tracked shell test is unchanged. This avoids executing the prohibited cleanup command while retaining framing, literal whitespace argument, physical symlinked-home path and environment assertions.

## Disagreements and concerns

- No disagreement with accepted I1/I2 or R8–R10. All requested source fixes are committed. The literal build-box remote.test.sh gate remains an explicit verification limitation; its 19 assertions passed locally. The controller-run eight-file live gate is supplied evidence, independently read from its log, and is not attributed to this implementer.
- I3 was never RED and needed no production fix. Its deterministic first-run GREEN tests are the required substitute evidence. The grandfathered merge allowance was also GREEN on BASE by design. Existing positive staged/deletion/grandfather compatibility checks remained GREEN; they establish preservation rather than a new failure.
- In the merge-history regression, the original implementation fails first on the transient side-branch evidence. The later merge-created-evidence assertion is GREEN coverage, not a separately witnessed old-runtime failure; endpoint checks already reject visible tip additions. Likewise, two fresh original build-box destinations already had correct bytes after accurately modeling remote cleanup; its new regression's original RED concerns the explicit completed-path handoff, while stale local merging is reproduced by the reused-destination regression.
- Failed capture directories and failed `.shots-transfer.*` staging remain for inspection and must not be published. Remote generations remain with the lane until cleanup. No arbitrary caller directory is cleared or overwritten. Publishing a caller-selected directory still records caller-supplied provenance and does not prove clean-checkout generation.
- Preserve all five final-declined boundaries: provider controls rely on controller readback; future expiry/indefinite retention is unobservable now; public-gallery browser/offline-CSP/codec rendering remains unverified; hostile same-user ancestor replacement remains outside the established trust boundary; alternate NAS/LFS/pricing state remains historical/out of scope. Local browser harness tests do not establish public-gallery rendering. No fresh cloud upload was needed because transport/config stayed unchanged.

This report was written last, after all source commits and verification. No commits follow it.

=== DONE ===
