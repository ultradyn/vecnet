# Grok recipient-drain source acceptance — GitHub #4

Reviewed source `5ac67e87590a0db10e018484ebed688590307b5f`, tree `88de7d27689af70b08c6c9da5b070eb2b8609b4d`, on original master base
`3f006ec68032bb5d1676fd77613642af5cd3adcc`.

Explicit `recipient_drain_v1` routes now coalesce wakes for one route and
recipient. Existing and migrated routes retain raw Delivery bodies and
per-delivery behavior through default `delivery_v1`. The new transport sends
a frozen versioned wake envelope with the original inner bytes, advisory
pending count and recipient-scoped drain instructions. Followers stay in the
inbox without fabricated receipts, attempts or run reservations.

The durable coordinator preserves finite retry/lease/cover boundaries and
exact invocation-token accounting across lifecycle changes, handling and
recovery. Route creation/update, CLI and onboarding expose explicit selection;
omitted or explicit legacy onboarding preserves historical canonical bytes and
MAC. Route statistics add always-present int64 `coalesced`, counting retained
first-cover evidence independently of the capped attempt sample and current
ownership. HTTP, MCP and CLI share this population.

Four implementation tasks passed independent spec and quality reviews, with
accepted findings corrected and re-reviewed. The fresh [whole-branch review](2026-11-16-grokbot-recipient-drain-review.md)
also passed both axes. The [ledger](2026-11-16-grokbot-recipient-drain-ledger.md)
records source-bound rulings and original failed/setup gates. No accepted
source finding is deferred.

## Final verification

All execution used the serialized x-boss build-box lane with two jobs/threads,
synthetic HOME/XDG/fixtures, private physical TMPDIR and umask0022.

- Full repository: **8156 passing test/subtest events, zero failures,
  three existing named skips**: unavailable foreign-owner privileges, the
  subprocess-only registry helper, and the opt-in TUI screenshot writer.
  Three packages with no test files are reported separately, not as tests.
- Full hub/push race run: **1275 passing events, zero failures/skips**;
  45-minute package timeout accommodates the documented large pure-Go SQLite
  fixtures. No scale assertion or fixture was removed.
- `go vet -p 2 ./...` and actual protocol/example/blob validation: **exit0**.
- All 2252 executable/source fixture manifest entries match
  before/after/current files and actual reviewed commit blobs. Closing
  documentation changes preserve those bytes. The full repository run includes
  all ten affected packages; no separate repeated affected run is claimed.

The central real-client/httptest fixture checks seven pending messages,
exactly six covered follower IDs and one actual POST, with Requests1/Coalesced6.
It preserves inbox visibility and absence of follower attempts/receipts/budget.
Legacy seven-POST/raw-body, frozen inline/reference bytes, full-envelope cap,
authority, lifecycle, exact-token/NULL-only accounting and finite recovery
controls also pass. Task-specific behavioral REDs are distinguished from
compile-only setup and additive compatibility fixtures in the retained reports.

The controller's first final-gate aggregator stopped after the successful full
suite because it rejected three actual no-test-files package terminals. That
orchestration failure is retained. The corrected aggregator verifies their
literal raw output and resumes only the same successful command/HEAD/source;
it does not relabel a failed application run or repeat that suite.

| Retained raw gate | SHA256 |
| --- | --- |
| `final-all` | `8704c60caa393b7c93fbe31b805a2adeeb73c40d8f9be8e62956a08759596c85` |
| `final-vet` | `7f14d5fa3c5cb6e510b3757cdafa464fe47d7c8537987a4f9d7c4fe71cbae40d` |
| `final-race` | `1d2d81206b97a5178275a5824ecceefbc96e43573d080fd95c7035049ff56cab` |
| `final-protocol` | `3dcd9ae6f9b2ce554ab000d6c3d365377f603edb67f44ad014ce821fe25c6284` |

Try the synthetic behavior from a checkout with the build-box tooling:

```sh
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 CGO_ENABLED=0 go test -p 2 -count=1 ./internal/hub ./internal/push -run '^(TestRouteStatsSixCoveredOneRequest|TestGrokDrainV1SevenReleaseOnePOST|TestMigratedLegacySevenReleaseRawBytes)$'
```

## Remaining installed acceptance

GitHub #4 remains OPEN. Source acceptance does not adapt or activate installed
routines. Verify the intended recipient and credential authority, envelope
support, complete authorized inbox draining, per-delivery processed receipts,
safe acknowledgements and actual paid-run reduction before opting in a live
route. The checked Fly topology has three separate peer-owned Grok routes;
external routine credential scope was not established. No paid external run,
remote-processing proof or physical exactly-once guarantee is claimed here.

The original v0.2.4 release/install predates this source loop. This receipt
does not claim another installed release or production rollout. Complete
operational logs, scripts, reports and failed gates must be archived and
hash-verified outside the worktree before cleanup.

## Integration

Master fast-forwarded to `177e621dfc958e4ba4c48e0f97329f07662ee07d`. All
2,252 source/fixture entries in the merged checkout match the fully tested and
reviewed source. A fresh targeted check of that identical merged commit through
the owned worktree gate passes eight events with zero failures/skips, including
legacy seven-POST/raw bytes, opted one-POST/count7, two workers, current scoped
acknowledgement, coordinator-cleanup recovery and Requests1/Coalesced6.
Closing integration metadata changes only Markdown; installed acceptance stays
open and no production activation is included.
