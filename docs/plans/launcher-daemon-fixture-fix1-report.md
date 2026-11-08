# Acknowledged restart and durable status retry fixture

Source commit: `d91b0e3cd2ac1932afb01dc30af25279ece35f18`.
This separate report is LAST for the R-L13 fixture correction. Only
`connectors/launcher/test/daemon.test.ts` changed; production and the shared
daemon harness are unchanged.

The combined run at `e694d761` remains recorded as **2117 pass / 1 fail / 2118**,
actual exit 1. The restart test observed three HTTP POST attempts where it
expected two. Twenty later focused passes did not clear that failure.

`launched()` waits for the fake hub to receive a running-status HTTP request.
The fake hub records attempts without idempotent storage. The launcher's outbox
marks its durable row sent only after the HTTP result. Restarting between those
events correctly retries the same row and bytes. The original test now uses a
bounded `until()` to wait until there are no pending send/job_status rows for
its exact job, including deferred rows via `pendingOutbox(MAX, MAX)`, before
its intentional acknowledged-running restart. Every original assertion and
status count is retained.

A separate deterministic regression stops the daemon synchronously when the
fake hub records the running request, before its response can acknowledge the
outbox. Read-only SQLite snapshots prove exactly two persisted rows, identical
IDs/keys/serialized payloads across restart, sequence still 2, and the second
row changing pending to sent. Raw HTTP attempts have sequences `[1, 2, 2]`,
exactly two distinct keys, and byte-identical second/third request bodies.
There is one launch and one backend session. The new regression also requires
eventual durable outbox settlement.

The regression was drafted before the original-case barrier. Its source SHA
was `a0aa9ebf019836d1604da4b9036bd502063c62a020e3454fce3d43ada4100616`;
the final tested/committed file SHA is
`3931ef820a5564ece5b060cf69109ca119e4a8e71236f80967fd9af832b6e859`.
The pre-barrier draft and ordering receipt are preserved in the evidence packet.
This is a fixture-boundary correction with a new passing retry control, not a
claim that production behavior was changed or that the new control failed first.

Sequential execution used the explicitly released CX slot, 05:14:23–55 UTC:

| Gate | Actual exit | Result |
| --- | --- | --- |
| External forced-boundary diagnostic with SQLite receipt | 0 | 1/1 pass |
| `node --disable-warning=ExperimentalWarning --test --test-concurrency=1 connectors/launcher/test/daemon.test.ts` | 0 | 19/19 pass |
| `node connectors/launcher/node_modules/typescript/lib/tsc.js --noEmit -p connectors/launcher/tsconfig.json` | 0 | Full configured strict TypeScript |

Raw logs, exact commands, actual exit receipts and before/after tracked-source
inventories are under `/work/package-cache/xertrov/vecnet-daemon-fixture-fix1/`.
The separate diagnostic source and its detailed SQLite receipt accompany them.
Every command's tracked-source inventory was unchanged, SHA-256
`d0cd1a0feaf33c9c6dc52c8887ebb4af601c7e3f15751cdf19edd63061203e78`.
HEAD during execution was `e694d761` with the exact named daemon-test patch and
disjoint in-progress harness changes; this was not a clean final-branch gate.
The diagnostic's persisted row IDs are 1/2, statusSeq 2 before/after, row 2
pending/attempts 0 before and sent/attempts 1 after. No production mutation,
real hub, provider, profile or service was used.

Main then STOPPED/drained and explicitly returned CX to the harness owner.
The later CoS R-L18 loan was returned unused; there was no duplicate execution.
The entire final combined Node suite must still rerun at the final immutable
freeze, and the scoped independent fixture review remains controller-owned.
