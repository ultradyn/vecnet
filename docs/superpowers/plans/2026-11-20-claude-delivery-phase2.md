# Claude Delivery Phase 2 — Real-Host Canary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Linux pointer-only Claude canary usable on a real host. Each build is qualified by the digest of its kernel-attested executable rather than by `CLAUDE_CODE_VERSION`. The canary is opted into per profile for one exact peer scope, and status reports the result truthfully. This plan also closes the slice-3 final-review residuals.

**Architecture:** Registration hashes the executable behind `/proc/<pid>/exe` of the verified Claude process. It caches the digest by file identity in the per-user state domain and stores the digest, identity and table label on a v2 binding row. Admission checks that row against a pinned table, and the live exe identity must still match. A durable per-profile canary record replaces the daemon env var as the gate. The adapter admits at most one peer scope, pins its children to that binding's config, promotes only arrivals after admission, and keeps its claim ledger bounded. Status shows `canary` only when the host sidecar confirms admission.

**Tech Stack:** Go CLI (`go test`, `golang.org/x/sys/unix`), TypeScript launcher (node:test via `just ln-test`), Python E2E harness, disposable loopback hub, real Claude Code 2.1.322 under `vecnet-test-<nonce>` tmux with a loopback model stub.

**Spec:** `docs/superpowers/specs/2026-11-19-claude-delivery-design.md` (binding authority: Automatic setup and exact ownership; Claim, recovery and acknowledgements; Acceptance and rollout). Residuals come from `.superpowers/sdd/2026-11-19-claude-delivery-chain/progress.md` lines 39–41. Base: `0cd6e37d` on `claude-delivery-phase2`.

## Global Constraints

- "Unknown or untested builds stay legacy until qualified; no path-type or version-number guess." A build qualifies only when the sha256 of its kernel-attested executable is in the pinned table. The table label is the version shown. The path, `CLAUDE_CODE_EXECPATH` and `CLAUDE_CODE_VERSION` never qualify a build.
- Initial table: one entry, `{Label: "2.1.322", Platform: "linux/amd64", SHA256: "bfb3703ad5380242328459bc62ada4bde8ae7ba57c6e1eeadb9c00e08874c478"}`. No env var, flag or file can add an entry. Only `_test.go` code may extend it, through a helper that restores it with `t.Cleanup`.
- "The first canary accepts peer credentials and a single exact recipient scope only." "`claude enable` alone cannot turn a gated adapter on." No `--as` scope and no principal credential may hold the canary, and at most one canary record may exist per user.
- Pointer-only frame, one LF-terminated record, "4 KiB maximum encoded frame", "one-second connect/write deadline", "one native attempt in flight per scope", "128 unresolved native reservations per scope". These are unchanged from slice 3.
- "No native auto-ack in the initial implementation." Observing the hub's ack position is read-only and never posts `/v1/ack` or a receipt.
- "Commit `attempting` before any possibility of writing bytes." `attempting`/`uncertain` is never resent or reclaimed automatically.
- Durable opt-outs (global and addressed) are never cleared by enrollment, a hook, repair or canary enable.
- Disk formats: `claude-bindings.json` becomes v2 and the Claude status sidecar becomes v2. Both are read only by the CLI and runtime of the same release. Older readers refuse them, which leaves the host legacy (fail closed).
- Tests never use the user's working Claude session, hub identity, config or transcript. They use private `HOME`s, fixture credentials, the Go helper-process fixture, a loopback hub and stub, and `vecnet-test-<nonce>` tmux with verified teardown. No paid provider.
- Go tests run under `umask 022`. `just` needs `scripts/bb/bin` on `PATH` (on x-boss, use the `~/bb-src` toolchain env). Commits carry no co-author trailer.

## Review Focus

1. **Claude auto-updates under a live session.** The updater replaces or unlinks the version file. The running session keeps its qualified digest, because `/proc/<pid>/exe` still opens the old inode. A new session on an untabled build stays legacy. Owned by Task 1 (`TestClaudeBuildSurvivesUnlinkedExecutable`).
2. **Cold page cache makes the 340 MB hash slow inside the 1.5 s hook budget.** Observation gives up within its own bound and caches nothing partial. The hook output stays byte-identical, and the next hook retries. Owned by Task 1 (`TestClaudeBuildObservationIsBounded`) and Task 2 (`TestClaudeSetupSlowDigestLeavesHookOutput`).
3. **`/proc/<pid>/exe` is unreadable (hidepid, EACCES).** Passive registration still succeeds, the binding is unqualified with a stable reason, and the hook never errors. Owned by Task 1 (`TestClaudeBuildUnreadableExeIsUnqualified`).
4. **A stale `CLAUDE_CODE_VERSION=2.1.322` export is left over from the old test deck.** An untabled build stays legacy despite it. A qualified build with a contradictory export stays qualified. Owned by Task 2 (`TestClaudeRegistrationIgnoresVersionEnv`).
5. **One hub is reached through two URL aliases** (`127.0.0.1` vs `localhost`, LAN vs public). Both aliases yield one binding key. The second root is refused ownership, and a second canary enable is refused. Owned by Task 2 (`TestClaudeRegistrationHubAliasesShareOwner`) and Task 3 (`TestClaudeCanarySingleScope`).

## Pre-flight conflict scan

Tasks run sequentially, in order. The table shows the files that overlap, so a controller can serialize or brief accordingly.

| Task | Primary files | Shares files with |
| --- | --- | --- |
| 1 Build observation | new `internal/cli/claude_build{,_linux,_other}.go`, `claude_build{,_linux}_test.go` | none |
| 2 Binding v2 | `claude_binding.go`, `claude_setup.go`, `claude_status.go`, `claude_deliver.go`, `claude_deliveries.go`, their tests | 3, 4, 5 (`claude_status.go`, `claude_binding.go`, `claude_deliver.go`) |
| 3 Canary opt-in + status | new `claude_canary.go`; `claude_status.go`, `claude_binding.go`, `commands.go`; TS `claude-status.ts`, `claude-adapter.ts`, `host.ts`, `daemon.ts`, `codex/delivery-main.ts` | 2, 5, 6, 7 |
| 4 Helper hardening | `claude_deliver.go`, `claude_deliver_linux.go`, `claude_deliver_peer_other.go`, `claude_deliver_test.go`; TS `claude-adapter.ts` transport | 2, 3, 5, 6 |
| 5 Child pinning + baseline | `claude_status.go` (snapshot), `claude_binding.go`; TS `claude-status.ts`, `claude-adapter.ts` | 2, 3, 4, 6 |
| 6 Ledger hygiene | TS `claude-adapter.ts`, `claude-adapter.test.ts` | 3, 4, 5 |
| 7 Docs | `commands.go`, `docs/PROTOCOL.md`, `docs/connectors/claude.md` | 3 (`commands.go`) |
| 8 Acceptance | `scripts/e2e/claude-delivery-e2e.py`, `test_claude_delivery_e2e.py`, `build-e2e-page.py` | none |

`claude-adapter.ts` is touched by Tasks 3–6. Each later task must rebase its brief on the previous task's committed adapter, and no two of them run in parallel.

---

### Task 1: Kernel-attested build observation and digest cache

**Files:** Create `internal/cli/claude_build.go` (table, cache document, evidence type), `claude_build_linux.go` (exe open/stat/hash), `claude_build_other.go` (always unqualified), `claude_build_test.go`, `claude_build_linux_test.go`.

**Interfaces:**
- Produces `type claudeQualifiedBuild struct{ Label, Platform, SHA256 string }`, `var claudeQualifiedBuilds = []claudeQualifiedBuild{…}` (the one Global Constraints entry), and `func claudeQualifiedBuildFor(sha string) (claudeQualifiedBuild, bool)`. That function requires `Platform == runtime.GOOS+"/"+runtime.GOARCH`.
- Produces `type claudeBuildEvidence struct{ SHA256, Identity, Build, Reason string }`. `Identity` is the decimal `"<dev>:<ino>:<size>:<mtime_ns>:<ctime_ns>"` from `fstat` of the opened exe. `Build` is the table label, or `""` when the build is unknown.
- Produces `func (a *app) observeClaudeBuild(ctx context.Context, pid int, start string) (claudeBuildEvidence, error)`. The error is non-nil only when `procStart(pid) != start` before the open or after the hash (stale or reused pid). Every other failure returns evidence with `Build == ""` and one of the fixed `Reason` strings: `claude build digest is not qualified`, `claude executable is unreadable`, `CLAUDE_CODE_EXECPATH names a different executable`, `claude build observation timed out`.
- Produces `func claudeExeIdentityLive(pid int, identity string) bool`. Linux runs `stat("/proc/<pid>/exe")` and compares only the `dev:ino` prefix of the identity. Unlinking a file updates its ctime, and an auto-update unlinks the version file, so comparing the full tuple would wrongly disqualify a running session. A running image cannot be modified in place (ETXTBSY), and its inode cannot be reused while mapped. An `exec` keeps the pid and start time but changes `dev:ino`, so this comparison catches it. Other platforms return `false`.
- Test seams: `var claudeBuildHash = func(ctx context.Context, r io.Reader) (string, error)` and `var claudeBuildHashBudget = time.Second`.
- Cache: private local state `claude-exe-digests.json`, strict v1 `{version:1, entries:[{identity, sha256, observed_at}]}`, max 16 entries, evicting the oldest `observed_at`. It is written only under `lockLocalState(ctx, "claude-exe-digests")`, and only after a complete hash whose post-hash `fstat` identity is unchanged.

- [ ] **Red:** In `claude_build_test.go`, add `TestClaudeQualifiedBuildTableIsPinned`. It asserts `claudeQualifiedBuilds` deep-equals the single Global Constraints entry, and that `claudeQualifiedBuildFor` refuses an unknown digest and refuses a tabled digest when the platform differs. In `claude_build_linux_test.go`, use the existing helper-process fixture (`TestClaudeHelperProcess`) as the observed pid and add a test helper `claudeWithQualifiedBuild(t, sha)`. Add these tests:
  - `TestClaudeBuildDigestComesFromKernelExe`: the digest equals sha256 of `os.Args[0]`. `Build` is `""` until the helper tables it, then it is the injected label.
  - `TestClaudeBuildSurvivesUnlinkedExecutable`: copy the test binary to a temp path, start the fixture from the copy, observe it, then unlink or replace the path. A fresh observation still carries the original digest, and `claudeExeIdentityLive` with the first identity stays true.
  - `TestClaudeBuildExecPathHint`: if `CLAUDE_CODE_EXECPATH` names another existing file, the reason is the EXECPATH reason and `Build` is `""`. If it names a missing file, the hint is ignored. If it names the same inode, the build is accepted.
  - `TestClaudeBuildCacheAvoidsRehash`: with `claudeBuildHash` counting calls, the second observation makes 0 hash calls. Touching the file's mtime forces exactly 1 more call.
  - `TestClaudeBuildObservationIsBounded`: a `claudeBuildHash` that blocks past `claudeBuildHashBudget = 50ms` returns the timeout reason within 200 ms and writes no cache entry.
  - `TestClaudeBuildUnreadableExeIsUnqualified`: an open seam returns EACCES, giving the unreadable reason and no error.
  - `TestClaudeBuildStalePidErrors`: a mismatched `start` returns an error.
  - `TestClaudeExeIdentityLive`: true for the live fixture and false after the fixture exits.
- [ ] **Confirm red:** Run `umask 022 && go test ./internal/cli -run '^(TestClaudeQualifiedBuild|TestClaudeBuild|TestClaudeExeIdentity)' -count=1`. Expect undefined symbols.
- [ ] **Implement:** `open("/proc/<pid>/exe", O_RDONLY|O_CLOEXEC)`, with `procStart` checked before the open and after the hash. After the hash, also require `stat("/proc/<pid>/exe")` dev:ino to equal the fd's, which catches an `exec` during the hash. Then `fstat` the fd: it must be a regular file. Look up the cache by identity, otherwise hash through the fd itself under `claudeBuildHashBudget`, then `fstat` again and require the same identity. Never re-resolve the path. Compare `CLAUDE_CODE_EXECPATH` by `stat` dev/ino against the fd. Never read `CLAUDE_CODE_VERSION`.
- [ ] **Verify:** Run the same command and expect it to pass. Then compile the other platforms with `GOOS=windows GOARCH=amd64 go test -exec=true ./internal/cli -run '^$'` and the same with `GOOS=darwin`.
- [ ] **Commit** `feat(claude): observe the build from the kernel-attested executable`.

### Task 2: Binding document v2 with build evidence and pinned hub identity

**Files:** Modify `internal/cli/claude_binding.go`, `claude_setup.go` (drop the `CLAUDE_CODE_VERSION` requirement at lines 207 and 281 and update the `errClaudeEnrollmentHost` copy), `claude_status.go` (replace `claudeVersionQualifiedBuilds`/`claudeVersionQualified` at :391–401 and `claudeAdmissionEvidence`), `claude_deliver.go` (resolve admission), `claude_deliveries.go` (resolve admission and legacy-key listing). Tests: `claude_binding_linux_test.go`, `claude_setup_test.go`, `claude_setup_linux_test.go`, `claude_status_linux_test.go`, `claude_deliveries_test.go`. Drop `CLAUDE_CODE_VERSION` from the fixtures and replace `claudeFixtureVersion` with `claudeWithQualifiedBuild`.

**Interfaces:**
- Consumes everything Task 1 produces.
- `claudeRoot` replaces `Version` with `Build string json:"build,omitempty"`, `ExeSHA256 string json:"exe_sha256,omitempty"` and `ExeIdentity string json:"exe_identity,omitempty"`. `claudeRootShape` no longer requires a version, but requires the digest to be 64-hex and the identity well formed whenever they are present.
- `claudeBindingsFile` v2 is `{version:2, bindings, opt_outs, canary}`. `Canary []claudeScope json:"canary"` holds at most 1 entry and has no writer until Task 3. A v1 document decodes with its bindings discarded (they re-register at the next hook) and its opt-outs kept. The next write emits v2.
- `func (a *app) claudeHubIdentity(profile string, p Profile) (string, error)` returns `effectivePin(profile, p)`, which must pass `hubid.DecodePubkey`. With no pin, it returns the fixed pending reason `claude registration needs a pinned hub identity; run a vecnet command for this profile once`. `claudeScopeForProfile` sets `HubIdentity` from it. `claudeScopeValid` requires a canonical `ed25519:` identity for bindings and canary entries. Opt-outs may also carry the legacy endpoint form.
- `claudeSameRecipient(a, b)` is `a.Recipient == b.Recipient && (a.HubIdentity == b.HubIdentity || a.HubEndpoint == b.HubEndpoint)`. `claudeBindingKey` keeps its formula over the pinned identity. Add `func claudeLegacyBindingKey(s claudeScope) string`, which is the same formula over `HubEndpoint`.
- `claudeAdmissionEvidence(...).VersionQualified` (JSON `version_qualified`, unchanged for TS) is true when `claudeQualifiedBuildFor(b.ExeSHA256)` succeeds with `Label == b.Build` and `claudeExeIdentityLive(b.PID, b.ExeIdentity)` holds.

- [ ] **Red:** Add these tests:
  - `TestClaudeRegistrationRecordsKernelBuild`: the row carries the fixture digest and identity. `Build` is the label when the build is tabled, and `""` with `version_qualified:false` when it is not. Registration succeeds in both cases.
  - `TestClaudeRegistrationIgnoresVersionEnv` (Review Focus 4): `CLAUDE_CODE_VERSION=2.1.322` with an untabled digest leaves `version_qualified:false`. `CLAUDE_CODE_VERSION=9.9.9` with a tabled digest leaves it true. With the variable unset, registration succeeds.
  - `TestClaudeRegistrationRenewsBuildWithoutNewGeneration`: same process and session with new evidence keep the same `Generation`, and the row is renewed.
  - `TestClaudeRegistrationHubAliasesShareOwner` (Review Focus 5): profiles `a` and `b` point at `http://127.0.0.1:P` and `http://localhost:P` with the same pin and caller. They give one binding key, and the second root is refused with the "cannot be stolen" reason.
  - `TestClaudeRegistrationNeedsPinnedHub`: refused with the fixed reason.
  - `TestClaudeBindingsV1Migrates`: v1 bindings are dropped, opt-outs still block both key forms, and the rewrite is version 2.
  - `TestClaudeAdmissionRequiresLiveExeIdentity`: a stored identity that does not match the live exe gives `version_qualified:false`.
  - `TestClaudeSetupNoVersionEnvRegisters`: in-session enrollment registers without `CLAUDE_CODE_VERSION`.
  - `TestClaudeSetupSlowDigestLeavesHookOutput` (Review Focus 2): a blocked hash under `hookBudget` keeps the user-prompt hook output byte-identical.
  - `TestClaudeDeliveriesListsLegacyKeyRows`: slice-3 claims keyed by `claudeLegacyBindingKey` still appear in `claude deliveries`, still count in `claude status` `pending_count`, and still accept `claude resolve --keep-inbox`. `--retry` on a legacy-key claim is refused, because its scope can never be admitted again.
- [ ] **Confirm red:** Run `umask 022 && go test ./internal/cli -run '^(TestClaudeRegistration|TestClaudeBindings|TestClaudeAdmission|TestClaudeSetup|TestClaudeDeliveries)' -count=1`. Expect failures.
- [ ] **Implement:** `claudeRootEvidence` calls `observeClaudeBuild` after `procStart` and before `registerClaudeRoot` takes `claude-bindings`. Do not hold the bindings lock across a hash. Under that lock, also require `claudeExeIdentityLive(root.PID, root.ExeIdentity)` when an identity is present. The `renewed` transition compares `(Build, ExeSHA256, ExeIdentity)`.
- [ ] **Verify:** Run `umask 022 && go test ./internal/cli -run '^(TestClaude|TestHook|TestLocal)' -count=1` and expect it to pass, comparing any umask-dependent failures against master. Then run the two cross-compiles from Task 1.
- [ ] **Commit** `feat(claude): qualify bindings by kernel build digest and pinned hub identity`.

### Task 3: Per-profile canary opt-in, single scope, and a truthful status mode

**Files:** Create `internal/cli/claude_canary.go` and `claude_canary_test.go`. Modify `claude_status.go` (status result, sidecar v2 reader, snapshot admission), `claude_binding.go` (canary validation), `commands.go` (usage line `claude canary enable|disable`), TS `connectors/launcher/src/delivery/claude-status.ts`, `claude-adapter.ts`, `host.ts`, `src/daemon.ts`, `src/codex/delivery-main.ts`, and tests `test/claude-adapter.test.ts`, `test/delivery-claude-status.test.ts`, `test/delivery-host.test.ts`.

**Interfaces:**
- Consumes Task 2's v2 `Canary` field and `claudeSameRecipient`.
- Go: `func (a *app) cmdClaudeCanary(args []string) error` is dispatched from `cmdClaude` as `canary`, and requires `--net P`. `func (a *app) setClaudeCanary(ctx context.Context, scope claudeScope, enabled bool) error` runs under `claude-bindings`.
  - Enable refuses with fixed copy when `scope.As != ""` or `!ids.Is(ids.Peer, scope.Caller)` ("the first canary accepts peer credentials and a single exact recipient scope only"). It also refuses when another recipient holds the record ("another recipient holds the canary; disable it there first"). It never clears an opt-out.
  - Disable is idempotent.
  - `claudeSnapshotAdmission` gains `Canary bool json:"canary"`. This flag is true only when a canary record matches the binding through `claudeSameRecipient`, `As == ""` and the caller is a `peer_`. `claudeDeliverResolveBinding` and `claudeResolveAdmission` refuse unless it is true (`the recipient has not opted into the native canary`).
  - `claudeStatusResult` gains `CanaryOptIn bool json:"canary_opt_in"`, `BuildQualified bool json:"build_qualified"` and `Build string json:"build,omitempty"`.
  - `Mode` is `canary` only when the owner is `registered` and the fresh sidecar row at the current generation has `native: "canary"`. Otherwise the mode is `legacy`/`opted_out`, with a reason such as `canary opted in; the build is not qualified`. Status never reports `native`.
  - The sidecar reader accepts `version` 1 or 2. `native` is allowed only in v2 and only with the value `"canary"`.
- TS:
  - `claudeDeliveryCanaryAllowed(env): boolean` (renamed from `claudeDeliveryCanaryEnabled`) is `env.VECNET_CLAUDE_DELIVERY_CANARY !== '0'`. It is a host kill switch, and the durable record is the gate.
  - `ClaudeSnapshotAdmission` gains `canary: boolean`, and `validAdmission` requires it.
  - The adapter returned by `createClaudeDeliveryAdapter` adds `admitted(): ReadonlyMap<string, string>` (binding key → generation).
  - `admittedRow` also requires `admission.canary` and `scope.as === undefined`. If more than one row qualifies, none is admitted and the adapter logs `claude_delivery_canary_ambiguous`.
  - `DeliveryHostOptions.auxiliary` becomes `(native: () => ReadonlyMap<string, string>) => HostedAdapter`.
  - `createClaudeStatus` takes `nativeAdmissions?: () => ReadonlyMap<string, string>` and publishes sidecar `version: 2` rows with `native: 'canary'` when the key and generation match.

- [ ] **Red (Go):** Add these tests:
  - `TestClaudeCanarySingleScope` (Review Focus 5): enabling a second recipient is refused, as is an alias of the first through another profile. The record count stays 1.
  - `TestClaudeCanaryRefusesDelegatedAndPrincipal`: `--as` is refused, as is a `prn_` caller.
  - `TestClaudeCanaryNeverClearsOptOut`: after disable then canary enable, `policy:false` and the mode stays `opted_out`.
  - `TestClaudeStatusCanaryModeNeedsHostAdmission`: opted in, live and qualified, but without sidecar `native` → mode `legacy` with the reason. With it → `canary`. With a stale generation → `legacy`.
  - `TestClaudeStatusNeverClaimsNativeReady`: update the existing test so `native` never appears as a mode.
  - `TestClaudeSnapshotAdmissionCanary`.
  - `TestClaudeDeliverAndRetryRequireCanary`: without the record, the helper refuses before connect and `resolve --retry` refuses, with the claim left unchanged.
- [ ] **Red (TS):** Add these tests:
  - `claude-adapter.test.ts` `test('only the single canary-opted peer scope is admitted')`: rows without `canary`, rows with `as`, and two canary rows all give no subscription and no transport call.
  - `test('VECNET_CLAUDE_DELIVERY_CANARY=0 constructs no adapter')`.
  - `delivery-host.test.ts` `test('sidecar marks native canary only for the admitted generation')`.
  - `delivery-claude-status.test.ts`: an old snapshot without `admission.canary` is rejected.
- [ ] **Confirm red:** Run `umask 022 && go test ./internal/cli -run '^(TestClaudeCanary|TestClaudeStatus|TestClaudeSnapshot)' -count=1` and `just ln-test connectors/launcher/test/claude-adapter.test.ts connectors/launcher/test/delivery-host.test.ts connectors/launcher/test/delivery-claude-status.test.ts`.
- [ ] **Implement:** The host constructs the delivery adapter whenever `claudeDeliveryCanaryAllowed(env)` holds, and passes `delivery.admitted` into the status factory. `daemon.ts` and `delivery-main.ts` pass the factory. The crash seam (`VECNET_CLAUDE_DELIVERY_CRASH_BEFORE_WRITE`) is unchanged.
- [ ] **Verify:** Run both suites and `just ln-typecheck`. Expect only the 4 pre-existing windows-platform typecheck errors, matching base.
- [ ] **Commit** `feat(claude): per-profile single-scope canary opt-in with canary status mode`.

### Task 4: Harden the deliver helper (no explicit endpoint, guard through write, panic is uncertain)

**Files:** Modify `internal/cli/claude_deliver.go`, `claude_deliver_linux.go`, `claude_deliver_peer_other.go`, `claude_deliver_test.go`, TS `claude-adapter.ts` (`DeliverExit`, `spawnClaudeDeliver`, the disposition mapping), and `test/claude-adapter.test.ts`.

**Interfaces:**
- Remove the `--socket`, `--session-id`, `--expect-pid`, `--expect-uid` and `--expect-start` flags. Only the binding-key mode is parsed.
- Tests drive `func (a *app) claudeDeliverAttempt(ctx context.Context, in claudeDeliverInput, frame []byte, recheck func() error) error` directly with fixture endpoints. `recheck` runs after peer verification and before the first write.
- New exit contract:
  - `0` = full write plus EOF flush (never presentation proof).
  - `3` = connect failed (zero bytes).
  - `5` = refused before any write (resolution, admission, generation, lock wait, socket type, frame size, peer identity or session-row recheck; zero bytes).
  - `4` = write failure, partial write, or a panic recovered after the lock was taken.
  - `2` = flag parsing only.
- `claudeDeliverLockWait = time.Second`. The `claude-bindings` lock is held from resolution through `CloseWrite`.
- TS: `export type DeliverExit = 0 | 3 | 4 | 5`. The transport maps a child exit of 2, any unmapped code, a signal or a timeout to `4`, and a spawn `error` to `5`. Dispositions are `0`→`released/attempted`, `3`→`refused/connect_refused_or_timeout`, `5`→`refused/refused_before_write`, `4`→`uncertain/write_failed_or_outcome_unknown`.

- [ ] **Red:** Add these tests:
  - `TestClaudeDeliverHasNoExplicitEndpointMode`: `--socket …` exits 2 with no connect, and the fake server sees 0 accepts.
  - `TestClaudeDeliverHoldsGuardThroughWrite`: a registration attempted while a slow fake server holds the write cannot commit a new generation until the helper exits. The registration's `lockLocalState` waits or times out, and the stored generation is unchanged until the helper's `CloseWrite`.
  - `TestClaudeDeliverRechecksSessionRowBeforeWrite`: the session row changes to a new id between resolve and write, so the helper exits 5 and the server reads 0 bytes.
  - `TestClaudeDeliverLockWaitIsBounded`: a held lock gives exit 5 in ≤1.5 s.
  - `TestClaudeDeliverPanicIsUncertain`: a panicking `recheck` gives exit 4.
  - `TestClaudeDeliverExitContract` covers all five codes.
  - TS `test('helper exit 2, signals and unmapped codes are uncertain, never refused')`.
  - TS `test('exit 5 is a definite pre-write refusal')`.
- [ ] **Confirm red:** Run `umask 022 && go test ./internal/cli -run '^TestClaudeDeliver' -count=1` and `just ln-test connectors/launcher/test/claude-adapter.test.ts`.
- [ ] **Implement:**
  - A `recover` in `cmdClaudeDeliver` maps every recovered panic to 4.
  - An unrecoverable runtime fatal still exits 2, which the TS side now treats as uncertain. A Go runtime panic exits with status 2, and that is the residual this fixes.
  - In `exitClaudeDeliver`, a `claudeDeliverError` keeps its code. A `usageError` raised after flag parsing maps to 5. Set `a.claudeDeliverParsed = true` once `parseFlags` returns, so a flag-parse error stays 2. Any other error maps to 4.
  - The recheck is `claudeSessionRecordValid` on the resolved root, plus the canary, opt-out and generation checks, all still under the lock.
- [ ] **Verify:** Run both suites plus `^(TestClaudeDeliveries|TestClaudeStatus)`.
- [ ] **Commit** `fix(claude): gate the deliver helper to registry mode and fence through the write`.

### Task 5: Pin adapter children to the binding's config and bound the first-admission backlog

**Files:** Modify `internal/cli/claude_status.go` (snapshot scope and admission), `claude_binding.go`, TS `claude-status.ts` (`validScope`, `validAdmission`), `claude-adapter.ts` (`spawnClaudeInbox`, `claudeInboxArgs`, `subscribeWatches`, `stageScope`), and tests.

**Interfaces:**
- `claudeSnapshotScope` gains `Config string json:"config"` (the binding's canonical config path) and `HubIdentity string json:"hub_identity"`. `claudeSnapshotAdmission` gains `Witness bool json:"witness"`. Add `func (a *app) claudeScopeWitnessAt(scope claudeScope) error`, which is `claudeScopeWitness` against `scope.ConfigPath` rather than the env-derived config. Neither field reaches the sidecar.
- TS: `ClaudeSnapshotScope` gains `config: string` (absolute, ≤4096, no NUL) and `hub_identity`. `admittedRow` also requires `admission.witness`. The inbox, watch and ack children run with `env.VECNET_CONFIG = row.scope.config`.
- TS: `ClaudeCandidatePage` becomes `{ events; nextCursor?: string }`, parsed from `next_cursor`. `claudeInboxArgs(c, 'head')` emits `--since head`.
- **Ruling: new arrivals only, no tail.** When a scope has no progress record, a different generation or an empty cursor, `stageScope` reads `--since head`. It persists `{cursor: nextCursor, generation}`, logs `claude_delivery_scope_baselined`, and stages nothing that pass. Earlier unacked deliveries stay on the legacy hook and inbox path.

- [ ] **Red:** Add these tests:
  - `TestClaudeSnapshotScopeCarriesBindingConfig`.
  - `TestClaudeSnapshotWitnessFailsAfterConfigReplaced`: the profile is removed or its caller changed at the stored path, so `witness:false`.
  - TS `test('children run against the binding config, not the daemon default')`: a daemon env `VECNET_CONFIG=/other`, a row config of `/bind/config.toml`, and every spawned inbox/watch env carries `/bind/config.toml`.
  - TS `test('first admission baselines at head and promotes only later arrivals')`: a 40-event unacked backlog before admission gives 0 claims and 0 transport calls. One wake after the baseline gives exactly 1 claim.
  - TS `test('generation change rebaselines instead of replaying the window')`.
  - TS `test('failed baseline read stages nothing and retries')`.
- [ ] **Confirm red, Implement, Verify:** Run `umask 022 && go test ./internal/cli -run '^(TestClaudeSnapshot|TestClaudeStatus)' -count=1` and `just ln-test connectors/launcher/test/claude-adapter.test.ts connectors/launcher/test/delivery-claude-status.test.ts`. Both pass.
- [ ] **Commit** `fix(claude): pin canary children to the binding config and promote only new arrivals`.

### Task 6: Claim ledger hygiene (ack observation, tombstone retirement, disable drain, linear passes)

**Files:** Modify `connectors/launcher/src/delivery/claude-adapter.ts` and `test/claude-adapter.test.ts`. Also add `internal/cli/claude_deliveries_test.go` coverage that `hub_ack_observed` renders.

**Interfaces:**
- `ClaudeDeliveryAdapterDeps` gains `fetchAckWindow?: (c: ClaudeDeliveryScopeCoordinates) => Promise<{ deliveryIds: Set<string>; complete: boolean } | undefined>`. The default is `inbox --json --limit 200 --no-display-proof` with no `--since`, so it reads from the hub's acked cursor. `complete` means `events.length < 200`.
- `ACK_OBSERVE_MS = 60_000` per scope. Observation runs only for an admitted scope that has unretired claims.
- **First `hubAckObserved` writer.** When the window is complete, every claim of that scope whose `deliveryId` is absent gets `hubAckObserved = true` through `saveIfCurrent`. An incomplete window changes nothing.
- **Retirement.** In its own scope's lane only, delete claims with `hubAckObserved === true` and state ∈ {`released`, `refused`, `held`, `expired`}. `uncertain` claims with the flag stay until an operator resolves them.
- **Disable drain.** Every pass runs a fence sweep in a dedicated lane, even with zero admitted scopes. For claims whose scope is not admitted and that are not in flight:
  - `reserved` → `state: 'released', disposition: 'released', dispositionReason: 'fenced_scope_not_admitted'` (replacing `expired`/`scope_absent`).
  - `attempting` → `uncertain`/`restart_may_have_written`.
- **Linear passes.** One `claimRows()` per pass builds `ClaimIndex = { byScope: Map<string, Array<{claim, raw}>>; ids: Set<string>; pending(scope): number }`. It is updated in memory on every save or delete. `pending()` and `recoverScope` no longer rescan the kv.

- [ ] **Red:** Add these tests:
  - `test('a complete acked window marks and then retires settled tombstones of its own scope only')`: a foreign-scope acked row survives.
  - `test('an incomplete window marks nothing')`.
  - `test('uncertain claims are marked but never retired')`.
  - `test('ack observation never posts ack or receipts')`: the fake records argv and no `--ack`/`ack` appears.
  - `test('disabling the lone admitted scope drains reserved rows')`: zero admitted rows release every reserved claim and turn attempting claims into uncertain.
  - `test('a pass is linear in claims')`: with a counting kv, 200 claims and 32 staged events, `kv.get` calls ≤ 200 + 4·32.
- [ ] **Confirm red, Implement, Verify:** Run `just ln-test connectors/launcher/test/claude-adapter.test.ts connectors/launcher/test/delivery-host.test.ts`, `just ln-typecheck` and `umask 022 && go test ./internal/cli -run '^TestClaudeDeliveries' -count=1`.
- [ ] **Commit** `fix(claude): observe hub acks to retire tombstones and drain disabled scopes`.

### Task 7: Documentation

**Files:** Modify `internal/cli/commands.go:65` (help line), `docs/PROTOCOL.md` (§13 command list near line 1250 and the inbox-output item near line 1267), `docs/connectors/claude.md`, and a usage test in `internal/cli/cli_test.go`.

**Interfaces:** Consumes the final behavior of Tasks 1–6. No code interfaces.

- [ ] **Red:** Add `TestUsageListsInboxNoDisplayProof`. It asserts the global usage contains `inbox [--since CURSOR] [--ack] [--limit N] [--include-sender-chain] [--no-display-proof]` and `claude canary enable|disable`.
- [ ] **Implement:**
  - Help line.
  - PROTOCOL: `--no-display-proof` means a machine read that records no hook display state and never acks.
  - `claude.md`:
    - Registration needs no `CLAUDE_CODE_VERSION`. The build is qualified by the kernel-attested executable digest against the pinned table, with the cache file named and `CLAUDE_CODE_EXECPATH` used as a hint only.
    - Hub identity is the pinned key.
    - `claude canary enable|disable`, single peer scope, and the `VECNET_CLAUDE_DELIVERY_CANARY=0` kill switch.
    - Status `mode: canary`, `canary_opt_in`, `build_qualified`, `build`.
    - The helper is registry-only and its exit contract.
    - New arrivals only after admission.
    - Ack observation and tombstone retirement semantics, including that `hub_ack_observed` also covers an event the hub expired unacked.
    - Disable drain.
    - Remove every statement that real builds need an env provision.
- [ ] **Verify:** Run `umask 022 && go test ./internal/cli -run '^TestUsage' -count=1` and `git diff --check`. Also run `grep -n CLAUDE_CODE_VERSION docs/connectors/claude.md` and expect only the sentence saying it is ignored.
- [ ] **Commit** `docs(claude): document the real-host canary and inbox --no-display-proof`.

### Task 8: Acceptance — provision-free disposable-hub canary with negative controls

**Files:** Modify `scripts/e2e/claude-delivery-e2e.py`, `scripts/e2e/test_claude_delivery_e2e.py` and, if needed, `scripts/e2e/build-e2e-page.py`. Evidence goes under the run dir. Publish to `~/served-html/vecnet/claude-delivery-e2e/phase2/` with the existing builder.

**Interfaces:** Consumes Tasks 1–7 through the CLI only: `claude canary enable|disable`, `claude status --json` (`mode`, `build_qualified`, `canary_opt_in`), `claude deliveries --json`, and the daemon log events `claude_delivery_scope_baselined` and `claude_delivery_scope_admitted`.

- [ ] **Harness, provision-free:**
  - Delete `CLAUDE_CODE_VERSION` from the launch env and the enrol script (`:718`, `:727`, `:851`).
  - Delete `VECNET_CLAUDE_DELIVERY_CANARY=1` from the daemon env (`:781`, `:796`). The crash phase keeps only `VECNET_CLAUDE_DELIVERY_CRASH_BEFORE_WRITE=1`.
  - Replace the meta `claude_code_version_provision`/`canary_opt_in` entries with `"provisions": []` and `"canary_opt_in": "vecnet --net claude-rx claude canary enable"`.
- [ ] **Qualification route:** The harness runs the real 2.1.322 binary, whose production table entry qualifies it. No test-only injection exists, or could exist, outside `_test.go`.
- [ ] **Preflight:** sha256 the resolved `claude` binary and fail unless it equals the table digest. Resolve `~/.local/share/claude/versions/2.1.318` as the unknown-build control binary (read-only, launched only under the disposable `HOME`/`CLAUDE_CONFIG_DIR`). Fail if it is absent or its digest is tabled. After launch, read `/proc/<disposable claude pid>/environ` and fail if `CLAUDE_CODE_VERSION` is present.
- [ ] **Positive path:**
  - Run `claude canary enable` for `claude-rx` before the daemon starts.
  - Wait for `claude_delivery_scope_baselined` before any send.
  - `claude status --json` reports `mode:"canary"`, `build_qualified:true`, `build:"2.1.322"`.
  - The existing ordered ID-matched lifecycle, pane pointer, one fetch, zero acks, and hold, crash and cap-129 controls still pass. Sends happen after the baseline.
- [ ] **Negative controls (new):**
  - (a) **Canary off:** `claude canary disable` for `claude-rx`, then one wake. Expect no new claim, no pointer in any later frame, and status `mode:"legacy"`. Reserved rows are drained to `fenced_scope_not_admitted`.
  - (b) **Unknown digest:** a second disposable profile `claude-rx2` runs a session from the 2.1.318 binary in its own window on the private tmux server, with its own config dir and socket, enrolled in-session. Run this control after (a), so the single-scope rule lets rx2 hold the canary. Then `claude canary enable` for `claude-rx2` and one wake. Expect status `build_qualified:false`, `mode:"legacy"` with the not-qualified reason, zero claims under rx2's key, and no pointer in rx2's pane. The control **fails**, and does not pass vacuously, if rx2 has no stored binding, because then the build check was never reached.
  - (c) **Pre-admission backlog:** a wake sent before the baseline is never pointered.
- [ ] **Harness unit tests:** `test_claude_delivery_e2e.py` gains `test_meta_lists_no_provisions`, `test_canary_off_control_requires_no_claim_and_legacy_mode`, and `test_unknown_build_control_requires_unqualified_status`. All existing tamper tests still pass: `python3 -I -m unittest scripts/e2e/test_claude_delivery_e2e.py`.
- [ ] **Run once:** `python3 -I scripts/e2e/claude-delivery-e2e.py`. Expect exit 0, all controls green, teardown verified (private server gone, no `vecnet-test-*` strays), and raw state disposed.
- [ ] **Publish:** Build the page and validate it with text-only checks (checker stdout, `curl`, sha256). Use an image-capable model for any visual check.
- [ ] **Commit** `test(claude): provision-free real-build canary acceptance with negative controls` (harness and evidence index only, never raw run state).

## Not in this plan

- Default-on native `wake` (slice 4) and its gates: writer takeover/rollback with real old/new binaries, actual-request capture for busy placement, and a demonstrated regular window-clearing path at scale.
- Named content, `wake_idle`, native reply egress, macOS/WSL/Windows, and principal-narrowed canary scopes.
- Qualifying any build other than 2.1.322. Each new table entry needs its own qualification run and a reviewed commit.
- Rewriting the served test deck at `~/served-html/vecnet/claude-delivery-testing/deck.html`. **Follow-up:** its `CLAUDE_CODE_VERSION=2.1.322 claude` provision slide (line ~38–40) is obsolete after Task 2. Replace it with `vecnet --net P claude canary enable` and the status `mode: canary` check.
- Malformed `hub_notice` payload behavior, which is still a known limit from the slice-3 review.
