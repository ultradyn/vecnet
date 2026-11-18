# Release CI scheduling and acceptance

The workflow preserves full native Linux x64 / Windows x64 / Windows ARM64
Go, Node and E2E coverage. Twelve native Go lanes per platform execute two
complete whole-root groups each (24 disjoint partitions). Three independent
Linux producer jobs cross-compile platform-selected test packages once in six
bounded groups, after cold-dependency stages; source/hash-bound bundles include
native test2json. Consumers validate/copy to private physical temp, discover
root names natively, then execute binaries with original package cwd—no native
toolchain setup, relinking or repeated go test driver. Packages with20+roots
are partitioned whole-root; smaller/no-test packages remain complete.
`go-timings.json` records R4 measured slow roots and positive package-cost
fallbacks, including the supplied native FULL hub probe. Deterministic weighted
placement balances whole roots, then batches each package's roots within a
partition. It is not a coverage allowlist: discovery alone supplies the inventory,
and unknown/new roots always receive a positive cost. Each group runs at most
two independent package binaries concurrently, retaining package cwd and exit
status; a failure stops queued work and drains the other worker. Native
copy/list discovery also has at most two workers. The enclosing budget owns the
entire group and all descendants; no per-child budget multiplication.

Each platform's twelve consumers wait only for its matching producer, not the
slowest of all three compile jobs. YAML step/strategy anchors share identical
commands, not extra platform exceptions. There are 55 executed jobs including
acceptance (54 before it); `schedule.mjs` supplies the audit cardinality. More
native runners trade serial runtime for hosted concurrency, artifact transfer
and queue overhead. R4 timing replay is only a placement estimate, not native
GREEN; actual queue/setup/Windows FULL tails must satisfy the same limits.
New roots/examples/fuzz seeds and small/no-test packages remain conserved.
No SQLite override: production and test default FULL remains unchanged.

Four independent target builds produce exact-source/version/toolchain/hash
receipts and all eight CLI/hub binaries. Node/E2E reuse those fresh matching
artifacts instead of repeating Go setup/builds. `build.mjs --assemble DIR`
validates the complete target set before packaging runtime, licenses and
BUILD_INFO. No-argument `build.mjs` still performs the original sequential local
build. Every native package gate verifies the same tarball checksum; publish
retains matching master/tag guard, npm12 trusted publishing/OIDC/provenance.

Cold dependency stages: module download, standard library, SQLite, remaining
large UI/schema/MCP dependencies. All compilation uses matching `-trimpath`
flags. The pinned official Go1.27.4 archive is checksum verified; download and
native-tar extraction are distinct bounded steps, avoiding ARM .NET ZIP
extraction. Reproduce cold state with private empty GOCACHE/GOMODCACHE, never
by deleting shared caches. Native cold/ARM timings still require actual CI.

`budget.mjs 53000 command [args...]` owns the complete command, not one child
at a time. Linux uses a kernel child-subreaper: adopted descendants are killed
and reaped on early success/failure, deadline or cancellation. Windows creates
the leader suspended, assigns a kill-on-close JobObject, resumes it, terminates
and drains the Job on all exits. Parent control pipe closure cancels ownership;
no PID-ancestry sampling or host-wide process cleanup. argv is shell-free,
including existing npmInvocation resolution on Windows. Tests cover early
exit0/7 with detached grandchildren, inherited/ignored pipes, cancellation,
timeout, literal arguments, and actual discovered Go execution conservation.

`accept` audits finished source-bound jobs/steps including setup/post/action
and queue time, fails on any missing/failed/incomplete evidence, requires45s
remaining reserve before publication, and exports an absolute overall deadline.
All remaining commands cap themselves to that deadline with cleanup reserve;
publish/release commands additionally have14s caps. No retries mask failures.
The completed-run `Release budget acceptance` workflow checks the entire run
including acceptance/publication/post steps under the same strict <300s and
<60s policy. It publishes the explicit commit status **Release budget acceptance** on the
triggering tested SHA (not default-branch HEAD), pending then success/failure,
linked to exact source run/attempt; API/source/audit errors fail closed. Require
that exact status context for release/branch acceptance; an ordinary
Release green badge alone is insufficient. Its policy runs from the trusted
default branch, not arbitrary privileged PR checkout code; only statuses:write
is added for binding. Until this listener is on master, parent external audit
remains mandatory.

Five-minute job ceilings are cancellation guards, not a longer allowed overall
budget. Native evidence and final acceptance audit must be green before release
acceptance. Cold compilation, scheduling/queue limits and indivisible tests can
still make a candidate RED; never raise user limits or drop coverage.
