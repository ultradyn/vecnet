# Launcher setup fix2 — LAST-DONE

M1 is addressed: `launcher setup --acp-arg VALUE` preserves its next operand during global flag extraction. Global-looking child values no longer become vecnet help, JSON, profile or identity flags. The production change is restricted to the `launcher setup` command prefix; genuine globals and the standalone `--` terminator retain their behavior.

## Source and scope

- Worktree: `/home/xertrov/src/vecnet-launcher-live`, branch `feat/launcher-codex-live`.
- Starting HEAD: `9b56fff8fab6fca59a44e8cde90e67fbf5de7009`.
- Validated source/test/guidance commit: `2597f46cefbdf3a1b0894d71134892e4dcb02f2c`, `fix(cli): preserve opaque launcher ACP arguments`, trailer `Plan-Task: launcher-setup-fix2`.
- Source commit paths: `internal/cli/commands.go`, `internal/cli/launcher_acp_test.go`, `docs/connectors/launcher.md`. This report is the only path in the subsequent report-LAST commit.
- Snapshot trust, npm/package closure, delivery, native backend and UI production files were not edited by this worker. Other workers' dirty/untracked paths were excluded from both commits.

Tests exercise real `RunContext`, a synthetic private HOME/profile, a private repository and an installed fake ACP executable. The 27 new cases check separate and equals forms of `-h`, `--help`, `--json`, `--net=other`, `--as=other`, and literal `--`; ordered repeated/spaced arguments; actual config and selected-profile credential writes; unchanged enrolled profiles; genuine global flag placement/help/identity/profile controls; missing operands; terminators; and preservation of other commands' help handling. Exit 0 or a preview alone does not satisfy the write assertions.

## Actual chronology and commands

The controller granted the sole CX test slot after UI drained. All commands below ran sequentially with `GOMAXPROCS=1` and Go package concurrency 1. No production edit preceded behavioral RED.

1. The first invocation used `-p1`; Go exited 1 in 0.004 seconds with `no Go files` in the repository root. This was an invocation failure, **not behavioral RED**. Its unmodified log and result are preserved as `red-invocation.log` and `red-invocation-result.json`.
2. Corrected behavioral RED, against the drafted tests and unchanged production parser:

   ```sh
   GOMAXPROCS=1 go test -v -p 1 ./internal/cli -run '^TestLauncherSetupACP(Opaque|Global)' -count=1
   ```

   Go exit **1**, elapsed 6.841 seconds. Four top-level tests: 2 failed, 2 passed. Subcases: **10 failed, 17 passed**. Separate help operands returned usage without a write; separate JSON/profile/identity operands were extracted; JSON placement cases previewed despite the requested write. Equals forms and control/scope cases passed. Raw log: `red-focused.log`.
3. Added the small opaque-operand seam in `parseGlobal`, updated exact-argument documentation, ran gofmt on the two owned Go files and checked the scoped diff. No test assertions were weakened; the drafted test file's SHA256 is identical at RED and GREEN.
4. Focused GREEN:

   ```sh
   GOMAXPROCS=1 go test -v -p 1 ./internal/cli -run '^TestLauncher' -count=1
   ```

   Go exit **0**, elapsed 6.480 seconds. **15 top-level tests passed**, 44 subcases passed, 1 existing foreign-owner fixture skipped because privileges were unavailable. All 27 new fix2 subcases passed. Raw log: `green-focused.log`.
5. Full CLI GREEN, on the same frozen source bytes:

   ```sh
   GOMAXPROCS=1 go test -v -p 1 ./internal/cli -count=1
   ```

   Go exit **0**, elapsed 177.171 seconds (package test time 176.524 seconds). **536 top-level tests passed**, 1,019 subcases passed, the same 1 existing fixture skip, no failures. Raw log: `green-full-cli.log`.

Python capture wrappers recorded the actual Go subprocess return codes in `*-result.json` and themselves exited 0 after collecting evidence, including for RED. No build, npm, typecheck, live provider, deployment or real service operation ran. Fixture tests used their private/fake managers. No further tests were started after the full CLI run.

## Hashes and evidence

Local raw evidence directory: `/work/package-cache/xertrov/vecnet-launcher-setup-fix2/`. The controller owns copying/mirroring it. It contains the logs, per-command actual exits/timings/hashes, RED source metadata, complete tracked-file hash manifests before/after GREEN, owned-file hashes, the source commit patch and drain observation. A checksum inventory is recorded as `evidence.sha256`.

SHA256 of sorted tracked-file checksum inventories (2,077 tracked regular files, each line `file_sha256  relative_path`):

| Observation | Inventory SHA256 |
| --- | --- |
| RED, drafted tests with unchanged production | `2dccd27203b578c6342349a544c5bb162bca81c29c1f9672038d977745fc651d` |
| GREEN before focused/full validation | `65ebc229ee58b78ec0bad65aae59b7f7bb92107ce6ab94f80eedf22d9e455197` |
| GREEN after full CLI validation | `65ebc229ee58b78ec0bad65aae59b7f7bb92107ce6ab94f80eedf22d9e455197` |

These are observations of the full tracked worktree, including other workers' existing tracked edits, and exclude untracked worker state/receipts. They are not a claim that the worktree was clean. The new report was written after validation. Owned source bytes were rechecked against the source commit after committing:

| Path | Validated file SHA256 |
| --- | --- |
| `internal/cli/commands.go` | `2b50d0920362de84d8dade36e61f25ee23614469b8792aae9da598824ef6cbe5` |
| `internal/cli/launcher_acp_test.go` | `91552a091bff66f713765c1cf0f653607261421b26b693a1d65ea4b27529a8a6` |
| `docs/connectors/launcher.md` | `cd7b74279fcdfe1ca56aa8bf998b63323024cd6977af6cee77b051832a14fadb` |

| Raw log | SHA256 |
| --- | --- |
| `red-invocation.log` | `43a795e8ecaa87a55bf8e3c4fc3c4bfb02a8d2d91cd1fe1097223984db956eb3` |
| `red-focused.log` | `0bcc69516e81edda551cc30413d44cf2971b367c409fdb5d6d3aedd3d34add64` |
| `green-focused.log` | `2fb99de62d8cae436b505e00c1bb932631a0de6e9bb9600795f99f8273f6eb02` |
| `green-full-cli.log` | `5e9238125696c69cf6b8fd024ae6b35341f11e5cbe4d5f0ad4d12364bf3f7268` |

## STOP / drain / remaining integration gates

Producer test work is **STOPPED**, test/compiler children are **DRAINED**. A post-suite process inventory found no `go`, `compile`, `link`, `asm`, `vet` or `cli.test` processes. The exclusive test slot was explicitly **RELEASED** to the controller before report preparation. Only scoped Git commits, report/evidence preparation and authorized GitHub branch push remained afterward.

This report is LAST after the source commit and validation; its commit identity and push result are delivered separately to avoid a self-referential hash. No further production edits or execution gates are implied. Independent scoped rereview, final combined integration/package gates and any live acceptance/deployment remain controller-owned.
