# Codex delivery isolation implementation plan

Issue: https://github.com/ultradyn/vecnet/issues/68

## Ownership and delivery contract

The private Codex bindings file is the authority for automatic delivery. A
binding identifies the canonical vecnet config, authenticated hub recipient,
canonical Codex home, exact root thread, and random generation. Profile aliases
do not create separate recipients. Renewal preserves the generation; an explicit
compare-and-swap handoff changes it. Missing, old, conflicting, or corrupt records
cannot authorize delivery.

Codex hook events cannot reliably distinguish a child from its parent. They must
never claim or emit peer data, including SessionStart. The shared dispatcher may
report bounded setup diagnostics; the app-server sends to the exact bound thread.
Manual inbox remains an explicit operator/agent operation. Embedded hosts without
a supported bridge must report unavailable automatic delivery, not readiness.

Delivery must hold an interprocess ownership guard through claim and transmission.
Transfers serialize against that guard. Durable recovery records include the full
original owner. Unknown or mismatched recovery records are quarantined; replacement
credentials or profiles must never release the previous owner's claims. Ambiguous
sends retain exact-thread receipt recovery and prevent unsafe handoff.

## Work sequence

- [x] Binding schema, canonical identity, alias exclusion, addressed handoff,
      generation guards, and regression fixtures (`internal/cli/codex*.go`).
- [x] Fail-closed Codex hook admission and generic, reversible migration preserving
      custom hooks (`internal/cli/hook*.go`).
- [x] Connector ownership guards, durable owner-scoped recovery, duplicate connector
      exclusion, and status (`connectors/launcher/src/codex`, corresponding tests).
- [x] One setup path discovers the invoking thread, binds it, migrates hooks and
      reports actual availability. Update guide, invitations, help and protocol.
- [x] Run affected CLI/launcher suites, independently review admission and races,
      and exercise disposable live sessions with exact-thread canaries.
- [x] Record exact runtime versions and any unavailable capability/rollout evidence;
      prepare a PR whose completion claims match the evidence.

## Verification

Use the repository's `just bb` build-box runner. Baseline issue-68 CLI fixtures
passed before changes on 2026-11-12. Add tests before implementation for aliases,
handoff CAS, unknown/child events, corrupt state, concurrent dispatch and transfer,
owner-scoped restart recovery, and reversible installation. Run launcher typecheck
and all affected tests after integration. Live acceptance must use disposable owned
threads and private hub/config state; do not reset real delivery state or implicitly
trust hooks. Source tests and rollout evidence are separate gates.

## Remaining population rollout gates

See the [runtime report](../research/2026-11-12-codex-isolation-runtime.md).
An expired existing registration is not permission to assign its inbox to a new
conversation. Production enrollment needs an explicit recipient and reachable
root. Windows, embedded/desktop/stdio-only hosts need a supported bridge; hosts
with DNS or SSH trust failures need access restored. These gates keep issue #68
open even when the implementation and reachable Linux installations are ready.
