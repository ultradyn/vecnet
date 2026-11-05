# Captured benchmark observations — 2026-11-05

These excerpts were transcribed from the build-box wrapper's streamed output
for the constrained full run. They are evidence of observed background worker
contention, not a complete log or a count of all errors.

Command:

```sh
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 just bench -mode limited -duration 10s -min-sends 500 -workers 4 -idle-window 32s -idle-counts 0,100,1000,5000 -blob-repeats 3 -out bench/results/2026-11-05-limited.json
```

Selected worker output:

```text
2026/11/05 13:11:20 ERROR events: step err="store: begin: database is locked (5) (SQLITE_BUSY)"
2026/11/05 13:12:31 ERROR push: claim due err="store: begin: database is locked (5) (SQLITE_BUSY)"
2026/11/05 13:18:29 ERROR hub: tick expire deliveries err="store: begin: database is locked (5) (SQLITE_BUSY)"
```

The same run logged one `hub: touch reachability` / `sql: database is closed`
at 13:23:44 during delivery-test shutdown. The initial harness returned from
`Server.Serve` while the asynchronous `Server.Shutdown` was still draining;
the final harness waits for shutdown before closing SQLite. This happens
after the completed delivery sample and does not change its latency values.
Subsequent standalone runs validate the corrected child lifecycle.

The original constrained idle suite used request-written/active-handler
readiness for long-poll. The final harness also verifies registered event-log
waiters, ensuring authentication and reachability updates have completed.
The dedicated constrained idle rerun added this check and retained the
original full-run JSON without rewriting its measurements. The report now
uses the later per-client-verified reruns described below.

An intermediate strong-readiness probe is retained as
`results/2026-11-05-limited-idle-probe.json`. Its required 0/100/1,000 samples
and optional 5,000 SSE sample completed, but the 5,000-long-poll attempt failed
with `longpoll transport failed`. The client then had a 45-second response-header
timeout; admission queues plus the production 25-second hold could exceed it.
The final harness allows three minutes for headers and uses an explicit
two-minute registered-waiter readiness deadline. It records optional 5,000
probe failures as unavailable, without publishing partial-concurrency CPU/RSS.
It also starts a fresh child per protocol, so long-poll's zero-stream memory
baseline does not inherit SSE's 5,000-stream heap high water.

The intermediate `results/2026-11-05-limited-idle.json` artifact records the optional long-poll probe as
unavailable after 122.4 seconds: 4,997 active handlers and 3,505 registered
event-log waiters against the 5,000 target. That binary allowed two minutes
for the registered-waiter stage after the initial client-write stage. The
committed harness shares one two-minute setup deadline across both stages
and their control HTTP requests. This tightening does not change any completed
idle window; the unavailable probe has no published CPU/RSS estimate.

The initial unconstrained probe is retained as
`results/2026-11-05-unconstrained-probe.json` and
`results/2026-11-05-unconstrained-probe-run.txt`. Its 200 B/F=2 scenario hit:

```text
2026/11/05 13:49:54 ERROR internal error err="store: begin: database is locked (5) (SQLITE_BUSY)"
benchmark failed: POST /v1/messages: HTTP 500
```

The final runner counts HTTP 429/5xx sends, failure latencies and attempts
separately from fresh 202s, and continues collecting accepted samples. Failed
attempts remain in elapsed/CPU totals. It retains strict message-row and
delivery-ID checks and aborts on other unexpected statuses or transport
failures. No store timeout, durability policy or product code was changed.

A final source review found that `eventlog.Wait` unregisters cancelled waiters
through `context.AfterFunc`. During a long-poll renewal, a stale cancelled
waiter can briefly coexist with its replacement. A total waiter count of N
therefore does not by itself prove that N distinct clients have passed
admission. This was a possible interleaving found in source, not an observed
miscount in the earlier runs.

The committed driver requires each long-poll client to complete its own first
validated empty HTTP 200 response after the real 25-second hold before it can
announce readiness. It records `longpoll_clients_verified`; renewing one client
cannot increase that count twice. It then checks active handlers/registered
waiters, settles, and rechecks client liveness and active handlers as before.
SSE admission continues to require each client's flushed 200 event-stream
headers. The final report uses the separate `*-idle-verified.json` reruns for
both modes; all earlier idle measurements remain preserved and superseded.
Send, storage, delivery and blob measurements do not depend on this idle
readiness change.

In the final constrained rerun, every required sample and the 5,000-SSE
sample passed. The optional 5,000-long-poll probe completed every client's
first response but did not meet the simultaneous registered-waiter target
within its setup budget: 5,000 active handlers and 3,704 registered waiters.
Its cancellation produced expected context-cancelled request logs, retained
in `results/2026-11-05-limited-idle-verified-run.txt`, alongside production
worker `SQLITE_BUSY` messages during setup. No partial idle CPU/RSS sample is
published for that probe.

The final unconstrained rerun likewise passed all required samples and
5,000 SSE streams. Its optional 5,000-long-poll probe timed out with 5,000
active handlers and 3,603 registered waiters; its full log is retained in
`results/2026-11-05-unconstrained-idle-verified-run.txt`.
