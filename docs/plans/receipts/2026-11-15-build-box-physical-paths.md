# GitHub #51 physical build-box paths — source acceptance

Reviewed source `e6e9c865d99d4f619c1694b4664c2787cc6e3fa1` on branch
`fix/issue-51-physical-build-paths`, base master `da6122ba`.

The remote wrapper resolves the existing build-box BASE physically before
deriving lane/toolchain/cache/runtime paths. The caller still synchronizes to
the same target directories through its alias; framing, clean environment,
lane validation, owner metadata, limits, watchdog and cleanup remain unchanged.
Missing-base failure64 is preserved by source inspection. No dependency,
release installation, runtime activation or Fly change is included.

Sol implementation subagent used PIRFL. The same synthetic real-wrapper test
failed on unchanged source with logical HOME, then passed all19 physical-path,
argv and job-cap assertions. Controller-final on the exact committed source
parsed each of remote.sh/remote.test.sh/run.sh separately and passed all19
assertions on serialized x-boss, two jobs, private physicalTMPDIR, umask0022.
Wrapper/buildbox exits0. Before/after SHA256 manifests match final Git blobs.
Original multi-file bash-n claims are withdrawn: those commands parsed only
the first script. Their raw logs remain retained; corrected controller-final
provides actual three-script syntax evidence. No broad Go/Node replay was
needed for this private Bash normalization.

Fresh independent whole-branch review covers the sole task and final source
under ledgerR4: Spec compliance PASS, Code quality PASS, zero findings.
[Review](2026-11-15-build-box-physical-paths-review.md) and
[ledger](2026-11-15-build-box-physical-paths-ledger.md). No accepted finding is
deferred. Closing plan/receipt metadata preserves tested executable bytes.

Source freshness correction: historicalf5977804 is in ancestry through
reconciliation merges, but its patch was absent from current baseline source.
Current source plus genuine RED establish need; ancestry alone did not.
GitHub#51 remains open for all other performance, Settings and rollout items.

Try it synthetically: `BB_HOST=x-boss BB_JOBS=2 BB_PULLBACK=0 just bb bash scripts/bb/remote.test.sh`.
Raw operational evidence must be copied and hash-verified outside the worktree
before deleting only this worktree and merged branch.
