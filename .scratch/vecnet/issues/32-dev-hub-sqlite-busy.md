# 32 (resolved): Dev hub logs SQLITE_BUSY from background workers

Found 2026-11-04 while restarting the dev hub (`.scratch/vecnet/build/devhub.log`). 295 `database is locked (5) (SQLITE_BUSY)` errors between 2026-11-01 and 2026-11-04: `events: step` 126, `push: claim due` 114, `hub: tick expire deliveries` 27, `hub: tick expire approvals` 22. All from background loops; no HTTP request has failed with SQLITE_BUSY in the log (the `internal error` lines are context-canceled client disconnects).

The store already uses `_txlock=immediate` and `busy_timeout(5000)` (internal/store/store.go:18-25), so each error means a writer held the lock for over 5 s, or another process had the file open.

## Investigate
- Which transaction holds the write lock that long (log tx duration over 1 s with its call site), and whether any work inside a Tx blocks on I/O (webhook/push POST, blob write, long-poll).
- Whether another process opens `.data/hub.db` (e.g. `vecnet-hub` admin subcommands or scripts reading it while the hub runs).
- Workers should back off and retry quietly on SQLITE_BUSY rather than log at ERROR each tick.

## Order
Low priority; investigate before a production deploy.

## Evidence 2026-11-04 13:00-13:04 AEDT
- codex-llmp (dogfooding) saw a 7.8 s `vecnet thread` read and a receipt+ack sequence over 10 s, with no error responses; a later inbox took 61 ms. Hub log shows SQLITE_BUSY bursts at 13:00:24, 13:01:29, 13:02:17-29 (423 total in the log now).
- Host at that moment: load average 92/70/57, `/proc/pressure/io` some avg10=41 full avg10=16, cpu some avg10=33. Top CPU users were unrelated (tesseract, cc1plus/compile, Chrome, Paseo) plus two vecnet build workflows in agent-build.slice.
- Likely mechanism: fsync stalls under I/O pressure keep a write transaction open past busy_timeout(5000), so background loops and requests queue behind it. Check: log tx duration and fsync time; consider WAL with synchronous=NORMAL (if not already), and a quieter retry for background loops.
- Store runs WAL with synchronous(FULL) (store.go:23-24), so every commit fsyncs. synchronous(NORMAL) in WAL survives process crashes and only risks the last transactions on power loss; that is a durability decision for Max, not a silent fix.

## Decision 2026-11-04 (Max: rec)
Dev hub switches to `synchronous=NORMAL` via the new `VECNET_SQLITE_SYNCHRONOUS=normal` (default stays `full`; production decided separately). Re-check the SQLITE_BUSY rate after the switch before closing.
- 2026-11-04 13:29:20 AEDT: dev hub restarted with VECNET_SQLITE_SYNCHRONOUS=normal; SQLITE_BUSY count in devhub.log at restart: 441.
- 2026-11-04 15:20 AEDT: still 441 after ~110 min on NORMAL (zero new SQLITE_BUSY) through two merge gates and two concurrent builds; host load 15-30. Resolved for the dev hub. Production default stays FULL; revisit only if a deployed hub shows the same stalls.
