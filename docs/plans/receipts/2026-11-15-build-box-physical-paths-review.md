# Independent whole-branch review — GitHub #51 physical build-box paths

Spec compliance: **PASS**

Code quality: **PASS**

Findings: none (Critical: 0; Important: 0; Minor: 0). No fixes or controller-gated focused probes requested.

## Reviewed identity and scope

- BASE: `da6122bae8210062b8ac15c77346eff639db3dc3`
- HEAD: `e6e9c865d99d4f619c1694b4664c2787cc6e3fa1`
- TREE: `7eedb3b94cb3441258a149c689f74842fdcbd939`
- Read task-1-brief.md, frozen issue-51.json, plan, progress.md R1–R5, task-1-report.md including its correction appendix, and the full review.diff. The supplied review.diff is byte-identical to the pinned `git diff --unified=10 BASE HEAD` (the default three-line-context rendering differs only in rendering).
- Reviewed all five changed files, the complete remote.sh and remote.test.sh, and immediate run.sh caller/sync/owner/pullback seams. No unrelated code crawl, subagents, tests/builds, remote actions, source edits, index/HEAD/branch changes, or other writes were performed. This report is the sole write.
- Historical `f5977804` is an ancestor of BASE (ancestry command exit 0), and its remote.sh contains physical normalization. BASE's actual remote.sh lacks it. Thus ancestry alone did not establish delivery; the pinned patch restores the required source effect.

## Spec compliance and review focus

- **RF1 — PASS.** `scripts/bb/remote.sh:7` constructs the existing base; `scripts/bb/remote.sh:9` resolves it using a subshell `cd` and `pwd -P`, before RL/TC at lines 11–12. Lane HOME, TMPDIR, TMUX_TMPDIR, REAP_ROOTS, XDG, PATH, Go caches/GOPATH, npm cache and Playwright paths all derive from those physical values at `scripts/bb/remote.sh:25` through line 31. The fixture independently establishes a physical root and a space-containing owned target volume, then symlinks fixture home/bb-src at `scripts/bb/remote.test.sh:6` through line 14. Exact expected strings are checked at `scripts/bb/remote.test.sh:29` through line 43. Line 53 invokes the actual source wrapper with a real `bash -c` probe; no replacement normalization implementation is tested.
- **RF2 — PASS.** `scripts/bb/remote.test.sh:50` and line 51 frame environment and argv with NUL/base64, including the probe, physical paths containing spaces, and one literal whitespace argument. Assertions at lines 44–47 require BB_JOBS=2, GOFLAGS=-p=2, exactly three probe arguments, and the intact third argument. The actual decoder at `scripts/bb/remote.sh:16` through line 18 and job handling at lines 34–43 remain unchanged. Caller framing at `scripts/bb/run.sh:124` through line 131 is compatible and unchanged. This boundary is covered in the same 19-assertion path regression.
- **RF3 — PASS by source inspection, as expressly permitted.** At `scripts/bb/remote.sh:9`, failed `cd` prevents `pwd -P`; either command's failure makes the assignment fail and immediately exits 64. This precedes lane derivation, environment decoding, directory writes, and user-command launch at line 62. For an existing ordinary physical absolute base, `pwd -P` returns that base and preserves execution. The subshell does not change the wrapper's current directory; its later lane-source `cd` at line 56 is retained. No unresolved concrete doubt calls for additional controls; missing-base and ordinary-base controls were not executed by this reviewer or claimed as runtime evidence.

## Code quality and preservation

The production delta is one explanatory comment and one guarded assignment. It uses the existing Bash style and exit-64 convention, with quoting appropriate for spaces. The existing lane validation (`scripts/bb/remote.sh:13`), clean environment and argv array execution (lines 20–62), process-group/timeout/nice limits (line 62), watchdog and child reaping (lines 65–83), and changed-file collection/exit propagation (lines 85–90) are preserved.

`scripts/bb/run.sh:24` through line 44 retains environment selection and secret-name rejection; SSH options at line 48 retain authentication/forwarding behavior. Owner checks and source/wrapper sync at lines 83–112 still address the same directories through the original logical alias. Physical normalization changes path spelling inside the remote wrapper, while owner files, .runstamp and changed.list remain in the same target directories. Caller serialization (lines 77–80), watchdog connection handling (lines 127–132), and pullback paths (lines 137–164) remain compatible and unchanged. No new public interface, dependency, authorization change, or limit change appears.

Fixture cleanup at `scripts/bb/remote.test.sh:7` removes only its mktemp-owned root, including its owned target volume; it does not recursively follow the fixture symlink into operator paths. Canonicalization at line 8 retains that ownership. HOME is overridden only on the child `env ... bash` invocation at line 53; task-specific variables do not mutate the operator shell's HOME. `docs/BUILDBOX.md:50` and `CHANGELOG.md:2` accurately describe the bounded physical-base behavior. Plan/receipt closing metadata remains controller work after this PASS, as requested.

## Raw gate evidence and byte equivalence

Read the original baseline, RED, GREEN, syntax and controller-final `.log`, `.exit`, `.head`, `.dirty`, and source-before/source-after JSON artifacts directly. Gate code records local source hashes before and after, invokes the current run.sh through the serialized build-box route, and retains the command output and exit. The unchanged caller syncs these source/wrapper bytes before executing the remote command. Per-gate HEAD alone was not used as evidence of source equivalence.

| Gate | Raw result and limits |
|---|---|
| baseline-syntax | Build-box exit 0; gate exit 0. Its multi-file `bash -n` command establishes remote.sh syntax only, not run.sh syntax. |
| task1-red-physical-paths-01 | `FAIL: HOME`: expected the owned physical-volume lane home, received fixture home/bb-src alias. Zero assertions passed; build-box exit 1, just/gate exit 1. This is the intended behavioral RED, not a fixture/toolchain failure. |
| task1-green-physical-paths-01 | `PASS: physical lane paths and framed argv/environment (19 assertions)`; build-box exit 0, gate exit 0. |
| task1-syntax-physical-paths-01 | Build-box exit 0, gate exit 0. Its original multi-file `bash -n` proves remote.sh only. The report appendix expressly withdraws the earlier three-script claim; that withdrawn claim is not relied upon. |
| controller-final | `logs/controller-final.log:9` explicitly loops over all three files and runs `bash -n` separately; lines 10–12 print each syntax PASS, line 13 records all 19 regression assertions, line 14 records build-box exit 0. The separate gate `.exit` is 0. |

All before/after pairs are identical. RED's wrapper hash is `60d88516935664b4cb1f9485a6f281adcd9dda6f179d0559d721d9e7d9397ee5`, independently matching the pinned BASE wrapper bytes. RED's fixture is already byte-identical to final, and run.sh is unchanged from BASE. Thus RED and GREEN exercise the same fixture with the intended production delta.

Independently SHA-256-hashed the final committed blobs and working files. Both match the GREEN, original syntax and controller-final before/after manifests for all three executable-source files:

| Path | Final blob | SHA-256 |
|---|---|---|
| scripts/bb/remote.sh | bae3faa8c27125d95e1e1aa412f01a741a8834e0 | 16911a0aeafb8ed9e8027c4cce76c0233fcc1d232c60a66d3d2b76b8f47bfea6 |
| scripts/bb/remote.test.sh | 145a4a429c1c17a0853acd78291110af5933fe75 | 8f310e92c5b5eb762880af905b2b1127ba33973736f74a88ac58bd7147bf878f |
| scripts/bb/run.sh | 59376e1a6ca2105babc6455c11cce85d30f982c5 | 1c95265037295c3ed79e377f5ead3539e01d819e755f3b2a2f5f00253564406e |

Controller-final HEAD matches the pinned HEAD and its dirty snapshot is empty. Current HEAD/tree also match the pins; tracked status is clean and pinned `git diff --check` exits 0. These were read-only identity/hash/diff checks, not local test execution. Existing focused controller evidence suffices for this private Bash normalization; no broad suite or additional focused probe is justified by a concrete concern.

=== DONE ===
