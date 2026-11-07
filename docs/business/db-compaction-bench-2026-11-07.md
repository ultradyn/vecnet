# DB compaction measurements — 2026-11-07

Disposable modernc/SQLite fixtures on **x-boss**, FULL durability, GOMAXPROCS=2, seed 8. No real hub, Fly volume, deployment, full VACUUM or existing NONE conversion was used. This is a core hub/storage comparison; it does not establish production capacity or a Fly SLA.

## Provenance and reproduction

Raw matrix: [2026-11-07-compaction-measured.json](../../bench/results/2026-11-07-compaction-measured.json); supplemental snapshots/plans: [2026-11-07-compaction-supplement.json](../../bench/results/2026-11-07-compaction-supplement.json).
Go go1.27.1; CPU AMD RYZEN AI MAX+ 395 w/ Radeon 8060S; started 2026-11-07T06:41:43.405092276Z. Production base 8e3b780; schema: `0001_init.sql,0002_reach_via.sql,0003_blob_uploaders.sql,0004_delivery_read_at.sql,0005_event_subscriptions.sql,0006_oauth.sql,0007_grokbot_routes.sql,0008_sources.sql,0009_connector_hardening.sql,0010_audit_action_index.sql,0011_inbox_indexes.sql,0012_reactions.sql,0013_delivery_dispatching.sql,0014_principal_peers.sql,0015_reserved.sql,0016_peer_avatars.sql,0017_peer_name_open.sql,0018_sender_info.sql,0019_event_subscription_status.sql,0020_event_refresh_before.sql,0021_hub_identity_notices.sql,0022_hub_preferences.sql,0023_event_payload_refs.sql,0024_backfill_exceptions.sql,0025_r2_blobs.sql,0026_event_retention_floors.sql,0027_event_outbox_retention_seq.sql,0028_callback_retention_lookups.sql,0029_event_retention_rule_intervals.sql,0030_callback_retention_identity.sql,0031_idempotency_created_at_index.sql,0032_thread_seq_history_tombstones.sql,0033_thread_history_loop_parent.sql`. Exact source SHA-256 values are in each raw receipt. The final delivery commit adds this report and gate receipts; it does not change wire rendering. A subsequent reclaim-cap guard also accounts for SQLite locking pages at the 1 GiB boundary; the measured fixtures are below that boundary, and the final gate covers the completed source tree.

```sh
BB_HOST=x-boss BB_JOBS=2 BB_LANE=db-t08-perf just bb GOMAXPROCS=2 VECNET_BENCH_REVISION=8e3b780 CGO_ENABLED=0 just bench -suite compaction -min-sends 80 -out bench/results/2026-11-07-compaction-measured.json
BB_HOST=x-boss BB_JOBS=2 BB_LANE=db-t08-perf just bb GOMAXPROCS=2 VECNET_BENCH_REVISION=8e3b780 CGO_ENABLED=0 just bench -suite compaction -compaction-focus supplement -min-sends 80 -out bench/results/2026-11-07-compaction-supplement.json
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 python3 bench/report.py --compaction bench/results/2026-11-07-compaction-measured.json --supplement bench/results/2026-11-07-compaction-supplement.json --out docs/business/db-compaction-bench-2026-11-07.md
```

The existing `just bench` command now accepts `-suite compaction`; source: [compaction.go](../../bench/compaction.go), [compaction_extra.go](../../bench/compaction_extra.go). Each paired fixture has identical seeded text, identities, clock, configuration and lifecycle. It makes 80 serial real `Hub.Send` calls, reads 200-event pages repeatedly through `Log.Read`, and claims one fan-out group per `ClaimDue` call. Every claimed payload must match its stored SHA-256. No push route is contacted. Subscription fixtures use private loopback callbacks, enqueue exact bodies on a 503, then settle them with 204 before retention. Core push timing excludes network time. Varied text is seeded printable ASCII with quotes, backslashes and newlines; Unicode compatibility is covered by the existing frozen corpus.

All fixtures are populated and ANALYZEd before read/push timing and query plans. Each storage capture records pre-checkpoint DB/WAL state and PASSIVE progress, then uses the harness’s existing quiescent TRUNCATE probe for per-object dbstat. TRUNCATE here is benchmark instrumentation; production maintenance only uses PASSIVE. The raw `Stats.db_bytes` and `Stats.wal_bytes` are the pre-probe files; `Storage.DBBytes/WALBytes` are after that probe. Neither checkpoint nor deletion is reported as vacuum. Auto-checkpoints may reuse an already allocated WAL; WAL file length is an allocated high-water measure and is not total WAL bytes written.

Controls added by Task 8: `--storage-maintenance` enables slow-tick PASSIVE checkpoints; `--storage-reclaim-pages` independently selects 0..128 physical pages per slice (default 0); `--storage-maintenance-target` defaults to 50 ms. All maintenance is disabled by default. `GET /v1/admin/storage/maintenance` is operator-only, non-cacheable, and exposes the last in-process slice’s counts, page/file sizes, free space, checkpoint progress and pause reason. New empty files receive 4096-byte pages and INCREMENTAL before schema creation; existing modes/page sizes remain intact. On-disk tests cover NONE reuse and quiesced backup/restore, reader-pinned yield/resume, low headroom between steps, pooled settings and pointer-map cap boundaries.

## Immediate physical live-page savings

Live bytes = `(page_count - freelist_count) * page_size`. The following values subtract each fixture’s own empty-schema baseline and divide by accepted sends. All table/index pages, including growth in maintenance indexes, are charged. Empty root pages are fixed schema overhead shown in raw baselines and the index inventory. These are allocated live pages, not logical forecasts.

| Profile | Inline live B/send | Reference live B/send | Saved B/send | Saved % |
| --- | --- | --- | --- | --- |
| F1/2048/repeated-new | 11059.2 | 7731.2 | 3328.0 | 30.1 |
| F1/2048/varied-reused | 10342.4 | 6963.2 | 3379.2 | 32.7 |
| F1/4096/repeated-new | 13260.8 | 8550.4 | 4710.4 | 35.5 |
| F1/4096/varied-reused | 14438.4 | 9011.2 | 5427.2 | 37.6 |
| F2/2048/repeated-new | 16896.0 | 10598.4 | 6297.6 | 37.3 |
| F2/2048/varied-reused | 15923.2 | 9625.6 | 6297.6 | 39.5 |
| F2/4096/repeated-new | 20480.0 | 11417.6 | 9062.4 | 44.2 |
| F2/4096/varied-reused | 22118.4 | 11673.6 | 10444.8 | 47.2 |
| F5/2048/repeated-new | 32972.8 | 17305.6 | 15667.2 | 47.5 |
| F5/2048/varied-reused | 31385.6 | 15718.4 | 15667.2 | 49.9 |
| F5/4096/repeated-new | 40857.6 | 18329.6 | 22528.0 | 55.1 |
| F5/4096/varied-reused | 53964.8 | 17766.4 | 36198.4 | 67.1 |
| F20/2048/repeated-new | 125593.6 | 73062.4 | 52531.2 | 41.8 |
| F20/2048/varied-reused | 127129.6 | 68300.8 | 58828.8 | 46.3 |
| F20/4096/repeated-new | 198912.0 | 74496.0 | 124416.0 | 62.5 |
| F20/4096/varied-reused | 196915.2 | 72396.8 | 124518.4 | 63.2 |
| F2/2048/varied-new/reference | 16896.0 | 10598.4 | 6297.6 | 37.3 |
| F2/2048/varied-new/hosted | 17561.6 | 11929.6 | 5632.0 | 32.1 |
| F2/2048/varied-new/1 subs per recipient | 22169.6 | 15820.8 | 6348.8 | 28.6 |
| F2/2048/varied-new/3 subs per recipient | 33331.2 | 26931.2 | 6400.0 | 19.2 |

The F=1 results above include page quantization. Every compared F=2/5/20 fixture has positive physical live-page savings. Normal reference delivery events contain no top-level Message and use no legacy snapshot; the driver asserts both. The durable MCP callback copy is still charged for subscribed recipients.

## Measured logical terms and exception costs

The illustrative R/C/B/I constants are replaced here by measured per-send byte terms and dbstat allocations. `M`, events, idempotency response and audit detail use SQLite `sum(length(CAST(... AS BLOB)))`. Delivery scalar bytes are the explicitly listed text columns in the driver; SQLite numeric/record headers and all indexes are represented by the separate physical dbstat pages, not invented row-overhead constants. Thread/page amortization is workload dependent, so no universal fixed B or C is substituted.

| Profile | M B/send | Event B/send | R p50/p95/p99 B | Idem response B/send | Delivery scalars B/send | Audit detail B/send | Outbox B/send |
| --- | --- | --- | --- | --- | --- | --- | --- |
| F1/2048/repeated-new | 2607.0 | 688.0 | 688/688/688 | 246.0 | 269.0 | 56.0 | 0.0 |
| F1/2048/varied-reused | 3054.2 | 688.9 | 689/689/689 | 246.0 | 269.0 | 57.0 | 0.0 |
| F1/4096/repeated-new | 4655.0 | 688.0 | 688/688/688 | 246.0 | 269.0 | 56.0 | 0.0 |
| F1/4096/varied-reused | 5487.9 | 688.9 | 689/689/689 | 246.0 | 269.0 | 57.0 | 0.0 |
| F2/2048/repeated-new | 2641.0 | 1448.0 | 724/724/724 | 369.0 | 538.0 | 56.0 | 0.0 |
| F2/2048/varied-reused | 3088.2 | 1449.8 | 725/725/725 | 369.0 | 538.0 | 57.0 | 0.0 |
| F2/4096/repeated-new | 4689.0 | 1448.0 | 724/724/724 | 369.0 | 538.0 | 56.0 | 0.0 |
| F2/4096/varied-reused | 5521.9 | 1449.8 | 725/725/725 | 369.0 | 538.0 | 57.0 | 0.0 |
| F5/2048/repeated-new | 2743.0 | 4160.0 | 832/832/832 | 738.0 | 1345.0 | 56.0 | 0.0 |
| F5/2048/varied-reused | 3190.2 | 4164.4 | 833/833/833 | 738.0 | 1345.0 | 57.0 | 0.0 |
| F5/4096/repeated-new | 4791.0 | 4160.0 | 832/832/832 | 738.0 | 1345.0 | 56.0 | 0.0 |
| F5/4096/varied-reused | 5623.9 | 4164.4 | 833/833/833 | 738.0 | 1345.0 | 57.0 | 0.0 |
| F20/2048/repeated-new | 3253.0 | 27440.0 | 1372/1372/1372 | 2583.0 | 5380.0 | 57.0 | 0.0 |
| F20/2048/varied-reused | 3700.2 | 27457.8 | 1373/1373/1373 | 2583.0 | 5380.0 | 58.0 | 0.0 |
| F20/4096/repeated-new | 5301.0 | 27440.0 | 1372/1372/1372 | 2583.0 | 5380.0 | 57.0 | 0.0 |
| F20/4096/varied-reused | 6133.9 | 27457.8 | 1373/1373/1373 | 2583.0 | 5380.0 | 58.0 | 0.0 |
| F2/2048/varied-new/reference | 3194.0 | 1448.0 | 724/724/724 | 369.0 | 538.0 | 56.0 | 0.0 |
| F2/2048/varied-new/hosted | 3194.0 | 2478.0 | 1239/1239/1239 | 369.0 | 538.0 | 56.0 | 0.0 |
| F2/2048/varied-new/1 subs per recipient | 3027.0 | 1448.0 | 724/724/724 | 369.0 | 538.0 | 56.0 | 4595.4 |
| F2/2048/varied-new/3 subs per recipient | 3027.0 | 1448.0 | 724/724/724 | 369.0 | 538.0 | 56.0 | 13786.3 |

Hosted attachments reuse one synthetic blob across the cohort, so BlobBytes is reported separately in the raw results. Recipient-scoped frozen attachment overrides increase R; their actual cost is included above. Reference-only attachments do not acquire a hosted blob URL. R grows with fan-out because every frozen thread summary retains its participant ids; the constant 650-byte reference assumption does not fit every profile. Subscription callbacks and hosted attachment overrides explain their lower percentage reductions. Snapshot counts are zero in normal cohorts.

| Legacy F | Inline live delta B | Reference live delta B | Snapshot bodies B | Snapshots | Reference event B |
| --- | --- | --- | --- | --- | --- |
| 1 | 663552 | 749568 | 198080 | 80 | 35200 |
| 2 | 1015808 | 827392 | 198080 | 80 | 70400 |
| 5 | 2027520 | 1032192 | 198080 | 80 | 176000 |
| 20 | 7159808 | 2068480 | 198080 | 80 | 704000 |

The legacy cohorts retain the canonical Message plus a whitespace-different immutable replay snapshot. One snapshot per Message is deduplicated across recipients, and every reconstruction is byte-checked. Any F=1 loss in that table is the cost of retaining both immutable representations plus the reference metadata; it must not be generalized to normal reference writes.

## Lifecycle, file size and reclaim

| Stage (F5/4096 varied/reused) | Messages/events/idem/outbox rows | Payload M/events B | Pages/free | Live B | DB B after checkpoint | WAL B before checkpoint | Checkpoint log/copied/busy |
| --- | --- | --- | --- | --- | --- | --- | --- |
| empty | 0/0/0/0 | 0/0 | 208/0 | 851968 | 851968 | 1944672 | 472/472/0 |
| immediate | 80/400/80/0 | 449910/333155 | 555/0 | 2273280 | 2273280 | 4264232 | 371/371/0 |
| post_backfill_pre_reclaim | 80/400/80/0 | 449910/333155 | 564/0 | 2310144 | 2310144 | 2826352 | 2/2/0 |
| idempotency_expired | 80/400/0/0 | 449910/333155 | 564/22 | 2220032 | 2310144 | 70072 | 17/17/0 |
| acked_events_expired | 80/0/0/0 | 449910/0 | 564/130 | 1777664 | 2310144 | 1256632 | 305/305/0 |
| finite_history | 0/0/0/0 | 0/0 | 564/337 | 929792 | 2310144 | 1664512 | 404/404/0 |
| post_reclaim | 0/0/0/0 | 0/0 | 227/0 | 929792 | 929792 | 74192 | 6/6/0 |

The raw data contains these same seven stages for every inline/reference pair. Inline cohorts backfill through the production bounded converter before expiry; reference cohorts need no rewrite. Idempotency expires after a synthetic 25-hour advance. All owners acknowledge, then event retention runs after eight days. Finite history is explicitly previewed and confirmed on the disposable fixture; the clock advances past its next nightly sweep. Existing blob or replay dependencies can keep bodies; those retained rows remain in the totals. History and event cleanup use a finite number of bounded passes, not a steady-state completion promise; retained tail rows remain visible in the raw counts. Post-reclaim is incremental vacuum in bounded slices, with actual remaining free pages and allocated file bytes retained in the raw receipt.
Maximum externally timed backfill call in the supplemental F5/F20 cohorts, including probes and transaction completion: 55.683 ms. The original matrix's BackfillMaxNanos field uses the frozen synthetic policy clock and reports zero; it is not a wall-time measurement. That frozen clock also holds the converter's elapsed clock fixed, so the measured calls stop at row/byte caps; they measure batch cost rather than independently validating the 50 ms limiter. The 50 ms target is cooperative, not a hard SQL statement latency guarantee; 100 rows/1 MiB remain the initial operational caps. Maintenance requires more than 64 MiB (or a configured larger reserve) plus current allocated WAL headroom. No headroom figure is claimed to be a worst-case SQLite allocation bound.

## Latency, CPU, allocations and the release tradeoff

| Profile | Send p95 inline/ref ms | Read p95 inline/ref ms | Push p95 inline/ref ms | >10% regressions |
| --- | --- | --- | --- | --- |
| F1/2048/repeated-new | 19.576/14.765 | 0.499/3.029 | 13.082/10.859 | read |
| F1/2048/varied-reused | 13.352/13.399 | 0.484/4.042 | 11.053/11.347 | read |
| F1/4096/repeated-new | 14.269/15.775 | 0.513/5.069 | 11.311/11.670 | send, read |
| F1/4096/varied-reused | 13.263/13.309 | 0.738/5.240 | 11.127/12.587 | read, push |
| F2/2048/repeated-new | 14.243/14.043 | 0.744/6.781 | 11.430/11.219 | read |
| F2/2048/varied-reused | 13.276/13.857 | 0.791/7.269 | 13.248/11.386 | read |
| F2/4096/repeated-new | 15.668/14.385 | 0.902/8.140 | 11.440/12.311 | read |
| F2/4096/varied-reused | 14.453/14.636 | 0.981/10.387 | 11.919/11.675 | read |
| F5/2048/repeated-new | 15.494/14.350 | 0.897/8.118 | 11.258/12.627 | read, push |
| F5/2048/varied-reused | 17.890/21.800 | 1.167/10.396 | 11.751/13.421 | send, read, push |
| F5/4096/repeated-new | 14.793/17.059 | 1.483/11.677 | 12.734/11.600 | send, read |
| F5/4096/varied-reused | 19.699/15.788 | 1.602/12.974 | 12.068/45.043 | read, push |
| F20/2048/repeated-new | 35.025/30.860 | 1.223/9.466 | 21.128/18.562 | read |
| F20/2048/varied-reused | 32.068/29.202 | 1.551/11.305 | 18.035/17.158 | read |
| F20/4096/repeated-new | 61.400/34.020 | 1.783/14.143 | 23.209/25.395 | read |
| F20/4096/varied-reused | 61.362/29.732 | 1.543/14.538 | 22.206/29.131 | read, push |
| F2/2048/varied-new/reference | 21.935/20.096 | 0.773/7.095 | 18.484/16.463 | read |
| F2/2048/varied-new/hosted | 18.648/21.417 | 0.928/8.392 | 17.956/16.116 | send, read |
| F2/2048/varied-new/1 subs per recipient | 20.213/20.869 | 0.929/7.497 | 16.800/17.206 | read |
| F2/2048/varied-new/3 subs per recipient | 25.298/18.645 | 0.882/7.088 | 16.728/18.492 | read, push |

Raw results retain p50/p95/p99, sample counts, CPU seconds and allocated bytes for every operation/profile. Send/read/push wall samples cover the named API calls only. CPU and allocations are cumulative phase measurements: send includes seeded request construction, and push includes verification plus batched synthetic settlement; they are not isolated per-call CPU profiles. The 80-send cohorts and shared build host limit tail precision; the original constrained HTTP benchmark remains a separate workload.

Investigation of >10% regressions: `HydrateBatch` loads immutable bodies, parses references, recomposes frozen attachment patches and checks Message/Delivery SHA-256 before returning bytes. Inline reads return stored bytes without that work. `EncodeDelivery` also derives references from the existing full wire renderer and verifies identity, so this release saves durable copies while retaining send-time JSON CPU. Read/claim batch sizes and row counts are fixed within each pair; no missing-delivery or failed-send sample is counted as a speedup. The CPU/allocation deltas below quantify the cost rather than attributing every wall-time fluctuation to storage.

| Profile | CPU s send/read/push inline → ref | Allocated MiB send/read/push inline → ref |
| --- | --- | --- |
| F1/2048/repeated-new | 0.086 → 0.097 / 0.016 → 0.142 / 0.043 → 0.055 | 10.13 → 15.71 / 22.09 → 129.37 / 0.93 → 3.65 |
| F1/2048/varied-reused | 0.077 → 0.111 / 0.017 → 0.173 / 0.042 → 0.058 | 12.61 → 18.43 / 26.38 → 136.70 / 1.03 → 3.83 |
| F1/4096/repeated-new | 0.093 → 0.097 / 0.021 → 0.247 / 0.033 → 0.056 | 14.92 → 25.55 / 36.15 → 214.15 / 1.28 → 5.78 |
| F1/4096/varied-reused | 0.105 → 0.117 / 0.027 → 0.242 / 0.043 → 0.066 | 19.55 → 30.66 / 40.84 → 229.52 / 1.39 → 6.16 |
| F2/2048/repeated-new | 0.091 → 0.117 / 0.029 → 0.311 / 0.055 → 0.066 | 11.31 → 22.68 / 45.59 → 242.51 / 1.70 → 6.67 |
| F2/2048/varied-reused | 0.098 → 0.122 / 0.032 → 0.322 / 0.057 → 0.077 | 14.10 → 25.82 / 56.43 → 256.35 / 1.97 → 7.01 |
| F2/4096/repeated-new | 0.090 → 0.125 / 0.040 → 0.365 / 0.052 → 0.066 | 16.65 → 37.22 / 72.15 → 398.04 / 2.37 → 10.55 |
| F2/4096/varied-reused | 0.103 → 0.144 / 0.042 → 0.489 / 0.054 → 0.073 | 21.42 → 42.88 / 81.53 → 421.04 / 2.60 → 11.14 |
| F5/2048/repeated-new | 0.105 → 0.155 / 0.038 → 0.357 / 0.063 → 0.097 | 15.58 → 44.80 / 60.06 → 308.55 / 4.29 → 16.75 |
| F5/2048/varied-reused | 0.124 → 0.182 / 0.046 → 0.434 / 0.086 → 0.143 | 18.56 → 48.34 / 69.82 → 319.25 / 4.78 → 17.29 |
| F5/4096/repeated-new | 0.123 → 0.220 / 0.058 → 0.514 / 0.090 → 0.123 | 21.98 → 72.55 / 89.36 → 486.89 / 5.76 → 25.66 |
| F5/4096/varied-reused | 0.153 → 0.235 / 0.059 → 0.612 / 0.096 → 0.168 | 27.52 → 80.23 / 104.74 → 511.58 / 6.54 → 26.92 |
| F20/2048/repeated-new | 0.261 → 0.455 / 0.055 → 0.427 / 0.162 → 0.258 | 42.43 → 165.87 / 81.69 → 358.37 / 21.09 → 76.38 |
| F20/2048/varied-reused | 0.282 → 0.505 / 0.060 → 0.496 / 0.202 → 0.314 | 44.58 → 173.89 / 81.69 → 367.00 / 21.09 → 78.13 |
| F20/4096/repeated-new | 0.316 → 0.569 / 0.072 → 0.603 / 0.151 → 0.273 | 54.27 → 261.29 / 107.08 → 531.74 / 26.17 → 111.08 |
| F20/4096/varied-reused | 0.319 → 0.647 / 0.064 → 0.686 / 0.251 → 0.376 | 65.90 → 278.08 / 132.47 → 556.00 / 31.25 → 115.81 |
| F2/2048/varied-new/reference | 0.113 → 0.123 / 0.031 → 0.316 / 0.067 → 0.082 | 13.98 → 25.85 / 56.53 → 258.84 / 1.98 → 7.07 |
| F2/2048/varied-new/hosted | 0.108 → 0.136 / 0.036 → 0.368 / 0.069 → 0.083 | 15.91 → 29.37 / 56.53 → 312.73 / 1.98 → 8.43 |
| F2/2048/varied-new/1 subs per recipient | 0.093 → 0.119 / 0.034 → 0.343 / 0.061 → 0.091 | 12.98 → 24.64 / 53.60 → 253.58 / 1.90 → 6.94 |
| F2/2048/varied-new/3 subs per recipient | 0.095 → 0.130 / 0.035 → 0.326 / 0.073 → 0.090 | 12.97 → 24.64 / 53.60 → 253.53 / 1.90 → 6.94 |

A separate controlled repeat uses the same F5/F20 4096-byte varied/reused profiles, seed and 80-call sample counts. Original measurements remain above; the repeat is additional investigation evidence.

| Repeat profile | Send p95 inline/ref ms | Read p95 inline/ref ms | Push p95 inline/ref ms |
| --- | --- | --- | --- |
| F5/4096/varied-reused | 15.123/14.939 | 1.462/15.504 | 9.452/9.418 |
| F20/4096/varied-reused | 18.563/20.113 | 1.768/15.337 | 10.423/12.355 |

The read increase persists in the repeat. Wall-time send/push differences vary between runs, so this receipt does not assign all tail variance to hydration. The repeat and the main run preserve the same byte checks and durability settings.

**Recorded implementation tradeoff:** preserve byte/hash validation and bounded memory while accepting the measured hydration/encoding CPU and allocation costs in exchange for the physical storage reductions above. Reference writes and physical reclaim remain independently opt-in. The p95 increases are explicitly disclosed; this receipt does not claim a latency-neutral rollout. Disabling FULL durability or hiding callback/snapshot bytes is not used to meet the storage acceptance criterion. Production enablement and its latency budget remain Task 9/operator work, with no deployment performed here.

## Populated modernc query plans and index accounting

- `claim_due`: `SCAN d USING INDEX deliveries_event_seq_hash`; `CORRELATED SCALAR SUBQUERY 1`; `SEARCH p USING INDEX deliveries_recipient_thr (recipient=? AND thr_id=?)`; `BLOOM FILTER ON r (route_id=?)`; `SEARCH r USING INDEX sqlite_autoindex_routes_1 (route_id=?)`; `SEARCH e USING INTEGER PRIMARY KEY (rowid=?)`
- `history`: `SEARCH messages USING INDEX sqlite_autoindex_messages_2 (thr_id=? AND seq>?)`
- `history_full_forever_policy`: `SEARCH m USING INDEX sqlite_autoindex_messages_2 (thr_id=? AND seq>?)`
- `history_locator`: `COMPOUND QUERY`; `LEFT-MOST SUBQUERY`; `SEARCH messages USING INDEX sqlite_autoindex_messages_1 (msg_id=?)`; `UNION ALL`; `SEARCH message_history_locators USING INDEX sqlite_autoindex_message_history_locators_1 (msg_id=?)`
- `idempotency`: `SEARCH idempotency USING COVERING INDEX idempotency_created_at (created_at<?)`
- `inbox`: `SEARCH deliveries USING INDEX deliveries_recipient_dly (recipient=? AND dly_id<?)`
- `message_gc`: `SEARCH messages USING COVERING INDEX messages_history_age (created_at<?)`
- `opview_inbox_full`: `SEARCH d USING INDEX deliveries_recipient_dly (recipient=? AND dly_id<?)`; `CORRELATED SCALAR SUBQUERY 2`; `SEARCH a USING INDEX approvals_open (closed_at=? AND expires_at>?)`; `SEARCH m USING INDEX sqlite_autoindex_messages_1 (msg_id=?)`; `SCAN t`; `CORRELATED SCALAR SUBQUERY 1`; `SEARCH a USING INDEX approvals_history_message (msg_id=?)`
- `opview_unread_full`: `SEARCH d USING INDEX deliveries_recipient_thr (recipient=?)`; `CORRELATED SCALAR SUBQUERY 1`; `SEARCH a USING INDEX approvals_open (closed_at=? AND expires_at>?)`; `CORRELATED SCALAR SUBQUERY 2`; `SEARCH a USING INDEX approvals_history_message (msg_id=?)`
- `purge`: `SEARCH events USING INTEGER PRIMARY KEY (rowid>?)`
- `purge_pins_full`: `SCAN CONSTANT ROW`; `SCALAR SUBQUERY 6`; `SEARCH d USING INDEX deliveries_event_seq_hash (event_seq=?)`; `SCALAR SUBQUERY 7`; `SEARCH o USING INDEX event_outbox_retention_candidate (seq=?)`; `SCALAR SUBQUERY 9`; `CO-ROUTINE scopes`; `SCAN 3 CONSTANT ROWS`; `SCALAR SUBQUERY 2`; `SEARCH peers USING INDEX sqlite_autoindex_peers_1 (peer_id=?)`; `SCALAR SUBQUERY 4`; `SEARCH o USING INDEX sqlite_autoindex_oauth_principals_3 (peer_id=?)`; `SEARCH p USING INDEX sqlite_autoindex_peers_1 (peer_id=?)`; `SCAN scopes`; `SEARCH s USING INDEX event_subscriptions_retention_identity (peer_id=? AND principal_kind=? AND principal_id=? AND <expr>>?)`; `CORRELATED SCALAR SUBQUERY 8`; `SEARCH o USING COVERING INDEX event_outbox_retention_seq (sub_id=? AND seq=?)`
- `purge_prefix_full`: `SEARCH events USING INDEX events_owner_seq (owner=?)`
- `reference_pin`: `SEARCH events USING COVERING INDEX events_msg_ref (msg_id=?)`
- `unread`: `SEARCH deliveries USING INDEX deliveries_unread (recipient=?)`

Full SQL and bound parameter shapes are in `compactionPlans`/`addCompactionPlans`; the history query uses the actual `1` visibility predicate for the initial forever policy. No index is dropped or replaced. Full operator inbox/unread correlated approval predicates and the complete ClaimDue predecessor condition are measured, together with owner-prefix/pin, history/locator and idempotency probes. Some small/selective distributions choose scans after ANALYZE; this is not evidence that an index is redundant.

| Named index (all KEEP) | dbstat allocated B, F5/4096 reference |
| --- | --- |
| approvals_history_message | 4096 |
| approvals_open | 4096 |
| audit_action_at | 4096 |
| avatar_ops_peer_at | 4096 |
| blob_delete_jobs_next | 4096 |
| blob_object_refs_object | 4096 |
| blob_object_refs_sha256 | 4096 |
| blob_objects_sha256 | 4096 |
| blob_objects_state_expiry | 4096 |
| blob_objects_uploader_state | 4096 |
| blob_r2_quota_day | 4096 |
| blob_refs_msg | 4096 |
| blob_upload_requests_issue_day | 4096 |
| blob_usage_events_day | 4096 |
| blob_usage_events_transition | 4096 |
| blob_usage_events_uploader_day | 4096 |
| credentials_subject | 4096 |
| deliveries_due | 12288 |
| deliveries_event_seq_hash | 36864 |
| deliveries_expiry | 20480 |
| deliveries_msg | 20480 |
| deliveries_recipient_dly | 36864 |
| deliveries_recipient_thr | 45056 |
| deliveries_unread | 20480 |
| durable_blob_refs_payload | 4096 |
| event_outbox_done | 4096 |
| event_outbox_history_delivery | 4096 |
| event_outbox_pending | 4096 |
| event_outbox_replay_unavailable | 4096 |
| event_outbox_retention_candidate | 4096 |
| event_outbox_retention_seq | 4096 |
| event_subscriptions_peer_expiry | 4096 |
| event_subscriptions_retention_identity | 4096 |
| event_subscriptions_retention_live | 4096 |
| events_backfill_inline_delivery_seq | 4096 |
| events_history_delivery | 4096 |
| events_history_inline_message | 4096 |
| events_history_message | 4096 |
| events_msg_ref | 20480 |
| events_owner_seq | 24576 |
| events_snapshot_ref | 4096 |
| group_members_peer | 4096 |
| hub_notices_active | 4096 |
| idempotency_created_at | 4096 |
| jobs_history_thread | 4096 |
| messages_history_age | 12288 |
| oauth_codes_expires | 4096 |
| oauth_tokens_rotated_from | 4096 |
| peers_created_by | 4096 |
| peers_prn | 4096 |
| reaction_ops_peer_at | 4096 |
| route_runs_route_at | 4096 |
| routes_owner | 4096 |
| team_members_peer | 4096 |
| thread_participants_peer | 4096 |

Every autoindex and table, plus overflow-only page totals, is also retained in each raw `Storage.Tables` and `ObjectOverflowBytes` map. Task 8 adds no maintenance index. Existing event-reference, age, callback, history, preferences, notices and R2 indexes retain the default KEEP verdict; no R2 network operation is benchmarked.

## Scope limits and skipped work

- The full Cartesian product of repeated/varied × new/reused × subscription counts × attachments is skipped. The representative matrix pairs repeated/new with varied/reused and adds independent varied/new attachment and one/three-subscription cohorts. Every required fan-out and text size has a comparable normal inline/reference fixture.
- Hub-authored, source and reaction latency matrices are skipped to keep runtime bounded. Their exact delivery bytes and retry/cursor semantics remain covered by the frozen corpus and full/focused suites. Legacy snapshot costs are separately measured with a hub sender. No claim about those actors’ throughput is made.
- No total WAL write-traffic counter, hardware performance-counter CPU profile, remote webhook network latency, production shared-core CPU, long-duration steady-state traffic, R2 transfer or compression experiment is measured. Allocated WAL sizes and checkpoint frame counts are reported; they must not be relabeled as cumulative write amplification.
- Existing NONE reuse/backup and reader-pinned/disk-pressure/pointer-map cases are on-disk behavior tests on x-boss, not a second full NONE benchmark matrix. Offline conversion remains deferred.

## Release gate evidence

An initial broad race run reached the default 10-minute hub-package timeout while progressing through unrelated spill tests; its store run also reproduced the subsequently fixed pointer-map cap bug. Final store races and focused hub/HTTP races supersede those failures. The companion [gate receipt](../../bench/results/2026-11-07-compaction-gates.json) records final suite, race, web-model, protocol and CLI dependency assertions. All test/benchmark execution occurs on build boxes. The final assignment result in `.scratch/vecnet/build-db/task-08.impl.json` records the actual commit list and status.
