# Automatic enrollment and project maintenance acceptance

Date: 2026-11-15 (Australia/Sydney). Implements [#71](https://github.com/ultradyn/vecnet/issues/71); supported host boundaries remain in [the connector contract](../connectors/codex-appserver.md).

## Disposable live result

The final automatic enrollment harness completed successfully against Codex app-server **0.167.2**, Node **22.23.3**, and three real model root conversations: two in one checkout and an unbound neighboring worktree. It used a private HOME/CODEX_HOME, loopback demo hub, temporary Git worktrees and freshly invited recipients. No real user service, profile, ownership, message or recovery state was changed.

- A reached fresh exact-owner readiness through the real `enrol` command and embedded standalone runtime without a separate setup/bind/install command.
- B's enrollment process was killed at the capability probe after its credential/intent commit. The first `repair` from the unbound neighboring root reached ready for the saved original root, without the invitation token or a handoff.
- New wake deliveries produced exact owner-derived user-message receipts and completed model responses containing each intended delivery ID and random canary. The injected peer instruction was rejected. The other root and unbound neighbor contained neither the wrong receipt nor the wrong model canary within the observed histories and settling interval.
- Ordinary use registered the neighboring worktree and adopted its recognized stale hooks. Actual repair migrated both projects' hooks, preserved a custom handler/custom JSON and retained exact binding owners/generations.
- Cleanup confirmed runtime stop, archival of all three fixture roots, demo shutdown and private daemon stop. It also retired the one private Codex package PID updater left by `daemon stop`; a subsequent process audit found no process with the fixture HOME. Evidence reported `complete: true`.

| Root | Delivery | Turn | Owner receipt |
|---|---|---|---|
| A | `dly_01M7DEX7ERQ7S235WG4KH23KYQ` | `01a1daee-a000-7010-879b-b17259318799` | `vn.9f9c37be003b93c7e22b40d7` |
| B | `dly_01M7DEXAQ1E8M1P6GGRDE95HQH` | `01a1daee-ad07-72f0-94f1-603132e09237` | `vn.017996380bfb182ee4e7223f` |

Acceptance CLI version: `issue71-acceptance`; SHA-256 `61d1eda92c43e9ebc29f258f194d94359209e94ce16de518dddc15910db4de61`. Raw evidence/logs remain private at `/tmp/vecnet-auto-ClcUTt/`; provider configuration, credentials and canary values are excluded from this receipt.

**Service boundary:** this run used a private fake-systemctl adapter that executes the generated unit's actual embedded Node/CLI command. It verifies real enrollment, runtime staging/startup, daemon transport, delivery and model contexts. It does **not** verify real user-systemd activation, desktop/embedded bridges, interactive rendering or isolation outside the observed histories. The adapter never falls back to the real user's systemctl.

## Live-driven timing correction

The first interrupted-enrollment run returned pending at 12 seconds with `unverified_thread`. A diagnostic run proved that the same original owner became ready afterward without additional setup. The connector reloads/publishes every 10 seconds and first publishes newly launched engines before their asynchronous verification finishes; ready may appear on the second heartbeat. The readiness window now covers 25 seconds, inside a one-minute reconciliation bound that also accommodates runtime activation's own 30-second cap. Caller cancellation and all identity/readiness checks remain enforced.

`TestAutomaticEnrollmentWaitsForNextConnectorStatus` delays real binding-matched status publication by 18 seconds. It failed under the original wait, then passed after this correction. The final live harness still requires its first repair invocation to succeed; it does not count an automatic second repair as acceptance.

The harness also waits for the bootstrap turn's completion notification before querying its persisted history, avoiding Codex's empty-rollout window. RPC error details stay in private fixture logs.

## Repeat the disposable scenario

Build `cmd/vecnet` and `internal/opseed/demo` on a build box, then supply their paths plus a minimal private provider-only Codex config (no project/hooks/user state) to:

```sh
CODEX_ISOLATION_CONFIG=/private/provider-config.toml \
CODEX_BIN=/path/to/codex \
VECNET_BIN=/path/to/vecnet \
VECNET_DEMO_BIN=/path/to/vecnet-demo \
node --disable-warning=ExperimentalWarning scripts/automatic-session-smoke.mjs --run
```

Ordinary use after an installed update schedules registered maintenance. Inspect with `vecnet projects list --json` and `vecnet repair --status --json`; explicitly run all registered repairs with `vecnet repair --json`. These commands do not transfer existing recipients or renew expired bindings.

## Verification and review

All build/test commands below ran through `just bb` on x-tuf with `BB_JOBS=2`; the disposable model scenario ran locally against its private fixture.

| Gate | Result |
|---|---|
| `CGO_ENABLED=0 go test -p 2 ./...` | All packages passed; 855.6s wall time, before the final readiness-only CLI timing correction. |
| Final full `CGO_ENABLED=0 go test -p 2 ./internal/cli` | Passed, 176.390s, including the timing correction. |
| `just ln-check` (launcher + hub-client) | Typecheck and all 1,887 tests passed, 0 skipped/failures; 193.017s test duration. |
| Final CLI race selection (`TestLocal`, managed hook repair, automatic enrollment/enrollment, all-project repair) | Passed, 48.352s. |
| Managed runtime race selection | Passed, 43.514s before later CLI/harness-only changes. |
| Packed npm x64/arm64 build and `just npm-check` | 6/6 passed, including actual package install/message exchange and observed native version. |
| Automatic-session/isolation harness tests | 21/21 passed, including exact private updater cleanup and foreign-HOME preservation. |
| Isolated versioned Go module installation | Both distributed commands installed; embedded assets available outside a checkout. |
| Portable full CLI build | Darwin arm64 and Windows amd64 passed; managed activation remains an explicit unsupported capability there. |
| Protocol/schema/blob examples | Passed, including expected negative fixtures. |

The independent whole-branch standards/spec review found no remaining material issues. Scoped follow-ups approved the live-driven readiness correction, fixture-helper cleanup, evidence receipt and test-only JSONL framing repair. The trace regression demonstrated a red partial-record read, then green parsing of complete records while malformed completed records still fail. It changes no production connector code or test timeouts.

## PR72 integration review corrections

The merge review tightened startup maintenance to an explicit set of project/network commands. Local guide, profile, diagnostic and audit inspection, preference reads/previews, unknown commands and internal delivery children do not observe projects or schedule saved enrollment work. Preference previews use the same flag grammar as the preference command, including flag termination and invalid boolean values.

A legacy `vecnet-launcher.service` without managed standalone delivery now schedules maintenance and reports an actionable pending installer upgrade rather than reporting a completed software upgrade after only repairing hooks. Its existing unit/configuration is preserved. When standalone delivery is installed alongside a leftover launcher unit, repair upgrades the standalone runtime; that runtime's live service-conflict checks still govern activation. Legacy launcher-only upgrades require the launcher installer and service restart described in the pending result.

Regression coverage exercises saved pending enrollment during each inspection, launcher-only pending state and preserved unit bytes/applied version, and standalone repair with a leftover launcher unit. Both preview and standalone-priority regressions were observed failing before the correction. The integration gate uses x-boss because x-tuf was unreachable, with its temporary directory canonicalized to avoid the build host's `/home` to `/work` alias in filesystem fixtures.

Final merge correction gate: on the quieter x-tall host with private tmpfs temporary storage, both state-persistence failures from loaded x-boss passed three consecutive runs (0.345s total); the full CLI suite passed (103.964s), managed runtime passed (1.250s), and the focused maintenance/inspection/launcher/startup race suite passed (1.436s). Production timeout bounds are unchanged. The scoped independent review approved both corrections. The earlier x-boss full attempt failed only the two two-second registry/persistence operations under filesystem load; its managed runtime suite passed (38.199s).
