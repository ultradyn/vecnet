# Real hub resource measurements — 2026-11-05

This is an isolated benchmark of the real Go hub, SQLite store and HTTP APIs on **x-boss**.
It measures resource use; it does not establish a Fly SLA, a tenant boundary, a pricing decision,
or production capacity. The dev hub, user configuration, `.data/`, Fly and customer data were untouched.

## Reproduce

From the `bench-hub` worktree, based on `master` **79dfd2e**:

```sh
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 just bench -mode limited -duration 10s -min-sends 500 -workers 4 -idle-window 32s -idle-counts 0,100,1000,5000 -blob-repeats 3 -out bench/results/2026-11-05-limited.json
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 just bench -mode unconstrained -duration 10s -min-sends 500 -workers 4 -idle-window 32s -idle-counts 0,100,1000,5000 -blob-repeats 3 -out bench/results/2026-11-05-unconstrained.json
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 just bench -mode limited -suite idle -idle-window 32s -idle-counts 0,100,1000,5000 -out bench/results/2026-11-05-limited-idle-verified.json
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 just bench -mode unconstrained -suite idle -idle-window 32s -idle-counts 0,100,1000,5000 -out bench/results/2026-11-05-unconstrained-idle-verified.json
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 just test
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 python3 bench/report.py
```

Raw observations: [limited](../../bench/results/2026-11-05-limited.json),
[unconstrained](../../bench/results/2026-11-05-unconstrained.json).
The idle tables use dedicated reruns with per-client admission verification:
[limited idle](../../bench/results/2026-11-05-limited-idle-verified.json) and
[unconstrained idle](../../bench/results/2026-11-05-unconstrained-idle-verified.json).
Earlier idle observations are preserved as raw evidence and superseded by these reruns.
Selected initial-run log evidence is retained in [observations](../../bench/observations-2026-11-05.md).
Hardware/filesystem capture is retained in [environment](../../bench/results/2026-11-05-environment.txt).
The standalone Linux [harness](../../bench/) is not linked into either product binary.
`just test` discovers its small real-store HTTP smoke test with `go test ./...`.

## Machine and isolation
CPU: **AMD RYZEN AI MAX+ 395 w/ Radeon 8060S**, 16 cores / 32 threads. RAM: **61.43 GiB kernel-visible** (64,411,456 KiB MemTotal). Kernel: `Linux version 7.0.0-42-generic (buildd@lcy02-amd64-049) (x86_64-linux-gnu-gcc (Ubuntu 15.2.0-16ubuntu1) 15.2.0, GNU ld (GNU Binutils for Ubuntu) 2.46) #42-Ubuntu SMP PREEMPT_DYNAMIC Thu Oct 15 08:06:44 UTC 2026`.
The lane resides on ext4 (`/dev/mapper/boss--vg-home`). This is a LAN machine with other potential activity, not a dedicated cloud instance. Initial host load and complete memory counters are in the raw JSON. The build-box wrapper runs at nice 10. The loopback load generator uses GOMAXPROCS=2 in a separate process.

- **limited**: Go go1.27.1, hub GOMAXPROCS=1; `cpu.max=100000 100000`, `memory.max=536870912`. Started UTC 2026-11-05T02:10:34.906773358Z.
- **unconstrained**: Go go1.27.1, hub GOMAXPROCS=32; `cpu.max` unavailable at the child scope (empty raw reading), `memory.max=max`. Started UTC 2026-11-05T02:54:58.417648752Z.
For each constrained child the driver invokes:
`systemd-run --user --scope --quiet -p CPUQuota=100% -p MemoryMax=512M -- <bench-binary> -serve -data-dir <fresh-temp-dir>`,
with GOMAXPROCS=1 and the build-box user's runtime bus explicitly selected. It verifies the resulting cgroup files.
CPU quota is one core of aggregate time with a 100 ms period; it does not make this Ryzen core equivalent to a Fly shared core.
Unconstrained children use all 32 logical CPUs in a separate private systemd scope with no benchmark-added quota or memory cap.
That scope keeps the driver's memory out of the child's `memory.current` counter as well as its CPU and RSS.
The parent and children raise their soft file-descriptor limit to 32,768 (within the existing hard limit).
Every child binds a random loopback port and owns a fresh temporary database/blob directory, removed on exit.
The child also shuts itself down if the driver disappears.

## Method

The harness builds the production `store.Open`, `hub.New`, `httpapi.NewStack` and configured HTTP server,
with the real clock and production push, Events and ticker workers running. SQLite uses the default WAL + **synchronous=FULL**,
four pooled connections and a 5 s busy timeout. Only anonymous enrolment throttling is disabled for local setup;
peers are created and enrolled over the real admin/enrol APIs and given high positive send/thread limits through the admin API.
Authorization, validation, transactions, worker cadence and the 15 s heartbeat stay in place.

Each send scenario has one sender and F distinct recipients, four closed-loop clients, at least 10 s and 500 sends,
unique idempotency keys and exact ASCII text byte counts. Every send creates a **new thread**, uses priority `hold`,
and initially queues deliveries to recipients without push routes or active consumers. This is an ingestion workload.
Text is a zero-padded 20-digit sequence plus colon and `x` padding to the requested size, with no extra message metadata;
the sequence makes every spill unique instead of benefiting from content deduplication.
Only fresh HTTP **202** responses count; the committed message-row delta must match. No failed request is silently dropped.
Latency runs from before client JSON encoding through reading/decoding the HTTP response.
Send percentiles describe accepted responses only. HTTP 429 and 5xx attempts are counted separately, with failure latencies;
their elapsed time and CPU remain in the accepted-throughput and CPU/accepted-send measurements. Each attempt uses a new key;
failed attempts are not silently retried under the same idempotency key. Other unexpected statuses or transport failures abort the scenario.
Nearest-rank percentiles describe one run per scenario, not confidence intervals or repeated-trial estimates.

After the timed ingestion phase, the harness reads every recipient log through HTTP, verifies every delivery ID exactly once,
acks the final cursor once per recipient, and posts one `processed` receipt per delivery. Acks generate accepted receipts.
It then enables presence subscriptions on F recipients and posts 100 alternating working/blocked reports.
Send CPU ends before its final checkpoint; **lifecycle CPU** includes ingestion, polling, acks, processed receipts,
checkpoints and instrumentation up to that phase, excluding the subsequent presence experiment.
Those phases are sequential: lifecycle CPU is not a concurrent-consumer throughput measurement.

During the constrained matrix, production background workers logged SQLite `SQLITE_BUSY` lock timeouts
(`events: step`, `push: claim due` and a delivery-expiry tick) while foreground lifecycle writes were running.
The benchmark did not suppress these logs or change the busy timeout. Successful scenario results still require every
accepted entry to have returned 202, exact message-row counts, every expected delivery ID, successful acks and processed receipts.
The unconstrained probe also exposed a foreground HTTP 500 from a SQLite lock timeout; the final runner records such rejected
attempts rather than aborting the matrix. The constrained send matrix predates that counter addition and completed with zero rejected requests.
These observations expose writer contention under this saturation/batch-drain workload; they do not establish a production SLO.

Hub CPU is process user+system CPU from `getrusage`, excluding the driver. RSS comes from `/proc/self/statm`;
cgroup memory includes more than RSS, such as charged file cache. The hub workers and instrumentation contribute to these numbers.
The short samples do not measure large-history/query scaling, TLS, public-network latency, backups, recovery,
push callbacks, OAuth refresh or real-agent execution.

## Send throughput and latency

CPU columns are milliseconds of hub CPU per accepted send. Lifecycle includes F accepted receipts and F processed receipts.
RSS is the hub's observed value immediately after ingestion, not a continuously sampled peak.
### Limited

| Text B | F | 202 / attempts | Sends/s | p50 ms | p95 ms | p99 ms | Send CPU ms | Lifecycle CPU ms | RSS MiB |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 200 | 1 | 530/530 | 52.7 | 63.54 | 113.81 | 282.14 | 1.683 | 2.284 | 30.30 |
| 200 | 2 | 536/536 | 52.6 | 64.31 | 116.51 | 149.30 | 1.942 | 3.083 | 33.00 |
| 200 | 5 | 500/500 | 48.9 | 68.98 | 119.80 | 235.74 | 2.328 | 4.991 | 33.67 |
| 200 | 20 | 500/500 | 36.6 | 96.32 | 167.81 | 281.29 | 5.268 | 15.798 | 34.86 |
| 2048 | 1 | 554/554 | 55.1 | 64.97 | 100.79 | 182.85 | 1.790 | 2.273 | 33.89 |
| 2048 | 2 | 588/588 | 58.5 | 64.75 | 84.57 | 97.05 | 1.973 | 2.971 | 33.26 |
| 2048 | 5 | 532/532 | 52.9 | 66.51 | 96.58 | 246.39 | 2.466 | 5.136 | 35.47 |
| 2048 | 20 | 500/500 | 40.7 | 88.61 | 152.77 | 230.13 | 5.155 | 15.273 | 31.46 |
| 16384 | 1 | 500/500 | 28.6 | 128.81 | 236.49 | 322.19 | 2.828 | 3.320 | 30.24 |
| 16384 | 2 | 500/500 | 25.8 | 130.69 | 279.31 | 819.75 | 3.368 | 4.490 | 30.70 |
| 16384 | 5 | 500/500 | 26.6 | 133.22 | 271.70 | 784.24 | 3.721 | 6.466 | 33.80 |
| 16384 | 20 | 500/500 | 20.6 | 175.18 | 310.73 | 648.45 | 8.937 | 22.955 | 34.93 |

### Unconstrained

| Text B | F | 202 / attempts | Sends/s | p50 ms | p95 ms | p99 ms | Send CPU ms | Lifecycle CPU ms | RSS MiB |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 200 | 1 | 665/665 | 65.2 | 26.07 | 103.03 | 1180.82 | 1.753 | 2.520 | 36.93 |
| 200 | 2 | 661/661 | 65.2 | 26.69 | 88.44 | 1360.69 | 1.935 | 3.656 | 38.51 |
| 200 | 5 | 646/646 | 64.2 | 21.07 | 167.47 | 1098.70 | 2.491 | 6.806 | 37.54 |
| 200 | 20 | 503/503 | 44.7 | 31.12 | 280.70 | 1293.94 | 5.868 | 19.831 | 36.36 |
| 2048 | 1 | 690/690 | 68.5 | 26.21 | 120.04 | 1074.09 | 2.232 | 3.005 | 36.89 |
| 2048 | 2 | 628/628 | 62.1 | 19.15 | 63.43 | 1987.62 | 2.501 | 4.259 | 38.55 |
| 2048 | 5 | 541/541 | 53.6 | 27.51 | 251.74 | 1582.78 | 3.169 | 7.298 | 39.07 |
| 2048 | 20 | 503/503 | 48.0 | 34.23 | 269.61 | 1063.82 | 6.503 | 22.535 | 36.29 |
| 16384 | 1 | 503/503 | 24.5 | 120.73 | 344.51 | 1270.47 | 5.698 | 6.798 | 35.30 |
| 16384 | 2 | 503/503 | 25.6 | 111.61 | 275.71 | 1334.49 | 5.788 | 7.426 | 35.09 |
| 16384 | 5 | 503/503 | 24.9 | 103.40 | 387.27 | 1729.52 | 6.257 | 10.505 | 38.23 |
| 16384 | 20 | 503/503 | 21.3 | 156.13 | 362.77 | 982.00 | 11.074 | 27.703 | 39.44 |

## Durable storage

All values below are **bytes per accepted send** in the constrained run, after explicit `wal_checkpoint(TRUNCATE)`.
DB+WAL and `page_count*page_size` are separately measured allocation deltas; they do not measure total bytes written,
transient WAL high water, filesystem block reservations or backup amplification. Lifecycle storage adds consumption,
accepted receipts and processed receipts, before presence. All 16 KiB inputs are unique and spill at the real 4 KiB threshold:
the database contains preview/attachment metadata while the full text adds 16 KiB of local blob data per send.
| Text B | F | Send DB+WAL B | Send page B | Send blob B | Lifecycle DB+WAL B |
| --- | --- | --- | --- | --- | --- |
| 200 | 1 | 4582.9 | 4582.9 | 0.0 | 5603.0 |
| 200 | 2 | 6999.9 | 6999.9 | 0.0 | 8986.7 |
| 200 | 5 | 18276.4 | 18276.4 | 0.0 | 23224.3 |
| 200 | 20 | 108585.0 | 108585.0 | 0.0 | 128254.0 |
| 2048 | 1 | 10387.9 | 10387.9 | 0.0 | 11400.8 |
| 2048 | 2 | 15548.1 | 15548.1 | 0.0 | 17533.4 |
| 2048 | 5 | 31328.2 | 31328.2 | 0.0 | 36263.5 |
| 2048 | 20 | 120717.3 | 120717.3 | 0.0 | 140386.3 |
| 16384 | 1 | 14082.0 | 14082.0 | 16384.0 | 15097.9 |
| 16384 | 2 | 21282.8 | 21282.8 | 16384.0 | 23289.9 |
| 16384 | 5 | 43147.3 | 43147.3 | 16384.0 | 48095.2 |
| 16384 | 20 | 195248.1 | 195248.1 | 16384.0 | 214917.1 |

Table accounting method: **dbstat allocated B-tree pages (tables and indexes separate)**. Each raw scenario includes every table/index delta and row count. Thread state below groups tables beginning with `thread`; indexes are separate from tables, and other contains remaining schema/metadata objects.

| Text B | F | Messages | Events | Deliveries | Idempotency | Audit | Thread state | Indexes | Other |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 200 | 1 | 1027.9 | 1367.9 | 316.9 | 409.6 | 208.7 | 340.0 | 911.9 | 0.0 |
| 200 | 2 | 1024.0 | 2735.8 | 634.3 | 512.0 | 214.0 | 435.6 | 1444.3 | 0.0 |
| 200 | 5 | 1368.1 | 10272.8 | 1581.1 | 1024.0 | 213.0 | 720.9 | 3096.6 | 0.0 |
| 200 | 20 | 2048.0 | 82100.2 | 6324.2 | 4096.0 | 204.8 | 2154.5 | 11657.2 | 0.0 |
| 2048 | 1 | 4110.8 | 4110.8 | 317.9 | 414.0 | 207.0 | 332.7 | 894.6 | 0.0 |
| 2048 | 2 | 4109.9 | 8219.9 | 633.9 | 515.5 | 209.0 | 438.9 | 1421.1 | 0.0 |
| 2048 | 5 | 4111.4 | 20533.9 | 1578.3 | 1024.0 | 215.6 | 723.7 | 3141.3 | 0.0 |
| 2048 | 20 | 4096.0 | 92184.6 | 6324.2 | 4096.0 | 204.8 | 2154.5 | 11657.2 | 0.0 |
| 16384 | 1 | 5120.0 | 6144.0 | 319.5 | 409.6 | 213.0 | 344.1 | 1220.6 | 311.3 |
| 16384 | 2 | 5120.0 | 12296.2 | 630.8 | 516.1 | 213.0 | 434.2 | 1761.3 | 311.3 |
| 16384 | 5 | 5120.0 | 30752.8 | 1581.1 | 1024.0 | 213.0 | 720.9 | 3424.3 | 311.3 |
| 16384 | 20 | 6144.0 | 164020.2 | 6324.2 | 4096.0 | 204.8 | 2154.5 | 11993.1 | 311.3 |

If table accounting uses estimates, it sums actual SQLite cell byte lengths with the stated row/key overhead, including partial-index predicates. Estimated logical bytes do not sum to measured allocated DB pages; varints, record headers, page fill and allocator reuse differ. There is no claim of exact per-table disk allocation without dbstat.

### Lifecycle increments

Allocation increments depend on existing free space. Ack B/accepted-delivery amortizes one cumulative ack request per recipient over all its N queued deliveries; it is not a one-message ack-request result. Processed receipts are fresh transitions. Presence B/report includes its F subscriber events; B/event divides by the observed event count. All columns include associated table/index/audit/state changes and checkpointed DB+WAL growth.

| Text B | F | Ack B/request (batch) | Ack B/accepted delivery | Processed B/receipt | Presence B/report | Presence B/event | Presence events |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 200 | 1 | 204800.0 | 386.4 | 633.7 | 204.8 | 204.8 | 100 |
| 200 | 2 | 198656.0 | 370.6 | 622.8 | 450.6 | 225.3 | 200 |
| 200 | 5 | 184320.0 | 368.6 | 621.0 | 1187.8 | 237.6 | 500 |
| 200 | 20 | 182681.6 | 365.4 | 618.1 | 4423.7 | 221.2 | 2000 |
| 2048 | 1 | 212992.0 | 384.5 | 628.4 | 286.7 | 286.7 | 100 |
| 2048 | 2 | 219136.0 | 372.7 | 620.0 | 450.6 | 225.3 | 200 |
| 2048 | 5 | 194969.6 | 366.5 | 620.6 | 1105.9 | 221.2 | 500 |
| 2048 | 20 | 182886.4 | 365.8 | 617.7 | 4423.7 | 221.2 | 2000 |
| 16384 | 1 | 196608.0 | 393.2 | 622.6 | 245.8 | 245.8 | 100 |
| 16384 | 2 | 190464.0 | 380.9 | 622.6 | 409.6 | 204.8 | 200 |
| 16384 | 5 | 184320.0 | 368.6 | 621.0 | 1187.8 | 237.6 | 500 |
| 16384 | 20 | 182681.6 | 365.4 | 618.1 | 4423.7 | 221.2 | 2000 |

## Idle streams

These are HTTP/1 connections to **one dedicated, otherwise unused enrolled peer**, with presence subscription `none`.
They measure connection cost; they do not measure 5,000 distinct principals or peers. Each protocol starts a fresh hub,
then reuses it across increasing counts, so heap arenas/cache retained by earlier counts can affect later RSS.
Every SSE client must verify 200 event-stream headers. Every long-poll client must finish its own first validated empty
200 response after the production 25 s hold. This verifies distinct clients: cancelled event-log waiters are removed
asynchronously, so a registered-waiter count alone can briefly include an old poll alongside its renewal.
Server handler counts and registered event-log waiters must then reach N before a 1 s settle.
After settling, client liveness and active handler counts are rechecked. Normal subsequent long-poll renewals or SSE heartbeat
re-registration are included in idle work; the runner does not demand that all waiters remain parked simultaneously throughout the window.
No traffic is sent. Samples last 32 s, with RSS/cgroup memory and active requests sampled about once per second.
SSE streams are continuously drained; long polls renew immediately after validated empty 25 s responses.
Long-poll renewal briefly reduces active handler counts; the table records the observed minimum.
CPU% uses 100% = one full CPU. Baselines include production worker wakeups and about one snapshot request/second.
RSS/connection below is `(RSS at start - same-protocol 0-connection RSS at start)/N`; it is a reused-process slope, not fresh allocations.
### Limited

| Protocol | N | RSS start/end MiB | RSS/conn KiB | Cgroup max MiB | CPU% | Active min | HB / LP responses |
| --- | --- | --- | --- | --- | --- | --- | --- |
| sse | 0 | 25.03 / 25.79 | — | 12.11 | 0.188 | 0 | 0 / 0 |
| sse | 100 | 29.94 / 31.13 | 50.32 | 18.11 | 0.228 | 100 | 200 / 0 |
| sse | 1000 | 80.14 / 85.32 | 56.44 | 78.84 | 0.995 | 1000 | 2008 / 0 |
| sse | 5000 | 310.37 / 366.02 | 58.44 | 375.22 | 3.577 | 5000 | 10654 / 0 |
| longpoll | 0 | 25.23 / 25.61 | — | 12.12 | 0.137 | 0 | 0 / 0 |
| longpoll | 100 | 30.49 / 30.95 | 53.88 | 19.68 | 0.296 | 100 | 0 / 100 |
| longpoll | 1000 | 82.03 / 82.69 | 58.17 | 77.32 | 1.366 | 1000 | 0 / 1000 |
| longpoll | 5000 | unavailable | — | — | — | — | — |

Optional longpoll N=5000 probe: **readiness timeout: active=5000/5000 registered_event_waiters=3704/5000; no partial-concurrency result**. Setup ran 120.0 seconds. This is not a completed idle sample.

### Unconstrained

| Protocol | N | RSS start/end MiB | RSS/conn KiB | Cgroup max MiB | CPU% | Active min | HB / LP responses |
| --- | --- | --- | --- | --- | --- | --- | --- |
| sse | 0 | 26.07 / 27.32 | — | 14.77 | 0.171 | 0 | 0 / 0 |
| sse | 100 | 34.44 / 36.02 | 85.72 | 27.32 | 0.354 | 100 | 200 / 0 |
| sse | 1000 | 82.95 / 93.49 | 58.25 | 84.37 | 1.589 | 1000 | 2004 / 0 |
| sse | 5000 | 342.22 / 362.65 | 64.75 | 376.78 | 8.516 | 5000 | 10610 / 0 |
| longpoll | 0 | 26.37 / 27.08 | — | 14.49 | 0.170 | 0 | 0 / 0 |
| longpoll | 100 | 35.38 / 35.93 | 92.24 | 25.83 | 0.329 | 100 | 0 / 100 |
| longpoll | 1000 | 90.31 / 91.07 | 65.48 | 87.87 | 2.075 | 999 | 0 / 1000 |
| longpoll | 5000 | unavailable | — | — | — | — | — |

Optional longpoll N=5000 probe: **readiness timeout: active=5000/5000 registered_event_waiters=3603/5000; no partial-concurrency result**. Setup ran 120.0 seconds. This is not a completed idle sample.

A connection is counted as established only at verified readiness; every numerical sample must have `valid=true`.
The optional 5,000-connection probe is reported as unavailable when its readiness/settle target cannot be met,
with no CPU/RSS claim for a partial connection count. A setup deadline is not proof of a universal server limit.
Observed maxima are sampled rather than continuous peaks. The 5,000-stream result does not remove Fly's configured
250/300 soft/hard connection proxy limits. Long-poll CPU includes renewing/authenticating requests and their reachability writes;
SSE CPU includes actual production heartbeats and credential checks. Neither is free simply because no message is sent.

## Delivery latency

One persistent recipient SSE stream receives 100 unique 200 B, F=1 messages, paced to 10 sends/s.
Latency is measured before client send encoding to parsing the corresponding recipient frame, including commit and HTTP.
Correlation uses the real message ID. An ack follows the sample sequence. These are loopback times, without WAN or TLS.
| Mode | Samples | Target sends/s | p50 ms | p95 ms | p99 ms |
| --- | --- | --- | --- | --- | --- |
| limited | 100 | 10 | 13.36 | 15.90 | 20.18 |
| unconstrained | 100 | 10 | 13.74 | 19.67 | 20.23 |

## Local blob path

Today's default cap is **25 MiB**. The 100 MiB production-path test must return HTTP **413 blob_too_large**;
it uses known Content-Length and `Expect: 100-continue`, verifies zero stored-blob growth and is not a 100 MiB transfer.
The separately labelled 100 MiB transfer changes only the isolated benchmark's blob cap to 100 MiB, restoring it afterward.
No product limit was changed. Three unique uploads and immediate downloads run per size/mode, with one fresh uploader per repeat.
Payload readers repeat a fresh random 32 KiB block, without allocating a file-sized buffer. Stored byte growth and SHA-256
are checked; downloads verify bytes while reading. Uploads exercise the actual local fsync/rename/SQLite metadata path.
Downloads are warm-cache loopback reads, and client-side hashing contributes to wall time but not hub CPU.
Wall throughput is therefore an observed end-to-end rate, not an isolated disk benchmark or WAN bandwidth promise.
CPU snapshots bracket each request, so tiny transfers include some instrumentation/background cost.
MiB is binary; MB/s and CPU seconds/GB use decimal 1e6 and 1e9 bytes.
Table rates divide total bytes by total elapsed time across three repeats; CPU/GB divides summed CPU by total bytes.
| Mode | MiB | Direction | MB/s | Hub CPU s/GB | Repeats | Cap |
| --- | --- | --- | --- | --- | --- | --- |
| limited | 10 | upload | 248.7 | 1.034 | 3 | default |
| limited | 10 | download | 559.7 | 0.138 | 3 | default |
| limited | 100 | upload | 786.1 | 0.853 | 3 | benchmark override |
| limited | 100 | download | 1470.4 | 0.068 | 3 | benchmark override |
| unconstrained | 10 | upload | 235.3 | 0.936 | 3 | default |
| unconstrained | 10 | download | 564.5 | 0.130 | 3 | default |
| unconstrained | 100 | upload | 677.3 | 0.787 | 3 | benchmark override |
| unconstrained | 100 | download | 1503.7 | 0.067 | 3 | benchmark override |

All production-cap oversize attempts returned 413; raw JSON records timings, CPU, body bytes read by the transport and storage deltas. Direct object URLs could remove this measured relay/hashing/file-serving work from the hub, plus the corresponding local blob occupancy. This experiment did not run R2, signed URLs, object request billing or external egress; it cannot claim a measured R2 speedup or dollar saving. Proxying an R2 download through the hub would retain relay CPU and outbound network cost.

## What this means for the pricing analysis

For the constrained 2 KiB/F=2 scenario, the hub accepted **58.5 sends/s**, with p95 **84.6 ms**, using **1.97 ms CPU/send** and **2.97 ms CPU/send** for the measured lifecycle. Ingestion consumed **11.5% of one CPU**. This run does not demonstrate CPU saturation. It includes FULL durability and observed SQLite lock contention; fsync wait and other sources of elapsed time were not profiled separately.

Extra CPUs did not uniformly improve latency: the unconstrained 2 KiB/F=2 run accepted **62.1 sends/s**, but its p99 was **1987.6 ms**, compared with **97.1 ms** constrained. Both final matrices had zero rejected send requests. These are single trials on a shared LAN host; they do not isolate the cause of the tail-latency difference.

For 2 KiB/F=2, real new-thread ingestion allocated **15.18 KiB/send** and the measured full consumption/receipt lifecycle allocated **17.12 KiB/send**. Compare this with the pricing analysis's synthetic 14,651 B/send and 16 KiB planning allowance: the synthetic figure is not an HTTP lifecycle measurement. Use the measured workload's actual scope when replacing assumptions.

Fan-out remains material: 2 KiB/F=20 allocated **117.89 KiB/send** before consumption/receipts, of which **76.4%** was the events table itself (excluding its indexes). Delivery events include the message and thread summary, so their durable cost exceeds raw text bytes. Each measured send also created a thread; existing-thread traffic was not measured. Moving blobs alone would not remove these message/event/index allocations.

The following is **capacity arithmetic using explicitly assumed profiles**, not a measured multi-hub packing result. Solo uses the worksheet's 10k sends/month, six streams, 2 GB permanent blobs, and measured F=2. Team uses 100k sends/month, 30 streams, 20 GB permanent blobs, and measured **F=5 as a conservative workload substitution for its unmeasured F=3**, not an upper bound on its entire workload. Both use 2 KiB text, fresh threads and one processed receipt per delivery. Traffic is spread evenly over 30 days; queued consumption, backups and growth can produce very different peaks.

| Profile | Sends/mo | Measured F | Lifecycle CPU s/mo | Lifecycle+idle CPU % | Lifecycle+idle CPU-only hubs at 70% | Estimated MiB/hub | RAM-only hubs in 384 MiB | DB GB/mo | Local permanent blobs GB | One-month DB+blobs GB |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Solo | 10000 | 2 | 29.7 | 0.192 | 364 | 25.32 | 15 | 0.175 | 2 | 2.175 |
| Team | 100000 | 5 | 513.6 | 0.220 | 318 | 26.50 | 14 | 3.626 | 20 | 23.626 |

CPU-only hub counts add the observed zero-stream worker baseline and linearly estimated six/30-stream idle CPU from the 100-stream sample. They exclude blob upload/download CPU, presence reports, retries, backups and other worksheet activity; the separate blob measurements above can be used with an explicit transfer profile. RAM takes the larger of two estimates: the RSS baseline plus its positive 0-to-100-stream slope, and the cgroup memory baseline plus its corresponding positive slope, with 384 MiB available after an illustrative 128 MiB machine reserve. Shared file pages can appear in RSS while being charged to another cgroup, and summing RSS can count shared pages repeatedly; using the larger estimate avoids treating the smaller cgroup counter alone as the whole footprint. Neither independent-process idle cost nor this interpolation/extrapolation was measured: the 100 streams share one peer. Do not use the large CPU-only number as a deployment limit; RAM, query/history growth, connection caps, disk, latency during bursts and noisy-neighbor scheduling bind separately. Memory usage from a small fresh database is not a mature-hub bound.

A 1 GB volume cannot hold either assumed profile's permanent local blobs, even before message history. With blobs remote, the DB GB/mo column estimates additional primary allocation each month, before WAL/staging headroom, free-space reserve and backups. History is cumulative: this run did not add retention. For any chosen volume budget V, disk-only packed hubs are at most floor(V / (history_months × DB_GB_per_month + local_blob_GB + headroom)), with V and terms in the same units. Remote blobs change that disk term, not message/event/receipt history.

The measured CPU service times support a low raw compute cost at the worksheet's modest average message rates. They do not establish a support labor budget or eliminate minimum always-on machine cost. Provider tariffs, regional quotes, provisioned versus occupied disk, backup policy, public transfer/egress, payment fees and support time remain separately priced assumptions in [pricing-analysis.md](pricing-analysis.md). The real storage duplication, spill files, idle connections and blob relay work are measurable costs; the earlier dollar allowances are not dollars spent per message. Before changing quotas or committing a shared-cpu packing count, replay representative peak traffic and retention-sized databases, measure separate hub processes together, and include the actual TLS/proxy/backup path.

As separate arithmetic, applying the constrained 10 MiB production-path CPU/GB observations to the worksheet's 5 GB upload + 10 GB download Solo profile gives **6.6 CPU seconds/month**; the 50/100 GB Team profile gives **65.5 CPU seconds/month**. These assume comparable object sizes and warm-cache download behavior and exclude public TLS, retries and the future object authorization path. They are outside the lifecycle+idle CPU packing column above. Direct object URLs would mainly change local occupancy and relay/egress routing at those assumed volumes; this experiment supplies no measured R2 bill or product-price justification.
