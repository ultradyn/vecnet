# Codex terminal-only completion recovery implementation plan

> For agentic workers: use the PIRFL implement/review/fix loop under the controller; source implementation is delegated, reviews are independent.

**Goal:** restore automatic reconsideration of queued eligible Codex wakes after a terminal notice arrives without a separate idle-status notice.
**Architecture:** observe the already-loaded owned thread authoritatively, fence stale replies and coalesce bounded observation retries, then use the existing delivery/claim/receipt machinery. Adapt the historical terminal-recovery change to current engine admission/settlement semantics; do not merge its old branch.
**Tech Stack:** TypeScript, Node >=22.18, existing launcher/fake app-server tests, Go E2E hub.
**Spec:** GitHub #61 body/comments (frozen as issue-61.json in the operational workspace), scoped to its source-proven terminal-only completion defect. The approximately eleven-message incident and F1 generic refusal classification remain open; this plan cannot close the entire issue.
**Exit tests owned:** ET1 terminal-only idle drains exactly once; ET2 failed/stale/burst observations preserve bounded liveness; ET3 loaded/owner/policy/idle-age/settlement gates remain correct.

## Global Constraints

- Use only this isolated worktree and branch fix/issue-61-terminal-recovery. Controller already has operator authorization for selection, subagents, review/fix, merge and cleanup.
- All builds/tests run through the serialized x-boss gate, two jobs/threads, private physical TMPDIR, umask 0022, synthetic fixtures and lane-private HOME/XDG. No real runtime, profile, bindings, credentials or live messages.
- Preserve current admission/ownership/generation/claim/receipt/expiry fences, attendance, hold and wake_idle semantics, approval and input gates, ambiguous recovery, and current rejected-request handling. Completion is neither proof of idle nor authorization.
- No new dependencies, schema changes, deployment, service activation or whole historical-branch merge.
- Fix every accepted independent review finding, including minor findings. Final source review must PASS before merge.

## Review Focus

1. RF1: Terminal-only authoritative idle, including long-running turns, delivers retained wake_idle exactly once without another event; history/receipt proof remains required. Pin in Task 1 with named terminal-only drain and long-turn tests.
2. RF2: Failed status reads retry independently with one coalesced timer and capped 1/2/4/8/15-second backoff; finite duplicate bursts create at most one in-flight and one trailing observation; unchanged idle observation does not renew idle age. Pin in Task 1 with failed-read, burst and idle-age tests.
3. RF3: A newer started/waiting/unloaded/archived/owner-invalidated state or shutdown cannot be overwritten by a stale idle response or restarted by a timer. Pin in Task 1 with stale response and blocked-read stop controls.
4. RF4: Launcher-origin completion discovery of notLoaded never invokes ensureLoaded or resumes the thread. Pin in Task 1 with a new regression whose oracle fails if completion reconcile(false) becomes reconcile().

## Decisions

Use guarded authoritative status observation rather than trusting the completion payload: only the former preserves approval/input/systemError/unloaded state. Reuse the historical tests as reference and preserve their behavioral oracles while adapting the engine; blanket cherry-pick would replace newer identity/admission behavior. Generic definite-refusal replay is rejected because the current RPC contract lacks typed general retryability; it remains a distinct #61 follow-up.

### Task 1: Port current-source terminal completion recovery

**Files:** Modify connectors/launcher/src/codex/engine.ts; Test connectors/launcher/test/codex-engine.test.ts and connectors/launcher/test/fake-codex-appserver.ts; update CHANGELOG.md and create docs/plans/receipts/2026-11-15-codex-terminal-recovery.md. DO NOT TOUCH other production files or existing historical receipts.
**Consumes:** current createEngine, clock, cached status, reconcile/verifyAdmission, push/history retries, scan/recoverClaims and hook claim/receipt operations. Historical 8e69ff5b is reference only.
**Produces:** unchanged public Engine/Binding interfaces; private completion-observation state and fake terminal-only fixture option with original default fixture behavior retained.

- [x] Read the complete current engine and historical changed executable functions/tests. Write current-source test-first reproduction of terminal-only idle followed by a queued wake_idle with no new event; assert exactly one delivery and claim/receipt safety. Adapt the historical behavioral regressions and add the launcher-origin notLoaded mutation oracle. Preserve existing assertions.
- [x] Run focused RED using the gate script against unchanged production engine. Failures must show the missing state recovery, not compile/fixture failure. Record exact commands, relevant output and test names. Guard controls already passing are recorded as controls rather than claimed RED.
- [x] Implement the smallest current-engine adaptation: one coalesced loaded-thread status obligation, bounded retry cadence, stale generation/status/subscription/ownership fences and cancellation. Preserve first running-to-idle activity while unchanged idle reads keep their age. Reconcile the completion observation without enabling cold loading; settle only with existing history/receipt proof before reconsideration.
- [x] Run focused GREEN, complete engine/RPC tests and canonical strict TypeScript/full launcher+hub-client tests via the gate. Final exact-source affected gates are recorded in the receipt; R4/R6 govern proportional integration reuse.
- [x] Add concise changelog/receipt with actual source pins, test evidence and limitations, commit explicit owned paths with fix(codex): recover queued wakes after terminal-only completion. Write task-1-report.md last.

## Integration and exit

Independent task review and two fix/re-review rounds PASS, followed by a fresh whole-branch source review PASS. Exact-source strict/full launcher gates passed; the prior-source generic launcher E2E is retained with its limits under R4/R6. Preserve concurrent master, merge normally to master and push; report #61 source progress accurately while leaving incident/F1 acceptance open. Preserve and verify an external artifact copy before deleting only this worktree/merged branch. Try it: synthetic terminal-only cases in the named engine tests; no real session activation or installed-release claim.

## Self-review notes

This is one cohesive task in three executable files, with docs folded in; no shared task interfaces. Historical fixes are evidence/reference, not current gates. All three exit tests and RF1–RF4 belong to Task 1. Current source independently confirms missing status refresh; actual incident attribution is explicitly outside this deliverable. No runtime/deploy uncertainty is needed for synthetic acceptance.

--- SUMMARY ---

Port the absent terminal-only recovery onto current source with behavioral RED/GREEN, independent review, full affected integration, merge and safe cleanup. Keep #61 incident/F1 follow-ups open.
