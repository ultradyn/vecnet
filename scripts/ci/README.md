# Release CI scheduling and acceptance

The workflow preserves full native Linux x64 / Windows x64 / Windows ARM64
Go, Node and E2E coverage. Two Linux and six Windows lanes per platform execute
four complete whole-root groups each (8 Linux / 24 Windows partitions). Three independent
Linux producer jobs cross-compile platform-selected test packages once in six
bounded groups, after cold-dependency stages; source/hash-bound bundles include
native test2json. Consumers validate/copy to private physical temp, discover
root names natively, then execute binaries with original package cwd—no native
toolchain setup, relinking or repeated go test driver. Test binaries and native
test2json use normal Go linker stripping (`-s -w`), without changing production
binaries or weakening source/hash receipts. Packages with20+roots are partitioned
whole-root. Smaller packages also split when they have at least two roots and
complete measured process elapsed >8s. Cheap, unknown-small, single-root and
no-test packages remain whole. The8s split threshold and packet caps are scheduling policy,
not a measured speedup or a claim that the native budget passes.
`go-timings.json` records complete R9 PASS root maxima across all18 native Go
logs (run44260174057 attempt1, source1c05da91), Windows maximum of x64/ARM. Complete
package process elapsed maxima are retained separately; small whole packages
use that observed process cost rather than summing potentially parallel roots.
Unknown roots use the median complete PASS root cost for their package, or a
positive0.1s fallback when entirely unobserved. Failed/unfinished packages and
roots (including the empty app-server JSON assertion) are explicitly censored;
completed roots in later-failed packages remain valid root observations. Native
skips are not timings. Startup uses the median nonnegative completed package
elapsed minus summed root durations. `go-profile.mjs` replays the source-bound
archived logs without fetching credentials or selecting coverage. Invoke with
`DIRECTORY OUTPUT SOURCE_SHA RUN_ID ATTEMPT`; job metadata must bind the complete
native log set, and output retains SHA256 hashes of those exact logs. R9 supplies
2557 Linux and2327 Windows complete PASS roots, with81 censored records;
264 Windows and2 Linux discovered roots still lack PASS observations. These are placement
hints, not post-change runtime forecasts or proof of the workflow budget.
Deterministic weighted
placement balances whole roots, then forms package packets within a partition:
at most8 roots and4 estimated root seconds, plus one observed startup hint.
An indivisible expensive root may exceed that placement cap; it is never split
or dropped. Spare workers can now execute multiple packets of the same package.
Every process receives independent private physical HOME/USERPROFILE, APPDATA/LOCALAPPDATA,
TMPDIR/TMP/TEMP and XDG config/cache/data/state/runtime directories, removed after it exits.
Original cwd, binary filenames and native assertions/hooks/cleanup remain intact. It is not a coverage allowlist: discovery alone supplies the inventory,
and unknown/new roots always receive a positive cost. Each group runs at most
four independent package binaries concurrently on Windows and two on Linux,
retaining package cwd and exit status. The bounded pool accepts an explicit
positive integer capacity; its default remains two for Node and other callers.
A failure stops all queued work, drains every already-running worker and preserves
the first rejection. Compilation and native copy/list discovery retain two workers. The enclosing budget owns the
entire group and all descendants; no per-child budget multiplication.

Each platform's consumers wait only for its matching producer, not the
slowest of all three compile jobs. YAML step/strategy anchors share identical
commands, not coverage exceptions. There are33 executed jobs including acceptance
(32 before it); `schedule.mjs` supplies the audit cardinality. Reducing Linux6 to2
frees four runner slots while keeping Windows6+6. Source-bound R9 inventory
(46/2559 Linux packages/roots;45/2591 Windows) gives packet worker-model maximum
Linux group25.17s / lane88.94s with2 lanes;1 lane costs169.83s. Windows6 models
44.005s maximum group /116.235s lane. These are PASS-cost placement models, not
runtime guarantees:264 Windows roots use fallbacks and process startup is uncertain.
An explicit20-slot replay with archived producer release dates, non-Go occupancy,
maximum observed consumer setup/idle/post costs and35s accept models354s for
Linux2 vs392s for Linux6: neither meets300s. Sparse consumer checkout retains all
source/fixture directories documented by the frozen checkout scout; producers
retain full discovery. Accept uses only scripts/ci, scripts/npm and npm. Non-tag
assembly checkout is shallow; pushed version tags retain full ancestry. Sparse
speedups are unmeasured and not subtracted from the model. Native completion
under300s and every step under60s remain mandatory and unproven.
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

The three Linux-hosted test-bundle producers explicitly request
`CI_GO_COMPILE_JOBS=4`. One shared policy validates canonical integers1..6,
caps explicit requests by `os.availableParallelism()`, and applies the same
effective value to both Go `-p` and the producer Go subprocess `GOMAXPROCS`.
Unset requests preserve the existing two-job default and inherited environment.
All cold dependency build stages, six bundle compile groups and event-tool
finalization use this policy; malformed overrides fail before producer work.
Other target/assembly builds, discovery and native execution remain unchanged:
native `GOMAXPROCS=2`, four Windows execution workers and two Linux workers.
Parent's actual ubuntu-24.04 capacity probe run44264657616, heada573c13b,
`capacity-probe/capacity.json` verified availableParallelism4, cpus4 and
16464306176 bytes totalMemory. This is allocation evidence, not speedup or memory
acceptance. The runtime cap also handles future lower-CPU allocations. BB checks
retain two jobs; full native time/memory and300s/60s acceptance remain parent-owned.

Discovery's real compiled helper prepares one reusable native test2json before
packet execution. Go1.27 can compile tool commands into GOCACHE on demand;
invoking `go tool test2json` after each packet changes HOME/XDG would rebuild the
tool repeatedly in private caches. Bundle consumers already use the hash-bound
native event tool; discovery now likewise avoids invoking the Go driver during
private packet execution, without changing assertions or root selection.

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
