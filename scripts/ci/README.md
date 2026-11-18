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
whole-package process elapsed >8s. On Windows, if no whole measurement is
available, the sum of root hints is used instead. Cheap, unknown-small,
single-root and no-test packages remain whole. The 8s split threshold and
packet caps are scheduling policy,
not a measured speedup or a claim that the native budget passes.
`go-timings.json` started with complete PASS root maxima from all 14 native Go jobs
in R12 (run 44271362850, attempt 1, source `6845842d`): Windows used four
workers with `GOMAXPROCS=1` and independent fixture executable copies. Windows
weights use the maximum observed PASS across x64 and ARM64. Whole-package
elapsed is distinct from filtered packet elapsed.
Unknown roots use the median complete PASS root cost for their package, or
0.1s when entirely unobserved. Failed, skipped and unfinished observations are
censored; a root that passed on x64 can still have a censored ARM64 record.
Startup uses the median nonnegative package PASS elapsed minus summed root
elapsed, excluding overlapping same-package processes because test2json does
not identify which process emitted an event.

The Windows placement profile refresh uses R16 run 44305065506 PASS values.
When a root has no ARM64 PASS in that run, it also considers the latest
same-architecture PASS from R14 (44292345778), or R12 if absent, retaining the
larger cost. Whole-package costs require a successful non-overlapping process
covering the exact discovered root set. If only filtered or ambiguous samples
exist in R16, keep a verified R12 whole-package sample when available.
Failures, skips and unfinished roots do not contribute PASS root timings.
Linux keeps its R12
profile. The file retains source/run/attempt and SHA256 log receipts.

Replay with `node scripts/ci/go-profile.mjs DIRECTORY OUTPUT SOURCE_SHA RUN_ID
ATTEMPT [INVENTORY_JSON]`. Without inventory the importer emits root costs only,
not whole-package elapsed. Inventory JSON binds `sha`, `run`, `attempt` and a
`platforms` map (`linux`, `windows-x64`, `windows-arm` → package/root arrays).
The importer requires the exact Go lane names and unique job IDs of
one complete geometry: historical 6+6+6 or current 2+6+6. Job metadata must
match source/run/attempt and completion; the current archive also requires
matching `run.json` and each indexed log's SHA256 and byte length. The output
retains the raw log hashes. R12 supplies 2,557 Linux and 2,548 Windows observed
PASS roots, with 89 censored records. Of 2,559 Linux and 2,592 Windows discovered
roots, 2 Linux and 44 Windows roots have no PASS weight. The profile never
selects coverage.

Deterministic weighted
placement balances whole roots, then forms package packets within a partition:
at most8 roots and4 estimated root seconds, plus one observed startup hint.
An indivisible expensive root may exceed that placement cap; it is never split
or dropped. Spare workers can now execute multiple packets of the same package.
Every process receives independent private physical HOME/USERPROFILE, APPDATA/LOCALAPPDATA,
TMPDIR/TMP/TEMP and XDG config/cache/data/state/runtime directories, removed after it exits.
Original cwd, binary filenames and native assertions/hooks/cleanup remain intact. It is not a coverage allowlist: discovery alone supplies the inventory,
and unknown/new roots always receive a positive cost. Only the GitHub Actions
native Go group opts into six ordinary packets on Windows and four on Linux.
R16 showed genuine Node admission contention at six Windows packets before
the three exact caller exclusions below. The remaining ordinary callers and
throughput at this width still require native validation. The hosted runners
were observed with four CPUs.
Local Go execution, Node and other default pool callers use two.
A failure stops all queued work, drains every already-running worker and preserves
the first rejection. Compilation and bundle discovery have separate
capacity policies. The enclosing budget owns the
entire group and all descendants; no per-child budget multiplication.

On Windows only, these discovered roots use exclusive singleton packets:
`TestSlowSSEConsumerDoesNotBlockSend`,
`TestCodexHookWindowsInstalledCommandPreservesArgv`, and
`TestLauncherWindowsManagedRepairPreservesServiceIntent`, plus the three CLI
roots that failed cold Node admission in R16: `TestLauncherWindowsInstallPreviewAndExplicitIntent`,
`TestLauncherWindowsInstallRejectsUnsafeDACLAndHardlinks`, and
`TestLauncherWindowsRepairPendingDriftUnit`, and the exact R21 failure
`TestLauncherWindowsInstallRejectsUnsafeExecutableBeforeExecution`. In the R21
full run its first valid cold Node probe failed under the ARM64 ordinary pool;
the same root passed in isolation in a separate diagnostic, after other tests.
It still runs all five original independently cold cases. Each exclusive root runs serially
before the ordinary packet pool in its assigned group, with the same process
isolation, root filter and deadlines. The repair root still runs all four
original cases together, alone in its existing group slot: R16 recorded a
27.95s repair PASS, then the ordinary tail ran out of the 53s group budget.
Windows placement evaluates the serial prefix plus a six-worker packet
makespan, with a soft 37s target and lane-total balancing. This is not a limit
or a forecast. All-15 launcher-fixture caller serialization was rejected in
replay because its stressed bins exceed the owner. Untested contention and
native queue/acceptance remain open; Linux placement is unchanged. The R21
archive replay did not establish a critical-path benefit from refreshing the
shared Windows timing profile or using separate architecture profiles, so the
existing timing profile and packet caps remain. The added isolation is a
functional admission candidate, not a CI budget fix.

Diagnostic runs 44294556720, 44295400381 and 44297999615 exercised these
roots under different loads with `GOMAXPROCS=2`, not Release Windows' `1`.
Earlier isolated repair samples from diagnostic run 44282441492 used `1`,
but were taken after other work in the same job. They support testing this
policy, not assuming the cold Release bin will pass or meet the budget.

Each platform's consumers wait only for its matching producer, not the
slowest of all three compile jobs. YAML step/strategy anchors share identical
commands, not coverage exceptions. There are33 executed jobs including acceptance
(32 before it); `schedule.mjs` supplies the audit cardinality. R12 completed
in 351s, with a 60s Linux Node artifact download and a failing ARM Go group.
The critical path was ARM Go lane 4, then acceptance; Linux Go started up to
49s after its producer and also finished late. Replaying the same R12 PASS
costs, the refreshed weights reduce the modeled worst ARM execution lane
from 161s to 142s versus stale placement. With archived producer times,
consumer setup/post, observed queue delay and acceptance, the model still
lands around 352s. Linux 4/6 and Windows 8 lanes did not support a sub-300s
run in the queue model, and alternative packet caps offered little ARM gain.
Job count, packet caps, isolation and all deadlines are unchanged. These
models are not a native pass or evidence that the clipboard failure is fixed.
Source producers retain full discovery. Native Go, package and acceptance
consumers restore source from their producer artifacts; Node/E2E retain sparse
checkouts. Non-tag assembly checkout is shallow; pushed version tags retain
full ancestry. Actual native
completion under 300s and every step under 60s remain mandatory and unproven.
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

The four package artifact consumers restore the producer's committed
`scripts/ci`, `scripts/npm`, `npm` and complete `connectors` trees, plus root
and ancestor files. Package tests compare shipped launcher source against
those exact bytes; installed runtime, schemas and the systemd template come
from the tarball. Build stages this source archive beside the npm tool and
tested tarball in `npm-release`, outside the published payload and checksums.
Acceptance restores the same verified archive; the GitHub Release CLI uses
explicit `GH_REPO` without requiring a checkout. Source producers retain full
checkouts and the tag/master ancestry check.

The Linux build stages the already-pinned npm 12.0.1 package after packing
vecnet. `npm-release` carries it as `npm-tool/npm.tgz` next to the exact
vecnet tarball and `SHA256SUMS`, not inside either published payload. The
nested archive avoids unpacking the npm tree on Linux and in acceptance.
Windows consumers extract it with native tar, validate its manifest, execute
its CLI to confirm the version, and set `npm_execpath` for the package test.
The real npm installation of the checked vecnet tarball and generated `.cmd`
shims are still tested. Linux package tests and OIDC publishing keep the
global pin. R13 measured a 28s global npm pin on each Windows package runner;
the replacement's net savings and any queue improvement need native evidence.

The three Linux-hosted test-bundle producers explicitly request
`CI_GO_COMPILE_JOBS=4`. One shared policy validates canonical integers1..6,
caps explicit requests by `os.availableParallelism()`, and applies the same
effective value to both Go `-p` and the producer Go subprocess `GOMAXPROCS`.
Unset requests preserve the existing two-job default and inherited environment.
All cold dependency build stages, six bundle compile groups and event-tool
finalization use this policy; malformed overrides fail before producer work.
Other target/assembly builds and discovery are unchanged. For native execution,
`GOMAXPROCS` stays `1` per Windows process and `2` per Linux process; wider
Release packet pools do not change the per-process setting.
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

R12 controlled comparison (run44268049956) replayed three previously failing
Windows groups against the same R11 test bundle. With four workers, GOMAXPROCS1
completed retention and OAuth groups below the existing deadline; GOMAXPROCS2
timed out retention on x64. Two workers/GOMAXPROCS1 passed all three groups,
but took longer. The four-worker runs exposed a separate fixture hardlink
race: synthetic CLI helpers linked the shared running test image while managed
repair correctly required a single-link image. Fixture helpers now copy their
Windows image; guards and every original repair assertion remain intact.
That comparison used one Go scheduler thread per packet and four independent
workers; the current hosted ordinary pool is documented above. Producer compilation, Node/E2E, Linux
and all time/coverage/source gates are unchanged. Full native acceptance is
required; these focused comparisons do not establish the overall300s budget.

Node/E2E checkouts retain complete connectors, internal, scripts, docs/protocol
and npm directories plus root files. Discovery still sees all connector test
files, backend fixtures, embedded protocol schemas and cross-platform scripts;
unrelated documentation screenshots are omitted from these consumers. Source
producers retain full checkout, so Go discovery cannot lose new package roots.


R16 source staging replaces each native Go consumer checkout with a
producer-created archive of the same sparse source trees, ancestor files and
root files. The archive uses the immutable source revision and preserves Git
blob bytes, including hidden root fixtures. The same-run test artifact carries
a standalone Node bootstrap and a receipt binding the archive's revision and
SHA-256; restore rejects mismatches before native tar extraction. Git remains
installed for tests which create their own repositories and worktrees. The
source checkout's `.git` directory is not required by the native Go tests.

Node groups 0 and 1 remain with the Node helper guards. On Linux and Windows
ARM64, group 2 also runs there; only group 3 follows the four E2E groups.
Windows x64 retains groups 2 and 3 in its E2E job, keeping its earlier Node
completion milestone available for package admission. All groups,
platforms, shards and assertions remain admitted once, without additional
jobs. These changes enable overlap and remove repeated checkout work; actual
native completion below 300 seconds and individual steps below 60 seconds
are still required.

The three Node platform jobs keep identical steps but expose completion
milestones for package admission. Windows package gates wait for build, the
ARM test producer and x64 Node completion; Linux package gates wait for build,
the ARM test producer and Linux Node completion. This makes every ARM Go lane
eligible before package
gates claim runner slots, without adding jobs or test commands. Actual GitHub
allocation, full native correctness and the strict overall budget remain
required; the dependency graph does not guarantee a queue order.
