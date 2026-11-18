# Windows concurrent Ensure — bounded PIRFL report

## Status

Candidate fix committed for parent review/native gate; **native GREEN is not claimed**. Base: `101f072702819a4066ed5b037c42191ef1c79d37`. Ownership remained entirely `internal/localruntime`; no sibling fixtures, Node CI, or Go scheduling files changed. No subagents, GitHub, pushes, merges, rebases, or local builds/tests.

## Causal diagnosis

Supplied native R7 run44254434973 / job134633298739 reports `windows_runtime_test.go:279: open ...pending.json: The process cannot access the file because it is being used by another process.` The existing regression launches four Ensure goroutines and asserts exactly one task start; it is unchanged.

`ensureWindows` called `inspectWindowsMetadata` and `previewWindowsRelease` before `install.lock`. The former opens pending metadata through `readPrivate` → `platform.OpenFile` → `openAt`. That native open shares READ|WRITE, deliberately **not DELETE**. Concurrent activation writes manifests via `atomicWrite` → `platform.Replace` (`MoveFileEx`, replace-existing/write-through), then removes pending.json after success. Delete access held during replacement/deletion conflicts with a new no-share-delete read; conversely an existing reader can deny replacement. Thus the lock serializes writers but does not exclude another goroutine's preflight reader. The observed error is an **open** of pending.json, not the lock path or duplicate-start assertion. This narrows the mechanism to the open/delete-access overlap; the log alone cannot identify replacement versus final deletion.

Added native `TestWindowsPendingReadConflictsWithCommitHandle`: retain DELETE access on the real pending file with all sharing enabled, then run the real optionalManifest reader and require ERROR_SHARING_VIOLATION. This precisely characterizes the necessary sharing conflict rather than guessing that two READ handles conflict. **Compiled, not executed here.** Native confirmation of this causal characterization remains part of the parent gate.

## Minimal fix and invariants

A cancellable, process-local operation guard now covers Windows preparation/preflight through activation and pending cleanup. This prevents the reported four goroutines from inspecting pending metadata while another is mutating it. No filesystem writes are introduced before trust checks; preview still creates no files. No sharing flags, ownership/SID/DACL/reparse/hardlink checks, unknown-release checks, pending intent ordering, cancellation checks, task CAS, executable pins, or activation verification were removed. The filesystem install lock remains unchanged for cross-process writers. Existing 60-second limit now starts before guard waiting (not extended). No sleeps/retries/timeouts were added to hide the race.

Tradeoffs/limits: the guard conservatively serializes different HOME values within one process. It does **not** solve preflight contention between independent processes, nor change the deliberately lock-free Read API. No claim beyond the requested concurrent-goroutine failure is made.

## RED / GREEN evidence

All commands ran through `evidence/run-bb.sh` (or its equivalent inline RED invocation), with `BB_HOST=x-boss BB_JOBS=2 BB_LANE=ci80-concurrent BB_PULLBACK=0 GOMAXPROCS=2`, Go `-p 2`, private physical `/tmp` mode0700 plus private HOME/XDG, umask022, and ACP_TEST_ROOT unset.

- Supplied native RED: retained full raw source at `internal/localruntime/evidence/native-r7-windows.log` (ignored local evidence; original also remains at the user-supplied ci80 worktree path).
- `guard-red.log`: unguarded baseline fails `contending preflight entered: <nil>`.
- `guard-mutation-red.log`: replacing acquisition with a no-op fails `contending preflight entered before cancellation: <nil>`; real implementation restored before GREEN.
- `final-green.log`: `CGO_ENABLED=0 go test -p 2 -count=1 ./internal/localruntime` PASS (0.433s).
- Same final invocation: `CGO_ENABLED=1 CC=/usr/bin/gcc go test -race -p 2 -count=1 -run '^TestWindowsOperationGuard' ./internal/localruntime` PASS (1.008s).
- Same final invocation: `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -p 2 -c -o "$TMPDIR/localruntime.test.exe" ./internal/localruntime` PASS; BB exit0.
- `git diff --check` passed. Self-review checked security/order/cancellation and scope; no independent reviewer per explicit no-subagent constraint.

The deterministic cross-platform guard regression tests exclusion, cancellation while waiting, canceled-waiter ownership, reacquisition, and rejection of canceled callers when idle. It validates the real synchronization primitive, **not Windows integration**. Parent must run affected native localruntime tests, particularly unchanged `TestWindowsConcurrentEnsureSerializesActivation` and new `TestWindowsPendingReadConflictsWithCommitHandle`, before concluding R7 is resolved. No native execution was possible on the authorized Linux BB, and no alternate platform/service/network probe was attempted.
