# 33: Bind hook state to its identity, and qualify hook hints with the profile

Raised 2026-11-04 by codex-llmp's planning review (thread thr_01M6J4H1CTW0RBCSB8Y58RBAR0, msg_01M6J5QZE0KT2ASQRM863RYCV8); confirmed against hook-hygiene f073538.

## 1. State is not bound to hub + peer
`hookState` (internal/cli/hookstate.go) holds `poll_cursor`, `shown` and `last_post_tool_poll`, keyed only by config dir + profile[@as]. Re-pointing a profile name to another peer or hub (detach and re-enrol, edit config) reuses the old cursor and shown ids; a same-epoch cursor from another stream is not necessarily refused.
- Fix: store the normalized hub URL and the authenticated peer id (and `as`) in the state. On mismatch, discard cursor and shown ids and take the first-run path (summary line, seed at head). Credential rotation for the same peer keeps the state. Legacy files without identity adopt the current identity once.
- `watch --for-monitor` uses the same state, so apply the same check there.

## 2. Hook hints omit the profile
`hookContext`/`hookSummary` print `vecnet reply <msg> …`, `vecnet ack <cursor>`, `vecnet inbox` with no `--net`. An agent whose session is bound to a non-default profile (codex-llmp vs the default cc-vecnet-cos) would act as the wrong identity.
- Fix: every model-visible command includes `--net <profile>` (and `--as <peer>` when set); when `VECNET_CONFIG` was set explicitly, say so in one short clause. The profile is already `inert`ed in the header; hints use the same validated name.
- Update PROTOCOL §13.9 and docs/connectors/claude-code.md examples.

## Tests
State with a different hub URL or peer id → first-run summary, nothing replayed from the old stream; same peer after credential rotation → normal output; hint strings contain `--net <profile>` (and `--as`) for a non-default profile.

## Order
Small follow-up right after hook-hygiene merges, before the Codex adapter plan is implemented.
