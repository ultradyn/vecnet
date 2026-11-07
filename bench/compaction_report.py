"""Measured Task 8 receipt, dispatched by the existing bench/report.py."""
import argparse
import json
from pathlib import Path


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--compaction', type=Path, required=True)
    ap.add_argument('--supplement', type=Path, required=True)
    ap.add_argument('--out', type=Path, required=True)
    args = ap.parse_args()
    data = json.loads(args.compaction.read_text())
    extra = json.loads(args.supplement.read_text())
    assert 'error' not in data and 'error' not in extra
    rows = data['rows']
    assert len(rows) == 40
    pairs = [(rows[i], rows[i+1]) for i in range(0, len(rows), 2)]
    for before, after in pairs:
        assert not before['References'] and after['References']
        assert before['Profile'] == after['Profile']
        for r in (before, after):
            assert r['SendSamples'] == r['Sends'] and r['ReadSamples'] >= 30 and r['PushSamples'] >= r['Sends']
            assert r['RawBytesVerified'] == r['Sends'] * r['Profile']['Fanout']
            immediate = r['Stages'][1]
            assert immediate['Storage']['Rows']['messages'] == r['Sends']
            assert immediate['Stats']['synchronous'] == 2
            if r['References']:
                assert immediate['EventFormats'].get('delivery_ref_v1') == r['Sends'] * r['Profile']['Fanout']
                assert immediate['Storage']['Rows']['event_message_snapshots'] == 0
            assert r['Stages'][3]['Storage']['Rows']['idempotency'] == 0
        assert live(before) > live(after), (before['Profile'], live(before), live(after))
    lines = []
    def text(s=''):
        lines.extend(s.splitlines() if s else [''])
    def table(headers, records):
        text('| ' + ' | '.join(headers) + ' |')
        text('| ' + ' | '.join('---' for _ in headers) + ' |')
        for record in records:
            text('| ' + ' | '.join(map(str, record)) + ' |')
        text()
    def profile(r):
        p = r['Profile']
        return f"F{p['Fanout']}/{p['TextBytes']}/{'varied-reused' if p['Reused'] else 'varied-new' if p['Varied'] else 'repeated-new'}" + (f"/{p['Attachment']}" if p['Attachment'] else '') + (f"/{p['Subscriptions']} subs per recipient" if p['Subscriptions'] else '')
    def delta(r, stage=1):
        return r['Stages'][stage]['Stats']['live_bytes'] - r['Stages'][0]['Stats']['live_bytes']
    text('# DB compaction measurements — 2026-11-07')
    text()
    text('Disposable modernc/SQLite fixtures on **x-boss**, FULL durability, GOMAXPROCS=2, seed 8. '
         'No real hub, Fly volume, deployment, full VACUUM or existing NONE conversion was used. '
         'This is a core hub/storage comparison; it does not establish production capacity or a Fly SLA.')
    text()
    text('## Provenance and reproduction')
    text()
    text(f"Raw matrix: [{args.compaction.name}](../../{args.compaction.as_posix()}); supplemental snapshots/plans: [{args.supplement.name}](../../{args.supplement.as_posix()}).")
    text(f"Go {data['go']}; CPU {data['host']['/proc/cpuinfo']}; started {data['started_utc']}. "
         f"Production base {data['revision']}; schema: `{rows[0]['Schema']}`. Exact source SHA-256 values are in each raw receipt. "
         'The final delivery commit adds this report and gate receipts; it does not change wire rendering. A subsequent reclaim-cap guard also accounts for SQLite locking pages at the 1 GiB boundary; the measured fixtures are below that boundary, and the final gate covers the completed source tree.')
    text()
    text('```sh\nBB_HOST=x-boss BB_JOBS=2 BB_LANE=db-t08-perf just bb GOMAXPROCS=2 VECNET_BENCH_REVISION=8e3b780 CGO_ENABLED=0 just bench -suite compaction -min-sends 80 -out bench/results/2026-11-07-compaction-measured.json\n'
         'BB_HOST=x-boss BB_JOBS=2 BB_LANE=db-t08-perf just bb GOMAXPROCS=2 VECNET_BENCH_REVISION=8e3b780 CGO_ENABLED=0 just bench -suite compaction -compaction-focus supplement -min-sends 80 -out bench/results/2026-11-07-compaction-supplement.json\n'
         'BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 python3 bench/report.py --compaction bench/results/2026-11-07-compaction-measured.json --supplement bench/results/2026-11-07-compaction-supplement.json --out docs/business/db-compaction-bench-2026-11-07.md\n```')
    text()
    text('The existing `just bench` command now accepts `-suite compaction`; source: '
         '[compaction.go](../../bench/compaction.go), [compaction_extra.go](../../bench/compaction_extra.go). '
         'Each paired fixture has identical seeded text, identities, clock, configuration and lifecycle. '
         'It makes 80 serial real `Hub.Send` calls, reads 200-event pages repeatedly through `Log.Read`, '
         'and claims one fan-out group per `ClaimDue` call. Every claimed payload must match its stored SHA-256. '
         'No push route is contacted. Subscription fixtures use private loopback callbacks, enqueue exact bodies on a 503, '
         'then settle them with 204 before retention. Core push timing excludes network time. '
         'Varied text is seeded printable ASCII with quotes, backslashes and newlines; Unicode compatibility is covered by the existing frozen corpus.')
    text()
    text('All fixtures are populated and ANALYZEd before read/push timing and query plans. '
         'Each storage capture records pre-checkpoint DB/WAL state and PASSIVE progress, then uses '
         'the harness’s existing quiescent TRUNCATE probe for per-object dbstat. '
         'TRUNCATE here is benchmark instrumentation; production maintenance only uses PASSIVE. '
         'The raw `Stats.db_bytes` and `Stats.wal_bytes` are the pre-probe files; `Storage.DBBytes/WALBytes` are after that probe. '
         'Neither checkpoint nor deletion is reported as vacuum. Auto-checkpoints may reuse an already allocated WAL; '
         'WAL file length is an allocated high-water measure and is not total WAL bytes written.')
    text()
    text('Controls added by Task 8: `--storage-maintenance` enables slow-tick PASSIVE checkpoints; `--storage-reclaim-pages` independently selects 0..128 physical pages per slice (default 0); `--storage-maintenance-target` defaults to 50 ms. All maintenance is disabled by default. `GET /v1/admin/storage/maintenance` is operator-only, non-cacheable, and exposes the last in-process slice’s counts, page/file sizes, free space, checkpoint progress and pause reason. New empty files receive 4096-byte pages and INCREMENTAL before schema creation; existing modes/page sizes remain intact. On-disk tests cover NONE reuse and quiesced backup/restore, reader-pinned yield/resume, low headroom between steps, pooled settings and pointer-map cap boundaries.')
    text()
    text('## Immediate physical live-page savings')
    text()
    text('Live bytes = `(page_count - freelist_count) * page_size`. The following values subtract '
         'each fixture’s own empty-schema baseline and divide by accepted sends. All table/index pages, '
         'including growth in maintenance indexes, are charged. Empty root pages are fixed schema overhead shown in raw baselines and the index inventory. These are allocated live pages, not logical forecasts.')
    text()
    table(['Profile', 'Inline live B/send', 'Reference live B/send', 'Saved B/send', 'Saved %'], [
        [profile(a), f'{delta(a)/a["Sends"]:.1f}', f'{delta(b)/b["Sends"]:.1f}', f'{(delta(a)-delta(b))/a["Sends"]:.1f}', f'{100*(delta(a)-delta(b))/delta(a):.1f}'] for a,b in pairs])
    text('The F=1 results above include page quantization. Every compared F=2/5/20 fixture has positive physical live-page savings. '
         'Normal reference delivery events contain no top-level Message and use no legacy snapshot; the driver asserts both. '
         'The durable MCP callback copy is still charged for subscribed recipients.')
    text()
    text('## Measured logical terms and exception costs')
    text()
    text('The illustrative R/C/B/I constants are replaced here by measured per-send byte terms and dbstat allocations. '
         '`M`, events, idempotency response and audit detail use SQLite `sum(length(CAST(... AS BLOB)))`. '
         'Delivery scalar bytes are the explicitly listed text columns in the driver; SQLite numeric/record headers and '
         'all indexes are represented by the separate physical dbstat pages, not invented row-overhead constants. '
         'Thread/page amortization is workload dependent, so no universal fixed B or C is substituted.')
    text()
    table(['Profile', 'M B/send', 'Event B/send', 'R p50/p95/p99 B', 'Idem response B/send', 'Delivery scalars B/send', 'Audit detail B/send', 'Outbox B/send'], [
        [profile(b), f'{s["PayloadBytes"]["messages.body"]/b["Sends"]:.1f}', f'{s["PayloadBytes"]["events.data"]/b["Sends"]:.1f}', '/'.join(str(int(s['ReferenceBytes'][f'p{x}_bytes'])) for x in (50,95,99)), f'{s["PayloadBytes"]["idempotency.response"]/b["Sends"]:.1f}', f'{s["PayloadBytes"]["delivery.scalar_columns"]/b["Sends"]:.1f}', f'{s["PayloadBytes"]["audit.details"]/b["Sends"]:.1f}', f'{s["PayloadBytes"]["outbox.body"]/b["Sends"]:.1f}'] for _,b in pairs for s in [b['Stages'][1]]])
    text('Hosted attachments reuse one synthetic blob across the cohort, so BlobBytes is reported separately in the raw results. '
         'Recipient-scoped frozen attachment overrides increase R; their actual cost is included above. '
         'Reference-only attachments do not acquire a hosted blob URL. R grows with fan-out because every frozen thread summary retains its participant ids; the constant 650-byte reference assumption does not fit every profile. Subscription callbacks and hosted attachment overrides explain their lower percentage reductions. Snapshot counts are zero in normal cohorts.')
    text()
    costs = extra['snapshot_costs']
    table(['Legacy F', 'Inline live delta B', 'Reference live delta B', 'Snapshot bodies B', 'Snapshots', 'Reference event B'], [
        [a['Fanout'], a['After']['Stats']['live_bytes']-a['Before']['Stats']['live_bytes'], b['After']['Stats']['live_bytes']-b['Before']['Stats']['live_bytes'], b['After']['PayloadBytes']['snapshots.body'], b['After']['Storage']['Rows']['event_message_snapshots'], b['After']['PayloadBytes']['events.data']] for a,b in zip(costs[::2],costs[1::2])])
    text('The legacy cohorts retain the canonical Message plus a whitespace-different immutable replay snapshot. '
         'One snapshot per Message is deduplicated across recipients, and every reconstruction is byte-checked. '
         'Any F=1 loss in that table is the cost of retaining both immutable representations plus the reference metadata; '
         'it must not be generalized to normal reference writes.')
    text()
    text('## Lifecycle, file size and reclaim')
    text()
    representative = next(b for _,b in pairs if b['Profile']==dict(Fanout=5,TextBytes=4096,Subscriptions=0,Varied=True,Reused=True,Attachment=''))
    table(['Stage (F5/4096 varied/reused)', 'Messages/events/idem/outbox rows', 'Payload M/events B', 'Pages/free', 'Live B', 'DB B after checkpoint', 'WAL B before checkpoint', 'Checkpoint log/copied/busy'], [
        [s['Name'], '/'.join(str(s['Storage']['Rows'][k]) for k in ('messages','events','idempotency','event_outbox')), f'{s["PayloadBytes"]["messages.body"]}/{s["PayloadBytes"]["events.data"]}', f'{s["Stats"]["page_count"]}/{s["Stats"]["freelist_count"]}', s['Stats']['live_bytes'], s['Storage']['DBBytes'], s['Stats']['wal_bytes'], '/'.join(str(s['Checkpoint'][k]) for k in ('log_pages','checkpointed_pages','busy'))] for s in representative['Stages']])
    text('The raw data contains these same seven stages for every inline/reference pair. '
         'Inline cohorts backfill through the production bounded converter before expiry; reference cohorts need no rewrite. '
         'Idempotency expires after a synthetic 25-hour advance. All owners acknowledge, then event retention runs after eight days. '
         'Finite history is explicitly previewed and confirmed on the disposable fixture; the clock advances past its next nightly sweep. '
         'Existing blob or replay dependencies can keep bodies; those retained rows remain in the totals. '
         'History and event cleanup use a finite number of bounded passes, not a steady-state completion promise; retained tail rows remain visible in the raw counts. Post-reclaim is incremental vacuum in bounded slices, with actual remaining free pages and allocated file bytes retained in the raw receipt.')
    text(f"Maximum externally timed backfill call in the supplemental F5/F20 cohorts, including probes and transaction completion: {max(r['BackfillWallMaxNanos'] for r in extra['rows'])/1e6:.3f} ms. The original matrix's BackfillMaxNanos field uses the frozen synthetic policy clock and reports zero; it is not a wall-time measurement. That frozen clock also holds the converter's elapsed clock fixed, so the measured calls stop at row/byte caps; they measure batch cost rather than independently validating the 50 ms limiter. "
         'The 50 ms target is cooperative, not a hard SQL statement latency guarantee; 100 rows/1 MiB remain the initial operational caps. '
         'Maintenance requires more than 64 MiB (or a configured larger reserve) plus current allocated WAL headroom. '
         'No headroom figure is claimed to be a worst-case SQLite allocation bound.')
    text()
    text('## Latency, CPU, allocations and the release tradeoff')
    text()
    table(['Profile', 'Send p95 inline/ref ms', 'Read p95 inline/ref ms', 'Push p95 inline/ref ms', '>10% regressions'], [
        [profile(a), *[f'{a[k]["p95_ms"]:.3f}/{b[k]["p95_ms"]:.3f}' for k in ('Send','Read','Push')], ', '.join(k.lower() for k in ('Send','Read','Push') if b[k]['p95_ms']>1.1*a[k]['p95_ms']) or 'none'] for a,b in pairs])
    text('Raw results retain p50/p95/p99, sample counts, CPU seconds and allocated bytes for every operation/profile. '
         'Send/read/push wall samples cover the named API calls only. CPU and allocations are cumulative phase measurements: '
         'send includes seeded request construction, and push includes verification plus batched synthetic settlement; '
         'they are not isolated per-call CPU profiles. The 80-send cohorts and shared build host limit tail precision; '
         'the original constrained HTTP benchmark remains a separate workload.')
    text()
    text('Investigation of >10% regressions: `HydrateBatch` loads immutable bodies, parses references, recomposes frozen '
         'attachment patches and checks Message/Delivery SHA-256 before returning bytes. Inline reads return stored bytes '
         'without that work. `EncodeDelivery` also derives references from the existing full wire renderer and verifies '
         'identity, so this release saves durable copies while retaining send-time JSON CPU. Read/claim batch sizes and '
         'row counts are fixed within each pair; no missing-delivery or failed-send sample is counted as a speedup. '
         'The CPU/allocation deltas below quantify the cost rather than attributing every wall-time fluctuation to storage.')
    text()
    table(['Profile', 'CPU s send/read/push inline → ref', 'Allocated MiB send/read/push inline → ref'], [
        [profile(a), ' / '.join(f'{a[k]:.3f} → {b[k]:.3f}' for k in ('SendCPU','ReadCPU','PushCPU')), ' / '.join(f'{a[k]/2**20:.2f} → {b[k]/2**20:.2f}' for k in ('SendAllocBytes','ReadAllocBytes','PushAllocBytes'))] for a,b in pairs])
    text('A separate controlled repeat uses the same F5/F20 4096-byte varied/reused profiles, seed and 80-call sample counts. Original measurements remain above; the repeat is additional investigation evidence.')
    text()
    table(['Repeat profile', 'Send p95 inline/ref ms', 'Read p95 inline/ref ms', 'Push p95 inline/ref ms'], [
        [profile(a), *[f'{a[k]["p95_ms"]:.3f}/{b[k]["p95_ms"]:.3f}' for k in ('Send','Read','Push')]] for a,b in zip(extra['rows'][::2],extra['rows'][1::2])])
    text('The read increase persists in the repeat. Wall-time send/push differences vary between runs, so this receipt does not assign all tail variance to hydration. The repeat and the main run preserve the same byte checks and durability settings.')
    text()
    text('**Recorded implementation tradeoff:** preserve byte/hash validation and bounded memory while accepting the '
         'measured hydration/encoding CPU and allocation costs in exchange for the physical storage reductions above. '
         'Reference writes and physical reclaim remain independently opt-in. The p95 increases are explicitly disclosed; '
         'this receipt does not claim a latency-neutral rollout. Disabling FULL durability or hiding callback/snapshot '
         'bytes is not used to meet the storage acceptance criterion. Production enablement and its latency budget remain '
         'Task 9/operator work, with no deployment performed here.')
    text()
    text('## Populated modernc query plans and index accounting')
    text()
    plans = extra['rows'][1]['Plans']
    for name, details in sorted(plans.items()):
        text(f"- `{name}`: " + '; '.join(f'`{d}`' for d in details))
    text()
    text('Full SQL and bound parameter shapes are in `compactionPlans`/`addCompactionPlans`; '
         'the history query uses the actual `1` visibility predicate for the initial forever policy. '
         'No index is dropped or replaced. Full operator inbox/unread correlated approval predicates and the complete '
         'ClaimDue predecessor condition are measured, together with owner-prefix/pin, history/locator and idempotency probes. '
         'Some small/selective distributions choose scans after ANALYZE; this is not evidence that an index is redundant.')
    text()
    objects = representative['Stages'][1]['Storage']['Tables']
    index_names = set()
    for migration in Path('internal/store/migrations').glob('*.sql'):
        import re
        index_names.update(re.findall(r'CREATE\s+(?:UNIQUE\s+)?INDEX\s+(\w+)', migration.read_text(), re.I))
    table(['Named index (all KEEP)', 'dbstat allocated B, F5/4096 reference'], [[name, objects.get(name, 'not present')] for name in sorted(index_names)])
    text('Every autoindex and table, plus overflow-only page totals, is also retained in each raw `Storage.Tables` and '
         '`ObjectOverflowBytes` map. Task 8 adds no maintenance index. Existing event-reference, age, callback, history, '
         'preferences, notices and R2 indexes retain the default KEEP verdict; no R2 network operation is benchmarked.')
    text()
    text('## Scope limits and skipped work')
    text()
    text('- The full Cartesian product of repeated/varied × new/reused × subscription counts × attachments is skipped. '
         'The representative matrix pairs repeated/new with varied/reused and adds independent varied/new attachment '
         'and one/three-subscription cohorts. Every required fan-out and text size has a comparable normal inline/reference fixture.')
    text('- Hub-authored, source and reaction latency matrices are skipped to keep runtime bounded. Their exact delivery '
         'bytes and retry/cursor semantics remain covered by the frozen corpus and full/focused suites. Legacy snapshot '
         'costs are separately measured with a hub sender. No claim about those actors’ throughput is made.')
    text('- No total WAL write-traffic counter, hardware performance-counter CPU profile, remote webhook network latency, '
         'production shared-core CPU, long-duration steady-state traffic, R2 transfer or compression experiment is measured. '
         'Allocated WAL sizes and checkpoint frame counts are reported; they must not be relabeled as cumulative write amplification.')
    text('- Existing NONE reuse/backup and reader-pinned/disk-pressure/pointer-map cases are on-disk behavior tests on '
         'x-boss, not a second full NONE benchmark matrix. Offline conversion remains deferred.')
    text()
    text('## Release gate evidence')
    text()
    text('An initial broad race run reached the default 10-minute hub-package timeout while progressing through unrelated spill tests; its store run also reproduced the subsequently fixed pointer-map cap bug. Final store races and focused hub/HTTP races supersede those failures. The companion [gate receipt](../../bench/results/2026-11-07-compaction-gates.json) records final suite, race, '
         'web-model, protocol and CLI dependency assertions. All test/benchmark execution occurs on build boxes. '
         'The final assignment result in `.scratch/vecnet/build-db/task-08.impl.json` records the actual commit list and status.')
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text('\n'.join(lines)+'\n')
    print(f'wrote {args.out}: {len(rows)} measured profiles; all normal paired live-page deltas positive')


def live(row):
    return row['Stages'][1]['Stats']['live_bytes'] - row['Stages'][0]['Stats']['live_bytes']
