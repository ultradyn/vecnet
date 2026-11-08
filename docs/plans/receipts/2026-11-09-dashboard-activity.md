# Dashboard activity and Fly deployment receipt — 2026-11-09

## Delivered

The web dashboard leads with messages sent in the last hour, the hourly-average messages/minute, and active sending peers. A 60-point one-minute volume trace appears in the overview; each peer has its hour count, average rate and shared-scale sparkline. Charts support pointer inspection, arrows/Home/End, Escape dismissal and expandable exact-value tables. Independent 30-second snapshots preserve focus and explicitly distinguish unknown, empty, refreshing and stale/error states.

`GET /v1/admin/activity-stats` is operator-only and `no-store`. One indexed metadata aggregate reads committed non-reaction messages in the hub-clock interval `[now − 60 minutes, now)`. Source/hub messages contribute volume; active peers are distinct `peer_` senders, including the operator and dots, not presence. Historical sends remain counted when a peer is disabled or archived. No migration, service, runtime dependency or storage-flag change was added.

Source branch: `feature/dashboard-activity`, based on `5a0735a3`. Changes are intentionally uncommitted: deployment was requested, but no git commit/push was requested. Runtime source was frozen for review and deployment; public asset hashes were compared against the same local files after deployment.

## Validation

| Check | Result |
| --- | --- |
| Controller full Go suite, `GOMAXPROCS=2 go test -p 2 ./...` | PASS, 38 package success rows, exit 0 |
| Controller UI suite, `node --test --test-concurrency=2 internal/ui/jstest/` | 454 passed, 0 failed, 0 skipped |
| Static hub build, `CGO_ENABLED=0 GOMAXPROCS=2 go build -p 2 ./cmd/vecnet-hub` with scratch output path | PASS |
| Existing dashboard browser journey | PASS: independent sections, refresh, links, isolated failure, no CSP errors |
| New activity browser journey | PASS: real API, loading/empty/error/stale/recovery, keyboard/pointer/table, shared peer scale, DOM/focus retention, refresh/unmount, desktop dark/light and 390/320px |
| Selected chart palette validation | PASS in light and dark; computed/rendered colors verified |
| Fresh whole-change review | Spec PASS, quality PASS, no findings; includes independent real-app moving-window focus probe |
| Fresh visual/source-geometry review | PASS; 17 screenshots/crop inspected, no blocking defects |
| `git diff --check` | PASS |

The first controller full-suite attempt used the symlinked job scratch path and failed seven existing CLI canonical-path tests. Those tests passed with the physical scratch path. The known-invalid run was stopped, the scratch-only gate environment corrected, and the entire gate rerun successfully; no production code or assertions were changed to hide those failures.

Backend and renderer tests have recorded RED/GREEN evidence. The earlier pure-model task used empty exports before its initial RED, violating strict tests-first sequencing. That historical process lapse remains disclosed; independent review validated the assertions and a separate calendar regression's genuine RED/GREEN. It is not represented as retroactively repaired.

Embedded assets measure 654,896 bytes under the 640-KiB (655,360-byte) all-files guard. The guard increased from 624 KiB by the existing smallest-16-KiB-step convention; its summation and assertion remain intact. Remaining headroom is 464 bytes.

Illustrative reviewed fixtures, not production data:
- [Desktop dark](../shots/dashboard-activity/dark-desktop.png)
- [320px dark](../shots/dashboard-activity/dark-mobile.png)

## Deployment and verification

Controller deployed remotely to Fly app `vecnet` using the existing Dockerfile/configuration and version/date build arguments. No initialization, credential rotation, volume/storage operation, git push or commit was performed.

- Version: `2026.1108.1523+5a0735a3.dirty` (the dirty marker truthfully identifies the uncommitted source).
- Image: `registry.fly.io/vecnet:deployment-01M6XW4N5CJ2JV43SPH2WH3P5B`.
- Machine: `48e2590f495258`, `syd`, started, health check passing.
- `https://vecnet.xk.io/healthz` returned `ok: true` and the expected version.
- Production `dash.js`, `dash.css`, `api.js`, `sparkline.js` and `sparkline-model.js` SHA-256 hashes matched the tested local files.
- Unauthenticated production activity-stat requests returned `401` with `Cache-Control: no-store`.

Try the signed-in dashboard at **https://vecnet.xk.io/ui/#dash**.

## Verification limits

Authenticated production dashboard/API interaction was not performed: this session had Fly deployment access but no working production operator credential. Authenticated behavior was exercised against the isolated real demo and unit/HTTP fixtures. Production-volume latency and manual assistive-technology sessions were not benchmarked. Existing mobile sidebar stacking remains unchanged; wide peer/value tables scroll within their containers.

Raw gate logs, full screenshots, review reports, source fingerprints and deployment output are recorded in this job's scratch ledger `/home/xertrov/.claude/jobs/3c27868b/tmp/dashboard-progress.md`; the durable summary and representative captures are retained here.
