# Automatic enrollment, project inventory and upgrade repair Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development and the per-task review loop in ultra-code-workflows. The operator authorized implementation of the complete issue; no further design approval is required.

**Goal:** Implement issue #71: enrollment configures the exact supported Codex conversation, local project inventory records use and managed artifacts, and first use after an upgrade reconciles all registered installations.

**Architecture:** One idempotent local reconciliation path serves enrollment, explicit repair and upgrade maintenance. The private project inventory records paths and successful migrations separately from observed binaries; the existing Codex authority alone grants delivery ownership. Connector TypeScript sources are embedded into the CLI so npm packages and direct executables carry matching software for managed standalone activation.

**Tech Stack:** Go CLI and embedded assets, Node >=22.18 with SQLite and TypeScript, Linux user systemd, existing app-server connector.

**Spec:** GitHub issue #71 (snapshot `.superpowers/sdd/automatic-session-setup/issue-71.md`) and `docs/research/2026-11-13-codex-hooks-multiple-sessions.md`, follow-up requirements.

## Global constraints

- Preserve exact-thread ownership, compare-and-swap handoff, expiry, recovery and explicit ack. Never assign an existing peer based on directory or upgrade.
- Use HOME-based private local inventory, no credentials/message bodies. Preserve separate worktrees; support non-Git directories and explicit hook settings paths.
- Bounded automatic startup work; pending work is durable/retryable. Read-only help/status/inspection, hooks, watches and internal connector commands must not recursively set up or change ownership.
- Inventory-only ordinary use does not install hooks. Existing owned integration files may be adopted/migrated; custom content and trust stay intact.
- Tests/builds run through `BB_HOST=x-tuf BB_JOBS=2 just bb`; private disposable HOME/config/server fixtures only. No real peer messages or state resets.

## Task 1: Private project inventory (independent)

**Files:** `internal/cli/localregistry.go`, `localregistry_test.go` and platform helpers if needed. Do not edit commands.go, hookinstall.go, codexsetup.go.

**Produces:** `localProject`, `localArtifact`, `(*app).observeLocalProject(version string, now time.Time) (localProject,error)`, `(*app).localProjects() ([]localProject,error)`, `(*app).updateLocalProject(root string, fn func(*localProject) error) error`, `(*app).registerLocalArtifact(path,kind,version string,now time.Time) error`, `(*app).cmdProjects(args []string) error`.

- [x] Write failing private inventory tests for canonical aliases/worktrees/non-Git, observed versus applied versions, atomic concurrent updates, unknown JSON versions/unsafe paths, inspect/forget.
- [x] Implement private locking/atomic writes, directory identity, metadata and managed artifacts with hash/schema/version. Inspection must not create files; forgetting must not touch integration/ownership.
- [x] Run meaningful red/green focused tests and write task report. Independent review before integration completion.

## Task 2: Self-contained managed delivery runtime (independent)

**Files:** `connectors/assets.go`, `internal/localruntime/*.go` and tests. Do not edit CLI command integration or npm files.

**Produces:** `localruntime.Ensure(ctx context.Context, options Options) (Result,error)` with `Options{Home,Binary,Node,Version string, Getenv func(string)string, Run func(context.Context,string,...string)([]byte,error)}`. Result contains `Version,Release,Unit string; Changed bool`. Embed connector/hub-client source and package manifests in the Go executable; stage content-addressed assets and private CLI. Expose a read-only managed installation/version check for maintenance.

- [x] Write failing tests with injected process execution/private HOME: capability probe before writes, compatible unit staging, activation/restart, repeated ensure no needless restart, incompatible/custom unit refusal, failed restart remains retryable, preservation of authority/ledger.
- [x] Implement managed private installation, node capability checks, systemd escaping and sanitized errors, interprocess locking and atomic durable manifest. Query existing launcher/legacy services before duplicate activation; report conflicts rather than running two engines.
- [x] Verify startup imports work without node_modules, build portable Go stubs for unsupported hosts, report paths and red/green evidence. Independent review.

## Task 3: Reconciliation, enrollment and upgrade lifecycle

**Files:** `internal/cli/localsetup*.go`, `commands.go`, `codexsetup.go`, `hookinstall.go` and related regression tests; `scripts/npm/build.mjs` for build metadata when needed.

**Consumes:** Task 1 inventory and Task 2 runtime. Uses existing cmdCodexSetup/admission/status and migration rather than duplicating them.

- [x] Add failing enrollment integration and all-project upgrade tests. Enroll saves credential then durably records exact invoking root/profile/config/home; release config lock before setup. Retry saved pending setup without redeeming invitation.
- [x] Implement `projects list/forget`, `repair [--json]` and bounded first-run maintenance; register ordinary use, adopt only recognized legacy hooks, record user-level/explicit targets, migrate registered artifacts across projects using existing guarded installer. Keep no-op/help/status/internal child paths read-only.
- [x] Automatic enrollment ensures supported runtime, binds only intended new recipient and migrates hooks, then waits boundedly for fresh matching readiness. Return structured partial state on host/legacy/ownership conflict, not false ready or lost credentials.
- [x] Upgrade runtime and files as one retryable sequence. Store applied digest only after successful work; missing/inaccessible paths defer. Retire verified stopped/empty legacy sources with backups and no loss of original recovery; fail closed if cannot prove retirement.
- [x] Test concurrent/partial/repeated runs, unsupported hosts, active legacy conflicts and expired bindings. Review task and fix findings.

## Task 4: Documentation, end-to-end and release-ready integration

**Files:** agent/invitation guide, help goldens, `docs/connectors/{codex,codex-appserver}.md`, npm README/package verification, protocol, changelog, plan receipts, standalone installer consistency if needed.

- [x] Update normal onboarding to one enrollment action and maintenance status/repair, preserve explicit advanced setup and required host trust interactions.
- [x] Run affected/full CLI, registry/runtime race, launcher/hub-client tests/typecheck, npm package verification, portable builds and protocol checks. Exercise automatic enrollment/maintenance through disposable services/hubs and supported exact-thread delivery canaries where available.
- [ ] Independent whole-branch standards/spec review; resolve material findings, repeat affected checks, commit and publish a reviewable PR linked to #71.

## Review focus

Pin actual automatic entrypoints, direct binary/npm asset parity, interruption after credential save, version skew and inaccessible project retries, hostile/symlink inventory paths, simultaneous same-checkout enrollments, no recursive connector maintenance, and no upgrade ownership changes.

## Self-review notes

Inventory, runtime, integration and user docs each have an owner. Only integration edits command dispatch and existing setup. Tasks 1/2 are independent; Task 3 consumes their exact signatures. Linux managed service is the supported activation surface; unavailable host bridges remain explicit per #68. The spec's readiness requirement is a fresh owner status, never service activation alone.

## Completion evidence

[Acceptance receipt](../research/2026-11-15-automatic-session-setup-runtime.md) records the full Go/final CLI/race, launcher (1,887 tests), package, module distribution, portable builds and protocol gates, independent review, and successful disposable model/repair scenario with complete cleanup. Managed activation retains the documented Linux/default-config capability boundary; the live service adapter is explicitly distinguished from real user-systemd activation.
