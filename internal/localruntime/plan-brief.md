# Concurrent Windows Ensure: bounded PIRFL plan

Goal: diagnose native R7 run44254434973 pending.json sharing failure and minimally restore four concurrent Ensure calls / exactly one task start. Base: 101f072702819a4066ed5b037c42191ef1c79d37.

Ownership: internal/localruntime only; preserve sibling changes. No subagents or network except BB; no local builds/tests. Parent owns decisive native Windows execution.

1. Inspect supplied native RED log and trace pre-lock preview, file handle access/share flags, lock acquisition and activation path. Distinguish pending-file read/write contention from lock or task-start races.
2. Add a focused deterministic regression using existing cross-platform Windows seams if feasible. Run RED through BB only, retaining raw output.
3. Make the smallest production ordering/validation fix; preserve read-only, no mutation before trust, ownership/reparse/hardlink/DACL checks, unknown release, pending cancellation and activation guard.
4. Run affected localruntime tests and Windows cross-compile through BB with BB_HOST=x-boss BB_JOBS=2 BB_LANE=ci80-concurrent BB_PULLBACK=0 GOMAXPROCS=2. Remote shell: private physical /tmp root mode0700, private HOME/XDG, umask022, ACP_TEST_ROOT unset.
5. Self-review correctness/security and scope. Write report.md last, then unsigned commit of owned files; verify clean terminal status. Retain raw logs and state native gate limitation.

Design constraint: no retries, sleeps, timeout changes, skipped tests or weakened assertions; no redesign. Stop/report if bounded evidence cannot support a safe fix.

## Execution/review outcome

- Supplied native failure retained verbatim. Found preflight opens pending.json without FILE_SHARE_DELETE outside install.lock; deletion/replacement needs delete access. Added native handle-conflict characterization, compiled only.
- Minimal selected fix: process-local cancellable operation guard before Windows preparation/preflight, retained through activation cleanup. Does not mutate disk or replace the cross-process install lock. Existing 60-second budget now includes guard waiting.
- Deterministic guard RED (unguarded baseline and mutation) then GREEN on BB; full localruntime Linux tests, focused guard race detector, and Windows amd64 test crosscompile passed.
- Self-review: rejected a context-observation integration test because context.WithTimeout can evaluate Done before guard acquisition, making its synchronization misleading. Kept honest guard behavior test + existing four-Ensure native regression + native handle characterization.
- Remaining gate: parent native Windows execution; cross-process preflight contention is not solved by this bounded goroutine fix. No sibling source files changed.
