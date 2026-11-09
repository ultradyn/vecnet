# WAKE-PAUSE-1 fresh whole-component source review

SpecPASS / QualityPASS
Counts: Critical 0 / Important 0 / Minor 1.
No known blocker or major defect in the supplied six-path component. M1 is a regression-oracle gap; the current production guard is correct.

## Scope and provenance

Reviewed the supplied 1001-line `final-component-review.diff`, all six current pinned files, and the focused callsites needed to trace delivery, settlement, policy, loading and cancellation. The declared comparison base is `3d06953f21299b268d84649ea3a488b62807ddad`. This verdict covers that component delta. Combined-tree integration remains a separate gate.

The requested model is explicit GPT-6.1-Sol. The supplied brief contains the parent's startup-header/model attestation; this source-only review did not inspect the parent or independently introspect model routing. This is a fresh review context, with no implementation or prior scoped-review context. No worker, fallback, test executor, build, import, browser, network operation, private runtime inspection or real-thread action was used.

The brief was read first. The plan, public receipt and named `progress.md` rulings were read to establish requirements, reported validation and authorized scope extensions. Prior reviewer conclusions were not used as correctness evidence. No prior scoped-review report was needed. The only written artifact is this absolute report path:

`/home/xertrov/src/vecnet4/.worktrees/paused-peer-wake/.superpowers/sdd/2026-11-09-paused-peer-wake/final-component-sol-review.md`

## Finding

### M1 — Minor: the terminal-only unload control does not exercise the launcher loading branch

**Location:** `connectors/launcher/test/codex-engine.test.ts:690`; default binding at `connectors/launcher/test/codex-engine.test.ts:81`. Related production guards: `connectors/launcher/src/codex/engine.ts:435` and `connectors/launcher/src/codex/engine.ts:496`.

**Scenario:** The terminal-only `notLoaded` control supplies an `ensureLoaded` callback and asserts `loads === 0`, but inherits `origin: 'explicit'` from the harness. Loading is restricted to `origin === 'launcher'`.

**Effect:** This control cannot detect accidental removal of the new `allowLoad` protection. Replacing completion recovery's `reconcile(false)` with ordinary `reconcile()` would still leave this control's callback unused. The assertion protects the explicit-binding path, while the newly added launcher-specific safety fence lacks a matching terminal-only oracle.

**Evidence:** The new status-control harness options at line 690 omit `origin`; line 81 supplies `explicit`. Engine line 496 requires both `allowLoad` and launcher origin before calling `ensureLoaded`. Current completion recovery passes `false` at line 435 and also refuses a new subscription at line 513. Thus this is a test gap, not an observed production cold-load defect. The existing launcher-loading test at test line 1146 exercises the ordinary permitted loading path, not terminal-only recovery.

**Suggested check:** Add a terminal-only `notLoaded` variant that starts as loaded/active with `origin: 'launcher'`, counts `ensureLoaded` calls, and returns false from that callback. Before any heartbeat or independent load trigger, require zero callback calls, zero additional resumes, no claim and no input after the terminal event and duplicates. It should fail if completion recovery's `reconcile(false)` becomes `reconcile()`. Execution of this proposed check is **CannotVerify** in this review; no executor was granted.

## Source checks and regressions

| Requirement | Source evidence and result |
| --- | --- |
| Existing backlog reconsidered after terminal-only completion | Engine lines 487 and 396–448 route a matching thread completion into recovery, perform an authoritative read, reconcile and scan settlement obligations, then arm the existing lane. Delivery still enters through `push` rather than directly from the notification. The main regression at test lines 611–681 requires no new message or idle notice, an engine-owned idle read before input, a persisted claim/attempt before RPC, and exactly one receipt/display publication. |
| Failed observation retained without retrying input in the recovery routine | Engine lines 386–406 and 449–454 retain one observation obligation and timer. Backoff is 1/2/4/8/15 seconds, capped at 15 seconds; a duplicate cannot bypass an existing retry timer. The two test cases at lines 740–799 cover an initial failure and a failed trailing observation after duplicates, with no later external trigger and exactly one eventual input. |
| Coalescing and resource bounds | Engine lines 87–89 and 396–408 use one pending recovery, one trailing flag and a set of existing inflight keys. A read has the existing 5000 ms deadline through lines 47 and 183–185. The finite burst test at lines 834–860 measures concurrent and total engine reads independently of barrier reads. This is a concurrency/rate bound; the code does not impose a fixed total retry count while a binding remains eligible. |
| Completion activity versus unchanged idle | Engine line 424 records activity only for the guarded running-to-idle observation. Subsequent unchanged idle observations retain that checkpoint. Tests at lines 2044–2123 and 2127–2203 cover a native turn longer than the dormancy window and repeated successful idle observations while actual receipt confirmation remains pending. The latter advances the clock in small drained steps, then verifies settlement finishes without reviving retired backlog. |
| Newer status, start, close, archive and invalidation | Engine lines 419–420 require the same connection, close epoch, observed status object, live recovery obligation, attachment and eligibility before applying a reply. Status updates cancel the obligation; close increments the epoch; archive and over-limit changes fence recovery. Tests at lines 862–883 capture the old response before the newer notice, then release it and check that no claim/input appears. The stale-terminal test at lines 724–737 also checks actual newer running state. |
| Receipt/display/claim ordering and exact-once recovery | Completion only selects rows for the existing history scan; it does not treat terminal payloads as display proof (engine lines 400–401 and 437–445). `scan` at lines 553–586 requires matching user-message history or a complete bounded absence proof. Settlement persists original attempt authority and validates receipt output before clearing, at lines 153–167 and 210–239. The push fences at lines 681, 701, 721, 743 and 755 preserve settlement and key ownership across awaits. Duplicate completion tests check actual delivery reservations and receipt publications. |
| Shutdown and restart | Engine lines 818–834 disable the lane, cancel timers, close an outstanding completion probe before awaiting the queue, drain children, and preserve accepted/ambiguous rows. RPC close rejects pending calls at `rpc.ts:42–47` and `rpc.ts:104`. Tests at lines 709–722 and 801–832 cover an armed timer and a response blocked on the wire; the latter requires stop to finish while the gate remains closed, then checks late responses/notices arm nothing. Existing restart coverage at lines 1104–1113 verifies retained native history settles once without resending. New recovery flags are reset at engine lines 812–814; durable rows still use the existing startup recovery path. |
| Waiting, approval and system-error exclusions | Engine `reason` at lines 169–180 checks waiting states before recovery can read or send. Authoritative waiting flags are preserved by `mapStatus`; systemError and unknown states map to ineligible `unknown` (`protocol.ts:49–57`). The four terminal-only controls at test lines 683–707 require no claims, input or approval replies. Existing waiting controls at lines 1418–1427 preserve the subscription without answering requests. |
| Attendance, expiry, identity, over-limit and policy | Recovery checks the existing `reason` before and after the read and before re-arming. Identity, expiry, archive, limit and policy remain ahead of eligible-state handling (engine lines 169–180). Unattended asking policy is rejected during guarded resume at line 529. Existing expiry, attendance/policy, identity and limit tests at lines 1124–1126, 1404–1437 and 1485–1489 corroborate those unchanged guards. No terminal-only exception or authority/policy override is introduced. |
| Cold-load prevention | The recovery path uses `reconcile(false)` at engine line 435. Both owner loading at line 496 and creation of a subscription at line 513 are gated by `allowLoad`. It can observe or retire an existing subscription. Ordinary launcher loading remains in the existing owner path. M1 identifies the missing launcher-origin terminal-only regression oracle. |
| Fixture defaults and delayed-response fidelity | `fake-codex-appserver.ts:277–285` retains ordinary completion notification's automatic idle transition. The opt-in helper at lines 288–294 updates native history/read state and sends only the subscriber terminal notice. Read gates clone the response when requested at lines 139–143, so releasing a gate cannot silently replace the stale snapshot with current state. Ungated read behavior remains the previous immediate response. |
| Regression oracles | Harness barrier reads use the underlying RPC at test lines 116–127, bypassing the engine observation wrappers. The main and long-turn cases therefore count actual engine observations rather than fixture synchronization. The long-turn snapshot holder retains the first actual response at lines 2048 and 2064, and assertions at lines 2094–2096 inspect it. Existing assertions in the supplied diff were preserved; the additional fixture behavior is opt-in. |
| Documentation claims | The plan distinguishes a source-proven edge from the unestablished incident and leaves integration open. The receipt identifies synthetic gates, exact executable pins and the retained strict compiler failure. Its description of coalescing, idle age, settlement and policy fences matches the source paths above. The changelog describes component behavior without claiming deployment or incident attribution. |

No new Critical or Important regression was established by this source review. The first-transition activity change and unchanged-idle behavior satisfy their opposing dormancy requirements in the reviewed source. This verdict does not replace executable regression results.

## Files and pins checked

SHA-256 was computed from all six current files and matched every entry in `final-component-review-pins.json`. A final read-only hash check immediately before writing this report confirmed the same pins.

| File | Verified SHA-256 |
| --- | --- |
| `connectors/launcher/src/codex/engine.ts` | `cbc1b95363687bb5280330c9c26a5673678f5183d80d92a3d3a4d1d3c3e29986` |
| `connectors/launcher/test/codex-engine.test.ts` | `ec92d2fcc63f1892742b647770752c65a96064230619f78689714213ceb3ddb6` |
| `connectors/launcher/test/fake-codex-appserver.ts` | `8749cbdd10105dd4cefd5b2b1594acae91c6921c7dbf9f077faeeced003e4ac7` |
| `docs/plans/2026-11-09-codex-terminal-recovery.md` | `4d5f0654fb4972f9e5f9e140d9b9fb8641ba7b0a80d3bcdb2d053d501b3bef3c` |
| `docs/plans/receipts/2026-11-09-codex-terminal-recovery.md` | `17acef605c2065717c90d2c391aff993ccfdbf31cfac95c803bc40ee2494a1a6` |
| `CHANGELOG.md` | `5fa35aef5fa4021a404651454b26c5cf8bd11aa2e5f211109a321b0fad06ef34` |

Focused dependency reads: `connectors/launcher/src/codex/protocol.ts` for status, resume policy, pagination and allowed request construction; `connectors/launcher/src/codex/rpc.ts` for deadlines, ignored server requests and close cancellation; `connectors/launcher/src/input-plan.ts` for wake/wake_idle/hold eligibility. Current engine source was read through its delivery, settlement and lifecycle paths. Test reads included the complete new blocks, their harness/fixture behavior and the specific existing exclusion/restart checks listed above.

## Reported execution evidence and remaining unknowns

The public receipt at lines 18–26 reports focused 17, canonical strict TypeScript plus full 2170 units, and launcher E2E 44: each actual/bb exit 0/0, with zero failures/skips/cancellations. It records raw hashes `dfeff0aa…`, `9fbc2eea…` and `613d66bc…`, temporary homes, frozen 2151-file source manifests, and two empty owned-process drains per gate. Those executable pins match the files reviewed here. The earlier TS2339/TS7006 failure remains explicitly recorded at receipt line 34.

These are parent-reported execution results read from the public receipt and ledger. Independent execution, raw-output rehashing, remote source manifests and drain scans are **CannotVerify** under this zero-executor/source-only grant; none was performed or invented. Source assertions were assessed directly rather than inferred from reported PASS results. This review found no source reason to relabel the reported gates as failures.

Out-of-component observations and obligations:

- F1 definite-rejection classification remains unresolved and separate. The generic rejection/cleanup logic at engine lines 649–674 is unchanged by this component. This review grants no new rejection retry policy or interpretation of refusal as permission.
- Actual attribution of the reported approximately eleven waiting messages, affected identity, priorities, native state timeline and installed connector remains **CannotVerify**. The receipt explicitly distinguishes the related four-delivery owner report; no incident evidence was collected here.
- Full combined-tree integration, prerequisite release history, master ancestry, Go/UI/screenshots/protocol/release gates, installed behavior and deployment remain **CannotVerify** in this review. No refs, commits, parent or sibling state were inspected. The stated master ancestor and prerequisite constraints still belong to the parent integration gate.
- Explicit GPT-6.1-Sol startup verification is supplied by the parent's brief attestation; independent runtime/model verification was outside this source-only inspection.

The source-review verdict permits the parent to assess this component with M1 recorded. It supplies no execution, publication, integration or deployment grant.

=== DONE ===
