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

Finalized Go test bundles travel as one solid `go-tests.tar.zst` archive of the
contents of `dist/go-tests`, encoded on Linux with `zstd -9 --long=27 -T2`
inside the existing 53s command owner. Artifact upload uses compression level 0
to avoid recompressing it. Consumers download to `dist/go-test-archive`, then
expand with native `tar` in a separate one-minute step before source restore;
the restore bootstrap is inside the archive. Source/hash validation, native
discovery and execution retain their existing order. The parent's R31 Linux
probe reduced the 275,969,980-byte R30 ZIP to 117,595,459 bytes in 4.190s;
decoding took 0.644s with all 55 files byte-identical, and a truncated archive
failed. These are local codec measurements; network savings, native Windows
tar support and the strict full-run <300s / step <60s acceptance remain unproven.

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

Windows root weights retain the prior refreshes, including R31's measured
replacement of the interrupted-publication root after its directory-walk fix.
The R38 refresh takes the maximum of each retained hint and complete PASS root
times from R37 runs 44415327162 and 44416023762 at source `9645a20c` (x64/ARM64).
It adds nine previously unweighted roots and raises 733 existing hints; no hint
decreases. Failed, skipped and unfinished observations contribute no timing.
Linux, package fallbacks, whole-package costs and startup hints remain unchanged.
Whole-package costs require a successful non-overlapping process covering the
exact discovered root set; these new imports supply root costs only.
The file retains source/run/attempt, log SHA256 and metadata receipts. Placement
estimates do not establish native throughput or waive the strict acceptance gates.

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

The reduced `TestStateConcurrentWriters` fixture changed at `4db57b7a` from
1,600 claim attempts to 128, retaining eight competing writers and its property
assertions. Its Windows hint is reset from 21.69s for the old workload to the
2.21s complete-PASS maximum (x64 1.93s / ARM 2.21s) in source-bound run
`44471440331`, attempt 1, source `5946a271`. `rootHintAdjustments` records the
exact root, raw log hashes/lengths and fixture hash unchanged since that change;
all other hints, Linux values, censored records and original source metadata
remain intact. The run itself failed on two other x64 group deadlines.

With existing six Windows lanes, four groups and 33 jobs, replay over all 2,629
Windows roots reduces modeled maximum group load from 50.087s to 41.803s
(8.284s more headroom); worst lane rises from 129.140s to 135.273s. Forty-four
roots lack PASS weights and 101 observations remain censored. These are
execution placement estimates, excluding queue/setup/acceptance, and require
fresh strict native evidence.

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

Windows Go jobs prepare one genuine private, single-link Node image after native
bundle discovery, before the packet pool. An explicit CLI test-binary utility
copies the source, checks private ownership and independent source-matching
bytes, and runs the original real `--version` preparation with its 12s context.
Only success publishes a path. Read-only packet fixtures validate and borrow
that job image; they never remove it or fall back after an invalid path. Real
installation admission and its 5s version probe still run. Cold admission keeps
an independent never-executed copy. Security mutation cases prepare their own
independent mutable copy before valid setup; they never modify the job image.
An `always()` step
removes only the fixed fixture child of the native bundle, ignoring the published
path as a cleanup target. Both new Windows-only steps retain the 53s owner and
one-minute step deadline. This deduplicates preparation; the cause of the R35 ARM
and R36 x64 stalls and a native performance benefit remain unproven.

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
It still runs all five original cases with independently isolated, prepared
mutable images; cold admission is covered separately. Each exclusive root runs serially
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
explicit `GH_REPO` without requiring a checkout. Source producers retain all
build and test inputs and the tag/master ancestry check.

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

The four target producers and three Linux-hosted test-bundle producers use
private `GOCACHE` and `GOMODCACHE` directories under `runner.temp/ci-go-cache`
(`build` and `modules`). Explicit `actions/cache/restore@v4` runs after pinned
Go extraction and before all four unchanged dependency stages. Keys separate
schema `go-compile-v1`, Linux host OS/architecture, exact Go 1.27.4, target
GOOS/GOARCH, target versus tests role, `go.mod`/`go.sum` hash and checked-out
Go source hash. The first fallback retains the full dependency prefix for
source edits; a second fallback drops only the dependency hash so updates reuse
standard-library and other unchanged build objects. Go module download/build
validates input versions and checksums, and Go checks actual changed inputs,
including embedded files, before reuse. The source hash is deliberately coarse;
it does not replace Go input validation. Neither fallback crosses compiler,
host, target or role boundaries.

Release producers restore existing remote snapshots only; their private caches
remain writable for the current job. They publish no new snapshots and have no
automatic cache-save post step. A 315 MB save stalled past the 60s guard after
the required Windows ARM test bundle had uploaded, blocking its consumers;
optional cache persistence must not delay required artifact delivery. Reuse
replaces no build, compile, discovery or native execution command. Native
consumers, assembly, publication and source/artifact validation are unchanged.

Existing snapshots can become less useful as inputs diverge, and repository
cache quota/eviction can remove them. A miss still runs every cold dependency and
compilation stage. Cold-cache and aging-snapshot performance require actual
hosted evidence: strict overall <300s and every-step <60s acceptance, including
restores, remains mandatory. Existing PR-merge-ref snapshots cannot be reused
on master; a warm PR run establishes neither master/tag cache state nor timing.
Restore failures remain hard CI failures with no acceptance bypass.

`budget.mjs 53000 command [args...]` owns the complete command, not one child
at a time. Linux uses a kernel child-subreaper: adopted descendants are killed
and reaped on early success/failure, deadline or cancellation. Windows creates
the leader suspended, assigns a kill-on-close JobObject, resumes it, terminates
and drains the Job on all exits. Parent control pipe closure cancels ownership;
no PID-ancestry sampling or host-wide process cleanup. argv is shell-free,
including existing npmInvocation resolution on Windows. Tests cover early
exit0/7 with detached grandchildren, inherited/ignored pipes, cancellation,
timeout, literal arguments, and actual discovered Go execution conservation.

Windows target artifacts and Go bundles also carry a CI-only native Go owner,
with separate source, SHA-256 and PE architecture checks. It is excluded from
the npm package. Consumers stage validated bytes into a fresh temp directory
and export `CI_WINDOWS_OWNER` before the first budgeted command. Go uses
`windows-owner.mjs use-go`; Node/E2E and Windows package tests use
`native.mjs --owner-only`. The same containment and drain contract applies.
Only an unset owner selects the PowerShell bootstrap; an invalid configured
owner fails. This removes repeated PowerShell and C# compilation from those
steps; exact native lifecycle and full-run timing still require validation.

`accept` selects verified cached native Node 24 after restoring its source. This
uses the same identity-checking selector as Windows consumers instead of
repeating package-manager discovery: the first merged-master run spent 11s
in setup-node, versus 1s in the preceding PR run. Source restoration uses the
hosted runner's existing Node for plain JavaScript, as on Windows. Tagged
publication still pins npm 12.0.1 explicitly. Native timing remains required.

The audit includes setup/post/action and queue time, fails on any
missing/failed/incomplete evidence, reserves8s
after non-tag preflight (R5 audit4s/posts0s; verification unmeasured) or61s for tags
(40s npm +14s GitHub publication caps plus7s verification/cleanup), and exports an absolute overall deadline.
All remaining commands cap themselves to that deadline with cleanup reserve;
npm publication additionally has a40s cap and GitHub Release creation a14s cap.
The npm helper shares a38s internal deadline across lookup/upload/verification;
after one successful upload it polls only registry visibility for at most5s,
within that deadline. Publication is never retried. No retries mask failures.

## npm publication recovery

Immediately after a successful upload, before any visibility request, the
Actions summary records a full `accepted` receipt binding source, version,
tarball and integrity. Verification failures append a failure indication while
retaining that receipt and failing the step, without pending/verified output.
Pre-upload lookup failures or failed uploads do not record acceptance.

A successful upload followed only by404 responses through that visibility
window now returns **pending**, rather than failing or claiming npm availability.
The publication step emits `state=pending` or `state=verified` for the GitHub
Release step and records a JSON receipt in its existing Actions summary. The
receipt binds the full source commit, package version, tested tarball filename,
and SHA-512 integrity. `verified` means registry metadata matches that integrity;
upload errors, lookup/transport errors and conflicting integrity still fail.
The summary is the receipt store; there is no new job or artifact transfer.

Pending publication creates a clearly described **draft** GitHub Release, with
the tested assets and no claim that npm is available. Existing drafts remain
drafts even on verified recovery: the controller must verify actual public
tarball bytes before finalizing them or installing. Pending state against an
existing public GitHub Release fails for controller review. A green pending
upload result does not change an earlier failed run or establish public npm
availability; the completed-run budget acceptance status remains required.

Do not rerun the publish command or a tagged workflow while that version is
pending: the summary receipt is evidence, not a cross-run upload lock. Keep the
same version and wait for registry visibility. Once metadata appears, the
existing exact-integrity recovery path skips uploading, but do not invoke it
while the version still returns404. There is no reason to reauthenticate merely
because an accepted version is processing.

Controller recovery uses the original source/run's tested `npm-release`
artifact, not a repacked archive. Confirm its version/source/integrity against
the Actions receipt and its `SHA256SUMS`. From the directory containing `dist`,
these bounded, read-only commands compare the public tarball with those bytes
(set `version` from the receipt;404 remains pending):

```bash
version=VERSION_FROM_RECEIPT
public_archive=$(mktemp)
(cd dist && sha256sum --check SHA256SUMS) &&
  curl --fail --silent --show-error --max-time 15 \
    "https://registry.npmjs.org/vecnet/-/vecnet-$version.tgz" --output "$public_archive" &&
  cmp "dist/vecnet-$version.tgz" "$public_archive"
```

Only after that comparison succeeds and the exact-source budget acceptance
passes may the controller finalize an existing draft. That evidence can come
from accepted source CI; recovery does not require a new successful tag run to
replace a failed upload attempt. Replace pending notes with verified release notes:
`gh release edit "v$version" --draft=false --notes-file VERIFIED_RELEASE_NOTES.md`.
Remove the temporary public archive after inspection. None of these recovery
commands upload to npm, create a new version, or repair an earlier failed run.

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
unrelated documentation screenshots are omitted from these consumers. Target
producers retain cmd, internal, connectors, scripts/ci, scripts/npm and npm,
including the Go package that embeds connector assets. Test producers retain bench, cmd,
internal, connectors, docs/protocol, scripts/ci and scripts/npm. Every Go package
and embedded asset remains present, and the source archive still selects from
the complete committed Git tree. Root files and cone ancestors are retained.
At R26 these producer checkouts contain 20–27 MB of the 155 MB tree; full
checkout included 86 MB of documentation and 42 MB of scratch files. This
reduces transferred input, without establishing a native runtime guarantee.


R16 source staging replaces each native Go consumer checkout with a
producer-created archive of the same sparse source trees, ancestor files and
root files. The archive uses the immutable source revision and preserves Git
blob bytes, including hidden root fixtures. The same-run test artifact carries
a standalone Node bootstrap and a receipt binding the archive's revision and
SHA-256; restore rejects mismatches before native tar extraction. Git remains
installed for tests which create their own repositories and worktrees. The
source checkout's `.git` directory is not required by the native Go tests.

Windows Go consumers use the runner's existing Node for this plain JavaScript
source restore, then `native-node.mjs` selects a cached canonical Node 24 and
verifies its actual version, native architecture and platform before publishing
PATH. This avoids npm/yarn version discovery in jobs that only require Node.
A missing or mismatched cache fails explicitly. Earlier unloaded measurements
were 8–9s for selection versus 6–7s for setup-node; R23's critical setup-node
steps took 29s and 22s. The change targets that startup variance and does not
establish a median speedup or a successful overall budget.

On Linux and Windows ARM64, Node groups 0, 1 and 2 run with the helper guards;
only group 3 follows the four E2E groups. Windows x64 runs groups 0 and 1 with
the guards, and groups 2 and 3 after E2E. R26's previous x64 layout put all
four Node groups after E2E, producing a 209s job and the final test completion
at +314s. Returning groups 0 and 1 to the Node job moves 52s in that fixed
duration comparison; package eligibility also moves later and runner queues
still require native measurement. All groups,
platforms, shards and assertions remain admitted once, without additional
jobs. These changes enable overlap and remove repeated checkout work; actual
native completion below 300 seconds and individual steps below 60 seconds
are still required.

The three Node platform jobs keep identical steps but expose completion
milestones for package admission. Windows package gates wait for build, the
ARM test producer and x64 Node completion; Linux package gates wait for build
and the ARM test producer. Linux packages can run alongside their Node job:
R31's Linux Node completion at +243s postponed package completion to +282s
and acceptance preflight to +296s. Both package inputs were ready at +126s.
Removing that unrelated dependency targets the measured tail; it does not
establish a new native duration. This makes every ARM Go lane
eligible before package
gates claim runner slots, without adding jobs or test commands. Actual GitHub
allocation, full native correctness and the strict overall budget remain
required; the dependency graph does not guarantee a queue order.
