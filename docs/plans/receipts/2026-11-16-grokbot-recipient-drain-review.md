# Independent whole-branch review — issue 4 recipient drain

Spec compliance: **PASS**. Code quality: **PASS**.

Findings: **Critical 0 / Important 0 / Minor 0**. No concrete source defect or unanswered executable concern remains from this review. No additional controller probe is requested. These verdicts cover the opt-in source feature; installed-routine/live acceptance remains outstanding and issue #4 must remain OPEN.

## Immutable scope and independence

- BASE: `3f006ec68032bb5d1676fd77613642af5cd3adcc`
- HEAD: `5ac67e87590a0db10e018484ebed688590307b5f`
- Tree: `88de7d27689af70b08c6c9da5b070eb2b8609b4d`
- Complete pinned diff: `whole-branch-review.diff`, 8,980 lines, SHA256 `8cc83f26d26280881623d462f29c024b2cc1b4c83b3a1418f57eb6789968d9e8`; 57 changed files, +5,958/-193.

Read the operative `docs/plans/2026-11-15-grokbot-recipient-drain.md` and frozen `issue-4.json` first. Read all eight required original/fix implementation reports, then the complete progress ledger and bindings R1–R16. Implementation rationale was treated as a claim to check. Prior independent reviewer reports were not first-pass evidence.

Read the complete pinned BASE..HEAD diff in bounded chunks, covering migration/claim/transport, lifecycle/handling integration, onboarding/CLI/protocol, statistics and all changed tests/fixtures. Inspected the relevant unchanged Worker, delivery hydration/settlement, receipt/ack, presence/ticker, route ownership and CLI/MCP neighborhoods. This was the single fresh whole-branch seat: no children, source edits, index/HEAD/branch mutations, direct builds/tests/application probes, private configuration/runtime access, browser, GitHub activity or external messages. The only review write is this report.

## ReviewFocus matrix — concrete checked-and-fine results

Every named test below has a terminal PASS in the exact-source `logs/final-all.log`. Independently matched all **91 added top-level Test declarations** in the pinned diff to actual full-run PASS events. Assertions and relevant source were inspected; discovery/pass alone was not substituted for a contract audit.

| Focus | Source and actual test pins | Checked result |
|---|---|---|
| RF1 — T1/T3 default and wire compatibility | `internal/push/grokbot_coalescing_test.go:41` `TestMigratedLegacySevenReleaseRawBytes`; `:68` `TestGrokDrainV1SevenReleaseOnePOST`; `internal/store/grokbot_recipient_drain_migration_test.go:11` `TestGrokbotRecipientDrainMigrationUpgradeReopen` | Migrated/default/omitted routes remain raw Delivery/per-delivery. The legacy fixture counts seven real receiver POSTs and compares whole bodies; the separate explicit opt-in fixture observes one actual client/receiver POST, inclusive pending count seven and exactly the six expected first followers. Followers remain visible, unreceipted and attempt-zero with no follower reservations. |
| RF1 — API, CLI and explicit declaration | `internal/httpapi/route_transport_test.go:14` `TestRouteTransportPatch204ThenList`; `internal/cli/grokbot_transport_test.go:16` `TestRouteTransportExplicitCLI`; `:64` `TestAgentTransportExplicitCLI`; `:91` `TestAgentTransportBindingsClosedAndExplicit`; `internal/httpapi/onboarding_transport_test.go:12` `TestOnboardingTransportInputClosedTopLevel` | Create/update/list validation accepts only the declared Grok capability; invalid/null/wrong-type/other-kind inputs fail safely. PATCH remains 204/empty with the historical CLI acknowledgement and no automatic GET. Agent selection maps only to optional `inputs.grokbot_transport`; the closed top-level request and SpecVersion stay unchanged. |
| RF1 — historical onboarding authentication and instructions | `internal/hub/onboarding_transport_test.go:82` `TestTransportHistoricalMACCompatibility`; `internal/hub/onboarding_grokbot_test.go:783` `TestOnboardingDrainV1RequestReadbackInstructions`; `:825` `TestTransportChangedSameRequestIDConflicts`; `:856` `TestTransportLegacyV1ReceiptReplay`; `:916` `TestTransportRevocationReturnsLegacyAfterLease`; `internal/onboarding/grokbot_transport_test.go:69` `TestTransportDefaultDoesNotRenderDrainSupport`; `internal/mcpserver/onboarding_transport_test.go:54` `TestTransportHistoricalMCPSafeguards` | Omission and explicit legacy preserve the literal historical canonical bytes/digest/MAC; drain changes the digest and conflicts under the same request ID. The old oracle was captured against pre-change production code, not a modified encoder against itself. Saved selection/replay/status is separate from the mutable live route. SafeComplete/version and secret-redaction guards survive. Only opted instructions declare envelope support and require recipient authority, cursor-ordered full drain, per-delivery processed receipts and safe ack. No implicit opt-in or GUI addition. |
| RF2 — token ownership and duplicate/stale settlement | `internal/push/grokbot_coalescing_test.go:158` `TestMixedLegacyAndTokenReleaseSameRouteTime`; `:331` `TestGrokAmbiguousRestartLateToken`; `:425` `TestGrokTwoWorkersOneDomain`; `:766` `TestGrokDuplicatePreflightCannotWithdrawArmedPOST`; `:791` `TestGrokDuplicateResultCannotRefundSettled200`; `:814` `TestGrokKnownNoninvocationAfterBeginAndShutdownAmbiguity`; `:946` `TestGrokBeginChecksCurrentAuthorityAndRoute`; `:993` `TestGrokSettlementRollbackIsAtomic` | Begin is single-use and atomically validates current claim, expiry, route URL/key/mode, principal/owner and recipient before reserving/arming. Settlement is token/phase/lease guarded and atomic with the representative result. Legacy release selects NULL tokens only; opted release is exact route/token. Settled-200 reservations cannot be refunded by duplicate refusal/cancellation. Late old results do not reset or settle a new token, manufacture receipts or decrement twice. |
| RF2 — legacy-enable race and expired proof | `internal/push/grokbot_coalescing_fix_test.go:42` `TestGrokEnableImportsLeaseBeforeLegacySettlement`; `:119` `TestGrokImportedBarrierExcludesLegacyInFlight`; `:223` `TestGrokKnownNoninvocationExpiredLease`; `internal/hub/grokbot_coalescing_test.go:173` `TestGrokLegacyImportTransactionAndCleanup` | Legacy identities are captured in the capability writer transaction before a legacy result can clear its delivery lease. Multiple active legacy leaders remain independent barriers and are excluded from follower coverage. Their finite original deadlines survive legacy success/ambiguity. A proven noninvocation refunds only its own unsettled reservation after expiry/replacement; current-claim compensation is separately guarded and does not alter the replacement. |
| RF2 — lifecycle, idle and authority | `internal/hub/grokbot_coalescing_lifecycle_test.go:177` `TestIdleBeforeBeginAllowsNewCover`; `:191` `TestIdleAfterBeginRejectsLateCover`; `:195` `TestStaleOnIdleOnlyReleases`; `:230` `TestURLABAInvalidatesLateSuccess`; `:239` `TestPrincipalMoveSamePeerRouteInvalidates`; `:246` `TestDisableReenableAndRevokeRetainLease`; `:270` `TestRemovalAndRepairCannotTransferReservation`; `:299` `TestLifecycleBeforeBeginCompensates`; `:341` `TestStalePresenceReportDoesNotInvalidate`; `:364` `TestIdleAfterSettledSuccessClosesCover`; `:376` `TestNoopLifecycleRetainsCover` | Explicit applied idle increments the transaction-local counter, captured at Begin. Stale/unknown ticker/direct OnIdle hold release is distinct. Lifecycle hooks reach transaction-local indirect callers. ABA, key, principal, disable/archive, removal, repair/retry and revocation invalidate rights monotonically. Claimed noninvocation can compensate; armed/ambiguous I/O retains its original finite exclusion. No-op updates retain cover. |
| RF2/RF5 — actual handling and scoped acknowledgement | `internal/hub/grokbot_coalescing_handled_test.go:35` `TestProcessedAndScopedAckSuppress`; `:141` `TestReadAndAcceptedRunStartNotProcessing`; `:168` `TestAckDuringPostKeepsExclusion`; `:373` `TestAckResolvesCurrentAuthorityInWriter`; `:506` `TestAmbiguousHandledLeaderIsNotNoPost`; `:620` `TestHandledCoveredLeaderCompensatesPriorAttempt`; `:857` `TestHandledUnarmedRequiresExactClaimProof`; `:890` `TestHandledUnarmedCompensationRollsBackWithReceipt` | Suppression annotations use current writer-transaction authority and the exact changed population. Read/no-op/accepted-run-start/armed ambiguity do not count as processed no-POST work. Proven unarmed handled leaders retire their claim and undo its claim attempt without changing the real handled state/remaining attempts or depending on whether a first-cover INSERT succeeded. Receipt/claim rollback is atomic. |
| RF3 — finite shared cadence and recovery | `internal/push/grokbot_coalescing_test.go:236` `TestGrokTerminalThenValidArrival`; `:265` `TestGrokLeaderDiscardExpiryCollectionKeepsDeadline`; `:306` `TestGrok429FollowerCannotBypass`; `internal/hub/grokbot_coalescing_handled_test.go:260` `TestRepairAndExplicitRetryBoundedRecovery`; `:304` `TestSharedFiniteFailureCadenceAfterLeaderLoss`; `:464` `TestExplicitRetryDuringPostRetainsLease`; `:699` `TestHandledUnarmedRetirementPreservesIndependentWait`; `:758` `TestHandledWaitingRetryKeepsCadence`; `internal/push/grokbot_coalescing_fix_test.go:319` `TestGrokCoveredRetryAfterCoordinatorCleanup` | Retry deadline/failure ordinal belongs to the domain and survives replacement, terminal/discard/expiry/collection and handling. Followers and new arrivals cannot restart backoff or extend an existing finite lease/cover. Later valid work recovers after its gate; removed coordinators do not leave historical cover as a permanent eligibility gate. Repair cannot withdraw armed ambiguity. Worker Step remains claim-count semantics; representative attempts/DeferPush remain separate from actual client-entry telemetry. |
| RF4 — bounded materialization, continuation and cleanup | `internal/hub/grokbot_coalescing_test.go:16` `TestGrokQueryPlansManyDomains`; `:109` `TestGrokEmptyGateCleanup`; `:144` `TestGrokGateRollbackPreservesReservation`; `:173` import/cleanup test; `internal/push/grokbot_coalescing_test.go:448` `TestGrokOneConnectionHydrationRollback`; `:479` `TestGrokOver16SameThreadContinuation`; `:511` `TestGrokAttachmentPageDoesNotStarve`; `:867` `TestGrokCrossRouteThreadOrder` | Candidate discovery uses keyset pages of 16, closes readers before representative hydration, and continues after attachment-only pages. SQL COUNT and INSERT SELECT cover followers without materializing domain/follower ID sets. Query-plan fixtures exercise 2,049 domains and import/cleanup fixtures 2,048 identities, with one-connection/rollback controls. Same-thread and cross-route order survive continuation. Empty finite gates clean up; coordinator/import rows do not FK-pin leaders/routes. SQL time/writer-lock work and retained history are not claimed bounded. |
| RF5 — frozen bytes, accounting and isolation | `internal/push/grokbot_coalescing_test.go:532` `TestGrokFrozenEnvelopeInlineReferenceRetry`; `:617` `TestGrokFullEnvelopeBodyCap`; `:898` `TestGrokSharedRouteCountsOnlyNamedRecipient`; `internal/hub/grokbot_coalescing_lifecycle_test.go:412` `TestUnarmedRepairPreservesFrozenRetryBody`; `internal/hub/grokbot_coalescing_handled_test.go:214` `TestBulkCoverExcludesLeaderAndHandledBeforeBeginIsSeparate`; `:242` `TestReCoverReplacementKeepsFirstEvidence` | Exact original whitespace/escape/reference bytes are inserted directly into the outer envelope; full body/count persists across retry/restart/repair and later arrivals. The complete body is capped and measured in BodyBytes. Counts, elections, invocation, first coverage, processing and billing remain different populations. Current elected/armed/imported leaders are excluded from follower first cover; handled-before-Begin is separate evidence. Sibling counts/coverage/receipts/reservations never transfer. Prior first cover is retained. |
| RF5 — operator statistics and common surfaces | `internal/hub/route_stats_test.go:999` `TestRouteStatsSixCoveredOneRequest`; `:1075` `TestCoalescedFirstCoverageDistinctWindowAndMove`; `:1136` `TestCoalescedRecoversAndHandlesWithoutRewritingFirstEvidence`; `:1209` `TestCoalescedOperatorOnlySnapshot`; `internal/mcpserver/route_stats_test.go:600` `TestCoalescedHTTPMCPCLIParity`; `internal/cli/route_stats_test.go:554` `TestCoalescedCLIInt64AndOldServer` | The real client/receiver fixture observes one request, seven pending and six exact covered IDs. Common HTTP/MCP/CLI uses always-present int64 Coalesced, including zero for other populations and old-server omission. Counts use distinct retained first cover on the historical route and inclusive window, independently of the 1,000-attempt sample (1,003-cover fixture), current routing or membership. Operator authority, consistent snapshots and safe failures are retained. No billing reduction is inferred. |

The production seams supporting these results are `internal/hub/grokbot_coalescing.go:34` (Begin), `:101` (proof-bearing cancellation), `:157` (result settlement), `internal/hub/grokbot_coalescing_claim.go:23` (set coverage), `:36` (candidate query), `:154` (election), `:203` (frozen body), and `internal/hub/grokbot_coalescing_lifecycle.go:11`, `:39`, `:65`, `:87` (unarmed cancellation, explicit idle, handling and compensation). `internal/push/grokbot_coalescing_transport.go:13` separates preflight, Begin, proof-bearing cancellation and ambiguous client entry. `internal/hub/route_stats.go:54` reads the retained historical cover population in the existing snapshot.

Migration inspection found only new unshipped `0039` changed/added in this range; historical `0001`–`0038` bytes/checksums remain unchanged. The populated upgrade/reopen test preserves old values, raw bodies, acknowledgements and attempts, then checks additive defaults and cleanup/cascade behavior. Strict inner Delivery/event schemas and Worker source remain unchanged; the outer transport has its own version.

## Final-gate barrier and source binding

Read the complete `final-gates-evidence.json` after it appeared, completed `2026-11-15T17:06:42.835587+00:00`. It names the exact HEAD/tree above, four exit-zero commands, 2,252 source entries and `committed_source_matches=true`. Independently parsed the raw final logs and all four per-gate evidence files, `.head`, `.exit` and before/after manifests. All four manifests are identical before/after and across gates. Compared every one of the 2,252 SHA256 entries with actual committed Git blob bytes and current files: zero missing/mismatched entries. All 57 changed-path hashes also match the aggregate. HEAD/tree/diff hash remain pinned; tracked working tree is clean. No source edit followed a covering gate.

| Separate controller execution | Actual result |
|---|---|
| `go test -json -p 2 -count=1 -timeout=20m ./...` | 8,156 PASS / 0 FAIL / 3 named baseline test/subtest SKIPs; 42 PASS package terminals and three actual no-test-files package SKIP terminals. Every test run has a matched terminal; no unfinished/build-failure event. |
| `go vet -p 2 ./...` | Exit 0, wrapper/buildbox 0; no failure. This is not a test-event population. |
| `env CGO_ENABLED=1 CC=/usr/bin/gcc go test -json -race -p 2 -count=1 -timeout=45m ./internal/hub ./internal/push` | 1,275 PASS / 0 FAIL / 0 SKIP / 0 unfinished; hub 1,042 and push 233, both package PASS. No race/build failure. |
| `just validate-protocol` | Exit 0; schema-copy diff succeeded, 11 positive example validations and two intended negative validations produced `ALL GOOD`; R2 blob/reference/TTL/retry/hygiene/dependency fixtures executed successfully. No example-dependency skip occurred. This output is not Go JSON or TAP. |

All four raw logs have exactly one `bb: exit=0`, matching wrapper and command exits. Controller wrapper/source and logged invocations retain the serialized x-boss lane, two jobs/threads, umask0022 and private physical TMPDIR; the buildbox wrapper supplies synthetic HOME/XDG. No direct reviewer execution was used. The 45-minute race package timeout is the R15 adjustment documented for instrumented pure-Go SQLite large fixtures in `docs/BUILDBOX.md:38`; it does not increase the job/thread cap.

Full-repository execution includes all ten affected packages. Their actual final terminal counts total 5,505 PASS and the same two CLI skips. No separately repeated affected run is claimed. The historical ten-package baseline was 5,315 PASS / 0 FAIL / 2 skips; it is not relabeled as a three-skip baseline.

Actual full-run per-package **test/subtest terminal events** follow. Package names below are relative to `github.com/XertroV/vecnet/`; each listed package has a PASS package terminal.

| Package | PASS events | Test/subtest SKIPs |
|---|---:|---:|
| `bench` | 1 | 0 |
| `cmd/vecnet-hub` | 203 | 0 |
| `internal/audit` | 3 | 0 |
| `internal/auth` | 6 | 0 |
| `internal/avatar` | 37 | 0 |
| `internal/blob` | 175 | 0 |
| `internal/blobclient` | 79 | 0 |
| `internal/blobwire` | 14 | 0 |
| `internal/buildinfo` | 1 | 0 |
| `internal/cli` | 2531 | 2 |
| `internal/egress` | 13 | 0 |
| `internal/envelope` | 114 | 0 |
| `internal/eventlog` | 60 | 0 |
| `internal/eventpayload` | 56 | 0 |
| `internal/events` | 154 | 0 |
| `internal/httpapi` | 669 | 0 |
| `internal/httpdiag` | 11 | 0 |
| `internal/hub` | 1042 | 0 |
| `internal/hubid` | 34 | 0 |
| `internal/hubnotice` | 70 | 0 |
| `internal/ids` | 8 | 0 |
| `internal/inboxview` | 11 | 0 |
| `internal/ingress` | 12 | 0 |
| `internal/localruntime` | 62 | 0 |
| `internal/logging` | 2 | 0 |
| `internal/mcpdiag` | 9 | 0 |
| `internal/mcpserver` | 716 | 0 |
| `internal/oauth` | 94 | 0 |
| `internal/onboarding` | 9 | 0 |
| `internal/opapi` | 7 | 0 |
| `internal/opclient` | 796 | 0 |
| `internal/opseed` | 6 | 0 |
| `internal/opseed/demo` | 34 | 0 |
| `internal/opview` | 85 | 0 |
| `internal/prefs` | 42 | 0 |
| `internal/push` | 233 | 0 |
| `internal/store` | 128 | 0 |
| `internal/tui` | 595 | 1 |
| `internal/tui/theme` | 27 | 0 |
| `internal/tui/theme/themetest` | 1 | 0 |
| `internal/ui` | 2 | 0 |
| `internal/webhook` | 4 | 0 |

The three named baseline test/subtest skips are:

- `internal/cli` — `TestLauncherInstallRejectsUnsafeSnapshotReuse/foreign_runtime_owner`: foreign-owner fixture unavailable without privileges (`launcher_test.go:403`).
- `internal/cli` — `TestLocalRegistryProcessWorker`: subprocess-only helper (`localregistry_test.go:312`).
- `internal/tui` — `TestWriteShots`: opt-in frame writer; `VECNET_SHOTS` unset (`shots_test.go:20`).

Separately, the raw log has actual `[no test files]` package SKIP terminals for `cmd/vecnet`, `connectors`, and `internal/testhub`. They are neither passing test cases nor additional skipped tests. No unknown skip was waived.

The first controller aggregator rejected these actual no-test-files package terminals after the successful full run. That infrastructure failure is retained. The corrected source-bound resume reused that same exit-zero run; there was no application RED→PASS transition or invented second full execution.

## Historical RED/setup/PASS evidence distinctions

Read and independently parsed retained raw Go JSON, rather than accepting report summaries as the execution oracle. Parent/subtest event counts are not counts of unique assertions.

- T1 `t1-01-old-api-red.log` is the unsupported/busy fixture setup failure. `t1-02-old-api-red.log` is the genuine compiled old-API seven-POST symptom RED. `t1-03-declarations-setup.log` is compile-only setup (zero tests/five packages), not behavioral RED. `t1-04-opted-and-release-red.log` contains two genuine feature/release failures. Permanent migrated-legacy-seven and explicitly-opted-one tests are distinct; the historical diagnostic is not claimed to pass unchanged.
- T1 fix1's first gate is a helper compile/setup failure. `t1-fix1-02-regressions-red.log` contains seven genuine failing events for lost legacy barrier, covered imported leaders and expired proof refund; final source fixes the underlying independent identities/refund seam. Fix2 `t1-fix2-01-recovery-red-oracle-controls.log` has ten passing controls and one genuine recovery failure. The corrected sender-side receipt-absence oracle has a positive control and is not misrepresented as a production RED.
- T2 corrected feature RED has 13 PASS/27 FAIL events; the current-authority adversarial gate has three genuine failures, no-op controls four, and frozen-retry control one. The final handled-compensation RED `t2-fix1-02-final-regressions-red.log` has 19 PASS/15 FAIL, followed by the corresponding corrected implementation GREEN. Armed/ambiguous/unrelated controls stay distinct from handled unarmed claim compensation.
- T3 `t3-01-historical-oracle.log` has one PASS against the original eleven-field canonical encoder before its production change. The literal fixture/digest/MAC source binding predates the new encoder. `t3-04-transport-red.log` has 2 PASS/12 FAIL. `t3-06-review-edge-red.log` combines behavioral failures and a CLI build failure; `t3-07-expanded-green.log` still has a build failure and exit1 despite 17 PASS events, so is not final GREEN. Corrected `t3-08-corrected-edge-red.log` has 6 PASS/2 FAIL. R13's three projection failures are compatibility-fixture maintenance, not a coalescing symptom. The documentation/CLI-oracle fix did not require a fictitious production RED; valid key and omission positive controls now exercise the intended rejection.
- T4 `t4-01-dto-compile.log` has zero tests/six compile-only package passes. `t4-02-coalesced-red.log` contains twelve genuine failing events; `t4-03-coalesced-green.log` contains the corresponding twelve passes. Final full-run/RF5 fixtures additionally use real synthetic payload-client and receiver invocation counts rather than only claims/stat rows.

These historical failures remain retained. They do not contaminate or substitute for the final exact-source four successful executions. Partial race evidence was never used as a PASS; this report was withheld until the completed final barrier was read and checked.

## Scoped rulings and limits

R1/R2 preserve explicit opt-in and source-only acceptance. R5's cohesive helper split is within the coordinator/lifecycle seam. R7's migration-test adjustments preserve original backfill/rollback/old-value assertions and explicitly check additive defaults rather than assuming old column counts. R8's new finite imported-identity table and settled-200 fence address the capability/legacy race without modifying historical migrations. R9/R11 place idle/authority hooks at the actual writer-transaction seams, including indirect lifecycle callers. R10 fixes absent-coordinator Boolean semantics without erasing retained first cover. R12's literal historical oracle preserves the original authentication contract. R13 changes only the optional safe MCP enum projection/description and its focused guard; old required properties/types/constraints remain. R14 adds only the always-present zero field in two adjacent dispatch literals, with an explicit old-server-omission control. R15 full-repository subsumption and race-timeout changes preserve the four distinct validators. R16's overlap was read-only, and its final barrier was satisfied before this report. None of these scoped deviations weakens the user-observable contract.

The checked execution surface is synthetic testhub/httptest through the controller's serialized gate. It does not verify an installed Grok identity/profile, deployed envelope adaptation, administrator opt-in, actual external full inbox draining, processed receipts, safe acknowledgement or paid-run reduction. No external routine compliance is inferred from fixtures. Issue #4 remains OPEN for that live acceptance.

SQL count/set coverage has O(n) work and writer-lock cost; bounded Go candidate memory is not a hard bound on SQL time or retained-history storage. Coverage can disappear with retained delivery collection, so Coalesced is the documented retained historical population. Ambiguous network/client entry conservatively keeps the original finite reservation/exclusion; neither physical exactly-once nor remote billing/processing is established. These are explicit contract limits, not deferred findings or an unsupported acceptance claim.

=== DONE ===
