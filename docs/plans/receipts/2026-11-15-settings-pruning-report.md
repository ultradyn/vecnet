# GitHub #51 Settings pruning report — source acceptance

Reviewed whole branch `14e77c17e171c296de0885559271f5b866a88255`; executable source
`e208e271f7e81a7a928ada002d9eefe8a5ebccd2`, base master `6902e9de`.

Successful operator preferences GET/PATCH on disk and R2 hubs exposes the
output-only startup boolean `runtime.event_retention_enabled`. The shared
preferences ETag binds that boolean, so reconstructing with a changed gate
invalidates conditional reads and stale write preconditions even at the same
stored revision. Descriptor values, source, runtime/editability, optional
storage and preview/confirmation authority retain their existing contracts.

Settings List and Policy display enabled, disabled or not-reported as the
hub's last report. Saving an editable retention policy cannot enable pruning.
Only accepted network responses update status; generations, 304 barriers,
cached-conflict isolation and busy/failure/destroy checks prevent stale
reports. Saved PATCH values still reconcile independently. Archive text is
qualified and the existing recipient-warning Policy control is restored.
No worker-health monitor, activation, retention behavior or installed release
is included. GitHub #51 remains open for its other follow-ups.

Two Sol implementation subagents used PIRFL with independent per-task reviews.
Task1 backend review: Spec compliance PASS, Code quality PASS, no findings.
Task2 review: Spec compliance PASS, Code quality PASS, no findings.
Fresh whole-branch review: Spec compliance PASS, Code quality PASS, no findings.
No accepted finding, including minors, is deferred. Closing metadata preserves
all nine tested/reviewed source hashes.

Behavioral RED showed missing runtime metadata, unchanged gate-restart ETags
and stale conditionals. An existing Snapshot round-trip oracle was adapted to
compare every Snapshot-owned field while separately asserting HTTP-only runtime
metadata. Current affected backend evidence combines full HTTP667 with retained
identical-production hub938/prefs42/opapi7: 1,654 passes, zero current fails or
skips, from separate gates. The original overall wrapper1 is retained as
historical evidence rather than relabeled as a successful four-package run.

Task2 status RED: 85 pass/26 fail. Final source focused119, full UI641 (613
top-level), Go UI2 and full real-demo Settings journey pass, all zero failures,
skips or cancellations where applicable. The controller independently ran
focused119 and checked raw counts, exits and all9 before/after SHA256 manifests
against committed blobs. An additional current-source HTTP UI contract gate passes all 8 tests, including size cap, static serving and CSP. Full journey includes all existing row/edit/request/
confirmation/conflict assertions and98PNG artifacts. The controller viewed
four actual final gate-off captures at1440x900 and390x844: status is legible,
wraps within viewport and preserves visible controls. True/missing metadata
states have mounted-DOM coverage, not mocked browser captures.

The full journey initially exposed a pre-existing missing Policy token for
`presence.send_warning_after`; R8 restores it with canonical fixture and mounted
value/source/edit coverage. R9 qualifies the adjacent archive pill. Historical
visual failures and a fixture-risk regression (caught by the unchanged409
fallback test) remain in raw logs; final full/focused reruns pass after the
fixture correction. An earlier Task1 resume attempt failed before worker
launch; it is retained and the exact original thread was resumed successfully.
No implementation or evidence failure is silently waived.

All tests/builds/browser activity used the serialized x-boss lane, two jobs,
private physical TMPDIR, umask0022 and synthetic HOME/XDG. No real messages,
profiles, services, gate activation or installed deployment were exercised.

Try synthetically: `SHOTS_DIR=/tmp/vecnet-settings-shots BB_HOST=x-boss BB_JOBS=2 BB_PULLBACK=0 just bb node scripts/ui/prefs.mjs`.
Raw operational reports, gates and PNGs must be copied and hash-verified outside
the owned worktree before removing it and its merged branch.

[Backend review](2026-11-15-settings-pruning-report-task-1-review.md),
[UI review](2026-11-15-settings-pruning-report-task-2-review.md),
[whole-branch review](2026-11-15-settings-pruning-report-final-review.md) and
[ledger](2026-11-15-settings-pruning-report-ledger.md).

A final-review package assembly attempt also stopped before worker launch when
a broad credential-pattern check matched a synthetic fixture Authorization
expression. The controller verified the exact code expression, retained the
failed attempt and launched the corrected package; no private value was present.
