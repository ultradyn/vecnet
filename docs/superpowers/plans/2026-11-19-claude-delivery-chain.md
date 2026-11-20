# Claude Native Delivery Chain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the delivery chain from a hub message to a native Claude Code inbox pointer, with durable claim fencing, operator surfaces, and a captured end-to-end proof.

**Architecture:** The shared delivery host (Tasks 1–2) gains a Claude adapter as a hosted auxiliary; it subscribes to pooled verified watches, admits deliveries against the Task 3 registry, and persists claim state before delegating the single socket write to a Go transport helper. Operator commands extend the Task 5 `claude` command surface. Slice 3 of the spec (Linux pointer-only canary) is the first transport qualified; named content, `wake_idle`, default-activation and native replies stay gated per the spec.

**Tech Stack:** TypeScript launcher (node:test), Go CLI (go test), disposable loopback hub, real Claude Code 2.1.322 session under `vecnet-test-<nonce>` tmux.

**Spec:** `docs/superpowers/specs/2026-11-19-claude-delivery-design.md` (binding authority — Delivery contract, Claim/recovery, Acceptance gates)

## Global Constraints

- Pointer-only payload: fixed `Vecnet` label, first line `Vecnet message available.`, strictly validated opaque thread/message IDs, spec's model-facing copy. No peer-controlled text in the frame.
- One LF-terminated UTF-8 JSON record per attempt; exact receiver `session_id`; opaque `msg_id`; ≤4 KiB encoded; 1s connect/write deadline; one attempt in flight per scope; no transport-level retry.
- `hold` is inbox-only; never posted natively, never auto-acked. No native auto-ack at all: separate observed / local-disposition / hub-acked positions.
- Durable `attempting` before any byte can leave; `attempting`/`uncertain` on restart ⇒ retain claim, surface operator disposition, never resend.
- Pending cap 128 per scope; reaching it blocks new native sends (`blocked_pending_limit`), not inbox reads.
- No synthetic sender permission class, no address in a label, no parsing of rendered bylines.
- Registration and delivery never touch a user's real session, hub identity, or transcript. Test sessions use `vecnet-test-<nonce>` tmux and disposable credentials; teardown verified.
- Go tests run under `umask 022`; `just` needs `scripts/bb/bin` on PATH.

## Review Focus

- Crash between `attempting` persistence and socket write must be indistinguishable from post-write crash — tests inject both, restart asserts no resend.
- A later `wake` must not promote or ack an earlier quarantined `hold`/uncertainty in the same window.
- Cap enforcement must fail closed (no new sends) while explicit inbox reads still work.
- `msg_id` reuse acceptance (Claude accepts the same ID twice) must not be read as dedup by us.
- Terminal/socket refusal (ECONNREFUSED, ENOENT, partial write, timeout) maps to distinct dispositions without resend.

---

### Task 6: Go transport helper `claude deliver`

**Files:** Create `internal/cli/claude_deliver.go`, `internal/cli/claude_deliver_test.go`.

**Interfaces:** Produces `func (a *app) cmdClaudeDeliver(args []byte) error` dispatched as `claude deliver --socket PATH --session-id ID --msg-id ID --thread-id ID --message-id ID` (all values validated ULID/opaque forms); exit contract: 0 confirmed full write+EOF-safe flush, 3 connect refused/timeout, 4 write failure/partial, 2 usage/validation. Performs exactly one connect+write, no read of the socket beyond a bounded close, no retry, no state writes.

- [ ] **Red:** `TestClaudeDeliver*` against a fake unix-socket server: happy path frame equals exact LF JSON ≤4KiB and contains no extra fields; refused socket → exit 3; server closes mid-write → exit 4; oversized assembled frame → validation exit 2 before connect; slow server exceeds 1s deadline → exit 3/4 without hang; invalid ULIDs → exit 2; helper never writes any file.
- [ ] **Confirm red:** `umask 022 go test ./internal/cli -run '^TestClaudeDeliver' -count=1` — expect missing command.
- [ ] **Implement:** single connect (`SOCK_STREAM`, `O_CLOEXEC`), one `write` loop bounded by deadline and 4 KiB, monotonic clock injection, no environment-derived paths beyond explicit flags.
- [ ] **Verify:** `umask 022 go test ./internal/cli -run '^(TestClaudeDeliver|TestClaudeRegistration|TestClaudeSetup)' -count=1` — all pass.
- [ ] **Commit** `feat(claude): add single-shot native deliver helper`.

### Task 7: Hosted Claude delivery adapter with durable claims

**Files:** Create `connectors/launcher/src/delivery/claude-adapter.ts`, `connectors/launcher/test/claude-adapter.test.ts`; modify `connectors/launcher/src/delivery/host.ts` (auxiliary wiring behind an options flag, default off).

**Interfaces:** Consumes `WatchPool.subscribe`, `claudeScope` snapshot via the `claude runtime-snapshot` child (extended in this task to expose admission fields: live, policy, version-qualified), `DeliveryHostOptions.auxiliary`. Produces `createClaudeDeliveryAdapter(deps: { pool; clock; log; kv; env; transport?: (frame) => Promise<DeliverExit> })` implementing `HostedAdapter`. Claim records under `claude-claims` keys: `{msgId, threadId, deliveryId, scope, generation, cursor, predecessor, state: reserved|attempting|held|refused|expired|uncertain|released, attemptId, timestamps, dispositionReason}` — no body/credentials.

- [ ] **Red:** fake-transport tests: admission gating (non-live/non-qualified binding → no transport call); `hold` never transported and never acked; `reserved` crash → restart releases after fencing; `attempting` crash → restart retains + surfaces `uncertain`, never resends; transport exit 0 → disposition `attempted` (no positive presentation proof recorded); exit 3/4 → `refused`/`uncertain` respectively; later `wake` after quarantined hold delivers new message but leaves old tombstone visible; 129th pending → `blocked_pending_limit`, no send; tombstones survive reconcile until confirmed hub ack.
- [ ] **Confirm red:** `just ln-test connectors/launcher/test/claude-adapter.test.ts` — module missing.
- [ ] **Implement:** watch events → bounded per-scope ordered staging window → admission → durable `reserved`+`attempting` (kv transaction) → single transport invocation → disposition. Coalesced reconciler with bounded backoff; no network or writer guards held across transport I/O (transport awaited, host stop drains it).
- [ ] **Verify:** `just ln-typecheck && just ln-test connectors/launcher/test/claude-adapter.test.ts connectors/launcher/test/delivery-host.test.ts connectors/launcher/test/delivery-watch-pool.test.ts` — all pass; adapter absent from default host options (flag off).
- [ ] **Commit** `feat(claude): host native pointer delivery with fenced claims`.

### Task 8: Operator surface for deliveries

**Files:** Modify `internal/cli/claude_commands.go` (Task 5's surface), its tests.

**Interfaces:** `vecnet --net P claude deliveries [--json]` (bounded dispositions, IDs, states, reasons); `claude resolve DELIVERY --expect-generation G [--keep-inbox|--retry]` (`--retry` requires explicit `--confirm-duplicate-risk`); pending counts and `blocked_pending_limit` in `claude status`. No command accepts a raw path to a foreign inbox; reads go through the existing claim/registry documents.

- [ ] **Red:** `TestClaudeDeliveries*`: listing redaction (no body/credentials); resolve with wrong generation → refusal, state unchanged; `--keep-inbox` stops promotion, keeps readable, does not ack; `--retry` without confirm flag → refusal; resolve of unknown/foreign scope → refusal; status shows cap state.
- [ ] **Confirm red**, **Implement** against Task 7's claim documents (cross-language reader via a Go-side strict decoder mirroring the TS schema, versioned).
- [ ] **Verify:** `umask 022 go test ./internal/cli -run '^(TestClaudeDeliveries|TestClaudeStatus|TestClaudeSetup|TestClaudeRegistration)' -count=1` — pass.
- [ ] **Commit** `feat(claude): surface bounded native delivery dispositions`.

### Task 9: End-to-end canary proof and gallery

**Files:** `scripts/e2e/claude-delivery-e2e.sh` (or `.py`), evidence output dir, served gallery page under `~/served-html/vecnet/claude-delivery-e2e/`.

- [ ] **Harness:** disposable loopback hub + dedicated fresh profile/credentials (never a user identity); packaged runtime under test; `vecnet-test-<nonce>` tmux session running a real Claude Code 2.1.322 session; enrollment via the Task 4 flow; adapter enabled via the experimental canary opt-in.
- [ ] **Proof chain, all captured:** hub send receipt → daemon log lines (admission, reserved, attempting, exit disposition) → tmux pane capture showing the native pointer message in the live session → the model's fetch via `vecnet_inbox`/CLI → transcript JSONL `queue-operation`/`queued_command` rows → hub receipts showing no ack emitted. Fail the run if any link is missing or if any resend occurs.
- [ ] **Negative controls in the same run:** `hold` stays inbox-only; SIGKILL of the daemon between `attempting` and write → restart shows `uncertain`, no resend; cap behavior with 129 staged messages.
- [ ] **Teardown:** kill tmux session, verify `no server running` or no `vecnet-test-*` sessions remain; dispose hub/profile.
- [ ] **Clip:** record the tmux session (frame captures at minimum; animated GIF/asciinema if tooling permits) plus the daemon log excerpt; build the gallery page per the served-gallery skill (modest theme, template improvements folded back).
- [ ] **Commit** harness + evidence-index `test(claude): end-to-end native delivery canary proof`.
