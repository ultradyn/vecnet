PASS
Spec compliance: PASS
Code quality: PASS

Independent whole-branch review of issue #47, base `d4038c6c0f9ea5bfb99bda953ed0887c9a8dc17f`, head `db0b1c5ec4f5ece94d74b41d237620d4b4089c05`, tree `92d21ef168f8102fb9e5ab73a174280620d05554`, branch `fix/issue-47-received-hub-urls`.

The supplied final-review.diff contains the complete issue change: its diff section equals `git diff -U10 d4038c6c HEAD` byte for byte. Both d4038c6c and f0f1c828 are ancestors of this head. The concurrent master work is present in the tree and excluded by the correct review base. The tracked worktree was clean. All 27 changed paths are within the declared source/test/documentation scope; `git diff --check d4038c6c HEAD` passes.

## Findings

None. Zero Critical, Major, Important or Minor findings; zero unresolved accepted findings. No finding ID/severity/file:line/Scenario/Effect/Fix entry is warranted. No required review-focus half is missing.

Historical findings independently checked against current source and covering raw output:

| ID | Accepted severity | Current disposition |
| --- | --- | --- |
| I1 | Important | ADDRESSED. `internal/opclient/client.go:555` requires whole-document JSON validity before observed blob recovery can replace the original error. Trailing junk/second-document cases retain http_410; incomplete-body and transport guards remain. Regression: `internal/opclient/notices_response_test.go:612`. |
| T2-1 | Minor | ADDRESSED. `internal/tui/data.go:236` revalidates all recovery fields, then preserves only the exact fixed invalid-relocation marker at line 239. Stored error, toast and broadcast agree; arbitrary supplied Message remains excluded. Regression: `internal/tui/received_huburl_test.go:228`. |
| T2-2 | Important after R13 | ADDRESSED. `internal/cli/notice.go:40` resolves explicit environment/client policy and only a matching profile's opt-in; lines 46–54 attach it to a local client copy before response decoding. No profile permission lending or client mutation. Regression: `internal/cli/received_huburl_test.go:463`. |

## Review Focus audit

1. **RF1 — PINNED.** `internal/opclient/notices_response_test.go:200` and `:247` independently cover Hub/NewURL userinfo (including username-only), query/empty query, fragment/empty fragment, malformed authority/IPv6/escape, relative/scheme failures, null/mistyped/missing fields, cleared and nonmoving notices, both page/list orderings and accepted controls. The tail witnesses require zero apply/ack/accepted effects and an unchanged retry cursor. `internal/cli/received_huburl_test.go:76`, `:125`, `:202` and `:286` cover list/post human+JSON, inbox JSON, ordinary/monitor watch, poll and SSE, hidden invalid suffix under --limit, untouched cache/dedupe/cursor/restart/move state, no receipts/ack/follow/destination requests and no output. `internal/tui/received_huburl_test.go:38`, `:104` and `:119` cover fake notice loads, direct batch/remember/formatter seams, HTTP null/type admission and no partial load/model effects. Both rejection and effect-prevention halves are pinned.

2. **RF2 — PINNED.** `internal/opclient/redirect_response_test.go:151` (matrix implemented at :154) exercises real malformed Location parsing for ordinary requests, discovery, raw upload and progress upload; :152 preserves parseable no-follow responses. It checks original url.Error/cause identity, safe public text, response closure and zero destination attempts. `:240` suppresses received request-query diagnostics; `:259` pins retry/backoff, cancellation and absent callbacks/ack. `internal/httpdiag/transport_test.go:12` and `:33` pin complete outer-URL admission, errors.Is/errors.As, nil, timeout and temporary semantics. `internal/cli/redirect_response_test.go:20` and `:72` pin stderr, prefs JSON, reconnect presentation, exit 3 and typed causes. `internal/tui/received_huburl_test.go:119` pins the HTTP-to-model redirect presentation. The transport-preservation controls inside `internal/opclient/notices_response_test.go:357` also check DNS/network/TLS/context identities. Both safe diagnostics and classification/exit/retry halves are pinned.

3. **RF3 — PINNED.** `internal/hubnotice/received_test.go:14` verifies exact signed-field equality/signatures for case/escaped prefixes, bracketed IPv6 and public/LAN/loopback HTTP with strict/allow policy; :82 checks raw event nonmutation and independent Hub/NewURL policy. `internal/cli/received_huburl_test.go:234`, `:325`, `:424` and `:463` exercise actual CLI JSON/human/event/direct-client boundaries with strict/profile/environment and mismatched-profile controls. `internal/tui/received_huburl_test.go:169` verifies signed fields and raw retained events; :228 preserves signed recovery and IPv6 targets across HTTP/fake reprojection. `internal/opclient/notices_response_test.go:247` compares accepted Event.Data byte for byte. Signed free-text Message containing URL-like text remains content. Helpers neither normalize URLs nor authenticate notices; existing follow separately verifies identity, signature and lifecycle. Both negative-policy and exact accepted-value halves are pinned.

4. **RF4 — PINNED.** `internal/opclient/notices_response_test.go:357` covers hostile/mismatched/empty Message, checked and rejected allowlisted fields, exact status/code dispatch and unchanged unrelated rich errors; :519 and :656 cover disk/discovery/mint and independent recovery siblings. `internal/hubnotice/received_test.go:114` pins independent null/type/canary and strict/allow siblings, whole-notice rejection, input immutability and signature preservation. `internal/cli/redirect_response_test.go:88` covers enrol output/hints, exact typed status/code, no follow/config mutation and unrelated rich errors. `internal/tui/received_huburl_test.go:192` and `:228` cover fake and real projection, fixed-marker preservation, typed cause identity and immutable inputs. Existing `internal/cli/route_stats_relocation_test.go:286` and `:319` retain hostile unsigned sibling/signed-follow and signature/source/destination/expiry/clear/replay/key refusal witnesses. The sole expectation adjustment at :369 correctly reflects earlier URL refusal and preserves exits, no-output/no-follow and unchanged-state assertions. Both safe exact-move projection and unrelated/authenticated-recovery preservation halves are pinned.

5. **RF5 — PINNED.** `internal/cli/codexbind_huburl_test.go:13` checks unusable copied Hub labels, unchanged stored bytes, TTL state and full prefix/IPv6 output; :65 checks unchanged directory permissions and absent ownership-lock creation. `cmd/vecnet-hub/huburl_flag_diagnostics_test.go:19` and `:95` retain both environment help/parser refusals and valid flag/environment selection. `internal/cli/invite_huburl_response_test.go:224`, `:275` and `:348` retain human/JSON/copy refusals, accepted prefix/IPv6/HTTP output and earlier unsafe-fallback refusal. `internal/tui/notice_test.go:98`, `:118` and `:358` retain actual valid rendered banner goldens and event lines. All these families have passing terminal events in the exact-head full log; no golden file changed. Both stored inspection and shipped/rendering preservation halves are pinned.

## Seams inspected

Read the complete changed production functions and the surrounding admission/effect/control-flow consumers, using source rather than implementer narratives:

- Shared boundary: `internal/hubnotice/received.go`, `internal/httpdiag/transport.go`, and their complete tests; `internal/hubid/hubid.go` CheckHubURL/HTTP policy/normalization distinction. Raw notice decode preserves null/type evidence; empty optional NewURL remains allowed, required Hub cannot be empty, and cleared/nonmoving notices receive the same URL checks. Validation leaves accepted strings and Event.Data untouched.
- Operator HTTP and transfer boundary: complete `internal/opclient/client.go` and `internal/opclient/tail.go`, plus `client_test.go`, `notices_response_test.go` and `redirect_response_test.go`. Checked response projection is exact-status/code only. The bounded blob observer preserves original body read/close behavior and rejects overflow, transport/truncation and malformed-whole-document promotion. Full page validation precedes restart handling, callbacks, acknowledgement, accepted notification and cursor changes.
- CLI: complete `internal/cli/client.go` and `internal/cli/notice.go`; relevant complete connect/enrol/poll/inbox/remember/exit functions in `commands.go`; watch moving-refresh/follow/switch/poll/replay/SSE/handle/ack consumers in `watch.go`; binding selection/read/list/mutation split in `codexbind.go`. Read `hubguard.go`, `follow.go`, `route_stats.go`, `profile.go`'s direct notice client, `tui.go`, `reconnect.go`, prefs error presentation, and `codexbind_unix.go`'s descriptor/ownership checks. Inspected all new CLI tests and changed existing assertions. Local client copies apply explicit policy before error decoding, successful response validation and follow; no received scheme grants permission.
- TUI: complete `internal/tui/model.go` and `data.go`; complete viewNotices/hubHost/inboxEventText seams in `layout.go` and `screen_inbox.go`; complete new received_huburl tests and existing notice rendering/control tests. Fake notice fetch validates before snapshot application. Fake tail callbacks, direct batches and remember reject before state/load/ack effects. Concrete formatters defend direct data. Recovery wrapping exposes checked typed errors through As while Unwrap preserves original cause identity, without mutating the caller's error.
- Previously shipped boundaries: help-default/parser tests and invitation-response tests, including their positive and fallback contracts. Inspected plan, receipt, changelog, original issue body/comments, all task review/re-review findings, Task 1 narrative and both fix appends, Task 2 narrative and final resume/fix append, and progress Ruling lines. The generated hundreds-of-test evidence index was not substituted for source or raw gate verification.

No independent actionable standards/maintainability defect was found. No repository AGENTS.md, CODING_STANDARDS or CONTRIBUTING file was present in the file inventory; supplied instructions, the declared scope and existing Go conventions were applied. Shared helpers centralize admission/presentation without expanding into arbitrary-text filtering, authentication or hub behavior.

## Ledger rulings judged by effect

- **R1:** appropriate repository-specific runner adaptation. gate.sh pins x-boss, two Go jobs/threads and a serialized lane. run.sh/remote.sh supply a clean lane environment, bounded jobs and actual exit propagation; source pullback is disabled.
- **R2:** existing operator authorization justifies autonomous implementation choices. It does not waive review, evidence or this reviewer's read-only/report-only scope.
- **R3:** exact model IDs/authenticated launch route concern orchestration. No product behavior depends on this ruling. I did not replay launch provenance or inspect credentials, and did not spawn agents.
- **R4:** adapting the narrow historical component was appropriate. Current code and current-source tests, rather than old ancestry or prior assurances, determine this verdict.
- **R5/R6:** R6 correctly supersedes R5. umask 0022 preserves explicit permission fixtures; Python TemporaryDirectory supplies a mode-0700 physical root. The two earlier failed permission baselines remain historical evidence, not passing results.
- **R7:** complete request-URL validation is necessary because received cursors enter queries; validating only the configured base would leak them. Bounded blob observation is justified by lost raw null/type and recovery information, provided whole-body/transport contracts are retained; current guards and tests do so.
- **R8:** accepting I1 was necessary. Its whole-document guard remains present and covered; the final implementation does not promote a malformed blob response into recovery.
- **R9:** ignoring non-object JSON in the operational summary parser is appropriate. Actual wrapper .exit and raw bb exit agree, and this review independently counted raw Go events rather than relying on that parser.
- **R10:** correct goal fit. An invalid unsigned target must not erase an independently valid signed notice, and an invalid notice must not erase a checked target. Every URL inside the notice is still mandatory, invalid notices remain atomic, and normal lists/pages remain atomic. Retaining recovery data is separate from authorizing follow. Existing authenticated follow still checks source, destination, key/signature, active lifecycle and replay before profile/pin changes.
- **R11:** the one unsafe-notice expected-reason update is justified by mandatory earlier receive refusal. Compared the diff and original hostile fixtures: they were not replaced, and exit/state/no-follow assertions were not weakened.
- **R12:** preserving concurrent master and testing the combined tree is correct. Both named masters are ancestors, the final diff has the intended base, and integration-r2 evidence records this exact head/tree.
- **R13:** Important is justified for loss of explicitly allowed recovery. The cheap diagnostic fix also needed completion under the fix-all requirement. Current local-copy policy propagation and fixed-marker reprojection solve both without granting unrelated profile permission or forwarding arbitrary prose.

## Gate evidence independently verified

Reparsed raw JSON terminal test/subtest events (parents included; package events excluded), read saved actual wrapper .exit files, compared raw `bb: exit=` lines, and inspected representative behavioral RED diagnostics and exact-head preservation PASS events. Summaries agree with the underlying logs.

| Gate basename under logs/ | Pass / fail / skip | Actual wrapper / build-box exit |
| --- | ---: | ---: |
| task1-red-corrected | 98 / 534 / 0 | 1 / 1 |
| task1-fix1-red | 0 / 5 / 0 | 1 / 1 |
| task1-fix1-green-focused | 210 / 0 / 0 | 0 / 0 |
| task1-fix2-red | 100 / 225 / 0 | 1 / 1 |
| task1-fix2-green-affected | 877 / 0 / 0 | 0 / 0 |
| task2-red | 10 / 285 / 0 | 1 / 1 |
| task2-resume1-green-affected-suite | 4112 / 0 / 3 | 0 / 0 |
| task2-reviewfix1-red | 18 / 12 / 0 | 1 / 1 |
| task2-reviewfix1-green-affected-suite | 3113 / 0 / 3 | 0 / 0 |
| integration-r2-full | 7955 / 0 / 3 | 0 / 0 |
| integration-r2-vet | no Go test-event count | 0 / 0 |
| integration-r2-protocol | no Go test-event count | 0 / 0 |

Historical RED diagnostics are actual behavior failures: task1-fix1-red.log:15 exposes malformed-body promotion; task1-fix2-red.log:15 exposes erased checked recovery; task2-red.log:160 exposes notice-list canary output; task2-reviewfix1-red.log:16/:26 expose loss of fixed TUI refusal. They are not compilation failures presented as regression proof.

Exact-head full counts include CLI 2518, opclient 796, TUI 595, hubnotice 70 and httpdiag 11 passing events. Only baseline skips remain: TestLauncherInstallRejectsUnsafeSnapshotReuse/foreign_runtime_owner, TestLocalRegistryProcessWorker and TestWriteShots. The screenshot-writing skip does not skip the rendered notice goldens.

Representative exact-head raw PASS locations in integration-r2-full.log: help diagnostics :413 and override selection :1064; binding URL/read-only families :2271/:2275; invite refusal/accepted output :8615/:8779; notice CLI :16840; direct relocation HTTP policy :18076; redirect diagnostics/classification :18116/:18120; signed clone/refusals :18168/:18230; tail page atomicity :26827; rendered banner frames :32394/:32398 and event line :32430; TUI HTTP responses/signed values/fixed diagnostic :32502/:32506/:32562.

integration-r2-head.txt and integration-r2-tree.txt match this review's head/tree; integration-r2-dirty.log is empty. The full command is `go test -mod=readonly -p 2 -count=1 -timeout=20m -json ./...`; vet is `go vet -mod=readonly -p 2 ./...`. Their raw headers show the required runner and private TMPDIR/0022 wrapper. Protocol raw output includes schema equality, positive/expected-negative examples, ALL GOOD, and blob fixtures/CLI dependency checks. Expected-negative example diagnostics are successful controls, not gate failures. The older 7925-pass integration is not used as evidence for db0b1c5e.

## Documentation check

The committed plan, receipt and changelog accurately describe current boundaries, original signed-value preservation, policy ownership, independent recovery siblings, read-only binding display and synthetic validation. Their named test families and covering counts match code and raw evidence. Historical all-or-nothing recovery statements and Task 2 NEEDS_CONTEXT/proposal are explicitly superseded by the appended R10/resume sections; they are not unresolved requirements or permission to weaken fixtures.

The plan's remaining task/acceptance checkboxes and receipt's controller-owned review/integration language are closing metadata awaiting this verdict, as explicitly instructed. Fresh integration-r2 closes the earlier reports' pending full-gate limitation for this source. Controller may now update that metadata with the actual verdict/evidence. No installed-release, deployment, migration, real binding renewal or ownership-transfer claim is made by this source issue.

## Limitations and disposition

This is a source-and-retained-evidence whole-branch review, not an independent replay of historical execution or remote launch/fetch provenance. No test was rerun: source and the fresh controller-owned exact-head raw gates resolved the reviewed questions, so no additional focused probe was necessary. No race suite, live terminal session, production probe, real credential/session/profile/binding/store access or delivery operation was performed. Rendering evidence is the committed synthetic regression/golden suite, not a fresh live screenshot.

No source/index/HEAD/branch edits, child agents, user questions, notifications, pushes, merges or live operations occurred. The only reviewer-written file is this report. Final metadata updates, merge/push/issue closure and worktree cleanup remain controller actions; this verdict does not claim those actions happened.

PASS for the reviewed source and issue #47 acceptance boundaries. All required RF lines are pinned; all previously accepted findings are addressed; no new finding remains to fix before merge.

=== DONE ===
