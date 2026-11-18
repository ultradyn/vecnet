# Release CI scheduling

The release workflow runs Go, Node connectors, real-hub E2E, and packing
independently. Linux x64 and both Windows architectures retain the full native
Go/Node/E2E suites. Linux ARM64 and all other target platforms install and test
the **same** checksum-bound tarball. Publishing still requires every suite,
the matching version tag on master, and npm OIDC.

`budget.mjs 53000 command [args...]` is the outer deadline for an entire step,
including its discovery, compilation and children. Exit 124 means budget
failure, not a retry or a skip. It uses shell-free argv (including npm on
Windows), POSIX process-group plus descendant cleanup, or Windows `taskkill
/T /F`; automatic Actions/setup/post steps have one-minute platform ceilings.
Actual timestamps, including post steps and queueing, must still prove the
strict **<60s every step / <300s end-to-end** requirement. Four-minute job caps
and one-minute step caps are fail-fast guards, not performance evidence.

`suite.mjs discover-go` compiles and inventories all native Go packages using
Go's own `-list .`. `suite.mjs go LANE GROUP` conserves the complete inventory:
large packages split by whole root test/example/fuzz name across two lanes and
eight groups; smaller/no-test packages run unfiltered exactly once. Fuzz seed
corpora still run normally (this does not add a fuzzing campaign). Go test
result caching is disabled with `-count=1`.

`suite.mjs node 0 GROUP` distributes the original connector file globs over
four groups. `suite.mjs e2e 0 GROUP` distributes the four real-hub files over
four groups. All original E2E case bodies/assertions are retained, with shared
setup in `connectors/launcher/test/e2e/harness.ts`. The measured serial
`codex-engine.test.ts` workload is likewise moved without assertion changes
into four files with `codex-engine-harness.ts`; loop-generated registrations
remain in test files, never in shared imports. Test-native platform skips
remain owned by the original tests; scheduling adds none. New matching files
and Go roots enter the inventory automatically.

Production/default SQLite **FULL** remains in force. The supported NORMAL dev
profile was measured on disposable build-box databases but not adopted: no
material gain was established and explicit FULL contracts must remain intact.

Run helper tests with `node --test --test-concurrency=2 scripts/ci/*.test.mjs`
on the build box, not locally. These include real generated Go suites to
check execution conservation, literal argv/exit propagation, and an
adversarial detached grandchild deadline. Native Windows is required to prove
its taskkill path; Linux is not a substitute. Cold cache/setup/compilation,
Windows root-test costs and runner queueing can still make this candidate
RED. Do not raise budgets or drop coverage to make it green.
