# Web UI thumbnail current integration — source evidence and pending gates

Refresh: **2026-11-12; ignored candidate only, not installed.** Final source review: **Spec PASS / Quality PASS / C0/I0/M0**. Earlier component runtime gates are attributed below; final combined-tree runtime/functional/protocol/screens acceptance and parent documentation review remain **OPEN**. The parent reviews and installs closing documents after its Go work drains and required acceptance is reconciled. This drafting pass used public source/text/hash/read-only Git and executed no application, tests, builds or runtime probes.

## Current source and review provenance — 2026-11-12

- Selected current HEAD: `1a038e59086e8999929041441931e810af2d95e2`; tree `807b3de221bfe6c49099a17f0960354b6a903407`.
- Reviewed baseline: `124d5c8fd3c373215deb14ce893c8e35b315d3e6`; tree `0b67f1610e10bf74b845727b2e0f9c33463f71a1`. The current-port amendment began at `b067fc9d`; the final review uses the later pinned baseline, not the obsolete `8d2394d7` baseline. A moving local master ref is not a refreshed acceptance baseline.
- Exact delta: 18 paths, +6833/−193, including the 16 transport/budget/caller/embedding paths, `internal/opseed/demo/main.go` public-origin/callback fixture and `scripts/ui/att-thread.mjs` journey. All 18 are byte-equivalent to reviewed predecessor `f71bf0c74f4f809ad7e784d942cc6832c897aa4c`; equivalence is source evidence, not executed final acceptance. F closing docs are outside that reviewed source slice and remain candidates.
- Actual final Sol report: `.scratch/vecnet/integrations/cos-refresh-20261112/web-current-final-review-report.md`, Spec PASS / Quality PASS / C0/I0/M0. It traced the transport, document budget/local cache, viewer, thread/app retirement, preserved PR67 behavior and journey, without executing runtime gates.
- Current static inventory in that source report: 76 files / 749561 bytes, 736KiB cap (753664 bytes), 4103 bytes headroom; 720KiB is short by 12281 bytes. The strict complete-file inventory and CSP/import/asset assertions remain. Shared `scripts/ui/lib.mjs` SHA-256: `441f0098b1f9fcf38318104d7f676b29353437e82d1fbcfe11cadf23d10c4bf5`; this merged-master input is outside the memory delta and must accompany actual final gates.
- Preserve current-master/PR67 guarded storage, hub-down recovery, title/unread retirement, dirty Settings leave guard, draft/send behavior, action/route/phone focus and hidden/modal unread refusal. PR63 already shipped WEB-MEM-1/2/4 basic dashboard/pump/Markdown bounds per the brief; new thumbnail/lifetime work remains unshipped. No protocol/schema change or native helper is shipped by this source branch.

## Earlier runtime evidence — applicability is limited

| Recorded result | Applicable source / evidence | Final combined-tree status |
| --- | --- | --- |
| 637 full UI and 134 focused passes | Parent current-port progress, 2026-11-12 02:32:33, preceding `f71bf0c7`; parent records all three gate input hashes matching | Earlier component evidence; canonical final-tree acceptance OPEN |
| Native actual-app header-time401 PASS | f71 component record; earlier old-candidate app401 r5 also recorded PASS `99db0` on source2221 with unchanged tools and two drains | Narrow case only; broader/final actual-app functional acceptance OPEN |
| Both-arm native reader, all 4 cases PASS | Historical 2026-11-10 progress: BEFORE `4f285`, AFTER `c434`, older helper/source inputs | Historical fixture capability; current baseline/candidate parity OPEN |
| AFTER served API, all 14 cases PASS; BEFORE expected unsupported | Historical 2026-11-10 progress: BEFORE `f693`, AFTER `c487` | Historical API capability; final API/native framing/functional coverage OPEN |

The 637/134 values are retained exactly as parent-recorded counts, without inventing child/semantic totals. The narrow 401 case does not complete pressure, R2, retry recovery, hide/show, focus or lifecycle behavior. Prior failures and the unused diagnostic copy stay intact. No results were replayed, independently runtime-certified or promoted to memory/GB evidence by this refresh.

## Historical 2026-11-10 A–D component evidence

Packet paths below are relative to `.superpowers/sdd/2026-11-10-webui-thumbnail-master/`. A–D publish receipts record exact matching origin/gh component refs at publication; that is historical publication evidence, not a network refresh or current master/deployment verification.

| Slice | Component commit | Parent GREEN | Complete input entries |
| --- | --- | --- | ---: |
| A: transport, two paths | `11ed63afab01cb27e645af04b992e03d55d3814f` | 50 API tests; zero failures/skips/cancellations; actual0/bb0 | 2204 |
| B: admission/cache, six paths | `5058976be6494c071b31a07966e0391da1d97405` | 60 focused tests; zero failures/skips/cancellations; actual0/bb0 | 2209 |
| C: atomic callers/lifetime, seven paths | `7244a683839a52a8e5efff0a1894900b5bb87a9f` | 165 outer passes, including wrapper1, plus 30 distinct child passes; semantic=165-1+30=194; actual0/bb0 | 2212 |
| D: embedding cap/history, one path | `84022d85a4592a17e2d96b74900bd3fba8922881` | Eight embedding/CSP pass events; skips []; actual0/bb0 | 2212 |

Each `task-*-green/parent-verification.json` (A: `task-a-api-green/`, D: `task-d-embedding-green/`) records source verification and two empty exact-owned drains. C's `task-c-component-acceptance.json` records COMPONENT_READY_FOR_REVIEW, the child totals and source review, explicitly without native memory acceptance. These historical focused gates precede the journey and final F installation; they are not current canonical full-UI or whole-R acceptance.

| Historical parent gate | Raw SHA-256 |
| --- | --- |
| A API GREEN | `7170bc19fa55b87f77fcd8474ae8a78a9932a342128667d870e6ee9cb193549c` |
| B focused GREEN | `9c898390bbac52225fcebcab37008f338d00c1e0e192a6642e6d671ea9a9ac74` |
| C focused GREEN | `7fffcda0f9ea9cf2b774c277c8c647b77956359d53fbcbd166d2ad8bf17f0ab9` |
| D embedding/CSP GREEN | `76a961010ea0895b75c4a9db38f627bd2811fbf173a6649302b55a075b3e350c` |
| A API RED | `a02cdbef1ab543bfdf141efcd1f0d1f62f7eae8b29f688fcb5be3d635bdb67c6` |
| B prerequisite RED | `5cb4a25c44c4320aba6650f1822a0452cb752e0363762a13e2bb3963b4a9dc7e` |
| C caller RED | `e4dba5a78ecb95dc4d45452ac52b1d728f07d4b45185de91b56bf55d0709d78e` |
| D size-cap RED | `25eca0352891b6d0fba3d1cc1cca63bc2dd60ad756000b323a292218feeb04f5` |

RED interpretation matters: A observed 24 existing controls passing and 26 missing-method failures, not every loop's behavioral assertion. B observed eight old controls and four module-prerequisite file failures; new behavior did not execute. C observed actual behavior failures: outer165/pass8/fail157, roots3/29, child30/pass0/fail30. Its first retained-view drain failure precedes reset cascades; this is not thirty independent defect proofs. Each RED had actual1/bb1 and parent-verified source/drains.

D's `TestUIUnderSizeCap` genuinely failed at 729336 > 671744 bytes (656KiB), actual1/bb1; the parent then accepted R-WEB-D1 for exactly the cap/history hunk, +4/−1. The all-files 76-file inventory implies the smallest 16KiB step: 720KiB = 737280 bytes. At that milestone, `internal/httpapi/ui_test.go` declared that cap, SHA-256 `24a1231fa38de8908d70853548d5854e924ba44ccc1d50c9a83baf1d5d409a00`. The complete inventory/strict-sum/CSP/import checks remain intact. S's 750580-byte inventory/736KiB cap is historical and was not transplanted. D's scoped GREEN is accepted component evidence; final-tree remeasurement/checks and independent whole-R review covering D remain mandatory.

Independent source reviews `task-a-code-review-report.md`, `task-b-code-review-report.md` and `task-c-code-review-report.md` each report Spec PASS / Quality PASS / C0/I0/M0. The reviewers did not execute gates or presume current GREEN. Exact report SHA-256:

- A: `59e5b0b4cb33c03c880268a8a9d256079c34f2f54ca04e21895b4905fe193e6b`.
- B: `7025c61e2b60a13b7112a84e0fdde54caa8e36acbd67bc375bad5967dd13fbae`.
- C: `0ece6b2855157f2c37817f572ed56aa85d519e0de596f7c5b6c020e9b03fc7ee`.

Parent production verification records exact S after-images: A preserves 2202 other inputs; B preserves 2207 other inputs; C production preserves 2208 other inputs. C test installation separately preserves 2209 old inputs. These records and scoped review do not replace the eventual whole-I review and complete manifest.

## Ownership and behavior contract

One document-lived budget admits work across thumbnail mounts and session/route generations. Each instance keeps payloads, object URLs, leases and physical consumers local. Sharing is within an instance for an eligible hash; duplicate occurrences still require separate physical source permits. Independent tabs have independent budgets.

Default source/model bounds per document are residentBytes+reservedBytes<=33554432, running<=2, running+residentEntries<=16, queued<=16 and source permits<=16. Each request reserves up to 10485760 bytes before dispatch; the error envelope cap is min(8192,maxBytes). These logical bounds exclude native stream buffers, Blob copies, decoded pixels/animation, browser/GPU caches and whole-tab/process RAM.

Only actual connected visible demand, or explicit viewer intent, requests images. A hidden document drops automatic sources while preserving an explicit viewer. Completions patch affected local owners. Offscreen sources detach and last-owner URLs retire earlier than route exit. Viewer waiting/loading/error states expose Retry, Cancel/Close and Files, with focus restored to the exact eligible attachment occurrence; Files opens/focuses the existing panel without an automatic download. Failed eligible rasters retain deferred image buttons; SVG/reference-only/oversized/R2 controls retain chips. These are source/model contracts; actual desktop/narrow layouts, focus and native dialogs await E.

Cancellation is a request, not settlement: running tickets remain charged through actual fetch/read/cancel completion, including ignored abort after retirement. Header-time401 notification can initiate sign-out before a held body finishes; bounded503 detail awaits its real terminal read. Explicit priority provides ordering when capacity is available, not immediate progress under stalled requests.

Physical src/listener detachment precedes revoke/drop. Failed published cleanup stays reachable through the same release owner; failed unpublished cleanup retains original-ticket debt in a bounded per-budget pool and is reached by an explicit later viewer Retry. Partial cleanup progress survives failures. Thread/app replacement and sign-out complete only after successful owned cleanup, preserving a public retry path and original avatar/invitation authority. This proves neither native collection nor recovery of any real attachment or incident.

## Historical source receipts and selected exceptions

The eight source design/plan/receipts retain their historical source milestone results and pins, with the current 2026-11-12 integration amendment. Earlier pending/legacy/no-app-edit statements do not describe current C. The one stale WEB-MEM-4 handoff is updated to identify PR63/master/deployment per the brief; source numerical results are unchanged.

Source Task3 parent evidence cited in the approved plan includes focused273 semantic (244-1+30), fullUI754 semantic (725-1+30), embedding7, source2166 and verified drains. Raw hashes respectively: `bba61e342951972488d1b8bbad8b7d28d288e26dc0a11e020541a990f7643431`, `81c3117775b8c9089fbe9c1ac584731d020484093eb1a6e15dc65bcd8dcbe4ef`, `51bd50905366049ff68e9f93b2c6941fd9bd287847300f6e2876fc6139724241`. These remain historical S-branch evidence. They are not today's integrated test counts, an embedding result for M+A/B/C, or native-browser acceptance.

Selection includes the narrow guarded retry_after_seconds coercion fix in shared HTTP error normalization, pure hasConcurrencySlot, and atomic app lifetime gating. The historical port preserved the 675-line app helper plus 44-line wrapper. Current integration adds only three PR67 fixture lines to that helper; the wrapper still requires exactly 30 successful unique children. Discovery and original avatar ownership remain preserved. Historical source delta budgets are not reset by this port. D implements only the reconciled dated embedding-cap history/constant update. The journey is committed in the current 18-path source slice; E-memory helpers/runners remain ignored, with scoped review/checks required before any acceptance use. No new wire/API/debug surface is authorized by these docs.

## Current pending gates and R-WEB-F1 release obligations — OPEN

The parent accepted R-WEB-F1 in `progress.md` after the source-only `task-f-release-slice-assessment.md`. This changes sequencing only: **E-functional is a release gate; E-memory remains a mandatory OPEN goal-completion obligation.** It does not waive functional safety, integrated checks or proof thresholds, and does not imply release readiness. R-DEPLOY-CURRENT-1 retires the obsolete pre35 blanket hold for ordinary no-new-migration releases; backup/restore operations evidence remains UNKNOWN.

- **E-functional:** independently review/run the corrected journey and require both-arm native-fetch pending-body, release/cancel/abort/destroy and reader/request/socket drains. AFTER additionally needs its exact served API: header-time401 and actual app sign-out/credential removal before body release; bounded503 detail; K/K+1 and M/M+1 limits; framing outcomes. Native-inexpressible framing remains explicitly model-only/OPEN. A pure helper PASS or buffered/pre-header fixture is insufficient.
- **Actual app behavior:** demanded/offscreen/actually hidden states; stable admission under observer chatter; waiting/loading/error, one Retry, Cancel/Close/Files; exact duplicate-occurrence focus through redraw and Escape/outside/Close; no stale writes after successful route/session exit; source/URL retirement. Retain history/anchors, unread dwell/hidden/modal refusal, drafts/idempotent send, dashboard Older/Newer/Latest, Matrix/Files/inert-original downloads and SVG/reference/R2 refusal. Exercise actual hide/show, reload, sign-out/in and tab close/reopen at desktop1440×900 and narrow390×844/reduced motion. The 467-line journey does not by itself cover or prove this entire list.
- **Final integration:** freeze R commit/tree and complete product/harness/browser/build/served-asset manifests, refresh both master remotes via the parent workflow and reconcile any new base. Final Go terminal acceptance remains OPEN in this receipt; no concurrent parent result is inferred. Canonical full UI discovery (outer, wrapper1, all child30 and semantic=outer−1+30), final embedding/CSP, complete protocol examples/blob fixtures, full ui-shots/index and actual visual inspection, and acceptance of the final installed documentation are OPEN. The actual final source-only Sol review already passed C0/I0/M0; any later combined-tree change needs applicability review and affected reruns. Keep applicable vet/format/module/build/ISA checks; record launcher gate applicability and justified skips separately. No skipped check is PASS; fixes require re-pins and affected reruns.
- **Owned execution/evidence:** retain the parent-isolated x-boss envelope, global TWO resource ceiling and exclusive TWO native allocation, exact inputs, bounded fixtures/output, identity-checked children/roots, actual exits and two empty owned drains. Source-pinned tools used for acceptance need scoped review and meaningful checks; unpromoted E modules must not enter ui-shots as an INCOMPLETE default CLI or be hidden by a filter.
- **Merge/deployment:** new thumbnail/lifetime work remains unshipped. R-DEPLOY-CURRENT-1 permits the ordinary authorized no-new-migration release after mandatory gates: exact tested/reviewed target, no-new-migration manifest, current metadata/compatible rollback artifact, serialized deployment, public health/assets and affected-feature smoke. It retires the old pre35 blanket hold; historical consistent backup/restore evidence remains UNKNOWN and current acquisition is an operations task. Operation-specific schema/storage, compaction, retention and R2 gates still apply to those operations. A provider snapshot is separate recovery evidence, never verified consistent backup/restore. This refresh verifies no live deployment or backup, changes no storage policy/identity/volume and authorizes no old snapshot restoration. Native actor acceptance and attachment recovery remain outside this source proof.

## Mandatory E-memory follow-through — parent CoS owns completion

The original goal to stop multi-GB Web UI use remains ACTIVE. Parent CoS owns the durable OPEN obligation and the next bounded source/probe handoff; this receipt transfers no responsibility to an unnamed future worker. Accepted pilot/pure components, including T1 launch/provenance and T7 control predicates, do not complete their whole native stages. The remaining T1–T8 obligations are:

| Stage | Remaining native/integration proof |
| --- | --- |
| T1 | Owned headed Chromium/Xvfb entry, exact product/browser/served pins, complete PID/target/isolate census and safe capability refusal. |
| T2 | Same-origin parity, real body streaming on both arms, strict AFTER API/app pilots, actual terminal/cancellation/socket drains. |
| T3 | Separate native-image allocator diagnostics with real paths/units/PID attribution; unsupported or ambiguous data leaves acceptance OPEN. |
| T4 | Identical finite synthetic seeds and six fresh arms in three paired repetitions: BEFORE/AFTER, AFTER/BEFORE, BEFORE/AFTER. |
| T5 | Fixed real-app actions and visibility, 10 warm + 50 measured cycles without reload, quiescent checkpoints10/20/30/40/50. |
| T6 | Pressure/cancellation/safety and finite functional resets, retaining duplicate focus, history/unread/draft/download controls. |
| T7 | Bounded resource/evidence guards, every-repeat plateaus, R-WEB-E6 per-checkpoint text/repeated controls and honest paired metric decisions. |
| T8 | Retained cleanup owners, observed reader/socket/process drains, identity-checked disposal and complete closeout evidence. |

Keep three separate matched diagnostic pairs, no forced GC in main plateau runs, and the fixed metric-specific plateau tolerances in `plan.md`. R-WEB-E6 checks every checkpoint of each of three pairs for text and repeated controls against max(5% of BEFORE checkpoint10, metric floor): 16MiB renderer RSS/PSS, 2MiB heap, 4MiB supported native-image. Missing/nonfinite data is INCOMPLETE; unsupported native-image stays OPEN. Publish raw values/slopes/differences/spread; a reduction requires all pairs supporting that named metric/workload with effect exceeding observed spread. RSS minus heap and pixels×4 are not measured native-image bytes. Preserve the existing 90-minute schedule, per-arm15min and resource/evidence bounds; changing release timing changes none of these criteria.

**Next bounded proof:** parent pins/reviews only the necessary driver/fixture inputs, first proves owned native entry and both-arm raw-reader/proxy streaming with real cancellation/drains, then the served AFTER API/app and corrected-journey functional cases. Missing wiring is a blocker, not presumed implemented by pure component passes. Complete release-functional gates before shipping; continue T1–T8's matched memory experiment as mandatory goal work. The broader 200/1000/10000-history × 1/20/100-image/pixel/animation study and production RAM/incident diagnosis remain OPEN. No browser-RAM ceiling, GB/native improvement, incident attribution/resolution or issue #60 closure follows from this bounded resource-hygiene release.

## Historical port identity provenance — 2026-11-10 only

The following identity notes preserve the earlier component checkpoint. Every use of “current”, “dirty”, “not yet pinned”, “17 paths” or “28 paths” in this historical excerpt refers to that checkpoint, not the 2026-11-12 candidate above. Its identities, cap and shared-library pin are superseded for current acceptance; no historical runtime result is rewritten.

- Master base M: `8d2394d7896edacd4eadd437ca616a2243728fe3`.
- Selected cumulative source S: `fc1f8b79ff096fafff616793e50ae23a41d94fac`; extraction base P: `3d06953f21299b268d84649ea3a488b62807ddad`.
- Actual M/S merge base: `6bca9d61404197b44be00851135ede6cde4ec886`; P is not that merge base. Selection ports explicit after-images without S ancestry or its unrelated/native changes.
- Current product HEAD: D `84022d85a4592a17e2d96b74900bd3fba8922881`, tree `2f46cde39a9613b9955099677c3f3f26bfdb7bee`, exactly A–D's 16 paths relative to M. All 15 A–C after-images match selected S; D is the selective master cap/history amendment, not S's embedding file.
- Sole dirty tracked path: `scripts/ui/att-thread.mjs`, 467 readable lines / +140−32, SHA-256 `0823ef93240442704ac87f68754d9a9f383344a7a41f3b069fddd19a6abd6b9a`. Its source draft corrects the hostile-raster oracle, pre-module URL instrumentation, viewport demand, Retry/Files/Cancel and occurrence focus; no browser PASS follows. `task-f-functional-journey-report.md` lists coverage still owed. Shared `scripts/ui/lib.mjs` remains `b86eb1ebf38f0ccb6ec066c84c656667ef736d3bc4798238d627227b6970b924`.
- Final release R commit/tree/full inputs and served-asset manifests are **not yet pinned or accepted**. The original plan's final I/native AFTER identity likewise remains unresolved; neither S nor current component HEAD substitutes for final acceptance. R-WEB-F1 selects A–D + this corrected journey + exact 11 F paths: 28 tracked paths relative to M when installed, before any separately reviewed fix. Current tracked delta is 17 paths; F remains ignored. No `thumbs-memory*.mjs` candidate is promoted into the release.
- WEB-MEM-1/2/4 dashboard/pump/Markdown fixes are already integrated by PR63 on master and deployed according to the parent brief. Production state was not queried here. Keep those shipped fixes and regression tests intact.
- Current C source review records unchanged protected dashboard/chart/store/Markdown, Settings/TTL/leave guards, invitations/peer UI, route35/migration0035, OAuth, launcher and discovery/index inputs. No backend, schema, protocol, connector/native, shared fixture or dependency change is selected. This is scoped source preservation, not a fresh remote-master or deployment verification.

