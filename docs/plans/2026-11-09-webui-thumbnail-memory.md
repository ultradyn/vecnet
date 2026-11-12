# Web UI thumbnail memory implementation plan

## Current integration note — 2026-11-12

This amendment describes the selected, unshipped thumbnail/lifetime source at `1a038e59086e8999929041441931e810af2d95e2` (tree `807b3de221bfe6c49099a17f0960354b6a903407`) against reviewed master baseline `124d5c8fd3c373215deb14ce893c8e35b315d3e6` (tree `0b67f1610e10bf74b845727b2e0f9c33463f71a1`). Its exact 18-path source slice is byte-equivalent to predecessor `f71bf0c74f4f809ad7e784d942cc6832c897aa4c`. The baseline is pinned; moving master refs are not acceptance inputs. The earlier `8d2394d7`/`84022d85` port, 720KiB cap and source milestone statements below remain historical records, superseded for current identity and release sequencing by this note.

The actual final Sol source review reports Spec PASS / Quality PASS / C0/I0/M0. That verdict covers source only. Parent-recorded 637 full UI / 134 focused passes and the f71 native app401 pass are earlier component evidence; the older both-arm reader 4-case and AFTER API 14-case passes likewise do not certify this final combined tree. Final Go/UI, embedding/CSP, complete protocol examples/blob fixtures, migrated attachment journey, canonical screens/visual inspection and broader actual-app functional acceptance remain OPEN here. No terminal parent Go result is adopted. Drafting these bytes grants no documentation PASS or permission to install them before parent acceptance.

Current source uses one document-lived admission owner across route/sign-out generations, with mount-local payloads and URLs. B=33554432, C=2, E=16, N=16, Q=16 and M=10485760 remain source/model limits; the error envelope is at most min(8192,maxBytes). Unsettled cancellation stays charged; physical sources detach before revoke/accounting retirement, with retained cleanup/retry authority. Demanded rasters and deferred failed openers expose viewer Retry/Cancel/Close/Files and exact occurrence focus; automatic offscreen last-owner URLs retire before route exit. Priority does not guarantee progress before actual settlement. These bounds do not limit decoded/native/browser RAM or create a cross-tab quota. The reviewed current inventory is 76 static files / 749561 bytes; 736KiB=753664 bytes is the minimum 16KiB step, with 4103 bytes headroom. Historical cap evidence is unchanged.

PR63 already shipped WEB-MEM-1/2/4's dashboard, event-pump and Markdown basic bounds per the parent brief. Preserve those fixes, all current-master/PR67 behavior and the historical evidence below. The new thumbnail/lifetime source remains unshipped. R-WEB-F1 permits source plus functional shipping after mandatory integration/functional acceptance while the matched E-memory matrix remains mandatory OPEN under the parent CoS. All existing functional/P1/P2 acceptance values, resource ceilings and metric thresholds remain unchanged; no replacement plan or framework is introduced.

R-DEPLOY-CURRENT-1 supersedes the old pre35 blanket deployment hold for ordinary no-new-migration releases. The release owner still needs the exact tested/reviewed target, no-new-migration manifest, current metadata/compatible rollback artifact, serialized deployment and public health/assets plus affected-feature smoke. Historical consistent backup/restore evidence remains UNKNOWN and an operations obligation; operation-specific schema/storage, compaction, retention and R2 gates retain their stated scope. No deployment, recovery, native-image/RAM/GB improvement, production incident attribution, native actor acceptance or issue #60 closure is inferred. See the integration receipt for evidence provenance and pending gates.

[Current integration evidence and OPEN gates](receipts/2026-11-10-webui-thumbnail-master-integration.md).

## Historical source record

**Goal:** make thumbnail work follow visible/user demand with bounded application-owned bytes and request lifetimes, preserving image viewing, downloads, history and receipts. This is WEB-MEM-3 within issue #60; the reported multi-GB incident is not yet attributed.

**Base:** isolated fix/webui-thread-memory from 3d06953f. **Design:** [reviewed interfaces and invariants](2026-11-09-webui-thumbnail-memory-design.md). Its proposed/unrun labels record the design snapshot; this plan tracks execution. Independent design review found three Important contract gaps, and fix1 re-review accepted all three with C0/I0/M0. Source does not establish a whole-tab/native RAM ceiling.

## Amendment 2026-11-09 — Task 1 review correction

R-M36: the all-files embedding test measured 689,764 bytes after the opt-in transport, 1,636 above 672 KiB. Apply the standing minimum 16 KiB step to 688 KiB with dated history and the complete measurement/assertion retained.

R-M37: independent review found that a schema-valid error envelope can contain a non-coercible `retry_after_seconds` object. A narrowly guarded coercion in shared `httpError` is permitted so this malformed detail yields the ordinary typed API error and preserves the envelope. Valid existing normalization remains unchanged; add a real ordinary-JSON regression as well as thumbnail cases. This is an explicit exception to the initial no-change boundary for malformed ordinary JSON error normalization. The separate unsupported-error-body cancellation defect and abort-during-cleanup coverage are in the same test-first fix round.

## Global Constraints

- Explicit GPT-6.1-Sol implementation/review workers; independent review, durable reports and parent-owned RED/GREEN gates.
- All tests/builds/browser on x-boss, aggregate two jobs; use private temporary homes, physical owned TMPDIR and synthetic fixtures. No real configuration, browser session, peer, thread or private data.
- Keep the frozen native release candidate unchanged. Source preparation here grants no installation, master merge or deployment. Final integration follows existing release dependencies and live approval boundaries.
- Preserve history/matrix access, anchors, unread/dwell receipts, drafts, R2 chips, original downloads, image names and keyboard/viewer focus. Do not lower history caps or remove images to improve measurements.
- Bound both successful and HTTP-error thumbnail reads. Never free cancelled-but-unsettled quota early. One lightweight admission owner survives cache generations within a document; payloads/URLs/DOM remain local.
- Changes to existing blob/download/R2 behavior require an explicit demonstrated need and ruling. No global payload cache or retry storm; no implementation counters/budgets in user copy.
- Candidate limits, exact interfaces and browser capability assumptions live in the companion design. They are logical application bounds, not claims about native decoded images, independent tabs or total browser RAM.

## Review Focus

1. HTTP 401/503 and malformed/oversized bodies cannot bypass byte ownership; authentication notification and error precedence remain correct.
2. Delayed, rejected or abort-ignoring reads/cancellations retain ownership until actual settlement, across repeated mounts and same-hash successors.
3. Pending explicit viewer work is bounded, cancellable and admitted fairly after real capacity returns; no premature accounting or silent permanent loss of access.
4. Visibility, duplicate eligibility, atomic redraw, detach-before-revoke, physical source limits and viewer/focus transfer preserve actual UI behavior without full-thread redraw storms.
5. Matched synthetic before/after profiling and functional browser gates distinguish JS, DOM, URL-backed bytes and native process memory; no multi-GB closure from unit counters alone.

## Task 1: Opt-in bounded thumbnail transport

**Files:** internal/ui/static/api.js; internal/ui/jstest/api.test.mjs.
**Consumes:** existing createApi authenticated request and APIError/httpError behavior, with existing blob/download compatibility tests.
**Produces:** api.thumbnailBlob(sha, { signal, maxBytes }) returning an allowed typed raster Blob under the reviewed success/error read and cleanup contract. R-M35: maxBytes is required, a positive safe integer within the design ceiling; invalid values reject RangeError before fetch. No automatic thumbnail caller is activated yet.

- [x] Append real-createApi failing regressions under WEB-MEM-3 T1, preserving original assertions and actual abort/read/cancel provenance.
- [x] Parent observes genuine RED on x-boss, including existing API controls; record actual exits, raw hashes and exact source.
- [x] Implement only the opt-in transport and required private helpers. Keep existing blob/download/R2 behavior intact.
- [x] Parent runs focused GREEN, full affected UI tests and embedding checks; independent source review and fixes pass.
- [x] Commit this independently reviewable transport dependency without claiming the whole memory issue is fixed.

## Task 2: Shared admission ownership and mount-local payload cache

R-M38 (2026-11-09): split Task 2a pure shared budget and new dedicated thumbs-budget.test.mjs from Task 2b mount-local cache integration. The exact accepted interfaces and 18 budget regression contracts are in [the budget plan](2026-11-09-webui-thumbnail-budget.md). Existing constructors and DOM callers remain unchanged until their complete ownership transition.

- [x] Task2a pure shared admission budget: focused26/fullUI522/Go10 pass; independent Sol source PASS C0/I0/M2. Coverage minors and remaining cache/DOM obligations are recorded in [the component receipt](receipts/2026-11-09-thumbnail-budget.md).
- [x] Task2b1 opt-in cache core: 28 focused/542 full UI/7 Go embedding passes; independent Sol core and cap reviews C0/I0/M0. [Component receipt](receipts/2026-11-09-thumbnail-cache-core.md).
- [x] Task2b2 owner transfer/retry: 34 focused/548 full UI/7 Go embedding passes and fresh independent Sol C0/I0/M0. [Extension receipt](receipts/2026-11-09-thumbnail-cache-transfer.md). No caller activation. Task3 cleanup recovery reachability is a separately recorded design dependency.
- [x] Task2b3 cleanup recovery owner: 59 focused/555 full UI/7 Go embedding passes and fresh independent Sol C0/I0/M0. [Dependency receipt](receipts/2026-11-09-thumbnail-cleanup-owner.md). A bounded per-budget resource pool preserves original charges after retired unpublished-URL cleanup failures; Task3 caller activation and physical memory proof remain pending.

**Files:** internal/ui/static/thumbs-budget.js; internal/ui/static/thumbs-model.js; internal/ui/jstest/thumbs.test.mjs.
**Consumes:** Task 1 promise settlement/error semantics. **Produces:** the exact budget tickets, source permits and cache acquire/move/drop/retry/broken/release interfaces in the design; no incompatible partial DOM activation.

- [x] Write named RED tests for two/many caches sharing one budget before old calls drain, byte/entry/request/queue/source bounds, duplicate eligibility, stale generations and fair explicit admission.
- [x] Observe RED, implement minimum ownership model, then parent GREEN/full relevant suites and independent review.
- [x] Preserve original cache eligibility and current callers until Task 3 can switch their complete ownership protocol together.

## Task 3: Atomic thumbnail, viewer and focus integration

**Files:** internal/ui/static/thumbs.js; internal/ui/static/thread.js; internal/ui/static/thread.css; internal/ui/jstest/thumbs-lifecycle.test.mjs; scripts/ui/att-thread.mjs.
**Consumes:** Tasks 1–2 interfaces. **Produces:** demand-driven thumbnail registration, local completion updates, bounded physical image consumers and correct viewer/focus lifetime as one integrated change.

- [ ] Reproduce offscreen eager requests, unmount/late callback ownership, redraw transfer and viewer pressure/focus failures at real seams before implementation.
- [ ] Implement visibility/admission and local updates with detach-before-revoke, document-lifetime quota and explicit waiting/cancel/retry behavior. Preserve receipts and history semantics.
- [ ] Parent runs focused/full UI and relevant Go/launcher/protocol checks; independent review, real browser functional scenarios and required screenshots pass.
- [ ] Profile matched text-only/distinct-image/repeated-hash/R2 controls, scrolling, redraw, viewer and route churn. Record source pins, actual peaks/plateaus and instrumentation gaps.
- [ ] Review the whole component, integrate serially into the authorized release, run combined mandatory gates, deploy/smoke only under the existing release authority, and retain unresolved incident attribution.

## Handoffs and completion limits

The dashboard e709f47d and shared event-pump c085377f components are separate published dependencies; do not reimplement them. WEB-MEM-4 parsed-markdown memo byte limits were separate work at this historical checkpoint; WEB-MEM-1/2/4 are now integrated through PR63 on master and deployed per the parent brief. History pagination/windowing remains separate work. Intrinsic pixels/animation and browser native caches may require another measured slice. Issue #60 stays open until the requested memory behavior is demonstrated in the combined installed product.

## Self-review notes

The transport step is a necessary dependency, not a substitute for bounded UI ownership. Three design review gaps are explicitly resolved in the companion contracts. Runtime reduction, actual incident cause, supported-browser execution, release integration and deployment remain unverified.

--- SUMMARY ---

Bound thumbnail success/error transport, retain quota across mount generations, then switch DOM/viewer ownership atomically and prove useful behavior with matched browser measurements. Preserve the full memory objective and all outstanding release tasks.
