# GitHub #51 Settings last-reported event-pruning gate — implementation plan

Selected master base `6902e9de8906a34c6daf5c7386d28a99c90fafc6`. Worktree `/home/xertrov/.codex/worktrees/7b63/vecnet`, branch `fix/issue-51-settings-pruning-report`. Spec: GitHub #51 named Settings pruning-gate item, frozen in operational issue-51.json. This is a GitHub task; no Markdown backlog ticket selected. Two serial implementation tasks under PIRFL, with independent task/final review.

## Decision and acceptance

Expose `runtime.event_retention_enabled` through existing operator preferences GET/PATCH responses and bind the startup boolean into the shared preferences ETag. Display a small note in List and Policy identifying what the hub **last reported**. Disabled warns that saving the retention policy does not enable pruning; enabled reports only the gate; missing/malformed metadata says not reported.

This meets #51's Settings acceptance: an editable retention duration no longer implies its scheduled consumer is enabled. It does not add a worker-health claim, continuous monitor, gate control, retention change, rollout step, endpoint or dependency. Preserve saved values, source badges, locks, editing, preview/save/confirmation behavior and optional R2 storage. No zero-policy wording or track semantic redesign. Confirmation-note attribution remains independent.

Alternatives: top-level HTTP metadata uses the existing projection with the least contract churn. Per-descriptor additive metadata expands shared Snapshot plumbing; reusing descriptor `runtime`/`editable` would incorrectly lock policy edits. Static warning or UI-only unconditional GET cannot supply trustworthy false/true/not-reported status and leaves other conditional clients stale after restart.

## Direct source evidence at this HEAD

| Files/functions | Relevant fact |
| --- | --- |
| `internal/hub/hub.go:48-61,102`, `Config`, `DefaultConfig`; `internal/hub/tickers.go:140`, `Tick` | Gate defaults false and controls the slow-tick `PruneEventSlice` call; explicit maintenance calls remain possible. |
| `internal/hub/blob_preferences.go:14`, `newProductionPreferences`; `internal/hub/preferences.go:180-205,319-337` | Retention consumers remain installed/editable regardless of gate. Shared ETag digest includes bootstrap/consumer inputs but omits this boolean. |
| `internal/httpapi/prefs.go`, `adminGetPreferences`, `adminPatchPreferences` | GET checks `If-None-Match` before writing its body; GET/PATCH share the response helper. `no-store` alone cannot prevent stale explicit conditional reads after restart. |
| `internal/httpapi/blob_capabilities.go:78`, `writePreferencesSnapshot` | Disk returns Snapshot directly; transfers add optional storage through an explicit projection. Both branches need the boolean. |
| `internal/opapi/types.go:633`; `internal/prefs/types.go:166-207` | Preferences is a Snapshot alias with custom JSON, not an independent DTO. Decode ignores additive fields after JSON structure checks; reserialization drops HTTP extras. |
| `internal/ui/static/settings-model.js`, `adopt`, `load`, `refresh`, `applySaved`, `failSave`, `undo`, `adoptNewer` | Refresh skips same-tag rows and only refreshes storage while busy; cached conflict bodies can later be adopted. Status must be ingested separately. |
| `internal/ui/static/settings.js:32,401,508`; `settings-model.js:233` | Group, Policy, archive-exit risk text and ack lane suffix contain unconditional pruning claims requiring narrow copy qualification. |

The separately named build-box follow-up is merged at23e97129. This Settings item remains missing by current source inspection; historical receipts and issue deployment statements do not establish runtime flags. The shipped CLI has no assignment enabling this gate (`cmd/vecnet-hub/main.go`; `docs/DEPLOY.md:257`).

## Global constraints

- Operator authorized selection, isolated implementation subagents/PIRFL, independent review/fixes, normal merge/push and verified archive before cleanup. Ignore GitHub1/2; no Markdown backlog candidate selected.
- Exact gpt-6.1-sol/high implementation and review, gpt-6-luna--fast only research. Authenticated CLI workers on x-boss; no child/reviewer spawning by workers or model alias substitution.
- All builds/tests/visuals through serialized x-boss gate, BB_JOBS=2/GOMAXPROCS=2, private physicalTMPDIR, umask0022, isolated synthetic HOME/XDG. No real runtime/config/profile/credentials/messages/services, notifications/attn, deployments or gate activation.
- Preserve policy values, sources, editability, preview/save/confirmation authority, storage projection and all existing async acceptance checks. New network ordering guard affects only new status. No new dependency, preference key, endpoint, CLI gate switch or durable schema.
- T1 owns backend projection/digest, new HTTP regression file and the narrowly adapted existing Snapshot-owned metadata assertion in prefs_test.go (R7). T2 owns only the two Settings JS modules, existing settings fixture/test and Settings screenshot script. Controller owns plan/receipt/changelog/ledger and review dispatch.
- Fix every accepted finding including minors. Task review PASS before dependent task; fresh whole-branch review PASS before merge. Issue51 stays open for other items.

## Review Focus

RF1: Actual boolean false/true is output on successful disk/R2 GET/PATCH without changing preferences/storage or allowing runtime writes; pin Task1 projection/read-only tests.
RF2: Same stored revision/policy with gate-changing restart has a new shared ETag, current conditionalGET body and rejected stale If-Match, while same config retains304; pin Task1 restart-validator test.
RF3: List and Policy show exact last-reported enabled/disabled/not-reported notes, qualify contradictory pruning copy, and preserve all policy controls/semantics; pin Task2 mounted-DOM and real-demo gate-off screenshots.
RF4: Accepted network generations,304barriers,missing metadata,cached conflict adoption,busy/failure/destroy paths cannot resurrect a stale new report or discard valid saved values; pin Task2 deferred-network tests.

## Task 1 — Backend projection and shared validator

Owner: backend. RF1/RF2; expected 200–320 changed lines including tests, hard cap 599. Task 2 consumes this exact additive contract:

```json
{"revision":7,"preferences":[],"runtime":{"event_retention_enabled":false}}
```

`preferences` above abbreviates the existing full descriptor array. Successful GET and PATCH always include an actual boolean, including false; true uses the identical shape. Existing optional `storage` and its failure handling remain unchanged. Errors and previews gain no field. Runtime is output-only, never persisted or accepted as a patch.

- Modify `internal/httpapi/blob_capabilities.go::writePreferencesSnapshot` to explicitly project `snapshot.Revision`, `snapshot.Descriptors()` and `runtime` from `s.Hub.Cfg.EventRetentionEnabled`, then add storage under its current condition. Reuse the map style; remove only the direct-Snapshot early return. Do not embed Snapshot anonymously: its custom marshaler can swallow sibling fields. Leave `opapi.Preferences`, Snapshot JSON and descriptors unchanged.
- Modify `internal/hub/preferences.go::newPreferencesWithPolicy`: add named `EventRetentionEnabled bool` to the canonical digest input from startup Config. Preserve revision, tag format, all other digest inputs, matching rules and confirmation authority. An HTTP-only tag suffix would break service If-Match and is rejected.
- Config is fixed during serving under current production wiring. Reconstruct a hub to change the gate; this slice adds no supported hot setter. Changed gate at the same stored revision must change the shared tag. Same configuration/revision may retain its tag. No persisted generation or activation change is needed.
- Strict unknown-patch refusal remains: top-level `runtime` is invalid; SET/RESET of `runtime.event_retention_enabled` is an unknown preference. No new registry key, consumer installation change, CLI flag, ticker or pruning change.

Add `internal/httpapi/prefs_runtime_test.go`, reusing existing `prefsGET`, `prefsHTTP`, `testhub.StartWith`, `Env.CloneDB` and `newR2HTTP`:

1. `TestPreferencesHTTPEventRetentionRuntimeProjection`: disk/R2 × false/true, successful GET/PATCH; require explicit presence and boolean type (absence/null/string are failures), unchanged descriptors/editability/provenance, preserved optional storage, and successful `opapi.Preferences` decode.
2. `TestPreferencesHTTPEventRetentionRuntimeRestartValidator`: stored override survives reconstruction; false→true and true→false keep revision/value/source but change tag. Old exact/weak/list conditional tags receive 200/current bool, never 304; matching same-config tag still receives 304. Use Config changes solely as inputs to reconstruction, not live toggling. Old If-Match must receive 412; preserve fresh-preview requirements after that conflict.
3. `TestPreferencesHTTPEventRetentionRuntimeReadOnly`: reject runtime injection through top-level/SET/RESET, with unchanged Config/revision/value. Retain existing operator/as-peer/unauthenticated protections and confirmation checks.

Task1 owned paths: `internal/httpapi/blob_capabilities.go`, `internal/hub/preferences.go`, new `internal/httpapi/prefs_runtime_test.go`, plus the narrowly adapted `TestPreferencesHTTPStoredChangeMetadata` assertion in `internal/httpapi/prefs_test.go` under R7.

Validation: these three tests plus existing `TestPreferencesHTTPMetadataAndConditionalRead`, `TestPreferencesHTTPOperatorAuthority`, `TestRetentionBootstrapProductionPreferences` and relevant R2 preference checks. No workers or pruning are needed for these fixtures.

## Task 2 — Small Settings note and isolated status ingestion

Owner: UI. RF3/RF4; expected 230–380 changed lines including tests, hard cap 599. Integrates Task 1 without changing the API transport or preference write contract.

- In `internal/ui/static/settings-model.js`, add `state.eventRetentionEnabled: true | false | null`, initially null, and `eventRetentionEnabledOf(body)`. Accept only a boolean in a non-array `runtime` object; absent member/container, null, string, number or malformed type produces null. An otherwise valid legacy preferences response remains loadable.
- Add pure `eventRetentionNote(enabled)` and render its text beside the ack row in List `details` and under the ack Policy `laneEl` in `internal/ui/static/settings.js`. Use existing DOM text helpers/styles and `data-event-retention="disabled|enabled|not-reported"`; retain the note when that row is shown under List filters. Both views use the same state.

| Value | Exact note |
| --- | --- |
| false | Acknowledged-event pruning gate: disabled (last reported by hub). Saving this policy does not enable pruning. |
| true | Acknowledged-event pruning gate: enabled (last reported by hub). |
| null | Acknowledged-event pruning gate: not reported by this hub. |

Qualify only contradictory prose: in `GROUP_NOTE.retention_storage`, the Policy archive note and `riskText`'s `archive_disabled` tail, replace the unconditional ack-pruning assertion with “Scheduled acknowledged-event pruning also requires its rollout gate; archive mode does not pause that policy.” Preserve surrounding zero-policy advice and confirmation content. Change only the ack `LANES.then` suffix to “then pruned if the pruning gate is enabled” and qualify its related comment. Leave `ZERO`, `timingOf`, `laneView` calculations, position/stops/paused state, accessible control semantics and every edit/save guard unchanged.

### Narrow network contract for the new field

Keep `adopt(body, etag)` responsible only for its existing preference-value reconciliation. Never parse runtime there: `undo`/`adoptNewer` must not replay status from cached `newer.body`.

- Assign a monotonically increasing request generation at each existing prefs GET/PATCH invocation (`load`, `refresh`, `commit`, conflict-recovery GET in `failSave`). A tiny `acceptRuntime(body, generation)` accepts only responses meeting existing live/valid/busy acceptance checks and not older than the latest accepted runtime generation. This guard applies only to the new status, not whole-row adoption, errors or saves. No counter for preview, meter calls or local edits.
- Invoke runtime acceptance on valid 200 responses at those network sites before adopting/staging preference values. In refresh, parse it even for a same-ETag 200 or a dirty page that keeps its old base values. A newer accepted response omitting/mistyping metadata replaces a previous bool with null. Emit through the existing settings notification if a status change would otherwise be silent; keep the storage-only notification path unchanged.
- Accepted 304 has no new report: retain the existing bool/null and advance only the runtime ordering barrier, so an older in-flight 200 cannot overwrite it. Failures, unreadable whole snapshots and responses discarded by existing checks do not supply a report or reset one. A late lower-generation accepted response cannot overwrite a newer report, including newer not-reported metadata.
- Preserve current busy refresh exactly: it reads only the meter and retains the last report. No invalidation-on-focus, pending refresh, idle drain, extra network call, request framework or DOM-update channel. Existing focus/visibility/Reload/live hooks obtain the next report when their normal prefs read occurs; a successful PATCH also supplies one. No later idle refresh is necessary for the stated last-reported contract.
- Preserve initial load, clean/dirty refresh, PATCH acknowledgement, preview tokens, 412 recovery, conflict notices/choices and destroy guards. An old successful PATCH still acknowledges its values even if its runtime generation is superseded. R6: a refresh reply arriving after confirmation/save begins must not update the new report or ordering barrier, including same-tag200/304; preserve existing row/meter behavior. Cached-body adoption never changes runtime. Do not fix existing whole-row races, invalid-recovery conflict behavior or error ordering in this task; those are separate follow-ups.

Extend optional runtime support in `internal/ui/jstest/settings-fixture.mjs`, retaining omitted-status fixtures. Extend `internal/ui/jstest/settings.test.mjs` using its existing model/deferred-promise and mounted DOM harnesses, with these exact test names:

1. `event retention report accepts booleans and treats missing or malformed metadata as not reported`: false/true/missing/null/wrong types; initial legacy load and known→missing on accepted GET/PATCH; no inference from saved duration or descriptor runtime.
2. `event retention note appears in List and Policy without changing policy controls`: all three notes, filtered List, conditional pruning copy, identical values/source/locks/zero labels/tracks; existing preview/cancel/confirm/save requests remain unchanged.
3. `event retention report follows accepted network generations, not cached conflict bodies`: dirty refresh and conflict GET report immediately; same-tag 200 processes status; 304 retains report; newer false or missing survives a late older true GET/PATCH and Undo/adoptNewer. Assert the runtime guard does not discard successful saved values.
4. `busy and failed settings refresh retain the last event retention report`: no extra prefs request while busy, unchanged meter/confirmation behavior, failure retains prior report, subsequent normal refresh/PATCH replaces it, destroyed responses stay inert.

Task2 owned paths: `internal/ui/static/settings-model.js`, `internal/ui/static/settings.js`, `internal/ui/jstest/settings-fixture.mjs`, `internal/ui/jstest/settings.test.mjs`, `scripts/ui/prefs.mjs`.

Validation: `node --test internal/ui/jstest/settings.test.mjs internal/ui/jstest/settings-api.test.mjs internal/ui/jstest/settings-presence.test.mjs`. Update only assertions affected by the new note/conditional suffix; retain existing save/confirmation coverage.

Task2 visual acceptance: extend existing `scripts/ui/prefs.mjs` real-demo journey with assertions for the default-off last-reported note in List/Policy beside ack retention. Capture centered ack row at desk/phone with its existing both() helper; preserve current editing/request assertions. No new demo flag or mocked status. True/missing/malformed remain unit/DOM cases. Run only existing Settings script via serialized gate with owned SHOTS_DIR and inspect the four gate-off images; full screenshot matrix is unnecessary. Gate caller can prefix SHOTS_DIR=<absolute workspace>/shots, and run.sh handles screenshot pullback independently from BB_PULLBACK0.

## Integration and limits

Land backend contract/validator first, then its UI consumer; the additive response is compatible with existing typed clients, and the new UI handles older hubs as not reported. A gate-changing restart invalidates conditional reads through the shared tag without altering stored policy or granting deletion authority.

The note describes a last accepted report, not current worker health, actual deletion, activation approval, effective historical retention, disk reclamation or deployed state. The browser cannot detect an unobserved restart. Busy/failed reads retaining a qualified previous report are intentional. Direct hot Config mutation is outside the startup ETag contract. No broad dashboard, runtime probe, rollout or unrelated async repair belongs in this slice.



## Integration gates and source delivery

Task1: focused new/related HTTP+hub preference tests, then full `./internal/httpapi ./internal/hub ./internal/prefs ./internal/opapi` through gate. Task2: named Settings files, full `just test-ui`, existing Settings-only screenshot journey and narrow Go UI embedding/native app checks as relevant. Controller independently reads raw gates/manifests; avoid duplicate broad replays on unchanged bytes. Extra gates require a concrete unresolved concern. Before final review fetchmain and preserve concurrent changes; normal merge/push after PASS and byte-equivalence, update only named #51 item, verified external evidence copy before cleanup. Source work does not imply a new installed release.

Plan self-review: two serial tasks; T1 additive JSON/shared ETag exactly consumed by T2; shared file conflicts none. T1 self and T2 self requirements/tests/ownership checked Clean. All four Review Focus items pinned above. Stage0: no UNVERIFIED external API/runtime needed; existing withDemo/prefs journey accepts owned SHOTS_DIR and uses disposable hub/browser. No live gate probe needed. Try it through the synthetic Settings visual journey on x-boss.

Execution refinement R7: the original full affected gate found an old whole-HTTP-body Snapshot round-trip assertion incompatible with deliberate HTTP-only runtime metadata. Adapt it to compare every Snapshot-owned revision/preferences field, separately requiring the raw runtime bool. Current fullHTTP667PASS plus identical-production prior fullhub938/prefs42/opapi7PASS form current affected evidence; the old overall wrapper1 remains historical, not relabeled.

Execution refinements R8/R9: the preserved real-demo journey exposed a pre-existing missing `presence.send_warning_after` Policy control. Restore that existing preference in Policy and canonical fixtures with mounted editing/source coverage, retaining every journey assertion. Qualify the adjacent archive pill’s universal acknowledged-pruning claim as ‘History kept; acknowledged-event policy unchanged’, with archive-on/gate-disabled coverage. These narrow additions stay within the five UI-owned paths; no retention or warning behavior changes.
