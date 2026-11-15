# Independent Task 1 review

Spec compliance: PASS
Code quality: PASS
Findings: none (Critical 0 / Important 0 / Minor 0).

Reviewed BASE `7870e2fcfe871ce6ecc1193f7594eb020654652f` → HEAD `eb9f61b800818250f22eb131ab27ee7e2a263a4b`, TREE `b71d8d5bd5455aceeb9f4e3717cc389568c9b44d`. HEAD/tree resolve exactly; worktree source equals committed blobs; supplied review diff equals the pinned Git diff with ten context lines. Read the brief first, full plan including Task 1/global constraints/RF1/RF2, frozen issue, progress R1–R7, implementation report and Context followup 1, actual changed functions and immediate integration seams.

## Implementation assessment

- RF1: `internal/httpapi/blob_capabilities.go:78` explicitly projects exactly the existing Snapshot revision/descriptors and adds an actual runtime boolean, including false. It avoids Snapshot's custom-marshaler embedding trap. Both disk and R2 use this helper on successful GET/PATCH. Optional storage still uses the same transfer condition, query, values and sanitized failure path. Preview/error responses remain separate.
- RF2: `internal/hub/preferences.go:186` includes named `EventRetentionEnabled bool` in the shared canonical startup digest. Snapshot generation and service preconditions consume that digest directly; no HTTP suffix or revision change was introduced. Both gate-changing reconstruction directions are exercised with cloned persisted overrides, equal full descriptors/revision, changed shared tag, stale exact/weak/list conditional GET returning current metadata, current-tag 304, stale If-Match 412, stale confirmation rejection and fresh-preview/save success. Same-config reconstruction retains its tag.
- Output-only/authority: unchanged strict Patch decoding/registry reject top-level runtime and SET/RESET of its dotted key. The new tests cover both gates, PATCH/preview refusal, unchanged revision/descriptors/config, unauthenticated/session/as-peer rejection and confirmation-required refusal. No new preference, schema, runtime setter, consumer activation or persistence path exists. Production construction supplies the same fixed startup Config to the digest and HTTP hub; unsupported hot mutation is outside this contract.
- R7: `internal/httpapi/prefs_test.go:590` compares the entire Snapshot serialization with exactly the original revision and complete preferences array, preserving every nested descriptor/provenance/change field. The existing literal row assertions and save/unrelated-save/restart/no-op/reset/read sequence remain. A separate raw-object/boolean assertion checks HTTP runtime against startup Config. This resolves the historical incompatible whole-body comparison without weakening Snapshot-owned metadata coverage.
- `internal/httpapi/prefs_runtime_test.go` uses the real current HTTP helpers, production hub construction and existing opapi alias decoder. The disk/R2 × boolean GET/PATCH matrix checks explicit presence/type, policy/provenance/editability, full descriptor projection, storage accounting/errors and zero synthetic-provider calls. Fixtures reconstruct hubs rather than toggle serving Config; no worker/pruning execution is needed. The production changes are small and preserve existing module boundaries; no quality defect identified.

## Independently inspected evidence

Parsed the raw JSON logs, terminal test events, package results, wrapper/build-box exits and before/after source manifests under `.superpowers/sdd/issue-51-settings-pruning-report/logs/`. Counts include parent/subtest events; package results are separate.

| Gate | Test pass / fail / skip | Package result; wrapper/build-box exit |
| --- | --- | --- |
| `baseline-prefs` | 5 / 0 / 0 | HTTP PASS; hub no selected tests; 0/0 |
| `task1-red-01` | 8 / 8 / 0 | HTTP FAIL; hub no selected tests; 1/1 |
| `task1-red-02` | 8 / 8 / 0 | HTTP FAIL; hub no selected tests; 1/1 |
| `task1-green-01` | 16 / 0 / 0 | HTTP PASS; hub no selected tests; 0/0 |
| `task1-affected-01` | 1651 / 3 / 0 | HTTP FAIL; hub/prefs/opapi PASS; 1/1 retained |
| `task1-context1-focused-01` | 19 / 0 / 0 | HTTP PASS; 0/0 |
| `task1-context1-http-full-01` | 667 / 0 / 0 | HTTP PASS; 0/0 |
| `controller-task1` | 19 / 0 / 0 | HTTP PASS; 0/0 |

RED-01's disclosed descriptor-comparison correction is not treated as feature evidence. Authoritative RED-02 uses final runtime-test bytes against BASE production and shows missing runtime, unchanged gate-restart tags, stale conditional 304s and accepted stale If-Match. Read-only and existing guards already pass as controls. The historical affected failure is exactly StoredChangeMetadata/default, /flag and their parent; all pass in the new focused/full HTTP logs.

Current affected evidence combines new HTTP 667 with retained unchanged-production hub 938, prefs 42 and opapi 7: 1654 passing events, no current failures/skips. This is evidence from separate gates, not a new four-package pass; the old wrapper exit 1 remains intact. Raw gate commands use x-boss/two jobs, the wrapper's synthetic HOME/XDG, physical private TMPDIR and umask0022. No review test/build or broad replay was run.

Every inspected gate's before/after manifests is identical. RED production hashes match BASE; RED-02 runtime-test bytes match HEAD. GREEN/affected manifests match final bytes for every recorded entry (those older manifests omit prefs_test.go). Followup and controller manifests include prefs_test.go and match HEAD for all nine entries. Production and runtime-test blobs stayed unchanged after `fc79780c`; later tracked changes are the R7 assertion and controller plan refinements. Pinned diff whitespace check is clean.

| Owned path | Verified final Git blob |
| --- | --- |
| `internal/httpapi/blob_capabilities.go` | `dd552ca7d2a7555ae2cbf08e8dfbdc68b5343652` |
| `internal/hub/preferences.go` | `8b2249428f54cc51df030b882e1431500f247b4f` |
| `internal/httpapi/prefs_runtime_test.go` | `d193e8339d766763ae0edc109f123126c6ca25f6` |
| `internal/httpapi/prefs_test.go` | `f6c5bf7549c07d8562795f203fe8358d1646c400` |

Task 1's backend handoff is verified for Task 2 consumption: successful GET/PATCH supplies `runtime.event_retention_enabled` as a boolean alongside unchanged Snapshot fields and optional storage; the shared ETag binds the startup gate. This verdict covers Task 1 only. UI implementation, visual acceptance, whole-branch review, issue metadata, merge and cleanup remain controller-owned and are not claimed complete. No source/index/branch/HEAD changes, child agents, runtime access or external writes were performed; this review file is the sole write.

===DONE===
