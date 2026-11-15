# Independent whole-branch review — GitHub #51 Settings pruning report

Spec compliance: PASS
Code quality: PASS
Findings: none (Critical 0 / Important 0 / Minor 0). No accepted finding is deferred.

## Reviewed identity and scope

- BASE: `6902e9de8906a34c6daf5c7386d28a99c90fafc6`
- HEAD: `14e77c17e171c296de0885559271f5b866a88255`
- TREE: `52e5355893169ded90350f890982778ab6a60026`
- Diff SHA256: `6bf01547695ab35efe6f5ad32c975bbbdf20ffc374a1e604cb99f80a3674ff3f`

Verified the refs/tree and supplied diff hash against the actual ten-context-line Git diff. Read the supplied final diff once, the complete plan, frozen issue, both implementation reports including followups, both task reviews, progress R1–R10, visual-inspection.md and current committed receipt. Reports were treated as claims; conclusions below come from changed code/tests, immediate callers and independently parsed raw evidence. No conversational history was used as evidence. The review axes were assessed independently in this reviewer; the operator's no-agent instruction overrides the skill's parallel-agent procedure.

All nine source/test SHA256 entries from final gates match current worktree bytes and committed blobs at both executable commit `e208e271f7e81a7a928ada002d9eefe8a5ebccd2` and reviewed HEAD. The last commit changes only documentation. The pinned diff passes `git diff --check`; tracked worktree was clean. No separate AGENTS.md, CONTRIBUTING.md, CODING_STANDARDS.md or CLAUDE.md was present in the repository search; supplied operator instructions and established surrounding module conventions were applied.

## Spec compliance assessment

### RF1 — actual output-only boolean and preserved projection: PASS

`internal/httpapi/blob_capabilities.go:78` explicitly projects the same revision and `snapshot.Descriptors()` that `prefs.Snapshot.MarshalJSON` emits at `internal/prefs/types.go:168`, adding an actual boolean, including false. It avoids anonymous embedding/custom-marshaler suppression. GET (`internal/httpapi/prefs.go:44`) and PATCH (`:104`) use this same helper. R2 storage retains the same transfer condition, accounting query, output values and sanitized failure; disk still omits storage. Preview and error responses remain separate.

Behavioral pins:

- `internal/httpapi/prefs_runtime_test.go:59`, `TestPreferencesHTTPEventRetentionRuntimeProjection`: disk/R2 × false/true × successful GET/PATCH; explicit presence/type/value, complete descriptors, revision/headers, source/origin/editability, saved values, existing opapi decoding, exact optional storage accounting, storage failures and no synthetic-provider calls.
- `internal/httpapi/prefs_runtime_test.go:238`, `TestPreferencesHTTPEventRetentionRuntimeReadOnly`: top-level runtime injection and dotted SET/RESET rejected for PATCH/preview; operator/session/as-peer/unauthenticated authority and confirmation refusal; Config, revision, descriptors and registry remain unchanged.
- R7 at `internal/httpapi/prefs_test.go:535`, `TestPreferencesHTTPStoredChangeMetadata`: the adapted round-trip at `:590` compares the entire reserialized Snapshot with the complete original revision/preferences projection, including every nested field. Existing literal metadata and save/restart/no-op/reset checks remain; raw runtime is separately required to be an actual startup boolean. No Snapshot/opapi/storage schema was changed.

### RF2 — shared startup ETag and stale preconditions: PASS

`internal/hub/preferences.go:185` adds the named startup boolean to the existing canonical digest. `:320` uses that digest in shared Snapshot ETags, and `:354` enforces the same tag in service preconditions. HTTP conditional GET checks that shared tag before writing the body. There is no HTTP-only suffix or durable revision/generation change.

Behavioral pin: `internal/httpapi/prefs_runtime_test.go:147`, `TestPreferencesHTTPEventRetentionRuntimeRestartValidator`. Both false→true and true→false reconstruct a hub over cloned saved rows: full descriptors, stored override/source and revision remain equal; same-config reconstruction keeps its tag; gate-changing reconstruction changes it. Stale exact/weak/list conditional reads return 200/current boolean and tag, current-tag reads return bodyless 304, stale If-Match returns 412, an old confirmation cannot authorize a fresh-tag write, and a fresh preview/confirmation succeeds.

Hub construction copies Config into the hub (`internal/hub/hub.go:257`) before constructing preferences (`:262`); projection and digest therefore share the same fixed startup input. Production source has no new gate setter. Consumer installation remains independent of the gate in `internal/hub/blob_preferences.go:16`; the scheduled prune guard remains unchanged at `internal/hub/tickers.go:140`. Disabled status neither locks the policy nor permits saving to enable pruning. Unsupported live Config mutation is outside this startup contract.

### RF3 — truthful note, controls and R8/R9: PASS

`internal/ui/static/settings-model.js:226` accepts only an actual boolean in a non-array runtime object, otherwise null; `:231` supplies the exact three required strings. `internal/ui/static/settings.js:159` and `:318` render that shared state beside the acknowledged-event row/lane with the required data attribute. Group, archive, risk and lane text qualify scheduled pruning; R9's archive-on pill says the acknowledged-event policy is unchanged. Zero labels, timing, lane math/stops/positions, history-only pause and controls are unchanged.

Behavioral pins:

- `internal/ui/jstest/settings.test.mjs:1281`: booleans, legacy absence and malformed containers/members; accepted GET/PATCH replaces a known report with not reported without inferring activation from duration.
- `internal/ui/jstest/settings.test.mjs:1312`: all exact notes in both mounted views, filtered List, unchanged source/editability/zero stops/track geometry, conditional copy and preserved preview/cancel/exact confirmed PATCH/If-Match. Saving retains the disabled report.
- `internal/ui/jstest/settings.test.mjs:1532`: R8's restored `presence.send_warning_after` control in List/Policy; wire bounds/default/zero, invalid input refusal, edited→stored provenance, exact writes/If-Match, shared saved value and no advisory confirmation.
- `internal/ui/jstest/settings.test.mjs:1572`: R9 archive-on/gate-disabled pill, history pause, unchanged unpaused ack track/zero stop and draft-only staging.
- `scripts/ui/prefs.mjs:138`: real default-off hub metadata, exact note beside ack, visibility after centering, both views at both sizes; existing `rowsMatchHub` at `:77` still walks every backend row and checks source, availability and controls.

R8 reuses the ordinary Policy token/drawer. Its fixture matches `internal/prefs/registry.go:77`; wording agrees with the successful-send advisory in `internal/hub/recipient_warning.go:16` (zero disables; connected streams count as activity), without making TTL or deletion claims. The fixture's risk predicate names only the four TTL keys rather than every presence preference. The unchanged fallback assertion at `settings.test.mjs:365` still requires PATCH→preview→confirmation independently of client danger metadata. Warning and retention consumers were not edited.

### RF4 — accepted network generations, cached isolation and lifecycle: PASS

Inspected actual network invocations and callers: load `settings-model.js:474`, refresh `:519`, conflict GET `:620`, commit PATCH `:702`. `acceptRuntime` at `:403` updates only the report/barrier. Existing transport (`internal/ui/static/api.js:165`, `:390`) preserves successful metadata, resolves 304 separately and rejects failed/unreadable requests. Validity/live checks precede ingestion. Accepted missing/malformed metadata clears a known report; same-tag 200 and dirty refresh ingest status independently of row reconciliation. An accepted 304 keeps status and advances the barrier. Invalid/failing/discarded responses supply neither report nor barrier.

R6 checks busy after awaiting refresh, including same-tag 200/304. Busy-entry refresh remains meter-only, with no extra read/drain. Report-only changes use the ordinary settings notification; storage-only rendering remains unchanged. Common `adopt` at `:413` has no runtime ingestion, so Undo (`:583`) and adoptNewer (`:756`) cannot replay cached reports. A superseded successful PATCH still calls applySaved and acknowledges values. No general row/error/lifecycle ordering rewrite was introduced.

Behavioral pins:

- `internal/ui/jstest/settings.test.mjs:1387`: dirty/same-tag status, normal notification, 304 retention, cached Undo/adoptNewer, conflict GET ingestion, newer false/missing/304 surviving an older GET, load ordering, and superseded PATCH retaining newer status while acknowledging values, clearing draft and recording saved keys.
- `internal/ui/jstest/settings.test.mjs:1450`: busy-entry meter/confirmation preservation, failed/unreadable refresh, ordinary recovery/PATCH, R6 deferred same-tag 200/304 during confirming and saving (including no barrier advancement), and destroyed load/refresh/PATCH/conflict replies staying inert.

Existing coverage at `settings.test.mjs:103`, `:147`, `:283`, `:337`, `:485`, `:513`, `:870`, `:892`, `:1036` and `:1105` continues to exercise locks, atomic save, conflicts, confirmation/apply-existing authority, storage privacy, meter behavior and reconnect/mount cleanup. Those contracts and their assertions were preserved.

## Code quality assessment

PASS. Production changes remain within existing projection, shared digest, local store and text-rendering boundaries. Parser and note text each have one shared helper; the two counters are local and scoped to the new field. No new dependency, endpoint, preference key, schema, request framework, browser persistence or activation path was added. New tests use the established real HTTP, deferred-promise and mounted-DOM seams. R7 preserves the correct ownership boundary for HTTP-only metadata; R8's fixture correction preserves server-risk independence. No actionable standards violation or maintainability defect was identified; stylistic preferences are not findings.

## Independently inspected validation evidence

Read/parsed raw `.log` results, test diagnostics, terminal Go test/package events or TAP summaries, wrapper `.exit`, build-box exit, `.head` and before/after SHA256 manifests under this workspace's `logs/`. Counts include parent/subtest events. Every inspected before/after manifest is identical; historical bytes remain distinct from final bytes.

| Gate | Test pass / fail / skip | Wrapper / build-box |
| --- | --- | --- |
| baseline-prefs | 5 / 0 / 0 | 0 / 0 |
| task1-red-01; task1-red-02 | each 8 / 8 / 0 | each 1 / 1 |
| task1-green-01 | 16 / 0 / 0 | 0 / 0 |
| task1-affected-01 | HTTP 664 / 3 / 0; hub 938 / 0 / 0; prefs 42 / 0 / 0; opapi 7 / 0 / 0 | 1 / 1 retained |
| task1-context1-focused-01; controller-task1 | each 19 / 0 / 0 | each 0 / 0 |
| task1-context1-http-full-01 | 667 / 0 / 0 | 0 / 0 |
| baseline-ui | 607 / 0 / 0 | 0 / 0 |
| task2-red-01 | 85 / 26 / 0 | 1 / 1 |
| task2-green-01; task2-green-02 | 111 / 0 / 0; 115 / 0 / 0 | each 0 / 0 |
| task2-ui-full-01; task2-ui-embed-01 | 637 / 0 / 0; 2 / 0 / 0 | each 0 / 0 |
| task2-settings-visual; task2-settings-visual-02 | missing Policy warning token; incomplete journeys | each 1 / 1 |
| task2-context1-red-01 | 114 / 5 / 0 | 1 / 1 |
| task2-context1-green-01; task2-context1-ui-full-01 | 118 / 1 / 0; 640 / 1 / 0 | each 1 / 1 |
| task2-context1-green-02; controller-task2 | each 119 / 0 / 0 | each 0 / 0 |
| task2-context1-ui-full-02 | 641 / 0 / 0 (613 top-level) | 0 / 0 |
| task2-context1-ui-embed-01 | 2 / 0 / 0; package PASS | 0 / 0 |
| task2-context1-settings-visual-01 | full Settings journey PASS; 98 PNGs | 0 / 0 |
| controller-ui-http-contract | 8 / 0 / 0; package PASS | 0 / 0 |

Node gates also have zero cancellations. Task1 authoritative RED-02 production hashes equal BASE and runtime-test bytes equal final source: genuine missing booleans, unchanged restart tags, stale 304 and accepted stale If-Match. RED-01's additional unsuitable descriptor oracle was corrected and is not used as feature proof. The three historical affected failures are precisely StoredChangeMetadata/default, /flag and parent; current full HTTP resolves all three. Current affected backend evidence combines HTTP667 with retained identical-production hub938/prefs42/opapi7, totaling 1,654 passing events from separate gates. This is not a newly successful four-package run; original wrapper1 is intact.

Task2 RED production hashes equal BASE; raw failures are missing status/notes. Initial visual failures retain the exact missing `tok-presence.send_warning_after` timeout. Context RED exposes the missing control, advisory false-confirmation and archive-pill copy; its search-list mismatch is a disclosed fixture-oracle adaptation. The first fixture correction's sole focused/full failure is the unchanged 409 fallback assertion. Final corrected focused/full runs and full real-demo journey pass. No historical failure is relabeled, skipped or waived.

Final focused/full UI, Go UI, real-demo and controller manifests match all nine reviewed entries. Backend production/runtime-test entries remain equal to their retained passing gates; the R7 test-only followup is separately pinned. R10's current-source gate directly covers embedded size cap, CSP, static serving, icons and no inline/HTML sinks, resolving the backend/UI integration seam without a broad replay. Raw commands show the serialized x-boss lane, two jobs and physical private TMPDIR/umask isolation wrapper. No new review probe was necessary or requested.

## Visual inspection and limits

Independently opened all four final gate-off PNGs in `visual-context1/`: List and Policy at 1440×900 and 390×844. The exact disabled note is legible beside acknowledged retention, wraps within the viewport and leaves value/source/controls/tracks visible. This agrees with controller inspection. Independently verified all 98 final PNG hashes against the Task2 report inventory, including:

| PNG | SHA256 |
| --- | --- |
| prefs-list-gate-off-desk.png | a3121c385e338f2c9e4c94524eeef25fbe5a822f33825771328e11dd38dacd4b |
| prefs-list-gate-off-phone.png | 9ca88d8c8b45ad76dd7d786a6d81366a0abc2b3529f49535fbd3e2fa22ce8638 |
| prefs-policy-gate-off-desk.png | b2aef56f0c98d5b5a580bd98cff2ff2d4ff570a0d7412f88560c3c02efbf55e1 |
| prefs-policy-gate-off-phone.png | 812c1ae32271552c75cc750410af03041015d4e75e2d44a7b8f5ee289f929abd |

Enabled/not-reported rendering has mounted-DOM coverage, not browser screenshots. This is source acceptance based on inspected source and retained raw runs; I did not execute tests, builds, browsers or agents. Worker health, live gate state, actual deletion, installed release and deployment/activation were not verified or claimed. An unobserved restart and unsupported hot Config mutation remain outside the stated last-reported contract; qualified reports intentionally survive busy/failed reads. Existing general row/error races excluded by the plan were not repaired or promoted into new defects.

No findings require ID/severity/scenario/effect/fix-verification entries. This review file is the sole write; source/index/HEAD/branch/commits and external systems are unchanged. The committed receipt intentionally awaits this verdict. Controller closing receipt/checkbox/review/ledger metadata must preserve the reviewed executable bytes. GitHub #51 remains open for its other performance and rollout items; merge, push, archival and cleanup are controller-owned and not claimed here.

===DONE===
