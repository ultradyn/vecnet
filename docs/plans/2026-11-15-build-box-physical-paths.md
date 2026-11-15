# GitHub #51 physical build-box paths

Goal: make build-box path comparisons reliable when bb-src is a symlink to another volume.
Spec: GitHub #51 Test hygiene item; frozen issue-51-refresh.json. Selected base masterda6122ba7d; exact SHA recorded in ledger.
Architecture: canonicalize the existing remote BASE before deriving lane, toolchain, HOME, TMPDIR, XDG and cache paths. Do not canonicalize unrelated user paths or change synchronization locations.
Tech: existing Bash remote wrapper; synthetic shell regression.

## Global constraints

- One isolated branch/worktree, one Sol implementation agent with PIRFL. User authorized selection/implementation/review/merge/cleanup. No Markdown backlog ticket selected.
- All test/build/syntax gates through serialized x-boss lane, BB_JOBS=2, GOMAXPROCS=2, private physical TMPDIR, umask0022. No service activation, real messages, runtime/config/profile/credential access, or deployments.
- Preserve argv/env NUL/base64 framing, lane validation, clean environment, owner/sync metadata, limits, watchdog and cleanup. No dependencies or public interface changes.
- Exact model gpt-6.1-sol/high; workers cannot spawn subagents or attn. Controller owns fresh independent review and scoped re-review of accepted fixes. Fix all accepted findings including minors.
- Leave #51 open: only its named build-box item is delivered.

## Review Focus

RF1: symlinked bb-src yields exact physical HOME, TMPDIR, XDG paths, Go/npm caches, GOPATH and other derived runtime paths; source wrapper called by a real synthetic command, not copied logic.
RF2: NUL/base64 argv preserves one argument containing whitespace, and BB_JOBS=2 still yields GOFLAGS=-p=2; test boundary alongside path behavior.
RF3: resolving a missing existing base exits with status64 before executing user command; preserve ordinary physical-base execution. Source inspection is enough unless a concrete concern warrants controls.

## Task1 — normalize remote lane base

Files: Modify scripts/bb/remote.sh, docs/BUILDBOX.md, CHANGELOG.md; Create/Test scripts/bb/remote.test.sh. Controller owns all plan/receipt/operational metadata. No other tracked file changes.
Consumes: remote.sh lane + base64 NUL-separated environment + argv; run.sh current lane creation/sync.
Produces: unchanged interface, existing BASE resolved with cd/pwd-P; tiny synthetic regression.

- [ ] Read complete current remote wrapper and relevant run.sh caller. Historical f5977804 is reference only, not a merged fix or permission to cherry-pick unrelated commits.
- [ ] Write synthetic regression in remote.test.sh with owned mktemp fixture and cleanup trap; fixture home/bb-src symlinks to physical volume. Invoke the current wrapper with child-only HOME, framing preserved. Require exact physical private/cache paths and literal framed argument. Use task-specific shell variable names; never repurpose operator HOME/system variables in agent shell.
- [ ] Gate RED against unchanged production wrapper. Expected behavioral failure identifies logical HOME or TMPDIR, not compilation, fixture or toolchain error. Report raw log and exit.
- [ ] Resolve existing BASE physically before deriving RL/TC, failing with existing64 convention if unavailable. Keep runtime behavior otherwise identical. Tiny doc/changelog statement.
- [ ] Gate GREEN synthetic test and bash-n all changed shell scripts. Full Go/Node replay is disproportionate for this Bash-only normalization; add checks only for concrete concern. Record source hashes.
- [ ] Self-review, git diff-check, commit explicit owned paths conventionally on feature branch, report LAST. Do not amend/rebase/push/merge. Report concerns/deviations honestly, end === DONE ===.

## Review, integration and exit

Fresh independent Sol whole-branch reviewer checks spec/code quality and complete small diff; this one review serves sole-task and final gates because there is no later task/integration delta. Controller separately inspects source and reads raw gate evidence. Resolve every accepted finding and same-reviewer scoped re-review; no silent minor deferrals. After PASS close plan/receipt metadata, verify executable source equivalence, preserve concurrent master, normal merge/push, accurate #51 progress comment. Verified external copy/hash manifest must precede worktree/branch removal.
Try it synthetically: BB_HOST=x-boss BB_JOBS=2 BB_PULLBACK=0 just bb bash scripts/bb/remote.test.sh. Source delivery does not imply an installed release.

## Self-review

One cohesive task, four owned paths, no shared task interfaces. T1 self: RED names real wrapper; GREEN pins physical paths and framing; failure64 remains source contract; Clean. Stage0: no UNVERIFIED runtime/API needed. Current source lacks pwd-P and historicalf597 IS ancestor through reconciliation merges but its change is absent from current tree; refresh before selection. No operator rollout or private-state step required. All RFs tied to test/source seam above; no general runner rewrite.
