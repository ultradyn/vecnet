#!/usr/bin/env python3
"""Render the dated report from committed raw measurements (stdlib only)."""
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DATA = [json.loads((ROOT / f"bench/results/2026-11-05-{mode}.json").read_text())
        for mode in ("limited", "unconstrained")]
# Preserve the original full run. Use the independently rerun constrained
# idle suite with registered-waiter admission, rather than its earlier sample.
for d in DATA:
    refresh = json.loads((ROOT / f"bench/results/2026-11-05-{d['mode']}-idle-verified.json").read_text())
    assert "error" not in refresh and refresh["mode"] == d["mode"]
    d["idle"] = refresh["idle"]
for d in DATA:
    assert "error" not in d, (d["mode"], d.get("error"))
    assert len(d["sends"]) == 12
    assert {(s["text_bytes"], s["fanout"]) for s in d["sends"]} == {
        (size, f) for size in (200, 2048, 16384) for f in (1, 2, 5, 20)}
    for s in d["sends"]:
        assert s["accepted"] >= 500 and s["elapsed_seconds"] >= 10
        assert s.get("attempted",s["accepted"]) == s["accepted"]+s.get("failed_requests",0)
        assert sum(s.get("failed_statuses",{}).values()) == s.get("failed_requests",0)
        assert s["storage"]["Rows"]["messages"] == s["accepted"]
        assert s["storage"]["Rows"]["deliveries"] == s["accepted"]*s["fanout"]
        assert s["spilled"] == (s["accepted"] if s["text_bytes"] == 16384 else 0)
        assert s["storage"]["BlobBytes"] == (s["accepted"]*16384 if s["text_bytes"] == 16384 else 0)
    assert len(d["idle"]["samples"]) == 8
    idle_ids=[(s["mode"],s["connections"]) for s in d["idle"]["samples"]]
    assert len(set(idle_ids))==8 and set(idle_ids)=={(p,n) for p in ("sse","longpoll") for n in (0,100,1000,5000)}
    for s in d["idle"]["samples"]:
        if not s["valid"]:
            assert s["connections"] >= 5000 and s.get("unavailable_reason")
            continue
        assert s["active_at_start"] == s["connections"]
        assert s["window_seconds"] >= 32
        if s["connections"]:
            if s["mode"] == "longpoll":
                assert s["longpoll_clients_verified"] == s["connections"]
            else:
                assert s["sse_headers_verified"] == s["connections"]
            counter = "sse_heartbeats_during_window" if s["mode"] == "sse" else "longpoll_responses_during_window"
            assert s[counter] >= s["connections"]
    assert d["delivery"]["samples"] == 100
    assert len(d["blobs"]["samples"]) == 9
    blob_ids=[(s["scenario"],s["repeat"],s.get("size_bytes",s.get("declared_body_bytes"))) for s in d["blobs"]["samples"]]
    expected_blobs={(scenario,r,size) for r in (1,2,3) for scenario,size in (
        ("production_25MiB_cap",10*2**20),
        ("production_25MiB_cap_100MiB_http_rejection",100*2**20),
        ("benchmark_only_extended_100MiB_cap",100*2**20))}
    assert len(set(blob_ids))==9 and set(blob_ids)==expected_blobs
    for s in d["blobs"]["samples"]:
        if "http_status" in s:
            assert s["http_status"] == 413 and s["storage_delta"]["BlobBytes"] == 0
        else:
            assert s["sha256_verified"] and s["storage_delta"]["BlobBytes"] == s["size_bytes"]
lines = []


def text(s=""):
    lines.extend(s.strip().splitlines() if s else [""])


def table(headers, rows):
    text("| " + " | ".join(headers) + " |")
    text("| " + " | ".join("---" for _ in headers) + " |")
    for row in rows:
        text("| " + " | ".join(map(str, row)) + " |")
    text()


def disk(s):
    return s["DBBytes"] + s["WALBytes"]


def mib(n):
    return f"{n / 2**20:.2f}"


text("""# Real hub resource measurements — 2026-11-05

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
""")
host = DATA[0]["host"]
mem_kib=int(host["/proc/meminfo"].splitlines()[0].split()[1])
text(f"CPU: **{host['/proc/cpuinfo']}**, 16 cores / 32 threads. RAM: "
     f"**{mem_kib/2**20:.2f} GiB kernel-visible** ({mem_kib:,} KiB MemTotal). Kernel: `{host['/proc/version'].strip()}`.")
text("The lane resides on ext4 (`/dev/mapper/boss--vg-home`). This is a LAN machine with other potential activity, "
     "not a dedicated cloud instance. Initial host load and complete memory counters are in the raw JSON. "
     "The build-box wrapper runs at nice 10. The loopback load generator uses GOMAXPROCS=2 in a separate process.")
text()
for d in DATA:
    s = d["sends"][0]["limits"]
    cpu_limit = f"`cpu.max={s['CPUQuota']}`" if s["CPUQuota"] else "`cpu.max` unavailable at the child scope (empty raw reading)"
    text(f"- **{d['mode']}**: Go {d['go']}, hub GOMAXPROCS={s['GOMAXPROCS']}; "
         f"{cpu_limit}, `memory.max={s['MemoryMax']}`. "
         f"Started UTC {d['started_utc']}.")
text("""
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
""")
for d in DATA:
    text(f"### {d['mode'].capitalize()}")
    text()
    table(["Text B", "F", "202 / attempts", "Sends/s", "p50 ms", "p95 ms", "p99 ms", "Send CPU ms", "Lifecycle CPU ms", "RSS MiB"],
          [[s["text_bytes"], s["fanout"], f"{s['accepted']}/{s.get('attempted',s['accepted'])}", f"{s['accepted_per_second']:.1f}",
            *(f"{s['latency'][f'p{p}_ms']:.2f}" for p in (50, 95, 99)),
            f"{s['hub_cpu_ms_per_send']:.3f}", f"{s['lifecycle_hub_cpu_ms_per_send']:.3f}", mib(s["hub_rss_bytes"])]
           for s in d["sends"]])
    failures=[s for s in d["sends"] if s.get("failed_requests",0)]
    if failures:
        text("Rejected send attempts (excluded from accepted-latency percentiles):")
        text()
        table(["Text B","F","Failures","HTTP statuses","Failure p50 ms","Failure p95 ms","Failure p99 ms"],
              [[s["text_bytes"],s["fanout"],s["failed_requests"],str(s["failed_statuses"]),
                *(f"{s['failed_latency'][f'p{p}_ms']:.2f}" for p in (50,95,99))] for s in failures])
limited = DATA[0]
text("""## Durable storage

All values below are **bytes per accepted send** in the constrained run, after explicit `wal_checkpoint(TRUNCATE)`.
DB+WAL and `page_count*page_size` are separately measured allocation deltas; they do not measure total bytes written,
transient WAL high water, filesystem block reservations or backup amplification. Lifecycle storage adds consumption,
accepted receipts and processed receipts, before presence. All 16 KiB inputs are unique and spill at the real 4 KiB threshold:
the database contains preview/attachment metadata while the full text adds 16 KiB of local blob data per send.
""")
table(["Text B", "F", "Send DB+WAL B", "Send page B", "Send blob B", "Lifecycle DB+WAL B"],
      [[s["text_bytes"], s["fanout"], f"{disk(s['storage'])/s['accepted']:.1f}",
        f"{s['storage']['PageBytes']/s['accepted']:.1f}", f"{s['storage']['BlobBytes']/s['accepted']:.1f}",
        f"{disk(s['lifecycle_storage'])/s['accepted']:.1f}"] for s in limited["sends"]])
method = limited["sends"][0]["storage"]["Method"]
text(f"Table accounting method: **{method}**. "
     "Each raw scenario includes every table/index delta and row count. Thread state below groups tables beginning with `thread`; "
     "indexes are separate from tables, and other contains remaining schema/metadata objects.")
text()


def split_storage(s):
    st = s["storage"]
    groups = dict.fromkeys(["messages", "events", "deliveries", "idempotency", "audit", "thread", "indexes", "other"], 0)
    for name, size in st["Tables"].items():
        key = name if name in groups else "thread" if name.startswith("thread") else "other"
        if name not in st["Rows"] and name not in ("sqlite_schema", "sqlite_master"):
            key = "indexes"
        groups[key] += size
    return [f"{v/s['accepted']:.1f}" for v in groups.values()]


table(["Text B", "F", "Messages", "Events", "Deliveries", "Idempotency", "Audit", "Thread state", "Indexes", "Other"],
      [[s["text_bytes"], s["fanout"], *split_storage(s)] for s in limited["sends"]])
text("If table accounting uses estimates, it sums actual SQLite cell byte lengths with the stated row/key overhead, "
     "including partial-index predicates. Estimated logical bytes do not sum to measured allocated DB pages; "
     "varints, record headers, page fill and allocator reuse differ. There is no claim of exact per-table disk allocation without dbstat.")
text()
text("### Lifecycle increments")
text()
text("Allocation increments depend on existing free space. Ack B/accepted-delivery amortizes one cumulative ack request per recipient "
     "over all its N queued deliveries; it is not a one-message ack-request result. Processed receipts are fresh transitions. "
     "Presence B/report includes its F subscriber events; B/event divides by the observed event count. "
     "All columns include associated table/index/audit/state changes and checkpointed DB+WAL growth.")
text()
table(["Text B", "F", "Ack B/request (batch)", "Ack B/accepted delivery", "Processed B/receipt", "Presence B/report", "Presence B/event", "Presence events"],
      [[s["text_bytes"], s["fanout"], f"{disk(s['ack']['storage'])/s['ack']['requests']:.1f}",f"{disk(s['ack']['storage'])/s['ack']['accepted_deliveries']:.1f}",
        f"{disk(s['receipt']['storage'])/s['receipt']['requests']:.1f}",
        f"{disk(s['presence']['storage'])/s['presence']['requests']:.1f}",
        f"{disk(s['presence']['storage'])/max(1,s['presence']['events']):.1f}", s["presence"]["events"]] for s in limited["sends"]])
text("""## Idle streams

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
""")
for d in DATA:
    text(f"### {d['mode'].capitalize()}")
    text()
    rows = d["idle"]["samples"]
    baseline = {s["mode"]: s["rss_at_start_bytes"] for s in rows if s["connections"] == 0}
    table(["Protocol", "N", "RSS start/end MiB", "RSS/conn KiB", "Cgroup max MiB", "CPU%", "Active min", "HB / LP responses"],
          [[s["mode"], s["connections"], f"{mib(s['rss_at_start_bytes'])} / {mib(s['rss_at_end_bytes'])}",
            f"{(s['rss_at_start_bytes']-baseline[s['mode']])/max(1,s['connections'])/1024:.2f}" if s["connections"] else "—",
            mib(s["cgroup_memory_observed_max_bytes"]), f"{100*s['hub_cpu_seconds_per_wall_second']:.3f}",
            s["active_observed_min"], f"{s['sse_heartbeats_during_window']} / {s['longpoll_responses_during_window']}"] if s["valid"]
           else [s["mode"],s["connections"],"unavailable","—","—","—","—","—"] for s in rows])
    for s in rows:
        if not s["valid"]:
            text(f"Optional {s['mode']} N={s['connections']} probe: **{s['unavailable_reason']}**. "
                 f"Setup ran {s['setup_seconds']:.1f} seconds. This is not a completed idle sample.")
            text()
text("""A connection is counted as established only at verified readiness; every numerical sample must have `valid=true`.
The optional 5,000-connection probe is reported as unavailable when its readiness/settle target cannot be met,
with no CPU/RSS claim for a partial connection count. A setup deadline is not proof of a universal server limit.
Observed maxima are sampled rather than continuous peaks. The 5,000-stream result does not remove Fly's configured
250/300 soft/hard connection proxy limits. Long-poll CPU includes renewing/authenticating requests and their reachability writes;
SSE CPU includes actual production heartbeats and credential checks. Neither is free simply because no message is sent.

## Delivery latency

One persistent recipient SSE stream receives 100 unique 200 B, F=1 messages, paced to 10 sends/s.
Latency is measured before client send encoding to parsing the corresponding recipient frame, including commit and HTTP.
Correlation uses the real message ID. An ack follows the sample sequence. These are loopback times, without WAN or TLS.
""")
table(["Mode", "Samples", "Target sends/s", "p50 ms", "p95 ms", "p99 ms"],
      [[d["mode"], d["delivery"]["samples"], d["delivery"]["target_sends_per_second"],
        *(f"{d['delivery']['latency'][f'p{p}_ms']:.2f}" for p in (50, 95, 99))] for d in DATA])
text("""## Local blob path

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
""")
blob_rows = []
for d in DATA:
    samples = d["blobs"]["samples"]
    for scenario in ("production_25MiB_cap", "benchmark_only_extended_100MiB_cap"):
        selected = [s for s in samples if s["scenario"] == scenario]
        for direction in ("upload", "download"):
            size = sum(s[direction]["bytes"] for s in selected)
            secs = sum(s[direction]["seconds"] for s in selected)
            cpu = sum(s[direction]["hub_cpu_seconds"] for s in selected)
            blob_rows.append([d["mode"], selected[0]["size_bytes"] // 2**20, direction,
                              f"{size/1e6/secs:.1f}", f"{cpu/(size/1e9):.3f}", len(selected),
                              "default" if scenario.startswith("production") else "benchmark override"])
table(["Mode", "MiB", "Direction", "MB/s", "Hub CPU s/GB", "Repeats", "Cap"], blob_rows)
text("All production-cap oversize attempts returned 413; raw JSON records timings, CPU, body bytes read by the transport "
     "and storage deltas. Direct object URLs could remove this measured relay/hashing/file-serving work from the hub, "
     "plus the corresponding local blob occupancy. This experiment did not run R2, signed URLs, object request billing "
     "or external egress; it cannot claim a measured R2 speedup or dollar saving. Proxying an R2 download through the hub "
     "would retain relay CPU and outbound network cost.")
text()
text("## What this means for the pricing analysis")
text()
solo = next(s for s in limited["sends"] if s["text_bytes"] == 2048 and s["fanout"] == 2)
team = next(s for s in limited["sends"] if s["text_bytes"] == 2048 and s["fanout"] == 5)
text(f"For the constrained 2 KiB/F=2 scenario, the hub accepted **{solo['accepted_per_second']:.1f} sends/s**, "
     f"with p95 **{solo['latency']['p95_ms']:.1f} ms**, using **{solo['hub_cpu_ms_per_send']:.2f} ms CPU/send** "
     f"and **{solo['lifecycle_hub_cpu_ms_per_send']:.2f} ms CPU/send** for the measured lifecycle. "
     f"Ingestion consumed **{100*solo['hub_cpu_seconds']/solo['elapsed_seconds']:.1f}% of one CPU**. "
     "This run does not demonstrate CPU saturation. It includes FULL durability and observed SQLite lock contention; "
     "fsync wait and other sources of elapsed time were not profiled separately.")
text()
unlimited_solo = next(s for s in DATA[1]["sends"] if s["text_bytes"] == 2048 and s["fanout"] == 2)
text(f"Extra CPUs did not uniformly improve latency: the unconstrained 2 KiB/F=2 run accepted "
     f"**{unlimited_solo['accepted_per_second']:.1f} sends/s**, but its p99 was "
     f"**{unlimited_solo['latency']['p99_ms']:.1f} ms**, compared with "
     f"**{solo['latency']['p99_ms']:.1f} ms** constrained. Both final matrices had zero rejected send requests. "
     "These are single trials on a shared LAN host; they do not isolate the cause of the tail-latency difference.")
text()
text(f"For 2 KiB/F=2, real new-thread ingestion allocated **{disk(solo['storage'])/solo['accepted']/1024:.2f} KiB/send** "
     f"and the measured full consumption/receipt lifecycle allocated **{disk(solo['lifecycle_storage'])/solo['accepted']/1024:.2f} KiB/send**. "
     "Compare this with the pricing analysis's synthetic 14,651 B/send and 16 KiB planning allowance: "
     "the synthetic figure is not an HTTP lifecycle measurement. Use the measured workload's actual scope when replacing assumptions.")
text()
wide = next(s for s in limited["sends"] if s["text_bytes"] == 2048 and s["fanout"] == 20)
text(f"Fan-out remains material: 2 KiB/F=20 allocated **{disk(wide['storage'])/wide['accepted']/1024:.2f} KiB/send** "
     f"before consumption/receipts, of which **{100*wide['storage']['Tables']['events']/disk(wide['storage']):.1f}%** "
     "was the events table itself (excluding its indexes). Delivery events include the message and thread summary, "
     "so their durable cost exceeds raw text bytes. Each measured send also created a thread; existing-thread traffic was not measured. "
     "Moving blobs alone would not remove these message/event/index allocations.")
text()
seconds_month = 30*86400
idle = limited["idle"]["samples"]
zero = next(s for s in idle if s["mode"] == "sse" and s["connections"] == 0)
hundred = next(s for s in idle if s["mode"] == "sse" and s["connections"] == 100)
per_stream = max(0, hundred["cgroup_memory_at_start_bytes"]-zero["cgroup_memory_at_start_bytes"])/100
rss_per_stream=max(0,hundred["rss_at_start_bytes"]-zero["rss_at_start_bytes"])/100
estimates = []
for label, s, messages, streams, blob_gb in (("Solo", solo, 10000, 6, 2), ("Team", team, 100000, 30, 20)):
    cpu = s["lifecycle_hub_cpu_ms_per_send"]/1000*messages
    idle_core = zero["hub_cpu_seconds_per_wall_second"] + max(0, hundred["hub_cpu_seconds_per_wall_second"]-zero["hub_cpu_seconds_per_wall_second"])*streams/100
    fraction = cpu/seconds_month+idle_core
    memory = max(zero["cgroup_memory_at_start_bytes"]+streams*per_stream,
                 zero["rss_at_start_bytes"]+streams*rss_per_stream)
    db_gb = disk(s["lifecycle_storage"])/s["accepted"]*messages/1e9
    estimates.append([label, messages, s["fanout"], f"{cpu:.1f}", f"{fraction*100:.3f}",
                      int(.7/fraction), mib(memory), int(384*2**20/memory), f"{db_gb:.3f}", blob_gb,
                      f"{db_gb+blob_gb:.3f}"])
text("The following is **capacity arithmetic using explicitly assumed profiles**, not a measured multi-hub packing result. "
     "Solo uses the worksheet's 10k sends/month, six streams, 2 GB permanent blobs, and measured F=2. "
     "Team uses 100k sends/month, 30 streams, 20 GB permanent blobs, and measured **F=5 as a conservative workload substitution for its unmeasured F=3**, not an upper bound on its entire workload. "
     "Both use 2 KiB text, fresh threads and one processed receipt per delivery. "
     "Traffic is spread evenly over 30 days; queued consumption, backups and growth can produce very different peaks.")
text()
table(["Profile", "Sends/mo", "Measured F", "Lifecycle CPU s/mo", "Lifecycle+idle CPU %", "Lifecycle+idle CPU-only hubs at 70%", "Estimated MiB/hub", "RAM-only hubs in 384 MiB", "DB GB/mo", "Local permanent blobs GB", "One-month DB+blobs GB"], estimates)
text("CPU-only hub counts add the observed zero-stream worker baseline and linearly estimated six/30-stream idle CPU "
     "from the 100-stream sample. They exclude blob upload/download CPU, presence reports, retries, backups and other worksheet activity; "
     "the separate blob measurements above can be used with an explicit transfer profile. RAM takes the larger of two estimates: "
     "the RSS baseline plus its positive 0-to-100-stream slope, and the cgroup memory baseline plus its corresponding positive slope, "
     "with 384 MiB available after an illustrative 128 MiB machine reserve. "
     "Shared file pages can appear in RSS while being charged to another cgroup, and summing RSS can count shared pages repeatedly; "
     "using the larger estimate avoids treating the smaller cgroup counter alone as the whole footprint. "
     "Neither independent-process idle cost nor this interpolation/extrapolation was measured: the 100 streams share one peer. "
     "Do not use the large CPU-only number as a deployment limit; RAM, query/history growth, connection caps, disk, "
     "latency during bursts and noisy-neighbor scheduling bind separately. Memory usage from a small fresh database is not a mature-hub bound.")
text()
text("A 1 GB volume cannot hold either assumed profile's permanent local blobs, even before message history. "
     "With blobs remote, the DB GB/mo column estimates additional primary allocation each month, before WAL/staging "
     "headroom, free-space reserve and backups. History is cumulative: this run did not add retention. "
     "For any chosen volume budget V, disk-only packed hubs are at most floor(V / (history_months × DB_GB_per_month + local_blob_GB + headroom)), "
     "with V and terms in the same units. Remote blobs change that disk term, not message/event/receipt history.")
text()
text("The measured CPU service times support a low raw compute cost at the worksheet's modest average message rates. "
     "They do not establish a support labor budget or eliminate minimum always-on machine cost. "
     "Provider tariffs, regional quotes, provisioned versus occupied disk, backup policy, public transfer/egress, "
     "payment fees and support time remain separately priced assumptions in [pricing-analysis.md](pricing-analysis.md). "
     "The real storage duplication, spill files, idle connections and blob relay work are measurable costs; "
     "the earlier dollar allowances are not dollars spent per message. "
     "Before changing quotas or committing a shared-cpu packing count, replay representative peak traffic and retention-sized databases, "
     "measure separate hub processes together, and include the actual TLS/proxy/backup path.")
text()
prod_blobs=[s for s in limited["blobs"]["samples"] if s["scenario"]=="production_25MiB_cap"]
cpu_per_gb={direction:sum(s[direction]["hub_cpu_seconds"] for s in prod_blobs)/(sum(s[direction]["bytes"] for s in prod_blobs)/1e9)
            for direction in ("upload","download")}
solo_blob_cpu=5*cpu_per_gb["upload"]+10*cpu_per_gb["download"]
text(f"As separate arithmetic, applying the constrained 10 MiB production-path CPU/GB observations to the worksheet's "
     f"5 GB upload + 10 GB download Solo profile gives **{solo_blob_cpu:.1f} CPU seconds/month**; "
     f"the 50/100 GB Team profile gives **{10*solo_blob_cpu:.1f} CPU seconds/month**. "
     "These assume comparable object sizes and warm-cache download behavior and exclude public TLS, retries and the future object authorization path. "
     "They are outside the lifecycle+idle CPU packing column above. Direct object URLs would mainly change local occupancy and relay/egress routing "
     "at those assumed volumes; this experiment supplies no measured R2 bill or product-price justification.")
path = ROOT / "docs/business/benchmarks-2026-11-05.md"
path.write_text("\n".join(lines) + "\n")
print(f"wrote {path.relative_to(ROOT)}")
