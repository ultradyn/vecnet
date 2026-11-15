# Received Hub URL Safety Implementation Plan

> For agentic workers: implement with the PIRFL plan/implement/review/fix loop. The controller dispatches independent reviewers and owns integration. The operator authorized implementation, review fixes, merge to master and worktree cleanup.

**Goal:** complete GitHub issue [#47](https://github.com/ultradyn/vecnet/issues/47): received hub URLs reach output and state only after the shared URL boundary accepts them.
**Architecture:** share typed notice validation and safe HTTP transport diagnostics between the operator client, CLI and TUI. Validate complete received event pages before callbacks, output, cursor changes or acknowledgement; retain original accepted signed values.
**Tech stack:** Go, net/http, existing hubid/envelope types, Go regression and rendered TUI tests.
**Spec:** the issue body and comments, audited against master `f0f1c82874ca1737023d54ccae929fdfc8f17797` on 2026-11-15. The help-default and invitation-response slices have shipped and remain preservation gates.
**Status:** implementation in progress; acceptance and independent review are required before closure.

## Global Constraints

- Use `hubid.CheckHubURL` for structured hub URL fields; no arbitrary-text or regular-expression redaction.
- Refuse userinfo, query, fragment, malformed/nonabsolute URLs and mistyped/null URL fields with fixed diagnostics that contain none of the rejected value.
- Preserve accepted signed notice fields byte for byte, URL path prefixes and bracketed IPv6. Public HTTP follows explicit caller/profile/environment policy; a received scheme grants no permission.
- Preserve status/code and validated relocation recovery fields, typed error chains, timeout/retry classification, context cancellation and CLI exit codes. Unrelated rich API errors keep their existing contracts.
- Invalid received notices must not cause partial event callbacks, state retention, cursor advancement, acknowledgement, follow or destination requests. Persisted binding inspection never rewrites bindings or claims ownership.
- Use synthetic peers, URLs, servers, profiles and bindings only. No real credentials, sessions, data stores, production probes or delivery operations.
- Every build/test runs through the x-boss build-box runner with at most two Go jobs/threads, a mode-0700 physical TMPDIR root and CI-compatible umask 0022. Save RED/GREEN output and actual wrapper exit status.
- Keep source changes within the named receive/diagnostic consumers. No database migration, web UI feature, hub behavior change or automatic production deployment is part of this client fix.

## Review Focus

1. RF1: Notice Hub/NewURL canaries, malformed/null/mistyped values, cleared notices and mixed event pages are rejected before output or state/acknowledgement effects.
2. RF2: Malformed redirect Location canaries never reach CLI stderr, prefs JSON, reconnect status, operator request/upload errors or TUI; errors.Is/errors.As and timeout/retry/exit behavior remain correct.
3. RF3: Safe signed strings, URL prefixes, IPv6 and explicit HTTP allowances survive unchanged; URL validation does not authenticate, normalize or rewrite signed content.
4. RF4: Exact 410/hub_moved errors use fixed public prose and checked recovery fields despite hostile/mismatched Message/Details; other errors and verified relocation behavior retain their contracts.
5. RF5: Stored binding listing contains no unchecked Hub URL and does not change stored bytes; shipped help-default/invite boundary regressions and valid rendered TUI frames remain passing.

## Decisions

| Approach | Goal | Decision |
| --- | --- | --- |
| Validate structured receive fields with one shared notice helper | Stop unsafe URL output and side effects without changing signed values | Use; reuses the existing validator and gives each consumer the same admission contract. |
| Search or redact arbitrary error/message strings | Same | Reject; cannot prove the structured boundary and changes unrelated user content. |
| Port the focused unmerged 39015454 component after current-source RED tests | Preserve reviewed work while completing all consumers | Use; adapt its helper interfaces and fixtures, without merging old branch ancestry. |
| List an invalid stored binding with fixed Hub label `(unusable)` | Keep inventory inspectable without exposing unchecked values | Use; preserve other fields and TTL state, and never rewrite the source file. |

## Task 1: Shared receive/diagnostic boundaries and operator client

**Files:** create `internal/hubnotice/received.go`, `internal/httpdiag/transport.go` and corresponding focused test files; modify `internal/opclient/client.go`, `internal/opclient/tail.go`; create/adapt `internal/opclient/notices_response_test.go`, `internal/opclient/redirect_response_test.go`, and valid existing fixtures in `client_test.go`/`tail_test.go` only where the new documented contract requires a real Hub URL.

**Consumes:** `hubid.CheckHubURL(raw string) error`, `hubid.ErrPlainHTTP`, `envelope.HubNotice`, `envelope.Event`, `opclient.Error`, `(*opclient.Client).Notices`, `(*opclient.Client).TailWithAck`, existing request/upload methods.

**Produces:**

```go
var ErrInvalidNotice error // fixed text, no received fields
func ValidateNotice(n envelope.HubNotice, allowHTTP bool) error
func DecodeNotice(raw json.RawMessage, allowHTTP bool) (envelope.HubNotice, error)
func ValidateEvent(event envelope.Event, allowHTTP bool) error
func ValidateBatch(events []envelope.Event, allowHTTP bool) error
func ProjectRelocation(details map[string]any, allowHTTP bool) (string, map[string]any)
func TransportError(err error) error // package httpdiag; nil remains nil; unwrap original
```

- [ ] Establish RED with named current-source regressions `TestReceivedNoticeURLs`, `TestReceivedNoticeBatchBeforeEffects`, `TestReceivedRelocationErrors`, `TestMalformedRedirectDiagnostics` (or retained more-specific legacy test names recorded in the report). Test each unsafe URL class independently in Hub and NewURL; add positive signed prefix/IPv6/public-HTTP controls. Use actual httptest responses and request/upload/tail consumers, not mocked assertions about helper calls.
- [ ] Verify RED through `.superpowers/sdd/issue-47/bin/gate.sh task1-red go test -mod=readonly -p 2 -count=1 -json -run '<focused names>' ./internal/opclient`. A missing helper import alone is not sufficient RED evidence: receive-path tests must fail against runnable original consumers.
- [ ] Implement the shared validators and safe transport presentation, adapting the narrow existing component. Decode missing/null/type errors to fixed notice refusals. Validate all notices/events before returning or invoking handlers. Project only exact 410/hub_moved errors; keep checked recovery fields and fixed public prose. Wrap transport presentation without severing its original chain or changing retry/context behavior. Check any displayed outer request URL is supplied by an already checked request path.
- [ ] Verify focused GREEN and run all `./internal/httpdiag ./internal/hubnotice ./internal/opclient` tests once via the gate script. Inspect counts, skips and actual exits; do not weaken existing assertions to obtain green.
- [ ] Commit explicit owned paths with `fix(opclient): validate received hub URLs before effects`. Write the complete report last, including exact test names, RED/GREEN evidence, reused commit provenance, changed files and any concerns.

**Independent gate:** task reviewer checks RF1–RF4 against the diff and raw gate evidence before Task 2 begins. All accepted findings, including minor defects, must be fixed and re-reviewed.

## Task 2: CLI, TUI and stored binding consumers

**Files:** modify `internal/cli/client.go`, `notice.go`, `commands.go`, `watch.go`, `codexbind.go`, `internal/tui/model.go`, `data.go` and, if the named formatter risk requires it, `layout.go`/`screen_inbox.go`; add focused `internal/cli/received_huburl_test.go`, `redirect_response_test.go`, `codexbind_huburl_test.go` and `internal/tui/received_huburl_test.go`; adjust only affected existing receive fixtures/tests. Update `CHANGELOG.md`, this plan and a receipt in `docs/plans/receipts/`.

**Consumes:** Task 1's exact exported helpers. Existing CLI newClient/send/readHubError/activeNotices/cmdNotice/cmdInbox/watch event acceptance/cmdCodex bindings; TUI notice load, tail state acceptance, hubHost and notice rendering.
**Produces:** complete issue #47 acceptance through actual CLI/TUI consumers; no new public command, schema or dependency.

- [ ] Write runnable RED canaries before production edits for notice list/post human+JSON, inbox JSON, watch event human+JSON, mixed page no-state/no-ack, TUI banner/event pane, enrol relocation Message/Details mismatch, CLI stderr/prefs JSON/reconnect malformed Location, and persisted binding JSON. Existing help-default and invitation-response tests are controls.
- [ ] Verify focused RED with the gate script and record failures against unchanged consumers. Assert synthetic destination request counters remain zero. A secret-looking field must be independently attributable; do not combine all unsafe URL classes into a single compound canary.
- [ ] Adopt shared validation at decode/acceptance boundaries with the selected profile/environment's HTTP policy, before any output/cache/cursor/acknowledgement/follow side effect. Retain the operator client's existing caller-owned public-HTTP contract. Make TUI formatter failure return fixed safe text. Defend direct TUI acceptance seams used by non-HTTP/fake API implementations as well as the normal HTTP path.
- [ ] Apply safe transport presentation at CLI transport boundaries, retain typed causes and exit 3/context behavior, and verify reconnect still schedules correctly. Exact relocation responses must not forward unchecked supplied prose or Details; keep validated signed notice fields/recovery behavior.
- [ ] For binding inspection, copy invalid rows for presentation, use `(unusable)` for Hub and keep original file bytes unchanged; safe URL prefixes and IPv6 print fully.
- [ ] Run focused GREEN and complete affected CLI/opclient/TUI/hubid/hub-command packages once. Update the receipt with all seven issue checklist items and named evidence, including the two already-shipped controls. Commit explicit owned paths with `fix(cli): check received hub URLs before display and state changes`.
- [ ] Independent task review, fix every accepted finding, and re-review changed ranges. Then obtain one fresh whole-branch review, including shared consumer seams and all RF1–RF5.

## Integration and exit tests

- [ ] Fresh full `go test -mod=readonly -p 2 -count=1 -timeout=20m -json ./...` via the gate script on the exact reviewed source; relevant vet/protocol checks. Broaden further only for new failures or changes.
- [ ] Review PASS with no accepted unresolved finding; source current with origin/master. Merge to the repository's `master` branch (its main branch) and push normally. No force push.
- [ ] Verify the merged tree against the reviewed source and required gate results; close #47 with the actual commit and evidence. Preserve the operational ledger/reviews before removing this worktree. Other worktrees and local Markdown tickets remain untouched.

Exit mapping: help/defaults → existing hub flag diagnostics; invitations → existing invite_huburl_response tests; notices/events → Task 1/2 canaries and atomic state checks; relocation → safe error projection and caller tests; redirects → request/upload/CLI/TUI typed diagnostics; bindings → unchanged-file listing test; safe values → signed URL/prefix/IPv6/HTTP positive controls and existing TUI goldens.

Try it after integration: normal `vecnet notice list --json` and `vecnet codex bindings --json` retain their interfaces in the next CLI build/release. Synthetic refusal behavior and safe transport diagnostics are exercised by the committed regression tests. This source-only issue loop does not replace the currently installed npm release or alter Fly.
