# Codex delivery isolation runtime evidence

Issue: [#68](https://github.com/ultradyn/vecnet/issues/68). Run on x-boss, 2026-11-12.

## Method

The coordinator ran `scripts/codex-isolation-smoke.mjs` against fresh private HOME,
CODEX_HOME, XDG directories, an isolated demo hub, and three new root conversations
in one working directory. No existing session, binding, delivery state or hook file was modified.
The minimal provider configuration used the existing configured Responses route
with `ocx/gpt-6-astra`, read-only sandbox and never-ask approval policy. Credentials
were inherited privately, not written to this report.

The fixture creates conversations through the private daemon control socket.
These are daemon app-server roots, not an interactive TUI rendering test.
Their `source` was `vscode` and `parentThreadId` was null; source is not a UI label.

## Codex 0.167.0

Installed in a disposable npm prefix. CLI and managed daemon both reported 0.167.0.
The full initial fixture exited successfully, archived its threads and stopped its daemon.

- three independent root conversations in one checkout.
- generic setup and recipient alias exclusion.
- authenticated readiness and single connector.
- parent-id-only and neighboring lifecycle hooks emit no context.
- idle automatic wake with isolated model-context canaries.
- hold never wakes and acknowledgement remains explicit.
- busy urgent steering and deferred wake_idle.
- connector restart preserves accepted delivery deduplication.

Exact idle receipt and model-context evidence:

| Recipient | Root thread | Turn | Delivery |
|---|---|---|---|
| A | `01a1cd5c-e46c-7da1-a1aa-8dd9392ba6f7` | `01a1cd5d-3572-7c60-9ee9-d3c5c064b3b7` | `dly_01M76NTCTT94SKH0VP15AF345R` |
| B | `01a1cd5c-f28e-7ee0-92ce-1f68f4aa6056` | `01a1cd5d-3580-78d1-9049-ab28d008140a` | `dly_01M76NTCV05R3JE8EDT5YAKK5P` |

Busy steering retained turn `01a1cd5d-5d12-7c33-8251-d762acd961be` for delivery `dly_01M76NTYPP407RRGA5R3ZXBM9M`.
Deferred `wake_idle` reached new turn `01a1cd5d-b922-78f0-a89c-49f9470a4df8` as `dly_01M76NTWQYD8TCCRJET2F3C0MC`.

The model returned each delivery ID and its private canary, ignored an instruction
embedded in the untrusted peer text, and did not return the other recipient’s canary.
The unregistered third root received neither canary. Accepted messages and held
messages remained unacknowledged in the hub. Restart used retained connector state.

Sanitized machine-readable evidence: [0.167.0](evidence/codex-isolation-0.167.0.json).

## Codex 0.167.2

The installed CLI and a separate disposable daemon both reported 0.167.2. The
same complete fixture passed, including unmarked/missing-ID legacy hook state
preservation, addressed handoff and stale-generation rejection. The handoff
delivery `dly_01M76NZJ5WG10BWZEAHB82WAMJ` reached only root
`01a1cd5e-1b8d-7b00-bc48-9ad8c310052f`, turn
`01a1cd5f-cad2-7821-91d3-616ed5628742`.

Sanitized machine-readable evidence: [0.167.2](evidence/codex-isolation-0.167.2.json).
These runs used a retained in-memory connector KV across restart; the deterministic
ledger/recovery tests cover persistence. They are not a power-loss simulation.

A further complete 0.167.2 run used a real disposable Git repository with two
roots in the same checkout and a third root in a neighboring Git worktree. All
nine checks passed, including addressed handoff to the neighboring root and
rejection of the old generation. See the
[worktree evidence](evidence/codex-isolation-worktrees-0.167.2.json).

## Existing installation inventory

The x-boss installation had a separate `vecnet-delivery@` service from an older
locally installed build. Its registry contained one explicit coordinator binding,
expired on 2026-11-10. The generic legacy bindings file had no rows. No current
recipient was inferred from the project's profile-specific hooks. Upgrading the
software must preserve the older registry and ledger and must not silently renew
this expired owner or assign its inbox to the current conversation.

The running shared daemon reports 0.166.0, while the installed CLI reports
0.167.2. Several separate stdio app-server hosts and the desktop app also run on
x-boss; installing a shared-daemon connector does not make those hosts reachable.

The subsequent development-host inventory was read-only:

| Host | Observed runtime | Rollout constraint |
|---|---|---|
| x-tuf | Ubuntu 26.04.1, Codex 0.167.0, Node 22.22.1 | No existing vecnet binary, binding or delivery service; Linux installation possible |
| x-m6 | Windows 11 build 26300, Codex 0.166.0, Node 26.13.0 | No vecnet installation found; this Unix-socket/flock connector and systemd installer do not support Windows |
| x-alpha | Not reached | `x-alpha.local` DNS resolution failed; the fallback address recorded in SSH configuration had no route |
| x-tall, x-lappy | Not reached | SSH host-key verification failed; trust checks were not bypassed |

## Linux installation

The supported standalone package was installed on x-boss first, then x-tuf:

- Content-addressed release: `6d1f9dbb78f34c492fa0`.
- CLI SHA-256: `74a007d0976a430864503bf912c42b1081c3f01db5bf8b18f0a699c3e6fe623f`.
- Service: `vecnet-codex-delivery.service`, enabled for the user.
- Both services were verified `active/running`, exit status zero, no restarts,
  with fresh private status heartbeats after final activation.
- Node: 22.23.3 on both hosts. x-tuf's distribution Node 22.22.1 lacks TypeScript
  support. The installer now probes TypeScript and SQLite before any write;
  x-tuf uses the repository build-box's checksum-verified official 22.23.3 archive,
  installed separately from the system Node.

On x-boss the previous `vecnet-delivery@` instance was stopped and disabled. Its
registry, expired binding, ledger and CLI were preserved, with a private rollback
snapshot under `~/.local/state/vecnet/rollout-68-20261112T013714Z`. The old ledger
had zero KV recovery records. The primary checkout's two profile-specific handlers
were replaced by three generic handlers with a checksum-guarded backup. Existing
user-level foreign hooks and hook trust were preserved.

This installs delivery infrastructure; it does **not** claim population enrollment.
Neither new service has an active binding. The current x-boss conversation was
read-only verified as an active root on daemon 0.166.0, but its recipient profile
has not been specified. No expired owner was renewed and no profile was taken from
a neighboring conversation. Recipient selection/enrollment, Windows/embedded host
bridges, and the three inaccessible hosts remain open under #68.

## Verification

- Full CLI suite: passed, 74.103s.
- Affected CLI/state/ownership race suite: passed, 6.154s.
- Full launcher/hub-client typecheck and tests: 1,882 passed, zero skipped.
- Standalone runner: seven startup/shutdown, durable recovery and unsafe-path tests.
- Installer: three tests, including rejecting a Node build without TypeScript
  before installation, preserving state/backups and systemd path escaping.
- UI: 527 passed. Protocol validation and scoped HTTP identity tests passed.
- The first broad Go run had an unrelated push retry timing failure; its exact
  `TestGrokbotOutcomes/network_error_retries` rerun passed (0.200s). Earlier stale
  hook fixtures were corrected and the complete CLI suite rerun successfully.

## Protocol sources

- [Official Codex hook contract](https://developers.openai.com/codex/hooks): child hooks may share the parent session ID; asynchronous completion does not wake an idle root.
- [0.167.0 SessionSource type](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/app-server-protocol/schema/typescript/v2/SessionSource.ts): explicit root sources versus subagent metadata.
- [0.167.0 Thread type](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/app-server-protocol/schema/typescript/v2/Thread.ts): exact thread ID, shared session ID, parent and fork lineage.
- [Earlier runtime research](2026-11-04-codex-app-server-delivery.md): existing receipt, turn and approval contracts.

## Limits

Live checks use daemon app-server roots and explicit hook invocations. Interactive TUI
rendering, desktop/embedded bridges, power-loss fault injection, and every combination
of layered user/project hook trust are not established by this run. Deterministic
CLI and launcher fixtures cover invalid ownership, recovery races, malformed state,
migration rollback, multiple homes and unsupported versions. Other surfaces remain
explicitly unavailable until their own supported bridge exists.
