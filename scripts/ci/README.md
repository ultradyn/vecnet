# Release CI scheduling and acceptance

The workflow preserves full native Linux x64 / Windows x64 / Windows ARM64
Go, Node and E2E coverage. Six native Go lanes per platform execute four
complete whole-root groups each (24 disjoint partitions). Three independent
Linux producer jobs cross-compile platform-selected test packages once in six
bounded groups, after cold-dependency stages; source/hash-bound bundles include
native test2json. Consumers validate/copy to private physical temp, discover
root names natively, then execute binaries with original package cwd—no native
toolchain setup, relinking or repeated go test driver. Packages with20+roots
are partitioned whole-root; smaller/no-test packages remain complete.
`go-timings.json` records complete R7 PASS root maxima across all18 native Go
logs (run44254434973, source101f0727), Windows maximum of x64/ARM. Complete
package process elapsed maxima are retained separately; small whole packages
use that observed process cost rather than summing potentially parallel roots.
Unknown roots use the median complete PASS root cost for their package, or a
positive0.1s fallback when entirely unobserved. Failed/unfinished packages and
roots (including sibling-owned localruntime failures) are explicitly censored;
completed roots in later-failed packages remain valid root observations. Native
skips are not timings. Startup uses the median nonnegative completed package
elapsed minus summed root durations. `go-profile.mjs` replays the source-bound
archived logs without fetching credentials or selecting coverage. New split
Windows drift roots have no native root measurement yet. These are placement
hints, not post-change runtime forecasts or proof of the workflow budget.
Deterministic weighted
placement balances whole roots, then batches each package's roots within a
partition. It is not a coverage allowlist: discovery alone supplies the inventory,
and unknown/new roots always receive a positive cost. Each group runs at most
two independent package binaries concurrently, retaining package cwd and exit
status; a failure stops queued work and drains the other worker. Native
copy/list discovery also has at most two workers. The enclosing budget owns the
entire group and all descendants; no per-child budget multiplication.

Each platform's six consumers wait only for its matching producer, not the
slowest of all three compile jobs. YAML step/strategy anchors share identical
commands, not extra platform exceptions. There are 37 executed jobs including
acceptance (36 before it); `schedule.mjs` supplies the audit cardinality. Eighteen Go
consumers replace36: R5 public FREE concurrency queued Linux starts until249s
and ARM until280s. Fewer consumers cut artifact/setup work and queue waves,
while retaining all24partitions. Actual native timings still must satisfy all limits.
New roots/examples/fuzz seeds and small/no-test packages remain conserved.
Disposable testhub/preference stores copy a closed fully migrated image into
independent files, then regenerate identity material. FreshStore and the named
OpenFixtureStore explicitly select per-database NORMAL, including testhub clone
reopens: these fixtures assert logical state/rollback, not power-loss durability.
StartFull is reserved for FULL maintenance/diagnostic contracts; those assertions
remain unchanged. Direct store/default/migration/compaction and real packaged
binary E2E retain FULL. Production store.Open and operator policy are unchanged;
no process environment override or t.Setenv under parallel tests.

Node file discovery remains exhaustive. R6 measured file costs place whole files
across four groups with two workers and positive unknown fallback. One measured
indivisible file, backends/paseo.test.ts (x6446s, ARM incomplete at50s), has four
registration shards. The CI-only loader wraps actual node:test registration,
including parameterized/new roots; ordinal modulo selects each root once, while
nested assertions and hooks stay with the selected root. No source-name parser,
case allowlist, connector edits or suite timeout changes. A future suite-shaped
registration fails closed for review, not silently losing coverage. Each child
reports registered/selected counts and file elapsed time; all errors drain the
other worker and propagate. E2E files retain original whole-file grouping.

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
and queue time, fails on any missing/failed/incomplete evidence, reserves8s
after non-tag preflight (R5 audit4s/posts0s; verification unmeasured) or35s for tags
(two14s publication caps plus7s verification/cleanup), and exports an absolute overall deadline.
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
