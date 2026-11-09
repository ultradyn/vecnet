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


## PR #62 committed integration — 2026-11-09

The sections above describe the original uncommitted Fly v49 deployment. PR #62 preserves that source in git and updates its presentation: exact minute values appear in pointer/keyboard readouts, with no expandable raw-value tables. Peer readouts remain inside their rows at narrow widths. Removed or filtered peers release chart/row ownership; restored views start without stale inspection, and surviving views preserve identity and focus. SVG accessible names use the existing control/bidi sanitizer.

Product pin `ab862a52` includes the correction `7519a251` and preserves GitHub's merged OAuth PR #58 (`958111d3`). Later commits only repair/index screenshot fixtures: `cb679284` normalizes tmux tab-compressed whitespace for matching while retaining original ANSI raster bytes; `3b7c5801` reuses reviewed launcher header/row captures. The current gate pin is `f8b7b186`. No database migration, runtime dependency or storage-flag change is introduced.

| Integration check | Result |
| --- | --- |
| x-boss full Go suite | 5,147 test passes, 2,014 top-level tests, 38 passing packages; actual exit 0 / build-box exit 0. Existing opt-in `TestWriteShots` skipped. |
| x-boss canonical UI suite | 458 passed; zero failures, skips or cancellations. |
| New retention/accessibility regressions | Three genuine assertion failures before production correction; focused suite 10/10 green afterwards. |
| TUI whitespace regression | Tab-compressed live status reproduced as one focused failure with three controls passing; all four helper tests green after correction. Offline/reconnecting controls remain negative. |
| Independent source reviews | Whole-PR findings I1/I2 addressed; scoped product, route integration, timeout/index and tmux repair reviews PASS with no new findings. Launcher fixture integration review PASS. |
| Full x-boss screenshot gate | PASS: all 389 generated PNGs indexed exactly once; actual exit 0 / build-box exit 0. |
| Representative visual check | Desktop overview and 320px peer inspection: complete readable readout within its row/viewport, visible keyboard focus; synthetic data. Final captures of desktop, 320px focus, launcher table header and launcher row inspected and passed. |
| Embedded static assets | 654,213 bytes under the unchanged 640-KiB (655,360-byte) all-files guard; 1,147 bytes headroom. |

Gates use disposable HOME/XDG/CODEX roots and physical temporary paths on x-boss, at most two threads in aggregate. All 2,028 gate source hashes are checked before and after locally/remotely; two empty owned-process drains are retained. Go and UI evidence applies to the final unchanged product bytes; subsequent differences are reviewed screenshot harness, index, receipt and generated-image changes, verified separately.

Evidence SHA-256:

- Full Go: `9c3e84c497b17dcb3354165f846d48cbe78ddccc5c37f220e7b6d52a5997a716`.
- Canonical UI: `c641060ff6f88491b84c3136305c8169a3ce53355985d966357d39edb92926cf`.
- Genuine feature RED: `09b13ae7c54e7a60ed40faf4c2dd04227cbf8f598449ff1ca40536a4fed2f582`.
- Focused feature GREEN: `9282beac6947fc2e2257cae38638f2e260add86189153a6153eee87771b90f5c`.
- TUI helper RED/GREEN: `7ec6f8c1f50c441d65334827b9fde8bf00624ee4c3f3dc86ef883b07887b9f46` / `9538b2e391611a47c65897c04f1fd112de31b01ed4b3b51d10c4ff5030bd464d`.
- Full screenshots: `f827b5008c029fee1f588872236a894227dcc5add09048a4751a378156592987`.

Superseded gates remain recorded as failures: the first feature RED included an invalid synthetic peer ID, corrected before the three genuine RED assertions; two full screenshot attempts reached a literal-space TUI wait, whose retained frame proved `live\tapr 2`; the next reached an obsolete requirement for the peer table header and taller final row to fit one screen. None is represented as a pass. The original pure-model TDD lapse above also remains disclosed.

The screenshot suite uses isolated synthetic hubs and browser instances. Production acceptance after deployment verifies health/build identity and served asset hashes; it does not establish production-volume latency, multi-GB browser-memory resolution, or manual assistive-technology acceptance. Native Codex actor-policy acceptance and the broader memory work remain separate.
