# Independent Task 2 review

Spec compliance: PASS
Code quality: PASS
Findings: none (Critical 0 / Important 0 / Minor 0).

Reviewed BASE `eb9f61b800818250f22eb131ab27ee7e2a263a4b` → HEAD `e208e271f7e81a7a928ada002d9eefe8a5ebccd2`, TREE `522237d2aa40f6ec86c891278060eb37403718c2`. HEAD/tree resolve exactly. The supplied `task-2-review.diff` equals `git diff -U10 BASE HEAD` byte-for-byte and has SHA256 `b1d5230d0c8f836d1557576647bbab1c3a2ba562479e67810b17ad0cc94cee48`. The cumulative change contains only the five authorized paths, 397 insertions / 20 deletions; whitespace check passes. All nine source entries recorded by the final gates equal their HEAD blobs and current worktree bytes.

Read `task-2-brief.md` first among task materials, the plan including Task2/narrow network contract/RF3/RF4, progress R1–R9, original and Context followup 1 Task2 report, explicit context prompt, Task1 handoff report and independent review, and controller `visual-inspection.md`. Inspected actual changed functions, callers, fixture, mounted/deferred tests and existing screenshot journey. The Task1 API seam was checked as needed; its separately reviewed backend scope was not reopened. The two review axes were assessed independently in this reviewer; no child agents were spawned under the binding worker constraint.

## Spec assessment

- RF3 — `internal/ui/static/settings-model.js:226` accepts only a boolean member inside a non-null, non-array runtime object; all other metadata yields null. The initial report is null. Neither retention duration nor descriptor runtime/editability supplies activation. The pure note helper contains the exact three required strings. `internal/ui/static/settings.js:159` and `:318` render the shared report beside the acknowledged-event List row and Policy lane, with the required data attributes. Filtered List coverage retains the note. The group/archive/risk prose, ack suffix and R9 archive-on pill qualify the contradictory claims without changing zero labels, timing, track positions/stops, pause mechanics, values, sources, locks or confirmation authority.
- RF4 — generations are allocated only at actual existing preference network calls: load (`settings-model.js:474`), refresh (`:519`), conflict-recovery GET (`:620`) and commit PATCH (`:702`). Each site's existing live/readability checks remain. `acceptRuntime` (`:403`) guards only the new field; it does not discard row adoption or a successful PATCH acknowledgement. Common `adopt`, Undo and adoptNewer never parse runtime, so cached bodies cannot revive an old report.
- Refresh ingests valid same-tag 200 and dirty-page responses separately from row reconciliation. Accepted missing/malformed metadata replaces a prior boolean with null. An accepted 304 preserves the report and advances the generation barrier; failures/unreadable snapshots do neither. R6 checks busy after the awaited reply, including same-tag 200/304, discarding only the report/barrier while preserving existing row/meter behavior. Entry-busy refresh still makes only its existing meter request. Report-only changes use normal settings emission; the storage-only notification path is preserved. There is no new read, idle drain, request framework or general row/error ordering repair.
- Deferred tests exercise older GET/load/PATCH completion, a newer false or missing report, 304 barriers, dirty/conflict ingestion, same-tag otherwise-quiet notification, cached Undo/adoptNewer, busy entry and response-becomes-busy cases, failed/unreadable replies, recovery and destroy paths. The superseded PATCH cases assert saved values, cleared draft and saved keys as well as the retained newer report. These use the real store and mounted renderer, not source-text substitutes.
- R8 — `settings.js:414` adds the actual `presence.send_warning_after` token through the existing Policy drawer flow. Fixture type/default/range/zero metadata and description match `internal/prefs/registry.go:77`; existing fixture defaults remain unchanged. Mounted List/Policy tests check the 1h control, bounds rejection, 2h and zero saves, exact PATCH/If-Match, shared saved values, provenance and absence of advisory confirmation. The corrected fixture risk predicate names only default/Grok/session/launcher TTL keys, matching the server's TTL dependency classes. The existing 409 fallback assertion at `settings.test.mjs:365` is unchanged and still demands PATCH → preview → confirmation independently of client danger metadata.
- R9 — the mounted archive-on/gate-disabled test checks truthful pill copy, history pause, unpaused acknowledged events, unchanged ack radio/zero-stop mechanics and draft-only staging. No archive, zero or track behavior changed.
- API seam — the unchanged transport returns 304 separately and throws failed/unreadable requests; successful GET/PATCH retain the additive runtime object supplied by Task1's projection. No writable setting, preference key, schema, endpoint, dependency, flag or activation was introduced. The note describes a last accepted gate report and makes no worker-health or deployment claim.

## Code-quality assessment

The production changes are small and follow the existing store/render boundaries. Runtime parsing and note copy each have one shared helper; ordering state remains local to the store. Existing storage, value reconciliation, save/preview/confirmation and error paths are preserved. Text uses the existing DOM text construction and styles. The optional fixture metadata preserves legacy omitted-runtime cases. Tests extend the established deferred-promise and mounted-DOM harnesses, and the screenshot script retains the existing real-demo helpers and every wire-row/edit/request/confirmation assertion. No actionable standards violation or maintainability defect was identified; no stylistic preference is elevated into a finding.

## Independently inspected evidence

Parsed raw TAP/Go JSON results and diagnostics, wrapper `.exit` values, build-box exits and before/after source manifests under `logs/`; did not rely solely on report summaries. Counts below include parent/subtest events. All Node runs have zero skips/cancellations; both Go UI runs have two passing tests, no failures/skips and a passing package result.

| Gate | Pass / fail | Wrapper / build-box exit |
| --- | --- | --- |
| `baseline-ui` | 607 / 0 | 0 / 0 |
| `task2-red-01` | 85 / 26 | 1 / 1 |
| `task2-green-01` | 111 / 0 | 0 / 0 |
| `task2-green-02` | 115 / 0 | 0 / 0 |
| `task2-ui-full-01` | 637 / 0 | 0 / 0 |
| `task2-ui-embed-01` | 2 / 0 | 0 / 0 |
| `task2-settings-visual` | historical Policy-token timeout; 4 PNGs | 1 / 1 |
| `task2-settings-visual-02` | same historical timeout; 8 PNGs | 1 / 1 |
| `task2-context1-red-01` | 114 / 5 | 1 / 1 |
| `task2-context1-green-01` | 118 / 1 | 1 / 1 |
| `task2-context1-ui-full-01` | 640 / 1 | 1 / 1 |
| `task2-context1-green-02` | 119 / 0 | 0 / 0 |
| `task2-context1-ui-full-02` | 641 / 0 | 0 / 0 |
| `task2-context1-ui-embed-01` | 2 / 0 | 0 / 0 |
| `task2-context1-settings-visual-01` | complete journey PASS; 98 PNGs | 0 / 0 |
| `controller-task2` | 119 / 0 | 0 / 0 |

Original RED production hashes match BASE for both Settings modules. Its failures are behavioral missing-status/missing-note assertions, not setup or compilation errors; existing controls already passing in RED are not counted as new feature failures. The final expanded tests add malformed containers/members and false/missing ordering cases. Context RED exposes the missing Policy warning token/copy, false advisory TTL-confirmation behavior and old archive pill; its search-list mismatch is an acknowledged fixture-oracle adaptation. The subsequent focused/full failures are exactly the unchanged 409 fallback test catching the first fixture risk-predicate mistake. The final four-key predicate fixes that simulation error, and the same assertion passes. Both historical visual failures identify the missing `tok-presence.send_warning_after`; neither is relabeled as a completed journey.

Every inspected Task2/baseline/controller before/after manifest is identical. Final focused/full UI/Go UI/visual/controller gates match all nine current committed source entries. Earlier manifests pin their earlier tested bytes; they are not passed off as final followup source. HEAD stamps on pre-commit gates are distinguished from their dirty-source hashes. Final full UI contains 613 top-level / 641 total passes. The gate uses serialized x-boss/two jobs, physical private TMPDIR and umask0022; the build-box wrapper supplies lane-private synthetic HOME/XDG. No review test/build/browser execution or duplicate broad replay was performed. Prior backend package passes remain separately reviewed, source-equivalent Task1 evidence, not new Task2 gates.

## Visual verification

Read the complete final Settings log and preserved failure logs. The final script asserts actual default-off backend metadata, exact disabled text beside ack, visibility after centering and no sideways overflow through existing `both()` helpers. It preserves `rowsMatchHub`, all existing edit/request/confirmation/conflict assertions, three isolated withDemo fixtures and cleanBuild. No mocked gate report or new startup flag was added.

Independently opened all four actual final gate-off PNGs, agreeing with the controller inspection: note legible beside ack retention, contained wrapping at desktop/phone, visible value/source/controls and intact Policy tracks. Verified all 98 final PNG hashes against the report inventory. Final gate-off pins under `visual-context1/`:

| File | SHA256 |
| --- | --- |
| `prefs-list-gate-off-desk.png` | `a3121c385e338f2c9e4c94524eeef25fbe5a822f33825771328e11dd38dacd4b` |
| `prefs-list-gate-off-phone.png` | `9ca88d8c8b45ad76dd7d786a6d81366a0abc2b3529f49535fbd3e2fa22ce8638` |
| `prefs-policy-gate-off-desk.png` | `b2aef56f0c98d5b5a580bd98cff2ff2d4ff570a0d7412f88560c3c02efbf55e1` |
| `prefs-policy-gate-off-phone.png` | `812c1ae32271552c75cc750410af03041015d4e75e2d44a7b8f5ee289f929abd` |

True/missing presentation is mounted-DOM coverage only, as authorized. Preliminary images remain historical evidence and are not final journey acceptance.

## Final source pins and boundary

| Owned path | Verified HEAD blob |
| --- | --- |
| `internal/ui/static/settings-model.js` | `13fbaed9bc1d180ae9a7b73aa9e951875ed082f4` |
| `internal/ui/static/settings.js` | `74a47c3ce1091bc8ead33082f29e4d11467aeccc` |
| `internal/ui/jstest/settings-fixture.mjs` | `d7bba59f1a3aa4090d55f99f0ffc7be2d0671390` |
| `internal/ui/jstest/settings.test.mjs` | `8574bbb47269dfd7695ece4673f8f32a9514a331` |
| `scripts/ui/prefs.mjs` | `45a45031c2ae6e937db083f2894355d51830959f` |

Task2 RF3/RF4 and R8/R9 pass with no unresolved findings, including minors. This report is the sole write. No source/index/branch/HEAD/commit changes, agent spawning, test/build/browser execution, private runtime/config/credential access or external writes were performed. Whole-branch final review, controller documentation, issue handling, merge/push, verified archive and cleanup remain pending and are not claimed complete; issue51 remains open for its other items.

===DONE===
