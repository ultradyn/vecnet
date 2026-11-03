# 32: Dev hub logs SQLITE_BUSY from background workers

Found 2026-11-04 while restarting the dev hub (`.scratch/vecnet/build/devhub.log`). 295 `database is locked (5) (SQLITE_BUSY)` errors between 2026-11-01 and 2026-11-04: `events: step` 126, `push: claim due` 114, `hub: tick expire deliveries` 27, `hub: tick expire approvals` 22. All from background loops; no HTTP request has failed with SQLITE_BUSY in the log (the `internal error` lines are context-canceled client disconnects).

The store already uses `_txlock=immediate` and `busy_timeout(5000)` (internal/store/store.go:18-25), so each error means a writer held the lock for over 5 s, or another process had the file open.

## Investigate
- Which transaction holds the write lock that long (log tx duration over 1 s with its call site), and whether any work inside a Tx blocks on I/O (webhook/push POST, blob write, long-poll).
- Whether another process opens `.data/hub.db` (e.g. `vecnet-hub` admin subcommands or scripts reading it while the hub runs).
- Workers should back off and retry quietly on SQLITE_BUSY rather than log at ERROR each tick.

## Order
Low priority; investigate before a production deploy.
