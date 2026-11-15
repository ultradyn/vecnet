Spec PASS
Quality PASS

Independent whole-change review of GitHub #51's bounded retention-warning-label slice, BASE 9dc1730c9b98bf4b62d1536f87ec1cd8e9cdccbf through f407edaab3ea8ba25686c4d6130ca2b966ac4846. Reviewed all six changed source/test/receipt paths using the supplied full-range diff, brief.md, report.md, controller-evidence.json, focused source context, retained raw logs/manifests, Git objects and actual screenshot pixels. Prior review verdicts were treated as claims requiring independent evidence. This verdict covers this checklist slice, not the whole issue.

## Findings

No findings. Critical: none. Important: none. Minor: none. No accepted finding, including a minor, is deferred.

## Spec audit

Missing: none. Extra: none. Misunderstood: none.

internal/ui/static/settings.js:515 prefixes each separate retention_shortened explanation with the existing human labels resolved from that risk's own keys. It preserves the original explanation and the separate Hub paragraph at :564. No dependency, refactor, CSS redesign, backend/policy change or rollout change appears in the full range. Git tree comparison confirms that the only changes after 3338187049a6dfca5445d219b48200c32747b1f9 are the three Markdown receipts.

## Correctness and safe rendering

- Metadata compatibility: internal/prefs/types.go:344 permits multiple string keys. internal/hub/preferences_preview.go:158 and :174 currently emit separate one-key shortening risks. settings-model.js:359 does not validate keys, so the Array.isArray guard at settings.js:516 is necessary. Missing, null and non-array metadata retain the prior generic sentence. All available known labels are joined; unknown keys and malformed array members cannot accidentally resolve through coercion because settings-model.js:394 uses strict key equality. Snapshot labels are strings under settings-model.js:220. These conclusions about null and malformed array members follow from source inspection; the added subtests do not explicitly enumerate every malformed value.
- Rendering: settings.js:564 passes the combined explanation through h's text property. internal/ui/static/dom.js:15 assigns cleaned textContent, so labels introduce no HTML parsing or new DOM sink. The Hub paragraph remains unchanged. The mounted markup-like-label case is supported by inspecting that real sink and the retained passing TestUIStaticNoHTMLSinks; its fake-DOM no-image assertion alone would not establish real-browser HTML safety.
- Association: scripts/ui/prefs.mjs:294 compares each rendered card with its corresponding preview risk's keys and independently fetched Hub labels; :295 rejects the other setting's label. Multiple-key behavior is asserted separately in internal/ui/jstest/settings.test.mjs:1532. Tests assert visible text and requests, rather than production source strings.

## Review-focus audit and regressions

ET1 PASS — scripts/ui/prefs.mjs:291 requires two separate cards; :294 verifies the correct label and :296 compares the original Hub message exactly. Calls at :369 and :390 cover prospective and existing-data previews. The view loop at :314 and viewport loop at :286 produce eight combinations and sixteen card checks. The new mounted cases at internal/ui/jstest/settings.test.mjs:1532 cover multiple known keys mixed with an unknown key, absent metadata, non-array metadata and markup-like label text. Assertions at :1551 preserve labels, :1552 preserve the original explanation, :1555 preserve Hub text and :1557 forbid a preview PATCH.

ET2 PASS — scripts/ui/prefs.mjs:13 fixes 1440x900 and 390x844. :298 checks dialog, form and each risk card for horizontal overflow; :301 fails on overflow; :309 invokes the page-width check at :50. Actual inspection of all eight warning-focused images below found readable separate labels and wrapped Hub prose with no visible horizontal clipping. The retained full browser GREEN reaches its final success message and exit 0 after these assertions.

ET3 PASS — scripts/ui/prefs.mjs:356 binds previews to revision/ETag; :357 preserves the draft; :358 requires a token; :359 rejects preview writes. :364 and :367 assert default-off existing-data approval, :368 asserts initial Cancel focus, and :370–373 cancel through Escape without writes or losing edits. :377 requires a separate retrospective preview/token; :385–387 assert typed-trim gating; :389 checks token absence from browser storage/URL; :393–396 require one PATCH with the exact confirmation token, apply_existing, If-Match and draft. :398–403 verify saved values/revision and reject replay. All these existing assertions remain present in the full diff and were traversed by the retained GREEN journey. The production change only changes explanatory text; it introduces no save/cancel/focus or rerender/retry state mutation.

## Raw evidence and receipt audit

No test, build or browser run was performed for this review. No unanswered concrete concern required a new gated probe. Results below describe retained executions, not fresh suite runs.

| Raw artifact under logs/ | Independently observed result |
| --- | --- |
| ui-red-labels.log and .exit | Exit 1 from the actual missing acknowledged-event setting-label assertion at prefs.mjs:294; 50 PNGs retained. |
| node-red-labels.log and .exit | Exit 1; 5 counted, 2 pass, 3 fail, 0 skipped. Multiple-key and markup-like-label leaf tests fail for missing labels; the failed parent contributes the third failure. Missing/non-array cases already pass. |
| ui-green-labels.log and .exit | Full Settings success, including retention and revision replay; exit 0; 106 PNGs retained. |
| node-green-labels.log and .exit | 646 pass, 0 fail/cancelled/skipped/todo; all four added leaf cases explicitly execute and pass. |
| go-green-labels.log and .exit | 10 named tests and 2 packages pass, no fail/skip; exit 0. Includes static safe-sink and affected embedded UI/HTTP checks. |
| ui-baseline.log and .exit | 641 pass, 0 fail/cancelled/skipped/todo; exit 0. |

Genuine RED then GREEN: each of the six gate manifests contains 2,252 entries and is byte-identical before/after its gate. Baseline-to-browser-RED manifest differences contain only scripts/ui/prefs.mjs. Both RED manifests retain BASE settings.js SHA256 581ec28a76dd3b906f726e3f4ef58a34c4f9055ceda4e1f9e13fd21d23ae61a4, independently matched to Git blob 74a47c3ce1091bc8ead33082f29e4d11467aeccc. Browser RED already uses the final browser-test bytes. Focused Node RED already uses the final mounted tests; its manifest differs from Node GREEN only in settings.js. All three GREEN manifests are identical, match the current 2,252 manifested files, and their three changed source/test hashes match the actual committed blobs at 33381870 and f407edaa. Thus the failure is meaningful, the final behavior is exercised, and no silently dropped case or truncated summary is being accepted as success.

controller-evidence.json's counts agree with raw results. All 156 screenshot hashes match its recorded SHA256 values, and directory counts are 50 RED / 106 GREEN. The three receipt files match their f407edaa blobs. Their counts, RED/GREEN, scope and no-executable-change claims are supported. The integration gate and external evidence archival are stated as remaining controller work, not falsely reported as completed.

Gate provenance: retained logs name x-boss, the isolated lane, jobs=2 and the physical private TMPDIR/umask0022 wrapper. bin/gate.sh serializes with flock and supplies GOMAXPROCS=2/CGO_ENABLED=0. The focused run.sh/remote.sh inspection confirms filtered forwarding and env -i with lane-specific HOME/XDG paths. scripts/ui/lib.mjs:213 creates fresh temporary demo data, :214 binds loopback, and :327 creates a fresh browser context. No real credential/runtime access or execution was needed for this review.

Preserved warning: node-green-labels.log:11 and ui-baseline.log:11 emit “ExperimentalWarning: The MockTimers API is an experimental feature and might change at any time”, followed by “(Use `node --trace-warnings ...` to show where the warning was created)”. This disclosed baseline warning is unchanged. The expected RED assertion failures and recipe exit-1 messages remain failures; neither is recast as a passing run. No additional warning was observed in the retained gate logs.

## Images actually inspected and visual limits

Used view_image to inspect these ten original PNGs under shots/green/:

1. prefs-list-retention-confirm-warnings-desk.png
2. prefs-list-retention-confirm-warnings-phone.png
3. prefs-list-retention-existing-confirm-warnings-desk.png
4. prefs-list-retention-existing-confirm-warnings-phone.png
5. prefs-policy-retention-confirm-warnings-desk.png
6. prefs-policy-retention-confirm-warnings-phone.png
7. prefs-policy-retention-existing-confirm-warnings-desk.png
8. prefs-policy-retention-existing-confirm-warnings-phone.png
9. prefs-list-retention-existing-confirm-phone.png
10. prefs-policy-retention-existing-confirm-phone.png

These show both view modes, both sizes and prospective/existing-data warnings. The two lower phone captures show impact fields, filled trim input and Cancel/Apply controls. Both shortening labels and their Hub paragraphs are readable and remain within the dialog width. Tall dialogs scroll vertically: some titles, tables or lower controls lie outside individual frames; the Policy existing-data desktop frame is already scrolled. This limits simultaneous whole-dialog visibility. Still images do not establish keyboard behavior, animation, all scroll positions, arbitrary labels or other viewport sizes; lifecycle support comes from source and retained browser assertions. No RED retention-confirmation screenshot is claimed, because its missing-label assertion failed before capture.

Implementation, index, HEAD and branches were not mutated. No children, attn, GitHub writes, merge/push, tests/builds/browser runs or real runtime access were used. This final-review.md is the only review artifact written.
