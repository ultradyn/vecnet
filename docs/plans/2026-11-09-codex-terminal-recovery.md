# Codex terminal-only completion recovery

**Goal:** restore delivery eligibility when a completed native turn leaves the engine's cached status active. Track WAKE-PAUSE-1 / issue #61.

Max reports that xpp-icant-native paused after a cyber-security restriction and was never woken with roughly 11 messages waiting. He confirmed the app was Codex. Source diagnosis identifies this recovery edge in both `5a0735a3` and `3d06953f`; the affected peer, priorities, owner, runtime version and native state timeline are still unknown. This slice fixes a source-proven edge without claiming incident attribution.

**Base:** `fix/paused-peer-wake` at `3d06953f21299b268d84649ea3a488b62807ddad`. Diagnosis: `.superpowers/sdd/2026-11-09-paused-peer-wake/diagnosis.md`.

## Amendment — 2026-11-09 parent verification and review fixes

The initial regression and three review-driven extensions have genuine RED evidence. Recovery now retries failed authoritative observations with a single coalesced timer and 1/2/4/8/15-second capped backoff. Waiting on settlement and observing unchanged idle do not renew idle age. The first guarded running-to-idle completion establishes an activity checkpoint so a long-running native turn can drain eligible backlog. Newer notices, cancellation, shutdown and lost subscription invalidate stale observations. The scoped independent Sol reviews accept these changes; a fresh whole-component review passed with C0/I0/M1 before publication.

The original 250-line test-draft limit applied to Task 1. Subsequent parent-approved review regressions extended that test file while preserving prior assertions; the final component changes three executable files. A separate test-only typed holder preserves the first actual asynchronous response and fixes strict TypeScript narrowing; its earlier compiler failure is retained as failure evidence.

Current exact-source gates on x-boss: focused17, canonical strict TypeScript plus full2170 units, and E2E44 all passed with zero skipped, cancelled or failed tests. The receipt records immutable raw hashes and source pins. Integration, full merged-tree gates, installed behavior and real incident attribution remain open; this component does not activate a real thread or change policy.

## Global Constraints

- Use this isolated worktree, explicit GPT-6.1-Sol workers and independent review. Keep the frozen release candidate unchanged.
- The parent runs all tests on x-boss, within the two-job aggregate cap, using temporary homes and fake app-server fixtures.
- Preserve receipt/display/claim settlement, attendance, binding eligibility and policy gates. Do not cold-load threads, answer approvals or reinterpret a refusal as user permission.
- Do not inspect other agents' private runtime or act on real threads. Message notification and the policy decision remain separate.
- The rejection-retry classification finding F1 is a separate slice. Do not blindly re-arm every rejected request.

## Review Focus

An authoritative eligible idle state after terminal-only completion must allow existing wake/wake_idle backlog to be reconsidered without a new message. Repeated terminal events must not duplicate claims or input. Waiting, approval, system-error, unloaded and invalid-binding gates must remain intact. Probes must be bounded, coalesced and cancelled during shutdown.

## Task 1: Regression tests only

**Files:** `connectors/launcher/test/codex-engine.test.ts`; only if needed, `connectors/launcher/test/fake-codex-appserver.ts`. At most 250 added lines.

- [x] Add an explicit terminal-only notification fixture path. Existing fixture calls must retain their automatic idle transition by default.
- [x] Reproduce cached active status followed by terminal completion without a status notice or new delivery. An authoritative read now reports idle; queued wake_idle must be delivered exactly once.
- [x] Cover repeated completion and relevant ineligible-state controls. Preserve all existing assertions and default fixture semantics.
- [x] Return the test draft without execution. The parent observes genuine RED before granting production changes.

## Task 2: Refresh and reconsider eligible backlog

**File:** `connectors/launcher/src/codex/engine.ts`. Test corrections require an explicit parent ruling on a demonstrated fixture error.

- [x] Refresh the actual loaded thread status after a relevant terminal event, then re-arm eligible backlog after required settlement.
- [x] Coalesce and bound the refresh; respect shutdown and existing receipt/claim ordering.
- [x] Do not assume completion means idle, clear approval flags, map systemError to idle, cold-load, or retry rejected policy work automatically.
- [x] Obtain parent GREEN and independent review.

## Task 3: Verify and integrate

- [x] Run focused tests, canonical launcher strict TypeScript and full launcher unit tests on x-boss; add the relevant E2E/integration gates.
- [x] Retain exact source, raw exits and test names.
- [x] Complete fresh whole-component independent source review (SpecPASS/QualityPASS; C0/I0/M1).
- [x] Keep issue #61 open until the incident path and installed behavior are established.
- [ ] Integrate/deploy serially under the existing release authority and dependencies.

## Deferred validation follow-up

Whole-component review M1: add a launcher-origin notLoaded terminal-only control that would fail if reconcile(false) were changed to reconcile(). The present explicit-origin control does not exercise owner loading; production guards are correct. This minor regression-oracle gap is retained in issue61 alongside F1 and incident attribution.

--- SUMMARY ---

Repair terminal-only Codex state recovery using authoritative eligible-state reads and exactly-once backlog rescheduling. Reproduce the missing edge with fake app-server tests, preserve policy and claim boundaries, and verify on x-boss. Keep rejection retry classification and real incident attribution separate.
