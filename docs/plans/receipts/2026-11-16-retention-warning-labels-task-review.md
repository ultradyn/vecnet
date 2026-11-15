Spec PASS
Quality PASS

Independent read-only review of GitHub #51's retention-warning-label slice, full BASE 9dc1730c9b98bf4b62d1536f87ec1cd8e9cdccbf..HEAD 3338187049a6dfca5445d219b48200c32747b1f9. Reviewed the supplied full-range diff, brief.md, report.md and controller-evidence.json; checked claims against raw gate logs, source manifests, actual source context and actual screenshot pixels. No implementation, index, HEAD or branch changes; no children, shell commands, tests/builds/browser runs, runtime/credential access or external writes. This report is the only file written.

## Findings

No findings. Critical: none. Important: none. Minor: none. No accepted minors are deferred.

## Spec audit

Missing: none. Extra: none. Misunderstood: none within this checklist slice; this verdict does not close all of #51.

internal/ui/static/settings.js:515 adds only the setting-label prefix to retention_shortened explanatory text. It resolves every available label through store.rowOf, retains separate risk cards and the original explanatory sentence, and leaves the Hub paragraph untouched at :564. The diff contains only this production change, browser assertions/screenshots and focused mounted tests; no dependency, CSS redesign, refactor, backend, policy or rollout change.

## Review-focus audit and regressions

- ET1 PASS — scripts/ui/prefs.mjs:291 requires two separate shortening cards; :294 asserts each preview key's independently fetched human setting label; :295 rejects the unrelated setting's label; :296 compares the original Hub message exactly. Calls at :369 and :390 exercise prospective and existing-data previews in both views. The viewport loop at :286 repeats these assertions for both sizes: eight combinations, sixteen card checks. These are observable browser behavior assertions, not source-string checks.
- ET2 PASS — scripts/ui/prefs.mjs:13 fixes 1440x900 and 390x844; :298 checks dialog, form and every risk card's scrollWidth against clientWidth; :301 rejects overflow; :309 also invokes the page-width assertion at :50. The retained full GREEN browser journey reached its success message and exit 0. Actual inspection of the eight warning-focused images below found correct, readable labels and wrapped Hub prose within the dialog width, with no visible horizontal clipping.
- ET3 PASS — scripts/ui/prefs.mjs:356 binds preview to revision/ETag; :357 preserves the draft body; :358 requires a token; :359 forbids preview writes. :364 and :367 assert apply-existing is opt-in; :368 checks initial Cancel focus; :370–373 exercise Escape/cancel without losing edits or writing; :377 requires a distinct retrospective preview/token. :385–387 gate approval on typed trim; :389 checks tokens stay out of browser storage/URL; :393–396 assert one PATCH with exact token, apply_existing, If-Match and draft; :398–403 verify the saved revision/values and reject replay. The full GREEN journey covers these existing assertions. The complete production diff changes none of this lifecycle.

Metadata/safe-rendering checks:

- internal/prefs/types.go:344 defines Risk.Keys as []string; internal/hub/preferences_preview.go:158 and :174 currently emit one-key shortening risks. Multiple keys remain permitted by the type and are handled by mapping and joining all known labels at settings.js:516, rather than taking only the first.
- internal/ui/static/settings-model.js:359 does not validate risk.keys. The new Array.isArray guard preserves the previous generic sentence for absent/null/non-array metadata. Unknown keys and malformed array members do not coerce to a setting name: rowOf at :394 uses strict key equality and the renderer uses optional access. Known-key labels come from snapshots requiring string labels at :220. No raw unknown key is invented as a label.
- settings.js:564 supplies both the prefixed sentence and Hub message through h's text attribute; internal/ui/static/dom.js:15 uses textContent. There is no new HTML sink. internal/ui/jstest/settings.test.mjs:1532 mounts the page and asserts output for multiple known keys mixed with an unknown key, missing keys, non-array keys and a markup-like label; :1551 checks labels, :1552–1555 preserve explanation/Hub prose, :1556 rejects image nodes, and :1557 forbids PATCH during preview rendering. The fake-DOM test is supported by inspection of the actual textContent sink and the passing Go static-sink check; it is not claimed as a real-browser hostile-markup probe.

## Raw verification evidence

No suite was rerun, and no unresolved code concern required a new gated probe. Results below are independently checked retained evidence, not newly executed tests.

| Gate/raw log under logs/ | Observed result |
| --- | --- |
| ui-red-labels.log | Exit 1: actual browser error “retention-confirm list desk: retention.ack_events warning missing setting label Acknowledged event retention”; 50 PNGs retained. |
| node-red-labels.log | Exit 1; 5 counted, 2 pass / 3 fail / 0 skipped. Two failing leaf cases are multiple keys and markup-like label; their parent accounts for the third failure. Missing/non-array cases already pass. |
| ui-green-labels.log | Full Settings journey success, including retention and revision replay; exit 0; 106 PNGs retained. |
| node-green-labels.log | 646 tests, 646 pass / 0 fail / 0 cancelled / 0 skipped / 0 todo; all four new leaf cases explicitly execute and pass. |
| go-green-labels.log | Go JSON records show 10 named tests passing and 2 packages passing, no fail/skip; exit 0. Includes TestUIStaticNoHTMLSinks and affected embedded UI/HTTP tests. |
| ui-baseline.log | Existing retained baseline: 641 pass / 0 fail / 0 skipped; exit 0. |

Genuine RED then GREEN: all six before/after source manifests are identical within their respective gate, with 2252 entries each. RED .head is BASE and its dirty summary contains only the browser test; settings.js SHA256 is 581ec28a76dd3b906f726e3f4ef58a34c4f9055ceda4e1f9e13fd21d23ae61a4. Reversing the supplied sole production hunk produces exactly that SHA256 and Git blob 74a47c3ce1091bc8ead33082f29e4d11467aeccc, matching the diff's old blob. Browser test hash dc0cf7516d169a9f1a02b381eacccdcc8524609ef20f99acb5fcc2860b8321cd is unchanged from browser RED to GREEN. Focused Node RED likewise precedes the production change. All three current owned files match the GREEN manifests and the diff's new blob IDs bcd0a9c2, f8732ee6 and 6bb101dc. Thus the missing-label RED is an application assertion against unchanged BASE production, not an infrastructure failure or a silently absent test.

Gate provenance: raw logs identify x-boss, jobs=2, and the private physical temporary-directory wrapper with umask 0022. bin/gate.sh serializes with flock and supplies GOMAXPROCS=2/CGO_ENABLED=0. The narrowly inspected scripts/bb/run.sh and remote.sh chain uses a filtered forwarded environment and env -i with lane-specific HOME/XDG paths. No gate was invoked during this review.

Preserved raw warning: node-green-labels.log:11 and ui-baseline.log:11 both emit “ExperimentalWarning: The MockTimers API is an experimental feature and might change at any time”, followed by “(Use node --trace-warnings ... to show where the warning was created)”. Expected RED assertion errors and the resulting recipe exit-1 messages are retained above, not treated as successful runs. GREEN logs end with complete summaries/exit status; no truncated output was treated as success.

## Images actually inspected and limits

Used the available view_image tool to inspect these ten original retained PNGs under shots/green/; their SHA256 values also match controller-evidence.json:

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

These cover both views, both viewport sizes, and prospective/existing-data warnings. The two extra phone captures show lower impact/trim/action controls, including the filled trim input and Cancel/Apply buttons. Warning-focused phone images show both labelled cards and original Hub paragraphs. Tall dialogs scroll vertically; some titles/tables or lower controls are outside an individual capture, and the Policy existing-data desktop capture is already vertically scrolled. That is a visual limitation, not evidence that the entire dialog is simultaneously visible. Still images do not prove keyboard behavior, animation, every scroll position or arbitrary labels/viewport sizes; lifecycle evidence comes from the assertions and raw GREEN journey. No new horizontal clipping is visible in the inspected frames, corroborated by the explicit card/form/dialog/page width checks. No RED warning screenshot is claimed: the browser correctly failed before capturing that confirmation state.
